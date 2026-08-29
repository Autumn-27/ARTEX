package agent

import (
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestWorkerSessionIDIsStablePerIntent(t *testing.T) {
	if got := WorkerSessionID(12, 34); got != "exp12-worker-i34" {
		t.Fatalf("session id = %q, want exp12-worker-i34", got)
	}
}

func TestWorkerChatMarkerOnlyMatchesItsNormalUserTurn(t *testing.T) {
	requestID := "worker-message-123"
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(workerChatMarker(requestID))}},
		llm.UserText(workerChatMarker("worker-message-other") + "\nother"),
		llm.UserText(workerChatMarker(requestID) + "\nnew intent"),
	}
	if !hasWorkerChatMessage(messages, requestID) {
		t.Fatal("expected the matching user turn to be detected")
	}
	if hasWorkerChatMessage(messages, "worker-message-missing") {
		t.Fatal("unrelated request id matched a Worker user turn")
	}
}
