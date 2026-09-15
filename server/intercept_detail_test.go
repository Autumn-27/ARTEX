package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestInterceptDetailHTTP(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	// Exercise the authenticated HTTP surface without starting unrelated task
	// schedulers or mutating the process-global tool assembly used by other tests.
	m := &Manager{pg: d, interceptor: intercept.New(d)}
	s := &Server{m: m, jwtKey: []byte("approval-http-test-signing-key")}
	h := s.Handler()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.pg.CreateInterceptPending(0, 0, "approval-http", "test", "Write", []byte(`{}`), "[模型] 确认", &db.InterceptAudit{InitialAction: "ask", UserMessage: "snapshot", ExecutionStatus: "not_started"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.pg.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
	do := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	path := fmt.Sprintf("/api/intercept/history/%d", id)
	if r := do(http.MethodGet, path, "", false); r.Code != 401 {
		t.Fatalf("unprotected detail: %d", r.Code)
	}
	executionPath := path + "/execution"
	if r := do(http.MethodGet, executionPath, "", false); r.Code != 401 {
		t.Fatal("unprotected execution source")
	}
	if r := do(http.MethodGet, executionPath, "", true); r.Code != 409 {
		t.Fatalf("legacy source: %d", r.Code)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/intercept/history/0/execution", 400}, {"/api/intercept/history/not-a-number/execution", 400}, {"/api/intercept/history/9223372036854775807/execution", 404}} {
		if r := do(http.MethodGet, tc.path, "", true); r.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, r.Code)
		}
	}

	conv, err := d.CreateConversation("test", "approval navigation", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM conversations WHERE id=$1`, conv.ID) })
	seq, err := d.AppendConvActivity(conv.ID, db.Activity{Worker: "test", Kind: "tool_use", Tool: "Bash", ToolUseID: "http-source", Summary: "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := d.CreateInterceptPending(0, conv.ID, "", "test", "Bash", []byte(`{"command":"pwd"}`), "review", &db.InterceptAudit{ToolUseID: "http-source", Correlation: "exact"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, sourceID) })
	sourceReply := do(http.MethodGet, fmt.Sprintf("/api/intercept/history/%d/execution", sourceID), "", true)
	var execution struct {
		Seq            int64         `json:"seq"`
		ConversationID int64         `json:"conversation_id"`
		Items          []ActivityDTO `json:"items"`
	}
	if sourceReply.Code != 200 || json.Unmarshal(sourceReply.Body.Bytes(), &execution) != nil || execution.Seq != seq || execution.ConversationID != conv.ID || len(execution.Items) != 1 || execution.Items[0].Seq != seq {
		t.Fatalf("source reply: %d %s", sourceReply.Code, sourceReply.Body.String())
	}

	if err := d.DeleteConversation(conv.ID); err != nil {
		t.Fatal(err)
	}
	deletedReply := do(http.MethodGet, fmt.Sprintf("/api/intercept/history/%d/execution?conversation=%d", sourceID, conv.ID), "", true)
	if deletedReply.Code != 410 || !strings.Contains(deletedReply.Body.String(), "对话已被删除") {
		t.Fatalf("deleted conversation: %d %s", deletedReply.Code, deletedReply.Body.String())
	}

	r := do(http.MethodGet, path, "", true)
	var detail db.InterceptDetail
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &detail) != nil || detail.Audit == nil || detail.Audit.UserMessage != "snapshot" {
		t.Fatalf("detail: %d %s", r.Code, r.Body.String())
	}
	for _, tc := range []struct {
		path string
		code int
	}{{"/api/intercept/history/not-a-number", 400}, {"/api/intercept/history/0", 400}, {"/api/intercept/history/9223372036854775807", 404}} {
		if r := do(http.MethodGet, tc.path, "", true); r.Code != tc.code {
			t.Fatalf("%s: %d", tc.path, r.Code)
		}
	}
	decisionPath := fmt.Sprintf("/api/intercept/pending/%d/decide", id)
	if r := do(http.MethodPost, decisionPath, `{"decision":"denied"}`, true); r.Code != 200 {
		t.Fatalf("decide: %d %s", r.Code, r.Body.String())
	}
	if r := do(http.MethodPost, decisionPath, `{"decision":"allowed"}`, true); r.Code != 409 {
		t.Fatalf("duplicate decision: %d", r.Code)
	}
}
