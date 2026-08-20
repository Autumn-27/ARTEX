package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type scriptedLLMProvider struct {
	events []llm.StreamEvent
	err    error
	calls  int
}

func (p *scriptedLLMProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.calls++
		for _, event := range p.events {
			if !yield(event, nil) {
				return
			}
		}
		if p.err != nil {
			yield(llm.StreamEvent{}, p.err)
		}
	}
}

func collectTaskLLMStream(seq iter.Seq2[llm.StreamEvent, error]) ([]llm.StreamEvent, error) {
	var events []llm.StreamEvent
	var streamErr error
	for event, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
		events = append(events, event)
	}
	return events, streamErr
}

func TestIsQuotaExhaustedError(t *testing.T) {
	t.Parallel()
	positive := []string{
		`openai: code=insufficient_quota status=429`,
		`provider error: quota_exceeded`,
		`grpc code=RESOURCE_EXHAUSTED`,
		`You exceeded your current quota, please check your plan and billing details.`,
		`billing_hard_limit_reached`,
		`billing_not_active`,
		`HTTP status 402 Payment Required`,
		`账户余额不足，请充值`,
		`credit balance is too low`,
	}
	for _, message := range positive {
		if !isQuotaExhaustedError(errors.New(message)) {
			t.Errorf("expected quota classification for %q", message)
		}
	}

	negative := []string{
		`HTTP status 429: rate limit exceeded`,
		`RESOURCE_EXHAUSTED: rate limit exceeded`,
		`HTTP status 429: quota exceeded for quota metric GenerateRequestsPerMinutePerProjectPerBaseModel`,
		`RESOURCE_EXHAUSTED: TPM quota exceeded`,
		`HTTP status 401: invalid api key`,
		`HTTP status 401: insufficient_quota`,
		`HTTP status 403: forbidden`,
		`HTTP status 403: billing_hard_limit_reached`,
		`HTTP status 500: internal server error`,
		`HTTP status 503: quota_exceeded`,
		`dial tcp: network is unreachable`,
		`context length exceeded`,
		`invalid request parameter`,
	}
	for _, message := range negative {
		if isQuotaExhaustedError(errors.New(message)) {
			t.Errorf("unexpected quota classification for %q", message)
		}
	}
	if isQuotaExhaustedError(nil) {
		t.Error("nil error must not be classified as quota exhaustion")
	}
}

func TestTaskDTOUsesEmptyArrays(t *testing.T) {
	t.Parallel()
	dto := taskDTO(&Task{ID: "7"}, "created")
	raw, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"source_task_ids", "llm_profile_ids"} {
		value, ok := payload[field].([]any)
		if !ok || len(value) != 0 {
			t.Fatalf("%s must serialize as [], payload=%s", field, raw)
		}
	}
}

func TestTaskLLMStateSnapshotIsConcurrentAndDetached(t *testing.T) {
	t.Parallel()
	task := &Task{ID: "7"}
	var wg sync.WaitGroup
	for writer := 0; writer < 2; writer++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				id := int64(offset*1000 + i + 1)
				task.setLLMState(&id, &id, []int64{id, id + 1}, "ready", "")
			}
		}(writer)
	}
	for reader := 0; reader < 4; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				state := task.llmStateSnapshot()
				if len(state.ProfileIDs) > 0 {
					state.ProfileIDs[0] = -1
				}
				_ = taskDTO(task, "running")
			}
		}()
	}
	wg.Wait()
	state := task.llmStateSnapshot()
	if len(state.ProfileIDs) != 2 || state.ProfileIDs[0] <= 0 {
		t.Fatalf("snapshot mutation leaked into task state: %+v", state)
	}
}

func TestTaskLLMStreamRetriesPreStreamQuotaOnNextProfile(t *testing.T) {
	t.Parallel()
	quota := errors.New("openai: status 402: insufficient_quota")
	first := &scriptedLLMProvider{err: quota}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "fallback"}}}
	active := int64(11)
	transitions := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(selection taskLLMSelection, _ error) (db.TaskLLMTransition, error) {
			if selection.profileID != 11 {
				t.Fatalf("unexpected exhausted profile %d", selection.profileID)
			}
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
		transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Text != "fallback" {
		t.Fatalf("unexpected fallback events: %+v", events)
	}
	if first.calls != 1 || second.calls != 1 || transitions != 1 {
		t.Fatalf("calls first=%d second=%d transitions=%d", first.calls, second.calls, transitions)
	}
}

func TestTaskLLMStreamRetriesAfterNonContentStartEvent(t *testing.T) {
	t.Parallel()
	quota := errors.New("anthropic: status 402: insufficient credit balance")
	first := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SEMessageStart}},
		err:    quota,
	}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{
		{Type: llm.SEMessageStart},
		{Type: llm.SETextDelta, Text: "fallback"},
	}}
	active := int64(11)
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != llm.SEMessageStart || events[1].Text != "fallback" {
		t.Fatalf("non-content event from failed attempt leaked or fallback missing: %+v", events)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls first=%d second=%d", first.calls, second.calls)
	}
}

func TestTaskLLMStreamRetriesStalePreStreamFailureAgainstReplacementChain(t *testing.T) {
	t.Parallel()
	first := &scriptedLLMProvider{err: errors.New("status 402: insufficient_quota")}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "replacement"}}}
	current := first
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			profileID := int64(11)
			if current == second {
				profileID = 22
			}
			return taskLLMSelection{profileID: profileID, provider: current}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			current = second
			return db.TaskLLMTransition{PreviousProfileID: 11, Stale: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if err != nil || len(events) != 1 || events[0].Text != "replacement" {
		t.Fatalf("stale call did not continue on replacement chain: events=%+v err=%v", events, err)
	}
}

func TestTaskLLMStreamDoesNotSwitchForOrdinaryErrors(t *testing.T) {
	t.Parallel()
	tests := []string{
		"openai: status 429: rate limit exceeded",
		"openai: status 401: invalid api key",
		"openai: status 500: internal server error",
		"dial tcp: network is unreachable",
	}
	for _, message := range tests {
		t.Run(message, func(t *testing.T) {
			provider := &scriptedLLMProvider{err: errors.New(message)}
			exhausted := 0
			hooks := taskLLMStreamHooks{
				current: func() (taskLLMSelection, error) {
					return taskLLMSelection{profileID: 11, provider: provider}, nil
				},
				exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
					exhausted++
					return db.TaskLLMTransition{}, nil
				},
			}
			_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
			if err == nil || err.Error() != message {
				t.Fatalf("error=%v, want %q", err, message)
			}
			if exhausted != 0 {
				t.Fatalf("ordinary error changed failover state %d times", exhausted)
			}
		})
	}
}

func TestTaskLLMStreamAdvancesAfterMidStreamQuotaWithoutReplay(t *testing.T) {
	t.Parallel()
	quota := errors.New("anthropic: status 402: credit balance is too low")
	first := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "partial"}},
		err:    quota,
	}
	second := &scriptedLLMProvider{events: []llm.StreamEvent{{Type: llm.SETextDelta, Text: "next-call"}}}
	active := int64(11)
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			if active == 11 {
				return taskLLMSelection{profileID: 11, provider: first}, nil
			}
			return taskLLMSelection{profileID: 22, provider: second}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			active = 22
			next := active
			return db.TaskLLMTransition{PreviousProfileID: 11, NextProfileID: &next, Advanced: true}, nil
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if len(events) != 1 || events[0].Text != "partial" || err == nil {
		t.Fatalf("mid-stream result events=%+v err=%v", events, err)
	}
	if first.calls != 1 || second.calls != 0 {
		t.Fatalf("mid-stream call was replayed: first=%d second=%d", first.calls, second.calls)
	}

	nextEvents, nextErr := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if nextErr != nil || len(nextEvents) != 1 || nextEvents[0].Text != "next-call" || second.calls != 1 {
		t.Fatalf("next call did not use fallback: events=%+v err=%v calls=%d", nextEvents, nextErr, second.calls)
	}
}

func TestTaskLLMStreamMarkFailureAfterToolStartSkipsWorkerReplay(t *testing.T) {
	t.Parallel()
	quota := errors.New("openai: status 402: insufficient_quota")
	markFailure := errors.New("database temporarily unavailable")
	provider := &scriptedLLMProvider{
		events: []llm.StreamEvent{{Type: llm.SEToolUseStart}},
		err:    quota,
	}
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{}, markFailure
		},
	}

	events, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if len(events) != 1 || events[0].Type != llm.SEToolUseStart || err == nil {
		t.Fatalf("committed tool stream events=%+v err=%v", events, err)
	}
	if !isTaskLLMRuntimeError(err) {
		t.Fatalf("committed mark failure must be router-classified: %T %v", err, err)
	}
	if retryableWorkerModelError(harness.ReasonModelError, err) {
		t.Fatal("worker must not replay after a committed tool stream when quota persistence fails")
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d, want 1", provider.calls)
	}
}

func TestTaskLLMRuntimeErrorSkipsWorkerReplay(t *testing.T) {
	partialStreamErr := &taskLLMError{
		taskID:         "7",
		chainExhausted: false,
		cause:          errors.New("insufficient_quota after partial output"),
	}
	if retryableWorkerModelError(harness.ReasonModelError, partialStreamErr) {
		t.Fatal("worker must not replay an intent after the task router handled a mid-stream quota error")
	}
	if !retryableWorkerModelError(harness.ReasonModelError, errors.New("temporary network error")) {
		t.Fatal("ordinary model errors should retain the existing worker retry behavior")
	}
}

func TestEngineReadyForTaskOverride(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	if e.Ready() {
		t.Fatal("engine without a global provider must not be globally ready")
	}
	e.SetAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return &agent.Planner{}, &agent.Worker{}
	})
	if !e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("task-specific provider chain must make that task ready")
	}
}

func TestResolvedTaskStatusKeepsExhaustedStartedTaskRunning(t *testing.T) {
	t.Parallel()
	engine := NewEngine(nil)
	engine.started.Store("7", true)
	task := &Task{ID: "7", Status: "running"}
	profileID := int64(11)
	task.setLLMState(&profileID, nil, []int64{profileID}, "chain_exhausted", "quota exhausted")
	s := &Server{engine: engine}
	if got := s.resolvedTaskStatus(task); got != "running" {
		t.Fatalf("exhausted started status=%q, want running", got)
	}
	task.Paused = true
	if got := s.resolvedTaskStatus(task); got != "paused" {
		t.Fatalf("paused exhausted status=%q, want paused", got)
	}
}

func TestAuthoritativeTaskResolverDoesNotFallBackWhenChainUnavailable(t *testing.T) {
	t.Parallel()
	e := &Engine{}
	e.UseLLM(&agent.Planner{}, &agent.Worker{})
	if !e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("global provider should initially make task ready")
	}
	e.SetAuthoritativeAgentResolver(func(*Task) (*agent.Planner, *agent.Worker) {
		return nil, nil
	})
	if e.ReadyFor(&Task{ID: "7"}) {
		t.Fatal("unavailable authoritative task chain must not fall back to global provider")
	}
}

func TestTaskLLMStreamStopsWhenChainExhausted(t *testing.T) {
	t.Parallel()
	provider := &scriptedLLMProvider{err: errors.New("billing_not_active")}
	transitions := 0
	hooks := taskLLMStreamHooks{
		current: func() (taskLLMSelection, error) {
			return taskLLMSelection{profileID: 11, provider: provider}, nil
		},
		exhaust: func(taskLLMSelection, error) (db.TaskLLMTransition, error) {
			return db.TaskLLMTransition{PreviousProfileID: 11, Advanced: true, ChainExhausted: true}, nil
		},
		transition: func(taskLLMSelection, db.TaskLLMTransition, error) { transitions++ },
	}

	_, err := collectTaskLLMStream(streamTaskLLM(context.Background(), "7", llm.CompletionRequest{}, hooks))
	if !isTaskLLMChainExhausted(err) {
		t.Fatalf("expected chain-exhausted error, got %v", err)
	}
	if provider.calls != 1 || transitions != 1 {
		t.Fatalf("calls=%d transitions=%d", provider.calls, transitions)
	}
}

func TestProfileDeleteRestoresQuotaBlockedIntentWhenFallbackAvailable(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:   fmt.Sprintf("delete-quota-fallback-%d", time.Now().UnixNano()),
		Format: "openai",
		Model:  "test-model",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTaskWithOptions("quota fallback", "resume intent", db.TaskCreateOptions{
		LLMProfileIDs: []int64{profileID},
	})
	if err != nil {
		_ = m.pg.DeleteProfile(profileID)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		_ = m.pg.DeleteProfile(profileID)
	})

	intentID, err := task.Store.AddIntent(map[string]any{"action": "retry after fallback"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := parseTaskID(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if transition, err := m.pg.MarkTaskLLMProfileQuotaExhausted(taskID, profileID, "insufficient_quota"); err != nil || !transition.ChainExhausted {
		t.Fatalf("exhaust task chain: transition=%+v err=%v", transition, err)
	}
	if err := task.Store.SetIntentBlockedReason(intentID, db.IntentBlockedLLMQuota); err != nil {
		t.Fatal(err)
	}
	task.Paused = true
	if err := m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}

	// Removing the final explicit entry restores the legacy/global fallback. The
	// post-delete sync must reopen only quota-blocked work without unpausing it.
	if err := m.pg.DeleteProfile(profileID); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		m:             m,
		llmOn:         true,
		llmProv:       &scriptedLLMProvider{},
		provByProfile: map[int64]*provEntry{},
	}
	s.restoreTasksAfterProfileDelete(m.pg)

	node, err := task.Store.GetNode(intentID)
	if err != nil {
		t.Fatal(err)
	}
	if node == nil || node.State != "open" || node.BlockedReason != "" {
		t.Fatalf("quota-blocked intent was not reopened: %+v", node)
	}
	if !task.Paused {
		t.Fatal("profile deletion must not resume a paused task")
	}
	state := task.llmStateSnapshot()
	if len(state.ProfileIDs) != 0 || state.FailoverState != "default" {
		t.Fatalf("task did not return to fallback state: %+v", state)
	}
}
