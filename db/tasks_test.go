package db

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestTaskLifecycleAndDeleteCascade(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("迁移测试", "目标X", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// populate the exploration subgraph
	es := d.Exploration(tk.ExplorationID)
	if _, err := es.AddIntent(map[string]any{"summary": "x"}, 5, nil, "planner"); err != nil {
		t.Fatal(err)
	}

	// pause + status
	if err := d.SetPaused(tk.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTask(tk.ID)
	if err != nil || got == nil || !got.Paused {
		t.Fatalf("paused not persisted: %+v err=%v", got, err)
	}
	if got.Queued {
		t.Fatalf("new task should not be queued: %+v", got)
	}

	// queued (concurrency-hold) flag round-trips independently of paused
	if err := d.SetQueued(tk.ID, true); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g == nil || !g.Queued {
		t.Fatalf("queued not persisted: %+v", g)
	}
	if err := d.SetQueued(tk.ID, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g == nil || g.Queued {
		t.Fatalf("queued not cleared: %+v", g)
	}

	// list contains it
	list, _ := d.ListTasks()
	found := false
	for _, x := range list {
		if x.ID == tk.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("task not in list")
	}

	// delete cascades exploration subgraph
	if err := d.DeleteTask(tk.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := d.GetTask(tk.ID); g != nil {
		t.Fatalf("task should be gone")
	}
	var nodes int
	d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, tk.ExplorationID).Scan(&nodes)
	if nodes != 0 {
		t.Fatalf("exploration nodes should be cascade-deleted, got %d", nodes)
	}
	var exps int
	d.QueryRow(`SELECT count(*) FROM explorations WHERE id=$1`, tk.ExplorationID).Scan(&exps)
	if exps != 0 {
		t.Fatalf("exploration should be deleted, got %d", exps)
	}
}

func TestTaskDeleteCascadeAssets(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	first, err := d.CreateTask("级联删除测试", "目标A", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.CreateTask("共享资产保留测试", "目标B", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	assets := d.Assets()
	exclusiveID, err := assets.UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("delete-%d.example.test", first.ID),
		TaskID: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	sharedDomain := fmt.Sprintf("shared-%d.example.test", first.ID)
	sharedID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: sharedDomain, TaskID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: sharedDomain, TaskID: second.ID}); err != nil {
		t.Fatal(err)
	}
	anchorOnlyDomain := fmt.Sprintf("anchor-only-%d.example.test", first.ID)
	anchorOnlyID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: anchorOnlyDomain})
	if err != nil {
		t.Fatal(err)
	}
	firstOrigin, err := d.Exploration(first.ExplorationID).OriginFactID()
	if err != nil || firstOrigin == 0 {
		t.Fatalf("first origin: id=%d err=%v", firstOrigin, err)
	}
	if err := d.Exploration(first.ExplorationID).Anchor(firstOrigin, anchorOnlyID); err != nil {
		t.Fatal(err)
	}
	protectedDomain := fmt.Sprintf("other-anchor-%d.example.test", first.ID)
	protectedID, err := assets.UpsertRootDomain(UpsertRootDomainReq{Domain: protectedDomain, TaskID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	secondOrigin, err := d.Exploration(second.ExplorationID).OriginFactID()
	if err != nil || secondOrigin == 0 {
		t.Fatalf("second origin: id=%d err=%v", secondOrigin, err)
	}
	if err := d.Exploration(second.ExplorationID).Anchor(secondOrigin, protectedID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(first.ID)
		_ = d.DeleteTask(second.ID)
		_, _ = assets.DeleteByIDs([]int64{exclusiveID, sharedID, anchorOnlyID, protectedID})
	})

	hosts, err := assets.HostsByTask(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	hostSet := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		hostSet[host] = true
	}
	if !hostSet[fmt.Sprintf("delete-%d.example.test", first.ID)] || !hostSet[sharedDomain] {
		t.Fatalf("task hosts missing cascade fixtures: %v", hosts)
	}
	deletableHosts, err := assets.HostsForTaskDeletion(first.ID, first.ExplorationID)
	if err != nil {
		t.Fatal(err)
	}
	deletableSet := make(map[string]bool, len(deletableHosts))
	for _, host := range deletableHosts {
		deletableSet[host] = true
	}
	if !deletableSet[fmt.Sprintf("delete-%d.example.test", first.ID)] || !deletableSet[anchorOnlyDomain] {
		t.Fatalf("exclusive or anchor-only cleanup host missing: %v", deletableHosts)
	}
	if deletableSet[sharedDomain] || deletableSet[protectedDomain] {
		t.Fatalf("shared host was not protected from traffic cleanup: %v", deletableHosts)
	}

	findingID, err := d.AddFinding(first.ID, 0, "__task_delete_cascade__", "", SeverityHigh, "summary", "evidence", "tester", []int64{exclusiveID})
	if err != nil {
		t.Fatal(err)
	}

	result, err := d.DeleteTaskCascade(first.ID, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.AssetsDeleted < 2 || result.AssetsDetached < 2 {
		t.Fatalf("unexpected asset cleanup result: %+v", result)
	}
	if result.FindingsDeleted != 1 {
		t.Fatalf("unexpected finding cleanup result: %+v", result)
	}
	if finding, err := d.GetFinding(findingID); err != nil || finding != nil {
		t.Fatalf("finding should be deleted, got finding=%+v err=%v", finding, err)
	}
	remaining, err := assets.GetByIDs([]int64{exclusiveID, sharedID, anchorOnlyID, protectedID})
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("only shared and other-anchored assets should remain, got %+v", remaining)
	}
	byID := make(map[int64]*Asset, len(remaining))
	for _, asset := range remaining {
		byID[asset.ID] = asset
	}
	if shared := byID[sharedID]; shared == nil || len(shared.TaskIDs) != 1 || shared.TaskIDs[0] != second.ID {
		t.Fatalf("deleted task should be detached from shared asset: %+v", shared)
	}
	if protected := byID[protectedID]; protected == nil || len(protected.TaskIDs) != 0 {
		t.Fatalf("other task's anchor should preserve the asset after detaching ownership: %+v", protected)
	}
}

func TestTaskRelationsAndLLMFailoverChain(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	suffix := time.Now().UnixNano()
	profileIDs := make([]int64, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := d.SaveProfile(&LLMProfile{
			Name: fmt.Sprintf("task-chain-%d-%d", suffix, i), Format: "openai",
			Model: fmt.Sprintf("model-%d", i), APIKey: "test-key",
		})
		if err != nil {
			t.Fatal(err)
		}
		profileIDs = append(profileIDs, id)
	}

	sourceA, err := d.CreateTask("source A", "goal A", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sourceB, err := d.CreateTask("source B", "goal B", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := d.CreateTaskWithOptions("child", "new goal", TaskCreateOptions{
		SourceTaskIDs: []int64{sourceA.ID, sourceB.ID},
		LLMProfileIDs: profileIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteTask(child.ID)
		_ = d.DeleteTask(sourceA.ID)
		_ = d.DeleteTask(sourceB.ID)
		for _, id := range profileIDs {
			_ = d.DeleteProfile(id)
		}
	})

	got, err := d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.SourceTaskIDs) != fmt.Sprint([]int64{sourceA.ID, sourceB.ID}) {
		t.Fatalf("unexpected direct sources: %v", got.SourceTaskIDs)
	}
	if fmt.Sprint(got.LLMProfileIDs) != fmt.Sprint(profileIDs) || got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profileIDs[0] {
		t.Fatalf("unexpected initial chain: %+v", got)
	}
	initialRevision := got.LLMChainRevision
	if err := d.ReplaceTaskLLMProfiles(child.ID, profileIDs, profileIDs[0]); err != nil {
		t.Fatal(err)
	}
	staleRevision, err := d.MarkTaskLLMProfileQuotaExhaustedAtRevision(child.ID, profileIDs[0], initialRevision, "late quota from prior generation")
	if err != nil {
		t.Fatal(err)
	}
	if !staleRevision.Stale || staleRevision.Advanced || staleRevision.ChainExhausted {
		t.Fatalf("old chain revision changed replacement chain: %+v", staleRevision)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LLMChainRevision <= initialRevision || got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profileIDs[0] {
		t.Fatalf("replacement revision/cursor not preserved: %+v", got)
	}

	transition, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[0], "insufficient_quota")
	if err != nil {
		t.Fatal(err)
	}
	if !transition.Advanced || transition.ChainExhausted || transition.NextProfileID == nil || *transition.NextProfileID != profileIDs[1] {
		t.Fatalf("unexpected first transition: %+v", transition)
	}
	late, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[0], "late duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if late.Advanced || late.NextProfileID == nil || *late.NextProfileID != profileIDs[1] {
		t.Fatalf("late failure advanced past current profile: %+v", late)
	}
	if _, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[1], "quota_exceeded"); err != nil {
		t.Fatal(err)
	}
	last, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[2], "余额不足")
	if err != nil {
		t.Fatal(err)
	}
	if !last.Advanced || !last.ChainExhausted || last.NextProfileID != nil {
		t.Fatalf("unexpected exhausted transition: %+v", last)
	}
	duplicateLast, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[2], "late final duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if duplicateLast.Advanced || !duplicateLast.ChainExhausted || duplicateLast.NextProfileID != nil {
		t.Fatalf("duplicate final failure must be idempotent: %+v", duplicateLast)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID != nil || got.LLMFailoverState != "chain_exhausted" {
		t.Fatalf("chain exhaustion was not persisted: %+v", got)
	}

	// Two requests can observe the same last active profile. Only the transaction
	// that clears the cursor may report an advance; the late one is idempotent.
	if err := d.ReplaceTaskLLMProfiles(child.ID, []int64{profileIDs[2]}, profileIDs[2]); err != nil {
		t.Fatal(err)
	}
	transitions := make(chan TaskLLMTransition, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transition, markErr := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[2], "concurrent final quota")
			if markErr != nil {
				errs <- markErr
				return
			}
			transitions <- transition
		}()
	}
	wg.Wait()
	close(errs)
	close(transitions)
	for markErr := range errs {
		t.Fatal(markErr)
	}
	advanced := 0
	for transition := range transitions {
		if transition.Advanced {
			advanced++
		}
		if !transition.ChainExhausted {
			t.Fatalf("concurrent final transition must report exhausted chain: %+v", transition)
		}
	}
	if advanced != 1 {
		t.Fatalf("last profile advanced %d times, want exactly once", advanced)
	}

	// Starting manually from the middle consumes only candidates after that cursor.
	// Earlier ready profiles must not be revived by hydration or unrelated deletes.
	if err := d.ReplaceTaskLLMProfiles(child.ID, []int64{profileIDs[2], profileIDs[0], profileIDs[1]}, profileIDs[0]); err != nil {
		t.Fatal(err)
	}
	if step, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[0], "middle quota"); err != nil || step.NextProfileID == nil || *step.NextProfileID != profileIDs[1] {
		t.Fatalf("middle cursor did not advance to its successor: transition=%+v err=%v", step, err)
	}
	if end, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[1], "tail quota"); err != nil || !end.ChainExhausted {
		t.Fatalf("tail did not exhaust manual chain: transition=%+v err=%v", end, err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID != nil || got.LLMFailoverState != "chain_exhausted" {
		t.Fatalf("hydration revived a profile before the manual cursor: %+v", got)
	}
	unrelatedID, err := d.SaveProfile(&LLMProfile{
		Name: fmt.Sprintf("task-chain-unrelated-%d", suffix), Format: "openai", Model: "other", APIKey: "test-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProfile(unrelatedID); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID != nil || got.LLMFailoverState != "chain_exhausted" {
		t.Fatalf("deleting an unrelated profile revived an exhausted chain: %+v", got)
	}

	// A call that finishes after its profile was removed from the chain is stale:
	// preserve the new cursor and surface the original provider error upstream.
	if err := d.ReplaceTaskLLMProfiles(child.ID, []int64{profileIDs[2], profileIDs[1]}, profileIDs[2]); err != nil {
		t.Fatal(err)
	}
	stale, err := d.MarkTaskLLMProfileQuotaExhausted(child.ID, profileIDs[0], "obsolete in-flight quota")
	if err != nil {
		t.Fatalf("removed in-flight profile returned an internal error: %v", err)
	}
	if stale.Advanced || stale.ChainExhausted || stale.NextProfileID != nil {
		t.Fatalf("removed in-flight profile changed the replacement chain: %+v", stale)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profileIDs[2] {
		t.Fatalf("stale failure changed active replacement profile: %+v", got)
	}

	if err := d.ReplaceTaskLLMProfiles(child.ID, []int64{profileIDs[2], profileIDs[0], profileIDs[1]}, profileIDs[0]); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profileIDs[0] || got.LLMFailoverState != "ready" || got.LLMFailoverReason != "" {
		t.Fatalf("chain reset did not clear failure state: %+v", got)
	}

	if err := d.DeleteProfile(profileIDs[0]); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID == nil || *got.ActiveLLMProfileID != profileIDs[1] {
		t.Fatalf("deleting active profile did not select next ready entry: %+v", got)
	}
	if err := d.DeleteProfile(profileIDs[1]); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveLLMProfileID != nil || len(got.LLMProfileIDs) != 0 || got.LLMFailoverState != "default" {
		t.Fatalf("deleting active chain tail must fall back instead of wrapping: %+v", got)
	}

	if err := d.DeleteTask(sourceA.ID); err != nil {
		t.Fatal(err)
	}
	got, err = d.GetTask(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.SourceTaskIDs) != fmt.Sprint([]int64{sourceB.ID}) {
		t.Fatalf("source delete did not cascade only its relation: %v", got.SourceTaskIDs)
	}
}

func TestTaskContextRejectsDuplicatesAndTerminalEdits(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer d.Close()

	source, err := d.CreateTask("source", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(source.ID) })
	if _, err := d.CreateTaskWithOptions("bad", "goal", TaskCreateOptions{SourceTaskIDs: []int64{source.ID, source.ID}}); err == nil {
		t.Fatal("duplicate source task ids should be rejected")
	}

	task, err := d.CreateTask("terminal", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	if err := d.SetStatus(task.ID, "done"); err != nil {
		t.Fatal(err)
	}
	if err := d.ReplaceTaskLLMProfiles(task.ID, nil, 0); err == nil {
		t.Fatal("terminal task LLM edit should be rejected")
	}
}
