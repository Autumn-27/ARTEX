package server

import (
	"context"
	"log"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

type goalSpec struct {
	Text      string
	VulnClass string
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
		for _, g := range agent.DecomposeGoals(ctx, cfg, t.Goal, t.Description, emit) {
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

// goalsLLMConfig resolves the LLM config for goal decomposition: the task's pinned
// profile if any, else the global active profile (same source the engine runs on).
func (s *Server) goalsLLMConfig(t *Task) (agent.Config, bool) {
	if t != nil && t.LLMProfileID != nil {
		if cfg, ok := s.loadProfileConfig(*t.LLMProfileID); ok {
			return cfg, true
		}
	}
	return s.loadLLMConfig()
}
