package db

import (
	"errors"
	"testing"
)

// open 意图计数 + open 意图作废事务(cancel_intent 的 db 路径)。
// PG-gated:无库即跳,走包内既有 skip 模式。
func TestCountOpenIntentsAndCancelOpenIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("test", "intent dispatch")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	if n, err := es.CountOpenIntents(); err != nil || n != 0 {
		t.Fatalf("empty frontier count=%d err=%v, want 0", n, err)
	}

	open1, err := es.AddIntent(map[string]any{"summary": "stale direction"}, 8, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	open2, err := es.AddIntent(map[string]any{"summary": "dup direction"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	running, err := es.AddIntent(map[string]any{"summary": "claimed direction"}, 9, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := es.ClaimIntent(running, "worker-1"); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if n, err := es.CountOpenIntents(); err != nil || n != 2 {
		t.Fatalf("count=%d err=%v, want 2(running 不计入)", n, err)
	}

	// 作废 open 意图:状态落 stopped、payload 带作废标记、原因挂图成事实、frontier 排除。
	factID, err := es.CancelOpenIntent(open1, "方向已过期", "mainagent")
	if err != nil {
		t.Fatal(err)
	}
	if factID <= 0 {
		t.Fatalf("fact id=%d, want >0", factID)
	}
	node, err := es.GetNode(open1)
	if err != nil || node == nil {
		t.Fatalf("intent node=%v err=%v", node, err)
	}
	if node.State != "stopped" {
		t.Fatalf("state=%q, want stopped", node.State)
	}
	fact, err := es.GetNode(factID)
	if err != nil || fact == nil || fact.Kind != KindFact {
		t.Fatalf("audit fact=%v err=%v", fact, err)
	}
	if n, err := es.CountOpenIntents(); err != nil || n != 1 {
		t.Fatalf("count after cancel=%d err=%v, want 1", n, err)
	}
	frontier, err := es.Frontier(10)
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range frontier {
		if fn.ID == open1 {
			t.Fatal("已作废意图不得再进 frontier")
		}
	}

	// 重复作废 → 状态冲突;作废 running 意图 → 状态冲突(kill_work 的职责)。
	if _, err := es.CancelOpenIntent(open1, "again", "mainagent"); !errors.Is(err, ErrIntentStateConflict) {
		t.Fatalf("re-cancel err=%v, want ErrIntentStateConflict", err)
	}
	if _, err := es.CancelOpenIntent(running, "kill instead", "mainagent"); !errors.Is(err, ErrIntentStateConflict) {
		t.Fatalf("cancel running err=%v, want ErrIntentStateConflict", err)
	}
	if _, err := es.CancelOpenIntent(999999999, "ghost", "mainagent"); err == nil {
		t.Fatal("不存在的意图必须报错")
	}

	// 其余意图不受影响。
	keep, err := es.GetNode(open2)
	if err != nil || keep == nil || keep.State != "open" {
		t.Fatalf("unrelated intent=%+v err=%v, want open", keep, err)
	}
}
