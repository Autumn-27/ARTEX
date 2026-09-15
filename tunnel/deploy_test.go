// deploy_test.go 是部署辅助纯函数的单测（真实 chisel 部署属 VPS e2e 验收）。
package tunnel

import "testing"

func TestRewriteURLHost(t *testing.T) {
	cases := []struct {
		name         string
		url          string
		platformAddr string
		want         string
	}{
		{"回环占位换 callback host", "http://127.0.0.1:8787/s/tok/chisel", "10.0.0.5:20001", "http://10.0.0.5:8787/s/tok/chisel"},
		{"无端口 URL", "http://example.com/s/tok/chisel", "10.0.0.5:20001", "http://10.0.0.5/s/tok/chisel"},
		{"platformAddr 非法原样返回", "http://127.0.0.1:8787/s/tok/chisel", "bad", "http://127.0.0.1:8787/s/tok/chisel"},
		{"URL 非法原样返回", "://nope", "10.0.0.5:20001", "://nope"},
	}
	for _, c := range cases {
		if got := rewriteURLHost(c.url, c.platformAddr); got != c.want {
			t.Errorf("%s: rewriteURLHost = %q, want %q", c.name, got, c.want)
		}
	}
}
