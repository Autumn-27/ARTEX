package agent

import (
	"strings"
	"testing"
)

func TestRenderSystemOverrideAndFallback(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// no override → built-in default
	PromptOverride = nil
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "g"}); got != "DEFAULT" {
		t.Fatalf("no override should give default, got %q", got)
	}

	// override → rendered with vars
	PromptOverride = func(k string) (string, bool) {
		if k == "planner" {
			return "目标:{{.Goal}} 范围:{{.Scope}}", true
		}
		return "", false
	}
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "拿下X", Scope: "*.x.com"}); got != "目标:拿下X 范围:*.x.com" {
		t.Fatalf("override render: %q", got)
	}

	// override referencing a non-catalog var → execution error → fallback to default
	PromptOverride = func(k string) (string, bool) { return "{{.NotInCatalog}}", true }
	if got := renderSystem("planner", "DEFAULT", PlannerVars{Goal: "x"}); got != "DEFAULT" {
		t.Fatalf("bad var should fall back to default, got %q", got)
	}

	// full plannerSystem path: DB body [A] is honored, then the code-owned tail
	// [C] (中间产物输出规约) is ALWAYS appended — editing the body can't drop it.
	PromptOverride = func(k string) (string, bool) { return "PLANNER {{.Goal}}", true }
	got := plannerSystem("拿下X", "/data", "/data")
	if !strings.HasPrefix(got, "PLANNER 拿下X") {
		t.Fatalf("plannerSystem body not honored: %q", got)
	}
	if !strings.Contains(got, "中间产物输出规约") || !strings.Contains(got, "/data") {
		t.Fatalf("plannerSystem missing code-owned artifact tail: %q", got)
	}

	// worker dual-text via {{if .ProxyAddr}} in a user template, plus the code tail:
	// [B] trafficTool present only when RECORDING (caCert set — the MITM is on, so
	// the traffic_* tools exist), [C] artifact spec always present. The trafficTool
	// block is gated on the CA (arg 2), NOT on ProxyAddr — a global egress proxy
	// with capture off routes traffic but records nothing.
	PromptOverride = func(k string) (string, bool) {
		return "{{if .ProxyAddr}}走代理 {{.ProxyAddr}}{{else}}手动{{end}}", true
	}
	recording := workerSystem("127.0.0.1:8080", "/ca.pem", "/data", "/data", false)
	if !strings.HasPrefix(recording, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker proxy branch body: %q", recording)
	}
	if !strings.Contains(recording, "traffic_search") {
		t.Fatalf("worker while recording should inject trafficTool: %q", recording)
	}
	if strings.Contains(recording, "traffic_refs") {
		t.Fatalf("worker bypassed shared optional evidence policy: %q", recording)
	}
	if !strings.Contains(recording, "中间产物输出规约") {
		t.Fatalf("worker missing artifact tail: %q", recording)
	}
	// Egress proxy set but capture OFF (no CA): the ProxyAddr template branch still
	// renders, but the trafficTool block must NOT — those tools are not registered.
	egressOnly := workerSystem("127.0.0.1:8080", "", "/data", "/data", false)
	if !strings.HasPrefix(egressOnly, "走代理 127.0.0.1:8080") {
		t.Fatalf("worker egress-only branch body: %q", egressOnly)
	}
	if strings.Contains(egressOnly, "traffic_search") {
		t.Fatalf("worker without recording must NOT inject trafficTool: %q", egressOnly)
	}
	noProxy := workerSystem("", "", "/data", "/data", false)
	if !strings.HasPrefix(noProxy, "手动") {
		t.Fatalf("worker no-proxy branch body: %q", noProxy)
	}
	if strings.Contains(noProxy, "traffic_search") {
		t.Fatalf("worker without proxy must NOT inject trafficTool: %q", noProxy)
	}
}

// 期 4:intranet=true 时正文换 worker.intranet 变体(DB 覆盖优先,缺失退回
// worker 链),代码固定内网红线尾 ALWAYS 追加,编辑正文删不掉。
func TestWorkerSystemIntranetVariant(t *testing.T) {
	t.Cleanup(func() { PromptOverride = nil })

	// 无 DB 覆盖:退回 worker 默认正文 + 内网红线尾。
	PromptOverride = nil
	got := workerSystem("", "", "/data", "/data", true)
	if !strings.Contains(got, "内网红线") {
		t.Fatalf("intranet run missing code-owned 内网红线尾: %q", got)
	}

	// worker.intranet 覆盖优先于 worker 覆盖。
	PromptOverride = func(k string) (string, bool) {
		switch k {
		case "worker.intranet":
			return "内网正文 {{.Intranet}}", true
		case "worker":
			return "外网正文", true
		}
		return "", false
	}
	if got = workerSystem("", "", "/data", "/data", true); !strings.HasPrefix(got, "内网正文 true") {
		t.Fatalf("worker.intranet override should win: %q", got)
	}
	if !strings.Contains(got, "内网红线") {
		t.Fatalf("edited variant body must not drop code-owned 内网红线尾: %q", got)
	}
	// 变体缺失时退回 worker 本身的覆盖。
	PromptOverride = func(k string) (string, bool) {
		if k == "worker" {
			return "外网正文", true
		}
		return "", false
	}
	if got = workerSystem("", "", "/data", "/data", true); !strings.HasPrefix(got, "外网正文") {
		t.Fatalf("missing variant should fall back to worker key: %q", got)
	}
	// intranet=false 不追加红线尾。
	if got = workerSystem("", "", "/data", "/data", false); strings.Contains(got, "内网红线") {
		t.Fatalf("non-intranet run must not append 内网红线尾: %q", got)
	}
}
