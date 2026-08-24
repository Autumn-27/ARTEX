package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

func TestSeedAssociatesTargetAssetWithTask(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	defer m.Close()
	task, err := m.CreateTask("seed ownership", "seed ownership", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{DeleteAssets: true})
	})

	host := fmt.Sprintf("seed-%s.example.test", task.ID)
	s := &Server{m: m}
	s.seed(task, "https://"+host)
	taskID := mustTaskID(t, task.ID)
	assets, err := m.Assets().QueryByTask(taskID, "", 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, asset := range assets {
		if asset.Domain == host || asset.URL == "https://"+host {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("seed target was not associated with task %s: %+v", task.ID, assets)
	}
}

func TestDeleteTaskFilesRemovesOnlyOwnedWorkspaceAndTranscripts(t *testing.T) {
	dataDir := t.TempDir()

	removed := []struct {
		path  string
		isDir bool
	}{
		{path: filepath.Join("tasks", "77"), isDir: true},
		{path: filepath.Join("transcripts", "exp12-main.jsonl")},
		{path: filepath.Join("transcripts", "exp12-planner.jsonl")},
		{path: filepath.Join("transcripts", "exp12-worker-i9.jsonl")},
		{path: filepath.Join("transcripts", "exp12-main"), isDir: true},
		{path: filepath.Join("transcripts", "exp12-worker-i9"), isDir: true},
	}
	kept := []struct {
		path  string
		isDir bool
	}{
		{path: filepath.Join("tasks", "78"), isDir: true},
		{path: filepath.Join("transcripts", "exp123-main.jsonl")},
		{path: filepath.Join("transcripts", "exp123-main"), isDir: true},
		{path: filepath.Join("transcripts", "exp1-main.jsonl")},
		{path: filepath.Join("transcripts", "exp12main.jsonl")},
		{path: filepath.Join("transcripts", "exp12-main.jsonl.bak")},
		{path: filepath.Join("transcripts", "exp12-main.txt")},
		{path: filepath.Join("transcripts", "conv-12.jsonl")},
		{path: filepath.Join("transcripts", "unrelated-session"), isDir: true},
	}

	for _, fixture := range append(removed, kept...) {
		createDeleteFixture(t, dataDir, fixture.path, fixture.isDir)
	}

	deleted, err := deleteTaskFiles(dataDir, "77", 12)
	if err != nil {
		t.Fatalf("delete task files: %v", err)
	}
	if !deleted {
		t.Fatal("expected deletion to be reported")
	}
	for _, fixture := range removed {
		assertPathMissing(t, filepath.Join(dataDir, fixture.path))
	}
	for _, fixture := range kept {
		assertPathExists(t, filepath.Join(dataDir, fixture.path))
	}
}

func TestDeleteTaskFilesReportsTranscriptOnlyDeletion(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join("transcripts", "exp44-main.jsonl")
	createDeleteFixture(t, dataDir, path, false)

	deleted, err := deleteTaskFiles(dataDir, "missing-task", 44)
	if err != nil {
		t.Fatalf("delete task files: %v", err)
	}
	if !deleted {
		t.Fatal("expected transcript deletion to be reported")
	}
	assertPathMissing(t, filepath.Join(dataDir, path))
}

func TestDeleteTaskFilesMissingTargetsIsIdempotent(t *testing.T) {
	deleted, err := deleteTaskFiles(t.TempDir(), "77", 12)
	if err != nil {
		t.Fatalf("delete missing task files: %v", err)
	}
	if deleted {
		t.Fatal("missing targets must not be reported as deleted")
	}
}

func TestStageTaskFilesRollbackRestoresWorkspaceAndTranscripts(t *testing.T) {
	dataDir := t.TempDir()
	paths := []string{
		filepath.Join("tasks", "77", "notes.txt"),
		filepath.Join("transcripts", "exp12-main.jsonl"),
		filepath.Join("transcripts", "exp12-worker-i9", "sidechain.jsonl"),
	}
	for _, path := range paths {
		createDeleteFixture(t, dataDir, path, false)
	}

	stage, err := stageTaskFiles(dataDir, "77", 12)
	if err != nil {
		t.Fatal(err)
	}
	if !stage.deleted {
		t.Fatal("expected files to be staged")
	}
	for _, path := range paths {
		assertPathMissing(t, filepath.Join(dataDir, path))
	}
	if err := stage.rollback(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		assertPathExists(t, filepath.Join(dataDir, path))
	}
}

func TestStageTaskFilesRollbackReportsRestoreFailure(t *testing.T) {
	dataDir := t.TempDir()
	workspace := filepath.Join(dataDir, "tasks", "77")
	createDeleteFixture(t, dataDir, filepath.Join("tasks", "77"), true)

	stage, err := stageTaskFiles(dataDir, "77", 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspace, []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("forced PostgreSQL delete failure")
	err = rollbackTaskDelete(cause, nil, stage)
	if err == nil || !errors.Is(err, cause) || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("rollback err=%v, want original and restore errors", err)
	}
	if _, statErr := os.Stat(stage.stageDir); statErr != nil {
		t.Fatalf("staging was removed after failed restore: %v", statErr)
	}
}

func TestManagerDeleteTaskRestoresFilesAndTrafficWhenPostgresDeleteFails(t *testing.T) {
	dataDir := t.TempDir()
	m, err := NewManager(dataDir, "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) - skipping", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	task, err := m.CreateTask("delete rollback", "delete rollback", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID := mustTaskID(t, task.ID)
	triggerName := fmt.Sprintf("test_fail_task_delete_%d", taskID)
	functionName := triggerName + "_fn"
	cleanupDB := func() {
		_, _ = m.pg.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON tasks`, triggerName))
		_, _ = m.pg.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, functionName))
		_, _ = m.DeleteTask(task.ID, DeleteTaskOptions{DeleteAssets: true, DeleteTraffic: true, DeleteFiles: true})
	}
	t.Cleanup(cleanupDB)

	host := fmt.Sprintf("delete-rollback-%d.example.test", taskID)
	if _, err := m.Assets().UpsertHTTPService(pgdb.UpsertHTTPServiceReq{URL: "https://" + host, TaskID: taskID}); err != nil {
		t.Fatal(err)
	}
	createDeleteFixture(t, dataDir, filepath.Join("tasks", task.ID), true)
	transcriptPath := filepath.Join(dataDir, "transcripts", fmt.Sprintf("exp%d-main.jsonl", task.ExpID))
	createDeleteFixture(t, dataDir, filepath.Join("transcripts", filepath.Base(transcriptPath)), false)

	tr, err := traffic.Open(filepath.Join(dataDir, "traffic"), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.traffic = tr
	trafficTree := filepath.Join(dataDir, "traffic", host, "GET", "1-0001")
	if err := os.MkdirAll(trafficTree, 0o755); err != nil {
		t.Fatal(err)
	}
	trafficMarker := filepath.Join(trafficTree, "request.http")
	if err := os.WriteFile(trafficMarker, []byte("original traffic"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, "1-0001", 1, host, "GET", "/", "https://"+host+"/", 200, "text/plain", 0, 0, host+"/GET/1-0001"); err != nil {
		t.Fatal(err)
	}

	if _, err := m.pg.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $body$
BEGIN RAISE EXCEPTION 'forced task delete failure'; END $body$`, functionName)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.pg.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON tasks
FOR EACH ROW WHEN (OLD.id = %d) EXECUTE FUNCTION %s()`, triggerName, taskID, functionName)); err != nil {
		t.Fatal(err)
	}

	_, err = m.DeleteTask(task.ID, DeleteTaskOptions{DeleteTraffic: true, DeleteFiles: true})
	if err == nil || !strings.Contains(err.Error(), "forced task delete failure") {
		t.Fatalf("delete err=%v, want injected PostgreSQL failure", err)
	}
	if got, err := m.pg.GetTask(taskID); err != nil || got == nil {
		t.Fatalf("task row was lost after failed delete: task=%+v err=%v", got, err)
	}
	assertPathExists(t, filepath.Join(dataDir, "tasks", task.ID))
	assertPathExists(t, transcriptPath)
	if got, err := os.ReadFile(trafficMarker); err != nil || string(got) != "original traffic" {
		t.Fatalf("traffic tree was not restored: content=%q err=%v", got, err)
	}
	var trafficRows int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE host=?`, host).Scan(&trafficRows); err != nil || trafficRows != 1 {
		t.Fatalf("traffic index was not restored: rows=%d err=%v", trafficRows, err)
	}
}

func createDeleteFixture(t *testing.T, root, relative string, isDir bool) {
	t.Helper()
	path := filepath.Join(root, relative)
	if isDir {
		if err := os.MkdirAll(filepath.Join(path, "subagents"), 0o755); err != nil {
			t.Fatalf("create fixture directory %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, "subagents", "agent-test.jsonl"), []byte("test"), 0o644); err != nil {
			t.Fatalf("create fixture sidechain %s: %v", path, err)
		}
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture parent %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("create fixture file %s: %v", path, err)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err=%v", path, err)
	}
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to remain: %v", path, err)
	}
}
