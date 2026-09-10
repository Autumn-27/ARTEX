package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

type sideHTTPProvider struct {
	started chan llm.CompletionRequest
	release chan struct{}
	summary func(context.Context, llm.CompletionRequest) (llm.Message, llm.Usage, error)
}

func (p *sideHTTPProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(y func(llm.StreamEvent, error) bool) {
		p.started <- req
		if !y(llm.StreamEvent{Type: llm.SEMessageStart, Usage: llm.Usage{InputTokens: 19}}, nil) {
			return
		}
		if !y(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial answer"}, nil) {
			return
		}
		select {
		case <-p.release:
			y(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
		case <-ctx.Done():
			y(llm.StreamEvent{}, ctx.Err())
		}
	}
}
func (p *sideHTTPProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	if req.Thinking == "disabled" && p.summary != nil {
		msg, usage, err := p.summary(ctx, req)
		return msg, "end_turn", usage, err
	}
	for _, err := range p.Stream(ctx, req) {
		if err != nil {
			return llm.Message{}, "", llm.Usage{}, err
		}
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("atomic answer")}}, "end_turn", llm.Usage{InputTokens: 19}, nil
}

func TestSideHTTPPreparationCancellationAndClear(t *testing.T) {
	for _, clear := range []bool{false, true} {
		t.Run(fmt.Sprint(clear), func(t *testing.T) {
			f := newSideHTTPFixture(t)
			p, path := f.conversation(t)
			snap := f.checkpoint(t, p)
			for i := 0; i < 21; i++ {
				e, _, err := f.m.pg.StartSideRequest(t.Context(), snap, fmt.Sprint(i), "past question")
				if err != nil {
					t.Fatal(err)
				}
				e.Answer = "old answer"
				e.Sequence = 1
				e.Status = "completed"
				if _, err = f.m.pg.UpdateSideRequest(t.Context(), *e); err != nil {
					t.Fatal(err)
				}
			}
			started := make(chan struct{})
			f.provider.summary = func(ctx context.Context, req llm.CompletionRequest) (llm.Message, llm.Usage, error) {
				if len(req.Tools) != 0 {
					t.Error("summary has tools")
				}
				close(started)
				<-ctx.Done()
				return llm.UserText("late summary"), llm.Usage{InputTokens: 17}, nil
			}
			e := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "follow-up", "client_request_id": "new"}, 202))
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("summary did not start")
			}
			row, err := f.m.pg.SideRequest(t.Context(), e.ID)
			if err != nil || row.Context.Phase != "summarizing_history" {
				t.Fatalf("phase not persisted: %+v %v", row, err)
			}
			// A blocked summary must not hold the global admission/clear mutex.
			other, otherPath := f.conversation(t)
			f.checkpoint(t, other)
			f.call(t, "POST", otherPath, map[string]string{"question": "other session", "client_request_id": "independent"}, 202)
			f.s.side.mu.Lock()
			done := f.s.side.runs[e.ID].done
			f.s.side.mu.Unlock()
			if clear {
				f.call(t, "DELETE", path, nil, 200)
			} else {
				f.call(t, "POST", "/api/side-questions/"+e.ID+"/cancel", nil, 200)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("summary cancellation did not settle")
			}
			row, err = f.m.pg.SideRequest(t.Context(), e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if clear {
				if row != nil {
					t.Fatal("cleared request reappeared")
				}
			} else if row.Status != "cancelled" || row.Usage.InputTokens != 17 {
				t.Fatalf("cancel usage: %+v", row)
			}
			var memory string
			if err = f.m.pg.QueryRow(`SELECT memory::text FROM side_question_sessions WHERE session_key=$1`, p.Key()).Scan(&memory); err != nil || memory != "{}" {
				t.Fatalf("late summary saved: %s %v", memory, err)
			}
		})
	}
}

type sideHTTPFixture struct {
	s        *Server
	m        *Manager
	provider *sideHTTPProvider
	handler  http.Handler
	token    string
}

func newSideHTTPFixture(t *testing.T) *sideHTTPFixture {
	t.Helper()
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx, jwtKey: []byte("btw-test-signing-key-only"), chatBusy: map[string]bool{}}
	s.initSideQuestions()
	p := &sideHTTPProvider{started: make(chan llm.CompletionRequest, 16), release: make(chan struct{})}
	cfg := agent.Config{Format: llm.FormatOpenAI, BaseURL: "http://fixture.invalid", Model: "fixture", Stream: true}
	s.cfgMu.Lock()
	s.llmCfg = cfg
	s.llmOn = true
	s.llmDirect = bindSideProvider(p, cfg, 0, "fixture")
	s.cfgMu.Unlock()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	f := &sideHTTPFixture{s: s, m: m, provider: p, handler: s.Handler(), token: token}
	t.Cleanup(func() {
		cancel()
		for _, done := range s.cancelSideWhere(func(sidequestion.Parent) bool { return true }) {
			<-done
		}
		<-s.side.done
		s.flushSideSnapshots()
		m.Close()
	})
	return f
}
func (f *sideHTTPFixture) call(t *testing.T, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func (f *sideHTTPFixture) conversation(t *testing.T) (sidequestion.Parent, string) {
	t.Helper()
	c, err := f.m.pg.CreateConversation("mainagent", "side HTTP", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.m.pg.DeleteConversation(c.ID) })
	return sidequestion.Parent{ConversationID: c.ID}, fmt.Sprintf("/api/conversations/%d/side-questions", c.ID)
}
func (f *sideHTTPFixture) checkpoint(t *testing.T, p sidequestion.Parent) sidequestion.Snapshot {
	t.Helper()
	snap := sidequestion.Snapshot{Parent: p, RunID: time.Now().UnixNano(), Version: 1, CapturedAt: time.Now().UTC(), Model: sideModel(f.s.llmCfg, 0, "fixture"), Request: llm.CompletionRequest{MaxTokens: 128, Messages: []llm.Message{llm.UserText("main context marker")}}}
	if err := f.m.pg.SaveSideSnapshot(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	return snap
}
func decodeSide(t *testing.T, w *httptest.ResponseRecorder) sidequestion.Exchange {
	t.Helper()
	var e sidequestion.Exchange
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e
}
func waitSide(t *testing.T, d *db.DB, id, status string) *sidequestion.Exchange {
	t.Helper()
	until := time.Now().Add(3 * time.Second)
	for time.Now().Before(until) {
		e, err := d.SideRequest(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if e != nil && e.Status == status {
			return e
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("side %s did not reach %s", id, status)
	return nil
}

func TestSideHTTPBusyIsolationClearAndReconnect(t *testing.T) {
	f := newSideHTTPFixture(t)
	p, path := f.conversation(t)
	f.call(t, "POST", path, map[string]string{"question": "old session", "client_request_id": "old"}, 409)
	f.checkpoint(t, p)
	// Main busy status must not reject the side request.
	f.s.chatMu.Lock()
	f.s.chatBusy[fmt.Sprintf("conv-%d", p.ConversationID)] = true
	f.s.chatMu.Unlock()
	e := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "question", "client_request_id": "one"}, 202))
	select {
	case req := <-f.provider.started:
		if !strings.Contains(req.Messages[0].Text(), "main context marker") {
			t.Fatal("missing checkpoint")
		}
	case <-time.After(time.Second):
		t.Fatal("side blocked by main busy")
	}
	again := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "question", "client_request_id": "one"}, 200))
	if again.ID != e.ID {
		t.Fatal("duplicate request re-executed")
	}
	f.call(t, "POST", path, map[string]string{"question": "another", "client_request_id": "two"}, 409)
	// SSE reconnect always starts with a cumulative snapshot even with Last-Event-ID.
	httpServer := httptest.NewServer(f.handler)
	defer httpServer.Close()
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest("GET", httpServer.URL+"/api/side-questions/"+e.ID+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Last-Event-ID", "999")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(resp.Body)
		seen := false
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				seen = strings.Contains(scanner.Text(), e.ID)
				break
			}
		}
		resp.Body.Close()
		if !seen {
			t.Fatal("reconnect did not return cumulative request")
		}
	}
	f.call(t, "POST", "/api/side-questions/"+e.ID+"/cancel", nil, 200)
	done := waitSide(t, f.m.pg, e.ID, "cancelled")
	if done.Answer != "partial answer" || done.Usage.InputTokens != 19 {
		t.Fatalf("cancel lost partial/usage: %+v", done)
	}
	f.s.chatMu.Lock()
	mainBusy := f.s.chatBusy[fmt.Sprintf("conv-%d", p.ConversationID)]
	f.s.chatMu.Unlock()
	if !mainBusy {
		t.Fatal("side cancellation stopped main")
	}
	f.call(t, "DELETE", path, nil, 200)
	if history, err := f.m.pg.SideHistory(t.Context(), p.Key(), 0, 20); err != nil || len(history) != 0 {
		t.Fatalf("clear %+v %v", history, err)
	}
	if snap, err := f.m.pg.SideSnapshot(t.Context(), p.Key()); err != nil || snap == nil {
		t.Fatal("clear lost main snapshot")
	}
	var count int
	if err := f.m.pg.QueryRow(`SELECT count(*) FROM conversation_activities WHERE conversation_id=$1`, p.ConversationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("side contaminated main activity %d %v", count, err)
	}
	f.s.cfgMu.Lock()
	f.s.llmCfg.Model = "changed"
	f.s.cfgMu.Unlock()
	f.call(t, "GET", path, nil, 200)
	f.call(t, "POST", path, map[string]string{"question": "changed config", "client_request_id": "new"}, 409)
}

func TestSideHTTPGlobalLimitTaskWorkerAndDeletion(t *testing.T) {
	f := newSideHTTPFixture(t)
	var requests []sidequestion.Exchange
	for i := 0; i < 20; i++ {
		p, path := f.conversation(t)
		f.checkpoint(t, p)
		want := 202
		if i >= 4 {
			want = 429
		}
		w := f.call(t, "POST", path, map[string]string{"question": "blocked", "client_request_id": "id"}, want)
		if want == 202 {
			requests = append(requests, decodeSide(t, w))
		}
	}
	for _, e := range requests {
		f.call(t, "POST", "/api/side-questions/"+e.ID+"/cancel", nil, 200)
		waitSide(t, f.m.pg, e.ID, "cancelled")
	}
	close(f.provider.release)
	task, err := f.m.CreateTask("side route task", "context", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = f.m.pg.DeleteTask(id) })
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "own worker"}, 1, "paused", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, intent := range []int64{0, iid} {
		p := sidequestion.Parent{TaskID: id, ExplorationID: task.ExpID, IntentID: intent}
		f.checkpoint(t, p)
		path := "/api/tasks/" + task.ID + "/chat/side-questions"
		if intent > 0 {
			path = fmt.Sprintf("/api/tasks/%s/intents/%d/side-questions", task.ID, intent)
		}
		e := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "which context", "client_request_id": "own"}, 202))
		waitSide(t, f.m.pg, e.ID, "completed")
	}
	f.call(t, "GET", fmt.Sprintf("/api/tasks/%s/intents/%d/side-questions", task.ID, iid+1000000), nil, 404)
	if _, err := f.s.applyIntentControl(t.Context(), task, iid, "cancel", "cleanup"); err != nil {
		t.Fatal(err)
	}
	if snap, err := f.m.pg.SideSnapshot(t.Context(), (sidequestion.Parent{TaskID: id, ExplorationID: task.ExpID, IntentID: iid}).Key()); err != nil || snap != nil {
		t.Fatal("stopped intent side snapshot remains")
	}
}

func TestSideCheckpointPersistsBeforeAdmissionAndRestart(t *testing.T) {
	f := newSideHTTPFixture(t)
	p, path := f.conversation(t)
	bound := bindSideProvider(f.provider, f.s.llmCfg, 0, "fixture")
	ctx, deps := sidequestion.Attach(f.s.ctx, p, harness.QueryDeps{}, bound)
	mainCtx, stopMain := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range deps.CallModel(mainCtx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("live checkpoint")}, MaxTokens: 128}) {
		}
	}()
	select {
	case <-f.provider.started:
	case <-time.After(time.Second):
		t.Fatal("main never started")
	}
	e := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "side", "client_request_id": "live"}, 202))
	snap, err := f.m.pg.SideSnapshot(t.Context(), p.Key())
	if err != nil || snap == nil || snap.Request.Messages[0].Text() != "live checkpoint" {
		t.Fatalf("admission checkpoint not durable %+v %v", snap, err)
	}
	stopMain()
	<-done
	f.call(t, "POST", "/api/side-questions/"+e.ID+"/cancel", nil, 200)
	waitSide(t, f.m.pg, e.ID, "cancelled")
	// Simulate an abrupt process exit: persisted running row survives, memory doesn't.
	orphan, _, err := f.m.pg.StartSideRequest(t.Context(), *snap, "orphan", "restart")
	if err != nil {
		t.Fatal(err)
	}
	orphan.Answer = "saved before crash"
	orphan.Sequence = 1
	if _, err = f.m.pg.UpdateSideRequest(t.Context(), *orphan); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithCancel(t.Context())
	defer cancel()
	restarted := &Server{m: f.m, ctx: ctx2, llmCfg: f.s.llmCfg, llmDirect: bound, llmOn: true}
	restarted.initSideQuestions()
	if got := waitSide(t, f.m.pg, orphan.ID, "interrupted"); got.Answer != "saved before crash" {
		t.Fatal("restart discarded partial answer")
	}
	restored, err := restarted.sideSnapshot(t.Context(), p)
	if err != nil || restored == nil {
		t.Fatal("restart missing snapshot")
	}
	if _, err = restarted.sideProvider(restored.Model); err != nil {
		t.Fatal(err)
	}
	if req, err := sidequestion.BuildRequest(*restored, nil, "continue after restart"); err != nil || req.Messages[0].Text() != "live checkpoint" {
		t.Fatal("restart cannot ask from stored context")
	}
	// All routes pass the same authentication middleware.
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 401 {
		t.Fatalf("unauthenticated side route: %d", w.Code)
	}
}

func TestSideRejectsDeletedOrChangedCachedProfile(t *testing.T) {
	f := newSideHTTPFixture(t)
	p := &db.LLMProfile{Name: "btw-cached-profile", Format: "openai", BaseURL: "http://fixture.invalid", APIKey: "test-only", Model: "fixture", Streaming: true}
	id, err := f.m.pg.SaveProfile(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.m.pg.DeleteProfile(id) })
	cfg, ok := f.s.loadProfileConfig(id)
	if !ok {
		t.Fatal("profile unavailable")
	}
	f.s.provByProfile = map[int64]*provEntry{id: {prov: f.provider, cfg: cfg}}
	model := sideModel(cfg, id, p.Name)
	if _, err = f.s.sideProvider(model); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.pg.Exec(`UPDATE llm_profiles SET model='changed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.sideProvider(model); err == nil {
		t.Fatal("cached profile bypassed model identity check")
	}
	if err = f.m.pg.DeleteProfile(id); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.sideProvider(model); err == nil {
		t.Fatal("deleted profile remained usable from cache")
	}
}

func TestSideTaskDrainPersistsBeforeArchive(t *testing.T) {
	f := newSideHTTPFixture(t)
	task, err := f.m.CreateTask("side archive drain", "context", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = f.m.pg.DeleteTask(id) })
	p := sidequestion.Parent{TaskID: id, ExplorationID: task.ExpID}
	f.checkpoint(t, p)
	path := "/api/tasks/" + task.ID + "/chat/side-questions"
	e := decodeSide(t, f.call(t, "POST", path, map[string]string{"question": "answer before archive", "client_request_id": "drain"}, 202))
	select {
	case <-f.provider.started:
	case <-time.After(time.Second):
		t.Fatal("side never started")
	}
	// The real archive entry point closes this same admission barrier first.
	if !f.s.beginTaskDelete(task.ID) {
		t.Fatal("cannot close task admission")
	}
	defer f.s.abortTaskDelete(task.ID)
	f.call(t, "POST", path, map[string]string{"question": "too late", "client_request_id": "late"}, 409)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := f.s.drainTaskSideQuestions(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	row, err := f.m.pg.SideRequest(t.Context(), e.ID)
	if err != nil || row == nil || row.Status != "cancelled" || row.Answer != "partial answer" || row.Usage.InputTokens != 19 {
		t.Fatalf("drain returned before final persistence: %+v %v", row, err)
	}
	archive, err := f.m.pg.SnapshotTaskArchive(id)
	if err != nil {
		t.Fatal(err)
	}
	var rows []sidequestion.Exchange
	if err := json.Unmarshal(archive.Tables["side_question_requests"], &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "cancelled" || rows[0].Usage.InputTokens != 19 {
		t.Fatalf("archive lost drained side answer/usage: %+v", rows)
	}
}

func TestSideRestoredWorkerRuntimePublishesNewCheckpoint(t *testing.T) {
	f := newSideHTTPFixture(t)
	task, err := f.m.CreateTask("restore side publisher", "context", nil, 3600, 0)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(task.ID, 10, 64)
	t.Cleanup(func() { _ = f.m.pg.DeleteTask(id) })
	if err := f.m.pg.SetPaused(id, true); err != nil {
		t.Fatal(err)
	}
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "restored worker"}, 1, "paused", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the production startup path, including the deadline coordinator
	// which registers the shared runtime before the worker loops.
	f.s.restoreTaskRuntimes()
	t.Cleanup(func() {
		for _, task := range f.m.List() {
			f.s.engine.StopTask(task.ID)
		}
	})
	f.s.engine.runtimeMu.Lock()
	runtime := f.s.engine.runtimes[task.ID]
	f.s.engine.runtimeMu.Unlock()
	if runtime == nil {
		t.Fatal("restored task runtime missing")
	}
	p := sidequestion.Parent{TaskID: id, ExplorationID: task.ExpID, IntentID: iid}
	close(f.provider.release)
	bound := bindSideProvider(f.provider, f.s.llmCfg, 0, "fixture")
	ctx, deps := sidequestion.Attach(runtime.ctx, p, harness.QueryDeps{}, bound)
	_, _, _, err = deps.CallModelSync(ctx, llm.CompletionRequest{Messages: []llm.Message{llm.UserText("after restart context")}})
	if err != nil {
		t.Fatal(err)
	}
	f.s.flushSideSnapshots()
	snap, err := f.m.pg.SideSnapshot(t.Context(), p.Key())
	if err != nil || snap == nil || snap.Request.Messages[0].Text() != "after restart context" {
		t.Fatalf("restored runtime lost checkpoint publisher: %+v %v", snap, err)
	}
}
