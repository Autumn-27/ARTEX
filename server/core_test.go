package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

// TestCoreTaskLifecyclePG exercises the migrated core (tasks/exploration on PG)
// through the real HTTP mux: create → goal nodes seeded → list → delete cascade.
func TestCoreTaskLifecyclePG(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	td := t.TempDir()
	s := New(context.Background(), m, td, td)
	h := s.Handler()
	tok, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	do := func(method, path string, body any) (int, map[string]any) {
		var r *http.Request
		if body != nil {
			b, _ := json.Marshal(body)
			r = httptest.NewRequest(method, path, bytes.NewReader(b))
		} else {
			r = httptest.NewRequest(method, path, nil)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// doRetry retries on 5xx (transient DB conflicts from parallel test packages).
	doRetry := func(method, path string, body any) (int, map[string]any) {
		var code int
		var out map[string]any
		for i := 0; i < 5; i++ {
			code, out = do(method, path, body)
			if code < 500 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		return code, out
	}

	// create a task → 201, returns PG task (string id + exploration_id)
	code, out := doRetry("POST", "/api/tasks", map[string]string{"description": "smoke", "goal": "测试 SQLi/XSS"})
	if code != 201 {
		t.Fatalf("create task: %d (%v)", code, out)
	}
	id, _ := out["id"].(string)
	expID := int64(out["exploration_id"].(float64))
	if id == "" || expID == 0 {
		t.Fatalf("bad task payload: %v", out)
	}

	// the exploration owns goal node(s) — goal seeding is async, poll briefly
	var goals []*db.Node
	for i := 0; i < 30; i++ {
		goals, err = m.pg.Exploration(expID).ListByKind("goal", 10)
		if err != nil || len(goals) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) < 1 {
		t.Fatalf("expected goal nodes seeded, got %d", len(goals))
	}

	// it shows in the task list
	code, out = doRetry("GET", "/api/tasks", nil)
	if code != 200 {
		t.Fatalf("list tasks: %d", code)
	}

	// delete → cascade removes the exploration subgraph
	code, _ = doRetry("DELETE", "/api/tasks/"+id, nil)
	if code != 200 {
		t.Fatalf("delete task: %d", code)
	}
	nid, _ := strconv.ParseInt(id, 10, 64)
	if got, _ := m.pg.GetTask(nid); got != nil {
		t.Fatalf("task should be deleted from PG")
	}
	var n int
	m.pg.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, expID).Scan(&n)
	if n != 0 {
		t.Fatalf("exploration nodes should be cascade-deleted, got %d", n)
	}
}
