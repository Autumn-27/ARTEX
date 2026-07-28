package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	acperm "github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/artex/db"
)

// compactIntents distills intents to {id, summary, state, asset_ids, parents,
// yields} so the planner sees both the direction and its LINEAGE — parents (the
// upstream nodes it derived from: facts/intents/findings) and yields (the facts/
// findings it produced) — without pulling full payloads. parentsOf/yieldsOf are
// built from the exploration edges in graph_overview.
func compactIntents(ns []*db.Node, parentsOf, yieldsOf map[int64][]int64) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		var p map[string]any
		_ = json.Unmarshal(n.Payload, &p)
		m := map[string]any{"id": n.ID, "summary": p["summary"], "state": n.State}
		// asset_ids is the structured "which assets this direction covers" signal for
		// dedup; fall back to legacy payload keys (target_ids plural, then target_id
		// single) so intents stored before the rename still surface their anchors.
		if tg, ok := p["asset_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_ids"]; ok && tg != nil {
			m["asset_ids"] = tg
		} else if tg, ok := p["target_id"]; ok && tg != nil && tg != "" {
			m["asset_ids"] = []any{tg}
		}
		if ps := parentsOf[n.ID]; len(ps) > 0 {
			m["parents"] = ps // 上游：本意图派生自哪些节点（多个事实可共同产生一个意图）
		}
		if ys := yieldsOf[n.ID]; len(ys) > 0 {
			m["yields"] = ys // 下游：本意图产生了哪些事实/发现
		}
		out = append(out, m)
	}
	return out
}

// ToolSet exposes the PG-backed dual graph (asset + exploration) to an LLM agent.
// One ToolSet is created per planner/worker run; per-run signals live here.
type ToolSet struct {
	as     *db.AssetStore   // asset store (optional; nil = asset tools not available)
	cs     *db.CompanyStore // company store (optional)
	ts     *db.ExplorationStore
	worker string
	taskID int64 // PG tasks.id; 0 when unknown (tests / orchestrator cross-task reads)
	// ownerNode is the exploration node that writes attach to: assets this run
	// touches get anchored to it as lineage/provenance (NOT visibility — the asset
	// graph is global and shared). Worker = its claimed intent; planner = begin root.
	ownerNode int64
	GoalMet   bool
	Reason    string
	writes    WriteCounts
	// killWork, if set, terminates a running work by intent id (engine callback,
	// wired by the planner). nil = the kill_work tool reports unavailable.
	killWork func(intentID int64) error
	// steerWork, if set, queues a mid-run course-correction for the work running an
	// intent id (engine callback, wired by the planner): the worker injects it before
	// its next tool call and re-plans, without being killed. nil = tool unavailable.
	steerWork func(intentID int64, msg string) error
	// enrich, if set, receives async auto-completion triggers (DNS resolve for a
	// domain, HTTP probe for a site). nil = no engine enrichment.
	enrich EnrichTrigger
	// notify, if set, wakes the task's planner after a graph change that should be
	// re-planned promptly (currently: a new hint). nil = no wake (the hint is still
	// stored and read on the next round triggered by other events). debounced.
	notify func()
}

// SetNotify wires the planner-wake callback (see ToolSet.notify). Set by callers
// that hold the task handle (main-agent chat, cross-task orchestration).
func (t *ToolSet) SetNotify(fn func()) { t.notify = fn }

// EnrichTrigger is the enrichment engine seen from the tool layer (see package
// enrich). Kept as an interface here to avoid coupling agent → enrich.
type EnrichTrigger interface {
	ResolveDomain(id int64, host string)
	ProbeSite(id int64, url string)
}

// WriteCounts breaks down what a worker persisted this run, by node kind, so the
// engine can log an accurate "wrote back" summary instead of lumping assets and
// findings under "facts" (record_fact → Facts, insert_assets → Assets,
// report_finding → Findings; each element of a batch counts once).
type WriteCounts struct {
	Facts    int
	Assets   int
	Findings int
}

// Total is every node persisted this run, regardless of kind — the
// "explored but persisted nothing" signal (Total == 0).
func (w WriteCounts) Total() int { return w.Facts + w.Assets + w.Findings }

// String renders the per-kind breakdown for logs, e.g. "事实1 资产25 漏洞0".
func (w WriteCounts) String() string {
	return fmt.Sprintf("事实%d 资产%d 漏洞%d", w.Facts, w.Assets, w.Findings)
}

// Writes reports what this run wrote back, split by node kind (so the engine can
// tell "explored but persisted nothing" apart from a completed intent, and log an
// honest breakdown instead of calling assets/findings "facts").
func (t *ToolSet) Writes() WriteCounts { return t.writes }

func NewToolSet(ts *db.ExplorationStore, worker string) *ToolSet {
	return &ToolSet{ts: ts, worker: worker}
}

// SetTaskID sets the PG task id on this ToolSet so that report_finding can
// dual-write to the standalone findings table (which survives task deletion).
func (t *ToolSet) SetTaskID(id int64) { t.taskID = id }

// Cross-task reuse: exported accessors returning the per-task tool logic bound to
// THIS ToolSet's store. Host-side orchestration tools build a ToolSet for an
// arbitrary task, then Call these — so cross-task reads/hint reuse the exact
// same logic as the in-task tools. (readTool ignores ToolContext, so Call(…,nil)
// is safe; add_hint is a writeTool but also doesn't deref the context here.)
func (t *ToolSet) GraphOverviewTool() actool.CoreTool      { return t.graphOverview() }
func (t *ToolSet) ListFindingsTool() actool.CoreTool       { return t.listFindings() }
func (t *ToolSet) GetWorkerTraceTool() actool.CoreTool     { return t.getWorkerTrace() }
func (t *ToolSet) ListWorkerTracesTool() actool.CoreTool   { return t.listWorkerTraces() }
func (t *ToolSet) SearchWorkerTracesTool() actool.CoreTool { return t.searchAllWorkerTraces() }
func (t *ToolSet) AddHintTool() actool.CoreTool            { return t.addHint() }

// SetEnrich wires the async enrichment engine (DNS/HTTP auto-completion).
func (t *ToolSet) SetEnrich(e EnrichTrigger) { t.enrich = e }

// SetOwnerNode sets the exploration node that writes anchor to (worker: its
// intent node; planner/main: the begin root). Assets created/referenced while
// ownerNode is set are anchored to it as lineage (not visibility).
func (t *ToolSet) SetOwnerNode(id int64) { t.ownerNode = id }

// anchorOwner records a lineage edge from this run's owner node to an asset
// (no-op if unset). Provenance only — the asset graph is global and shared, so
// this no longer affects which assets a task can read.
func (t *ToolSet) anchorOwner(assetID int64) {
	if t.ownerNode > 0 && assetID > 0 {
		_ = t.ts.Anchor(t.ownerNode, assetID)
	}
}

// pid parses an id that may arrive as a JSON number or string ("" / 0 → 0).
func pid(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// pidList parses a list of ids (number|string), dropping zeros/invalids.
func pidList(raw []json.RawMessage) []int64 {
	var out []int64
	for _, r := range raw {
		if v := pid(r); v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func intp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func idp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func readTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		ReadOnly:    func(json.RawMessage) bool { return true },
		Concurrent:  func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func writeTool(name, desc string, schema map[string]any, run func(context.Context, json.RawMessage) (actool.Result, error)) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: desc, Schema: schema,
		Permissions: func(context.Context, json.RawMessage, acperm.Context) acperm.Decision { return acperm.Allowed() },
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			return run(ctx, in)
		},
	})
}

func jsonResult(v any) (actool.Result, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return actool.Errorf(err.Error()), nil
	}
	return actool.Text(string(b)), nil
}

// --- read tools (planner + worker) ---

func (t *ToolSet) graphOverview() actool.CoreTool {
	return readTool("graph_overview",
		"(探索链路图)探索态势蒸馏摘要：资产计数、无接口的站点、frontier、发现、hints(人类/主 agent 的战略提示，生成意图时须纳入)。规划时先调它。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			return jsonResult(t.graphOverviewData())
		})
}

// graphOverviewData computes the distilled situational snapshot shared by the
// graph_overview tool and the planner's wake-up prompt (which pre-injects it so
// the model needn't spend a turn calling the tool — every plan round starts with
// an empty context and always needs this first).
func (t *ToolSet) graphOverviewData() map[string]any {
	{
		out := map[string]any{}
		// goals summary folded in so the planner needn't call list_goals each round.
		goals, _ := t.ts.ListByKind(db.KindGoal, 100)
		gsum := make([]map[string]any, 0, len(goals))
		for _, g := range goals {
			var p map[string]any
			_ = json.Unmarshal(g.Payload, &p)
			gsum = append(gsum, map[string]any{"id": g.ID, "state": g.State, "text": p["text"]})
		}
		out["goals"] = gsum
		// hints: 人类/主 agent 通过 add_hint 挂上图的战略提示；folded in so the
		// planner reads them every round when generating intents (否则只写不读).
		hints, _ := t.ts.ListByKind(db.KindHint, 50)
		hsum := make([]map[string]any, 0, len(hints))
		for _, h := range hints {
			var p map[string]any
			_ = json.Unmarshal(h.Payload, &p)
			hsum = append(hsum, map[string]any{"id": h.ID, "state": h.State, "text": p["text"]})
		}
		out["hints"] = hsum
		// lineage from the exploration edges: an intent's parents (what it
		// derived_from — possibly several facts combined) and its yields (the
		// facts/findings it produced). factFrom maps a fact → the intent that
		// produced it. This is the relationship layer the flat lists lacked.
		edges, _ := t.ts.Edges(5000)
		parentsOf := map[int64][]int64{}
		yieldsOf := map[int64][]int64{}
		factFrom := map[int64]int64{}
		for _, e := range edges {
			switch e.Rel {
			case db.RelDerivedFrom, db.RelSpawns: // upstream: derived_from (fact/finding/intent→intent) or spawns (origin fact→goal, legacy begin→intent)
				parentsOf[e.To] = append(parentsOf[e.To], e.From)
			case db.RelYields: // intent --yields--> fact/finding
				yieldsOf[e.From] = append(yieldsOf[e.From], e.To)
				factFrom[e.To] = e.From
			}
		}
		fr, _ := t.ts.Frontier(100)
		out["open_intents"] = compactIntents(fr, parentsOf, yieldsOf)
		all, _ := t.ts.ListByKind(db.KindIntent, 300)
		var running, recentDone []*db.Node
		for _, n := range all {
			switch n.State {
			case "running":
				running = append(running, n)
			case "done", "blocked", "exhausted":
				if len(recentDone) < 15 {
					recentDone = append(recentDone, n)
				}
			}
		}
		out["running_intents"] = compactIntents(running, parentsOf, yieldsOf)
		out["recent_done_intents"] = compactIntents(recentDone, parentsOf, yieldsOf)
		out["frontier_open"] = len(fr)
		// findings (confirmed vulns) and facts (worker exploration results) are
		// now distinct node kinds. recent_facts surfaces fact summaries (esp.
		// negative results) so the planner sees them in one call; full content
		// via node_detail(id).
		vulnNodes, _ := t.ts.ListByKind(db.KindFinding, 1000)
		factNodes, _ := t.ts.ListByKind(db.KindFact, 1000) // newest first
		out["findings"] = len(vulnNodes)                   // 确认漏洞数（目标判定看它）
		out["facts"] = len(factNodes)                      // 探索事实/结论数（含否定结论）
		recentFacts := make([]map[string]any, 0, 20)
		for _, n := range factNodes {
			if len(recentFacts) >= 20 {
				break
			}
			m := compactNode(n)
			if from := factFrom[n.ID]; from > 0 {
				m["from_intent"] = from // 本事实由哪个意图产生
			}
			// confidence 带进概览：让规划者一眼看出哪条结论只是 inferred（尤其否定结论
			// 别当铁案）；evidence 较长，留给 node_detail(id)。
			var fp map[string]any
			if json.Unmarshal(n.Payload, &fp) == nil {
				if c, ok := fp["confidence"].(string); ok && c != "" {
					m["confidence"] = c
				}
			}
			recentFacts = append(recentFacts, m)
		}
		out["recent_facts"] = recentFacts // 最近事实的 {id, summary, from_intent, confidence?}，详情/证据用 node_detail(id)
		// the original task (root) so the planner always has it, not just the
		// decomposed goals.
		if description, goal, err := t.ts.Root(); err == nil {
			out["task"] = map[string]any{"description": description, "goal": goal}
		}
		return out
	}
}

// compactNode distills any exploration node to id + summary + state, dropping the
// big detail/evidence (fetch that on demand via node_detail).
func compactNode(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	return map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
}

// compactFinding is compactNode plus the vuln-specific vulnclass/severity.
func compactFinding(n *db.Node) map[string]any {
	var p map[string]any
	_ = json.Unmarshal(n.Payload, &p)
	m := map[string]any{"id": n.ID, "state": n.State, "summary": p["summary"]}
	if vc, ok := p["vulnclass"]; ok && vc != nil && vc != "" {
		m["vulnclass"] = vc
	}
	if sv, ok := p["severity"]; ok && sv != nil && sv != "" {
		m["severity"] = sv
	}
	return m
}

func (t *ToolSet) listFindings() actool.CoreTool {
	return readTool("list_findings", "列【确认漏洞】(紧凑：id+task_id+intent_id+vulnclass+severity+摘要+状态)。task_id=该漏洞所属任务, intent_id=产生它的意图。这里只有漏洞,不含普通探索事实(那是 list_facts)。详情用 node_detail(id)。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKind(db.KindFinding, 500)
			intentOf, _ := t.ts.FindingIntents() // finding id -> 产生它的 intent id
			taskID := t.ts.ID()                  // exploration id = 任务 id（本 store 内所有 finding 同属）
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				m := compactFinding(n)
				m["task_id"] = taskID
				if iid, ok := intentOf[n.ID]; ok {
					m["intent_id"] = iid
				}
				out = append(out, m)
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listFacts() actool.CoreTool {
	return readTool("list_facts", "列【探索事实/结论】(紧凑：id+摘要+状态),即 worker 按意图探出的结论(含'端口关闭/不可注入'等否定结论)。详情用 node_detail(id)。漏洞看 list_findings。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			f, _ := t.ts.ListByKind(db.KindFact, 500)
			out := make([]map[string]any, 0, len(f))
			for _, n := range f {
				out = append(out, compactNode(n))
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) nodeDetail() actool.CoreTool {
	return readTool("node_detail", "按 id 取某个【探索图节点】的完整内容(事实/发现/意图/目标：摘要 + 详情/证据/PoC)。仅限探索图节点 id(来自 list_facts / list_findings / graph_overview 的 recent_facts 返回的 id)。查【资产】请用 list_assets / asset_neighbors——资产 id 不是探索节点 id,不要传进来。",
		obj(map[string]any{"id": idp("探索图节点 id(非资产 id)")}, "id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				ID json.RawMessage `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.ID)
			if id <= 0 {
				return actool.Errorf("id 必填"), nil
			}
			n, err := t.ts.GetNode(id)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if n == nil {
				return actool.Errorf(fmt.Sprintf("未找到探索节点 %d。若你想查的是资产，请用 list_assets / asset_neighbors（资产与探索节点是不同的 id 空间，资产 id 不能传给 node_detail）。", id)), nil
			}
			return jsonResult(n) // full payload incl. detail / evidence
		})
}

// --- planner write tools ---

// intentItem 是 add_intent 批量/单条的一条探索方向。
type intentItem struct {
	Summary   string            `json:"summary"`
	AssetIDs  []json.RawMessage `json:"asset_ids"`
	ParentIDs []json.RawMessage `json:"parent_ids"`
	Priority  int               `json:"priority"`
}

// addOneIntent 创建一条意图节点并连上游血缘，返回 id。
// 约束：意图只能锚在已确认知识上——每个 parent_id 必须是已存在的 fact/finding
// 节点（不能挂在别的意图/目标/提示上）。顶层全新方向留空 parent_ids，兜底连 origin fact。
// 这样"每个意图都连到 fact 节点、且是发现驱动而非凭空规划"从创建路径上被强制。
func (t *ToolSet) addOneIntent(it intentItem) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary 不能为空")
	}
	// 先校验锚点（建节点前，避免坏锚点留下孤儿意图）。
	parents := pidList(it.ParentIDs)
	for _, pidv := range parents {
		n, err := t.ts.GetNode(pidv)
		if err != nil || n == nil {
			return 0, fmt.Errorf("parent_id %d 不存在：parent_ids 必须是已存在的【事实(fact)/发现(finding)】节点 id；顶层全新方向请留空 parent_ids", pidv)
		}
		if n.Kind != db.KindFact && n.Kind != db.KindFinding {
			return 0, fmt.Errorf("parent_id %d 是 %q 节点，不能作为意图锚点：意图只能锚在已确认的【事实(fact)/发现(finding)】上，不能挂在意图/目标/提示上；顶层全新方向请留空 parent_ids", pidv, n.Kind)
		}
	}
	priority := it.Priority
	if priority == 0 {
		priority = 5
	}
	anchors := pidList(it.AssetIDs)
	payload := map[string]any{"summary": it.Summary}
	if len(anchors) > 0 {
		payload["asset_ids"] = anchors
	}
	id, err := t.ts.AddIntent(payload, priority, anchors, "planner")
	if err != nil {
		return 0, err
	}
	// upstream lineage: link each (validated) fact/finding parent → this intent, so
	// "multiple facts combine into one new intent" is expressible.
	for _, parent := range parents {
		_ = t.ts.Link(parent, db.RelDerivedFrom, id)
	}
	// a top-level intent (no explicit parent) connects to the origin fact, so every
	// intent still traces back to a fact node — at task start the only fact is the
	// origin, and the first intents derive from it.
	if len(parents) == 0 {
		if origin, _ := t.ts.OriginFactID(); origin > 0 {
			_ = t.ts.Link(origin, db.RelDerivedFrom, id)
		}
	}
	return id, nil
}

func (t *ToolSet) addIntent() actool.CoreTool {
	return writeTool("add_intent", "生成【探索方向】写入 frontier，并连入探索链路。意图是开放的探索方向，不是固定类型——用 summary 一句话自由描述要探索/验证/利用什么。\n"+
		"★优先批量：一轮筛出的多个新方向放进 intents 数组一次提交（比逐条调用省往返）。返回 ids 数组，与 intents 等长同序（失败项 id=0，详情见 errors）。单条则省略 intents 直接给顶层 summary。",
		obj(map[string]any{
			"intents":    map[string]any{"type": "array", "description": "【优先用这个】要新增的探索方向数组，按顺序处理。每个元素字段同下方顶层字段（summary/asset_ids/parent_ids/priority）。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":    str("[单条] 一句话描述这个探索方向：做什么+为什么。已写清方向即可，不依赖资产 id。"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "本方向要测试/攻击的【目标资产 id】（**尽量传**，0/1/多个；是 list_assets 返回的资产 id，不是探索节点 id）：这条探索方向针对哪些资产（站点/接口/参数/主机等）。只要方向围绕某些具体资产就务必传上——它是「这条探索打哪些目标」的结构化标记，用于覆盖去重、把意图连入资产链路。仅当纯全局侦察、确实没有具体目标资产时才留空。"},
			"parent_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "上游锚点 id（可选，0/1/多个）：本方向由哪些【已确认的事实(fact)/发现(finding)】综合得出。**只能填已存在的 fact/finding 节点 id,不能填意图/目标/提示**——意图必须锚在已确认知识上,发现驱动而非凭空规划。多个事实共同产生一个新意图就传多个;顶层全新侦察方向请留空（会自动挂到任务起点 origin fact）。"},
			"priority":   intp("优先级 0-10，默认5"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Intents    []intentItem `json:"intents"`
				intentItem              // 单条模式：顶层 summary/asset_ids/parent_ids/priority
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Intents) > 0
			items := a.Intents
			if !batch {
				items = []intentItem{a.intentItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneIntent(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("intent created: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

func (t *ToolSet) listGoals() actool.CoreTool {
	return readTool("list_goals", "列出本任务的目标节点及其状态（open/met），用于判断是否达成。",
		obj(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			g, _ := t.ts.ListByKind(db.KindGoal, 100)
			return jsonResult(g)
		})
}

func (t *ToolSet) proveGoal() actool.CoreTool {
	return writeTool("prove_goal", "当你判断某个发现/事实证明了某个目标达成时调用：把证据节点连到目标节点，并标记目标 met。",
		obj(map[string]any{
			"goal_id":     idp("目标节点 id"),
			"evidence_id": idp("证明它的发现/事实节点 id"),
			"reason":      str("为什么这个证据满足该目标"),
		}, "goal_id", "evidence_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				GoalID     json.RawMessage `json:"goal_id"`
				EvidenceID json.RawMessage `json:"evidence_id"`
				Reason     string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			goal, ev := pid(a.GoalID), pid(a.EvidenceID)
			if goal == 0 || ev == 0 {
				return actool.Errorf("goal_id 和 evidence_id 必填"), nil
			}
			_ = t.ts.Link(ev, db.RelProves, goal)
			_ = t.ts.SetNodeState(goal, "met")
			// 每标记一个目标 met，就检查本任务是否【所有目标】都已 met；若是，自动判定
			// 任务完成（置 GoalMet），无需再依赖模型显式调 goal_met。
			if goals, err := t.ts.ListByKind(db.KindGoal, 1000); err == nil && len(goals) > 0 {
				allMet := true
				for _, g := range goals {
					if g.State != "met" {
						allMet = false
						break
					}
				}
				if allMet {
					t.GoalMet = true
					t.Reason = fmt.Sprintf("所有 %d 个目标均已 met（最后由 goal %d 触发）", len(goals), goal)
					return actool.Text(fmt.Sprintf("goal %d marked met；本任务所有目标均已达成，任务自动判定完成", goal)), nil
				}
			}
			return actool.Text(fmt.Sprintf("goal %d marked met", goal)), nil
		})
}

func (t *ToolSet) goalMet() actool.CoreTool {
	return writeTool("goal_met", "【立即结束整个任务】——仅当你确认任务的【全部目标都已真正达成、整体收官】时才调（注意是任务【整体】完成；仅仅达成了其中某一个目标/某一个 flag/某一个漏洞【不算】——那种情况用 prove_goal 标记该目标即可）。⚠️它不是用来“结束本轮规划”的：本轮没有新意图要派、或在等 worker 产出，都【直接结束本轮即可，不要调本工具】（0 个意图是完全正常的）。正常判定优先用 prove_goal 逐个证明目标；goal_met 只是绕过逐个证明、直接从全局收官的手段。",
		obj(map[string]any{"reason": str("达成理由（必须是目标真正达成的证据，不能是“本轮无新方向”这类结束本轮的理由）")}, "reason"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Reason string }
			_ = json.Unmarshal(in, &a)
			t.GoalMet = true
			t.Reason = a.Reason
			return actool.Text("acknowledged: goal marked met"), nil
		})
}

// --- worker write tools ---

func (t *ToolSet) addFinding() actool.CoreTool {
	return writeTool("report_finding", "发现漏洞时必须调用该工具!记录一个确认的漏洞发现。在任务上下文中 intent_id 必填（当前正在执行的意图 id）；在会话上下文中 intent_id 可不填。",
		obj(map[string]any{
			"vulnclass": str("漏洞类"),
			"severity":  str("high|medium|low"),
			"summary":   str("发现摘要"),
			"intent_id": idp("产生本发现的意图 id（任务上下文必填；会话上下文可不填）"),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "受影响资产 id（可选，0/1/多个）：参数/端点/站点等。一个漏洞影响多处可全填，纯观察可不填。"},
			"evidence":  str("证据/PoC 文本"),
		}, "vulnclass", "severity", "summary"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				VulnClass, Severity, Summary, Evidence string
				IntentID                               json.RawMessage   `json:"intent_id"`
				AssetIDs                               []json.RawMessage `json:"asset_ids"`
			}
			_ = json.Unmarshal(in, &a)
			payload := map[string]any{"vulnclass": a.VulnClass, "severity": a.Severity, "summary": a.Summary,
				"evidence": map[string]any{"by": t.worker, "poc": a.Evidence}}
			var anchors []int64
			for _, raw := range a.AssetIDs {
				if p := pid(raw); p > 0 {
					anchors = append(anchors, p)
				}
			}
			var id int64
			if t.ts != nil {
				var err error
				id, err = t.ts.AddNode(db.KindFinding, payload, 9, "confirmed", t.worker, anchors)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if intent := pid(a.IntentID); intent > 0 {
					_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> finding
				}
				_, _ = t.ts.AddStandaloneFinding(t.taskID, id, a.VulnClass, a.Severity, a.Summary, a.Evidence, t.worker, anchors)
			} else {
				// conversation context: no exploration store available, cannot record finding
				return actool.Errorf("report_finding 需要任务上下文（exploration store 未初始化）"), nil
			}
			t.writes.Findings++
			return actool.Text(fmt.Sprintf("finding recorded: %d", id)), nil
		})
}

// recordFact writes a general exploration RESULT/conclusion (not a vuln, not a
// new asset) into the EXPLORATION graph, chained to the intent that produced it.
// This is the home for observations and — importantly — negative results
// ("port closed", "param not injectable", "no login found"). Such conclusions
// must NOT be stuffed into the asset graph via upsert_asset.
// factItem 是 record_fact 批量/单条的一条事实。
type factItem struct {
	Summary    string            `json:"summary"`
	Detail     string            `json:"detail"`
	Evidence   string            `json:"evidence"`   // 一行关键证据（命令+关键输出行），支撑结论、便于事后核对
	Confidence string            `json:"confidence"` // observed（直接看到）| inferred（据现象推断）
	IntentID   json.RawMessage   `json:"intent_id"`
	AssetIDs   []json.RawMessage `json:"asset_ids"`
}

// recordOneFact 写一条 fact 节点并连到意图（intent→yields→fact）。defaultIntent 为
// 批量时的默认意图（本条未给 intent_id 时用）。
func (t *ToolSet) recordOneFact(it factItem, defaultIntent int64) (int64, error) {
	if strings.TrimSpace(it.Summary) == "" {
		return 0, fmt.Errorf("summary 不能为空")
	}
	payload := map[string]any{"summary": it.Summary}
	if it.Detail != "" {
		payload["detail"] = it.Detail
	}
	if e := strings.TrimSpace(it.Evidence); e != "" {
		payload["evidence"] = e
	}
	if c := strings.TrimSpace(it.Confidence); c != "" {
		payload["confidence"] = c
	}
	// a fact is its OWN node kind (distinct from a vuln finding).
	id, err := t.ts.AddNode(db.KindFact, payload, 5, "confirmed", t.worker, pidList(it.AssetIDs))
	if err != nil {
		return 0, err
	}
	intent := pid(it.IntentID)
	if intent <= 0 {
		intent = defaultIntent
	}
	if intent > 0 {
		_ = t.ts.Link(intent, db.RelYields, id) // chain: intent -> fact
	}
	t.writes.Facts++
	return id, nil
}

func (t *ToolSet) recordFact() actool.CoreTool {
	return writeTool("record_fact", "把探索【事实/结论】写入探索图，连到产生它的意图（intent_id）。用于记录探索结果——包括指纹/枚举等【正向结论】，和'端口关闭'/'参数不可注入'/'未发现登录入口'等【否定结论】。\n"+
		"⚠️一次探索的多个观察要【汇总成一条事实】，不要拆成多条，可以合并成一条事实的就尽量用一条事实表示：summary=对本次结论的总结性一句话，detail=相关细节（可含多个具体项）。例：指纹意图→一条事实 {summary:'识别了 X 站点的技术栈与响应特征', detail:'nginx 1.25 / Vue3 / 200 / title=.. / body_len=..'}，而不是状态码、指纹、标题各记一条。一条意图通常只产出一条事实，拆太碎会让图谱无限膨胀。\n"+
		"★facts 数组用于一次写多条【彼此不同】的结论（每条可省略 intent_id，默认用顶层 intent_id）。返回 ids 数组，与 facts 等长同序。\n"+
		"⚠️只写你在工具输出里【真实看到】的结论，不要脑补。evidence 与 confidence 用来防止不准确的结论污染图谱：\n"+
		"  · evidence=支撑本结论的【一行】关键证据（命令+最能证明的那一两行输出），**务必简洁**——细节已在 detail，这里不要再粘大段输出。\n"+
		"  · confidence=observed（输出里直接看到）| inferred（据现象推断）。\n"+
		"  · **否定结论**（不可注入/端口关闭/未发现入口等）尤其要给 evidence 并如实标 confidence——它会让规划者放弃这个方向，错的否定代价很大；只探了一次或证据弱，就标 inferred、别当铁案。",
		obj(map[string]any{
			"facts":      map[string]any{"type": "array", "description": "【有多条不同结论时用】事实数组，元素字段同下方顶层字段（summary/detail/evidence/confidence/intent_id/asset_ids）；省略 intent_id 则用顶层 intent_id。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"summary":    str("对本次探索结论的【总结性一句话】（是对 detail 的概括）"),
			"intent_id":  idp("产生本事实的意图 id（你领到的意图；批量时作为各条默认）"),
			"detail":     str("本事实的相关细节：把这次探索的多个观察事实都写进这里"),
			"evidence":   str("【一行】关键证据：命令 + 最能证明结论的那一两行输出。务必简洁，不要粘大段输出（细节放 detail）。"),
			"confidence": str("observed（输出里直接看到）| inferred（据现象推断）。否定结论务必如实标注。"),
			"asset_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "相关资产 id（可选，0/1/多个）：该事实涉及哪些资产"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Facts    []factItem `json:"facts"`
				factItem            // 单条模式 + 批量默认 intent_id
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Facts) > 0
			items := a.Facts
			if !batch {
				items = []factItem{a.factItem}
			}
			defaultIntent := pid(a.factItem.IntentID) // 顶层 intent_id = 批量默认

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.recordOneFact(it, defaultIntent)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("fact recorded: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

type hintItem struct {
	Text     string            `json:"text"`
	AssetIDs []json.RawMessage `json:"asset_ids"`
}

// addOneHint 挂一条 hint 节点(active/human)到探索图,可锚定资产,返回 id。
func (t *ToolSet) addOneHint(it hintItem) (int64, error) {
	if strings.TrimSpace(it.Text) == "" {
		return 0, fmt.Errorf("text 不能为空")
	}
	var anchors []int64
	for _, raw := range it.AssetIDs {
		if tid := pid(raw); tid > 0 {
			anchors = append(anchors, tid)
		}
	}
	id, err := t.ts.AddNode(db.KindHint, map[string]any{"text": it.Text}, 0, "active", "human", anchors)
	if err == nil && t.notify != nil {
		t.notify() // wake the planner so the new hint is read promptly (debounced)
	}
	return id, err
}

func (t *ToolSet) addHint() actool.CoreTool {
	return writeTool("add_hint", "把人类/主 agent 的战略提示挂到探索图，规划者下次生成意图时会读到它。\n"+
		"★优先批量：多条提示放进 hints 数组一次提交（比逐条调用省往返）。返回 ids 数组，与 hints 等长同序（失败项 id=0，详情见 errors）。单条则省略 hints 直接给顶层 text。",
		obj(map[string]any{
			"hints":     map[string]any{"type": "array", "description": "【优先用这个】要新增的提示数组，按顺序处理。每个元素字段同下方顶层字段（text/asset_ids）。返回 ids 与本数组等长、同序。", "items": map[string]any{"type": "object"}},
			"text":      str("[单条] 提示内容，如'重点挖认证后接口'"),
			"asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "锚定的资产 id（可选，0/1/多个）"},
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Hints    []hintItem `json:"hints"`
				hintItem            // 单条模式：顶层 text/asset_ids
			}
			_ = json.Unmarshal(in, &a)
			batch := len(a.Hints) > 0
			items := a.Hints
			if !batch {
				items = []hintItem{a.hintItem}
			}

			ids := make([]int64, len(items))
			errs := map[string]string{}
			for i, it := range items {
				id, err := t.addOneHint(it)
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				ids[i] = id
			}

			if !batch { // 单条：保持原返回
				if e, bad := errs["0"]; bad {
					return actool.Errorf(e), nil
				}
				return actool.Text(fmt.Sprintf("hint added: %d", ids[0])), nil
			}
			out := map[string]any{"ids": ids}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		})
}

// killWorkTool lets the planner terminate a single running work (by intent id).
func (t *ToolSet) killWorkTool() actool.CoreTool {
	return writeTool("kill_work", "终止一条正在运行的意图(work)。用于叫停跑偏/无意义的探索；被终止的意图标记为 stopped，不再自动重领。先用 get_worker_output 看看它在干嘛再决定。",
		obj(map[string]any{"intent_id": idp("要终止的意图 id（= work 句柄）")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.killWork == nil {
				return actool.Errorf("kill_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			if err := t.killWork(id); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 发送终止信号", id)), nil
		})
}

// steerWorkTool lets the planner inject a mid-run course-correction into a running
// work WITHOUT killing it: the message reaches the worker before its next tool call,
// which re-plans its next step (already-gathered context is kept). For in-intent
// nudges ("停做 X、聚焦 Y"); if the whole direction is wrong use kill_work + a new intent.
func (t *ToolSet) steerWorkTool() actool.CoreTool {
	return writeTool("steer_work", "给一条正在运行的意图(work)实时注入纠偏指令，不打断它、不丢已有进展：worker 会在下一步动作前收到你的指令并据此调整。用于'别再走 X、聚焦 Y'这类【意图内】纠偏；若方向整个错了应改用 kill_work 再下新意图。建议先用 get_worker_output 看它在干嘛。",
		obj(map[string]any{
			"intent_id": idp("要纠偏的意图 id（= work 句柄）"),
			"message":   str("给 worker 的纠偏指令，明确让它停止什么、转向什么"),
		}, "intent_id", "message"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.steerWork == nil {
				return actool.Errorf("steer_work 当前不可用"), nil
			}
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Message  string          `json:"message"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			if strings.TrimSpace(a.Message) == "" {
				return actool.Errorf("message 必填"), nil
			}
			if err := t.steerWork(id, a.Message); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已向意图 %d 的 work 注入纠偏指令（下一步生效）", id)), nil
		})
}

// getWorkerOutput returns a work's final (or截至中止时的) conclusion text by intent id.
func (t *ToolSet) getWorkerOutput() actool.CoreTool {
	return readTool("get_worker_output", "取某条意图(work)的最终输出结论。正常结束返回其总结；被终止(stopped)/异常的 work 返回其截至中止时的最后输出(terminated=true)。",
		obj(map[string]any{"intent_id": idp("意图 id（= work 句柄）")}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			acts, _, err := t.ts.ActivityList(&id, 0, 1000)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var chosen, fallback *db.Activity
			for i := range acts {
				switch acts[i].Kind {
				case "result":
					chosen = &acts[i]
					fallback = &acts[i]
				case "text":
					fallback = &acts[i]
				}
			}
			pick, terminated := chosen, false
			if pick == nil {
				pick, terminated = fallback, true
			}
			if pick == nil {
				return actool.Text("（该 work 尚无任何输出）"), nil
			}
			detail, _ := t.ts.ActivityDetail(pick.ID)
			if detail == "" {
				detail = pick.Summary
			}
			return jsonResult(map[string]any{
				"intent_id": id, "worker_name": pick.Worker, "final_text": detail,
				"summary": pick.Summary, "is_error": pick.IsError, "terminated": terminated,
			})
		})
}

// traceSteps renders summary-only trace rows, re-truncating each summary to 100
// chars — the stored summary is capped at 200 for the UI transcript; the trace
// tools want it tighter since a whole work's step list is many rows.
func traceSteps(acts []db.Activity) []map[string]any {
	steps := make([]map[string]any, 0, len(acts))
	for i := range acts {
		steps = append(steps, map[string]any{
			"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
			"is_error": acts[i].IsError, "summary": firstLine(acts[i].Summary, 100),
		})
	}
	return steps
}

// getWorkerTrace exposes a work's execution PROCESS (not just its final output):
// list step summaries, keyword-search within one work, or pull full detail of a
// few specific steps. Thinking steps are excluded everywhere.
func (t *ToolSet) getWorkerTrace() actool.CoreTool {
	return readTool("get_worker_trace",
		"查看某条意图(work)的【执行过程】（区别于 get_worker_output 只给最终结论）。三种用法：\n"+
			"① 只传 intent_id → 返回该 work 每一步的摘要流（summary≤100字，含 step_id；只是动作轮廓，不含完整输出）；\n"+
			"② intent_id + q → 只返回命中关键字的步骤摘要（在摘要和完整输出里都搜；仍只给 summary，要看内容用③）；\n"+
			"③ intent_id + step_ids（最多5个）→ 返回这些步骤的完整内容(detail)。\n"+
			"典型流程：先①/②定位可疑步骤的 step_id，再用③取其完整输出。不含思考(thinking)步骤。",
		obj(map[string]any{
			"intent_id": idp("意图 id（= work 句柄）"),
			"q":         str("关键字：只返回摘要/完整输出命中它的步骤（可选；与 step_ids 互斥）"),
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "要取完整内容的 step_id（来自①/②返回，一次最多 5 个）"},
			"limit":     intp("摘要流/检索的返回上限（可选）"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage   `json:"intent_id"`
				Q        string            `json:"q"`
				StepIDs  []json.RawMessage `json:"step_ids"`
				Limit    int               `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			// ③ detail drill-down by step ids (max 5), thinking excluded by the store.
			if len(a.StepIDs) > 0 {
				var ids []int64
				for _, raw := range a.StepIDs {
					if v := pid(raw); v > 0 {
						ids = append(ids, v)
					}
				}
				if len(ids) > 5 {
					return actool.Errorf("step_ids 一次最多 5 个（先用不带 step_ids 的调用定位，再分批取）"), nil
				}
				acts, err := t.ts.ActivityByIDs(ids)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				steps := make([]map[string]any, 0, len(acts))
				for i := range acts {
					steps = append(steps, map[string]any{
						"step_id": acts[i].ID, "kind": acts[i].Kind, "tool": acts[i].Tool,
						"is_error": acts[i].IsError, "detail": acts[i].Detail,
					})
				}
				return jsonResult(map[string]any{"intent_id": id, "steps": steps})
			}
			// ①/② summary stream, optionally keyword-filtered; 100-char summaries.
			var acts []db.Activity
			var err error
			if strings.TrimSpace(a.Q) != "" {
				acts, err = t.ts.ActivityTraceSearch(&id, a.Q, a.Limit)
			} else {
				acts, err = t.ts.ActivityTrace(id, a.Limit)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"intent_id": id, "steps": traceSteps(acts)})
		})
}

// searchAllWorkerTraces keyword-searches EVERY work's process in this task — for
// finding what a worker saw but never wrote back as a fact. Returns only matching
// summaries (≤100 chars), each tagged with its intent_id for follow-up drill-down.
func (t *ToolSet) searchAllWorkerTraces() actool.CoreTool {
	return readTool("search_all_worker_traces",
		"在【本任务所有 work 的执行过程】里按关键字(q)检索——用于找回某个 worker 见过、却没写进 fact 的东西（某路径/token/报错等）。"+
			"只返回命中步骤的摘要(summary≤100字)，每条带 intent_id；据此再用 get_worker_trace(intent_id, step_ids=[...]) 取完整内容。不含思考(thinking)步骤。",
		obj(map[string]any{
			"q":     str("关键字（在所有 work 步骤的摘要+完整输出里搜）"),
			"limit": intp("返回上限，默认 100（可选）"),
		}, "q"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Q) == "" {
				return actool.Errorf("q 必填"), nil
			}
			acts, err := t.ts.ActivityTraceSearch(nil, a.Q, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			hits := make([]map[string]any, 0, len(acts))
			for i := range acts {
				var intent int64
				if acts[i].NodeID != nil {
					intent = *acts[i].NodeID
				}
				hits = append(hits, map[string]any{
					"intent_id": intent, "step_id": acts[i].ID, "worker": acts[i].Worker,
					"kind": acts[i].Kind, "tool": acts[i].Tool, "is_error": acts[i].IsError,
					"summary": firstLine(acts[i].Summary, 100),
				})
			}
			return jsonResult(map[string]any{"query": a.Q, "hits": hits})
		})
}

// listWorkerTraces gives a worker (which has no graph_overview and can't see the
// intent graph) a lightweight index of the works in this task — intent_id +
// one-line summary + state — so it can DISCOVER which works to inspect via
// get_worker_trace. Without this a worker only knows intent_ids that come back
// from search_all_worker_traces hits. Excludes still-open intents (not yet run →
// no process to inspect).
func (t *ToolSet) listWorkerTraces() actool.CoreTool {
	return readTool("list_worker_traces",
		"列出本任务里【已跑过的 work（意图）】索引：intent_id + 一句话方向(summary) + 状态。"+
			"你(worker)看不到探索图，用它来发现有哪些 work 值得翻看——再用 get_worker_trace(intent_id) 看其步骤、get_worker_trace(intent_id, step_ids=[...]) 取详情。"+
			"只列已执行的(running/done/exhausted/blocked/stopped)，不含还没跑的 open。注意：你的任务边界仍是你领到的那条意图，看别的 work 只为复用观察/避免重复劳动。",
		obj(map[string]any{
			"q":     str("按 summary 关键字过滤（可选）"),
			"limit": intp("返回上限，默认 50（可选）"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Q     string `json:"q"`
				Limit int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			limit := a.Limit
			if limit <= 0 {
				limit = 50
			}
			all, err := t.ts.ListByKind(db.KindIntent, 500)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Q))
			out := make([]map[string]any, 0, limit)
			for _, n := range all {
				switch n.State {
				case "running", "done", "exhausted", "blocked", "stopped": // has run → has a process
				default:
					continue
				}
				var p map[string]any
				_ = json.Unmarshal(n.Payload, &p)
				summary, _ := p["summary"].(string)
				if q != "" && !strings.Contains(strings.ToLower(summary), q) {
					continue
				}
				out = append(out, map[string]any{"intent_id": n.ID, "summary": summary, "state": n.State})
				if len(out) >= limit {
					break
				}
			}
			return jsonResult(map[string]any{"works": out})
		})
}

// PlannerTools is the read + intent-generation + goal-judgement tool set.
func (t *ToolSet) PlannerTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.listGoals(), t.addIntent(), t.proveGoal(), t.goalMet(),
		t.killWorkTool(), t.steerWorkTool(),
		// report_finding：规划态势研判时若自身已确证漏洞，可直接登记（与 worker 同工具）。
		t.addFinding(),
		// list_companies：查看企业列表 + scope + 资产数（拿 company_id / 理解归属范围）。
		t.listCompanies(),
	}
}
