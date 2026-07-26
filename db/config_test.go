package db

import (
	"encoding/json"
	"testing"
)

func TestConfigStores(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	// LLM profile: save, active, key never serialized
	pid, err := d.SaveProfile(&LLMProfile{Name: "t-default", Format: "openai", Model: "gpt-x", APIKey: "secret123"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, pid)
	if err := d.SetActiveProfile(pid); err != nil {
		t.Fatal(err)
	}
	act, err := d.ActiveProfile()
	if err != nil || act == nil || act.APIKey != "secret123" {
		t.Fatalf("active profile/key: %+v err=%v", act, err)
	}
	// list must hide the key, expose hint
	list, _ := d.ListProfiles()
	for _, p := range list {
		if p.ID == pid {
			b, _ := json.Marshal(p)
			if string(b) == "" || contains(string(b), "secret123") {
				t.Fatalf("api key leaked in list json: %s", b)
			}
			if p.APIKeyHint != "…t123" {
				t.Fatalf("hint want …t123, got %q", p.APIKeyHint)
			}
		}
	}

	// agents seeded; prompt versioning
	ag, err := d.GetAgentByKey("planner")
	if err != nil || ag == nil {
		t.Fatalf("planner agent: %v", err)
	}
	v1, err := d.SavePrompt(ag.ID, "你是规划者 {{.Goal}}", "init", "test")
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := d.SavePrompt(ag.ID, "你是规划者 v2 {{.Goal}} {{.Scope}}", "edit", "test")
	if v2 != v1+1 {
		t.Fatalf("version should increment: %d -> %d", v1, v2)
	}
	cur, _ := d.CurrentPrompt(ag.ID)
	if cur != "你是规划者 v2 {{.Goal}} {{.Scope}}" {
		t.Fatalf("current prompt wrong: %q", cur)
	}
	vers, _ := d.ListPromptVersions(ag.ID)
	if len(vers) < 2 {
		t.Fatalf("want >=2 versions, got %d", len(vers))
	}
	pv, _ := d.PromptVars(ag.ID)
	// planner must have at least the seeded catalog vars (Goal, AssetSummary)
	if len(pv) < 2 {
		t.Fatalf("planner catalog want >=2 vars, got %d", len(pv))
	}
	d.Exec(`DELETE FROM agent_prompts WHERE agent_id=$1`, ag.ID)
	d.Exec(`UPDATE agents SET current_prompt_id=NULL WHERE id=$1`, ag.ID)

	// mcp + skill + visibility (bidirectional via one join)
	// Clean up any leftover MCP from prior runs to keep this test idempotent.
	d.Exec(`DELETE FROM mcp_servers WHERE name = 't-gh'`)
	mid, err := d.SaveMCP(&MCPServer{Name: "t-gh", Transport: "stdio", Command: "npx", Args: json.RawMessage(`["server-github"]`), Env: json.RawMessage(`{"GITHUB_TOKEN":"x"}`), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// agent-side write. MCP is id-keyed (generic visibility join); skills are now
	// filesystem-based, so their visibility is keyed by skill (directory) name in a
	// dedicated table. Clear any pre-existing MCP visibility rows so the assertions
	// below isolate on exactly what this test sets.
	d.Exec(`DELETE FROM agent_visibility WHERE agent_id = $1 AND resource_kind = 'mcp'`, ag.ID)
	if err := d.ToggleVisibility(ag.ID, "mcp", mid, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetAgentSkillVisibility(ag.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.ToggleSkillVisibility(ag.ID, "t-skill", true); err != nil {
		t.Fatal(err)
	}
	// agent-side read
	vm, _ := d.AgentVisible(ag.ID, "mcp")
	if len(vm) != 1 || vm[0] != mid {
		t.Fatalf("agent visible mcp: %+v", vm)
	}
	// resource-side read (same join row) → bidirectional
	ra, _ := d.ResourceAgents("mcp", mid)
	if len(ra) != 1 || ra[0] != ag.ID {
		t.Fatalf("resource agents: %+v", ra)
	}
	// toggle off
	d.ToggleVisibility(ag.ID, "mcp", mid, false)
	vm2, _ := d.AgentVisible(ag.ID, "mcp")
	if len(vm2) != 0 {
		t.Fatalf("after toggle off: %+v", vm2)
	}
	// skill visibility is name-keyed: verify the read, then deleting the skill's
	// visibility rows (called when a skill is removed) clears it.
	names, _ := d.AgentSkillNames(ag.ID)
	if len(names) != 1 || names[0] != "t-skill" {
		t.Fatalf("agent visible skills: %+v", names)
	}
	if err := d.DeleteSkillVisibility("t-skill"); err != nil {
		t.Fatal(err)
	}
	names2, _ := d.AgentSkillNames(ag.ID)
	if len(names2) != 0 {
		t.Fatalf("skill visibility should be cleared on delete: %+v", names2)
	}
	d.DeleteMCP(mid)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
