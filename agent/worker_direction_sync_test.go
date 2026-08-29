package agent

import (
	"context"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

type directionCaptureProvider struct {
	mu       sync.Mutex
	requests []llm.CompletionRequest
}

func (p *directionCaptureProvider) Stream(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return func(yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "同步完成"}, nil) {
			return
		}
		if !yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (p *directionCaptureProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("同步完成")}}, "end_turn", llm.Usage{}, nil
}

func (p *directionCaptureProvider) lastRequest(t *testing.T) llm.CompletionRequest {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		t.Fatal("provider received no completion request")
	}
	return p.requests[len(p.requests)-1]
}

func lastUserText(t *testing.T, req llm.CompletionRequest) string {
	t.Helper()
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return req.Messages[i].Text()
		}
	}
	t.Fatal("completion request has no user message")
	return ""
}

type directionAgentFixture struct {
	d        *db.DB
	task     *db.Task
	store    *db.ExplorationStore
	intentID int64
	activity db.Activity
}

func newDirectionAgentFixture(t *testing.T) *directionAgentFixture {
	t.Helper()
	d := testDB(t)
	task, err := d.CreateTask("agent direction sync", "synchronize worker steering", nil, 0, 0)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(task.ID)
		_ = d.Close()
	})
	store := d.Exploration(task.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{"summary": "原始接口枚举方向"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimIntent(intentID, "work#direction"); err != nil || !claimed {
		t.Fatalf("claim direction fixture: claimed=%v err=%v", claimed, err)
	}
	const requestID = "agent-direction-sync-1"
	const direction = "停止接口枚举，立即聚焦登录后的水平越权"
	if _, err := store.ReserveIntentIntervention(intentID, requestID, direction); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.CompareAndSetIntentState(intentID, "running", "paused"); err != nil || !changed {
		t.Fatalf("pause direction fixture: changed=%v err=%v", changed, err)
	}
	activity, duplicate, err := store.AcceptIntentIntervention(intentID, requestID, direction)
	if err != nil || duplicate {
		t.Fatalf("accept direction fixture: activity=%+v duplicate=%v err=%v", activity, duplicate, err)
	}
	return &directionAgentFixture{d: d, task: task, store: store, intentID: intentID, activity: activity}
}

func TestGraphOverviewIncludesLatestWorkerDirection(t *testing.T) {
	f := newDirectionAgentFixture(t)
	overview := NewToolSet(f.store, "planner").graphOverviewData()
	directions, ok := overview["worker_directions"].([]map[string]any)
	if !ok || len(directions) != 1 {
		t.Fatalf("worker_directions=%T %+v", overview["worker_directions"], overview["worker_directions"])
	}
	if directions[0]["activity_id"] != f.activity.ID || directions[0]["intent_id"] != f.intentID ||
		directions[0]["intent_summary"] != "原始接口枚举方向" ||
		directions[0]["message"] != "停止接口枚举，立即聚焦登录后的水平越权" {
		t.Fatalf("worker direction overview=%+v", directions[0])
	}
}

func TestPlannerRequestIncludesWorkerMessageTriggerAndPersistedDirection(t *testing.T) {
	f := newDirectionAgentFixture(t)
	provider := &directionCaptureProvider{}
	planner := NewPlanner(provider, "test-model", t.TempDir(), nil, 0, 1)
	triggerDetail := "本轮只验证登录后的对象级授权"
	_, _, err := planner.Plan(context.Background(), f.task.ID, nil, f.store, f.task.Goal, []TriggerEvent{{
		Kind: TriggerWorkerMessage, ActivityID: f.activity.ID, IntentID: f.intentID, Detail: triggerDetail,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := lastUserText(t, provider.lastRequest(t))
	for _, want := range []string{"【本次触发本轮的实际变动】", triggerDetail, `"worker_directions"`, "停止接口枚举，立即聚焦登录后的水平越权"} {
		if !strings.Contains(input, want) {
			t.Fatalf("Planner input missing %q:\n%s", want, input)
		}
	}
}

func TestMainAgentNextTurnIncludesPersistedWorkerDirection(t *testing.T) {
	f := newDirectionAgentFixture(t)
	provider := &directionCaptureProvider{}
	mainAgent := NewMainAgent(provider, "test-model", t.TempDir(), nil, 0, 1)
	const userMessage = "汇报当前执行方向"
	if _, err := mainAgent.Chat(context.Background(), f.task.ID, nil, f.store, f.task.Goal, userMessage, nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	input := lastUserText(t, provider.lastRequest(t))
	for _, want := range []string{"【系统同步的 Worker 最新人工方向】", "停止接口枚举，立即聚焦登录后的水平越权", "【用户本轮消息】", userMessage} {
		if !strings.Contains(input, want) {
			t.Fatalf("Main Agent input missing %q:\n%s", want, input)
		}
	}
	if strings.Index(input, "【系统同步的 Worker 最新人工方向】") > strings.Index(input, "【用户本轮消息】") {
		t.Fatalf("Main Agent context is not separated before current user message:\n%s", input)
	}
}
