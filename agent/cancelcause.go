package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "用户暂停了任务",
		"用户通过任务控制接口（POST /api/tasks/{id}/control，action=pause）暂停了任务。本次 Planner/Worker 运行被主动取消；运行中的意图会退回 frontier(open)，恢复任务后重新领取并从头执行")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "编排 Agent 暂停了任务",
		"编排 Agent 调用了 pause_task 工具暂停本任务。本次 Planner/Worker 运行被主动取消；运行中的意图会退回 frontier(open)，恢复后重新执行")
	AbortTaskDeleted = cause("task_deleted", "任务被删除",
		"任务正在删除（DELETE /api/tasks/{id}），删除屏障已取消该任务正在运行的 Planner、Worker 和主 Agent；本次运行结果不会再被使用")
	AbortPausedOnReload = cause("paused_on_reload", "后端恢复了任务的暂停状态",
		"后端启动时根据数据库中持久化的状态恢复了任务暂停。本次运行被取消；正常情况下恢复阶段没有正在运行的 Agent")
	AbortGoalMet = cause("goal_met", "规划者判定任务目标已达成",
		"规划者判定任务目标已达成并将任务置为 done，随后取消仍在运行的 Worker；这些意图会标记为 stopped，而不是失败")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "任务超时收尾的等待时间已用尽",
		"任务到达 timeout 后等待正在运行的 Worker 优雅收尾，但 90 秒 drain 宽限仍不足，因此执行硬取消；意图会标记为 exhausted，收尾阶段已经写入的事实和资产会保留")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "规划者终止了这条意图",
		"规划者调用 kill_work 主动终止了这条意图，通常表示方向跑偏或已无继续价值；意图会标记为 stopped，不会自动重新领取")
	AbortWorkPausedByUser = cause("work_paused_by_user", "用户暂停了这条 Worker 意图",
		"用户暂停了正在运行的 Worker。本次调用被取消，意图转为 paused；已经登记的意图、事实、漏洞和活动记录全部保留，恢复后从头重新执行")
	AbortWorkIntervenedByUser = cause("work_intervened_by_user", "用户暂停 Worker 并发起了新对话",
		"用户向正在运行的 Worker 发送了新的对话意图。本次调用被立即取消并短暂进入 paused；新消息作为同一会话的下一条用户消息，随后自动恢复并优先重新领取")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "用户取消了这条 Worker 意图",
		"用户取消了正在运行的 Worker。本次调用被取消；Worker 退出写入区后，服务端会事务性删除该意图及其直接产生的事实、漏洞和执行记录")
	AbortWorkFinished = cause("work_finished", "Worker 已正常结束并释放 context",
		"Worker 已正常结束，引擎在 detachWork 中释放其 context 资源。这不是运行中断；若它出现在中断消息中，说明取消与收场事件发生了竞态")
	AbortPausedRaceGuard = cause("paused_race_guard", "任务暂停期间拒绝启动新运行",
		"任务处于暂停状态时，引擎拒绝发出新的执行 context，用于防止 claim 与暂停之间的竞态导致 Worker 继续启动；已领取的意图会退回 frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "用户停止了本轮对话",
		"用户点击了停止，主动中止本轮主 Agent 或会话 Agent 运行。已经产生的活动记录会保留，可以继续发送下一条消息")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "任务暂停并中止了主 Agent 对话",
		"用户暂停任务时，正在运行的主 Agent 对话也被同步取消。已经产生的活动记录会保留；恢复任务后不会自动重放本轮消息")
	AbortChatTurnFinished = cause("chat_turn_finished", "本轮对话已正常结束并释放 context",
		"本轮对话已正常结束，服务端正在释放该轮 context 资源。这不是运行中断；若它出现在中断消息中，说明取消与收场事件发生了竞态")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "后端进程正在关闭",
		"后端进程收到 SIGINT 或 SIGTERM，正在重启、更新或关闭。所有运行中的 Agent 会被取消；重启后残留的 running 意图会重置为 open 并重新执行")
	AbortRunHardTimeout = cause("run_hard_timeout", "单次运行的硬超时兜底已触发",
		"单次运行超过软墙钟预算及额外宽限，说明模型请求或某个工具长时间没有返回，导致正常的回合边界收尾无法执行。请重点检查中断前最后一个未返回的工具调用")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "上游 context 到达 deadline",
			"上游 context 到达 deadline，但设置方没有通过 WithTimeoutCause 附加具名原因: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "取消方未附加具名原因",
			"上游 context 被取消，但取消方没有通过 context.WithCancelCause 附加具名原因；请在 agent/cancelcause.go 登记原因并接入该取消点", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
