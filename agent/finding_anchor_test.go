package agent

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

// anchorTestAssets 覆盖 endpoint/service/ip/root_domain 四类的固定资产集。
func anchorTestAssets() []*db.Asset {
	return []*db.Asset{
		{ID: 1, Type: "endpoint", URL: "http://example.com/api"},
		{ID: 2, Type: "endpoint", URL: "http://example.com/api/users"},
		{ID: 3, Type: "endpoint", URL: "https://example.com/api"},
		{ID: 4, Type: "service", URL: "http://example.com"},
		{ID: 5, Type: "service", URL: "http://10.0.0.1:8080"},
		{ID: 6, Type: "ip", IP: "10.0.0.2"},
		{ID: 7, Type: "ip", IP: "10.0.0.1"},
		{ID: 8, Type: "root_domain", Domain: "example.com"},
	}
}

func TestAnchorFindingAsset(t *testing.T) {
	assets := anchorTestAssets()
	cases := []struct {
		name string
		text string
		want int64
	}{
		{
			name: "endpoint 最长前缀优先",
			text: "GET http://example.com/api/users/1?q=<script> 反射型 XSS",
			want: 2, // /api/users 比 /api 前缀更长
		},
		{
			name: "endpoint 次长前缀",
			text: "http://example.com/api/x 存在注入",
			want: 1,
		},
		{
			name: "路径段边界不前缀匹配",
			text: "http://example.com/apisix 未授权访问", // /api 不命中 /apisix
			want: 4,                              // 落到 service
		},
		{
			name: "service 回退",
			text: "http://example.com/other/path RCE",
			want: 4,
		},
		{
			name: "https/http 区分:https 命中 https endpoint",
			text: "https://example.com/api 敏感信息泄露",
			want: 3,
		},
		{
			name: "https/http 区分:http service 不匹配 https URL",
			text: "https://example.com/login 弱口令", // https endpoint /api 不前缀匹配 /login
			want: 8,                                 // http service 端口/scheme 不符 → root_domain
		},
		{
			name: "显式端口不匹配默认端口 service",
			text: "http://example.com:8080/x 任意文件上传",
			want: 8, // service 是 80 端口 → root_domain
		},
		{
			name: "ip 兜底",
			text: "http://10.0.0.2:9000/admin 存在弱口令",
			want: 6,
		},
		{
			name: "service 优先于同 host 的 ip",
			text: "http://10.0.0.1:8080/actuator 未授权",
			want: 5,
		},
		{
			name: "root_domain 兜底:子域名后缀匹配",
			text: "https://sub.example.com/ sqli",
			want: 8,
		},
		{
			name: "多个 URL 取第一个",
			text: "先请求 http://example.com/api/x 再对比 http://10.0.0.2/y",
			want: 1,
		},
		{
			name: "无 URL 按 host:port 匹配 service",
			text: "10.0.0.1:8080 存在未授权访问",
			want: 5,
		},
		{
			name: "无 URL 裸 host 兜底到 ip",
			text: "10.0.0.2 开放了 22 端口且弱口令可登录",
			want: 6,
		},
		{
			name: "无 URL 无命中保持空",
			text: "内网服务存在弱口令,暂无可用标识",
			want: 0,
		},
		{
			name: "空文本",
			text: "",
			want: 0,
		},
		{
			name: "URL 带尾标点需剔除",
			text: "见 http://example.com/api/x. 响应中反射了输入",
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := anchorFindingAsset(c.text, assets)
			if got != c.want {
				t.Errorf("anchorFindingAsset(%q) = %d (%s), want %d", c.text, got, reason, c.want)
			}
			if (got > 0) != (reason != "") {
				t.Errorf("id=%d 与理由 %q 不一致", got, reason)
			}
		})
	}
}

func TestAnchorFindingAssetEmptyCandidates(t *testing.T) {
	if got, _ := anchorFindingAsset("http://example.com/x", nil); got != 0 {
		t.Errorf("空资产集应返回 0, got %d", got)
	}
}

func TestAnchorFindingAssetDeterministicTie(t *testing.T) {
	// 两条同路径 endpoint:取 id 最小者,保证合并键稳定。
	assets := []*db.Asset{
		{ID: 9, Type: "endpoint", URL: "http://example.com/api"},
		{ID: 4, Type: "endpoint", URL: "http://example.com/api"},
	}
	if got, _ := anchorFindingAsset("http://example.com/api/x", assets); got != 4 {
		t.Errorf("同分应取最小 id 4, got %d", got)
	}
}

func TestFallbackFindingName(t *testing.T) {
	cases := []struct {
		vulnClass, text, want string
	}{
		{"xss", "见 http://example.com/api/users/1 反射", "xss http://example.com/api/users/1"},
		{"sqli", "没有 URL 也没有 host", "sqli"},
		{"", "", "未命名漏洞"},
		{"  xss  ", "http://a.com/1", "xss http://a.com/1"},
	}
	for _, c := range cases {
		if got := fallbackFindingName(c.vulnClass, c.text); got != c.want {
			t.Errorf("fallbackFindingName(%q, %q) = %q, want %q", c.vulnClass, c.text, got, c.want)
		}
	}
}
