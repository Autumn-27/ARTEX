package server

import (
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// ---------- P3 agent triggers (仅自定义 agent) ----------

func (s *Server) pgListTriggers(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	trs, err := pg.ListTriggersFor(a.Key)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"triggers": trs})
}

type triggerReq struct {
	Enabled            bool     `json:"enabled"`
	IntervalSec        int      `json:"interval_sec"`
	OnFinding          bool     `json:"on_finding"`
	OnGoalMet          bool     `json:"on_goal_met"`
	OnTaskTimeout      bool     `json:"on_task_timeout"`
	OnToolCall         bool     `json:"on_tool_call"`
	OnTaskCreate       bool     `json:"on_task_create"`
	IntervalMessage    string   `json:"interval_message"`
	FindingMessage     string   `json:"finding_message"`
	GoalMessage        string   `json:"goal_message"`
	TaskTimeoutMessage string   `json:"task_timeout_message"`
	ToolCallMessage    string   `json:"tool_call_message"`
	TaskCreateMessage  string   `json:"task_create_message"`
	ToolNames          []string `json:"tool_names"`
}

// validateTrigger enforces the shared trigger rules for create/update:
// at least one condition, and on_tool_call requires a non-empty tool set.
func validateTrigger(req *triggerReq) string {
	if req.IntervalSec == 0 && !req.OnFinding && !req.OnGoalMet && !req.OnTaskTimeout && !req.OnToolCall && !req.OnTaskCreate {
		return "至少选择一种触发条件(定时/发现finding/目标达成/任务超时/工具调用/任务创建)"
	}
	if req.OnToolCall && len(req.ToolNames) == 0 {
		return "工具调用触发至少选择一个工具"
	}
	return ""
}

func (s *Server) pgCreateTrigger(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if a.Builtin {
		writeErr(w, 400, "触发器仅支持自定义 agent")
		return
	}
	var req triggerReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.IntervalSec < 0 {
		req.IntervalSec = 0
	}
	if msg := validateTrigger(&req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	tr, err := pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		OnFinding: req.OnFinding, OnGoalMet: req.OnGoalMet, OnTaskTimeout: req.OnTaskTimeout, OnToolCall: req.OnToolCall, OnTaskCreate: req.OnTaskCreate,
		IntervalMessage: req.IntervalMessage, FindingMessage: req.FindingMessage,
		GoalMessage: req.GoalMessage, TaskTimeoutMessage: req.TaskTimeoutMessage,
		ToolCallMessage: req.ToolCallMessage, TaskCreateMessage: req.TaskCreateMessage, ToolNames: req.ToolNames,
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, tr)
}

func (s *Server) pgUpdateTrigger(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad trigger id")
		return
	}
	var req triggerReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.IntervalSec < 0 {
		req.IntervalSec = 0
	}
	if msg := validateTrigger(&req); msg != "" {
		writeErr(w, 400, msg)
		return
	}
	if err := pg.UpdateTrigger(&db.AgentTrigger{
		ID: id, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		OnFinding: req.OnFinding, OnGoalMet: req.OnGoalMet, OnTaskTimeout: req.OnTaskTimeout, OnToolCall: req.OnToolCall, OnTaskCreate: req.OnTaskCreate,
		IntervalMessage: req.IntervalMessage, FindingMessage: req.FindingMessage,
		GoalMessage: req.GoalMessage, TaskTimeoutMessage: req.TaskTimeoutMessage,
		ToolCallMessage: req.ToolCallMessage, TaskCreateMessage: req.TaskCreateMessage, ToolNames: req.ToolNames,
	}); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pgDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad trigger id")
		return
	}
	if err := pg.DeleteTrigger(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}
