package server

import (
	"bytes"
	"encoding/json"
	"errors"
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
	if len(request.CategoryID) == 0 {
		writeErr(w, http.StatusBadRequest, "category_id is required")
		return
	}
	var categoryID *int64
	if !bytes.Equal(bytes.TrimSpace(request.CategoryID), []byte("null")) {
		var id int64
		if err := json.Unmarshal(request.CategoryID, &id); err != nil {
			writeErr(w, http.StatusBadRequest, "任务分类 id 无效")
			return
		}
		categoryID = &id
	}
	if categoryID != nil && *categoryID <= 0 {
		writeErr(w, http.StatusBadRequest, "任务分类 id 无效")
		return
	}
	if _, err := s.m.SetTaskCategory(taskID, categoryID); err != nil {
		writeTaskCategoryError(w, err)
		return
	}
	task, _ := s.m.Task(taskID)
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}
