package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
)

// End-to-end through a real norma provider: the Capture rides the context into
// norma, survives its internal request building, and comes back holding the
// exact bytes buildBody() put on the wire. This is the load-bearing assumption
// of the whole feature — norma must propagate the caller's context down to
// http.NewRequestWithContext.
func TestCapturePropagatesThroughNormaProvider(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":11,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	// Record exactly what the server receives, so the capture can be compared
	// against it byte for byte rather than merely spot-checked for fields.
	var gotPath, serverSaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server read body: %v", err)
		}
		serverSaw = string(b)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	cfg := Config{
		Format:  llm.FormatAnthropic,
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}
	prov, err := cfg.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	ctx, capt := llmrec.NewCapture(context.Background())
	req := llm.CompletionRequest{
		System:   []string{"you are a scanner"},
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "go"}}}},
		Tools: []llm.ToolSchema{{
			Name:        "bash",
			Description: "run a shell command",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		}},
		MaxTokens: 1024,
	}

	var text strings.Builder
	for ev, err := range prov.Stream(ctx, req) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if ev.Type == llm.SETextDelta {
			text.WriteString(ev.Text)
		}
	}
	if text.String() != "hello" {
		t.Fatalf("stream text=%q, capture interfered with delivery", text.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path=%q", gotPath)
	}

	// The raw request must be what norma actually sent, not the recorder's
	// re-serialization — which is exactly why it carries fields the normalized
	// view drops.
	raw := capt.RawRequest()
	if raw == "" {
		t.Fatal("no raw request captured — context did not reach the transport")
	}
	// The load-bearing claim: what we stored equals, byte for byte, what the
	// server received — not merely "has the right fields".
	if raw != serverSaw {
		t.Fatalf("captured request != what the server received:\n got: %q\nsaw: %q", raw, serverSaw)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("raw request is not valid JSON: %v\n%s", err, raw)
	}
	if body["model"] != "claude-test" {
		t.Errorf("model=%v want claude-test (absent from CompletionRequest)", body["model"])
	}
	if body["stream"] != true {
		t.Errorf("stream=%v want true", body["stream"])
	}
	// The full tool schema is the headline gain: the normalized view keeps names only.
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["description"] != "run a shell command" {
		t.Errorf("tool description missing: %v", tool)
	}
	if tool["input_schema"] == nil {
		t.Errorf("tool input_schema missing: %v", tool)
	}

	// And the response is the untouched SSE frames, including the events the
	// recorder never turns into stored output.
	if capt.RawResponse() != sse {
		t.Errorf("RawResponse mismatch:\n got: %q\nwant: %q", capt.RawResponse(), sse)
	}
}
