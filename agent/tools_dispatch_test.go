package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// frontier 硬上限判定(纯函数):open 计数达到 MaxOpenIntents 即满。
func TestFrontierFull(t *testing.T) {
	if MaxOpenIntents != 8 {
		t.Fatalf("MaxOpenIntents=%d, want 8(红日 3 R2 修复契约:frontier 硬上限 8 open)", MaxOpenIntents)
	}
	for _, tc := range []struct {
		open int
		want bool
	}{
		{0, false}, {1, false}, {MaxOpenIntents - 1, false},
		{MaxOpenIntents, true}, {MaxOpenIntents + 5, true}, {36, true},
	} {
		if got := FrontierFull(tc.open); got != tc.want {
			t.Errorf("FrontierFull(%d)=%v, want %v", tc.open, got, tc.want)
		}
	}
}

// capFrontierTools 必须把 add_intent 包上门卫、追加 cancel_intent,其余工具原样。
func TestCapFrontierToolsWrapsAddIntentAndAppendsCancel(t *testing.T) {
	ts := NewToolSet(nil, "planner")
	base := ts.PlannerTools()
	out := ts.capFrontierTools(base)
	if len(out) != len(base)+1 {
		t.Fatalf("capFrontierTools len=%d, want %d(+cancel_intent)", len(out), len(base)+1)
	}
	var wrapped, hasCancel bool
	for _, tool := range out {
		switch tool.Name() {
		case "add_intent":
			if _, ok := tool.(*frontierCapTool); !ok {
				t.Fatal("add_intent 未被 frontierCapTool 包装")
			}
			wrapped = true
		case "cancel_intent":
			hasCancel = true
		}
	}
	if !wrapped || !hasCancel {
		t.Fatalf("wrapped=%v hasCancel=%v", wrapped, hasCancel)
	}
}

// nil store(如 toolcatalog 的 seed 构造)下门卫必须原样放行,不能 panic。
func TestFrontierCapToolPassesThroughWithNilStore(t *testing.T) {
	inner := &stubTool{name: "add_intent"}
	tool := &frontierCapTool{CoreTool: inner, ts: nil}
	if _, err := tool.Call(context.Background(), json.RawMessage(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if !inner.called {
		t.Fatal("nil store 时门卫未放行到内层工具")
	}
	if tool.Name() != "add_intent" {
		t.Fatalf("Name()=%q, want add_intent(身份必须委托)", tool.Name())
	}
}

// MainAgentTools 现在带 kill_work/cancel_intent;cancel_intent 的 schema 只要求 intent_id。
func TestMainAgentToolsIncludeKillAndCancel(t *testing.T) {
	ts := NewToolSet(nil, "human")
	var kill, cancel actool.CoreTool
	for _, tool := range ts.MainAgentTools() {
		switch tool.Name() {
		case "kill_work":
			kill = tool
		case "cancel_intent":
			cancel = tool
		}
	}
	if kill == nil || cancel == nil {
		t.Fatalf("kill_work=%v cancel_intent=%v, mainagent 必须同时绑定", kill != nil, cancel != nil)
	}
	if !strings.Contains(kill.Description(), "cancel_intent") || !strings.Contains(kill.Description(), "steer_work") {
		t.Fatal("kill_work 描述必须写明与 steer_work/cancel_intent 的区别")
	}
	if !strings.Contains(cancel.Description(), "kill_work") || !strings.Contains(cancel.Description(), "open") {
		t.Fatal("cancel_intent 描述必须写明只作废 open、与 kill_work 的区别")
	}
}

type stubTool struct {
	actool.CoreTool
	name   string
	called bool
}

func (s *stubTool) Name() string { return s.name }
func (s *stubTool) Call(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
	s.called = true
	return actool.Text("ok"), nil
}
