package traffic

import (
	"errors"
	"net/url"
	"testing"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"example.com:443": "example.com",
		"example.com":     "example.com",
		"10.0.0.1:8080":   "10.0.0.1",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q)=%q want %q", in, got, want)
		}
	}
}

func TestProxyCausedErr(t *testing.T) {
	proxy := []string{
		"protocol error: received DATA on a HEAD request",
		"http2: server sent GOAWAY",
		"malformed HTTP response",
	}
	target := []string{ // target-side failures must NOT trigger passthrough
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"read: connection reset by peer",
		"context deadline exceeded",
	}
	for _, s := range proxy {
		if !proxyCausedErr(errors.New(s)) {
			t.Errorf("expected proxy-caused: %q", s)
		}
	}
	for _, s := range target {
		if proxyCausedErr(errors.New(s)) {
			t.Errorf("expected NOT proxy-caused: %q", s)
		}
	}
}

func TestMaybePassthroughFlagsHostOnce(t *testing.T) {
	tr := &Traffic{}
	f := &mproxy.Flow{Request: &mproxy.Request{URL: &url.URL{Host: "target.test:443"}}}

	// Target-caused error → do NOT flag (keep MITM + recording).
	tr.maybePassthrough(f, errors.New("connection refused"))
	if _, ok := tr.pass.Load("target.test"); ok {
		t.Fatal("target-caused error must not flag passthrough")
	}

	// Proxy-caused error → flag the host for transparent passthrough.
	tr.maybePassthrough(f, errors.New("protocol error: received DATA on a HEAD request"))
	if _, ok := tr.pass.Load("target.test"); !ok {
		t.Fatal("proxy-caused error must flag passthrough")
	}

	// The shouldIntercept rule uses hostOnly(req.Host); the CONNECT host carries a
	// port, so it must resolve to the same flagged key → intercept=false (tunnel).
	if _, tunnel := tr.pass.Load(hostOnly("target.test:443")); !tunnel {
		t.Fatal("flagged host must be recognized for the CONNECT form with port")
	}
}
