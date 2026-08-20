package server

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type goalSpec struct {
	Text      string
	VulnClass string
}

// launchTask runs the shared post-creation sequence for a task created via ANY
// path (HTTP createTask 或 orchestration spawn_task),避免两处复制粘贴:
//  1. seed 根资产,喂给事件驱动 loop;
//  2. 可选种子意图,worker 免等首轮 planner 直接开跑;
//  3. 后台异步做目标分解(发「第 0 轮目标拆解」round + LLM 分解步骤 + 逐条 goal,页面可见),
//     分解完再 engine.Run —— 引擎在 goal 节点就绪后才启动,避免 planner 抢在 goal 之前跑的竞态。
//
// 异步(goroutine)所以调用方立即返回,两条路径行为一致:秒建任务、后台拆目标。
func (s *Server) launchTask(t *Task, seedText string, seedFirstIntent bool) {
	if !s.engine.beginTaskOperation(t.ID) {
		return
	}
	s.seed(t, seedText)
	if seedFirstIntent {
		s.seedFirstIntent(t)
	}
	if s.queueIfAtCapacity(t) {
		s.engine.decInflight(t.ID)
		return
	}
	go func() {
		defer s.engine.decInflight(t.ID)
		s.startTaskEngine(t)
	}()
}

func (s *Server) startTaskEngine(t *Task) {
	ctx := s.engine.execContextFor(s.ctx, t.ID)
	if ctx.Err() != nil || s.engine.IsDeleting(t.ID) {
		return
	}
	s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: "第 0 轮目标拆解"})
	goals := s.createGoals(ctx, t, func(r db.Activity) {
		s.engine.emitActivity(t, r)
	})
	if ctx.Err() != nil || s.engine.IsDeleting(t.ID) {
		return
	}
	for _, g := range goals {
		summary := g.Text
		if g.VulnClass != "" {
			summary = fmt.Sprintf("[%s] %s", g.VulnClass, g.Text)
		}
		s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "text", Summary: summary})
	}
	if ctx.Err() != nil || s.engine.IsDeleting(t.ID) {
		return
	}
	s.engine.Run(s.ctx, t)
}

func (s *Server) occupiesConcurrencySlot(t *Task) bool {
	return t != nil && !t.Queued && !t.Paused && !s.engine.IsPaused(t.ID) && !isTerminalStatus(t.Status)
}

func (s *Server) runningTaskCount(excludeID string) int {
	count := 0
	for _, task := range s.m.List() {
		if task.ID != excludeID && s.occupiesConcurrencySlot(task) {
			count++
		}
	}
	return count
}

func (s *Server) queueIfAtCapacity(t *Task) bool {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	enabled, limit := s.m.ConcurrencyLimit()
	if !enabled || s.runningTaskCount(t.ID) < limit {
		return false
	}
	if err := s.m.SetTaskQueued(t.ID, true); err != nil {
		log.Printf("[concurrency] task %s 置排队失败: %v", t.ID, err)
		return false
	}
	s.engine.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
		Summary: fmt.Sprintf("已排队：达到并发上限 %d，等待空位后自动开始", limit)})
	return true
}

func (s *Server) reconcileConcurrency() {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	queued := []*Task{}
	for _, task := range s.m.List() {
		if task.Queued && !isTerminalStatus(task.Status) {
			queued = append(queued, task)
		}
	}
	sort.Slice(queued, func(i, j int) bool { return queued[i].CreatedAt < queued[j].CreatedAt })
	enabled, limit := s.m.ConcurrencyLimit()
	slots := len(queued)
	if enabled {
		slots = limit - s.runningTaskCount("")
	}
	for _, task := range queued {
		if slots <= 0 {
			break
		}
		// Keep FIFO order, but do not consume a concurrency slot for a task
		// whose explicit chain/global provider is unavailable. It will be retried
		// after the user configures or resets its LLM chain.
		if task.Paused || !s.engine.ReadyFor(task) {
			continue
		}
		if !s.engine.beginTaskOperation(task.ID) {
			continue
		}
		if err := s.m.SetTaskQueued(task.ID, false); err != nil {
			s.engine.decInflight(task.ID)
			continue
		}
		go func(t *Task) {
			defer s.engine.decInflight(t.ID)
			s.startTaskEngine(t)
		}(task)
		slots--
	}
}

// reviveTask 让一个已停下的任务重新跑起来:把终态(done/failed/timeout)拉回 running、
// 解除暂停,并(重)启动引擎循环 + 唤醒。已在 running 且未暂停的任务:只剩 Run 里的一次
// Notify,近乎无副作用。用于「主 agent set_goals 新增目标」和「重跑 blocked 意图」两处。
//
// 为什么必须显式复活:planner/worker 循环的终态门(engine.go)会吞掉普通 notify——光改
// 图 + Notify 唤不醒已判完成的任务;重启后终态任务的 goroutine 也可能已不在,故还要 Run。
func (s *Server) reviveTask(t *Task) {
	if t == nil {
		return
	}
	if t.Queued {
		return
	}
	// 终态 → 拉回 running(SetTaskStatus 会清 completed_at 并同步内存 handle 的 Status,
	// 使 planner/worker 循环的终态门放行)。
	if isTerminalStatus(t.Status) {
		if err := s.m.SetTaskStatus(t.ID, "running"); err != nil {
			log.Printf("[revive] task %s 置 running 失败: %v", t.ID, err)
		}
	}
	// 确保引擎循环存活:已 started 只会 Notify;未 started(重启后未恢复的终态任务)则重起循环。
	s.engine.Run(s.ctx, t)
	// 暂停中 → 解除暂停(清引擎内存态 + Notify),并持久化 paused=false(存活过重启)。
	if t.Paused || s.engine.IsPaused(t.ID) {
		s.engine.Resume(t)
		t.Paused = false
		if err := s.m.SetTaskPaused(t.ID, false); err != nil {
			log.Printf("[revive] task %s 解除暂停失败: %v", t.ID, err)
		}
	}
}

// createGoals materializes the goal node(s) under the task root (rel objective).
// Decomposition is done ENTIRELY by the LLM (the project requires an LLM). There is
// no rule-based fallback splitter — it only ever produced garbage (shredded URLs,
// meaningless 2-way splits). If the LLM yields nothing (an error), the raw task goal
// is used verbatim as a single goal so the task still has something to judge against.
// Returns the seeded specs so callers can emit activity records for them.
// emit, when non-nil, is forwarded to DecomposeGoals so LLM steps are visible in the UI.
func (s *Server) createGoals(ctx context.Context, t *Task, emit func(db.Activity)) []goalSpec {
	if t == nil {
		return nil
	}
	// Use the SAME LLM the task runs on (its pinned profile, else the active profile),
	// NOT agent.FromEnv() — the LLM is configured via the UI (DB profile), not env vars,
	// so FromEnv returned empty and every task silently fell back to the crude rule
	// splitter (which shredded URLs / made meaningless 2-way splits).
	var specs []goalSpec
	var as *db.AssetStore
	if s.m != nil {
		as = s.m.Assets()
	}
	taskID, _ := strconv.ParseInt(t.ID, 10, 64)
	s.engine.BeginLLMCall(t.ID)
	decomposed := agent.DecomposeGoalsWithProvider(ctx, s.agentsForTask(t).runtime, s.m.dir, t.Goal, t.Description, as, t.Store, taskID, emit)
	s.engine.EndLLMCall(t.ID)
	for _, g := range decomposed {
		if strings.TrimSpace(g.Text) != "" {
			specs = append(specs, goalSpec{Text: g.Text, VulnClass: g.VulnClass})
		}
	}
	if len(specs) == 0 {
		// No decomposed goals (LLM error / no provider): use the raw task goal verbatim
		// as a single goal so the task still has something to judge against. This is the
		// only path that writes here — decomposed goals are already persisted by the tool.
		if g := strings.TrimSpace(t.Goal); g != "" {
			log.Printf("[goals] task %s: LLM 目标拆解无产出，回退为「原始目标作为单目标」", t.ID)
			origin, _ := t.Store.OriginFactID()
			id, _ := t.Store.AddNode(db.KindGoal, map[string]any{"text": g}, 0, "open", "system", nil)
			if origin > 0 && id > 0 {
				_ = t.Store.Link(origin, db.RelSpawns, id) // goal descends from the task root (origin fact)
			}
			specs = []goalSpec{{Text: g}}
		}
	}
	return specs
}
