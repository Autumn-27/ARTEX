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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
	actool "github.com/Autumn-27/norma/tool"
)

type retestProvider struct {
	complete func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error)
}

func (p retestProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	return p.complete(ctx, req)
}
func (p retestProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{}, errors.New("test expects non-streaming"))
	}
}

func newRetestServer(t *testing.T) (*Server, int64) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("test postgres not configured")
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	td := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: &Manager{pg: pg, dir: td, tasks: map[string]*Task{}},
		chatBusy: map[string]bool{}, chatCancel: map[string]context.CancelCauseFunc{}}
	if err := s.seedFindingRetester(); err != nil {
		t.Fatal(err)
	}
	fid, err := pg.AddFinding(0, 0, "retest-server", "复测测试", "high", "summary", "original proof", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldAugment, oldResolve, oldPrompt := agent.ToolAugment, agent.ToolResolve, agent.PromptOverride
	agent.ToolAugment = func(context.Context, string) ([]actool.CoreTool, agent.DeferredInfo, func()) {
		return s.findingRetestTools(), agent.DeferredInfo{}, func() {}
	}
	agent.ToolResolve = nil
	agent.PromptOverride = func(string) (string, bool) { return agent.RetesterDefaultPrompt, true }
	t.Cleanup(func() {
		cancel()
		waitRetestIdle(t, s)
		agent.ToolAugment, agent.ToolResolve, agent.PromptOverride = oldAugment, oldResolve, oldPrompt
		_, _ = pg.Exec(`DELETE FROM conversations WHERE id IN (SELECT conversation_id FROM finding_retests WHERE finding_id=$1)`, fid)
		_, _ = pg.DeleteFinding(fid)
		pg.Close()
	})
	return s, fid
}

func setRetestProvider(s *Server, p retestProvider) {
	s.chatAgent = agent.NewChatAgent(p, "test", s.m.dir, nil, 10000)
	s.chatAgent.SetNonStreaming(func() bool { return true })
}

func retestRequest(handler http.HandlerFunc, method string, id int64, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/", strings.NewReader(body))
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func waitRetestIdle(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.chatMu.Lock()
		n := len(s.chatBusy)
		s.chatMu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("conversation did not become idle")
}

func TestRetestActiveStatusLifecycle(t *testing.T) {
	s, fid := newRetestServer(t)
	check := func(want string, conversationID int64) {
		t.Helper()
		w := retestRequest(s.listActiveFindingRetests, http.MethodGet, 0, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var body struct {
			Retests []db.ActiveFindingRetest `json:"retests"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var matches []db.ActiveFindingRetest
		for _, item := range body.Retests {
			if item.FindingID == fid {
				matches = append(matches, item)
			}
		}
		if want == "" {
			if len(matches) != 0 {
				t.Fatalf("terminal retest still active: %+v", matches)
			}
		} else if len(matches) != 1 || matches[0].Status != want || matches[0].ConversationID != conversationID {
			t.Fatalf("active=%+v want status=%s conversation=%d", matches, want, conversationID)
		}
		for _, field := range []string{`"snapshot"`, `"evidence"`, `"notes"`, `"summary"`} {
			if strings.Contains(w.Body.String(), field) {
				t.Fatalf("status response leaks %s", field)
			}
		}
	}
	check("", 0)
	for _, terminal := range []string{"completed", "failed", "stopped"} {
		r, c, _, err := s.m.pg.CreateFindingRetest(t.Context(), fid, "private notes")
		if err != nil {
			t.Fatal(err)
		}
		check("pending", c.ID)
		if _, err := s.m.pg.StartFindingRetest(t.Context(), r.ID); err != nil {
			t.Fatal(err)
		}
		check("running", c.ID)
		if terminal == "completed" {
			if err := s.m.pg.RecordFindingRetestResult(t.Context(), c.ID, "fixed", "summary", "proof"); err != nil {
				t.Fatal(err)
			}
			check("running", c.ID) // A staged verdict is still running until the turn ends.
		}
		if err := s.m.pg.FinishFindingRetest(r.ID, terminal, ""); err != nil {
			t.Fatal(err)
		}
		check("", 0)
	}
}

func TestRetestHTTPThroughConversationAndTools(t *testing.T) {
	s, fid := newRetestServer(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	setRetestProvider(s, retestProvider{complete: func(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return llm.Message{}, "", llm.Usage{}, ctx.Err()
			}
		}
		name, input := "get_finding_retest_context", `{}`
		if n == 2 {
			serialized, _ := json.Marshal(req.Messages)
			if !bytes.Contains(serialized, []byte("original proof")) {
				t.Error("agent did not receive source evidence")
			}
			name, input = "record_finding_retest_result", `{"verdict":"inconclusive","summary":"缺少测试登录态","evidence":"已检查原证据；当前缺少有效登录态，无法确认修复状态。"}`
		}
		if n > 2 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("复测结论已保存")}}, "end_turn", llm.Usage{}, nil
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: strconv.Itoa(int(n)), Name: name, Input: json.RawMessage(input)}}}, "tool_use", llm.Usage{}, nil
	}})
	w := retestRequest(s.startFindingRetest, "POST", fid, `{"notes":"仅验证原接口"}`)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("provider not called")
	}
	dup := retestRequest(s.startFindingRetest, "POST", fid, `{}`)
	if dup.Code != 200 || !strings.Contains(dup.Body.String(), `"created":false`) {
		t.Fatalf("duplicate %d %s", dup.Code, dup.Body)
	}
	close(release)
	waitRetestIdle(t, s)
	rows, err := s.m.pg.ListFindingRetests(fid)
	if err != nil || len(rows) != 1 || rows[0].Status != "completed" || rows[0].Verdict != "inconclusive" {
		t.Fatalf("history=%+v err=%v", rows, err)
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected LLM calls", calls.Load())
	}
	f, _ := s.m.pg.GetFinding(fid)
	if f.Status != db.FindingPending || f.Evidence != "original proof" {
		t.Fatal("source modified")
	}
	listing := retestRequest(s.listFindingRetests, "GET", fid, "")
	if listing.Code != 200 || strings.Contains(listing.Body.String(), `"snapshot"`) {
		t.Fatalf("history %d %s", listing.Code, listing.Body)
	}
	res, err := s.findingRetestTools()[1].Call(intercept.WithConvID(t.Context(), *rows[0].ConversationID), json.RawMessage(`{"verdict":"fixed","summary":"bad overwrite","evidence":"bad"}`), nil)
	if err != nil || !res.IsError {
		t.Fatal("sealed result accepted", res, err)
	}
}

func TestRetestStopsFailuresAndNoVerdict(t *testing.T) {
	for _, mode := range []string{"stop", "failure", "no_verdict"} {
		t.Run(mode, func(t *testing.T) {
			s, fid := newRetestServer(t)
			setRetestProvider(s, retestProvider{complete: func(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
				if mode == "stop" {
					<-ctx.Done()
					return llm.Message{}, "", llm.Usage{}, ctx.Err()
				}
				if mode == "failure" {
					return llm.Message{}, "", llm.Usage{}, errors.New("test model failed")
				}
				return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("未保存结论")}}, "end_turn", llm.Usage{}, nil
			}})
			w := retestRequest(s.startFindingRetest, "POST", fid, `{}`)
			if w.Code != 202 {
				t.Fatalf("create %d %s", w.Code, w.Body)
			}
			if mode == "stop" {
				var body struct {
					Retest db.FindingRetest `json:"retest"`
				}
				_ = json.Unmarshal(w.Body.Bytes(), &body)
				stop := retestRequest(s.pgStopConversation, "POST", *body.Retest.ConversationID, `{}`)
				if stop.Code != 200 || !strings.Contains(stop.Body.String(), "stopping") {
					t.Fatalf("immediate stop: %d %s", stop.Code, stop.Body)
				}
			}
			waitRetestIdle(t, s)
			rows, _ := s.m.pg.ListFindingRetests(fid)
			want := "failed"
			if mode == "stop" {
				want = "stopped"
			}
			if len(rows) != 1 || rows[0].Status != want || rows[0].Error == "" {
				t.Fatalf("expected %s: %+v", want, rows)
			}
		})
	}
}

func TestRetestFixedUpdatesFindingThroughConversation(t *testing.T) {
	s, fid := newRetestServer(t)
	var calls atomic.Int32
	setRetestProvider(s, retestProvider{complete: func(context.Context, llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		if calls.Add(1) == 1 {
			return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.BlockToolUse, ID: "fixed-result", Name: "record_finding_retest_result", Input: json.RawMessage(`{"verdict":"fixed","summary":"修复生效","evidence":"原触发条件失效，正常对照仍可用。"}`)}}}, "tool_use", llm.Usage{}, nil
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("结论已保存")}}, "end_turn", llm.Usage{}, nil
	}})
	w := retestRequest(s.startFindingRetest, "POST", fid, `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	waitRetestIdle(t, s)
	f, err := s.m.pg.GetFinding(fid)
	if err != nil || f.Status != db.FindingFixed || f.Evidence != "original proof" {
		t.Fatalf("fixed finding=%+v err=%v", f, err)
	}
	// The new manual status is also accepted by the existing update API.
	w = retestRequest(s.patchFinding, "PATCH", fid, `{"status":"fixed"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"fixed"`) {
		t.Fatalf("manual fixed: %d %s", w.Code, w.Body)
	}
}

func TestRetestValidationSeedingAndScope(t *testing.T) {
	s, fid := newRetestServer(t)
	for _, tc := range []struct {
		id   int64
		body string
		code int
	}{{0, `{}`, 400}, {fid, `{`, 400}, {fid, `{"notes":"` + strings.Repeat("文", 4001) + `"}`, 400}, {999999999, `{}`, 404}, {fid, `{}`, 503}} {
		w := retestRequest(s.startFindingRetest, "POST", tc.id, tc.body)
		if w.Code != tc.code {
			t.Fatalf("code=%d want=%d %s", w.Code, tc.code, w.Body)
		}
	}
	rows, _ := s.m.pg.ListFindingRetests(fid)
	if len(rows) != 0 {
		t.Fatal("invalid request created state")
	}
	for _, tool := range s.findingRetestTools() {
		res, err := tool.Call(t.Context(), json.RawMessage(`{"verdict":"fixed","summary":"x","evidence":"y"}`), nil)
		if err != nil || !res.IsError {
			t.Fatal("unassociated conversation accepted", tool.Name(), res, err)
		}
	}
	a, _ := s.m.pg.GetAgentByKey(db.FindingRetestAgentKey)
	if a == nil || a.Builtin {
		t.Fatal("missing editable retester")
	}
	var triggers int
	if err := s.m.pg.QueryRow(`SELECT count(*) FROM agent_triggers WHERE agent_key=$1`, db.FindingRetestAgentKey).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatal("unexpected automatic trigger", triggers, err)
	}
	if _, err := s.m.pg.SavePrompt(a.ID, "customized retest prompt", "test", "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.seedFindingRetester(); err != nil {
		t.Fatal(err)
	}
	var prompt string
	if err := s.m.pg.QueryRow(`SELECT p.template_text FROM agent_prompts p JOIN agents a ON a.current_prompt_id=p.id WHERE a.key=$1`, a.Key).Scan(&prompt); err != nil || prompt != "customized retest prompt" {
		t.Fatal("seed overwrote prompt", prompt, err)
	}
	_, _ = s.m.pg.SavePrompt(a.ID, agent.RetesterDefaultPrompt, "restore", "test")
}
