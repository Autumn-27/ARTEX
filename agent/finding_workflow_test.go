package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestFindingWorkflowSharedAssemblyAndSwitch(t *testing.T) {
	oldPolicy, oldResolve, oldAugment := FindingTrafficBindingEnabled, ToolResolve, ToolAugment
	t.Cleanup(func() { FindingTrafficBindingEnabled, ToolResolve, ToolAugment = oldPolicy, oldResolve, oldAugment })
	ToolAugment = nil
	on := false
	FindingTrafficBindingEnabled = func() bool { return on }
	ts := NewToolSet(nil, "fixture")
	ToolResolve = func(_ context.Context, _ string, base []actool.CoreTool) []actool.CoreTool {
		out := make([]actool.CoreTool, len(base))
		for i, tool := range base {
			out[i] = DecorateTool(tool, "CUSTOM DESCRIPTION", tool.InputSchema())
		}
		return out
	}
	for _, role := range []string{"worker", "planner", "mainagent", "auto", "pentest", "custom-agent"} {
		for _, enabled := range []bool{false, true, false} {
			on = enabled
			out, def, cleanup := AugmentTools(t.Context(), role, []actool.CoreTool{ts.addFinding(), ts.addHint()})
			cleanup()
			system, _ := deferredSystem("USER CUSTOM PROMPT", def)
			if !strings.HasPrefix(system[0], "USER CUSTOM PROMPT") {
				t.Fatal("custom prompt replaced")
			}
			if strings.Contains(system[0], "traffic_refs") != on {
				t.Fatalf("%s: guidance ignored switch: %v", role, on)
			}
			if on && (!strings.Contains(system[0], "TCP") || !strings.Contains(system[0], "evidence_hint_id")) {
				t.Fatal("missing optional/handoff contract")
			}
			for _, tool := range out {
				if !strings.HasPrefix(tool.Description(), "CUSTOM DESCRIPTION") {
					t.Fatal("custom description replaced")
				}
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				json.Unmarshal(raw, &schema)
				props := schema["properties"].(map[string]any)
				if (props["traffic_refs"] != nil) != on {
					t.Fatalf("%s: binding schema ignored switch", role)
				}
				if tool.Name() == "add_hint" {
					nested := props["hints"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
					if (nested["traffic_refs"] != nil) != on {
						t.Fatal("nested hint schema ignored switch")
					}
				}
			}
		}
	}
	on = true
	ToolResolve = func(context.Context, string, []actool.CoreTool) []actool.CoreTool { return nil }
	out, def, cleanup := AugmentTools(t.Context(), "planner", []actool.CoreTool{ts.addFinding()})
	defer cleanup()
	if len(out) != 0 || def.FindingGuidance != "" {
		t.Fatal("reintroduced disabled tool or its guidance")
	}
}
