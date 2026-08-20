package server

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestPauseCancelsWithNamedCause(t *testing.T) {
	e := NewEngine(nil)
	execCtx := e.execContextFor(context.Background(), "t1")
	workCtx, workCancel := context.WithCancelCause(execCtx)
	defer workCancel(nil)
	e.Pause("t1", agent.AbortPausedByUser)
	if code, _, _, ok := agent.AbortReason(workCtx); !ok || code != "paused_by_user" {
		t.Fatalf("code=%q ok=%v, want paused_by_user", code, ok)
	}
}

func TestKillWorkCancelsOnlyThatWorkWithNamedCause(t *testing.T) {
	e := NewEngine(nil)
	execCtx := e.execContextFor(context.Background(), "t1")
	workCtx, workCancel := context.WithCancelCause(execCtx)
	e.registerWork(42, workCancel)
	if err := e.KillWork(42); err != nil {
		t.Fatal(err)
	}
	if code, _, _, ok := agent.AbortReason(workCtx); !ok || code != "killed_by_planner" {
		t.Fatalf("code=%q ok=%v, want killed_by_planner", code, ok)
	}
	if execCtx.Err() != nil {
		t.Fatal("kill_work cancelled the entire task context")
	}
	_, complete := e.detachWork(42)
	complete()
}

func TestControlWorkCarriesUserActionCause(t *testing.T) {
	for _, tc := range []struct {
		action string
		code   string
	}{
		{action: "pause", code: "work_paused_by_user"},
		{action: "cancel", code: "work_cancelled_by_user"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			e := NewEngine(nil)
			ctx, cancel := context.WithCancelCause(context.Background())
			e.registerWork(42, cancel)
			done := make(chan error, 1)
			go func() { done <- e.ControlWork(42, tc.action) }()
			<-ctx.Done()
			if code, _, _, _ := agent.AbortReason(ctx); code != tc.code {
				t.Fatalf("code=%q, want %q", code, tc.code)
			}
			action, complete := e.detachWork(42)
			if action != tc.action {
				t.Fatalf("action=%q, want %q", action, tc.action)
			}
			complete()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSettleDrainAndGoalMetCausesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		cause *agent.AbortCause
		code  string
	}{
		{agent.AbortSettleDrainTimeout, "settle_drain_timeout"},
		{agent.AbortGoalMet, "goal_met"},
	} {
		e := NewEngine(nil)
		ctx := e.execContextFor(context.Background(), "t1")
		e.cancelExec("t1", tc.cause)
		if code, _, _, _ := agent.AbortReason(ctx); code != tc.code {
			t.Fatalf("code=%q, want %q", code, tc.code)
		}
	}
}

func TestExecContextWhilePausedCarriesRaceGuardCause(t *testing.T) {
	e := NewEngine(nil)
	e.Pause("t1", agent.AbortPausedByUser)
	ctx := e.execContextFor(context.Background(), "t1")
	if ctx.Err() == nil {
		t.Fatal("paused task received a live execution context")
	}
	if code, _, _, _ := agent.AbortReason(ctx); code != "paused_race_guard" {
		t.Fatalf("code=%q, want paused_race_guard", code)
	}
}

func TestResumeYieldsFreshContext(t *testing.T) {
	e := NewEngine(nil)
	old := e.execContextFor(context.Background(), "t1")
	e.Pause("t1", agent.AbortPausedByUser)
	if old.Err() == nil {
		t.Fatal("Pause did not cancel old context")
	}
	e.paused.Delete("t1")
	if fresh := e.execContextFor(context.Background(), "t1"); fresh.Err() != nil {
		t.Fatal("resume did not create a fresh execution context")
	}
}
