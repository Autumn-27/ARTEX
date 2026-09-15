// intranet_probe_test.go 覆盖人工探活 API(POST /api/sessions/{id}/probe)
// 的存活/死亡路径(httptest 模拟活马与 404),以及启动探活阈值判定纯函数。
// handler 测试需要会话台账所在的 Postgres(db.DSN),无库时跳过(与
// assembly_test.go 同一套 skip 语义)。
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/session"
)

// probeSentinelRe 匹配 php eval 马存活探针载荷:echo "START".(20+22)."END";
// (session.newSentinel:start 8 位 hex,end 12 位 hex)。
var probeSentinelRe = regexp.MustCompile(`"([0-9a-f]{8})"\.\(20\+22\)\."([0-9a-f]{12})"`)

// liveShellMock 模拟存活的 php eval 一句话马:解析探针哨兵并回显 START42END。
func liveShellMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m := probeSentinelRe.FindStringSubmatch(r.Form.Get("cmd"))
		if m == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(m[1] + "42" + m[2]))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// deadShellMock 模拟马被删除/靶场关停后仍回 HTTP 404 的目标。
func deadShellMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("404 Not Found"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newProbeTestServer 建最小 Server(仅会话子系统)+ 真实 SessionStore。
func newProbeTestServer(t *testing.T) (*Server, *db.SessionStore) {
	t.Helper()
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { pg.Close() })
	store := db.NewSessionStore(pg, []byte("probe-test-master-key"))
	return &Server{sessStore: store, sessReg: session.NewRegistry()}, store
}

// registerProbeSession 落一条 http_php 会话并把指向 targetURL 的驱动登记进注册表。
func registerProbeSession(t *testing.T, s *Server, store *db.SessionStore, targetURL string) int64 {
	t.Helper()
	ctx := context.Background()
	sh, err := session.NewHTTPShell(targetURL, "cmd", "php")
	if err != nil {
		t.Fatalf("NewHTTPShell: %v", err)
	}
	secret, _ := json.Marshal(sh.Secret())
	id, err := store.Create(ctx, &db.SessionRecord{
		Kind: "http_php", URL: targetURL, Secret: secret, Lang: "php", Status: db.SessionAlive,
	})
	if err != nil {
		t.Fatalf("会话落库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), id) })
	sh.SetID(id)
	s.sessReg.Add(sh)
	return id
}

func doProbe(t *testing.T, s *Server, id int64) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/"+strconv.FormatInt(id, 10)+"/probe", nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	s.intranetProbeSession(w, req)
	var body map[string]any
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("响应 JSON 解析失败: %v (%q)", err, w.Body.String())
		}
	}
	return w, body
}

// TestProbeMockSpeaksProtocol 不依赖 DB 的预检:mock 马必须能通过真实
// HTTPShell.Test() 探针(保证有库环境下 handler 存活路径测试有效)。
func TestProbeMockSpeaksProtocol(t *testing.T) {
	sh, err := session.NewHTTPShell(liveShellMock(t).URL, "cmd", "php")
	if err != nil {
		t.Fatalf("NewHTTPShell: %v", err)
	}
	if err := sh.Test(context.Background()); err != nil {
		t.Fatalf("活马 mock 应通过 Test 探针: %v", err)
	}
	dead, err := session.NewHTTPShell(deadShellMock(t).URL, "cmd", "php")
	if err != nil {
		t.Fatalf("NewHTTPShell: %v", err)
	}
	if err := dead.Test(context.Background()); err == nil {
		t.Fatal("404 目标的 Test 必须报错")
	}
}

// TestIntranetProbeSessionAlive 存活路径:活马 → alive=true,status=alive,last_beat 刷新。
func TestIntranetProbeSessionAlive(t *testing.T) {
	s, store := newProbeTestServer(t)
	id := registerProbeSession(t, s, store, liveShellMock(t).URL)

	w, body := doProbe(t, s, id)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %q", w.Code, w.Body.String())
	}
	if alive, _ := body["alive"].(bool); !alive {
		t.Fatalf("活马应 alive=true: %v", body)
	}
	if _, hasErr := body["error"]; hasErr {
		t.Fatalf("存活路径不应带 error: %v", body)
	}
	rec, err := store.Get(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("会话读取失败: %v", err)
	}
	if rec.Status != db.SessionAlive {
		t.Fatalf("status 应 alive: %s", rec.Status)
	}
	if time.Since(rec.LastBeat) > time.Minute {
		t.Fatalf("last_beat 应刚刷新: %v", rec.LastBeat)
	}
}

// TestIntranetProbeSessionDead 死亡路径:目标 404 → alive=false + error,status=dead。
func TestIntranetProbeSessionDead(t *testing.T) {
	s, store := newProbeTestServer(t)
	id := registerProbeSession(t, s, store, deadShellMock(t).URL)

	w, body := doProbe(t, s, id)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %q", w.Code, w.Body.String())
	}
	if alive, _ := body["alive"].(bool); alive {
		t.Fatalf("404 目标应 alive=false: %v", body)
	}
	if e, _ := body["error"].(string); e == "" {
		t.Fatalf("死亡路径必须带 error 原因: %v", body)
	}
	rec, err := store.Get(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("会话读取失败: %v", err)
	}
	if rec.Status != db.SessionDead {
		t.Fatalf("status 应 dead: %s", rec.Status)
	}
}

// TestIntranetProbeSessionNotInRegistry 会话不在注册表 → 404(与 exec 同语义)。
func TestIntranetProbeSessionNotInRegistry(t *testing.T) {
	s, _ := newProbeTestServer(t)
	w, _ := doProbe(t, s, 999999)
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, body = %q", w.Code, w.Body.String())
	}
}

// TestSessionNeedsStartupProbe 启动探活阈值判定(纯函数)。
func TestSessionNeedsStartupProbe(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		rec  *db.SessionRecord
		age  time.Duration
		want bool
	}{
		{"alive 且超阈值", &db.SessionRecord{Status: db.SessionAlive, LastBeat: now.Add(-25 * time.Hour)}, 24 * time.Hour, true},
		{"alive 但新鲜", &db.SessionRecord{Status: db.SessionAlive, LastBeat: now.Add(-time.Hour)}, 24 * time.Hour, false},
		{"dead 超阈值不探", &db.SessionRecord{Status: db.SessionDead, LastBeat: now.Add(-72 * time.Hour)}, 24 * time.Hour, false},
		{"恰好边界不算陈旧", &db.SessionRecord{Status: db.SessionAlive, LastBeat: now.Add(-24 * time.Hour)}, 24 * time.Hour, false},
		{"零值 last_beat 视为陈旧", &db.SessionRecord{Status: db.SessionAlive}, 24 * time.Hour, true},
		{"nil 记录", nil, 24 * time.Hour, false},
	}
	for _, c := range cases {
		if got := sessionNeedsStartupProbe(c.rec, now, c.age); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
