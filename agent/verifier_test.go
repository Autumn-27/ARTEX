package agent

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

// C4:只有 confirmed 的漏洞才算"已突破";pending 占位/误报不能作 prove_goal
// 证据、不计入确认漏洞数;无独立 findings 行("")保持原行为。
func TestProvenFinding(t *testing.T) {
	if !provenFinding("") {
		t.Error("empty status (no standalone row) should stay countable")
	}
	if !provenFinding(db.FindingConfirmed) {
		t.Error("confirmed should be countable")
	}
	for _, st := range []string{db.FindingPending, db.FindingFalsePositive, db.FindingFixed, db.FindingIgnored, db.FindingDuplicate} {
		if provenFinding(st) {
			t.Errorf("status %q should not count as a proven finding", st)
		}
	}
}
