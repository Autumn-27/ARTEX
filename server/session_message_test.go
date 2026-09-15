package server

// session_message_test.go 覆盖 register_session 返回的马型标签与登记明示文案
// （复盘 R1：命令马登记成功必须明示「已登记为命令执行马」;disable_functions
// 指纹与绕过可用性如实上报）。纯函数测试，不依赖 PG。

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/session"
)

func TestShellTypeLabel(t *testing.T) {
	cases := []struct {
		info session.ProbeInfo
		want string
	}{
		{session.ProbeInfo{Lang: "php", Variant: "eval"}, "eval"},
		{session.ProbeInfo{Lang: "php", Variant: "assert"}, "eval"},
		{session.ProbeInfo{Lang: "phpcmd", Variant: "cmd"}, "cmd"},
		{session.ProbeInfo{Lang: "jsp"}, "jsp"},
		{session.ProbeInfo{Lang: "aspx"}, "aspx"},
		{session.ProbeInfo{Lang: "phpenc", Variant: "eval"}, "enc"},
		{session.ProbeInfo{Lang: "jspenc"}, "enc"},
	}
	for _, c := range cases {
		if got := shellTypeLabel(c.info); got != c.want {
			t.Errorf("shellTypeLabel(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}

func TestRegisterMessageCmdShellExplicit(t *testing.T) {
	msg := registerMessage(session.ProbeInfo{Lang: "phpcmd", Variant: "cmd"})
	if !strings.Contains(msg, "已登记为命令执行马") {
		t.Fatalf("命令马登记必须明示, got %q", msg)
	}
}

func TestRegisterMessageEvalWithBypass(t *testing.T) {
	msg := registerMessage(session.ProbeInfo{
		Lang: "php", Variant: "eval", DisableFunctions: true, Bypass: "ld_preload",
	})
	if !strings.Contains(msg, "disable_functions") || !strings.Contains(msg, "LD_PRELOAD") {
		t.Fatalf("disable_functions + 绕过可用必须明示, got %q", msg)
	}
	msg = registerMessage(session.ProbeInfo{Lang: "php", Variant: "eval", DisableFunctions: true})
	if !strings.Contains(msg, "仅文件读写") {
		t.Fatalf("无绕过时必须明示退化边界, got %q", msg)
	}
	msg = registerMessage(session.ProbeInfo{Lang: "php", Variant: "eval"})
	if !strings.Contains(msg, "eval 马") {
		t.Fatalf("正常 eval 马文案异常, got %q", msg)
	}
}
