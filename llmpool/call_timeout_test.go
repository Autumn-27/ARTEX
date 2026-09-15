package llmpool

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// 归类纯函数:墙钟超时(调用方 ctx 存活 + DeadlineExceeded)归「可转移的瞬时
// 失败」;主动取消 / 上层截止(调用方 ctx 已结束)仍永不转移。
func TestShouldFailoverCallTimeout(t *testing.T) {
	// 下层派生 ctx 的墙钟到点:agent 包 ARTEX_LLM_CALL_TIMEOUT 归一化出的
	// call_timeout(Unwrap DeadlineExceeded),或 SDK 内部 attempt 超时。
	wrapped := fmt.Errorf("llm call_timeout: anthropic/m 整体墙钟超时: %w", context.DeadlineExceeded)
	if !shouldFailover(context.Background(), context.DeadlineExceeded) {
		t.Error("调用方存活 + DeadlineExceeded 应判定为可转移瞬时失败(call_timeout)")
	}
	if !shouldFailover(context.Background(), wrapped) {
		t.Error("归一化后的 call_timeout 错误应可转移")
	}
	// 调用方 ctx 存活但错误是 Canceled:不转移。
	if shouldFailover(context.Background(), context.Canceled) {
		t.Error("Canceled 永不转移")
	}
	// 用户主动取消:ctx 已结束,即使错误是 DeadlineExceeded 也不转移。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldFailover(ctx, context.DeadlineExceeded) {
		t.Error("主动取消后 DeadlineExceeded 不应转移")
	}
	if shouldFailover(ctx, context.Canceled) {
		t.Error("主动取消不应转移")
	}
	// 任务级截止(上层墙钟):ctx 自己到期,同样永不转移。
	dctx, dcancel := context.WithTimeout(context.Background(), 0)
	defer dcancel()
	if shouldFailover(dctx, context.DeadlineExceeded) {
		t.Error("上层截止(ctx 到期)不应转移")
	}
	// call_timeout 不是 hard failure:无状态码,走软熔断计数。
	if isHardFailure(wrapped) {
		t.Error("call_timeout 应归瞬时(软熔断),不是 hard failure")
	}
	if statusOf(wrapped) != 0 {
		t.Error("call_timeout 错误文本不应被解析出 HTTP 状态码")
	}
}

// 墙钟挂死的 member 在安全窗口内(未交付任何事件)失败 → 转移到下一个,
// 且按瞬时失败计软熔断(不立即开路)。
func TestFailoverOnCallTimeout(t *testing.T) {
	a := &fakeProv{name: "a", errs: []error{context.DeadlineExceeded}}
	b := okProv("b", "hello")
	reg := NewRegistry(nil, nil)
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, reg)

	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if err != nil {
		t.Fatalf("call_timeout 应触发故障转移并成功, got %v", err)
	}
	if got != "hello" {
		t.Fatalf("text=%q, want hello", got)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls: a=%d b=%d, want 1/1", a.calls, b.calls)
	}
	st := reg.Get(1)
	if st.Fails != 1 {
		t.Fatalf("call_timeout 应计入瞬时失败计数, Fails=%d, want 1", st.Fails)
	}
	if st.Open() {
		t.Fatal("单次 call_timeout 是瞬时失败,不应立即熔断(软熔断需连续多次)")
	}
}

// 安全窗口语义对 call_timeout 同样成立:已交付输出后墙钟到点,错误原样上抛,
// 不重放到别的 profile(否则重复模型输出)。
func TestNoFailoverOnCallTimeoutAfterEmit(t *testing.T) {
	a := &fakeProv{
		name:   "a",
		events: [][]llm.StreamEvent{{{Type: llm.SETextDelta, Text: "partial"}}},
		errs:   []error{context.DeadlineExceeded},
	}
	b := okProv("b", "full")
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))

	got, err := drain(p.Stream(context.Background(), llm.CompletionRequest{}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want DeadlineExceeded 原样上抛", err)
	}
	if got != "partial" {
		t.Fatalf("text=%q, want 已交付的 partial", got)
	}
	if b.calls != 0 {
		t.Fatalf("交付输出后不应因 call_timeout 转移(backup calls=%d)", b.calls)
	}
}

// Complete 路径:墙钟失败同样转移(非流式调用原子,无重复输出风险)。
func TestCompleteFailoverOnCallTimeout(t *testing.T) {
	a := &fakeProv{name: "a", errs: []error{context.DeadlineExceeded}}
	b := okProv("b", "done")
	p := New([]*Member{member(1, "a", 10, a), member(2, "b", 5, b)}, NewRegistry(nil, nil))

	msg, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete 应在 call_timeout 后转移成功, got %v", err)
	}
	if msg.Text() != "done" {
		t.Fatalf("text=%q, want done", msg.Text())
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("calls: a=%d b=%d, want 1/1", a.calls, b.calls)
	}
}
