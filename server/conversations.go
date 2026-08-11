package server

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// ---------- conversations (chat page) ----------
//
// A conversation is a ChatGPT-style thread bound to an agent key, independent of
// the pentest task graph. Turns run on ChatAgent; steps persist to
// conversation_activities and the browser POLLS ?since=cursor for live updates
// (no per-conversation SSE broadcaster needed).

func (s *Server) pgListConversations(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	cs, err := pg.ListConversations()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"conversations": cs})
}

func (s *Server) pgCreateConversation(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct {
		AgentKey     string `json:"agent_key"`
		Title        string `json:"title"`
		LLMProfileID *int64 `json:"llm_profile_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.AgentKey = strings.TrimSpace(req.AgentKey)
	if req.AgentKey == "" {
		writeErr(w, 400, "agent_key 不能为空")
		return
	}
	a, err := pg.GetAgentByKey(req.AgentKey)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if a == nil {
		writeErr(w, 404, "agent 不存在")
		return
	}
	if req.LLMProfileID != nil {
		if _, ok := s.loadProfileConfig(*req.LLMProfileID); !ok {
			writeErr(w, 400, "指定的 LLM 配置不存在或未设置 API Key")
			return
		}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "新对话"
	}
	c, err := pg.CreateConversation(req.AgentKey, title, req.LLMProfileID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) pgUpdateConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct {
		LLMProfileID *int64 `json:"llm_profile_id"` // null clears the override
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.LLMProfileID != nil {
		if _, ok := s.loadProfileConfig(*req.LLMProfileID); !ok {
			writeErr(w, 400, "指定的 LLM 配置不存在或未设置 API Key")
			return
		}
	}
	if err := pg.UpdateConversationProfile(c.ID, req.LLMProfileID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// convByID resolves the {id} path value to a conversation (404 if absent).
func (s *Server) convByID(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Conversation, bool) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil, false
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad conversation id")
		return nil, nil, false
	}
	c, err := pg.GetConversation(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}
	if c == nil {
		writeErr(w, 404, "conversation not found")
		return nil, nil, false
	}
	return pg, c, true
}

func (s *Server) pgRenameConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct{ Title string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeErr(w, 400, "标题不能为空")
		return
	}
	if err := pg.RenameConversation(c.ID, title); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pgDeleteConversation(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	if err := pg.DeleteConversation(c.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": c.ID})
}

func (s *Server) convBusyKey(id int64) string { return "conv-" + strconv.FormatInt(id, 10) }

// pgConversationMessages returns steps after ?since=cursor plus whether a turn is
// still running (so the client knows to keep polling). Same item shape as the task
// activity stream, so the frontend transcript renderer is reused verbatim.
func (s *Server) pgConversationMessages(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit := atoiDefault(q.Get("limit"), 200)
	s.chatMu.Lock()
	running := s.chatBusy[s.convBusyKey(c.ID)]
	s.chatMu.Unlock()

	// Incremental tail: ?since=N returns steps after id N (ASC) — used by the live
	// poll and the post-send fetch. A generous cap so a burst is never dropped.
	if sv := q.Get("since"); sv != "" {
		since, _ := strconv.ParseInt(sv, 10, 64)
		items, cursor, err := pg.ConvActivityList(c.ID, since, max(limit, 1000))
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": false})
		return
	}

	// History page (reverse pagination): no ?since → the latest page; ?before=N →
	// the page of older steps ending before id N. hasMore lets the client stop
	// loading earlier history once the top of the thread is reached.
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	items, hasMore, err := pg.ConvActivityPage(c.ID, before, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cursor := before
	if n := len(items); n > 0 {
		cursor = items[n-1].ID
	}
	writeJSON(w, 200, map[string]any{"items": activityDTOs(items), "cursor": cursor, "running": running, "hasMore": hasMore})
}

// pgConversationMsgDetail lazily returns one step's full detail blob.
func (s *Server) pgConversationMsgDetail(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	seq, ok := pathInt(r, "seq")
	if !ok {
		writeErr(w, 400, "bad seq")
		return
	}
	detail, err := pg.ConvActivityDetail(c.ID, seq)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"detail": detail})
}

// pgStopConversation aborts the in-flight run for one conversation (manual stop —
// the run/stop button in the chat UI). It cancels only THIS session's agent run;
// the P3 trigger queue is untouched, so the agent's next queued fire still starts.
func (s *Server) pgStopConversation(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	cancel := s.chatCancel[busyKey]
	s.chatMu.Unlock()
	if cancel == nil {
		writeJSON(w, 200, map[string]any{"status": "idle"}) // nothing running
		return
	}
	cancel()
	writeJSON(w, 200, map[string]any{"status": "stopping"})
}

// pgSendConversationMessage persists the human turn, then runs the agent in the
// background (steps stream to conversation_activities, polled by the client). One
// in-flight turn per conversation.
func (s *Server) pgSendConversationMessage(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	var req struct{ Message string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg := strings.TrimSpace(req.Message)
	if msg == "" {
		writeErr(w, 400, "消息不能为空")
		return
	}
	ca := s.chatAgentRef()
	if ca == nil {
		writeErr(w, 503, "LLM 未配置，无法对话")
		return
	}

	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	if s.chatBusy[busyKey] {
		s.chatMu.Unlock()
		writeErr(w, 409, "该会话正在处理上一条消息，请稍候")
		return
	}
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()

	// persist + float the human turn; auto-title the thread from the first message.
	// Worker is the agent key (not "user") so the transcript stays a single lane
	// (no worker chips) — the kind='user' already right-aligns it as a human bubble.
	if _, err := pg.AppendConvActivity(c.ID, db.Activity{Worker: c.AgentKey, Kind: "user", Summary: firstLine(msg, 200), Detail: msg}); err != nil {
		log.Printf("[conv %d] append user msg failed: %v", c.ID, err)
	}
	if c.Title == "" || c.Title == "新对话" {
		_ = pg.RenameConversation(c.ID, firstLine(msg, 40))
	}
	_ = pg.TouchConversation(c.ID)

	s.runConversation(c, msg, busyKey)
	writeJSON(w, 202, map[string]any{"status": "accepted"})
}

// runConversation runs ONE agent turn on a conversation in a background goroutine
// (each conversation is independent → parallel). Steps stream to
// conversation_activities. Shared by the chat HTTP handler and the P3 scheduler.
// busyKey clears when the run ends (best-effort in-flight marker).
func (s *Server) runConversation(c *db.Conversation, msg, busyKey string) {
	go s.runConversationSync(c, msg, busyKey)
}

// runConversationSync runs ONE agent turn on a conversation and BLOCKS until the
// run ends (clearing busyKey). runConversation wraps it in a goroutine for the
// fire-and-forget chat path; the P3 trigger queue calls it directly so it can wait
// for completion before starting the next queued fire for the same agent.
func (s *Server) runConversationSync(c *db.Conversation, msg, busyKey string) {
	// Per-run cancellable context so a manual stop (pgStopConversation) can abort
	// just this session. Registered under chatMu so the stop handler can find it.
	ctx, cancel := context.WithCancel(intercept.WithConvID(s.ctx, c.ID))
	s.chatMu.Lock()
	s.chatCancel[busyKey] = cancel
	s.chatMu.Unlock()
	defer func() {
		cancel()
		s.chatMu.Lock()
		delete(s.chatBusy, busyKey)
		delete(s.chatCancel, busyKey)
		s.chatMu.Unlock()
	}()
	// precedence: the conversation agent's own binding → this conversation's pin → global.
	ca := s.chatAgentRef()
	if eff := s.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
		if pa := s.chatAgentForProfile(*eff); pa != nil {
			ca = pa
		}
	}
	if ca == nil {
		return
	}
	pg := s.m.pg
	maxTurns := s.agentMaxTurns(c.AgentKey)
	maxDuration := time.Duration(s.agentRunSeconds(c.AgentKey)) * time.Second
	webSearch := false
	if a, err := pg.GetAgentByKey(c.AgentKey); err == nil && a != nil {
		webSearch = a.WebSearch
	}
	sessionID := s.convBusyKey(c.ID) // "conv-<id>" transcript session
	emit := func(rec db.Activity) {
		if _, err := pg.AppendConvActivity(c.ID, rec); err != nil {
			log.Printf("[conv %d] append activity failed: %v", c.ID, err)
		}
	}
	// On a manual stop ctx is cancelled; Chat already emits a clean "已手动停止"
	// step, so skip the raw-error entry — only surface genuine failures.
	if _, err := ca.Chat(ctx, c.AgentKey, sessionID, msg, maxTurns, maxDuration, webSearch, emit); err != nil && ctx.Err() == nil {
		_, _ = pg.AppendConvActivity(c.ID, db.Activity{Worker: c.AgentKey, Kind: "text", IsError: true,
			Summary: "（出错：" + err.Error() + "）", Detail: err.Error()})
	}
	_ = pg.TouchConversation(c.ID)
}

// triggerBehavior is an agent's cached P3 trigger post-processing策略 (see the
// agents table trigger_* columns). Read once per fire in StartTriggeredRun so the
// pump never touches the DB while holding queueMu.
type triggerBehavior struct {
	runMode     string // serial | parallel
	mergeMode   string // by_task | all | none (serial 用;parallel 忽略,每条各自一个会话)
	maxParallel int    // parallel 用的每 agent 并发上限;<=0=不限
}

// readTriggerBehavior loads an agent's策略, falling back to safe defaults
// (serial / by_task / 5) on any error or unknown enum value.
func (s *Server) readTriggerBehavior(agentKey string) triggerBehavior {
	b := triggerBehavior{runMode: "serial", mergeMode: "by_task", maxParallel: 5}
	if s.m.pg == nil {
		return b
	}
	a, err := s.m.pg.GetAgentByKey(agentKey)
	if err != nil || a == nil {
		return b
	}
	if a.TriggerRunMode == "parallel" {
		b.runMode = "parallel"
	}
	switch a.TriggerMergeMode {
	case "by_task", "all", "none":
		b.mergeMode = a.TriggerMergeMode
	}
	b.maxParallel = a.TriggerMaxParallel
	return b
}

// StartTriggeredRun enqueues a P3 trigger fire for agentKey and pumps the queue. The
// agent's策略 decides concurrency + merge: serial → run one at a time (optionally
// merging by task / all / none); parallel → run each fire in its own concurrent
// conversation up to trigger_max_parallel. Distinct agents always run concurrently.
func (s *Server) StartTriggeredRun(agentKey, title, message string, taskID int64, mergeable bool) {
	if s.m.pg == nil || s.chatAgentRef() == nil {
		return
	}
	cfg := s.readTriggerBehavior(agentKey) // DB read BEFORE the lock (never under queueMu)
	s.queueMu.Lock()
	s.triggerCfg[agentKey] = cfg
	s.triggerQ[agentKey] = append(s.triggerQ[agentKey], triggeredRun{agentKey: agentKey, title: title, message: message, taskID: taskID, mergeable: mergeable})
	s.pumpLocked(agentKey)
	s.queueMu.Unlock()
}

// pumpLocked launches queued fires for one agent up to its concurrency limit, then
// returns. Serial → limit 1; parallel → limit = maxParallel (<=0 → unlimited). Each
// launched run re-pumps on completion (runAndPump) to fill the freed slot. Caller
// holds queueMu.
func (s *Server) pumpLocked(agentKey string) {
	if s.ctx.Err() != nil {
		return
	}
	cfg := s.triggerCfg[agentKey]
	limit := 1
	if cfg.runMode == "parallel" {
		if cfg.maxParallel <= 0 {
			limit = math.MaxInt
		} else {
			limit = cfg.maxParallel
		}
	}
	for s.triggerActive[agentKey] < limit && len(s.triggerQ[agentKey]) > 0 {
		item := s.nextTriggerRun(agentKey, cfg)
		s.triggerActive[agentKey]++
		go s.runAndPump(agentKey, item)
	}
}

// runAndPump runs one fire to completion, then decrements the active counter and
// re-pumps to fill the freed slot (cleaning up the agent's maps when fully idle).
func (s *Server) runAndPump(agentKey string, item triggeredRun) {
	s.runTriggeredRun(item)
	s.queueMu.Lock()
	s.triggerActive[agentKey]--
	if s.triggerActive[agentKey] <= 0 && len(s.triggerQ[agentKey]) == 0 {
		delete(s.triggerActive, agentKey)
		delete(s.triggerQ, agentKey)
		delete(s.triggerCfg, agentKey)
	} else {
		s.pumpLocked(agentKey)
	}
	s.queueMu.Unlock()
}

// nextTriggerRun pops the next run from the agent's queue per the merge mode:
//
//	parallel / none → one head item, no merge
//	by_task         → head + all queued same-task mergeable fires merged into one
//	all             → the ENTIRE queue merged into one run (regardless of task/type)
//
// Caller holds queueMu.
func (s *Server) nextTriggerRun(agentKey string, cfg triggerBehavior) triggeredRun {
	q := s.triggerQ[agentKey]
	if cfg.runMode == "parallel" || cfg.mergeMode == "none" {
		s.triggerQ[agentKey] = q[1:]
		return q[0]
	}
	if cfg.mergeMode == "all" {
		s.triggerQ[agentKey] = q[:0:0]
		return mergeAllRuns(q)
	}
	// by_task: head + same-task mergeable fires.
	head := q[0]
	if !head.mergeable || head.taskID == 0 {
		s.triggerQ[agentKey] = q[1:]
		return head
	}
	group := []triggeredRun{head}
	rest := q[:0:0] // keep non-matching items in order
	for _, it := range q[1:] {
		if it.mergeable && it.taskID == head.taskID {
			group = append(group, it)
		} else {
			rest = append(rest, it)
		}
	}
	s.triggerQ[agentKey] = rest
	return mergeTriggeredRuns(group)
}

// mergeTriggeredRuns folds several same-task event fires into one run: a header plus
// each fire's message, so the agent handles the task's burst in a single conversation.
func mergeTriggeredRuns(items []triggeredRun) triggeredRun {
	if len(items) == 1 {
		return items[0]
	}
	first := items[0]
	var b strings.Builder
	fmt.Fprintf(&b, "【本会话合并了任务 #%d 的 %d 条触发事件，请一并处理】\n", first.taskID, len(items))
	for i, it := range items {
		fmt.Fprintf(&b, "\n── 触发 %d ──\n%s\n", i+1, it.message)
	}
	return triggeredRun{
		agentKey:  first.agentKey,
		title:     fmt.Sprintf("合并触发 · task#%d · %d 条", first.taskID, len(items)),
		message:   b.String(),
		taskID:    first.taskID,
		mergeable: true,
	}
}

// mergeAllRuns folds the ENTIRE queued burst into one run (merge_mode='all'),
// regardless of task or type — a header plus each fire's message.
func mergeAllRuns(items []triggeredRun) triggeredRun {
	if len(items) == 1 {
		return items[0]
	}
	first := items[0]
	var b strings.Builder
	fmt.Fprintf(&b, "【本会话合并了队列中的 %d 条触发事件（不区分任务），请一并处理】\n", len(items))
	for i, it := range items {
		fmt.Fprintf(&b, "\n── 触发 %d（task#%d）──\n%s\n", i+1, it.taskID, it.message)
	}
	return triggeredRun{
		agentKey:  first.agentKey,
		title:     fmt.Sprintf("合并触发 · 全部 · %d 条", len(items)),
		message:   b.String(),
		taskID:    first.taskID,
		mergeable: true,
	}
}

// runTriggeredRun creates the conversation for one queued fire, records the human
// turn, and runs it synchronously (blocks until the run ends). recover keeps a
// panic from killing the drain loop and wedging the agent's queue.
func (s *Server) runTriggeredRun(item triggeredRun) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[trigger] run for %s panicked: %v", item.agentKey, r)
		}
	}()
	pg := s.m.pg
	c, err := pg.CreateConversation(item.agentKey, firstLine(item.title, 60), nil)
	if err != nil {
		log.Printf("[trigger] create conversation for %s failed: %v", item.agentKey, err)
		return
	}
	if _, err := pg.AppendConvActivity(c.ID, db.Activity{Worker: item.agentKey, Kind: "user", Summary: firstLine(item.message, 200), Detail: item.message}); err != nil {
		log.Printf("[trigger] append msg failed: %v", err)
	}
	busyKey := s.convBusyKey(c.ID)
	s.chatMu.Lock()
	s.chatBusy[busyKey] = true
	s.chatMu.Unlock()
	s.runConversationSync(c, item.message, busyKey)
}

// firstLine returns a single-line, length-capped preview (shared with summaries).
func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > max {
		s = string([]rune(s)[:max]) + "…"
	}
	return s
}
