package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
	"github.com/jackc/pgx/v5/pgconn"
)

// isFKViolation reports whether err is a Postgres foreign-key violation (SQLSTATE
// 23503) — e.g. an activity insert whose exploration_id has no parent row.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// dropReason classifies why an activity write was dropped, so the log can be
// grouped/analysed by cause rather than by raw error text.
func dropReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return "fk_violation(23503,父exploration不存在)"
		case "23505":
			return "unique_violation(23505)"
		default:
			return "pg_error(" + pgErr.Code + ")"
		}
	}
	return "write_error"
}

// bumpDrop increments and returns the running count of dropped (unpersistable)
// activity records for a task. Concurrent planner + worker emits race here, so the
// counter is an atomic behind sync.Map. The count in the log shows loss scale at a
// glance instead of forcing a grep-and-count.
func (e *Engine) bumpDrop(taskID string) int64 {
	v, _ := e.dropCnt.LoadOrStore(taskID, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// preview collapses newlines and trims s to a short rune-safe snippet for one-line
// log output (avoids dumping a multi-KB summary/detail into the log).
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// model_error（provider/API 故障：LLM 层瞬时重试耗尽，或流已开始后中途断流）
// 收场的 work 不是「试过没做完」也不是真失败，而是外部抖动。默认把它当永久
// blocked 会白丢一条意图，所以这里对该终态额外重跑几次，每次之间退避一下，给
// provider 恢复的时间；重试期间若被暂停/终止/取消则立即让位给对应分支处理。
const (
	modelErrorRetries      = 2               // model_error 收场后额外重试的次数
	modelErrorRetryBackoff = 3 * time.Second // 每次重试前的退避
	workControlWaitTimeout = 30 * time.Second
)

var errWorkControlConflict = errors.New("work control conflict")
var errWorkInterventionConflict = errors.New("work intervention conflict")

// retryableWorkerModelError excludes errors already handled by the task router.
// In particular, a quota error after partial streaming advances the task cursor
// for the next LLM call but must not replay this whole intent on the backup.
func retryableWorkerModelError(reason harness.TerminalReason, err error) bool {
	return reason == harness.ReasonModelError && !isTaskLLMRuntimeError(err)
}

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

	mu       sync.RWMutex
	planner  *agent.Planner
	worker   *agent.Worker
	started  sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct  sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	llmCalls sync.Map // taskID -> *int64, actual planner/worker/main-agent LLM calls
	paused   sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)
	deleting sync.Map // taskID -> bool, delete barrier (no new task-owned writes)
	dropCnt  sync.Map // taskID -> *int64, running count of dropped (unpersistable) activity records

	// deleteMu makes installing the delete barrier atomic with registering a new
	// task operation. Once BeginDelete returns, every admitted writer is reflected
	// in inflight and every later writer is rejected.
	deleteMu sync.RWMutex

	// Every long-lived task goroutine (planner, workers and deadline coordinator)
	// runs under one task-scoped context. Successful deletion cancels that context,
	// waits for all goroutines, then releases every task-level Engine reference.
	runtimeMu sync.Mutex
	runtimes  map[string]*taskRuntime

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot. Every cancellation carries a named
	// cause so the activity trace can identify the initiating control path.
	execMu     sync.Mutex
	execCancel map[string]context.CancelCauseFunc
	execCtx    map[string]context.Context

	// Per-work control lets the planner kill a worker and lets the UI pause/cancel
	// one intent without pausing the whole task. The done channel closes only after
	// runWorkerStep has stopped writing and committed its final state.
	workMu sync.Mutex
	work   map[int64]*workExecution
	// interventionLocks serializes reservation, cancellation and message hand-off
	// per intent. PostgreSQL separately serializes the durable transaction steps.
	interventionLocks sync.Map // intentID -> *sync.Mutex

	// directedClaims keeps intents with a new human message ahead of the normal
	// frontier without mutating their persisted priority. runWorkerStep installs
	// the entry before releasing ControlWork, so a single worker slot cannot grab
	// unrelated work in the paused->open hand-off gap.
	directedMu     sync.Mutex
	directedClaims map[string][]int64
	directedReady  map[int64]bool

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

	// resolve, if set, returns a task's dedicated planner/worker. In legacy mode
	// nil,nil falls back to the global pair; an authoritative resolver uses nil,nil
	// to mean this task is deliberately unavailable (for example an exhausted chain).
	resolve              func(t *Task) (*agent.Planner, *agent.Worker)
	resolveAuthoritative bool
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type workExecution struct {
	cancel context.CancelCauseFunc
	done   chan error
	action string // user action: pause | cancel | intervene
}

type WorkInterventionResult struct {
	RequestID  string
	ActivityID int64
	State      string
	Duplicate  bool
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
func (e *Engine) Pause(taskID string, cause error) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID, cause)
}

// BeginDelete installs an execution barrier before task data/files are removed.
// The temporary pause is not a user pause. The server serializes this transition
// with lifecycle admission and tells AbortDelete whether the persisted task is
// paused/queued if cleanup fails.
func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	if _, loaded := e.deleting.LoadOrStore(taskID, true); loaded {
		e.deleteMu.Unlock()
		return false
	}
	e.paused.Store(taskID, true)
	e.deleteMu.Unlock()
	e.cancelExec(taskID, agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	if !e.IsDeleting(taskID) {
		e.deleteMu.Unlock()
		return
	}
	e.deleting.Delete(taskID)
	if !keepPaused {
		e.paused.Delete(taskID)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if t, ok := e.m.Task(taskID); ok {
			t.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	_, ok := e.deleting.Load(taskID)
	return ok
}

// registerTaskRoutines reserves count goroutines in the task runtime. Callers
// hold deleteMu for reading so StopTask cannot race WaitGroup.Add with Wait.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRuntime {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	rt := e.runtimes[taskID]
	if rt == nil {
		ctx, cancel := context.WithCancel(parent)
		rt = &taskRuntime{ctx: ctx, cancel: cancel}
		e.runtimes[taskID] = rt
	}
	rt.wg.Add(count)
	return rt
}

func runTaskRoutine(rt *taskRuntime, fn func(context.Context)) {
	go func() {
		defer rt.wg.Done()
		fn(rt.ctx)
	}()
}

// StopTask permanently stops every long-lived goroutine and removes all Engine
// state for a successfully deleted task. The delete barrier remains installed
// until cleanup finishes, so no new task operation can race the teardown.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	e.deleting.Store(taskID, true)
	e.deleteMu.Unlock()

	e.cancelExec(taskID, agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt != nil {
		rt.cancel()
	}
	e.runtimeMu.Unlock()
	if rt != nil {
		rt.wg.Wait()
	}

	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(agent.AbortTaskDeleted)
	}
	delete(e.execCancel, taskID)
	delete(e.execCtx, taskID)
	e.execMu.Unlock()

	e.runtimeMu.Lock()
	if e.runtimes[taskID] == rt {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()

	e.started.Delete(taskID)
	e.lastAct.Delete(taskID)
	e.llmCalls.Delete(taskID)
	e.paused.Delete(taskID)
	e.dropCnt.Delete(taskID)
	e.plannerRound.Delete(taskID)
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.inflight.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.directedMu.Lock()
	for _, intentID := range e.directedClaims[taskID] {
		delete(e.directedReady, intentID)
	}
	delete(e.directedClaims, taskID)
	e.directedMu.Unlock()
	e.deleteMu.Lock()
	e.deleting.Delete(taskID)
	e.deleteMu.Unlock()
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string, cause error) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(cause)
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	// BeginDelete owns the pause barrier once deletion starts. A concurrent
	// resume must never clear it and let a planner/worker re-enter while cleanup
	// is waiting for task operations to drain.
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(t.ID) {
		return
	}
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
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancelCause(parent)
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

// BeginLLMCall/EndLLMCall track actual provider calls separately from the
// scheduler's task-operation counter. A task can have live loops while all of
// them are waiting for a trigger; that state must remain idle in the UI.
func (e *Engine) BeginLLMCall(taskID string) {
	v, _ := e.llmCalls.LoadOrStore(taskID, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (e *Engine) EndLLMCall(taskID string) {
	if v, ok := e.llmCalls.Load(taskID); ok {
		p := v.(*int64)
		if atomic.AddInt64(p, -1) <= 0 {
			atomic.StoreInt64(p, 0)
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if v, ok := e.llmCalls.Load(taskID); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelCauseFunc{}, execCtx: map[string]context.Context{},
		work: map[int64]*workExecution{}, steerBox: map[int64][]string{},
		directedClaims: map[string][]int64{}, directedReady: map[int64]bool{}, runtimes: map[string]*taskRuntime{}}
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelCauseFunc) {
	e.workMu.Lock()
	e.work[intentID] = &workExecution{cancel: cancel, done: make(chan error, 1)}
	e.workMu.Unlock()
}

// detachWork removes the live control handle once Execute has returned. complete
// must be called after the final intent state write so a waiting cancel handler can
// safely delete the worker's blackboard output without racing a late write.
func (e *Engine) detachWork(intentID int64) (action string, complete func(error)) {
	e.workMu.Lock()
	run := e.work[intentID]
	if run != nil {
		delete(e.work, intentID)
		action = run.action
		run.cancel(agent.AbortWorkFinished) // release resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
	if run == nil {
		return action, func(error) {}
	}
	return action, func(err error) { run.done <- err }
}

// ControlWork requests a user-visible pause or cancellation and waits until the
// worker has fully stopped writing. Cancellation cleanup is performed by the API
// handler after this returns; pause state is committed by runWorkerStep itself.
func (e *Engine) ControlWork(ctx context.Context, intentID int64, action string) error {
	if action != "pause" && action != "cancel" && action != "intervene" {
		return fmt.Errorf("unsupported work action %q", action)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.workMu.Lock()
	run := e.work[intentID]
	if run == nil {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 当前没有运行中的 work（可能已结束或未被领取）", errWorkControlConflict, intentID)
	}
	if run.action != "" {
		e.workMu.Unlock()
		return fmt.Errorf("%w: 意图 %d 正在执行 %s 操作", errWorkControlConflict, intentID, run.action)
	}
	run.action = action
	done := run.done
	cause := error(agent.AbortWorkPausedByUser)
	switch action {
	case "cancel":
		cause = agent.AbortWorkCancelledByUser
	case "intervene":
		cause = agent.AbortWorkIntervenedByUser
	}
	run.cancel(cause)
	e.workMu.Unlock()

	timer := time.NewTimer(workControlWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, ctx.Err())
	case <-timer.C:
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("等待意图 %d %s 收尾: %w", intentID, action, context.DeadlineExceeded)
	}
}

func (e *Engine) lockWorkIntervention(intentID int64) func() {
	value, _ := e.interventionLocks.LoadOrStore(intentID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (e *Engine) validateWorkInterventionTask(t *Task, allowAdmissionPause bool) error {
	if t == nil {
		return fmt.Errorf("%w: task not found", errWorkInterventionConflict)
	}
	lifecycle := t.lifecycleSnapshot()
	switch {
	case e.IsDeleting(t.ID):
		return fmt.Errorf("%w: 任务正在删除，无法向 Worker 发送消息", errWorkInterventionConflict)
	case lifecycle.Paused || (!allowAdmissionPause && e.IsPaused(t.ID)):
		return fmt.Errorf("%w: 任务已暂停，无法向 Worker 发送消息", errWorkInterventionConflict)
	case lifecycle.Queued:
		return fmt.Errorf("%w: 排队中的任务无法向 Worker 发送消息", errWorkInterventionConflict)
	case isTerminalStatus(lifecycle.Status):
		return fmt.Errorf("%w: 终态任务无法向 Worker 发送消息", errWorkInterventionConflict)
	case e.isSettling(t.ID):
		return fmt.Errorf("%w: 任务正在收尾，无法向 Worker 发送消息", errWorkInterventionConflict)
	default:
		return nil
	}
}

func (e *Engine) abandonWorkIntervention(t *Task, intentID int64, requestID string, reason error) {
	e.removeDirectedClaim(t.ID, intentID)
	target := "open"
	lifecycle := t.lifecycleSnapshot()
	if e.IsDeleting(t.ID) || isTerminalStatus(lifecycle.Status) {
		target = "stopped"
	} else if e.isSettling(t.ID) {
		target = "exhausted"
	}
	if changed, err := t.Store.CompareAndSetIntentState(intentID, "paused", target); err != nil {
		log.Printf("[worker intervention] task %s 意图 #%d 补偿 paused->%s 失败: %v", t.ID, intentID, target, err)
	} else if changed && target == "open" {
		t.NotifyWorker()
	}
	if err := t.Store.RejectIntentIntervention(intentID, requestID, reason.Error()); err != nil {
		log.Printf("[worker intervention] task %s 意图 #%d 标记请求 %s rejected 失败: %v", t.ID, intentID, requestID, err)
	}
}

// InterveneWork performs the complete Worker message protocol. Once the request
// has a durable pending reservation it no longer depends on the HTTP request
// context: the caller supplies a server-owned bounded context so disconnects do
// not strand the intent. The accepted DB transition is running -> paused ->
// open, with the visible user message and reopen committed atomically.
func (e *Engine) InterveneWork(ctx context.Context, t *Task, intentID int64, requestID, message string) (WorkInterventionResult, error) {
	return e.interveneWork(ctx, t, intentID, requestID, message, false)
}

func (e *Engine) interveneWork(ctx context.Context, t *Task, intentID int64, requestID, message string, allowAdmissionPause bool) (WorkInterventionResult, error) {
	out := WorkInterventionResult{RequestID: requestID}
	if t == nil || t.Store == nil {
		return out, fmt.Errorf("%w: task not found", errWorkInterventionConflict)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	unlock := e.lockWorkIntervention(intentID)
	defer unlock()

	existing, err := t.Store.IntentInterventionByRequest(intentID, requestID)
	if err != nil {
		return out, err
	}
	if existing != nil {
		if existing.Message != message {
			return out, fmt.Errorf("%w: request_id 已用于不同的 Worker 消息", errWorkInterventionConflict)
		}
		if existing.Status == db.IntentInterventionAccepted {
			out.ActivityID, out.State, out.Duplicate = existing.ActivityID, existing.IntentState, true
			if out.State == "open" && e.validateWorkInterventionTask(t, allowAdmissionPause) == nil {
				e.queueDirectedClaim(t.ID, intentID)
				e.readyDirectedClaim(intentID)
				t.NotifyWorker()
			}
			return out, nil
		}
		if existing.Status != db.IntentInterventionPending {
			return out, fmt.Errorf("%w: Worker 消息请求状态为 %s", errWorkInterventionConflict, existing.Status)
		}
	}
	if err := e.validateWorkInterventionTask(t, allowAdmissionPause); err != nil {
		lifecycle := t.lifecycleSnapshot()
		if existing != nil && (e.IsDeleting(t.ID) || e.isSettling(t.ID) || isTerminalStatus(lifecycle.Status)) {
			e.abandonWorkIntervention(t, intentID, requestID, err)
		}
		return out, err
	}
	_, worker := e.snapshotFor(t)
	if worker == nil {
		return out, fmt.Errorf("%w: worker 尚未就绪", errWorkInterventionConflict)
	}
	if existing == nil {
		existing, err = t.Store.ReserveIntentIntervention(intentID, requestID, message)
		if err != nil {
			if errors.Is(err, db.ErrIntentInterventionConflict) {
				return out, fmt.Errorf("%w: %v", errWorkInterventionConflict, err)
			}
			return out, err
		}
	}
	out.ActivityID = existing.ActivityID

	// A pending reservation can be recovered after a process restart. An open
	// row is fenced back to paused before publishing the user turn; a running row
	// is stopped through the normal live-work protocol so no late writer races us.
	for {
		node, getErr := t.Store.GetNode(intentID)
		if getErr != nil {
			return out, getErr
		}
		if node == nil {
			return out, fmt.Errorf("%w: intent not found", errWorkInterventionConflict)
		}
		switch node.State {
		case "running":
			controlErr := e.ControlWork(ctx, intentID, "intervene")
			if controlErr != nil {
				// The per-work 30s wait may expire before the server-owned operation
				// context. Keep waiting for the named cancellation to settle; timeout
				// release marks the run no-handoff so a late exit cannot install a
				// permanent directed-claim barrier behind our back. A freshly claimed
				// intent can also be running just before registerWork; retrying closes
				// that small claim-to-registration window.
				for {
					settled, readErr := t.Store.GetNode(intentID)
					if readErr != nil {
						return out, readErr
					}
					if settled != nil && settled.State == "paused" {
						break
					}
					if ctx.Err() != nil || settled == nil || settled.State != "running" {
						e.removeDirectedClaim(t.ID, intentID)
						return out, fmt.Errorf("%w: %v", errWorkInterventionConflict, controlErr)
					}
					if sleepCtx(ctx, 50*time.Millisecond) {
						e.removeDirectedClaim(t.ID, intentID)
						return out, fmt.Errorf("%w: %v", errWorkInterventionConflict, controlErr)
					}
					if errors.Is(controlErr, errWorkControlConflict) {
						controlErr = e.ControlWork(ctx, intentID, "intervene")
					}
				}
			}
		case "open":
			changed, setErr := t.Store.CompareAndSetIntentState(intentID, "open", "paused")
			if setErr != nil {
				return out, setErr
			}
			if !changed {
				continue
			}
			e.queueDirectedClaim(t.ID, intentID)
		case "paused":
			e.queueDirectedClaim(t.ID, intentID)
		default:
			e.abandonWorkIntervention(t, intentID, requestID,
				fmt.Errorf("意图已进入 %s，无法发送新消息", node.State))
			return out, fmt.Errorf("%w: 意图已进入 %s，无法发送新消息", errWorkInterventionConflict, node.State)
		}
		break
	}
	e.queueDirectedClaim(t.ID, intentID)

	// Close lifecycle races that began while cancellation was settling. A task
	// pause or queue preserves the pending message and paused intent for a later
	// admission; terminal, settling and deleting transitions close the request.
	if err := e.validateWorkInterventionTask(t, allowAdmissionPause); err != nil {
		// The user message has not entered the transcript yet. Preserve the
		// reservation and paused intent so the same request can be retried after a
		// transient task pause; do not reopen it behind a terminal transition.
		e.removeDirectedClaim(t.ID, intentID)
		lifecycle := t.lifecycleSnapshot()
		if e.IsDeleting(t.ID) || e.isSettling(t.ID) || isTerminalStatus(lifecycle.Status) {
			e.abandonWorkIntervention(t, intentID, requestID, err)
		}
		return out, err
	}
	activity, duplicate, err := t.Store.AcceptIntentIntervention(intentID, requestID, message)
	if err != nil {
		// Keep this intent paused so a retry of the same request can safely finish;
		// remove only the task-wide claim barrier so unrelated workers keep moving.
		e.removeDirectedClaim(t.ID, intentID)
		return out, err
	}
	if !duplicate {
		e.publishStoredActivity(t, activity)
	}
	e.readyDirectedClaim(intentID)
	t.NotifyWorker()
	node, err := t.Store.GetNode(intentID)
	if err != nil {
		return out, err
	}
	out.Duplicate = duplicate
	if node != nil {
		out.State = node.State
	}
	return out, nil
}

// RecoverWorkerMessages reconstructs unfinished Worker chat handoffs before
// ordinary frontier claiming. Pending messages are fenced in paused until an
// agent runtime is available; accepted messages regain their directed priority
// and are consumed by ExecuteWithMessage on the next claim.
func (e *Engine) RecoverWorkerMessages(ctx context.Context, t *Task) error {
	return e.recoverWorkerMessages(ctx, t, false)
}

// recoverWorkerMessagesForAdmission runs while the scheduler still owns the
// Engine pause barrier but has already committed a runnable task lifecycle. It
// restores directed hand-offs before Resume wakes any worker, so ordinary
// frontier work cannot overtake the human message.
func (e *Engine) recoverWorkerMessagesForAdmission(ctx context.Context, t *Task) error {
	return e.recoverWorkerMessages(ctx, t, true)
}

func (e *Engine) recoverWorkerMessages(ctx context.Context, t *Task, allowAdmissionPause bool) error {
	if t == nil || t.Store == nil {
		return nil
	}
	items, err := t.Store.RecoverableIntentInterventions()
	if err != nil {
		return err
	}
	lifecycle := t.lifecycleSnapshot()
	runnable := !e.IsDeleting(t.ID) && !e.isSettling(t.ID) && !isTerminalStatus(lifecycle.Status) &&
		!lifecycle.Paused && !lifecycle.Queued && (allowAdmissionPause || !e.IsPaused(t.ID))
	if !runnable {
		for _, item := range items {
			e.removeDirectedClaim(t.ID, item.IntentID)
		}
		return nil
	}
	for _, item := range items {
		switch item.Status {
		case db.IntentInterventionPending:
			if item.IntentState == "open" {
				if changed, setErr := t.Store.CompareAndSetIntentState(item.IntentID, "open", "paused"); setErr != nil {
					return setErr
				} else if changed {
					item.IntentState = "paused"
				}
			}
			if _, worker := e.snapshotFor(t); worker == nil {
				continue
			}
			if _, recoverErr := e.interveneWork(ctx, t, item.IntentID, item.RequestID, item.Message, allowAdmissionPause); recoverErr != nil {
				log.Printf("[worker chat] task %s 恢复意图 #%d 待处理消息失败: %v", t.ID, item.IntentID, recoverErr)
			}
		case db.IntentInterventionAccepted:
			if item.IntentState == "paused" {
				if changed, setErr := t.Store.CompareAndSetIntentState(item.IntentID, "paused", "open"); setErr != nil {
					return setErr
				} else if changed {
					item.IntentState = "open"
				}
			}
			if item.IntentState == "open" {
				e.queueDirectedClaim(t.ID, item.IntentID)
				e.readyDirectedClaim(item.IntentID)
				t.NotifyWorker()
			}
		}
	}
	return nil
}

// releaseWorkControl drops only this caller's reservation after its wait is
// cancelled. The work context stays cancelled; runWorkerStep recognizes the
// named cancellation cause and settles the intent into the recoverable paused
// state even if the HTTP caller has gone away.
func (e *Engine) releaseWorkControl(intentID int64, run *workExecution, action string) {
	e.workMu.Lock()
	if current := e.work[intentID]; current == run && current.action == action {
		if action == "intervene" {
			current.action = "intervene_no_handoff"
		} else {
			current.action = ""
		}
	}
	e.workMu.Unlock()
}

func transitionIntentState(store *db.ExplorationStore, intentID int64, expected, state string) error {
	changed, err := store.CompareAndSetIntentState(intentID, expected, state)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: 意图 %d 不再是 %s 状态", db.ErrIntentStateConflict, intentID, expected)
	}
	return nil
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("纠偏消息不能为空")
	}
	e.workMu.Lock()
	running := e.work[intentID] != nil
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
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return fmt.Errorf("意图 %d 当前没有运行中的 work（可能已结束或未被领取）", intentID)
	}
	run.cancel(agent.AbortKilledByPlanner)
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) db.Activity {
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "执行中" forever, and
		// a lost 'result'/'round' record leaves the session with no summary ("无总结").
		// Everything needed to分析根因 goes into ONE error-level line: reason class,
		// summary preview, running drop count for this task, and — on the FK case — a
		// live probe of WHY the parent exploration is unreachable.
		n := e.bumpDrop(t.ID)
		diag := ""
		// On the FK-parent failure (23503) probe the live DB so the log records WHY the
		// exploration is unreachable (row gone / wrong expID) instead of just that it is.
		if isFKViolation(err) {
			storeID := t.Store.ID()
			if exists, refs, maxID, dErr := e.m.pg.ExplorationDiag(storeID); dErr != nil {
				diag = fmt.Sprintf(" | FK诊断查询失败(store.expID=%d task.ExpID=%d): %v", storeID, t.ExpID, dErr)
			} else {
				diag = fmt.Sprintf(" | FK诊断: store.expID=%d task.ExpID=%d exploration存在=%v 引用它的task数=%d MAX(exploration.id)=%d",
					storeID, t.ExpID, exists, refs, maxID)
			}
		}
		log.Printf("[activity] task %s 丢弃活动记录(该任务累计第 %d 条) worker=%s kind=%s tool=%s tuid=%s reason=%s summary=%q: %v%s",
			t.ID, n, r.Worker, r.Kind, r.Tool, r.ToolUseID, dropReason(err), preview(r.Summary, 80), err, diag)
		e.touch(t.ID)
		return r
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
	return r
}

// publishStoredActivity broadcasts an activity inserted by a larger DB
// transaction, avoiding a second INSERT through emitActivity.
func (e *Engine) publishStoredActivity(t *Task, r db.Activity) {
	if t == nil || r.ID <= 0 {
		return
	}
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
		log.Printf("[activity] task %s 写入失败 (第 %d/3 次, worker=%s kind=%s tool=%s expID=%d): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, t.Store.ID(), err)
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
	e.resolveAuthoritative = false
}

// SetAuthoritativeAgentResolver installs a resolver whose nil result must not
// fall through to the global provider. Task-level failover chains use this so a
// fully exhausted chain cannot silently bypass its configured boundary.
func (e *Engine) SetAuthoritativeAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = true
}

// snapshotFor returns the planner/worker a task should run on: the task's dedicated
// pair when it pins a specific LLM profile (via the resolver), else the global active
// pair. The resolver returning nil means "no override" (fall back to global).
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		p, w := e.resolve(t)
		if (p != nil && w != nil) || e.resolveAuthoritative {
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

// ReadyFor reports whether a specific task can resolve a planner/worker pair.
// An explicit task profile chain can be runnable even when no global default
// provider is configured, so task status must not rely on Ready alone.
func (e *Engine) ReadyFor(t *Task) bool {
	p, w := e.snapshotFor(t)
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
	workers := e.m.Workers()
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		t.Notify() // already running — just nudge a planning round
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1+workers)
	e.deleteMu.RUnlock()
	e.touch(t.ID)
	runTaskRoutine(rt, func(loopCtx context.Context) { e.plannerLoop(loopCtx, t) })
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("work#%d", i+1)
		runTaskRoutine(rt, func(loopCtx context.Context) { e.workerLoop(loopCtx, t, name) })
	}
	e.startDeadlineCoordinator(ctx, t) // 任务级超时定时器(仅 timeout>0;去重)
	// 仅在「完全没有活动意图(open+running)」时才 kick 首轮规划。带种子意图的任务:种子已
	// 是 open,或已被上面刚起的 worker 抢先 claim 成 running——两种都算「有活干」,一律跳过
	// 首轮 planner,worker 直接领种子意图开跑,跑完由 NotifyDone/心跳唤醒 planner。
	// ⚠️ 不能用 Frontier(只数 open):worker 领取(open→running)与本检查存在竞态,会误 kick。
	// 重启自动恢复时也可能只剩 running 意图,同样应跳过。
	if has, _ := t.Store.HasActiveIntent(); !has {
		t.Notify() // kick the first planning round (acted on once LLM is ready)
	}
}

// plannerHeartbeatInterval 解析任务的 planner 心跳间隔。db.CreateTask 已归一
// (低于 600 一律抬到 600);这里再兜一次底,防内存态异常值。
func plannerHeartbeatInterval(t *Task) time.Duration {
	sec := t.PlanHeartbeatSeconds
	if sec < db.MinPlanHeartbeatSeconds { // 下限=默认=600(10min)
		sec = db.MinPlanHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// resetPlannerTimer 安全重臂一个可能已触发的 Timer(标准 Stop→drain→Reset 模式)。
func resetPlannerTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	interval := plannerHeartbeatInterval(t)
	// 心跳定时器在 loop 入口臂 = 从任务 start 计时:即使是跳过首轮 planner 的 seed 任务
	// (Run 里 frontier 非空不 kick 首轮)、这里一直阻塞,心跳也会在「任务 start + interval」
	// 触发第一轮规划。之后每次唤醒(边沿/心跳)都重臂 = 距上次任意规划触发的时长。
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()

	// runRound 跑一轮规划(含 debounce 合并 + 各 guard)。src 仅用于日志区分触发来源。
	runRound := func(src string) {
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
			return // idle until LLM configured
		}
		if e.IsPaused(t.ID) {
			return // user-paused: don't plan
		}
		if e.IsDeleting(t.ID) {
			return
		}
		// terminal task (goals all met → done, or failed): the run is over. A
		// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
		// re-plan (it would burn an LLM round and re-confirm a settled result).
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			return
		}
		// 任务级超时收尾中:丢弃普通唤醒——worker 收尾写回、Resume 的 Notify 都不再
		// 触发常规规划轮;终局那一轮由协调器(settleTask)直接驱动,不走这里。
		if e.isSettling(t.ID) {
			return
		}
		// goalless（人工直投）分支：任务已无 open 目标时 planner 不跑——跑了会重判
		// met→cancelExec 杀掉用户经主 agent 直投的意图。是否结束改由 frontier 决定：
		// 还有 open/running 意图 → 保持 running、静默等待；意图已全部跑干 → 落 done。
		// 整段纯 Go、不触发任何 LLM 调用，也不打规划轮 marker。
		if open, err := t.Store.HasOpenGoal(); err == nil && !open {
			t.drainTriggers() // 丢弃累积的 done/finding 触发，避免 goalless 长会话里无界增长
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				// frontier 抽干且无在跑意图 → 收尾。用 Guarded 版做 CAS，避免踩到并发的
				// pause/delete/超时收尾的状态转换。
				if won, err := e.m.SetTaskStatusGuarded(t.ID, "done"); err != nil {
					log.Printf("[goalless] task %s 收尾落 done 失败: %v", t.ID, err)
				} else if won {
					e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
						Summary: "目标已全部达成，直投意图已执行完毕，任务结束"})
				}
			}
			return // goalless 分支永不进入 planner.Plan
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		defer e.decInflight(t.ID)
		e.stampFirstRun(t) // 首次真正规划 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
		if ectx.Err() != nil || e.IsDeleting(t.ID) {
			return
		}
		log.Printf("[planner] task %s 规划中…(%s 触发)", t.ID, src)
		// round marker: each Plan() is one planner round; emit a boundary so the
		// UI can separate rounds in the transcript (kind='round').
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
			Summary: fmt.Sprintf("第 %d 轮规划", e.nextPlannerRound(t.ID))})
		// what fired this round (worker done / finding; may be several — debounce
		// coalesces a burst; empty for time/heartbeat wakes).
		triggers := t.drainTriggers()
		taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
		e.BeginLLMCall(t.ID)
		met, reason, err := planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit)
		e.EndLLMCall(t.ID)
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
			e.cancelExec(t.ID, agent.AbortGoalMet)
		default:
			log.Printf("[planner] task %s 规划完成", t.ID)
		}
		e.touch(t.ID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			runRound("edge") // worker 结束 / finding / kill / resume / seed 首轮
		case <-heartbeat.C:
			// 周期兜底:死锁兜底 + 唤醒去监督飞行中的 worker(steer/kill) + 周期复查。
			runRound("heartbeat")
		}
		// 每次唤醒(边沿或心跳)后重臂心跳:任意规划触发都重算这段静置计时。
		resetPlannerTimer(heartbeat, interval)
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
			if waitWorker(ctx, t, 1500*time.Millisecond) {
				return
			}
			continue
		}
		if e.IsPaused(t.ID) {
			if waitWorker(ctx, t, 1000*time.Millisecond) {
				return
			}
			continue // user-paused: don't claim/execute intents
		}
		if e.IsDeleting(t.ID) {
			return
		}
		if e.isSettling(t.ID) {
			if waitWorker(ctx, t, 1000*time.Millisecond) {
				return
			}
			continue // 任务超时收尾中:不再领新意图(在跑的自行收尾,协调器等其 drain)
		}
		if isTerminalStatus(e.m.TaskStatus(t.ID)) {
			if waitWorker(ctx, t, 1000*time.Millisecond) {
				return
			}
			continue // 任务已终态(done/failed/timeout):停止领取遗留意图,别在完成后空跑 frontier
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		claimed := e.runWorkerStep(ctx, t, name, worker)
		e.decInflight(t.ID)
		if !claimed && waitWorker(ctx, t, 800*time.Millisecond) {
			return
		}
	}
}

// runWorkerStep claims and fully settles at most one intent. Its caller holds one
// task-operation admission for the whole claim/execute/state-write sequence so a
// delete cannot observe quiescence between the LLM return and the final DB writes.
func (e *Engine) runWorkerStep(ctx context.Context, t *Task, name string, worker *agent.Worker) bool {
	intent := e.claimNext(t, name)
	if intent == nil {
		return false
	}
	log.Printf("[worker %s] task %s 领取意图 #%d", name, t.ID, intent.ID)
	e.stampFirstRun(t) // 首次真正执行 → 盖 first_run_at + 算 deadline(仅带 timeout 的任务)
	e.touch(t.ID)
	emit := func(r db.Activity) { e.emitActivity(t, r) }
	ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; 带任务 deadline
	if ectx.Err() != nil || e.IsDeleting(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 领取后回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// per-work child context so the planner's kill_work can stop just this work.
	workCtx, workCancel := context.WithCancelCause(ectx)
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
	chatRequestID, chatMessage, hasChatMessage, chatErr := t.Store.PendingIntentInterventionMessage(intent.ID)
	if chatErr != nil {
		log.Printf("[worker %s] task %s 读取意图 #%d 待处理人工对话失败: %v", name, t.ID, intent.ID, chatErr)
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 人工对话读取失败后回退失败: %v", name, t.ID, intent.ID, err)
		}
		_, completeWork := e.detachWork(intent.ID)
		completeWork(chatErr)
		return true
	}
	e.BeginLLMCall(t.ID)
	var reason harness.TerminalReason
	var wrote agent.WriteCounts
	var err error
	if hasChatMessage {
		reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, chatRequestID, chatMessage)
	} else {
		reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
	}
	e.EndLLMCall(t.ID)
	if hasChatMessage {
		if markErr := t.Store.MarkIntentInterventionHandoffClaimed(intent.ID); markErr != nil {
			log.Printf("[worker %s] task %s 标记意图 #%d 人工对话已消费失败: %v", name, t.ID, intent.ID, markErr)
		}
	}
	// model_error 收场 → 额外重跑几次（退避后再试）。仅在意图仍属本 work、任务
	// 未暂停/未终止/未取消【且未进入收尾】时重试；否则让位给对应分支处理(收尾期不
	// 再重试,避免退避挤占其他 worker 的优雅收尾窗口)。
	for attempt := 1; attempt <= modelErrorRetries &&
		retryableWorkerModelError(reason, err) &&
		workCtx.Err() == nil && ectx.Err() == nil && !e.IsPaused(t.ID) && !e.isSettling(t.ID); attempt++ {
		log.Printf("[worker %s] task %s 意图 #%d model_error 收场，%v 后重试 (%d/%d)",
			name, t.ID, intent.ID, modelErrorRetryBackoff, attempt, modelErrorRetries)
		if sleepCtx(workCtx, modelErrorRetryBackoff) {
			break // 退避期间被取消（终止/暂停）→ 交给下方分支处理
		}
		e.BeginLLMCall(t.ID)
		if hasChatMessage {
			reason, wrote, err = worker.ExecuteWithMessage(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding, chatRequestID, chatMessage)
		} else {
			reason, wrote, err = worker.Execute(workCtx, name, wTaskID, e.m.assets, t.Store, intent, hooks, emit, e.m.enrich, t.NotifyFinding)
		}
		e.EndLLMCall(t.ID)
	}
	// Capture kill state before detachWork cancels workCtx. kill = this work's
	// ctx was cancelled (planner kill_work) while the TASK ctx kept running; a
	// pause cancels the task ctx (ectx) instead. Checking workCtx.Err() AFTER
	// unregister would always be true (unregister cancels it) → every completed
	// work would be wrongly marked stopped.
	workCause := context.Cause(workCtx)
	killed := workCtx.Err() != nil && ectx.Err() == nil
	action, completeWork := e.detachWork(intent.ID)
	// A caller may stop waiting and release its in-memory reservation before the
	// agent honors cancellation. The named context cause remains authoritative and
	// still settles the stopped run into a recoverable state.
	if action == "" {
		switch {
		case errors.Is(workCause, agent.AbortWorkPausedByUser):
			action = "pause"
		case errors.Is(workCause, agent.AbortWorkIntervenedByUser):
			action = "intervene"
		case errors.Is(workCause, agent.AbortWorkCancelledByUser):
			action = "cancel"
		}
	}
	var controlErr error
	defer func() { completeWork(controlErr) }()
	if action == "pause" {
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 暂停状态落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已暂停", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "intervene" || action == "intervene_no_handoff" {
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 对话暂停落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		// Install the directed barrier before completeWork releases ControlWork.
		// With one worker slot this prevents its next loop from taking unrelated
		// frontier work during the message hand-off.
		if action == "intervene" {
			e.queueDirectedClaim(t.ID, intent.ID)
		}
		log.Printf("[worker %s] task %s 意图 #%d 已暂停，等待新的对话意图", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	if action == "cancel" {
		// Park the stopped run in paused before handing cleanup to the API. If the
		// request disconnects after cancellation, the intent remains recoverable and
		// a later cancel can finish cleanup instead of leaving a phantom running row.
		controlErr = transitionIntentState(t.Store, intent.ID, "running", "paused")
		if controlErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 取消栅栏落库失败: %v", name, t.ID, intent.ID, controlErr)
			return true
		}
		log.Printf("[worker %s] task %s 意图 #%d 已停止，等待取消清理", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// if a pause cancelled this run mid-flight, return the intent to the frontier
	// so it is re-claimed on resume — the worker will resume the prior LLM
	// conversation from its transcript instead of restarting from scratch.
	if ectx.Err() != nil && taskExecutionPaused(context.Cause(ectx)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "open"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 任务暂停回退失败: %v", name, t.ID, intent.ID, err)
		}
		return true
	}
	// 任务超时收尾的硬兜底 cancel(非 pause、非 kill)取消了本 run → 归为 exhausted(已收尾),
	// 不要误标 blocked。此时 worker 通常已在 settlement 阶段把结果写回。
	if ectx.Err() != nil && e.isSettling(t.ID) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "exhausted"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 超时收尾状态落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务超时收尾结束(exhausted)，写回 %s", name, t.ID, intent.ID, wrote)
		e.touch(t.ID)
		return true
	}
	// 任务已判完成(done via 常规路径)→ 上面 cancelExec 取消了本 run。意图结果已无意义,
	// 标 stopped(不是 blocked),别污染已完成任务的意图状态。
	if ectx.Err() != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 因任务已完成而取消(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		return true
	}
	// killed by the planner: mark stopped (don't write back results, don't auto-reclaim).
	if killed {
		if err := transitionIntentState(t.Store, intent.ID, "running", "stopped"); err != nil {
			log.Printf("[worker %s] task %s 意图 #%d planner 停止落库失败: %v", name, t.ID, intent.ID, err)
		}
		log.Printf("[worker %s] task %s 意图 #%d 被终止(stopped)", name, t.ID, intent.ID)
		e.touch(t.ID)
		t.Notify()
		return true
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
	if state == "blocked" && isTaskLLMChainExhausted(err) {
		_ = t.Store.SetIntentBlockedReason(intent.ID, db.IntentBlockedLLMQuota)
	} else {
		if stateErr := transitionIntentState(t.Store, intent.ID, "running", state); stateErr != nil {
			log.Printf("[worker %s] task %s 意图 #%d 终态 %s 落库失败: %v", name, t.ID, intent.ID, state, stateErr)
		}
	}
	log.Printf("[worker %s] task %s 意图 #%d 结束: %s (写回 %s)", name, t.ID, intent.ID, state, wrote)
	e.touch(t.ID)
	t.NotifyDone(intent.ID) // results changed the graph -> wake the planner (with the just-finished intent id)
	return true
}

func taskExecutionPaused(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	switch abort.Code {
	case "paused_by_user", "paused_by_orchestrator", "paused_on_reload", "paused_race_guard",
		"queued_for_admission", "llm_unavailable_queued", "task_deleted":
		return true
	default:
		return false
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

func waitWorker(ctx context.Context, t *Task, d time.Duration) (done bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	var wake <-chan struct{}
	if t != nil {
		wake = t.workerWake
	}
	select {
	case <-ctx.Done():
		return true
	case <-wake:
		return false
	case <-timer.C:
		return false
	}
}

func (e *Engine) queueDirectedClaim(taskID string, intentID int64) {
	if taskID == "" || intentID <= 0 {
		return
	}
	e.directedMu.Lock()
	defer e.directedMu.Unlock()
	for _, id := range e.directedClaims[taskID] {
		if id == intentID {
			return
		}
	}
	e.directedClaims[taskID] = append(e.directedClaims[taskID], intentID)
	e.directedReady[intentID] = false
}

func (e *Engine) readyDirectedClaim(intentID int64) {
	e.directedMu.Lock()
	if _, exists := e.directedReady[intentID]; exists {
		e.directedReady[intentID] = true
	}
	e.directedMu.Unlock()
}

func (e *Engine) removeDirectedClaim(taskID string, intentID int64) {
	e.directedMu.Lock()
	defer e.directedMu.Unlock()
	items := e.directedClaims[taskID]
	for i, id := range items {
		if id != intentID {
			continue
		}
		items = append(items[:i], items[i+1:]...)
		delete(e.directedReady, intentID)
		if len(items) == 0 {
			delete(e.directedClaims, taskID)
		} else {
			e.directedClaims[taskID] = items
		}
		return
	}
}

// claimDirected tries every pending human-message hand-off before ordinary work
// frontier. blocked=true means at least one hand-off is still paused/running;
// callers must not let a just-released single worker slot overtake it.
func (e *Engine) claimDirected(t *Task, name string) (intent *db.Node, blocked bool) {
	e.directedMu.Lock()
	ids := append([]int64(nil), e.directedClaims[t.ID]...)
	ready := make(map[int64]bool, len(ids))
	for _, id := range ids {
		ready[id] = e.directedReady[id]
	}
	e.directedMu.Unlock()
	for _, id := range ids {
		node, err := t.Store.GetNode(id)
		if err != nil {
			return nil, true
		}
		if node == nil {
			e.removeDirectedClaim(t.ID, id)
			continue
		}
		switch node.State {
		case "open":
			if !ready[id] {
				blocked = true
				continue
			}
			ok, err := t.Store.ClaimIntent(id, name)
			if err != nil {
				return nil, true
			}
			if ok {
				e.removeDirectedClaim(t.ID, id)
				node.State, node.Owner = "running", name
				return node, false
			}
			blocked = true
		case "paused", "running":
			blocked = true
		default:
			e.removeDirectedClaim(t.ID, id)
		}
	}
	return nil, blocked
}

func (e *Engine) claimNext(t *Task, name string) *db.Node {
	if intent, blocked := e.claimDirected(t, name); intent != nil || blocked {
		return intent
	}
	fr, _ := t.Store.Frontier(20)
	for _, in := range fr {
		if ok, _ := t.Store.ClaimIntent(in.ID, name); ok {
			return in
		}
	}
	return nil
}
