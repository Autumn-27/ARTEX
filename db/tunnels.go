// tunnels.go 是多层代理隧道台账的落库层（schema.sql §L，内网渗透期 3a,
// INTRANET-PIVOT-DESIGN.md §4.3/4.3a)。deploy_params 是完整部署参数 JSON
// （含 auth、server_pid、stage_token、远端路径、验证目标）——断链自动重拉全靠它
// (PivotHub 的教训：参数打散存列、不存会话引用，重拉名存实亡；这里必须存全）。
// deploy_params 含隧道 auth token，属平台信任域内数据，明文 JSONB 存储
// （与 sessions/credentials 的目标侧凭据不同：那是用户资产，这是平台自造的一次性令牌）。
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// 隧道状态机：deploying → alive → (error → 重拉 → alive)×N → stopped。
const (
	TunnelDeploying = "deploying"
	TunnelAlive     = "alive"
	TunnelError     = "error"
	TunnelStopped   = "stopped"
)

// TunnelRecord 是 tunnels 表的一行。DeployParams 是完整部署参数 JSON
// (tunnel.Plan 的序列化）。
type TunnelRecord struct {
	ID           int64           `json:"id"`
	TaskID       int64           `json:"task_id"`
	Kind         string          `json:"kind"` // socks | portfwd
	Adapter      string          `json:"adapter"`
	ListenHost   string          `json:"listen_host"`
	ListenPort   int             `json:"listen_port"`
	TargetHost   string          `json:"target_host,omitempty"`
	TargetPort   int             `json:"target_port,omitempty"`
	ViaSessionID int64           `json:"via_session_id"`
	RemotePID    int             `json:"remote_pid,omitempty"`
	DeployParams json.RawMessage `json:"deploy_params"`
	State        string          `json:"state"`
	LastCheck    time.Time       `json:"last_check"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

// TunnelStore 是 tunnels 表的访问层。
type TunnelStore struct {
	d *DB
}

// NewTunnelStore 构造访问层。
func NewTunnelStore(d *DB) *TunnelStore {
	return &TunnelStore{d: d}
}

// Create 插入一条隧道（初始 state 通常为 deploying）并返回主键。
func (s *TunnelStore) Create(ctx context.Context, rec *TunnelRecord) (int64, error) {
	if rec.Kind != "socks" && rec.Kind != "portfwd" {
		return 0, fmt.Errorf("非法隧道类型 %q（socks|portfwd)", rec.Kind)
	}
	adapter := rec.Adapter
	if adapter == "" {
		adapter = "chisel"
	}
	state := rec.State
	if state == "" {
		state = TunnelDeploying
	}
	params := rec.DeployParams
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	var id int64
	err := s.d.QueryRowContext(ctx, `
INSERT INTO tunnels(task_id, kind, adapter, listen_host, listen_port,
	target_host, target_port, via_session_id, remote_pid, deploy_params, state, error)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
RETURNING id`,
		rec.TaskID, rec.Kind, adapter, rec.ListenHost, rec.ListenPort,
		rec.TargetHost, rec.TargetPort, rec.ViaSessionID, rec.RemotePID,
		string(params), state, rec.Error).Scan(&id)
	return id, err
}

const tunnelCols = `id, task_id, kind, adapter, listen_host, listen_port,
	target_host, target_port, via_session_id, remote_pid, deploy_params,
	state, last_check, error, created_at`

func scanTunnel(row interface{ Scan(...any) error }) (*TunnelRecord, error) {
	var r TunnelRecord
	if err := row.Scan(&r.ID, &r.TaskID, &r.Kind, &r.Adapter, &r.ListenHost, &r.ListenPort,
		&r.TargetHost, &r.TargetPort, &r.ViaSessionID, &r.RemotePID, &r.DeployParams,
		&r.State, &r.LastCheck, &r.Error, &r.CreatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// Get 取一条；不存在返回 (nil, nil)。
func (s *TunnelStore) Get(ctx context.Context, id int64) (*TunnelRecord, error) {
	r, err := scanTunnel(s.d.QueryRowContext(ctx,
		`SELECT `+tunnelCols+` FROM tunnels WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// UpdateState 更新状态与错误信息，并刷新 last_check。
func (s *TunnelStore) UpdateState(ctx context.Context, id int64, state, errMsg string) error {
	switch state {
	case TunnelDeploying, TunnelAlive, TunnelError, TunnelStopped:
	default:
		return fmt.Errorf("非法隧道状态 %q", state)
	}
	_, err := s.d.ExecContext(ctx,
		`UPDATE tunnels SET state=$2, error=$3, last_check=now() WHERE id=$1`, id, state, errMsg)
	return err
}

// UpdateDeployParams 全量回写部署参数（部署推进时逐层补齐 pid/stage_token 等），
// 可同时回填 remote_pid 列。
func (s *TunnelStore) UpdateDeployParams(ctx context.Context, id int64, remotePID int, params json.RawMessage) error {
	if len(params) == 0 {
		params = json.RawMessage("{}")
	}
	_, err := s.d.ExecContext(ctx,
		`UPDATE tunnels SET deploy_params=$2, remote_pid=$3, last_check=now() WHERE id=$1`,
		id, string(params), remotePID)
	return err
}

// ListByTask 取某任务的全部隧道（按 id 升序）。
func (s *TunnelStore) ListByTask(ctx context.Context, taskID int64) ([]*TunnelRecord, error) {
	return s.list(ctx, ` WHERE task_id=$1`, taskID)
}

// ListAlive 取全部存活（alive）隧道——健康巡检与端口审计用。
func (s *TunnelStore) ListAlive(ctx context.Context) ([]*TunnelRecord, error) {
	return s.list(ctx, ` WHERE state='alive'`)
}

// ListActive 取全部未终结（deploying|alive|error）隧道。
func (s *TunnelStore) ListActive(ctx context.Context) ([]*TunnelRecord, error) {
	return s.list(ctx, ` WHERE state IN ('deploying','alive','error')`)
}

// List 取台账全部行（含 stopped)，按 id 升序——内网作战页台账视图用。
func (s *TunnelStore) List(ctx context.Context) ([]*TunnelRecord, error) {
	return s.list(ctx, ``)
}

func (s *TunnelStore) list(ctx context.Context, where string, args ...any) ([]*TunnelRecord, error) {
	rows, err := s.d.QueryContext(ctx, `SELECT `+tunnelCols+` FROM tunnels`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*TunnelRecord{}
	for rows.Next() {
		r, err := scanTunnel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Delete 删除台账行（部署失败逐层回滚的最后一步）。
func (s *TunnelStore) Delete(ctx context.Context, id int64) error {
	_, err := s.d.ExecContext(ctx, `DELETE FROM tunnels WHERE id=$1`, id)
	return err
}
