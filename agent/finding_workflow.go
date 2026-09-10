package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**漏洞编号约定**：finding_id 是独立漏洞记录 ID；finding_node_id 是探索节点 ID。list_findings / list_task_findings / node_detail / get_task_node_detail 的 id 保留为探索节点 ID，应从同一返回的 finding_id 读取独立编号。get_finding_traffic / bind_finding_traffic 用独立 finding_id。旧 update_finding_report 的 finding_id 参数仍传 finding_node_id。不要把 report_finding 第一行的数字用于证据工具，也不要遇到编号错误后猜测其他数字。"

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

const findingTrafficDisabled = "Agent 自动绑定流量已关闭；请在系统设置开启后重试，或使用页面人工绑定。未登记本次携带流量的漏洞。无流量上报可省略 traffic_refs / evidence_hint_id。"

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\n默认由报告 Agent 在编写报告前核对并绑定流量。上报者在 evidence 中保留验证命令、关键输出、已有的真实流量 ID 及其用途，供报告 Agent 对照执行记录核实；无需为绑定额外查包。兼容显式即时绑定：traffic_refs 或 evidence_hint_id 可提交已核实的引用，后者读取本任务指定 hint 的结构化引用；任一无效则本次上报全部失败。TCP/无包不需要这些可选参数。返回 finding_id 与 finding_node_id 分别表示独立记录和探索节点。"
		case "add_hint", "add_task_hint":
			note = "\n交接已确认漏洞时，在对应提示的 traffic_refs 中保留已核实流量的 ID、用途、说明和顺序（单条放顶层，批量放对应 hints 元素），并在 text 中说明它证明的具体漏洞。调用方不能只交接文字而丢弃已有流量引用。未核实的候选不能作为证据传递。"
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**流量证据交接（可选）**：自动绑定默认由报告 Agent 在漏洞入库后、编写报告前完成。上报者应在 evidence 保留验证命令、关键输出、已有真实流量 ID 及其用途，任务中带 intent_id，便于报告 Agent 追溯；不必为了绑定额外查包。Auto / Planner 代为上报时不要丢弃执行者已有的引用。add_hint / add_task_hint 可用 traffic_refs 交接；显式即时绑定仍兼容 report_finding 的 traffic_refs / evidence_hint_id。TCP 或无包时正常登记，不能猜测 ID，也不能仅为补包重复探测。"
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\n平台对话没有任务上下文时，不直接调用 report_finding；通过 add_task_hint 向已有对应任务交接，由任务 Agent 登记，并用 list_task_findings 核对结果。"
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\n判定目标完成前，先完成本次已有证据的上报/交接。不要在证据交接尚未完成时仅因文字漏洞已登记就结束任务、取消 Worker；无包不要求等待或强行抓包。"
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**报告前自动关联流量（已开启）**：你负责为本次触发的漏洞核对并绑定流量，再撰写报告。先从 report_finding 返回 JSON 或 get_task_node_detail / list_task_findings 取得明确的 finding_id 与 finding_node_id。读取漏洞详情、对应意图的执行记录及已有证据清单，优先使用上报者交接的真实 ID。若本次验证为 HTTP 且流量工具可用，用 traffic_search 筛选候选，再用 traffic_get 逐条核实请求/响应确实支持该漏洞；域名和时间只用于筛选，不证明归属。将确认的证据按复现顺序用 bind_finding_traffic(finding_id, traffic_refs) 关联，选择 baseline / proof / verification / supporting 并说明用途。只能操作本次漏洞，不重复创建漏洞或重新探测目标。绑定成功后重新调用 get_finding_traffic 获取最新 version，读取所需正文，再将实际读取的 version 作为 evidence_version 传给 update_finding_report（其 finding_id 参数仍用 finding_node_id）。已有绑定不必重复追加。TCP、未采集、工具不可用或没有确切匹配时，跳过自动绑定，依据文字/命令证据正常写报告并说明原因，不得为凑齐流量而猜测。绑定失败不宣称成功；保留已有证据并在报告说明未绑定原因。"
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "可选：已核实且对应本提示中具体漏洞的流量引用，保留顺序；交接后 report_finding 可传 evidence_hint_id 携带这些引用。", "items": obj(map[string]any{"traffic_id": str("真实流量 ID"), "role": str("baseline / proof / verification / supporting"), "note": str("该流量支持什么结论")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d 必须是本任务的提示节点（继承提示不可直接用于绑定）", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
