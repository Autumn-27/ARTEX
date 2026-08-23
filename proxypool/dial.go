// Package proxypool provides the outbound proxy pool: dialing through a proxy of
// any supported scheme, active health probing, and fetching free proxy sources.
// It is deliberately independent of the traffic (MITM) layer so the pool works
// whether or not traffic recording is on.
package proxypool

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	xproxy "golang.org/x/net/proxy"
)

// DialThrough opens a TCP connection to address (host:port) through the given
// proxy URL. Supports http/https (CONNECT tunnel) and socks5/socks4. The returned
// conn is a raw byte stream to the target; TLS to the target (if any) is the
// caller's job — the proxy only tunnels, it never terminates TLS to the target.
func DialThrough(ctx context.Context, proxyURL *url.URL, address string) (net.Conn, error) {
	switch strings.ToLower(proxyURL.Scheme) {
	case "socks5", "socks4":
		return dialSOCKS(ctx, proxyURL, address)
	case "http", "https":
		return dialHTTPConnect(ctx, proxyURL, address)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
}

// dialSOCKS tunnels through a SOCKS5 proxy (x/net/proxy also drives socks4 hosts
// via the same SOCKS5 dialer for the common no-auth case).
func dialSOCKS(ctx context.Context, proxyURL *url.URL, address string) (net.Conn, error) {
	var auth *xproxy.Auth
	if proxyURL.User != nil {
		pass, _ := proxyURL.User.Password()
		auth = &xproxy.Auth{User: proxyURL.User.Username(), Password: pass}
	}
	dialer, err := xproxy.SOCKS5("tcp", proxyURL.Host, auth, xproxy.Direct)
	if err != nil {
		return nil, err
	}
	if cd, ok := dialer.(xproxy.ContextDialer); ok {
		return cd.DialContext(ctx, "tcp", address)
	}
	return dialer.Dial("tcp", address)
}
