package traffic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrafficArchiveRoundTripAndCurrentRowWins(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const (
		id   = "archive-exchange-1"
		host = "archive.example"
	)
	requestHead := "POST /v1/test HTTP/1.1\nHost: archive.example"
	responseHead := "HTTP/1.1 200 OK\nContent-Type: application/json"
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "POST", "/v1/test", "https://archive.example/v1/test", 200,
		"application/json", 7, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,resp_head,resp_body)
VALUES(?,?,?,?,?)`, id, requestHead, []byte("payload"), responseHead, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	archiveDir := filepath.Join(t.TempDir(), "traffic")
	count, err := tr.ExportHosts([]string{host, host}, archiveDir)
	if err != nil || count != 1 {
		t.Fatalf("ExportHosts count=%d err=%v", count, err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "traffic.json")); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExact([]string{host})
	if err != nil {
		t.Fatal(err)
	}
	if stage.Deleted() != 1 {
		t.Fatalf("staged deleted=%d, want 1", stage.Deleted())
	}
	if err := stage.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Get(id); err == nil {
		t.Fatal("deleted exchange remained readable")
	}
	imported, err := tr.ImportArchive(archiveDir)
	if err != nil || imported != 1 {
		t.Fatalf("ImportArchive imported=%d err=%v", imported, err)
	}
	req, resp, err := tr.Get(id)
	if err != nil || !strings.Contains(req, "payload") || !strings.Contains(resp, `{"ok":true}`) {
		t.Fatalf("restored exchange req=%q resp=%q err=%v", req, resp, err)
	}
	if _, err := tr.DB().Exec(`UPDATE exchange_bodies SET resp_body=? WHERE id=?`, []byte(`{"current":true}`), id); err != nil {
		t.Fatal(err)
	}
	imported, err = tr.ImportArchive(archiveDir)
	if err != nil || imported != 0 {
		t.Fatalf("idempotent import imported=%d err=%v", imported, err)
	}
	_, resp, err = tr.Get(id)
	if err != nil || !strings.Contains(resp, `{"current":true}`) || strings.Contains(resp, `{"ok":true}`) {
		t.Fatalf("archive overwrote current exchange resp=%q err=%v", resp, err)
	}
}

func TestRecoverArchiveHostDeleteStageRollsBackBeforePostgresCommit(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const host = "rollback-archive.example"
	insertArchiveRecoveryExchange(t, tr, "rollback-exchange", host)
	legacyDir := filepath.Join(trafficDir, sanitize(host))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "request.http"), []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExactForArchive([]string{host}, 17, 42)
	if err != nil {
		t.Fatal(err)
	}
	simulateTrafficStageProcessExit(t, stage)
	if err := tr.RecoverHostDeleteStages(func(id, taskID int64) (bool, error) {
		if id != 17 || taskID != 42 {
			t.Fatalf("archive id=%d task=%d, want 17/42", id, taskID)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "request.http")); err != nil {
		t.Fatalf("legacy traffic tree not restored: %v", err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exchange count=%d err=%v, want 1", count, err)
	}
}

func TestRecoverArchiveHostDeleteStageCompletesAfterPostgresCommit(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const host = "committed-archive.example"
	insertArchiveRecoveryExchange(t, tr, "committed-exchange", host)
	legacyDir := filepath.Join(trafficDir, sanitize(host))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExactForArchive([]string{host}, 18, 43)
	if err != nil {
		t.Fatal(err)
	}
	simulateTrafficStageProcessExit(t, stage)
	if err := tr.RecoverHostDeleteStages(func(id, taskID int64) (bool, error) { return id == 18 && taskID == 43, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("committed legacy traffic tree remains: %v", err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 0 {
		t.Fatalf("exchange count=%d err=%v, want 0", count, err)
	}
}

func insertArchiveRecoveryExchange(t *testing.T, tr *Traffic, id, host string) {
	t.Helper()
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "GET", "/", "https://"+host+"/", 200, "text/plain", 0, 0, ""); err != nil {
		t.Fatal(err)
	}
}

func simulateTrafficStageProcessExit(t *testing.T, stage *HostDeleteStage) {
	t.Helper()
	if err := stage.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stage.done = true
	stage.traffic.wmu.Unlock()
}
