package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/artex/sidequestion"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

type sideRun struct {
	key    string
	parent sidequestion.Parent
	cancel context.CancelFunc
	done   chan struct{}
}
type sideQuestionState struct {
	mu           sync.Mutex
	commands     sync.Mutex // short admission / clear operations only, never inference
	pending      map[string]sidequestion.Snapshot
	latest       map[string]sidequestion.Snapshot
	seen         map[string][2]int64
	runs         map[string]sideRun
	done         chan struct{}
	outputTokens int
}

func sideModel(cfg agent.Config, id int64, name string) sidequestion.Model {
	b, _ := json.Marshal([]any{cfg.Format, cfg.BaseURL, cfg.Model, cfg.ThinkingType, cfg.ReasoningEffort, cfg.Stream, cfg.MaxTokensField, cfg.SessionHeaderKey})
	h := sha256.Sum256(b)
	return sidequestion.Model{ProfileID: id, Name: name, Format: string(cfg.Format), Model: cfg.Model, Identity: hex.EncodeToString(h[:]), Streaming: cfg.Stream, WindowTokens: cfg.CompactionWindow()}
}

func bindSideProvider(p llm.Provider, cfg agent.Config, id int64, name string) llm.Provider {
	return sidequestion.Bind(p, sideModel(cfg, id, name))
}

func (s *Server) initSideQuestions() {
	s.side = &sideQuestionState{pending: map[string]sidequestion.Snapshot{}, latest: map[string]sidequestion.Snapshot{}, seen: map[string][2]int64{}, runs: map[string]sideRun{}, done: make(chan struct{})}
	if n, err := strconv.Atoi(os.Getenv("ARTEX_BTW_MAX_OUTPUT_TOKENS")); err == nil && n >= 256 && n <= 32768 {
		s.side.outputTokens = n
	}
	if s.m.pg == nil {
		close(s.side.done)
		return
	}
	if err := s.m.pg.InterruptSideRequests(s.ctx); err != nil {
		log.Printf("[btw] recover: %v", err)
	}
	s.ctx = sidequestion.WithPublisher(s.ctx, func(snap sidequestion.Snapshot) {
		s.side.mu.Lock()
		defer s.side.mu.Unlock()
		key := snap.Parent.Key()
		old, ok := s.side.seen[key]
		if ok && (old[0] > snap.RunID || old[0] == snap.RunID && old[1] >= snap.Version) {
			return
		}
		s.side.latest[key] = snap
		s.side.seen[key] = [2]int64{snap.RunID, snap.Version}
		s.side.pending[key] = snap
	})
	go func() {
		defer close(s.side.done)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				s.flushSideSnapshots()
			case <-s.ctx.Done():
				s.flushSideSnapshots()
				return
			}
		}
	}()
}

func (s *Server) flushSideSnapshots() {
	if s.side == nil || s.m.pg == nil {
		return
	}
	s.side.commands.Lock()
	defer s.side.commands.Unlock()
	s.side.mu.Lock()
	pending := s.side.pending
	s.side.pending = map[string]sidequestion.Snapshot{}
	s.side.mu.Unlock()
	for key, snap := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := s.m.pg.SaveSideSnapshot(ctx, snap)
		cancel()
		s.side.mu.Lock()
		if err == nil || errors.Is(err, db.ErrSideParentGone) {
			if cur, ok := s.side.latest[key]; ok && cur.RunID == snap.RunID && cur.Version == snap.Version {
				delete(s.side.latest, key)
			}
		} else if newer, ok := s.side.pending[key]; !ok || newer.RunID < snap.RunID || newer.RunID == snap.RunID && newer.Version < snap.Version {
			s.side.pending[key] = snap
		}
		s.side.mu.Unlock()
		if err != nil && !errors.Is(err, db.ErrSideParentGone) {
			log.Printf("[btw] checkpoint persistence: %v", err)
		}
	}
}

func (s *Server) sideSnapshot(ctx context.Context, p sidequestion.Parent) (*sidequestion.Snapshot, error) {
	s.side.mu.Lock()
	snap, ok := s.side.latest[p.Key()]
	s.side.mu.Unlock()
	if ok {
		return &snap, nil
	}
	return s.m.pg.SideSnapshot(ctx, p.Key())
}

func (s *Server) cancelSideWhere(match func(sidequestion.Parent) bool) []<-chan struct{} {
	if s.side == nil {
		return nil
	}
	s.side.mu.Lock()
	defer s.side.mu.Unlock()
	var done []<-chan struct{}
	for _, run := range s.side.runs {
		if match(run.parent) {
			run.cancel()
			done = append(done, run.done)
		}
	}
	return done
}

// Called after the task admission barrier closes and the main loops drain.
// Wait for final answer/usage writes before taking the archive snapshot.
func (s *Server) drainTaskSideQuestions(ctx context.Context, taskID string) error {
	if s.side == nil {
		return nil
	}
	id, _ := strconv.ParseInt(taskID, 10, 64)
	s.side.commands.Lock()
	done := s.cancelSideWhere(func(p sidequestion.Parent) bool { return p.TaskID == id })
	s.side.commands.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.flushSideSnapshots()
	// A failed flush must not silently leave the latest checkpoint out of archive.
	s.side.mu.Lock()
	defer s.side.mu.Unlock()
	for _, snap := range s.side.pending {
		if snap.Parent.TaskID == id {
			return errors.New("旁路上下文尚未保存，请重试")
		}
	}
	return nil
}

func (s *Server) cancelWorkerSide(taskID string, expID, iid int64) {
	if s.side == nil {
		return
	}
	id, _ := strconv.ParseInt(taskID, 10, 64)
	p := sidequestion.Parent{TaskID: id, ExplorationID: expID, IntentID: iid}
	s.side.commands.Lock()
	defer s.side.commands.Unlock()
	s.cancelSideWhere(func(parent sidequestion.Parent) bool { return parent == p })
}

func (s *Server) sideProvider(model sidequestion.Model) (llm.Provider, error) {
	var p llm.Provider
	var cfg agent.Config
	var ok bool
	if model.ProfileID > 0 {
		// Validate the persisted reference even if a previous provider is cached.
		current, exists := s.loadProfileConfig(model.ProfileID)
		if !exists || sideModel(current, model.ProfileID, model.Name).Identity != model.Identity {
			return nil, errors.New("模型配置已删除或变化，请先运行主 Agent 更新上下文")
		}
		p, cfg, ok = s.providerForProfile(model.ProfileID)
	} else {
		s.cfgMu.Lock()
		p, cfg, ok = s.llmDirect, s.llmCfg, s.llmOn
		s.cfgMu.Unlock()
	}
	if !ok || p == nil || sideModel(cfg, model.ProfileID, model.Name).Identity != model.Identity {
		return nil, errors.New("模型配置已删除或变化，请先运行主 Agent 更新上下文")
	}
	return p, nil
}

func (s *Server) sideParent(w http.ResponseWriter, r *http.Request, kind string) (sidequestion.Parent, bool) {
	p := sidequestion.Parent{}
	if s.side == nil || s.m.pg == nil {
		writeErr(w, 503, "旁路服务不可用")
		return p, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad id")
		return p, false
	}
	if kind == "conversation" {
		c, err := s.m.pg.GetConversation(id)
		if err != nil {
			writeErr(w, 500, err.Error())
			return p, false
		}
		if c == nil {
			writeErr(w, 404, "conversation not found")
			return p, false
		}
		p.ConversationID = id
		return p, true
	}
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return p, false
	}
	pt, err := s.m.pg.GetTask(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return p, false
	}
	if pt == nil || s.engine.IsDeleting(t.ID) {
		writeErr(w, 409, "任务已归档或正在删除")
		return p, false
	}
	p.TaskID, p.ExplorationID = id, t.ExpID
	if kind == "worker" {
		iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
		if err != nil || iid <= 0 {
			writeErr(w, 400, "bad intent id")
			return p, false
		}
		n, err := t.Store.GetNode(iid)
		if err != nil {
			writeErr(w, 500, err.Error())
			return p, false
		}
		if n == nil || n.Kind != db.KindIntent {
			writeErr(w, 404, "intent not found")
			return p, false
		}
		if n.State == "stopped" {
			writeErr(w, 409, "Worker 已删除")
			return p, false
		}
		p.IntentID = iid
	}
	return p, true
}

func (s *Server) registerSideRoutes(mux *http.ServeMux) {
	for _, route := range []struct{ path, kind string }{{"/api/conversations/{id}", "conversation"}, {"/api/tasks/{id}/chat", "main"}, {"/api/tasks/{id}/intents/{iid}", "worker"}} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			mux.HandleFunc(method+" "+route.path+"/side-questions", func(w http.ResponseWriter, r *http.Request) {
				p, ok := s.sideParent(w, r, route.kind)
				if ok {
					s.handleSideQuestions(w, r, p)
				}
			})
		}
	}
	mux.HandleFunc("GET /api/side-questions/{requestID}/events", s.sideEvents)
	mux.HandleFunc("POST /api/side-questions/{requestID}/cancel", s.cancelSideRequest)
}

func (s *Server) handleSideQuestions(w http.ResponseWriter, r *http.Request, p sidequestion.Parent) {
	key := p.Key()
	if r.Method == "DELETE" {
		s.side.commands.Lock()
		defer s.side.commands.Unlock()
		s.side.mu.Lock()
		for _, run := range s.side.runs {
			if run.key == key {
				run.cancel()
			}
		}
		s.side.mu.Unlock()
		if err := s.m.pg.ClearSideHistory(r.Context(), key); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"cleared": true})
		return
	}
	snap, err := s.sideSnapshot(r.Context(), p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if r.Method == "GET" {
		before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		items, err := s.m.pg.SideHistory(r.Context(), key, before, 21)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		var next int64
		if len(items) > 20 {
			items = items[:20]
			next = items[len(items)-1].Ordinal
		}
		current, err := s.m.pg.CurrentSideRequest(r.Context(), key)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		var meta any
		if snap != nil {
			_, modelErr := s.sideProvider(snap.Model)
			reason := ""
			if modelErr != nil {
				reason = modelErr.Error()
			}
			meta = map[string]any{"captured_at": snap.CapturedAt, "model": snap.Model, "available": modelErr == nil, "reason": reason}
		}
		writeJSON(w, 200, map[string]any{"items": items, "current": current, "next_cursor": next, "snapshot": meta})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var in struct {
		Question string `json:"question"`
		ClientID string `json:"client_request_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, 400, "bad json")
		return
	}
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || len([]rune(in.Question)) > 4000 || !validWorkerMessageRequestID(in.ClientID) {
		writeErr(w, 400, "问题须为 1–4000 字符，并提供有效请求 ID")
		return
	}
	s.side.commands.Lock()
	defer s.side.commands.Unlock()
	if p.TaskID > 0 && s.engine.IsDeleting(strconv.FormatInt(p.TaskID, 10)) {
		writeErr(w, 409, "任务正在归档或删除")
		return
	}
	if existing, err := s.m.pg.ExistingSideRequest(r.Context(), key, in.ClientID); err != nil {
		writeErr(w, 500, err.Error())
		return
	} else if existing != nil {
		if existing.Question != in.Question {
			writeErr(w, 409, "同一请求 ID 不能用于不同问题")
			return
		}
		writeJSON(w, 200, existing)
		return
	}
	if snap == nil {
		writeErr(w, 409, "尚无上下文快照，请先运行主 Agent")
		return
	}
	provider, err := s.sideProvider(snap.Model)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	s.side.mu.Lock()
	full := len(s.side.runs) >= 4
	busy := false
	for _, run := range s.side.runs {
		if run.key == key {
			busy = true
		}
	}
	s.side.mu.Unlock()
	if busy {
		writeErr(w, 409, db.ErrSideBusy.Error())
		return
	}
	if full {
		writeErr(w, 429, "旁路请求已达并发上限，请稍后重试")
		return
	}
	if err = s.m.pg.SaveSideSnapshot(r.Context(), *snap); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	e, created, err := s.m.pg.StartSideRequest(r.Context(), *snap, in.ClientID, in.Question)
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if created {
		ctx, cancel := context.WithTimeout(s.ctx, 120*time.Second)
		s.side.mu.Lock()
		s.side.runs[e.ID] = sideRun{key: key, parent: p, cancel: cancel, done: make(chan struct{})}
		s.side.mu.Unlock()
		go s.runSide(ctx, cancel, *e, p, provider, *snap)
	}
	writeJSON(w, 202, e)
}

func (s *Server) runSide(ctx context.Context, cancel context.CancelFunc, e sidequestion.Exchange, parent sidequestion.Parent, provider llm.Provider, snapshot sidequestion.Snapshot) {
	defer cancel()
	defer func() {
		s.side.mu.Lock()
		if run, ok := s.side.runs[e.ID]; ok {
			close(run.done)
		}
		delete(s.side.runs, e.ID)
		s.side.mu.Unlock()
	}()
	ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-btw-%s", parent.ExplorationID, e.ID))
	if parent.TaskID > 0 {
		ctx = llmrec.WithTaskID(ctx, strconv.FormatInt(parent.TaskID, 10))
	}
	// Deletions can race between request insertion and registration. Observe the
	// persisted lifecycle even while a provider is blocked without emitting text.
	go func() {
		tick := time.NewTicker(500 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				row, err := s.m.pg.SideRequest(ctx, e.ID)
				if err == nil && (row == nil || !row.Running()) {
					cancel()
					return
				}
			}
		}
	}()
	lastSave := time.Now()
	persist := func() (bool, error) {
		e.Sequence++
		writeCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		ok, err := s.m.pg.UpdateSideRequest(writeCtx, e)
		if err != nil {
			log.Printf("[btw] answer persistence: %v", err)
			return true, err
		}
		return ok, nil
	}
	memory, runErr := s.m.pg.SideMemory(ctx, e)
	var answer sidequestion.Answer
	if runErr == nil {
		answer, e.Context, runErr = (sidequestion.SideQuestionService{Provider: provider}).Respond(ctx, snapshot, e.Question, sidequestion.Replay{
			Memory: memory,
			Load: func(ctx context.Context, after int64) ([]sidequestion.Exchange, error) {
				return s.m.pg.SideReplayPage(ctx, e, after)
			},
			Save: func(ctx context.Context, memory sidequestion.Memory) error {
				return s.m.pg.SaveSideMemory(ctx, e, memory)
			},
		}, sidequestion.ContextOptions{OutputTokens: s.side.outputTokens}, func(a sidequestion.Answer, info sidequestion.ContextInfo) {
			phaseChanged := e.Context.Phase != info.Phase
			e.Answer, e.Usage, e.Context = a.Text, a.Usage, info
			if phaseChanged || time.Since(lastSave) >= 250*time.Millisecond {
				lastSave = time.Now()
				if ok, _ := persist(); !ok {
					cancel()
				}
			}
		})
	}
	e.Answer, e.Usage = answer.Text, answer.Usage
	e.Status = "completed"
	if ctx.Err() != nil {
		e.Status = "cancelled"
		e.Error = "回答已停止"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			e.Status = "failed"
			e.Error = "旁路回答超过 120 秒，已停止"
		}
	} else if runErr != nil {
		e.Status = "failed"
		e.Error = runErr.Error()
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := persist(); err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func (s *Server) cancelSideRequest(w http.ResponseWriter, r *http.Request) {
	e, err := s.m.pg.SideRequest(r.Context(), r.PathValue("requestID"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if e == nil {
		writeErr(w, 404, "side question not found")
		return
	}
	s.side.mu.Lock()
	if run, ok := s.side.runs[e.ID]; ok {
		run.cancel()
	}
	s.side.mu.Unlock()
	writeJSON(w, 200, map[string]bool{"cancelled": true})
}

func (s *Server) sideEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unavailable")
		return
	}
	id := r.PathValue("requestID")
	first, err := s.m.pg.SideRequest(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if first == nil {
		writeErr(w, 404, "side question not found")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	seq := int64(-1)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		e := first
		first = nil
		if e == nil {
			e, err = s.m.pg.SideRequest(r.Context(), id)
		}
		if err != nil {
			return
		}
		if e == nil {
			fmt.Fprint(w, "event: cleared\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		if e.Sequence > seq {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %d\nevent: snapshot\ndata: %s\n\n", e.Sequence, b)
			flusher.Flush()
			seq = e.Sequence
		}
		if !e.Running() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-tick.C:
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
