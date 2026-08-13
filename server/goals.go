package server

import (
	"context"
	"fmt"
	"log"
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
	s.seed(t, seedText)
	if seedFirstIntent {
		s.seedFirstIntent(t)
	}
	go func() {
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
	}()
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
	if cfg, ok := s.goalsLLMConfig(t); ok {
		var as *db.AssetStore
		if s.m != nil {
			as = s.m.Assets()
		}
		taskID, _ := strconv.ParseInt(t.ID, 10, 64)
		for _, g := range agent.DecomposeGoals(ctx, cfg, t.Goal, t.Description, as, taskID, emit) {
			if strings.TrimSpace(g.Text) != "" {
				specs = append(specs, goalSpec{Text: g.Text, VulnClass: g.VulnClass})
			}
		}
	}
	if len(specs) == 0 {
		// No rule-based splitting: use the raw task goal verbatim as a single goal
		// (only reached on an LLM error — normal runs get real decomposed goals).
		if g := strings.TrimSpace(t.Goal); g != "" {
			log.Printf("[goals] task %s: LLM 目标拆解无产出，回退为「原始目标作为单目标」", t.ID)
			specs = []goalSpec{{Text: g}}
		}
	}
	origin, _ := t.Store.OriginFactID()
	for _, g := range specs {
		payload := map[string]any{"text": g.Text}
		if g.VulnClass != "" {
			payload["vulnclass"] = g.VulnClass
		}
		id, _ := t.Store.AddNode(db.KindGoal, payload, 0, "open", "system", nil)
		if origin > 0 && id > 0 {
			_ = t.Store.Link(origin, db.RelSpawns, id) // goals descend from the task root (origin fact)
		}
	}
	return specs
}

// goalsLLMConfig resolves the LLM config for goal decomposition by the standard
// precedence: the goals agent's own binding → the task's pinned profile → the global
// active profile (same source the engine runs on).
func (s *Server) goalsLLMConfig(t *Task) (agent.Config, bool) {
	var pin *int64
	if t != nil {
		pin = t.LLMProfileID
	}
	if eff := s.effectiveProfileForAgent("goals", pin); eff != nil {
		if cfg, ok := s.loadProfileConfig(*eff); ok {
			return cfg, true
		}
	}
	return s.loadLLMConfig()
}
