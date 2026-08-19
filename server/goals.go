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
	"github.com/Autumn-27/norma/llm"
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
	s.seed(t, seedText)
	if seedFirstIntent {
		s.seedFirstIntent(t)
	}
	// 并发上限:满了就挂起排队(不做目标拆解、不启动引擎),由 reconcileConcurrency 补位启动。
	if s.queueIfAtCapacity(t) {
		return
	}
	go s.startTaskEngine(t)
}

// startTaskEngine does the actual running work: 第0轮目标拆解 + 启动引擎循环。
// Shared by fresh launches and by the concurrency reconciler promoting a queued task.
func (s *Server) startTaskEngine(t *Task) {
	s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
		Summary: "第 0 轮目标拆解"})
	goals := s.createGoals(s.ctx, t, func(r db.Activity) {
		s.engine.emitActivity(t, r)
	})
	for _, g := range goals {
		summary := g.Text
		if g.VulnClass != "" {
			summary = fmt.Sprintf("[%s] %s", g.VulnClass, g.Text)
		}
		s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "text", Summary: summary})
	}
	s.engine.Run(s.ctx, t)
}

// occupiesSlot reports whether a task counts against the concurrency cap: it is
// live (not terminal), not queued, and not paused. A just-created / decomposing
// task counts too (it will run momentarily).
func (s *Server) occupiesSlot(t *Task) bool {
	if t == nil || t.Queued || isTerminalStatus(t.Status) {
		return false
	}
	return !t.Paused && !s.engine.IsPaused(t.ID)
}

// runningTaskCount counts tasks occupying a concurrency slot, excluding excludeID
// (pass the candidate's id when deciding admission so it doesn't count itself).
func (s *Server) runningTaskCount(excludeID string) int {
	n := 0
	for _, t := range s.m.List() {
		if t.ID == excludeID {
			continue
		}
		if s.occupiesSlot(t) {
			n++
		}
	}
	return n
}

// queueIfAtCapacity holds a task in the queued state when the concurrency cap is
// enabled and already full. Returns true if the task was queued (caller must not
// start it). No-op (false) when the feature is off or a slot is free.
func (s *Server) queueIfAtCapacity(t *Task) bool {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	enabled, limit := s.m.ConcurrencyLimit()
	if !enabled || s.runningTaskCount(t.ID) < limit {
		return false
	}
	if err := s.m.SetTaskQueued(t.ID, true); err != nil {
		log.Printf("[concurrency] task %s 置排队失败: %v", t.ID, err)
		return false // 落库失败宁可放它跑,也别静默丢任务
	}
	t.Queued = true
	s.engine.emitActivity(t, db.Activity{Worker: "planner", Kind: "text",
		Summary: fmt.Sprintf("已排队：达到并发上限 %d，等待空位后自动开始", limit)})
	log.Printf("[concurrency] task %s 排队(并发上限 %d)", t.ID, limit)
	return true
}

// reconcileConcurrency promotes queued tasks into free slots. Runs on every
// scheduler tick. When the feature is disabled it releases ALL queued tasks;
// when enabled it starts the oldest-queued tasks up to (limit - running).
func (s *Server) reconcileConcurrency() {
	s.concMu.Lock()
	defer s.concMu.Unlock()
	var queued []*Task
	for _, t := range s.m.List() {
		if t.Queued && !isTerminalStatus(t.Status) {
			queued = append(queued, t)
		}
	}
	if len(queued) == 0 {
		return
	}
	// 最早排队的先启动。
	sort.Slice(queued, func(i, j int) bool { return queued[i].CreatedAt < queued[j].CreatedAt })

	enabled, limit := s.m.ConcurrencyLimit()
	slots := len(queued) // feature off → 全部放行
	if enabled {
		if !s.engine.Ready() {
			return // 引擎未就绪(无 LLM):继续挂起,别空跑
		}
		slots = limit - s.runningTaskCount("")
	}
	for _, t := range queued {
		if slots <= 0 {
			break
		}
		if err := s.m.SetTaskQueued(t.ID, false); err != nil {
			log.Printf("[concurrency] task %s 解除排队失败: %v", t.ID, err)
			continue
		}
		t.Queued = false
		log.Printf("[concurrency] task %s 出队启动", t.ID)
		go s.startTaskEngine(t)
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
	// 排队中的任务不复活启动——它本就等待并发空位,交给 reconcileConcurrency。
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
	// Use the SAME LLM the task runs on (its pinned profile, else the active profile),
	// NOT agent.FromEnv() — the LLM is configured via the UI (DB profile), not env vars,
	// so FromEnv returned empty and every task silently fell back to the crude rule
	// splitter (which shredded URLs / made meaningless 2-way splits).
	var specs []goalSpec
	if prov, ok := s.goalsProvider(t); ok {
		var as *db.AssetStore
		if s.m != nil {
			as = s.m.Assets()
		}
		taskID, _ := strconv.ParseInt(t.ID, 10, 64)
		// DecomposeGoals persists each decomposed goal via the set_goals tool straight
		// into t.Store; it returns the specs it wrote so we can emit activity + spot the
		// empty (LLM error / no provider) case below. No write-back needed here anymore.
		for _, g := range agent.DecomposeGoals(ctx, prov, s.m.dir, t.Goal, t.Description, as, t.Store, taskID, emit) {
			if strings.TrimSpace(g.Text) != "" {
				specs = append(specs, goalSpec{Text: g.Text, VulnClass: g.VulnClass})
			}
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

// goalsProvider resolves the provider for goal decomposition by the standard
// precedence: the goals agent's own binding → the task's pinned profile → the
// global active provider (the very instance the engine runs on, so decomposition
// shares its rate limiter, gets recorded, and follows LLM failover).
func (s *Server) goalsProvider(t *Task) (llm.Provider, bool) {
	var pin *int64
	if t != nil {
		pin = t.LLMProfileID
	}
	if eff := s.effectiveProfileForAgent("goals", pin); eff != nil {
		if prov, cfg, ok := s.providerForProfile(*eff); ok {
			return s.poolForBinding(*eff, prov, cfg), true
		}
	}
	s.cfgMu.Lock()
	prov, on := s.llmProv, s.llmOn
	s.cfgMu.Unlock()
	return prov, on && prov != nil
}
