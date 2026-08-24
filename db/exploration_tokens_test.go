package db

import (
	"fmt"
	"testing"
)

func TestTokenStatsBySessionUsesCompleteIntentHistory(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("token sessions", "aggregate complete session history")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	store := d.Exploration(expID)

	intentID, err := store.AddIntent(map[string]any{"summary": "worker session"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	factID, err := store.AddNode(KindFact, map[string]any{"summary": "not a worker"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	otherExpID, err := d.CreateExploration("other token sessions", "foreign intent must not match")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, otherExpID)
	foreignIntentID, err := d.Exploration(otherExpID).AddIntent(map[string]any{"summary": "foreign worker"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	appendResult := func(nodeID *int64, worker string, input, output, read, write int) {
		t.Helper()
		if _, appendErr := store.AppendActivity(Activity{
			NodeID:           nodeID,
			Worker:           worker,
			Kind:             "result",
			InputTokens:      &input,
			OutputTokens:     &output,
			CacheReadTokens:  &read,
			CacheWriteTokens: &write,
		}); appendErr != nil {
			t.Fatal(appendErr)
		}
	}

	appendResult(nil, "mainagent", 11, 12, 13, 14)
	appendResult(nil, "planner", 21, 22, 23, 24)
	// More than ActivityPage's default limit proves this aggregate reads the
	// persisted history directly instead of summing the currently loaded page.
	const completedRuns = 205
	for i := 0; i < completedRuns; i++ {
		appendResult(&intentID, fmt.Sprintf("work#%d", i%3+1), 1, 2, 3, 4)
	}

	// All three rows remain part of the legacy worker/whole-task totals, but none
	// represents a local Worker session and therefore none may create intent:*.
	appendResult(&factID, "work#fact", 31, 32, 33, 34)
	appendResult(nil, "work#missing-node", 41, 42, 43, 44)
	appendResult(&foreignIntentID, "work#foreign", 51, 52, 53, 54)
	ignoredInput := 1000
	if _, err := store.AppendActivity(Activity{
		NodeID:      &intentID,
		Worker:      "work#1",
		Kind:        "usage",
		InputTokens: &ignoredInput,
	}); err != nil {
		t.Fatal(err)
	}

	sessions, err := store.TokenStatsBySession()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]SessionTokenUsage, len(sessions))
	for _, usage := range sessions {
		got[usage.Session] = usage
	}
	if len(got) != 3 {
		t.Fatalf("sessions = %+v, want only main, plan, and the local intent", sessions)
	}
	assertSessionTokens(t, got["main"], "main", 11, 12, 13, 14)
	assertSessionTokens(t, got["plan"], "plan", 21, 22, 23, 24)
	assertSessionTokens(t, got[fmt.Sprintf("intent:%d", intentID)], fmt.Sprintf("intent:%d", intentID),
		completedRuns, completedRuns*2, completedRuns*3, completedRuns*4)

	workers, err := store.TokenStatsByWorker()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 8 { // main, planner, 3 executors, and the 3 deliberately invalid session rows
		t.Fatalf("legacy workers changed: got %d entries: %+v", len(workers), workers)
	}
	total, err := store.TokenTotal()
	if err != nil {
		t.Fatal(err)
	}
	assertTokenUsage(t, total,
		11+21+completedRuns+31+41+51,
		12+22+completedRuns*2+32+42+52,
		13+23+completedRuns*3+33+43+53,
		14+24+completedRuns*4+34+44+54)
}

func assertSessionTokens(t *testing.T, got SessionTokenUsage, session string, input, output, read, write int) {
	t.Helper()
	if got.Session != session || got.InputTokens != input || got.OutputTokens != output ||
		got.CacheReadTokens != read || got.CacheWriteTokens != write {
		t.Fatalf("session %q = %+v, want input=%d output=%d cache-read=%d cache-write=%d",
			session, got, input, output, read, write)
	}
}

func assertTokenUsage(t *testing.T, got TokenUsage, input, output, read, write int) {
	t.Helper()
	if got.InputTokens != input || got.OutputTokens != output ||
		got.CacheReadTokens != read || got.CacheWriteTokens != write {
		t.Fatalf("token total = %+v, want input=%d output=%d cache-read=%d cache-write=%d",
			got, input, output, read, write)
	}
}
