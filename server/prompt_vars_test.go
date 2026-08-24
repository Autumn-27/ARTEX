package server

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

// A stored catalog entry that collides with a global runtime var (e.g. a legacy
// seeded goals."Now") must not produce a duplicate: the global wins and the name
// appears exactly once, so the UI never sees duplicate React keys.
func TestWithGlobalVarsDedupesCollidingNames(t *testing.T) {
	t.Parallel()
	stored := []db.PromptVar{
		{Name: "EngagementDescription", Description: "task", Source: "exploration"},
		{Name: "Now", Description: "stale per-agent copy", Source: "runtime"},
	}
	out := withGlobalVars(stored)

	counts := map[string]int{}
	var now db.PromptVar
	for _, v := range out {
		counts[v.Name]++
		if v.Name == "Now" {
			now = v
		}
	}
	if counts["Now"] != 1 {
		t.Fatalf("Now appeared %d times, want 1: %+v", counts["Now"], out)
	}
	if counts["EngagementDescription"] != 1 {
		t.Fatalf("non-colliding stored var was dropped or duplicated: %+v", out)
	}
	// The surviving Now must be the authoritative global definition, not the stale
	// stored one.
	if now.Description == "stale per-agent copy" {
		t.Fatalf("stored var shadowed the global instead of the other way around: %+v", now)
	}
	// Every global is present exactly once.
	for _, g := range globalPromptVars {
		if counts[g.Name] != 1 {
			t.Fatalf("global %q present %d times, want 1", g.Name, counts[g.Name])
		}
	}
}

// The common case (no collisions) keeps every stored var and appends all globals.
func TestWithGlobalVarsKeepsDistinctVars(t *testing.T) {
	t.Parallel()
	stored := []db.PromptVar{{Name: "Goal", Source: "exploration"}}
	out := withGlobalVars(stored)
	if len(out) != len(stored)+len(globalPromptVars) {
		t.Fatalf("len=%d, want %d: %+v", len(out), len(stored)+len(globalPromptVars), out)
	}
	if out[0].Name != "Goal" {
		t.Fatalf("stored var order not preserved: %+v", out)
	}
}
