package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

// goalsDefaultTmpl is the built-in EDITABLE body (段 [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `你是渗透测试目标分解器。你的职责是从用户输入中识别出**最终要达成的结果**，而不是规划攻击步骤。

**目标 = 最终可交付/可核验的结果**

**不是目标的内容（禁止列为子目标）**：
- 信息收集、侦察、端点扫描
- 漏洞分析与验证过程
- 攻击步骤、利用手段
- 结果验证步骤

**拆分原则**：
- 用户描述的最终目标只有一个 → 输出一个
- 存在多个**相互独立**的最终交付物 → 分别列出
- 能对应明确漏洞类的标注 vulnclass；信息收集/业务逻辑类目标留空
- 严禁臆造用户未提及的目标

调用 set_goals 提交结果。`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**额外职责：登记测试资产范围**
除拆分目标外，你还要从上面的「任务目标 / 任务描述」里识别出**明确给出的测试资产范围**，并调用 add_task_scope 登记为本任务范围（资产测试覆盖度的分母，也是授权边界）：
- 完整根域名（如 example.com，其所有子域都在测试范围）→ kind=root_domain，value=example.com
- 单个精确子域名（如 app.example.com，只测这一个）→ kind=subdomain，value=app.example.com
- IP 或网段 → kind=ip / cidr，value=IP 或 CIDR
规则（务必遵守）：
- 只登记**目标/描述里明确写出**的范围；严禁臆造或推断未提及的域名/IP。
- 粒度拿不准时选更**精确**的那种（宁可 subdomain，不要轻易升级为 root_domain）。
- 只处理域名 / IP / 网段；**不要**登记公司范围（company）——任务刚建立、资产系统里通常还没有这家公司，登记不上，公司级范围交由后续 plan 阶段处理。
- reason 简述依据来自哪句话，便于审计。
- 若目标/描述中没有任何明确资产范围，则**不要**调用 add_task_scope。
先用 add_task_scope 登记范围（如有），再调用 set_goals 提交目标。`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// desc is the task's free-text description (背景：靶标范围/flag 数量/交战说明等).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
func DecomposeGoals(ctx context.Context, c Config, goalText, desc string, as *db.AssetStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	prov, err := c.NewProvider()
	if err != nil {
		return nil
	}
	var out struct {
		Goals []GoalSpec `json:"goals"`
	}
	captured := false
	submitTool := actool.Build(actool.Spec{
		Name:        "set_goals",
		Description: "提交拆分后的目标列表。",
		Schema: map[string]any{"type": "object", "properties": map[string]any{
			"goals": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"text":      map[string]any{"type": "string", "description": "一个独立可验证的目标"},
					"vulnclass": map[string]any{"type": "string", "description": "对应漏洞类(若明确)，如 SQLi/IDOR；业务逻辑目标可留空"},
				}, "required": []string{"text"},
			}},
		}, "required": []string{"goals"}},
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			_ = json.Unmarshal(in, &out)
			captured = true
			return actool.Text("ok"), nil
		},
	})
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{})
	tools := []actool.CoreTool{submitTool}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, (&ToolSet{as: as, taskID: taskID}).addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "任务目标：\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\n任务描述（背景信息，可能含靶标范围/flag 数量/交战说明；仅供参考，不要臆造其中未提及的内容）：\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		MaxTurns:               6,
	}, userMsg, captureEmit)
	if !captured {
		return nil
	}
	return out.Goals
}
