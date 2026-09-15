package agent

import (
	"context"
	"fmt"
	"iter"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/norma/llm"
)

// 整体墙钟(call-level hard wall clock)。
//
// 实战出现过 LLM 连接挂死数小时:TCP 没断、SSE 没帧,SDK 的建连重试和 per-attempt
// HTTP timeout 都管不到「流已经开始但永远不再来数据」这种半开状态,worker/planner/
// 收尾时序被整体冻住。本层在 provider 组装处(Config.NewProvider)给每一次
// Stream/Complete 调用套一个 context.WithTimeout:从发起计时,覆盖整个流式生命周期;
// 到点 cancel,错误归一化为 *CallTimeoutError(call_timeout 类,语义 = 可重试的瞬时
// 失败,参照 429→402 归一化模式),供 llmpool 走故障转移。
//
// 与已有的 per-attempt HTTP timeout 不冲突:那是单次 HTTP 尝试的保险,本层是单次
// 逻辑调用(含 SDK 内部重试、流式全程)的更上层保险。
//
// 配置:env ARTEX_LLM_CALL_TIMEOUT,单位秒,默认 600,0 = 不限(不包这层)。
const defaultCallTimeoutSeconds = 600

// callTimeoutFromEnv 解析 ARTEX_LLM_CALL_TIMEOUT(秒)。未设/非法 → 默认 600s;
// 0 → 0(不限)。负值视为非法,回落默认。每次构建 provider 时读一次(非热路径),
// 因此改 env 后新建/重建的 provider 即生效。
func callTimeoutFromEnv() time.Duration {
	s := strings.TrimSpace(os.Getenv("ARTEX_LLM_CALL_TIMEOUT"))
	if s == "" {
		return defaultCallTimeoutSeconds * time.Second
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		log.Printf("[llm-call] WARN ARTEX_LLM_CALL_TIMEOUT=%q 非法(应为非负整数秒),按默认 %ds 处理", s, defaultCallTimeoutSeconds)
		return defaultCallTimeoutSeconds * time.Second
	}
	return time.Duration(n) * time.Second
}

// CallTimeoutError 是整体墙钟到点后的归一化错误(call_timeout 类)。语义:本层判定
// 该次调用挂死,属可重试的瞬时失败——llmpool 把它当传输级瞬时故障走故障转移(软熔断
// 计数,非 hard failure)。Unwrap 返回 context.DeadlineExceeded,因此
// errors.Is(err, context.DeadlineExceeded) 成立;与「调用方主动取消/上层截止」的
// 区分不在错误本身,而在调用方 ctx 是否也已结束(见 normalizeDeadline 与
// llmpool.shouldFailover)。
type CallTimeoutError struct {
	Provider string        // 提供方格式名(anthropic/openai/…)
	Model    string        // 模型 id
	Timeout  time.Duration // 墙钟上限
	Elapsed  time.Duration // 触发时已耗时(≈ Timeout,保留实测值便于核对)
}

func (e *CallTimeoutError) Error() string {
	return fmt.Sprintf("llm call_timeout: %s/%s 整体墙钟超时(已耗时 %s,上限 %s),判定连接挂死,按瞬时失败处理",
		e.Provider, e.Model, e.Elapsed.Round(time.Millisecond), e.Timeout)
}

// Unwrap 暴露 context.DeadlineExceeded:下游所有 errors.Is 截止判定继续成立。
func (e *CallTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// normalizeDeadline 是超时归类的纯函数,区分三种情况:
//
//   - 调用方 ctx 已结束(用户停止 / 任务级截止 / 上层墙钟):原样透传。主动取消永不
//     归一化、永不转移——这是 llmpool 的既有语义,不能因本层存在而被破坏。
//   - 调用方 ctx 存活而派生 callCtx 已结束:只有本层计时器会取消 callCtx,即墙钟到点,
//     归一化为 *CallTimeoutError。
//   - 两个 ctx 都存活:普通错误(含 SDK 内部 per-attempt 超时),原样透传。
func normalizeDeadline(ctx, callCtx context.Context, err error, provider, model string, timeout time.Duration, start time.Time) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || callCtx.Err() == nil {
		return err
	}
	return &CallTimeoutError{Provider: provider, Model: model, Timeout: timeout, Elapsed: time.Since(start)}
}

// callTimeoutProvider 在单次调用外包一层整体墙钟。包在 recorder(llmrec.Wrap)之下,
// 因此归一化后的错误会带 status=error 与真实耗时落进 llm_usage。
type callTimeoutProvider struct {
	inner    llm.Provider
	timeout  time.Duration
	provider string
	model    string
}

// wrapCallTimeout 按 env 配置给 inner 套整体墙钟;0(不限)或 nil inner 时原样返回。
func wrapCallTimeout(inner llm.Provider, c Config) llm.Provider {
	if inner == nil {
		return nil
	}
	timeout := callTimeoutFromEnv()
	if timeout <= 0 {
		return inner
	}
	return &callTimeoutProvider{inner: inner, timeout: timeout, provider: c.Provider(), model: c.Model}
}

func (p *callTimeoutProvider) warnTimeout(elapsed time.Duration) {
	// 注意:provider 组装层拿不到 profile 名(Config 里没有);profile 名由外层
	// llmpool 故障转移日志与 llm_usage 记录(recorder 按 profile 名打标)补全。
	log.Printf("[llm-call] WARN 整体墙钟超时(call_timeout):provider=%s model=%s 已耗时=%s 上限=%s — 判定连接挂死,归一化为瞬时失败交上层重试/故障转移",
		p.provider, p.model, elapsed.Round(time.Millisecond), p.timeout)
}

// Stream 从发起计时,覆盖整个流式生命周期;墙钟到点即 cancel 并把错误归一化为
// call_timeout。调用方中途停止消费(yield=false)时 defer 的 cancel 会立刻断掉
// 底层流,不留半开连接。
func (p *callTimeoutProvider) Stream(ctx context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		start := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, p.timeout)
		defer cancel()
		for ev, err := range p.inner.Stream(callCtx, req) {
			if err != nil {
				if nerr := normalizeDeadline(ctx, callCtx, err, p.provider, p.model, p.timeout, start); nerr != err {
					p.warnTimeout(time.Since(start))
					yield(llm.StreamEvent{}, nerr)
					return
				}
			}
			if !yield(ev, err) {
				return
			}
			if err != nil {
				return
			}
		}
	}
}

// Complete 与 Stream 同语义:非流式调用是原子的,墙钟到点直接归一化返回。
func (p *callTimeoutProvider) Complete(ctx context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	start := time.Now()
	callCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	msg, stop, usage, err := p.inner.Complete(callCtx, req)
	if err != nil {
		if nerr := normalizeDeadline(ctx, callCtx, err, p.provider, p.model, p.timeout, start); nerr != err {
			p.warnTimeout(time.Since(start))
			return llm.Message{}, "", llm.Usage{}, nerr
		}
	}
	return msg, stop, usage, err
}
