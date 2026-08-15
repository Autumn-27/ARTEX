package agent

import (
	"strings"
	"testing"
	"time"
)

// TestChatNowVarRenders verifies the universal {{.Now}} runtime variable: a custom
// (chat) agent prompt referencing it renders the live server time each turn, rather
// than failing template execution and falling back to DefaultAssistantPrompt.
func TestChatNowVarRenders(t *testing.T) {
	prev := PromptOverride
	defer func() { PromptOverride = prev }()

	PromptOverride = func(key string) (string, bool) {
		if key == "tec_benchmark" {
			return "当前时间：{{.Now}}", true
		}
		return "", false
	}

	out := chatSystem("tec_benchmark", "/app/data", "/tmp/x")
	if !strings.Contains(out, "当前时间：") {
		t.Fatalf("custom prompt body missing, likely fell back to default: %q", out)
	}
	year := time.Now().Format("2006")
	if !strings.Contains(out, year) {
		t.Fatalf("{{.Now}} did not render the live time (want year %s): %q", year, out)
	}
}

// TestChatDataDirVarRenders verifies the universal {{.DataDir}} runtime variable:
// a custom prompt referencing it renders the server data root (s.m.dir), rather
// than failing template execution and falling back to DefaultAssistantPrompt.
func TestChatDataDirVarRenders(t *testing.T) {
	prev := PromptOverride
	defer func() { PromptOverride = prev }()

	PromptOverride = func(key string) (string, bool) {
		return "数据根目录：{{.DataDir}}", true
	}

	out := chatSystem("tec_benchmark", "/app/data", "/tmp/x")
	if !strings.Contains(out, "数据根目录：/app/data") {
		t.Fatalf("{{.DataDir}} did not render the data root: %q", out)
	}
}

// TestChatUnknownVarFallsBack verifies an out-of-catalog {{.X}} still degrades
// safely to the default assistant prompt (never a half-rendered prompt).
func TestChatUnknownVarFallsBack(t *testing.T) {
	prev := PromptOverride
	defer func() { PromptOverride = prev }()

	PromptOverride = func(key string) (string, bool) {
		return "引用了不存在的变量：{{.Bogus}}", true
	}

	out := chatSystem("whatever", "/app/data", "/tmp/x")
	if strings.Contains(out, "引用了不存在的变量") {
		t.Fatalf("broken template should have fallen back, got custom body: %q", out)
	}
	if !strings.Contains(out, DefaultAssistantPrompt) {
		t.Fatalf("expected fallback to DefaultAssistantPrompt, got: %q", out)
	}
}
