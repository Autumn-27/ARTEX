package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	assets := d.Assets()
	assetID, err := assets.UpsertRootDomain(db.UpsertRootDomainReq{Domain: domain})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = assets.DeleteByIDs([]int64{assetID}) })

	task, err := d.CreateTaskWithOptions("company context", "read configured scope", db.TaskCreateOptions{
		CompanyIDs: []int64{companyID},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })

	tools := NewToolSet(d.Exploration(task.ExplorationID), "planner")
	tools.SetTaskID(task.ID)
	tools.SetAssetStore(assets, companies)
	linkedAssets, err := assets.QueryByTask(task.ID, "root_domain", 10, 0)
	if err != nil || len(linkedAssets) != 1 || linkedAssets[0].ID != assetID {
		t.Fatalf("company asset was not linked to task: assets=%+v err=%v", linkedAssets, err)
	}
	if linkedAssets[0].TaskSource != "company" || !strings.Contains(linkedAssets[0].TaskSourceSummary, companiesName(t, companies, companyID)) {
		t.Fatalf("company asset provenance missing: %+v", linkedAssets[0])
	}
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
	hosts, ok := coverage["hosts"].([]string)
	if !ok || !containsString(hosts, domain) {
		t.Fatalf("company asset host missing from agent context: %#v", coverage["hosts"])
	}
	untested := callReadJSON(t, tools.listUntestedAssets(), `{"type":"root_domain","page":1,"page_size":10}`)
	if !strings.Contains(fmt.Sprint(untested), domain) {
		t.Fatalf("company asset missing from untested backlog: %#v", untested)
	}
	byTask := callReadJSON(t, tools.listAssets(), fmt.Sprintf(
		`{"dsl":"task_id==%d","type":"root_domain","limit":10}`, task.ID,
	))
	if !strings.Contains(fmt.Sprint(byTask), domain) {
		t.Fatalf("company asset missing from task-scoped agent query: %#v", byTask)
	}
	byCompany := callReadJSON(t, tools.listAssets(), fmt.Sprintf(
		`{"dsl":"company_id==%d","type":"root_domain","limit":10}`, companyID,
	))
	if !strings.Contains(fmt.Sprint(byCompany), domain) {
		t.Fatalf("company asset missing from company-scoped agent query: %#v", byCompany)
	}
	assetJSON, _ := json.Marshal(assetID)
	intentID, err := tools.addOneIntent(intentItem{
		Summary:  "test associated company asset",
		AssetIDs: []json.RawMessage{assetJSON},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerAssets, err := assets.IntentAssets(task.ID)
	if err != nil || len(workerAssets) != 1 || workerAssets[0].IntentID != intentID || workerAssets[0].AssetID != assetID {
		t.Fatalf("worker target did not retain company asset: assets=%+v err=%v", workerAssets, err)
	}

	coverageDisabled := false
	disabledTask, err := d.CreateTaskWithOptions("company context without coverage", "still expose company assets", db.TaskCreateOptions{
		CompanyIDs: []int64{companyID}, CoverageEnabled: &coverageDisabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(disabledTask.ID) })
	disabledTools := NewToolSet(d.Exploration(disabledTask.ExplorationID), "planner")
	disabledTools.SetTaskID(disabledTask.ID)
	disabledTools.SetCoverageEnabled(false)
	disabledTools.SetAssetStore(assets, companies)
	disabledOverview := disabledTools.graphOverviewData()
	disabledCoverage, ok := disabledOverview["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("coverage-disabled task lost asset context: %#v", disabledOverview["coverage"])
	}
	disabledHosts, ok := disabledCoverage["hosts"].([]string)
	if !ok || !containsString(disabledHosts, domain) {
		t.Fatalf("coverage-disabled task lost company asset host: %#v", disabledCoverage["hosts"])
	}
	if _, exists := disabledCoverage["denominator"]; exists {
		t.Fatalf("coverage-disabled task unexpectedly exposed metrics: %#v", disabledCoverage)
	}
	if linked, err := assets.QueryByTask(disabledTask.ID, "root_domain", 10, 0); err != nil || len(linked) != 1 || linked[0].ID != assetID {
		t.Fatalf("coverage-disabled task asset link=%+v err=%v", linked, err)
	}
	disabledIntentID, err := disabledTools.addOneIntent(intentItem{
		Summary:  "test associated company asset without coverage metrics",
		AssetIDs: []json.RawMessage{assetJSON},
	})
	if err != nil {
		t.Fatal(err)
	}
	disabledWorkerAssets, err := assets.IntentAssets(disabledTask.ID)
	if err != nil || len(disabledWorkerAssets) != 1 || disabledWorkerAssets[0].IntentID != disabledIntentID || disabledWorkerAssets[0].AssetID != assetID {
		t.Fatalf("coverage-disabled worker target=%+v err=%v", disabledWorkerAssets, err)
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func companiesName(t *testing.T, companies *db.CompanyStore, companyID int64) string {
	t.Helper()
	company, err := companies.GetCompany(companyID)
	if err != nil || company == nil {
		t.Fatalf("company %d: company=%+v err=%v", companyID, company, err)
	}
	return company.Name
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

	facts := callReadJSON(t, tools.listFacts(), `{}`).(map[string]any)["facts"].([]any)
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

// TestGetWorkerTraceStepIDsDegradeGracefully pins the over-cap behaviour: instead
// of erroring, get_worker_trace returns the first 5 requested steps and tells the
// model which ids it deferred, after de-duplicating and dropping invalid ids.
func TestGetWorkerTraceStepIDsDegradeGracefully(t *testing.T) {
	d := testDB(t)
	defer d.Close()

	task, err := d.CreateTask("trace-cap", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	store := d.Exploration(task.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "cap work"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}

	var stepIDs []int64
	for i := range 6 {
		sid, err := store.AppendActivity(db.Activity{
			NodeID: &intent, Worker: "w", Kind: "result", Tool: "HTTP",
			Summary: fmt.Sprintf("step %d", i), Detail: fmt.Sprintf("detail %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		stepIDs = append(stepIDs, sid)
	}

	tools := NewToolSet(store, "worker")
	tools.SetAssetStore(d.Assets(), d.Companies())

	// Request 7 ids: a duplicate of the first, an invalid 0, then all 6 real ids.
	// After dedup/cleanup that is 6 valid ids — one over the cap.
	requested := []int64{stepIDs[0], stepIDs[0], 0, stepIDs[1], stepIDs[2], stepIDs[3], stepIDs[4], stepIDs[5]}
	input, _ := json.Marshal(map[string]any{"intent_id": intent, "step_ids": requested})
	res := callReadJSON(t, tools.getWorkerTrace(), string(input)).(map[string]any)

	returned, _ := res["returned_step_ids"].([]any)
	if len(returned) != 5 {
		t.Fatalf("returned_step_ids=%v, want the first 5", res["returned_step_ids"])
	}
	// First 5 distinct valid ids, in request order.
	wantReturned := []int64{stepIDs[0], stepIDs[1], stepIDs[2], stepIDs[3], stepIDs[4]}
	for i, raw := range returned {
		if int64(raw.(float64)) != wantReturned[i] {
			t.Fatalf("returned[%d]=%v, want %d", i, raw, wantReturned[i])
		}
	}
	omitted, _ := res["omitted_step_ids"].([]any)
	if len(omitted) != 1 || int64(omitted[0].(float64)) != stepIDs[5] {
		t.Fatalf("omitted_step_ids=%v, want [%d]", res["omitted_step_ids"], stepIDs[5])
	}
	if notice, _ := res["notice"].(string); notice == "" {
		t.Fatalf("notice missing — model would not know a step was deferred")
	}
	if steps, _ := res["steps"].([]any); len(steps) != 5 {
		t.Fatalf("steps=%d, want 5 detail rows", len(steps))
	}

	// At or under the cap: no notice, no omitted list.
	okInput, _ := json.Marshal(map[string]any{"intent_id": intent, "step_ids": stepIDs[:3]})
	okRes := callReadJSON(t, tools.getWorkerTrace(), string(okInput)).(map[string]any)
	if _, hasNotice := okRes["notice"]; hasNotice {
		t.Fatalf("notice present for an in-cap request: %#v", okRes["notice"])
	}
	if _, hasOmitted := okRes["omitted_step_ids"]; hasOmitted {
		t.Fatalf("omitted_step_ids present for an in-cap request: %#v", okRes["omitted_step_ids"])
	}
}
