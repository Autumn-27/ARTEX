package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTaskCategoryBatchRoute pins the contract of the batch move endpoint: it is
// registered ahead of the /api/tasks/{id}/... patterns, it validates the request
// before touching the database, and it reports unknown ids per task instead of
// failing the whole batch.
func TestTaskCategoryBatchRoute(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()

	td := t.TempDir()
	s := New(context.Background(), m, td, td, td)
	h := s.Handler()
	token, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/tasks/category/batch", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// category_id is required, and omitting it must not be read as "uncategorize".
	if rec := post(`{"task_ids":["1"]}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "category_id is required") {
		t.Fatalf("missing category_id: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// An empty selection is rejected before any database work.
	if rec := post(`{"task_ids":[],"category_id":null}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "task_ids 数量必须为") {
		t.Fatalf("empty task_ids: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Unknown and malformed ids come back as per-task failures on a 200, so the
	// caller can tell exactly which selections went stale.
	rec := post(`{"task_ids":["999999999","abc","999999999"],"category_id":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Items []struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"items"`
		Category *struct {
			ID int64 `json:"id"`
		} `json:"category"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	// The duplicate id is collapsed, so two entries remain.
	if len(response.Items) != 2 {
		t.Fatalf("items=%+v, want 2 after de-duplication", response.Items)
	}
	if response.Items[0].ID != "999999999" || response.Items[0].OK || response.Items[0].Error != "task not found" {
		t.Fatalf("unknown id entry=%+v", response.Items[0])
	}
	if response.Items[1].ID != "abc" || response.Items[1].OK || response.Items[1].Error != "bad task id" {
		t.Fatalf("malformed id entry=%+v", response.Items[1])
	}
	if response.Category != nil {
		t.Fatalf("category=%+v, want null when clearing", response.Category)
	}
}
