package db

import "testing"

func TestToolUsageLedger(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	const (
		toolA = "zz_test_tool_usage_a"
		toolB = "zz_test_tool_usage_b"
	)
	cleanup := func() {
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE tool_key IN ($1,$2)`, toolA, toolB)
	}
	cleanup()
	defer cleanup()

	rows := []*ToolUsage{
		{ToolKey: toolA, AgentKey: "worker", TaskID: 991, ExplorationID: 5, IntentID: 7},
		{ToolKey: toolA, AgentKey: "planner", TaskID: 992, ExplorationID: 6},
		{ToolKey: toolA, AgentKey: "chatbot", SessionID: "conv-1"},
		{ToolKey: toolB, AgentKey: "worker", TaskID: 991},
	}
	for _, row := range rows {
		if err := d.InsertToolUsage(row); err != nil {
			t.Fatalf("insert %s: %v", row.ToolKey, err)
		}
	}

	counts, err := d.ToolUsageCounts()
	if err != nil {
		t.Fatal(err)
	}
	if got := counts[toolA]; got != 3 {
		t.Errorf("%s calls: want 3, got %d", toolA, got)
	}
	if got := counts[toolB]; got != 1 {
		t.Errorf("%s calls: want 1, got %d", toolB, got)
	}
}
