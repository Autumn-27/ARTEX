package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/norma/harness"
	"github.com/Autumn-27/norma/llm"
)

// every TerminalReason the harness can emit must have an explanation — otherwise
// a run silently falls back to "未知终态", which is the bug this file exists to fix.
func TestReasonHintCoversEveryTerminalReason(t *testing.T) {
	all := []harness.TerminalReason{
		harness.ReasonCompleted, harness.ReasonBlockingLimit, harness.ReasonImageError,
		harness.ReasonModelError, harness.ReasonAbortedStreaming, harness.ReasonAbortedTools,
		harness.ReasonPromptTooLong, harness.ReasonStopHookPrevented, harness.ReasonHookStopped,
		harness.ReasonMaxTurns, harness.ReasonTimeout,
	}
	for _, r := range all {
		if strings.TrimSpace(reasonHint[r]) == "" {
			t.Errorf("terminal reason %q 没有中文说明", r)
		}
	}
}

// the cause attached at cancel time must survive the ctx chain the engine builds:
// task exec ctx -> per-work child -> value ctx -> the run's hard-timeout backstop.
func TestAbortCausePropagatesThroughRunContextChain(t *testing.T) {
	type key struct{}
	exec, cancelExec := context.WithCancelCause(context.Background())
	work, cancelWork := context.WithCancelCause(exec)
	defer cancelWork(nil)
	valued := context.WithValue(work, key{}, "task-1")
	run, cancelRun := context.WithTimeoutCause(valued, time.Hour, AbortRunHardTimeout)
	defer cancelRun()

	cancelExec(AbortPausedByUser)

	code, _, text, ok := AbortReason(run)
	if !ok || code != "paused_by_user" {
		t.Fatalf("code=%q ok=%v，期望 paused_by_user", code, ok)
	}
	if !strings.Contains(text, "退回 frontier") {
		t.Errorf("原因文案没传下来: %q", text)
	}
}

func TestAbortReasonFallbacks(t *testing.T) {
	if _, _, _, ok := AbortReason(context.Background()); ok {
		t.Error("未取消的 ctx 不应报出中断原因")
	}
	// a cancel site that forgot to attach a cause must be called out, not guessed at
	plain, cancel := context.WithCancel(context.Background())
	cancel()
	code, _, _, ok := AbortReason(plain)
	if !ok || code != "canceled_no_cause" {
		t.Errorf("code=%q ok=%v，期望 canceled_no_cause", code, ok)
	}

	timed, cancelT := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelT()
	<-timed.Done()
	if code, _, _, _ := AbortReason(timed); code != "deadline_exceeded" {
		t.Errorf("code=%q，期望 deadline_exceeded", code)
	}
}

// an aborted run's one-line summary must name the actual cause, and the detail
// must pin down the tool that never came back.
func TestTerminalTextAbortedNamesCauseAndHangingTool(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortKilledByPlanner)

	tr := &runTrace{startedAt: time.Now().Add(-90 * time.Second)}
	tr.start("tu_1", "Bash", `{"command":"nmap -p- 10.0.0.1"}`)

	term := &harness.Terminal{
		Reason: harness.ReasonAbortedTools,
		Err:    context.Canceled,
		Turns:  7,
		Usage:  llm.Usage{InputTokens: 1200, OutputTokens: 340},
	}
	sum, detail := terminalText(ctx, term, tr)

	if !strings.Contains(sum, "kill_work") {
		t.Errorf("摘要没点出取消方: %q", sum)
	}
	if strings.Contains(sum, "\n") {
		t.Errorf("摘要必须是单行（前端按首行截断）: %q", sum)
	}
	for _, want := range []string{"killed_by_planner", "aborted_tools", "7 轮", "Bash", "未返回结果", "1200"} {
		if !strings.Contains(detail, want) {
			t.Errorf("详情缺少 %q:\n%s", want, detail)
		}
	}
}

// a tool that DID return must not be reported as the hang site.
func TestTerminalTextCompletedToolNotBlamed(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(AbortShutdown)
	tr := &runTrace{startedAt: time.Now()}
	tr.start("tu_1", "Read", `{"path":"/etc/hosts"}`)
	tr.done("tu_1")

	_, detail := terminalText(ctx, &harness.Terminal{Reason: harness.ReasonAbortedStreaming}, tr)
	if strings.Contains(detail, "未返回结果") {
		t.Errorf("已返回的工具被误报为卡住:\n%s", detail)
	}
	if !strings.Contains(detail, "已正常返回") {
		t.Errorf("详情没记录最后一个工具:\n%s", detail)
	}
}

// non-abort terminals keep their own explanation instead of the interruption text.
func TestTerminalTextNonAbortReasons(t *testing.T) {
	ctx := context.Background()
	tr := &runTrace{startedAt: time.Now()}

	sum, _ := terminalText(ctx, &harness.Terminal{Reason: harness.ReasonMaxTurns}, tr)
	if !strings.Contains(sum, "达运行预算上限") || strings.Contains(sum, "中断") {
		t.Errorf("max_turns 摘要不对: %q", sum)
	}

	sum, detail := terminalText(ctx, &harness.Terminal{
		Reason: harness.ReasonModelError, Err: errors.New("429 rate limited")}, tr)
	if !strings.Contains(sum, "model_error") {
		t.Errorf("model_error 摘要不对: %q", sum)
	}
	if !strings.Contains(detail, "429 rate limited") {
		t.Errorf("详情没带上底层错误:\n%s", detail)
	}
}

// every enumerated cause must carry both a compact headline (for the collapsed
// row) and a full explanation (for the expanded detail).
func TestAbortCausesAreWellFormed(t *testing.T) {
	all := []*AbortCause{
		AbortPausedByUser, AbortPausedByOrchestrator, AbortTaskDeleted, AbortPausedOnReload,
		AbortGoalMet, AbortSettleDrainTimeout, AbortKilledByPlanner, AbortWorkFinished,
		AbortPausedRaceGuard, AbortChatStoppedByUser, AbortChatTurnFinished,
		AbortShutdown, AbortRunHardTimeout,
	}
	seen := map[string]bool{}
	for _, c := range all {
		switch {
		case c.Code == "" || seen[c.Code]:
			t.Errorf("code 缺失或重复: %q", c.Code)
		case c.Short == "" || utf8.RuneCountInString(c.Short) > 40:
			t.Errorf("%s: Short 必须存在且 ≤40 字（进折叠摘要行）: %q", c.Code, c.Short)
		case utf8.RuneCountInString(c.Text) <= utf8.RuneCountInString(c.Short):
			t.Errorf("%s: Text 应比 Short 更完整", c.Code)
		}
		seen[c.Code] = true
	}
}

// summaries are mostly 中文; capping by bytes used to slice a rune in half.
func TestFirstLineCapsByRuneNotByte(t *testing.T) {
	got := firstLine(strings.Repeat("恢复", 10), 5)
	if got != "恢复恢复恢…" {
		t.Errorf("firstLine 按字节截断了: %q", got)
	}
	if got := firstLine("头\n尾", 100); got != "头" {
		t.Errorf("多行没截到首行: %q", got)
	}
}
