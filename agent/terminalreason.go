package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// This file turns a run's terminal state into something a human can act on.
//
// Before: a cancelled run produced one fixed sentence — "（运行被中断：任务暂停 /
// 终止 / 重启所致；未完成）" — a guess-list, not a diagnosis. The information to do
// better was already there (the cancel cause, the terminal reason, the turn
// count, the tool that never returned); it was just being thrown away.
//
// terminalText produces a self-contained ONE-LINE summary (the transcript's
// collapsed row, cut at the first newline by firstLine) plus a multi-line detail
// block rendered in full when the row is expanded.

// runTrace records which tool call is in flight, so an aborted / timed-out run can
// name the step it died on — the single most useful fact when a run hangs.
type runTrace struct {
	startedAt time.Time
	id        string // tool_use id of the most recent call
	name      string // its tool name
	input     string // its raw input JSON
	at        time.Time
	pending   bool // true = no tool_result arrived for it yet
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

// reasonHint explains each harness terminal reason in plain 中文: what it means,
// whether results were still written back, and what the engine does next. Covers
// every TerminalReason the harness can emit.
var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted: "模型主动结束了本轮（正常收场），但没有留下文字总结。事实/资产以工具调用为准，看上面的 tool_use 记录",
	harness.ReasonMaxTurns: "撞到步数上限(MaxTurns)：SDK 已自动跑了一轮收尾(settlement)把事实/资产写回，只是缺一段文字总结。" +
		"意图标记为 exhausted——表示「这个方向试过但没做完」，规划者会据此换角度，不是失败",
	harness.ReasonTimeout: "撞到单次运行的软墙钟预算(MaxDuration)：SDK 在回合边界优雅收尾并写回事实/资产，只是缺文字总结。" +
		"意图标记为 exhausted，不是失败",
	harness.ReasonModelError:    "模型/API 调用失败（网络、鉴权、限流、供应商 5xx 等）。引擎会退避后自动重跑几次；重试用尽仍失败则意图标记为 blocked",
	harness.ReasonBlockingLimit: "上下文长度撞硬上限，请求在发出前就被拦下。通常是单次运行塞进了过多工具输出——考虑收窄意图粒度或压缩工具返回",
	harness.ReasonPromptTooLong: "提示词过长且压缩(compaction)重试已用尽，无法继续。同样指向单次运行累积的上下文过大",
	harness.ReasonImageError:    "多模态内容不被当前模型支持（截图/图片块）。换支持视觉的模型，或让工具别回图",
	harness.ReasonStopHookPrevented: "Stop 钩子阻止了本轮结束（守卫判定还不能收场），且随后没能继续下去。" +
		"检查任务的 Guard 规则是否过严",
	harness.ReasonHookStopped:      "某个工具/钩子主动叫停了继续执行（守卫拦截，如越界目标、禁用命令）。看上面最后一条 tool_result 里的拦截说明",
	harness.ReasonAbortedStreaming: "运行在【模型输出流式生成阶段】被取消",
	harness.ReasonAbortedTools:     "运行在【工具执行阶段】被取消",
}

// terminalText renders a terminal event with no final text into (summary, detail).
// summary is one self-contained line; detail is the expandable breakdown.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools

	// ---- one-line summary: cause first (that's what the reader is after), then
	// just enough run shape to judge it without expanding. ----
	var sum string
	switch {
	case aborted:
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "取不到取消原因（run 的 context 上没有挂 cause）"
		}
		stage := "工具执行阶段"
		if reason == harness.ReasonAbortedStreaming {
			stage = "模型输出阶段"
		}
		sum = "（运行被中断：" + short + "；停在" + stage + progressSuffix(term, tr) + "，未完成）"
	case reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout:
		sum = "（达运行预算上限(" + string(reason) + ")，已收尾写回事实" + progressSuffix(term, tr) + "；本次无文字总结）"
	default:
		hint := reasonHint[reason]
		if hint == "" {
			hint = "未知终态（harness 新增了终态但这里还没登记，请补 reasonHint）"
		}
		sum = "（无文字总结，终态 " + string(reason) + "：" + firstLine(hint, 80) + "）"
	}

	// ---- expandable detail ----
	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "- **终态**: `%s` — %s\n", reason, reasonHint[reason])
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **中断原因** (`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **中断原因**: 取不到——取消方没有用 context.WithCancelCause 挂上具名原因。" +
				"这属于漏网的取消点，请补 agent/cancelcause.go 里的枚举\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **底层错误**: `%v`\n", term.Err)
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **已执行**: %d 轮模型回合\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **本次运行耗时**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **累计 token**: 输入 %d / 输出 %d / 缓存读 %d / 缓存写 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name != "" {
		if tr.pending {
			fmt.Fprintf(&b, "- **中断时正在执行的工具**: `%s`(已跑 %s，**未返回结果**) — 若是硬超时/长时间无进展，问题多半就在这一步\n  ```\n  %s\n  ```\n",
				tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
		} else {
			fmt.Fprintf(&b, "- **中断前最后一个工具**: `%s`(已正常返回)\n", tr.name)
		}
	} else {
		b.WriteString("- **工具调用**: 本次运行还没发出任何工具调用就结束了\n")
	}
	return sum, b.String()
}

// progressSuffix adds "已跑 N 轮 / 12m30s" to a summary line — how far the run got
// before it died, which decides whether an abort cost anything worth re-running.
func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d 轮", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "，已跑 " + strings.Join(parts, " / ")
}

// roundDur formats a duration at a readable granularity (no 3.400213s noise).
func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}
