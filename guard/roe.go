package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/hook"
)

// RoEConfig wires the RoE authorization-scope check (F5 / AUDIT-REPORT H2) into
// the PreToolUse chain. All fields are optional; a nil Scope disables the check
// entirely (保持现状语义).
type RoEConfig struct {
	// Mode returns the enforcement mode from settings 键 roe_enforcement:
	// "off" | "warn" | "strict". 空/未知值按 warn(默认)。
	Mode func() string
	// Scope returns the task's registered scope rules; server 装配时从
	// db task_scope 拉取。空列表 = 未登记范围 → Unknown → 放行。
	Scope func(taskID int64) []ScopeRule
	// TaskIDOf extracts the task id from the hook ctx (server 注入
	// agent.RunInfoFrom;guard 不反向依赖 agent 包)。nil → 0 → Unknown。
	TaskIDOf func(ctx context.Context) int64
}

// SetRoE installs the RoE scope check. Zero RoEConfig disables it.
func (g *Guard) SetRoE(cfg RoEConfig) {
	g.mu.Lock()
	g.roe = cfg
	g.mu.Unlock()
}

// roeMode normalizes the configured mode; 默认 warn。
func (c RoEConfig) mode() string {
	if c.Mode == nil {
		return "warn"
	}
	switch strings.ToLower(strings.TrimSpace(c.Mode())) {
	case "off":
		return "off"
	case "strict":
		return "strict"
	default:
		return "warn"
	}
}

// checkRoE is the PreToolUse RoE scope gate: extract interaction targets from
// the tool call and compare them against the task's registered scope. Returns
// stop=true with a hook.Result when the call is gated (strict ask / block).
// In / Unknown / no-target all pass through.
func (g *Guard) checkRoE(ctx context.Context, ev hook.Event, cmd string) (hook.Result, bool) {
	g.mu.Lock()
	cfg := g.roe
	g.mu.Unlock()
	if cfg.Scope == nil {
		return hook.Result{}, false
	}
	mode := cfg.mode()
	if mode == "off" {
		return hook.Result{}, false
	}
	var taskID int64
	if cfg.TaskIDOf != nil {
		taskID = cfg.TaskIDOf(ctx)
	}
	if taskID <= 0 {
		return hook.Result{}, false // 非任务运行(chat 等),无范围可查 → Unknown → 放行
	}
	hosts := roeHosts(ev.ToolName, ev.Input, cmd)
	if len(hosts) == 0 {
		return hook.Result{}, false // 提取不出目标 = 无目标,不拦截(保守取向)
	}
	matcher := NewScopeMatcher(cfg.Scope(taskID))
	var out []string
	unknown := false
	for _, h := range hosts {
		switch matcher.Check(h) {
		case ScopeOut:
			out = append(out, h)
		case ScopeUnknown:
			unknown = true
		}
	}
	if len(out) == 0 {
		return hook.Result{}, false // In 或 Unknown → 放行
	}
	if unknown {
		// 部分目标出了已登记范围、部分无规则可依 —— 按 Out 处理已有的违规即可。
		log.Printf("[guard][roe] task %d 部分目标无范围规则可依: %v", taskID, hosts)
	}
	reason := roeMessage(out, cfg.Scope(taskID))
	if mode == "strict" {
		return g.roeAsk(ctx, ev, reason)
	}
	// warn:放行,但写 guard 审计 + 日志。
	g.record(ev.ToolName, "allow", reason, cmd)
	log.Printf("[guard][roe] task %d 范围外目标(warn 放行): %s", taskID, reason)
	return hook.Result{}, false
}

// roeAsk routes a strict-mode out-of-scope call through the same human-approval
// machinery as intercept ask rules; without an interceptor it hard-blocks.
func (g *Guard) roeAsk(ctx context.Context, ev hook.Event, reason string) (hook.Result, bool) {
	if g.interceptor == nil {
		return g.block(ev.ToolName, systemBlockMessage(reason), ""), true
	}
	if ctx.Err() != nil {
		return g.block(ev.ToolName, systemBlockMessage("工作已取消，平台安全管控阻止执行"), ""), true
	}
	dec := intercept.Decision{Action: "ask", RuleName: "内置 RoE 范围强制", Message: reason}
	convID := intercept.ConvIDFromContext(ctx)
	if !g.interceptor.HandleAsk(ctx, convID, dec, ev.ToolName, ev.Input) {
		return g.block(ev.ToolName, systemBlockMessage("RoE 范围外目标，人工审批未通过（用户拒绝或审批超时）"), ""), true
	}
	// 审批通过只豁免 RoE 这一道,其余拦截规则照常评估(不借 RoE 审批绕过 deny 规则)。
	g.interceptor.Log(ctx, convID, dec, ev.ToolName, ev.Input, "allowed")
	return hook.Result{}, false
}

// roeMessage builds the human/agent-facing justification for an out-of-scope
// verdict: 命中的 host、登记的范围规则、如何关闭/降级。
func roeMessage(out []string, rules []ScopeRule) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "RoE 授权范围检查:目标 %s 不在本任务已登记范围内", strings.Join(out, ", "))
	if len(rules) > 0 {
		parts := make([]string, 0, len(rules))
		for _, r := range rules {
			parts = append(parts, r.Kind+"="+r.Value)
		}
		fmt.Fprintf(&sb, "（登记范围: %s）", strings.Join(parts, ", "))
	}
	sb.WriteString("。如需放行:把目标加入任务范围(add_task_scope),或在系统设置把 roe_enforcement 改为 warn(仅告警)/off(关闭)。")
	return sb.String()
}

// roeHosts returns the interaction targets of a tool call: ExtractHosts over
// the shell surface for Bash 类工具,URL host for WebFetch。
func roeHosts(toolName string, input []byte, cmd string) []string {
	if cmd != "" {
		return ExtractHosts(cmd)
	}
	if toolName == "WebFetch" {
		var in struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(input, &in) == nil && in.URL != "" {
			if u, err := url.Parse(in.URL); err == nil && u.Hostname() != "" {
				return []string{u.Hostname()}
			}
		}
	}
	return nil
}
