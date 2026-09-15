package guard

import "testing"

func scopeRules(kv ...string) []ScopeRule {
	var out []ScopeRule
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, ScopeRule{Kind: kv[i], Value: kv[i+1]})
	}
	return out
}

func TestScopeMatcherDomain(t *testing.T) {
	m := NewScopeMatcher(scopeRules("domain", "Example.COM"))
	cases := []struct {
		host string
		want ScopeVerdict
	}{
		{"example.com", ScopeIn},            // 精确匹配,大小写不敏感
		{"EXAMPLE.COM", ScopeIn},            //
		{"www.example.com", ScopeIn},        // 子域
		{"a.b.example.com", ScopeIn},        // 多级子域
		{"example.com.", ScopeIn},           // 末尾点
		{"www.EXAMPLE.com.", ScopeIn},       // 大小写+末尾点混合
		{"notexample.com", ScopeOut},        // 后缀不等于子域
		{"example.com.evil.com", ScopeOut},  // 域名做前缀不算
		{"other.org", ScopeOut},             //
		{"example.com:8443", ScopeIn},       // host:port 去端口
	}
	for _, c := range cases {
		if got := m.Check(c.host); got != c.want {
			t.Errorf("Check(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestScopeMatcherIP(t *testing.T) {
	m := NewScopeMatcher(scopeRules("ip", "10.0.0.5", "ip", "2001:db8::1"))
	for _, c := range []struct {
		host string
		want ScopeVerdict
	}{
		{"10.0.0.5", ScopeIn},
		{"10.0.0.5:8080", ScopeIn}, // 去端口后精确匹配
		{"10.0.0.6", ScopeOut},
		{"2001:db8::1", ScopeIn},
		{"[2001:db8::1]:443", ScopeIn},
		{"2001:db8::2", ScopeOut},
	} {
		if got := m.Check(c.host); got != c.want {
			t.Errorf("Check(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestScopeMatcherCIDR(t *testing.T) {
	m := NewScopeMatcher(scopeRules("cidr", "192.168.1.0/24", "cidr", "2001:db8::/32"))
	for _, c := range []struct {
		host string
		want ScopeVerdict
	}{
		{"192.168.1.1", ScopeIn},
		{"192.168.1.254", ScopeIn},
		{"192.168.2.1", ScopeOut},
		{"2001:db8::dead:beef", ScopeIn}, // IPv6 包含
		{"2001:db9::1", ScopeOut},
	} {
		if got := m.Check(c.host); got != c.want {
			t.Errorf("Check(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestScopeMatcherICPAndKeyword(t *testing.T) {
	// icp 按 domain 语义;keyword 是死规则,不参与匹配。
	m := NewScopeMatcher(scopeRules("icp", "example.cn", "keyword", "某集团"))
	if got := m.Check("www.example.cn"); got != ScopeIn {
		t.Errorf("icp 域名语义应命中, got %v", got)
	}
	if got := m.Check("evil.com"); got != ScopeOut {
		t.Errorf("keyword 规则不应命中任何 host, got %v", got)
	}
}

func TestScopeMatcherBadRules(t *testing.T) {
	// 坏规则跳过;全部跳过 = 空规则 → Unknown(不算"已登记范围内")。
	m := NewScopeMatcher(scopeRules("cidr", "not-a-cidr", "ip", "999.999.1.1", "domain", "", "keyword", "x"))
	if got := m.Check("anything.com"); got != ScopeUnknown {
		t.Errorf("全部坏规则应为 Unknown, got %v", got)
	}
	// 好坏混合:好规则照常生效。
	m2 := NewScopeMatcher(scopeRules("cidr", "bogus", "domain", "ok.com"))
	if got := m2.Check("ok.com"); got != ScopeIn {
		t.Errorf("好规则应生效, got %v", got)
	}
}

func TestScopeMatcherEmptyRulesUnknown(t *testing.T) {
	m := NewScopeMatcher(nil)
	if got := m.Check("8.8.8.8"); got != ScopeUnknown {
		t.Errorf("空规则 = 未登记范围, 应为 Unknown, got %v", got)
	}
	if m.InScope("8.8.8.8") {
		t.Error("Unknown 不应视为 InScope")
	}
}

func TestScopeMatcherLocalOption(t *testing.T) {
	m := NewScopeMatcher(scopeRules("domain", "target.com"))
	for _, h := range []string{"127.0.0.1", "localhost", "::1", "169.254.1.1"} {
		if got := m.Check(h); got != ScopeIn {
			t.Errorf("默认 AllowLocal:%s 应放行(worker 本机操作), got %v", h, got)
		}
	}
	// RFC1918 内网地址不在本机放行之列——必须登记进范围。
	if got := m.Check("10.0.0.9"); got != ScopeOut {
		t.Errorf("未登记的内网地址应 Out, got %v", got)
	}
	m.AllowLocal = false
	if got := m.Check("127.0.0.1"); got != ScopeOut {
		t.Errorf("AllowLocal=false 时 127.0.0.1 应走范围匹配, got %v", got)
	}
}
