package proxypool

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// Gateway is a local forward proxy (入口C): agents point HTTP_PROXY at it and it
// tunnels each connection out through a pool proxy chosen per target host. It is a
// plain CONNECT tunnel / HTTP forwarder — it does NOT decrypt TLS or record, so it
// works with cert-pinned targets and protocols the MITM can't parse. Independent
// of the traffic layer; used when traffic capture is off but the pool is on.
type Gateway struct {
	addr     string
	srv      *http.Server
	upstream atomic.Pointer[func(host string) *url.URL]
}

// NewGateway builds a gateway listening on addr (e.g. 127.0.0.1:8789).
func NewGateway(addr string) *Gateway { return &Gateway{addr: addr} }

// Addr returns the listen address.
func (g *Gateway) Addr() string { return g.addr }

// SetUpstream installs (or clears with nil) the per-host upstream resolver. A nil
// return for a host means dial that host directly (gateway adds nothing).
func (g *Gateway) SetUpstream(fn func(host string) *url.URL) {
	if fn == nil {
		g.upstream.Store(nil)
		return
	}
	g.upstream.Store(&fn)
}

func (g *Gateway) pick(host string) *url.URL {
	if fn := g.upstream.Load(); fn != nil {
		return (*fn)(hostOnlyGW(host))
	}
	return nil
}

// Start begins serving in the background. Safe to call once.
func (g *Gateway) Start() error {
	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return err
	}
	g.srv = &http.Server{Handler: http.HandlerFunc(g.handle)}
	go func() {
		if err := g.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[proxygw] stopped: %v", err)
		}
	}()
	log.Printf("[proxypool] gateway on %s", g.addr)
	return nil
}

// Stop gracefully shuts the gateway down.
func (g *Gateway) Stop() {
	if g.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = g.srv.Shutdown(ctx)
	}
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.handleConnect(w, r)
		return
	}
	g.handleHTTP(w, r)
}

// handleConnect tunnels an HTTPS (or any TCP) target: dial out (through the chosen
// pool proxy, or direct), 200 the client, then splice the two connections.
func (g *Gateway) handleConnect(w http.ResponseWriter, r *http.Request) {
	target := r.Host // host:port
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	upConn, err := g.dialOut(ctx, target)
	if err != nil {
		http.Error(w, "gateway dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upConn.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		upConn.Close()
		return
	}
	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		clientConn.Close()
		upConn.Close()
		return
	}
	splice(clientConn, upConn)
}

// handleHTTP forwards a plain (non-CONNECT) HTTP request through the chosen pool
// proxy (or direct) and copies the response back.
func (g *Gateway) handleHTTP(w http.ResponseWriter, r *http.Request) {
	tr := &http.Transport{DisableKeepAlives: true}
	if up := g.pick(r.Host); up != nil {
		switch up.Scheme {
		case "http", "https":
			tr.Proxy = http.ProxyURL(up)
		case "socks5":
			tr.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
				return dialSOCKS(ctx, up, address)
			}
		}
	}
	defer tr.CloseIdleConnections()
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	resp, err := tr.RoundTrip(outReq)
	if err != nil {
		http.Error(w, "gateway forward failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// dialOut opens a raw connection to target, through the pool proxy chosen for its
// host when one is available, else directly.
func (g *Gateway) dialOut(ctx context.Context, target string) (net.Conn, error) {
	if up := g.pick(target); up != nil {
		return DialThrough(ctx, up, target)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", target)
}

// splice copies bytes both ways between two connections until either side closes.
func splice(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	a.Close()
	b.Close()
}

// hostOnlyGW strips a trailing :port so the upstream resolver keys on bare host.
func hostOnlyGW(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}
