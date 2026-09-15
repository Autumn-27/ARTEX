package traffic

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

func TestOpenGeneratesAndPersistsProxyAuth(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if tr.auth == nil {
		t.Fatal("proxy auth must be enabled by default")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "_auth"))
	if err != nil {
		t.Fatalf("credential file must be persisted: %v", err)
	}
	if !strings.Contains(string(raw), tr.auth.pass) {
		t.Fatal("credential file must hold the generated password")
	}

	// ProxyAddr is the single source of truth: credentialed loopback URL.
	u, err := url.Parse(tr.ProxyAddr())
	if err != nil {
		t.Fatalf("ProxyAddr not a URL: %v", err)
	}
	if u.User.Username() != tr.auth.user {
		t.Fatalf("ProxyAddr user=%q, want %q", u.User.Username(), tr.auth.user)
	}
	if pw, ok := u.User.Password(); !ok || pw != tr.auth.pass {
		t.Fatal("ProxyAddr must embed the generated password")
	}
	if u.Host != "127.0.0.1:0" || u.Scheme != "http" {
		t.Fatalf("ProxyAddr=%q, want http://…@127.0.0.1:0", tr.ProxyAddr())
	}
	// The redacted form must not leak the password.
	if strings.Contains(tr.ProxyAddrRedacted(), tr.auth.pass) {
		t.Fatalf("ProxyAddrRedacted leaks the password: %q", tr.ProxyAddrRedacted())
	}

	// Reopening the same dir loads the SAME credentials (no regeneration).
	tr2, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr2.Close()
	if tr2.auth.user != tr.auth.user || tr2.auth.pass != tr.auth.pass {
		t.Fatal("reopen must reload the persisted credentials, not regenerate")
	}
	if tr2.ProxyAddr() != tr.ProxyAddr() {
		t.Fatal("ProxyAddr must be stable across restarts")
	}
}

func TestProxyAuthCheck(t *testing.T) {
	a := newProxyAuth("artex", "s3cret")

	// Correct credentials → allowed, and the header is stripped so the
	// password is neither recorded nor forwarded to the target.
	req := httptest.NewRequest(http.MethodGet, "http://target.example/", nil)
	req.Header.Set("Proxy-Authorization", basicHeader("artex", "s3cret"))
	ok, err := a.check(nil, req)
	if !ok || err != nil {
		t.Fatalf("valid credentials rejected: ok=%v err=%v", ok, err)
	}
	if got := req.Header.Get("Proxy-Authorization"); got != "" {
		t.Fatal("Proxy-Authorization must be stripped after a successful check")
	}

	// Missing / wrong credentials → rejected (the proxy entry answers 407).
	for _, h := range []string{"", basicHeader("artex", "wrong"), basicHeader("root", "s3cret"), "Bearer xyz"} {
		req := httptest.NewRequest(http.MethodGet, "http://target.example/", nil)
		if h != "" {
			req.Header.Set("Proxy-Authorization", h)
		}
		if ok, err := a.check(nil, req); ok || err == nil {
			t.Fatalf("header %q must be rejected", h)
		}
	}
}

func TestProxyAuthEnvSwitch(t *testing.T) {
	// off → no credentials, plain loopback URL.
	t.Setenv("ARTEX_PROXY_AUTH", "off")
	tr, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if tr.auth != nil {
		t.Fatal("ARTEX_PROXY_AUTH=off must disable authentication")
	}
	if got := tr.ProxyAddr(); got != "http://127.0.0.1:0" {
		t.Fatalf("ProxyAddr with auth off = %q, want http://127.0.0.1:0", got)
	}

	// Explicit user:pass overrides generation (no _auth file consulted).
	t.Setenv("ARTEX_PROXY_AUTH", "alice:wonder land")
	tr2, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr2.Close()
	if tr2.auth.user != "alice" || tr2.auth.pass != "wonder land" {
		t.Fatalf("explicit credentials not honored: %+v", tr2.auth)
	}
	if u, _ := url.Parse(tr2.ProxyAddr()); u.User.Username() != "alice" {
		t.Fatalf("ProxyAddr must carry the explicit user: %q", tr2.ProxyAddr())
	}

	// Malformed value → fail loudly, never silently unauthenticated.
	t.Setenv("ARTEX_PROXY_AUTH", "no-colon-here")
	if _, err := Open(t.TempDir(), "127.0.0.1:0"); err == nil {
		t.Fatal("malformed ARTEX_PROXY_AUTH must fail Open")
	}
}

func TestSslInsecureEnvSwitch(t *testing.T) {
	// Default stays true (historical behavior, pentest targets break TLS).
	tr, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if !tr.proxy.Opts.SslInsecure {
		t.Fatal("SslInsecure must default to true")
	}

	t.Setenv("ARTEX_PROXY_SSL_INSECURE", "false")
	tr2, err := Open(t.TempDir(), "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr2.Close()
	if tr2.proxy.Opts.SslInsecure {
		t.Fatal("ARTEX_PROXY_SSL_INSECURE=false must turn SslInsecure off")
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// TestOpenTightensPermissions covers F11c: directories 0700, the SQLite index
// and the credential file 0600, pre-existing loose trees migrated on Open, and
// the migration running only once (marker). Mode bits are a no-op on Windows,
// so the assertions skip there.
func TestOpenTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix mode bits")
	}
	dir := t.TempDir()

	// Simulate a legacy install: loose dirs/files written by older versions.
	legacy := filepath.Join(dir, "old.example.com", "GET")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyFile := filepath.Join(legacy, "request.http")
	if err := os.WriteFile(legacyFile, []byte("GET / HTTP/1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	for _, d := range []string{dir, "_index", "_blobs", "_ca"} {
		if m := fileMode(t, filepath.Join(dir, d)); m != 0o700 {
			t.Errorf("dir %s mode=%o, want 700", d, m)
		}
	}
	for _, f := range []string{"_index/index.sqlite", "_auth"} {
		if m := fileMode(t, filepath.Join(dir, f)); m != 0o600 {
			t.Errorf("file %s mode=%o, want 600", f, m)
		}
	}
	// CA private key must be owner-only.
	caEntries, err := os.ReadDir(filepath.Join(dir, "_ca"))
	if err != nil {
		t.Fatal(err)
	}
	if len(caEntries) == 0 {
		t.Fatal("_ca must hold the generated CA files")
	}
	for _, e := range caEntries {
		if m := fileMode(t, filepath.Join(dir, "_ca", e.Name())); m != 0o600 {
			t.Errorf("CA file %s mode=%o, want 600", e.Name(), m)
		}
	}
	// Legacy tree migrated by the one-time sweep.
	if m := fileMode(t, legacy); m != 0o700 {
		t.Errorf("legacy dir mode=%o, want 700", m)
	}
	if m := fileMode(t, legacyFile); m != 0o600 {
		t.Errorf("legacy file mode=%o, want 600", m)
	}
	// Migration marker written → a second Open does not re-sweep.
	if _, err := os.Stat(filepath.Join(dir, permsMigrationMarker)); err != nil {
		t.Fatalf("migration marker missing: %v", err)
	}
	if err := os.Chmod(legacyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	tr2, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr2.Close()
	if m := fileMode(t, legacyFile); m != 0o644 {
		t.Errorf("second Open re-swept despite marker: mode=%o, want untouched 644", m)
	}

	// New blobs are written 0600.
	body := make([]byte, maxInlineBody+1)
	for i := range body {
		body[i] = 'A'
	}
	sb := tr2.spill(body, "text/plain")
	if sb.hash == "" {
		t.Fatal("body above maxInlineBody must spill to a blob")
	}
	bp, err := tr2.blobPath(sb.hash)
	if err != nil {
		t.Fatal(err)
	}
	if m := fileMode(t, bp); m != 0o600 {
		t.Errorf("blob mode=%o, want 600", m)
	}
}
