package agent

import (
	"context"
	"encoding/json"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// TestBuiltinToolSeeds ensures the catalog builds from a nil-store ToolSet without
// panicking, has stable keys, unions agent bindings, and carries real descriptions.
func TestBuiltinToolSeeds(t *testing.T) {
	seeds := BuiltinToolSeeds()
	if len(seeds) == 0 {
		t.Fatal("no seeds")
	}
	byKey := map[string]ToolSeed{}
	for _, s := range seeds {
		if s.Key == "" || s.Desc == "" {
			t.Errorf("seed %q missing key/desc", s.Key)
		}
		byKey[s.Key] = s
	}
	// record_fact is worker-only.
	if rf, ok := byKey["record_fact"]; !ok || len(rf.Agents) != 1 || rf.Agents[0] != "worker" {
		t.Errorf("record_fact agents = %v, want [worker]", rf.Agents)
	}
	// SDK generic tools (incl. sleep, now part of DefaultTools) are deliberately NOT
	// seeded — every agent owns them; they flow through ToolResolve untouched.
	for _, k := range []string{"Bash", "Read", "Write", "Edit", "Grep", "sleep"} {
		if _, ok := byKey[k]; ok {
			t.Errorf("SDK tool %q should not be seeded", k)
		}
	}
}

// TestDecorateToolInjectsDefaults verifies a schema "default" fills a missing param
// before the underlying handler runs, and an explicitly-provided value is kept.
func TestDecorateToolInjectsDefaults(t *testing.T) {
	var seen map[string]any
	base := actool.Build(actool.Spec{
		Name: "probe", Description: "orig",
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			_ = json.Unmarshal(in, &seen)
			return actool.Text("ok"), nil
		},
	})
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"limit": map[string]any{"type": "integer", "description": "n", "default": float64(3)},
		"q":     map[string]any{"type": "string", "description": "query"},
	}}
	dec := DecorateTool(base, "new desc", schema)
	if dec.Description() != "new desc" {
		t.Errorf("description = %q", dec.Description())
	}

	// limit omitted → default 3 injected; q kept.
	if _, err := dec.Call(context.Background(), json.RawMessage(`{"q":"x"}`), nil); err != nil {
		t.Fatal(err)
	}
	if seen["limit"] != float64(3) || seen["q"] != "x" {
		t.Errorf("injected = %v, want limit=3 q=x", seen)
	}

	// limit provided → default does NOT override.
	if _, err := dec.Call(context.Background(), json.RawMessage(`{"limit":9}`), nil); err != nil {
		t.Fatal(err)
	}
	if seen["limit"] != float64(9) {
		t.Errorf("limit = %v, want 9 (no override)", seen["limit"])
	}
}
