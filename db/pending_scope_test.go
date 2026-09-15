package db

import (
	"testing"
	"time"
)

// pendingScopeTaskID 用时间戳派生一个隔离的 task_id(pending_scope 无外键,
// 不依赖 tasks 表),多次跑测试互不干扰。
func pendingScopeTaskID() int64 { return time.Now().UnixNano() }

// 全生命周期:upsert 幂等(仅 pending 累计)→ 列表过滤 → decide → 已决不再累计。
func TestPendingScopeLifecycle(t *testing.T) {
	d, as, _ := testSetup(t)
	defer d.Close()
	taskID := pendingScopeTaskID()

	created, err := as.UpsertPendingScope(taskID, "cidr", "10.9.8.0/24")
	if err != nil || !created {
		t.Fatalf("首次 upsert 应 created=true: created=%v err=%v", created, err)
	}
	// 重复登记:created=false,hits 累计。
	created, err = as.UpsertPendingScope(taskID, "cidr", "10.9.8.0/24")
	if err != nil || created {
		t.Fatalf("重复 upsert 应 created=false: created=%v err=%v", created, err)
	}
	items, err := as.ListPendingScope(taskID, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("pending 列表应 1 条: %v err=%v", items, err)
	}
	row := items[0]
	if row.Status != "pending" || row.Hits != 2 || row.Value != "10.9.8.0/24" {
		t.Fatalf("条目状态不符: %+v", row)
	}
	if row.DecidedAt != nil {
		t.Fatalf("pending 条目不应有 decided_at: %+v", row)
	}

	// approve:状态迁移 + decided_at;此后 upsert 不再累计(防重复骚扰)。
	decided, err := as.DecidePendingScope(row.ID, true)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.Status != "approved" || decided.DecidedAt == nil {
		t.Fatalf("approve 后状态不符: %+v", decided)
	}
	if created, err = as.UpsertPendingScope(taskID, "cidr", "10.9.8.0/24"); err != nil || created {
		t.Fatalf("已决条目 upsert 应 created=false: created=%v err=%v", created, err)
	}
	again, err := as.GetPendingScope(row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if again.Hits != 2 || again.Status != "approved" {
		t.Fatalf("已决条目不应再累计: %+v", again)
	}
	// 冲突决定不改变原状(返回现状)。
	conflict, err := as.DecidePendingScope(row.ID, false)
	if err != nil || conflict.Status != "approved" {
		t.Fatalf("冲突决定应返回现状 approved: %+v err=%v", conflict, err)
	}

	// 默认列表不含已决;includeDecided 含。
	if items, _ = as.ListPendingScope(taskID, false); len(items) != 0 {
		t.Fatalf("默认列表应不含已决: %v", items)
	}
	if items, _ = as.ListPendingScope(taskID, true); len(items) != 1 {
		t.Fatalf("includeDecided 应含已决: %v", items)
	}

	// dismiss 路径。
	created, err = as.UpsertPendingScope(taskID, "cidr", "10.9.9.0/24")
	if err != nil || !created {
		t.Fatalf("新条目 upsert: created=%v err=%v", created, err)
	}
	items, _ = as.ListPendingScope(taskID, false)
	if len(items) != 1 {
		t.Fatalf("应有 1 条新 pending: %v", items)
	}
	dismissed, err := as.DecidePendingScope(items[0].ID, false)
	if err != nil || dismissed.Status != "dismissed" || dismissed.DecidedAt == nil {
		t.Fatalf("dismiss 后状态不符: %+v err=%v", dismissed, err)
	}
}

// 参数守卫:非法入参 no-op;查无此条返回 sql.ErrNoRows。
func TestPendingScopeGuards(t *testing.T) {
	d, as, _ := testSetup(t)
	defer d.Close()

	for _, args := range [][3]any{{int64(0), "cidr", "1.2.3.0/24"}, {int64(1), "", "1.2.3.0/24"}, {int64(1), "cidr", ""}} {
		created, err := as.UpsertPendingScope(args[0].(int64), args[1].(string), args[2].(string))
		if err != nil || created {
			t.Fatalf("非法入参应 no-op: %v → created=%v err=%v", args, created, err)
		}
	}
	if _, err := as.GetPendingScope(-1); err == nil {
		t.Fatalf("查无此条应报错")
	}
}
