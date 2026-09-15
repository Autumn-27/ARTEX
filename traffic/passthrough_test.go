package traffic

import (
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

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
	if tr.pass.tunneled("target.test") {
		t.Fatal("target-caused error must not flag passthrough")
	}

	// Proxy-caused error → flag the host for transparent passthrough.
	tr.maybePassthrough(f, errors.New("protocol error: received DATA on a HEAD request"))
	if !tr.pass.tunneled("target.test") {
		t.Fatal("proxy-caused error must flag passthrough")
	}

	// The shouldIntercept rule uses hostOnly(req.Host); the CONNECT host carries a
	// port, so it must resolve to the same flagged key → intercept=false (tunnel).
	if !tr.pass.tunneled(hostOnly("target.test:443")) {
		t.Fatal("flagged host must be recognized for the CONNECT form with port")
	}
}

// TestPassthroughTTLExpiry verifies that a passthrough flag lapses after
// passTTL: the host then leaves the tunnel set, so its next connection is
// retried with MITM + recording instead of staying blind forever. A repeated
// failure re-flags it (flag returns already=false again after expiry).
func TestPassthroughTTLExpiry(t *testing.T) {
	now := time.Now()
	tr := &Traffic{}
	tr.pass.now = func() time.Time { return now }

	f := &mproxy.Flow{Request: &mproxy.Request{URL: &url.URL{Host: "flaky.test:443"}}}
	fail := errors.New("protocol error: received DATA on a HEAD request")

	tr.maybePassthrough(f, fail)
	if !tr.pass.tunneled("flaky.test") {
		t.Fatal("host must be tunneled right after a proxy-caused error")
	}

	// Within the TTL the host stays tunneled, and a repeated error is treated
	// as "already flagged" (no state change, no re-log transition).
	now = now.Add(passTTL - time.Minute)
	if !tr.pass.tunneled("flaky.test") {
		t.Fatal("host must stay tunneled within the TTL")
	}
	tr.maybePassthrough(f, fail)
	if !tr.pass.tunneled("flaky.test") {
		t.Fatal("host must stay tunneled after a repeated error within the TTL")
	}

	// After the TTL the entry expires on lookup: the host is back to MITM.
	now = now.Add(2 * time.Minute)
	if tr.pass.tunneled("flaky.test") {
		t.Fatal("host must leave the tunnel set after the TTL lapses")
	}

	// A fresh proxy-caused error flags it again (retry → fail → tunnel).
	tr.maybePassthrough(f, fail)
	if !tr.pass.tunneled("flaky.test") {
		t.Fatal("host must be re-flagged by a new proxy-caused error after expiry")
	}
}

// TestPassthroughCapEvictsOldest verifies the set is bounded: at passMax the
// oldest entry is evicted (and thereby retried with MITM) instead of growing
// the set without bound.
func TestPassthroughCapEvictsOldest(t *testing.T) {
	now := time.Now()
	l := &passList{now: func() time.Time { return now }}

	l.flag("oldest.test")
	for i := 0; i < passMax-1; i++ {
		now = now.Add(time.Second)
		l.flag(fmt.Sprintf("host-%d.test", i))
	}
	if len(l.m) != passMax {
		t.Fatalf("set size=%d, want %d", len(l.m), passMax)
	}

	now = now.Add(time.Second)
	l.flag("overflow.test")
	if len(l.m) != passMax {
		t.Fatalf("set size=%d after overflow, want capped %d", len(l.m), passMax)
	}
	if l.tunneled("oldest.test") {
		t.Fatal("oldest entry must be evicted at the cap")
	}
	if !l.tunneled("overflow.test") || !l.tunneled(fmt.Sprintf("host-%d.test", passMax-2)) {
		t.Fatal("newest entries must survive the eviction")
	}
}
