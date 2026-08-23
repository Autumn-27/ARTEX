package proxypool

import "testing"

// TestParseHostPortList verifies the raw "host:port" list parser skips blanks,
// comments, and malformed lines while tagging every entry with the source protocol.
func TestParseHostPortList(t *testing.T) {
	body := []byte(`
1.2.3.4:8080
# a comment
5.6.7.8:1080

9.9.9.9:notaport
10.0.0.1:70000
11.22.33.44:3128
`)
	got := parseHostPortList(body, "socks5")
	if len(got) != 3 {
		t.Fatalf("want 3 valid proxies, got %d: %+v", len(got), got)
	}
	for _, p := range got {
		if p.Protocol != "socks5" {
			t.Errorf("protocol = %q, want socks5", p.Protocol)
		}
	}
	if got[0].Host != "1.2.3.4" || got[0].Port != 8080 {
		t.Errorf("first entry = %+v", got[0])
	}
}

// TestSourceNamesUnique guards against a copy-paste duplicate in the catalog.
func TestSourceNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range SourceNames() {
		if seen[n] {
			t.Fatalf("duplicate source name %q", n)
		}
		seen[n] = true
	}
	if len(seen) == 0 {
		t.Fatal("no built-in sources defined")
	}
}
