package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestGate 构造一个固定参数的门控(不经过环境变量解析)。
func newTestGate() *Gate {
	return newGate("/g-testtoken1234567890ab", "testtoken1234567890ab",
		[]byte("0123456789abcdef0123456789abcdef"), time.Hour)
}

// innerReached 是被门控保护的内层 handler;响应体用于区分"放行"与"伪装"。
func innerReached() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("INNER-REACHED"))
	})
}

// TestGateDecoyWithoutCookie 无 cookie 时各路径(SPA 根、/api/*、health、
// favicon、错误路径)一律返回逐字节一致的伪装页。
func TestGateDecoyWithoutCookie(t *testing.T) {
	h := newTestGate().wrap(innerReached())
	var ref []byte
	for _, p := range []string{"/", "/api/health", "/api/tasks", "/favicon.ico", "/login", "/g-wrongpath"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", p, rec.Code)
		}
		if got := rec.Header().Get("Server"); got != "nginx" {
			t.Fatalf("%s: Server = %q, want nginx", p, got)
		}
		if !bytes.Equal(rec.Body.Bytes(), decoyPage) {
			t.Fatalf("%s: body != decoy page", p)
		}
		if ref == nil {
			ref = rec.Body.Bytes()
		} else if !bytes.Equal(rec.Body.Bytes(), ref) {
			t.Fatalf("%s: body differs across paths", p)
		}
	}
}

// TestGateLoginPage GET 入口路径显示门禁页:无外链资源、title 不含 artex。
func TestGateLoginPage(t *testing.T) {
	g := newTestGate()
	rec := httptest.NewRecorder()
	g.wrap(innerReached()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, g.path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>Access</title>") {
		t.Fatalf("login page title leaked or missing: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "artex") {
		t.Fatalf("login page contains artex fingerprint")
	}
}

// TestGateHandshakeAndCookiePassthrough 正确口令握手 → 签名 cookie → 放行。
func TestGateHandshakeAndCookiePassthrough(t *testing.T) {
	g := newTestGate()
	h := g.wrap(innerReached())

	form := url.Values{"password": {"testtoken1234567890ab"}}
	req := httptest.NewRequest(http.MethodPost, g.path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == gateCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no gate cookie issued")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie attrs: HttpOnly=%v SameSite=%v", cookie.HttpOnly, cookie.SameSite)
	}
	if cookie.Secure {
		t.Fatal("Secure must not be set on plain http")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Body.String() != "INNER-REACHED" {
		t.Fatalf("valid cookie not passed through: %q", rec2.Body.String())
	}
}

// TestGateWrongPasswordIdenticalToWrongPath 错误口令与错误路径的响应逐字节一致。
func TestGateWrongPasswordIdenticalToWrongPath(t *testing.T) {
	g := newTestGate()
	h := g.wrap(innerReached())

	form := url.Values{"password": {"wrong-password"}}
	req := httptest.NewRequest(http.MethodPost, g.path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recBad := httptest.NewRecorder()
	h.ServeHTTP(recBad, req)

	recWrongPath := httptest.NewRecorder()
	h.ServeHTTP(recWrongPath, httptest.NewRequest(http.MethodGet, "/no-such-path", nil))

	if recBad.Code != recWrongPath.Code {
		t.Fatalf("status mismatch: %d vs %d", recBad.Code, recWrongPath.Code)
	}
	if !bytes.Equal(recBad.Body.Bytes(), recWrongPath.Body.Bytes()) {
		t.Fatal("wrong-password body != wrong-path body")
	}
	if !bytes.Equal(recBad.Body.Bytes(), decoyPage) {
		t.Fatal("wrong-password body != decoy page")
	}
	if recBad.Header().Get("Server") != recWrongPath.Header().Get("Server") {
		t.Fatal("Server header mismatch")
	}
	if len(recBad.Result().Cookies()) != 0 {
		t.Fatal("wrong password must not issue a cookie")
	}
}

// TestGateTamperedCookie 篡改签名的 cookie 被拒绝(回伪装页)。
func TestGateTamperedCookie(t *testing.T) {
	g := newTestGate()
	h := g.wrap(innerReached())

	good := g.signCookie()
	// 翻转签名段的最后一个十六进制字符。
	last := good[len(good)-1]
	flip := byte('0')
	if last == '0' {
		flip = '1'
	}
	tampered := good[:len(good)-1] + string(flip)

	for name, v := range map[string]string{
		"tampered-sig": tampered,
		"garbage":      "not-a-cookie",
		"forged-exp":   "9999999999.deadbeef",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: gateCookieName, Value: v})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !bytes.Equal(rec.Body.Bytes(), decoyPage) {
			t.Fatalf("%s: tampered cookie passed through", name)
		}
	}
}

// TestGateExpiredCookie 过期 cookie 被拒绝(用 ttl<0 直接签出已过期值)。
func TestGateExpiredCookie(t *testing.T) {
	g := newTestGate()
	g.ttl = -time.Hour
	expired := g.signCookie()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: gateCookieName, Value: expired})
	rec := httptest.NewRecorder()
	g.wrap(innerReached()).ServeHTTP(rec, req)
	if !bytes.Equal(rec.Body.Bytes(), decoyPage) {
		t.Fatal("expired cookie passed through")
	}
}

// TestGateRateLimit 口令尝试超过每 IP 每分钟上限后,连正确口令也拿到伪装页。
func TestGateRateLimit(t *testing.T) {
	g := newTestGate()
	h := g.wrap(innerReached())
	post := func(password string) *httptest.ResponseRecorder {
		form := url.Values{"password": {password}}
		req := httptest.NewRequest(http.MethodPost, g.path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < gateRateLimit; i++ {
		post("wrong")
	}
	if rec := post("testtoken1234567890ab"); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), decoyPage) {
		t.Fatalf("rate limiter did not kick in: code=%d", rec.Code)
	}
}

// TestGateOffPassthrough 门控关闭(nil Gate)时一切直通。
func TestGateOffPassthrough(t *testing.T) {
	var g *Gate
	h := g.wrap(innerReached())
	for _, p := range []string{"/", "/api/health", "/anything"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Body.String() != "INNER-REACHED" {
			t.Fatalf("%s: gate=off did not pass through", p)
		}
	}
}

// TestNewGateResolution 环境变量与监听地址的开关解析 + 入口文件落盘。
func TestNewGateResolution(t *testing.T) {
	dataDir := t.TempDir()
	keyDir := t.TempDir()

	// 显式关闭优先于非 loopback 默认开启。
	t.Setenv("ARTEX_GATE", "off")
	g, err := NewGate("0.0.0.0:8787", dataDir, keyDir)
	if err != nil || g != nil {
		t.Fatalf("ARTEX_GATE=off: gate=%v err=%v", g, err)
	}

	// loopback 默认关闭。
	t.Setenv("ARTEX_GATE", "")
	g, err = NewGate("127.0.0.1:8787", dataDir, keyDir)
	if err != nil || g != nil {
		t.Fatalf("loopback default: gate=%v err=%v", g, err)
	}

	// 非 loopback 默认开启,随机 /g- 入口,落盘 gate.path 与 gate.key。
	g, err = NewGate("0.0.0.0:8787", dataDir, keyDir)
	if err != nil || g == nil {
		t.Fatalf("non-loopback default: gate=%v err=%v", g, err)
	}
	if !strings.HasPrefix(g.path, "/g-") || len(g.path) < 3+16 {
		t.Fatalf("unexpected gate path %q", g.path)
	}
	if g.token != strings.TrimPrefix(g.path, "/g-") {
		t.Fatalf("default token should equal the path token: %q vs %q", g.token, g.path)
	}
	pf, err := os.ReadFile(filepath.Join(dataDir, gatePathFilename))
	if err != nil || strings.TrimSpace(string(pf)) != g.path {
		t.Fatalf("gate.path file: %q err=%v", pf, err)
	}
	kf, err := os.ReadFile(filepath.Join(keyDir, gateKeyFilename))
	if err != nil || len(strings.TrimSpace(string(kf))) < 32 {
		t.Fatalf("gate.key file: len=%d err=%v", len(kf), err)
	}

	// 显式入口路径与口令。
	t.Setenv("ARTEX_GATE_PATH", "/custom-entrance")
	t.Setenv("ARTEX_GATE_TOKEN", "s3cret")
	g, err = NewGate("127.0.0.1:8787", dataDir, keyDir) // ARTEX_GATE 未设 → loopback 默认关?显式 PATH 不算开关
	if err != nil {
		t.Fatal(err)
	}
	if g != nil {
		t.Fatal("explicit path alone must not force-enable the gate on loopback")
	}
	t.Setenv("ARTEX_GATE", "on")
	g, err = NewGate("127.0.0.1:8787", dataDir, keyDir)
	if err != nil || g == nil {
		t.Fatalf("ARTEX_GATE=on: gate=%v err=%v", g, err)
	}
	if g.path != "/custom-entrance" || g.token != "s3cret" {
		t.Fatalf("explicit path/token not honored: %q %q", g.path, g.token)
	}
	// 密钥复用:两次 NewGate 用同一 keyDir,cookie 可互相验证。
	g2, err := NewGate("127.0.0.1:8787", dataDir, keyDir)
	if err != nil || g2 == nil {
		t.Fatalf("second NewGate: %v", err)
	}
	if !g2.validCookie(g.signCookie()) {
		t.Fatal("gate key not reused across NewGate calls")
	}
}
