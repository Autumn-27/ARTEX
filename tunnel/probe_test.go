// probe_test.go 是探针解析与证据分档的纯函数单测。
package tunnel

import (
	"strings"
	"testing"
)

func TestParseProbeOutput(t *testing.T) {
	cases := []struct {
		name      string
		tag, out  string
		wantOK    bool
		wantFound bool
	}{
		{"ok 标记", "TCP_1", "ARTEX_TCP_1_OK\n", true, true},
		{"fail 标记", "TCP_1", "ARTEX_TCP_1_FAIL\n", false, true},
		{"标记混在回显里", "DNS", ";; reply\nARTEX_DNS_OK\n;; done", true, true},
		{"无标记（通道异常）", "TCP_1", "bash: connect: 拒绝\n", false, false},
		{"空输出", "TCP_1", "", false, false},
		{"OK 子串不误命中 FAIL", "HTTP_2", "ARTEX_HTTP_2_OK_200", true, true},
		{"其它探针标记不误命中", "TCP_1", "ARTEX_TCP_2_OK\n", false, false},
	}
	for _, c := range cases {
		ok, found := ParseProbeOutput(c.tag, c.out)
		if ok != c.wantOK || found != c.wantFound {
			t.Errorf("%s: ParseProbeOutput(%q,%q) = (%v,%v), want (%v,%v)",
				c.name, c.tag, c.out, ok, found, c.wantOK, c.wantFound)
		}
	}
}

func rep(results ...ProbeResult) *ProbeReport {
	return &ProbeReport{SessionID: 1, Results: results}
}

func TestProbeCmdTCPThreeState(t *testing.T) {
	cmd := probeCmd("tcp", "TCP_1", "10.0.0.5:20000")
	for _, want := range []string{
		"timeout 6", "/dev/tcp/10.0.0.5/20000", "rc=$?",
		"ARTEX_TCP_1_OK_FILTERED", "ARTEX_TCP_1_OK_REFUSED", "ARTEX_TCP_1_OK_FAIL",
		"refused", // 拒连关键词匹配（RST 算通）
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("tcp 探针命令缺少 %q:\n%s", want, cmd)
		}
	}
}

func TestParseTCPOutput(t *testing.T) {
	cases := []struct {
		name      string
		tag, out  string
		wantState TCPState
		wantFound bool
	}{
		{"连接成功", "TCP_1", "ARTEX_TCP_1_OK\n", TCPConnected, true},
		{"refused 拒连（路径通、暂无监听）", "TCP_1", "ARTEX_TCP_1_OK_REFUSED\n", TCPRefused, true},
		{"refused 不得误判为 connected", "TCP_1", "bash: connect: Connection refused\nARTEX_TCP_1_OK_REFUSED\n", TCPRefused, true},
		{"超时 filtered", "TCP_1", "ARTEX_TCP_1_OK_FILTERED\n", TCPFiltered, true},
		{"filtered 不得误判为 connected", "TCP_1", "ARTEX_TCP_1_OK_FILTERED\n", TCPFiltered, true},
		{"其它失败", "TCP_1", "ARTEX_TCP_1_OK_FAIL\n", TCPFail, true},
		{"无标记（通道异常）", "TCP_1", "bash: command not found\n", TCPFail, false},
		{"空输出", "TCP_1", "", TCPFail, false},
		{"其它探针标记不误命中", "TCP_1", "ARTEX_TCP_2_OK\n", TCPFail, false},
	}
	for _, c := range cases {
		state, found := ParseTCPOutput(c.tag, c.out)
		if state != c.wantState || found != c.wantFound {
			t.Errorf("%s: ParseTCPOutput(%q,%q) = (%v,%v), want (%v,%v)",
				c.name, c.tag, c.out, state, found, c.wantState, c.wantFound)
		}
	}
}

func TestProbeAddrs(t *testing.T) {
	cases := []struct {
		name      string
		host      string
		poolRange string
		want      []string
	}{
		{
			"常用口 + 端口池起始终端采样",
			"10.0.0.5", "20000-21000",
			[]string{"10.0.0.5:443", "10.0.0.5:80", "10.0.0.5:53", "10.0.0.5:20000", "10.0.0.5:21000"},
		},
		{
			"空池走默认端口池",
			"10.0.0.5", "",
			[]string{"10.0.0.5:443", "10.0.0.5:80", "10.0.0.5:53", "10.0.0.5:20000", "10.0.0.5:21000"},
		},
		{
			"非法池只保留常用口",
			"10.0.0.5", "abc",
			[]string{"10.0.0.5:443", "10.0.0.5:80", "10.0.0.5:53"},
		},
		{
			"池口与常用口重复时去重",
			"10.0.0.5", "80-90",
			[]string{"10.0.0.5:443", "10.0.0.5:80", "10.0.0.5:53", "10.0.0.5:90"},
		},
		{
			"单口池不重复采样",
			"10.0.0.5", "20000-20000",
			[]string{"10.0.0.5:443", "10.0.0.5:80", "10.0.0.5:53", "10.0.0.5:20000"},
		},
	}
	for _, c := range cases {
		got := ProbeAddrs(c.host, c.poolRange)
		if len(got) != len(c.want) {
			t.Errorf("%s: ProbeAddrs(%q,%q) = %v, want %v", c.name, c.host, c.poolRange, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: ProbeAddrs(%q,%q)[%d] = %q, want %q", c.name, c.host, c.poolRange, i, got[i], c.want[i])
			}
		}
	}
}

func TestConcludeGrades(t *testing.T) {
	tcpOK := ProbeResult{Name: "tcp_1", Kind: "tcp", Target: "10.0.0.5:443", OK: true, State: string(TCPConnected)}
	tcpRefused := ProbeResult{Name: "tcp_4", Kind: "tcp", Target: "10.0.0.5:20000", OK: true, State: string(TCPRefused)}
	tcpFiltered := ProbeResult{Name: "tcp_1", Kind: "tcp", Target: "10.0.0.5:443", State: string(TCPFiltered)}
	tcpFail := ProbeResult{Name: "tcp_1", Kind: "tcp", Target: "10.0.0.5:443"}
	httpOK := ProbeResult{Name: "http_1", Kind: "http", Target: "10.0.0.5:443", OK: true}
	dnsOK := ProbeResult{Name: "dns", Kind: "dns", Target: "www.baidu.com", OK: true}
	icmpOK := ProbeResult{Name: "icmp", Kind: "icmp", Target: "223.5.5.5", OK: true}

	cases := []struct {
		name         string
		report       *ProbeReport
		wantGrade    string
		wantFeasible bool
		wantPort     int
	}{
		{"TCP 通→反向 socks", rep(tcpOK, dnsOK), GradeTCPEgress, true, 443},
		{"TCP 通建议端口取首个探通口", rep(ProbeResult{Kind: "tcp", Target: "10.0.0.5:80", OK: true}, tcpOK), GradeTCPEgress, true, 80},
		{"refused 算通（RST 证明路径可达）", rep(tcpFiltered, tcpRefused, dnsOK), GradeTCPEgress, true, 20000},
		{"常用口 filtered 但池口 refused→tcp_egress", rep(
			ProbeResult{Kind: "tcp", Target: "10.0.0.5:443", State: string(TCPFiltered)},
			ProbeResult{Kind: "tcp", Target: "10.0.0.5:80", State: string(TCPFiltered)},
			ProbeResult{Kind: "tcp", Target: "10.0.0.5:53", State: string(TCPFiltered)},
			tcpRefused, dnsOK), GradeTCPEgress, true, 20000},
		{"只 HTTP→http_only", rep(tcpFiltered, httpOK, dnsOK), GradeHTTPOnly, true, 0},
		{"全 filtered 但 DNS 通→dns_only 不建议隧道", rep(tcpFiltered, dnsOK), GradeDNSOnly, false, 0},
		{"只 DNS→dns_only 不建议隧道", rep(tcpFail, dnsOK), GradeDNSOnly, false, 0},
		{"只 ICMP→icmp_only", rep(tcpFail, icmpOK), GradeICMPOnly, false, 0},
		{"全断→isolated 建议正向/webshell", rep(tcpFail), GradeIsolated, false, 0},
		{"空报告→isolated", rep(), GradeIsolated, false, 0},
	}
	for _, c := range cases {
		got := Conclude(c.report)
		if got.Grade != c.wantGrade || got.Feasible != c.wantFeasible || got.SuggestedPort != c.wantPort {
			t.Errorf("%s: Conclude = %+v, want grade=%s feasible=%v port=%d",
				c.name, got, c.wantGrade, c.wantFeasible, c.wantPort)
		}
		if got.Recommendation == "" {
			t.Errorf("%s: Recommendation 不能为空", c.name)
		}
	}
}

func TestProbeReportHelpers(t *testing.T) {
	r := rep(
		ProbeResult{Kind: "tcp", Target: "10.0.0.5:443"},
		ProbeResult{Kind: "tcp", Target: "10.0.0.5:53", OK: true},
		ProbeResult{Kind: "dns", Target: "x", OK: true},
	)
	if !r.OKKind("tcp") || !r.OKKind("dns") || r.OKKind("http") {
		t.Error("OKKind 判定错误")
	}
	if p := r.FirstOKPort(); p != 53 {
		t.Errorf("FirstOKPort = %d, want 53", p)
	}
}
