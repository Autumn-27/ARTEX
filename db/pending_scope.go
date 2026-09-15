package db

import (
	"database/sql"
	"time"
)

// PendingScope 是一条【未授权网段】登记(期 4):内网被动侦察发现当前 task_scope
// 之外的新网段时按 /24 聚合登记(见 server.reconRegisterPendingScope),人工批准
// (approved)后才写进 task_scope 扩范围;拒绝(dismissed)后同一条目不再累计
// hits,防同一网段反复侦察造成的重复骚扰。
type PendingScope struct {
	ID        int64      `json:"id"`
	TaskID    int64      `json:"task_id"`
	Kind      string     `json:"kind"`   // 目前只有 cidr(按 /24 聚合)
	Value     string     `json:"value"`  // 网段,如 10.0.3.0/24
	Status    string     `json:"status"` // pending|approved|dismissed
	Hits      int        `json:"hits"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

const pendingScopeCols = `id, task_id, kind, value, status, hits, first_seen, last_seen, decided_at`

func scanPendingScope(row interface{ Scan(...any) error }) (PendingScope, error) {
	var p PendingScope
	err := row.Scan(&p.ID, &p.TaskID, &p.Kind, &p.Value, &p.Status, &p.Hits,
		&p.FirstSeen, &p.LastSeen, &p.DecidedAt)
	return p, err
}

// UpsertPendingScope 幂等登记一条待授权网段:新插入返回 created=true;已存在时仅当
// 仍 pending 才 hits+1、刷新 last_seen(created=false);已 approved/dismissed 的
// 条目不动(防重复骚扰)。taskID<=0 或 kind/value 为空 → no-op。
func (s *AssetStore) UpsertPendingScope(taskID int64, kind, value string) (bool, error) {
	if taskID <= 0 || kind == "" || value == "" {
		return false, nil
	}
	var id int64
	err := s.db.QueryRow(`
INSERT INTO pending_scope(task_id, kind, value) VALUES ($1, $2, $3)
ON CONFLICT (task_id, kind, value) DO NOTHING
RETURNING id`, taskID, kind, value).Scan(&id)
	if err == nil {
		return true, nil
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	if _, err := s.db.Exec(`
UPDATE pending_scope SET hits = hits + 1, last_seen = now()
WHERE task_id=$1 AND kind=$2 AND value=$3 AND status='pending'`, taskID, kind, value); err != nil {
		return false, err
	}
	return false, nil
}

// ListPendingScope 列出一个任务的待授权登记;includeDecided=false 只给 pending。
func (s *AssetStore) ListPendingScope(taskID int64, includeDecided bool) ([]PendingScope, error) {
	query := `SELECT ` + pendingScopeCols + ` FROM pending_scope WHERE task_id=$1`
	if !includeDecided {
		query += ` AND status='pending'`
	}
	query += ` ORDER BY id`
	rows, err := s.db.Query(query, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PendingScope{}
	for rows.Next() {
		p, err := scanPendingScope(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPendingScope 单条读取;不存在返回 sql.ErrNoRows。
func (s *AssetStore) GetPendingScope(id int64) (PendingScope, error) {
	return scanPendingScope(s.db.QueryRow(
		`SELECT `+pendingScopeCols+` FROM pending_scope WHERE id=$1`, id))
}

// DecidePendingScope 处置一条登记:approve→approved,否则 dismissed,写 decided_at。
// 只迁移 status='pending' 的行——重复或冲突的决定不改变原状,返回条目现状(调用方
// 比对 Status 即可识别冲突,见 server.pendingScopeDecide)。
func (s *AssetStore) DecidePendingScope(id int64, approve bool) (PendingScope, error) {
	status := "dismissed"
	if approve {
		status = "approved"
	}
	if _, err := s.db.Exec(`
UPDATE pending_scope SET status=$2, decided_at=now() WHERE id=$1 AND status='pending'`,
		id, status); err != nil {
		return PendingScope{}, err
	}
	return s.GetPendingScope(id)
}
