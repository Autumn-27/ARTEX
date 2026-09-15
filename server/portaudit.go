package server

import (
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 台账外监听端口审计(F13 轻量版,只观测不处置):worker 的 Bash 能起任意监听
// (python -m http.server 之类的孤儿服务是通病),平台每 5 分钟扫一次本机监听,
// 凡不在平台台账、且绑在非 loopback 的监听者打 WARN。不自动 kill——先只观测,
// 误杀平台不知道的正当服务比漏报更糟。

// knownManagedPorts 是平台台账内的监听端口:主服务(8787,stage 下载复用该端口)、
// 流量录制代理(8788)、SSH(22),外加动态登记的受管端口(隧道 server 监听口等,
// 见 registerManagedPort)。
func knownManagedPorts() map[int]bool {
	known := map[int]bool{
		8787: true, // 主 HTTP(UI/API + /s/ 受管暂存下载)
		8788: true, // 流量录制代理
		22:   true, // sshd
	}
	dynManagedPorts.Range(func(k, _ any) bool {
		known[k.(int)] = true
		return true
	})
	return known
}

// dynManagedPorts 是运行时动态登记的受管监听端口(隧道 chisel server 监听口等)。
// 受管资源原则:凡平台自己拉起的监听都登记进台账,端口审计不误报。
var dynManagedPorts sync.Map // map[int]struct{}

// registerManagedPort 登记一个受管监听端口(隧道 server 启动时调用)。
func registerManagedPort(port int) { dynManagedPorts.Store(port, struct{}{}) }

// unregisterManagedPort 注销一个受管监听端口(隧道 teardown/回滚时调用)。
func unregisterManagedPort(port int) { dynManagedPorts.Delete(port) }

const portAuditInterval = 5 * time.Minute

// listenSock 是 /proc/net/tcp{,6} 里一条 LISTEN 状态的本地监听。
type listenSock struct {
	IP   net.IP
	Port int
}

// startPortAudit 参照 startLLMRecordsRetention 的模式:启动即扫一次,之后每 5 分钟。
// 仅 Linux(/proc/net/tcp{,6}),其它平台直接跳过。
func (m *Manager) startPortAudit() {
	if runtime.GOOS != "linux" {
		return
	}
	audit := func() {
		for _, w := range scanUnmanagedListeners() {
			log.Printf("[portaudit] WARN 台账外监听者: %s (非 loopback,不在平台端口台账;若为遗忘的临时服务请处置)", w)
		}
	}
	audit()
	go func() {
		t := time.NewTicker(portAuditInterval)
		defer t.Stop()
		for range t.C {
			audit()
		}
	}()
}

// scanUnmanagedListeners 读取 /proc/net/tcp{,6},返回所有「非台账端口 + 非 loopback」
// 的监听描述。文件读不到(容器裁剪等)静默跳过——审计降级不影响主流程。
func scanUnmanagedListeners() []string {
	known := knownManagedPorts()
	var out []string
	seen := map[string]bool{}
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, ls := range parseProcNetTCP(string(data)) {
			if known[ls.Port] || ls.IP.IsLoopback() {
				continue
			}
			desc := fmt.Sprintf("%s:%d", ls.IP, ls.Port)
			if !seen[desc] {
				seen[desc] = true
				out = append(out, desc)
			}
		}
	}
	return out
}

// parseProcNetTCP 解析 /proc/net/tcp 或 /proc/net/tcp6 内容,返回 LISTEN(状态 0A)
// 的本地地址。纯函数,方便用 fixture 单测。
func parseProcNetTCP(content string) []listenSock {
	var out []listenSock
	for _, line := range strings.Split(content, "\n") {
		f := strings.Fields(line)
		// 列:sl local_address rem_address st ...(数据行首列形如 "  0:")
		if len(f) < 4 || !strings.HasSuffix(f[0], ":") || f[3] != "0A" {
			continue
		}
		ip, port, err := parseProcAddr(f[1])
		if err != nil {
			continue
		}
		out = append(out, listenSock{IP: ip, Port: port})
	}
	return out
}

// parseProcAddr 解析 "0100007F:1F91" 这样的 hex 地址。IPv4 是 8 hex(整体小端);
// IPv6 是 32 hex(4 个 32 位字,每个字内部小端)。
func parseProcAddr(s string) (net.IP, int, error) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, fmt.Errorf("bad addr %q", s)
	}
	var port int
	if _, err := fmt.Sscanf(portHex, "%X", &port); err != nil {
		return nil, 0, err
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return nil, 0, err
	}
	switch len(raw) {
	case 4: // IPv4,小端
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]), port, nil
	case 16: // IPv6,按 32 位字小端
		ip := make(net.IP, 16)
		for w := 0; w < 4; w++ {
			ip[w*4+0] = raw[w*4+3]
			ip[w*4+1] = raw[w*4+2]
			ip[w*4+2] = raw[w*4+1]
			ip[w*4+3] = raw[w*4+0]
		}
		return ip, port, nil
	}
	return nil, 0, fmt.Errorf("bad addr len %d", len(raw))
}
