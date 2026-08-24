package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

func callReadJSON(t *testing.T, tool actool.CoreTool, input string) any {
	t.Helper()
	result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	var out any
	if err := json.Unmarshal([]byte(result.Flatten()), &out); err != nil {
		t.Fatalf("decode tool result: %v; raw=%s", err, result.Flatten())
	}
	return out
}

func TestGraphOverviewExpandsAssociatedCompanyScope(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	companies := d.Companies()
	companyID, _, err := companies.UpsertCompany(fmt.Sprintf("overview-scope-%d", time.Now().UnixNano()), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = companies.DeleteCompany(companyID) })

	domain := fmt.Sprintf("overview-scope-%d.invalid", companyID)
	ip := fmt.Sprintf("2001:db8:%x::42", companyID%0xffff)
	cidr := fmt.Sprintf("2001:db8:%x:1::/64", companyID%0xffff)
	icp := fmt.Sprintf("京 ICP 备 %d 号", companyID)
	keyword := fmt.Sprintf("Scope Company %d", companyID)
	inputs := []db.ScopeInput{
		{Kind: "domain", Value: domain},
		{Kind: "ip", Value: ip},
		{Kind: "cidr", Value: cidr},
		{Kind: "icp", Value: icp},
		{Kind: "keyword", Value: keyword},
	}
	added, skipped, invalid, scopeErrors := companies.AddScopeInputs(companyID, inputs, "task context test")
	if added != len(inputs) || skipped != 0 || invalid != 0 || len(scopeErrors) != 0 {
		t.Fatalf("add company scope: added=%d skipped=%d invalid=%d errors=%v", added, skipped, invalid, scopeErrors)
	}

	task, err := d.CreateTaskWithOptions("company context", "read configured scope", db.TaskCreateOptions{
		CompanyIDs: []int64{companyID},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	tools := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(d.Assets(), companies)
	overview := tools.graphOverviewData()
	coverage, ok := overview["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("coverage missing: %#v", overview["coverage"])
	}
	scopeRows, ok := coverage["scope"].([]map[string]any)
	if !ok || len(scopeRows) != 1 {
		t.Fatalf("task scope missing: %#v", coverage["scope"])
	}
	companyScope, ok := scopeRows[0]["company_scope"].([]map[string]any)
	if !ok || len(companyScope) != len(inputs) {
		t.Fatalf("company scope not expanded: %#v", scopeRows[0])
	}
	kinds := make(map[string]string, len(companyScope))
	for _, rule := range companyScope {
		kinds[fmt.Sprint(rule["kind"])] = fmt.Sprint(rule["value"])
	}
	for _, input := range inputs {
		if kinds[input.Kind] != input.Value {
			t.Errorf("scope %s=%q want %q", input.Kind, kinds[input.Kind], input.Value)
		}
	}
	keywords, ok := scopeRows[0]["company_keywords"].([]string)
	if !ok || len(keywords) != 1 || keywords[0] != keyword {
		t.Fatalf("company keywords missing: %#v", scopeRows[0]["company_keywords"])
	}
}

func TestBlackboardToolsReadDirectSources(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	grand, err := d.CreateTask("grand", "grand goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("source", "source goal", db.TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("current", "current goal", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(current.ID)
		_ = d.DeleteTask(source.ID)
		_ = d.DeleteTask(grand.ID)
	})

	grandStore := d.Exploration(grand.ExplorationID)
	sourceStore := d.Exploration(source.ExplorationID)
	currentStore := d.Exploration(current.ExplorationID)
	grandFact, err := grandStore.AddNode(db.KindFact, map[string]any{"summary": "indirect-only"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceFact, err := sourceStore.AddNode(db.KindFact, map[string]any{"summary": "shared fact", "confidence": "verified"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceIntent, err := sourceStore.AddIntent(map[string]any{"summary": "shared work"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.SetIntentState(sourceIntent, "done"); err != nil {
		t.Fatal(err)
	}
	sourceFinding, err := sourceStore.AddNode(db.KindFinding, map[string]any{
		"summary": "shared finding", "vulnclass": "idor", "severity": "high",
	}, 9, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.Link(sourceIntent, db.RelYields, sourceFinding); err != nil {
		t.Fatal(err)
	}
	stepID, err := sourceStore.AppendActivity(db.Activity{
		NodeID: &sourceIntent, Worker: "source-worker", Kind: "result", Tool: "HTTP",
		Summary: "shared trace marker", Detail: "shared trace full detail",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 301; i++ {
		if _, err := sourceStore.AddIntent(map[string]any{"summary": fmt.Sprintf("newer live source work %d", i)}, 1, nil, "planner"); err != nil {
			t.Fatal(err)
		}
	}
	assets := d.Assets()
	host := fmt.Sprintf("overview-host-%d.invalid", source.ID)
	assetID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: host, TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{assetID}) })
	if _, err := sourceStore.AddNode(db.KindFact, map[string]any{"summary": "source host anchor"}, 0, "confirmed", "worker", []int64{assetID}); err != nil {
		t.Fatal(err)
	}

	tools := NewToolSet(currentStore, "worker")
	tools.SetTaskID(current.ID)
	tools.SetAssetStore(assets, assets.Companies())
	parentID, _ := json.Marshal(sourceFact)
	derivedID, err := tools.addOneIntent(intentItem{
		Summary: "derive locally from shared fact", ParentIDs: []json.RawMessage{parentID},
	})
	if err != nil {
		t.Fatalf("derive from inherited fact: %v", err)
	}
	if derived, err := currentStore.GetNode(derivedID); err != nil || derived == nil {
		t.Fatalf("derived intent must be local: node=%+v err=%v", derived, err)
	}
	beforeFacts, _ := currentStore.ListByKind(db.KindFact, 100)
	sourceIntentID, _ := json.Marshal(sourceIntent)
	if _, err := tools.recordOneFact(factItem{Summary: "must not attach to inherited intent", IntentID: json.RawMessage(sourceIntentID)}, sourceIntent); err == nil {
		t.Fatal("record_fact must reject inherited intent")
	}
	afterFacts, _ := currentStore.ListByKind(db.KindFact, 100)
	if len(afterFacts) != len(beforeFacts) {
		t.Fatalf("rejected inherited write persisted a fact: before=%d after=%d", len(beforeFacts), len(afterFacts))
	}
	overview := tools.graphOverviewData()
	related, ok := overview["related_tasks"].([]map[string]any)
	if !ok || len(related) != 1 {
		t.Fatalf("related_tasks: %#v", overview["related_tasks"])
	}
	if related[0]["source_task_id"] != source.ID || related[0]["inherited"] != true {
		t.Fatalf("related source provenance: %#v", related[0])
	}
	recentFacts, ok := related[0]["recent_facts"].([]map[string]any)
	foundSourceFact := false
	for _, fact := range recentFacts {
		if fact["id"] == sourceFact {
			foundSourceFact = true
			break
		}
	}
	if !ok || !foundSourceFact {
		t.Fatalf("related fact summary: %#v", related[0]["recent_facts"])
	}
	if findings, ok := related[0]["recent_findings"].([]map[string]any); !ok || len(findings) == 0 || findings[0]["id"] != sourceFinding {
		t.Fatalf("related finding summary: %#v", related[0]["recent_findings"])
	}
	results, ok := related[0]["recent_intent_results"].([]map[string]any)
	if !ok || len(results) == 0 || results[0]["id"] != sourceIntent {
		t.Fatalf("terminal source intent was starved by newer live work: %#v", related[0]["recent_intent_results"])
	}
	if results[0]["result_summary"] != "shared trace marker" {
		t.Fatalf("terminal source intent result was not distilled: %#v", results[0])
	}
	coverage, ok := overview["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("coverage missing from overview: %#v", overview["coverage"])
	}
	hosts, ok := coverage["hosts"].([]string)
	if !ok || len(hosts) != 1 || hosts[0] != host || coverage["host_count"] != 1 {
		t.Fatalf("inherited host context missing: %#v", coverage)
	}

	facts := callReadJSON(t, tools.listFacts(), `{}`).([]any)
	seenSource, seenGrand := false, false
	for _, raw := range facts {
		item := raw.(map[string]any)
		id := int64(item["id"].(float64))
		if id == sourceFact {
			seenSource = item["inherited"] == true && int64(item["source_task_id"].(float64)) == source.ID
		}
		seenGrand = seenGrand || id == grandFact
	}
	if !seenSource || seenGrand {
		t.Fatalf("list_facts direct-only: source=%v grand=%v payload=%#v", seenSource, seenGrand, facts)
	}

	findings := callReadJSON(t, tools.listFindings(), `{}`).([]any)
	if len(findings) != 1 {
		t.Fatalf("list_findings: %#v", findings)
	}
	finding := findings[0].(map[string]any)
	if finding["inherited"] != true || int64(finding["task_id"].(float64)) != source.ID || int64(finding["intent_id"].(float64)) != sourceIntent {
		t.Fatalf("inherited finding provenance: %#v", finding)
	}

	detailInput, _ := json.Marshal(map[string]any{"id": sourceFact})
	detail := callReadJSON(t, tools.nodeDetail(), string(detailInput)).(map[string]any)
	if detail["inherited"] != true || int64(detail["source_task_id"].(float64)) != source.ID {
		t.Fatalf("inherited node detail: %#v", detail)
	}

	traceInput, _ := json.Marshal(map[string]any{"intent_id": sourceIntent})
	trace := callReadJSON(t, tools.getWorkerTrace(), string(traceInput)).(map[string]any)
	if trace["inherited"] != true || int64(trace["source_task_id"].(float64)) != source.ID {
		t.Fatalf("inherited trace: %#v", trace)
	}
	steps := trace["steps"].([]any)
	if len(steps) != 1 || int64(steps[0].(map[string]any)["step_id"].(float64)) != stepID {
		t.Fatalf("inherited trace steps: %#v", steps)
	}
	output := callReadJSON(t, tools.getWorkerOutput(), string(traceInput)).(map[string]any)
	if output["inherited"] != true || output["final_text"] != "shared trace full detail" {
		t.Fatalf("inherited worker output: %#v", output)
	}

	search := callReadJSON(t, tools.searchAllWorkerTraces(), `{"q":"shared trace marker"}`).(map[string]any)
	hits := search["hits"].([]any)
	if len(hits) != 1 || hits[0].(map[string]any)["inherited"] != true {
		t.Fatalf("inherited trace search: %#v", hits)
	}
}
