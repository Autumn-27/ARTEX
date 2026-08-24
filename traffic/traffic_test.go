package traffic

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

func TestRequestHeaderLinesIncludesHost(t *testing.T) {
	req := &mproxy.Request{
		URL:    &url.URL{Host: "target.example:8443"},
		Header: http.Header{"Accept": []string{"application/json"}},
	}
	got := requestHeaderLines(req)
	if !strings.Contains(got, "Host: target.example:8443\n") {
		t.Fatalf("request headers missing Host: %q", got)
	}
	if !strings.Contains(got, "Accept: application/json\n") {
		t.Fatalf("request headers missing regular header: %q", got)
	}
}

// TestDeleteHost verifies the delete contract: rows for hosts containing the
// substring are removed together with their file trees, non-matching hosts are
// untouched, and the count is right.
func TestDeleteHost(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// Seed two hosts' index rows + trees directly (record() needs a live Flow).
	for i, h := range []string{"a.example.com", "b.example.com"} {
		id := fmt.Sprintf("1-%04d", i+1)
		exDir := filepath.Join(dir, h, "GET", id)
		if err := os.MkdirAll(exDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(exDir, "meta.json"), []byte(fmt.Sprintf(`{"id":%q,"host":%q}`, id, h)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, i+1, h, "GET", "/", "http://"+h+"/", 200, "text/html", 0, 0, h+"/GET/"+id); err != nil {
			t.Fatal(err)
		}
	}

	// Substring: "a.example" matches a.example.com only, leaves b.example.com.
	n, err := tr.DeleteHost("a.example")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d, want 1", n)
	}
	// Tree removed for the target, intact for the other host.
	if _, err := os.Stat(filepath.Join(dir, "a.example.com")); !os.IsNotExist(err) {
		t.Fatalf("a.example.com tree still exists (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.example.com")); err != nil {
		t.Fatalf("b.example.com tree removed: %v", err)
	}
	// Index reduced to the other host's single row.
	var c int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("rows=%d, want 1", c)
	}
	// A substring matching nothing is a no-op, not an error.
	n, err = tr.DeleteHost("nope.example")
	if err != nil || n != 0 {
		t.Fatalf("DeleteHost(missing)=%d, err=%v; want 0, nil", n, err)
	}
	// A broader substring sweeps the remaining host too.
	if n, err = tr.DeleteHost("example.com"); err != nil || n != 1 {
		t.Fatalf("DeleteHost(example.com)=%d, err=%v; want 1, nil", n, err)
	}
	c = 0
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&c); err == nil && c != 0 {
		t.Fatalf("rows=%d, want 0 after full sweep", c)
	}
}

// TestHosts verifies the target picker contract: distinct hosts with counts,
// most recent activity first.
func TestHosts(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for i, row := range []struct {
		host string
		ts   int64
	}{{"old.example.com", 1}, {"new.example.com", 3}, {"old.example.com", 2}} {
		id := fmt.Sprintf("1-%04d", i+1)
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, row.ts, row.host, "GET", "/", "http://"+row.host+"/", 200, "text/html", 0, 0, row.host+"/GET/"+id); err != nil {
			t.Fatal(err)
		}
	}
	hosts, err := tr.Hosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("hosts=%d, want 2", len(hosts))
	}
	// newest activity (ts=3) first
	if hosts[0].Host != "new.example.com" || hosts[0].Count != 1 {
		t.Fatalf("hosts[0]=%+v, want new.example.com/1", hosts[0])
	}
	if hosts[1].Host != "old.example.com" || hosts[1].Count != 2 {
		t.Fatalf("hosts[1]=%+v, want old.example.com/2", hosts[1])
	}
}

// TestDeleteHostsExact verifies the batch delete: exact host match only — a
// host whose name contains another as a substring is untouched — duplicates in
// the batch are harmless, and the per-host trees are removed.
func TestDeleteHostsExact(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	seed := func(id, h string) {
		exDir := filepath.Join(dir, h, "GET", id)
		if err := os.MkdirAll(exDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, 1, h, "GET", "/", "http://"+h+"/", 200, "text/html", 0, 0, h+"/GET/"+id); err != nil {
			t.Fatal(err)
		}
	}
	// "api.example.com" is a substring of "api.example.com.cn".
	seed("1-0001", "api.example.com")
	seed("1-0002", "api.example.com.cn")
	seed("1-0003", "shop.example.com")

	// Duplicate entry in the batch must not double-delete or error.
	n, err := tr.DeleteHostsExact([]string{"api.example.com", "api.example.com", "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted=%d, want 2", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "api.example.com")); !os.IsNotExist(err) {
		t.Fatalf("api.example.com tree still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "shop.example.com")); !os.IsNotExist(err) {
		t.Fatalf("shop.example.com tree still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "api.example.com.cn")); err != nil {
		t.Fatalf("api.example.com.cn removed by an exact delete that shouldn't match: %v", err)
	}
	var c int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1 {
		t.Fatalf("rows=%d, want 1 (api.example.com.cn only)", c)
	}
}

func TestDeleteHostsExactReportsTreeRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	const host = "api.example.com"
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		"1-0001", 1, host, "GET", "/", "http://"+host+"/", 200, "text/html", 0, 0, host+"/GET/1-0001"); err != nil {
		t.Fatal(err)
	}

	notDir := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr.dir = notDir

	n, err := tr.DeleteHostsExact([]string{host})
	if err == nil {
		t.Fatal("DeleteHostsExact returned nil after traffic tree removal failed")
	}
	if n != 0 {
		t.Fatalf("deleted=%d, want 0 after atomic rollback", n)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rolled-back index count=%d err=%v, want 1", count, err)
	}
}

func TestDeleteHostsExactRollsBackWholeIndexBatch(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	for i, host := range []string{"a.example.com", "b.example.com"} {
		id := fmt.Sprintf("1-%04d", i+1)
		if err := os.MkdirAll(filepath.Join(dir, host, "GET", id), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "GET", "/", "http://"+host+"/", 200, "text/html", 0, 0, host+"/GET/"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.DB().Exec(`CREATE TRIGGER fail_second_host BEFORE DELETE ON exchanges
WHEN OLD.host='b.example.com' BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`); err != nil {
		t.Fatal(err)
	}

	if n, err := tr.DeleteHostsExact([]string{"a.example.com", "b.example.com"}); err == nil || n != 0 {
		t.Fatalf("DeleteHostsExact failure=(%d,%v), want (0,error)", n, err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rolled-back index count=%d err=%v, want 2", count, err)
	}
	for _, host := range []string{"a.example.com", "b.example.com"} {
		if _, err := os.Stat(filepath.Join(dir, host)); err != nil {
			t.Fatalf("tree %s changed despite index rollback: %v", host, err)
		}
	}
}

func TestStageDeleteHostsExactRollbackRestoresIndexAndTree(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	const host = "rollback.example.com"
	const id = "1-0001"
	tree := filepath.Join(dir, host, "GET", id)
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(tree, "request.http")
	if err := os.WriteFile(marker, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "GET", "/", "http://"+host+"/", 200, "text/html", 0, 0, host+"/GET/"+id); err != nil {
		t.Fatal(err)
	}

	stage, err := tr.StageDeleteHostsExact([]string{host})
	if err != nil {
		t.Fatal(err)
	}
	if stage.Deleted() != 1 {
		t.Fatalf("staged deleted=%d, want 1", stage.Deleted())
	}
	if _, err := os.Stat(filepath.Join(dir, host)); !os.IsNotExist(err) {
		t.Fatalf("host tree was not staged: %v", err)
	}
	if err := stage.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "original" {
		t.Fatalf("restored tree content=%q err=%v", got, err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored index count=%d err=%v, want 1", count, err)
	}
}

func TestStageDeleteHostsExactRollbackReportsRestoreFailure(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	const host = "conflict.example.com"
	hostDir := filepath.Join(dir, host)
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExact([]string{host})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an out-of-band conflicting destination. Rollback must surface the
	// failed rename instead of claiming the external data was restored.
	if err := os.WriteFile(hostDir, []byte("conflict"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := stage.Rollback(); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("rollback err=%v, want restore failure", err)
	}
	if _, err := os.Stat(stage.stageDir); err != nil {
		t.Fatalf("staging was removed after failed restore: %v", err)
	}
}

// TestDeleteHostGCBlobs verifies blob garbage collection: after a host's trees
// are removed, blobs referenced by no remaining exchange are deleted, while
// blobs still referenced (including shared ones) survive.
func TestDeleteHostGCBlobs(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	// Two distinct blobs + one shared blob (referenced by two hosts).
	blobA := filepath.Join(dir, "_blobs", "sha256", "aa", "aa", strings.Repeat("a", 64)+".bin")
	blobB := filepath.Join(dir, "_blobs", "sha256", "bb", "bb", strings.Repeat("b", 64)+".bin")
	blobC := filepath.Join(dir, "_blobs", "sha256", "cc", "cc", strings.Repeat("c", 64)+".bin")
	for _, b := range []string{blobA, blobB, blobC} {
		if err := os.MkdirAll(filepath.Dir(b), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(id, h, ref string) {
		exDir := filepath.Join(dir, h, "GET", id)
		if err := os.MkdirAll(exDir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "no blob"
		if ref != "" {
			body = "@blob sha256:" + ref + " (len=1)"
		}
		if err := os.WriteFile(filepath.Join(exDir, "request.http"), []byte("GET / HTTP/1.1\n\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(exDir, "response.http"), []byte("HTTP 200 OK\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			id, 1, h, "GET", "/", "http://"+h+"/", 200, "text/html", 0, 0, h+"/GET/"+id); err != nil {
			t.Fatal(err)
		}
	}
	ha := strings.Repeat("a", 64)
	hb := strings.Repeat("b", 64)
	hc := strings.Repeat("c", 64)
	seed("1-0001", "a.example.com", ha) // sole reference to blobA
	seed("1-0002", "b.example.com", hb) // sole reference to blobB
	seed("1-0003", "c.example.com", hc) // shares blobC with d
	seed("1-0004", "d.example.com", hc)

	// Delete a: blobA orphaned → removed; blobB/blobC still referenced → kept.
	if n, err := tr.DeleteHost("a.example"); err != nil || n != 1 {
		t.Fatalf("DeleteHost(a.example)=%d, err=%v; want 1, nil", n, err)
	}
	if _, err := os.Stat(blobA); !os.IsNotExist(err) {
		t.Fatalf("orphaned blobA still exists: %v", err)
	}
	if _, err := os.Stat(blobB); err != nil {
		t.Fatalf("referenced blobB removed: %v", err)
	}
	if _, err := os.Stat(blobC); err != nil {
		t.Fatalf("shared blobC removed while d still references it: %v", err)
	}

	// Delete c (shares blobC with d): blobC must survive.
	if n, err := tr.DeleteHost("c.example"); err != nil || n != 1 {
		t.Fatalf("DeleteHost(c.example)=%d, err=%v; want 1, nil", n, err)
	}
	if _, err := os.Stat(blobC); err != nil {
		t.Fatalf("shared blobC removed after deleting one sharer: %v", err)
	}

	// Delete d: last reference gone → blobC collected.
	if n, err := tr.DeleteHost("d.example"); err != nil || n != 1 {
		t.Fatalf("DeleteHost(d.example)=%d, err=%v; want 1, nil", n, err)
	}
	if _, err := os.Stat(blobC); !os.IsNotExist(err) {
		t.Fatalf("blobC still exists after last reference removed: %v", err)
	}
}
