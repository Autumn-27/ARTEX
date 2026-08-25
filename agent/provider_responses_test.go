package agent

import (
	"testing"

	"github.com/Autumn-27/norma/llm"
)

// TestConfigFromOpenAIResponses locks the openai-responses format wiring:
// ConfigFrom resolves the Responses format, strips a full /responses endpoint
// back to the API base, and Provider() round-trips the short name. NewProvider
// must build a working provider for it.
func TestConfigFromOpenAIResponses(t *testing.T) {
	c := ConfigFrom("openai-responses", "gpt-5", "https://gw.example/v1/responses", "sk-x", "")
	if c.Format != llm.FormatOpenAIResponses {
		t.Fatalf("format=%v, want FormatOpenAIResponses", c.Format)
	}
	if c.BaseURL != "https://gw.example/v1" {
		t.Fatalf("base_url=%q, want the /responses suffix stripped", c.BaseURL)
	}
	if c.Provider() != "openai-responses" {
		t.Fatalf("Provider()=%q", c.Provider())
	}
	if !c.Stream { // default streaming preserved
		t.Fatal("Stream should default true")
	}
	if _, err := c.NewProvider(); err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
}
