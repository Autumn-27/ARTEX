package db

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func newRunningInterventionIntent(t *testing.T, d *DB, label string) (*ExplorationStore, int64, func()) {
	t.Helper()
	task, err := d.CreateTask("worker intervention "+label, "verify intervention protocol", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	intentID, err := store.AddIntent(map[string]any{"summary": label}, 1, nil, "planner")
	if err != nil {
		_ = d.DeleteTask(task.ID)
		t.Fatal(err)
	}
	if claimed, err := store.ClaimIntent(intentID, "work#1"); err != nil || !claimed {
		_ = d.DeleteTask(task.ID)
		t.Fatalf("claim intervention intent: claimed=%v err=%v", claimed, err)
	}
	return store, intentID, func() { _ = d.DeleteTask(task.ID) }
}

func TestIntentInterventionReservationAndAcceptanceAreIdempotent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	store, intentID, cleanup := newRunningInterventionIntent(t, d, "idempotent")
	defer cleanup()

	const requestID = "intervention-idempotent-1"
	const message = "停止枚举，优先验证登录越权"
	reserved, err := store.ReserveIntentIntervention(intentID, requestID, message)
	if err != nil {
		t.Fatal(err)
	}
	if reserved.Status != IntentInterventionPending || reserved.ActivityID <= 0 || reserved.IntentState != "running" {
		t.Fatalf("unexpected reservation: %+v", reserved)
	}

	replayed, err := store.ReserveIntentIntervention(intentID, requestID, message)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ActivityID != reserved.ActivityID || replayed.Status != IntentInterventionPending {
		t.Fatalf("reservation retry created a different operation: first=%+v retry=%+v", reserved, replayed)
	}
	if _, err := store.ReserveIntentIntervention(intentID, requestID, "改成另一条 Worker 消息"); !errors.Is(err, ErrIntentInterventionConflict) {
		t.Fatalf("same request_id with different message error=%v, want intervention conflict", err)
	}
	if _, err := store.ReserveIntentIntervention(intentID, "intervention-other", message); !errors.Is(err, ErrIntentInterventionConflict) {
		t.Fatalf("second pending request error=%v, want intervention conflict", err)
	}

	if changed, err := store.CompareAndSetIntentState(intentID, "running", "paused"); err != nil || !changed {
		t.Fatalf("settle intent before acceptance: changed=%v err=%v", changed, err)
	}
	activity, duplicate, err := store.AcceptIntentIntervention(intentID, requestID, message)
	if err != nil || duplicate {
		t.Fatalf("accept intervention: duplicate=%v err=%v", duplicate, err)
	}
	if activity.ID <= reserved.ActivityID || activity.NodeID == nil || *activity.NodeID != intentID ||
		activity.Worker != "user" || activity.Kind != "user" || activity.Summary != message || activity.Detail != message {
		t.Fatalf("accepted audit mismatch: %+v", activity)
	}
	var metadata map[string]any
	if err := json.Unmarshal(activity.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["intervention_request_id"] != requestID || metadata["intervention_status"] != IntentInterventionAccepted {
		t.Fatalf("accepted metadata=%v", metadata)
	}
	node, err := store.GetNode(intentID)
	if err != nil || node == nil || node.State != "open" || node.Owner != "" {
		t.Fatalf("accepted intent=%+v err=%v, want reopened and unowned", node, err)
	}

	again, duplicate, err := store.AcceptIntentIntervention(intentID, requestID, message)
	if err != nil || !duplicate || again.ID != activity.ID {
		t.Fatalf("accept retry: activity=%+v duplicate=%v err=%v", again, duplicate, err)
	}
	lookup, err := store.IntentInterventionByRequest(intentID, requestID)
	if err != nil || lookup == nil || lookup.Status != IntentInterventionAccepted || lookup.ActivityID != activity.ID {
		t.Fatalf("accepted lookup=%+v err=%v", lookup, err)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity
		WHERE exploration_id=$1 AND node_id=$2
		  AND metadata->>'intervention_request_id'=$3`, store.ID(), intentID, requestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("Worker message rows=%d, want one hidden control row and one visible user row", rows)
	}
	activities, cursor, err := store.ActivityList(&intentID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || activities[0].ID != activity.ID || cursor != activity.ID {
		t.Fatalf("public activities leaked control row: activities=%+v cursor=%d", activities, cursor)
	}
	maxID, err := store.ActivityMaxID()
	if err != nil || maxID != activity.ID {
		t.Fatalf("public activity cursor=%d err=%v, want visible user row %d", maxID, err, activity.ID)
	}
	if detail, err := store.ActivityDetail(reserved.ActivityID); err != nil || detail != "" {
		t.Fatalf("hidden control detail=%q err=%v", detail, err)
	}
}

func TestIntentInterventionConcurrentReservationHasSingleDurableWinner(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	store, intentID, cleanup := newRunningInterventionIntent(t, d, "concurrent")
	defer cleanup()

	const contenders = 12
	const requestID = "intervention-concurrent-1"
	const message = "只保留被动请求并验证当前假设"
	start := make(chan struct{})
	results := make(chan *IntentIntervention, contenders)
	errs := make(chan error, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, reserveErr := store.ReserveIntentIntervention(intentID, requestID, message)
			results <- reservation
			errs <- reserveErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reservation: %v", err)
		}
	}
	var activityID int64
	for result := range results {
		if result == nil || result.ActivityID <= 0 {
			t.Fatalf("invalid concurrent reservation: %+v", result)
		}
		if activityID == 0 {
			activityID = result.ActivityID
		} else if result.ActivityID != activityID {
			t.Fatalf("concurrent requests produced activity ids %d and %d", activityID, result.ActivityID)
		}
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity
		WHERE exploration_id=$1 AND node_id=$2
		  AND metadata->>'intervention_request_id'=$3`, store.ID(), intentID, requestID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("concurrent intervention rows=%d, want 1", rows)
	}
}

func TestIntentInterventionAcceptanceCanRetryAfterStateSettlement(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()
	store, intentID, cleanup := newRunningInterventionIntent(t, d, "settlement retry")
	defer cleanup()

	const requestID = "intervention-settle-1"
	const message = "立刻停止当前动作并检查权限边界"
	reserved, err := store.ReserveIntentIntervention(intentID, requestID, message)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptIntentIntervention(intentID, requestID, message); !errors.Is(err, ErrIntentInterventionConflict) {
		t.Fatalf("accept before worker settlement error=%v, want conflict", err)
	}
	pending, err := store.IntentInterventionByRequest(intentID, requestID)
	if err != nil || pending == nil || pending.Status != IntentInterventionPending || pending.ActivityID != reserved.ActivityID {
		t.Fatalf("failed acceptance lost reservation: pending=%+v err=%v", pending, err)
	}
	if changed, err := store.CompareAndSetIntentState(intentID, "running", "paused"); err != nil || !changed {
		t.Fatalf("settle intent: changed=%v err=%v", changed, err)
	}
	activity, duplicate, err := store.AcceptIntentIntervention(intentID, requestID, message)
	if err != nil || duplicate || activity.ID <= reserved.ActivityID {
		t.Fatalf("retry acceptance: activity=%+v duplicate=%v err=%v", activity, duplicate, err)
	}
}
