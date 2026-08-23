package db

import (
	"fmt"
	"testing"
	"time"
)

// TestParseProxyLine covers URL/host:port parsing + protocol validation, no DB.
func TestParseProxyLine(t *testing.T) {
	cases := []struct {
		in      string
		proto   string
		host    string
		port    int
		user    string
		pass    string
		wantErr bool
	}{
		{in: "1.2.3.4:8080", proto: "http", host: "1.2.3.4", port: 8080},
		{in: "socks5://9.9.9.9:1080", proto: "socks5", host: "9.9.9.9", port: 1080},
		{in: "http://user:pass@10.0.0.1:3128", proto: "http", host: "10.0.0.1", port: 3128, user: "user", pass: "pass"},
		{in: "socks4://5.6.7.8:1080", wantErr: true}, // socks4 不支持（仅 http/https/socks5）
		{in: "  8.8.8.8:80  ", proto: "http", host: "8.8.8.8", port: 80},
		{in: "ftp://1.2.3.4:21", wantErr: true}, // unsupported scheme
		{in: "1.2.3.4", wantErr: true},          // no port
		{in: "1.2.3.4:notaport", wantErr: true}, // bad port
		{in: "1.2.3.4:99999", wantErr: true},    // out of range
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		p, err := ParseProxyLine(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseProxyLine(%q) want error, got %+v", c.in, p)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseProxyLine(%q) unexpected error: %v", c.in, err)
			continue
		}
		if p.Protocol != c.proto || p.Host != c.host || p.Port != c.port || p.Username != c.user || p.Password != c.pass {
			t.Errorf("ParseProxyLine(%q) = %+v, want proto=%s host=%s port=%d user=%s pass=%s",
				c.in, p, c.proto, c.host, c.port, c.user, c.pass)
		}
	}
}

// TestProxyURLRoundTrip verifies URL() rebuilds a dial URL including auth.
func TestProxyURLRoundTrip(t *testing.T) {
	p := Proxy{Protocol: "socks5", Host: "1.2.3.4", Port: 1080, Username: "u", Password: "p"}
	if got := p.URL().String(); got != "socks5://u:p@1.2.3.4:1080" {
		t.Fatalf("URL = %q", got)
	}
	p2 := Proxy{Protocol: "http", Host: "5.6.7.8", Port: 8080}
	if got := p2.URL().String(); got != "http://5.6.7.8:8080" {
		t.Fatalf("URL = %q", got)
	}
}

// TestProxyMasked ensures the password is never serialized in the clear.
func TestProxyMasked(t *testing.T) {
	p := Proxy{Password: "secret"}
	if p.Masked().Password != "********" {
		t.Fatalf("password not masked: %q", p.Masked().Password)
	}
	if (Proxy{}).Masked().Password != "" {
		t.Fatalf("empty password should stay empty")
	}
}

// TestProxyStoreLifecycle exercises import de-dup, health update + auto-disable,
// per-host sticky selection, and trusted-only filtering against dev PG.
func TestProxyStoreLifecycle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	ps := d.Proxies()

	// Unique host octet per run so parallel/repeat runs don't collide.
	seed := time.Now().UnixNano() % 250
	host := func(n int64) string { return fmt.Sprintf("203.0.113.%d", (seed+n)%254+1) }
	var ids []int64
	t.Cleanup(func() {
		for _, id := range ids {
			_ = ps.DeleteProxy(id)
		}
	})

	// Import three trusted proxies; the duplicate line must be skipped.
	lines := []string{
		"http://" + host(0) + ":8080",
		"http://" + host(0) + ":8080", // dup
		"socks5://" + host(1) + ":1080",
	}
	added, invalid, err := ps.ImportProxies(lines)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || len(invalid) != 0 {
		t.Fatalf("import added=%d invalid=%v, want added=2", added, invalid)
	}

	all, err := ps.ListProxies(ProxyFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.Host == host(0) || p.Host == host(1) {
			ids = append(ids, p.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("want 2 imported rows, got %d", len(ids))
	}

	// Before any probe, nothing is healthy → no selection.
	pick, err := ps.SelectForHost("target.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if pick != nil {
		t.Fatalf("no healthy proxy yet, got %+v", pick)
	}

	// Mark both healthy, then selection must return one and be sticky per host.
	for _, id := range ids {
		if err := ps.UpdateHealth(id, true, 100, ""); err != nil {
			t.Fatal(err)
		}
	}
	p1, err := ps.SelectForHost("stickyhost", true)
	if err != nil || p1 == nil {
		t.Fatalf("select: %v %+v", err, p1)
	}
	p2, _ := ps.SelectForHost("stickyhost", true)
	if p2 == nil || p1.ID != p2.ID {
		t.Fatalf("per-host stickiness broken: %v vs %v", p1, p2)
	}

	// trustedOnly=false must still return (all imports are trusted anyway).
	if p, _ := ps.SelectForHost("x", false); p == nil {
		t.Fatal("select trustedOnly=false returned nil")
	}

	// Fail one proxy ProxyFailAutoDisable times → auto-disabled → drops out.
	victim := ids[0]
	for i := 0; i < ProxyFailAutoDisable; i++ {
		if err := ps.UpdateHealth(victim, false, 0, "timeout"); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ps.GetProxy(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatalf("proxy should auto-disable after %d fails", ProxyFailAutoDisable)
	}

	// Free-source (untrusted) proxy: a single probe failure deletes it outright.
	freeAdded, err := ps.UpsertFromSource("unittest-free", []Proxy{{Protocol: "http", Host: host(2), Port: 8080}})
	if err != nil || freeAdded != 1 {
		t.Fatalf("seed free proxy: added=%d err=%v", freeAdded, err)
	}
	all, err = ps.ListProxies(ProxyFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var freeID int64
	for _, p := range all {
		if p.Host == host(2) {
			freeID = p.ID
			ids = append(ids, p.ID)
		}
	}
	if freeID == 0 {
		t.Fatal("free proxy not found after upsert")
	}
	if err := ps.UpdateHealth(freeID, false, 0, "timeout"); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.GetProxy(freeID); err != ErrProxyNotFound {
		t.Fatalf("free-source proxy should be deleted on probe failure, got err=%v", err)
	}
}

// TestProxySourceToggle covers source enable persistence + fetch stamping.
func TestProxySourceToggle(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	ps := d.Proxies()

	name := fmt.Sprintf("unittest-source-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM proxy_sources WHERE name=$1`, name) })

	if err := ps.SetSourceEnabled(name, true); err != nil {
		t.Fatal(err)
	}
	enabled, err := ps.EnabledSources()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range enabled {
		if n == name {
			found = true
		}
	}
	if !found {
		t.Fatalf("source %s not in enabled set %v", name, enabled)
	}
	if err := ps.RecordFetch(name, 42, ""); err != nil {
		t.Fatal(err)
	}
	sources, err := ps.ListSources([]string{name})
	if err != nil || len(sources) != 1 {
		t.Fatalf("list sources: %v %+v", err, sources)
	}
	if sources[0].LastCount != 42 || !sources[0].Enabled {
		t.Fatalf("fetch not recorded: %+v", sources[0])
	}
}
