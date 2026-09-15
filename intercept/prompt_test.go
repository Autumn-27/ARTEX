package intercept

import "testing"

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantAction string
		wantReason string
	}{
		{"bare allow", "ALLOW", "allow", ""},
		{"bare deny", "DENY:删除生产文件(D4)", "deny", "删除生产文件(D4)"},
		{"bare ask", "ASK:归属不明", "ask", "归属不明"},
		{"lowercase", "deny: rm 系统文件", "deny", "rm 系统文件"},
		{"fullwidth colon", "DENY：命中D4", "deny", "命中D4"},
		// The model echoing the prompt's Chinese label must NOT fall through to
		// unparseable (which would fail-open on a real DENY).
		{"echoed label deny", "拦截:DENY:命中D5", "deny", "命中D5"},
		{"echoed label allow", "放行:ALLOW", "allow", ""},
		{"echoed label ask", "转人工：ASK:无法判断影响面", "ask", "无法判断影响面"},
		{"leading whitespace/blank line", "\n  ALLOW\n", "allow", ""},
		{"extra trailing text on later line", "DENY:清库(D4)\n其它解释", "deny", "清库(D4)"},
		{"unparseable", "我认为这个命令没问题", "", ""},
		{"empty", "", "", ""},
		// Adversarial: substrings must not count as whole-word verdicts.
		{"disallowed is not allow", "DISALLOWED", "", ""},
		{"disallowed with reason", "DISALLOWED:命中D4", "", ""},
		{"not allow negated", "NOT ALLOW", "", ""},
		{"not allow lowercase", "not allow", "", ""},
		{"not allow with punctuation", "NOT ALLOW.", "", ""},
		{"ask inside word", "I ran TASKLIST to check", "", ""},
		// Mixed output: the strictest whole-word verdict wins regardless of position.
		{"deny beats earlier allow", "ALLOW? 不,DENY:命中D4", "deny", "命中D4"},
		{"ask beats earlier allow", "ALLOW 吧,还是 ASK:无法判断", "ask", "无法判断"},
		{"deny beats ask", "ASK:先看看 DENY:命中D5", "deny", "命中D5"},
		// A negated ALLOW is skipped, but a later clean ALLOW still counts.
		{"later clean allow after negation", "NOT ALLOWED? actually ALLOW", "allow", ""},
		// Only the first non-empty line is read.
		{"verdict on first non-empty line", "\n\nDENY:清库(D4)\nALLOW", "deny", "清库(D4)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ParseVerdict(c.in)
			if v.Action != c.wantAction {
				t.Errorf("action = %q, want %q (in=%q)", v.Action, c.wantAction, c.in)
			}
			if v.Reason != c.wantReason {
				t.Errorf("reason = %q, want %q (in=%q)", v.Reason, c.wantReason, c.in)
			}
		})
	}
}

func TestParseVerdictReasonCapped(t *testing.T) {
	long := "DENY:" + string(make([]byte, 500))
	if got := ParseVerdict(long); len(got.Reason) > 200 {
		t.Errorf("reason length = %d, want <= 200", len(got.Reason))
	}
}
