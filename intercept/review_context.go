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
)

const (
	BackgroundUserMessage   = "user_message"
	BackgroundWorkerSummary = "worker_summary"
)

// ReviewBackground is selected by the application from an existing message or
// the current intent's summary. Worker summaries are planner-authored, not human
// instructions. Neither source can override the reviewer's own action policy.
type ReviewBackground struct {
	Source    string `json:"source"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
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
	Version          int               `json:"version"`
	WorkingDir       string            `json:"working_directory,omitempty"`
	Background       *ReviewBackground `json:"background,omitempty"`
	History          []ReviewExecution `json:"history"`
	HistoryTruncated bool              `json:"history_truncated,omitempty"`
	Correlation      string            `json:"correlation"`
	Tool             string            `json:"tool_name"`
	Arguments        json.RawMessage   `json:"arguments"`
}

type reviewContextKey struct{}
type reviewEnvironment struct {
	workingDir string
	background ReviewBackground
}

// WithReviewContext explicitly binds the permitted background for one run. Never
// fall back to the raw turn transcript: it may contain the full scheduler prompt.
// This is application wiring, not a model-callable tool.
func WithReviewContext(ctx context.Context, workingDir string, background ReviewBackground) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, reviewEnvironment{workingDir, background})
}

// WithReviewWorkingDirectory preserves only explicitly selected background.
// Chat runs can be human-initiated or scheduled, so the Agent must not infer
// message provenance from the text it receives.
func WithReviewWorkingDirectory(ctx context.Context, workingDir string) context.Context {
	env, _ := ctx.Value(reviewContextKey{}).(reviewEnvironment)
	env.workingDir = workingDir
	return context.WithValue(ctx, reviewContextKey{}, env)
}

func BuildReviewInput(ctx context.Context, tool string, arguments json.RawMessage) (ReviewInput, error) {
	if !json.Valid(arguments) {
		return ReviewInput{}, fmt.Errorf("工具参数不是有效 JSON")
	}
	in := ReviewInput{Version: 2, Tool: tool, Arguments: append(json.RawMessage(nil), arguments...), History: []ReviewExecution{}, Correlation: "unavailable"}
	if env, ok := ctx.Value(reviewContextKey{}).(reviewEnvironment); ok {
		in.WorkingDir = env.workingDir
		background := env.background
		if (background.Source == BackgroundUserMessage || background.Source == BackgroundWorkerSummary) && strings.TrimSpace(background.Text) != "" {
			var cut bool
			background.Text, cut = bounded(background.Text, reviewTextLimit)
			background.Truncated = background.Truncated || cut
			in.Background = &background
		}
	}
	if audit, ok := ctx.Value(callKey{}).(db.InterceptAudit); ok {
		in.Correlation = audit.Correlation
		in.HistoryTruncated = audit.ContextTruncated
		// Ambiguous concurrent calls must not borrow another call's history.
		if audit.Correlation == "exact" {
			var cut bool
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
