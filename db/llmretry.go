package db

import (
	"encoding/json"
	"time"
)

// LLM 重试策略：五层重试的「次数 + 间隔」全局配置，见 docs/LLM重试设计.md。
// 存在 settings 表的一个 JSON 值里 —— 它是整机一份的运行参数，不值得为它开一张表；
// 读取走内置默认兜底，所以键不存在(全新库/从未配置过)时行为与写死常量时代完全一致。

const settingLLMRetryPolicy = "llm_retry_policy"

// RetryRule is one layer's knob pair. The zero value means "unset":
//
//	Attempts   0 = 用内置默认次数; -1 = 关闭该层重试; >0 = 用该值
//	IntervalMS 0 = 用该层原本的间隔策略(通常是指数退避); >0 = 改用固定毫秒间隔
//
// -1 是「显式关掉」而不是「0 次」，因为 0 已经被「未配置」占用了。
type RetryRule struct {
	Attempts   int `json:"attempts"`
	IntervalMS int `json:"interval_ms"`
}

// Interval returns the configured fixed interval, or 0 when unset (caller keeps
// its own default ladder).
func (r RetryRule) Interval() time.Duration {
	if r.IntervalMS <= 0 {
		return 0
	}
	return time.Duration(r.IntervalMS) * time.Millisecond
}

// Or returns the rule with each unset field filled in from fallback. Used to
// layer a profile override on top of the global policy field by field, so a
// profile that only pins the interval still inherits the global count.
func (r RetryRule) Or(fallback RetryRule) RetryRule {
	if r.Attempts == 0 {
		r.Attempts = fallback.Attempts
	}
	if r.IntervalMS == 0 {
		r.IntervalMS = fallback.IntervalMS
	}
	return r
}

// retry knob bounds. A count above the cap turns a blip into a token bonfire;
// an interval above an hour outlives any transient failure worth waiting out.
const (
	maxRetryAttempts   = 20
	maxRetryIntervalMS = 3600_000 // 1h
)

// Clamped returns the rule with out-of-range values pulled back into the sane
// band (attempts within [-1, 20], interval within [0, 1h]).
func (r RetryRule) Clamped() RetryRule {
	if r.Attempts < -1 {
		r.Attempts = -1
	}
	if r.Attempts > maxRetryAttempts {
		r.Attempts = maxRetryAttempts
	}
	if r.IntervalMS < 0 {
		r.IntervalMS = 0
	}
	if r.IntervalMS > maxRetryIntervalMS {
		r.IntervalMS = maxRetryIntervalMS
	}
	return r
}

// Clamped bounds a profile's override the same way the global policy is bounded,
// so a hand-crafted API payload can't land a value the CHECK constraint rejects.
func (o RetryOverride) Clamped() RetryOverride {
	o.Connect, o.Empty, o.Stream = o.Connect.Clamped(), o.Empty.Clamped(), o.Stream.Clamped()
	return o
}

// LLMRetryPolicy holds the五层 retry configuration. Connect/Empty/Stream are the
// per-request layers (a profile may override them, see LLMProfile.Retry);
// Breaker and Intent are process-wide by nature and live only here.
type LLMRetryPolicy struct {
	// Connect：SDK 建连重试(连接重置/超时/429/5xx，流开始前)。默认 3 次、指数退避。
	Connect RetryRule `json:"connect"`
	// Empty：SDK 空响应重试(完成但无 content block，仅 openai 格式)。默认 2 次、指数退避。
	Empty RetryRule `json:"empty"`
	// Stream：同 provider 安全窗口重试(未交付输出前的断流重放)。默认 2 次、0.5s 起指数(封顶 4s)。
	Stream RetryRule `json:"stream"`
	// Breaker：轮询熔断。Attempts=连续几次瞬时失败触发熔断(默认 3，-1=瞬时失败不熔断，
	// 硬失败如余额不足/密钥失效仍立即熔断)；IntervalMS=固定冷却时长(0=默认 1/5/30min 梯度)。
	Breaker RetryRule `json:"breaker"`
	// Intent：worker 以 model_error 收场后的整条意图重跑。默认 2 次、固定 3s。
	Intent RetryRule `json:"intent"`
}

// Clamped returns the policy with every rule clamped.
func (p LLMRetryPolicy) Clamped() LLMRetryPolicy {
	p.Connect, p.Empty, p.Stream = p.Connect.Clamped(), p.Empty.Clamped(), p.Stream.Clamped()
	p.Breaker, p.Intent = p.Breaker.Clamped(), p.Intent.Clamped()
	return p
}

// LLMRetryPolicy reads the global retry policy. A missing or unparseable value
// yields the zero policy — i.e. every layer on its built-in default.
func (d *DB) LLMRetryPolicy() LLMRetryPolicy {
	var p LLMRetryPolicy
	if d == nil {
		return p
	}
	raw, ok, err := d.GetSetting(settingLLMRetryPolicy)
	if err != nil || !ok || raw == "" {
		return p
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LLMRetryPolicy{}
	}
	return p.Clamped()
}

// SetLLMRetryPolicy persists the global retry policy (values are clamped first).
func (d *DB) SetLLMRetryPolicy(p LLMRetryPolicy) error {
	raw, err := json.Marshal(p.Clamped())
	if err != nil {
		return err
	}
	return d.SetSetting(settingLLMRetryPolicy, string(raw))
}
