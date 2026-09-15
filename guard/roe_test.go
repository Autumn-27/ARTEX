package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// RoE 集成测试(无 interceptor 的可测部分):strict+Out 无审批通道 → 硬拦;
// warn → 放行但写审计;In / Unknown / off / 无目标 / 无 taskID → 放行。

func roeGuard(mode string, rules []ScopeRule) *Guard {
	g := New()
	g.SetRoE(RoEConfig{
		Mode:     func() string { return mode },
		Scope:    func(taskID int64) []ScopeRule { return rules },
		TaskIDOf: func(ctx context.Context) int64 { return 42 },
	})
	return g
}

func bashBlocked(t *testing.T, g *Guard, cmd string) (bool, string) {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"command": cmd})
	blocked, msg, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
	return blocked, msg
}

func TestRoEStrictBlocksOutOfScope(t *testing.T) {
	g := roeGuard("strict", []ScopeRule{{Kind: "domain", Value: "target.com"}})
	blocked, msg := bashBlocked(t, g, `curl https://evil.com/`)
	if !blocked {
		t.Fatal("strict 模式下范围外目标应被拦截")
	}
	if !strings.Contains(msg, "evil.com") || !strings.Contains(msg, "roe_enforcement") {
		t.Errorf("拦截消息应写明 host 与关闭/降级方式, got: %s", msg)
	}
}

func TestRoEStrictAllowsInScope(t *testing.T) {
	g := roeGuard("strict", []ScopeRule{{Kind: "domain", Value: "target.com"}})
	for _, cmd := range []string{
		`curl https://api.target.com/`,
		`nmap 127.0.0.1`,           // 本机操作放行
		`ls -la`,                   // 无目标
		`curl https://target.com/`, //
	} {
		if blocked, _ := bashBlocked(t, g, cmd); blocked {
			t.Errorf("strict 模式下 %q 不应被拦截", cmd)
		}
	}
}

func TestRoEUnknownScopeAllows(t *testing.T) {
	// 未登记范围(空规则)→ Unknown → 放行,保持现状语义。
	g := roeGuard("strict", nil)
	if blocked, _ := bashBlocked(t, g, `curl https://anything.com/`); blocked {
		t.Error("未登记范围不应拦截")
	}
}

func TestRoEWarnAllowsButAudits(t *testing.T) {
	g := roeGuard("warn", []ScopeRule{{Kind: "cidr", Value: "10.0.0.0/8"}})
	if blocked, _ := bashBlocked(t, g, `nmap 172.16.0.5`); blocked {
		t.Fatal("warn 模式应放行")
	}
	var found bool
	for _, e := range g.Audit() {
		if e.Action == "allow" && strings.Contains(e.Reason, "RoE") && strings.Contains(e.Reason, "172.16.0.5") {
			found = true
		}
	}
	if !found {
		t.Error("warn 放行应写 guard 审计(注明 RoE 与 host)")
	}
}

func TestRoEOffDisables(t *testing.T) {
	g := roeGuard("off", []ScopeRule{{Kind: "domain", Value: "target.com"}})
	if blocked, _ := bashBlocked(t, g, `curl https://evil.com/`); blocked {
		t.Error("off 模式不应拦截")
	}
}

func TestRoENoTaskIDAllows(t *testing.T) {
	g := roeGuard("strict", []ScopeRule{{Kind: "domain", Value: "target.com"}})
	g.SetRoE(RoEConfig{
		Mode:     func() string { return "strict" },
		Scope:    func(int64) []ScopeRule { return []ScopeRule{{Kind: "domain", Value: "target.com"}} },
		TaskIDOf: func(context.Context) int64 { return 0 }, // chat 等非任务运行
	})
	if blocked, _ := bashBlocked(t, g, `curl https://evil.com/`); blocked {
		t.Error("无 taskID 应放行")
	}
}

func TestRoEWebFetch(t *testing.T) {
	g := roeGuard("strict", []ScopeRule{{Kind: "domain", Value: "target.com"}})
	input, _ := json.Marshal(map[string]string{"url": "https://evil.com/x"})
	blocked, _, _ := g.Hooks().PreToolUse(context.Background(), "WebFetch", input)
	if !blocked {
		t.Error("WebFetch 的 URL host 同样受 RoE 检查")
	}
}
