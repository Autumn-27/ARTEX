package db

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestNormalizeVulnClass(t *testing.T) {
	cases := map[string]string{
		"SQLi":                           "sqli",
		"sql-injection":                  "sqli",
		"SQL Injection":                  "sqli",
		" sql_injection ":                "sqli",
		"XSS":                            "xss",
		"Cross-Site Scripting":           "xss",
		"SSRF":                           "ssrf",
		"SSTI":                           "ssti",
		"Server-Side Template Injection": "ssti",
		"XXE":                            "xxe",
		"RCE":                            "cmdi",
		"cmdi":                           "cmdi",
		"Command Injection":              "cmdi",
		"remote code execution":          "cmdi",
		"File Upload":                    "upload",
		"Auth Bypass":                    "auth-bypass",
		"business_logic":                 "logic",
		"Deserialization":                "deser",
		"Insecure Deserialization":       "deser",
		"Information Disclosure":         "info-leak",
		"privilege escalation":           "privesc",
		"other":                          "other",
		// 模糊段:实战漂移字符串对(归一后必须相等,才能互相合并)
		"Stored Cross-Site Scripting (XSS)": "xss",
		"XSS (Stored)":                      "xss",
		// blind sqli 归到 sqli(与现有粗类一致,盲注是利用手法而非新类目)
		"SQL Injection (Blind)": "sqli",
		"Blind SQL Injection":   "sqli",
		"SQL 盲注":                "sqli",
		// 中文 / 带 CVE 号 / 括号内缩写
		"任意文件上传 (CVE-2017-12615)": "upload",
		"任意文件上传":                  "upload",
		"远程代码执行":                  "cmdi",
		"远程命令执行":                  "cmdi",
		"跨站脚本":                    "xss",
		"本地文件包含 (LFI)":            "lfi",
		"敏感信息泄露":                  "info-leak",
		"业务逻辑漏洞":                  "logic",
		"密码爆破":                    "brute-force",
		// 保守:含 "injection" 不等于 sqli,SSTI 由 template-injection 关键词收敛
		"Template Injection":                      "ssti",
		"Jinja2 模板注入":                             "ssti",
		"Insecure Deserialization (Java)":         "deser",
		"Vertical Privilege Escalation (PrivEsc)": "privesc",
		"OS Command Injection (RCE)":              "cmdi",
		"Authentication Bypass (MFA)":             "auth-bypass",
		// 未知名目:原样小写、折叠后保留
		"csrf":             "csrf",
		"Weird  New_Class": "weird-new-class",
		"":                 "",
	}
	for in, want := range cases {
		if got := NormalizeVulnClass(in); got != want {
			t.Errorf("NormalizeVulnClass(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaxSeverity(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"low", "medium", "medium"},
		{"critical", "high", "critical"},
		{"high", "high", "high"},
		{"medium", "low", "medium"},
		{"", "low", "low"},
		{"weird", "", "weird"},        // 都未知:返回 a
		{"medium", "weird", "medium"}, // 未知按 0 处理
	}
	for _, c := range cases {
		if got := MaxSeverity(c.a, c.b); got != c.want {
			t.Errorf("MaxSeverity(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestMergeFindingFields(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	old := DBFinding{Severity: "medium", Summary: "旧摘要", Evidence: "旧证据正文"}

	// summary 不同:替换,旧摘要归档进 evidence;severity 取高;新证据追加带标记
	sev, sum, ev := mergeFindingFields(old, RecordFindingInput{
		Severity: "high", Summary: "更深度利用摘要", Evidence: "新 PoC", IntentID: 77, Worker: "w2",
	}, now)
	if sev != "high" || sum != "更深度利用摘要" {
		t.Fatalf("severity/summary: got (%q,%q)", sev, sum)
	}
	for _, want := range []string{"旧证据正文", "[原摘要]", "旧摘要", "[新证据]", "新 PoC", "intent #77", "by w2", "2026-09-13T12:00:00Z"} {
		if !strings.Contains(ev, want) {
			t.Errorf("merged evidence missing %q:\n%s", want, ev)
		}
	}
	if !strings.HasPrefix(ev, "旧证据正文") {
		t.Errorf("原 evidence 必须保留在最前:\n%s", ev)
	}

	// summary 相同:不替换、不归档;severity 平级保留旧值;空 evidence 不追加
	sev, sum, ev = mergeFindingFields(old, RecordFindingInput{Severity: "low", Summary: "旧摘要"}, now)
	if sev != "medium" || sum != "旧摘要" || ev != "旧证据正文" {
		t.Fatalf("no-op merge: got (%q,%q,%q)", sev, sum, ev)
	}

	// 新 summary 为空:保留旧 summary,不动 evidence
	_, sum, ev = mergeFindingFields(old, RecordFindingInput{Severity: "medium", Evidence: "  "}, now)
	if sum != "旧摘要" || ev != "旧证据正文" {
		t.Fatalf("empty summary/evidence merge: got (%q,%q)", sum, ev)
	}

	// 旧 summary 为空、新 summary 非空:替换但不产生"原摘要"归档段
	emptyOld := DBFinding{Severity: "low", Summary: "", Evidence: "E"}
	_, sum, ev = mergeFindingFields(emptyOld, RecordFindingInput{Severity: "low", Summary: "S", Evidence: "E2", IntentID: 0}, now)
	if sum != "S" || strings.Contains(ev, "[原摘要]") || !strings.Contains(ev, "E2") {
		t.Fatalf("empty old summary merge: got (%q,%q)", sum, ev)
	}

	// vulnclass 措辞漂移(归一化后才相等):追加段标注本次原文,便于审计模型漂移
	oldX := DBFinding{VulnClass: "XSS (Stored)", Severity: "medium", Summary: "S", Evidence: "E"}
	_, _, ev = mergeFindingFields(oldX, RecordFindingInput{
		VulnClass: "Stored Cross-Site Scripting (XSS)", Severity: "medium", Summary: "S", Evidence: "新证据",
	}, now)
	if !strings.Contains(ev, `本次 vulnclass 原文 "Stored Cross-Site Scripting (XSS)"`) {
		t.Errorf("drift annotation missing:\n%s", ev)
	}

	// vulnclass 原文相同:不标注
	_, _, ev = mergeFindingFields(oldX, RecordFindingInput{
		VulnClass: "XSS (Stored)", Severity: "medium", Summary: "S", Evidence: "新证据",
	}, now)
	if strings.Contains(ev, "vulnclass 原文") {
		t.Errorf("no drift expected:\n%s", ev)
	}

	// 无新证据且 summary 不变,但有漂移:单独留审计段,痕迹不丢
	_, _, ev = mergeFindingFields(oldX, RecordFindingInput{
		VulnClass: "Stored Cross-Site Scripting (XSS)", Severity: "medium", Summary: "S",
	}, now)
	if !strings.Contains(ev, "vulnclass 原文") || !strings.Contains(ev, "仅 vulnclass 措辞漂移") {
		t.Errorf("drift-only segment missing:\n%s", ev)
	}
}

// --- PG 依赖路径(本机无 ARTEX_PG_DSN 时自动 skip;需在有 PG 的环境实测) ---

// TestRecordFindingTxMerge 端到端验证档 A 合并:命中 confirmed/pending 同入口
// finding 时不新增行/节点,evidence 追加、severity 取高、summary 替换并归档、
// 血缘边指向既有节点;异类/异主资产/终态/无资产均不合并。
// 需要 PG(testDSN 无配置即 skip)。
func TestRecordFindingTxMerge(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("合并测试", "目标", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)

	mkAsset := func(domain string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(`INSERT INTO assets(type,domain,root_domain,task_ids,last_seen)
			VALUES ('root_domain',$1,$1,ARRAY[$2]::bigint[],now()) RETURNING id`, domain, tk.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	assetA := mkAsset("merge-a.invalid")
	assetB := mkAsset("merge-b.invalid")
	defer func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=ANY($1::bigint[])`, []int64{assetA, assetB}) }()

	mkFinding := func(vulnclass, status string, assets []int64) (fid, nodeID int64) {
		t.Helper()
		var err error
		nodeID, err = es.AddNode(KindFinding, map[string]any{
			"vulnclass": vulnclass, "severity": "medium", "summary": "旧摘要",
			"evidence": map[string]any{"by": "w1", "poc": "旧证据"},
		}, 9, "confirmed", "w1", nil)
		if err != nil {
			t.Fatal(err)
		}
		fid, err = d.AddFinding(tk.ID, nodeID, vulnclass, "", "medium", "旧摘要", "旧证据", "w1", assets)
		if err != nil {
			t.Fatal(err)
		}
		if status != FindingPending {
			if _, err = d.SetFindingStatus(fid, status); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	record := func(in RecordFindingInput) *RecordedFinding {
		t.Helper()
		in.TaskID, in.ExplorationID = tk.ID, tk.ExplorationID
		var out *RecordedFinding
		err := d.WithEvidenceTx(t.Context(), func(tx *sql.Tx) error {
			var e error
			out, e = RecordFindingTx(tx, in, nil)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	intentID, err := es.AddNode(KindIntent, map[string]any{"summary": "深入利用"}, 5, "open", "planner", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 1) 命中:同任务、confirmed、"SQL-Injection"≈"sqli"、主资产相同 → 合并
	fid, nodeID := mkFinding("SQL-Injection", FindingConfirmed, []int64{assetA})
	out := record(RecordFindingInput{
		VulnClass: "sqli", Severity: "critical", Summary: "拿到凭据的深度利用",
		Evidence: "新 PoC 输出", Worker: "w2", IntentID: intentID, AssetIDs: []int64{assetA},
	})
	if !out.Merged || out.FindingID != fid || out.NodeID != nodeID {
		t.Fatalf("want merge into (%d,%d), got %+v", fid, nodeID, out)
	}
	if out.MergedIntoFindingID != fid || out.MergedIntoNodeID != nodeID {
		t.Fatalf("merged_into fields: %+v", out)
	}
	f, err := d.GetFinding(fid)
	if err != nil || f == nil {
		t.Fatal(err)
	}
	if f.Severity != "critical" || f.Summary != "拿到凭据的深度利用" {
		t.Fatalf("merged fields: severity=%q summary=%q", f.Severity, f.Summary)
	}
	for _, want := range []string{"旧证据", "[原摘要]", "旧摘要", "[新证据]", "新 PoC 输出", "intent #"} {
		if !strings.Contains(f.Evidence, want) {
			t.Errorf("merged evidence missing %q:\n%s", want, f.Evidence)
		}
	}
	// 血缘边 intent --yields--> 既有节点
	var edges int
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges
		WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`,
		tk.ExplorationID, intentID, RelYields, nodeID).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("yields edge: count=%d err=%v", edges, err)
	}
	// 节点 payload 同步
	var payload string
	if err := d.QueryRow(`SELECT payload::text FROM exploration_nodes WHERE id=$1`, nodeID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "拿到凭据的深度利用") || !strings.Contains(payload, "critical") {
		t.Errorf("node payload not synced: %s", payload)
	}
	// 再次上报同 intent:边幂等,仍合并
	out2 := record(RecordFindingInput{
		VulnClass: "SQLi", Severity: "high", Summary: "拿到凭据的深度利用",
		Evidence: "又一次证据", Worker: "w2", IntentID: intentID, AssetIDs: []int64{assetA},
	})
	if !out2.Merged || out2.FindingID != fid {
		t.Fatalf("re-merge: %+v", out2)
	}
	if err := d.QueryRow(`SELECT count(*) FROM exploration_edges
		WHERE exploration_id=$1 AND src_id=$2 AND rel=$3 AND dst_id=$4`,
		tk.ExplorationID, intentID, RelYields, nodeID).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("edge idempotency: count=%d err=%v", edges, err)
	}

	// 2) pending 占位也参与合并(压测 0 触发的根因修复):同主资产较新的
	// pending 行被命中(ORDER BY id DESC),证据聚合进去,status 保持 pending
	// 由验证管线(C2 增量重验)裁决,不在合并处提前判真伪。
	pfid, _ := mkFinding("sqli", FindingPending, []int64{assetA})
	out3 := record(RecordFindingInput{
		VulnClass: "SQL Injection (Blind)", Severity: "low", Summary: "另一条",
		Evidence: "pending 合并证据", AssetIDs: []int64{assetA},
	})
	if !out3.Merged || out3.FindingID != pfid {
		t.Fatalf("pending must merge into %d: %+v", pfid, out3)
	}
	pf, err := d.GetFinding(pfid)
	if err != nil || pf == nil {
		t.Fatal(err)
	}
	if pf.Status != FindingPending {
		t.Fatalf("merge must not change status, got %q", pf.Status)
	}
	if !strings.Contains(pf.Evidence, "pending 合并证据") ||
		!strings.Contains(pf.Evidence, `本次 vulnclass 原文 "SQL Injection (Blind)"`) {
		t.Errorf("pending merged evidence missing segment/drift note:\n%s", pf.Evidence)
	}

	// 3) 归一化后不同类别 → 新建
	mkFinding("xss", FindingConfirmed, []int64{assetA})
	out4 := record(RecordFindingInput{
		VulnClass: "ssrf", Severity: "low", Summary: "ssrf", AssetIDs: []int64{assetA},
	})
	if out4.Merged {
		t.Fatalf("different class must not merge: %+v", out4)
	}
	defer func() { _, _ = d.DeleteFinding(out4.FindingID) }()

	// 4) 终态排除 + 主资产不同:assetB 上有一条 false_positive 的 sqli,
	// 终态不参与合并;assetB 上无 confirmed/pending sqli → 新建
	fpfid, _ := mkFinding("sqli", FindingFalsePositive, []int64{assetB})
	out5 := record(RecordFindingInput{
		VulnClass: "sqli", Severity: "low", Summary: "别的资产", AssetIDs: []int64{assetB},
	})
	if out5.Merged || out5.FindingID == fpfid {
		t.Fatalf("terminal state must not merge (fp fid=%d): %+v", fpfid, out5)
	}
	defer func() { _, _ = d.DeleteFinding(out5.FindingID) }()

	// 5) 无资产 → 合并键不完整,新建
	out6 := record(RecordFindingInput{VulnClass: "sqli", Severity: "low", Summary: "无资产"})
	if out6.Merged {
		t.Fatalf("asset-less report must not merge: %+v", out6)
	}
	defer func() { _, _ = d.DeleteFinding(out6.FindingID) }()

	// 6) 合并键为主资产对等(asset_ids->0),不是包含:
	// (a) 候选 [ep,svc] vs 输入 [svc] —— svc 只是候选的次资产,主资产不同,不合并;
	// (b) 候选 [ep,svc] vs 输入 [ep] —— 主资产相同,合并。
	epAsset := mkAsset("merge-ep.invalid")
	svcAsset := mkAsset("merge-svc.invalid")
	defer func() { _, _ = d.Exec(`DELETE FROM assets WHERE id=ANY($1::bigint[])`, []int64{epAsset, svcAsset}) }()

	mfid, _ := mkFinding("csrf", FindingConfirmed, []int64{epAsset, svcAsset})
	out7 := record(RecordFindingInput{
		VulnClass: "csrf", Severity: "low", Summary: "挂在 service 上的另一条",
		AssetIDs: []int64{svcAsset},
	})
	if out7.Merged {
		t.Fatalf("secondary-asset containment must not merge (candidate fid=%d): %+v", mfid, out7)
	}
	defer func() { _, _ = d.DeleteFinding(out7.FindingID) }()

	out8 := record(RecordFindingInput{
		VulnClass: "CSRF", Severity: "medium", Summary: "同入口加深",
		AssetIDs: []int64{epAsset},
	})
	if !out8.Merged || out8.FindingID != mfid {
		t.Fatalf("same primary asset must merge into %d: %+v", mfid, out8)
	}
}
