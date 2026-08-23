package proxypool

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// TestGatewayConnectTunnel starts the gateway in direct mode (no upstream) and
// verifies a CONNECT tunnel to a local echo server round-trips bytes.
func TestGatewayConnectTunnel(t *testing.T) {
	// Echo target: writes back whatever it reads.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { _, _ = io.Copy(c, c); c.Close() }(c)
		}
	}()

	gw := NewGateway("127.0.0.1:0")
	// Bind an explicit listener so we know the port (NewGateway+Start uses g.addr).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var picked atomic.Int32
	gw.SetUpstream(func(string) *url.URL { picked.Add(1); return nil }) // direct
	gw.srv = &http.Server{Handler: http.HandlerFunc(gw.handle)}
	go func() { _ = gw.srv.Serve(ln) }()
	defer gw.Stop()

	// Client dials the gateway, sends CONNECT to the echo target, then echoes.
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("CONNECT " + target.Addr().String() + " HTTP/1.1\r\nHost: " + target.Addr().String() + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil || status != "HTTP/1.1 200 Connection Established\r\n" {
		t.Fatalf("connect status=%q err=%v", status, err)
	}
	// Consume the blank line terminating the CONNECT response headers.
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", string(buf))
	}
	if picked.Load() == 0 {
		t.Fatal("upstream resolver was not consulted")
	}
}

// TestGatewayPickUsesHostOnly verifies the resolver receives a bare host (no port).
func TestGatewayPickUsesHostOnly(t *testing.T) {
	gw := NewGateway("127.0.0.1:0")
	var gotHost string
	gw.SetUpstream(func(host string) *url.URL { gotHost = host; return nil })
	_ = gw.pick("example.com:443")
	if gotHost != "example.com" {
		t.Fatalf("resolver host = %q, want example.com", gotHost)
	}
}
