package server

import (
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

// 没有任何配置时，解析结果必须是全零 —— 也就是 SDK 与 task_llm 各自的内置默认，
// 与「重试可配」这个特性上线之前逐字节一致。
func TestResolveRetryUnconfigured(t *testing.T) {
	got := resolveRetry(db.RetryOverride{}, db.LLMRetryPolicy{})
	if got != (agent.RetryConfig{}) {
		t.Fatalf("resolveRetry=%+v, want zero", got)
	}
	retries, backoff := sameProviderRetryPolicy(got)
	if retries != sameProviderStreamRetries {
		t.Fatalf("retries=%d, want %d", retries, sameProviderStreamRetries)
	}
	if d := backoff(1); d != time.Second {
		t.Fatalf("backoff(1)=%v, want 1s (the default ladder)", d)
	}
}

// The global policy applies to a profile that overrides nothing.
func TestResolveRetryFromGlobal(t *testing.T) {
	pol := db.LLMRetryPolicy{
		Connect: db.RetryRule{Attempts: 5, IntervalMS: 2000},
		Empty:   db.RetryRule{Attempts: -1},
		Stream:  db.RetryRule{Attempts: 4, IntervalMS: 1500},
	}
	got := resolveRetry(db.RetryOverride{}, pol)
	want := agent.RetryConfig{
		ConnectAttempts: 5, ConnectInterval: 2 * time.Second,
		EmptyAttempts:  -1,
		StreamAttempts: 4, StreamInterval: 1500 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("resolveRetry=%+v, want %+v", got, want)
	}
	retries, backoff := sameProviderRetryPolicy(got)
	if retries != 4 {
		t.Fatalf("retries=%d, want 4", retries)
	}
	for _, attempt := range []int{0, 3} {
		if d := backoff(attempt); d != 1500*time.Millisecond {
			t.Fatalf("backoff(%d)=%v, want a fixed 1.5s", attempt, d)
		}
	}
}

// A profile override wins field by field: the pinned count replaces the global
// one, while the interval it left alone still comes from the global policy.
func TestResolveRetryProfileOverridesPerField(t *testing.T) {
	pol := db.LLMRetryPolicy{
		Connect: db.RetryRule{Attempts: 5, IntervalMS: 2000},
		Stream:  db.RetryRule{Attempts: 4, IntervalMS: 1500},
	}
	over := db.RetryOverride{
		Connect: db.RetryRule{Attempts: 1},
		Stream:  db.RetryRule{IntervalMS: 250},
	}
	got := resolveRetry(over, pol)
	if got.ConnectAttempts != 1 || got.ConnectInterval != 2*time.Second {
		t.Fatalf("connect=%d/%v, want 1 attempt inheriting the 2s interval", got.ConnectAttempts, got.ConnectInterval)
	}
	if got.StreamAttempts != 4 || got.StreamInterval != 250*time.Millisecond {
		t.Fatalf("stream=%d/%v, want the global 4 attempts with a pinned 250ms", got.StreamAttempts, got.StreamInterval)
	}
}

// -1 turns the same-provider layer off entirely (0 retries), rather than being
// mistaken for "unset".
func TestSameProviderRetryDisabled(t *testing.T) {
	retries, _ := sameProviderRetryPolicy(agent.RetryConfig{StreamAttempts: -1})
	if retries != 0 {
		t.Fatalf("retries=%d, want 0 (disabled)", retries)
	}
}

// Values beyond the sane range are clamped on the way into the DB, so a hostile
// or fat-fingered payload can't park a worker for a day.
func TestRetryRuleClamp(t *testing.T) {
	pol := db.LLMRetryPolicy{
		Connect: db.RetryRule{Attempts: -50, IntervalMS: -1},
		Stream:  db.RetryRule{Attempts: 9999, IntervalMS: 99_999_999},
	}
	got := resolveRetry(db.RetryOverride{}, pol.Clamped())
	if got.ConnectAttempts != -1 || got.ConnectInterval != 0 {
		t.Fatalf("connect=%d/%v, want -1 attempts and no interval", got.ConnectAttempts, got.ConnectInterval)
	}
	if got.StreamAttempts != 20 || got.StreamInterval != time.Hour {
		t.Fatalf("stream=%d/%v, want the 20-attempt / 1h caps", got.StreamAttempts, got.StreamInterval)
	}
}
