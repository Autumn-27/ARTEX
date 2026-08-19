package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

// The engine's cancel paths must hand a NAMED cause to the run being cancelled —
// that cause is the whole content of the "运行被中断" line in the trace. These
// exercise the context machinery only (no Manager / DB needed).

func TestPauseCancelsWithNamedCause(t *testing.T) {
	e := NewEngine(nil)
	ectx := e.execContextFor(context.Background(), "t1")
	// the per-work child the worker loop actually runs under
	workCtx, workCancel := context.WithCancelCause(ectx)
	defer workCancel(nil)

	e.Pause("t1", agent.AbortPausedByUser)

	if code, _, _, ok := agent.AbortReason(workCtx); !ok || code != "paused_by_user" {
		t.Fatalf("work ctx 拿到的原因 code=%q ok=%v，期望 paused_by_user", code, ok)
	}
}

func TestKillWorkCancelsOnlyThatWorkWithNamedCause(t *testing.T) {
	e := NewEngine(nil)
	ectx := e.execContextFor(context.Background(), "t1")
	workCtx, workCancel := context.WithCancelCause(ectx)
	e.registerWork(42, workCancel)

	if err := e.KillWork(42); err != nil {
		t.Fatalf("KillWork: %v", err)
	}
	if code, _, _, ok := agent.AbortReason(workCtx); !ok || code != "killed_by_planner" {
		t.Fatalf("code=%q ok=%v，期望 killed_by_planner", code, ok)
	}
	// the task ctx must survive — kill_work stops one work, not the whole task
	if ectx.Err() != nil {
		t.Error("kill_work 不该取消整个任务的 exec ctx")
	}
	if err := e.KillWork(999); err == nil {
		t.Error("对没有在跑的意图 KillWork 应该报错")
	}
}

func TestSettleDrainAndGoalMetCausesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause *agent.AbortCause
		want  string
	}{
		{"settle", agent.AbortSettleDrainTimeout, "settle_drain_timeout"},
		{"goalMet", agent.AbortGoalMet, "goal_met"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(nil)
			ectx := e.execContextFor(context.Background(), "t1")
			e.cancelExec("t1", tc.cause)
			if code, _, _, _ := agent.AbortReason(ectx); code != tc.want {
				t.Fatalf("code=%q，期望 %q", code, tc.want)
			}
		})
	}
}

// asking for an exec ctx while paused must return an already-cancelled ctx that
// still SAYS why — this is the claim→Execute race guard, easily mistaken for a
// mystery abort.
func TestExecContextWhilePausedCarriesRaceGuardCause(t *testing.T) {
	e := NewEngine(nil)
	e.Pause("t1", agent.AbortPausedByUser)
	ctx := e.execContextFor(context.Background(), "t1")
	if ctx.Err() == nil {
		t.Fatal("暂停期间不该发出可用的 exec ctx")
	}
	if code, _, _, _ := agent.AbortReason(ctx); code != "paused_race_guard" {
		t.Fatalf("code=%q，期望 paused_race_guard", code)
	}
}

// Resume must hand out a live context again (cancelling is one-shot).
func TestResumeYieldsFreshContext(t *testing.T) {
	e := NewEngine(nil)
	old := e.execContextFor(context.Background(), "t1")
	e.Pause("t1", agent.AbortPausedByUser)
	if old.Err() == nil {
		t.Fatal("Pause 没取消旧 ctx")
	}
	e.paused.Delete("t1") // Resume() without the Task/Notify plumbing
	if fresh := e.execContextFor(context.Background(), "t1"); fresh.Err() != nil {
		t.Fatal("恢复后应拿到全新可用的 exec ctx")
	}
}
