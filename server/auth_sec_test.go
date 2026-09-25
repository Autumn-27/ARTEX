package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 登录限流器：新 IP 放行，连续超出 burst 后拒绝并给出 Retry-After 秒数。
func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	for i := 0; i < loginRateBurst; i++ {
		if ok, _ := l.allow("1.1.1.1"); !ok {
			t.Fatalf("request %d within burst should be allowed", i)
		}
	}
	if ok, wait := l.allow("1.1.1.1"); ok {
		t.Fatal("request beyond burst should be rejected")
	} else if wait <= 0 {
		t.Fatalf("wait should be > 0, got %d", wait)
	}
	// 不同 IP 互不影响。
	if ok, _ := l.allow("2.2.2.2"); !ok {
		t.Fatal("a different IP must not be rate limited by another IP's traffic")
	}
	// 补充速率：把某 IP 的桶退回到过去，elapsed 补足令牌后应重新放行。
	l.mu.Lock()
	if b, ok := l.m["3.3.3.3"]; ok {
		b.tokens = 0
		b.lastRefill = time.Now().Add(-2 * time.Minute) // 2 分钟前，补 2 个令牌
	} else {
		l.m["3.3.3.3"] = &loginBucket{tokens: 0, lastRefill: time.Now().Add(-2 * time.Minute), lastSeen: time.Now()}
	}
	l.mu.Unlock()
	if ok, _ := l.allow("3.3.3.3"); !ok {
		t.Fatal("tokens should refill over time")
	}
}

// 限流命中时响应必须是 429 + Retry-After 头（前端可读）。
func TestAuthLogin_RateLimited_429(t *testing.T) {
	lim := newLoginLimiter()
	lim.burst = 1
	lim.allow("9.9.9.9") // 用掉唯一令牌
	saved := loginRateLimiter
	loginRateLimiter = lim
	defer func() { loginRateLimiter = saved }()

	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "9.9.9.9:1234"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	rec := httptest.NewRecorder()
	s.authLogin(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Fatal("missing Retry-After header on 429")
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.7:5555"
	if got := clientIP(req); got != "10.0.0.7" {
		t.Fatalf("clientIP RemoteAddr=%q", got)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.7")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Fatalf("clientIP XFF=%q", got)
	}
}

// CORS：未配置的 Origin 一律不再回显（历史实现是 *）。
func TestCORS_RejectsUnlistedOrigin(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := cors(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, req)
	if v := rec.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("unlisted origin must not be allowed, got %q", v)
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodOptions, "/api/x", nil)
	req2.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec2, req2)
	if v := rec2.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("preflight unlisted origin must not be allowed, got %q", v)
	}
}

// 安全响应头：所有响应都应带上（点击劫持 / MIME 嗅探 / 引用策略）。
func TestSecurityHeaders(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := securityHeaders(inner)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}
