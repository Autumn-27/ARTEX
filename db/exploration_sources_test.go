package db

import (
	"fmt"
	"testing"
)

func TestInheritedActivityReadsRequireTerminalIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	expID, err := d.CreateExploration("source activity boundary", "source activity boundary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID) })
	store := d.Exploration(expID)
	intentID, err := store.AddIntent(map[string]any{"summary": "source work"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	appendStep := func(kind, summary string) int64 {
		t.Helper()
		id, appendErr := store.AppendActivity(Activity{
			NodeID: &intentID, Worker: "worker", Kind: kind, Summary: summary, Detail: summary + " detail",
		})
		if appendErr != nil {
			t.Fatal(appendErr)
		}
		return id
	}
	textID := appendStep("text", "shared text")
	thinkingID := appendStep("thinking", "private reasoning")
	usageID := appendStep("usage", "private accounting")
	resultID := appendStep("result", "shared result")
	if err := store.SetIntentState(intentID, "done"); err != nil {
		t.Fatal(err)
	}

	page, more, err := store.ActivityPageForTerminalIntent(intentID, 0, 10)
	if err != nil || more || len(page) != 2 || page[0].ID != textID || page[1].ID != resultID {
		t.Fatalf("terminal page boundary: page=%+v more=%v err=%v", page, more, err)
	}
	list, cursor, err := store.ActivityListForTerminalIntent(intentID, 0, 10)
	if err != nil || len(list) != 2 || cursor != resultID {
		t.Fatalf("terminal list boundary: list=%+v cursor=%d err=%v", list, cursor, err)
	}
	trace, err := store.ActivityTraceForTerminalIntent(intentID, 10)
	if err != nil || len(trace) != 2 {
		t.Fatalf("terminal trace boundary: trace=%+v err=%v", trace, err)
	}
	hits, err := store.ActivityTraceSearchForTerminalIntent(intentID, "shared", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("terminal search boundary: hits=%+v err=%v", hits, err)
	}
	details, err := store.ActivityByIDsForTerminalIntents([]int64{textID, thinkingID, usageID, resultID})
	if err != nil || len(details) != 2 || details[0].ID != textID || details[1].ID != resultID {
		t.Fatalf("terminal detail boundary: details=%+v err=%v", details, err)
	}

	// Reopening a source intent must close every inherited activity read even if
	// callers still hold a stale terminal-state snapshot.
	if err := store.SetIntentState(intentID, "running"); err != nil {
		t.Fatal(err)
	}
	page, _, err = store.ActivityPageForTerminalIntent(intentID, 0, 10)
	if err != nil || len(page) != 0 {
		t.Fatalf("reopened intent leaked through page: page=%+v err=%v", page, err)
	}
	list, _, err = store.ActivityListForTerminalIntent(intentID, 0, 10)
	if err != nil || len(list) != 0 {
		t.Fatalf("reopened intent leaked through list: list=%+v err=%v", list, err)
	}
	trace, err = store.ActivityTraceForTerminalIntent(intentID, 10)
	if err != nil || len(trace) != 0 {
		t.Fatalf("reopened intent leaked through trace: trace=%+v err=%v", trace, err)
	}
	hits, err = store.ActivityTraceSearchForTerminalIntent(intentID, "shared", 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("reopened intent leaked through search: hits=%+v err=%v", hits, err)
	}
	details, err = store.ActivityByIDsForTerminalIntents([]int64{textID, resultID})
	if err != nil || len(details) != 0 {
		t.Fatalf("reopened intent leaked through detail: details=%+v err=%v", details, err)
	}
}

func TestExplorationDirectSourceReadView(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	grand, err := d.CreateTask("grand source", "grand goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("direct source", "source goal", TaskCreateOptions{
		SourceTaskIDs: []int64{grand.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("current", "current goal", TaskCreateOptions{
		SourceTaskIDs: []int64{source.ID},
	})
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

	grandFact, err := grandStore.AddNode(KindFact, map[string]any{"summary": "grand-only fact"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceFact, err := sourceStore.AddNode(KindFact, map[string]any{"summary": "direct source fact"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceFinding, err := sourceStore.AddNode(KindFinding, map[string]any{"summary": "direct source finding"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceIntent, err := sourceStore.AddIntent(map[string]any{"summary": "source work"}, 9, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.Link(sourceIntent, RelYields, sourceFinding); err != nil {
		t.Fatal(err)
	}
	currentIntent, err := currentStore.AddIntent(map[string]any{"summary": "current work"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	currentFact, err := currentStore.AddNode(KindFact, map[string]any{"summary": "current fact"}, 0, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	sources, err := currentStore.DirectSourceStores()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Task.TaskID != source.ID {
		t.Fatalf("want only direct source %d, got %+v", source.ID, sources)
	}

	frontier, err := currentStore.Frontier(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(frontier) != 1 || frontier[0].ID != currentIntent {
		t.Fatalf("source intent leaked into local frontier: %+v", frontier)
	}
	if claimed, err := currentStore.ClaimIntent(sourceIntent, "wrong-task-worker"); err != nil || claimed {
		t.Fatalf("source intent must not be claimable: claimed=%v err=%v", claimed, err)
	}
	if got, err := currentStore.GetNodeWithSources(sourceIntent); err != nil || got != nil {
		t.Fatalf("live source intent must not be readable as inherited history: node=%+v err=%v", got, err)
	}
	if got, err := currentStore.ListByKindWithSources(KindIntent, 100); err != nil || len(got) != 1 || got[0].ID != currentIntent {
		t.Fatalf("live source intent leaked through inherited list: nodes=%+v err=%v", got, err)
	}
	if got, err := currentStore.FindingIntentsWithSources(); err != nil || got[sourceFinding] != 0 {
		t.Fatalf("live source intent leaked through finding lineage: lineage=%+v err=%v", got, err)
	}

	facts, err := currentStore.ListByKindWithSources(KindFact, 100)
	if err != nil {
		t.Fatal(err)
	}
	seenCurrent, seenSource, seenGrand := false, false, false
	for _, fact := range facts {
		switch fact.ID {
		case currentFact:
			seenCurrent = !fact.Inherited && fact.SourceTaskID == 0
		case sourceFact:
			seenSource = fact.Inherited && fact.SourceTaskID == source.ID
		case grandFact:
			seenGrand = true
		}
	}
	if !seenCurrent || !seenSource || seenGrand {
		t.Fatalf("unexpected direct-source fact view: current=%v source=%v grand=%v", seenCurrent, seenSource, seenGrand)
	}

	gotSource, err := currentStore.GetNodeWithSources(sourceFact)
	if err != nil || gotSource == nil || !gotSource.Inherited || gotSource.SourceTaskID != source.ID {
		t.Fatalf("source node lookup: node=%+v err=%v", gotSource, err)
	}
	gotGrand, err := currentStore.GetNodeWithSources(grandFact)
	if err != nil || gotGrand != nil {
		t.Fatalf("indirect source must be invisible: node=%+v err=%v", gotGrand, err)
	}

	// Existing write methods remain bound to currentStore.expID. Passing an
	// inherited id is a no-op and cannot mutate the source blackboard.
	if err := currentStore.SetNodeState(sourceFact, "dismissed"); err != nil {
		t.Fatal(err)
	}
	unchanged, err := sourceStore.GetNode(sourceFact)
	if err != nil || unchanged == nil || unchanged.State != "confirmed" {
		t.Fatalf("inherited fact was mutated: node=%+v err=%v", unchanged, err)
	}

	if err := sourceStore.SetIntentState(sourceIntent, "done"); err != nil {
		t.Fatal(err)
	}
	if got, err := currentStore.GetNodeWithSources(sourceIntent); err != nil || got == nil || !got.Inherited {
		t.Fatalf("terminal source intent should become readable history: node=%+v err=%v", got, err)
	}
	if got, err := currentStore.FindingIntentsWithSources(); err != nil || got[sourceFinding] != sourceIntent {
		t.Fatalf("terminal source finding lineage missing: lineage=%+v err=%v", got, err)
	}
	sourcePlannerStep, err := sourceStore.AppendActivity(Activity{
		Worker: "planner", Kind: "text", Summary: "private source planner", Detail: "private source planner transcript",
	})
	if err != nil {
		t.Fatal(err)
	}
	openSourceIntent, err := sourceStore.AddIntent(map[string]any{"summary": "source work still running"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.SetIntentState(openSourceIntent, "running"); err != nil {
		t.Fatal(err)
	}
	openSourceStep, err := sourceStore.AppendActivity(Activity{
		NodeID: &openSourceIntent, Worker: "source-live-worker", Kind: "text",
		Summary: "private live source work", Detail: "private live source transcript",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceStep, err := sourceStore.AppendActivity(Activity{
		NodeID: &sourceIntent, Worker: "source-worker", Kind: "tool_result", Tool: "HTTP",
		Summary: "source-secret response", Detail: "source-secret full detail",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentPlannerStep, err := currentStore.AppendActivity(Activity{
		Worker: "planner", Kind: "text", Summary: "current planner", Detail: "current planner detail",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentThinkingStep, err := currentStore.AppendActivity(Activity{
		Worker: "planner", Kind: "thinking", Summary: "current thinking", Detail: "current thinking detail",
	})
	if err != nil {
		t.Fatal(err)
	}
	grandStep, err := grandStore.AppendActivity(Activity{
		NodeID: &grandFact, Worker: "grand-worker", Kind: "tool_result", Tool: "HTTP",
		Summary: "grand-only response", Detail: "grand-only detail",
	})
	if err != nil {
		t.Fatal(err)
	}

	trace, err := currentStore.ActivityTraceWithSources(sourceIntent, 20)
	if err != nil || len(trace) != 1 || trace[0].ID != sourceStep || !trace[0].Inherited || trace[0].SourceTaskID != source.ID {
		t.Fatalf("source trace lookup: trace=%+v err=%v", trace, err)
	}
	hits, err := currentStore.ActivityTraceSearchAllWithSources(0, "source-secret", 20)
	if err != nil || len(hits) != 1 || hits[0].ID != sourceStep || !hits[0].Inherited {
		t.Fatalf("source trace search: hits=%+v err=%v", hits, err)
	}
	grandHits, err := currentStore.ActivityTraceSearchAllWithSources(0, "grand-only", 20)
	if err != nil || len(grandHits) != 0 {
		t.Fatalf("indirect source trace leaked: hits=%+v err=%v", grandHits, err)
	}
	liveHits, err := currentStore.ActivityTraceSearchAllWithSources(0, "private live source", 20)
	if err != nil || len(liveHits) != 0 {
		t.Fatalf("live source trace leaked: hits=%+v err=%v", liveHits, err)
	}
	if liveTrace, err := currentStore.ActivityTraceWithSources(openSourceIntent, 20); err != nil || len(liveTrace) != 0 {
		t.Fatalf("live inherited intent trace leaked: trace=%+v err=%v", liveTrace, err)
	}
	details, err := currentStore.ActivityByIDsWithSources([]int64{
		sourcePlannerStep, openSourceStep, sourceStep, currentPlannerStep, grandStep,
	})
	if err != nil || len(details) != 2 || details[0].ID != sourceStep || !details[0].Inherited || details[1].ID != currentPlannerStep || details[1].Inherited {
		t.Fatalf("source trace detail allow-list: details=%+v err=%v", details, err)
	}
	if detail, err := currentStore.ActivityDetailWithSources(sourcePlannerStep); err != nil || detail != "" {
		t.Fatalf("source planner transcript leaked: detail=%q err=%v", detail, err)
	}
	if detail, err := currentStore.ActivityDetailWithSources(openSourceStep); err != nil || detail != "" {
		t.Fatalf("live source intent transcript leaked: detail=%q err=%v", detail, err)
	}
	if detail, err := currentStore.ActivityDetailWithSources(currentPlannerStep); err != nil || detail != "current planner detail" {
		t.Fatalf("local activity detail behavior changed: detail=%q err=%v", detail, err)
	}
	if detail, err := currentStore.ActivityDetailWithSources(currentThinkingStep); err != nil || detail != "current thinking detail" {
		t.Fatalf("local thinking detail behavior changed: detail=%q err=%v", detail, err)
	}
}

func TestTaskAssetContextUsesDirectSourceScopeAndAnchors(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	grand, err := d.CreateTask("grand assets", "grand goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := d.CreateTaskWithOptions("source assets", "source goal", TaskCreateOptions{SourceTaskIDs: []int64{grand.ID}})
	if err != nil {
		t.Fatal(err)
	}
	current, err := d.CreateTaskWithOptions("current assets", "current goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assets := d.Assets()
	sourceTestedDomain := fmt.Sprintf("context-tested-%d.invalid", source.ID)
	sourceUntestedDomain := fmt.Sprintf("context-untested-%d.invalid", source.ID)
	sourceAnchoredOnlyDomain := fmt.Sprintf("context-anchor-only-%d.invalid", source.ID)
	grandDomain := fmt.Sprintf("context-grand-%d.invalid", grand.ID)
	sourceTestedID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: sourceTestedDomain, TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	sourceUntestedID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: sourceUntestedDomain, TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	sourceAnchoredOnlyID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: sourceAnchoredOnlyDomain, TaskID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	grandID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: grandDomain, TaskID: grand.ID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(current.ID)
		_ = d.DeleteTask(source.ID)
		_ = d.DeleteTask(grand.ID)
		_, _ = assets.DeleteByIDs([]int64{sourceTestedID, sourceUntestedID, sourceAnchoredOnlyID, grandID})
	})
	if _, err := assets.AddAgentScope(source.ID, "root_domain", sourceTestedDomain, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.AddAgentScope(source.ID, "root_domain", sourceUntestedDomain, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := assets.AddAgentScope(grand.ID, "root_domain", grandDomain, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(source.ExplorationID).AddNode(KindFact, map[string]any{"summary": "tested source asset"}, 0, "confirmed", "worker", []int64{sourceTestedID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(source.ExplorationID).AddNode(KindFact, map[string]any{"summary": "anchored-only source asset"}, 0, "confirmed", "worker", []int64{sourceAnchoredOnlyID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exploration(grand.ExplorationID).AddNode(KindFact, map[string]any{"summary": "indirect tested asset"}, 0, "confirmed", "worker", []int64{grandID}); err != nil {
		t.Fatal(err)
	}

	scopes, err := assets.ListTaskScopeWithSources(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0].TaskID != source.ID || scopes[1].TaskID != source.ID {
		t.Fatalf("direct source scopes only: %+v", scopes)
	}
	cov, err := assets.TaskCoverageWithSources(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cov.Denominator != 3 || cov.Tested != 2 {
		t.Fatalf("combined coverage: %+v", cov)
	}
	untested, total, err := assets.ListUntestedAssetsWithSources(current.ID, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(untested) != 1 || untested[0].ID != sourceUntestedID {
		t.Fatalf("combined untested assets: total=%d assets=%+v", total, untested)
	}
	hosts, err := assets.HostsByTaskWithSources(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	hostSet := map[string]bool{}
	for _, host := range hosts {
		hostSet[host] = true
	}
	if !hostSet[sourceTestedDomain] || !hostSet[sourceUntestedDomain] || !hostSet[sourceAnchoredOnlyDomain] || hostSet[grandDomain] {
		t.Fatalf("direct source hosts only: %v", hosts)
	}
	refs, err := d.Exploration(current.ExplorationID).AssetRefsWithSources(sourceAnchoredOnlyID)
	if err != nil || len(refs) != 1 || !refs[0].Inherited || refs[0].SourceTaskID != source.ID || refs[0].Kind != KindFact {
		t.Fatalf("source asset refs provenance: refs=%+v err=%v", refs, err)
	}
	grandRefs, err := d.Exploration(current.ExplorationID).AssetRefsWithSources(grandID)
	if err != nil || len(grandRefs) != 0 {
		t.Fatalf("indirect source asset refs leaked: refs=%+v err=%v", grandRefs, err)
	}
	graph, err := assets.BuildCoverageGraph(current.ID, current.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	seenAnchoredOnly := false
	for _, node := range graph.Nodes {
		if node.AssetID == sourceAnchoredOnlyID {
			seenAnchoredOnly = node.Tested && node.InScope
		}
	}
	if !seenAnchoredOnly {
		t.Fatalf("anchored-only source asset missing from coverage graph: %+v", graph.Nodes)
	}
}
