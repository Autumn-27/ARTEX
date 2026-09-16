package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestChatMentionParsing(t *testing.T) {
	refs, err := parseChatMentions("分析@[漏洞#12 同名] 与 @[接口#34 GET /api] @[漏洞#12 重复] user@example.com @漏洞")
	if err != nil || len(refs) != 2 || refs[0].Kind != "finding" || refs[1].ID != 34 {
		t.Fatalf("refs=%+v err=%v", refs, err)
	}
	for _, msg := range []string{"@[漏洞#0]", "@[漏洞#999999999999999999999999]"} {
		if _, err := parseChatMentions(msg); err == nil {
			t.Fatalf("accepted %q", msg)
		}
	}
	var msg strings.Builder
	for i := 1; i <= 11; i++ {
		fmt.Fprintf(&msg, "@[资产#%d] ", i)
	}
	if _, err := parseChatMentions(msg.String()); err == nil {
		t.Fatal("accepted more than 10 references")
	}
	if actual, err := composeChatMentionMessage(nil, "普通消息 user@example.com @漏洞"); err != nil || actual != "普通消息 user@example.com @漏洞" {
		t.Fatalf("plain chat changed: %s %v", actual, err)
	}
}

func TestChatMentionPagination(t *testing.T) {
	s, fid := newRetestServer(t)
	pg := s.m.pg
	query := fmt.Sprintf("mention-pages-%d", fid)
	for i := 0; i < 43; i++ {
		id, err := pg.AddFinding(0, 0, "xss", query, "low", query, "proof", "test", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pg.DeleteFinding(id) })
	}
	var companyID, assetID int64
	if err := pg.QueryRow(`INSERT INTO companies(name,nkey) VALUES($1,$1) RETURNING id`, query).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM companies WHERE id=$1`, companyID) })
	if err := pg.QueryRow(`INSERT INTO assets(type,app_name,bundle_id) VALUES('app',$1,$1) RETURNING id`, query).Scan(&assetID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM assets WHERE id=$1`, assetID) })
	cursor := ""
	seen := map[string]bool{}
	for pageNum := 0; ; pageNum++ {
		if pageNum > 3 {
			t.Fatal("pagination did not terminate")
		}
		page, err := pg.SearchChatMentionsPage(t.Context(), "", query, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > 20 {
			t.Fatal("page exceeded limit")
		}
		for _, item := range page.Items {
			key := fmt.Sprintf("%s:%d", item.Kind, item.ID)
			if seen[key] {
				t.Fatalf("duplicate %s", key)
			}
			seen[key] = true
		}
		if pageNum == 0 {
			if page.NextCursor == "" {
				t.Fatal("missing next page")
			}
			if _, err := pg.SearchChatMentionsPage(t.Context(), "", "changed", page.NextCursor); !errors.Is(err, db.ErrInvalidChatMentionCursor) {
				t.Fatalf("accepted stale cursor: %v", err)
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 45 {
		t.Fatalf("lost records: %d", len(seen))
	}
	page, err := pg.SearchChatMentionsPage(t.Context(), "finding", fmt.Sprint(fid), "")
	if err != nil || len(page.Items) == 0 || page.Items[0].ID != fid {
		t.Fatalf("exact id not first: %+v %v", page, err)
	}
	next, err := pg.SearchChatMentionsPage(t.Context(), "finding", fmt.Sprint(fid), page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range next.Items {
		if item.ID == fid {
			t.Fatal("exact id repeated on second page")
		}
	}
	w := httptest.NewRecorder()
	s.searchChatMentions(w, httptest.NewRequest("GET", "/api/chat/mentions?cursor=invalid", nil))
	if w.Code != 400 {
		t.Fatalf("bad cursor: %d", w.Code)
	}
}

func TestChatMentionWorkerReceivesServerDetails(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, m: m, engine: NewEngine(m)}
	task, err := m.CreateTask("Worker mention test", "Read referenced records", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = s.waitTaskQuiescent(drain, task.ID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	iid, err := task.Store.AddNode(db.KindIntent, map[string]any{"summary": "Read referenced records only"}, 1, "paused", "human", nil)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := m.pg.AddFinding(0, 0, "xss", "Worker mention", "low", "summary", "worker-hidden-proof", "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteFinding(fid)
	requests := make(chan llm.CompletionRequest, 10)
	worker := agent.NewWorker(retestProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		requests <- req
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已读取引用")}}, "end_turn", llm.Usage{}, nil
	}}, "test", m.dir, nil, 10000, 1)
	worker.SetNonStreaming(func() bool { return true })
	s.engine.UseLLM(nil, worker)
	send := func(message string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"message": message, "request_id": "worker-mention-test"})
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.SetPathValue("id", task.ID)
		r.SetPathValue("iid", fmt.Sprint(iid))
		w := httptest.NewRecorder()
		s.sendWorkerMessage(w, r)
		return w
	}
	if w := send("@[漏洞#9223372036854775807]"); w.Code != 400 {
		t.Fatalf("invalid ref accepted: %d %s", w.Code, w.Body)
	}
	node, _ := task.Store.GetNode(iid)
	items, _, _ := task.Store.ActivityList(&iid, 0, 100)
	if node.State != "paused" || len(items) != 0 {
		t.Fatal("invalid reference started worker or persisted a turn")
	}
	message := fmt.Sprintf("请核对 @[漏洞#%d 测试]", fid)
	if w := send(message); w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	select {
	case req := <-requests:
		blob, _ := json.Marshal(req.Messages)
		if !strings.Contains(string(blob), "worker-hidden-proof") || !strings.Contains(string(blob), "用户引用的记录快照") {
			t.Fatalf("worker missing reference details: %s", blob)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not receive a model request")
	}
	drain, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := s.waitTaskQuiescent(drain, task.ID); err != nil {
		t.Fatal(err)
	}
	items, _, err = task.Store.ActivityList(&iid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	userCount := 0
	for _, item := range items {
		if item.Kind == "user" {
			userCount++
			if item.Summary != message {
				t.Fatalf("UI history contains expanded details: %s", item.Summary)
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("human turns=%d", userCount)
	}
}

func TestChatMentionBoundedJSON(t *testing.T) {
	long := strings.Repeat("中文", 10000)
	items := make([]any, 102)
	for i := range items {
		items[i] = long
	}
	v := boundChatMentionValue(map[string]any{"report": long, "scope": items}).(map[string]any)
	if !strings.Contains(v["report"].(string), "已截断") || len(v["scope"].([]any)) != 101 {
		t.Fatal("missing truncation markers")
	}
	encoded, err := json.Marshal(v)
	if err != nil || !utf8.Valid(encoded) || !json.Valid(encoded) {
		t.Fatalf("invalid bounded JSON: %v", err)
	}
}

func TestChatMentionCatalogAndContext(t *testing.T) {
	s, fid := newRetestServer(t)
	pg := s.m.pg
	var cid int64
	if err := pg.QueryRow(`INSERT INTO companies(name,nkey) VALUES('引用测试公司','mention-test-company') RETURNING id`).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM companies WHERE id=$1`, cid) })
	if _, err := pg.Exec(`INSERT INTO company_scope(company_id,kind,domain,raw) VALUES($1,'domain','mention.example','mention.example')`, cid); err != nil {
		t.Fatal(err)
	}
	var refs strings.Builder
	fmt.Fprintf(&refs, "分析 @[漏洞#%d 客户端伪造标题] @[企业#%d 公司] ", fid, cid)
	for _, kind := range []string{"root_domain", "subdomain", "ip", "app", "service", "endpoint"} {
		var id int64
		if err := pg.QueryRow(`INSERT INTO assets(type,company_id,domain,ip,app_name,url,method,extra)
          VALUES($1,$2,$3,'192.0.2.81','引用测试应用',$4,'GET','{"note":"参数和扩展信息"}') RETURNING id`,
			kind, cid, kind+".mention.example", "https://"+kind+".mention.example/path").Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pg.Exec(`DELETE FROM assets WHERE id=$1`, id) })
		label := ""
		for name, value := range chatMentionKinds {
			if value == kind {
				label = name
			}
		}
		fmt.Fprintf(&refs, "@[%s#%d 条目] ", label, id)
		items, err := pg.SearchChatMentions(t.Context(), kind, fmt.Sprint(id))
		if err != nil || len(items) == 0 || items[0].ID != id || items[0].Kind != kind {
			t.Fatalf("search %s: %+v %v", kind, items, err)
		}
		if kind == "ip" {
			if _, err := composeChatMentionMessage(pg, fmt.Sprintf("@[应用#%d]", id)); err == nil {
				t.Fatal("accepted mismatched type")
			}
			if data, err := loadChatMention(pg, chatMentionRef{"asset", id, "资产"}); err != nil || data == nil {
				t.Fatalf("generic asset: %v", err)
			}
		}
	}
	if _, err := pg.Exec(`UPDATE findings SET report='完整报告内容',summary='最新摘要' WHERE id=$1`, fid); err != nil {
		t.Fatal(err)
	}
	msg, err := composeChatMentionMessage(pg, refs.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"完整报告内容", "最新摘要", "original proof", "mention.example", "引用测试公司", "参数和扩展信息"} {
		if !strings.Contains(msg, want) {
			t.Errorf("context missing %s", want)
		}
	}
	if _, err := composeChatMentionMessage(pg, "@[漏洞#9223372036854775807]"); err == nil {
		t.Fatal("accepted missing record")
	}
	for _, kind := range []string{"finding", "company", "asset", ""} {
		r := httptest.NewRequest("GET", "/api/chat/mentions?kind="+kind+"&q="+url.QueryEscape("引用"), nil)
		w := httptest.NewRecorder()
		s.searchChatMentions(w, r)
		if w.Code != 200 {
			t.Fatalf("search %s: %d %s", kind, w.Code, w.Body)
		}
	}
	for _, query := range []string{"kind=unsupported", "q=" + url.QueryEscape(strings.Repeat("字", 201))} {
		w := httptest.NewRecorder()
		s.searchChatMentions(w, httptest.NewRequest("GET", "/api/chat/mentions?"+query, nil))
		if w.Code != 400 {
			t.Fatalf("invalid search: %d", w.Code)
		}
	}
	// Wildcards are literal input; no whole-table match for a '%' query.
	items, err := pg.SearchChatMentions(t.Context(), "company", "%")
	if err != nil || len(items) != 0 {
		t.Fatalf("literal wildcard: %+v %v", items, err)
	}
}

func TestChatMentionConversationReceivesServerDetails(t *testing.T) {
	s, fid := newRetestServer(t)
	setRetestProvider(s, retestProvider{complete: func(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
		blob, _ := json.Marshal(req.Messages)
		if !strings.Contains(string(blob), "original proof") || !strings.Contains(string(blob), "用户引用的记录快照") {
			t.Errorf("model did not receive resolved evidence: %s", blob)
		}
		return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("已读取引用")}}, "end_turn", llm.Usage{}, nil
	}})
	c, err := s.m.pg.CreateConversation("auto", "引用测试", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { waitRetestIdle(t, s); _, _ = s.m.pg.Exec(`DELETE FROM conversations WHERE id=$1`, c.ID) })
	message := fmt.Sprintf("请查看 @[漏洞#%d 示例]", fid)
	body, _ := json.Marshal(map[string]any{"message": message})
	w := retestRequest(s.pgSendConversationMessage, http.MethodPost, c.ID, string(body))
	if w.Code != 202 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	waitRetestIdle(t, s)
	items, _, err := s.m.pg.ConvActivityList(c.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	foundUser, foundResult := false, false
	for _, item := range items {
		if item.Kind == "user" {
			foundUser = true
			if item.Summary != message {
				t.Fatalf("history leaked expanded context: %s", item.Summary)
			}
		}
		if item.Kind == "result" {
			foundResult = true
		}
	}
	if !foundUser || !foundResult {
		t.Fatalf("turn incomplete: user=%v result=%v", foundUser, foundResult)
	}
	w = retestRequest(s.pgSendConversationMessage, http.MethodPost, c.ID, `{"message":"@[漏洞#9223372036854775807]"}`)
	if w.Code != 400 {
		t.Fatalf("missing ref send: %d %s", w.Code, w.Body)
	}
	after, _, _ := s.m.pg.ConvActivityList(c.ID, 0, 100)
	if len(items) != len(after) {
		t.Fatal("invalid reference persisted a turn")
	}
}
