package agent

import (
	"context"
	"time"
)

// TaskClock carries a task's absolute deadline into a worker/planner run so the run
// can clamp its own wall-clock budget to the task's remaining time and pick the
// right wrap-up words (per-run vs task-timeout). Attached to the run ctx by the
// engine. Zero value = no task-level timeout (behaves exactly as before).
type TaskClock struct {
	DeadlineUnix int64 // absolute deadline (unix seconds); 0 = no task timeout
	Final        bool  // coordinator-driven FINAL planner round (task ending now)
}

type taskClockKey struct{}

// WithTaskClock attaches a TaskClock to ctx for the run.
func WithTaskClock(ctx context.Context, tc TaskClock) context.Context {
	return context.WithValue(ctx, taskClockKey{}, tc)
}

// taskClockFrom reads the TaskClock (zero value if none attached).
func taskClockFrom(ctx context.Context) TaskClock {
	if v, ok := ctx.Value(taskClockKey{}).(TaskClock); ok {
		return v
	}
	return TaskClock{}
}

// clampMaxDuration folds a task deadline into a run's own wall-clock budget.
//   - ownBudget = the agent's own run_seconds (0 = unlimited).
//   - Returns eff = the MaxDuration to use (floored at 1s so we never pass ≤0, which
//     harness reads as "unlimited"), and clamped = whether the TASK deadline is the
//     binding constraint (remaining ≤ ownBudget, or ownBudget unlimited). When there
//     is no deadline, returns (ownBudget, false) unchanged.
func clampMaxDuration(deadlineUnix int64, ownBudget time.Duration) (eff time.Duration, clamped bool) {
	if deadlineUnix <= 0 {
		return ownBudget, false
	}
	remaining := time.Until(time.Unix(deadlineUnix, 0))
	if remaining < time.Second {
		remaining = time.Second // max(1, …): never pass ≤0 (harness treats 0 as unlimited)
	}
	// clamped when the task deadline binds this run's time: remaining ≤ own budget,
	// or the agent has no own time budget (then remaining always binds).
	clamped = ownBudget <= 0 || remaining <= ownBudget
	eff = remaining
	if ownBudget > 0 && ownBudget < remaining {
		eff = ownBudget
	}
	return eff, clamped
}
