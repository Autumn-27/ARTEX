package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/harness"
)

// 快速配置:阈值缩小,用例不必喂几十步。
func testStuckConfig() StuckConfig {
	return StuckConfig{
		Window: 10, SigPrefix: 200,
		SameSigTrigger: 3, FailTrigger: 4, FailSimilarity: 0.8,
		Cooldown: 2, MaxFires: 3,
	}
}

func TestNormalizeStuckOutput(t *testing.T) {
	base := normalizeStuckOutput("malloc(): memory corruption\nAborted (core dumped)")
	if got := normalizeStuckOutput("malloc(): memory corruption\nAborted (core dumped)"); got != base {
		t.Fatalf("同一文本归一化不一致: %q vs %q", got, base)
	}
	// 不同时间戳、同一结构的输出应归一一致(时间戳占位 <ts> 保留位置信息)。
	ts1 := normalizeStuckOutput("[2026-07-01T13:45:02Z] malloc(): memory corruption\nAborted (core dumped)")
	ts2 := normalizeStuckOutput("[2026/09/13 08:01:59] malloc(): memory corruption\nAborted (core dumped)")
	if ts1 != ts2 {
		t.Errorf("时间戳变体归一化应一致:\n a=%q\n b=%q", ts1, ts2)
	}
	if strings.ContainsAny(ts1, "0123456789") {
		t.Errorf("时间戳归一化后不应残留数字: %q", ts1)
	}
	// 数字变体归一化后数字位应被 # 占位,两次不同数字应归一一致
	a := normalizeStuckOutput("request id 88123 failed after 3 retries")
	b := normalizeStuckOutput("request id 41007 failed after 9 retries")
	if a != b {
		t.Errorf("数字应被归一: %q vs %q", a, b)
	}
	if strings.ContainsAny(a, "0123456789") {
		t.Errorf("归一化后不应残留数字: %q", a)
	}
}

func TestNormalizeStuckOutputPathsAndRandom(t *testing.T) {
	a := normalizeStuckOutput("open /tmp/sess_a1b2c3d4/upload_9912.php: permission denied")
	b := normalizeStuckOutput("open /tmp/sess_zz99ww88/upload_1234.php: permission denied")
	if a != b {
		t.Errorf("路径+随机串应被归一:\n a=%q\n b=%q", a, b)
	}
	c := normalizeStuckOutput(`token=4f8b2c1d9e0a4f8b2c1d9e0a4f8b2c1d status=403 forbidden`)
	d := normalizeStuckOutput(`token=aa00bb11cc22dd33ee44ff5566778899 status=403 forbidden`)
	if c != d {
		t.Errorf("长 hex token 应被归一:\n c=%q\n d=%q", c, d)
	}
}

func TestStuckSameSignatureTriggers(t *testing.T) {
	d := NewStuckDetector(testStuckConfig())
	out := "HTTP/1.1 403 Forbidden\r\nContent-Length: 291\r\n\r\n<html>blocked by waf</html>"
	for i := 1; i <= 2; i++ {
		if ev := d.Observe("Bash", out, false); ev != nil {
			t.Fatalf("第 %d 步不应触发(阈值 3)", i)
		}
	}
	ev := d.Observe("Bash", out, false)
	if ev == nil {
		t.Fatal("连续 3 次同签名应触发")
	}
	if ev.Kind != "same_signature" || ev.Count != 3 || ev.N != 1 {
		t.Errorf("事件字段不符: %+v", ev)
	}
	msg, ok := d.Drain()
	if !ok || !strings.Contains(msg, "【停滞检测】") {
		t.Errorf("触发后应有待注入消息: %q, %v", msg, ok)
	}
	if _, ok := d.Drain(); ok {
		t.Error("只应有一条待注入消息")
	}
}

func TestStuckDifferentSignaturesNoTrigger(t *testing.T) {
	d := NewStuckDetector(testStuckConfig())
	outs := []string{
		"HTTP/1.1 200 OK\r\n\r\nlogin page",
		"HTTP/1.1 302 Found\r\nLocation: /home",
		"HTTP/1.1 404 Not Found\r\n\r\nno such route",
		"HTTP/1.1 500 Internal Server Error",
		"HTTP/1.1 200 OK\r\n\r\nadmin panel",
		"HTTP/1.1 401 Unauthorized\r\nWWW-Authenticate: Basic",
	}
	for i, out := range outs {
		if ev := d.Observe("WebFetch", out, false); ev != nil {
			t.Fatalf("第 %d 步不同签名不应触发: %+v", i+1, ev)
		}
	}
}

func TestStuckSignatureToleratesVolatileFields(t *testing.T) {
	// R4 场景:同一崩溃签名,只是每次 PID/地址不同 —— 应被识别为同质。
	d := NewStuckDetector(testStuckConfig())
	for i := 1; i <= 3; i++ {
		out := fmt.Sprintf("malloc(): memory corruption (pid %d)\n[2026-07-01 13:%02d:11] Aborted (core dumped)", 4000+i, i)
		ev := d.Observe("Bash", out, true)
		if i < 3 && ev != nil {
			t.Fatalf("第 %d 步不应触发", i)
		}
		if i == 3 && ev == nil {
			t.Fatal("同一崩溃签名连出 3 次应触发")
		}
	}
}

func TestStuckCooldown(t *testing.T) {
	cfg := testStuckConfig()
	d := NewStuckDetector(cfg)
	out := "same response over and over"
	steps := cfg.SameSigTrigger + cfg.Cooldown + 1 // 触发 + 冷却期 + 冷却结束第 1 步
	var fires []int
	for i := 1; i <= steps; i++ {
		if ev := d.Observe("Bash", out, false); ev != nil {
			fires = append(fires, i)
		}
	}
	if len(fires) != 2 {
		t.Fatalf("应触发 2 次(第 %d 步 + 冷却 %d 步后),实际 %v", cfg.SameSigTrigger, cfg.Cooldown, fires)
	}
	if fires[0] != cfg.SameSigTrigger {
		t.Errorf("首触发应在第 %d 步,实际第 %d 步", cfg.SameSigTrigger, fires[0])
	}
	if fires[1] != cfg.SameSigTrigger+cfg.Cooldown+1 {
		t.Errorf("冷却 %d 步内不应触发,第二次应在第 %d 步,实际 %v", cfg.Cooldown, cfg.SameSigTrigger+cfg.Cooldown+1, fires)
	}
}

func TestStuckMaxFires(t *testing.T) {
	cfg := testStuckConfig()
	d := NewStuckDetector(cfg)
	out := "identical failure output"
	fires := 0
	// 喂足够多的步数:若无上限,每 Cooldown+1 步就会再触发一次
	for i := 0; i < 100; i++ {
		if ev := d.Observe("Bash", out, false); ev != nil {
			fires++
			if ev.N != fires {
				t.Errorf("事件序号应递增: got %d, want %d", ev.N, fires)
			}
		}
	}
	if fires != cfg.MaxFires {
		t.Errorf("触发次数应被上限 %d 截断,实际 %d", cfg.MaxFires, fires)
	}
	if d.Fires() != cfg.MaxFires {
		t.Errorf("Fires() = %d, want %d", d.Fires(), cfg.MaxFires)
	}
}

func TestStuckConsecutiveFailuresSimilar(t *testing.T) {
	cfg := testStuckConfig()
	cfg.SameSigTrigger = 100 // 关掉同签名路径,只考同工具连续失败+相似度路径
	d := NewStuckDetector(cfg)
	outs := []string{
		"Error: access denied for user 'root'@'10.0.0.11' (using password: YES)",
		"Error: access denied for user 'admin'@'10.0.0.12' (using password: YES)",
		"Error: access denied for user 'test'@'10.0.0.13' (using password: YES)",
		"Error: access denied for user 'backup'@'10.0.0.14' (using password: YES)",
	}
	for i, out := range outs {
		ev := d.Observe("Bash", out, true)
		if i < len(outs)-1 && ev != nil {
			t.Fatalf("第 %d 步不应触发", i+1)
		}
		if i == len(outs)-1 {
			if ev == nil {
				t.Fatal("同工具连续 4 次相似失败应触发")
			}
			if ev.Kind != "same_tool_failure" {
				t.Errorf("Kind = %q, want same_tool_failure", ev.Kind)
			}
		}
	}
}

func TestStuckConsecutiveFailuresDissimilarNoTrigger(t *testing.T) {
	cfg := testStuckConfig()
	cfg.SameSigTrigger = 100 // 只考失败路径
	d := NewStuckDetector(cfg)
	outs := []string{
		"Error: access denied for user 'root'@'10.0.0.11'",
		"segfault at 0x7f2a1b3c ip 00007f2a sp 00007fff error 4 in libc",
		"connection timed out after 30000 milliseconds",
		"panic: runtime error: index out of range [5] with length 2",
		"dns resolve failed: no such host",
	}
	for i, out := range outs {
		if ev := d.Observe("Bash", out, true); ev != nil {
			t.Fatalf("第 %d 步输出不相似不应触发: %+v", i+1, ev)
		}
	}
}

func TestStuckSuccessResetsFailureStreak(t *testing.T) {
	cfg := testStuckConfig()
	cfg.SameSigTrigger = 100
	d := NewStuckDetector(cfg)
	fail := "Error: access denied for user 'root'@'10.0.0.11' (using password: YES)"
	for i := 0; i < cfg.FailTrigger-1; i++ {
		d.Observe("Bash", fail, true)
	}
	d.Observe("Bash", "total 48\ndrwxr-xr-x 2 root root 4096 bin", false) // 一次成功打断连败
	for i := 0; i < cfg.FailTrigger-1; i++ {
		if ev := d.Observe("Bash", fail, true); ev != nil {
			t.Fatalf("连败被打断后重新计数,第 %d 步不应触发", i+1)
		}
	}
	if ev := d.Observe("Bash", fail, true); ev == nil {
		t.Fatal("重新累计到阈值应触发")
	}
}

func TestStuckInterventionMessage(t *testing.T) {
	msg := StuckIntervention(StuckEvent{Tool: "Bash", Kind: "same_signature", Count: 12, Signature: "abcd1234", N: 1})
	for _, want := range []string{"【停滞检测】", "信息梯度为零", "换维度", "榨干", "请求转向", "12", "abcd1234"} {
		if !strings.Contains(msg, want) {
			t.Errorf("干预消息缺少 %q:\n%s", want, msg)
		}
	}
}

func TestStuckPivotHintAppended(t *testing.T) {
	d := NewStuckDetector(testStuckConfig())
	d.PivotHint = "同类场景的可选转向路径(测试):\n- [web] 换编码/位置/Content-Type"
	out := "HTTP/1.1 403 Forbidden\r\n\r\n<html>blocked</html>"
	for i := 0; i < 3; i++ {
		d.Observe("Bash", out, false)
	}
	msg, ok := d.Drain()
	if !ok {
		t.Fatal("触发后应有待注入消息")
	}
	if !strings.Contains(msg, "【停滞检测】") || !strings.Contains(msg, "[web] 换编码/位置/Content-Type") {
		t.Errorf("PivotHint 应追加在通用干预消息之后:\n%s", msg)
	}
	// 空 PivotHint 维持原样(纯通用措辞)。
	d2 := NewStuckDetector(testStuckConfig())
	for i := 0; i < 3; i++ {
		d2.Observe("Bash", out, false)
	}
	msg2, _ := d2.Drain()
	if strings.Contains(msg2, "可选转向路径") {
		t.Errorf("空 PivotHint 不应出现转向建议段:\n%s", msg2)
	}
}

// ---------- wrapup 收尾自检段(chains L1 §4)----------

func TestWrapupSelfCheckWorker(t *testing.T) {
	s := wrapupSettlement("worker", []string{"Bash"})
	for _, want := range []string{"结束前对照自检", "record_fact", "被墙", "此路不通", "换什么维度"} {
		if !strings.Contains(s.Prompt, want) {
			t.Errorf("worker 收尾词缺少自检内容 %q:\n%s", want, s.Prompt)
		}
	}
	// 既有正文必须完整保留(自检是追加,不是替换)
	if !strings.HasPrefix(s.Prompt, settleWrapUpPrompt) {
		t.Error("自检段应追加在既有收尾词之后,既有正文不应被改动")
	}
}

func TestWrapupSelfCheckNotForOtherAgents(t *testing.T) {
	for _, key := range []string{"planner", "mainagent", "custom-x"} {
		if strings.Contains(wrapupSettlement(key, nil).Prompt, "结束前对照自检") {
			t.Errorf("%s 的收尾词不应拼 worker 自检段", key)
		}
	}
}

func TestWrapupSelfCheckTaskTimeoutVariant(t *testing.T) {
	s := wrapupSettlementForTask("worker", []string{"Bash"}, true)
	if s.PromptByReason == nil {
		t.Fatal("clamped 时应有 PromptByReason")
	}
	if !strings.Contains(s.PromptByReason[harness.ReasonTimeout], "结束前对照自检") {
		t.Error("任务超时收尾词也应带自检段")
	}
	if !strings.Contains(s.PromptByReason[harness.ReasonMaxTurns], "结束前对照自检") {
		t.Error("per-run 收尾词应带自检段")
	}
}
