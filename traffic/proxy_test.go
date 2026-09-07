package traffic

import "testing"

func TestValidateProxyURL(t *testing.T) {
	ok := []string{
		"http://127.0.0.1:8080",
		"https://proxy.example.com:3128",
		"socks5://10.0.0.1:1080",
		"socks5://user:pass@10.0.0.1:1080",
	}
	for _, raw := range ok {
		if _, err := ValidateProxyURL(raw); err != nil {
			t.Errorf("ValidateProxyURL(%q) unexpected error: %v", raw, err)
		}
	}
	bad := []string{
		"127.0.0.1:8080",         // no scheme
		"ftp://host:21",          // unsupported scheme
		"http://",                // no host
		"socks4://10.0.0.1:1080", // unsupported scheme
	}
	for _, raw := range bad {
		if _, err := ValidateProxyURL(raw); err == nil {
			t.Errorf("ValidateProxyURL(%q) expected error, got nil", raw)
		}
	}
}

func TestSetUpstreamProxyStoreClear(t *testing.T) {
	tr := &Traffic{}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("initial upstream = %v, want nil", got)
	}
	if err := tr.SetUpstreamProxy("socks5://user:pass@10.0.0.1:1080"); err != nil {
		t.Fatalf("SetUpstreamProxy: %v", err)
	}
	u := tr.upstream.Load()
	if u == nil || u.Scheme != "socks5" || u.Host != "10.0.0.1:1080" {
		t.Fatalf("stored upstream = %v, want socks5://10.0.0.1:1080", u)
	}
	if pw, _ := u.User.Password(); u.User.Username() != "user" || pw != "pass" {
		t.Fatalf("stored upstream lost credentials: %v", u)
	}
	// Empty clears back to direct.
	if err := tr.SetUpstreamProxy("  "); err != nil {
		t.Fatalf("SetUpstreamProxy(clear): %v", err)
	}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("after clear upstream = %v, want nil", got)
	}
	// Invalid value is rejected and does not mutate current state.
	if err := tr.SetUpstreamProxy("nope://x"); err == nil {
		t.Fatal("SetUpstreamProxy(invalid) expected error")
	}
	if got := tr.upstream.Load(); got != nil {
		t.Fatalf("invalid set mutated upstream to %v, want nil", got)
	}
}
