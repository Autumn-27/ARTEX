package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/norma/llm"
)

func TestCallTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		set  bool
		want time.Duration
	}{
		{set: false, want: 600 * time.Second},   // 未设 → 默认 600s
		{env: "0", set: true, want: 0},          // 0 = 不限
		{env: "30", set: true, want: 30 * time.Second},
		{env: " 120 ", set: true, want: 120 * time.Second},
		{env: "abc", set: true, want: 600 * time.Second}, // 非法 → 默认
		{env: "-5", set: true, want: 600 * time.Second},  // 负值 → 默认
	}
	for _, c := range cases {
		if c.set {
			t.Setenv("ARTEX_LLM_CALL_TIMEOUT", c.env)
		} else {
			t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "")
			// 空串与未设同义(TrimSpace 后为空 → 默认)
		}
		if got := callTimeoutFromEnv(); got != c.want {
			t.Errorf("env=%q set=%v: callTimeoutFromEnv()=%v, want %v", c.env, c.set, got, c.want)
		}
	}
}

// 归类纯函数:主动取消(调用方 ctx 结束)绝不归一化;调用方存活而派生 ctx 到期
// 才是本层墙钟,归 call_timeout。
func TestNormalizeDeadlineClassification(t *testing.T) {
	start := time.Now()
	constTO := 600 * time.Second

	t.Run("用户主动取消→透传", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		callCtx, callCancel := context.WithTimeout(ctx, constTO)
		defer callCancel()
		cancel() // 用户停止:父 ctx 先结束,派生 ctx 跟着结束
		err := normalizeDeadline(ctx, callCtx, context.Canceled, "anthropic", "m", constTO, start)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v, want context.Canceled 原样透传", err)
		}
		var cto *CallTimeoutError
		if errors.As(err, &cto) {
			t.Fatal("主动取消被错误归一化为 call_timeout")
		}
	})

	t.Run("上层任务级截止→透传", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		callCtx, callCancel := context.WithTimeout(ctx, constTO)
		defer callCancel()
		time.Sleep(5 * time.Millisecond) // 让父 ctx 到期
		err := normalizeDeadline(ctx, callCtx, context.DeadlineExceeded, "anthropic", "m", constTO, start)
		var cto *CallTimeoutError
		if errors.As(err, &cto) {
			t.Fatal("上层截止被错误归一化为 call_timeout(会导致对主动停止故障转移)")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err=%v, want DeadlineExceeded 透传", err)
		}
	})

	t.Run("本层墙钟到点→call_timeout", func(t *testing.T) {
		ctx := context.Background() // 调用方存活
		callCtx, callCancel := context.WithTimeout(ctx, time.Millisecond)
		defer callCancel()
		time.Sleep(5 * time.Millisecond) // 只有派生计时器能取消 callCtx
		err := normalizeDeadline(ctx, callCtx, context.DeadlineExceeded, "openai", "gpt-x", constTO, start)
		var cto *CallTimeoutError
		if !errors.As(err, &cto) {
			t.Fatalf("err=%v, want *CallTimeoutError", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("CallTimeoutError 必须 Unwrap 出 DeadlineExceeded 供下游 errors.Is")
		}
		if cto.Provider != "openai" || cto.Model != "gpt-x" || cto.Timeout != constTO {
			t.Fatalf("CallTimeoutError 字段不符: %+v", cto)
		}
	})

	t.Run("普通错误→透传", func(t *testing.T) {
		ctx := context.Background()
		callCtx, callCancel := context.WithTimeout(ctx, constTO)
		defer callCancel()
		orig := errors.New("dial tcp: connection reset by peer")
		if got := normalizeDeadline(ctx, callCtx, orig, "anthropic", "m", constTO, start); got != orig {
			t.Fatalf("普通传输错误应原样透传, got %v", got)
		}
	})

	t.Run("nil→nil", func(t *testing.T) {
		callCtx, callCancel := context.WithTimeout(context.Background(), constTO)
		defer callCancel()
		if got := normalizeDeadline(context.Background(), callCtx, nil, "anthropic", "m", constTO, start); got != nil {
			t.Fatalf("nil err 应透传 nil, got %v", got)
		}
	})
}

// hangProv 模拟半开挂死的 provider:Stream/Complete 都阻塞到 ctx 结束,然后把
// ctx.Err() 当错误抛出。墙钟到点或用户取消都能唤醒它,正好区分两种路径。
type hangProv struct{}

func (hangProv) Stream(ctx context.Context, _ llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		<-ctx.Done()
		yield(llm.StreamEvent{}, ctx.Err())
	}
}

func (hangProv) Complete(ctx context.Context, _ llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	<-ctx.Done()
	return llm.Message{}, "", llm.Usage{}, ctx.Err()
}

func TestStreamWallClockNormalizes(t *testing.T) {
	p := &callTimeoutProvider{inner: hangProv{}, timeout: 30 * time.Millisecond, provider: "anthropic", model: "claude-x"}
	start := time.Now()
	var gotErr error
	for _, err := range p.Stream(context.Background(), llm.CompletionRequest{}) {
		gotErr = err
	}
	elapsed := time.Since(start)
	var cto *CallTimeoutError
	if !errors.As(gotErr, &cto) {
		t.Fatalf("err=%v, want *CallTimeoutError", gotErr)
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatal("call_timeout 必须保留 DeadlineExceeded 语义")
	}
	if elapsed < 30*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("墙钟应在 ~30ms 触发,实际耗时 %v", elapsed)
	}
}

func TestCompleteWallClockNormalizes(t *testing.T) {
	p := &callTimeoutProvider{inner: hangProv{}, timeout: 30 * time.Millisecond, provider: "openai", model: "gpt-x"}
	_, _, _, err := p.Complete(context.Background(), llm.CompletionRequest{})
	var cto *CallTimeoutError
	if !errors.As(err, &cto) {
		t.Fatalf("err=%v, want *CallTimeoutError", err)
	}
}

// 用户主动取消:即使包着墙钟,错误也必须原样是 Canceled,绝不归一化(否则
// llmpool 会对「用户停止」烧备份 key)。
func TestUserCancelNotNormalized(t *testing.T) {
	p := &callTimeoutProvider{inner: hangProv{}, timeout: 10 * time.Second, provider: "anthropic", model: "claude-x"}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	var gotErr error
	for _, err := range p.Stream(ctx, llm.CompletionRequest{}) {
		gotErr = err
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", gotErr)
	}
	var cto *CallTimeoutError
	if errors.As(gotErr, &cto) {
		t.Fatal("主动取消被归一化为 call_timeout — 会破坏「取消永不转移」语义")
	}
}

// 流中途墙钟到点(已交付过事件)同样归一化;是否转移由 llmpool 的安全窗口决定,
// 本层只负责「到点必断 + 错误归类」。反向用例:正常快速完成的流不受墙钟影响。
func TestStreamNormalFlowUnaffected(t *testing.T) {
	inner := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		if !yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "partial"}, nil) {
			return
		}
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}}
	p := &callTimeoutProvider{inner: inner, timeout: 30 * time.Second, provider: "anthropic", model: "m"}
	var text string
	var gotErr error
	for ev, err := range p.Stream(context.Background(), llm.CompletionRequest{}) {
		if err != nil {
			gotErr = err
			continue
		}
		if ev.Type == llm.SETextDelta {
			text += ev.Text
		}
	}
	if gotErr != nil {
		t.Fatalf("正常快速完成的流不应触发墙钟: %v", gotErr)
	}
	if text != "partial" {
		t.Fatalf("text=%q, want partial(墙钟不影响正常流)", text)
	}
}

func TestWrapCallTimeout(t *testing.T) {
	inner := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEvent{Type: llm.SEMessageStop}, nil)
	}}
	if got := wrapCallTimeout(nil, Config{}); got != nil {
		t.Fatal("nil inner 应返回 nil")
	}
	// env=0 → 不限,原样返回(不包墙钟)。captureUsageProvider 含 func 字段不可
	// 比较,用类型断言验证「没有被包成 *callTimeoutProvider」。
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "0")
	if got := wrapCallTimeout(inner, Config{Model: "m"}); got == nil {
		t.Fatal("env=0 不应返回 nil")
	} else if _, wrapped := got.(*callTimeoutProvider); wrapped {
		t.Fatal("ARTEX_LLM_CALL_TIMEOUT=0 应原样返回 inner,不包墙钟")
	}
	// env 设值 → 包一层,字段取自 Config
	t.Setenv("ARTEX_LLM_CALL_TIMEOUT", "5")
	got := wrapCallTimeout(inner, Config{Format: llm.FormatAnthropic, Model: "m"})
	ctp, ok := got.(*callTimeoutProvider)
	if !ok {
		t.Fatalf("env=5 应返回 *callTimeoutProvider, got %T", got)
	}
	if ctp.timeout != 5*time.Second || ctp.provider != "anthropic" || ctp.model != "m" {
		t.Fatalf("wrap 字段不符: %+v", ctp)
	}
}

// 防回归:归一化错误的文本不能携带 "status NNN" 形态,否则 llmpool 会按 HTTP
// 状态码误归类(call_timeout 应保持「无状态码的传输级瞬时失败」)。
func TestCallTimeoutErrorTextHasNoStatus(t *testing.T) {
	err := &CallTimeoutError{Provider: "anthropic", Model: "m", Timeout: 600 * time.Second, Elapsed: 600 * time.Second}
	got := err.Error()
	for _, want := range []string{"call_timeout", "anthropic", "m", "10m0s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("错误文本缺 %q: %q", want, got)
		}
	}
	if strings.Contains(got, "status ") {
		t.Fatalf("错误文本不应含 HTTP 状态码形态: %q", got)
	}
}
