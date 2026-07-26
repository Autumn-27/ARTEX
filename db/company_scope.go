package db

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ParsedScope is one parsed asset-scope entry. Exactly one of Domain / Net is set.
// Used internally by ParseScopeLine / ParseScopeLines (passed to CompanyStore).
type ParsedScope struct {
	Kind   string // "domain" | "ip" | "cidr"
	Domain string // normalized registrable/root domain (kind=domain)
	Net    string // normalized CIDR, single IP as /32 or /128 (kind=ip|cidr)
	Raw    string // original input line
}

// ParseScopeLine classifies and validates one scope line (root domain / IP /
// CIDR). Guardrails reject bare TLDs and over-broad networks so a rule can never
// swallow the internet. IP ranges must be expressed as CIDR.
func ParseScopeLine(line string) (ParsedScope, error) {
	raw := strings.TrimSpace(line)
	r := ParsedScope{Raw: raw}
	if raw == "" {
		return r, fmt.Errorf("空行")
	}
	s := raw
	if i := strings.Index(s, "://"); i >= 0 { // strip scheme if a URL was pasted
		s = s[i+3:]
	}
	// CIDR first — it contains '/', which the path-strip below would remove.
	if _, ipnet, err := net.ParseCIDR(s); err == nil {
		ones, bits := ipnet.Mask.Size()
		if bits == 32 && ones < 16 {
			return r, fmt.Errorf("网段过宽(IPv4 需 >= /16): %s", raw)
		}
		if bits == 128 && ones < 32 {
			return r, fmt.Errorf("网段过宽(IPv6 需 >= /32): %s", raw)
		}
		r.Kind, r.Net = "cidr", ipnet.String()
		return r, nil
	}
	// Strip any path and :port remnants for host-like inputs.
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	// Single IP.
	if ip := net.ParseIP(strings.TrimSpace(s)); ip != nil {
		r.Kind = "ip"
		if ip.To4() != nil {
			r.Net = ip.String() + "/32"
		} else {
			r.Net = ip.String() + "/128"
		}
		return r, nil
	}
	if host, _, ok := strings.Cut(s, ":"); ok { // host:port → host
		s = host
	}
	if strings.Contains(raw, "-") && strings.Count(raw, ".") >= 6 {
		return r, fmt.Errorf("IP 段请用 CIDR 表示(如 1.2.3.0/24): %s", raw)
	}
	// Domain (registrable). Reject bare TLDs / public suffixes.
	d := DomainKey(s)
	if d == "" || !strings.Contains(d, ".") {
		return r, fmt.Errorf("无法识别为根域名/IP/CIDR: %s", raw)
	}
	if suf, icann := publicsuffix.PublicSuffix(d); icann && suf == d {
		return r, fmt.Errorf("不能用裸 TLD 作为范围: %s", raw)
	}
	r.Kind, r.Domain = "domain", d
	return r, nil
}

// ParseScopeLines parses a whole text block (one entry per line), returning the
// valid rules and the invalid lines (with reasons) so the caller can report both.
func ParseScopeLines(text string) (rules []ParsedScope, invalid []string) {
	for _, ln := range strings.Split(text, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		r, err := ParseScopeLine(ln)
		if err != nil {
			invalid = append(invalid, err.Error())
			continue
		}
		rules = append(rules, r)
	}
	return rules, invalid
}
