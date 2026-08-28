package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDeleteFinding verifies删除漏洞 removes both the findings row and its
// originating exploration node (kind='finding').
func TestDeleteFinding(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("删除漏洞测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	// seed a finding node in the task's exploration graph, then a findings row on it
	es := d.Exploration(tk.ExplorationID)
	nodeID, err := es.AddNode(KindFinding, map[string]any{"summary": "x", "severity": "high"}, 5, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := d.AddFinding(tk.ID, nodeID, "XSS", "反射型 XSS", "high", "summary", "poc", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}

	n, err := d.DeleteFinding(fid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteFinding rows: want 1, got %d", n)
	}
	if f, _ := d.GetFinding(fid); f != nil {
		t.Fatalf("finding row should be gone, got %+v", f)
	}
	var cnt int
	d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE id=$1`, nodeID).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("originating finding node should be deleted, still %d", cnt)
	}

	// deleting a non-existent finding is a no-op (0 rows), not an error
	if n, err := d.DeleteFinding(fid); err != nil || n != 0 {
		t.Fatalf("re-delete: want (0,nil), got (%d,%v)", n, err)
	}
}

// TestFindingsPageAndStats exercises ListFindingsPage (filter/sort/paging) and
// FindingStats against the live dev PG. It tags its rows with a unique vulnclass
// so assertions are isolated from any pre-existing data, and cleans up after.
func TestFindingsPageAndStats(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	const vc = "__test_vc_pagination__"
	// clean any leftovers from a prior aborted run, and clean up on exit
	cleanup := func() { _, _ = d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, vc) }
	cleanup()
	defer cleanup()

	// Seed 6 findings under the marker vulnclass: 1 critical, 3 high, 2 low; 2 pending.
	// The critical row carries a name to verify round-trip.
	seed := []struct {
		sev, status, name string
	}{
		{"critical", "pending", "严重漏洞标题"},
		{"high", "resolved", ""},
		{"high", "resolved", ""},
		{"high", "pending", ""},
		{"low", "resolved", ""},
		{"low", "resolved", ""},
	}
	var ids []int64
	for i, s := range seed {
		id, err := d.AddFinding(0, 0, vc, s.name, s.sev, "summary", "poc", "tester", nil)
		if err != nil {
			t.Fatalf("AddFinding[%d]: %v", i, err)
		}
		if _, err := d.SetFindingStatus(id, s.status); err != nil {
			t.Fatalf("SetFindingStatus[%d]: %v", i, err)
		}
		ids = append(ids, id)
	}

	// Filter by our vulnclass → exactly the 6 seeded rows, paged 2 per page.
	p1, total, err := d.ListFindingsPage(FindingFilter{VulnClass: vc, Sort: "severity"}, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if total != 6 {
		t.Fatalf("total: want 6, got %d", total)
	}
	if len(p1) != 2 {
		t.Fatalf("page1 size: want 2, got %d", len(p1))
	}
	// severity sort → critical first (with its name round-tripped), then high.
	if p1[0].Severity != "critical" {
		t.Fatalf("severity sort: want critical first, got %q", p1[0].Severity)
	}
	if p1[0].Name != "严重漏洞标题" {
		t.Fatalf("name round-trip: want 严重漏洞标题, got %q", p1[0].Name)
	}
	if p1[1].Severity != "high" {
		t.Fatalf("severity sort: want high second, got %q", p1[1].Severity)
	}

	// Combined filter: vulnclass + status=pending → 2 rows.
	pend, total, err := d.ListFindingsPage(FindingFilter{VulnClass: vc, Status: FindingPending}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(pend) != 2 {
		t.Fatalf("pending filter: want 2/2, got %d/%d", total, len(pend))
	}

	// Combined filter: vulnclass + severity=high → 3 rows.
	_, total, err = d.ListFindingsPage(FindingFilter{VulnClass: vc, Severity: "high"}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("high filter: want 3, got %d", total)
	}

	// Stats: whole-table, so assert our contribution is reflected (>=) and the
	// marker vulnclass is present.
	st, err := d.FindingStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Critical < 1 {
		t.Fatalf("stats critical undercount: %+v", st)
	}
	if st.Total < 6 || st.High < 3 || st.Low < 2 || st.Pending < 2 {
		t.Fatalf("stats undercount: %+v", st)
	}
	if !slices.Contains(st.VulnClasses, vc) {
		t.Fatalf("stats vulnclasses missing %q", vc)
	}

	// GetFinding: single-row fetch round-trips id/name/severity.
	one, err := d.GetFinding(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if one == nil || one.ID != ids[0] || one.Severity != "critical" || one.Name != "严重漏洞标题" {
		t.Fatalf("GetFinding mismatch: %+v", one)
	}
	if one.Report != "" {
		t.Fatalf("new finding report should be empty, got %q", one.Report)
	}
	// report column round-trips through GetFinding.
	if _, err := d.Exec(`UPDATE findings SET report=$1 WHERE id=$2`, "# 报告\n正文", ids[0]); err != nil {
		t.Fatal(err)
	}
	if one, _ = d.GetFinding(ids[0]); one.Report != "# 报告\n正文" {
		t.Fatalf("report not read back: %q", one.Report)
	}
	for _, test := range []struct {
		name  string
		query string
		want  int
	}{
		{name: "name", query: "严重漏洞标题", want: 1},
		{name: "summary", query: "summary", want: 6},
		{name: "evidence", query: "poc", want: 6},
		{name: "report", query: "正文", want: 1},
		{name: "case insensitive vulnclass", query: strings.ToUpper(vc), want: 6},
	} {
		t.Run("query_"+test.name, func(t *testing.T) {
			matches, searchTotal, searchErr := d.ListFindingsPage(
				FindingFilter{VulnClass: vc, Query: test.query}, 1, 20,
			)
			if searchErr != nil || searchTotal != test.want || len(matches) != test.want {
				t.Fatalf("query %q: len=%d total=%d err=%v, want %d", test.query, len(matches), searchTotal, searchErr, test.want)
			}
		})
	}
	if _, err := d.Exec(`UPDATE findings SET name=$1 WHERE id=$2`, "literal %_ marker", ids[1]); err != nil {
		t.Fatal(err)
	}
	matches, searchTotal, err := d.ListFindingsPage(FindingFilter{VulnClass: vc, Query: "%_"}, 1, 20)
	if err != nil || searchTotal != 1 || len(matches) != 1 || matches[0].ID != ids[1] {
		t.Fatalf("query wildcards must be literal: matches=%+v total=%d err=%v", matches, searchTotal, err)
	}
	if miss, err := d.GetFinding(-1); err != nil || miss != nil {
		t.Fatalf("GetFinding(-1): want nil,nil got %+v,%v", miss, err)
	}

	// SetFindingSeverity: standalone row updates; 0 rows for unknown id.
	if n, err := d.SetFindingSeverity(ids[0], "high"); err != nil || n != 1 {
		t.Fatalf("SetFindingSeverity: want 1,nil got %d,%v", n, err)
	}
	one, _ = d.GetFinding(ids[0])
	if one.Severity != "high" {
		t.Fatalf("severity not updated: %q", one.Severity)
	}
	if n, err := d.SetFindingSeverity(-1, "low"); err != nil || n != 0 {
		t.Fatalf("SetFindingSeverity(-1): want 0,nil got %d,%v", n, err)
	}
}

func TestListFindingsPageUsesStableIDTieBreaker(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	vc := fmt.Sprintf("__test_finding_stable_page_%d__", time.Now().UnixNano())
	defer d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, vc)
	ids := make([]int64, 0, 7)
	for i := 0; i < 7; i++ {
		id, addErr := d.AddFinding(0, 0, vc, "", SeverityHigh, fmt.Sprintf("finding %d", i), "", "test", nil)
		if addErr != nil {
			t.Fatalf("AddFinding[%d]: %v", i, addErr)
		}
		ids = append(ids, id)
	}
	sharedCreatedAt := time.Date(2026, time.August, 21, 8, 30, 0, 0, time.UTC)
	if _, err := d.Exec(`UPDATE findings SET created_at=$1 WHERE vulnclass=$2`, sharedCreatedAt, vc); err != nil {
		t.Fatal(err)
	}

	want := make([]int64, len(ids))
	for i := range ids {
		want[i] = ids[len(ids)-1-i]
	}
	for _, sort := range []string{"time", "severity"} {
		t.Run(sort, func(t *testing.T) {
			var got []int64
			for page := 1; page <= 3; page++ {
				items, total, pageErr := d.ListFindingsPage(FindingFilter{VulnClass: vc, Sort: sort}, page, 3)
				if pageErr != nil {
					t.Fatal(pageErr)
				}
				if total != len(ids) {
					t.Fatalf("page %d total=%d, want %d", page, total, len(ids))
				}
				for _, item := range items {
					got = append(got, item.ID)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("same-timestamp pagination was unstable: got=%v want=%v", got, want)
			}
		})
	}
}

func TestFindingGroupsAndUnassignedPaging(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	vc := fmt.Sprintf("__test_finding_groups_%d__", time.Now().UnixNano())
	cleanupFindings := func() { _, _ = d.Exec(`DELETE FROM findings WHERE vulnclass=$1`, vc) }
	defer cleanupFindings()

	taskA, err := d.CreateTask("group A", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(taskA.ID)
	taskB, err := d.CreateTask("group B", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(taskB.ID)

	seed := []struct {
		taskID   int64
		severity string
		status   string
	}{
		{taskA.ID, SeverityCritical, FindingPending},
		{taskA.ID, SeverityHigh, FindingResolved},
		{taskB.ID, SeverityLow, FindingPending},
		{0, SeverityMedium, FindingPending},
	}
	for i, item := range seed {
		id, addErr := d.AddFinding(item.taskID, 0, vc, "", item.severity, fmt.Sprintf("summary %d", i), "", "test", nil)
		if addErr != nil {
			t.Fatalf("AddFinding[%d]: %v", i, addErr)
		}
		if _, setErr := d.SetFindingStatus(id, item.status); setErr != nil {
			t.Fatalf("SetFindingStatus[%d]: %v", i, setErr)
		}
	}
	matchedGroups, matchedGroupTotal, matchedFindingTotal, err := d.ListFindingGroups(
		FindingFilter{VulnClass: vc, Query: "summary 0"}, 1, 10,
	)
	if err != nil || matchedGroupTotal != 1 || matchedFindingTotal != 1 || len(matchedGroups) != 1 ||
		matchedGroups[0].TaskID == nil || *matchedGroups[0].TaskID != taskA.ID {
		t.Fatalf("query-filtered groups: %+v groups=%d findings=%d err=%v",
			matchedGroups, matchedGroupTotal, matchedFindingTotal, err)
	}
	// Retained findings from deleted tasks join the same bucket as findings that
	// were created without any task.
	if err := d.DeleteTask(taskB.ID); err != nil {
		t.Fatal(err)
	}

	page, groupTotal, findingTotal, err := d.ListFindingGroups(FindingFilter{VulnClass: vc, Sort: "severity"}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || groupTotal != 2 || findingTotal != 4 {
		t.Fatalf("page totals: items=%d groups=%d findings=%d", len(page), groupTotal, findingTotal)
	}
	groups, _, _, err := d.ListFindingGroups(FindingFilter{VulnClass: vc}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	var live, unassigned *FindingGroup
	for i := range groups {
		if groups[i].TaskID == nil {
			unassigned = &groups[i]
		} else if *groups[i].TaskID == taskA.ID {
			live = &groups[i]
		}
	}
	if live == nil || live.Count != 2 || live.Critical != 1 || live.High != 1 || live.TaskDescription != "group A" {
		t.Fatalf("live group mismatch: %+v", live)
	}
	if unassigned == nil || unassigned.Count != 2 || unassigned.Medium != 1 || unassigned.Low != 1 {
		t.Fatalf("unassigned group mismatch: %+v", unassigned)
	}
	groupForTask := func(items []FindingGroup, taskID int64) *FindingGroup {
		for i := range items {
			if items[i].TaskID != nil && *items[i].TaskID == taskID {
				return &items[i]
			}
		}
		return nil
	}
	if err := d.SetPaused(taskA.ID, true); err != nil {
		t.Fatal(err)
	}
	groups, _, _, err = d.ListFindingGroups(FindingFilter{VulnClass: vc}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if group := groupForTask(groups, taskA.ID); group == nil || group.TaskStatus != "paused" {
		t.Fatalf("paused task group status mismatch: %+v", groups)
	}
	if err := d.SetPaused(taskA.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := d.Enqueue(taskA.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	groups, _, _, err = d.ListFindingGroups(FindingFilter{VulnClass: vc}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if group := groupForTask(groups, taskA.ID); group == nil || group.TaskStatus != "queued" {
		t.Fatalf("queued task group status mismatch: %+v", groups)
	}
	if err := d.SetStatus(taskA.ID, "done"); err != nil {
		t.Fatal(err)
	}
	groups, _, _, err = d.ListFindingGroups(FindingFilter{VulnClass: vc}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if group := groupForTask(groups, taskA.ID); group == nil || group.TaskStatus != "done" {
		t.Fatalf("terminal task status must win over queued flag: %+v", groups)
	}

	orphans, total, err := d.ListFindingsPage(FindingFilter{VulnClass: vc, TaskID: FindingUnassignedTask}, 1, 10)
	if err != nil || total != 2 || len(orphans) != 2 {
		t.Fatalf("unassigned page: len=%d total=%d err=%v", len(orphans), total, err)
	}
	maxPage := int(^uint(0) >> 1)
	farFindings, farTotal, err := d.ListFindingsPage(FindingFilter{VulnClass: vc}, maxPage, 200)
	if err != nil || farTotal != len(seed) || len(farFindings) != 0 {
		t.Fatalf("far finding page: len=%d total=%d err=%v", len(farFindings), farTotal, err)
	}
	farGroups, farGroupTotal, farFindingTotal, err := d.ListFindingGroups(FindingFilter{VulnClass: vc}, maxPage, 100)
	if err != nil || farGroupTotal != 2 || farFindingTotal != len(seed) || len(farGroups) != 0 {
		t.Fatalf("far group page: len=%d groups=%d findings=%d err=%v",
			len(farGroups), farGroupTotal, farFindingTotal, err)
	}
	filtered, filteredGroups, filteredFindings, err := d.ListFindingGroups(
		FindingFilter{VulnClass: vc, Status: FindingResolved}, 1, 10,
	)
	if err != nil || filteredGroups != 1 || filteredFindings != 1 || len(filtered) != 1 || filtered[0].High != 1 {
		t.Fatalf("filtered groups: %+v groups=%d findings=%d err=%v", filtered, filteredGroups, filteredFindings, err)
	}
}

func TestAddFindingFollowUpIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	task, err := d.CreateTask("finding follow-up", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{
		Domain: fmt.Sprintf("follow-up-%d.example.test", time.Now().UnixNano()),
		TaskID: task.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, assetID)

	store := d.Exploration(task.ExplorationID)
	findingNodeID, err := store.AddNode(KindFinding, map[string]any{"summary": "source"}, 5, "confirmed", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}
	findingID, err := d.AddFinding(task.ID, findingNodeID, "test", "source", SeverityHigh, "source", "", "worker", []int64{assetID})
	if err != nil {
		t.Fatal(err)
	}

	auditInput := Activity{Worker: "system", Kind: "text", Summary: "人工提交漏洞深入利用意图", Detail: "验证可利用性并形成证据链"}
	intentID, audit, err := store.AddFindingFollowUpIntent(findingID, findingNodeID, "验证可利用性并形成证据链", auditInput)
	if err != nil {
		t.Fatal(err)
	}
	secondID, _, err := store.AddFindingFollowUpIntent(findingID, findingNodeID, "从另一条路径深入", Activity{
		Worker: "system", Kind: "text", Summary: "人工提交漏洞深入利用意图", Detail: "从另一条路径深入",
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondID == intentID {
		t.Fatal("repeated follow-up submissions must create distinct intents")
	}
	expectedAuditSummary := fmt.Sprintf("%s #%d", auditInput.Summary, intentID)
	if audit.ID <= 0 || audit.NodeID == nil || *audit.NodeID != intentID || audit.CreatedAt.IsZero() || audit.Summary != expectedAuditSummary {
		t.Fatalf("persisted audit mismatch: %+v", audit)
	}
	node, err := store.GetNode(intentID)
	if err != nil || node == nil {
		t.Fatalf("GetNode: node=%+v err=%v", node, err)
	}
	if node.Kind != KindIntent || node.Priority != 10 || node.State != "open" || node.Origin != "human" {
		t.Fatalf("follow-up intent metadata: %+v", node)
	}
	var anchorCount, edgeCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_anchors WHERE node_id=$1 AND asset_id=$2`, intentID, assetID).Scan(&anchorCount); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_edges
		WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`,
		task.ExplorationID, findingNodeID, RelDerivedFrom, intentID).Scan(&edgeCount); err != nil {
		t.Fatal(err)
	}
	if anchorCount != 1 || edgeCount != 1 {
		t.Fatalf("follow-up lineage: anchors=%d edges=%d", anchorCount, edgeCount)
	}
	var persistedAuditCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity
		WHERE id=$1 AND exploration_id=$2 AND node_id=$3 AND worker='system' AND summary=$4`,
		audit.ID, task.ExplorationID, intentID, expectedAuditSummary).Scan(&persistedAuditCount); err != nil {
		t.Fatal(err)
	}
	if persistedAuditCount != 1 {
		t.Fatalf("atomic audit count=%d, want 1", persistedAuditCount)
	}

	var nodesBefore, activitiesBefore int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&nodesBefore); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1`, task.ExplorationID).Scan(&activitiesBefore); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddFindingFollowUpIntent(findingID, findingNodeID, "must roll back", Activity{
		Worker: "system", Kind: "text", Summary: "invalid audit", Metadata: json.RawMessage(`{`),
	}); err == nil {
		t.Fatal("invalid activity metadata should fail the transaction")
	}
	var nodesAfter, activitiesAfter int
	if err := d.QueryRow(`SELECT COUNT(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&nodesAfter); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT COUNT(*) FROM activity WHERE exploration_id=$1`, task.ExplorationID).Scan(&activitiesAfter); err != nil {
		t.Fatal(err)
	}
	if nodesAfter != nodesBefore || activitiesAfter != activitiesBefore {
		t.Fatalf("activity failure did not roll back graph: nodes %d->%d activities %d->%d",
			nodesBefore, nodesAfter, activitiesBefore, activitiesAfter)
	}

	if _, err := d.Exec(`DELETE FROM exploration_nodes WHERE id=$1`, findingNodeID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddFindingFollowUpIntent(findingID, findingNodeID, "should fail", auditInput); !errors.Is(err, ErrFindingOriginUnavailable) {
		t.Fatalf("missing source node: want ErrFindingOriginUnavailable, got %v", err)
	}
}
