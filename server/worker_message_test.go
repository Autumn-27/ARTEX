package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// After the worker-message rework, the human-message path no longer goes through
// ControlWork with an "intervene" action — it runs the intent in a dedicated
// goroutine (runDetachedIntent) and pauses, if needed, with the ordinary "pause"
// action. Guard that ControlWork only knows pause|cancel so a reintroduced
// "intervene" caller fails loudly instead of silently no-op'ing.
func TestControlWorkRejectsRemovedInterveneAction(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 1, "intervene")
	if err == nil || !containsUnsupported(err) {
		t.Fatalf("intervene should be unsupported, got %v", err)
	}
}

// A control request for an intent with no live work is a conflict, not a panic or
// a silent success — the same guard the message handler relies on when a run has
// already finished between the UI read and the request.
func TestControlWorkConflictsWhenNoWorkRegistered(t *testing.T) {
	e := NewEngine(nil)
	err := e.ControlWork(context.Background(), 42, "pause")
	if !errors.Is(err, errWorkControlConflict) {
		t.Fatalf("pause with no work = %v, want errWorkControlConflict", err)
	}
}

// validWorkerMessageRequestID gates the idempotency token; keep its charset stable
// so a client-generated UUID is always accepted and injection-y ids are rejected.
func TestValidWorkerMessageRequestID(t *testing.T) {
	ok := []string{"worker-message-abc123", "a.b:c_d-1", "550e8400-e29b-41d4-a716-446655440000"}
	for _, id := range ok {
		if !validWorkerMessageRequestID(id) {
			t.Errorf("id %q should be valid", id)
		}
	}
	bad := []string{"", "has space", "emoji😀", "slash/here", string(make([]byte, 129))}
	for _, id := range bad {
		if validWorkerMessageRequestID(id) {
			t.Errorf("id %q should be rejected", id)
		}
	}
}

func containsUnsupported(err error) bool {
	return err != nil && !errors.Is(err, errWorkControlConflict) &&
		strings.Contains(err.Error(), "unsupported work action")
}
