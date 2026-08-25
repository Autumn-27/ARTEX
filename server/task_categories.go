package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
)

const maxTaskCategoryRequestBytes = 16 << 10

type taskCategoryRequest struct {
	Name *string `json:"name"`
}

func decodeTaskCategoryRequest(w http.ResponseWriter, r *http.Request) (*taskCategoryRequest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCategoryRequestBytes)
	var request taskCategoryRequest
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return nil, false
	}
	if request.Name == nil || strings.TrimSpace(*request.Name) == "" {
		writeErr(w, http.StatusBadRequest, "分类名称不能为空")
		return nil, false
	}
	if utf8.RuneCountInString(strings.TrimSpace(*request.Name)) > db.MaxTaskCategoryNameRunes {
		writeErr(w, http.StatusBadRequest, "分类名称最多 80 个字符")
		return nil, false
	}
	return &request, true
}

func writeTaskCategoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTaskCategoryInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrTaskCategoryNameConflict):
		writeErr(w, http.StatusConflict, "分类名称已存在")
	case errors.Is(err, db.ErrTaskCategoryNotFound), errors.Is(err, db.ErrTaskCategoryTaskNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) pgListTaskCategories(w http.ResponseWriter, _ *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	categories, err := pg.ListTaskCategories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": categories})
}

func (s *Server) pgCreateTaskCategory(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	request, ok := decodeTaskCategoryRequest(w, r)
	if !ok {
		return
	}
	category, err := pg.CreateTaskCategory(*request.Name)
	if err != nil {
		writeTaskCategoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, category)
}

func (s *Server) pgRenameTaskCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad task category id")
		return
	}
	request, ok := decodeTaskCategoryRequest(w, r)
	if !ok {
		return
	}
	category, err := s.m.RenameTaskCategory(id, *request.Name)
	if err != nil {
		writeTaskCategoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, category)
}

func (s *Server) pgDeleteTaskCategory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad task category id")
		return
	}
	deleted, err := s.m.DeleteTaskCategory(id)
	if err != nil {
		writeTaskCategoryError(w, err)
		return
	}
	if !deleted {
		writeErr(w, http.StatusNotFound, "task category not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// parseCategoryIDField reads the category_id field shared by the single-task and
// batch endpoints. The field is required but may be null — that clears the
// category — so it arrives as RawMessage instead of *int64, which cannot tell
// an explicit null from an omitted key.
func parseCategoryIDField(w http.ResponseWriter, raw json.RawMessage) (*int64, bool) {
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "category_id is required")
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "任务分类 id 无效")
		return nil, false
	}
	return &id, true
}

func (s *Server) updateTaskCategory(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	if _, ok := s.m.Task(taskID); !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCategoryRequestBytes)
	var request struct {
		CategoryID json.RawMessage `json:"category_id"`
	}
	if err := decode(r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	categoryID, ok := parseCategoryIDField(w, request.CategoryID)
	if !ok {
		return
	}
	if _, err := s.m.SetTaskCategory(taskID, categoryID); err != nil {
		writeTaskCategoryError(w, err)
		return
	}
	task, _ := s.m.Task(taskID)
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}

// batchCategoryItem reports one requested task. Unlike batch pause/resume there
// is no per-task runtime outcome, so only the failure reason is carried.
type batchCategoryItem struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// updateTasksCategoryBatch moves every selected task into one category, or
// clears it when category_id is null. The database write is atomic; per-task
// entries only distinguish ids that never resolved to a live task, so a partial
// success here means those tasks were deleted, not that the move half-applied.
func (s *Server) updateTasksCategoryBatch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskCategoryRequestBytes)
	var request struct {
		TaskIDs    []string        `json:"task_ids"`
		CategoryID json.RawMessage `json:"category_id"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	categoryID, ok := parseCategoryIDField(w, request.CategoryID)
	if !ok {
		return
	}
	taskIDs := normalizeBatchTaskIDs(request.TaskIDs)
	if len(taskIDs) == 0 || len(taskIDs) > db.MaxTaskCategoryBatchSize {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("task_ids 数量必须为 1-%d", db.MaxTaskCategoryBatchSize))
		return
	}
	items := make([]batchCategoryItem, 0, len(taskIDs))
	pending := make([]string, 0, len(taskIDs))
	for _, parsed := range taskIDs {
		item := batchCategoryItem{ID: parsed.id}
		switch {
		case !parsed.valid:
			item.Error = "bad task id"
		default:
			if _, live := s.m.Task(parsed.id); live {
				pending = append(pending, parsed.id)
			} else {
				item.Error = "task not found"
			}
		}
		items = append(items, item)
	}
	var category *db.TaskCategory
	if len(pending) > 0 {
		updated, moved, err := s.m.SetTasksCategory(pending, categoryID)
		if err != nil {
			writeTaskCategoryError(w, err)
			return
		}
		category = moved
		for i := range items {
			if items[i].Error != "" {
				continue
			}
			if updated[items[i].ID] {
				items[i].OK = true
			} else {
				items[i].Error = "task not found"
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "category": category})
}
