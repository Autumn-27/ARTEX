package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillTools functionally exercises create_skill + update_skill_file against a
// temp skill dir (no DB needed — these tools only touch the filesystem).
func TestSkillTools(t *testing.T) {
	dir := t.TempDir()
	s := &Server{skillDir: dir}

	// create_skill writes a spec-compliant SKILL.md.
	in, _ := json.Marshal(map[string]any{
		"name": "my-skill", "description": "does X", "instructions": "## do\n1. a",
	})
	res, err := s.toolCreateSkill().Call(context.Background(), in, nil)
	if err != nil || res.IsError {
		t.Fatalf("create_skill: err=%v result=%+v", err, res)
	}
	md, err := os.ReadFile(filepath.Join(dir, "my-skill", "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md not written: %v", err)
	}
	if !strings.Contains(string(md), "name: my-skill") || !strings.Contains(string(md), "description: does X") || !strings.Contains(string(md), "1. a") {
		t.Fatalf("SKILL.md content:\n%s", md)
	}

	// creating the same skill again fails.
	if r, _ := s.toolCreateSkill().Call(context.Background(), in, nil); !r.IsError {
		t.Fatal("duplicate create should error")
	}

	// invalid name rejected.
	bad, _ := json.Marshal(map[string]any{"name": "Bad Name", "description": "x"})
	if r, _ := s.toolCreateSkill().Call(context.Background(), bad, nil); !r.IsError {
		t.Fatal("invalid skill name should error")
	}

	// update_skill_file writes a nested script; path traversal is rejected.
	up, _ := json.Marshal(map[string]any{"name": "my-skill", "file": "scripts/run.py", "content": "print(1)"})
	if r, err := s.toolUpdateSkillFile().Call(context.Background(), up, nil); err != nil || r.IsError {
		t.Fatalf("update_skill_file: err=%v result=%+v", err, r)
	}
	got, err := os.ReadFile(filepath.Join(dir, "my-skill", "scripts", "run.py"))
	if err != nil || string(got) != "print(1)" {
		t.Fatalf("nested file not written: %v %q", err, got)
	}
	evil, _ := json.Marshal(map[string]any{"name": "my-skill", "file": "../escape.txt", "content": "x"})
	if r, _ := s.toolUpdateSkillFile().Call(context.Background(), evil, nil); !r.IsError {
		t.Fatal("path traversal should be rejected")
	}
}
