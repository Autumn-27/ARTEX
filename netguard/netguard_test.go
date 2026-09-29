package netguard

import (
	"net"
	"net/http"
	"testing"
)

// 默认策略：拒绝链路本地/组播/未指定（含云元数据 169.254.169.254），
// 放行环回与内网（兼容本地/内网 LLM 网关）。
func TestCheckURL_DefaultPolicy(t *testing.T) {
	cases := []struct {
		url string
		err bool
	}{
		{"", true},
		{"not a url", true},
		{"http://169.254.169.254/latest/meta-data/", true}, // 云元数据
		{"http://169.254.1.1/", true},
		{"http://[fe80::1]/", true},          // IPv6 link-local
		{"http://224.0.0.1/", true},          // 组播
		{"http://0.0.0.0/", true},            // 未指定
		{"http://127.0.0.1:11434/v1", false}, // 本地 LLM 网关，默认放行
		{"http://192.168.1.50:8080/v1", false},
		{"http://10.0.0.1:8000", false},
		{"https://api.openai.com/v1", false},
		{"http://example.com", false},
	}
	for _, c := range cases {
		err := CheckURL(c.url)
		got := err != nil
		if got != c.err {
			t.Errorf("CheckURL(%q) err=%v, want err=%v", c.url, err, c.err)
		}
	}
}

func TestIsBlockedIP(t *testing.T) {
	cases := []struct {
		ip    string
		block bool
	}{
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"0.0.0.0", true}, // 未指定地址
		{"224.0.0.1", true},
		{"10.1.2.3", false},
		{"192.168.0.10", false},
		{"172.16.5.5", false},
		{"8.8.8.8", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", c.ip)
		}
		if got := IsBlockedIP(ip); got != c.block {
			t.Errorf("IsBlockedIP(%s)=%v, want %v", c.ip, got, c.block)
		}
	}
}

// ProtectTransport 必须替换 transport 的 DialContext（SSRF 防护拨号）。
func TestProtectTransport_SetsDialer(t *testing.T) {
	tr := &http.Transport{}
	if tr.DialContext != nil {
		t.Fatal("expected a fresh transport with no DialContext")
	}
	ProtectTransport(tr)
	if tr.DialContext == nil {
		t.Fatal("ProtectTransport must install a protective DialContext")
	}
}

// SafeDialer 返回的拨号函数对禁网段（元数据）拒绝、对公网放行（解析到 IP 层面）。
func TestSafeDialer_RejectsMetadata(t *testing.T) {
	d := SafeDialer()
	conn, err := d(t.Context(), "tcp", "169.254.169.254:80")
	if err == nil {
		conn.Close()
		t.Error("dial to cloud metadata IP must be rejected by SafeDialer")
	}
}
