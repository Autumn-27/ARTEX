// Package netguard 提供出站请求的 SSRF 防护：对用户可配置的目标地址（LLM
// base_url、自定义 http 工具 URL、MCP 端点等）做解析后 IP 校验。
//
// 默认策略（不破坏本地/内网 LLM 网关）：
//   - 拒绝 链路本地 169.254.0.0/16（含云实例元数据 169.254.169.254，经典
//     SSRF 凭证窃取目标）、组播、未指定地址。
//   - 允许 环回(127.0.0.1，本地 Ollama/llama.cpp 等 LLM 网关)与私网段 ——
//     能配置 LLM 端点的是已认证管理员，其已具备 Agent 任意命令执行能力，
//     私网/环回可达不构成额外提权，且内网渗透实验室普遍需要。
//
// 加固模式 ARTEX_SSRF_STRICT=1：额外拒绝环回与全部私网段（含 100.64.0.0/10），
// 适用于不希望任何内网出站的高安全部署。
//
// 放行清单 ARTEX_SSRF_ALLOW（逗号分隔 CIDR）：显式放行特定网段，优先级最高，
// 但链路本地/组播/未指定地址仍始终拒绝（例如 STRICT 下要放行本地 LLM：
// ARTEX_SSRF_ALLOW=127.0.0.1/32）。
//
// 实现要点：DialContext 内先自行解析域名、校验"全部"解析 IP（DNS 重绑定防护：
// 任一解析结果为禁网段即拒绝），再直连已校验的 IP（Host 头仍保留原域名，
// 虚拟主机/SNI 正常）。不使用 transport 自带的惰性解析，避免"校验一个 IP、
// 连接另一个 IP"的重绑定窗口。
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ErrBlocked 在目标地址命中禁网段时返回，调用方可据此给用户明确提示。
var ErrBlocked = errors.New("目标地址被安全策略禁止（云元数据/组播等）")

var (
	strictMode = strings.EqualFold(strings.TrimSpace(os.Getenv("ARTEX_SSRF_STRICT")), "1")
	allowNets  = loadAllow()
)

func loadAllow() []*net.IPNet {
	v := strings.TrimSpace(os.Getenv("ARTEX_SSRF_ALLOW"))
	if v == "" {
		return nil
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}

// IsBlockedIP 判断解析出的 IP 是否属于禁网段。
// 始终拒绝：链路本地(含 169.254 元数据) / 组播 / 未指定。
// strict 模式额外拒绝：环回 / 私网 / 运营商级 NAT(100.64.0.0/10)。
func IsBlockedIP(ip net.IP) bool {
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// 显式放行清单优先级最高（但上面的"始终拒绝"类仍生效）。
	for _, n := range allowNets {
		if n.Contains(ip) {
			return false
		}
	}
	if strictMode {
		if ip.IsLoopback() || ip.IsPrivate() {
			return true
		}
		if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0b11000000 == 64 { // RFC6598
			return true
		}
	}
	return false
}

// StrictMode 报告是否启用加固模式（供诊断/文档）。
func StrictMode() bool { return strictMode }

// SafeDialer 返回一个先解析校验、再直连已校验 IP 的 DialContext，
// 可直接赋值给 http.Transport.DialContext。
func SafeDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		var targetIPs []net.IP
		if ip := net.ParseIP(host); ip != nil {
			if IsBlockedIP(ip) {
				return nil, fmt.Errorf("%w: %s", ErrBlocked, host)
			}
			targetIPs = []net.IP{ip}
		} else {
			ips, err := d.Resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("解析 %s: %w", host, err)
			}
			for _, ipa := range ips {
				if IsBlockedIP(ipa.IP) {
					return nil, fmt.Errorf("%w: %s → %s", ErrBlocked, host, ipa.IP)
				}
			}
			for _, ipa := range ips {
				targetIPs = append(targetIPs, ipa.IP)
			}
			if len(targetIPs) == 0 {
				return nil, fmt.Errorf("%s 无解析结果", host)
			}
		}
		// 逐个尝试已校验的 IP（Host 头由 http.Transport 保留为原域名）。
		var lastErr error
		for _, ip := range targetIPs {
			conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// ProtectTransport 把 transport 的拨号替换为 SSRF 防护拨号。
// 调用方应传入已 Clone 的 transport，避免影响 http.DefaultTransport。
// 注意：非 nil 的 DialTLS 会绕过 DialContext，这里一并清空。
func ProtectTransport(tr *http.Transport) {
	tr.DialContext = SafeDialer()
	tr.DialTLS = nil
}

// CheckURL 校验出站 URL 的协议与主机。仅允许 http/https；IP 字面量直接校验，
// 域名延迟到连接阶段（SafeDialer 会做同样的逐 IP 校验）。
func CheckURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("无效 URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("仅允许 http/https 协议（收到 %q）", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	if ip := net.ParseIP(host); ip != nil && IsBlockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, host)
	}
	return nil
}
