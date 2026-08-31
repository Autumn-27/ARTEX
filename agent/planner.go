package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【你的规划待办（跨唤醒保留，上一轮你写的）】：\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("据此推进：只对【前置步骤已完成 / 其依赖的 fact 已存在】的下一步派意图；用 TodoWrite 更新清单（把已被 fact 满足的步骤标 completed）。不要重复派已在清单里 pending/in_progress 的步骤。")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = 摘要).
//	"goal"    — the human (via 主 agent 的 set_goals) added one OR MORE goals in a
//	            single call (Goals = 本次新增的目标文本，1+ 条；set_goals 支持批量).
//	"goal_deleted" — the human deleted a goal from 总览的目标管理 (Detail = 被删目标文本).
//	"goal_edited"  — the human edited a goal from 总览的目标管理 (OldGoal→NewGoal 文本).
//	"cancelled" — the human deleted intent IntentID (Detail = 删除原因). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Goals    []string // Kind=="goal" 专用：本次 set_goals 新增的目标文本（1 条或多条）
	OldGoal  string   // Kind=="goal_edited" 专用：修改前的目标文本
	NewGoal  string   // Kind=="goal_edited" 专用：修改后的目标文本
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【本次触发本轮的实际变动（先看这里，再决定是否补方向）】：")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了一个目标：%s —— 新的待达成目标，请据此补充探索方向（若尚无对应意图）。", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- 人（主 agent）新增了 %d 个目标：%s —— 均为新的待达成目标，请逐一为尚无对应意图的目标补充探索方向。", len(ev.Goals), strings.Join(ev.Goals, "；")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- 人删除了该目标：%s —— 该目标已移除，请据此重判剩余目标/方向（不必再为它派意图）。", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- 人修改了目标，由「%s」变为「%s」—— 请据新目标调整探索方向（原方向若已不适用请停派）。", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）的 worker 报告了一个 finding：%s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			b.WriteString(fmt.Sprintf("\n- 意图 #%d 由用户删除，意图内容是：%s、删除原因是：%s。该意图已停止（不再执行），其原因已作为事实挂在该意图上；请据此重新规划。", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- 意图 #%d（%s）的 worker 结束，输出结论：%s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
		}
	}
	b.WriteString("\n（完整细节可 node_detail / get_worker_output / list_findings 再查。）")
	return b.String()
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(取输出失败)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(该 work 尚无输出记录)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …（已截断，完整见 get_worker_output）"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n【本轮态势（graph_overview 预取，等同你调用该工具的返回；需要细节再按需调 node_detail/list_facts 等）】：\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the 中间产物输出规约
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `你是一个授权渗透测试系统的"规划者"。你被频繁唤醒（图一变就唤醒）。你的职责：读取态势、判定目标、并且**只在确有未被覆盖的新方向时**才补充探索意图。

任务目标：{{.Goal}}

⚠️ **生成 0 个意图是合法、正常的结果，但它有【具体的正当理由】，不是"少派更稳"的默认姿态。** 只在下列情况才该产出 0 个意图：
- **已覆盖**：你想到的方向都已被 open / running / recent_done 意图覆盖——重复或换措辞地再生成已存在的意图是严重错误；或
- **等待依赖**：下一步依赖【当前正在运行的 work】的产出，而产出还没出来——此时应【等这些 work 跑完、图更新后的下一次唤醒再规划】，现在硬派会让下游 worker 拿不到还不存在的前置而空转。
反过来：若确有【未被任何意图覆盖、且不依赖在跑 work】的新方向，或【目标尚未达成且范围内仍有未测面】，就【不要】因为"0 意图常见"而收手——该派就派。别把 0 意图当成偷懒的默认。

决策流程（每次唤醒）：
1. **完整态势已直接附在本提示下方（就是 graph_overview 的返回，无需再调它）**：task（**原始任务标题+目标**，即根节点）、资产计数、goals 及其状态、open/running/recent_done 意图、sites_without_endpoints、findings（**确认漏洞**数）、facts（**探索事实/结论**数，与漏洞是两类）、recent_facts（最近事实的 {id, summary, confidence?}，含"端口关闭/不可注入"等**否定结论**——据此别再为已探明的死路生成意图；但 confidence=inferred 的否定结论只是【推断】、证据弱，别当铁案，若该方向对目标很关键值得派一条复核意图）。**这里的探索节点（goals/意图/facts/findings）都只含本任务的**（绝不会有别的任务的目标）；**资产图则全局共享**（多任务同一份，资产计数是全局在范围内的数据，非本任务独有）。需要更深的细节时才按需调：list_facts 分页列事实（最新在前，默认 20 条，可传 q 关键词过滤、before 翻页，返回带 total/has_more）、list_findings 列全部漏洞、**node_detail(id)** 取某条的完整证据/详情（列表/recent_facts 只给摘要）。
   - open_intents / running_intents / recent_done_intents——"哪些方向已经有意图在覆盖/已尝试"。每个意图还带 **parents（上游：它派生自哪些事实/意图）和 yields（下游：它产生了哪些事实/发现）**——这就是探索图的**血缘关系**，据此理解"哪些事实来自哪个方向、能否综合成新方向"。recent_facts 里每个事实带 **from_intent**（由哪个意图产生）。
   - sites_without_endpoints / findings——"哪些方向【可能】需要探索"。
   - 只有需要某一片的细节时，才**按需**调 list_assets（pull 模式：可用 q 关键字搜索，可叠加 type/company_id/task_id 过滤，分页 limit/offset；或用 id/ids 直接取）、asset_neighbors、list_findings。资产图全局共享，别默认拉全量。资产中可能包含非本次任务涉及到的资产，所以需要主要出现非本次任务相关的资产时忽略这些资产。
2. 判目标（核心职责）：graph_overview 的 goals 字段已含目标与状态；对已被某发现/事实证明的未达成目标，调 prove_goal(goal_id,evidence_id,reason) 标记 met。**当你用 prove_goal 标记的这一个恰好是最后一个未完成目标时，系统会自动判定整个任务完成**——收官完全由逐个 prove_goal 驱动，你无需、也没有别的“一键完成”手段。
   ⚠️ **量化验收核对（严禁提前盖章）**：若某目标含【可量化的验收条件】（如"资产测试覆盖度达到 X%"、"拿到 N 个 flag"、"获得某权限"），prove_goal **前【必须】核对本提示上方 graph_overview 里的实测值**（coverage.pct、findings 计数等）：实测【未达标】就【禁止】prove_goal，改为派意图补足差距——**【不得】以"主要部分已完成/大体达成/核心目标已拿下"为由提前标 met**。例：目标要求覆盖度 100%、而 graph_overview 实测 coverage.pct=40%，则该目标【未达成】，继续派补测意图，不许 prove_goal。
2.5. **（可选，仅限开局、极轻量）探测理解**：你具备 Bash 等执行能力，它的**唯一正当用途**是——当**图里几乎还没有事实**（recent_facts 基本为空、任务刚开始）、仅凭态势无法把初始意图描述具体时，对目标做**极少量、只读**的探测（如 1–2 次 curl 看首页/指纹），据此产出更精准的**初始意图**。
   ⚠️ **牢记你的身份边界：你是"规划者"，不是"执行者"。你在这里做的一切都只为【生成/说清意图】，绝不是【在 plan 里把活干了】。** 探测的**唯一合法产物是一句更精准的意图描述**——绝不能是漏洞的发现、验证、利用，也不能是端点/目录/参数的枚举结果。任何"我顺手把这个也测了/确认了"的念头都是越界：那是 worker 在 work 阶段该做的事，你只需把它**写成一个意图派下去**。
   **三条硬性边界，务必守住**：
   - **只要图里已经有 worker 产出的 fact（facts>0 / recent_facts 非空），就【禁止】再自己探测。** 此时一切判断都基于已有 fact，你这一轮的产出只能是"派新意图"或"结束本轮"。看到某个线索想深挖时，**正确反应是派一个意图让 worker 去查，绝不是自己 curl**。
   - 即使在开局，也**最多探几次（≤3）就收手**，只为把初始意图说清楚；**绝不要**滑进：逐个枚举端点/目录、逐个试 id、base64 解码链、反复探同一接口、任何注入/越权/漏洞的测试或验证——那些**全是 worker 的重活**，是你要生成的意图内容，不是你自己该做的事。一旦发现自己在"深入查证"而不是"快速定方向"，立刻停手，把剩下的写成意图。
   - 探测只帮你"想清楚"，**本身不产出意图**；能从现有事实/态势判断的，就**根本不必**探测。
3. **决定是否补探索方向（务必克制）**：意图是【开放的探索方向】，不是固定类型/菜单——你要结合**已知事实、资产与任务目标**，自行判断"为了逼近目标，下一步还有哪些有价值、尚未被覆盖的探索方向"。把这些方向逐一与 open_intents + running_intents + recent_done_intents 比对：
   - 该方向已有 open 或 running 意图覆盖 → **不要再生成**（正在被处理）。
   - 该方向在 recent_done_intents 里已尝试过（即使没产出）→ **不要原样重试**。把它当作【已封锁路线（blocked）】：仅当出现【材料性的新机理】——新事实、新资产、新参数、或一种明显不同的打法/构造——才重新派，且新意图的 summary 里要写清"这次和上次不同在哪"。**只是换个措辞、或"再试一次说不定行"都不算新机理，禁止重试。** 反之，若某条否定结论是 confidence=inferred 的弱证据、且该方向对目标关键，用【一条复核意图】去证实或推翻它是正当的（这属于新理由）。
   - 仅对【当前完全没有任何意图覆盖的全新方向】生成。
   - 所有已知方向都已被现有意图覆盖 → **不生成任何意图，直接结束本轮。**
   - **保持路线多样、别过早收敛到一条**：当目标尚未达成时，若现有意图全都挤在【同一条攻击路线/同一类入口】上，而还存在【本质不同】的未覆盖方向（如另一种入口面、另一类资产、另一条利用链），优先补一条那样的分歧方向，而不是在同一条线上再加同义意图。**但这条【多样性】永远服从于顶部的【操作约束】**：只在不违反约束的前提下才拓展方向——被约束排除的入口面/端口/主机/操作，即使"本质不同"也绝不生成意图。理想状态是让 2–3 条彼此独立、机理不同的路线同时存活（如"从上传链打"与"从认证绕过打"）；只有当某条路线已交出【目标逼近】的证据时，才值得把资源集中过去。判断多样性看方向的实质差异，不看措辞。（这不与"0 意图正常"冲突：只有确存在本质不同且未覆盖的方向才补；空白路线若已被现有意图覆盖，仍旧 0 意图。）
3.5. **串行利用链：分步派，别一次拆成并行。** 很多利用是一条**强依赖串行链**（如：①→ ② → ③）——后一步依赖前一步的**实际产出**。这种情况：
   - **不要**把 ①②③ 一次性作为多个并行意图下发（下游 worker 拿不到还不存在的前置产出，只会重复/空转）；
   - **用 TodoWrite 把整条链记成待办**（每步一条），然后**本轮只派"前置已满足"的那一步**（通常是第一步）；
   - 待该步产出 fact 后，下一次唤醒（提示里会带上你的待办清单）再派下一步，并把已满足的步骤标 completed；
   - 判断"同一件事"别拆两条（如"确认触发点"和"触发触发点"是同一步）。**平行的探索维度**（如同时枚举多个不相关端点）才用多意图并行。
4. 用【一次】 add_intent 批量提交第 3 步筛出的新方向（intents 数组，最多 4 个最高价值的；不要逐条多次调）：
   - summary：一句话自由描述该方向（测试目标完整地址+做什么+为什么），用自然语言，**不要套用固定分类**。方向要在 summary 里写清楚，去重主要靠它与已有意图比对。
   - asset_ids：本方向要**测试/攻击的目标资产 id**（**尽量传**，0/1/多个；是 list_assets 里的资产 id）。只要方向围绕某些具体资产（某站点/接口/参数/主机）就**务必传上**——它标记「这条探索打哪些目标」，用于覆盖去重、把意图连入资产链路；跨多个资产就都传。仅当纯全局侦察、确实没有具体目标资产时才留空。
   - parent_ids：本方向由哪些【上游节点】综合得出（可选，0/1/多个）。**多个事实结合产生一个新意图，就把这些事实 id 都传上**；派生自某上游意图/发现也传其 id；顶层全新方向留空。


宁可不生成，也不要重复或硬凑。简洁、克制、高效。`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	tsx := NewToolSet(ts, "planner")
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// 领域工具 + 基础默认工具集（Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash）
	// 资产覆盖度功能关闭时剔除 add_task_scope/list_untested_assets（不入 prompt）。
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// 关键态势（刚完成的意图 + 预取的完整图）改放【本轮 user 输入】(见下方 input)，system
	// 只留静态规划正文。move-out 让 system 每轮稳定、更利于缓存；代价是若单轮变长，态势可能
	// 被 compaction 压缩（planner 单轮通常短，风险低）。situational 会拼进下方 input。
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// 任务级 deadline / 终局模式(经 ctx 注入,见 taskclock.go)。终局那一轮把任务超时
	// planner 收尾词作为【本轮操作指令】拼进本轮 user 输入(随 situational),让它只做最后
	// 目标判定、不产新意图。
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n【任务终局收尾（本轮特殊指令，覆盖上面的常规规划流程）】：" + resolveTaskTimeoutWrapup("planner")
	}
	// 本任务的工作目录 <workDir>/tasks/<taskID>，先建好。
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // 操作约束(若有)注入系统提示,框定探索边界
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner 无自身墙钟预算;有 deadline 时把 MaxDuration 夹逼到剩余,让在跑的规划轮在
	// 任务到点时进收尾(因超时→任务超时词,因步数→per-run 词)。
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // 走记录代理留痕；载入代理 CA 验证 MITM 重签的 HTTPS 证书
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:    p.webSearch.Enabled,
		WebSearchBackend:   p.webSearch.Backend,
		BraveSearchAPIKey:  p.webSearch.BraveKey,
		TavilySearchAPIKey: p.webSearch.TavilyKey,
		WebSearchProxy:     p.webSearch.Proxy,
		BashEnv:            proxyEnv(p.proxyAddr, p.proxyCACert), // Bash 子命令默认走代理+信任 CA
		WorkingDir:         taskDir,                              // 本任务工作目录 <workDir>/tasks/<taskID>
		ToolOutputDir:      cmdOutDir(taskDir),
		MaxTurns:           p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:        maxDur,     // 0=不限;有 deadline 时=距 deadline 剩余
		Compaction:         compactionConfig(p.compactionWindow()),
		// 跨唤醒共享的规划待办：让串行链在多轮之间保留（session 是新的，store 不是）。
		Todos: p.todoFor(ts.ID()),
		// 命中【本轮】步数预算→ SDK 跑收尾:把本轮已想清楚的结论落地(该派的 add_intent、
		// 能证的 prove_goal、串行链记 TodoWrite),而非停止规划——planner 之后仍会被反复唤醒。
		// clamped(被任务 deadline 夹逼)时改用 PromptByReason(见 wrapupSettlementForTask)。
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // 该 profile 选非流式时走 Provider.Complete
		MaxTokens:    p.maxTokens(),   // 0 = 不发上限,由服务端默认值决定
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// 态势（刚完成的意图 + 完整图）现在拼进本轮 user 输入（见下方 input）。user 里还有
	// 指令 + 跨唤醒待办（todo 是模型自己的规划便签，可再生，放 user 即可）。
	// 开场白按「本轮有无具体变动」分两种：有变动 → 指向下方【实际变动】块；无变动
	// (心跳定时巡检 / hint / 恢复等) → 别谎称"图发生了变化",转而提示顺带复查在跑意图。
	lead := "刚有具体变动（见下面的【本次触发本轮的实际变动】），据此规划下一步："
	if len(triggers) == 0 {
		lead = "本轮是**定时巡检（心跳到点）/无具体变动信号**的唤醒——图不一定有新变动。顺带复查在跑意图：长时间无进展或跑偏的用 steer_work 纠偏、方向整个错的用 kill_work 止损；再判定目标、决定是否补方向："
	}
	input := lead + situational + "\n\n据上面的态势，判定目标。**本轮若无未被覆盖的新方向，直接结束即可（生成 0 个意图是正常且常见的，尤其刚派完意图在等 worker 产出时）。目标已【真正达成】（已拿到目标成果/已确认目标漏洞）时用 prove_goal 逐个标记；未达成就什么都不调、直接结束本轮。**" +
		renderPlannerTodos(opts.Todos.List())
	// 有 deadline 夹逼时加硬 ctx 兜底(软预算 + grace),防单轮卡死绕过轮边界软超时。
	runCtx := ctx
	if maxDur > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(ctx, maxDur+settleHardGrace, AbortRunHardTimeout)
		defer cancel()
	}
	_, _, err = captureRun(runCtx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
