package chainskel

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateChainTags(t *testing.T) {
	cases := []struct {
		name    string
		tags    []string
		wantErr bool
	}{
		{"nil 合法", nil, false},
		{"单个合法", []string{"web"}, false},
		{"两个合法", []string{"web", "pivot"}, false},
		{"非法取值", []string{"web", "cloud"}, true},
		{"超过两个", []string{"web", "ad", "priv"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateChainTags(c.tags)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateChainTags(%v) err=%v, wantErr=%v", c.tags, err, c.wantErr)
			}
		})
	}
}

func TestNormalizeChainTags(t *testing.T) {
	got := NormalizeChainTags([]string{" Web ", "web", "PIVOT", "", "  "})
	want := []string{"web", "pivot"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		text string
		want []string // nil 表示期望无命中
	}{
		{"对 /api/user?id=1 做 sql 注入测试", []string{"web"}},
		{"域内 kerberos 委派滥用摸底", []string{"ad"}},
		{" webshell 落地后搭隧道做内网横向 ", []string{"pivot"}},
		{"Linux 提权:先查 suid 与内核漏洞", []string{"priv"}},
		{"ctf pwn 题,栈溢出 + rop", []string{"pwn"}},
		{"子域收集与指纹识别等侦察工作", []string{"recon"}},
		{"逆向该样本,先脱壳再静态分析", []string{"rev"}},
		{"apk 抓包受阻,上 frida hook", []string{"app"}},
		{"对该接口做 sql 注入,拿到后提权", []string{"web", "priv"}}, // 多命中,按规则表顺序取前 2
		{"常规巡检,无明确攻击面", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := Match(c.text)
		if len(got) != len(c.want) {
			t.Fatalf("Match(%q)=%v, want %v", c.text, got, c.want)
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Fatalf("Match(%q)=%v, want %v", c.text, got, c.want)
			}
		}
	}
}

func TestSkeletonEmbedded(t *testing.T) {
	for _, c := range Categories {
		sk, ok := Skeleton(c)
		if !ok {
			t.Fatalf("类别 %s 骨架缺失(go:embed 应保证 9 类全在)", c)
		}
		if !strings.Contains(sk, "场景") || !strings.Contains(sk, "判据") {
			t.Fatalf("类别 %s 骨架不含 场景/判据 字段,疑似内容错误", c)
		}
	}
	if _, ok := Skeleton("nope"); ok {
		t.Fatal("非法类别应返回 ok=false")
	}
}

func payload(t *testing.T, m map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestForIntentByTags(t *testing.T) {
	out := ForIntent(payload(t, map[string]any{"summary": "打某站", "chain_tags": []string{"web"}}))
	if !strings.Contains(out, `<chains-skeleton source="chains-skeleton" category="web">`) {
		t.Fatalf("按 chain_tags 注入失败:\n%s", out)
	}
	if strings.Contains(out, "untrusted-data") {
		t.Fatal("骨架分区不得复用 untrusted 语义标签")
	}
}

func TestForIntentMultiTagCap2(t *testing.T) {
	out := ForIntent(payload(t, map[string]any{"summary": "x", "chain_tags": []string{"web", "ad", "pivot"}}))
	if !strings.Contains(out, `category="web"`) || !strings.Contains(out, `category="ad"`) {
		t.Fatalf("前 2 个 tag 应注入:\n%s", out)
	}
	if strings.Contains(out, `category="pivot"`) {
		t.Fatal("第 3 个 tag 超出 MaxTagsPerIntent,不应注入")
	}
}

func TestForIntentInvalidTagFallsBack(t *testing.T) {
	// 非法 tag 被忽略;剩余无合法 tag 时回退到 summary 关键词匹配。
	out := ForIntent(payload(t, map[string]any{"summary": "做 sql 注入测试", "chain_tags": []string{"bogus"}}))
	if !strings.Contains(out, `category="web"`) {
		t.Fatalf("非法 tag 应回退关键词匹配注入 web:\n%s", out)
	}
}

func TestForIntentNoTagKeywordFallback(t *testing.T) {
	out := ForIntent(payload(t, map[string]any{"summary": "内网隧道选型与横向"}))
	if !strings.Contains(out, `category="pivot"`) {
		t.Fatalf("无 tags 应关键词回退注入 pivot:\n%s", out)
	}
}

func TestForIntentNoMatchSilent(t *testing.T) {
	if out := ForIntent(payload(t, map[string]any{"summary": "随便看看"})); out != "" {
		t.Fatalf("匹配不中应静默不注入, got:\n%s", out)
	}
	if out := ForIntent(json.RawMessage(`{bad json`)); out != "" {
		t.Fatal("payload 解析失败应静默不注入")
	}
	if out := ForIntent(payload(t, map[string]any{"chain_tags": []string{"bogus"}})); out != "" {
		t.Fatal("tags 全非法且 summary 无关键词应静默不注入")
	}
}

func TestSectionTruncation(t *testing.T) {
	defer func(orig int) { sectionCapRunes = orig }(sectionCapRunes)
	sectionCapRunes = 600 // 调小上限,强制截断
	out := Section([]string{"web", "ad"})
	if out == "" {
		t.Fatal("截断后仍应有内容")
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("超出 cap 应有截断标注:\n%s", out)
	}
	if n := len([]rune(out)); n > sectionCapRunes+80 { // 标注本身另算,给足余量
		t.Fatalf("截断后总长 %d 远超 cap %d", n, sectionCapRunes)
	}
}

func TestSectionMissingCategoryDegrades(t *testing.T) {
	// 非法类别(骨架必缺失)被静默跳过;有效类别仍注入。
	out := Section([]string{"nope", "web"})
	if !strings.Contains(out, `category="web"`) {
		t.Fatalf("缺失类别应跳过、有效类别照常注入:\n%s", out)
	}
	if strings.Contains(out, "nope") {
		t.Fatal("缺失类别不应出现在输出里")
	}
	if Section([]string{"nope"}) != "" {
		t.Fatal("全部缺失应返回空串(不注入)")
	}
}
