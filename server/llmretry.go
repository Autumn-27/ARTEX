package server

import (
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 重试策略的服务端解析，见 docs/LLM重试设计.md。五层里：
//   - 建连 / 空响应 / 同 provider 安全窗口 是「跟着端点走」的，每个 LLM 配置可以覆盖
//     全局默认（profile 的某项留空就继承全局，全局也没配就用内置默认）；
//   - 熔断 / 意图重跑 是进程级的，只有全局一份。
//
// 全局策略读一次 DB 一行 settings，调用点都在低频路径（构建 provider、work 收尾、
// 保存配置），不值得再加一层缓存；熔断参数是例外——它在失败路径上每次都要读，所以
// 由 applyRetryPolicy 推给 Registry 保存。

// retryPolicy reads the global policy; a nil DB yields the zero policy (all
// layers on their built-in defaults).
func (s *Server) retryPolicy() db.LLMRetryPolicy {
	if s.m == nil || s.m.pg == nil {
		return db.LLMRetryPolicy{}
	}
	return s.m.pg.LLMRetryPolicy()
}

// resolveRetry layers one profile's override on top of the global policy and
// converts the result into the form agent.Config carries. Rules combine field by
// field, so a profile that only pins an interval still inherits the global count.
func resolveRetry(o db.RetryOverride, pol db.LLMRetryPolicy) agent.RetryConfig {
	connect := o.Connect.Or(pol.Connect)
	empty := o.Empty.Or(pol.Empty)
	stream := o.Stream.Or(pol.Stream)
	return agent.RetryConfig{
		// 次数在这里保持「0=默认 / 负=关闭」的原始语义:SDK 的 MaxRetries /
		// EmptyResponseRetries 与之完全同构,交给它自己解析即可。
		ConnectAttempts: connect.Attempts, ConnectInterval: connect.Interval(),
		EmptyAttempts: empty.Attempts, EmptyInterval: empty.Interval(),
		StreamAttempts: stream.Attempts, StreamInterval: stream.Interval(),
	}
}

// applyProfileRetry fills cfg.Retry for a profile read from the DB.
func (s *Server) applyProfileRetry(cfg *agent.Config, p *db.LLMProfile) {
	if p == nil {
		return
	}
	cfg.Retry = resolveRetry(p.Retry, s.retryPolicy())
}

// 熔断(轮询冷却)的默认值,与 llmpool 内置的一致 —— 这里只在「用户配了值」时才覆盖。
// 意图重跑的默认值见 engine.go 的 modelErrorRetries / modelErrorRetryBackoff。

// applyRetryPolicy pushes the process-wide layers of the policy into the objects
// that consume them on a hot path: the circuit-breaker registry. Called at
// startup and whenever the policy is saved.
func (s *Server) applyRetryPolicy() {
	pol := s.retryPolicy()
	if s.llmHealth != nil {
		s.llmHealth.SetPolicy(pol.Breaker.Attempts, pol.Breaker.Interval())
	}
}

// modelErrorRetryPolicy resolves the intent-level replay knobs (layer ⑤): how
// many times a model_error work is re-run and how long to back off between runs.
func (e *Engine) modelErrorRetryPolicy() (retries int, backoff time.Duration) {
	retries, backoff = modelErrorRetries, modelErrorRetryBackoff
	if e == nil || e.m == nil || e.m.pg == nil {
		return retries, backoff
	}
	rule := e.m.pg.LLMRetryPolicy().Intent
	if rule.Attempts != 0 {
		retries = max(rule.Attempts, 0)
	}
	if d := rule.Interval(); d > 0 {
		backoff = d
	}
	return retries, backoff
}

// emptyTurnNudgeLimit resolves how many empty-turn continuations one work may
// inject (see steerHooks.Stop). It deliberately reuses layer ②'s knob —— 「空响应
// 重试次数」:两者是同一件事的两种手段。SDK 那层管「一个内容块都没有」，手段是把
// 同一个请求原样重发;这里管「只有思考、既无正文也无工具」，手段是追加一条指令让
// 模型带着已有思考接着做(原样重发对这种由上下文形状决定的空转没有意义)。判空口径
// 不同是因为 SDK 以「有没有 yield 过事件」为准，而思考增量本身就是事件——但用户配
// 「空响应重试几次」时想表达的是「模型没产出实质内容就再来一次」，两层共用一个次数
// 才对得上这个心智。
//
// 读全局策略而不是某个 profile 的覆盖:一个 run 中途可能因故障转移换 profile，而这
// 是整条意图的总量闸，不该跟着换端点而变。语义与 SDK 的 emptyRetries() 同构:
// 0 = 默认 defaultEmptyTurnNudges;-1(负) = 关闭空转续跑;>0 = 用该值。
func (e *Engine) emptyTurnNudgeLimit() int {
	if e == nil || e.m == nil || e.m.pg == nil {
		return defaultEmptyTurnNudges
	}
	switch n := e.m.pg.LLMRetryPolicy().Empty.Attempts; {
	case n == 0:
		return defaultEmptyTurnNudges
	case n < 0:
		return 0
	default:
		return n
	}
}
