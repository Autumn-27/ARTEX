package db

import (
	"encoding/json"
	"time"
)

// Task is a row in the task registry (1:1 with an exploration).
type Task struct {
	ID            int64      `json:"id"`
	Description   string     `json:"description"`
	Goal          string     `json:"goal"`
	ExplorationID int64      `json:"exploration_id"`
	Status        string     `json:"status"`
	Paused        bool       `json:"paused"`
	Queued        bool       `json:"queued"` // 因并发上限被挂起、等待空位自动启动;true=尚未开跑
	LLMProfileID  *int64     `json:"llm_profile_id,omitempty"`
	ParentRef     string     `json:"parent_ref,omitempty"` // 父任务 id(编排 spawn 记录;空=顶层)
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"` // 进入终态(done/failed/timeout)的时刻;非终态为 nil
	// 任务级超时(见 docs/任务级超时与收尾设计.md)。
	TimeoutSeconds int        `json:"timeout_seconds"`        // 0=不限时
	FirstRunAt     *time.Time `json:"first_run_at,omitempty"` // 首次真正开始运行的时刻(非 created_at);nil=尚未运行
	DeadlineAt     *time.Time `json:"deadline_at,omitempty"`  // = first_run_at + timeout_seconds;nil=不限或未运行
	// planner 心跳触发间隔(秒)：距上轮 plan 结束/任务开始满该值且期间无触发 → 触发一轮。
	// 下限=默认=300(5min)，低于一律抬到 300(在 CreateTask 归一)。见 docs/planner-trigger-impl-plan.md
	PlanHeartbeatSeconds int `json:"plan_heartbeat_seconds"`
}

// IsTerminal reports whether a task status is a terminal (finished) state.
// 单一真源，替换散落各处的 done/failed 硬编码判定。
func IsTerminal(status string) bool {
	return status == "done" || status == "failed" || status == "timeout"
}

// CreateTask creates an exploration + task in one transaction and returns the task.
// timeoutSeconds is the task-level wall-clock budget (0 = 不限时); deadline_at is
// stamped later at first real run (see engine), not here.
// MinPlanHeartbeatSeconds 是 planner 心跳间隔的下限 = 默认 = 10min。
// 低于它(含缺省 0 / 负值 / 误配的小值)一律抬到 10min，防止把 planner 打爆。
const MinPlanHeartbeatSeconds = 600

func normalizeHeartbeat(sec int) int {
	if sec < MinPlanHeartbeatSeconds {
		return MinPlanHeartbeatSeconds
	}
	return sec
}

func (d *DB) CreateTask(description, goal string, llmProfileID *int64, timeoutSeconds, planHeartbeatSeconds int) (*Task, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var expID int64
	if err := tx.QueryRow(`INSERT INTO explorations(description, goal) VALUES ($1,$2) RETURNING id`, description, goal).Scan(&expID); err != nil {
		return nil, err
	}
	// origin fact: the exploration graph's root, a KindFact node (state='origin')
	// holding the original task description. Goals/intents/findings descend from
	// it, and the seeded target asset is anchored to it as lineage (the asset graph
	// is global and shared, not isolated per task). Being a fact (not a special 'begin' kind) lets every
	// intent uniformly connect to a fact node, including the first ones.
	originPayload, _ := json.Marshal(map[string]any{
		"summary":     "任务起点：" + description + "；目标：" + goal,
		"description": description,
		"goal":        goal,
	})
	if _, err := tx.Exec(`
INSERT INTO exploration_nodes(exploration_id, kind, payload, priority, state, origin)
VALUES ($1, 'fact', $2, 0, 'origin', 'system')`, expID, string(originPayload)); err != nil {
		return nil, err
	}
	if timeoutSeconds < 0 {
		timeoutSeconds = 0
	}
	planHeartbeatSeconds = normalizeHeartbeat(planHeartbeatSeconds) // <=0→300，<30→30
	t := &Task{Description: description, Goal: goal, ExplorationID: expID, LLMProfileID: llmProfileID, TimeoutSeconds: timeoutSeconds, PlanHeartbeatSeconds: planHeartbeatSeconds}
	if err := tx.QueryRow(`
INSERT INTO tasks(description, goal, exploration_id, llm_profile_id, timeout_seconds, plan_heartbeat_seconds) VALUES ($1,$2,$3,$4,$5,$6)
RETURNING id, status, paused, created_at`, description, goal, expID, llmProfileID, timeoutSeconds, planHeartbeatSeconds).Scan(&t.ID, &t.Status, &t.Paused, &t.CreatedAt); err != nil {
		return nil, err
	}
	return t, tx.Commit()
}

const taskCols = `id, description, goal, exploration_id, status, paused, queued, llm_profile_id, COALESCE(parent_ref,''), created_at, completed_at, COALESCE(timeout_seconds,0), COALESCE(plan_heartbeat_seconds,300), first_run_at, deadline_at`

func scanTask(sc interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	if err := sc.Scan(&t.ID, &t.Description, &t.Goal, &t.ExplorationID, &t.Status, &t.Paused, &t.Queued, &t.LLMProfileID, &t.ParentRef, &t.CreatedAt, &t.CompletedAt, &t.TimeoutSeconds, &t.PlanHeartbeatSeconds, &t.FirstRunAt, &t.DeadlineAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// SetParentRef records a task's parent task id (编排 agent spawn_task 关联).
func (d *DB) SetParentRef(id int64, parentRef string) error {
	_, err := d.Exec(`UPDATE tasks SET parent_ref=NULLIF($2,'') WHERE id=$1`, id, parentRef)
	return err
}

// ListTasks returns alive tasks, newest first.
func (d *DB) ListTasks() ([]*Task, error) {
	rows, err := d.Query(`SELECT ` + taskCols + ` FROM tasks WHERE deleted_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTask returns one alive task (nil if not found/deleted).
func (d *DB) GetTask(id int64) (*Task, error) {
	t, err := scanTask(d.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=$1 AND deleted_at IS NULL`, id))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

// SetPaused persists a task's paused flag.
func (d *DB) SetPaused(id int64, paused bool) error {
	_, err := d.Exec(`UPDATE tasks SET paused=$1 WHERE id=$2`, paused, id)
	return err
}

// SetQueued persists a task's queued flag (并发上限挂起态). A queued task is held
// (engine not started) until the concurrency reconciler promotes it into a free slot.
func (d *DB) SetQueued(id int64, queued bool) error {
	_, err := d.Exec(`UPDATE tasks SET queued=$1 WHERE id=$2`, queued, id)
	return err
}

// SetStatus updates a task's lifecycle status. Entering a terminal state
// (done/failed/timeout) stamps completed_at once (COALESCE keeps the first stamp
// stable); moving back to a non-terminal state clears it, so a re-run has no stale
// finish time.
func (d *DB) SetStatus(id int64, status string) error {
	_, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = CASE WHEN $1 IN ('done','failed','timeout') THEN COALESCE(completed_at, now()) ELSE NULL END
 WHERE id = $2`, status, id)
	return err
}

// SetTerminalStatusGuarded sets a terminal status only when the task is NOT already
// terminal, so a completed↔timeout race resolves to the first writer (won=true).
// Returns won=false (no error) when another terminal status already stuck — the
// caller then leaves it alone. Non-terminal transitions / re-run still use SetStatus.
func (d *DB) SetTerminalStatusGuarded(id int64, status string) (won bool, err error) {
	res, err := d.Exec(`
UPDATE tasks
   SET status = $1,
       completed_at = COALESCE(completed_at, now())
 WHERE id = $2 AND status NOT IN ('done','failed','timeout')`, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// StampFirstRun records a task's first-real-run moment and computes its absolute
// deadline (= now + timeoutSeconds). Idempotent: only stamps when first_run_at is
// still NULL, so restarts / re-entries keep the original clock. timeoutSeconds<=0
// leaves deadline_at NULL (不限时). Returns the resulting deadline (nil = 不限/未变).
func (d *DB) StampFirstRun(id int64, timeoutSeconds int) (*time.Time, error) {
	var deadline *time.Time
	err := d.QueryRow(`
UPDATE tasks
   SET first_run_at = COALESCE(first_run_at, now()),
       deadline_at  = CASE
           WHEN first_run_at IS NOT NULL THEN deadline_at            -- 已盖过章：不动
           WHEN $2 > 0 THEN now() + make_interval(secs => $2)
           ELSE NULL END
 WHERE id = $1
 RETURNING deadline_at`, id, timeoutSeconds).Scan(&deadline)
	return deadline, err
}

// DeleteTask hard-deletes a task and its exploration subgraph (nodes/edges/
// activity via ON DELETE CASCADE). The task row is removed first so the
// ON DELETE RESTRICT on tasks.exploration_id does not block the exploration delete.
// Global assets are NOT touched.
func (d *DB) DeleteTask(id int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var expID int64
	if err := tx.QueryRow(`SELECT exploration_id FROM tasks WHERE id=$1`, id).Scan(&expID); err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil // already gone
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tasks WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM explorations WHERE id=$1`, expID); err != nil {
		return err
	}
	return tx.Commit()
}
