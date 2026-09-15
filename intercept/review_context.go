package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const (
	reviewTextLimit        = 4000
	reviewHistoryLimit     = 6
	reviewHistoryTextLimit = 2000
	reviewConstraintLimit  = 16000
)

// ReviewTask is loaded by the application, never from a tool's arguments. Origin
// records how constraints were registered; it is not proof of human approval.
type ReviewTask struct {
	TaskID      int64           `json:"task_id"`
	Description string          `json:"description"`
	Goal        string          `json:"goal"`
	Constraints []db.Constraint `json:"constraints"`
	Truncated   bool            `json:"truncated,omitempty"`
}

type ReviewExecution struct {
	ToolUseID string `json:"tool_use_id"`
	Tool      string `json:"tool"`
	Arguments string `json:"arguments_preview"`
	Result    string `json:"result"`
	Status    string `json:"status"` // succeeded | failed; a failed call may have partial effects
	Truncated bool   `json:"truncated,omitempty"`
}

// ReviewInput orders background before the exact current call. History is a
// bounded evidence window, not a complete transcript or an authorization source.
type ReviewInput struct {
	Version             int               `json:"version"`
	Task                *ReviewTask       `json:"task,omitempty"`
	WorkingDir          string            `json:"working_directory,omitempty"`
	Intent              string            `json:"worker_intent,omitempty"`
	TurnInput           string            `json:"turn_input,omitempty"`
	BackgroundTruncated bool              `json:"background_truncated,omitempty"`
	History             []ReviewExecution `json:"history"`
	HistoryTruncated    bool              `json:"history_truncated,omitempty"`
	Correlation         string            `json:"correlation"`
	Tool                string            `json:"tool_name"`
	Arguments           json.RawMessage   `json:"arguments"`
}

type reviewContextKey struct{}
type reviewEnvironment struct {
	workingDir, intent string
	loadTask           func(context.Context) (*ReviewTask, error)
}

// WithReviewContext binds one run's environment and a fresh task-context reader.
// It must be called by the application, not exposed as a model-callable tool.
func WithReviewContext(ctx context.Context, workingDir, intent string, loadTask func(context.Context) (*ReviewTask, error)) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, reviewEnvironment{workingDir, intent, loadTask})
}

func BuildReviewInput(ctx context.Context, tool string, arguments json.RawMessage) (ReviewInput, error) {
	if !json.Valid(arguments) {
		return ReviewInput{}, fmt.Errorf("工具参数不是有效 JSON")
	}
	in := ReviewInput{Version: 1, Tool: tool, Arguments: append(json.RawMessage(nil), arguments...), History: []ReviewExecution{}, Correlation: "unavailable"}
	if env, ok := ctx.Value(reviewContextKey{}).(reviewEnvironment); ok {
		in.WorkingDir = env.workingDir
		in.Intent, in.BackgroundTruncated = bounded(env.intent, reviewTextLimit)
		if env.loadTask != nil {
			task, err := env.loadTask(ctx)
			if err != nil || task == nil {
				return ReviewInput{}, fmt.Errorf("无法读取任务目标和操作约束")
			}
			copyTask := *task
			var cut bool
			copyTask.Description, cut = bounded(task.Description, reviewTextLimit)
			copyTask.Truncated = copyTask.Truncated || cut
			copyTask.Goal, cut = bounded(task.Goal, reviewTextLimit)
			copyTask.Truncated = copyTask.Truncated || cut
			copyTask.Constraints = append([]db.Constraint{}, task.Constraints...)
			// Do not silently drop a prohibition when a task exceeds the context budget.
			constraints, err := json.Marshal(copyTask.Constraints)
			if err != nil || len(constraints) > reviewConstraintLimit {
				return ReviewInput{}, fmt.Errorf("任务操作约束超过审查上下文上限")
			}
			in.Task = &copyTask
		}
	}
	if audit, ok := ctx.Value(callKey{}).(db.InterceptAudit); ok {
		in.Correlation = audit.Correlation
		var cut bool
		in.TurnInput, cut = bounded(audit.UserMessage, reviewTextLimit)
		in.BackgroundTruncated = in.BackgroundTruncated || cut || audit.UserTruncated
		in.HistoryTruncated = audit.ContextTruncated
		// Ambiguous concurrent calls must not borrow another call's history.
		if audit.Correlation == "exact" {
			in.History, cut = reviewHistory(audit.Context, audit.ToolUseID)
			in.HistoryTruncated = in.HistoryTruncated || cut
		}
	}
	return in, nil
}

func reviewFeedback(text string) bool {
	return strings.Contains(text, "【ARTEX 平台管控") || strings.Contains(text, "AegisHook 拒绝执行：") ||
		(strings.Contains(text, "实际操作：") && strings.Contains(text, "命中规则："))
}

func reviewHistory(entries []db.InterceptContextEntry, currentID string) ([]ReviewExecution, bool) {
	calls := map[string]db.InterceptContextEntry{}
	counts := map[string]int{}
	results := map[string]int{}
	for _, entry := range entries {
		if entry.Kind == "tool_use" {
			counts[entry.ToolUseID]++
		}
		if entry.Kind == "tool_result" {
			results[entry.ToolUseID]++
		}
	}
	history := []ReviewExecution{}
	seen := map[string]bool{}
	truncated := false
	for _, entry := range entries {
		id := entry.ToolUseID
		if id == "" || id == currentID || counts[id] != 1 || results[id] != 1 {
			continue
		}
		if entry.Kind == "tool_use" {
			calls[id] = entry
			continue
		}
		call, ok := calls[id]
		if entry.Kind != "tool_result" || !ok || call.Tool == "" || (entry.Tool != "" && entry.Tool != call.Tool) || seen[id] || reviewFeedback(entry.Text) {
			continue
		}
		seen[id] = true
		args, argsCut := bounded(call.Text, reviewHistoryTextLimit)
		result, resultCut := bounded(entry.Text, reviewHistoryTextLimit)
		status := "succeeded"
		if entry.IsError {
			status = "failed"
		}
		history = append(history, ReviewExecution{ToolUseID: id, Tool: call.Tool, Arguments: args, Result: result, Status: status,
			Truncated: call.Truncated || entry.Truncated || argsCut || resultCut})
	}
	if len(history) > reviewHistoryLimit {
		history = history[len(history)-reviewHistoryLimit:]
		truncated = true
	}
	return history, truncated
}
