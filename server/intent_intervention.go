package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

const maxWorkerMessageBytes = 64 << 10

func validWorkerMessageRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':' {
			continue
		}
		return false
	}
	return true
}

// sendWorkerMessage pauses a running Worker when necessary, records one user
// turn for its existing conversation, and immediately schedules that same
// intent to continue. A disconnected browser cannot strand an accepted message.
func (s *Server) sendWorkerMessage(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil || iid <= 0 {
		writeErr(w, http.StatusBadRequest, "bad intent id")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkerMessageBytes)
	var req struct {
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体过大")
			return
		}
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	message := strings.TrimSpace(req.Message)
	requestID := strings.TrimSpace(req.RequestID)
	if message == "" {
		writeErr(w, http.StatusBadRequest, "消息不能为空")
		return
	}
	if len([]rune(message)) > 4000 {
		writeErr(w, http.StatusBadRequest, "消息不能超过 4000 个字符")
		return
	}
	if !validWorkerMessageRequestID(requestID) {
		writeErr(w, http.StatusBadRequest, "request_id 必须是 1-128 位字母、数字、-、_、. 或 :")
		return
	}

	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, http.StatusConflict, "任务正在删除，无法向 Worker 发送消息")
		return
	}
	defer s.engine.decInflight(t.ID)

	node, err := t.Store.GetNode(iid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if node == nil {
		if inherited, sourceErr := t.Store.GetNodeWithSources(iid); sourceErr == nil && inherited != nil && inherited.Inherited {
			writeErr(w, http.StatusConflict, "继承意图为只读，不能发送 Worker 消息")
			return
		}
		writeErr(w, http.StatusNotFound, "intent not found")
		return
	}
	if node.Kind != db.KindIntent {
		writeErr(w, http.StatusConflict, "node is not an intent")
		return
	}
	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	opCtx, cancel := context.WithTimeout(base, workControlWaitTimeout+15*time.Second)
	defer cancel()
	result, err := s.engine.InterveneWork(opCtx, t, iid, requestID, message)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			writeErr(w, http.StatusGatewayTimeout, err.Error())
		case errors.Is(err, errWorkInterventionConflict), errors.Is(err, errWorkControlConflict),
			errors.Is(err, db.ErrIntentInterventionConflict), errors.Is(err, db.ErrIntentStateConflict):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":           iid,
		"state":        result.State,
		"accepted":     true,
		"activity_seq": result.ActivityID,
		"request_id":   result.RequestID,
		"duplicate":    result.Duplicate,
	})
}
