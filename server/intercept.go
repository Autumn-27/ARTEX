package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/llm"
)

// settingRoEEnforcement is the settings 键 for the RoE scope-enforcement mode:
// "off" | "warn"(默认) | "strict". See guard.RoEConfig.
const settingRoEEnforcement = "roe_enforcement"

// agentGuard returns a fresh guard wired with the manager's interceptor, used
// for chat conversations and for the planner / main-agent. Each agent build
// gets its own guard instance so a new LLM config never shares audit state with
// the old one.
func (s *Server) agentGuard() *guard.Guard {
	g := guard.NewWithInterceptor(s.m.interceptor)
	g.SetRoE(newRoEConfig(s.m.pg))
	return g
}

// newRoEConfig builds the guard RoE wiring backed by pg: mode from the
// roe_enforcement settings 键, scope rules lazily read from task_scope per call
// (范围随 add_task_scope 即时生效,guard 不缓存)。
func newRoEConfig(pg *db.DB) guard.RoEConfig {
	return guard.RoEConfig{
		Mode: func() string {
			v, _, _ := pg.GetSetting(settingRoEEnforcement)
			return v
		},
		Scope: func(taskID int64) []guard.ScopeRule {
			rows, err := pg.Assets().ListTaskScope(taskID)
			if err != nil {
				// 读取失败按未登记范围处理(Unknown → 放行),与覆盖度 fail-open 一致。
				return nil
			}
			return scopeRulesFromRows(rows)
		},
		TaskIDOf: func(ctx context.Context) int64 {
			return agent.RunInfoFrom(ctx).TaskID
		},
	}
}

// scopeRulesFromRows normalizes db task_scope rows into guard scope rules:
// root_domain/subdomain → domain;ip(/32、/128 网段)与 cidr → cidr;icp 原样;
// company/keyword 无可匹配的主机值,跳过(keyword 本来就是死规则)。
func scopeRulesFromRows(rows []db.TaskScope) []guard.ScopeRule {
	out := make([]guard.ScopeRule, 0, len(rows))
	for _, r := range rows {
		switch r.Kind {
		case "root_domain", "subdomain":
			if r.Domain != "" {
				out = append(out, guard.ScopeRule{Kind: "domain", Value: r.Domain})
			}
		case "ip", "cidr":
			if r.Net != "" {
				out = append(out, guard.ScopeRule{Kind: "cidr", Value: r.Net})
			}
		case "icp":
			if r.Value != "" {
				out = append(out, guard.ScopeRule{Kind: "icp", Value: r.Value})
			}
		}
	}
	return out
}

// wireInterceptReviewer installs the LLM fallback judge into the interceptor. The
// judge runs only on tool calls that matched no rule (see intercept.Judge). It
// resolves the configured judge profile (0 → active/default), builds a provider,
// runs a one-shot JSON classification with an explanation for every verdict.
func (s *Server) wireInterceptReviewer() {
	s.m.interceptor.SetReviewer(func(ctx context.Context, profileID int64, prompt string, input intercept.ReviewInput) (intercept.Decision, error) {
		if profileID == 0 {
			if p, err := s.m.pg.ActiveProfile(); err == nil && p != nil {
				profileID = p.ID
			}
		}
		if profileID == 0 {
			return intercept.Decision{}, fmt.Errorf("未配置可用的裁判模型")
		}
		prov, _, ok := s.providerForProfile(profileID)
		if !ok {
			return intercept.Decision{ProfileID: profileID}, fmt.Errorf("裁判模型 profile %d 不可用", profileID)
		}
		text, err := reviewCompletion(ctx, prov, prompt, input)
		if err != nil {
			return intercept.Decision{ProfileID: profileID}, err
		}
		v := intercept.ParseVerdict(text)
		if v.Action == "" {
			return intercept.Decision{ProfileID: profileID}, fmt.Errorf("模型裁决格式无效，必须包含裁决、实际操作、成功后的后果和命中规则")
		}
		return intercept.Decision{Action: v.Action, Message: v.Reason, ProfileID: profileID}, nil
	})
}

func reviewCompletion(ctx context.Context, prov llm.Provider, prompt string, input intercept.ReviewInput) (string, error) {
	user, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	// 整个 ReviewInput(工具参数/历史/任务背景)都含目标侧可控文本,按不可信数据
	// 包裹后再喂给裁判模型,防止其中的注入文本被当成指令。
	return streamCollectText(ctx, prov, prompt, agent.WrapUntrustedData("tool-input", string(user)))
}

// streamCollectText runs a single non-streaming-style completion (thinking off,
// low temperature, bounded output) and returns the concatenated text. The
// budget includes the explanation and complete closing JSON delimiters.
func streamCollectText(ctx context.Context, prov llm.Provider, system, user string) (string, error) {
	temp := 0.0
	req := llm.CompletionRequest{
		System:      []string{system},
		Messages:    []llm.Message{llm.UserText(user)},
		MaxTokens:   1024,
		Temperature: &temp,
		Thinking:    "disabled",
	}
	var sb strings.Builder
	for ev, err := range prov.Stream(ctx, req) {
		if err != nil {
			return "", err
		}
		if ev.Type == llm.SETextDelta {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String(), nil
}

// --- intercept rule CRUD ---

func (s *Server) interceptListRules(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	rules, err := pg.ListInterceptRules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if rules == nil {
		rules = []db.InterceptRule{}
	}
	writeJSON(w, 200, map[string]any{"rules": rules})
}

func (s *Server) interceptCreateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req interceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateInterceptRuleReq(req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.CreateInterceptRule(req.Name, req.MatchTarget, req.MatchType, req.Pattern, req.Action, req.Message, req.Priority, req.Enabled, req.TimeoutEnabled, req.TimeoutSeconds, req.TimeoutAction)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, rule)
}

func (s *Server) interceptUpdateRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad rule id")
		return
	}
	var req interceptRuleReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateInterceptRuleReq(req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rule, err := pg.UpdateInterceptRule(id, req.Name, req.MatchTarget, req.MatchType, req.Pattern, req.Action, req.Message, req.Priority, req.Enabled, req.TimeoutEnabled, req.TimeoutSeconds, req.TimeoutAction)
	if err != nil {
		if errors.Is(err, db.ErrBuiltinRule) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, rule)
}

func (s *Server) interceptDeleteRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad rule id")
		return
	}
	if err := pg.DeleteInterceptRule(id); err != nil {
		if errors.Is(err, db.ErrBuiltinRule) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, map[string]any{"deleted": id})
}

func (s *Server) interceptToggleRule(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
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
	if err := pg.ToggleInterceptRule(id, req.Enabled); err != nil {
		if errors.Is(err, db.ErrBuiltinRule) {
			writeErr(w, 400, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	s.m.interceptor.Invalidate()
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": req.Enabled})
}

// --- pending (ask) ---

func (s *Server) interceptListPending(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	pending, err := pg.ListPendingIntercepts()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if pending == nil {
		pending = []db.InterceptPending{}
	}
	writeJSON(w, 200, map[string]any{"pending": pending})
}

func (s *Server) interceptGetOne(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad pending id")
		return
	}
	p, err := pg.GetInterceptPending(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if p == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) interceptListTaskItems(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	taskID := r.PathValue("taskID")
	if taskID == "" {
		writeErr(w, 400, "bad task id")
		return
	}
	q := r.URL.Query()
	if q.Get("page") == "" && q.Get("size") == "" {
		items, err := pg.ListTaskIntercepts(taskID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if items == nil {
			items = []db.InterceptApprovalRow{}
		}
		writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	page, size := interceptPageParams(q)
	items, total, err := pg.ListTaskInterceptsPage(taskID, page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []db.InterceptApprovalRow{}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func (s *Server) interceptHistory(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	q := r.URL.Query()
	if q.Get("page") == "" && q.Get("size") == "" {
		items, err := pg.ListAllIntercepts(200)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if items == nil {
			items = []db.InterceptApprovalRow{}
		}
		writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
		return
	}
	page, size := interceptPageParams(q)
	items, total, err := pg.ListAllInterceptsPage(page, size)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if items == nil {
		items = []db.InterceptApprovalRow{}
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}

func interceptPageParams(q url.Values) (int, int) {
	page := atoiDefault(q.Get("page"), 1)
	size := atoiDefault(q.Get("size"), 20)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 20
	}
	if size > 100 {
		size = 100
	}
	return page, size
}

func (s *Server) interceptDecide(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad pending id")
		return
	}
	var req struct {
		Decision string `json:"decision"` // "allowed" | "denied"
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Decision != "allowed" && req.Decision != "denied" {
		writeErr(w, 400, "decision 必须是 allowed 或 denied")
		return
	}
	if err := s.m.interceptor.Decide(id, req.Decision == "allowed"); err != nil {
		if errors.Is(err, intercept.ErrAlreadyDecided) {
			writeErr(w, 409, err.Error())
			return
		}
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- guard auto-allow (一键放行) ---

// guardAutoAllowGet returns the current auto-allow status, including the
// remaining validity seconds. Expiry is enforced lazily on read.
func (s *Server) guardAutoAllowGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.m.interceptor.AutoAllowStatus())
}

// guardAutoAllowSet enables/disables the one-click auto-allow switch. When
// enabling, hours 夹逼到 {2,4,8};when disabling, hours is ignored.
func (s *Server) guardAutoAllowSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
		Hours   int  `json:"hours"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.m.interceptor.SetAutoAllow(req.Enabled, req.Hours); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s.m.interceptor.AutoAllowStatus())
}

// --- tool-config (全局工具拦截范围) ---

// interceptGetToolConfig returns the list of tool names that are currently
// configured to enter the intercept rule system.
func (s *Server) interceptGetToolConfig(w http.ResponseWriter, r *http.Request) {
	tools, err := s.m.interceptor.GetEnabledTools()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"enabled_tools": tools})
}

// interceptSetToolConfig replaces the list of tool names that should enter
// the intercept rule system.
func (s *Server) interceptSetToolConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnabledTools []string `json:"enabled_tools"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.EnabledTools == nil {
		req.EnabledTools = []string{}
	}
	if err := s.m.interceptor.SetEnabledTools(req.EnabledTools); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- LLM fallback judge config (全局模型兜底) ---

// interceptGetJudgeConfig returns the resolved judge configuration. Prompt is the
// effective prompt (built-in template when unset), so the UI can prefill it.
func (s *Server) interceptGetJudgeConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.m.interceptor.GetJudgeConfig())
}

// interceptSetJudgeConfig persists the judge configuration.
func (s *Server) interceptSetJudgeConfig(w http.ResponseWriter, r *http.Request) {
	var req intercept.JudgeConfig
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	switch req.FailAction {
	case "allow", "ask", "deny":
	default:
		writeErr(w, 400, "fail_action 必须是 allow、ask 或 deny")
		return
	}
	switch req.AskTimeoutAction {
	case "allow", "deny":
	default:
		writeErr(w, 400, "ask_timeout_action 必须是 allow 或 deny")
		return
	}
	if err := s.m.interceptor.SetJudgeConfig(req); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- helpers ---

type interceptRuleReq struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Priority       int    `json:"priority"`
	MatchTarget    string `json:"match_target"`
	MatchType      string `json:"match_type"`
	Pattern        string `json:"pattern"`
	Action         string `json:"action"`
	Message        string `json:"message"`
	TimeoutEnabled bool   `json:"timeout_enabled"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	TimeoutAction  string `json:"timeout_action"`
}

func validateInterceptRuleReq(req interceptRuleReq) error {
	if req.Name == "" {
		return fmt.Errorf("name 不能为空")
	}
	switch req.MatchTarget {
	case "tool_name", "tool_input":
	default:
		return fmt.Errorf("match_target 必须是 tool_name 或 tool_input")
	}
	switch req.MatchType {
	case "string", "regex":
	default:
		return fmt.Errorf("match_type 必须是 string 或 regex")
	}
	if req.Pattern == "" {
		return fmt.Errorf("pattern 不能为空")
	}
	switch req.Action {
	case "allow", "deny", "ask":
	default:
		return fmt.Errorf("action 必须是 allow、deny 或 ask")
	}
	if req.MatchType == "regex" {
		if _, err := regexp.Compile(req.Pattern); err != nil {
			return fmt.Errorf("pattern 不是有效正则：%w", err)
		}
	}
	return nil
}

func (s *Server) interceptDetail(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok || id <= 0 {
		writeErr(w, 400, "bad approval id")
		return
	}
	detail, err := pg.GetInterceptDetail(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if detail == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, detail)
}
