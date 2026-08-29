package server

import (
	"context"
	"errors"
	"testing"
	"time"

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
	complete(nil)
}

func TestControlWorkCarriesUserActionCause(t *testing.T) {
	for _, tc := range []struct {
		action string
		code   string
	}{
		{action: "pause", code: "work_paused_by_user"},
		{action: "cancel", code: "work_cancelled_by_user"},
		{action: "intervene", code: "work_intervened_by_user"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			e := NewEngine(nil)
			ctx, cancel := context.WithCancelCause(context.Background())
			e.registerWork(42, cancel)
			done := make(chan error, 1)
			go func() { done <- e.ControlWork(context.Background(), 42, tc.action) }()
			<-ctx.Done()
			if code, _, _, _ := agent.AbortReason(ctx); code != tc.code {
				t.Fatalf("code=%q, want %q", code, tc.code)
			}
			action, complete := e.detachWork(42)
			if action != tc.action {
				t.Fatalf("action=%q, want %q", action, tc.action)
			}
			complete(nil)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestControlWorkReturnsSettlementError(t *testing.T) {
	e := NewEngine(nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	e.registerWork(42, cancel)
	done := make(chan error, 1)
	go func() { done <- e.ControlWork(context.Background(), 42, "pause") }()
	<-ctx.Done()
	_, complete := e.detachWork(42)
	want := errors.New("pause persistence failed")
	complete(want)
	if err := <-done; !errors.Is(err, want) {
		t.Fatalf("ControlWork error=%v, want %v", err, want)
	}
}

func TestControlWorkWaitHonorsContextAndReleasesReservation(t *testing.T) {
	e := NewEngine(nil)
	_, cancel := context.WithCancelCause(context.Background())
	e.registerWork(42, cancel)
	waitCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := e.ControlWork(waitCtx, 42, "pause"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ControlWork error=%v, want deadline exceeded", err)
	}

	e.workMu.Lock()
	run := e.work[42]
	if run == nil || run.action != "" {
		t.Fatalf("timed-out control left reservation: %+v", run)
	}
	e.workMu.Unlock()
	_, complete := e.detachWork(42)
	complete(nil)
}

func TestControlWorkRejectsConcurrentController(t *testing.T) {
	e := NewEngine(nil)
	ctx, cancel := context.WithCancelCause(context.Background())
	e.registerWork(42, cancel)
	first := make(chan error, 1)
	go func() { first <- e.ControlWork(context.Background(), 42, "pause") }()
	<-ctx.Done()
	if err := e.ControlWork(context.Background(), 42, "cancel"); !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("second control error=%v, want conflict", err)
	}
	action, complete := e.detachWork(42)
	if action != "pause" {
		t.Fatalf("winning action=%q, want pause", action)
	}
	complete(nil)
	if err := <-first; err != nil {
		t.Fatal(err)
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

func TestTaskPauseCauseSurvivesImmediateResume(t *testing.T) {
	e := NewEngine(nil)
	task := &Task{ID: "t1", notify: make(chan struct{}, 1)}
	old := e.execContextFor(context.Background(), task.ID)
	e.Pause(task.ID, agent.AbortPausedByUser)
	e.Resume(task)
	if e.IsPaused(task.ID) {
		t.Fatal("resume did not clear the task pause flag")
	}
	if old.Err() == nil || !taskExecutionPaused(context.Cause(old)) {
		t.Fatalf("old execution cause=%v was not retained as a task pause", context.Cause(old))
	}
	if taskExecutionPaused(agent.AbortGoalMet) {
		t.Fatal("goal completion was misclassified as a task pause")
	}
}

func TestDeleteCancellationReturnsRunningIntentToFrontier(t *testing.T) {
	if !taskExecutionPaused(agent.AbortTaskDeleted) {
		t.Fatal("delete cancellation must settle a running intent as open so AbortDelete can resume it")
	}
	if taskExecutionPaused(agent.AbortGoalMet) {
		t.Fatal("terminal task cancellation must not reopen completed work")
	}
}

func TestResumeDoesNotClearDeleteBarrier(t *testing.T) {
	e := NewEngine(nil)
	task := &Task{ID: "t1", notify: make(chan struct{}, 1)}
	e.paused.Store(task.ID, true)
	e.deleting.Store(task.ID, true)
	e.Resume(task)
	if !e.IsPaused(task.ID) {
		t.Fatal("resume cleared the pause barrier owned by task deletion")
	}
}

func TestBootstrapAdmissionClearsQueuedPauseBarrier(t *testing.T) {
	e := NewEngine(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &Task{ID: "t1", notify: make(chan struct{}, 1)}
	s := &Server{engine: e, ctx: ctx}
	e.Pause(task.ID, agent.AbortPausedByUser)

	s.startAdmittedTask(task, "bootstrap")
	if e.IsPaused(task.ID) {
		t.Fatal("bootstrap admission left the queued task behind the pause barrier")
	}
}
