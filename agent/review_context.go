package agent

import (
	"context"
	"encoding/json"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// Select only the current intent's existing summary. Missing or malformed data
// deliberately produces no background; never substitute the payload, task goal,
// constraints, global exploration state, or a generated Worker prompt.
func withWorkerReviewContext(ctx context.Context, workDir string, intent *db.Node) context.Context {
	background := intercept.ReviewBackground{}
	if intent != nil {
		var payload struct {
			Summary string `json:"summary"`
		}
		if json.Unmarshal(intent.Payload, &payload) == nil {
			background = intercept.ReviewBackground{Source: intercept.BackgroundWorkerSummary, Text: payload.Summary}
		}
	}
	return intercept.WithReviewContext(ctx, workDir, background)
}
