package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestFindingPaginationParam(t *testing.T) {
	for _, test := range []struct {
		raw, name             string
		fallback, upper, want int
	}{
		{name: "missing", raw: "", fallback: 10, upper: 100, want: 10},
		{name: "invalid", raw: "not-a-number", fallback: 10, upper: 100, want: 10},
		{name: "zero", raw: "0", fallback: 10, upper: 100, want: 10},
		{name: "negative", raw: "-2", fallback: 1, upper: 0, want: 1},
		{name: "bounded", raw: "500", fallback: 10, upper: 100, want: 100},
		{name: "valid", raw: "25", fallback: 10, upper: 100, want: 25},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := findingPaginationParam(test.raw, test.fallback, test.upper); got != test.want {
				t.Fatalf("findingPaginationParam(%q,%d,%d)=%d, want %d",
					test.raw, test.fallback, test.upper, got, test.want)
			}
		})
	}
}

func TestFindingGroupsReturnsTaskBucketsAndNormalizesPagination(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	marker := fmt.Sprintf("__server_finding_groups_%d__", time.Now().UnixNano())
	createdTaskIDs := []int64{}
	defer func() {
		_, _ = m.pg.Exec(`DELETE FROM findings WHERE vulnclass=$1`, marker)
		for _, taskID := range createdTaskIDs {
			_ = m.pg.DeleteTask(taskID)
		}
	}()

	taskA, err := m.CreateTask("finding group A", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	createdTaskIDs = append(createdTaskIDs, mustTaskID(t, taskA.ID))
	taskB, err := m.CreateTask("finding group B", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	createdTaskIDs = append(createdTaskIDs, mustTaskID(t, taskB.ID))

	for i, seed := range []struct {
		taskID   string
		severity string
	}{
		{taskA.ID, db.SeverityCritical},
		{taskA.ID, db.SeverityHigh},
		{taskB.ID, db.SeverityLow},
	} {
		if _, err := m.pg.AddFinding(mustTaskID(t, seed.taskID), 0, marker, "", seed.severity,
			fmt.Sprintf("finding %d", i), "", "test", nil); err != nil {
			t.Fatalf("AddFinding[%d]: %v", i, err)
		}
	}

	s := &Server{m: m}
	request := httptest.NewRequest(http.MethodGet,
		"/api/exploration/findings/groups?vulnclass="+marker+"&sort=severity&page=1&limit=1", nil)
	recorder := httptest.NewRecorder()
	s.findingGroups(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("groups status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var page struct {
		Items        []db.FindingGroup `json:"items"`
		Total        int               `json:"total"`
		FindingTotal int               `json:"finding_total"`
		Page         int               `json:"page"`
		PageSize     int               `json:"page_size"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Total != 2 || page.FindingTotal != 3 || page.Page != 1 || page.PageSize != 1 {
		t.Fatalf("unexpected grouped page: %+v", page)
	}
	if page.Items[0].TaskID == nil || *page.Items[0].TaskID != mustTaskID(t, taskA.ID) ||
		page.Items[0].Count != 2 || page.Items[0].Critical != 1 || page.Items[0].High != 1 {
		t.Fatalf("unexpected first group: %+v", page.Items[0])
	}

	request = httptest.NewRequest(http.MethodGet,
		"/api/exploration/findings/groups?vulnclass="+marker+"&page=-4&limit=0", nil)
	recorder = httptest.NewRecorder()
	s.findingGroups(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("normalized groups status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	page = struct {
		Items        []db.FindingGroup `json:"items"`
		Total        int               `json:"total"`
		FindingTotal int               `json:"finding_total"`
		Page         int               `json:"page"`
		PageSize     int               `json:"page_size"`
	}{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Page != 1 || page.PageSize != 10 || len(page.Items) != 2 {
		t.Fatalf("pagination was not normalized: %+v", page)
	}
}

func TestDeepenFindingCreatesAuditedIntentAndRevivesTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()

	task, err := m.CreateTask("deepen finding", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := task.Store
	findingNodeID, err := store.AddNode(db.KindFinding, map[string]any{"summary": "source"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer m.pg.DeleteTask(taskID)
	findingID, err := m.pg.AddFinding(taskID, findingNodeID, "test", "source", db.SeverityHigh, "source", "", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteFinding(findingID)
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // admission is exercised without starting background planner/worker work
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx}
	live, unsubscribe := s.engine.Broadcaster().Subscribe(task.ID)
	defer unsubscribe()
	body := bytes.NewBufferString(`{"description":"验证完整利用链并保留可复现证据"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", body)
	req.SetPathValue("id", strconv.FormatInt(findingID, 10))
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deepen status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		TaskID   string `json:"task_id"`
		IntentID string `json:"intent_id"`
		State    string `json:"state"`
		Queued   bool   `json:"queued"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	intentID, err := strconv.ParseInt(response.IntentID, 10, 64)
	if err != nil || intentID <= 0 || response.TaskID != task.ID || response.State != "open" || response.Queued {
		t.Fatalf("unexpected response: %+v", response)
	}
	intent, err := store.GetNode(intentID)
	if err != nil || intent == nil {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	if intent.Priority != 10 || intent.Origin != "human" || intent.State != "open" {
		t.Fatalf("intent metadata: %+v", intent)
	}
	if task.Status != "running" || task.Paused || task.Queued {
		t.Fatalf("task was not revived through admission: %+v", task)
	}
	activity, _, err := store.ActivityList(&intentID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var persistedAudit db.Activity
	auditCount := 0
	for _, item := range activity {
		if item.Worker == "system" && strings.Contains(item.Summary, "人工提交漏洞深入利用意图") {
			persistedAudit = item
			auditCount++
		}
	}
	if auditCount != 1 || persistedAudit.ID <= 0 {
		t.Fatalf("atomic system audit count=%d activity=%+v", auditCount, activity)
	}
	select {
	case broadcast := <-live:
		if broadcast.ID != persistedAudit.ID || broadcast.NodeID == nil || *broadcast.NodeID != intentID ||
			broadcast.CreatedAt.IsZero() {
			t.Fatalf("broadcast must reuse committed activity: broadcast=%+v persisted=%+v", broadcast, persistedAudit)
		}
	default:
		t.Fatal("committed follow-up audit was not broadcast")
	}
	select {
	case duplicate := <-live:
		t.Fatalf("follow-up audit was broadcast more than once: %+v", duplicate)
	default:
	}

	// The retained finding can no longer be deepened after its task and node are
	// deleted; this must be a conflict rather than silently creating orphan work.
	if err := m.pg.DeleteTask(taskID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", bytes.NewBufferString(`{"description":"retry"}`))
	req.SetPathValue("id", strconv.FormatInt(findingID, 10))
	rec = httptest.NewRecorder()
	s.deepenFinding(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("deleted origin status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeepenFindingValidatesDescription(t *testing.T) {
	s := &Server{}
	tooLong, _ := json.Marshal(map[string]string{"description": strings.Repeat("x", maxFindingFollowUpRunes+1)})
	for _, body := range []string{`{}`, `{"description":"   "}`, string(tooLong)} {
		req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", bytes.NewBufferString(body))
		req.SetPathValue("id", "1")
		rec := httptest.NewRecorder()
		s.deepenFinding(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
	oversized := `{"description":"` + strings.Repeat("x", (32<<10)+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen", strings.NewReader(oversized))
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d response=%s", rec.Code, rec.Body.String())
	}
}

func TestDeepenAdmissionFailureDiscardsFollowUpIntent(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("deepen rollback", "leave no orphan intent", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	defer func() {
		_, _ = m.pg.Exec(`UPDATE tasks SET deleted_at=NULL WHERE id=$1`, taskID)
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{})
	}()
	findingNodeID, err := task.Store.AddNode(db.KindFinding, map[string]any{"summary": "source"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := m.pg.AddFinding(taskID, findingNodeID, "test", "source", db.SeverityHigh, "source", "", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := task.Store.ListByKind(db.KindIntent, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var activitiesBefore int
	if err := m.pg.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1`, task.Store.ID()).Scan(&activitiesBefore); err != nil {
		t.Fatal(err)
	}
	if err := m.SetTaskStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	// Keep the in-memory handle and graph available while forcing the atomic
	// admission UPDATE to reject this soft-deleted row.
	if _, err := m.pg.Exec(`UPDATE tasks SET deleted_at=now() WHERE id=$1`, taskID); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Server{m: m, engine: NewEngine(m), ctx: ctx}
	req := httptest.NewRequest(http.MethodPost, "/api/exploration/findings/1/deepen",
		bytes.NewBufferString(`{"description":"this intent must be rolled back"}`))
	req.SetPathValue("id", strconv.FormatInt(findingID, 10))
	rec := httptest.NewRecorder()
	s.deepenFinding(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := task.Store.ListByKind(db.KindIntent, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed admission left an orphan follow-up: before=%d after=%d nodes=%+v", len(before), len(after), after)
	}
	var activitiesAfter int
	if err := m.pg.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1`, task.Store.ID()).Scan(&activitiesAfter); err != nil {
		t.Fatal(err)
	}
	if activitiesAfter != activitiesBefore {
		t.Fatalf("failed admission left an orphan audit: before=%d after=%d", activitiesBefore, activitiesAfter)
	}
}
