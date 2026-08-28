package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrIntentStateConflict marks a failed compare-and-set transition. Callers use
// it to distinguish an expected control race (HTTP 409) from a storage failure.
var ErrIntentStateConflict = errors.New("intent state changed concurrently")

// utf8Clean makes a string safe for a PostgreSQL text column. It (1) replaces
// invalid UTF-8 byte sequences with U+FFFD and (2) strips NUL (0x00) bytes.
// Tool output (nmap/curl/… stdout) can carry raw/truncated bytes that PostgreSQL's
// UTF8 encoding rejects ("invalid byte sequence for encoding UTF8"); without this
// the INSERT fails and the activity record is silently lost. NUL is *valid* UTF-8
// (U+0000) so ToValidUTF8 leaves it in place, yet PostgreSQL text still rejects it
// (SQLSTATE 22021) — so it must be removed separately. JSONB payloads are fine
// (json.Marshal already sanitizes), so only the plain text columns need it.
func utf8Clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	return strings.ToValidUTF8(s, "�")
}

// Node is a typed reasoning node (= old task_nodes). kind ∈ goal|intent|finding|hint.
type Node struct {
	ID            int64           `json:"id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	Priority      int             `json:"priority"`
	State         string          `json:"state"`
	Origin        string          `json:"origin,omitempty"`
	Owner         string          `json:"owner,omitempty"`
	BlockedReason string          `json:"blocked_reason,omitempty"`
	Anchors       []int64         `json:"anchors,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SourceTaskID  int64           `json:"source_task_id,omitempty"`
	Inherited     bool            `json:"inherited,omitempty"`
}

// Activity is one worker execution step (= old task_activity).
type Activity struct {
	ID        int64           `json:"id"`
	NodeID    *int64          `json:"node_id,omitempty"`
	Worker    string          `json:"worker,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Tool      string          `json:"tool,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	IsError   bool            `json:"is_error"`
	Summary   string          `json:"summary,omitempty"`
	Detail    string          `json:"-"` // full blob; lazy-loaded, omitted from list payloads
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	// Token usage — set only on the terminal kind='result' record; nil elsewhere.
	InputTokens      *int  `json:"input_tokens,omitempty"`
	OutputTokens     *int  `json:"output_tokens,omitempty"`
	CacheReadTokens  *int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int  `json:"cache_write_tokens,omitempty"`
	SourceTaskID     int64 `json:"source_task_id,omitempty"`
	Inherited        bool  `json:"inherited,omitempty"`
}

// TokenUsage is a per-worker token aggregate (TokenStatsByWorker).
type TokenUsage struct {
	Worker           string `json:"worker"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// SessionTokenUsage is the authoritative completed-run total for one task UI
// session. Worker sessions are keyed by intent rather than the reusable work#N
// executor name, so retries and reassignment remain attached to the same row.
type SessionTokenUsage struct {
	Session          string `json:"session"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// DailyTokenBucket is one day's global token aggregate across all tasks.
type DailyTokenBucket struct {
	Day             string `json:"day"` // "YYYY-MM-DD"
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens int    `json:"cache_read_tokens"`
}

// TokenDailyAll aggregates token consumption by calendar day (UTC) for the
// past `days` days across all explorations. Only kind='result' rows carry token
// counts (the terminal per-run summary), so there is no double-counting.
func (d *DB) TokenDailyAll(days int) ([]DailyTokenBucket, error) {
	if days <= 0 {
		days = 30
	}
	rows, err := d.Query(`
		SELECT TO_CHAR(DATE_TRUNC('day', created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS day,
		       COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0),
		       COALESCE(SUM(cache_read_tokens), 0)
		FROM activity
		WHERE kind = 'result'
		  AND created_at >= NOW() - ($1 * INTERVAL '1 day')
		GROUP BY day
		ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyTokenBucket
	for rows.Next() {
		var b DailyTokenBucket
		if err := rows.Scan(&b.Day, &b.InputTokens, &b.OutputTokens, &b.CacheReadTokens); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ExplorationStore is a handle onto one exploration's reasoning graph + activity.
type ExplorationStore struct {
	db    *DB
	expID int64
}

// CreateExploration creates a new exploration root and returns its id.
func (d *DB) CreateExploration(description, goal string) (int64, error) {
	var id int64
	err := d.QueryRow(`INSERT INTO explorations(description, goal) VALUES ($1, $2) RETURNING id`, description, goal).Scan(&id)
	return id, err
}

// Exploration returns a handle bound to an exploration id.
func (d *DB) Exploration(id int64) *ExplorationStore { return &ExplorationStore{db: d, expID: id} }

func (s *ExplorationStore) ID() int64 { return s.expID }

// Root returns the exploration's description and goal.
func (s *ExplorationStore) Root() (description, goal string, err error) {
	var d sql.NullString
	err = s.db.QueryRow(`SELECT description, goal FROM explorations WHERE id=$1`, s.expID).Scan(&d, &goal)
	return d.String, goal, err
}

// AddNode writes a typed reasoning node with optional anchors to asset ids.
func (s *ExplorationStore) AddNode(kind string, payload map[string]any, priority int, state, origin string, anchors []int64) (int64, error) {
	raw, _ := json.Marshal(payload)
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		s.expID, kind, string(raw), priority, state, origin).Scan(&id); err != nil {
		return 0, err
	}
	for _, a := range anchors {
		if _, err := tx.Exec(`INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// AddIntent is a convenience: an open intent.
func (s *ExplorationStore) AddIntent(payload map[string]any, priority int, anchors []int64, origin string) (int64, error) {
	if origin == "" {
		origin = "planner"
	}
	return s.AddNode("intent", payload, priority, "open", origin, anchors)
}

// AddGoal writes a goal node (state open).
func (s *ExplorationStore) AddGoal(payload map[string]any, origin string) (int64, error) {
	return s.AddNode("goal", payload, 0, "open", origin, nil)
}

// OriginFactID returns this exploration's root fact id — the KindFact node with
// state='origin' seeded at task creation (the task root). Every intent traces
// back to it. Returns 0 (no error) when none exists (legacy explorations created
// under the old 'begin' root, or none yet).
func (s *ExplorationStore) OriginFactID() (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='fact' AND state='origin' ORDER BY id LIMIT 1`, s.expID).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// Anchor records a node→asset reference edge (many-to-many): it captures which
// global assets an exploration node touched, as lineage/provenance. The asset
// graph itself is global and shared — anchoring no longer gates visibility (any
// task may read any asset via search); it only preserves the reasoning trail.
// Idempotent.
func (s *ExplorationStore) Anchor(nodeID, assetID int64) error {
	if nodeID <= 0 || assetID <= 0 {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, nodeID, assetID)
	return err
}

// Link adds a typed exploration edge (idempotent).
func (s *ExplorationStore) Link(from int64, rel string, to int64) error {
	_, err := s.db.Exec(`
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id) VALUES ($1,$2,$3,$4)
ON CONFLICT (exploration_id, src_id, rel, dst_id) DO NOTHING`, s.expID, from, rel, to)
	return err
}

// SetNodeState updates any node's state (never deletes).
func (s *ExplorationStore) SetNodeState(id int64, state string) error {
	_, err := s.db.Exec(`UPDATE exploration_nodes SET state=$1, blocked_reason=NULL WHERE id=$2 AND exploration_id=$3`, state, id, s.expID)
	return err
}

// UpdateGoalPayload rewrites a goal node's payload text (and optional vulnclass);
// scoped to kind='goal' so it can never mutate an intent/fact by id. Returns an
// error if no such goal exists in this exploration.
func (s *ExplorationStore) UpdateGoalPayload(id int64, text, vulnclass string) error {
	payload := map[string]any{"text": text}
	if vulnclass != "" {
		payload["vulnclass"] = vulnclass
	}
	raw, _ := json.Marshal(payload)
	res, err := s.db.Exec(`UPDATE exploration_nodes SET payload=$1 WHERE id=$2 AND exploration_id=$3 AND kind='goal'`, string(raw), id, s.expID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("目标不存在")
	}
	return nil
}

// DeleteGoal hard-deletes a goal node; scoped to kind='goal' so it can never drop
// an intent/fact by id. The edges (spawns) and anchors reference it with ON DELETE
// CASCADE, so they go with it; activity.node_id is ON DELETE SET NULL. Returns an
// error if no such goal exists in this exploration.
func (s *ExplorationStore) DeleteGoal(id int64) error {
	res, err := s.db.Exec(`DELETE FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='goal'`, id, s.expID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("目标不存在")
	}
	return nil
}

// SetIntentState updates an intent node's state.
// ResetRunningIntents returns any intent left in 'running' back to 'open' so it
// is re-claimed. Called on startup: a 'running' intent with no live worker (a
// backend restart or crashed worker goroutine left it stuck) would otherwise spin
// forever in the UI. Workers resume from their transcript, so reopening is safe.
func (s *ExplorationStore) ResetRunningIntents() (int64, error) {
	res, err := s.db.Exec(`UPDATE exploration_nodes SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE exploration_id=$1 AND kind='intent' AND state='running'`, s.expID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ReopenIntent flips ONE not-successfully-finished intent (blocked/exhausted/stopped)
// back to 'open' so a worker re-claims it (graph writes it already produced stay).
// The worker resumes from its prior LLM transcript instead of restarting from scratch.
// done/open/running are left untouched. Returns whether a row changed. Used by "重跑".
func (s *ExplorationStore) ReopenIntent(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE exploration_nodes
SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE id=$1 AND exploration_id=$2 AND kind='intent'
  AND state IN ('blocked','exhausted','stopped')`, id, s.expID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReopenBlockedIntents flips EVERY 'blocked' intent in this exploration back to 'open'
// (batch rerun after e.g. an LLM/network outage that blocked many at once). Returns the
// number reopened. Workers resume from their transcripts; kept graph writes remain.
func (s *ExplorationStore) ReopenBlockedIntents() (int64, error) {
	res, err := s.db.Exec(`UPDATE exploration_nodes SET state='open', completed_at=NULL, blocked_reason=NULL
WHERE exploration_id=$1 AND kind='intent' AND state='blocked'`, s.expID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *ExplorationStore) SetIntentState(id int64, state string) error {
	// terminal states stamp completed_at; reopening (back to open/running) clears it.
	terminal := state == "done" || state == "blocked" || state == "exhausted" || state == "stopped"
	_, err := s.db.Exec(`UPDATE exploration_nodes
SET state=$1, blocked_reason=NULL, completed_at = CASE WHEN $4 THEN now() ELSE NULL END
WHERE id=$2 AND exploration_id=$3 AND kind='intent'`, state, id, s.expID, terminal)
	return err
}

// CompareAndSetIntentState transitions one local intent only when its current
// state still matches expected. It is the state boundary used by pause/resume and
// worker settlement, so a stale API read cannot overwrite a concurrent finish.
func (s *ExplorationStore) CompareAndSetIntentState(id int64, expected, state string) (bool, error) {
	terminal := state == "done" || state == "blocked" || state == "exhausted" || state == "stopped"
	res, err := s.db.Exec(`UPDATE exploration_nodes
SET state=$1, blocked_reason=NULL, completed_at = CASE WHEN $5 THEN now() ELSE NULL END
WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state=$4`, state, id, s.expID, expected, terminal)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// IntentCleanup summarizes the blackboard records removed when a user cancels
// one worker intent. Global assets and traffic are deliberately not part of this
// operation; exploration anchors disappear only because their owning nodes do.
type IntentCleanup struct {
	Intents    int64 `json:"intents"`
	Facts      int64 `json:"facts"`
	Findings   int64 `json:"findings"`
	Activities int64 `json:"activities"`
}

// StopIntentWithReason marks a running/paused intent as 'stopped' WITHOUT deleting
// it or any of its yielded facts/findings/activities, and records the user's reason
// as a fact node hanging off that intent (intent --yields--> fact). Returns the new
// fact node id. The caller must first stop a running worker to prevent late writes.
// Used by the "delete work" action, which no longer destroys data — it stops the
// intent and attaches why, so the planner can account for it.
func (s *ExplorationStore) StopIntentWithReason(id int64, reason, origin string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var state string
	var rawPayload []byte
	if err := tx.QueryRow(`SELECT state, payload FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, id, s.expID).Scan(&state, &rawPayload); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("intent not found")
		}
		return 0, err
	}
	if state != "running" && state != "paused" {
		return 0, fmt.Errorf("%w: intent state %s cannot be cancelled", ErrIntentStateConflict, state)
	}
	// Merge the delete marker into the intent's own payload so it travels with the
	// intent everywhere (session list badge, detail banner) without a schema change.
	ip := map[string]any{}
	_ = json.Unmarshal(rawPayload, &ip)
	ip["cancelled_by_user"] = true
	ip["cancel_reason"] = reason
	newPayload, _ := json.Marshal(ip)
	if _, err := tx.Exec(`UPDATE exploration_nodes SET state='stopped', payload=$3, blocked_reason=NULL
		WHERE id=$1 AND exploration_id=$2`, id, s.expID, string(newPayload)); err != nil {
		return 0, err
	}

	if origin == "" {
		origin = "user"
	}
	raw, _ := json.Marshal(map[string]any{
		"summary":     "用户删除了该意图",
		"detail":      "删除原因：" + reason,
		"confidence":  "observed",
		"user_cancel": true,
	})
	var factID int64
	if err := tx.QueryRow(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, 'fact', $2, 5, 'confirmed', $3) RETURNING id`,
		s.expID, string(raw), origin).Scan(&factID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
INSERT INTO exploration_edges(exploration_id, src_id, rel, dst_id) VALUES ($1,$2,$3,$4)
ON CONFLICT DO NOTHING`, s.expID, id, RelYields, factID); err != nil {
		return 0, err
	}
	return factID, tx.Commit()
}

// CancelIntent removes one local intent and the fact/finding nodes it directly
// yielded. The standalone finding row must be deleted before its node (whose FK
// otherwise uses ON DELETE SET NULL), so the entire cleanup is kept in one DB
// transaction. The caller must first stop a running worker to prevent late writes.
func (s *ExplorationStore) CancelIntent(id int64) (IntentCleanup, error) {
	var out IntentCleanup
	tx, err := s.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	var state string
	if err := tx.QueryRow(`SELECT state FROM exploration_nodes
		WHERE id=$1 AND exploration_id=$2 AND kind='intent' FOR UPDATE`, id, s.expID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return out, fmt.Errorf("intent not found")
		}
		return out, err
	}
	if state != "running" && state != "paused" {
		return out, fmt.Errorf("%w: intent state %s cannot be cancelled", ErrIntentStateConflict, state)
	}

	if err := tx.QueryRow(`SELECT
		COUNT(*) FILTER (WHERE n.kind='fact'),
		COUNT(*) FILTER (WHERE n.kind='finding')
	FROM exploration_edges e
	JOIN exploration_nodes n ON n.id=e.dst_id AND n.exploration_id=e.exploration_id
	WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel=$3
	  AND n.kind IN ('fact','finding')
	  AND NOT EXISTS (
	      SELECT 1 FROM exploration_edges other
	      WHERE other.exploration_id=e.exploration_id AND other.dst_id=e.dst_id
	        AND other.src_id<>$2
	  )`, s.expID, id, RelYields).Scan(&out.Facts, &out.Findings); err != nil {
		return out, err
	}

	if _, err := tx.Exec(`DELETE FROM findings f USING exploration_edges e, exploration_nodes n
		WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel=$3
		  AND n.id=e.dst_id AND n.exploration_id=e.exploration_id
		  AND n.kind='finding' AND f.node_id=n.id
		  AND NOT EXISTS (
		      SELECT 1 FROM exploration_edges other
		      WHERE other.exploration_id=e.exploration_id AND other.dst_id=e.dst_id
		        AND other.src_id<>$2
		  )`, s.expID, id, RelYields); err != nil {
		return out, err
	}
	// Activity rows are part of the blackboard cleanup, but their token usage is
	// irreversible metering data. Fold completed runs plus the latest unfinished
	// usage frame into detached daily result rows before deleting the conversation.
	// The rows are excluded from per-intent sessions while whole-task totals and
	// the original UTC reporting dates remain accurate.
	tokenBuckets, err := intentTokenRollup(tx, s.expID, id)
	if err != nil {
		return out, err
	}
	res, err := tx.Exec(`DELETE FROM activity WHERE exploration_id=$1 AND node_id=$2`, s.expID, id)
	if err != nil {
		return out, err
	}
	out.Activities, _ = res.RowsAffected()
	for _, bucket := range tokenBuckets {
		metadata, _ := json.Marshal(map[string]any{
			"cancelled_intent_id": id,
			"token_day":           bucket.Day.Format(time.DateOnly),
			"token_rollup":        true,
		})
		if _, err := tx.Exec(`INSERT INTO activity(
			exploration_id, worker, kind, summary, metadata,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at)
			VALUES ($1,'token-ledger','result',$2,$3,$4,$5,$6,$7,$8)`,
			s.expID, fmt.Sprintf("已取消意图 #%d 的 Token 计量", id), metadata,
			bucket.Usage.InputTokens, bucket.Usage.OutputTokens,
			bucket.Usage.CacheReadTokens, bucket.Usage.CacheWriteTokens, bucket.Day); err != nil {
			return out, err
		}
	}

	res, err = tx.Exec(`DELETE FROM exploration_nodes
		WHERE exploration_id=$1 AND (
			id=$2 OR id IN (
				SELECT e.dst_id FROM exploration_edges e
				JOIN exploration_nodes n ON n.id=e.dst_id AND n.exploration_id=e.exploration_id
					WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel=$3
					  AND n.kind IN ('fact','finding')
					  AND NOT EXISTS (
					      SELECT 1 FROM exploration_edges other
					      WHERE other.exploration_id=e.exploration_id AND other.dst_id=e.dst_id
					        AND other.src_id<>$2
					  )
				)
		)`, s.expID, id, RelYields)
	if err != nil {
		return out, err
	}
	if removed, _ := res.RowsAffected(); removed != 1+out.Facts+out.Findings {
		return out, fmt.Errorf("intent cleanup changed concurrently")
	}
	out.Intents = 1
	return out, tx.Commit()
}

type tokenUsageBucket struct {
	Day   time.Time
	Usage TokenUsage
}

// intentTokenRollup returns exactly-once usage grouped by its original UTC day.
// Each result terminates one run. A result carrying usage is authoritative; an
// older error/cancel result without usage falls back to the latest cumulative
// usage frame since the previous result. A trailing frame represents a run that
// was interrupted before its terminal result could be persisted.
func intentTokenRollup(tx *sql.Tx, explorationID, intentID int64) ([]tokenUsageBucket, error) {
	rows, err := tx.Query(`SELECT kind, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, created_at
		FROM activity
		WHERE exploration_id=$1 AND node_id=$2 AND kind IN ('usage','result')
		ORDER BY id`, explorationID, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDay := make(map[time.Time]TokenUsage)
	add := func(at time.Time, usage TokenUsage) {
		if usage.InputTokens == 0 && usage.OutputTokens == 0 &&
			usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
			return
		}
		utc := at.UTC()
		day := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
		total := byDay[day]
		total.add(usage)
		byDay[day] = total
	}

	var pending TokenUsage
	var pendingAt time.Time
	hasPending := false
	for rows.Next() {
		var kind string
		var input, output, read, write sql.NullInt64
		var createdAt time.Time
		if err := rows.Scan(&kind, &input, &output, &read, &write, &createdAt); err != nil {
			return nil, err
		}
		current := TokenUsage{
			InputTokens:      int(input.Int64),
			OutputTokens:     int(output.Int64),
			CacheReadTokens:  int(read.Int64),
			CacheWriteTokens: int(write.Int64),
		}
		hasCurrent := input.Valid || output.Valid || read.Valid || write.Valid
		if kind == "usage" {
			if hasCurrent {
				pending, pendingAt, hasPending = current, createdAt, true
			}
			continue
		}

		if hasCurrent {
			// Preserve a partially populated legacy terminal row by filling only its
			// missing dimensions from the latest cumulative usage frame.
			if hasPending {
				if !input.Valid {
					current.InputTokens = pending.InputTokens
				}
				if !output.Valid {
					current.OutputTokens = pending.OutputTokens
				}
				if !read.Valid {
					current.CacheReadTokens = pending.CacheReadTokens
				}
				if !write.Valid {
					current.CacheWriteTokens = pending.CacheWriteTokens
				}
			}
			add(createdAt, current)
		} else if hasPending {
			add(createdAt, pending)
		}
		pending, pendingAt, hasPending = TokenUsage{}, time.Time{}, false
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if hasPending {
		add(pendingAt, pending)
	}

	buckets := make([]tokenUsageBucket, 0, len(byDay))
	for day, usage := range byDay {
		buckets = append(buckets, tokenUsageBucket{Day: day, Usage: usage})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Day.Before(buckets[j].Day) })
	return buckets, nil
}

func (u *TokenUsage) add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheWriteTokens += other.CacheWriteTokens
}

const nodeCols = `id, kind, payload, priority, state, COALESCE(origin,''), COALESCE(owner,''), COALESCE(blocked_reason,''), created_at`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var payload []byte
	if err := sc.Scan(&n.ID, &n.Kind, &payload, &n.Priority, &n.State, &n.Origin, &n.Owner, &n.BlockedReason, &n.CreatedAt); err != nil {
		return nil, err
	}
	n.Payload = json.RawMessage(payload)
	return &n, nil
}

func scanNodes(rows *sql.Rows) ([]*Node, error) {
	var out []*Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListByKind lists nodes of a kind (newest first).
func (s *ExplorationStore) ListByKind(kind string, limit int) ([]*Node, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
WHERE exploration_id=$1 AND kind=$2 ORDER BY id DESC LIMIT $3`, s.expID, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// ListByKindPage returns one newest-first page of nodes of a kind for reverse
// pagination: up to `limit` nodes with id < `before` (before<=0 = the newest page).
// hasMore reports whether still-older nodes exist, so a client (e.g. the worker
// session list) can page past the old fixed cap instead of losing older intents.
func (s *ExplorationStore) ListByKindPage(kind string, before int64, limit int) ([]*Node, bool, error) {
	if limit <= 0 {
		limit = 300
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
WHERE exploration_id=$1 AND kind=$2 AND ($3 <= 0 OR id < $3)
ORDER BY id DESC LIMIT $4`, s.expID, kind, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(nodes) > limit
	if hasMore {
		nodes = nodes[:limit]
	}
	return nodes, hasMore, nil
}

// GetNode returns one node of this exploration by id (nil, nil if not found).
func (s *ExplorationStore) GetNode(id int64) (*Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM exploration_nodes WHERE id=$1 AND exploration_id=$2`, id, s.expID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return n, err
}

// Nodes lists all nodes (for graph viz).
func (s *ExplorationStore) Nodes(limit int) ([]*Node, error) {
	if limit <= 0 {
		limit = 2000
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 ORDER BY id LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// Edge is one row from exploration_edges.
type Edge struct {
	From int64
	Rel  string
	To   int64
}

// Edges lists exploration edges (for graph viz).
func (s *ExplorationStore) Edges(limit int) ([]Edge, error) {
	if limit <= 0 {
		limit = 5000
	}
	rows, err := s.db.Query(`SELECT src_id, rel, dst_id FROM exploration_edges WHERE exploration_id=$1 LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.Rel, &e.To); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// FindingLineage returns the sub-DAG that leads FROM the exploration root TO the
// given node: the node itself plus all its ancestors (nodes reverse-reachable by
// following edges backward), and every edge whose both endpoints are in that set.
// Used to render "how this finding was reached" — origin → … → finding. Returns
// empty (not error) for a nonexistent node.
func (s *ExplorationStore) FindingLineage(nodeID int64) ([]*Node, []Edge, error) {
	// anc = the node + everything that can reach it via edges (walk src<-dst).
	nodeRows, err := s.db.Query(`
WITH RECURSIVE anc(id) AS (
    SELECT $2::bigint
  UNION
    SELECT e.src_id
    FROM exploration_edges e
    JOIN anc ON e.dst_id = anc.id
    WHERE e.exploration_id = $1
)
SELECT `+nodeCols+`
FROM exploration_nodes
WHERE exploration_id = $1 AND id IN (SELECT id FROM anc)
ORDER BY id`, s.expID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := scanNodes(nodeRows)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) == 0 {
		return nil, nil, nil
	}
	// edges among the ancestor set only (the lineage's internal relations).
	edgeRows, err := s.db.Query(`
WITH RECURSIVE anc(id) AS (
    SELECT $2::bigint
  UNION
    SELECT e.src_id
    FROM exploration_edges e
    JOIN anc ON e.dst_id = anc.id
    WHERE e.exploration_id = $1
)
SELECT src_id, rel, dst_id
FROM exploration_edges
WHERE exploration_id = $1
  AND src_id IN (SELECT id FROM anc)
  AND dst_id IN (SELECT id FROM anc)`, s.expID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	defer edgeRows.Close()
	var edges []Edge
	for edgeRows.Next() {
		var e Edge
		if err := edgeRows.Scan(&e.From, &e.Rel, &e.To); err != nil {
			return nil, nil, err
		}
		edges = append(edges, e)
	}
	return nodes, edges, edgeRows.Err()
}

// FindingIntents maps each finding id to the intent that produced it (the
// intent --yields--> finding edge; report_finding links it). Findings with no
// producing intent are absent. Precise (JOIN, no edge-scan limit).
func (s *ExplorationStore) FindingIntents() (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT e.dst_id, e.src_id
FROM exploration_edges e
JOIN exploration_nodes n ON n.id = e.dst_id AND n.exploration_id = e.exploration_id
WHERE e.exploration_id=$1 AND e.rel=$2 AND n.kind='finding'`, s.expID, RelYields)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var findingID, intentID int64
		if err := rows.Scan(&findingID, &intentID); err != nil {
			return nil, err
		}
		out[findingID] = intentID
	}
	return out, rows.Err()
}

// FindingIntentsTerminal is the inherited-history variant: it returns finding
// lineage only when the producing intent has reached an immutable terminal
// state. This keeps a source task's live worker topology private.
func (s *ExplorationStore) FindingIntentsTerminal() (map[int64]int64, error) {
	rows, err := s.db.Query(`SELECT e.dst_id, e.src_id
	FROM exploration_edges e
	JOIN exploration_nodes finding ON finding.id=e.dst_id AND finding.exploration_id=e.exploration_id
	JOIN exploration_nodes intent ON intent.id=e.src_id AND intent.exploration_id=e.exploration_id
	WHERE e.exploration_id=$1 AND e.rel=$2 AND finding.kind='finding'
	  AND intent.kind='intent' AND intent.state IN ('done','blocked','exhausted','stopped')`, s.expID, RelYields)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var findingID, intentID int64
		if err := rows.Scan(&findingID, &intentID); err != nil {
			return nil, err
		}
		out[findingID] = intentID
	}
	return out, rows.Err()
}

// Frontier returns open intents ordered by priority desc then id (FIFO tiebreak).
func (s *ExplorationStore) Frontier(limit int) ([]*Node, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='open'
ORDER BY priority DESC, id ASC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodes(rows)
}

// HasActiveIntent reports whether this exploration has any intent still in play
// (state open OR running). Used to decide whether to kick the FIRST planner round:
// a seeded task's intent may already have been claimed (open→running) by a worker
// before the check, so counting only 'open' (Frontier) races with worker claim and
// wrongly kicks a redundant first round. Counting open+running is race-proof.
func (s *ExplorationStore) HasActiveIntent() (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state IN ('open','running'))`, s.expID).Scan(&exists)
	return exists, err
}

// HasOpenGoal reports whether this exploration still has any goal in state 'open'.
// false ⇒ 所有目标已 met/abandoned（或本任务无目标）⇒ 进入 goalless（人工直投）分支：
// planner 停跑，任务是否结束改由 frontier 是否抽干决定。met 与 abandoned 都算"已了结"。
func (s *ExplorationStore) HasOpenGoal() (bool, error) {
	var exists bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes
WHERE exploration_id=$1 AND kind='goal' AND state='open')`, s.expID).Scan(&exists)
	return exists, err
}

// ClaimIntent atomically moves an open intent to running. Returns true if claimed.
func (s *ExplorationStore) ClaimIntent(id int64, owner string) (bool, error) {
	res, err := s.db.Exec(`UPDATE exploration_nodes SET state='running', owner=$1
WHERE id=$2 AND exploration_id=$3 AND kind='intent' AND state='open'`, owner, id, s.expID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Stats returns node counts grouped by kind (for dashboard).
func (s *ExplorationStore) Stats() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT kind, count(*) FROM exploration_nodes WHERE exploration_id=$1 GROUP BY kind`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var c int
		if err := rows.Scan(&k, &c); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, rows.Err()
}

// --- activity (poll by global id cursor) ---

// AppendActivity records one worker step and returns its id.
func (s *ExplorationStore) AppendActivity(a Activity) (int64, error) {
	metadata := a.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	var id int64
	err := s.db.QueryRow(`
INSERT INTO activity(exploration_id, node_id, worker, kind, tool, tool_use_id, is_error, summary, detail, metadata, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),NULLIF($6,''),$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14)
RETURNING id`, s.expID, a.NodeID, utf8Clean(a.Worker), utf8Clean(a.Kind), utf8Clean(a.Tool), utf8Clean(a.ToolUseID), a.IsError, utf8Clean(a.Summary), utf8Clean(a.Detail),
		metadata, a.InputTokens, a.OutputTokens, a.CacheReadTokens, a.CacheWriteTokens).Scan(&id)
	return id, err
}

// ExplorationDiag probes, at the moment of an activity FK violation (23503), why the
// parent row is unreachable. It reports whether the exploration row still exists, how
// many task rows reference it (a live task should keep exactly 1 — and RESTRICT on
// tasks.exploration_id means the exploration CANNOT be deleted while that row lives),
// and the current MAX(explorations.id). Together these tell apart the failure modes:
//   - expExists=false, taskRefs=0 → the whole row set is gone (DB reset / wrong DB).
//   - expExists=false, taskRefs=1 → impossible under RESTRICT; would mean a broken FK.
//   - expExists=true              → the write's expID is NOT this exploration (stale/
//     wrong id in the in-memory store); compare against Store.ID().
func (d *DB) ExplorationDiag(expID int64) (expExists bool, taskRefs int, maxExpID int64, err error) {
	err = d.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM explorations WHERE id=$1),
		(SELECT COUNT(*) FROM tasks WHERE exploration_id=$1),
		COALESCE((SELECT MAX(id) FROM explorations),0)`, expID).Scan(&expExists, &taskRefs, &maxExpID)
	return
}

// TokenTotal sums token usage across ALL workers for this exploration (whole-task
// total). Same source as TokenStatsByWorker (kind='result' rows), just ungrouped.
func (s *ExplorationStore) TokenTotal() (TokenUsage, error) {
	var u TokenUsage
	err := s.db.QueryRow(`SELECT
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
	FROM activity WHERE exploration_id=$1 AND kind='result'`, s.expID).
		Scan(&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens)
	return u, err
}

// TokenTotalsAll returns the whole-task token total for every exploration in one
// query (exploration_id → total), so the task list can show per-task consumption
// without a query per row.
func (d *DB) TokenTotalsAll() (map[int64]TokenUsage, error) {
	rows, err := d.Query(`SELECT exploration_id,
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
	FROM activity WHERE kind='result' GROUP BY exploration_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TokenUsage{}
	for rows.Next() {
		var eid int64
		var u TokenUsage
		if err := rows.Scan(&eid, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens); err != nil {
			return nil, err
		}
		out[eid] = u
	}
	return out, rows.Err()
}

// LastActivityAll returns the unix time of the most recent activity per
// exploration (exploration_id → max created_at epoch), one query for all tasks.
// Persisted (unlike Engine.LastActivity's in-memory map), so it survives restarts
// and gives终态任务 a stable "ran until" time for computing run duration.
func (d *DB) LastActivityAll() (map[int64]int64, error) {
	rows, err := d.Query(`SELECT exploration_id, EXTRACT(EPOCH FROM MAX(created_at))::bigint FROM activity GROUP BY exploration_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var eid, ts int64
		if err := rows.Scan(&eid, &ts); err != nil {
			return nil, err
		}
		out[eid] = ts
	}
	return out, rows.Err()
}

// GoalCounts is the goal summary for one exploration.
type GoalCounts struct{ Total, Met int }

// TaskListMetrics contains the aggregates rendered in task lists.
type TaskListMetrics struct {
	Tokens       TokenUsage
	LastActivity int64
	Goals        GoalCounts
}

// TaskListMetricsAll returns list aggregates for every live task in one query.
// The lateral lookups use the per-exploration indexes instead of grouping the
// complete activity history on every poll.
func (d *DB) TaskListMetricsAll() (map[int64]TaskListMetrics, error) {
	rows, err := d.Query(`
		SELECT task.exploration_id,
		       COALESCE(token_metrics.input_tokens,0),
		       COALESCE(token_metrics.output_tokens,0),
		       COALESCE(token_metrics.cache_read_tokens,0),
		       COALESCE(token_metrics.cache_write_tokens,0),
		       COALESCE(latest_activity.created_at,0),
		       COALESCE(goal_metrics.total,0),
		       COALESCE(goal_metrics.met,0)
		FROM tasks task
		LEFT JOIN LATERAL (
			SELECT SUM(input_tokens) AS input_tokens,
			       SUM(output_tokens) AS output_tokens,
			       SUM(cache_read_tokens) AS cache_read_tokens,
			       SUM(cache_write_tokens) AS cache_write_tokens
			FROM activity
			WHERE exploration_id=task.exploration_id AND kind='result'
		) token_metrics ON true
		LEFT JOIN LATERAL (
			SELECT EXTRACT(EPOCH FROM created_at)::bigint AS created_at
			FROM activity
			WHERE exploration_id=task.exploration_id
			ORDER BY created_at DESC
			LIMIT 1
		) latest_activity ON true
		LEFT JOIN LATERAL (
			SELECT COUNT(*) AS total,
			       COUNT(*) FILTER (WHERE state='met') AS met
			FROM exploration_nodes
			WHERE exploration_id=task.exploration_id AND kind='goal'
		) goal_metrics ON true
		WHERE task.deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]TaskListMetrics{}
	for rows.Next() {
		var explorationID int64
		var metrics TaskListMetrics
		if err := rows.Scan(
			&explorationID,
			&metrics.Tokens.InputTokens,
			&metrics.Tokens.OutputTokens,
			&metrics.Tokens.CacheReadTokens,
			&metrics.Tokens.CacheWriteTokens,
			&metrics.LastActivity,
			&metrics.Goals.Total,
			&metrics.Goals.Met,
		); err != nil {
			return nil, err
		}
		out[explorationID] = metrics
	}
	return out, rows.Err()
}

// GoalCountsAll returns goal totals (total / met) per exploration id, one query
// for all tasks — used by listTasks so the task list shows progress for every task.
func (d *DB) GoalCountsAll() (map[int64]GoalCounts, error) {
	rows, err := d.Query(`
		SELECT exploration_id,
		       COUNT(*) AS total,
		       COUNT(*) FILTER (WHERE state = 'met') AS met
		FROM exploration_nodes
		WHERE kind = 'goal'
		GROUP BY exploration_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]GoalCounts{}
	for rows.Next() {
		var eid int64
		var gc GoalCounts
		if err := rows.Scan(&eid, &gc.Total, &gc.Met); err != nil {
			return nil, err
		}
		out[eid] = gc
	}
	return out, rows.Err()
}

// TokenStatsByWorker sums token usage per worker for this exploration (from the
// kind='result' records that carry usage). Used by the per-agent token display.
func (s *ExplorationStore) TokenStatsByWorker() ([]TokenUsage, error) {
	rows, err := s.db.Query(`SELECT COALESCE(worker,''),
		COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
	FROM activity WHERE exploration_id=$1 AND kind='result'
	GROUP BY worker ORDER BY worker`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenUsage{}
	for rows.Next() {
		var u TokenUsage
		if err := rows.Scan(&u.Worker, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// TokenStatsBySession aggregates every persisted completed run, independent of
// activity-history pagination. Main/planner use fixed keys; Worker executions use
// their intent node id, even when a different work#N process handled a retry.
func (s *ExplorationStore) TokenStatsBySession() ([]SessionTokenUsage, error) {
	rows, err := s.db.Query(`SELECT
		CASE
			WHEN COALESCE(a.worker,'')='mainagent' THEN 'main'
			WHEN COALESCE(a.worker,'')='planner' THEN 'plan'
			WHEN n.id IS NOT NULL THEN 'intent:' || n.id::text
			ELSE ''
		END AS session_key,
		COALESCE(SUM(a.input_tokens),0), COALESCE(SUM(a.output_tokens),0),
		COALESCE(SUM(a.cache_read_tokens),0), COALESCE(SUM(a.cache_write_tokens),0)
	FROM activity a
	LEFT JOIN exploration_nodes n
	  ON n.id=a.node_id AND n.exploration_id=a.exploration_id AND n.kind='intent'
	WHERE a.exploration_id=$1 AND a.kind='result'
	  AND (COALESCE(a.worker,'') IN ('mainagent','planner') OR n.id IS NOT NULL)
	GROUP BY session_key
	ORDER BY session_key`, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionTokenUsage{}
	for rows.Next() {
		var u SessionTokenUsage
		if err := rows.Scan(&u.Session, &u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ActivityList returns steps after sinceID (exclusive), optionally filtered by node.
// Returns items and the new cursor (max id seen).
func (s *ExplorationStore) ActivityList(nodeID *int64, sinceID int64, limit int) ([]Activity, int64, error) {
	if limit <= 0 {
		limit = 300
	}
	var rows *sql.Rows
	var err error
	const cols = `id, node_id, COALESCE(worker,''), COALESCE(kind,''), COALESCE(tool,''), COALESCE(tool_use_id,''), is_error, COALESCE(summary,''), metadata, created_at, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens`
	if nodeID != nil {
		rows, err = s.db.Query(`SELECT `+cols+`
FROM activity WHERE exploration_id=$1 AND node_id=$2 AND id>$3 ORDER BY id LIMIT $4`, s.expID, *nodeID, sinceID, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+cols+`
FROM activity WHERE exploration_id=$1 AND id>$2 ORDER BY id LIMIT $3`, s.expID, sinceID, limit)
	}
	if err != nil {
		return nil, sinceID, err
	}
	defer rows.Close()
	out := []Activity{}
	cursor := sinceID
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.Metadata, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, sinceID, err
		}
		if a.ID > cursor {
			cursor = a.ID
		}
		out = append(out, a)
	}
	return out, cursor, rows.Err()
}

// ActivityListForTerminalIntent is the inherited, incremental-read boundary.
// The intent state check and activity read happen in one statement so a source
// intent reopened between API-level checks cannot expose its new in-flight trace.
// Model reasoning and accounting rows are never inherited.
func (s *ExplorationStore) ActivityListForTerminalIntent(nodeID, sinceID int64, limit int) ([]Activity, int64, error) {
	if limit <= 0 {
		limit = 300
	}
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''),
		COALESCE(a.tool,''), COALESCE(a.tool_use_id,''), a.is_error, COALESCE(a.summary,''),
		a.created_at, a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
	WHERE a.exploration_id=$1 AND a.node_id=$2 AND a.id>$3
	  AND n.kind='intent' AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.kind NOT IN ('thinking','usage')
	ORDER BY a.id LIMIT $4`, s.expID, nodeID, sinceID, limit)
	if err != nil {
		return nil, sinceID, err
	}
	defer rows.Close()
	out := []Activity{}
	cursor := sinceID
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, sinceID, err
		}
		if a.ID > cursor {
			cursor = a.ID
		}
		out = append(out, a)
	}
	return out, cursor, rows.Err()
}

// ActivitySessionFilter selects one UI "session" within a task's activity stream.
// Exactly one field is meaningful:
//   - Worker != ""  → filter by worker name (Main = "mainagent", Plan = "planner").
//     Goal Agent 的第 0 轮拆解也以 worker="planner" 落库，故 Plan 会话完整覆盖 Goal+Planner。
//   - NodeID != nil → a Worker session, filtered by node_id (= intent id).
//
// A zero value (both empty) matches the whole task (no session filter).
type ActivitySessionFilter struct {
	Worker string
	NodeID *int64
}

func (f ActivitySessionFilter) cond(argStart int) (string, []any) {
	switch {
	case f.NodeID != nil:
		return fmt.Sprintf(" AND node_id=$%d", argStart), []any{*f.NodeID}
	case f.Worker != "":
		return fmt.Sprintf(" AND worker=$%d", argStart), []any{f.Worker}
	default:
		return "", nil
	}
}

// ActivityPage returns one page for reverse (newest-first) pagination of a session:
// up to `limit` steps ending before id `before` (exclusive; before<=0 = the latest
// page), returned in ASCENDING id order for direct display. hasMore reports whether
// still-older steps exist before the returned window (so the client can stop loading
// on scroll-up). Summary-only columns — detail is lazy-loaded via ActivityDetail.
func (s *ExplorationStore) ActivityPage(f ActivitySessionFilter, before int64, limit int) ([]Activity, bool, error) {
	if limit <= 0 {
		limit = 200
	}
	const cols = `id, node_id, COALESCE(worker,''), COALESCE(kind,''), COALESCE(tool,''), COALESCE(tool_use_id,''), is_error, COALESCE(summary,''), metadata, created_at, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens`
	args := []any{s.expID}
	cond, cargs := f.cond(len(args) + 1)
	args = append(args, cargs...)
	beforeArg := len(args) + 1
	args = append(args, before)
	limitArg := len(args) + 1
	args = append(args, limit+1) // one extra row to detect older history
	q := fmt.Sprintf(`SELECT `+cols+`
FROM activity WHERE exploration_id=$1%s AND ($%d <= 0 OR id < $%d)
ORDER BY id DESC LIMIT $%d`, cond, beforeArg, beforeArg, limitArg)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	desc := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.Metadata, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, false, err
		}
		desc = append(desc, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(desc) > limit
	if hasMore {
		desc = desc[:limit]
	}
	// reverse the newest-first window into ascending id order for display.
	out := make([]Activity, len(desc))
	for i, a := range desc {
		out[len(desc)-1-i] = a
	}
	return out, hasMore, nil
}

// ActivityPageForTerminalIntent is the inherited session-history boundary. It
// combines ownership, terminal-state and row-kind checks in one query, closing
// the race where a previously completed source intent is reopened before paging.
func (s *ExplorationStore) ActivityPageForTerminalIntent(nodeID, before int64, limit int) ([]Activity, bool, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''),
		COALESCE(a.tool,''), COALESCE(a.tool_use_id,''), a.is_error, COALESCE(a.summary,''),
		a.created_at, a.input_tokens, a.output_tokens, a.cache_read_tokens, a.cache_write_tokens
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
	WHERE a.exploration_id=$1 AND a.node_id=$2
	  AND n.kind='intent' AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.kind NOT IN ('thinking','usage')
	  AND ($3 <= 0 OR a.id < $3)
	ORDER BY a.id DESC LIMIT $4`, s.expID, nodeID, before, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	desc := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.CreatedAt,
			&a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens); err != nil {
			return nil, false, err
		}
		desc = append(desc, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(desc) > limit
	if hasMore {
		desc = desc[:limit]
	}
	out := make([]Activity, len(desc))
	for i, a := range desc {
		out[len(desc)-1-i] = a
	}
	return out, hasMore, nil
}

// ActivityMaxID returns the task's current maximum activity id (0 when empty). It is
// the task-level snapshot cursor handed to the SSE stream so history (id<=cursor) and
// the live tail (id>cursor) meet with no gap — deliberately task-level, not per
// session, since one SSE covers the whole task.
func (s *ExplorationStore) ActivityMaxID() (int64, error) {
	var max sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(id) FROM activity WHERE exploration_id=$1`, s.expID).Scan(&max)
	if err != nil {
		return 0, err
	}
	return max.Int64, nil
}

// ActivityDetail lazily returns the full detail blob for one step.
func (s *ExplorationStore) ActivityDetail(id int64) (string, error) {
	var d sql.NullString
	err := s.db.QueryRow(`SELECT detail FROM activity WHERE id=$1 AND exploration_id=$2`, id, s.expID).Scan(&d)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return d.String, err
}

// scanTrace reads the summary-only column set shared by the worker-trace tools
// (ActivityTrace / ActivityTraceSearch). Detail is never selected here — it is
// pulled separately, on demand, via ActivityByIDs.
func scanTrace(rows *sql.Rows) ([]Activity, error) {
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.IsError, &a.Summary); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const traceCols = `id, node_id, COALESCE(worker,''), COALESCE(kind,''), COALESCE(tool,''), is_error, COALESCE(summary,'')`

// ActivityTrace lists one work's steps (by intent/node id) for the trace tools:
// summaries only (detail is lazy — fetch via ActivityByIDs). thinking/usage rows
// are dropped so the caller sees the work's actions + observations, not the
// model's internal reasoning or token-accounting noise.
func (s *ExplorationStore) ActivityTrace(nodeID int64, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT `+traceCols+`
FROM activity WHERE exploration_id=$1 AND node_id=$2 AND kind NOT IN ('thinking','usage')
ORDER BY id LIMIT $3`, s.expID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// ActivityTraceForTerminalIntent returns one inherited work trace only while its
// source intent is still terminal at query time.
func (s *ExplorationStore) ActivityTraceForTerminalIntent(nodeID int64, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''), COALESCE(a.tool,''), a.is_error, COALESCE(a.summary,'')
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
	WHERE a.exploration_id=$1 AND a.node_id=$2 AND n.kind='intent'
	  AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.kind NOT IN ('thinking','usage')
	ORDER BY a.id LIMIT $3`, s.expID, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// ActivityTraceSearch finds steps whose summary OR detail matches q
// (case-insensitive), returning summaries only. nodeID != nil scopes to one work;
// nil searches across ALL works (node_id IS NOT NULL — planner/main steps have no
// node_id, so this cleanly means "worker traces"). thinking/usage rows dropped.
func (s *ExplorationStore) ActivityTraceSearch(nodeID *int64, q string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + q + "%"
	var rows *sql.Rows
	var err error
	if nodeID != nil {
		rows, err = s.db.Query(`SELECT `+traceCols+`
FROM activity WHERE exploration_id=$1 AND node_id=$2 AND kind NOT IN ('thinking','usage')
AND (summary ILIKE $3 OR detail ILIKE $3) ORDER BY id LIMIT $4`, s.expID, *nodeID, like, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+traceCols+`
FROM activity WHERE exploration_id=$1 AND node_id IS NOT NULL AND kind NOT IN ('thinking','usage')
AND (summary ILIKE $2 OR detail ILIKE $2) ORDER BY id LIMIT $3`, s.expID, like, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// ActivityTraceSearchForTerminalIntent is the scoped inherited search variant;
// terminal eligibility is evaluated by the same statement that reads the rows.
func (s *ExplorationStore) ActivityTraceSearchForTerminalIntent(nodeID int64, q string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + q + "%"
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''), COALESCE(a.tool,''), a.is_error, COALESCE(a.summary,'')
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
	WHERE a.exploration_id=$1 AND a.node_id=$2 AND n.kind='intent'
	  AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.kind NOT IN ('thinking','usage')
	  AND (a.summary ILIKE $3 OR a.detail ILIKE $3)
	ORDER BY a.id LIMIT $4`, s.expID, nodeID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// ActivityTraceSearchTerminalIntents searches inherited worker history without
// exposing planner/main rows or work that is still open/running in the source.
func (s *ExplorationStore) ActivityTraceSearchTerminalIntents(q string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 100
	}
	like := "%" + q + "%"
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.worker,''), COALESCE(a.kind,''), COALESCE(a.tool,''), a.is_error, COALESCE(a.summary,'')
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
	WHERE a.exploration_id=$1 AND n.kind='intent'
	  AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.kind NOT IN ('thinking','usage')
	  AND (a.summary ILIKE $2 OR a.detail ILIKE $2)
	ORDER BY a.id LIMIT $3`, s.expID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// AssetRef is a compact exploration node (intent/fact/finding) that references an
// asset via an anchor — for the coverage graph's node drawer.
type AssetRef struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Summary      string `json:"summary"`
	SourceTaskID int64  `json:"source_task_id,omitempty"`
	Inherited    bool   `json:"inherited,omitempty"`
}

// AssetRefs returns the intents / facts / findings in THIS exploration anchored to
// the given asset id (newest first) — what this task tested / concluded about it.
func (s *ExplorationStore) AssetRefs(assetID int64) ([]AssetRef, error) {
	if assetID <= 0 {
		return []AssetRef{}, nil
	}
	rows, err := s.db.Query(`
SELECT en.id, en.kind, en.state, en.payload
FROM exploration_anchors ea
JOIN exploration_nodes en ON en.id = ea.node_id
WHERE ea.asset_id = $1 AND en.exploration_id = $2 AND en.kind IN ('intent','fact','finding')
ORDER BY en.id DESC`, assetID, s.expID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssetRef{}
	for rows.Next() {
		var r AssetRef
		var payload []byte
		if err := rows.Scan(&r.ID, &r.Kind, &r.State, &payload); err != nil {
			return nil, err
		}
		var p map[string]any
		_ = json.Unmarshal(payload, &p)
		for _, k := range []string{"summary", "text"} {
			if v, ok := p[k].(string); ok && strings.TrimSpace(v) != "" {
				r.Summary = v
				break
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityTraceSearchExcluding keyword-searches every node's steps in this
// exploration EXCEPT one node's own (excludeNodeID) — so a worker's
// search_all_worker_traces doesn't return its own in-progress trace (which is
// already in its context). excludeNodeID<=0 → no exclusion (searches all nodes).
func (s *ExplorationStore) ActivityTraceSearchExcluding(excludeNodeID int64, q string, limit int) ([]Activity, error) {
	if excludeNodeID <= 0 {
		return s.ActivityTraceSearch(nil, q, limit)
	}
	if limit <= 0 {
		limit = 100
	}
	like := "%" + q + "%"
	rows, err := s.db.Query(`SELECT `+traceCols+`
FROM activity WHERE exploration_id=$1 AND node_id IS NOT NULL AND node_id <> $2
AND kind NOT IN ('thinking','usage')
AND (summary ILIKE $3 OR detail ILIKE $3) ORDER BY id LIMIT $4`, s.expID, excludeNodeID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTrace(rows)
}

// ActivityByIDs loads full detail for specific step ids (the trace drill-down),
// scoped to this exploration. thinking rows are skipped (never returned, even if
// their id is asked for). Order is ascending id, not the input order.
func (s *ExplorationStore) ActivityByIDs(ids []int64) ([]Activity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids)+1)
	args[0] = s.expID
	for i, id := range ids {
		ph[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = id
	}
	rows, err := s.db.Query(`SELECT id, node_id, COALESCE(kind,''), COALESCE(tool,''), is_error, COALESCE(detail,'')
FROM activity WHERE exploration_id=$1 AND kind<>'thinking' AND id IN (`+strings.Join(ph, ",")+`) ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Kind, &a.Tool, &a.IsError, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ActivityByIDsForTerminalIntents is the inherited-detail read boundary. It
// deliberately excludes node-less planner/main rows, non-intent activities, live
// source work, and thinking. Local callers that need legacy behavior use
// ActivityByIDs instead.
func (s *ExplorationStore) ActivityByIDsForTerminalIntents(ids []int64) ([]Activity, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids)+1)
	args[0] = s.expID
	for i, id := range ids {
		ph[i] = fmt.Sprintf("$%d", i+2)
		args[i+1] = id
	}
	rows, err := s.db.Query(`SELECT a.id, a.node_id, COALESCE(a.kind,''), COALESCE(a.tool,''), a.is_error, COALESCE(a.detail,'')
	FROM activity a
	JOIN exploration_nodes n ON n.id=a.node_id AND n.exploration_id=a.exploration_id
		WHERE a.exploration_id=$1 AND a.kind NOT IN ('thinking','usage') AND n.kind='intent'
	  AND n.state IN ('done','blocked','exhausted','stopped')
	  AND a.id IN (`+strings.Join(ph, ",")+`) ORDER BY a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Kind, &a.Tool, &a.IsError, &a.Detail); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddStandaloneFinding writes a finding to the standalone findings table, which
// persists across task deletion (task_id / node_id become NULL when the task or
// exploration node is deleted). taskID and nodeID may be 0 (stored as NULL).
func (s *ExplorationStore) AddStandaloneFinding(taskID, nodeID int64, vulnclass, name, severity, summary, evidence, worker string, assetIDs []int64) (int64, error) {
	return s.db.AddFinding(taskID, nodeID, vulnclass, name, severity, summary, evidence, worker, assetIDs)
}
