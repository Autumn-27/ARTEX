package server

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
)

type reviewCaptureProvider struct{ request llm.CompletionRequest }

func (p *reviewCaptureProvider) Stream(_ context.Context, request llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.request = request
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: `{"decision":"ask","comment":"实际操作：删除文件；成功后的后果：文件会丢失，归属尚未确认；命中规则：ASK（归属不明）"}`}, nil)
	}
}
func (p *reviewCaptureProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return accumulateStreamForTest(ctx, p.Stream, req)
}

// Assert the actual model request, rather than merely the envelope helper.
func TestReviewCompletionSendsTaskAndPairedContext(t *testing.T) {
	ctx := intercept.WithReviewContext(t.Context(), "/tmp/review-fixture", "清理本次测试文件", func(context.Context) (*intercept.ReviewTask, error) {
		return &intercept.ReviewTask{TaskID: 7, Goal: "仅验证测试站点", Constraints: []db.Constraint{{Kind: "deny", Text: "禁止删除生产文件", Origin: "human"}}}, nil
	})
	ctx, trace := intercept.WithTrace(ctx, "清理测试文件", []db.InterceptContextEntry{
		{Kind: "tool_use", Tool: "Write", ToolUseID: "created", Text: `{"path":"probe.txt"}`},
		{Kind: "tool_result", ToolUseID: "created", Text: "file created"},
		{Kind: "assistant", Text: "untrusted worker speculation"},
	})
	args := json.RawMessage(`{"command":"rm probe.txt","timeout":1000}`)
	trace.Start("current", "Bash", args)
	input, err := intercept.BuildReviewInput(intercept.WithCall(ctx, "Bash", args), "Bash", args)
	if err != nil {
		t.Fatal(err)
	}
	p := &reviewCaptureProvider{}
	prompt := intercept.EffectiveJudgePrompt(intercept.DefaultJudgePrompt)
	reply, err := reviewCompletion(ctx, p, prompt, input)
	if err != nil || intercept.ParseVerdict(reply).Action != "ask" {
		t.Fatalf("%s %v", reply, err)
	}
	if len(p.request.System) != 1 || p.request.System[0] != prompt || len(p.request.Messages) != 1 {
		t.Fatal("wrong review request roles")
	}
	if !strings.Contains(prompt, intercept.JudgeOutputContract) || p.request.MaxTokens < 1024 {
		t.Fatal("review request cannot return a complete explanation")
	}
	body := p.request.Messages[0].Content[0].Text
	var got intercept.ReviewInput
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Task.TaskID != 7 || got.Task.Constraints[0].Origin != "human" || got.WorkingDir != "/tmp/review-fixture" || len(got.History) != 1 || got.History[0].ToolUseID != "created" || string(got.Arguments) != string(args) || strings.Contains(body, "worker speculation") {
		t.Fatalf("wrong model input: %s", body)
	}
}
