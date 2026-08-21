// Package traffic implements the request-recording subsystem (docs §10): an
// embedded go-mitmproxy proxy whose addon writes every target HTTP exchange into
// a human-browsable file tree, with a sidecar SQLite index for paged queries.
// Full capture, plaintext (no redaction), target HTTP only.
package traffic

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
	_ "modernc.org/sqlite"
)

const indexSchema = `
CREATE TABLE IF NOT EXISTS exchanges (
  id           TEXT PRIMARY KEY,
  ts           INTEGER,
  host         TEXT,
  method       TEXT,
  url_template TEXT,
  url          TEXT,
  status       INTEGER,
  content_type TEXT,
  req_len      INTEGER,
  resp_len     INTEGER,
  path         TEXT
);
CREATE INDEX IF NOT EXISTS idx_ex_host ON exchanges(host);
CREATE INDEX IF NOT EXISTS idx_ex_tmpl ON exchanges(host, url_template);
CREATE INDEX IF NOT EXISTS idx_ex_ts   ON exchanges(ts);
`

const maxInlineBody = 256 * 1024

// Traffic runs the recording proxy and owns the file tree + index.
type Traffic struct {
	dir   string
	addr  string
	db    *sql.DB
	wmu   sync.Mutex // serializes record() vs DeleteHost (incl. blob GC)
	seq   atomic.Int64
	proxy *mproxy.Proxy
	// pass is the set of hosts whose MITM interception failed for a proxy/protocol
	// reason; connections to them are tunneled transparently (fail-open) so the
	// request still reaches the target — unrecorded — instead of being killed.
	pass sync.Map // hostname(string) -> struct{}
}

// Open initializes the traffic tree, blob store and SQLite index under dir.
func Open(dir, addr string) (*Traffic, error) {
	for _, d := range []string{dir, filepath.Join(dir, "_index"), filepath.Join(dir, "_blobs"), filepath.Join(dir, "_ca")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "_index", "index.sqlite"))
	if err != nil {
		return nil, err
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(indexSchema); err != nil {
		db.Close()
		return nil, err
	}
	t := &Traffic{dir: dir, addr: addr, db: db}

	p, err := mproxy.NewProxy(&mproxy.Options{
		Addr:        addr,
		SslInsecure: true,
		CaRootPath:  filepath.Join(dir, "_ca"),
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	// Dial targets DIRECTLY. go-mitmproxy's default upstream uses
	// http.ProxyFromEnvironment, so an HTTP_PROXY/HTTPS_PROXY in the environment
	// (a VPN/system proxy) would make it forward target requests through that
	// external proxy — which can't reach the target → 502. We capture target
	// traffic directly, never via the host's proxy.
	p.SetUpstreamProxy(func(*http.Request) (*url.URL, error) { return nil, nil })
	// Fail-open: MITM every host by default, EXCEPT ones a prior request proved we
	// can't intercept without breaking (see maybePassthrough). Those are tunneled
	// transparently so the request still reaches the target instead of being killed.
	p.SetShouldInterceptRule(func(req *http.Request) bool {
		_, tunnel := t.pass.Load(hostOnly(req.Host))
		return !tunnel
	})
	p.AddAddon(&sink{t: t})
	t.proxy = p
	return t, nil
}

// hostOnly strips an optional :port, so passthrough keys match whether the host
// arrives as "example.com:443" (CONNECT) or "example.com" (request URL).
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// ProxyAddr returns the address workers should set as HTTP(S)_PROXY.
func (t *Traffic) ProxyAddr() string { return "http://127.0.0.1" + t.addr }

// CACertPath returns the PEM CA cert clients must trust to verify HTTPS through
// the MITM proxy (go-mitmproxy writes it here on first start).
func (t *Traffic) CACertPath() string {
	return filepath.Join(t.dir, "_ca", "mitmproxy-ca-cert.pem")
}

// Start runs the proxy (blocking); run in a goroutine.
func (t *Traffic) Start() error { return t.proxy.Start() }

func (t *Traffic) Close() error { return t.db.Close() }
func (t *Traffic) DB() *sql.DB  { return t.db }

// sink is the go-mitmproxy addon that records completed exchanges.
type sink struct {
	mproxy.BaseAddon
	t *Traffic
}

func (s *sink) Response(f *mproxy.Flow) {
	if f.Request == nil || f.Response == nil {
		return
	}
	s.t.record(f)
}

// RequestError fires when a request through an established MITM tunnel fails. If
// the failure looks proxy/protocol-caused (h2 quirks, HEAD-with-body, protocol
// errors) — not a plain target-unreachable error — we flag the host for
// transparent passthrough so future requests to it succeed instead of dying.
func (s *sink) RequestError(f *mproxy.Flow, err error) { s.t.maybePassthrough(f, err) }

// maybePassthrough marks a host to be tunneled transparently on the next
// connection, but only for errors the proxy itself caused — a target that is
// simply down/filtered would fail without us too, and must stay MITM'd+recorded.
func (t *Traffic) maybePassthrough(f *mproxy.Flow, err error) {
	if err == nil || f == nil || f.Request == nil || f.Request.URL == nil || !proxyCausedErr(err) {
		return
	}
	host := f.Request.URL.Hostname()
	if host == "" {
		return
	}
	if _, loaded := t.pass.LoadOrStore(host, struct{}{}); !loaded {
		log.Printf("[traffic] 与 %s 的 MITM 出错，改为透传（该 host 后续直连目标、不再记录，但请求照常）：%v", host, err)
	}
}

// proxyCausedErr reports whether err indicates the interception layer (not the
// target) is at fault — HTTP/2 handling, HEAD-with-body, or a protocol violation.
func proxyCausedErr(err error) bool {
	s := strings.ToLower(err.Error())
	for _, p := range []string{"head request", "http2", "http/2", "protocol error", "protocol_error", "malformed"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

func (t *Traffic) record(f *mproxy.Flow) {
	// The write lock covers blob + tree + index writes, so DeleteHost (and its
	// blob GC) can run under the same lock without racing a concurrent record.
	t.wmu.Lock()
	defer t.wmu.Unlock()
	host := f.Request.URL.Hostname()
	method := f.Request.Method
	tmpl := db.TemplatePath(f.Request.URL.EscapedPath())
	n := t.seq.Add(1)
	now := time.Now()
	id := fmt.Sprintf("%d-%04d", now.Unix(), n%10000)

	// dir mirrors the URL path as a browsable file tree:
	//   <host>/<seg1>/<seg2>/.../<METHOD>/<id>/{request.http,response.http,meta.json}
	// e.g. http://h/api/v1/login → traffic/h/api/v1/login/GET/<id>/
	parts := []string{t.dir, sanitize(host)}
	for _, seg := range strings.Split(strings.Trim(tmpl, "/"), "/") {
		if seg != "" {
			parts = append(parts, sanitize(seg))
		}
	}
	parts = append(parts, method, id)
	exDir := filepath.Join(parts...)
	if err := os.MkdirAll(exDir, 0o755); err != nil {
		return
	}

	reqBody := t.bodyOrBlob(f.Request.Body)
	respBody := t.bodyOrBlob(f.Response.Body)
	ct := f.Response.Header.Get("Content-Type")

	reqTxt := fmt.Sprintf("%s %s %s\n%s\n%s", method, f.Request.URL.RequestURI(), f.Request.Proto, requestHeaderLines(f.Request), reqBody)
	respTxt := fmt.Sprintf("HTTP %d\n%s\n%s", f.Response.StatusCode, headerLines(f.Response.Header), respBody)
	_ = os.WriteFile(filepath.Join(exDir, "request.http"), []byte(reqTxt), 0o644)
	_ = os.WriteFile(filepath.Join(exDir, "response.http"), []byte(respTxt), 0o644)
	meta := fmt.Sprintf(`{"id":%q,"host":%q,"method":%q,"url":%q,"template":%q,"status":%d,"content_type":%q}`,
		id, host, method, f.Request.URL.String(), tmpl, f.Response.StatusCode, ct)
	_ = os.WriteFile(filepath.Join(exDir, "meta.json"), []byte(meta), 0o644)

	rel, _ := filepath.Rel(t.dir, exDir)
	_, _ = t.db.Exec(`INSERT OR REPLACE INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, now.Unix(), host, method, tmpl, f.Request.URL.String(), f.Response.StatusCode, ct,
		len(f.Request.Body), len(f.Response.Body), rel)
}

// bodyOrBlob returns the body inline if small, else stores it content-addressed
// and returns a human-readable pointer.
func (t *Traffic) bodyOrBlob(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) <= maxInlineBody {
		return string(body)
	}
	sum := sha256.Sum256(body)
	h := hex.EncodeToString(sum[:])
	blobDir := filepath.Join(t.dir, "_blobs", "sha256", h[:2], h[2:4])
	_ = os.MkdirAll(blobDir, 0o755)
	blobPath := filepath.Join(blobDir, h+".bin")
	if _, err := os.Stat(blobPath); os.IsNotExist(err) {
		_ = os.WriteFile(blobPath, body, 0o644)
	}
	return fmt.Sprintf("@blob sha256:%s (len=%d)", h, len(body))
}

func headerLines(h map[string][]string) string {
	var b strings.Builder
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// requestHeaderLines restores the HTTP Host header, which net/http stores on
// Request.Host rather than in Header. Keeping it in request.http makes the raw
// capture complete and directly replayable.
func requestHeaderLines(req *mproxy.Request) string {
	headers := req.Header.Clone()
	headers.Del("Host")
	host := req.URL.Host
	if raw := req.Raw(); raw != nil && strings.TrimSpace(raw.Host) != "" {
		host = raw.Host
	}
	var b strings.Builder
	if host = strings.TrimSpace(host); host != "" {
		b.WriteString("Host: ")
		b.WriteString(host)
		b.WriteByte('\n')
	}
	b.WriteString(headerLines(headers))
	return b.String()
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	out := r.Replace(s)
	if out == "" || out == "_" {
		return "root"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

// ExchangeMeta is one row of the index (returned by Search).
type ExchangeMeta struct {
	ID          string `json:"id"`
	TS          int64  `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URLTemplate string `json:"url_template"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
	Path        string `json:"path"`
}

// Search returns paged exchange metadata (never bodies).
func (t *Traffic) Search(host string, page, size int) ([]ExchangeMeta, error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	q := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges`
	args := []any{}
	if host != "" {
		q += ` WHERE host=?`
		args = append(args, host)
	}
	q += ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	args = append(args, size, page*size)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeMeta
	for rows.Next() {
		var m ExchangeMeta
		if err := rows.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Page returns one page of exchange metadata filtered by an optional host
// substring, exact method, and a free-text query q that fuzzy-matches across all
// indexed metadata columns (host/url/method/content-type/status), together with
// the total number of rows matching that filter (for the UI's pagination).
// Newest first. Bodies are never included.
func (t *Traffic) Page(host, method, q string, page, size int) (rows []ExchangeMeta, total int, err error) {
	if size <= 0 || size > 500 {
		size = 100
	}
	if page < 0 {
		page = 0
	}
	where := ""
	var args []any
	add := func(cond string, vs ...any) {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += cond
		args = append(args, vs...)
	}
	if h := strings.TrimSpace(host); h != "" {
		add("host LIKE ?", "%"+h+"%")
	}
	if m := strings.TrimSpace(method); m != "" {
		add("method=?", strings.ToUpper(m))
	}
	if s := strings.TrimSpace(q); s != "" {
		like := "%" + s + "%"
		// Fuzzy match across every indexed column (bodies aren't indexed).
		add("(host LIKE ? OR url LIKE ? OR url_template LIKE ? OR method LIKE ? OR content_type LIKE ? OR CAST(status AS TEXT) LIKE ?)",
			like, like, like, like, like, like)
	}
	if err = t.db.QueryRow(`SELECT COUNT(*) FROM exchanges`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	sel := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges` +
		where + ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	qargs := append(append([]any{}, args...), size, page*size)
	rs, err := t.db.Query(sel, qargs...)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()
	for rs.Next() {
		var m ExchangeMeta
		if err := rs.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, 0, err
		}
		rows = append(rows, m)
	}
	return rows, total, rs.Err()
}

// Get returns the full request/response of one exchange (reads from the tree).
func (t *Traffic) Get(id string) (req, resp string, err error) {
	var rel string
	if err = t.db.QueryRow(`SELECT path FROM exchanges WHERE id=?`, id).Scan(&rel); err != nil {
		return "", "", err
	}
	rb, _ := os.ReadFile(filepath.Join(t.dir, rel, "request.http"))
	pb, _ := os.ReadFile(filepath.Join(t.dir, rel, "response.http"))
	return string(rb), string(pb), nil
}

// HostCount is one distinct recorded host plus its exchange count.
type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// Hosts returns distinct recorded hosts with exchange counts, most recent
// activity first — powers the page's target picker.
func (t *Traffic) Hosts() ([]HostCount, error) {
	rows, err := t.db.Query(`SELECT host, COUNT(*) AS n, MAX(ts) AS last FROM exchanges GROUP BY host ORDER BY last DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostCount
	for rows.Next() {
		var h HostCount
		var last int64
		if err := rows.Scan(&h.Host, &h.Count, &last); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Count returns total recorded exchanges.
func (t *Traffic) Count() (int, error) {
	var n int
	err := t.db.QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&n)
	return n, err
}

// DeleteHost removes every recorded exchange whose host contains the given
// substring — mirroring the UI's host filter, so what you filtered is what gets
// deleted: the index rows plus each matched host's file tree (<dir>/<host>/…).
// Content-addressed blobs in _blobs are shared across hosts, so instead of
// deleting by host they are garbage-collected afterwards: any blob no longer
// referenced by a remaining exchange tree is removed (frees the data volume).
// Returns rows deleted.
func (t *Traffic) DeleteHost(host string) (int64, error) {
	like := "%" + host + "%"
	t.wmu.Lock()
	defer t.wmu.Unlock()
	// Collect distinct matched hosts first (index is the source of truth for
	// tree names) so the trees are removed even if the DELETE row count is 0.
	rows, err := t.db.Query(`SELECT DISTINCT host FROM exchanges WHERE host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	var hosts []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, err
		}
		hosts = append(hosts, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	res, err := t.db.Exec(`DELETE FROM exchanges WHERE host LIKE ?`, like)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	for _, h := range hosts {
		os.RemoveAll(filepath.Join(t.dir, sanitize(h)))
	}
	if n > 0 {
		_ = t.gcBlobs()
	}
	return n, nil
}

// DeleteHostsExact removes recorded exchanges for a set of EXACT hosts (the
// batch path for the page's multi-select delete): index rows + each host's file
// tree, then one blob-GC pass. Exact match — unlike DeleteHost's substring —
// so picking "api.example.com" never sweeps "api.example.com.cn". Returns rows
// deleted. Duplicate hosts are harmless (idempotent deletes, single tree pass).
func (t *Traffic) DeleteHostsExact(hosts []string) (int64, error) {
	stage, err := t.StageDeleteHostsExact(hosts)
	if err != nil {
		return 0, err
	}
	deleted := stage.Deleted()
	if err := stage.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

type stagedTrafficPath struct {
	source string
	staged string
}

// HostDeleteStage keeps the SQLite delete transaction open and moves matching
// host trees to a same-filesystem staging directory. The Traffic write lock is
// held until Commit or Rollback, so no recorder can recreate a staged tree or a
// blob reference while a task deletion is being coordinated with PostgreSQL.
type HostDeleteStage struct {
	traffic  *Traffic
	tx       *sql.Tx
	stageDir string
	moves    []stagedTrafficPath
	deleted  int64
	done     bool
}

// Deleted reports the number of exchange rows selected by this stage.
func (s *HostDeleteStage) Deleted() int64 {
	if s == nil {
		return 0
	}
	return s.deleted
}

// StageDeleteHostsExact prepares a reversible exact-host deletion. Callers must
// finish every successful stage with Commit or Rollback.
func (t *Traffic) StageDeleteHostsExact(hosts []string) (*HostDeleteStage, error) {
	t.wmu.Lock()
	stage := &HostDeleteStage{traffic: t}
	fail := func(cause error) (*HostDeleteStage, error) {
		if rollbackErr := stage.rollbackLocked(); rollbackErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("restore staged traffic: %w", rollbackErr))
		}
		return nil, cause
	}

	unique := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		unique = append(unique, host)
	}

	tx, err := t.db.Begin()
	if err != nil {
		t.wmu.Unlock()
		stage.done = true
		return nil, err
	}
	stage.tx = tx
	for _, h := range unique {
		res, err := tx.Exec(`DELETE FROM exchanges WHERE host=?`, h)
		if err != nil {
			return fail(err)
		}
		n, _ := res.RowsAffected()
		stage.deleted += n
	}

	stagedSources := make(map[string]struct{}, len(unique))
	for _, host := range unique {
		source := filepath.Join(t.dir, sanitize(host))
		if _, duplicatePath := stagedSources[source]; duplicatePath {
			continue
		}
		stagedSources[source] = struct{}{}
		if _, err := os.Lstat(source); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fail(fmt.Errorf("inspect traffic tree for %q: %w", host, err))
		}
		if stage.stageDir == "" {
			parent := filepath.Join(t.dir, "_delete_staging")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				return fail(fmt.Errorf("create traffic staging directory: %w", err))
			}
			stage.stageDir, err = os.MkdirTemp(parent, "hosts-")
			if err != nil {
				return fail(fmt.Errorf("create traffic stage: %w", err))
			}
		}
		staged := filepath.Join(stage.stageDir, fmt.Sprintf("%d-%s", len(stage.moves), filepath.Base(source)))
		if err := os.Rename(source, staged); err != nil {
			return fail(fmt.Errorf("stage traffic tree for %q: %w", host, err))
		}
		stage.moves = append(stage.moves, stagedTrafficPath{source: source, staged: staged})
	}
	return stage, nil
}

// Rollback restores both the SQLite rows and every staged host tree.
func (s *HostDeleteStage) Rollback() error {
	if s == nil || s.done {
		return nil
	}
	return s.rollbackLocked()
}

func (s *HostDeleteStage) rollbackLocked() error {
	if s == nil || s.done {
		return nil
	}
	var errs []error
	if s.tx != nil {
		if err := s.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			errs = append(errs, fmt.Errorf("rollback traffic index: %w", err))
		}
	}
	for i := len(s.moves) - 1; i >= 0; i-- {
		move := s.moves[i]
		if _, err := os.Lstat(move.source); err == nil {
			errs = append(errs, fmt.Errorf("restore destination already exists: %s", move.source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("inspect restore destination %s: %w", move.source, err))
			continue
		}
		if err := os.Rename(move.staged, move.source); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", move.source, err))
		}
	}
	if len(errs) == 0 && s.stageDir != "" {
		if err := os.RemoveAll(s.stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove traffic stage: %w", err))
		}
	}
	s.done = true
	s.traffic.wmu.Unlock()
	return errors.Join(errs...)
}

// Commit makes the staged deletion permanent. SQLite is committed only after
// the caller has committed its PostgreSQL task deletion.
func (s *HostDeleteStage) Commit() error {
	if s == nil || s.done {
		return nil
	}
	if err := s.tx.Commit(); err != nil {
		// A failed SQLite commit normally rolls the transaction back. Restore the
		// trees so the traffic store remains internally consistent and recoverable.
		restoreErr := s.restoreTreesLocked()
		s.done = true
		s.traffic.wmu.Unlock()
		return errors.Join(fmt.Errorf("commit traffic index deletion: %w", err), restoreErr)
	}
	var errs []error
	if s.stageDir != "" {
		if err := os.RemoveAll(s.stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove staged traffic trees: %w", err))
		}
	}
	if err := s.traffic.gcBlobs(); err != nil {
		errs = append(errs, fmt.Errorf("garbage collect traffic blobs: %w", err))
	}
	s.done = true
	s.traffic.wmu.Unlock()
	return errors.Join(errs...)
}

func (s *HostDeleteStage) restoreTreesLocked() error {
	var errs []error
	for i := len(s.moves) - 1; i >= 0; i-- {
		move := s.moves[i]
		if _, err := os.Lstat(move.source); err == nil {
			errs = append(errs, fmt.Errorf("restore destination already exists: %s", move.source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("inspect restore destination %s: %w", move.source, err))
			continue
		}
		if err := os.Rename(move.staged, move.source); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", move.source, err))
		}
	}
	if len(errs) == 0 && s.stageDir != "" {
		if err := os.RemoveAll(s.stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove traffic stage: %w", err))
		}
	}
	return errors.Join(errs...)
}

// gcBlobs removes content-addressed blobs in _blobs that no remaining exchange
// tree references. References appear as "@blob sha256:<hex>" lines inside the
// tree's request.http/response.http files (see bodyOrBlob). Best-effort: walk
// errors only skip cleanup of the affected files. Callers must hold wmu so this
// never races a concurrent record() writing a fresh blob + tree.
func (t *Traffic) gcBlobs() error {
	refs := make(map[string]struct{})
	blobRe := regexp.MustCompile(`@blob sha256:([0-9a-f]{64})`)
	err := filepath.WalkDir(t.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			// _blobs/_index/_ca never contain references; skip their subtrees.
			if p != t.dir && strings.HasPrefix(d.Name(), "_") {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); n != "request.http" && n != "response.http" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, m := range blobRe.FindAllSubmatch(b, -1) {
			refs[string(m[1])] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return filepath.WalkDir(filepath.Join(t.dir, "_blobs", "sha256"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		h := strings.TrimSuffix(d.Name(), ".bin")
		if _, ok := refs[h]; !ok {
			os.Remove(p)
		}
		return nil
	})
}

// query returns one page of exchange metadata filtered by host and/or a url
// substring. Default page size is intentionally small (3) to keep tool results
// lightweight and capped at 10; page is 0-based (page*limit offset).
func (t *Traffic) query(host, contains string, page, limit int) ([]ExchangeMeta, error) {
	if limit <= 0 {
		limit = 3
	}
	if limit > 10 {
		limit = 10
	}
	if page < 0 {
		page = 0
	}
	q := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM exchanges WHERE 1=1`
	args := []any{}
	if host != "" {
		q += ` AND host=?`
		args = append(args, host)
	}
	if contains != "" {
		q += ` AND (url LIKE ? OR url_template LIKE ?)`
		args = append(args, "%"+contains+"%", "%"+contains+"%")
	}
	q += ` ORDER BY ts DESC LIMIT ? OFFSET ?`
	args = append(args, limit, page*limit)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExchangeMeta
	for rows.Next() {
		var m ExchangeMeta
		if err := rows.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Tools exposes traffic lookup to work agents so they query already-captured
// traffic instead of re-curling the same resource (token + dedup win).
func (t *Traffic) Tools() []actool.CoreTool {
	allow := func(context.Context, json.RawMessage, permission.Context) permission.Decision {
		return permission.Allowed()
	}
	ro := func(json.RawMessage) bool { return true }

	search := actool.Build(actool.Spec{
		Name:        "traffic_search",
		Description: "查询记录代理已抓取的目标流量（必须指定 host，可再按 URL 子串过滤）。仅返回极轻量索引(id/method/url/status/resp_len)，不含任何响应内容。默认只返回 3 条、每页最多 10 条；结果多时用 page 翻页（page=0 起）；要看某条的请求/响应原文用 traffic_get(id)。回看已访问资源、找端点先用它，避免重复 curl 同一 URL。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"host":     map[string]any{"type": "string", "description": "按主机过滤（必填，如 '107.172.96.177:8082'）"},
				"contains": map[string]any{"type": "string", "description": "URL 子串过滤（可选，如 'api' / 'login'）"},
				"limit":    map[string]any{"type": "integer", "description": "每页条数，默认 3，最大 10"},
				"page":     map[string]any{"type": "integer", "description": "页码，从 0 开始，默认 0（按 ts 倒序分页）"},
			},
			"required": []any{"host"},
		},
		ReadOnly:    ro,
		Permissions: allow,
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Host, Contains string
				Limit          int
				Page           int
			}
			_ = json.Unmarshal(in, &a)
			if strings.TrimSpace(a.Host) == "" {
				return actool.Errorf("host 为必填参数：请指定要查询的主机（如 '107.172.96.177:8082'），避免全库扫描。"), nil
			}
			rows, err := t.query(a.Host, a.Contains, a.Page, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if len(rows) == 0 {
				return actool.Text("无匹配流量。"), nil
			}
			// 精简为最小索引：仅保留定位所需字段 + 响应码/长度，不带任何响应内容。
			type liteRow struct {
				ID      string `json:"id"`
				Method  string `json:"method"`
				URL     string `json:"url"`
				Status  int    `json:"status"`
				RespLen int    `json:"resp_len"`
			}
			lite := make([]liteRow, 0, len(rows))
			for _, r := range rows {
				lite = append(lite, liteRow{ID: r.ID, Method: r.Method, URL: r.URL, Status: r.Status, RespLen: r.RespLen})
			}
			b, _ := json.Marshal(lite)
			return actool.Text(string(b)), nil
		},
	})

	get := actool.Build(actool.Spec{
		Name:        "traffic_get",
		Description: "按 id 取一条已抓流量的请求/响应原文（过大会截断）。配合 traffic_search 用，避免重复 curl。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "string", "description": "traffic_search 返回的 id"}},
			"required":   []any{"id"},
		},
		ReadOnly:    ro,
		Permissions: allow,
		Run: func(_ context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct{ ID string }
			_ = json.Unmarshal(in, &a)
			req, resp, err := t.Get(a.ID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("=== REQUEST ===\n" + clip(req, 2500) + "\n\n=== RESPONSE ===\n" + clip(resp, 4000)), nil
		},
	})
	return []actool.CoreTool{search, get}
}

// SeedToolMetas returns the traffic tools built on a ZERO receiver, for seeding the
// tools catalog (metadata only — Name/Description/InputSchema). The handlers close
// over the nil receiver but are never invoked on this instance, so it is safe.
func SeedToolMetas() []actool.CoreTool { return (&Traffic{}).Tools() }

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [截断，共 %d 字节；完整在流量文件树] ...", len(s))
}
