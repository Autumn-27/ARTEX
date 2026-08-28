package db

import (
	"database/sql"
	"fmt"
	"time"
)

// CommandRecord is a paired tool_use + tool_result from the activity table
// (any tool, not just Bash). Command holds the raw tool input (JSON).
type CommandRecord struct {
	ID        int64     `json:"id"`
	ExpID     int64     `json:"exploration_id"`
	Worker    string    `json:"worker"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command"`
	Output    string    `json:"output"`
	IsError   bool      `json:"is_error"`
	CreatedAt time.Time `json:"created_at"`
}

// commandFilter builds the WHERE clause shared by the tool-execution list and
// its per-tool tally, so the summary always describes exactly the rows the table
// pages through. Returns the clause, its args, and the next placeholder index.
func commandFilter(expID *int64, q string) (string, []any, int) {
	where := `WHERE u.kind = 'tool_use'`
	args := []any{}
	argN := 1

	if expID != nil {
		where += fmt.Sprintf(` AND u.exploration_id = $%d`, argN)
		args = append(args, *expID)
		argN++
	}
	if q != "" {
		where += fmt.Sprintf(` AND (u.tool ILIKE $%d OR u.detail ILIKE $%d)`, argN, argN)
		args = append(args, "%"+q+"%")
		argN++
	}
	return where, args, argN
}

// ToolStat is one tool's execution tally for the usage summary.
type ToolStat struct {
	Tool   string `json:"tool"`
	Total  int    `json:"total"`
	Errors int    `json:"errors"`
}

// ToolStats counts executions grouped by tool under the same filters
// ListCommands takes. Unpaginated on purpose: the tally describes the whole
// filtered set, not the page currently on screen.
func (d *DB) ToolStats(expID *int64, q string) ([]ToolStat, error) {
	where, args, _ := commandFilter(expID, q)

	rows, err := d.Query(`
SELECT COALESCE(NULLIF(u.tool,''),'-') AS tool, COUNT(*) AS total,
       COUNT(*) FILTER (WHERE COALESCE(r.is_error,false)) AS errors
FROM activity u
LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind = 'tool_result'
`+where+`
GROUP BY 1
ORDER BY total DESC, tool ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ToolStat{}
	for rows.Next() {
		var s ToolStat
		if err := rows.Scan(&s.Tool, &s.Total, &s.Errors); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ListCommands returns tool executions (tool_use + paired tool_result) across all
// explorations, with optional filtering and pagination. Covers every tool, not
// just Bash; q matches the tool name or its input.
func (d *DB) ListCommands(expID *int64, q string, page, size int) ([]CommandRecord, int, error) {
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	offset := page * size

	where, args, argN := commandFilter(expID, q)

	// count
	var total int
	countQ := `SELECT COUNT(*) FROM activity u ` + where
	if err := d.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// data query: join tool_use with its tool_result
	dataQ := `
SELECT u.id, u.exploration_id, COALESCE(u.worker,''), COALESCE(u.tool,''), COALESCE(u.detail,''),
       COALESCE(r.detail,''), COALESCE(r.is_error, false), u.created_at
FROM activity u
LEFT JOIN activity r ON r.tool_use_id = u.tool_use_id AND r.kind = 'tool_result'
` + where + `
ORDER BY u.id DESC
LIMIT $` + fmt.Sprintf("%d", argN) + ` OFFSET $` + fmt.Sprintf("%d", argN+1)

	args = append(args, size, offset)
	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []CommandRecord{}
	for rows.Next() {
		var c CommandRecord
		if err := rows.Scan(&c.ID, &c.ExpID, &c.Worker, &c.Tool, &c.Command, &c.Output, &c.IsError, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// LLMRecord is one recorded LLM API call (request + response).
type LLMRecord struct {
	ID           int64     `json:"id"`
	Ts           time.Time `json:"ts"`
	Model        string    `json:"model"`
	ProfileName  string    `json:"profile_name"`
	SessionID    string    `json:"session_id"`
	TaskID       string    `json:"task_id"`
	Worker       string    `json:"worker"`
	LatencyMs    int       `json:"latency_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CacheRead    int       `json:"cache_read"`
	CacheWrite   int       `json:"cache_write"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	// RawRequest / RawResponse are the untouched HTTP bodies exchanged with the
	// provider — the request as buildBody() sent it (full tool schemas included)
	// and the raw SSE frames. RequestBody/ResponseBody above are the normalized
	// view, which drops tool schemas and tool_use blocks entirely. Empty for
	// records written before this was added, or when the call never reached HTTP.
	RawRequest  string `json:"raw_request,omitempty"`
	RawResponse string `json:"raw_response,omitempty"`
}

const llmRecordsSchema = `
CREATE TABLE IF NOT EXISTS llm_records (
    id            BIGSERIAL PRIMARY KEY,
    ts            TIMESTAMPTZ DEFAULT now(),
    model         TEXT,
    profile_name  TEXT,
    session_id    TEXT,
    task_id       TEXT,
    worker        TEXT,
    latency_ms    INTEGER,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cache_read    INTEGER,
    cache_write   INTEGER,
    status        TEXT,
    error         TEXT,
    request_body  TEXT,
    response_body TEXT,
    raw_request   TEXT,
    raw_response  TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_records_ts ON llm_records(ts);
CREATE INDEX IF NOT EXISTS idx_llm_records_session ON llm_records(session_id);
`

// llmRecordsMigrate adds new columns to existing tables.
const llmRecordsMigrate = `
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS task_id TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS worker TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS profile_name TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_request TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_response TEXT;
`

// EnsureLLMRecordsTable creates the llm_records table if it does not exist.
func (d *DB) EnsureLLMRecordsTable() error {
	if _, err := d.Exec(llmRecordsSchema); err != nil {
		return err
	}
	_, err := d.Exec(llmRecordsMigrate)
	return err
}

// InsertLLMRecord stores one LLM call record.
func (d *DB) InsertLLMRecord(r *LLMRecord) error {
	_, err := d.Exec(`
INSERT INTO llm_records(model, profile_name, session_id, task_id, worker, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status, error, request_body, response_body, raw_request, raw_response)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.Model, nullIfEmpty(r.ProfileName), r.SessionID, nullIfEmpty(r.TaskID), nullIfEmpty(r.Worker),
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite,
		r.Status, nullIfEmpty(r.Error), nullIfEmpty(r.RequestBody), nullIfEmpty(r.ResponseBody),
		nullIfEmpty(r.RawRequest), nullIfEmpty(r.RawResponse))
	return err
}

// ListLLMRecords returns paginated LLM records with optional filters.
func (d *DB) ListLLMRecords(model, session, task string, page, size int) ([]LLMRecord, int, error) {
	if size <= 0 {
		size = 50
	}
	if page < 0 {
		page = 0
	}
	offset := page * size

	where := `WHERE true`
	args := []any{}
	argN := 1
	if model != "" {
		where += fmt.Sprintf(` AND model = $%d`, argN)
		args = append(args, model)
		argN++
	}
	if session != "" {
		where += fmt.Sprintf(` AND session_id ILIKE $%d`, argN)
		args = append(args, "%"+session+"%")
		argN++
	}
	if task != "" {
		where += fmt.Sprintf(` AND COALESCE(task_id,'') = $%d`, argN)
		args = append(args, task)
		argN++
	}

	var total int
	if err := d.QueryRow(`SELECT COUNT(*) FROM llm_records `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQ := `SELECT id, ts, COALESCE(model,''), COALESCE(profile_name,''), COALESCE(session_id,''), COALESCE(task_id,''), COALESCE(worker,''),
		COALESCE(latency_ms,0), COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cache_read,0), COALESCE(cache_write,0),
		COALESCE(status,''), COALESCE(error,'')
		FROM llm_records ` + where + ` ORDER BY id DESC LIMIT $` + fmt.Sprintf("%d", argN) + ` OFFSET $` + fmt.Sprintf("%d", argN+1)
	args = append(args, size, offset)

	rows, err := d.Query(dataQ, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := []LLMRecord{}
	for rows.Next() {
		var r LLMRecord
		if err := rows.Scan(&r.ID, &r.Ts, &r.Model, &r.ProfileName, &r.SessionID, &r.TaskID, &r.Worker, &r.LatencyMs,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Status, &r.Error); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ModelTokenStat is one model's aggregated token usage for a task, summed from the
// llm_usage metering ledger (see db/llm_usage.go). Calls is the number of LLM calls
// that hit this model.
type ModelTokenStat struct {
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// LLMTask is one distinct task with its LLM-record count.
type LLMTask struct {
	TaskID string `json:"task_id"`
	Count  int    `json:"count"`
}

// LLMTasks returns distinct non-empty task_ids with record counts, most recent
// first — powers the LLM-records page's task picker.
func (d *DB) LLMTasks() ([]LLMTask, error) {
	rows, err := d.Query(`SELECT task_id, COUNT(*) AS n FROM llm_records
WHERE COALESCE(task_id,'') <> '' GROUP BY task_id ORDER BY MAX(id) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMTask
	for rows.Next() {
		var t LLMTask
		if err := rows.Scan(&t.TaskID, &t.Count); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteLLMRecords removes every LLM record for one exact task_id — the same
// match the page's task picker/filter uses. Returns rows deleted.
func (d *DB) DeleteLLMRecords(task string) (int64, error) {
	res, err := d.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'') = $1`, task)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetLLMRecord returns a single LLM record with full request/response bodies.
func (d *DB) GetLLMRecord(id int64) (*LLMRecord, error) {
	var r LLMRecord
	var reqBody, respBody, rawReq, rawResp sql.NullString
	err := d.QueryRow(`SELECT id, ts, COALESCE(model,''), COALESCE(profile_name,''), COALESCE(session_id,''), COALESCE(task_id,''), COALESCE(worker,''),
		COALESCE(latency_ms,0), COALESCE(input_tokens,0), COALESCE(output_tokens,0), COALESCE(cache_read,0), COALESCE(cache_write,0),
		COALESCE(status,''), COALESCE(error,''), request_body, response_body, raw_request, raw_response
		FROM llm_records WHERE id=$1`, id).
		Scan(&r.ID, &r.Ts, &r.Model, &r.ProfileName, &r.SessionID, &r.TaskID, &r.Worker, &r.LatencyMs,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheWrite, &r.Status, &r.Error,
			&reqBody, &respBody, &rawReq, &rawResp)
	if err != nil {
		return nil, err
	}
	r.RequestBody = reqBody.String
	r.ResponseBody = respBody.String
	r.RawRequest = rawReq.String
	r.RawResponse = rawResp.String
	return &r, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
