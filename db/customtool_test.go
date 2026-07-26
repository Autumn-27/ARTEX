package db

import (
	"encoding/json"
	"testing"
)

// TestCustomToolCRUD exercises create/list/update/delete of a user-defined tool
// (system=false) with kind/exec/deferred. Skips when no Postgres is configured.
func TestCustomToolCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	key := "ct_test_tool"
	_ = d.DeleteCustomTool(key) // clean slate

	in := &Tool{
		Key:         key,
		Description: "test",
		Schema:      json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		Agents:      []string{"worker"},
		Enabled:     true,
		Kind:        "script",
		Exec:        json.RawMessage(`{"code":"print(1)"}`),
		Deferred:    true,
	}
	if err := d.CreateCustomTool(in); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = d.DeleteCustomTool(key) })

	got, err := d.GetTool(key)
	if err != nil || got == nil {
		t.Fatalf("get after create: %v (nil=%v)", err, got == nil)
	}
	if got.System || got.Kind != "script" || !got.Deferred || len(got.Agents) != 1 {
		t.Fatalf("unexpected row: %+v", got)
	}

	// custom tools appear in ListCustomTools, not among system-only.
	customs, err := d.ListCustomTools()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range customs {
		if c.Key == key {
			found = true
			if c.System {
				t.Fatal("custom tool should have system=false")
			}
		}
	}
	if !found {
		t.Fatal("created tool not in ListCustomTools")
	}

	// update: change kind + disable + rebind.
	in.Kind = "command"
	in.Exec = json.RawMessage(`{"command":"echo hi"}`)
	in.Enabled = false
	in.Agents = []string{"worker", "planner"}
	in.Deferred = false
	if err := d.UpdateCustomTool(in); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := d.GetTool(key)
	if got2.Kind != "command" || got2.Enabled || got2.Deferred || len(got2.Agents) != 2 {
		t.Fatalf("update not applied: %+v", got2)
	}

	// delete only removes non-system rows.
	if err := d.DeleteCustomTool(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if g, _ := d.GetTool(key); g != nil {
		t.Fatal("tool still present after delete")
	}
}
