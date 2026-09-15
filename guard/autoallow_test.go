package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// 一键放行开启时:ask 规则直通(历史记 auto_allow),deny 规则照常拦截不豁免。
func TestAutoAllowKeepsDenyEnforced(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skipf("no database config (%v) — skipping", err)
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ic := intercept.New(d)
	if err := ic.SetAutoAllow(false, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ic.SetAutoAllow(false, 0) })
	if !ic.IsToolEnabled("Bash") {
		t.Skip("Bash 不在拦截范围内,无法验证规则行为")
	}

	// 两条测试规则:相同前缀的 marker,一条 deny 一条 ask,最高优先级。
	marker := fmt.Sprintf("autoallow-guard-%d", time.Now().UnixNano())
	denyRule, err := d.CreateInterceptRule("autoallow-test-deny", "tool_input", "string", marker+"-deny", "deny", "deny 兜底", 1<<30, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	askRule, err := d.CreateInterceptRule("autoallow-test-ask", "tool_input", "string", marker+"-ask", "ask", "", 1<<30, true, false, 0, "deny")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.DeleteInterceptRule(denyRule.ID)
		_ = d.DeleteInterceptRule(askRule.ID)
		_, _ = d.Exec(`DELETE FROM intercept_pending WHERE tool_input::text LIKE '%' || $1 || '%'`, marker)
	})

	g := NewWithInterceptor(ic)
	call := func(cmd string) bool {
		input, _ := json.Marshal(map[string]string{"command": cmd})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		blocked, _, _ := g.Hooks().PreToolUse(ctx, "Bash", input)
		return blocked
	}

	if err := ic.SetAutoAllow(true, 2); err != nil {
		t.Fatal(err)
	}

	// deny:照常拦截,且不产生 auto_allow 历史。
	if !call(marker + "-deny") {
		t.Fatal("auto-allow must not exempt deny rules")
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM intercept_pending
		WHERE decision_source='auto_allow' AND tool_input::text LIKE '%' || $1 || '%'`, marker).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("deny path must not record auto_allow decisions")
	}

	// ask:直通,历史记 auto_allow。
	if call(marker + "-ask") {
		t.Fatal("auto-allow enabled: ask rule should pass through")
	}
	var source string
	if err := d.QueryRow(`SELECT decision_source FROM intercept_pending
		WHERE tool_input::text LIKE '%' || $1 || '%'`, marker+"-ask").Scan(&source); err != nil {
		t.Fatalf("ask passthrough should write history: %v", err)
	}
	if source != "auto_allow" {
		t.Fatalf("decision_source = %s, want auto_allow", source)
	}

	// 关闭后:ask 回到人工审批路径(ctx 超时 → 拦截),deny 仍拦截。
	if err := ic.SetAutoAllow(false, 0); err != nil {
		t.Fatal(err)
	}
	if !call(marker + "-deny") {
		t.Fatal("deny should still block after disabling auto-allow")
	}
	if !call(marker + "-ask") {
		t.Fatal("auto-allow disabled: ask should wait for human approval (ctx timeout → block)")
	}
}
