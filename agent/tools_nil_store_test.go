package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

// The server-level ToolSet behind buildDomainReg carries a nil ExplorationStore,
// and the tools table can bind any of its tools to any agent — including agents
// that never run inside a task. Every domain tool must therefore survive being
// called with nil stores: a nil deref here runs on the harness's own goroutine,
// out of reach of every recover() in the server, and kills the whole process.
func TestDomainToolsSurviveNilStores(t *testing.T) {
	inputs := []string{
		`{}`,
		`{"id":379,"asset_id":1,"goal_id":1,"evidence_id":1,"intent_id":1,"node_id":1,"work_id":1,` +
			`"summary":"x","reason":"x","name":"x","severity":"low","vulnclass":"x","text":"x","q":"x"}`,
	}
	for _, tool := range NewToolSet(nil, "").AllDomainTools() {
		for _, in := range inputs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked with nil stores on %s: %v", tool.Name(), in, r)
					}
				}()
				if _, err := tool.Call(context.Background(), json.RawMessage(in), nil); err != nil {
					t.Fatalf("%s returned a transport error: %v", tool.Name(), err)
				}
			}()
		}
	}
}

// node_detail is the one that took the process down; assert it now answers with a
// usable refusal rather than dying.
func TestExplorationToolRefusesWithoutTask(t *testing.T) {
	ts := NewToolSet(nil, "")
	res, err := ts.NodeDetailTool().Call(context.Background(), json.RawMessage(`{"id":379}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "任务上下文") {
		t.Fatalf("want an explanatory tool error, got IsError=%v %q", res.IsError, res.Flatten())
	}
}

// A panicking tool must degrade to a tool error: the run continues and the process
// survives, instead of the supervisor restarting into the same crash on replay.
func TestGuardPanicConvertsPanicToToolError(t *testing.T) {
	boom := actool.Build(actool.Spec{
		Name: "boom",
		Run: func(context.Context, json.RawMessage, *actool.ToolContext) (actool.Result, error) {
			panic("nil map write")
		},
	})
	res, err := guardPanic(boom).Call(context.Background(), json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Flatten(), "nil map write") {
		t.Fatalf("want the panic reported as a tool error, got IsError=%v %q", res.IsError, res.Flatten())
	}
}
