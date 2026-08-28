package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	TaskArchiveFormatVersion       = 2
	TaskArchiveLegacyFormatVersion = 1
	TaskArchiveLLMRecordsPath      = "database/llm_records.ndjson"
)

func IsTaskArchiveFormatSupported(version int) bool {
	return version >= TaskArchiveLegacyFormatVersion && version <= TaskArchiveFormatVersion
}

const (
	ArchiveQueued = "archive_queued"
	Archiving     = "archiving"
	ArchiveFailed = "archive_failed"
	ArchiveReady  = "ready"
	RestoreQueued = "restore_queued"
	Restoring     = "restoring"
	RestoreFailed = "restore_failed"
	DeleteQueued  = "delete_queued"
	Deleting      = "deleting"
	DeleteFailed  = "delete_failed"
)

var (
	ErrTaskArchiveNotFound       = errors.New("task archive not found")
	ErrTaskArchiveIneligible     = errors.New("task must be paused or terminal before archiving")
	ErrTaskArchiveQueued         = errors.New("queued task must be paused before archiving")
	ErrTaskArchiveDependent      = errors.New("task is inherited by a live task")
	ErrTaskArchiveState          = errors.New("task archive state does not allow this operation")
	ErrTaskArchiveDeleteBlocked  = errors.New("task archive is required by another archive")
	ErrTaskArchiveFormatMismatch = errors.New("task archive format is not supported")
)

// TaskArchive is the compact PostgreSQL record retained while a task is cold.
// Sensitive profile configuration and API keys are intentionally absent.
type TaskArchive struct {
	ID                      int64           `json:"id"`
	TaskID                  int64           `json:"task_id"`
	State                   string          `json:"state"`
	Phase                   string          `json:"phase"`
	Progress                int             `json:"progress"`
	Error                   string          `json:"error,omitempty"`
	Warnings                json.RawMessage `json:"warnings"`
	FormatVersion           int             `json:"format_version"`
	ArchivePath             string          `json:"-"`
	SHA256                  string          `json:"sha256,omitempty"`
	OriginalSize            int64           `json:"original_size"`
	CompressedSize          int64           `json:"compressed_size"`
	TaskName                string          `json:"task_name"`
	TaskDescription         string          `json:"task_description"`
	TaskGoal                string          `json:"task_goal"`
	OriginalStatus          string          `json:"original_status"`
	CategoryIDSnapshot      *int64          `json:"category_id,omitempty"`
	CategoryNameSnapshot    string          `json:"category_name,omitempty"`
	SourceTaskIDs           []int64         `json:"source_task_ids"`
	RemainingTimeoutSeconds int64           `json:"remaining_timeout_seconds"`
	DataCounts              json.RawMessage `json:"data_counts"`
	AggregateStats          json.RawMessage `json:"aggregate_stats"`
	ArchivedAt              *time.Time      `json:"archived_at,omitempty"`
	RequestedAt             time.Time       `json:"requested_at"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

// TaskArchiveBlockers returns one live direct dependent for every source task
// that cannot currently be archived. Dependents already queued for archiving do
// not block their source because the FIFO worker will compact them first.
func (d *DB) TaskArchiveBlockers() (map[int64]int64, error) {
	rows, err := d.Query(`SELECT relation.source_task_id, MIN(child.id)
FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving')
GROUP BY relation.source_task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	blockers := map[int64]int64{}
	for rows.Next() {
		var sourceID, dependentID int64
		if err := rows.Scan(&sourceID, &dependentID); err != nil {
			return nil, err
		}
		blockers[sourceID] = dependentID
	}
	return blockers, rows.Err()
}

type TaskArchivePage struct {
	Items []TaskArchive `json:"items"`
	Total int           `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// TaskArchiveSnapshot is serialized into manifest.json inside the cold package.
// Small tables remain JSON arrays in Tables. Large v2 tables are streamed to
// package files listed in StreamedTables so their size is not bounded by memory.
type TaskArchiveSnapshot struct {
	FormatVersion     int                        `json:"format_version"`
	CreatedAt         time.Time                  `json:"created_at"`
	TaskID            int64                      `json:"task_id"`
	ExplorationID     int64                      `json:"exploration_id"`
	SourceTaskIDs     []int64                    `json:"source_task_ids"`
	Hosts             []string                   `json:"hosts"`
	ExclusiveHosts    []string                   `json:"exclusive_hosts"`
	ExclusiveAssetIDs []int64                    `json:"exclusive_asset_ids"`
	Tables            map[string]json.RawMessage `json:"tables"`
	StreamedTables    map[string]string          `json:"streamed_tables,omitempty"`
	DataCounts        map[string]int64           `json:"data_counts"`
	AggregateStats    map[string]any             `json:"aggregate_stats"`
}

func scanTaskArchive(sc interface{ Scan(...any) error }) (*TaskArchive, error) {
	var item TaskArchive
	var sources string
	err := sc.Scan(
		&item.ID, &item.TaskID, &item.State, &item.Phase, &item.Progress, &item.Error,
		&item.Warnings, &item.FormatVersion, &item.ArchivePath, &item.SHA256,
		&item.OriginalSize, &item.CompressedSize, &item.TaskName, &item.TaskDescription,
		&item.TaskGoal, &item.OriginalStatus, &item.CategoryIDSnapshot,
		&item.CategoryNameSnapshot, &sources, &item.RemainingTimeoutSeconds,
		&item.DataCounts, &item.AggregateStats, &item.ArchivedAt, &item.RequestedAt,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(sources), &item.SourceTaskIDs); err != nil {
		return nil, err
	}
	if len(item.Warnings) == 0 {
		item.Warnings = json.RawMessage("[]")
	}
	if len(item.DataCounts) == 0 {
		item.DataCounts = json.RawMessage("{}")
	}
	if len(item.AggregateStats) == 0 {
		item.AggregateStats = json.RawMessage("{}")
	}
	return &item, nil
}

const taskArchiveCols = `id, task_id, state, phase, progress, COALESCE(error,''), warnings,
format_version, COALESCE(archive_path,''), COALESCE(sha256,''), original_size,
compressed_size, COALESCE(task_name,''), COALESCE(task_description,''),
COALESCE(task_goal,''), COALESCE(original_status,''), category_id_snapshot,
COALESCE(category_name_snapshot,''), array_to_json(source_task_ids)::text,
remaining_timeout_seconds, data_counts, aggregate_stats, archived_at,
requested_at, created_at, updated_at`

func (d *DB) GetTaskArchive(id int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (d *DB) GetTaskArchiveByTask(taskID int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE task_id=$1`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return item, err
}

func (d *DB) ListTaskArchives(search, state string, page, size int) (TaskArchivePage, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	search = strings.TrimSpace(search)
	state = strings.TrimSpace(state)
	where := `WHERE ($1='' OR task_id::text ILIKE '%'||$1||'%' OR task_name ILIKE '%'||$1||'%' OR task_description ILIKE '%'||$1||'%')
AND ($2='' OR state=$2)`
	var out TaskArchivePage
	out.Page, out.Size = page, size
	if err := d.QueryRow(`SELECT count(*) FROM task_archives `+where, search, state).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := d.Query(`SELECT `+taskArchiveCols+` FROM task_archives `+where+`
ORDER BY COALESCE(archived_at, requested_at) DESC, id DESC LIMIT $3 OFFSET $4`, search, state, size, (page-1)*size)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanTaskArchive(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, *item)
	}
	return out, rows.Err()
}

// QueueTaskArchive validates lifecycle and direct inheritance while holding the
// task row. A failed archive can be explicitly retried through the same API.
func (d *DB) QueueTaskArchive(taskID int64) (*TaskArchive, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var name, description, goal, status, categoryName string
	var categoryID *int64
	var paused, queued bool
	var deadline *time.Time
	err = tx.QueryRow(`SELECT COALESCE(t.name,''), t.description, t.goal, t.status,
 t.category_id, COALESCE(c.name,''), t.paused, t.queued, t.deadline_at
FROM tasks t LEFT JOIN task_categories c ON c.id=t.category_id
WHERE t.id=$1 AND t.deleted_at IS NULL FOR UPDATE OF t`, taskID).Scan(
		&name, &description, &goal, &status, &categoryID, &categoryName, &paused, &queued, &deadline,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveNotFound
	}
	if err != nil {
		return nil, err
	}
	if queued {
		return nil, ErrTaskArchiveQueued
	}
	if !paused && !IsTerminal(status) {
		return nil, ErrTaskArchiveIneligible
	}
	var dependent int64
	err = tx.QueryRow(`SELECT child.id FROM task_relations relation
JOIN tasks child ON child.id=relation.task_id AND child.deleted_at IS NULL
LEFT JOIN task_archives pending ON pending.task_id=child.id
WHERE relation.source_task_id=$1
  AND (pending.id IS NULL OR pending.state NOT IN ('archive_queued','archiving'))
LIMIT 1`, taskID).Scan(&dependent)
	if err == nil {
		return nil, fmt.Errorf("%w: task %d", ErrTaskArchiveDependent, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var sources []int64
	rows, err := tx.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at, source_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if sources == nil {
		sources = []int64{}
	}
	remaining := int64(0)
	if paused && deadline != nil {
		remaining = int64(time.Until(*deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
	}
	_, err = tx.Exec(`INSERT INTO task_archives(
 task_id,state,phase,progress,error,warnings,format_version,task_name,
 task_description,task_goal,original_status,category_id_snapshot,
 category_name_snapshot,source_task_ids,remaining_timeout_seconds,requested_at)
VALUES ($1,$2,'queued',0,'','[]',$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
	ON CONFLICT (task_id) DO UPDATE SET
	 state=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.state ELSE task_archives.state END,
	 phase=CASE WHEN task_archives.state IN ('archive_failed') THEN 'queued' ELSE task_archives.phase END,
	 progress=CASE WHEN task_archives.state IN ('archive_failed') THEN 0 ELSE task_archives.progress END,
	 error=CASE WHEN task_archives.state IN ('archive_failed') THEN '' ELSE task_archives.error END,
	 format_version=CASE WHEN task_archives.state IN ('archive_failed') THEN EXCLUDED.format_version ELSE task_archives.format_version END,
	 requested_at=CASE WHEN task_archives.state IN ('archive_failed') THEN now() ELSE task_archives.requested_at END`,
		taskID, ArchiveQueued, TaskArchiveFormatVersion, name, description, goal, status,
		categoryID, categoryName, sources, remaining)
	if err != nil {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`SELECT `+taskArchiveCols+` FROM task_archives WHERE task_id=$1`, taskID))
	if err != nil {
		return nil, err
	}
	if item.State != ArchiveQueued && item.State != ArchiveFailed {
		return nil, fmt.Errorf("%w: current state %s", ErrTaskArchiveState, item.State)
	}
	return item, tx.Commit()
}

func (d *DB) QueueTaskArchiveRestore(id int64) (*TaskArchive, error) {
	item, err := scanTaskArchive(d.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 AND state IN ('ready','restore_failed') RETURNING `+taskArchiveCols, id, RestoreQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskArchiveState
	}
	return item, err
}

func (d *DB) QueueTaskArchiveDelete(id int64) (*TaskArchive, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var taskID int64
	if err := tx.QueryRow(`SELECT task_id FROM task_archives WHERE id=$1 AND state IN ('ready','delete_failed') FOR UPDATE`, id).Scan(&taskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveState
		}
		return nil, err
	}
	var dependent int64
	err = tx.QueryRow(`SELECT task_id FROM task_archives
WHERE id<>$1 AND $2=ANY(source_task_ids) AND state NOT IN ('delete_queued','deleting') LIMIT 1`, id, taskID).Scan(&dependent)
	if err == nil {
		return nil, fmt.Errorf("%w: task %d", ErrTaskArchiveDeleteBlocked, dependent)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	item, err := scanTaskArchive(tx.QueryRow(`UPDATE task_archives
SET state=$2, phase='queued', progress=0, error='', requested_at=now()
WHERE id=$1 RETURNING `+taskArchiveCols, id, DeleteQueued))
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

// RecoverTaskArchiveJobs keeps restore/delete resumable after an unclean shutdown.
// An interrupted archive requires an explicit retry: automatic startup retries can
// otherwise form a crash loop when the prior process was killed by resource limits.
func (d *DB) RecoverTaskArchiveJobs() error {
	_, err := d.Exec(`UPDATE task_archives SET
	 state=CASE state WHEN 'archiving' THEN 'archive_failed'
	                  WHEN 'restoring' THEN 'restore_queued'
	                  WHEN 'deleting' THEN 'delete_queued' ELSE state END,
	 phase='interrupted',
	 error=CASE WHEN state='archiving' THEN '上次归档进程异常退出，请手动重试' ELSE '' END
	WHERE state IN ('archiving','restoring','deleting')`)
	return err
}

// ClaimTaskArchiveJob claims one persistent FIFO item for the single archive
// worker. It returns nil when the queue is empty.
func (d *DB) ClaimTaskArchiveJob(ctx context.Context) (*TaskArchive, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var id int64
	var queuedState string
	err = tx.QueryRowContext(ctx, `SELECT id,state FROM task_archives
WHERE state IN ('archive_queued','restore_queued','delete_queued')
ORDER BY requested_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &queuedState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	active := map[string]string{ArchiveQueued: Archiving, RestoreQueued: Restoring, DeleteQueued: Deleting}[queuedState]
	item, err := scanTaskArchive(tx.QueryRowContext(ctx, `UPDATE task_archives
SET state=$2,phase='starting',progress=1,error='' WHERE id=$1 RETURNING `+taskArchiveCols, id, active))
	if err != nil {
		return nil, err
	}
	return item, tx.Commit()
}

func (d *DB) UpdateTaskArchiveProgress(id int64, phase string, progress int) error {
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	_, err := d.Exec(`UPDATE task_archives SET phase=$2,progress=$3 WHERE id=$1`, id, phase, progress)
	return err
}

func (d *DB) AppendTaskArchiveWarning(id int64, warning string) error {
	if strings.TrimSpace(warning) == "" {
		return nil
	}
	_, err := d.Exec(`UPDATE task_archives SET warnings=warnings || jsonb_build_array($2::text) WHERE id=$1`, id, warning)
	return err
}

func (d *DB) IsTaskArchiveRestored(id int64) (bool, error) {
	var restored bool
	err := d.QueryRow(`SELECT task.deleted_at IS NULL AND task.archived_at IS NULL
FROM task_archives archive JOIN tasks task ON task.id=archive.task_id WHERE archive.id=$1`, id).Scan(&restored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrTaskArchiveNotFound
	}
	return restored, err
}

func (d *DB) FailTaskArchiveJob(id int64, activeState string, cause error) error {
	failed := map[string]string{Archiving: ArchiveFailed, Restoring: RestoreFailed, Deleting: DeleteFailed}[activeState]
	if failed == "" {
		return fmt.Errorf("unknown active archive state %q", activeState)
	}
	message := "unknown archive failure"
	if cause != nil {
		message = cause.Error()
	}
	_, err := d.Exec(`UPDATE task_archives SET state=$2,phase='failed',error=$3 WHERE id=$1`, id, failed, message)
	return err
}

func queryArchiveRows(q interface {
<<<<<<< Updated upstream
	QueryRow(query string, args ...any) *sql.Row
}, inner string, args ...any) (json.RawMessage, error) {
	var raw []byte
	err := q.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(row_data)), '[]'::jsonb) FROM (`+inner+`) row_data`, args...).Scan(&raw)
	return json.RawMessage(raw), err
=======
	Query(query string, args ...any) (*sql.Rows, error)
}, inner string, args ...any) (json.RawMessage, int64, error) {
	// Do not aggregate the result in PostgreSQL. A jsonb array has a hard limit
	// of 256 MiB for its elements, which large LLM request/response histories can
	// exceed even though every individual record is valid. Reading row JSON in
	// order also avoids building a second copy of the full table in PostgreSQL.
	rows, err := q.Query(`SELECT row_to_json(row_data)::text FROM (`+inner+`) row_data`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	return encodeArchiveRows(rows)
}

func encodeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) (json.RawMessage, int64, error) {
	var output bytes.Buffer
	output.WriteByte('[')
	var count int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		if count > 0 {
			output.WriteByte(',')
		}
		output.Write(raw)
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	output.WriteByte(']')
	return json.RawMessage(output.Bytes()), count, nil
>>>>>>> Stashed changes
}

func writeArchiveRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}, writer io.Writer) (int64, error) {
	var count int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		if written, err := writer.Write(raw); err != nil {
			return 0, err
		} else if written != len(raw) {
			return 0, io.ErrShortWrite
		}
		if written, err := io.WriteString(writer, "\n"); err != nil {
			return 0, err
		} else if written != 1 {
			return 0, io.ErrShortWrite
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func streamArchiveRows(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, writer io.Writer, inner string, args ...any) (int64, error) {
	rows, err := q.Query(`SELECT row_to_json(row_data)::text FROM (`+inner+`) row_data`, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	return writeArchiveRows(rows, writer)
}

func rawRowCount(raw json.RawMessage) int64 {
	var rows []json.RawMessage
	if json.Unmarshal(raw, &rows) != nil {
		return 0
	}
	return int64(len(rows))
}

func archiveAssetIDsQuery() string {
	return `SELECT id FROM assets WHERE $1=ANY(task_ids)
UNION SELECT link.asset_id FROM task_asset_links link WHERE link.task_id=$1
UNION SELECT anchor.asset_id FROM exploration_anchors anchor
      JOIN exploration_nodes node ON node.id=anchor.node_id WHERE node.exploration_id=$2
UNION SELECT value::bigint FROM findings finding
      CROSS JOIN LATERAL jsonb_array_elements_text(
        CASE WHEN jsonb_typeof(finding.asset_ids)='array' THEN finding.asset_ids ELSE '[]'::jsonb END
      ) value WHERE finding.task_id=$1 AND value ~ '^[0-9]+$'`
}

// SnapshotTaskArchive reads one repeatable PostgreSQL snapshot. Task-owned Agent
// writes are already quiescent at the server barrier; repeatable-read also keeps
// the asset and accounting views mutually consistent during serialization.
func (d *DB) SnapshotTaskArchive(taskID int64) (*TaskArchiveSnapshot, error) {
	return d.snapshotTaskArchive(taskID, nil)
}

// SnapshotTaskArchiveWithLLMRecords streams the heavyweight record history to
// llmRecords while all other task-owned data is read from the same repeatable
// PostgreSQL snapshot.
func (d *DB) SnapshotTaskArchiveWithLLMRecords(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	if llmRecords == nil {
		return nil, errors.New("nil LLM record archive writer")
	}
	return d.snapshotTaskArchive(taskID, llmRecords)
}

func (d *DB) snapshotTaskArchive(taskID int64, llmRecords io.Writer) (*TaskArchiveSnapshot, error) {
	tx, err := d.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return nil, err
	}
	var expID int64
	if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1 AND deleted_at IS NULL`, taskID).Scan(&expID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskArchiveNotFound
		}
		return nil, err
	}
	tables := make(map[string]json.RawMessage)
	queries := []struct {
		name  string
		query string
		args  []any
	}{
		{"tasks", `SELECT * FROM tasks WHERE id=$1`, []any{taskID}},
		{"explorations", `SELECT * FROM explorations WHERE id=$1`, []any{expID}},
		{"exploration_nodes", `SELECT * FROM exploration_nodes WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"exploration_edges", `SELECT * FROM exploration_edges WHERE exploration_id=$1 ORDER BY src_id,dst_id`, []any{expID}},
		{"exploration_anchors", `SELECT anchor.* FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id WHERE node.exploration_id=$1 ORDER BY node_id,asset_id`, []any{expID}},
		{"task_constraints", `SELECT * FROM task_constraints WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"activity", `SELECT * FROM activity WHERE exploration_id=$1 ORDER BY id`, []any{expID}},
		{"task_relations", `SELECT * FROM task_relations WHERE task_id=$1 ORDER BY created_at,source_task_id`, []any{taskID}},
		{"task_asset_links", `SELECT * FROM task_asset_links WHERE task_id=$1 ORDER BY asset_id`, []any{taskID}},
		{"task_llm_profiles", `SELECT * FROM task_llm_profiles WHERE task_id=$1 ORDER BY position`, []any{taskID}},
		{"task_scope", `SELECT * FROM task_scope WHERE task_id=$1 ORDER BY id`, []any{taskID}},
		{"findings", `SELECT * FROM findings WHERE task_id=$1 ORDER BY id`, []any{taskID}},
		{"llm_records", `SELECT * FROM llm_records WHERE COALESCE(task_id,'')=$1 ORDER BY id`, []any{strconv.FormatInt(taskID, 10)}},
		{"llm_usage", `SELECT * FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2 ORDER BY id`, []any{strconv.FormatInt(taskID, 10), expID}},
		{"skill_usage", `SELECT * FROM skill_usage WHERE task_id=$1 OR exploration_id=$2 ORDER BY id`, []any{taskID, expID}},
		{"tool_usage", `SELECT * FROM tool_usage WHERE task_id=$1 OR exploration_id=$2 ORDER BY id`, []any{taskID, expID}},
		{"intercept_pending", `SELECT * FROM intercept_pending WHERE COALESCE(task_id,'')=$1 ORDER BY id`, []any{strconv.FormatInt(taskID, 10)}},
		{"assets", `SELECT asset.* FROM assets asset WHERE asset.id IN (` + archiveAssetIDsQuery() + `) ORDER BY asset.id`, []any{taskID, expID}},
	}
	counts := make(map[string]int64, len(queries))
	streamedTables := map[string]string{}
	for _, query := range queries {
<<<<<<< Updated upstream
		raw, err := queryArchiveRows(tx, query.query, query.args...)
=======
		if query.name == "llm_records" && llmRecords != nil {
			count, err := streamArchiveRows(tx, llmRecords, query.query, query.args...)
			if err != nil {
				return nil, fmt.Errorf("snapshot %s: %w", query.name, err)
			}
			tables[query.name] = json.RawMessage("[]")
			counts[query.name] = count
			streamedTables[query.name] = TaskArchiveLLMRecordsPath
			continue
		}
		raw, count, err := queryArchiveRows(tx, query.query, query.args...)
>>>>>>> Stashed changes
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", query.name, err)
		}
		tables[query.name] = raw
		counts[query.name] = rawRowCount(raw)
	}

	assetIDs, exclusiveAssetIDs, hosts, exclusiveHosts, err := archiveAssetMetadata(tx, taskID, expID, tables["assets"])
	if err != nil {
		return nil, err
	}
	_ = assetIDs // retained in the assets table payload; only exclusive ids need a side channel.
	var sources []int64
	rows, err := tx.Query(`SELECT source_task_id FROM task_relations WHERE task_id=$1 ORDER BY created_at,source_task_id`, taskID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	stats, err := taskArchiveAggregates(tx, taskID, expID)
	if err != nil {
		return nil, err
	}
	snapshot := &TaskArchiveSnapshot{
		FormatVersion: TaskArchiveFormatVersion, CreatedAt: time.Now().UTC(), TaskID: taskID,
		ExplorationID: expID, SourceTaskIDs: sources, Hosts: hosts, ExclusiveHosts: exclusiveHosts,
		ExclusiveAssetIDs: exclusiveAssetIDs, Tables: tables, StreamedTables: streamedTables,
		DataCounts: counts, AggregateStats: stats,
	}
	return snapshot, tx.Commit()
}

func archiveAssetMetadata(tx *sql.Tx, taskID, expID int64, assetRows json.RawMessage) ([]int64, []int64, []string, []string, error) {
	var rows []map[string]any
	if err := json.Unmarshal(assetRows, &rows); err != nil {
		return nil, nil, nil, nil, err
	}
	allHosts := map[string]struct{}{}
	assetIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		if id, ok := jsonInt64(row["id"]); ok {
			assetIDs = append(assetIDs, id)
		}
		for _, key := range []string{"domain", "ip"} {
			if value, _ := row[key].(string); strings.TrimSpace(value) != "" {
				allHosts[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
			}
		}
		if rawURL, _ := row["url"].(string); rawURL != "" {
			if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
				allHosts[strings.ToLower(parsed.Hostname())] = struct{}{}
			}
		}
	}
	exclusiveHosts, err := hostsForTaskDeletion(tx, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	rowsID, err := tx.Query(`WITH candidate AS (`+archiveAssetIDsQuery()+`)
SELECT asset.id FROM assets asset JOIN candidate ON candidate.id=asset.id
WHERE asset.company_id IS NULL
AND NOT EXISTS (SELECT 1 FROM tasks task WHERE task.id<>$1 AND task.deleted_at IS NULL AND task.id=ANY(asset.task_ids))
AND NOT EXISTS (
 SELECT 1 FROM exploration_anchors anchor JOIN exploration_nodes node ON node.id=anchor.node_id
 JOIN tasks task ON task.exploration_id=node.exploration_id
 WHERE anchor.asset_id=asset.id AND task.id<>$1 AND task.deleted_at IS NULL
) ORDER BY asset.id`, taskID, expID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var exclusiveIDs []int64
	for rowsID.Next() {
		var id int64
		if err := rowsID.Scan(&id); err != nil {
			rowsID.Close()
			return nil, nil, nil, nil, err
		}
		exclusiveIDs = append(exclusiveIDs, id)
	}
	if err := rowsID.Close(); err != nil {
		return nil, nil, nil, nil, err
	}
	hosts := make([]string, 0, len(allHosts))
	for host := range allHosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return assetIDs, exclusiveIDs, hosts, exclusiveHosts, nil
}

func taskArchiveAggregates(tx *sql.Tx, taskID, expID int64) (map[string]any, error) {
	stats := map[string]any{}
	var calls, input, output, cacheRead, cacheWrite int64
	if err := tx.QueryRow(`SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),
COALESCE(sum(cache_read),0),COALESCE(sum(cache_write),0)
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2`, strconv.FormatInt(taskID, 10), expID).
		Scan(&calls, &input, &output, &cacheRead, &cacheWrite); err != nil {
		return nil, err
	}
	stats["tokens"] = map[string]int64{"calls": calls, "input_tokens": input, "output_tokens": output, "cache_read_tokens": cacheRead, "cache_write_tokens": cacheWrite}
	for _, item := range []struct {
		name  string
		query string
		args  []any
	}{
		{"token_profiles", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT COALESCE(profile_name,'') profile_name,count(*) calls,1 tasks,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens,COALESCE(sum(cache_write),0) cache_write_tokens
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2
GROUP BY profile_name ORDER BY sum(input_tokens)+sum(output_tokens) DESC) x`, []any{strconv.FormatInt(taskID, 10), expID}},
		{"token_daily", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT COALESCE(profile_name,'') profile_name,to_char(ts AT TIME ZONE 'UTC','YYYY-MM-DD') date,
       COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
       COALESCE(sum(cache_read),0) cache_read_tokens
FROM llm_usage WHERE COALESCE(task_id,'')=$1 OR exploration_id=$2
GROUP BY profile_name,date ORDER BY date) x`, []any{strconv.FormatInt(taskID, 10), expID}},
		{"skills", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT skill name,count(*) n FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND found GROUP BY skill) x`, []any{taskID, expID}},
		{"skill_stats", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT skill,count(*) calls,1 tasks,
       COALESCE(array_agg(DISTINCT agent_key) FILTER (WHERE agent_key IS NOT NULL),ARRAY[]::text[]) agents,
       max(ts) last_used
FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND found GROUP BY skill) x`, []any{taskID, expID}},
		{"missing_skill_stats", `SELECT COALESCE(jsonb_agg(to_jsonb(x)),'[]'::jsonb) FROM (
SELECT skill,count(*) calls,0 tasks,
       COALESCE(array_agg(DISTINCT agent_key) FILTER (WHERE agent_key IS NOT NULL),ARRAY[]::text[]) agents,
       max(ts) last_used
FROM skill_usage WHERE (task_id=$1 OR exploration_id=$2) AND NOT found GROUP BY skill) x`, []any{taskID, expID}},
		{"tools", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT tool_key name,count(*) n FROM tool_usage WHERE task_id=$1 OR exploration_id=$2 GROUP BY tool_key) x`, []any{taskID, expID}},
		{"findings", `SELECT COALESCE(jsonb_object_agg(name,n),'{}'::jsonb) FROM (SELECT COALESCE(NULLIF(severity,''),'unknown') name,count(*) n FROM findings WHERE task_id=$1 GROUP BY severity) x`, []any{taskID}},
		{"finding_stats", `SELECT jsonb_build_object(
 'total',count(*),'pending',count(*) FILTER (WHERE status='pending'),
 'critical',count(*) FILTER (WHERE severity='critical'),'high',count(*) FILTER (WHERE severity='high'),
 'medium',count(*) FILTER (WHERE severity='medium'),'low',count(*) FILTER (WHERE severity='low'),
 'vulnclasses',COALESCE(jsonb_agg(DISTINCT vulnclass) FILTER (WHERE vulnclass<>''),'[]'::jsonb))
FROM findings WHERE task_id=$1`, []any{taskID}},
	} {
		var raw []byte
		if err := tx.QueryRow(item.query, item.args...).Scan(&raw); err != nil {
			return nil, err
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		stats[item.name] = value
	}
	return stats, nil
}

func jsonInt64(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), value == float64(int64(value))
	case json.Number:
		id, err := value.Int64()
		return id, err == nil
	case string:
		id, err := strconv.ParseInt(value, 10, 64)
		return id, err == nil
	default:
		return 0, false
	}
}
