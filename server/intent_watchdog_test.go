package server

import (
	"context"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

// P0 领取超时的纯判定:优先级门槛 + 5 分钟等待时长。
func TestP0ClaimOverdue(t *testing.T) {
	now := time.Now()
	created := now.Add(-6 * time.Minute)
	for _, tc := range []struct {
		name      string
		priority  int
		createdAt time.Time
		want      bool
	}{
		{"P0 等 6 分钟 → 逾期", 8, created, true},
		{"最高档 10 等 6 分钟 → 逾期", 10, created, true},
		{"P0 等 4 分钟 → 未逾期", 8, now.Add(-4 * time.Minute), false},
		{"恰低于阈值 7 等再久也不报", 7, now.Add(-time.Hour), false},
		{"默认档 5 等再久也不报", 5, now.Add(-time.Hour), false},
		{"阈值边界 8 恰好 5 分钟 → 逾期", 8, now.Add(-p0ClaimTimeout), true},
	} {
		overdue, waited := p0ClaimOverdue(tc.priority, tc.createdAt, now)
		if overdue != tc.want {
			t.Errorf("%s: overdue=%v, want %v", tc.name, overdue, tc.want)
		}
		if tc.want && waited < p0ClaimTimeout {
			t.Errorf("%s: waited=%v < p0ClaimTimeout", tc.name, waited)
		}
	}
	if p0ClaimTimeout != 5*time.Minute {
		t.Fatalf("p0ClaimTimeout=%v, want 5m(R2 修复契约)", p0ClaimTimeout)
	}
}

// 冷却:同一意图只报一次;撤销后允许重报(hint 写库失败的重试路径)。
func TestP0ReportedCooldown(t *testing.T) {
	e := NewEngine(nil)
	if !e.p0MarkReported("t1", 207) {
		t.Fatal("首次登记应返回 true")
	}
	if e.p0MarkReported("t1", 207) {
		t.Fatal("同一意图重复登记必须返回 false(只报一次)")
	}
	if !e.p0MarkReported("t1", 208) {
		t.Fatal("不同意图互不影响")
	}
	if !e.p0MarkReported("t2", 207) {
		t.Fatal("不同任务互不影响")
	}
	e.p0Unmark("t1", 207)
	if !e.p0MarkReported("t1", 207) {
		t.Fatal("unmark 后应允许重报")
	}
}

// mainagent 的 kill_work 必须带 killed_by_mainagent 具名原因,且只取消该 work、
// 不波及整个任务 ctx(对照 planner 的 killed_by_planner)。
func TestKillWorkAsCarriesMainagentCause(t *testing.T) {
	e := NewEngine(nil)
	execCtx := e.execContextFor(context.Background(), "t1")
	workCtx, workCancel := context.WithCancelCause(execCtx)
	e.registerWork(42, workCancel)
	if err := e.KillWorkAs(42, agent.AbortKilledByMainagent); err != nil {
		t.Fatal(err)
	}
	if code, _, _, ok := agent.AbortReason(workCtx); !ok || code != "killed_by_mainagent" {
		t.Fatalf("code=%q ok=%v, want killed_by_mainagent", code, ok)
	}
	if execCtx.Err() != nil {
		t.Fatal("mainagent kill_work cancelled the entire task context")
	}
	// planner 路径仍是原原因。
	workCtx2, workCancel2 := context.WithCancelCause(execCtx)
	e.registerWork(43, workCancel2)
	if err := e.KillWork(43); err != nil {
		t.Fatal(err)
	}
	if code, _, _, _ := agent.AbortReason(workCtx2); code != "killed_by_planner" {
		t.Fatalf("planner kill code=%q, want killed_by_planner", code)
	}
	_, complete := e.detachWork(42)
	complete(nil)
	_, complete2 := e.detachWork(43)
	complete2(nil)
}
