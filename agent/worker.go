package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/memory"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/Autumn-27/norma/transcript"
)

// Worker is an LLM work agent (docs §4.4): it claims ONE intent, completes it
// with real tools (Bash: kali tooling through the recording proxy), writes the
// FACTS it found back into the graph, and stops. It does NOT generate new
// directions (that is the planner's job) and does NOT keep exploring toward the
// goal on its own. Multiple workers run concurrently as goroutines.
// WebSearchOpts is the web-search backend selection the server pushes into each
// agent (planner/worker/main). Enabled=false leaves the web_search tool off.
// Backend is "ddgs" (no key), "brave-free" (BraveKey required), or "tavily"
// (TavilyKey required). It maps directly onto agentcore.Options.
// Proxy is a dedicated egress proxy for the search request (http/https/socks5),
// independent of the traffic-recording MITM proxy — set it when the search endpoint
// is only reachable via a VPN/SOCKS proxy. Empty = direct.
type WebSearchOpts struct {
	Enabled   bool
	Backend   string
	BraveKey  string
	TavilyKey string
	Proxy     string
}

type Worker struct {
	prov        llm.Provider
	model       string
	workDir     string
	proxyAddr   string
	proxyCACert string            // recording proxy's CA cert path (for WebFetch HTTPS verify)
	webSearch   WebSearchOpts     // web_search tool backend selection (off by default)
	mem         *memory.Store     // cross-engagement tradecraft memory (G4)
	tx          *transcript.Store // raw LLM conversation persistence (nil = off)
	window      int               // context window in tokens (for compaction)
	maxTurns    int               // max agent turns per run (0 = unlimited)
	// runTimeout is the wall-clock budget for the main exploration of one intent
	// (0 = unlimited). When it fires, the run is cut and a settlement round is
	// forced so already-identified facts get written back instead of being lost.
	runTimeout time.Duration
	// extraTools are host-provided tools (e.g. traffic query, oast) appended to
	// the worker's graph write-back tools.
	extraTools []actool.CoreTool
}

// SetRunTimeout configures the per-intent wall-clock budget for the main
// exploration (0 = unlimited). When it fires, the SDK settlement phase still runs
// so facts are never lost to a timeout. Safe to call before Execute.
func (w *Worker) SetRunTimeout(run time.Duration) {
	w.runTimeout = run
}

// settleHardGrace is how far past the soft wall-clock budget (runTimeout) the hard
// ctx deadline sits — a backstop for a hung turn; the soft budget handles the
// normal case at the turn boundary.
const settleHardGrace = 90 * time.Second

// settleWrapUpPrompt is injected by the SDK settlement phase when a worker hits its
// turn/time budget: stop probing, write back what was found, then end with a
// plain-text one-liner (which becomes this run's displayed result).
const settleWrapUpPrompt = "你即将因预算耗尽被终止。不要再运行任何命令/探测。请依次：(1) 把你上面已识别但还没写回的内容逐条写回——新资产用 insert_assets、探索结论/事实用 record_fact、确认漏洞用 report_finding；(2) **最后单独用一句话纯文本**总结你做了什么、得到哪些关键结论（这句会作为本次运行的结果展示，务必输出）。"

// SetMemory enables cross-engagement tradecraft memory (RecallMemory/RecordMemory
// tools + auto-injection of relevant memories, docs §8 G4).
func (w *Worker) SetMemory(m *memory.Store) { w.mem = m }

func NewWorker(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int, extra ...actool.CoreTool) *Worker {
	return &Worker{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, extraTools: extra}
}

// SetProxy configures the recording proxy address that workers route target
// traffic through, plus the CA cert path WebFetch trusts to verify HTTPS through
// that MITM proxy. Empty addr disables the hint.
func (w *Worker) SetProxy(addr, caCert string) { w.proxyAddr, w.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for this worker (off by default).
func (w *Worker) SetWebSearch(o WebSearchOpts) { w.webSearch = o }

// proxyEnv builds the Bash-subprocess env that routes child-command HTTP through
// the recording proxy and makes the common toolchain trust its MITM CA — so tools
// need no manual -x/--proxy/-k. Each ecosystem reads a different CA var (verified
// empirically): SSL_CERT_FILE→curl/urllib/Go/openssl, REQUESTS_CA_BUNDLE→python
// requests (it ignores SSL_CERT_FILE), CURL_CA_BUNDLE→curl, GIT_SSL_CAINFO→git,
// NODE_EXTRA_CA_CERTS→node; NODE_USE_ENV_PROXY makes Node 24+ honor the proxy vars.
// Empty proxyAddr → nil (direct, unchanged env).
func proxyEnv(proxyAddr, caCert string) []string {
	if proxyAddr == "" {
		return nil
	}
	env := []string{
		"HTTP_PROXY=" + proxyAddr, "HTTPS_PROXY=" + proxyAddr,
		"http_proxy=" + proxyAddr, "https_proxy=" + proxyAddr,
		"NODE_USE_ENV_PROXY=1", // Node 24+: honor HTTP(S)_PROXY in built-in fetch/http
	}
	if caCert != "" {
		env = append(env,
			"SSL_CERT_FILE="+caCert,
			"CURL_CA_BUNDLE="+caCert,
			"REQUESTS_CA_BUNDLE="+caCert,
			"GIT_SSL_CAINFO="+caCert,
			"NODE_EXTRA_CA_CERTS="+caCert,
		)
	}
	return env
}

// workerDefaultTmpl is the built-in EDITABLE body (段 [A]) of the worker system
// prompt, seeded into agent_prompts. The trafficTool block and the 中间产物输出规约
// are NOT here — they are code-owned and appended by workerSystem after rendering
// (段 [B]/[C]), so editing the DB body can never drop them.
const workerDefaultTmpl = `你是一个授权渗透测试系统的"执行者"(work agent)。你领到【一条意图】(一句话探索方向)，唯一职责是：**完成这一条意图、把发现的事实写回知识图谱，然后停止返回。**

铁律（务必遵守）：
1. **只做这一条意图**。意图边界就是你的红线。指纹意图就只做指纹识别，不要顺手去枚举端点、爆破目录、扒 JS 找 API、测漏洞——那些是【别的意图】的事，由规划者去派别的 worker。
2. **你不负责"探索方向"**。发现了值得继续追的新线索，不要自己接着打；只要把它写回图，规划者会读到、自己生成新意图。生成探索方向是规划者的职责，不是你的。
3. **穷尽后再返回，别在第一个障碍前放弃**。判"意图达成"的标准是【你已把这条方向真正探透】：初次尝试被拦（一个 payload 被过滤、一个端点 404、一个注入点没回显）不等于此路不通——先换编码/换方法/换参数/换路径把这条意图的合理手段走完，再下结论。**但边界不变**：穷尽的只是【这一条意图内部】的手段，绝不是顺手去做别的意图（枚举别的端点、测别的漏洞）；那些仍是规划者派别的 worker 的事。真正探透了、或确认此路不通了，就立即写回并返回，别因为"任务总目标还没达成"就继续，也别为凑步数在已探尽的方向空转。
4. **边发现边写回，并且写对地方**。每得出一个结果就立刻写回（别攒到最后，否则步数耗尽全丢）。结果只算写进图里的，活在你脑子/文字里的不算。**两张图分清楚**：
   - **发现新资产/资源** → insert_assets 写【资产图】（登记资产本身：endpoint / parameter / tech 指纹 / service / 凭据 / 子域 等）。资产的结构化属性写在它自己身上：站点/接口的状态码/标题/body 长度/content_type 放 props.http，技术栈登记为 type=tech 节点。多个资产用 insert_assets 的 assets 数组一次批量登记。
   - **得出探索结论/事实**（含指纹/枚举等正向结论，和"端口关闭"、"该参数不可注入"、"未发现登录入口"等否定结论）→ record_fact 写【探索图】（传 intent_id）。**一次探索的多个观察汇总成【一条】事实**：summary 写总结性一句话，detail 写相关细节（技术栈、状态码、响应特征等都塞进这一条的 detail）——**不要一个属性一条事实**，一条意图通常只产出一条事实，拆太碎会让图谱无限膨胀。真有多条【彼此不同】的结论才用 facts 数组一次写。【新增】**只写增量**：写回前先扫一眼上方【全局探索态势】里的 recent_facts——只写你这次【新得到】的结论，别把图里已有的事实换个措辞再记一遍（重复事实会让图谱膨胀、误导规划者以为有新进展）。若你的观察只是印证了已有 fact 而无新增，就不必再记一条。
   - **确认漏洞** → report_finding 写【探索图】（含 PoC，传 intent_id）。

可用工具：
- insert_assets：登记新资产（资产图）。**你只传原始信息，key 与父子关联由代码算**：新接口→传完整 url+method（代码自动建 domain→site→endpoint、自动抽 URL 里的 query 参数，body/header 参数放 params）；指纹→type=tech,name=技术名,on_url=站点地址,props填{version,category}。多个资产放 assets 数组一次批量登记。属性写在资产自己的 props 上，探索结论不要写这里。
- record_fact：把探索【事实/结论】写入探索图并连到意图（传 intent_id）。正向/否定结论、观察、判断用它；**一次探索的多个观察汇总成一条事实**（summary 总结一句话 + detail 放细节），不要一个属性一条。真有多条不同结论才用 facts 数组。**只写你真实看到的**：给 evidence（一行关键证据：命令+关键输出，简洁，别粘大段——细节在 detail）、标 confidence（observed 直接看到 / inferred 推断）；**否定结论**（不可注入/端口关闭等）尤其要给证据、证据弱就标 inferred，别让错的否定误导规划者放弃方向。
- report_finding：确认漏洞 → 记录(含 PoC，传 intent_id=你领到的意图id)。**只有你在本次运行里真实触发过该漏洞、拿到了可复现的证据（请求/响应或命令输出）才用它。** 严禁把下列当作已确认漏洞上报：仅凭版本号/指纹匹配到某 CVE、仅凭"参数看起来可注入"、仅凭外部漏洞库/更新日志/代码 diff 推断。**不要用查 CVE 库或"对比补丁版本"替代实际触发。** 触发不了但确有嫌疑，就用 record_fact 记一条 confidence=inferred 的事实（描述嫌疑点+为何未能触发），交给规划者派后续意图，别硬记成 finding。
- list_assets（查询资产，非探索节点） / asset_neighbors / list_facts(探索事实) / list_findings(漏洞) / node_detail(探索节点 id，非资产 id)：按需查上下文。
- 【必要时才会使用，大多数上下文都在本次会话中】search_all_worker_traces / list_worker_traces / get_worker_trace：**跨 work 复用信息**（别的 work 执行过程里出现过、却没写进 fact 的东西）。你看不到探索图，但可以：search_all_worker_traces(q) 按关键字搜全部 work 的过程（返回带 intent_id）；list_worker_traces 看有哪些 work 跑过；get_worker_trace(intent_id) 看某 work 的步骤摘要、get_worker_trace(intent_id, step_ids=[…]) 取那几步完整内容（一次≤5个）。**仅用于复用他人观察、避免重复劳动——不改变你的任务边界（仍只做你这条意图）。**

只在授权范围内操作。完成本意图后用一句话总结你做了什么、写回了哪些事实。务实、克制、聚焦这一条意图。`

// workerTrafficBlock is 段 [B]: the traffic-tool note, code-injected only when
// traffic capture is on (proxyAddr set). Not stored, not editable.
func workerTrafficBlock(proxyAddr string) string {
	if proxyAddr == "" {
		return ""
	}
	return "\n\n**流量工具**：\n- traffic_search / traffic_get：目标流量已被记录代理全量落库（本地文件树 data/traffic/<host>/<访问路径>/）。回看响应、找已访问过的资源，**先查流量、不要重复 curl 同一 URL**。traffic_search **必须指定 host**、默认只回 3 条极轻量索引(id/method/url/status/resp_len，无响应内容)，需要更多显式调大 limit；要看某条原文用 traffic_get(id)。"
}

// artifactSpec is 段 [C]: the code-owned, non-editable tail appended to every
// pentest agent's prompt — intermediate artifacts must land in the shared work
// dir, never /tmp. Guaranteed present regardless of how the DB body is edited.
func artifactSpec(workDir string) string {
	return "\n\n**中间产物输出规约**：脚本、payload、抓到的响应体、临时数据等一切中间产物，**一律写到共享工作目录 " + workDir + "**（这是所有 agent 共用的 CWD，相对路径即写在这里，也可用该绝对路径）——**不要写 /tmp、不要用其它绝对路径**。"
}

// workerArtifactSubdir is the worker-only addendum to artifactSpec: put a run's
// artifacts under an i<intentID>/ subdir to avoid concurrent name collisions.
const workerArtifactSubdir = "为避免与其他 work 撞名，把本次产物放到子目录 i<意图id>/ 下（如 i123/exploit.py）。"

func workerSystem(proxyAddr, workDir string) string {
	body := renderSystem("worker", workerDefaultTmpl, WorkerVars{ProxyAddr: proxyAddr})
	return body + workerTrafficBlock(proxyAddr) + artifactSpec(workDir) + workerArtifactSubdir
}

// renderIntentTask formats the claimed intent for the worker's SYSTEM prompt: the
// intent is the worker's whole job, so — like the planner's situational block — it
// belongs in system, where compaction can never drop it during a long run.
// intentAssetIDs pulls the intent's target asset ids out of its payload
// (planner's add_intent stores them as a numeric asset_ids array). nil on absence
// or malformed payload.
func intentAssetIDs(intent *db.Node) []int64 {
	if intent == nil {
		return nil
	}
	var p struct {
		AssetIDs []int64 `json:"asset_ids"`
	}
	if err := json.Unmarshal(intent.Payload, &p); err != nil {
		return nil
	}
	return p.AssetIDs
}

func renderIntentTask(intent *db.Node) string {
	return fmt.Sprintf("\n\n【你领到的意图（本次唯一任务：只做这一条、只产生事实、做完即停）】：\n%s\n意图 id: %d（写回 record_fact / report_finding 时传它）", string(intent.Payload), intent.ID)
}

// renderWorkerGraphOverview folds the global situational snapshot into the worker's
// SYSTEM prompt for AWARENESS ONLY. The framing is deliberately strong: the overview
// must NOT widen the worker's job — it still does only its assigned intent. Its sole
// purpose is letting the worker read context (existing facts/assets/hints)
// so it avoids redundant work and doesn't re-derive what others already found.
func renderWorkerGraphOverview(data map[string]any) string {
	// coverage 是给规划者判断「哪类测得少 / 要不要扩范围」的信号，与 worker「只做领到的
	// 那条意图、别追未覆盖的点」的职责边界相悖 → 从 worker 视图里剔除。data 是本次 worker
	// 专属的新 map，删键不影响 planner。
	delete(data, "coverage")
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back silently: the worker just won't have the global context
	}
	return "\n\n【全局探索态势（仅供你了解大局，不是你的任务清单）】：\n" +
		"下面是整个任务当前的探索概况。给你的**唯一目的**是让你了解全局动态、复用已有结论、避免重复别人已做过的事。\n" +
		"**它绝不扩大你的职责边界**：你仍然只做上面领到的那一条意图。看到这里有别的 open 意图 / 未覆盖的点 / 其它可打方向，也**绝不要自己去动手**——那些是别的 worker 的事，由规划者调度。你若发现相关新线索，最多写进 fact 让规划者知道，不要自己追。\n" +
		string(b)
}

// Execute runs one intent. hooks (the per-task Guard) gates every tool call; may
// be nil. emit, if non-nil, receives one ActivityRecord per execution step.
// Returns the terminal reason (so the engine can distinguish completed vs
// max_turns) and a per-kind breakdown of what was written back (so an intent that
// explored but persisted nothing isn't mistaken for done, and the engine can log
// facts/assets/findings separately instead of lumping them under "facts").
func (w *Worker) Execute(ctx context.Context, name string, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, intent *db.Node, hooks harness.HookRunner, emit func(db.Activity), enr EnrichTrigger) (harness.TerminalReason, WriteCounts, error) {
	tsx := NewToolSet(ts, name)
	tsx.SetTaskID(taskID)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetOwnerNode(intent.ID) // assets this worker discovers anchor to its intent → visible to the task
	tsx.SetEnrich(enr)          // async DNS/HTTP auto-completion for assets this worker writes
	// base = built-in worker tools ∪ host tools (traffic) ∪ default tools (incl. Bash);
	// then augment with the agent's visible skills/MCP. During the SDK settlement
	// phase, Bash is hidden via Settlement.DisabledTools (no local gating needed).
	base := append(tsx.WorkerTools(), w.extraTools...)
	base = append(base, actool.DefaultTools()...)
	tools, def, cleanup := AugmentTools(ctx, "worker", base)
	defer cleanup()

	// 意图是 worker 的全部任务，放进 system prompt（抗 compaction、整轮常驻），而不是
	// user 输入——与 planner 把关键态势放 system 一致。落在指令之后、deferred 块之前。
	// 全局态势(graphOverviewData)也带上，但仅供了解大局；意图放在最后、最醒目的位置。
	overview := renderWorkerGraphOverview(tsx.graphOverviewData())
	system, boundary := deferredSystem(workerSystem(w.proxyAddr, w.workDir)+overview+renderIntentTask(intent), def)
	// 任务级 deadline(经 ctx 注入)夹逼本 run 的墙钟预算 + 决定收尾词(见 taskclock.go)。
	tc := taskClockFrom(ctx)
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, w.runTimeout)
	settle := wrapupSettlement("worker", []string{"Bash"})
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("worker", []string{"Bash"}, clamped)
	}
	opts := agentcore.Options{
		Provider:        w.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		// WebFetch 走记录代理，其 HTTP 与 curl 一样被留痕；载入代理 CA 让经 MITM
		// 重签的 HTTPS 证书能【正常校验通过】（而非关掉校验）。proxy 空则直连。
		EnableWebFetch: true,
		WebFetchProxy:  w.proxyAddr,
		WebFetchCACert: w.proxyCACert,
		// 联网搜索(可选)。ddgs 无需 key；brave-free 需 BraveKey；tavily 需 TavilyKey。
		// WebSearchProxy 是独立的出口代理(http/https/socks5)，与记录流量的 MITM 代理无关；空则直连。
		EnableWebSearch:    w.webSearch.Enabled,
		WebSearchBackend:   w.webSearch.Backend,
		BraveSearchAPIKey:  w.webSearch.BraveKey,
		TavilySearchAPIKey: w.webSearch.TavilyKey,
		WebSearchProxy:     w.webSearch.Proxy,
		// Bash 子命令的 HTTP 默认走记录代理 + 信任其 CA（工具无需 -x/-k）。
		BashEnv:    proxyEnv(w.proxyAddr, w.proxyCACert),
		WorkingDir: w.workDir,
		MaxTurns:   w.maxTurns, // 0 = unlimited (configurable in agent management)
		// 墙钟预算,轮边界判,不打断半路;0 = 不限。有任务级 deadline 时夹逼到 min(自身预算,
		// 距 deadline 剩余),让本 run 在任务到点时自然进收尾(见 taskclock.go)。
		MaxDuration: maxDur,
		// 命中预算(轮次 OR 时长)→ SDK 跑一轮收尾(隐藏 Bash),把已识别的写回,避免烂尾。
		// clamped(被任务 deadline 夹逼)时用 PromptByReason:因超时=任务到点→任务超时词,
		// 因步数=夹逼窗口内步数先耗尽→回落 per-run 词。非 clamped 维持纯 per-run。
		Settlement: settle,
		// large tool output spills to cmd-output/ with a head + pointer (SDK tool.Capture);
		// full output preserved on disk. 截断上限用 SDK 默认(30000 字符)。
		ToolOutputDir: filepath.Join(w.workDir, "cmd-output"),
		Compaction:    compactionConfig(w.window), // long tool-heavy runs stay within the window
		Todos:         actool.NewTodoStore(),      // 会话级临时待办（TodoWrite），纯规划用，退出即丢
	}
	if hooks != nil { // typed-nil guard: only set when concrete (avoids harness panic)
		opts.Hooks = hooks
	}
	if w.mem != nil {
		opts.Memory = &agentcore.MemoryOptions{Store: w.mem, AutoInject: true, MaxInject: 3}
	}
	if w.tx != nil { // persist raw LLM conversation; one file per worked intent
		opts.Transcript = w.tx
		opts.SessionID = fmt.Sprintf("exp%d-worker-i%d", ts.ID(), intent.ID)
	}
	intentID := intent.ID
	emitWrap := func(r db.Activity) {
		if emit != nil {
			r.NodeID, r.Worker = &intentID, name
			emit(r)
		}
	}
	// 意图已在 system 里。user 输入只放启动指令 + 意图锚定资产的原始数据（供 worker 直接
	// 用，省去开场再查一次 list_assets）。原始 JSON 直接附上，不做提取/格式化。
	input := "开始执行 system 提示里的这条意图：只做它、只产生事实、做完即停。"
	if as != nil {
		if ids := intentAssetIDs(intent); len(ids) > 0 {
			if assets, err := as.GetByIDs(ids); err == nil && len(assets) > 0 {
				if b, err := json.Marshal(assets); err == nil {
					input += "\n\n本意图 asset_ids 对应的目标资产：\n" + string(b)
				}
				// 意图明确针对的这些资产 → 自动纳入任务测试范围（与 insertAssets 同一套
				// 保守粒度）。upsertTaskScope 的 ON CONFLICT DO NOTHING + uq_task_scope
				// 唯一索引保证不会重复添加；重跑/重试同样是幂等 no-op。
				for _, a := range assets {
					_ = as.AddAutoScope(taskID, a.Type, a.Domain, a.URL, a.IP)
				}
			}
		}
	}

	s := agentcore.NewSession(opts)
	defer s.Close() // release the session's background-task manager (temp dir + processes)

	// Budgets + settlement are owned by the SDK (MaxTurns/MaxDuration + Settlement):
	// on hit it runs a wrap-up turn and finishes with ReasonMaxTurns/ReasonTimeout.
	// We only add a HARD ctx backstop past the soft wall-clock budget, for the rare
	// case a single turn hangs so the turn-boundary check never runs — the soft
	// budget handles the normal case gracefully (no mid-stream abort). pause /
	// planner kill cancel ctx; the engine distinguishes those and re-queues/stops.
	// backstop uses the EFFECTIVE (deadline-clamped) budget, not the raw runTimeout,
	// so a task-timeout run's hard deadline sits just past its clamped soft budget.
	runCtx := ctx
	if maxDur > 0 {
		var runCancel context.CancelFunc
		runCtx, runCancel = context.WithTimeout(ctx, maxDur+settleHardGrace)
		defer runCancel()
	}
	_, reason, err := captureRunSession(runCtx, s, input, emitWrap)
	return reason, tsx.Writes(), err
}
