package proxypool

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultProbeURL is the liveness target hit through each proxy. generate_204 is
// tiny, plain HTTP, and returns 204 with no body — cheap and unambiguous.
const DefaultProbeURL = "http://www.gstatic.com/generate_204"

// ProbeResult is one liveness check outcome.
type ProbeResult struct {
	OK      bool
	Latency time.Duration
	Err     string
}

// Probe checks whether a proxy can reach probeURL within timeout, returning
// success + round-trip latency, or the failure reason. A 2xx/3xx response counts
// as alive (some probe targets redirect).
func Probe(ctx context.Context, proxyURL *url.URL, probeURL string, timeout time.Duration) ProbeResult {
	if probeURL == "" {
		probeURL = DefaultProbeURL
	}
	client, err := proxyHTTPClient(proxyURL, timeout)
	if err != nil {
		return ProbeResult{Err: err.Error()}
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return ProbeResult{Err: err.Error()}
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Err: trimErr(err.Error())}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	latency := time.Since(start)
	if resp.StatusCode >= 400 {
		return ProbeResult{Err: fmt.Sprintf("probe status %d", resp.StatusCode), Latency: latency}
	}
	return ProbeResult{OK: true, Latency: latency}
}

// proxyHTTPClient builds an *http.Client whose transport routes through proxyURL:
// http/https proxies use Transport.Proxy (native CONNECT), socks proxies use a
// custom DialContext. One-shot use — caller closes idle connections.
func proxyHTTPClient(proxyURL *url.URL, timeout time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: timeout,
	}
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		tr.Proxy = http.ProxyURL(proxyURL)
	case "socks5", "socks4":
		tr.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialSOCKS(ctx, proxyURL, address)
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
	return &http.Client{Transport: tr, Timeout: timeout}, nil
}

// trimErr shortens noisy dial errors to a storable single line.
func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 200
	if len(s) > max {
		s = s[:max]
	}
	return s
}
