package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// Real PostgreSQL + SDK hooks: the second call must see both the first result
// and a newly registered task constraint. No model or shell command is executed.
func TestTaskReviewContextAcrossToolCalls(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip("no test database configured")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	expID, err := d.CreateExploration("只操作隔离测试目录", "验证创建和清理")
	if err != nil {
		t.Fatal(err)
	}
	ts := d.Exploration(expID)
	taskID := fmt.Sprint(expID)
	t.Cleanup(func() {
		_, _ = d.Exec(`DELETE FROM intercept_pending WHERE task_id=$1`, taskID)
		_, _ = d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	})
	ic := intercept.New(d)
	priorTools, err := ic.GetEnabledTools()
	if err != nil {
		t.Fatal(err)
	}
	priorConfig := ic.GetJudgeConfig()
	t.Cleanup(func() { _ = ic.SetEnabledTools(priorTools); _ = ic.SetJudgeConfig(priorConfig) })
	const probeName = "ContextEvidenceProbe"
	if err := ic.SetEnabledTools([]string{probeName}); err != nil {
		t.Fatal(err)
	}
	if err := ic.SetJudgeConfig(intercept.JudgeConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var inputs []intercept.ReviewInput
	ic.SetReviewer(func(_ context.Context, _ int64, _ string, in intercept.ReviewInput) (intercept.Decision, error) {
		if in.Task == nil {
			t.Error("reviewer did not receive task context")
			return intercept.Decision{Action: "deny"}, nil
		}
		inputs = append(inputs, in)
		action := "allow"
		if len(in.Task.Constraints) > 0 {
			action = "deny"
		}
		return intercept.Decision{Action: action, Message: "probe policy"}, nil
	})
	turn, executions := 0, 0
	provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
		turn++
		events := []llm.StreamEvent{{Type: llm.SETextDelta, Text: "done"}, {Type: llm.SEMessageDelta, StopReason: "end_turn"}}
		if turn <= 2 {
			events = []llm.StreamEvent{
				{Type: llm.SEToolUseStart, ToolID: fmt.Sprintf("call-%d", turn), ToolName: probeName},
				{Type: llm.SEToolInputJSON, Text: fmt.Sprintf(`{"step":%d}`, turn)},
				{Type: llm.SEMessageDelta, StopReason: "tool_use"},
			}
		}
		for _, event := range events {
			if !yield(event, nil) {
				return
			}
		}
	}}
	probe := tool.Build(tool.Spec{Name: probeName, Schema: map[string]any{"type": "object"},
		Run: func(context.Context, json.RawMessage, *tool.ToolContext) (tool.Result, error) {
			executions++
			_, err := ts.AddConstraint("deny", "禁止后续清理", "human")
			return tool.Text("Created a new fixture; no existing file overwritten."), err
		},
	})
	workDir := t.TempDir()
	ctx := intercept.WithTaskContext(t.Context(), taskID, "test-agent", nil)
	ctx = withTaskReviewContext(ctx, expID, ts, workDir, &db.Node{Payload: json.RawMessage(`{"summary":"创建并清理"}`)})
	_, _, err = captureRun(ctx, agentcore.Options{Provider: provider, Tools: []tool.CoreTool{probe},
		Hooks: guard.NewWithInterceptor(ic).Hooks(), PermissionMode: permission.ModeBypass, WorkingDir: workDir, MaxTurns: 3},
		"请完成当前验证步骤", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 2 || executions != 1 {
		t.Fatalf("reviews=%d executions=%d", len(inputs), executions)
	}
	first, second := inputs[0], inputs[1]
	if first.Task == nil || first.Task.TaskID != expID || first.Task.Description != "只操作隔离测试目录" || len(first.Task.Constraints) != 0 || len(first.History) != 0 {
		t.Fatalf("wrong initial task snapshot: %+v", first)
	}
	if second.Task.Goal != "验证创建和清理" || len(second.Task.Constraints) != 1 || second.Task.Constraints[0].Text != "禁止后续清理" || second.WorkingDir != workDir || second.Intent == "" || second.TurnInput != "请完成当前验证步骤" {
		t.Fatalf("lost fresh task context: %+v", second)
	}
	if second.Correlation != "exact" || len(second.History) != 1 || second.History[0].ToolUseID != "call-1" || second.History[0].Status != "succeeded" || string(second.Arguments) != `{"step":2}` {
		t.Fatalf("lost paired execution evidence: %+v", second)
	}
	rows, err := d.ListTaskIntercepts(taskID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	for _, row := range rows {
		if row.Status != "denied" {
			continue
		}
		detail, err := d.GetInterceptDetail(row.ID)
		if err != nil || detail.Audit == nil || len(detail.Audit.ModelInput) == 0 || detail.Audit.ExecutionStatus != "not_executed" {
			t.Fatalf("denial lost model input or execution state: %+v err=%v", detail, err)
		}
	}

	t.Run("unavailable_constraints_do_not_fail_open", func(t *testing.T) {
		if err := ic.SetJudgeConfig(intercept.JudgeConfig{Enabled: true, FailAction: "allow"}); err != nil {
			t.Fatal(err)
		}
		ic.SetReviewer(func(context.Context, int64, string, intercept.ReviewInput) (intercept.Decision, error) {
			t.Error("reviewer called without required task constraints")
			return intercept.Decision{Action: "allow"}, nil
		})
		ctx := intercept.WithReviewContext(t.Context(), workDir, "", func(context.Context) (*intercept.ReviewTask, error) {
			return nil, fmt.Errorf("task database unavailable")
		})
		decision, judged := ic.Judge(ctx, probeName, json.RawMessage(`{}`))
		if !judged || decision.Action != "ask" || !decision.ModelFallback || len(decision.ModelInput) != 0 {
			t.Fatalf("missing policy was silently allowed: %+v", decision)
		}
	})
}
