package llmrec

import (
	"context"
	"testing"
)

func TestTaskIDContextUsesExplicitRegistryID(t *testing.T) {
	ctx := WithTaskID(context.Background(), " 42 ")
	if got := TaskIDFrom(ctx); got != "42" {
		t.Fatalf("TaskIDFrom()=%q want 42", got)
	}
	if got := TaskIDFrom(WithTaskID(ctx, "   ")); got != "42" {
		t.Fatalf("blank task id should preserve parent context, got %q", got)
	}
	if got := TaskIDFrom(nil); got != "" {
		t.Fatalf("nil context returned %q", got)
	}
}

func TestParseSessionFallbackIsExplorationScoped(t *testing.T) {
	taskID, worker := parseSession("exp12-worker-i99")
	if taskID != "12" || worker != "worker" {
		t.Fatalf("parseSession()=(%q,%q)", taskID, worker)
	}
	if taskID, worker := parseSession("not-a-task"); taskID != "" || worker != "" {
		t.Fatalf("unexpected non-session parse: (%q,%q)", taskID, worker)
	}
}
