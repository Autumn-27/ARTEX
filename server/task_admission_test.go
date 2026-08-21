package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func newAdmissionTestServer(m *Manager, available func(*Task) bool) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := NewEngine(m)
	planner, worker := new(agent.Planner), new(agent.Worker)
	e.SetAuthoritativeAgentResolver(func(task *Task) (*agent.Planner, *agent.Worker) {
		if available == nil || available(task) {
			return planner, worker
		}
		return nil, nil
	})
	return &Server{m: m, engine: e, ctx: ctx, chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
}

func restoreConcurrencySetting(t *testing.T, m *Manager) func() {
	t.Helper()
	enabled, limit := m.ConcurrencyLimit()
	if limit < 1 {
		limit = defaultConcurrencyLimit
	}
	return func() {
		if err := m.SetConcurrency(enabled, limit); err != nil {
			t.Errorf("restore concurrency setting: %v", err)
		}
	}
}

func TestAdmitAlreadyRunningTaskDoesNotQueueAfterLimitDecrease(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	oldEnabled, oldLimit := m.ConcurrencyLimit()
	if oldLimit < 1 {
		oldLimit = defaultConcurrencyLimit
	}
	defer m.SetConcurrency(oldEnabled, oldLimit)
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	running, err := m.CreateTask("already admitted", "accept follow-up", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := m.CreateTask("other admitted task", "occupy lowered limit", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(running.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
	}()

	s := newAdmissionTestServer(m, nil)
	// Simulate a task whose long-lived loops were admitted before the limit was
	// lowered. The other live task makes the current count exceed the new limit.
	s.engine.started.Store(running.ID, true)
	queued, err := s.admitTask(running, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if queued || running.Queued {
		t.Fatalf("already-running task was re-queued: queued=%v task=%+v", queued, running)
	}
}

func TestAdmissionDoesNotOverwriteConcurrentTerminalStatus(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(false, defaultConcurrencyLimit); err != nil {
		t.Fatal(err)
	}

	task, err := m.CreateTask("stale admission", "preserve terminal writer", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	// Model an Engine terminal commit after admission captured its in-memory
	// snapshot but before the scheduler UPDATE. Direct DB use intentionally leaves
	// the live handle stale at its old status for this deterministic interleaving.
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	if err := m.pg.SetStatus(taskID, "done"); err != nil {
		t.Fatal(err)
	}
	s := newAdmissionTestServer(m, nil)
	if _, err := s.admitTask(task, "resume"); err == nil {
		t.Fatal("stale admission overwrote a concurrent terminal transition")
	}
	stored, err := m.pg.GetTask(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != "done" || stored.Queued || stored.Paused {
		t.Fatalf("terminal state changed by stale admission: %+v", stored)
	}
}

func TestAdmitKeepsQueuedBootstrapMode(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	oldEnabled, oldLimit := m.ConcurrencyLimit()
	if oldLimit < 1 {
		oldLimit = defaultConcurrencyLimit
	}
	defer m.SetConcurrency(oldEnabled, oldLimit)
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	holder, err := m.CreateTask("queue holder", "occupy slot", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	queuedTask, err := m.CreateTask("queued bootstrap", "must decompose goals", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(queuedTask.ID, DeleteTaskOptions{})
	}()
	if err := m.EnqueueTask(queuedTask.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	firstQueuedAt := queuedTask.QueuedAt

	s := newAdmissionTestServer(m, nil)
	queued, err := s.admitTask(queuedTask, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if !queued || queuedTask.QueueMode != "bootstrap" || queuedTask.QueuedAt != firstQueuedAt {
		t.Fatalf("re-admission changed bootstrap FIFO state: queued=%v task=%+v", queued, queuedTask)
	}
}

func TestTerminalTaskQueuedByAdmissionKeepsExecutionBarrier(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	holder, err := m.CreateTask("slot holder", "hold", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("terminal follow-up", "must wait", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "follow-up"}, 10, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}

	s := newAdmissionTestServer(m, nil)
	queued, err := s.admitTask(task, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if !queued || !task.Queued || task.Status != "running" || !s.engine.IsPaused(task.ID) {
		t.Fatalf("queued terminal admission lost gate: queued=%v task=%+v enginePaused=%v", queued, task, s.engine.IsPaused(task.ID))
	}
	if execCtx := s.engine.execContextFor(context.Background(), task.ID); execCtx.Err() == nil {
		t.Fatal("queued terminal task received a live execution context")
	}
	node, err := task.Store.GetNode(intentID)
	if err != nil || node == nil || node.State != "open" {
		t.Fatalf("queued intent changed before admission: node=%+v err=%v", node, err)
	}
}

func TestTimedOutTaskRevivalResetsClockAndSettlingAcrossFIFO(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	holder, err := m.CreateTask("timeout revival holder", "occupy slot", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("timed out follow-up", "run with a fresh clock", nil, 120, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	if _, err := m.StampTaskFirstRun(task.ID); err != nil {
		t.Fatal(err)
	}
	oldClock := task.lifecycleSnapshot()
	if oldClock.FirstRunAt == 0 || oldClock.DeadlineAt == 0 {
		t.Fatalf("test setup did not stamp the original clock: %+v", oldClock)
	}
	if err := m.SetTaskStatus(task.ID, "timeout"); err != nil {
		t.Fatal(err)
	}

	s := newAdmissionTestServer(m, nil)
	s.engine.settling.Store(task.ID, true)
	s.engine.deadline.Store(task.ID, oldClock.DeadlineAt)
	s.engine.stamped.Store(task.ID, true)
	s.engine.coordStarted.Store(task.ID, true)

	queued, err := s.admitTask(task, "resume")
	if err != nil {
		t.Fatal(err)
	}
	queuedState := task.lifecycleSnapshot()
	if !queued || !queuedState.Queued || queuedState.Status != "running" ||
		queuedState.FirstRunAt != 0 || queuedState.DeadlineAt != 0 {
		t.Fatalf("timed-out task was not reset before FIFO wait: queued=%v state=%+v", queued, queuedState)
	}
	if s.engine.isSettling(task.ID) {
		t.Fatal("timed-out task retained the old settling gate while queued")
	}
	if _, ok := s.engine.deadline.Load(task.ID); ok {
		t.Fatal("timed-out task retained the old in-memory deadline")
	}
	if _, ok := s.engine.stamped.Load(task.ID); ok {
		t.Fatal("timed-out task retained the old first-run stamp")
	}
	stored, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.TimeoutSeconds != 120 || stored.FirstRunAt != nil || stored.DeadlineAt != nil {
		t.Fatalf("persisted timeout revival clock/config mismatch: %+v", stored)
	}

	// Release the only slot and promote the revived task. The test server uses a
	// cancelled root context so no real Agent call races these state assertions.
	s.engine.Pause(holder.ID, agent.AbortPausedByUser)
	if err := m.ApplyTaskPause(holder.ID); err != nil {
		t.Fatal(err)
	}
	s.reconcileConcurrency()
	runningState := task.lifecycleSnapshot()
	if runningState.Queued || runningState.Paused || s.engine.IsPaused(task.ID) || s.engine.isSettling(task.ID) {
		t.Fatalf("revived task did not leave FIFO ready to execute: state=%+v enginePaused=%v settling=%v",
			runningState, s.engine.IsPaused(task.ID), s.engine.isSettling(task.ID))
	}
	if execCtx := s.engine.execContextFor(context.Background(), task.ID); execCtx.Err() != nil {
		t.Fatalf("revived task did not receive a live execution context: %v", context.Cause(execCtx))
	}
	if _, ok := s.engine.coordStarted.Load(task.ID); !ok {
		t.Fatal("revived timeout task did not start a fresh deadline coordinator")
	}
}

func TestQueuedTaskCanPauseAndResumeAtFIFOTail(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	holder, err := m.CreateTask("slot holder", "hold", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTask("pause queued", "resume later", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	s := newAdmissionTestServer(m, nil)
	if err := m.SetTaskPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(task.ID, agent.AbortPausedByUser)

	first, err := s.applyTaskControl(task, "resume")
	if err != nil || !first.Queued || task.QueuedAt == 0 {
		t.Fatalf("first resume: result=%+v task=%+v err=%v", first, task, err)
	}
	firstQueuedAt := task.QueuedAt
	if _, err := s.applyTaskControl(task, "pause"); err != nil {
		t.Fatal(err)
	}
	if !task.Paused || task.Queued || task.QueuedAt != 0 {
		t.Fatalf("pause did not leave queue: %+v", task)
	}
	time.Sleep(2 * time.Millisecond)
	second, err := s.applyTaskControl(task, "resume")
	if err != nil || !second.Queued {
		t.Fatalf("second resume: result=%+v task=%+v err=%v", second, task, err)
	}
	if task.QueuedAt <= firstQueuedAt {
		t.Fatalf("resumed task did not move to FIFO tail: first=%d second=%d", firstQueuedAt, task.QueuedAt)
	}
}

func TestReadyFIFOIsAdmittedBeforeNewTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 2); err != nil {
		t.Fatal(err)
	}

	holder, err := m.CreateTask("slot holder", "hold", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	older, err := m.CreateTask("older queued", "first", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	incoming, err := m.CreateTask("new admission", "second", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(holder.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(older.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(incoming.ID, DeleteTaskOptions{})
	}()
	s := newAdmissionTestServer(m, nil)
	if err := m.EnqueueTask(older.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(older.ID, agent.AbortPausedByOrchestrator)

	queued, err := s.admitTask(incoming, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if !queued || !incoming.Queued || incoming.QueuedAt <= older.QueuedAt {
		t.Fatalf("new task bypassed ready FIFO: queued=%v older=%+v incoming=%+v", queued, older, incoming)
	}
	s.reconcileConcurrency()
	if older.Queued || !incoming.Queued {
		t.Fatalf("FIFO promotion order wrong: older=%+v incoming=%+v", older, incoming)
	}
}

func TestUnavailableTaskReleasesSlotForReadyQueue(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	unavailable, err := m.CreateTask("unavailable llm", "park", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := m.CreateTask("ready queued", "run", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(unavailable.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(ready.ID, DeleteTaskOptions{})
	}()
	s := newAdmissionTestServer(m, func(task *Task) bool { return task.ID != unavailable.ID })
	s.engine.started.Store(unavailable.ID, true)
	if err := m.EnqueueTask(ready.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(ready.ID, agent.AbortPausedByOrchestrator)

	s.reconcileConcurrency()
	if !unavailable.Queued || !s.engine.IsPaused(unavailable.ID) {
		t.Fatalf("unavailable task still occupies slot: %+v", unavailable)
	}
	if ready.Queued || s.engine.IsPaused(ready.ID) {
		t.Fatalf("ready task was not promoted: %+v", ready)
	}
}

func TestUnavailableTaskWaitsForActiveLLMCallBeforeParking(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	defer restoreConcurrencySetting(t, m)()
	if err := m.SetConcurrency(true, 1); err != nil {
		t.Fatal(err)
	}

	active, err := m.CreateTask("active profile edit", "finish current call", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := m.CreateTask("waiting task", "respect active call", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(active.ID, DeleteTaskOptions{})
		_, _ = m.DeleteTask(waiting.ID, DeleteTaskOptions{})
	}()

	available := map[string]bool{active.ID: true, waiting.ID: true}
	s := newAdmissionTestServer(m, func(task *Task) bool { return available[task.ID] })
	s.engine.started.Store(active.ID, true)
	if err := m.EnqueueTask(waiting.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	s.engine.Pause(waiting.ID, agent.AbortPausedByOrchestrator)

	// Model a manual chain edit that makes the task's next-call resolution
	// unavailable while its old provider is still serving the current call.
	s.engine.BeginLLMCall(active.ID)
	available[active.ID] = false
	s.reconcileConcurrency()
	if active.Queued || s.engine.IsPaused(active.ID) {
		t.Fatalf("active LLM call was cancelled/parked after profile edit: %+v", active)
	}
	if !waiting.Queued {
		t.Fatalf("waiting task bypassed active provider call: %+v", waiting)
	}

	s.engine.EndLLMCall(active.ID)
	s.reconcileConcurrency()
	if !active.Queued || !s.engine.IsPaused(active.ID) {
		t.Fatalf("unavailable task was not parked after its call ended: %+v", active)
	}
	if waiting.Queued || s.engine.IsPaused(waiting.ID) {
		t.Fatalf("ready FIFO task was not admitted after active call ended: %+v", waiting)
	}
}

func TestRerunAdmissionFailureRestoresIntentState(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("failed rerun admission", "restore intent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() {
		_, _ = m.pg.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, taskID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	intentID, err := task.Store.AddIntent(map[string]any{"summary": "retry"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Store.SetIntentBlockedReason(intentID, "network_unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}

	s := newAdmissionTestServer(m, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/1/intents/1/rerun", nil)
	req.SetPathValue("id", task.ID)
	req.SetPathValue("iid", fmt.Sprintf("%d", intentID))
	rec := httptest.NewRecorder()
	s.rerunIntent(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	node, err := task.Store.GetNode(intentID)
	if err != nil || node == nil || node.State != "blocked" || node.BlockedReason != "network_unavailable" {
		t.Fatalf("rerun rollback: node=%+v err=%v", node, err)
	}
}

func TestTaskLLMResolutionRejectsInvalidExplicitProfile(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	profileID, err := m.pg.SaveProfile(&db.LLMProfile{
		Name:   fmt.Sprintf("invalid-resolution-%d", time.Now().UnixNano()),
		Format: "openai",
		Model:  "invalid-model",
		APIKey: "test-key",
		Proxy:  "invalid-proxy-without-scheme",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := m.CreateTaskWithOptions("invalid resolution", "report unavailable", db.TaskCreateOptions{
		LLMProfileIDs: []int64{profileID},
	})
	if err != nil {
		_ = m.pg.DeleteProfile(profileID)
		t.Fatal(err)
	}
	defer func() {
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
		_ = m.pg.DeleteProfile(profileID)
	}()

	s := &Server{m: m, provByProfile: map[int64]*provEntry{}}
	got, err := s.resolveTaskRoleLLM(task, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || got.Source != "task_chain" || got.ProfileID == nil || *got.ProfileID != profileID {
		t.Fatalf("resolution=%+v, want unavailable explicit task profile", got)
	}
}

func TestTaskLLMResolutionReportsDatabaseFailure(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("resolution storage failure", "return server error", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.pg.Close(); err != nil {
		t.Fatal(err)
	}

	s := &Server{m: m}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/1/llm/resolution", nil)
	req.SetPathValue("id", task.ID)
	rec := httptest.NewRecorder()
	s.taskLLMResolutionHandler(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenStatsWithoutTaskReturnsStableShape(t *testing.T) {
	s := &Server{m: &Manager{tasks: map[string]*Task{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/exploration/tokens?task=missing", nil)
	rec := httptest.NewRecorder()
	s.tokenStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Workers  []db.TokenUsage        `json:"workers"`
		Sessions []db.SessionTokenUsage `json:"sessions"`
		Total    TokenTotalDTO          `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Workers == nil || payload.Sessions == nil {
		t.Fatalf("empty collections must serialize as arrays: %s", rec.Body.String())
	}
}

func TestBatchControlLimitCountsDeduplicatedIDs(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	s := newAdmissionTestServer(m, nil)

	request := func(handler http.HandlerFunc, path string, pathValues map[string]string, payload any) *httptest.ResponseRecorder {
		body, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		for key, value := range pathValues {
			req.SetPathValue(key, value)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}
	rec := request(s.controlTasksBatch, "/api/tasks/control/batch", nil, map[string]any{
		"task_ids": []string{" 0009223372036854775807 ", "9223372036854775807", " bad-id ", "bad-id"},
		"action":   "pause",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("normalized task ids status=%d body=%s", rec.Code, rec.Body.String())
	}
	var normalizedPayload struct {
		Items []batchControlItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &normalizedPayload); err != nil {
		t.Fatal(err)
	}
	if len(normalizedPayload.Items) != 2 || normalizedPayload.Items[0].ID != "9223372036854775807" ||
		normalizedPayload.Items[1].ID != "bad-id" || normalizedPayload.Items[1].Error != "bad task id" {
		t.Fatalf("normalized task response=%+v", normalizedPayload.Items)
	}

	repeatedTasks := make([]string, maxBatchControlIDs+1)
	for i := range repeatedTasks {
		repeatedTasks[i] = "missing"
	}
	rec = request(s.controlTasksBatch, "/api/tasks/control/batch", nil, map[string]any{
		"task_ids": repeatedTasks,
		"action":   "pause",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("deduplicated task ids status=%d body=%s", rec.Code, rec.Body.String())
	}
	var taskPayload struct {
		Items []batchControlItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &taskPayload); err != nil {
		t.Fatal(err)
	}
	if len(taskPayload.Items) != 1 || taskPayload.Items[0].ID != "missing" {
		t.Fatalf("deduplicated task response=%+v", taskPayload.Items)
	}

	uniqueTasks := make([]string, maxBatchControlIDs+1)
	for i := range uniqueTasks {
		uniqueTasks[i] = fmt.Sprintf("missing-%d", i)
	}
	rec = request(s.controlTasksBatch, "/api/tasks/control/batch", nil, map[string]any{
		"task_ids": uniqueTasks,
		"action":   "pause",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unique task ids status=%d body=%s", rec.Code, rec.Body.String())
	}

}

func TestPauseTaskToolPersistsAndDequeuesTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("orchestrated pause", "persist queued pause", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()
	if err := m.EnqueueTask(task.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}

	s := newAdmissionTestServer(m, nil)
	s.engine.Pause(task.ID, agent.Causef("queued_for_admission", "queued", "test queue barrier"))
	input, err := json.Marshal(map[string]string{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolPauseTask().Call(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
	if !task.Paused || task.Queued || task.QueueMode != "bootstrap" || !s.engine.IsPaused(task.ID) {
		t.Fatalf("tool pause did not persist/dequeue task: %+v enginePaused=%v", task, s.engine.IsPaused(task.ID))
	}
	stored, err := m.pg.GetTask(mustTaskID(t, task.ID))
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || !stored.Paused || stored.Queued {
		t.Fatalf("stored task=%+v, want paused and dequeued", stored)
	}
}

func TestPauseTaskToolUsesOrchestratorCancellationCause(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("orchestrated pause cause", "keep audit reason", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.DeleteTask(task.ID, DeleteTaskOptions{}) }()

	s := newAdmissionTestServer(m, nil)
	execCtx := s.engine.execContextFor(context.Background(), task.ID)
	input, err := json.Marshal(map[string]string{"task_id": task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolPauseTask().Call(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
	if code, _, _, ok := agent.AbortReason(execCtx); !ok || code != "paused_by_orchestrator" {
		t.Fatalf("pause_task cancellation code=%q ok=%v, want paused_by_orchestrator", code, ok)
	}
}
