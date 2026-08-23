package proxypool

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// dialHTTPConnect tunnels to address through an http/https proxy via a CONNECT
// request. For an https proxy the hop to the proxy itself is wrapped in TLS
// first. Mirrors the well-worn net/http dialConn CONNECT flow.
func dialHTTPConnect(ctx context.Context, proxyURL *url.URL, address string) (net.Conn, error) {
	d := &net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", proxyURL.Host)
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname()})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: http.Header{},
	}
	if proxyURL.User != nil {
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyURL.User.String())))
	}
	connectCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	done := make(chan error, 1)
	var resp *http.Response
	go func() {
		if werr := req.Write(conn); werr != nil {
			done <- werr
			return
		}
		r, rerr := http.ReadResponse(bufio.NewReader(conn), req)
		resp = r
		done <- rerr
	}()
	select {
	case <-connectCtx.Done():
		conn.Close()
		return nil, connectCtx.Err()
	case err = <-done:
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}
