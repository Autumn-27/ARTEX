package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/llmrec"
	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/transcript"
)

// Full chain against a real PG: Recorder → norma provider → capturing transport
// → llm_records. The unit tests cover each hop; this proves the raw bodies
// actually survive all the way into the row an operator opens when debugging.
func TestRecorderPersistsRawWireBodies(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer m.Close()
	pg := m.PG()
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		t.Fatalf("ensure table: %v", err)
	}

	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_raw","usage":{"input_tokens":5,"output_tokens":1}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"probing"}}`,
		``,
		// A tool_use block: the normalized response body drops these entirely, so
		// its presence in raw_response is the whole point of the feature.
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_raw","name":"bash"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"id\"}"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	cfg := agent.Config{Format: llm.FormatAnthropic, BaseURL: srv.URL, APIKey: "k", Model: "claude-raw-test"}
	inner, err := cfg.NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	const session = "exp999999-rawtest"
	rec := llmrec.Wrap(inner, pg, cfg.Model, "raw-test-profile", "", "", func() bool { return true })

	// Clean up whatever this test writes, whether or not it passes. Must be a
	// defer registered after `defer m.Close()` (LIFO puts it first) — a
	// t.Cleanup would run after the manager already closed the pool.
	defer func() {
		if _, err := pg.Exec(`DELETE FROM llm_records WHERE session_id = $1`, session); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	ctx := transcript.WithSessionID(context.Background(), session)
	for _, err := range rec.Stream(ctx, llm.CompletionRequest{
		System:   []string{"system prompt"},
		Messages: []llm.Message{{Role: "user", Content: []llm.ContentBlock{{Type: "text", Text: "go"}}}},
		Tools: []llm.ToolSchema{{
			Name:        "bash",
			Description: "run a shell command",
			InputSchema: map[string]any{"type": "object"},
		}},
		MaxTokens: 512,
	}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
	}

	var id int64
	if err := pg.QueryRow(`SELECT id FROM llm_records WHERE session_id=$1 ORDER BY id DESC LIMIT 1`, session).Scan(&id); err != nil {
		t.Fatalf("no record written: %v", err)
	}
	got, err := pg.GetLLMRecord(id)
	if err != nil {
		t.Fatalf("GetLLMRecord: %v", err)
	}

	if got.RawResponse != sse {
		t.Errorf("raw_response is not the wire bytes:\n got: %q\nwant: %q", got.RawResponse, sse)
	}
	// The tool call is absent from the normalized body but present in the raw one
	// — the specific gap this feature closes.
	if strings.Contains(got.ResponseBody, "toolu_raw") {
		t.Error("normalized body unexpectedly contains the tool_use block; test no longer proves the gap")
	}
	if !strings.Contains(got.RawResponse, "toolu_raw") {
		t.Error("raw_response lost the tool_use block")
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(got.RawRequest), &body); err != nil {
		t.Fatalf("raw_request is not valid JSON: %v\n%s", err, got.RawRequest)
	}
	if body["model"] != "claude-raw-test" {
		t.Errorf("raw_request model=%v", body["model"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("raw_request tools=%v", body["tools"])
	}
	if tool, _ := tools[0].(map[string]any); tool["input_schema"] == nil {
		t.Errorf("raw_request lost the tool schema: %v", tool)
	}
	// The normalized request keeps tool names only, so the schema is unique to raw.
	if strings.Contains(got.RequestBody, "input_schema") {
		t.Error("normalized request unexpectedly carries schemas; test no longer proves the gap")
	}
}
