package db

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInterceptDetails(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	create := func(t *testing.T, audit *InterceptAudit) int64 {
		t.Helper()
		id, err := d.CreateInterceptPending(0, 0, "approval-detail-test", "test", "Write", []byte(`{"path":"report.md"}`), "[模型] 请确认", audit)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = d.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id) })
		return id
	}
	t.Run("legacy and lazy payload", func(t *testing.T) {
		id := create(t, nil)
		got, err := d.GetInterceptDetail(id)
		if err != nil || got == nil || got.Audit != nil {
			t.Fatalf("legacy: %+v %v", got, err)
		}
		create(t, &InterceptAudit{UserMessage: "snapshot-only-marker", InitialAction: "ask"})
		items, err := d.ListTaskIntercepts("approval-detail-test")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(items)
		if strings.Contains(string(raw), "snapshot-only-marker") || strings.Contains(string(raw), `"audit"`) {
			t.Fatal("snapshot leaked into list response")
		}
		missing, err := d.GetInterceptDetail(-1)
		if err != nil || missing != nil {
			t.Fatal("missing record not reported")
		}
	})
	t.Run("decision race and exact output", func(t *testing.T) {
		id := create(t, &InterceptAudit{RunID: "run", ToolUseID: "call", Correlation: "exact", InitialAction: "ask", ExecutionStatus: "not_started"})
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				ok, err := d.ResolveIntercept(id, "allowed", "allow", "人工允许执行")
				if err != nil {
					t.Error(err)
				}
				if ok {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d decisions won", wins.Load())
		}
		ok, err := d.ResolveIntercept(id, "timeout", "deny", "late timeout")
		if err != nil || ok {
			t.Fatal("timeout overwrote decision")
		}
		if err := d.CompleteIntercept(id, "different-run", "call", "succeeded", "WRONG", false); err != nil {
			t.Fatal(err)
		}
		got, _ := d.GetInterceptDetail(id)
		if got.Audit.Output != "" {
			t.Fatal("cross-run result attached")
		}
		if err := d.CompleteIntercept(id, "run", "call", "failed", "permission denied", true); err != nil {
			t.Fatal(err)
		}
		got, err = d.GetInterceptDetail(id)
		if err != nil || got.Status != "allowed" || got.Audit.InitialAction != "ask" || got.Audit.ExecutionStatus != "failed" || !got.Audit.OutputTruncated {
			t.Fatalf("wrong details: %+v %v", got, err)
		}
		if err := d.CompleteIntercept(id, "run", "call", "succeeded", "late duplicate", false); err != nil {
			t.Fatal(err)
		}
		got, _ = d.GetInterceptDetail(id)
		if got.Audit.Output != "permission denied" {
			t.Fatal("duplicate result rewrote output")
		}
	})
	t.Run("denied output and timeout allow", func(t *testing.T) {
		id := create(t, &InterceptAudit{RunID: "run", ToolUseID: "call", ExecutionStatus: "not_started"})
		if _, err := d.ResolveIntercept(id, "denied", "deny", "人工拒绝"); err != nil {
			t.Fatal(err)
		}
		if err := d.CompleteIntercept(id, "run", "call", "failed", "Blocked by hook", false); err != nil {
			t.Fatal(err)
		}
		got, _ := d.GetInterceptDetail(id)
		if got.Audit.ExecutionStatus != "not_executed" || got.Audit.Output != "" {
			t.Fatal("denial presented as executed")
		}
		id = create(t, &InterceptAudit{RunID: "run2", ToolUseID: "call2", Correlation: "exact", InitialAction: "ask"})
		if _, err := d.ResolveIntercept(id, "timeout", "allow", "超时允许"); err != nil {
			t.Fatal(err)
		}
		if err := d.CompleteIntercept(id, "run2", "call2", "succeeded", "ok", false); err != nil {
			t.Fatal(err)
		}
		got, _ = d.GetInterceptDetail(id)
		if got.Status != "timeout" || got.Audit.EffectiveAction != "allow" || got.Audit.ExecutionStatus != "succeeded" {
			t.Fatal("timeout action lost")
		}
	})
	t.Run("archive compatibility", func(t *testing.T) {
		for _, legacy := range []bool{true, false} {
			id := create(t, &InterceptAudit{InitialAction: "ask", ExecutionStatus: "not_started"})
			var raw []byte
			if err := d.QueryRow(`SELECT row_to_json(ip) FROM intercept_pending ip WHERE id=$1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				t.Fatal(err)
			}
			if legacy {
				delete(row, "audit")
				delete(row, "decision_source")
			}
			archived, _ := json.Marshal([]map[string]any{row})
			tx, err := d.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`DELETE FROM intercept_pending WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			if err := restoreInterceptRows(tx, archived); err != nil {
				t.Fatalf("legacy=%t: %v", legacy, err)
			}
			var status, source string
			var auditJSON []byte
			if err := tx.QueryRow(`SELECT status, decision_source, audit FROM intercept_pending WHERE id=$1`, id).Scan(&status, &source, &auditJSON); err != nil {
				t.Fatal(err)
			}
			if status != "timeout" || source != "model" {
				t.Fatalf("restored %s/%s", status, source)
			}
			if legacy && len(auditJSON) > 0 {
				t.Fatal("fabricated legacy audit")
			}
			if !legacy {
				var a InterceptAudit
				if err := json.Unmarshal(auditJSON, &a); err != nil {
					t.Fatal(err)
				}
				if a.InitialAction != "ask" || a.EffectiveAction != "deny" || a.ExecutionStatus != "not_executed" {
					t.Fatalf("restored audit: %+v", a)
				}
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
