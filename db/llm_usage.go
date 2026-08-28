package db

import (
	"sort"
	"strconv"
	"time"
)

// LLMUsage is one lightweight LLM-call metering row — the always-on usage ledger,
// distinct from llm_records (which stores full request/response bodies and is a
// gated debug feature). One row per completion call, written on both success and
// error, so token accounting is complete even for interrupted/failed runs. Carries
// only the dimensions needed to slice token spend (model / profile / task / agent),
// never any prompt or response content.
type LLMUsage struct {
	TaskID        string `json:"task_id"`        // task registry id (matches llm_records.task_id)
	ExplorationID int64  `json:"exploration_id"` // exploration id parsed from the session (0 = unknown/non-task)
	Worker        string `json:"worker"`         // agent lane: worker / planner / mainagent / goals
	Model         string `json:"model"`
	ProfileName   string `json:"profile_name"`
	LatencyMs     int    `json:"latency_ms"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	CacheRead     int    `json:"cache_read"`
	CacheWrite    int    `json:"cache_write"`
	Status        string `json:"status"` // ok | error
}

const llmUsageSchema = `
CREATE TABLE IF NOT EXISTS llm_usage (
    id             BIGSERIAL PRIMARY KEY,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    task_id        TEXT,
    exploration_id BIGINT,
    worker         TEXT,
    model          TEXT,
    profile_name   TEXT,
    latency_ms     INTEGER,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_read     INTEGER NOT NULL DEFAULT 0,
    cache_write    INTEGER NOT NULL DEFAULT 0,
    status         TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_usage_task  ON llm_usage(task_id);
CREATE INDEX IF NOT EXISTS idx_llm_usage_model ON llm_usage(task_id, model);
CREATE INDEX IF NOT EXISTS idx_llm_usage_exp   ON llm_usage(exploration_id);
`

// EnsureLLMUsageTable creates the llm_usage metering table if it does not exist.
func (d *DB) EnsureLLMUsageTable() error {
	_, err := d.Exec(llmUsageSchema)
	return err
}

// InsertLLMUsage appends one metering row. Best-effort: callers log and continue on
// error (a lost metering row must never break the LLM call).
func (d *DB) InsertLLMUsage(u *LLMUsage) error {
	var expID any
	if u.ExplorationID > 0 {
		expID = u.ExplorationID
	}
	_, err := d.Exec(`
INSERT INTO llm_usage(task_id, exploration_id, worker, model, profile_name, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status)
VALUES (NULLIF($1,''),$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,$11)`,
		u.TaskID, expID, u.Worker, u.Model, u.ProfileName,
		u.LatencyMs, u.InputTokens, u.OutputTokens, u.CacheRead, u.CacheWrite, u.Status)
	return err
}

// TokenByModel aggregates a task's LLM token usage grouped by model, most-used
// first, from the always-on llm_usage ledger. taskID is the task registry id.
// Accurate even with per-agent model bindings, pool rotation/failover, and
// interrupted runs, since every call (success or error) is metered.
func (d *DB) TokenByModel(taskID string) ([]ModelTokenStat, error) {
	rows, err := d.Query(`
SELECT COALESCE(NULLIF(model,''),'(unknown)') AS model, COUNT(*) AS calls,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
WHERE COALESCE(task_id,'') = $1
GROUP BY model
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC, model`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelTokenStat{}
	for rows.Next() {
		var m ModelTokenStat
		if err := rows.Scan(&m.Model, &m.Calls, &m.InputTokens, &m.OutputTokens,
			&m.CacheReadTokens, &m.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ProfileUsage aggregates the whole ledger's token spend for one LLM profile
// (global, all tasks). Powers the dashboard's per-profile token card (new source).
type ProfileUsage struct {
	ProfileName      string `json:"profile_name"`
	Calls            int    `json:"calls"`
	Tasks            int    `json:"tasks"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// UsageByProfile returns global token spend grouped by profile name, most-used
// first. profile_name may be empty for calls made on env/non-persisted configs.
func (d *DB) UsageByProfile() ([]ProfileUsage, error) {
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name, COUNT(*) AS calls,
       COUNT(DISTINCT task_id) AS tasks,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
       COALESCE(SUM(cache_read),0), COALESCE(SUM(cache_write),0)
FROM llm_usage
GROUP BY profile_name
ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileUsage{}
	for rows.Next() {
		var p ProfileUsage
		if err := rows.Scan(&p.ProfileName, &p.Calls, &p.Tasks,
			&p.InputTokens, &p.OutputTokens, &p.CacheReadTokens, &p.CacheWriteTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	byName := make(map[string]ProfileUsage, len(out))
	for _, current := range out {
		byName[current.ProfileName] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenProfiles {
			current := byName[cold.ProfileName]
			current.ProfileName = cold.ProfileName
			current.Calls += cold.Calls
			current.Tasks += cold.Tasks
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			current.CacheWriteTokens += cold.CacheWriteTokens
			byName[cold.ProfileName] = current
		}
	}
	out = out[:0]
	for _, current := range byName {
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].InputTokens + out[i].OutputTokens
		right := out[j].InputTokens + out[j].OutputTokens
		if left != right {
			return left > right
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// ProfileDayUsage is one (profile, UTC calendar day) token bucket for the daily
// chart. Unlike the activity-based chart, ts is the real call time, so this is
// actual per-day consumption rather than tokens bucketed by task creation date.
type ProfileDayUsage struct {
	ProfileName     string `json:"profile_name"`
	Date            string `json:"date"` // YYYY-MM-DD (UTC)
	InputTokens     int    `json:"input_tokens"`
	OutputTokens    int    `json:"output_tokens"`
	CacheReadTokens int    `json:"cache_read_tokens"`
}

// UsageDaily returns per-(profile, day) token buckets for the past `days` days
// (default 365 when days<=0), so the dashboard can slice by profile + range.
func (d *DB) UsageDaily(days int) ([]ProfileDayUsage, error) {
	if days <= 0 {
		days = 365
	}
	rows, err := d.Query(`
SELECT COALESCE(profile_name,'') AS profile_name,
       to_char(ts AT TIME ZONE 'UTC', 'YYYY-MM-DD') AS day,
       COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(cache_read),0)
FROM llm_usage
WHERE ts >= now() - ($1 * interval '1 day')
GROUP BY profile_name, day
ORDER BY day`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProfileDayUsage{}
	for rows.Next() {
		var p ProfileDayUsage
		if err := rows.Scan(&p.ProfileName, &p.Date, &p.InputTokens, &p.OutputTokens, &p.CacheReadTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived, err := d.archivedTaskAggregates()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	byKey := make(map[string]ProfileDayUsage, len(out))
	for _, current := range out {
		byKey[current.ProfileName+"\x00"+current.Date] = current
	}
	for _, aggregate := range archived {
		for _, cold := range aggregate.TokenDaily {
			if cold.Date < cutoff {
				continue
			}
			key := cold.ProfileName + "\x00" + cold.Date
			current := byKey[key]
			current.ProfileName = cold.ProfileName
			current.Date = cold.Date
			current.InputTokens += cold.InputTokens
			current.OutputTokens += cold.OutputTokens
			current.CacheReadTokens += cold.CacheReadTokens
			byKey[key] = current
		}
	}
	out = out[:0]
	for _, current := range byKey {
		out = append(out, current)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ProfileName < out[j].ProfileName
	})
	return out, nil
}

// ParseExpID turns the exploration-id segment parsed from a session string into an
// int64 (0 when empty/non-numeric, e.g. chat sessions keyed by conversation id).
func ParseExpID(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
