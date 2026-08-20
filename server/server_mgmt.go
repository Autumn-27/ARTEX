package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/skill"
)

// reSkillName is the base character-set pattern; validSkillName enforces
// the full agentskills.io spec rules on top of it.
var reSkillName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// validSkillName checks all agentskills.io name constraints:
// lowercase alphanumeric + hyphens, 1-64 chars, no leading/trailing/consecutive hyphens.
func validSkillName(name string) bool {
	if l := len(name); l == 0 || l > 64 {
		return false
	}
	if !reSkillName.MatchString(name) {
		return false
	}
	if name[len(name)-1] == '-' {
		return false
	}
	return !strings.Contains(name, "--")
}

// reAgentKey mirrors the agents.key DB check: lowercase letter start, then
// lowercase letters / digits / underscores.
var reAgentKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// pgReady returns the PG handle, or writes 503 and returns nil if unavailable.
func (s *Server) pg(w http.ResponseWriter) *db.DB {
	if s.m.pg == nil {
		writeErr(w, 503, "管理后台数据源(PostgreSQL)未连接")
		return nil
	}
	return s.m.pg
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// ---------- tasks (delete) ----------

const taskDeleteDrainTimeout = 10 * time.Second

func canonicalTaskID(raw string) (string, bool) {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return "", false
	}
	return strconv.FormatInt(n, 10), true
}

func (s *Server) pgDeleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalTaskID(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 无效")
		return
	}
	var opts DeleteTaskOptions
	if err := decode(r, &opts); err != nil && err != io.EOF {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if !s.engine.BeginDelete(id) {
		writeErr(w, http.StatusConflict, "任务正在删除")
		return
	}
	deleted := false
	defer func() {
		if !deleted {
			s.engine.AbortDelete(id)
		}
	}()

	// Main-agent runs use a separate context from planner/workers. Cancel it, then
	// wait until both execution domains have returned before removing transcripts.
	s.cancelTaskChat(id, agent.AbortTaskDeleted)
	drainCtx, cancelDrain := context.WithTimeout(r.Context(), taskDeleteDrainTimeout)
	defer cancelDrain()
	if err := s.waitTaskQuiescent(drainCtx, id); err != nil {
		writeErr(w, http.StatusConflict, "任务仍有运行中的 Agent，删除已取消")
		return
	}

	result, err := s.m.DeleteTask(id, opts)
	if err != nil {
		var committed *taskDeleteCommittedError
		if errors.As(err, &committed) {
			// PostgreSQL is already gone. Complete runtime teardown and return the
			// auditable counts together with the post-commit cleanup warning.
			s.engine.StopTask(id)
			s.taskAgentMu.Lock()
			delete(s.taskAgents, id)
			s.taskAgentMu.Unlock()
			deleted = true
			writeCommittedTaskDelete(w, result, err)
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	// Manager has removed the task from the registry, so no new API operation can
	// resolve it. Now stop and join every task-owned Engine goroutine and clear its
	// lifecycle maps before releasing the delete barrier.
	s.engine.StopTask(id)
	s.taskAgentMu.Lock()
	delete(s.taskAgents, id)
	s.taskAgentMu.Unlock()
	deleted = true
	writeJSON(w, 200, result)
}

func writeCommittedTaskDelete(w http.ResponseWriter, result DeleteTaskResult, err error) {
	result.CleanupWarning = err.Error()
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) waitTaskQuiescent(ctx context.Context, taskID string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.chatMu.Lock()
		chatBusy := s.chatBusy[taskID]
		s.chatMu.Unlock()
		if s.engine.inflightCount(taskID) == 0 && !chatBusy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ---------- agents ----------

func (s *Server) pgListAgents(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ags, err := pg.ListAgents()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	dtos := agentDTOs(ags)
	// overlay per-agent binding counts (mcp/skill by id, tools by key) — best-effort.
	if mcp, skill, tools, err := pg.AgentBindingCounts(); err == nil {
		for i := range dtos {
			dtos[i].McpCount = mcp[ags[i].ID]
			dtos[i].SkillCount = skill[ags[i].ID]
			dtos[i].ToolCount = tools[ags[i].Key]
		}
	}
	writeJSON(w, 200, map[string]any{"agents": dtos})
}

// pgCreateAgent creates a CUSTOM conversational agent (builtin=false, role
// 'assistant') and seeds it a starter prompt so its editor isn't blank.
func (s *Server) pgCreateAgent(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req struct{ Key, Name, Description string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Key, req.Name = strings.TrimSpace(req.Key), strings.TrimSpace(req.Name)
	if !reAgentKey.MatchString(req.Key) {
		writeErr(w, 400, "key 需小写字母开头，仅含小写字母/数字/下划线")
		return
	}
	if req.Name == "" {
		writeErr(w, 400, "名称不能为空")
		return
	}
	if exist, _ := pg.GetAgentByKey(req.Key); exist != nil {
		writeErr(w, 409, "该 key 已存在")
		return
	}
	a, err := pg.CreateAgent(req.Key, req.Name, req.Description)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// starter prompt so the editor shows something editable from the start.
	if err := pg.SeedPromptIfEmpty(a.ID, agent.DefaultAssistantPrompt); err != nil {
		log.Printf("[agents] seed starter prompt for %s 失败: %v", a.Key, err)
	}
	writeJSON(w, 200, agentDTO(a))
}

// pgUpdateAgent updates a custom agent's name/description (built-in agents rejected).
func (s *Server) pgUpdateAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if a.Builtin {
		writeErr(w, 400, "内置 agent 不可修改名称/描述")
		return
	}
	var req struct{ Name, Description string }
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, 400, "名称不能为空")
		return
	}
	if err := pg.UpdateAgentMeta(a.Key, req.Name, req.Description); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgDeleteAgent removes a custom agent (built-in rejected). Prompts/vars/visibility
// cascade via FK; tool-binding cleanup is best-effort.
func (s *Server) pgDeleteAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if a.Builtin {
		writeErr(w, 400, "内置 agent 不可删除")
		return
	}
	if err := pg.DeleteAgent(a.Key); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := pg.RemoveAgentFromToolBindings(a.Key); err != nil {
		log.Printf("[agents] 清理 %s 工具绑定失败: %v", a.Key, err)
	}
	if err := pg.DeleteTriggersForAgent(a.Key); err != nil {
		log.Printf("[agents] 清理 %s 触发器失败: %v", a.Key, err)
	}
	writeJSON(w, 200, map[string]any{"deleted": a.Key})
}

func (s *Server) agentByKey(w http.ResponseWriter, r *http.Request) (*db.DB, *db.Agent, bool) {
	pg := s.pg(w)
	if pg == nil {
		return nil, nil, false
	}
	a, err := pg.GetAgentByKey(r.PathValue("key"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return nil, nil, false
	}
	if a == nil {
		writeErr(w, 404, "agent not found")
		return nil, nil, false
	}
	return pg, a, true
}

// pgSaveAgentConfig updates an agent's runtime config (currently max_turns) and
// re-applies the live LLM so the change takes effect immediately.
func (s *Server) pgSaveAgentConfig(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	// All fields optional (pointers) so a partial patch (e.g. the triggers tab sending
	// only trigger_* fields) leaves the untouched settings alone instead of resetting
	// max_turns/run_seconds to 0.
	var req struct {
		MaxTurns         *int  `json:"max_turns"`
		RunSeconds       *int  `json:"run_seconds"`
		WebSearch        *bool `json:"web_search"`
		InteractiveShell *bool `json:"interactive_shell"`
		// llm_profile_id 三态:字段缺省=不动;显式 null=解绑(跟随任务/全局);数字=绑定该 profile。
		LLMProfileID json.RawMessage `json:"llm_profile_id"`
		// P3 触发后处理策略(三者一起可选,提供任一即整体写入;未提供则不动)。
		TriggerRunMode     *string `json:"trigger_run_mode"`
		TriggerMergeMode   *string `json:"trigger_merge_mode"`
		TriggerMaxParallel *int    `json:"trigger_max_parallel"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	profileChanged := false
	if req.LLMProfileID != nil { // key present (数字 或 null)
		var id *int64
		if err := json.Unmarshal(req.LLMProfileID, &id); err != nil {
			writeErr(w, 400, "llm_profile_id 格式错误")
			return
		}
		if id != nil { // 绑定:校验目标 profile 有效
			if _, ok := s.loadProfileConfig(*id); !ok {
				writeErr(w, 400, "指定的 LLM 配置不存在或无效")
				return
			}
		}
		if err := pg.SetAgentLLMProfile(a.Key, id); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		profileChanged = true
	}
	if req.MaxTurns != nil {
		mt := *req.MaxTurns
		if mt < 0 {
			mt = 0
		}
		if err := pg.SetAgentMaxTurns(a.Key, mt); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.RunSeconds != nil {
		rs := *req.RunSeconds
		if rs < 0 {
			rs = 0
		}
		if err := pg.SetAgentRunSeconds(a.Key, rs); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.WebSearch != nil {
		if err := pg.SetAgentWebSearch(a.Key, *req.WebSearch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if req.InteractiveShell != nil {
		if err := pg.SetAgentInteractiveShell(a.Key, *req.InteractiveShell); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// P3 触发策略:三者作为一组写入(SetAgentTriggerBehavior 一次写三列),缺的字段用
	// 当前存量值回填,避免只传一个把另两个覆盖成默认。
	if req.TriggerRunMode != nil || req.TriggerMergeMode != nil || req.TriggerMaxParallel != nil {
		runMode, mergeMode, maxPar := a.TriggerRunMode, a.TriggerMergeMode, a.TriggerMaxParallel
		if req.TriggerRunMode != nil {
			runMode = *req.TriggerRunMode
		}
		if req.TriggerMergeMode != nil {
			mergeMode = *req.TriggerMergeMode
		}
		if req.TriggerMaxParallel != nil {
			maxPar = *req.TriggerMaxParallel
		}
		if err := pg.SetAgentTriggerBehavior(a.Key, runMode, mergeMode, maxPar); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	// an agent's LLM binding change also invalidates pinned-task / per-profile caches
	// so their next round re-resolves each agent's bound model.
	if profileChanged {
		s.invalidateProfileAgents()
	}
	// Task bundles capture operational Agent settings (turn/time budgets, tools,
	// web search). Rebuild them even when no global profile is configured.
	s.invalidateTaskAgents()
	// rebuild the live agents so the new max_turns/run_seconds/binding apply without a restart.
	s.cfgMu.Lock()
	cfg, on := s.llmCfg, s.llmOn
	s.cfgMu.Unlock()
	if on {
		_ = s.applyLLM(cfg)
	}
	resp := map[string]any{"ok": true}
	if req.MaxTurns != nil {
		resp["max_turns"] = *req.MaxTurns
	}
	if req.RunSeconds != nil {
		resp["run_seconds"] = *req.RunSeconds
	}
	writeJSON(w, 200, resp)
}

func (s *Server) pgGetAgent(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	cur, _ := pg.CurrentPrompt(a.ID)
	vars, _ := pg.PromptVars(a.ID)
	vars = withGlobalVars(vars)
	vers, _ := pg.ListPromptVersions(a.ID)
	if vers == nil {
		vers = []db.PromptVersion{}
	}
	mcp, _ := pg.AgentVisible(a.ID, "mcp")
	sk, _ := pg.AgentSkillNames(a.ID)
	if sk == nil {
		sk = []string{}
	}
	// 可选 LLM 配置列表(id/name/model/是否默认),供前端渲染 "默认模型" 下拉;当前绑定见 agent.llm_profile_id。
	profs, _ := pg.ListProfiles()
	llmProfiles := make([]map[string]any, 0, len(profs))
	for _, p := range profs {
		llmProfiles = append(llmProfiles, map[string]any{
			"id": p.ID, "name": p.Name, "model": p.Model, "is_default": p.IsDefault,
		})
	}
	writeJSON(w, 200, map[string]any{
		"agent": agentDTO(a), "prompt": cur, "variables": vars, "versions": vers,
		"visibility":   map[string]any{"mcp": mcp, "skill": sk},
		"llm_profiles": llmProfiles, // 可绑定的 LLM 配置候选

		"wrapup_prompt":            a.WrapupPrompt,                  // 已保存的收尾提示词(空=用内置默认)
		"wrapup_default":           agent.WrapupDefault(a.Key),      // 内置默认(供占位/恢复默认)
		"wrapup_max_turns":         a.WrapupMaxTurns,                // 已保存的收尾轮数(0=用内置默认)
		"wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key), // 内置默认轮数(供 "0=默认N" 提示)
		// 任务级超时收尾词(仅 worker/planner 有内置默认;task_timeout_supported 供前端决定是否显示该分区)
		"task_timeout_wrapup_supported":         agent.TaskTimeoutWrapupDefault(a.Key) != "",
		"task_timeout_wrapup_prompt":            a.TaskTimeoutWrapupPrompt,
		"task_timeout_wrapup_default":           agent.TaskTimeoutWrapupDefault(a.Key),
		"task_timeout_wrapup_max_turns":         a.TaskTimeoutWrapupMaxTurns,
		"task_timeout_wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key),
	})
}

func (s *Server) pgSavePrompt(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	var body struct{ Template, Note string }
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	vars, _ := pg.PromptVars(a.ID)
	if bad := validateTemplate(body.Template, withGlobalVars(vars)); bad != "" {
		writeErr(w, 400, bad)
		return
	}
	ver, err := pg.SavePrompt(a.ID, body.Template, body.Note, "ui")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"version": ver})
}

// pgResetPrompt restores an agent's prompt body to the in-code built-in default
// (段 [A]). Only built-in agents have a code default; custom agents have none.
func (s *Server) pgResetPrompt(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	tmpl, has := agent.BuiltinPromptSeeds()[a.Key]
	if !has {
		writeErr(w, 400, "该 agent 无内置默认提示词，无法恢复")
		return
	}
	ver, err := pg.ResetPromptToDefault(a.ID, tmpl)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"version": ver})
}

// pgSaveWrapup stores an agent's wrap-up (settlement) prompt — the text injected on
// timeout / step-exhaustion. Empty body clears the override → built-in default.
func (s *Server) pgSaveWrapup(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	// MaxTurns is optional (pointer): omit to leave the stored turn budget untouched.
	var body struct {
		Prompt   string `json:"prompt"`
		MaxTurns *int   `json:"max_turns"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.SetAgentWrapupPrompt(a.Key, body.Prompt); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if body.MaxTurns != nil {
		n := *body.MaxTurns
		if n < 0 {
			n = 0
		}
		if err := pg.SetAgentWrapupMaxTurns(a.Key, n); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgResetWrapup clears an agent's wrap-up prompt override so the code built-in
// default is used again.
func (s *Server) pgResetWrapup(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if err := pg.SetAgentWrapupPrompt(a.Key, ""); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := pg.SetAgentWrapupMaxTurns(a.Key, 0); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":                       true,
		"wrapup_default":           agent.WrapupDefault(a.Key),
		"wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key),
	})
}

// pgSaveTaskTimeoutWrapup stores an agent's TASK-TIMEOUT wrap-up prompt + turn budget
// (worker/planner only). Empty prompt / 0 turns clear the override → built-in default.
func (s *Server) pgSaveTaskTimeoutWrapup(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	var body struct {
		Prompt   string `json:"prompt"`
		MaxTurns *int   `json:"max_turns"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	turns := a.TaskTimeoutWrapupMaxTurns // 未传则保留原值
	if body.MaxTurns != nil {
		turns = *body.MaxTurns
		if turns < 0 {
			turns = 0
		}
	}
	if err := pg.SetAgentTaskTimeoutWrapup(a.Key, body.Prompt, turns); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgResetTaskTimeoutWrapup clears the task-timeout wrap-up override → built-in default.
func (s *Server) pgResetTaskTimeoutWrapup(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	if err := pg.SetAgentTaskTimeoutWrapup(a.Key, "", 0); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":                                    true,
		"task_timeout_wrapup_default":           agent.TaskTimeoutWrapupDefault(a.Key),
		"task_timeout_wrapup_max_turns_default": agent.WrapupTurnsDefault(a.Key),
	})
}

func (s *Server) pgListPromptVersions(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	vers, err := pg.ListPromptVersions(a.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"versions": vers})
}

func (s *Server) pgPromptVars(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	vars, err := pg.PromptVars(a.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"variables": withGlobalVars(vars)})
}

func (s *Server) pgPreviewPrompt(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	var body struct {
		Template string            `json:"template"`
		Sample   map[string]string `json:"sample"`
	}
	_ = decode(r, &body)
	vars, _ := pg.PromptVars(a.ID)
	if body.Template == "" {
		body.Template, _ = pg.CurrentPrompt(a.ID)
	}
	rendered, err := renderPrompt(body.Template, withGlobalVars(vars), body.Sample)
	if err != nil {
		writeJSON(w, 200, map[string]any{"rendered": "", "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"rendered": rendered})
}

func (s *Server) pgGetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	mcp, _ := pg.AgentVisible(a.ID, "mcp")
	sk, _ := pg.AgentSkillNames(a.ID)
	if sk == nil {
		sk = []string{}
	}
	writeJSON(w, 200, map[string]any{"mcp": mcp, "skill": sk})
}

func (s *Server) pgSetAgentVisibility(w http.ResponseWriter, r *http.Request) {
	pg, a, ok := s.agentByKey(w, r)
	if !ok {
		return
	}
	var body struct {
		MCP   []int64  `json:"mcp"`
		Skill []string `json:"skill"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.SetAgentVisibilityKind(a.ID, "mcp", body.MCP); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := pg.SetAgentSkillVisibility(a.ID, body.Skill); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- tools (内置工具目录) ----------

func (s *Server) pgListTools(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ts, err := pg.ListTools()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ts == nil {
		ts = []*db.Tool{}
	}
	writeJSON(w, 200, map[string]any{"tools": ts})
}

// pgUpdateTool saves the page-editable fields of a built-in tool: description,
// parameter schema (structure is expected unchanged — only per-param description/
// default move), agent binding, and enabled. key is taken from the path and never
// changes (it is welded to the Go handler).
func (s *Server) pgUpdateTool(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := r.PathValue("key")
	cur, err := pg.GetTool(key)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cur == nil {
		writeErr(w, 404, "工具不存在: "+key)
		return
	}
	var body struct {
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"schema"`
		Agents      []string        `json:"agents"`
		Enabled     bool            `json:"enabled"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	agents, _ := json.Marshal(body.Agents)
	if err := pg.UpdateTool(key, body.Description, body.Schema, agents, body.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgResetTool overwrites a tool row with its code-defined defaults (description,
// schema, agent binding) and re-enables it — the explicit "恢复默认" action, since
// startup seeding is first-insert-only and never overwrites edits.
func (s *Server) pgResetTool(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := r.PathValue("key")
	for _, sd := range agent.BuiltinToolSeeds() {
		if sd.Key != key {
			continue
		}
		schema, _ := json.Marshal(sd.Schema)
		agents, _ := json.Marshal(sd.Agents)
		if err := pg.UpsertToolForce(sd.Key, sd.Desc, schema, agents); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	// orchestration/platform tools (auto agent) — default-bind to "auto".
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range append(s.orchestrationTools(), s.platformTools()...) {
		if t.Name() != key {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema())
		if err := pg.UpsertToolForce(t.Name(), t.Description(), schema, autoAgents); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	writeErr(w, 404, "非内置工具或不存在: "+key)
}

// ---------- mcp ----------

func (s *Server) pgListMCP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ms, err := pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if ms == nil {
		ms = []*db.MCPServer{}
	}
	writeJSON(w, 200, map[string]any{"servers": ms})
}

func (s *Server) pgSaveMCP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var m db.MCPServer
	if err := decode(r, &m); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	isNew := m.ID == 0
	id, err := pg.SaveMCP(&m)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// On initial add, auto-discover + cache the tool list so the UI shows it right
	// away (bounded so a slow/broken server can't hang the request). Skipped on
	// plain updates (e.g. enable toggles) to avoid re-spawning the server each time.
	if isNew && m.Enabled {
		m.ID = id
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		if derr := s.discoverAndCacheMCP(ctx, &m); derr != nil {
			log.Printf("[mcp] %s 添加后工具发现失败: %v", m.Name, derr)
		}
		cancel()
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) pgDeleteMCP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	if err := pg.DeleteMCP(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": id})
}

// pgRefreshMCP re-discovers one MCP's tools on demand and re-caches them, so a
// config change or an earlier discovery failure can be fixed without a restart.
func (s *Server) pgRefreshMCP(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	all, err := pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var target *db.MCPServer
	for _, m := range all {
		if m.ID == id {
			target = m
			break
		}
	}
	if target == nil {
		writeErr(w, 404, "MCP 不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.discoverAndCacheMCP(ctx, target); err != nil {
		writeErr(w, 502, "工具发现失败："+err.Error())
		return
	}
	tools, _ := pg.MCPToolsDetailed(id)
	writeJSON(w, 200, map[string]any{"tools": tools})
}

// pgMCPTools returns one MCP's cached tools (name + description) for the detail UI.
func (s *Server) pgMCPTools(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	tools, err := pg.MCPToolsDetailed(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if tools == nil {
		tools = []db.MCPTool{}
	}
	writeJSON(w, 200, map[string]any{"tools": tools})
}

// ---------- skills (文件系统) ----------

type skillFileNode struct {
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	License       string   `json:"license,omitempty"`
	Compatibility string   `json:"compatibility,omitempty"`
	MCPs          []string `json:"mcps,omitempty"`
	Files         []string `json:"files"`
	// Usage ledger (db/skill_usage.go). Calls is 0 and LastUsed nil for a skill that
	// was never invoked — the ledger only has rows for skills agents actually loaded.
	Calls    int        `json:"calls"`
	Tasks    int        `json:"tasks"`
	Agents   []string   `json:"usage_agents"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

func (s *Server) fsListSkills(w http.ResponseWriter, r *http.Request) {
	_ = os.MkdirAll(s.skillDir, 0o755)
	allReg, _ := skill.LoadDir(s.skillDir)
	metaByDir := map[string]skill.Skill{}
	if allReg != nil {
		for _, sk := range allReg.List() {
			if sk.Dir != "" {
				metaByDir[filepath.Base(sk.Dir)] = sk
			}
		}
	}
	entries, err := os.ReadDir(s.skillDir)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Usage is best-effort decoration: a ledger read failure leaves the counts at
	// zero rather than failing the skill list itself.
	statBySkill := map[string]db.SkillStat{}
	if s.m.pg != nil {
		stats, err := s.m.pg.SkillStats()
		if err != nil {
			log.Printf("[skills] 读取调用统计失败: %v", err)
		}
		for _, st := range stats {
			statBySkill[st.Skill] = st
		}
	}
	nodes := []skillFileNode{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirName := e.Name()
		node := skillFileNode{Name: dirName, Agents: []string{}}
		if st, ok := statBySkill[dirName]; ok {
			node.Calls, node.Tasks, node.Agents, node.LastUsed = st.Calls, st.Tasks, st.Agents, st.LastUsed
		}
		if meta, ok := metaByDir[dirName]; ok {
			node.Description = meta.Description
			node.License = meta.License
			node.Compatibility = meta.Compatibility
			node.MCPs = meta.MCPs
		}
		node.Files, _ = walkSkillFiles(filepath.Join(s.skillDir, dirName))
		if node.Files == nil {
			node.Files = []string{}
		}
		nodes = append(nodes, node)
	}
	writeJSON(w, 200, map[string]any{"skills": nodes})
}

// fsSkillUsage returns one skill's recent invocations, newest first (detail panel).
func (s *Server) fsSkillUsage(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "非法 skill 名")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	calls, err := pg.RecentSkillCalls(name, limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"calls": calls})
}

// fsMissingSkills lists skill names agents asked for that do not exist — the gap
// list, i.e. which procedures are worth writing next.
func (s *Server) fsMissingSkills(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	missing, err := pg.MissingSkillStats(limit)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"missing": missing})
}

// cleanStrs trims each string and drops empties.
func cleanStrs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) fsCreateSkill(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`   // required per agentskills.io spec
		License       string   `json:"license"`       // optional
		Compatibility string   `json:"compatibility"` // optional
		MCPs          []string `json:"mcps"`          // optional; MCP servers this skill unlocks
		Instructions  string   `json:"instructions"`  // optional; scaffolded if empty
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !validSkillName(body.Name) {
		writeErr(w, 400, "skill name must be 1-64 lowercase alphanumeric/hyphen characters, not starting/ending/doubling hyphens")
		return
	}
	if strings.TrimSpace(body.Description) == "" {
		writeErr(w, 400, "description is required")
		return
	}
	skillPath := filepath.Join(s.skillDir, body.Name)
	if _, err := os.Stat(skillPath); err == nil {
		writeErr(w, 409, "skill already exists")
		return
	}
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Build a spec-compliant SKILL.md (agentskills.io format):
	//   YAML frontmatter: name (required), description (required),
	//                     license, compatibility (optional)
	//   Markdown body:    step-by-step instructions
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", body.Name)
	fmt.Fprintf(&sb, "description: %s\n", body.Description)
	if body.License != "" {
		fmt.Fprintf(&sb, "license: %s\n", body.License)
	}
	if body.Compatibility != "" {
		fmt.Fprintf(&sb, "compatibility: %s\n", body.Compatibility)
	}
	// mcps: MCP servers this skill unlocks on load (skill-gated deferred tools).
	if mcps := cleanStrs(body.MCPs); len(mcps) > 0 {
		fmt.Fprintf(&sb, "mcps: %s\n", strings.Join(mcps, ", "))
	}
	sb.WriteString("---\n")
	if strings.TrimSpace(body.Instructions) != "" {
		sb.WriteString(body.Instructions)
	} else {
		// scaffold a minimal Markdown body so the file is immediately useful
		fmt.Fprintf(&sb, "## %s\n\n", body.Name)
		sb.WriteString("<!-- Describe step-by-step instructions in Markdown. -->\n\n")
		sb.WriteString("1. \n2. \n3. \n")
	}
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(sb.String()), 0o644); err != nil {
		_ = os.RemoveAll(skillPath)
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"name": body.Name})
}

// fsUpdateSkillMeta rewrites the SKILL.md frontmatter fields that are safe to
// change without touching the instruction body: mcps, license, compatibility,
// description. Only fields present in the request body are updated; omitted
// fields are left as-is (the whole frontmatter is reconstructed from the
// parsed values, so the write is a clean replace, not a line-patch).
func (s *Server) fsUpdateSkillMeta(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	var body struct {
		MCPs          *[]string `json:"mcps"`
		Description   *string   `json:"description"`
		License       *string   `json:"license"`
		Compatibility *string   `json:"compatibility"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	skillMD := filepath.Join(s.skillDir, name, "SKILL.md")
	raw, err := os.ReadFile(skillMD)
	if err != nil {
		writeErr(w, 404, "skill not found")
		return
	}
	updated, err := rewriteSkillFrontmatter(raw, body.MCPs, body.Description, body.License, body.Compatibility)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := os.WriteFile(skillMD, updated, 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// rewriteSkillFrontmatter parses the SKILL.md YAML frontmatter and replaces the
// fields given as non-nil pointers. The instruction body (after the closing ---) is
// preserved verbatim. Returns an error if the file has no recognisable frontmatter.
func rewriteSkillFrontmatter(content []byte, mcps *[]string, description, license, compatibility *string) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("SKILL.md has no YAML frontmatter")
	}
	fmEnd := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			fmEnd = i
			break
		}
	}
	if fmEnd < 0 {
		return nil, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	// Collect existing key → value from frontmatter (preserve unknown keys)
	type kv struct{ k, v string }
	var pairs []kv
	for _, l := range lines[1:fmEnd] {
		if idx := strings.IndexByte(l, ':'); idx >= 0 {
			pairs = append(pairs, kv{strings.TrimSpace(l[:idx]), strings.TrimSpace(l[idx+1:])})
		} else if strings.TrimSpace(l) != "" {
			pairs = append(pairs, kv{"", l}) // preserve non-key lines verbatim
		}
	}
	// Apply updates (nil pointer = no change)
	applyStr := func(key string, val *string) {
		if val == nil {
			return
		}
		for i, p := range pairs {
			if p.k == key {
				pairs[i].v = strings.TrimSpace(*val)
				return
			}
		}
		pairs = append(pairs, kv{key, strings.TrimSpace(*val)})
	}
	applyStr("description", description)
	applyStr("license", license)
	applyStr("compatibility", compatibility)
	if mcps != nil {
		cleaned := cleanStrs(*mcps)
		// Remove existing mcps line
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.k != "mcps" {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
		if len(cleaned) > 0 {
			pairs = append(pairs, kv{"mcps", strings.Join(cleaned, ", ")})
		}
	}
	// Reconstruct
	var sb strings.Builder
	sb.WriteString("---\n")
	for _, p := range pairs {
		if p.k == "" {
			sb.WriteString(p.v)
		} else {
			fmt.Fprintf(&sb, "%s: %s", p.k, p.v)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("---\n")
	// Body (lines after closing ---)
	if fmEnd+1 < len(lines) {
		sb.WriteString(strings.Join(lines[fmEnd+1:], "\n"))
	}
	return []byte(sb.String()), nil
}

// zip-upload safety caps (defeat zip bombs / runaway archives).
const (
	maxSkillZipBytes   = 20 << 20  // 20MB compressed request body
	maxSkillTotalBytes = 100 << 20 // 100MB total uncompressed
	maxSkillFileBytes  = 20 << 20  // 20MB per extracted file
	maxSkillEntries    = 4000      // max files in the archive
)

// skillNameFromFrontmatter extracts the `name:` value from a SKILL.md's YAML
// frontmatter (the block between the first two `---` lines). "" if absent.
func skillNameFromFrontmatter(md []byte) string {
	lines := strings.Split(string(md), "\n")
	inFM := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "---" {
			if !inFM {
				inFM = true
				continue
			}
			break // end of frontmatter
		}
		if inFM && strings.HasPrefix(t, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "name:"))
		}
	}
	return ""
}

// fsUploadSkill installs a skill from an uploaded .zip. The archive must contain a
// SKILL.md (at the root or under a single top-level dir); the skill name is taken
// from that file's `name:` frontmatter (falling back to the top dir / zip name).
// Zip-slip is defeated by validating every entry path with skillRelPath, and a set
// of size/count caps guard against zip bombs. POST ?overwrite=true replaces an
// existing skill of the same name.
func (s *Server) fsUploadSkill(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSkillZipBytes)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少上传文件(表单字段 file)或超出大小限制")
		return
	}
	defer file.Close()
	buf, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		writeErr(w, 400, "无法解析压缩包(需为 zip 格式)："+err.Error())
		return
	}

	// locate the shallowest SKILL.md → its directory is the skill root inside the zip.
	skillMD := ""
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != "SKILL.md" {
			continue
		}
		if skillMD == "" || strings.Count(f.Name, "/") < strings.Count(skillMD, "/") {
			skillMD = f.Name
		}
	}
	if skillMD == "" {
		writeErr(w, 400, "压缩包内未找到 SKILL.md")
		return
	}
	root := path.Dir(skillMD) // "." when SKILL.md is at the zip root
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}

	// derive + validate the skill name from the SKILL.md frontmatter.
	md, err := readZipEntry(zr, skillMD)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name := skillNameFromFrontmatter(md)
	if name == "" && prefix != "" {
		name = path.Base(strings.TrimSuffix(prefix, "/"))
	}
	if name == "" {
		name = strings.TrimSuffix(hdr.Filename, ".zip")
	}
	if !validSkillName(name) {
		writeErr(w, 400, "skill 名称无效（取自 SKILL.md 的 name 字段）："+name)
		return
	}

	skillPath := filepath.Join(s.skillDir, name)
	overwrite := r.URL.Query().Get("overwrite") == "true"
	if _, err := os.Stat(skillPath); err == nil && !overwrite {
		writeErr(w, 409, "skill 已存在："+name+"（如需覆盖请确认后重试）")
		return
	}

	// extract into a temp dir first, then atomically swap in — a bad entry aborts
	// the whole upload without leaving a half-written skill.
	_ = os.MkdirAll(s.skillDir, 0o755)
	tmp, err := os.MkdirTemp(s.skillDir, ".upload-*")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(tmp) // no-op after a successful rename

	var total int64
	entries := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// only files under the skill root; skip archive junk.
		if prefix != "" && !strings.HasPrefix(f.Name, prefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		if rel == "" || strings.HasPrefix(rel, "__MACOSX/") || path.Base(rel) == ".DS_Store" {
			continue
		}
		clean, msg := skillRelPath(rel)
		if msg != "" {
			writeErr(w, 400, "压缩包含非法路径 "+f.Name+"："+msg)
			return
		}
		if entries++; entries > maxSkillEntries {
			writeErr(w, 400, "压缩包文件过多")
			return
		}
		if f.UncompressedSize64 > maxSkillFileBytes {
			writeErr(w, 400, "文件过大："+rel)
			return
		}
		dst := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		rc, err := f.Open()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		out, err := os.Create(dst)
		if err != nil {
			rc.Close()
			writeErr(w, 500, err.Error())
			return
		}
		n, err := io.Copy(out, io.LimitReader(rc, maxSkillFileBytes+1))
		out.Close()
		rc.Close()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		total += n
		if total > maxSkillTotalBytes {
			writeErr(w, 400, "压缩包解压后过大")
			return
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "SKILL.md")); err != nil {
		writeErr(w, 400, "解压后缺少 SKILL.md")
		return
	}

	if overwrite {
		_ = os.RemoveAll(skillPath)
	}
	if err := os.Rename(tmp, skillPath); err != nil {
		writeErr(w, 500, "安装失败："+err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"name": name, "files": entries})
}

// readZipEntry reads a single named entry's bytes (capped) from an open zip.
func readZipEntry(zr *zip.Reader, name string) ([]byte, error) {
	f, err := zr.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSkillFileBytes))
}

func (s *Server) fsDeleteSkill(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	if err := pg.DeleteSkillVisibility(name); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.RemoveAll(filepath.Join(s.skillDir, name)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": name})
}

// reFilePath is a strict ASCII whitelist for relative paths inside a skill directory.
// Allowed: A-Z a-z 0-9 hyphen underscore dot forward-slash.
// This blocks before any normalization: null bytes, backslash, %, unicode look-alikes,
// control characters, and any other byte not in the set.
// Go's net/http URL-decodes PathValue once (e.g. %2F→/ %2e→.), so an attacker who
// sends %252e%252e gets the literal string "%2e%2e" here — the '%' fails the whitelist.
var reFilePath = regexp.MustCompile(`^[A-Za-z0-9\-_./]+$`)

// skillRelPath validates a relative file path supplied by the client.
// Returns the cleaned path and an empty error string on success.
// Validation order matters: whitelist first (before Clean) so encoding tricks can't
// survive normalization.
func skillRelPath(file string) (string, string) {
	// 1. Whitelist — reject anything outside [A-Za-z0-9-_./] before normalization.
	//    This alone defeats: null bytes, backslash, %, unicode, control chars.
	if !reFilePath.MatchString(file) {
		return "", "invalid path: only [A-Za-z0-9-_./] allowed"
	}
	// 2. Reject ".." explicitly. With the whitelist above, encoding bypass is already
	//    impossible, but we keep this check so the intent is obvious to reviewers.
	if strings.Contains(file, "..") {
		return "", "invalid path: '..' not allowed"
	}
	// 3. Reject leading slash and empty segments (double slash).
	//    Leading slash would survive filepath.Clean as an absolute path.
	if strings.HasPrefix(file, "/") || strings.Contains(file, "//") {
		return "", "invalid path: must be relative with no empty segments"
	}
	// 4. Normalize and final safety re-check after Clean.
	//    filepath.Clean removes redundant separators and resolves single dots.
	clean := filepath.Clean(file)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", "invalid path"
	}
	return clean, ""
}

// walkSkillFiles returns all files under root (relative to root), sorted,
// including files in subdirectories (scripts/, references/, assets/, etc.).
// walkSkillFiles returns all entries under root relative to root.
// Directories are included with a trailing "/" so the frontend can distinguish
// them from files and render empty folders in the tree.
func walkSkillFiles(root string) ([]string, error) {
	var entries []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			entries = append(entries, rel+"/")
		} else {
			entries = append(entries, rel)
		}
		return nil
	})
	return entries, err
}

func (s *Server) fsListFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	dirPath := filepath.Join(s.skillDir, name)
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		writeErr(w, 404, "skill not found")
		return
	}
	files, err := walkSkillFiles(dirPath)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (s *Server) fsReadFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	file, errMsg := skillRelPath(r.PathValue("file"))
	if errMsg != "" {
		writeErr(w, 400, errMsg)
		return
	}
	data, err := os.ReadFile(filepath.Join(s.skillDir, name, file))
	if os.IsNotExist(err) {
		writeErr(w, 404, "file not found")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"content": string(data), "file": file})
}

func (s *Server) fsWriteFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	file, errMsg := skillRelPath(r.PathValue("file"))
	if errMsg != "" {
		writeErr(w, 400, errMsg)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	skillPath := filepath.Join(s.skillDir, name)
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		writeErr(w, 404, "skill not found")
		return
	}
	fullPath := filepath.Join(skillPath, file)
	// create parent subdirectory if needed (e.g. scripts/, references/, assets/)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(fullPath, []byte(body.Content), 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) fsCreateDir(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	dir, errMsg := skillRelPath(body.Path)
	if errMsg != "" {
		writeErr(w, 400, errMsg)
		return
	}
	skillPath := filepath.Join(s.skillDir, name)
	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		writeErr(w, 404, "skill not found")
		return
	}
	if err := os.MkdirAll(filepath.Join(skillPath, dir), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"dir": dir})
}

func (s *Server) fsDeletePath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validSkillName(name) {
		writeErr(w, 400, "invalid skill name")
		return
	}
	file, errMsg := skillRelPath(r.PathValue("file"))
	if errMsg != "" {
		writeErr(w, 400, errMsg)
		return
	}
	fullPath := filepath.Join(s.skillDir, name, file)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		writeErr(w, 404, "not found")
		return
	}
	if err := os.RemoveAll(fullPath); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": file})
}

// ---------- skill visibility ----------

func (s *Server) pgSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	agents, err := pg.SkillAgents(r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"agents": idStrings(agents)})
}

func (s *Server) pgToggleSkillVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		AgentID   int64  `json:"agent_id,string"`
		SkillName string `json:"skill_name"`
		Visible   bool   `json:"visible"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.ToggleSkillVisibility(body.AgentID, body.SkillName, body.Visible); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- visibility (MCP resource side + toggle) ----------

func (s *Server) pgResourceVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	agents, err := pg.ResourceAgents(r.PathValue("kind"), id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"agents": idStrings(agents)})
}

func (s *Server) pgToggleVisibility(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		AgentID    int64  `json:"agent_id,string"`
		Kind       string `json:"kind"`
		ResourceID int64  `json:"resource_id"`
		Visible    bool   `json:"visible"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.ToggleVisibility(body.AgentID, body.Kind, body.ResourceID, body.Visible); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------- llm profiles ----------

func (s *Server) pgListProfiles(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	ps, err := pg.ListProfiles()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"profiles": llmProfileDTOs(ps)})
}

func (s *Server) pgSaveProfile(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	// APIKey has json:"-" on db.LLMProfile so it is never leaked to the UI on
	// read; accept it here via a sibling field for create/update.
	var body struct {
		db.LLMProfile
		APIKey string `json:"api_key"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p := body.LLMProfile
	p.APIKey = body.APIKey
	id, err := pg.SaveProfile(&p)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// Editing a profile rebuilds any task pinned to it on its next round. Reapply
	// the active profile too: the global fallback and explicit task chains must
	// repopulate the same provider-cache entry and therefore share one limiter.
	s.invalidateProfileAgents()
	// Hot-apply. Not just when the ACTIVE profile changed: with failover on, any
	// profile's key/model/priority/pool_exclude edit reshapes the chain, so the
	// engine's provider has to be rebuilt either way.
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) pgDeleteProfile(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, _ := pathInt(r, "id")
	if err := pg.DeleteProfileContext(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, db.ErrActiveLLMProfileDelete):
			writeErr(w, 409, "当前激活的 LLM 配置不能删除，请先激活其他配置")
		case errors.Is(err, db.ErrLLMProfileReferencesChanged):
			writeErr(w, 409, "LLM 配置正在被任务或会话修改，请重试")
		case errors.Is(err, context.DeadlineExceeded):
			writeErr(w, 409, "等待 LLM 配置引用释放超时，请重试")
		case errors.Is(err, db.ErrLLMProfileNotFound):
			writeErr(w, 404, err.Error())
		default:
			writeErr(w, 500, err.Error())
		}
		return
	}
	s.invalidateProfileAgents() // drop cached agents for the removed profile
	s.llmHealth.Reset(id)       // its breaker state is meaningless now (row is FK-cascaded away)
	s.restoreTasksAfterProfileDelete(pg)
	// Cache invalidation also removed the active profile's shared provider entry.
	// Reapply after task-state sync so the wake-up observes the post-delete chain.
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) restoreTasksAfterProfileDelete(pg *db.DB) {
	for _, task := range s.m.List() {
		if taskID, err := strconv.ParseInt(task.ID, 10, 64); err == nil {
			if pt, err := pg.GetTask(taskID); err == nil && pt != nil {
				s.syncTaskLLMState(pt)
				// Deleting the active entry may advance the task to a ready successor,
				// or clear the explicit chain and restore its Agent/global fallback.
				// Resume only intents that were blocked by the exhausted chain; a
				// paused task remains paused and terminal tasks remain immutable.
				if !isTerminalStatus(task.Status) && s.taskRuntimeAvailable(task, "planner", "worker") {
					if _, reopenErr := task.Store.ReopenIntentsByBlockedReason(db.IntentBlockedLLMQuota); reopenErr != nil {
						log.Printf("[llm-profile] task %s reopen quota-blocked intents after profile delete: %v", task.ID, reopenErr)
					}
				}
				task.Notify()
			}
		}
	}
}

func (s *Server) pgActivateProfile(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		ID int64 `json:"id"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := pg.SetActiveProfile(body.ID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.invalidateProfileAgents() // active change may affect pinned-task fallbacks
	s.reapplyActiveProfile()    // switch the running engine to the newly activated profile
	writeJSON(w, 200, map[string]any{"ok": true})
}

// pgLLMPoolStatus reports the failover ("轮询") switches, the resolved chain order
// and every profile's circuit-breaker state — what the LLM page renders as the
// "轮询顺序" strip and the per-card health badges.
func (s *Server) pgLLMPoolStatus(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	writeJSON(w, 200, s.llmPoolStatus())
}

// pgLLMPoolReset clears a tripped profile's circuit breaker so the next call
// tries it again immediately ("立即恢复"). id=0 clears every profile.
func (s *Server) pgLLMPoolReset(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		ID int64 `json:"id"` // 0 / omitted = all
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.ID > 0 {
		s.llmHealth.Reset(body.ID)
	} else {
		for id := range s.llmHealth.Snapshot() {
			s.llmHealth.Reset(id)
		}
	}
	writeJSON(w, 200, s.llmPoolStatus())
}

// pgListModels fetches available models from the provider's API endpoint.
// Supports OpenAI-format (GET /models) and Anthropic-format (GET /v1/models); for
// Anthropic-compatible third parties (e.g. DeepSeek) whose model list lives only on
// the OpenAI path, it falls back to the OpenAI endpoint at the stripped root.
func (s *Server) pgListModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Provider  string `json:"provider"` // "openai" | "anthropic"
		BaseURL   string `json:"base_url"`
		APIKey    string `json:"api_key"`
		Proxy     string `json:"proxy"`
		ProfileID *int64 `json:"profile_id"` // fallback: use stored key from this profile
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// Resolve API key: form input > profile stored key.
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" && req.ProfileID != nil {
		if p, err := s.m.pg.ProfileByID(*req.ProfileID); err == nil && p != nil {
			apiKey = p.APIKey
		}
	}
	if apiKey == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "未提供 API Key"})
		return
	}

	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	provider := strings.TrimSpace(req.Provider)

	// Candidate endpoints to try in order. Some Anthropic-compatible providers
	// (e.g. DeepSeek) implement /v1/messages under an /anthropic path but expose
	// the model list only on their OpenAI-format path — so for anthropic we fall
	// back to the OpenAI endpoint at the stripped root.
	type candidate struct {
		url string
		hdr http.Header
	}
	bearerHdr := func() http.Header {
		h := http.Header{}
		h.Set("Authorization", "Bearer "+apiKey)
		return h
	}
	anthropicHdr := func() http.Header {
		h := http.Header{}
		h.Set("x-api-key", apiKey)
		h.Set("anthropic-version", "2023-06-01")
		return h
	}

	var candidates []candidate
	switch provider {
	case "openai":
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
		b := strings.TrimRight(strings.TrimSuffix(baseURL, "/chat/completions"), "/")
		candidates = append(candidates, candidate{b + "/models", bearerHdr()})
	default: // anthropic
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		b := strings.TrimRight(strings.TrimSuffix(baseURL, "/v1/messages"), "/")
		candidates = append(candidates, candidate{b + "/v1/models", anthropicHdr()})
		// Fallback for Anthropic-compatible third parties whose model list lives on
		// the OpenAI path: strip a trailing /anthropic and try the OpenAI endpoint.
		if root := strings.TrimRight(strings.TrimSuffix(b, "/anthropic"), "/"); root != b {
			candidates = append(candidates,
				candidate{root + "/models", bearerHdr()},
				candidate{root + "/v1/models", bearerHdr()},
			)
		}
	}

	// Build HTTP client with optional proxy.
	transport := &http.Transport{}
	if p := strings.TrimSpace(req.Proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}

	// Try each candidate; return the first that yields a non-empty model list.
	var lastErr string
	emptyOK := false
	for _, c := range candidates {
		httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.url, nil)
		if err != nil {
			lastErr = "构建请求失败: " + err.Error()
			continue
		}
		httpReq.Header = c.hdr
		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = "请求失败: " + err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Sprintf("API 返回 %d: %s", resp.StatusCode, string(body[:min(len(body), 512)]))
			continue
		}
		// Both OpenAI and Anthropic return {"data": [{"id": "..."},...]}.
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			lastErr = "解析响应失败: " + err.Error()
			continue
		}
		models := make([]string, 0, len(parsed.Data))
		for _, m := range parsed.Data {
			if m.ID != "" {
				models = append(models, m.ID)
			}
		}
		if len(models) > 0 {
			writeJSON(w, 200, map[string]any{"ok": true, "models": models})
			return
		}
		emptyOK = true // 200 but no model ids — keep trying other candidates
	}
	if emptyOK {
		writeJSON(w, 200, map[string]any{"ok": true, "models": []string{}})
		return
	}
	if lastErr == "" {
		lastErr = "未获取到模型列表"
	}
	writeJSON(w, 200, map[string]any{"ok": false, "error": lastErr})
}

// --- prompt template helpers (Go text/template + catalog 白名单) ---

// globalPromptVars are runtime variables available to EVERY agent (built-in and
// custom) regardless of its per-agent catalog. Each agent's render path fills them
// (see agent.nowStr, rendered fresh each turn), so a prompt may always reference
// {{.Now}} — e.g. subtract it from a fixed start stamp to reason about elapsed time.
var globalPromptVars = []db.PromptVar{
	{Name: "Now", Description: "服务端当前时间（每次运行实时刷新；可与固定起始时间相减判断已用时长）", Example: "2026-08-11 14:30:00 CST", Source: "runtime"},
	{Name: "DataDir", Description: "服务端数据根目录（所有任务/会话产物的根；各 agent 实际写盘在其下的子目录，如 <DataDir>/<taskID>）", Example: "/app/data", Source: "runtime"},
}

// withGlobalVars appends the universal runtime vars onto an agent's own catalog,
// so validation / the UI variable list / preview all recognize {{.Now}} etc.
func withGlobalVars(vars []db.PromptVar) []db.PromptVar {
	return append(append([]db.PromptVar{}, vars...), globalPromptVars...)
}

// validateTemplate parses the template and rejects any {{.Var}} not in the catalog.
// Returns "" if valid, otherwise an error message.
func validateTemplate(tmpl string, catalog []db.PromptVar) string {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "模板语法错误: " + err.Error()
	}
	allowed := map[string]bool{}
	for _, v := range catalog {
		allowed[v.Name] = true
	}
	for _, name := range templateFields(t) {
		if !allowed[name] {
			return "变量 {{." + name + "}} 不在该 agent 允许列表"
		}
	}
	return ""
}

// renderPrompt renders with example values (catalog example overridden by sample).
func renderPrompt(tmpl string, catalog []db.PromptVar, sample map[string]string) (string, error) {
	t, err := template.New("p").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", err
	}
	data := map[string]any{}
	for _, v := range catalog {
		data[v.Name] = v.Example
	}
	for k, val := range sample {
		data[k] = val
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// templateFields returns the distinct top-level {{.X}} field names referenced by
// the template (used to validate against the catalog whitelist).
func templateFields(t *template.Template) []string {
	seen := map[string]bool{}
	var out []string
	collect := func(p *parse.PipeNode) {
		if p == nil {
			return
		}
		for _, cmd := range p.Cmds {
			for _, arg := range cmd.Args {
				if f, ok := arg.(*parse.FieldNode); ok && len(f.Ident) > 0 && !seen[f.Ident[0]] {
					seen[f.Ident[0]] = true
					out = append(out, f.Ident[0])
				}
			}
		}
	}
	var walk func(n parse.Node)
	walk = func(n parse.Node) {
		switch x := n.(type) {
		case *parse.ListNode:
			if x == nil {
				return
			}
			for _, c := range x.Nodes {
				walk(c)
			}
		case *parse.ActionNode:
			collect(x.Pipe)
		case *parse.IfNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.RangeNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		case *parse.WithNode:
			collect(x.Pipe)
			walk(x.List)
			walk(x.ElseList)
		}
	}
	if t.Tree != nil {
		walk(t.Tree.Root)
	}
	return out
}
