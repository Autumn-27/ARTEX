package server

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
)

// P0 意图领取超时上报(红日 3 复盘 R2:决胜意图 #207 已证明 PTH 可行,却压在 36 条
// open 的 frontier 里从未被领取,用户三次追问全部落空)。
//
// 机制:plannerLoop 里的 watchdog ticker 周期扫描 frontier;priority ≥ 阈值且 open
// 超过 p0ClaimTimeout 仍未被领取的意图,写一条 hint 节点进探索图——planner 经
// graph_overview 的 hints 自然感知(mainagent 和人也能看到),不额外触发 LLM 调用。
// 冷却:同一意图只报一次(进程内台账,Engine.p0Reported)。
const (
	// p0PriorityThreshold 是「决胜档」优先级阈值:add_intent 的 priority 是 0-10,
	// mainagent 人工直投用 8-10,planner 默认 5——≥8 即视为 P0。
	p0PriorityThreshold = 8
	// p0ClaimTimeout 是 P0 意图的领取超时:open 超过这个时长未被领取即上报。
	p0ClaimTimeout = 5 * time.Minute
	// p0WatchInterval 是巡检周期:远小于超时即可,每次只是一次 frontier 查询。
	p0WatchInterval = time.Minute
)

// p0ClaimOverdue 是超时上报的纯判定:一条 open 意图(创建于 createdAt、优先级
// priority)在 now 时刻是否已构成「P0 逾期未领取」,以及它已等待多久。
func p0ClaimOverdue(priority int, createdAt, now time.Time) (overdue bool, waited time.Duration) {
	if priority < p0PriorityThreshold {
		return false, 0
	}
	waited = now.Sub(createdAt)
	return waited >= p0ClaimTimeout, waited
}

// p0MarkReported 登记一条意图的「已上报」冷却。返回 false = 已报过,调用方跳过。
func (e *Engine) p0MarkReported(taskID string, intentID int64) bool {
	v, _ := e.p0Reported.LoadOrStore(taskID, &sync.Map{})
	_, loaded := v.(*sync.Map).LoadOrStore(intentID, true)
	return !loaded
}

// p0Unmark 撤销登记(hint 写库失败时允许下个周期重试)。
func (e *Engine) p0Unmark(taskID string, intentID int64) {
	if v, ok := e.p0Reported.Load(taskID); ok {
		v.(*sync.Map).Delete(intentID)
	}
}

// reportOverdueP0Intents 扫描一次 frontier,给逾期未领取的 P0 意图各写一条
// 「【调度】P0 意图 #N 已等待 X 分钟未被领取」的 hint(每意图只报一次)。
// 只做 db 读写,不唤醒 planner(它经 graph_overview 自然感知),不触发 LLM 调用。
func (e *Engine) reportOverdueP0Intents(t *Task) {
	if t == nil || t.Store == nil {
		return
	}
	if e.IsPaused(t.ID) || e.IsDeleting(t.ID) || e.isSettling(t.ID) {
		return
	}
	if e.m != nil && isTerminalStatus(e.m.TaskStatus(t.ID)) {
		return
	}
	if _, worker := e.snapshotFor(t); worker == nil {
		return // 无 LLM 时没人能领取,上报只是噪声
	}
	frontier, err := t.Store.Frontier(50)
	if err != nil {
		log.Printf("[scheduler] task %s P0 巡检读取 frontier 失败: %v", t.ID, err)
		return
	}
	now := time.Now()
	for _, n := range frontier {
		overdue, waited := p0ClaimOverdue(n.Priority, n.CreatedAt, now)
		if !overdue {
			continue
		}
		if !e.p0MarkReported(t.ID, n.ID) {
			continue // 冷却:同一意图只报一次
		}
		msg := fmt.Sprintf("【调度】P0 意图 #%d 已等待 %d 分钟未被领取，建议 mainagent 点名派发或人工确认",
			n.ID, int(waited.Minutes()))
		_, err := t.Store.AddNode(db.KindHint, map[string]any{
			"text":      msg,
			"kind":      "p0_unclaimed",
			"intent_id": n.ID,
		}, 0, "active", "scheduler", nil)
		if err != nil {
			e.p0Unmark(t.ID, n.ID) // 写库失败不占用冷却,下周期重试
			log.Printf("[scheduler] task %s P0 意图 #%d 超时上报写 hint 失败: %v", t.ID, n.ID, err)
			continue
		}
		log.Printf("[scheduler] task %s P0 意图 #%d 已等待 %v 未被领取,已写入调度提示", t.ID, n.ID, waited.Round(time.Second))
	}
}
