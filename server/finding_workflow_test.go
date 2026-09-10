package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func workflowCall(t *testing.T, ctx context.Context, tool actool.CoreTool, input any, wantError bool) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := tool.Call(ctx, raw, nil)
	if err != nil || r.IsError != wantError {
		t.Fatalf("%s: error=%v result=%s", tool.Name(), err, r.Flatten())
	}
	return r.Flatten()
}

func workflowTool(t *testing.T, tools []actool.CoreTool, name string) actool.CoreTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("missing tool: %s", name)
	return nil
}

func TestFindingWorkflowAutoHintToPlannerAndSetting(t *testing.T) {
	s, initial, request := trafficEvidenceServer(t)
	pg := s.m.pg
	ctx := context.Background()
	if _, err := pg.Exec(`DELETE FROM settings WHERE key=$1`, settingAgentTrafficBinding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.SetBool(settingAgentTrafficBinding, false) })
	if s.settingsPayload()[settingAgentTrafficBinding] != false {
		t.Fatal("missing setting must default off")
	}
	seedServerEvidenceFlow(t, s, "handoff-proof", []byte("verified local proof"))
	seedServerEvidenceFlow(t, s, "handoff-baseline", []byte("local baseline"))
	seedServerEvidenceFlow(t, s, "handoff-verification", []byte{0, 1, 255})
	// Manual binding is independent of the Agent switch.
	if r := request("POST", fmt.Sprintf("/api/exploration/findings/%d/traffic", initial.FindingID), `{"traffic_refs":[{"traffic_id":"handoff-proof"}]}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	bind := s.toolBindFindingTraffic()
	input := map[string]any{"finding_id": initial.FindingID, "traffic_refs": []db.TrafficRef{{TrafficID: "handoff-baseline"}}}
	workflowCall(t, ctx, bind, input, true)
	if r := request("PUT", "/api/settings", `{"agent_traffic_binding":true}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	if !pg.GetBool(settingAgentTrafficBinding, false) {
		t.Fatal("switch was not persisted")
	}
	f, _ := pg.GetFinding(initial.FindingID)
	task := s.m.ResolveTask(fmt.Sprint(*f.TaskID))
	refs := []db.TrafficRef{{TrafficID: "handoff-baseline", Role: "baseline", Note: "normal response"}, {TrafficID: "handoff-proof", Role: "proof", Note: "proves the finding"}, {TrafficID: "handoff-verification", Role: "verification", Note: "binary verification"}}
	// Real cross-task Auto hint handler -> persisted graph -> Planner tool.
	hintResult := workflowCall(t, ctx, s.toolAddHint(), map[string]any{"task_id": task.ID, "hints": []any{map[string]any{"text": "Report the confirmed local finding with its verified evidence", "traffic_refs": refs}}}, false)
	var hints struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.Unmarshal([]byte(hintResult), &hints); err != nil || len(hints.IDs) != 1 || hints.IDs[0] == 0 {
		t.Fatal(hintResult, err)
	}
	graph := workflowCall(t, ctx, s.toolGetTaskGraph(), map[string]any{"task_id": task.ID}, false)
	for _, ref := range refs {
		if !strings.Contains(graph, ref.TrafficID) {
			t.Fatal("handoff lost reference", ref.TrafficID)
		}
	}
	for range 6 {
		if _, err := task.Store.AddNode(db.KindFact, map[string]any{"summary": "ID separation"}, 0, "confirmed", "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	ts := agent.NewToolSet(task.Store, "planner")
	ts.SetTaskID(*f.TaskID)
	ts.SetFindingRecorder(s.evidenceStore())
	notices := 0
	ts.SetNotifyFinding(func(int64, string) { notices++ })
	tools, def, cleanup := agent.AugmentTools(ctx, "planner", ts.PlannerTools())
	defer cleanup()
	if !strings.Contains(def.FindingGuidance, "evidence_hint_id") || !strings.Contains(def.FindingGuidance, "取消 Worker") {
		t.Fatal("Planner missed runtime guidance")
	}
	report := workflowTool(t, tools, "report_finding")
	result := workflowCall(t, ctx, report, map[string]any{"vulnclass": "TEST", "severity": "low", "summary": "Planner handoff fixture", "evidence_hint_id": hints.IDs[0]}, false)
	var recorded db.RecordedFinding
	if err := json.Unmarshal([]byte(strings.SplitN(result, "\n", 2)[1]), &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.FindingID == recorded.NodeID || len(recorded.Traffic.Bindings) != 3 || notices != 1 {
		t.Fatal("bad finding/traffic result", result)
	}
	for i, b := range recorded.Traffic.Bindings {
		if b.Snapshot.SourceTrafficID != refs[i].TrafficID || b.Role != refs[i].Role || b.Note != refs[i].Note {
			t.Fatalf("handoff mismatch: %+v", b)
		}
	}
	list := workflowCall(t, ctx, s.toolListTaskFindings(), map[string]any{"task_id": task.ID}, false)
	var nodes []struct {
		ID            int64 `json:"id"`
		FindingID     int64 `json:"finding_id"`
		FindingNodeID int64 `json:"finding_node_id"`
		Count         int   `json:"traffic_count"`
	}
	if err := json.Unmarshal([]byte(list), &nodes); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range nodes {
		if n.ID == recorded.NodeID {
			found = n.FindingID == recorded.FindingID && n.FindingNodeID == recorded.NodeID && n.Count == 3
		}
	}
	if !found {
		t.Fatal("canonical IDs/count missing", list)
	}
	workflowCall(t, ctx, s.toolGetFindingTraffic(), map[string]any{"finding_id": recorded.FindingID}, false)
	wrong := workflowCall(t, ctx, s.toolGetFindingTraffic(), map[string]any{"finding_id": int64(900000000000000000)}, true)
	if !strings.Contains(wrong, "独立漏洞记录 ID") {
		t.Fatal("ambiguous ID error", wrong)
	}
	input["finding_id"] = recorded.FindingID
	// Duplicate append preserves metadata and version.
	workflowCall(t, ctx, bind, input, false)
	after, err := pg.GetFindingTraffic(ctx, recorded.FindingID)
	if err != nil || len(after.Bindings) != 3 || after.Version != recorded.Traffic.Version || after.Bindings[0].Note != refs[0].Note {
		t.Fatal("duplicate changed evidence", after, err)
	}
	// A malformed handoff must not create a partial finding or notify Planner.
	badHint := workflowCall(t, ctx, s.toolAddHint(), map[string]any{"task_id": task.ID, "text": "missing packet", "traffic_refs": []db.TrafficRef{{TrafficID: "missing"}}}, false)
	var badID int64
	fmt.Sscanf(badHint, "hint added: %d", &badID)
	workflowCall(t, ctx, report, map[string]any{"vulnclass": "TEST", "severity": "low", "summary": "must fail", "evidence_hint_id": badID}, true)
	if notices != 1 {
		t.Fatal("failed handoff notified before commit")
	}
	child, err := s.m.CreateTaskWithOptions("inherited handoff", "fixture", db.TaskCreateOptions{SourceTaskIDs: []int64{*f.TaskID}})
	if err != nil {
		t.Fatal(err)
	}
	childID, _ := strconv.ParseInt(child.ID, 10, 64)
	childRow, _ := pg.GetTask(childID)
	t.Cleanup(func() { pg.DeleteTask(childRow.ID) })
	childCtx := agent.WithRunInfo(ctx, agent.RunInfo{TaskID: childRow.ID})
	workflowCall(t, childCtx, bind, input, true)
	workflowCall(t, childCtx, s.toolGetFindingTraffic(), map[string]any{"finding_id": recorded.FindingID}, false)
	// Existing, already assembled tools must honor a later switch-off.
	if r := request("PUT", "/api/settings", `{"agent_traffic_binding":false}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	workflowCall(t, ctx, bind, input, true)
	// Turning binding off must still save a confirmed finding from an already
	// assembled tool, even if the model sends the old optional evidence fields.
	offResult := workflowCall(t, ctx, report, map[string]any{"vulnclass": "TEST", "severity": "low", "summary": "off", "evidence_hint_id": hints.IDs[0]}, false)
	var offRecord struct {
		db.RecordedFinding
		EvidenceStatus string `json:"evidence_status"`
		EvidenceNote   string `json:"evidence_note"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(offResult, "\n", 2)[1]), &offRecord); err != nil {
		t.Fatal(err)
	}
	if offRecord.FindingID <= 0 || len(offRecord.Traffic.Bindings) != 0 || offRecord.EvidenceStatus != "not_bound" || !strings.Contains(offRecord.EvidenceNote, "已关闭") {
		t.Fatal("disabled binding discarded finding or bound evidence", offResult)
	}
	workflowCall(t, ctx, report, map[string]any{"vulnclass": "TCP", "severity": "low", "summary": "no packet needed"}, false)
	if notices != 3 {
		t.Fatal("optional no-packet report failed")
	}
	workflowCall(t, ctx, s.toolGetFindingTraffic(), map[string]any{"finding_id": recorded.FindingID}, false)
	tools, def, closeTools := agent.AugmentTools(ctx, "planner", ts.PlannerTools())
	defer closeTools()
	if def.FindingGuidance != "" {
		t.Fatal("disabled feature still injects guidance")
	}
	for _, tool := range tools {
		if tool.Name() == "bind_finding_traffic" {
			t.Fatal("disabled binding tool exposed")
		}
	}
}

func TestFindingWorkflowMigrationPreservesUserConfiguration(t *testing.T) {
	s, _, _ := trafficEvidenceServer(t)
	pg := s.m.pg
	for _, key := range []string{"report_finding", "add_hint", "add_task_hint", "traffic_search", "traffic_get", "traffic_blob", "bind_finding_traffic", "get_finding_traffic"} {
		old, err := pg.GetTool(key)
		if err != nil || old == nil {
			t.Fatal("missing tool", key, err)
		}
		t.Cleanup(func() {
			bindings, _ := json.Marshal(old.Agents)
			pg.UpdateTool(key, old.Description, old.Schema, bindings, old.Enabled)
		})
	}
	custom := json.RawMessage(`{"type":"object","properties":{"text":{"type":"string","description":"USER TEXT"},"hints":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string","description":"USER ITEM"}}}}},"required":["text"]}`)
	for _, key := range []string{"report_finding", "add_hint", "add_task_hint"} {
		if err := pg.UpdateTool(key, "USER DESCRIPTION", custom, json.RawMessage(`["custom-agent"]`), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"traffic_search", "traffic_get"} {
		row, _ := pg.GetTool(key)
		bindings := json.RawMessage(`["custom-agent"]`)
		if key == "traffic_search" {
			bindings = json.RawMessage(`["worker"]`)
		}
		if err := pg.UpdateTool(key, row.Description, row.Schema, bindings, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.SetSetting("finding_workflow_tools_v2_reporter", "false"); err != nil {
		t.Fatal(err)
	}
	s.seedFindingWorkflowTools()
	for _, key := range []string{"report_finding", "add_hint", "add_task_hint"} {
		row, _ := pg.GetTool(key)
		if row.Enabled || row.Description != "USER DESCRIPTION" || len(row.Agents) != 1 || row.Agents[0] != "custom-agent" {
			t.Fatal("changed user tool configuration", key)
		}
		if !strings.Contains(string(row.Schema), "USER TEXT") || !strings.Contains(string(row.Schema), "USER ITEM") {
			t.Fatal("changed user schema", key)
		}
		field := "traffic_refs"
		if key == "report_finding" {
			field = "evidence_hint_id"
		}
		if !strings.Contains(string(row.Schema), field) {
			t.Fatal("missing optional field", key)
		}
	}
	search, _ := pg.GetTool("traffic_search")
	get, _ := pg.GetTool("traffic_get")
	if search.Enabled || !contains(search.Agents, "reporter") || get.Enabled || len(get.Agents) != 1 || get.Agents[0] != "custom-agent" {
		t.Fatal("default/custom reader binding migration incorrect")
	}
	if err := pg.RemoveAgentFromTool("reporter", "traffic_search"); err != nil {
		t.Fatal(err)
	}
	s.seedFindingWorkflowTools()
	search, _ = pg.GetTool("traffic_search")
	if contains(search.Agents, "reporter") {
		t.Fatal("one-time migration undid later unbinding")
	}
}

func TestFindingWorkflowReporterBindsBeforeWritingReport(t *testing.T) {
	s, f, request := trafficEvidenceServer(t)
	pg := s.m.pg
	ctx := context.Background()
	if err := pg.SetBool(settingAgentTrafficBinding, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.SetBool(settingAgentTrafficBinding, false); s.m.SetTrafficEnabled(false) })
	if err := s.m.SetTrafficEnabled(true); err != nil {
		t.Fatal(err)
	}
	seedServerEvidenceFlow(t, s, "reporter-proof", []byte("local proof payload"))
	seedServerEvidenceFlow(t, s, "reporter-baseline", []byte("local normal response"))
	tools, def, cleanup := agent.AugmentTools(ctx, "reporter", nil)
	defer cleanup()
	if !strings.Contains(def.FindingGuidance, "报告前自动关联流量") || !strings.Contains(def.FindingGuidance, "绑定成功后重新调用") {
		t.Fatal("reporter did not receive binding workflow")
	}
	for _, name := range []string{"traffic_search", "traffic_get", "get_task_worker_trace", "get_task_node_detail", "bind_finding_traffic", "get_finding_traffic", "update_finding_report"} {
		workflowTool(t, tools, name)
	}
	finding, _ := pg.GetFinding(f.FindingID)
	detail := workflowCall(t, ctx, workflowTool(t, tools, "get_task_node_detail"), map[string]any{"task_id": fmt.Sprint(*finding.TaskID), "id": f.NodeID}, false)
	if !strings.Contains(detail, `"finding_id"`) || !strings.Contains(detail, `"finding_node_id"`) {
		t.Fatal("reporter lacks explicit ID mapping")
	}
	workflowCall(t, ctx, workflowTool(t, tools, "traffic_search"), map[string]any{"host": "evidence.local"}, false)
	for _, id := range []string{"reporter-baseline", "reporter-proof"} {
		packet := workflowCall(t, ctx, workflowTool(t, tools, "traffic_get"), map[string]any{"id": id}, false)
		if !strings.Contains(packet, "local") {
			t.Fatal("reporter cannot inspect packet", packet)
		}
	}
	refs := []db.TrafficRef{{TrafficID: "reporter-baseline", Role: "baseline"}, {TrafficID: "reporter-proof", Role: "proof", Note: "confirmed from recorded response"}}
	workflowCall(t, ctx, workflowTool(t, tools, "bind_finding_traffic"), map[string]any{"finding_id": f.FindingID, "traffic_refs": refs}, false)
	list := workflowCall(t, ctx, workflowTool(t, tools, "get_finding_traffic"), map[string]any{"finding_id": f.FindingID}, false)
	var summary struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal([]byte(list), &summary); err != nil || summary.Version != 1 {
		t.Fatal(list, err)
	}
	workflowCall(t, ctx, workflowTool(t, tools, "update_finding_report"), map[string]any{"finding_id": f.NodeID, "evidence_version": summary.Version, "report": "## Local report\n\nVerified baseline and proof using saved evidence."}, false)
	updated, err := pg.GetFinding(f.FindingID)
	if err != nil || updated.ReportEvidenceVersion != summary.Version || updated.EvidenceVersion != summary.Version {
		t.Fatal("report did not cover post-binding version", updated, err)
	}
	// Disabling automatic binding still lets Reporter read manually bound snapshots.
	if r := request("PUT", "/api/settings", `{"agent_traffic_binding":false}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body)
	}
	off, offDef, closeOff := agent.AugmentTools(ctx, "reporter", nil)
	defer closeOff()
	if offDef.FindingGuidance != "" {
		t.Fatal("off reporter still receives auto-binding guidance")
	}
	for _, tool := range off {
		if tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob" || tool.Name() == "bind_finding_traffic" {
			t.Fatal("auto-binding tool exposed while off", tool.Name())
		}
	}
	workflowCall(t, ctx, workflowTool(t, off, "get_finding_traffic"), map[string]any{"finding_id": f.FindingID}, false)
	workflowTool(t, off, "update_finding_report")
}
