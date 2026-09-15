package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// 红日3 复盘 R3:fact 可信度分级。纯逻辑部分(规范化/阴性检测)不依赖 PG;
// 落库/降级/升级走 testDB(PG 不可用时自动 skip)。

func TestNormalizeFactConfidence(t *testing.T) {
	cases := map[string]string{
		"":          FactConfidenceObserved, // 默认档
		"observed":  FactConfidenceObserved,
		" Observed": FactConfidenceObserved, // 大小写/空白容忍
		"inferred":  FactConfidenceInferred,
		"INFERRED":  FactConfidenceInferred,
		"confirmed": FactConfidenceConfirmed,
		"garbage":   FactConfidenceObserved, // 未识别 → 默认档,不产第四档
	}
	for in, want := range cases {
		if got := normalizeFactConfidence(in); got != want {
			t.Errorf("normalizeFactConfidence(%q)=%q want %q", in, got, want)
		}
	}
}

func TestIsNegativeConclusion(t *testing.T) {
	negative := []string{
		"目标 445 端口被封死",
		"该服务不可用",
		"参数 id 不存在",
		"服务器无响应",
		"目标不支持该协议",
		"Connection blocked by firewall",
		"host UNREACHABLE",
		"no response from target",
		"445/tcp filtered",
		"Feature Not Supported",
	}
	for _, s := range negative {
		if !isNegativeConclusion(s) {
			t.Errorf("isNegativeConclusion(%q)=false want true", s)
		}
	}
	// 正常/正向结论不得误伤
	positive := []string{
		"",
		"445 端口开放,可正常连接",
		"目标支持 TLS1.3",
		"响应正常 200 OK",
		"成功获取交互式 shell",
		"识别到 nginx 1.25 / Vue3 技术栈",
	}
	for _, s := range positive {
		if isNegativeConclusion(s) {
			t.Errorf("isNegativeConclusion(%q)=true want false", s)
		}
	}
}

// factTestRig 建一个真实任务 + ToolSet,供 PG-gated 的 fact 测试用。
func factTestRig(t *testing.T) *ToolSet {
	t.Helper()
	d := testDB(t)
	t.Cleanup(func() { d.Close() })
	task, err := d.CreateTaskWithOptions(fmt.Sprintf("fact-conf-%s", t.Name()), "fact confidence test", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.DeleteTask(task.ID) })
	ts := NewToolSet(d.Exploration(task.ExplorationID), "worker-x")
	ts.SetTaskID(task.ID)
	return ts
}

func callFactTool(t *testing.T, tool actool.CoreTool, input string) string {
	t.Helper()
	res, err := tool.Call(context.Background(), json.RawMessage(input), nil)
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	return res.Flatten()
}

func factPayload(t *testing.T, ts *ToolSet, id int64) map[string]any {
	t.Helper()
	node, err := ts.ts.GetNode(id)
	if err != nil || node == nil {
		t.Fatalf("GetNode(%d): node=%+v err=%v", id, node, err)
	}
	var p map[string]any
	if err := json.Unmarshal(node.Payload, &p); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	return p
}

func TestRecordFactNegativeDowngrade(t *testing.T) {
	ts := factTestRig(t)

	// 1) 阴性结论 + 自报 observed → 自动降 inferred,返回里告知
	text := callFactTool(t, ts.recordFact(), `{"summary":"目标 445 端口被封死,无法直连","confidence":"observed"}`)
	var id int64
	if _, err := fmt.Sscanf(text, "fact recorded: %d", &id); err != nil || id <= 0 {
		t.Fatalf("record_fact return: %q", text)
	}
	if !strings.Contains(text, "inferred") || !strings.Contains(text, "confirm_fact") {
		t.Fatalf("downgrade notice missing: %q", text)
	}
	if got := factPayload(t, ts, id)["confidence"]; got != FactConfidenceInferred {
		t.Fatalf("negative fact confidence=%v want inferred", got)
	}

	// 2) 正向结论 + observed → 保持 observed,无降级提示
	text = callFactTool(t, ts.recordFact(), `{"summary":"识别到 nginx 1.25,响应 200","confidence":"observed"}`)
	if strings.Contains(text, "⚠️") {
		t.Fatalf("positive fact must not be downgraded: %q", text)
	}
	var id2 int64
	_, _ = fmt.Sscanf(text, "fact recorded: %d", &id2)
	if got := factPayload(t, ts, id2)["confidence"]; got != FactConfidenceObserved {
		t.Fatalf("positive fact confidence=%v want observed", got)
	}

	// 3) 缺省 confidence → 落库 observed(永远有值)
	text = callFactTool(t, ts.recordFact(), `{"summary":"目标支持 TLS1.3"}`)
	var id3 int64
	_, _ = fmt.Sscanf(text, "fact recorded: %d", &id3)
	if got := factPayload(t, ts, id3)["confidence"]; got != FactConfidenceObserved {
		t.Fatalf("default confidence=%v want observed", got)
	}

	// 4) 显式 inferred 阴性结论 → 保持 inferred(不重复提示也没关系,但不得升档)
	text = callFactTool(t, ts.recordFact(), `{"summary":"端口无响应","confidence":"inferred"}`)
	var id4 int64
	_, _ = fmt.Sscanf(text, "fact recorded: %d", &id4)
	if got := factPayload(t, ts, id4)["confidence"]; got != FactConfidenceInferred {
		t.Fatalf("explicit inferred confidence=%v want inferred", got)
	}

	// 5) 批量:阴性条目的 note 进 notes 映射
	batch := callFactTool(t, ts.recordFact(), `{"facts":[{"summary":"主机 unreachable","confidence":"observed"},{"summary":"指纹识别完成","confidence":"observed"}]}`)
	var out map[string]any
	if err := json.Unmarshal([]byte(batch), &out); err != nil {
		t.Fatalf("batch result: %v raw=%q", err, batch)
	}
	notes, _ := out["notes"].(map[string]any)
	if len(notes) != 1 || notes["0"] == nil {
		t.Fatalf("batch notes=%v want only index 0", notes)
	}
	ids, _ := out["ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("batch ids=%v", out["ids"])
	}
	fid := int64(ids[0].(float64))
	if got := factPayload(t, ts, fid)["confidence"]; got != FactConfidenceInferred {
		t.Fatalf("batch negative confidence=%v want inferred", got)
	}
}

func TestConfirmFact(t *testing.T) {
	ts := factTestRig(t)

	// inferred 事实 → confirm_fact 升级 confirmed + 审计字段
	text := callFactTool(t, ts.recordFact(), `{"summary":"445 被封","confidence":"observed"}`)
	var id int64
	_, _ = fmt.Sscanf(text, "fact recorded: %d", &id)
	if got := factPayload(t, ts, id)["confidence"]; got != FactConfidenceInferred {
		t.Fatalf("pre-confirm confidence=%v want inferred", got)
	}
	msg := callFactTool(t, ts.confirmFact(), fmt.Sprintf(`{"fact_id":%d}`, id))
	if !strings.Contains(msg, "confirmed") {
		t.Fatalf("confirm_fact return: %q", msg)
	}
	p := factPayload(t, ts, id)
	if p["confidence"] != FactConfidenceConfirmed {
		t.Fatalf("post-confirm confidence=%v", p["confidence"])
	}
	if p["confirmed_by"] != "worker-x" || p["confirmed_at"] == nil || p["confirmed_at"] == "" {
		t.Fatalf("audit fields missing: %+v", p)
	}

	// 重复确认 → 幂等提示,不报错
	msg = callFactTool(t, ts.confirmFact(), fmt.Sprintf(`{"fact_id":%d}`, id))
	if !strings.Contains(msg, "已是 confirmed") {
		t.Fatalf("re-confirm return: %q", msg)
	}

	// 非 fact 节点(intent)→ 拒绝
	intentID, err := ts.ts.AddIntent(map[string]any{"text": "probe"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	msg = callFactTool(t, ts.confirmFact(), fmt.Sprintf(`{"fact_id":%d}`, intentID))
	if !strings.Contains(msg, "必须是本任务的事实节点") {
		t.Fatalf("confirm on intent must be rejected: %q", msg)
	}

	// 缺 fact_id → 拒绝
	msg = callFactTool(t, ts.confirmFact(), `{}`)
	if !strings.Contains(msg, "fact_id 必填") {
		t.Fatalf("missing fact_id must be rejected: %q", msg)
	}
}

func TestGraphOverviewMarksInferredFact(t *testing.T) {
	ts := factTestRig(t)
	text := callFactTool(t, ts.recordFact(), `{"summary":"端口无响应","confidence":"observed"}`)
	var id int64
	_, _ = fmt.Sscanf(text, "fact recorded: %d", &id)

	overview := ts.graphOverviewData()
	facts, _ := overview["recent_facts"].([]map[string]any)
	var found map[string]any
	for _, f := range facts {
		if fid, _ := f["id"].(int64); fid == id {
			found = f
			break
		}
	}
	if found == nil {
		t.Fatalf("fact %d missing from recent_facts: %#v", id, facts)
	}
	conf, _ := found["confidence"].(string)
	if !strings.Contains(conf, "inferred") || !strings.Contains(conf, "待复核") {
		t.Fatalf("inferred fact must carry 待复核 marker, confidence=%q", conf)
	}
}
