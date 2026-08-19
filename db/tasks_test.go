package db

import "testing"

func TestTaskLifecycleAndDeleteCascade(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("迁移测试", "目标X", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// populate the exploration subgraph
	es := d.Exploration(tk.ExplorationID)
	if _, err := es.AddIntent(map[string]any{"summary": "x"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}

	// pause + status
	if err := d.SetPaused(tk.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTask(tk.ID)
	if err != nil || got == nil || !got.Paused {
		t.Fatalf("paused not persisted: %+v err=%v", got, err)
	}
	if got.Queued {
		t.Fatalf("new task should not be queued: %+v", got)
	}

	// queued (concurrency-hold) flag round-trips independently of paused
	if err := d.SetQueued(tk.ID, true); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g == nil || !g.Queued {
		t.Fatalf("queued not persisted: %+v", g)
	}
	if err := d.SetQueued(tk.ID, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g == nil || g.Queued {
		t.Fatalf("queued not cleared: %+v", g)
	}

	// list contains it
	list, _ := d.ListTasks()
	found := false
	for _, x := range list {
		if x.ID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task not in list")
	}

	// delete cascades exploration subgraph
	if err := d.DeleteTask(tk.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g != nil {
		t.Fatalf("task should be gone")
	}
	var nodes int
	d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, tk.ExplorationID).Scan(&nodes)
	if nodes != 0 {
		t.Fatalf("exploration nodes should be cascade-deleted, got %d", nodes)
	}
	var exps int
	d.QueryRow(`SELECT count(*) FROM explorations WHERE id=$1`, tk.ExplorationID).Scan(&exps)
	if exps != 0 {
		t.Fatalf("exploration should be deleted, got %d", exps)
	}
}
