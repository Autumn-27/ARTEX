package guard

import (
	"net"
	"strings"
)

// ScopeRule is one RoE authorization-scope rule. Kind is one of
// domain|ip|cidr|icp|keyword (db task_scope 的 root_domain/subdomain 在 server
// 装配时归一化为 domain,ip 行的 /32、/128 网段按 cidr 传入)。
// keyword 与 db/task_scope.go 的既有语义一致:登记后不参与匹配(死规则)。
type ScopeRule struct {
	Kind  string
	Value string
}

// ScopeVerdict is the three-state result of a scope check.
type ScopeVerdict int

const (
	// ScopeUnknown means the task has no registered scope — 未登记范围,放行(保持现状语义)。
	ScopeUnknown ScopeVerdict = iota
	// ScopeIn means the host matches at least one scope rule.
	ScopeIn
	// ScopeOut means scope rules exist but none match the host.
	ScopeOut
)

func (v ScopeVerdict) String() string {
	switch v {
	case ScopeIn:
		return "in"
	case ScopeOut:
		return "out"
	default:
		return "unknown"
	}
}

// ScopeMatcher matches hosts against a task's RoE scope rules. Pure and
// stateless; safe for concurrent use.
type ScopeMatcher struct {
	domains  []string
	ips      []net.IP
	cidrs    []*net.IPNet
	hasRules bool
	// AllowLocal treats worker-local targets (loopback / link-local / localhost)
	// as in-scope: 本机操作不算出界。默认开;关掉后本机地址也走范围匹配。
	// 注意:RFC1918 内网地址不在放行之列——内网任务必须把目标网段登记进
	// task_scope,否则 RoE 对内网形同虚设。
	AllowLocal bool
}

// NewScopeMatcher compiles rules into a matcher. Rules with unparsable values
// are skipped. An empty (or all-skipped) rule set yields ScopeUnknown verdicts.
func NewScopeMatcher(rules []ScopeRule) *ScopeMatcher {
	m := &ScopeMatcher{AllowLocal: true}
	for _, r := range rules {
		v := strings.TrimSpace(r.Value)
		if v == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(r.Kind)) {
		case "domain", "icp":
			// icp 按 domain 语义处理(域名后缀匹配)。
			d := normalizeHost(v)
			if d != "" && net.ParseIP(d) == nil {
				m.domains = append(m.domains, d)
				m.hasRules = true
			}
		case "ip":
			if ip := net.ParseIP(normalizeHost(v)); ip != nil {
				m.ips = append(m.ips, ip)
				m.hasRules = true
			}
		case "cidr":
			if _, n, err := net.ParseCIDR(v); err == nil {
				m.cidrs = append(m.cidrs, n)
				m.hasRules = true
			}
		case "keyword":
			// 死规则:不参与匹配(见 db/task_scope.go covTargetCTE 注释)。
		}
	}
	return m
}

// Check reports whether host is in scope. host may carry a :port (stripped)
// and is matched case-insensitively with trailing dots removed.
func (m *ScopeMatcher) Check(host string) ScopeVerdict {
	if !m.hasRules {
		return ScopeUnknown
	}
	h := normalizeHost(host)
	if h == "" {
		return ScopeUnknown
	}
	if m.AllowLocal && isLocalHost(h) {
		return ScopeIn
	}
	if ip := net.ParseIP(h); ip != nil {
		for _, rip := range m.ips {
			if rip.Equal(ip) {
				return ScopeIn
			}
		}
		for _, n := range m.cidrs {
			if n.Contains(ip) {
				return ScopeIn
			}
		}
		return ScopeOut
	}
	for _, d := range m.domains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return ScopeIn
		}
	}
	return ScopeOut
}

// InScope is the boolean convenience form of Check (true only for ScopeIn).
func (m *ScopeMatcher) InScope(host string) bool { return m.Check(host) == ScopeIn }

// normalizeHost lowercases, trims a trailing dot and an optional :port, and
// strips IPv6 brackets.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			return strings.ToLower(strings.TrimSuffix(h[1:i], "."))
		}
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// isLocalHost reports whether h is a worker-local target: loopback, link-local,
// unspecified addresses, and the "localhost" name.
func isLocalHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	return false
}
