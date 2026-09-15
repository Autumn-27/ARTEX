package chainskel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifierChecklistGeneralAlways(t *testing.T) {
	for _, v := range []string{"", "某种没见过的洞", "weird-vuln"} {
		out := VerifierChecklist(v)
		if !strings.Contains(out, "以下判据用于反证,逐条过") {
			t.Fatalf("vulnclass=%q 缺固定段头:\n%s", v, out)
		}
		for _, want := range []string{"通用判据", "200 ≠ 打通", "锚定预期特征", "阴性 ≠ 无洞"} {
			if !strings.Contains(out, want) {
				t.Fatalf("vulnclass=%q 通用层缺 %q:\n%s", v, want, out)
			}
		}
		if strings.Contains(out, "专项判据") {
			t.Fatalf("vulnclass=%q 未命中类别不应有专项判据:\n%s", v, out)
		}
	}
}

func TestVerifierChecklistClasses(t *testing.T) {
	cases := []struct {
		vulnclass string
		class     string
		want      string // 专项判据里的锚点词
	}{
		{"SQL注入", "sqli", "真假分叉"},
		{"sqli (time-based)", "sqli", "基线"},
		{"XSS 反射型", "xss", "回显 ≠ 可执行"},
		{"跨站脚本", "xss", "回显"},
		{"SSRF", "ssrf", "源 IP"},
		{"命令注入", "cmdi", "线性对应"},
		{"远程命令执行(RCE)", "cmdi", "线性"},
		{"文件上传", "upload", "落地+解析"},
		{"越权访问", "auth-bypass", "双账号"},
		{"IDOR", "auth-bypass", "双账号"},
		{"敏感信息泄露", "info-leak", "真实性"},
	}
	for _, c := range cases {
		out := VerifierChecklist(c.vulnclass)
		if !strings.Contains(out, "→ "+c.class) {
			t.Fatalf("vulnclass=%q 应归一到 %s:\n%s", c.vulnclass, c.class, out)
		}
		if !strings.Contains(out, c.want) {
			t.Fatalf("vulnclass=%q 专项判据缺 %q:\n%s", c.vulnclass, c.want, out)
		}
		if !strings.Contains(out, "通用判据") {
			t.Fatalf("vulnclass=%q 通用层应永远在:\n%s", c.vulnclass, out)
		}
	}
	// 顺序敏感:命令注入不能被 sqli 的"注入"类锚点截胡。
	if out := VerifierChecklist("命令注入"); !strings.Contains(out, "→ cmdi") {
		t.Fatalf("命令注入应归一到 cmdi 而非 sqli:\n%s", out)
	}
}

func TestPivotHintsByTags(t *testing.T) {
	out := PivotHints([]string{"web"}, "", 3)
	if out == "" {
		t.Fatal("web tag 应抽出转向条目")
	}
	if !strings.Contains(out, "[web] ") {
		t.Fatalf("转向条目应带类别标记:\n%s", out)
	}
	if !strings.HasPrefix(out, "同类场景的可选转向路径") {
		t.Fatalf("缺引导行:\n%s", out)
	}
	if total := strings.Count(out, "\n- ["); total != 3 {
		t.Fatalf("max=3 应恰取 3 条, got %d:\n%s", total, out)
	}
	// 内容应来自骨架"转向:"后的原文(web.md 首条)。
	if !strings.Contains(out, "无分叉换时间盲/OOB") {
		t.Fatalf("应抽出自骨架的转向原文:\n%s", out)
	}
}

func TestPivotHintsMaxCap(t *testing.T) {
	out := PivotHints([]string{"web"}, "", 1)
	if total := strings.Count(out, "\n- ["); total != 1 {
		t.Fatalf("max=1 应只取 1 条, got %d:\n%s", total, out)
	}
	if PivotHints([]string{"web"}, "", 0) != "" {
		t.Fatal("max<=0 应返回空")
	}
}

func TestPivotHintsFallbackSummary(t *testing.T) {
	// tags 全非法 → 回退 summary 关键词匹配。
	out := PivotHints([]string{"bogus"}, "内网隧道选型与横向", 3)
	if !strings.Contains(out, "[pivot] ") {
		t.Fatalf("非法 tag 应回退关键词匹配到 pivot:\n%s", out)
	}
}

func TestPivotHintsNoMatchDegrades(t *testing.T) {
	if out := PivotHints(nil, "随便看看", 3); out != "" {
		t.Fatalf("无匹配应返回空(调用方用通用措辞), got:\n%s", out)
	}
	if out := PivotHints(nil, "", 3); out != "" {
		t.Fatal("空输入应返回空")
	}
}

func TestPivotHintsForIntent(t *testing.T) {
	out := PivotHintsForIntent(payload(t, map[string]any{"summary": "x", "chain_tags": []string{"web"}}), 3)
	if !strings.Contains(out, "[web] ") {
		t.Fatalf("payload 入口应按 chain_tags 出 web 转向:\n%s", out)
	}
	if out := PivotHintsForIntent(json.RawMessage(`{bad json`), 3); out != "" {
		t.Fatal("payload 解析失败应返回空")
	}
}

func TestRefsGuideForIntent(t *testing.T) {
	out := RefsGuideForIntent(payload(t, map[string]any{"summary": "打某站", "chain_tags": []string{"web"}}))
	if !strings.Contains(out, "skills/chains-web/refs/") || !strings.Contains(out, "精确篇目见对应 SKILL.md") {
		t.Fatalf("web tag 应给出 refs 指引:\n%s", out)
	}
	// 双 tag:两个路径都在一行里。
	out = RefsGuideForIntent(payload(t, map[string]any{"summary": "x", "chain_tags": []string{"web", "ad", "pivot"}}))
	if !strings.Contains(out, "skills/chains-web/refs/") || !strings.Contains(out, "skills/chains-ad/refs/") {
		t.Fatalf("前 2 个 tag 的路径都应在:\n%s", out)
	}
	if strings.Contains(out, "chains-pivot") {
		t.Fatal("第 3 个 tag 超上限不应出现")
	}
	// 无 tag 回退关键词;无命中静默。
	out = RefsGuideForIntent(payload(t, map[string]any{"summary": "做 sql 注入测试"}))
	if !strings.Contains(out, "skills/chains-web/refs/") {
		t.Fatalf("关键词回退应给出 web refs 指引:\n%s", out)
	}
	if out := RefsGuideForIntent(payload(t, map[string]any{"summary": "随便看看"})); out != "" {
		t.Fatalf("匹配不中应静默不注入, got:\n%s", out)
	}
	if out := RefsGuideForIntent(json.RawMessage(`{bad json`)); out != "" {
		t.Fatal("payload 解析失败应静默不注入")
	}
}
