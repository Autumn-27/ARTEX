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
