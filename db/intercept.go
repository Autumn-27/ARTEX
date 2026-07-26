package db

import (
	"database/sql"
	"encoding/json"
	"time"
)

// InterceptRule is one row of intercept_rules.
type InterceptRule struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Priority       int       `json:"priority"`
	MatchTarget    string    `json:"match_target"`
	MatchType      string    `json:"match_type"`
	Pattern        string    `json:"pattern"`
	Action         string    `json:"action"`
	Message        string    `json:"message"`
	TimeoutEnabled bool      `json:"timeout_enabled"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	TimeoutAction  string    `json:"timeout_action"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// InterceptPending is one row of intercept_pending.
type InterceptPending struct {
	ID             int64           `json:"id"`
	RuleID         *int64          `json:"rule_id"`
	ConversationID *int64          `json:"conversation_id"`
	TaskID         *string         `json:"task_id"`
	AgentName      string          `json:"agent_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Status         string          `json:"status"`
	DecidedAt      *time.Time      `json:"decided_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

const interceptRuleCols = `id, name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, created_at, updated_at`

func scanInterceptRule(row interface{ Scan(...any) error }) (InterceptRule, error) {
	var r InterceptRule
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority,
		&r.MatchTarget, &r.MatchType, &r.Pattern, &r.Action, &r.Message,
		&r.TimeoutEnabled, &r.TimeoutSeconds, &r.TimeoutAction,
		&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// ListInterceptRules returns all rules ordered by priority DESC then id.
func (d *DB) ListInterceptRules() ([]InterceptRule, error) {
	rows, err := d.Query(`SELECT ` + interceptRuleCols + ` FROM intercept_rules ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptRule
	for rows.Next() {
		r, err := scanInterceptRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateInterceptRule inserts a new rule.
func (d *DB) CreateInterceptRule(name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING `+interceptRuleCols,
		name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// UpdateInterceptRule replaces all editable fields of an existing rule.
func (d *DB) UpdateInterceptRule(id int64, name, matchTarget, matchType, pattern, action, message string, priority int, enabled bool, timeoutEnabled bool, timeoutSeconds int, timeoutAction string) (InterceptRule, error) {
	row := d.QueryRow(`
UPDATE intercept_rules
   SET name=$2, enabled=$3, priority=$4, match_target=$5,
       match_type=$6, pattern=$7, action=$8, message=$9,
       timeout_enabled=$10, timeout_seconds=$11, timeout_action=$12
WHERE id=$1
RETURNING `+interceptRuleCols,
		id, name, enabled, priority, matchTarget, matchType, pattern, action, message, timeoutEnabled, timeoutSeconds, timeoutAction)
	return scanInterceptRule(row)
}

// DeleteInterceptRule removes a rule.
func (d *DB) DeleteInterceptRule(id int64) error {
	_, err := d.Exec(`DELETE FROM intercept_rules WHERE id=$1`, id)
	return err
}

// ToggleInterceptRule flips the enabled state of a rule.
func (d *DB) ToggleInterceptRule(id int64, enabled bool) error {
	_, err := d.Exec(`UPDATE intercept_rules SET enabled=$2 WHERE id=$1`, id, enabled)
	return err
}

// CreateInterceptPending inserts a pending approval record and returns its ID.
// convID == 0 → conversation_id stored as NULL (background task).
// taskID == "" → task_id stored as NULL.
func (d *DB) CreateInterceptPending(ruleID, convID int64, taskID, agentName, toolName string, input []byte) (int64, error) {
	raw := json.RawMessage(input)
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var convIDPtr *int64
	if convID != 0 {
		convIDPtr = &convID
	}
	var taskIDPtr *string
	if taskID != "" {
		taskIDPtr = &taskID
	}
	var id int64
	err := d.QueryRow(`
INSERT INTO intercept_pending(rule_id, conversation_id, task_id, agent_name, tool_name, tool_input)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		ruleID, convIDPtr, taskIDPtr, agentName, toolName, raw).Scan(&id)
	return id, err
}

// DecideInterceptPending updates a pending record's status (allowed/denied/timeout).
func (d *DB) DecideInterceptPending(id int64, status string) error {
	_, err := d.Exec(`UPDATE intercept_pending SET status=$2, decided_at=NOW() WHERE id=$1`, id, status)
	return err
}

const interceptPendingCols = `id, rule_id, conversation_id, task_id, agent_name, tool_name, tool_input, status, decided_at, created_at`

func scanInterceptPending(s interface{ Scan(...any) error }, p *InterceptPending) error {
	return s.Scan(&p.ID, &p.RuleID, &p.ConversationID, &p.TaskID, &p.AgentName,
		&p.ToolName, &p.ToolInput, &p.Status, &p.DecidedAt, &p.CreatedAt)
}

// ListPendingIntercepts returns all unresolved approval requests, newest first.
func (d *DB) ListPendingIntercepts() ([]InterceptPending, error) {
	rows, err := d.Query(`SELECT ` + interceptPendingCols + ` FROM intercept_pending WHERE status='pending' ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptPending
	for rows.Next() {
		var p InterceptPending
		if err := scanInterceptPending(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetInterceptPending returns one pending record (nil if absent).
func (d *DB) GetInterceptPending(id int64) (*InterceptPending, error) {
	var p InterceptPending
	err := scanInterceptPending(
		d.QueryRow(`SELECT `+interceptPendingCols+` FROM intercept_pending WHERE id=$1`, id),
		&p,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &p, err
}

// InterceptApprovalRow is intercept_pending enriched with conversation and rule info.
type InterceptApprovalRow struct {
	InterceptPending
	ConvTitle    string `json:"conv_title"`
	ConvAgentKey string `json:"conv_agent_key"`
	RuleName     string `json:"rule_name"`
}

func scanInterceptApprovalRow(rows interface{ Scan(...any) error }, r *InterceptApprovalRow) error {
	return rows.Scan(
		&r.ID, &r.RuleID, &r.ConversationID, &r.TaskID, &r.AgentName,
		&r.ToolName, &r.ToolInput, &r.Status, &r.DecidedAt, &r.CreatedAt,
		&r.ConvTitle, &r.ConvAgentKey, &r.RuleName,
	)
}

const approvalRowSelect = `
SELECT ip.id, ip.rule_id, ip.conversation_id, ip.task_id, ip.agent_name,
       ip.tool_name, ip.tool_input, ip.status, ip.decided_at, ip.created_at,
       COALESCE(c.title,'') AS conv_title,
       COALESCE(c.agent_key,'') AS conv_agent_key,
       COALESCE(ir.name,'') AS rule_name
FROM intercept_pending ip
LEFT JOIN conversations c ON c.id = ip.conversation_id
LEFT JOIN intercept_rules ir ON ir.id = ip.rule_id`

// ListAllIntercepts returns up to limit intercept_pending rows (newest first)
// joined with conversation and rule info.
func (d *DB) ListAllIntercepts(limit int) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` ORDER BY ip.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListTaskIntercepts returns all intercept_pending rows for a specific task (newest first).
func (d *DB) ListTaskIntercepts(taskID string) ([]InterceptApprovalRow, error) {
	rows, err := d.Query(approvalRowSelect+` WHERE ip.task_id=$1 ORDER BY ip.created_at DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InterceptApprovalRow
	for rows.Next() {
		var r InterceptApprovalRow
		if err := scanInterceptApprovalRow(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
