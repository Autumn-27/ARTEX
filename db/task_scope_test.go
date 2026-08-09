package db

import "testing"

// TestStripHostPort covers the ip:port / [ipv6]:port stripping used by the
// ip/cidr scope path so a target like "10.0.188.136:3000" no longer 404s.
func TestStripHostPort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"10.0.188.136:3000", "10.0.188.136"},          // the reported IP case
		{"10.0.188.136", "10.0.188.136"},               // bare IPv4 unchanged
		{"[2001:db8::1]:8080", "2001:db8::1"},          // bracketed IPv6 + port
		{"2001:db8::1", "2001:db8::1"},                 // bare IPv6 unchanged (has colons)
		{" 1.2.3.4:80 ", "1.2.3.4"},                    // trims surrounding space
		{"example.com:443", "example.com"},             // domain + port → bare host
		{"api.example.com:8080", "api.example.com"},    // subdomain + port
		{"example.com", "example.com"},                 // bare domain unchanged
	}
	for _, c := range cases {
		if got := stripHostPort(c.in); got != c.want {
			t.Errorf("stripHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
