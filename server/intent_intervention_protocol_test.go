package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type workerMessageTestProvider struct{}

func (workerMessageTestProvider) Stream(_ context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "已按新的对话意图继续执行"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageDelta, StopReason: "end_turn"}, nil)
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}
}

func (p workerMessageTestProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	acc := llm.NewAccumulator()
	for event, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
		acc.Add(event)
	}
	return acc.Message(), acc.StopReason, acc.Usage, nil
}

type interventionProtocolFixture struct {
	m        *Manager
	task     *Task
	engine   *Engine
	worker   *agent.Worker
	tx       *transcript.Store
	intentID int64
}

func newInterventionProtocolFixture(t *testing.T, targetPriority int) *interventionProtocolFixture {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	task, err := m.CreateTask("worker message protocol", "verify immediate conversation handoff", nil, 0, 0)
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "target worker intent"}, targetPriority, nil, "planner")
	if err != nil {
		m.Close()
		t.Fatal(err)
	}
	if claimed, err := task.Store.ClaimIntent(intentID, "work#1"); err != nil || !claimed {
		m.Close()
		t.Fatalf("claim target intent: claimed=%v err=%v", claimed, err)
	}
	tx := transcript.NewStore(m.dir + "/transcripts")
	worker := agent.NewWorker(workerMessageTestProvider{}, "test-model", m.dir, tx, 0, 1)
	engine := NewEngine(m)
	engine.UseLLM(nil, worker)
	fixture := &interventionProtocolFixture{m: m, task: task, engine: engine, worker: worker, tx: tx, intentID: intentID}
	t.Cleanup(func() {
		taskID, _ := strconv.ParseInt(task.ID, 10, 64)
		_ = m.pg.DeleteTask(taskID)
		_ = m.Close()
	})
	return fixture
}

// settleRegisteredIntervention emulates the ordering inside runWorkerStep after
// a cancellation-aware provider returns: detach the live work handle, commit the
// running->paused final state, install the directed hand-off, then release the
// controller waiting on workExecution.done.
func settleRegisteredIntervention(t *testing.T, f *interventionProtocolFixture) string {
	t.Helper()
	action, complete := f.engine.detachWork(f.intentID)
	if action != "intervene" {
		t.Fatalf("settled action=%q, want intervene", action)
	}
	if changed, err := f.task.Store.CompareAndSetIntentState(f.intentID, "running", "paused"); err != nil || !changed {
		t.Fatalf("settle running->paused: changed=%v err=%v", changed, err)
	}
	f.engine.queueDirectedClaim(f.task.ID, f.intentID)
	complete(nil)
	return action
}

func countTranscriptWorkerMessages(t *testing.T, tx *transcript.Store, sessionID, requestID string) (int, []transcript.Record) {
	t.Helper()
	records, err := tx.Load(tx.MainPath(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	marker := "ARTEX_WORKER_CHAT:" + requestID
	count := 0
	for _, record := range records {
		if record.Message != nil && strings.Contains(record.Message.Text(), marker) {
			count++
		}
	}
	return count, records
}

func TestWorkerMessageCancelsBeforeAcceptAndContinuesSameSessionFirst(t *testing.T) {
	f := newInterventionProtocolFixture(t, 1)
	competitorID, err := f.task.Store.AddIntent(map[string]any{"summary": "higher priority competitor"}, 100, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-seed the same stable session so the message must continue its UUID
	// chain rather than creating a worker-slot-specific conversation.
	sessionID := agent.WorkerSessionID(f.task.ExpID, f.intentID)
	seed := f.tx.NewWriter(sessionID, "")
	seed.RecordMessage(llm.UserText("original intent turn"), llm.Usage{})
	if err := seed.Err(); err != nil {
		t.Fatal(err)
	}
	before, err := f.tx.Load(f.tx.MainPath(sessionID))
	if err != nil || len(before) != 1 {
		t.Fatalf("seed transcript records=%d err=%v", len(before), err)
	}

	workCtx, cancelWork := context.WithCancelCause(context.Background())
	f.engine.registerWork(f.intentID, cancelWork)
	live, unsubscribe := f.engine.Broadcaster().Subscribe(f.task.ID)
	defer unsubscribe()

	const requestID = "worker-message-engine-1"
	const message = "停止主动枚举，立即验证登录越权"
	type outcome struct {
		result WorkInterventionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.engine.InterveneWork(context.Background(), f.task, f.intentID, requestID, message)
		done <- outcome{result: result, err: err}
	}()

	select {
	case <-workCtx.Done():
		if code, _, _, _ := agent.AbortReason(workCtx); code != "work_intervened_by_user" {
			t.Fatalf("old execution cancel code=%q, want work_intervened_by_user", code)
		}
	case <-time.After(time.Second):
		t.Fatal("Worker message did not immediately cancel the old execution")
	}
	select {
	case early := <-done:
		t.Fatalf("Worker message accepted before old worker settled: %+v", early)
	case <-time.After(25 * time.Millisecond):
	}
	pending, err := f.task.Store.IntentInterventionByRequest(f.intentID, requestID)
	if err != nil || pending == nil || pending.Status != db.IntentInterventionPending {
		t.Fatalf("durable pending reservation=%+v err=%v", pending, err)
	}
	settleRegisteredIntervention(t, f)

	var accepted outcome
	select {
	case accepted = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for accepted Worker message")
	}
	if accepted.err != nil || accepted.result.ActivityID <= pending.ActivityID || accepted.result.Duplicate || accepted.result.State != "open" {
		t.Fatalf("accepted Worker message=%+v control=%+v err=%v", accepted.result, pending, accepted.err)
	}
	node, err := f.task.Store.GetNode(f.intentID)
	if err != nil || node == nil || node.State != "open" {
		t.Fatalf("reopened target=%+v err=%v", node, err)
	}

	// The API publishes the visible user activity but never edits transcript files.
	// agentcore appends the standard user turn only after the same Worker is claimed.
	count, records := countTranscriptWorkerMessages(t, f.tx, sessionID, requestID)
	if count != 0 || len(records) != 1 {
		t.Fatalf("HTTP handler edited Worker transcript: count=%d records=%+v", count, records)
	}
	select {
	case activity := <-live:
		if activity.ID != accepted.result.ActivityID || activity.NodeID == nil || *activity.NodeID != f.intentID ||
			activity.Worker != "user" || activity.Kind != "user" || activity.Summary != message {
			t.Fatalf("broadcast activity=%+v", activity)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted Worker message was not broadcast")
	}
	select {
	case duplicate := <-live:
		t.Fatalf("accepted Worker message broadcast twice: %+v", duplicate)
	default:
	}

	// The directed hand-off must outrank the normal frontier without changing the
	// persisted priority. This catches the one-worker race where the released slot
	// used to take an unrelated, higher-priority intent before the target reopened.
	claimed := f.engine.claimNext(f.task, "work#2")
	if claimed == nil || claimed.ID != f.intentID {
		t.Fatalf("directed claim=%+v, want target %d ahead of competitor %d", claimed, f.intentID, competitorID)
	}
	competitor, err := f.task.Store.GetNode(competitorID)
	if err != nil || competitor == nil || competitor.State != "open" {
		t.Fatalf("competitor was claimed before Worker message target: node=%+v err=%v", competitor, err)
	}
	request, nextMessage, hasMessage, err := f.task.Store.PendingIntentInterventionMessage(f.intentID)
	if err != nil || !hasMessage || request != requestID || nextMessage != message {
		t.Fatalf("pending Worker message request=%q message=%q present=%v err=%v", request, nextMessage, hasMessage, err)
	}
	taskID, _ := strconv.ParseInt(f.task.ID, 10, 64)
	if _, _, err := f.worker.ExecuteWithMessage(context.Background(), "work#2", taskID, nil, f.task.Store,
		claimed, nil, nil, nil, nil, request, nextMessage); err != nil {
		t.Fatalf("continue Worker conversation: %v", err)
	}
	if err := f.task.Store.MarkIntentInterventionHandoffClaimed(f.intentID); err != nil {
		t.Fatal(err)
	}
	count, records = countTranscriptWorkerMessages(t, f.tx, sessionID, requestID)
	if count != 1 || len(records) != 3 || records[1].ParentUUID != records[0].UUID || records[2].ParentUUID != records[1].UUID {
		t.Fatalf("Worker message did not form one normal chained turn: count=%d records=%+v", count, records)
	}
	if records[1].Message == nil || records[1].Message.Role != llm.RoleUser || !strings.Contains(records[1].Message.Text(), message) {
		t.Fatalf("Worker message was not recorded as the next user turn: %+v", records[1])
	}
	if _, _, pendingAfterRun, err := f.task.Store.PendingIntentInterventionMessage(f.intentID); err != nil || pendingAfterRun {
		t.Fatalf("Worker message handoff remains pending=%v err=%v", pendingAfterRun, err)
	}

	retry, err := f.engine.InterveneWork(context.Background(), f.task, f.intentID, requestID, message)
	if err != nil || !retry.Duplicate || retry.ActivityID != accepted.result.ActivityID {
		t.Fatalf("idempotent engine retry=%+v err=%v", retry, err)
	}
	if _, err := f.engine.InterveneWork(context.Background(), f.task, f.intentID, requestID, "同 request_id 的另一条消息"); !errors.Is(err, errWorkInterventionConflict) {
		t.Fatalf("same request_id different message error=%v, want conflict", err)
	}
	count, _ = countTranscriptWorkerMessages(t, f.tx, sessionID, requestID)
	if count != 1 {
		t.Fatalf("engine retries appended %d Worker messages, want 1", count)
	}
}

func TestSendWorkerMessageContinuesAfterRequestContextCancellation(t *testing.T) {
	f := newInterventionProtocolFixture(t, 1)
	workCtx, cancelWork := context.WithCancelCause(context.Background())
	f.engine.registerWork(f.intentID, cancelWork)
	s := &Server{m: f.m, engine: f.engine, ctx: context.Background()}

	requestBody, _ := json.Marshal(map[string]string{
		"message": "中止当前动作，只验证权限边界", "request_id": "worker-message-disconnect-1",
	})
	requestCtx, disconnect := context.WithCancel(context.Background())
	disconnect()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/1/intents/1/messages", bytes.NewReader(requestBody)).WithContext(requestCtx)
	req.SetPathValue("id", f.task.ID)
	req.SetPathValue("iid", strconv.FormatInt(f.intentID, 10))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		s.sendWorkerMessage(recorder, req)
		close(done)
	}()

	select {
	case <-workCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnected request did not continue to cancel the worker")
	}
	settleRegisteredIntervention(t, f)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server-owned Worker message did not finish after browser disconnect")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("disconnected Worker message status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	accepted, err := f.task.Store.IntentInterventionByRequest(f.intentID, "worker-message-disconnect-1")
	if err != nil || accepted == nil || accepted.Status != db.IntentInterventionAccepted {
		t.Fatalf("disconnected request acceptance=%+v err=%v", accepted, err)
	}
}

func TestWorkerMessageRejectsUnavailableTaskStatesWithoutReservation(t *testing.T) {
	cases := []struct {
		name  string
		block func(*interventionProtocolFixture)
	}{
		{name: "paused", block: func(f *interventionProtocolFixture) {
			f.task.updateLifecycle(func(state *taskLifecycleState) { state.Paused = true })
			f.engine.Pause(f.task.ID, agent.AbortPausedByUser)
		}},
		{name: "queued", block: func(f *interventionProtocolFixture) {
			f.task.updateLifecycle(func(state *taskLifecycleState) { state.Queued = true })
		}},
		{name: "terminal", block: func(f *interventionProtocolFixture) {
			f.task.updateLifecycle(func(state *taskLifecycleState) { state.Status = "done" })
		}},
		{name: "settling", block: func(f *interventionProtocolFixture) { f.engine.markSettling(f.task.ID) }},
		{name: "deleting", block: func(f *interventionProtocolFixture) { f.engine.BeginDelete(f.task.ID) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInterventionProtocolFixture(t, 1)
			tc.block(f)
			requestID := "worker-message-unavailable-" + tc.name
			if _, err := f.engine.InterveneWork(context.Background(), f.task, f.intentID, requestID, "must not persist"); !errors.Is(err, errWorkInterventionConflict) {
				t.Fatalf("unavailable state error=%v, want conflict", err)
			}
			reservation, err := f.task.Store.IntentInterventionByRequest(f.intentID, requestID)
			if err != nil || reservation != nil {
				t.Fatalf("unavailable state persisted reservation=%+v err=%v", reservation, err)
			}
			node, err := f.task.Store.GetNode(f.intentID)
			if err != nil || node == nil || node.State != "running" {
				t.Fatalf("unavailable state mutated intent=%+v err=%v", node, err)
			}
			count, _ := countTranscriptWorkerMessages(t, f.tx, agent.WorkerSessionID(f.task.ExpID, f.intentID), requestID)
			if count != 0 {
				t.Fatalf("unavailable state appended %d transcript interventions", count)
			}
		})
	}
}

func TestWorkerWakeInterruptsLongIdlePoll(t *testing.T) {
	task := &Task{workerWake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	returned := make(chan bool, 1)
	go func() {
		close(started)
		returned <- waitWorker(ctx, task, 5*time.Second)
	}()
	<-started
	start := time.Now()
	task.NotifyWorker()
	select {
	case stopped := <-returned:
		if stopped {
			t.Fatal("worker wake was mistaken for context shutdown")
		}
		if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
			t.Fatalf("worker wake took %s, want immediate wake", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("worker wake waited for the idle polling interval")
	}
}

func TestSendWorkerMessageRejectsOversizedBodyAsEntityTooLarge(t *testing.T) {
	f := newInterventionProtocolFixture(t, 1)
	s := &Server{m: f.m, engine: f.engine, ctx: context.Background()}
	body := `{"message":"` + strings.Repeat("x", maxWorkerMessageBytes) + `","request_id":"oversized"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/1/intents/1/messages", strings.NewReader(body))
	req.SetPathValue("id", f.task.ID)
	req.SetPathValue("iid", strconv.FormatInt(f.intentID, 10))
	recorder := httptest.NewRecorder()
	s.sendWorkerMessage(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized Worker message status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
