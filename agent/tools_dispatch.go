package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// 本文件是意图调度工具(红日 3 复盘 R2:决胜意图 #207 死在调度队列——frontier 无上限、
// P0 无人认领、mainagent 无 kill/cancel 手段)。独立成文件是为了不与 tools.go 的并行
// 改动冲突。
//
//   - frontier 硬上限:planner 的 add_intent 在 open 意图数 ≥ MaxOpenIntents 时拒派
//     (capFrontierTools 在 planner 装配层包一层门卫,不改 add_intent 本体;mainagent
//     的人工直投不受限——那是消化存量时的应急通道)。
//   - cancel_intent:作废一条【未开始(open)】的意图,mainagent/planner 均可绑。
//   - kill_work(mainagent 绑定):复用 tools.go 的 killWorkTool,只是 engine 回调带
//     killed_by_mainagent 具名原因(见 server 接线)。

// MaxOpenIntents 是 frontier 的硬上限:open 意图达到这个数,planner 的 add_intent
// 拒绝再派(先消化存量)。红日 3 里 frontier 积压 36 条 open,决胜意图排队致死。
const MaxOpenIntents = 8

// FrontierFull 是 frontier 硬上限的纯判定:open 意图数是否已达上限。
func FrontierFull(openCount int) bool { return openCount >= MaxOpenIntents }

// frontierCapTool 包装 add_intent:调用前查一次 open 计数,已满则拒派。
// 只包工具行为;Name/Description/Schema 等身份全部委托给被包工具。
type frontierCapTool struct {
	actool.CoreTool
	ts *db.ExplorationStore
}

func (f *frontierCapTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	if f.ts != nil {
		if open, err := f.ts.CountOpenIntents(); err == nil && FrontierFull(open) {
			return actool.Errorf(fmt.Sprintf(
				"frontier 已满(%d open),先消化存量:用 kill_work 终止跑偏的在跑意图、cancel_intent 作废不再需要的 open 意图,或等 worker 消化后再派",
				MaxOpenIntents)), nil
		}
	}
	return f.CoreTool.Call(ctx, in, tc)
}

// capFrontierTools 给 planner 的领域工具集加装 frontier 治理:add_intent 包硬上限
// 门卫,并追加 cancel_intent(给 planner 一个自己消化积压的手段)。其他工具原样保留。
// 只在 planner 装配点(Plan)调用;toolcatalog 的 seed 构造不走这里(seed 只需原始
// 描述/schema)。
func (t *ToolSet) capFrontierTools(tools []actool.CoreTool) []actool.CoreTool {
	out := make([]actool.CoreTool, 0, len(tools)+1)
	for _, tool := range tools {
		if tool.Name() == "add_intent" {
			tool = &frontierCapTool{CoreTool: tool, ts: t.ts}
		}
		out = append(out, tool)
	}
	return append(out, t.cancelIntentTool())
}

// CancelIntentTool exposes cancel_intent for assemblies outside this file
// (planner 的 Plan / toolcatalog 的 seed 构造)。
func (t *ToolSet) CancelIntentTool() actool.CoreTool { return t.cancelIntentTool() }

// cancelIntentTool 作废一条【未开始(open)】的意图。db 层事务见
// db.ExplorationStore.CancelOpenIntent:open → stopped,作废原因挂图留痕(审计),
// 不销毁数据。running 的意图被拒绝(那种用 kill_work)。
func (t *ToolSet) cancelIntentTool() actool.CoreTool {
	return t.writeExpTool("cancel_intent", "作废一条【尚未开始(open)】的意图:状态置为 stopped、不再被 worker 领取,作废原因作为事实挂图留痕。用于清理 frontier 里过期/重复/不再需要的积压方向。\n"+
		"与 steer_work / kill_work 的区别:steer_work 是【不打断】的实时纠偏;kill_work 是【立即终止】正在运行(running)的意图;cancel_intent 只作废【未开始(open)】的——对 running 的意图无效(那种用 kill_work)。",
		obj(map[string]any{
			"intent_id": idp("要作废的意图 id(必须处于 open 未领取状态)"),
			"reason":    str("作废原因(可选但建议填,会作为事实留痕,规划者后续能看到)"),
		}, "intent_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				IntentID json.RawMessage `json:"intent_id"`
				Reason   string          `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			id := pid(a.IntentID)
			if id <= 0 {
				return actool.Errorf("intent_id 必填"), nil
			}
			node, err := t.ts.GetNode(id)
			if err != nil || node == nil || node.Kind != db.KindIntent {
				return actool.Errorf("intent_id 必须是本任务的意图(关联任务意图只读)"), nil
			}
			origin := t.worker
			if origin == "" {
				origin = "mainagent"
			}
			factID, err := t.ts.CancelOpenIntent(id, a.Reason, origin)
			if err != nil {
				if errors.Is(err, db.ErrIntentStateConflict) {
					return actool.Errorf(fmt.Sprintf("意图 %d 当前不是 open 状态,不能作废;正在运行的意图请改用 kill_work", id)), nil
				}
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text(fmt.Sprintf("已作废意图 %d(open → stopped,不再被领取;原因已留痕为事实 #%d)", id, factID)), nil
		})
}
