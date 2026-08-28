package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const maxFindingFollowUpRunes = 4000

func findingPaginationParam(raw string, fallback, upperBound int) int {
	value := atoiDefault(raw, fallback)
	if value <= 0 {
		value = fallback
	}
	if upperBound > 0 && value > upperBound {
		value = upperBound
	}
	return value
}

// findingGroups serves the task-group page used by the global findings view.
// Findings inside a group continue to use the existing flat findings endpoint,
// preserving dashboard/export clients and giving every expanded group its own
// independent page cursor.
func (s *Server) findingGroups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := findingPaginationParam(q.Get("page"), 1, 0)
	limit := findingPaginationParam(q.Get("limit"), 10, 100)
	filter := db.FindingFilter{
		Severity:  normFilter(q.Get("severity")),
		Status:    normFilter(q.Get("status")),
		VulnClass: normFilter(q.Get("vulnclass")),
		TaskID:    normFilter(q.Get("task_id")),
		Query:     q.Get("q"),
		Sort:      q.Get("sort"),
	}
	groups, total, findingTotal, err := s.m.pg.ListFindingGroups(filter, page, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// PostgreSQL owns paused/queued persistence. The live engine adds the same
	// derived running/idle view used by task DTOs, so finding groups do not lag
	// behind the task list while an Agent is actively executing.
	if s.engine != nil {
		for i := range groups {
			if groups[i].TaskID == nil {
				continue
			}
			if task, ok := s.m.Task(i64s(*groups[i].TaskID)); ok {
				groups[i].TaskStatus = s.resolvedTaskStatus(task)
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"items":         groups,
		"total":         total,
		"finding_total": findingTotal,
		"page":          page,
		"page_size":     limit,
	})
}

// deepenFinding creates a high-priority human Worker intent under the finding's
// original task. The DB write validates and links the live finding node; task
// admission then revives or queues the task through the shared concurrency path.
func (s *Server) deepenFinding(w http.ResponseWriter, r *http.Request) {
	id := int64(atoiDefault(r.PathValue("id"), 0))
	if id <= 0 {
		writeErr(w, 400, "bad finding id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	var req struct {
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		}
		return
	}
	description := strings.TrimSpace(req.Description)
	switch {
	case description == "":
		writeErr(w, 400, "description is required")
		return
	case utf8.RuneCountInString(description) > maxFindingFollowUpRunes:
		writeErr(w, 400, fmt.Sprintf("description must be at most %d characters", maxFindingFollowUpRunes))
		return
	}

	finding, err := s.m.pg.GetFinding(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if finding == nil {
		writeErr(w, 404, "finding not found")
		return
	}
	if finding.TaskID == nil || finding.NodeID == nil {
		writeErr(w, 409, "finding origin task or node is no longer available")
		return
	}
	t, ok := s.m.Task(i64s(*finding.TaskID))
	if !ok || t == nil {
		writeErr(w, 409, "finding origin task is no longer available")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "task is being deleted")
		return
	}
	defer s.engine.decInflight(t.ID)

	audit := db.Activity{
		Worker:  "system",
		Kind:    "text",
		Summary: "人工提交漏洞深入利用意图",
		Detail:  description,
	}
	intentID, audit, err := t.Store.AddFindingFollowUpIntent(id, *finding.NodeID, description, audit)
	if errors.Is(err, db.ErrFindingOriginUnavailable) {
		writeErr(w, 409, "finding origin task or node is no longer available")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	queued, err := s.admitTask(t, "resume")
	if err != nil {
		if rollbackErr := t.Store.DiscardOpenIntent(intentID); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("discard follow-up intent %d: %w", intentID, rollbackErr))
		}
		writeErr(w, 500, err.Error())
		return
	}
	// The audit row committed atomically with the intent. Publish that exact row;
	// emitActivity would insert a second copy and break DB/SSE correspondence.
	s.engine.Broadcaster().Publish(t.ID, audit)
	s.engine.touch(t.ID)
	// Admission can wake a Worker immediately. Report the persisted intent state
	// observed at response time instead of always claiming it is still open. The
	// task-level queued flag remains separate because a queued task intentionally
	// keeps its Worker intent open until it receives a concurrency slot.
	state := "open"
	if current, stateErr := t.Store.GetNode(intentID); stateErr == nil && current != nil {
		state = current.State
	}
	writeJSON(w, 200, map[string]any{
		"task_id":   t.ID,
		"intent_id": i64s(intentID),
		"state":     state,
		"queued":    queued,
	})
}
