package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// 总览「目标管理」的人工 CRUD 接口。与 agent 侧的 set_goals 工具写同一批 goal 节点,
// 但入口是人类在 UI 上直接增删改;新增/修改后复用「复活任务」逻辑(admitTask resume:
// 终态→running、解除暂停、必要时排队),删除不复活(按产品决策)。每个变更 handler 都走
// beginTaskOperation/decInflight,避免与任务删除竞态(与意图 CRUD 一致)。

// listGoals 返回本任务的全部目标(text/vulnclass/state 拆好),供目标管理卡片渲染。
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err := t.Store.ListByKind(db.KindGoal, 10000)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal 人工新增一个目标:落库(挂到任务根 spawns 下)→ 记一条「新增了目标」触发唤醒
// planner → 复活任务,让规划者据新目标重判是否达成。
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法新增目标")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "目标内容不能为空")
		return
	}
	payload := map[string]any{"text": text}
	if vc := strings.TrimSpace(body.VulnClass); vc != "" {
		payload["vulnclass"] = vc
	}
	id, err := t.Store.AddGoal(payload, "human")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _ := t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // 记「人新增了目标:…」触发并唤醒 planner
	s.reviveTask(t)              // 把已完成/暂停的任务拉回运行态继续跑
	node, _ := t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "目标写入后读取失败")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal 人工修改一个目标文本(及 vulnclass):改库 → 记「用户修改了目标由 old 变为 new」
// 触发唤醒 planner → 复活任务,让规划者据新目标调整方向。
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法修改目标")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text      string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "目标内容不能为空")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "目标不存在")
		return
	}
	oldText := goalDTO(node).Text
	if err := t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // 记「人修改了目标由 old 变为 new」触发并唤醒 planner
	s.reviveTask(t)                   // 与新增一致:复活任务据新目标重判
	updated, _ := t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "目标更新后读取失败")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal 人工删除一个目标(硬删除,级联删边/锚点):删库 → 记「用户删除了该目标 X」
// 触发唤醒 planner 据此重判剩余目标。按产品决策,删除【不】复活任务。
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	if !s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "任务正在删除,无法删除目标")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err := strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err != nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err := t.Store.GetNode(gid)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind != db.KindGoal {
		writeErr(w, 404, "目标不存在")
		return
	}
	text := goalDTO(node).Text
	if err := t.Store.DeleteGoal(gid); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // 记「人删除了该目标:…」触发并唤醒 planner(不复活任务)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
