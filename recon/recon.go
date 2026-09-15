// Package recon 是内网被动侦察输出的解析层(内网渗透期 2,INTRANET-PIVOT-DESIGN.md
// §5 期 2;语义移植自 PivotHub pivothub/service/recon.py,Go 重写非照抄)。
// 全部解析器与推断规则都是纯函数,不碰 DB/网络,直接以真实命令输出样例单测。
//
// 覆盖命令(Linux 为主,Windows 各备一个变体):
//   - ip addr / ifconfig / ipconfig /all      → 网卡 + IP + 掩码
//   - ip route / route -n / route print       → 路由(标出非默认静态路由)
//   - arp -a / ip neigh                       → 邻居
//   - cat /etc/hosts(含 Windows hosts)      → 静态主机映射
//   - netstat -an / ss -an / netstat -ano     → 监听端口 + 已建立连接对端
//
// 高价值推断照 chains pivot 骨架「被动六表」判据(agent/chainskel/pivot.md):
// 非默认静态路由必通有价值段;established 对端是已证明可通主机;多网卡=潜在跳板。
package recon

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NIC 是一张网卡的一个 IPv4 地址。
type NIC struct {
	Iface  string `json:"iface"`
	IP     string `json:"ip"`
	Prefix int    `json:"prefix"` // CIDR 前缀长度(如 24)
}

// Route 是一条路由。IsStatic 标记非默认静态路由(高价值:静态路由指向的网段
// 管理员显式配置过,必通且通常有业务价值)。
type Route struct {
	Dest     string `json:"dest"` // CIDR("default" 表示默认路由)
	Via      string `json:"via,omitempty"`
	Iface    string `json:"iface,omitempty"`
	IsStatic bool   `json:"is_static"`
}

// Neighbor 是 ARP/邻居表的一项(同网段存活主机的被动证据)。
type Neighbor struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac,omitempty"`
	Iface string `json:"iface,omitempty"`
}

// HostEntry 是 /etc/hosts 的一行。
type HostEntry struct {
	IP    string   `json:"ip"`
	Names []string `json:"names"`
}

// Listener 是一个监听端口。
type Listener struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp | udp
}

// Peer 是一个已建立连接的对端。
type Peer struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// ReconReport 是一次被动侦察的结构化结果。
type ReconReport struct {
	NICs         []NIC       `json:"nics"`
	Routes       []Route     `json:"routes"`
	Neighbors    []Neighbor  `json:"neighbors"`
	HostsEntries []HostEntry `json:"hosts_entries"`
	Listening    []Listener  `json:"listening"`
	Peers        []Peer      `json:"peers"`
}

// Inference 是一条高价值推断(照 pivot 骨架判据)。
type Inference struct {
	Kind    string `json:"kind"` // multi_nic | static_route | proven_peer | arp_neighbors
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

var ipRe = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)

func isIPv4(s string) bool { return ipRe.MatchString(s) && net.ParseIP(s) != nil }

// SegmentOf 计算 ip/prefix 的网段 CIDR(如 "10.0.0.0/24");非法输入返回 ""。
func SegmentOf(ip string, prefix int) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return ""
	}
	if prefix < 0 || prefix > 32 {
		return ""
	}
	return netip.PrefixFrom(addr, prefix).Masked().String()
}

func isLoopbackOrLinkLocal(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return true
	}
	return addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr.IsMulticast()
}

func maskToPrefix(mask string) int {
	ip := net.ParseIP(mask)
	if ip == nil {
		return 24
	}
	ones, _ := net.IPMask(ip.To4()).Size()
	if ones == 0 {
		return 24
	}
	return ones
}

// ---------------------------------------------------------------------------
// 网卡:ip addr(Linux 新旧两种布局)/ ifconfig / ipconfig /all
// ---------------------------------------------------------------------------

// ParseIPAddr 解析 `ip addr` / `ip -o -4 addr show` 输出。
func ParseIPAddr(text string) []NIC {
	out := []NIC{}
	iface := ""
	for _, line := range strings.Split(text, "\n") {
		// "2: eth0: <BROADCAST,...>" / "5: eth0@if2: <...>" / ip -o 的 "2: eth0 inet ..."
		if m := regexp.MustCompile(`^\d+:\s+([^:@\s]+)[:@ ]`).FindStringSubmatch(line); m != nil {
			iface = m[1]
		}
		// inet 10.0.0.5/24(可能带 brd/scope 后缀;ip -o 与多行布局同此)
		if m := regexp.MustCompile(`\binet\s+(\d{1,3}(?:\.\d{1,3}){3})/(\d{1,2})\b`).FindStringSubmatch(line); m != nil {
			prefix, _ := strconv.Atoi(m[2])
			out = append(out, NIC{Iface: iface, IP: m[1], Prefix: prefix})
		}
	}
	return dedupNICs(out)
}

// ParseIfconfig 解析 `ifconfig` / `ifconfig -a` 输出(新式 flags 布局与老式
// Link encap 布局都认)。
func ParseIfconfig(text string) []NIC {
	out := []NIC{}
	iface := ""
	for _, line := range strings.Split(text, "\n") {
		if m := regexp.MustCompile(`^(\S+?):\s+flags=`).FindStringSubmatch(line); m != nil {
			iface = m[1]
			continue
		}
		if m := regexp.MustCompile(`^(\S+)\s+Link encap`).FindStringSubmatch(line); m != nil {
			iface = m[1]
			continue
		}
		// 新式: "inet 192.168.1.10  netmask 255.255.255.0  broadcast ..."
		// 老式: "inet addr:10.0.0.5  Bcast:10.0.0.255  Mask:255.255.255.0"
		m := regexp.MustCompile(`inet\s+(?:addr:)?(\d{1,3}(?:\.\d{1,3}){3})(?:\s+(?:Bcast|broadcast)[:=]?\s*\d{1,3}(?:\.\d{1,3}){3})?\s+(?:netmask\s*[:=]?\s*|Mask:\s*)(\d{1,3}(?:\.\d{1,3}){3})`).FindStringSubmatch(line)
		if m != nil && iface != "" {
			out = append(out, NIC{Iface: iface, IP: m[1], Prefix: maskToPrefix(m[2])})
		}
	}
	return dedupNICs(out)
}

// ParseIPConfigAll 解析 Windows `ipconfig /all`(中英文输出都认:IPv4 Address /
// IPv4 地址,Subnet Mask / 子网掩码;地址后可能带 "(首选)" 后缀)。
func ParseIPConfigAll(text string) []NIC {
	out := []NIC{}
	iface := ""
	pendingIP := ""
	for _, line := range strings.Split(text, "\n") {
		if m := regexp.MustCompile(`(?:adapter|适配器)\s+(.+?):\s*$`).FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			iface = strings.TrimSpace(m[1])
			pendingIP = ""
			continue
		}
		if m := regexp.MustCompile(`IPv4[^:：]*[:：]\s*(\d{1,3}(?:\.\d{1,3}){3})`).FindStringSubmatch(line); m != nil {
			pendingIP = m[1]
			continue
		}
		if m := regexp.MustCompile(`(?:Subnet Mask|子网掩码)[^:：]*[:：]\s*(\d{1,3}(?:\.\d{1,3}){3})`).FindStringSubmatch(line); m != nil {
			if pendingIP != "" {
				if iface == "" {
					iface = "iface"
				}
				out = append(out, NIC{Iface: iface, IP: pendingIP, Prefix: maskToPrefix(m[1])})
				pendingIP = ""
			}
		}
	}
	return dedupNICs(out)
}

// ParseNICsAuto 先按 ip addr 解析,空则 ifconfig,再空则 ipconfig /all。
func ParseNICsAuto(text string) []NIC {
	if n := ParseIPAddr(text); len(n) > 0 {
		return n
	}
	if n := ParseIfconfig(text); len(n) > 0 {
		return n
	}
	return ParseIPConfigAll(text)
}

func dedupNICs(in []NIC) []NIC {
	seen := map[string]bool{}
	out := []NIC{}
	for _, n := range in {
		key := n.Iface + "|" + n.IP
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, n)
	}
	return out
}

// ---------------------------------------------------------------------------
// 路由:ip route / route -n / route print
// ---------------------------------------------------------------------------

// ParseIPRoute 解析 Linux `ip route`。非默认且经网关(via)且非直连(scope link)
// 的路由标为静态路由。
func ParseIPRoute(text string) []Route {
	out := []Route{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		dest := fields[0]
		isDefault := dest == "default"
		if !isDefault && !strings.Contains(dest, "/") {
			continue // 非路由行
		}
		r := Route{Dest: dest}
		if m := regexp.MustCompile(`\bvia\s+(\S+)`).FindStringSubmatch(line); m != nil {
			r.Via = m[1]
		}
		if m := regexp.MustCompile(`\bdev\s+(\S+)`).FindStringSubmatch(line); m != nil {
			r.Iface = m[1]
		}
		connected := strings.Contains(line, "scope link") || strings.Contains(line, "proto kernel")
		r.IsStatic = !isDefault && r.Via != "" && !connected
		out = append(out, r)
	}
	return dedupRoutes(out)
}

// ParseRouteN 解析 Linux `route -n`(Destination Gateway Genmask Flags ... Iface)。
func ParseRouteN(text string) []Route {
	out := []Route{}
	for _, raw := range strings.Split(text, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 8 || !isIPv4(fields[0]) || !isIPv4(fields[1]) {
			continue
		}
		dest, gateway, mask, flags := fields[0], fields[1], fields[2], fields[3]
		r := Route{
			Dest:  SegmentOf(dest, maskToPrefix(mask)),
			Iface: fields[len(fields)-1],
		}
		isDefault := dest == "0.0.0.0"
		if isDefault {
			r.Dest = "default"
		}
		hasGW := strings.Contains(flags, "G")
		if hasGW {
			r.Via = gateway
		}
		r.IsStatic = !isDefault && hasGW
		out = append(out, r)
	}
	return dedupRoutes(out)
}

// ParseRoutePrint 解析 Windows `route print` 的 IPv4 活动路由(Network Destination
// Netmask Gateway Interface Metric;中文「网络目标/网络掩码/网关/接口/跃点数」同布局)。
// On-link(在链路上)网关不算静态路由。
func ParseRoutePrint(text string) []Route {
	out := []Route{}
	for _, raw := range strings.Split(text, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 5 || !isIPv4(fields[0]) || !isIPv4(fields[1]) {
			continue
		}
		dest, mask, gateway, iface := fields[0], fields[1], fields[2], fields[3]
		r := Route{Iface: iface}
		if dest == "0.0.0.0" && mask == "0.0.0.0" {
			r.Dest = "default"
		} else {
			r.Dest = SegmentOf(dest, maskToPrefix(mask))
		}
		onLink := !isIPv4(gateway) // "On-link" / "在链路上"
		if !onLink {
			r.Via = gateway
		}
		r.IsStatic = r.Dest != "default" && !onLink
		out = append(out, r)
	}
	return dedupRoutes(out)
}

// ParseRoutesAuto 依次按 ip route / route -n / route print 解析。
func ParseRoutesAuto(text string) []Route {
	if r := ParseIPRoute(text); len(r) > 0 {
		return r
	}
	if r := ParseRouteN(text); len(r) > 0 {
		return r
	}
	return ParseRoutePrint(text)
}

func dedupRoutes(in []Route) []Route {
	seen := map[string]bool{}
	out := []Route{}
	for _, r := range in {
		key := r.Dest + "|" + r.Via
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------------------
// 邻居:ip neigh / arp -a(Linux)/ arp -a(Windows)
// ---------------------------------------------------------------------------

// ParseNeighbors 解析 `ip neigh`、`arp -a`(Linux 与 Windows 两种格式)。
// FAILED/INCOMPLETE 项(无 MAC)不算存活证据,跳过。
func ParseNeighbors(text string) []Neighbor {
	out := []Neighbor{}
	seen := map[string]bool{}
	add := func(ip, mac, iface string) {
		if !isIPv4(ip) || isLoopbackOrLinkLocal(ip) || strings.HasPrefix(ip, "224.") || strings.HasPrefix(ip, "239.") {
			return
		}
		mac = strings.ToLower(strings.TrimSpace(mac))
		if mac == "" || mac == "<incomplete>" || mac == "ff-ff-ff-ff-ff-ff" || ip == "255.255.255.255" {
			return
		}
		if seen[ip] {
			return
		}
		seen[ip] = true
		out = append(out, Neighbor{IP: ip, MAC: mac, Iface: iface})
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		// ip neigh: "10.0.0.1 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE"
		if m := regexp.MustCompile(`^(\d{1,3}(?:\.\d{1,3}){3})\s+dev\s+(\S+)\s+lladdr\s+(\S+)`).FindStringSubmatch(line); m != nil {
			add(m[1], m[3], m[2])
			continue
		}
		// Linux arp -a: "? (10.0.0.1) at 00:11:22:33:44:55 [ether] on eth0"
		if m := regexp.MustCompile(`\((\d{1,3}(?:\.\d{1,3}){3})\)\s+at\s+(\S+)`).FindStringSubmatch(line); m != nil {
			iface := ""
			if im := regexp.MustCompile(`\bon\s+(\S+)`).FindStringSubmatch(line); im != nil {
				iface = im[1]
			}
			add(m[1], m[2], iface)
			continue
		}
		// Windows arp -a: "  10.0.0.1           00-11-22-33-44-55     动态/dynamic"
		if m := regexp.MustCompile(`^(\d{1,3}(?:\.\d{1,3}){3})\s+([0-9a-fA-F]{2}(?:-[0-9a-fA-F]{2}){5})\s+\S+`).FindStringSubmatch(line); m != nil {
			add(m[1], m[2], "")
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// /etc/hosts(含 Windows hosts)
// ---------------------------------------------------------------------------

// ParseHosts 解析 hosts 文件(忽略注释与空行)。
func ParseHosts(text string) []HostEntry {
	out := []HostEntry{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if !isIPv4(fields[0]) || len(fields) < 2 {
			continue
		}
		out = append(out, HostEntry{IP: fields[0], Names: fields[1:]})
	}
	return out
}

// ---------------------------------------------------------------------------
// 连接表:netstat -an / ss -an / netstat -ano
// ---------------------------------------------------------------------------

var knownStates = map[string]bool{
	"listen": true, "listening": true, "unconn": true,
	"established": true, "estab": true, "time_wait": true, "close_wait": true,
	"syn_sent": true, "syn_recv": true, "fin_wait1": true, "fin_wait2": true,
	"closing": true, "last_ack": true,
}

// splitHostPort 从 "10.0.0.5:22" / ":::8080" / "[::1]:3306" / "*:*" 拆 host/port。
// port 非数字("*")时 ok=false。
func splitHostPort(s string) (host string, port int, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	host = strings.Trim(s[:i], "[]")
	p := s[i+1:]
	if p == "*" || p == "" {
		return host, 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return "", 0, false
	}
	return host, n, true
}

// ParseConnTable 解析 netstat/ss 连接表,返回监听端口与已建立连接对端。
// 三种布局同一条解析路径:proto 在首列;state 与地址对的位置依工具不同,
// 靠「已知 state 词表 + addr:port 形态」识别,不依赖固定列位。
func ParseConnTable(text string) (listening []Listener, peers []Peer) {
	seenL := map[string]bool{}
	seenP := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 3 {
			continue
		}
		proto := strings.ToLower(fields[0])
		base := strings.TrimSuffix(proto, "6")
		if base != "tcp" && base != "udp" {
			continue
		}
		// state 与地址对:netstat 布局 state 在地址后,ss 布局 state 在 proto 后。
		state := ""
		addrs := []string{}
		for _, f := range fields[1:] {
			lf := strings.ToLower(f)
			if knownStates[lf] && state == "" {
				state = lf
				continue
			}
			if strings.Contains(f, ":") {
				addrs = append(addrs, f)
			}
		}
		if len(addrs) < 2 {
			continue
		}
		local, foreign := addrs[0], addrs[1]
		_, localPort, localOK := splitHostPort(local)
		foreignHost, foreignPort, foreignOK := splitHostPort(foreign)

		isListen := state == "listen" || state == "listening" || state == "unconn" ||
			(state == "" && base == "udp") // udp netstat 常无 state 列
		if isListen && localOK {
			key := base + "|" + strconv.Itoa(localPort)
			if !seenL[key] {
				seenL[key] = true
				listening = append(listening, Listener{Port: localPort, Proto: base})
			}
			continue
		}
		if (state == "established" || state == "estab") && foreignOK && isIPv4(foreignHost) &&
			!isLoopbackOrLinkLocal(foreignHost) {
			key := foreignHost + "|" + strconv.Itoa(foreignPort)
			if !seenP[key] {
				seenP[key] = true
				peers = append(peers, Peer{IP: foreignHost, Port: foreignPort})
			}
		}
	}
	sort.Slice(listening, func(i, j int) bool {
		if listening[i].Port != listening[j].Port {
			return listening[i].Port < listening[j].Port
		}
		return listening[i].Proto < listening[j].Proto
	})
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].IP != peers[j].IP {
			return peers[i].IP < peers[j].IP
		}
		return peers[i].Port < peers[j].Port
	})
	return listening, peers
}

// ---------------------------------------------------------------------------
// 高价值推断(照 pivot 骨架「被动六表」判据)
// ---------------------------------------------------------------------------

// Infer 从结构化报告推导高价值情报:
//   - 多网卡跨段 = 潜在跳板(multi_nic)
//   - 非默认静态路由指向的网段 = 必通的有价值段(static_route)
//   - established 对端里非本机网段 IP = 已证明可通的活主机(proven_peer)
//   - ARP 邻居 = 同网段被动可得的存活主机(arp_neighbors)
func Infer(r *ReconReport) []Inference {
	out := []Inference{}

	// 本机网段集合(排除回环/链路本地)。
	segments := map[string][]NIC{}
	for _, n := range r.NICs {
		if isLoopbackOrLinkLocal(n.IP) {
			continue
		}
		seg := SegmentOf(n.IP, n.Prefix)
		if seg == "" {
			continue
		}
		segments[seg] = append(segments[seg], n)
	}

	if len(segments) >= 2 {
		parts := []string{}
		for seg, nics := range segments {
			for _, n := range nics {
				parts = append(parts, fmt.Sprintf("%s %s/%d(网段 %s)", n.Iface, n.IP, n.Prefix, seg))
			}
		}
		sort.Strings(parts)
		out = append(out, Inference{
			Kind:    "multi_nic",
			Summary: fmt.Sprintf("主机有 %d 个非回环网段(%d 张网卡),是潜在跳板", len(segments), len(parts)),
			Detail:  strings.Join(parts, ";"),
		})
	}

	for _, rt := range r.Routes {
		if !rt.IsStatic {
			continue
		}
		out = append(out, Inference{
			Kind:    "static_route",
			Summary: fmt.Sprintf("非默认静态路由 %s via %s(%s)——管理员显式配置,该网段必通,属高价值侦察目标", rt.Dest, rt.Via, rt.Iface),
		})
	}

	// 已建立连接对端:不在本机任何网段的 = 已证明可通的外段活主机。
	inLocalSegment := func(ip string) bool {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return false
		}
		for seg := range segments {
			p, err := netip.ParsePrefix(seg)
			if err == nil && p.Contains(addr) {
				return true
			}
		}
		return false
	}
	proven := []string{}
	for _, p := range r.Peers {
		if !inLocalSegment(p.IP) {
			proven = append(proven, fmt.Sprintf("%s:%d", p.IP, p.Port))
		}
	}
	if len(proven) > 0 {
		out = append(out, Inference{
			Kind:    "proven_peer",
			Summary: fmt.Sprintf("%d 个已建立连接的对端不在本机网段——已证明可通的活主机(高成功率横向目标)", len(proven)),
			Detail:  strings.Join(proven, ";"),
		})
	}

	if len(r.Neighbors) > 0 {
		ips := []string{}
		for _, n := range r.Neighbors {
			ips = append(ips, n.IP)
		}
		detail := strings.Join(ips, ";")
		if len(ips) > 20 {
			detail = strings.Join(ips[:20], ";") + fmt.Sprintf("…(共 %d 个)", len(ips))
		}
		out = append(out, Inference{
			Kind:    "arp_neighbors",
			Summary: fmt.Sprintf("ARP 邻居表有 %d 个同网段存活主机(被动可得,无需发包)", len(r.Neighbors)),
			Detail:  detail,
		})
	}
	return out
}
