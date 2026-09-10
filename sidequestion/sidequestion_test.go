package sidequestion

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/llmpool"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type fakeProvider struct {
	stream   func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error]
	complete func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (p fakeProvider) Stream(ctx context.Context, r llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return p.stream(ctx, r)
}
func (p fakeProvider) Complete(ctx context.Context, r llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.complete(ctx, r)
}
func assistant(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(text)}}
}
func fixture() llm.CompletionRequest {
	temp := 0.3
	return llm.CompletionRequest{System: []string{"system"}, Temperature: &temp, MaxTokens: 128, Stop: []string{"END"}, Messages: []llm.Message{
		llm.UserText("<system-reminder>date</system-reminder>"), llm.UserText("read asset"),
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "reason", Signature: "signed"}, {Type: llm.BlockToolUse, ID: "tool-1", Name: "read", Input: json.RawMessage(`{"url":"fixture"}`)}}},
		{Role: llm.RoleUser, Content: []llm.ContentBlock{llm.ToolResultText("tool-1", "verified asset", false)}},
	}, Tools: []llm.ToolSchema{{Name: "read", InputSchema: map[string]any{"properties": map[string]any{"url": map[string]any{"type": "string"}}}}}}
}

func TestCheckpointDeepCopyAndBoundaries(t *testing.T) {
	var snapshots []Snapshot
	p := Bind(fakeProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		return assistant("finished"), "end_turn", llm.Usage{}, nil
	}}, Model{Model: "actual"})
	ctx, deps := Attach(WithPublisher(t.Context(), func(s Snapshot) { snapshots = append(snapshots, s) }), Parent{ConversationID: 1}, harness.QueryDeps{}, p)
	req := fixture()
	if _, _, _, err := deps.CallModelSync(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[1].Request.Messages[len(snapshots[1].Request.Messages)-1].Text() != "finished" {
		t.Fatalf("checkpoints: %+v", snapshots)
	}
	before, _ := json.Marshal(snapshots)
	req.System[0] = "mutated"
	*req.Temperature = 2
	req.Stop[0] = "bad"
	req.Messages[2].Content[1].Input[2] = 'X'
	req.Messages[3].Content[0].Content[0].Text = "mutated"
	req.Tools[0].InputSchema["properties"].(map[string]any)["url"].(map[string]any)["type"] = "number"
	after, _ := json.Marshal(snapshots)
	if string(before) != string(after) {
		t.Fatal("published checkpoint aliases structured input")
	}
	// Auxiliary/compaction calls lack the model-attempt marker.
	_, _, _, _ = p.Complete(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("summary request")}})
	if len(snapshots) != 2 {
		t.Fatal("auxiliary completion replaced snapshot")
	}
	terminal := fixture().Messages[1:]
	terminal = append(terminal, assistant("terminal answer"))
	Finish(ctx, terminal)
	last := snapshots[len(snapshots)-1]
	if last.Request.Messages[0].Text() != "<system-reminder>date</system-reminder>" || len(last.Request.Messages) != 5 {
		t.Fatalf("terminal/reminder duplication: %+v", last.Request.Messages)
	}
	if !strings.Contains(string(mustJSON(t, last)), "verified asset") {
		t.Fatal("terminal lost paired tool result")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestSnapshotExcludesPartialStreamAndSelectsPoolMember(t *testing.T) {
	var snapshots []Snapshot
	failed := Bind(fakeProvider{stream: func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) { y(llm.StreamEvent{}, errors.New("HTTP 503 unavailable")) }
	}}, Model{Model: "failed"})
	good := Bind(fakeProvider{stream: func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			if !y(llm.StreamEvent{Type: llm.SETextDelta, Text: "complete"}, nil) {
				return
			}
			y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
		}
	}}, Model{Model: "selected"})
	pool := llmpool.New([]*llmpool.Member{{ID: 1, Name: "failed", Rank: 2, Prov: failed}, {ID: 2, Name: "selected", Rank: 1, Prov: good}}, nil)
	ctx, deps := Attach(WithPublisher(t.Context(), func(s Snapshot) { snapshots = append(snapshots, s) }), Parent{ConversationID: 2}, harness.QueryDeps{}, pool)
	for ev, err := range deps.CallModel(ctx, fixture()) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.SETextDelta && strings.Contains(string(mustJSON(t, snapshots[len(snapshots)-1])), "complete") {
			t.Fatal("partial reply published")
		}
	}
	if last := snapshots[len(snapshots)-1]; last.Model.Model != "selected" || last.Request.Messages[len(last.Request.Messages)-1].Text() != "complete" {
		t.Fatalf("wrong selected model/checkpoint: %+v", last)
	}
	count := len(snapshots)
	for range deps.CallModel(ctx, fixture()) {
		break
	}
	if len(snapshots) != count+2 || len(snapshots[len(snapshots)-1].Request.Messages) != len(fixture().Messages) {
		t.Fatal("consumer stop published partial reply")
	}
}

func TestBuildRequestCompactionToolPairingAndBudget(t *testing.T) {
	s := Snapshot{Request: fixture(), Model: Model{WindowTokens: 100000}}
	s.Request.Messages = append(s.Request.Messages, llm.BoundaryMessage(llm.BoundaryMeta{Trigger: "auto"}), llm.UserText("compressed summary"), s.Request.Messages[3])
	history := []Exchange{}
	for i := 0; i < 25; i++ {
		history = append(history, Exchange{Question: strings.Repeat("q", 100), Answer: "answer", Status: "completed"})
	}
	r, err := BuildRequest(s, history, "current question")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 42 || r.Messages[0].Text() != "compressed summary" {
		t.Fatalf("history cap/compaction: %d %+v", len(r.Messages), r.Messages[0])
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				t.Fatal("orphan tool result survived")
			}
		}
	}
	s.Model.WindowTokens = 1600
	r, err = BuildRequest(s, history, "current question")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) >= 42 || len(r.Messages) < 2 {
		t.Fatal("budget did not reduce side history")
	}
	s.Model.WindowTokens = 10
	if _, err = BuildRequest(s, nil, "question"); err == nil {
		t.Fatal("oversized base accepted")
	}
}

func TestServiceNoToolsAndUsageOnFailure(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{true: "stream", false: "atomic"}[streaming], func(t *testing.T) {
			calls := 0
			p := fakeProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				calls++
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "new", Name: "run_shell", Input: json.RawMessage(`{"command":"touch forbidden"}`)}}}, "tool_use", llm.Usage{InputTokens: 11}, nil
			}, stream: func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
				return func(y func(llm.StreamEvent, error) bool) {
					calls++
					y(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 11}}, nil)
					y(llm.StreamEvent{Type: llm.SEToolUseStart, ToolID: "new", ToolName: "run_shell"}, nil)
					y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
				}
			}}
			before := fixture()
			req, _ := CloneRequest(before)
			out, err := (SideQuestionService{p}).Answer(t.Context(), req, streaming, nil)
			if err != nil || calls != 1 || !out.ToolUse || !strings.Contains(out.Text, "不能执行工具") || out.Usage.InputTokens != 11 {
				t.Fatalf("answer %+v calls=%d err=%v", out, calls, err)
			}
			if !reflect.DeepEqual(before, req) {
				t.Fatal("service mutated main context")
			}
		})
	}
	p := fakeProvider{stream: func(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		return func(y func(llm.StreamEvent, error) bool) {
			y(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 12}}, nil)
			y(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil)
			y(llm.StreamEvent{}, errors.New("broken"))
		}
	}}
	out, err := (SideQuestionService{p}).Answer(t.Context(), fixture(), true, nil)
	if err == nil || out.Text != "partial" || out.Usage.InputTokens != 12 {
		t.Fatalf("lost failure usage/partial: %+v %v", out, err)
	}
}

func TestMainSideConcurrencyAndIndependentCancellation(t *testing.T) {
	for _, cancelMain := range []bool{false, true} {
		t.Run(map[bool]string{true: "stop-main", false: "stop-side"}[cancelMain], func(t *testing.T) {
			started := make(chan struct{}, 2)
			p := fakeProvider{stream: func(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
				return func(y func(llm.StreamEvent, error) bool) {
					started <- struct{}{}
					y(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 7}}, nil)
					<-ctx.Done()
					y(llm.StreamEvent{}, ctx.Err())
				}
			}}
			mainCtx, stopMain := context.WithCancel(t.Context())
			defer stopMain()
			sideCtx, stopSide := context.WithCancel(t.Context())
			defer stopSide()
			var mu sync.Mutex
			bound := Bind(p, Model{Model: "blocking"})
			mainCtx, deps := Attach(WithPublisher(mainCtx, func(Snapshot) { mu.Lock(); mu.Unlock() }), Parent{ConversationID: 3}, harness.QueryDeps{}, bound)
			mainDone, sideDone := make(chan struct{}), make(chan Answer, 1)
			go func() {
				defer close(mainDone)
				for range deps.CallModel(mainCtx, fixture()) {
				}
			}()
			go func() { a, _ := (SideQuestionService{bound}).Answer(sideCtx, fixture(), true, nil); sideDone <- a }()
			for i := 0; i < 2; i++ {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("main and side did not execute concurrently")
				}
			}
			if cancelMain {
				stopMain()
				select {
				case <-mainDone:
				case <-time.After(time.Second):
					t.Fatal("main cancellation stuck")
				}
				select {
				case <-sideDone:
					t.Fatal("main cancellation stopped side")
				default:
				}
				stopSide()
			} else {
				stopSide()
				select {
				case a := <-sideDone:
					if a.Usage.InputTokens != 7 {
						t.Fatal("cancelled usage lost")
					}
				case <-time.After(time.Second):
					t.Fatal("side cancellation stuck")
				}
				select {
				case <-mainDone:
					t.Fatal("side cancellation stopped main")
				default:
				}
				stopMain()
			}
		})
	}
}
