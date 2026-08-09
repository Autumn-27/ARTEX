package server

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// model_error（provider/API 故障：LLM 层瞬时重试耗尽，或流已开始后中途断流）
// 收场的 work 不是「试过没做完」也不是真失败，而是外部抖动。默认把它当永久
// blocked 会白丢一条意图，所以这里对该终态额外重跑几次，每次之间退避一下，给
// provider 恢复的时间；重试期间若被暂停/终止/取消则立即让位给对应分支处理。
const (
	modelErrorRetries      = 2               // model_error 收场后额外重试的次数
	modelErrorRetryBackoff = 3 * time.Second // 每次重试前的退避
)

// Engine drives the event-driven exploration loop with real LLM agents
// (docs §4.3/§4.4): on asset/exploration-graph change (debounced) it wakes the
// planner, which reads the route, queries assets, judges goals and emits intents;
// N concurrent work agents claim intents and execute them. There is no
// simulation mode — an LLM provider is required. The planner/worker can be
// (re)installed at runtime (LLM configured from the UI); the loops always run
// but idle until an LLM is set.
type Engine struct {
	m        *Manager
	debounce time.Duration

	bc *Broadcaster // live activity pub/sub (SSE)

	mu      sync.RWMutex
	planner *agent.Planner
	worker  *agent.Worker
	started sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	paused  sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot.
	execMu     sync.Mutex
	execCancel map[string]context.CancelFunc
	execCtx    map[string]context.Context

	// per-work (per running intent) cancel, so the planner's kill_work can stop a
	// single worker without pausing the whole task. Keyed by globally-unique intent id.
	workMu     sync.Mutex
	workCancel map[int64]context.CancelFunc

	// steerBox queues planner course-corrections for a running work (keyed by intent
	// id). The worker's PreToolUse hook drains it before its next tool call and hands
	// the message to the model (blocking that call) so it re-plans — no kill needed.
	steerMu  sync.Mutex
	steerBox map[int64][]string

	plannerRound sync.Map // taskID -> int, planner round counter (for UI round separators)

	// 任务级超时(见 docs/任务级超时与收尾设计.md):
	settling     sync.Map // taskID -> bool, 任务已进入收尾时序(停止派/领新意图)
	deadline     sync.Map // taskID -> int64 unix, 绝对截止时刻(首次运行时盖章;0/缺省=不限)
	stamped      sync.Map // taskID -> bool, first_run_at 是否已盖章(本进程内只盖一次)
	inflight     sync.Map // taskID -> *int64, 在跑的 planner.Plan + worker.Execute 计数(用于 drain)
	coordStarted sync.Map // taskID -> bool, deadline 协调器是否已启动(Run/reload 去重)

	// resolve, if set, returns a task's dedicated planner/worker when it pins a
	// specific LLM profile (nil,nil = use the global active pair). Set once at startup.
	resolve func(t *Task) (*agent.Planner, *agent.Worker)
}

// nextPlannerRound returns the next planner round number for a task (1-based).
func (e *Engine) nextPlannerRound(taskID string) int {
	v, _ := e.plannerRound.LoadOrStore(taskID, 0)
	n := v.(int) + 1
	e.plannerRound.Store(taskID, n)
	return n
}

// Pause stops a task: marks it paused AND cancels any in-flight planner/worker run
// for it (a long worker.Execute would otherwise keep going until it finishes).
func (e *Engine) Pause(taskID string) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID)
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel()
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	e.paused.Delete(t.ID)
	t.Notify()
}

// execContextFor returns a live per-task context derived from parent, recreating
// it if a prior pause cancelled it.
func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if e.IsPaused(taskID) {
		// never hand out a live context while paused (guards the claim→Execute race)
		c, cancel := context.WithCancel(parent)
		cancel()
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancel(parent)
	e.execCtx[taskID] = c
	e.execCancel[taskID] = cancel
	return c
}

// IsPaused reports whether a task is user-paused.
func (e *Engine) IsPaused(taskID string) bool {
	v, ok := e.paused.Load(taskID)
	return ok && v.(bool)
}

// Started reports whether the engine loops are running for a task.
func (e *Engine) Started(taskID string) bool {
	_, ok := e.started.Load(taskID)
	return ok
}

// LastActivity returns the unix time of the last planner/worker activity for a
// task (0 if none yet).
func (e *Engine) LastActivity(taskID string) int64 {
	if v, ok := e.lastAct.Load(taskID); ok {
		return v.(int64)
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelFunc{}, execCtx: map[string]context.Context{},
		workCancel: map[int64]context.CancelFunc{}, steerBox: map[int64][]string{}}
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelFunc) {
	e.workMu.Lock()
	e.workCancel[intentID] = cancel
	e.workMu.Unlock()
}

// unregisterWork removes and releases the work's cancel (called when Execute returns).
func (e *Engine) unregisterWork(intentID int64) {
	e.workMu.Lock()
	if c := e.workCancel[intentID]; c != nil {
		delete(e.workCancel, intentID)
		c() // release context resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("纠偏消息不能为空")
	}
	e.workMu.Lock()
	running := e.workCancel[intentID] != nil
	e.workMu.Unlock()
	if !running {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	e.steerMu.Lock()
	e.steerBox[intentID] = append(e.steerBox[intentID], msg)
	e.steerMu.Unlock()
	return nil
}

// drainSteer pops the oldest queued steering message for intentID (FIFO), if any.
func (e *Engine) drainSteer(intentID int64) (string, bool) {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	q := e.steerBox[intentID]
	if len(q) == 0 {
		return "", false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(e.steerBox, intentID)
	} else {
		e.steerBox[intentID] = q[1:]
	}
	return msg, true
}

// steerHooks wraps the guard's hook runner so the planner can steer a running work:
// before each tool call it drains a queued course-correction (if any) and blocks the
// call, handing the message back to the model — which re-plans its next step instead
// of running the tool. No queued message → the guard behaves exactly as before.
type steerHooks struct {
	inner harness.HookRunner
	drain func() (string, bool)
}

func (h steerHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if msg, ok := h.drain(); ok {
		return true, "【规划者实时纠偏】" + msg +
			"\n（这是规划者对本意图的即时指令；本次工具调用未执行，请据此调整下一步。若与你当前打算冲突，以此为准。）", nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h steerHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

func (h steerHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	if h.inner != nil {
		return h.inner.Stop(ctx, messages)
	}
	return false, nil, ""
}

// KillWork cancels the in-flight work running intentID (planner's kill_work tool).
// The work's agent-core session honors ctx cancellation and aborts promptly.
func (e *Engine) KillWork(intentID int64) error {
	e.workMu.Lock()
	c := e.workCancel[intentID]
	e.workMu.Unlock()
	if c == nil {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	c()
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) {
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "执行中" forever.
		log.Printf("[activity] task %s 重试后仍失败、丢弃该记录 (kind=%s tool=%s tuid=%s): %v",
			t.ID, r.Kind, r.Tool, r.ToolUseID, err)
		e.touch(t.ID)
		return
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
}

// appendActivity persists one activity row, retrying briefly on write failure.
// Concurrent planner + worker inserts into the same exploration's activity log
// occasionally fail; a couple of quick retries recover most. Crucially, every
// failure is now LOGGED (it used to be swallowed by an `if err == nil`), so the
// underlying DB error is finally visible for diagnosis.
func (e *Engine) appendActivity(t *Task, r db.Activity) (int64, error) {
	var id int64
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if id, err = t.Store.AppendActivity(r); err == nil {
			if attempt > 1 {
				log.Printf("[activity] task %s 写入第 %d 次重试成功 (worker=%s kind=%s tool=%s)",
					t.ID, attempt, r.Worker, r.Kind, r.Tool)
			}
			return id, nil
		}
		log.Printf("[activity] task %s 写入失败 (第 %d/3 次, worker=%s kind=%s tool=%s): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, err)
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	return 0, err
}

// UseLLM installs (or replaces) the LLM planner + work agent at runtime.
func (e *Engine) UseLLM(p *agent.Planner, w *agent.Worker) {
	e.mu.Lock()
	e.planner, e.worker = p, w
	e.mu.Unlock()
}

func (e *Engine) snapshot() (*agent.Planner, *agent.Worker) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planner, e.worker
}

// SetAgentResolver installs a per-task planner/worker resolver (wired by the server).
// Called once at startup before any task loop runs, so no lock is needed on reads.
func (e *Engine) SetAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
}

// snapshotFor returns the planner/worker a task should run on: the task's dedicated
// pair when it pins a specific LLM profile (via the resolver), else the global active
// pair. The resolver returning nil means "no override" (fall back to global).
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		if p, w := e.resolve(t); p != nil && w != nil {
			return p, w
		}
	}
	return e.snapshot()
}

// Ready reports whether an LLM provider is configured.
func (e *Engine) Ready() bool {
	p, w := e.snapshot()
	return p != nil && w != nil
}

// Mode reports "llm" when ready, else "idle".
func (e *Engine) Mode() string {
	if e.Ready() {
		return "llm"
	}
	return "idle"
}

// Run starts the planner loop + N worker loops for a task. The loops always run
// but no-op until an LLM is configured (so a task created while idle picks up
// automatically once LLM is set from the UI).
func (e *Engine) Run(ctx context.Context, t *Task) {
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		t.Notify() // already running — just nudge a planning round
		return
	}
	e.touch(t.ID)
	go e.plannerLoop(ctx, t)
	workers := e.m.Workers()
	for i := 0; i < workers; i++ {
		go e.workerLoop(ctx, t, fmt.Sprintf("work#%d", i+1))
	}
	e.startDeadlineCoordinator(ctx, t) // 任务级超时定时器(仅 timeout>0;去重)
	// 仅在 frontier 为空时才 kick 首轮规划:普通任务(空 frontier)照常触发;带种子意图的
	// 任务 frontier 已非空 → 跳过首轮 planner,worker 直接领种子意图开跑,跑完由 NotifyDone
	// 唤醒 planner。重启自动恢复时 frontier 通常也非空 → 顺带避免白烧一轮。
	if fr, _ := t.Store.Frontier(1); len(fr) == 0 {
		t.Notify() // kick the first planning round (acted on once LLM is ready)
	}
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			// debounce: coalesce a burst of changes into one planning round
			timer := time.NewTimer(e.debounce)
		drain:
			for {
				select {
				case <-t.notify:
				case <-timer.C:
					break drain
				}
			}
			planner, _ := e.snapshotFor(t)
			if planner == nil {
				continue // idle until LLM configured
			}
			if e.IsPaused(t.ID) {
				continue // user-paused: don't plan
			}
			// terminal task (goals all met → done, or failed): the run is over. A
			// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
			// re-plan (it would burn an LLM round and re-confirm a settled result).
			if isTerminalStatus(t.Status) {
				continue
			}
			// 任务级超时收尾中:丢弃普通唤醒——worker 收尾写回、Resume 的 Notify 都不再
			// 触发常规规划轮;终局那一轮由协调器(settleTask)直接驱动,不走这里。
			if e.isSettling(t.ID) {
				continue
			}
			e.stampFirstRun(t) // 首次真正规划 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
			e.touch(t.ID)
			emit := func(r db.Activity) { e.emitActivity(t, r) }
			ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
			log.Printf("[planner] task %s 规划中…", t.ID)
			// round marker: each Plan() is one planner round; emit a boundary so the
			// UI can separate rounds in the transcript (kind='round').
			e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
				Summary: fmt.Sprintf("第 %d 轮规划", e.nextPlannerRound(t.ID))})
			// which intents' completion triggered this round (may be several — debounce
			// coalesces a burst; empty for non-completion wakes like a new hint).
			doneIntents := t.drainDone()
			e.incInflight(t.ID)
			taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
			met, reason, err := planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, doneIntents, emit)
			e.decInflight(t.ID)
			switch {
			case err != nil && ectx.Err() == nil:
				log.Printf("[planner] task %s 规划出错: %v", t.ID, err)
			case met:
				log.Printf("[planner] task %s 判定目标达成: %s", t.ID, reason)
				// 所有目标达成 → 持久化任务状态为 done（前端 DTO 会优先展示该终态）。
				if err := e.m.SetTaskStatus(t.ID, "done"); err != nil {
					log.Printf("[planner] task %s 标记完成落库失败: %v", t.ID, err)
				}
				// 任务已判完成 → 立刻取消在跑的 worker：它们手头的意图跑出来也没意义了。
				// 下一轮 worker 循环撞终态门就不再领新意图;被取消的这批走下方"任务已完成"分支
				// 归为 stopped(而非 blocked)。
				e.cancelExec(t.ID)
			default:
				log.Printf("[planner] task %s 规划完成", t.ID)
			}
			e.touch(t.ID)
		}
	}
}

func (e *Engine) workerLoop(ctx context.Context, t *Task, name string) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, worker := e.snapshotFor(t)
		if worker == nil {
			if sleepCtx(ctx, 1500*time.Millisecond) {
				return
			}
			continue
		}
		if e.IsPaused(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // user-paused: don't claim/execute intents
		}
		if e.isSettling(t.ID) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务超时收尾中:不再领新意图(在跑的自行收尾,协调器等其 drain)
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			if sleepCtx(ctx, 1000*time.Millisecond) {
				return
			}
			continue // 任务已终态(done/failed/timeout):停止领取遗留意图,别在完成后空跑 frontier
		}
		intent := e.claimNext(t, name)
		if intent == nil {
			if sleepCtx(ctx, 800*time.Millisecond) {
				return
			}
			continue
		}
		log.Printf("[worker %s] task %s 领取意图 #%d", name, t.ID, intent.ID)
		e.stampFirstRun(t) // 首次真正执行 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
		// per-work child context so the planner's kill_work can stop just this work.
		workCtx, workCancel := context.WithCancel(ectx)
		e.registerWork(intent.ID, workCancel)
		// wrap the guard hooks so steer_work can inject a mid-run course-correction
		// for THIS intent (drained before the worker's next tool call).
		iid := intent.ID
		taskEmit := func(a db.Activity) {
			nid := iid
			a.NodeID, a.Worker = &nid, name
			emit(a)
		}
		workCtx = intercept.WithTaskContext(workCtx, t.ID, fmt.Sprintf("%s · #%d", name, iid), taskEmit)
		hooks := steerHooks{inner: t.Guard.Hooks(), drain: func() (string, bool) { return e.drainSteer(iid) }}
		wTaskID, _ := strconv.ParseInt(t.ID, 10, 64)
		e.incInflight(t.ID) // 计入在跑,供收尾时序 drain 等待
		reason, wrote, err := worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich)
		// model_error 收场 → 额外重跑几次（退避后再试）。仅在意图仍属本 work、任务
		// 未暂停/未终止/未取消【且未进入收尾】时重试；否则让位给对应分支处理(收尾期不
		// 再重试,避免退避挤占其他 worker 的优雅收尾窗口)。
		for attempt := 1; attempt <= modelErrorRetries &&
			reason == harness.ReasonModelError &&
			workCtx.Err() == nil && ectx.Err() == nil && !e.IsPaused(t.ID) && !e.isSettling(t.ID); attempt++ {
			log.Printf("[worker %s] task %s 意图 #%d model_error 收场，%v 后重试 (%d/%d)",
				name, t.ID, intent.ID, modelErrorRetryBackoff, attempt, modelErrorRetries)
			if sleepCtx(workCtx, modelErrorRetryBackoff) {
				break // 退避期间被取消（终止/暂停）→ 交给下方分支处理
			}
			reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich)
		}
		e.decInflight(t.ID)
		// CAPTURE kill state BEFORE unregisterWork cancels workCtx. kill = this work's
		// ctx was cancelled (planner kill_work) while the TASK ctx kept running; a
		// pause cancels the task ctx (ectx) instead. Checking workCtx.Err() AFTER
		// unregister would always be true (unregister cancels it) → every completed
		// work would be wrongly marked stopped.
		killed := workCtx.Err() != nil && ectx.Err() == nil
		e.unregisterWork(intent.ID)
		// if a pause cancelled this run mid-flight, return the intent to the frontier
		// so it is re-claimed (and re-run from scratch) on resume — not marked done.
		if ectx.Err() != nil && e.IsPaused(t.ID) {
			_ = t.Store.SetIntentState(intent.ID, "open")
			continue
		}
		// 任务超时收尾的硬兜底 cancel(非 pause、非 kill)取消了本 run → 归为 exhausted(已收尾),
		// 不要误标 blocked。此时 worker 通常已在 settlement 阶段把结果写回。
		if ectx.Err() != nil && e.isSettling(t.ID) {
			_ = t.Store.SetIntentState(intent.ID, "exhausted")
			log.Printf("[worker %s] task %s 意图 #%d 因任务超时收尾结束(exhausted)，写回 %s", name, t.ID, intent.ID, wrote)
			e.touch(t.ID)
			continue
		}
		// 任务已判完成(done via 常规路径)→ 上面 cancelExec 取消了本 run。意图结果已无意义,
		// 标 stopped(不是 blocked),别污染已完成任务的意图状态。
		if ectx.Err() != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
			_ = t.Store.SetIntentState(intent.ID, "stopped")
			log.Printf("[worker %s] task %s 意图 #%d 因任务已完成而取消(stopped)", name, t.ID, intent.ID)
			e.touch(t.ID)
			continue
		}
		// killed by the planner: mark stopped (don't write back results, don't auto-reclaim).
		if killed {
			_ = t.Store.SetIntentState(intent.ID, "stopped")
			log.Printf("[worker %s] task %s 意图 #%d 被终止(stopped)", name, t.ID, intent.ID)
			e.touch(t.ID)
			t.Notify()
			continue
		}
		if err != nil {
			log.Printf("[worker %s] intent %d: %v", name, intent.ID, err)
		}
		// terminal分流：撞步数上限 ≠ 完成。max_turns→exhausted（规划者据此知道这个方向
		// 试过但没真正做完、需换角度，而非当成已覆盖永久跳过）；出错→blocked；正常→done。
		state := "done"
		switch {
		case err != nil:
			state = "blocked"
		case reason == harness.ReasonMaxTurns:
			state = "exhausted"
			log.Printf("[worker %s] intent %d 撞步数上限(exhausted)，本次写回 %s", name, intent.ID, wrote)
		case reason == harness.ReasonTimeout:
			state = "exhausted"
			log.Printf("[worker %s] intent %d 运行超时(exhausted)，收尾后写回 %s", name, intent.ID, wrote)
		}
		_ = t.Store.SetIntentState(intent.ID, state)
		log.Printf("[worker %s] task %s 意图 #%d 结束: %s (写回 %s)", name, t.ID, intent.ID, state, wrote)
		e.touch(t.ID)
		t.NotifyDone(intent.ID) // results changed the graph -> wake the planner (with the just-finished intent id)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) (done bool) {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func (e *Engine) claimNext(t *Task, name string) *db.Node {
	fr, _ := t.Store.Frontier(20)
	for _, in := range fr {
		if ok, _ := t.Store.ClaimIntent(in.ID, name); ok {
			return in
		}
	}
	return nil
}
