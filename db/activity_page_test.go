package db

import "testing"

// TestActivityPageSessions covers the reverse-paginated, per-session history added
// for the SSE remediation: Main/Plan/Worker filtering, before-cursor paging without
// gaps/overlap, hasMore, and the task-level snapshot cursor. Mirrors docs §11.1.
func TestActivityPageSessions(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "分页历史")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intentA, err := es.AddIntent(map[string]any{"summary": "intent A"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	intentB, err := es.AddIntent(map[string]any{"summary": "intent B"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	// Interleave a mix of agents so a session filter must actually discriminate.
	// 25 main, 25 planner (Goal+Planner share worker=planner), 30 workerA, 5 workerB.
	appendN := func(n int, a Activity) {
		for range n {
			if _, err := es.AppendActivity(a); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Interleaving order matters: emit round-robin-ish so ids of one session are
	// scattered, proving the WHERE filter (not a contiguous range) is what selects.
	for range 25 {
		appendN(1, Activity{Worker: "mainagent", Kind: "text", Summary: "m"})
		appendN(1, Activity{Worker: "planner", Kind: "text", Summary: "p"})
		appendN(1, Activity{NodeID: &intentA, Worker: "work#1", Kind: "text", Summary: "a"})
	}
	appendN(5, Activity{NodeID: &intentA, Worker: "work#1", Kind: "text", Summary: "a2"}) // workerA → 30 total
	appendN(5, Activity{NodeID: &intentB, Worker: "work#2", Kind: "text", Summary: "b"})

	// snapshot cursor = max id across the whole task.
	snap, err := es.ActivityMaxID()
	if err != nil {
		t.Fatal(err)
	}

	// Helper: page through a whole session backward and assert coverage.
	collect := func(f ActivitySessionFilter, pageSize int) []Activity {
		var all []Activity
		before := int64(0)
		seen := map[int64]bool{}
		for {
			items, hasMore, err := es.ActivityPage(f, before, pageSize)
			if err != nil {
				t.Fatal(err)
			}
			// ascending order within a page
			for i := 1; i < len(items); i++ {
				if items[i-1].ID >= items[i].ID {
					t.Fatalf("page not ascending: %d >= %d", items[i-1].ID, items[i].ID)
				}
			}
			// no overlap across pages
			for _, a := range items {
				if seen[a.ID] {
					t.Fatalf("duplicate id %d across pages", a.ID)
				}
				seen[a.ID] = true
			}
			all = append([]Activity{}, append(items, all...)...) // prepend older page
			if !hasMore || len(items) == 0 {
				break
			}
			before = items[0].ID
		}
		return all
	}

	main := collect(ActivitySessionFilter{Worker: "mainagent"}, 10)
	if len(main) != 25 {
		t.Fatalf("main count = %d, want 25", len(main))
	}
	plan := collect(ActivitySessionFilter{Worker: "planner"}, 7)
	if len(plan) != 25 {
		t.Fatalf("plan count = %d, want 25", len(plan))
	}
	wa := collect(ActivitySessionFilter{NodeID: &intentA}, 8)
	if len(wa) != 30 {
		t.Fatalf("workerA count = %d, want 30", len(wa))
	}
	wb := collect(ActivitySessionFilter{NodeID: &intentB}, 8)
	if len(wb) != 5 {
		t.Fatalf("workerB count = %d, want 5", len(wb))
	}
	// full ascending order across the reconstructed session
	for i := 1; i < len(wa); i++ {
		if wa[i-1].ID >= wa[i].ID {
			t.Fatalf("reconstructed session not ascending at %d", i)
		}
	}
	// latest page (before=0) must include the session's newest record.
	latest, hasMore, err := es.ActivityPage(ActivitySessionFilter{NodeID: &intentA}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Fatalf("workerA should have more than one page")
	}
	if latest[len(latest)-1].ID != wa[len(wa)-1].ID {
		t.Fatalf("latest page missing newest record")
	}
	// snapshot cursor is the whole-task max, ≥ any session's max.
	if snap < wa[len(wa)-1].ID {
		t.Fatalf("snapshot %d < workerA max %d", snap, wa[len(wa)-1].ID)
	}
}

// TestListByKindPage covers the paged worker(intent) list that lets the session list
// reach past the old fixed 300 cap (docs §8 / §11.1 item 10).
func TestListByKindPage(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "意图分页")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	const total = 25
	for range total {
		if _, err := es.AddIntent(map[string]any{"summary": "i"}, 1, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	// Page backward in chunks of 10; expect 10,10,5 and hasMore false on last.
	seen := map[int64]bool{}
	before := int64(0)
	pages := 0
	for {
		items, hasMore, err := es.ListByKindPage(KindIntent, before, 10)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, n := range items {
			if seen[n.ID] {
				t.Fatalf("dup intent %d across pages", n.ID)
			}
			seen[n.ID] = true
		}
		if len(items) == 0 || !hasMore {
			break
		}
		// newest-first within a page → the oldest (smallest id) is last; page older
		// history before it next.
		before = items[len(items)-1].ID
	}
	if len(seen) != total {
		t.Fatalf("paged intents = %d, want %d", len(seen), total)
	}
	if pages < 3 {
		t.Fatalf("expected ≥3 pages for %d items at size 10, got %d", total, pages)
	}
}
