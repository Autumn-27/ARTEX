package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names WHY a planner/worker run's context was cancelled.
//
// Background: when a run's ctx is cancelled mid-flight the harness finishes with
// ReasonAbortedStreaming / ReasonAbortedTools and Terminal.Err = context.Canceled.
// ARTEX cancels that ctx from a dozen different places (pause, delete, kill_work,
// goal-met, settle drain backstop, chat stop, process shutdown, hard run timeout,
// …) but by the time it reaches captureRun they all collapse into one bare
// context.Canceled — so the trace could only print a guess-list, not a diagnosis.
//
// Fix: every cancel site uses context.WithCancelCause / WithTimeoutCause and
// attaches one of the causes below; captureRun recovers it via context.Cause and
// prints exactly who cancelled the run, why, and what happens to the intent.
//
// When you add a new cancel site, add its cause HERE — a site that cancels
// without one shows up in the trace as "canceled_no_cause", by design.
type AbortCause struct {
	Code  string // stable machine-readable id (logs / future filtering)
	Short string // 一句话，进折叠摘要行
	Text  string // 完整说明：谁取消的、为什么、这条意图接下来会怎样
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds an ad-hoc cause with runtime detail (e.g. which intent, which
// deadline). Use the predeclared vars below when there is nothing dynamic to add.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

// The full enumeration of cancel sites, grouped by which context gets cancelled.
var (
	// ---- 任务级 exec ctx（Engine.cancelExec：取消该任务在跑的 planner + 全部 worker）----

	AbortPausedByUser = cause("paused_by_user", "用户暂停了任务",
		"用户暂停了任务（前端「暂停」按钮 / POST /api/tasks/{id}/state action=pause）。本次运行被主动取消，该意图已退回 frontier(open)，恢复任务后会被重新领取、从头重跑")

	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "编排 agent 调用 pause_task 暂停了任务",
		"编排 agent 调用了 pause_task 工具暂停本任务。本次运行被主动取消，该意图已退回 frontier(open)，恢复后重跑")

	AbortTaskDeleted = cause("task_deleted", "任务被删除",
		"任务被删除（DELETE /api/tasks/{id}）——删除前先取消在跑的 planner/worker。本次运行的结果不会再被使用")

	AbortPausedOnReload = cause("paused_on_reload", "后端启动时恢复了「已暂停」状态",
		"后端启动时按库里持久化的「已暂停」状态恢复暂停。本次运行被取消（正常情况下此时并没有在跑的 run）")

	AbortGoalMet = cause("goal_met", "规划者判定目标已达成，任务落终态 done",
		"规划者判定任务目标已达成、任务落终态 done，随即取消所有在跑的 worker——它们手头的意图跑完也没意义了。该意图标记为 stopped，不是失败")

	AbortSettleDrainTimeout = cause("settle_drain_timeout", "任务级超时收尾：drain 宽限用尽后强制取消",
		"任务级超时收尾的硬兜底：到达任务 timeout 后等在跑 worker 优雅收尾，超过 90s drain 宽限仍未结束 → 强制取消。该意图标记为 exhausted，收尾阶段通常已把事实/资产写回")

	// ---- 单个 work 的 ctx（Engine.workCancel[intentID]）----

	AbortKilledByPlanner = cause("killed_by_planner", "规划者用 kill_work 终止了这条意图",
		"规划者调用 kill_work 主动终止了这条意图（判定方向跑偏/无意义，止损）。该意图标记为 stopped，不会被自动重领")

	AbortWorkFinished = cause("work_finished", "work 已正常结束，引擎释放其 context",
		"work 已正常结束，引擎在 unregisterWork 里释放其 context 资源。这不是中断——若它出现在中断消息里，说明取消与收场事件存在竞态")

	AbortPausedRaceGuard = cause("paused_race_guard", "任务处于暂停态，引擎拒绝启动新 run",
		"任务处于暂停态时请求执行上下文，引擎直接返回一个已取消的 context（守卫 claim→Execute 的竞态，避免暂停后仍启动新 run）。该意图会退回 frontier")

	// ---- 对话 / 主 Agent 的单轮 ctx（Server.chatCancel[busyKey]）----

	AbortChatStoppedByUser = cause("chat_stopped_by_user", "用户点了「停止」中止本轮对话",
		"用户点了「停止」，主动中止本轮对话运行（POST .../chat/stop 或 .../conversations/{id}/stop）。已产生的步骤都保留，可以直接发下一条消息")

	AbortChatTurnFinished = cause("chat_turn_finished", "本轮对话已正常结束，服务端释放其 context",
		"本轮对话已正常结束，服务端在 defer 里释放其 context 资源。这不是中断——若它出现在中断消息里，说明取消与收场事件存在竞态")

	// ---- 进程级 ----

	AbortShutdown = cause("shutdown", "后端进程正在关停（重启 / 更新 / Ctrl-C）",
		"后端进程收到 SIGINT/SIGTERM 正在关停（重启 / 更新 / 手动 Ctrl-C）。所有在跑的 planner/worker 一并取消；重启后残留的 running 意图会被重置为 open 重跑")

	// ---- 单次 run 级（worker.Execute / planner.Plan 的硬兜底）----

	AbortRunHardTimeout = cause("run_hard_timeout", "单次运行的硬超时兜底触发：某一步卡死了",
		"单次运行的硬超时兜底被触发：正常情况下软墙钟预算(MaxDuration)会在回合边界优雅收尾，只有当某一步（模型请求或某个工具）自身卡死、久久不返回、导致回合边界检查根本跑不到时，才会走到「软预算 + 90s 宽限」的硬 cancel。请重点看中断前最后一个 tool_use——多半是它挂住了")
)

// AbortReason resolves the cause attached to a cancelled run context. ok=false
// means the ctx carries no cause at all (nothing was cancelled).
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	if ac, ok := errors.AsType[*AbortCause](c); ok {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "上游 context 到达 deadline（未挂具名原因）",
			"上游 context 到达 deadline，但设置方没有用 WithTimeoutCause 挂具名原因: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		// a plain Canceled means some cancel site still lacks a cause — say so
		// plainly instead of inventing one.
		return "canceled_no_cause", "取消方未挂具名原因（漏网的取消点）",
			"上游 context 被取消，但取消方没有用 context.WithCancelCause 挂上具名原因。" +
				"这属于漏网的取消点——请在 agent/cancelcause.go 补一条枚举并接到该取消处", true
	}
	return "other", firstLine(c.Error(), 80), c.Error(), true
}
