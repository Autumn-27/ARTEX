package server

import (
	"fmt"
	"net/http"

	"github.com/Autumn-27/artex/db"
)

// 任务级资产拦截/允许规则的 CRUD。规则按 task_id 归属，仅对该任务生效：
// action=block 拦截(禁止测试)，action=allow 允许(白名单)。执行判定见 db.EvaluateAssetGate。

type taskInterceptRuleReq struct {
	Enabled bool   `json:"enabled"`
	Action  string `json:"action"` // block | allow
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Note    string `json:"note"`
}

// validateTaskInterceptRuleReq 归一并校验；复用全局规则的 kind/pattern 校验器。
func validateTaskInterceptRuleReq(req *taskInterceptRuleReq) error {
	if req.Action == "" {
		req.Action = "block"
	}
	if req.Action != "block" && req.Action != "allow" {
		return fmt.Errorf("action 必须是 block 或 allow")
	}
	v := assetInterceptRuleReq{Enabled: req.Enabled, Kind: req.Kind, Pattern: req.Pattern, Note: req.Note}
	if err := validateAssetInterceptRuleReq(&v); err != nil {
		return err
	}
	req.Pattern = v.Pattern // 已 trim
	return nil
}

// buildTaskInterceptRules 校验创建任务时录入的任务级规则并转换为 db 输入形态。
func buildTaskInterceptRules(reqs []taskInterceptRuleReq) ([]db.TaskInterceptRuleInput, error) {
	if len(reqs) == 0 {
		return nil, nil
	}
	out := make([]db.TaskInterceptRuleInput, 0, len(reqs))
	for i := range reqs {
		rq := reqs[i]
		if err := validateTaskInterceptRuleReq(&rq); err != nil {
			return nil, err
		}
		out = append(out, db.TaskInterceptRuleInput{
			Enabled: rq.Enabled,
			Action:  rq.Action,
			Kind:    rq.Kind,
			Pattern: rq.Pattern,
			Note:    rq.Note,
		})
	}
	return out, nil
}

func (s *Server) taskInterceptListRules(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	rules, err := pg.Assets().ListTaskInterceptRules(taskID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rules == nil {
		rules = []db.AssetInterceptRule{}
	}
	writeJSON(w, 200, map[string]any{"rules": rules})
}

func (s *Server) taskInterceptCreateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().CreateTaskInterceptRule(taskID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptUpdateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req taskInterceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateTaskInterceptRuleReq(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.Assets().UpdateTaskInterceptRule(taskID, ruleID, req.Action, req.Kind, req.Pattern, req.Note, req.Enabled)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rule)
}

func (s *Server) taskInterceptDeleteRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	deleted, err := pg.Assets().DeleteTaskInterceptRule(taskID, ruleID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": deleted})
}

func (s *Server) taskInterceptToggleRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID, ok := pathInt(r, "id")
	if !ok || taskID <= 0 {
		writeErr(w, 400, "bad task id")
		return
	}
	ruleID, ok := pathInt(r, "rid")
	if !ok || ruleID <= 0 {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.Assets().ToggleTaskInterceptRule(taskID, ruleID, req.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": req.Enabled})
}
