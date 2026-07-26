package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// AgentTrigger is one P3 trigger attached to a custom agent. Four trigger
// conditions can be on at once: interval / on_finding / on_goal_met / on_task_timeout.
type AgentTrigger struct {
	ID                 int64      `json:"id"`
	AgentKey           string     `json:"agent_key"`
	Enabled            bool       `json:"enabled"`
	IntervalSec        int        `json:"interval_sec"`
	OnFinding          bool       `json:"on_finding"`
	OnGoalMet          bool       `json:"on_goal_met"`
	OnTaskTimeout      bool       `json:"on_task_timeout"`
	IntervalMessage    string     `json:"interval_message"` // 各触发条件的独立用户消息
	FindingMessage     string     `json:"finding_message"`
	GoalMessage        string     `json:"goal_message"`
	TaskTimeoutMessage string     `json:"task_timeout_message"`
	LastFire           *time.Time `json:"last_fire,omitempty"`
}

const triggerCols = `id, agent_key, enabled, interval_sec, on_finding, on_goal_met, on_task_timeout, interval_message, finding_message, goal_message, task_timeout_message, last_fire`

func scanTrigger(sc interface{ Scan(...any) error }) (*AgentTrigger, error) {
	var t AgentTrigger
	var lf sql.NullTime
	if err := sc.Scan(&t.ID, &t.AgentKey, &t.Enabled, &t.IntervalSec, &t.OnFinding, &t.OnGoalMet, &t.OnTaskTimeout,
		&t.IntervalMessage, &t.FindingMessage, &t.GoalMessage, &t.TaskTimeoutMessage, &lf); err != nil {
		return nil, err
	}
	if lf.Valid {
		t.LastFire = &lf.Time
	}
	return &t, nil
}

// CreateTrigger inserts a trigger for agentKey and returns it.
func (d *DB) CreateTrigger(t *AgentTrigger) (*AgentTrigger, error) {
	row := d.QueryRow(`
INSERT INTO agent_triggers(agent_key, enabled, interval_sec, on_finding, on_goal_met, on_task_timeout, interval_message, finding_message, goal_message, task_timeout_message)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+triggerCols,
		t.AgentKey, t.Enabled, t.IntervalSec, t.OnFinding, t.OnGoalMet, t.OnTaskTimeout,
		t.IntervalMessage, t.FindingMessage, t.GoalMessage, t.TaskTimeoutMessage)
	return scanTrigger(row)
}

// UpdateTrigger updates a trigger's fields (not last_fire).
func (d *DB) UpdateTrigger(t *AgentTrigger) error {
	_, err := d.Exec(`UPDATE agent_triggers SET enabled=$2, interval_sec=$3, on_finding=$4, on_goal_met=$5, on_task_timeout=$6, interval_message=$7, finding_message=$8, goal_message=$9, task_timeout_message=$10 WHERE id=$1`,
		t.ID, t.Enabled, t.IntervalSec, t.OnFinding, t.OnGoalMet, t.OnTaskTimeout,
		t.IntervalMessage, t.FindingMessage, t.GoalMessage, t.TaskTimeoutMessage)
	return err
}

// DeleteTrigger removes a trigger.
func (d *DB) DeleteTrigger(id int64) error {
	_, err := d.Exec(`DELETE FROM agent_triggers WHERE id=$1`, id)
	return err
}

// ListTriggersFor returns an agent's triggers.
func (d *DB) ListTriggersFor(agentKey string) ([]*AgentTrigger, error) {
	return d.queryTriggers(`SELECT `+triggerCols+` FROM agent_triggers WHERE agent_key=$1 ORDER BY id`, agentKey)
}

// ListEnabledTriggers returns all enabled triggers (for the scheduler).
func (d *DB) ListEnabledTriggers() ([]*AgentTrigger, error) {
	return d.queryTriggers(`SELECT ` + triggerCols + ` FROM agent_triggers WHERE enabled ORDER BY id`)
}

func (d *DB) queryTriggers(q string, args ...any) ([]*AgentTrigger, error) {
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AgentTrigger{}
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchTriggerFire records an interval trigger's fire time (now).
func (d *DB) TouchTriggerFire(id int64) error {
	_, err := d.Exec(`UPDATE agent_triggers SET last_fire=now() WHERE id=$1`, id)
	return err
}

// DeleteTriggersForAgent removes all triggers of an agent (custom agent delete).
func (d *DB) DeleteTriggersForAgent(agentKey string) error {
	_, err := d.Exec(`DELETE FROM agent_triggers WHERE agent_key=$1`, agentKey)
	return err
}

// ---------- scheduler_state (kv watermarks) ----------

func (d *DB) GetSchedState(key string) (string, error) {
	var v string
	err := d.QueryRow(`SELECT value FROM scheduler_state WHERE key=$1`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (d *DB) SetSchedState(key, value string) error {
	_, err := d.Exec(`INSERT INTO scheduler_state(key,value) VALUES ($1,$2)
ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, key, value)
	return err
}

// ---------- event queries (cross-exploration, for the scheduler) ----------

// TaskEvent is a finding/goal event carrying the owning task's info, used to
// compose the trigger message context.
type TaskEvent struct {
	NodeID    int64  `json:"node_id"`
	TaskID    int64  `json:"task_id"`
	TaskDesc  string `json:"task_description"`
	TaskGoal  string `json:"task_goal"`
	Summary   string `json:"summary"`   // finding summary / goal text
	VulnClass string `json:"vulnclass"` // finding only
	Severity  string `json:"severity"`  // finding only
}

// NewFindingsSince returns findings with node id > lastID across all live tasks,
// ordered by id (monotonic watermark → no double-fire).
func (d *DB) NewFindingsSince(lastID int64) ([]TaskEvent, error) {
	rows, err := d.Query(`
SELECT n.id, t.id, t.description, t.goal, n.payload
FROM exploration_nodes n JOIN tasks t ON t.exploration_id = n.exploration_id
WHERE n.kind='finding' AND n.id > $1 AND t.deleted_at IS NULL
ORDER BY n.id`, lastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskEvent{}
	for rows.Next() {
		var e TaskEvent
		var payload []byte
		if err := rows.Scan(&e.NodeID, &e.TaskID, &e.TaskDesc, &e.TaskGoal, &payload); err != nil {
			return nil, err
		}
		var p struct{ Summary, Vulnclass, Severity string }
		_ = json.Unmarshal(payload, &p)
		e.Summary, e.VulnClass, e.Severity = p.Summary, p.Vulnclass, p.Severity
		out = append(out, e)
	}
	return out, rows.Err()
}

// TimedOutTasksSince returns tasks that reached status='timeout' with id > lastID,
// ordered by id (monotonic watermark → no double-fire across restarts).
func (d *DB) TimedOutTasksSince(lastID int64) ([]TaskEvent, error) {
	rows, err := d.Query(`
SELECT id, description, goal FROM tasks
WHERE status='timeout' AND deleted_at IS NULL AND id > $1
ORDER BY id`, lastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskEvent{}
	for rows.Next() {
		var e TaskEvent
		if err := rows.Scan(&e.NodeID, &e.TaskDesc, &e.TaskGoal); err != nil {
			return nil, err
		}
		e.TaskID = e.NodeID // task id doubles as NodeID for the watermark
		e.Summary = e.TaskGoal
		out = append(out, e)
	}
	return out, rows.Err()
}

// MetGoals returns all met goals across live tasks (the scheduler filters out the
// ones it already fired for via the persisted fired-set).
func (d *DB) MetGoals() ([]TaskEvent, error) {
	rows, err := d.Query(`
SELECT n.id, t.id, t.description, t.goal, n.payload
FROM exploration_nodes n JOIN tasks t ON t.exploration_id = n.exploration_id
WHERE n.kind='goal' AND n.state='met' AND t.deleted_at IS NULL
ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TaskEvent{}
	for rows.Next() {
		var e TaskEvent
		var payload []byte
		if err := rows.Scan(&e.NodeID, &e.TaskID, &e.TaskDesc, &e.TaskGoal, &payload); err != nil {
			return nil, err
		}
		var p struct{ Text, Summary string }
		_ = json.Unmarshal(payload, &p)
		if p.Text != "" {
			e.Summary = p.Text
		} else {
			e.Summary = p.Summary
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
