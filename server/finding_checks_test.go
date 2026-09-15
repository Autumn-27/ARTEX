package server

import "testing"

func TestFindingVerifyAutoEnabled(t *testing.T) {
	// 默认(未设置/未知值)为 auto;只有显式 off 关闭(内网阶段)。
	for _, mode := range []string{"", "auto", "AUTO", "whatever"} {
		if !findingVerifyAutoEnabled(mode) {
			t.Errorf("mode %q should keep auto-verify enabled", mode)
		}
	}
	if findingVerifyAutoEnabled("off") {
		t.Error("mode off should disable auto-verify")
	}
}
