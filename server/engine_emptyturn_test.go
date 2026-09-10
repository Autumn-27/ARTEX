package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 空转回合(仅思考、无正文无工具)的识别与续跑，见 steerHooks.Stop。

func assistantThinking(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: text, Signature: "sig"},
	}}
}

func TestIsThinkingOnlyTurn(t *testing.T) {
	toolUse := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
		{Type: llm.BlockThinking, Thinking: "先扫端口"},
		{Type: llm.BlockToolUse, ID: "t1", Name: "run_nuclei"},
	}}
	cases := []struct {
		name string
		msgs []llm.Message
		want bool
	}{
		{"仅思考", []llm.Message{llm.UserText("开始"), assistantThinking("想想")}, true},
		{"思考+工具", []llm.Message{llm.UserText("开始"), toolUse}, false},
		{"思考+正文", []llm.Message{assistantThinking("想想"), {
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("结论")},
		}}, false},
		{"正文只有空白字符", []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.BlockThinking, Thinking: "x"}, llm.TextBlock("  \n ")},
		}}, true},
		{"完全空的 assistant 回合", []llm.Message{{Role: llm.RoleAssistant}}, true},
		// 工具结果是 user 角色，判定必须回溯到它前面那条 assistant，而不是就近误判。
		{"最后一条是工具结果", []llm.Message{toolUse, {
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.BlockToolResult, ToolUseID: "t1"}},
		}}, false},
		{"没有 assistant 消息", []llm.Message{llm.UserText("开始")}, false},
		{"空历史", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isThinkingOnlyTurn(c.msgs); got != c.want {
				t.Fatalf("isThinkingOnlyTurn = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeHooks 是一个可编程的 inner HookRunner，用来验证 steerHooks 对 inner 决定的尊重。
type fakeHooks struct {
	prevent  bool
	blocking []string
	msg      string
}

func (f fakeHooks) PreToolUse(context.Context, string, []byte) (bool, string, []byte) {
	return false, "", nil
}
func (f fakeHooks) PostToolUse(context.Context, string, []byte, []byte, bool) {}
func (f fakeHooks) Stop(context.Context, []llm.Message) (bool, []string, string) {
	return f.prevent, f.blocking, f.msg
}

func TestSteerHooksStopNudgesEmptyTurn(t *testing.T) {
	empty := []llm.Message{assistantThinking("我应该先枚举子域名")}

	t.Run("空转回合注入续跑指令", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges, label: "worker-1 · #1"}
		prevent, blocking, _ := h.Stop(context.Background(), empty)
		if prevent {
			t.Fatal("空转回合不应硬停")
		}
		if len(blocking) != 1 || blocking[0] != emptyTurnNudge {
			t.Fatalf("blocking = %v, want [emptyTurnNudge]", blocking)
		}
	})

	t.Run("有正文或工具时不介入", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		normal := []llm.Message{{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{llm.TextBlock("已完成扫描，未发现开放端口")},
		}}
		if _, blocking, _ := h.Stop(context.Background(), normal); blocking != nil {
			t.Fatalf("正常收场被误判为空转: %v", blocking)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("未介入时不应计数, got %d", n)
		}
	})

	t.Run("达到上限后放行收场", func(t *testing.T) {
		const limit = 5 // 用户把「空响应重试次数」配成 5
		h := steerHooks{nudges: &atomic.Int64{}, limit: limit}
		for i := 1; i <= limit; i++ {
			if _, blocking, _ := h.Stop(context.Background(), empty); len(blocking) != 1 {
				t.Fatalf("第 %d 次应仍在配额内, blocking = %v", i, blocking)
			}
		}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("超出上限仍在注入: %v", blocking)
		}
	})

	// 「空响应重试次数」配 -1 = 关掉这层，emptyTurnNudgeLimit 解析成 0。
	t.Run("配置关闭时不介入", func(t *testing.T) {
		h := steerHooks{nudges: &atomic.Int64{}, limit: 0}
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("已关闭仍在注入: %v", blocking)
		}
	})

	t.Run("inner 决定硬停时不叠加", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{prevent: true, msg: "guard 拒绝收场"}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		prevent, blocking, msg := h.Stop(context.Background(), empty)
		if !prevent || msg != "guard 拒绝收场" || blocking != nil {
			t.Fatalf("inner 的硬停被改写: prevent=%v blocking=%v msg=%q", prevent, blocking, msg)
		}
		if n := h.nudges.Load(); n != 0 {
			t.Fatalf("让位给 inner 时不应消耗配额, got %d", n)
		}
	})

	t.Run("inner 已要续跑时不叠加", func(t *testing.T) {
		h := steerHooks{inner: fakeHooks{blocking: []string{"guard 的续跑理由"}}, nudges: &atomic.Int64{}, limit: defaultEmptyTurnNudges}
		_, blocking, _ := h.Stop(context.Background(), empty)
		if len(blocking) != 1 || blocking[0] != "guard 的续跑理由" {
			t.Fatalf("inner 的续跑消息被改写: %v", blocking)
		}
	})

	t.Run("未装计数器时行为不变", func(t *testing.T) {
		h := steerHooks{limit: defaultEmptyTurnNudges} // 例如未来其他调用点忘了传 nudges
		if _, blocking, _ := h.Stop(context.Background(), empty); blocking != nil {
			t.Fatalf("无计数器时不应注入: %v", blocking)
		}
	})
}
