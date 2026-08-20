package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
)

func TestEngineDeleteBarrierRejectsNewTaskOperations(t *testing.T) {
	e := NewEngine(nil)
	const taskID = "42"

	if !e.beginTaskOperation(taskID) {
		t.Fatal("first task operation should be admitted")
	}
	if !e.BeginDelete(taskID) {
		t.Fatal("first delete should install the barrier")
	}
	if !e.IsDeleting(taskID) {
		t.Fatal("delete barrier should be visible")
	}
	if !e.IsPaused(taskID) {
		t.Fatal("delete should temporarily pause task execution")
	}
	if e.beginTaskOperation(taskID) {
		t.Fatal("operation started after BeginDelete returned")
	}
	if got := e.inflightCount(taskID); got != 1 {
		t.Fatalf("existing operation must remain drainable, inflight=%d", got)
	}
	e.decInflight(taskID)

	if e.BeginDelete(taskID) {
		t.Fatal("duplicate delete must not acquire the barrier")
	}
	e.AbortDelete(taskID)
	if e.IsPaused(taskID) {
		t.Fatal("aborted delete should restore a previously running task")
	}
	if !e.beginTaskOperation(taskID) {
		t.Fatal("aborting deletion should reopen operation admission")
	}
	e.decInflight(taskID)
}

func TestAbortDeletePreservesExistingPause(t *testing.T) {
	e := NewEngine(nil)
	e.Pause("42", agent.AbortPausedByUser)
	if !e.BeginDelete("42") {
		t.Fatal("delete barrier should be installed")
	}
	e.AbortDelete("42")
	if !e.IsPaused("42") {
		t.Fatal("aborted delete must preserve a pre-existing user pause")
	}
}

func TestStopTaskCancelsRuntimeAndClearsLifecycleState(t *testing.T) {
	e := NewEngine(nil)
	const taskID = "42"
	e.deleteMu.RLock()
	rt := e.registerTaskRoutines(context.Background(), taskID, 1)
	e.started.Store(taskID, true)
	e.lastAct.Store(taskID, int64(1))
	e.paused.Store(taskID, true)
	e.plannerRound.Store(taskID, 3)
	e.coordStarted.Store(taskID, true)
	e.deleteMu.RUnlock()

	done := make(chan struct{})
	runTaskRoutine(rt, func(ctx context.Context) {
		<-ctx.Done()
		close(done)
	})
	e.StopTask(taskID)

	select {
	case <-done:
	default:
		t.Fatal("task runtime was not cancelled before StopTask returned")
	}
	if e.Started(taskID) || e.IsPaused(taskID) || e.IsDeleting(taskID) {
		t.Fatalf("task lifecycle maps were not cleared: started=%v paused=%v deleting=%v",
			e.Started(taskID), e.IsPaused(taskID), e.IsDeleting(taskID))
	}
	if _, ok := e.lastAct.Load(taskID); ok {
		t.Fatal("last activity state was retained")
	}
	if _, ok := e.plannerRound.Load(taskID); ok {
		t.Fatal("planner round state was retained")
	}
	if _, ok := e.coordStarted.Load(taskID); ok {
		t.Fatal("deadline coordinator state was retained")
	}
	e.runtimeMu.Lock()
	_, retained := e.runtimes[taskID]
	e.runtimeMu.Unlock()
	if retained {
		t.Fatal("task runtime retained after StopTask")
	}
}

func TestCanonicalTaskID(t *testing.T) {
	for raw, want := range map[string]string{"42": "42", "00042": "42", "+42": "42"} {
		if got, ok := canonicalTaskID(raw); !ok || got != want {
			t.Fatalf("canonicalTaskID(%q)=(%q,%v), want (%q,true)", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "0", "-1", "abc", "42x"} {
		if got, ok := canonicalTaskID(raw); ok {
			t.Fatalf("canonicalTaskID(%q)=(%q,true), want invalid", raw, got)
		}
	}
}

func TestCancelChatThenWaitTaskQuiescent(t *testing.T) {
	e := NewEngine(nil)
	chatCtx, chatCancel := context.WithCancelCause(context.Background())
	s := &Server{
		engine:     e,
		chatBusy:   map[string]bool{"7": true},
		chatCancel: map[string]context.CancelCauseFunc{"7": chatCancel},
	}
	if !e.beginTaskOperation("7") {
		t.Fatal("task operation should be admitted")
	}

	cancelObserved := make(chan struct{})
	releaseChat := make(chan struct{})
	go func() {
		<-chatCtx.Done()
		close(cancelObserved)
		<-releaseChat
		s.finishTaskChat("7", chatCancel)
	}()
	if !s.cancelTaskChat("7", agent.AbortTaskDeleted) {
		t.Fatal("expected active chat to be cancelled")
	}
	<-cancelObserved

	waitDone := make(chan error, 1)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	go func() { waitDone <- s.waitTaskQuiescent(waitCtx, "7") }()

	select {
	case err := <-waitDone:
		t.Fatalf("wait returned while chat and engine operation were active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseChat)
	select {
	case err := <-waitDone:
		t.Fatalf("wait returned while engine operation was active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	e.decInflight("7")
	if err := <-waitDone; err != nil {
		t.Fatalf("wait for quiescence: %v", err)
	}
}

func TestWaitTaskQuiescentHonorsContext(t *testing.T) {
	s := &Server{
		engine:     NewEngine(nil),
		chatBusy:   map[string]bool{"9": true},
		chatCancel: map[string]context.CancelCauseFunc{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.waitTaskQuiescent(ctx, "9"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestTaskChatUploadRejectsDeleteBarrierBeforeCreatingDirectory(t *testing.T) {
	dataDir := t.TempDir()
	m := &Manager{dir: dataDir, tasks: map[string]*Task{"7": {ID: "7"}}}
	s := &Server{m: m, engine: NewEngine(m)}
	if !s.engine.BeginDelete("7") {
		t.Fatal("delete barrier should be installed")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/chat/upload?scope=task&id=7", nil)
	rec := httptest.NewRecorder()
	s.chatUpload(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while deleting, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tasks", "7")); !os.IsNotExist(err) {
		t.Fatalf("upload must not recreate task directory, stat err=%v", err)
	}
}

func TestCommittedTaskDeleteReturnsSuccessWithCleanupWarning(t *testing.T) {
	rec := httptest.NewRecorder()
	result := DeleteTaskResult{Deleted: "7", FilesDeleted: true}
	err := &taskDeleteCommittedError{err: errors.New("remove staged task files: permission denied")}

	writeCommittedTaskDelete(rec, result, err)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var got DeleteTaskResult
	if decodeErr := json.NewDecoder(rec.Body).Decode(&got); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if got.Deleted != "7" || !got.FilesDeleted || !strings.Contains(got.CleanupWarning, "cleanup incomplete") {
		t.Fatalf("unexpected response: %+v", got)
	}
}
