package db

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQueryArchiveRowsStreamsJSON(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	payload := strings.Repeat("large request/response payload ", 64*1024)
	raw, count, err := queryArchiveRows(d, `SELECT * FROM (VALUES
($1::bigint,$2::text),($3::bigint,$4::text)) archived_row(id,body) ORDER BY id`,
		int64(1), payload, int64(2), "quoted: \"value\"\nline")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("row count=%d, want 2", count)
	}
	var rows []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("decoded row count=%d, want 2", len(rows))
	}
	if rows[0].ID != 1 || rows[0].Body != payload || rows[1].ID != 2 || rows[1].Body != "quoted: \"value\"\nline" {
		t.Fatal("streamed rows were reordered or truncated")
	}
}

func TestTaskArchiveDatabaseRoundTrip(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	if err := d.EnsureLLMRecordsTable(); err != nil {
		t.Fatal(err)
	}
	if err := d.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	task, err := d.CreateTaskWithOptions("archive database roundtrip", "restore exact graph", TaskCreateOptions{Name: "cold task"})
	if err != nil {
		t.Fatal(err)
	}
	var companyID, llmProfileID int64
	if err := d.QueryRow(`INSERT INTO companies(name,nkey) VALUES($1,$2) RETURNING id`,
		fmt.Sprintf("archive-company-%d", task.ID), fmt.Sprintf("archive-company-%d", task.ID)).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`INSERT INTO llm_profiles(name,format,model) VALUES($1,'openai','archive-model') RETURNING id`,
		fmt.Sprintf("archive-chain-%d", task.ID)).Scan(&llmProfileID); err != nil {
		t.Fatal(err)
	}
	exhaustedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	chainCreatedAt := exhaustedAt.Add(-time.Hour)
	if _, err := d.Exec(`UPDATE tasks SET company_id=$2,llm_profile_id=$3,active_llm_profile_id=$3 WHERE id=$1`, task.ID, companyID, llmProfileID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO task_llm_profiles(task_id,profile_id,position,status,last_error,exhausted_at,created_at,updated_at)
VALUES($1,$2,0,'quota_exhausted','balance exhausted',$3,$4,$3)`, task.ID, llmProfileID, exhaustedAt, chainCreatedAt); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM llm_usage WHERE task_id=$1 OR exploration_id=$2`, fmt.Sprint(task.ID), task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM skill_usage WHERE task_id=$1 OR exploration_id=$2`, task.ID, task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM tool_usage WHERE task_id=$1 OR exploration_id=$2`, task.ID, task.ExplorationID)
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
		_, _ = d.Exec(`DELETE FROM llm_profiles WHERE id=$1`, llmProfileID)
		_, _ = d.Exec(`DELETE FROM companies WHERE id=$1`, companyID)
	}()
	if err := d.SetPaused(task.ID, true); err != nil {
		t.Fatal(err)
	}
	assetID, err := d.Assets().UpsertRootDomain(UpsertRootDomainReq{Domain: fmt.Sprintf("archive-%d.example", task.ID), TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	store := d.Exploration(task.ExplorationID)
	nodeID, err := store.AddNode(KindFact, map[string]any{"summary": "archived fact", "asset_ids": []int64{assetID}}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Anchor(nodeID, assetID); err != nil {
		t.Fatal(err)
	}
	profileName := fmt.Sprintf("archive-profile-%d", task.ID)
	skillName := fmt.Sprintf("archive-skill-%d", task.ID)
	toolName := fmt.Sprintf("archive-tool-%d", task.ID)
	vulnclass := fmt.Sprintf("archive-vuln-%d", task.ID)
	if err := d.InsertLLMUsage(&LLMUsage{TaskID: fmt.Sprint(task.ID), ExplorationID: task.ExplorationID, Worker: "worker", Model: "test", ProfileName: profileName, InputTokens: 11, OutputTokens: 7}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertSkillUsage(&SkillUsage{Skill: skillName, AgentKey: "worker", TaskID: task.ID, ExplorationID: task.ExplorationID, Found: true}); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertToolUsage(&ToolUsage{ToolKey: toolName, AgentKey: "worker", TaskID: task.ID, ExplorationID: task.ExplorationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(task.ID, nodeID, vulnclass, "archive finding", SeverityCritical, "summary", "evidence", "worker", []int64{assetID}); err != nil {
		t.Fatal(err)
	}
	archive, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || claimed == nil || claimed.ID != archive.ID || claimed.State != Archiving {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	snapshot, err := d.SnapshotTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DataCounts["assets"] != 1 || snapshot.DataCounts["exploration_nodes"] < 2 {
		t.Fatalf("unexpected snapshot counts: %#v", snapshot.DataCounts)
	}
	if err := d.CompleteTaskArchive(archive.ID, snapshot, "/tmp/test-task.tar.zst", "abc", 100, 50); err != nil {
		t.Fatal(err)
	}
	if live, err := d.GetTask(task.ID); err != nil || live != nil {
		t.Fatalf("archived task must be hidden, got %+v, %v", live, err)
	}
	var nodes, assets, usage int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE id=$1`, assetID).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1`, fmt.Sprint(task.ID)).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if nodes != 0 || assets != 0 || usage != 0 {
		t.Fatalf("hot compaction left nodes=%d assets=%d usage=%d", nodes, assets, usage)
	}
	ready, err := d.GetTaskArchive(archive.ID)
	if err != nil || ready == nil || ready.State != ArchiveReady {
		t.Fatalf("ready archive = %+v, %v", ready, err)
	}
	var stats map[string]any
	if err := json.Unmarshal(ready.AggregateStats, &stats); err != nil {
		t.Fatal(err)
	}
	if _, ok := stats["tokens"]; !ok {
		t.Fatalf("archive token summary missing: %#v", stats)
	}
	assertArchiveGlobalStats(t, d, profileName, skillName, toolName, vulnclass)
	if _, err := d.Exec(`DELETE FROM companies WHERE id=$1`, companyID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err = d.ClaimTaskArchiveJob(t.Context())
	if err != nil || claimed == nil || claimed.State != Restoring {
		t.Fatalf("restore claim = %+v, %v", claimed, err)
	}
	warnings, err := d.RestoreTaskArchive(archive.ID, snapshot, int64((10 * time.Minute).Seconds()))
	if err != nil {
		t.Fatal(err)
	}
	warningFound := false
	for _, warning := range warnings {
		if strings.Contains(warning, fmt.Sprintf("任务企业 %d 已删除", companyID)) {
			warningFound = true
		}
	}
	if !warningFound {
		t.Fatalf("missing deleted company warning: %v", warnings)
	}
	live, err := d.GetTask(task.ID)
	if err != nil || live == nil {
		t.Fatalf("restored task = %+v, %v", live, err)
	}
	if live.Name != "cold task" || !live.Paused {
		t.Fatalf("restored task metadata mismatch: %+v", live)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_nodes WHERE exploration_id=$1`, task.ExplorationID).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM assets WHERE id=$1 AND $2=ANY(task_ids)`, assetID, task.ID).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT count(*) FROM llm_usage WHERE task_id=$1`, fmt.Sprint(task.ID)).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if nodes < 2 || assets != 1 || usage != 1 {
		t.Fatalf("restore incomplete nodes=%d assets=%d usage=%d", nodes, assets, usage)
	}
	var restoredCompanyID *int64
	var restoredStatus, restoredError string
	var restoredExhaustedAt, restoredCreatedAt time.Time
	if err := d.QueryRow(`SELECT company_id FROM tasks WHERE id=$1`, task.ID).Scan(&restoredCompanyID); err != nil {
		t.Fatal(err)
	}
	if restoredCompanyID != nil {
		t.Fatalf("deleted legacy company restored as %v", *restoredCompanyID)
	}
	if err := d.QueryRow(`SELECT status,COALESCE(last_error,''),exhausted_at,created_at FROM task_llm_profiles WHERE task_id=$1 AND profile_id=$2`,
		task.ID, llmProfileID).Scan(&restoredStatus, &restoredError, &restoredExhaustedAt, &restoredCreatedAt); err != nil {
		t.Fatal(err)
	}
	if restoredStatus != "quota_exhausted" || restoredError != "balance exhausted" ||
		!restoredExhaustedAt.Equal(exhaustedAt) || !restoredCreatedAt.Equal(chainCreatedAt) {
		t.Fatalf("LLM chain state/time mismatch: status=%s error=%q exhausted=%s created=%s",
			restoredStatus, restoredError, restoredExhaustedAt, restoredCreatedAt)
	}
	assertArchiveGlobalStats(t, d, profileName, skillName, toolName, vulnclass)
	if err := d.CompleteTaskArchiveRestore(archive.ID); err != nil {
		t.Fatal(err)
	}
	if item, err := d.GetTaskArchive(archive.ID); err != nil || item != nil {
		t.Fatalf("archive metadata must be consumed: %+v, %v", item, err)
	}
}

func TestTaskArchiveBlockersIgnoreQueuedDependents(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	source, err := d.CreateTaskWithOptions("archive blocker source", "source", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := d.CreateTaskWithOptions("archive blocker dependent", "dependent", TaskCreateOptions{
		SourceTaskIDs: []int64{source.ID},
	})
	if err != nil {
		_ = d.DeleteTask(source.ID)
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id IN ($1,$2)`, source.ID, dependent.ID)
		_ = d.DeleteTask(dependent.ID)
		_ = d.DeleteTask(source.ID)
	}()

	blockers, err := d.TaskArchiveBlockers()
	if err != nil {
		t.Fatal(err)
	}
	if blockers[source.ID] != dependent.ID {
		t.Fatalf("source blocker=%d, want dependent %d", blockers[source.ID], dependent.ID)
	}
	if err := d.SetPaused(dependent.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := d.QueueTaskArchive(dependent.ID); err != nil {
		t.Fatal(err)
	}
	blockers, err = d.TaskArchiveBlockers()
	if err != nil {
		t.Fatal(err)
	}
	if blocker := blockers[source.ID]; blocker != 0 {
		t.Fatalf("queued dependent must not block source, got %d", blocker)
	}
}

func assertArchiveGlobalStats(t *testing.T, d *DB, profileName, skillName, toolName, vulnclass string) {
	t.Helper()
	profiles, err := d.UsageByProfile()
	if err != nil {
		t.Fatal(err)
	}
	matchedProfile := false
	for _, profile := range profiles {
		if profile.ProfileName != profileName {
			continue
		}
		matchedProfile = true
		if profile.Calls != 1 || profile.Tasks != 1 || profile.InputTokens != 11 || profile.OutputTokens != 7 {
			t.Fatalf("archive profile aggregate double-counted or missing: %+v", profile)
		}
	}
	if !matchedProfile {
		t.Fatalf("archive profile aggregate %q missing", profileName)
	}
	skills, err := d.SkillStats()
	if err != nil {
		t.Fatal(err)
	}
	matchedSkill := false
	for _, skill := range skills {
		if skill.Skill == skillName {
			matchedSkill = skill.Calls == 1 && skill.Tasks == 1
		}
	}
	if !matchedSkill {
		t.Fatalf("archive skill aggregate %q missing or double-counted: %+v", skillName, skills)
	}
	tools, err := d.ToolUsageCounts()
	if err != nil {
		t.Fatal(err)
	}
	if tools[toolName] != 1 {
		t.Fatalf("archive tool aggregate %q=%d, want 1", toolName, tools[toolName])
	}
	findings, err := d.FindingStats()
	if err != nil {
		t.Fatal(err)
	}
	foundClass := false
	for _, item := range findings.VulnClasses {
		if item == vulnclass {
			foundClass = true
		}
	}
	if !foundClass {
		t.Fatalf("archive finding class %q missing", vulnclass)
	}
}
