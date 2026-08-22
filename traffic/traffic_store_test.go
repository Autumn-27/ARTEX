package traffic

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

// flowOpt tweaks the synthetic flow built by newFlow.
type flowOpt func(*mproxy.Flow)

func withRespType(ct string) flowOpt {
	return func(f *mproxy.Flow) { f.Response.Header.Set("Content-Type", ct) }
}

// newFlow builds the minimal flow record() needs: a request with a URL, method
// and body, plus a response with a status and body.
func newFlow(host, method, path string, reqBody, respBody []byte, opts ...flowOpt) *mproxy.Flow {
	u, err := url.Parse("http://" + host + path)
	if err != nil {
		panic(err)
	}
	f := &mproxy.Flow{
		Request: &mproxy.Request{
			Method: method,
			URL:    u,
			Proto:  "HTTP/1.1",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   reqBody,
		},
		Response: &mproxy.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       respBody,
		},
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

func openTraffic(t *testing.T) (*Traffic, string) {
	t.Helper()
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr, dir
}

func onlyExchangeID(t *testing.T, tr *Traffic) string {
	t.Helper()
	var id string
	if err := tr.DB().QueryRow(`SELECT id FROM exchanges`).Scan(&id); err != nil {
		t.Fatalf("读取 exchange id: %v", err)
	}
	return id
}

// TestRecordKeepsBodiesInIndex is the core of the storage change: a recorded
// exchange produces no per-request directory at all, and its bodies are served
// back out of SQLite.
func TestRecordKeepsBodiesInIndex(t *testing.T) {
	tr, dir := openTraffic(t)

	tr.record(newFlow("api.example.com", "POST", "/v1/login",
		[]byte(`{"user":"admin","password":"P@ssw0rd"}`),
		[]byte(`{"token":"abc123","note":"内网测试账号"}`)))

	// The URL-mirroring tree is gone: no host directory, no nested path segments.
	if _, err := os.Stat(filepath.Join(dir, "api.example.com")); !os.IsNotExist(err) {
		t.Fatalf("record 仍在磁盘上创建 host 目录（stat err=%v）", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "_") {
			t.Fatalf("data 目录下出现非内部目录 %q，说明仍在写文件树", e.Name())
		}
	}

	id := onlyExchangeID(t, tr)
	req, resp, err := tr.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"POST /v1/login HTTP/1.1", "Host: api.example.com", `"password":"P@ssw0rd"`} {
		if !strings.Contains(req, want) {
			t.Fatalf("请求原文缺少 %q，实际：\n%s", want, req)
		}
	}
	for _, want := range []string{"HTTP 200", `"token":"abc123"`, "内网测试账号"} {
		if !strings.Contains(resp, want) {
			t.Fatalf("响应原文缺少 %q，实际：\n%s", want, resp)
		}
	}
}

// TestFullTextSearchMatchesBodies covers what the trigram index buys over the
// previous URL-only search: arbitrary substrings and CJK, across request and
// response bodies.
func TestFullTextSearchMatchesBodies(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.fts {
		t.Skip("驱动未启用 FTS5")
	}
	const host = "api.example.com"
	tr.record(newFlow(host, "POST", "/v1/login",
		[]byte(`{"user":"admin","password":"P@ssw0rd"}`),
		[]byte(`{"token":"abc123","note":"内网测试账号"}`)))
	tr.record(newFlow(host, "GET", "/v1/health", nil, []byte(`{"status":"ok"}`)))

	hits := func(term string) int {
		t.Helper()
		rows, err := tr.query(host, "", term, 0, 10)
		if err != nil {
			t.Fatalf("按正文搜索 %q 出错：%v", term, err)
		}
		return len(rows)
	}
	if n := hits("password"); n != 1 {
		t.Fatalf("搜 password 命中 %d 条，应为 1", n)
	}
	// Substring inside a token — the default unicode61 tokenizer cannot do this.
	if n := hits("ssw0r"); n != 1 {
		t.Fatalf("搜子串 ssw0r 命中 %d 条，应为 1", n)
	}
	if n := hits("内网测试"); n != 1 {
		t.Fatalf("搜中文命中 %d 条，应为 1", n)
	}
	if n := hits("nonexistent-marker"); n != 0 {
		t.Fatalf("无关关键词命中 %d 条，应为 0", n)
	}

	// Too-short terms are reported, not silently treated as "no match".
	if _, err := tr.query(host, "", "ab", 0, 10); err == nil {
		t.Fatal("两字符正文关键词应返回明确错误")
	}
}

// TestLargeBodySpillsButStaysSearchable is the case that motivated indexing from
// memory: the body lives in the blob store, only a preview is inline, and the
// part past the preview is still findable.
func TestLargeBodySpillsButStaysSearchable(t *testing.T) {
	tr, dir := openTraffic(t)
	if !tr.fts {
		t.Skip("驱动未启用 FTS5")
	}
	const host = "dump.example.com"
	const marker = "DB_PASSWORD=hunter2"
	// Marker sits far past blobPreview, so only the full-text index can find it.
	big := []byte(strings.Repeat("-- MySQL dump\n", maxInlineBody/14+2000) + marker)
	if len(big) <= maxInlineBody+blobPreview {
		t.Fatalf("测试数据不够大：%d 字节", len(big))
	}
	tr.record(newFlow(host, "GET", "/backup.sql", nil, big, withRespType("application/sql")))

	// Stored under a single bucket level, named by hash.
	var hash string
	if err := tr.DB().QueryRow(`SELECT resp_blob FROM exchange_bodies`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Fatalf("resp_blob=%q，应为 64 位 sha256", hash)
	}
	blob := filepath.Join(dir, "_blobs", "sha256", hash[:2], hash+".bin")
	st, err := os.Stat(blob)
	if err != nil {
		t.Fatalf("blob 未落盘到单层桶 %s：%v", blob, err)
	}
	if st.Size() != int64(len(big)) {
		t.Fatalf("blob 大小 %d，应为 %d", st.Size(), len(big))
	}

	// The reference is registered, which is what GC consults.
	var refs int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM blob_refs WHERE hash=?`, hash).Scan(&refs); err != nil {
		t.Fatal(err)
	}
	if refs != 1 {
		t.Fatalf("blob_refs 行数 %d，应为 1", refs)
	}

	// Inline: a readable preview plus the pointer, not the whole body.
	_, resp, err := tr.Get(onlyExchangeID(t, tr))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "-- MySQL dump") {
		t.Fatalf("响应缺少头部预览：\n%s", clip(resp, 300))
	}
	if !strings.Contains(resp, "@blob sha256:"+hash) {
		t.Fatalf("响应缺少 blob 指针：\n%s", clip(resp, 300))
	}
	if strings.Contains(resp, marker) {
		t.Fatal("预览不应包含超出 blobPreview 的内容")
	}
	if len(resp) > blobPreview*2 {
		t.Fatalf("内联内容 %d 字节，远超预览上限", len(resp))
	}

	// Searchable despite living on disk — the index was fed from memory.
	rows, err := tr.query(host, "", marker, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("超大正文中的关键词命中 %d 条，应为 1", len(rows))
	}

	// And retrievable in pages.
	data, total, err := tr.BlobRange(hash, int64(len(big)-len(marker)), 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != int64(len(big)) {
		t.Fatalf("BlobRange total=%d，应为 %d", total, len(big))
	}
	if string(data) != marker {
		t.Fatalf("BlobRange 读到 %q，应为 %q", data, marker)
	}
	if _, _, err := tr.BlobRange("../../etc/passwd", 0, 10); err == nil {
		t.Fatal("非法 hash 应被拒绝")
	}
}

// TestBinaryBodyStaysOutOfIndex keeps the index spend on things worth searching:
// binary payloads contribute nothing but a type tag.
func TestBinaryBodyStaysOutOfIndex(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.fts {
		t.Skip("驱动未启用 FTS5")
	}
	const host = "cdn.example.com"
	const marker = "SECRETINIMAGE"
	png := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00},
		[]byte(strings.Repeat("x", maxInlineBody)+marker)...)
	tr.record(newFlow(host, "GET", "/logo.png", nil, png, withRespType("image/png")))

	rows, err := tr.query(host, "", marker, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("二进制正文不应进入全文索引，却命中 %d 条", len(rows))
	}
	_, resp, err := tr.Get(onlyExchangeID(t, tr))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp, "[binary image/png") || !strings.Contains(resp, "magic=89504e47") {
		t.Fatalf("二进制正文应展示类型与魔数，实际：\n%s", clip(resp, 300))
	}
}

// TestGetFallsBackToLegacyTree keeps pre-migration captures readable: their rows
// carry a path and their bodies are still .http files on disk.
func TestGetFallsBackToLegacyTree(t *testing.T) {
	tr, dir := openTraffic(t)
	const host = "old.example.com"
	const id = "1-0001"
	rel := filepath.Join(host, "GET", id)
	exDir := filepath.Join(dir, rel)
	if err := os.MkdirAll(exDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exDir, "request.http"), []byte("GET / HTTP/1.1\nHost: old.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(exDir, "response.http"), []byte("HTTP 200\n\nlegacy body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "GET", "/", "http://"+host+"/", 200, "text/html", 0, 11, rel); err != nil {
		t.Fatal(err)
	}

	req, resp, err := tr.Get(id)
	if err != nil {
		t.Fatalf("历史记录应仍可读取：%v", err)
	}
	if !strings.Contains(req, "Host: old.example.com") {
		t.Fatalf("历史请求原文错误：%q", req)
	}
	if !strings.Contains(resp, "legacy body") {
		t.Fatalf("历史响应原文错误：%q", resp)
	}
}

// TestGCCollectsBlobsAndEmptyBuckets covers both halves of the collector: the
// reference lookup now comes from blob_refs, and emptied buckets are removed
// instead of accumulating forever.
func TestGCCollectsBlobsAndEmptyBuckets(t *testing.T) {
	tr, dir := openTraffic(t)
	const host = "dump.example.com"
	big := []byte(strings.Repeat("A", maxInlineBody+1024))
	tr.record(newFlow(host, "GET", "/big.bin", nil, big, withRespType("application/sql")))

	var hash string
	if err := tr.DB().QueryRow(`SELECT resp_blob FROM exchange_bodies`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	bucket := filepath.Join(dir, "_blobs", "sha256", hash[:2])
	if _, err := os.Stat(filepath.Join(bucket, hash+".bin")); err != nil {
		t.Fatal(err)
	}

	if n, err := tr.DeleteHostsExact([]string{host}); err != nil || n != 1 {
		t.Fatalf("DeleteHostsExact=(%d,%v)，应为 (1,nil)", n, err)
	}
	if _, err := os.Stat(filepath.Join(bucket, hash+".bin")); !os.IsNotExist(err) {
		t.Fatalf("失去引用的 blob 未被回收：%v", err)
	}
	if _, err := os.Stat(bucket); !os.IsNotExist(err) {
		t.Fatalf("空桶目录未被清理：%v", err)
	}
	// Bodies and full-text rows go with the exchange.
	for _, q := range []string{
		`SELECT COUNT(*) FROM exchange_bodies`,
		`SELECT COUNT(*) FROM blob_refs`,
	} {
		var c int
		if err := tr.DB().QueryRow(q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("%s = %d，应为 0", q, c)
		}
	}
	if tr.fts {
		var c int
		if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM ex_fts WHERE ex_fts MATCH ?`, ftsQuote("AAAA")).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("全文索引残留 %d 条", c)
		}
	}
}

// TestPageSearchesBodies checks the UI-facing search box picks up the full-text
// index too, not just metadata columns.
func TestPageSearchesBodies(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.fts {
		t.Skip("驱动未启用 FTS5")
	}
	tr.record(newFlow("api.example.com", "POST", "/v1/login", nil, []byte(`{"error":"invalid credentials"}`)))
	tr.record(newFlow("api.example.com", "GET", "/v1/health", nil, []byte(`{"status":"ok"}`)))

	rows, total, err := tr.Page("", "", "invalid credentials", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("正文关键词命中 total=%d rows=%d，应为 1/1", total, len(rows))
	}
	// Metadata matching still works alongside it.
	if _, total, err := tr.Page("", "", "health", 0, 100); err != nil || total != 1 {
		t.Fatalf("URL 关键词 total=%d err=%v，应为 1", total, err)
	}
}

// TestTruncateUTF8 guards the preview cut: never split a multi-byte rune.
func TestTruncateUTF8(t *testing.T) {
	s := "内网测试账号"
	for n := 0; n <= len(s); n++ {
		got := truncateUTF8([]byte(s), n)
		if !strings.HasPrefix(s, got) {
			t.Fatalf("n=%d 截断结果 %q 不是原串前缀", n, got)
		}
		if len(got) > n {
			t.Fatalf("n=%d 截断后 %d 字节，超出上限", n, len(got))
		}
	}
	if got := truncateUTF8([]byte("abc"), 10); got != "abc" {
		t.Fatalf("短于上限时应原样返回，得到 %q", got)
	}
}

// TestIsBinaryBody documents the classification: declared binary types, and the
// NUL backstop for anything mislabeled.
func TestIsBinaryBody(t *testing.T) {
	cases := []struct {
		ct   string
		body string
		want bool
	}{
		{"application/json", `{"a":1}`, false},
		{"text/html; charset=utf-8", "<html>", false},
		{"application/sql", "-- dump", false},
		{"", "plain text", false},
		{"image/png", "whatever", true},
		{"application/zip", "PK", true},
		{"APPLICATION/PDF", "%PDF", true},
		{"text/plain", "has\x00nul", true},
	}
	for _, c := range cases {
		if got := isBinaryBody(c.ct, []byte(c.body)); got != c.want {
			t.Errorf("isBinaryBody(%q, %q)=%v，应为 %v", c.ct, c.body, got, c.want)
		}
	}
}

// TestRecordConcurrent exercises the write path under contention: ids stay
// unique and every exchange lands in all three tables.
func TestRecordConcurrent(t *testing.T) {
	tr, _ := openTraffic(t)
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			tr.record(newFlow("api.example.com", "GET", fmt.Sprintf("/item/%d", i),
				nil, fmt.Appendf(nil, `{"id":%d}`, i)))
		})
	}
	wg.Wait()
	var exchanges, bodies int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&exchanges); err != nil {
		t.Fatal(err)
	}
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchange_bodies`).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if exchanges != n || bodies != n {
		t.Fatalf("并发写入后 exchanges=%d bodies=%d，应各为 %d", exchanges, bodies, n)
	}
}
