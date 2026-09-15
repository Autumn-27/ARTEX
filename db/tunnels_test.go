package db

import (
	"context"
	"encoding/json"
	"testing"
)

// tunnels 表 CRUD 需要真实 dev PG(ARTEX_PG_DSN);无 DSN 时按包约定 skip。
func TestTunnelStoreCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("dev PG 不可用: %v", err)
	}
	defer d.Close()
	ctx := context.Background()
	s := NewTunnelStore(d)

	params, _ := json.Marshal(map[string]any{
		"auth_user": "artex", "auth_pass": "deadbeef", "server_pid": 1234,
		"socks_port": 20002, "stage_token": "tok",
	})
	id, err := s.Create(ctx, &TunnelRecord{
		TaskID: 999001, Kind: "socks", ListenHost: "0.0.0.0", ListenPort: 20001,
		ViaSessionID: 3, State: TunnelDeploying, DeployParams: params,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _, _ = d.Exec(`DELETE FROM tunnels WHERE task_id=999001`) }()

	rec, err := s.Get(ctx, id)
	if err != nil || rec == nil {
		t.Fatalf("Get: %v rec=%v", err, rec)
	}
	if rec.State != TunnelDeploying || rec.ListenPort != 20001 || rec.Adapter != "chisel" {
		t.Errorf("行内容不符: %+v", rec)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.DeployParams, &p); err != nil || p["auth_pass"] != "deadbeef" {
		t.Errorf("deploy_params 往返失败: %v %v", err, p)
	}

	// 状态机与 last_check 刷新
	if err := s.UpdateState(ctx, id, TunnelAlive, ""); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}
	if err := s.UpdateState(ctx, id, "bogus", ""); err == nil {
		t.Error("非法状态应报错")
	}
	alive, err := s.ListAlive(ctx)
	if err != nil {
		t.Fatalf("ListAlive: %v", err)
	}
	found := false
	for _, r := range alive {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Error("ListAlive 未含新隧道")
	}

	// ListByTask
	byTask, err := s.ListByTask(ctx, 999001)
	if err != nil || len(byTask) != 1 {
		t.Errorf("ListByTask = %d 行, %v", len(byTask), err)
	}

	// UpdateDeployParams 回填 remote_pid
	params2, _ := json.Marshal(map[string]any{"remote_pid": 4321})
	if err := s.UpdateDeployParams(ctx, id, 4321, params2); err != nil {
		t.Fatalf("UpdateDeployParams: %v", err)
	}
	rec, _ = s.Get(ctx, id)
	if rec.RemotePID != 4321 {
		t.Errorf("RemotePID = %d, want 4321", rec.RemotePID)
	}

	// 非法 kind
	if _, err := s.Create(ctx, &TunnelRecord{TaskID: 999001, Kind: "gre"}); err == nil {
		t.Error("非法 kind 应报错")
	}

	// Delete
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rec, _ = s.Get(ctx, id)
	if rec != nil {
		t.Error("Delete 后 Get 应为 nil")
	}
}
