package db

import (
	"testing"
	"time"
)

func TestTaskQueuePreservesBootstrapAndFIFOPosition(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("queue metadata", "keep first-run mode", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)

	if err := d.Enqueue(task.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	first, err := d.GetTask(task.ID)
	if err != nil || first == nil || first.QueuedAt == nil {
		t.Fatalf("first enqueue: task=%+v err=%v", first, err)
	}

	// A follow-up/rerun can try to admit an already queued task as resume. The
	// original bootstrap mode and FIFO timestamp must remain authoritative.
	time.Sleep(time.Millisecond)
	if err := d.Enqueue(task.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	second, err := d.GetTask(task.ID)
	if err != nil || second == nil {
		t.Fatalf("second enqueue: task=%+v err=%v", second, err)
	}
	if second.QueueMode != "bootstrap" {
		t.Fatalf("queue mode=%q, want bootstrap", second.QueueMode)
	}
	if second.QueuedAt == nil || !second.QueuedAt.Equal(*first.QueuedAt) {
		t.Fatalf("repeated enqueue moved FIFO position: first=%v second=%v", first.QueuedAt, second.QueuedAt)
	}

	// Pausing a queued task removes it from the queue but keeps its required
	// startup mode. A later requeue receives a new tail position.
	if err := d.Dequeue(task.ID, false); err != nil {
		t.Fatal(err)
	}
	paused, err := d.GetTask(task.ID)
	if err != nil || paused == nil || paused.Queued || paused.QueueMode != "bootstrap" {
		t.Fatalf("paused queue metadata: task=%+v err=%v", paused, err)
	}
	time.Sleep(time.Millisecond)
	if err := d.Enqueue(task.ID, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	requeued, err := d.GetTask(task.ID)
	if err != nil || requeued == nil || requeued.QueuedAt == nil {
		t.Fatalf("requeue: task=%+v err=%v", requeued, err)
	}
	if !requeued.QueuedAt.After(*first.QueuedAt) {
		t.Fatalf("requeue did not move to FIFO tail: first=%v requeued=%v", first.QueuedAt, requeued.QueuedAt)
	}
}
