package db

import (
	"strings"
	"testing"
)

func TestValidCheckVerdict(t *testing.T) {
	valid := []string{"verified", "false_positive", "inconclusive"}
	for _, v := range valid {
		if !validCheckVerdict(v) {
			t.Errorf("verdict %q should be valid", v)
		}
	}
	invalid := []string{"", "reproduced", "fixed", "confirmed", "VERIFY", " verified"}
	for _, v := range invalid {
		if validCheckVerdict(v) {
			t.Errorf("verdict %q should be invalid", v)
		}
	}
}

func TestFindingCheckInitialMessage(t *testing.T) {
	c := &FindingCheck{FindingID: 42}
	msg := c.InitialMessage()
	for _, want := range []string{"#42", "get_finding_check_context", "record_finding_check_result", "verified", "false_positive", "inconclusive"} {
		if !strings.Contains(msg, want) {
			t.Errorf("initial message missing %q: %s", want, msg)
		}
	}
}
