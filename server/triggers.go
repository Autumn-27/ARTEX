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
	Enabled            bool   `json:"enabled"`
	IntervalSec        int    `json:"interval_sec"`
	OnFinding          bool   `json:"on_finding"`
	OnGoalMet          bool   `json:"on_goal_met"`
	OnTaskTimeout      bool   `json:"on_task_timeout"`
	IntervalMessage    string `json:"interval_message"`
	FindingMessage     string `json:"finding_message"`
	GoalMessage        string `json:"goal_message"`
	TaskTimeoutMessage string `json:"task_timeout_message"`
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
	if req.IntervalSec == 0 && !req.OnFinding && !req.OnGoalMet && !req.OnTaskTimeout {
		writeErr(w, 400, "至少选择一种触发条件(定时/发现finding/目标达成/任务超时)")
		return
	}
	tr, err := pg.CreateTrigger(&db.AgentTrigger{
		AgentKey: a.Key, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		OnFinding: req.OnFinding, OnGoalMet: req.OnGoalMet, OnTaskTimeout: req.OnTaskTimeout,
		IntervalMessage: req.IntervalMessage, FindingMessage: req.FindingMessage,
		GoalMessage: req.GoalMessage, TaskTimeoutMessage: req.TaskTimeoutMessage,
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
	if req.IntervalSec == 0 && !req.OnFinding && !req.OnGoalMet && !req.OnTaskTimeout {
		writeErr(w, 400, "至少选择一种触发条件(定时/发现finding/目标达成/任务超时)")
		return
	}
	if err := pg.UpdateTrigger(&db.AgentTrigger{
		ID: id, Enabled: req.Enabled, IntervalSec: req.IntervalSec,
		OnFinding: req.OnFinding, OnGoalMet: req.OnGoalMet, OnTaskTimeout: req.OnTaskTimeout,
		IntervalMessage: req.IntervalMessage, FindingMessage: req.FindingMessage,
		GoalMessage: req.GoalMessage, TaskTimeoutMessage: req.TaskTimeoutMessage,
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
