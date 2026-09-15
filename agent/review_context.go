package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// Keep operator constraints separate from worker plans and discovered assets.
// The reader runs for each review so mid-run constraint edits are visible.
func withTaskReviewContext(ctx context.Context, taskID int64, ts *db.ExplorationStore, workDir string, intent *db.Node) context.Context {
	intentText := ""
	if intent != nil {
		intentText = string(intent.Payload)
	}
	var load func(context.Context) (*intercept.ReviewTask, error)
	if ts != nil {
		load = func(ctx context.Context) (*intercept.ReviewTask, error) {
			description, goal, constraints, err := ts.OperationReviewContext(ctx)
			if err != nil {
				return nil, err
			}
			return &intercept.ReviewTask{TaskID: taskID, Description: description, Goal: goal, Constraints: constraints}, nil
		}
	}
	return intercept.WithReviewContext(ctx, workDir, intentText, load)
}
