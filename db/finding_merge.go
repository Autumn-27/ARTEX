package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// 档 A 同入口 finding 自动合并(FIXPLAN 需求一 / ROADMAP C2·C4)。
//
// RecordFindingTx 在插入前按 (task_id, 归一化 vulnclass, 主 asset_id) 查同任务
// 未关闭且 status IN ('confirmed','pending') 的既有 finding;命中则不新增
// 行/节点,改为追加 evidence、severity 取最高、summary 以最新深度利用为准
// (旧的挪进 evidence 存档)、新 intent 的 yields 边指向既有节点,流量快照绑到
// 既有 finding。查重与插入同处任务级证据锁(LockTaskEvidenceTx)内,天然串行化,
// 无并发双插窗口。
//
// pending 也参与合并的依据:压测中第二条上报到达时第一条往往还在 pending,
// 仅认 confirmed 会导致合并 0 触发。未验证结论先聚合、再由验证管线裁决——
// C2 已有合并后增量重验兜底(evidence.Store.OnMerged →
// server.RequestFindingCheck);若后被裁为误报,false_positive 处置时其合并
// 进来的证据段仍留在 evidence 里可审计。终态(fixed/false_positive/duplicate
// 等)仍排除,不污染聚合。
//
// 不参与合并的情形(均落回原有新建路径):
//   - 无 task(TaskID<=0)或无主资产(AssetIDs 为空)——合并键不完整;
//   - 候选 status 为终态(fixed/false_positive/duplicate 等);
//   - 候选无探索节点(node_id IS NULL)——合并必须保持 1节点↔1行 契约。

// vulnClassAliases 把常见漏洞类目收敛到粗类;未知名目原样小写保留(仍参与判等,
// 只是不做跨名目合并)。键必须先经 normalizeVulnClassKey 同款折叠(小写、空白/
// 下划线/连字符折叠为单连字符)。
var vulnClassAliases = map[string]string{
	"sqli":                           "sqli",
	"sql-injection":                  "sqli",
	"sql-inj":                        "sqli",
	"sql":                            "sqli",
	"xss":                            "xss",
	"cross-site-scripting":           "xss",
	"ssrf":                           "ssrf",
	"server-side-request-forgery":    "ssrf",
	"ssti":                           "ssti",
	"template-injection":             "ssti",
	"server-side-template-injection": "ssti",
	"xxe":                            "xxe",
	"xml-external-entity":            "xxe",
	"cmdi":                           "cmdi",
	"rce":                            "cmdi",
	"command-injection":              "cmdi",
	"os-command-injection":           "cmdi",
	"remote-code-execution":          "cmdi",
	"code-execution":                 "cmdi",
	"upload":                         "upload",
	"file-upload":                    "upload",
	"arbitrary-file-upload":          "upload",
	"unrestricted-upload":            "upload",
	"auth-bypass":                    "auth-bypass",
	"authentication-bypass":          "auth-bypass",
	"broken-auth":                    "auth-bypass",
	"broken-authentication":          "auth-bypass",
	"logic":                          "logic",
	"business-logic":                 "logic",
	"deser":                          "deser",
	"deserialization":                "deser",
	"insecure-deserialization":       "deser",
	"info-leak":                      "info-leak",
	"information-disclosure":         "info-leak",
	"info-disclosure":                "info-leak",
	"sensitive-data-exposure":        "info-leak",
	"privesc":                        "privesc",
	"privilege-escalation":           "privesc",
	"priv-esc":                       "privesc",
	"other":                          "other",
}

// normalizeVulnClassKey 折叠 vulnclass:小写、去首尾空白,空白/下划线/连字符/
// 斜杠折叠为单连字符。别名表的键与模糊段的关键词都是同款折叠后的形式。
func normalizeVulnClassKey(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	return strings.Join(strings.FieldsFunc(v, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '/'
	}), "-")
}

// vulnClassFuzzyKeywords 模糊段关键词表,按优先级顺序从上到下匹配,命中即收敛
// 到规范粗类。保守原则:刻意不收裸 "injection"——SSTI 等含 "injection" 但不
// 是 SQLi;"sql" 词根才是 sqli 的强信号。blind sqli 归到 sqli 而非独立
// sqli-blind:与既有粗类保持一致,盲注是 sqli 的利用手法而非新类目。
var vulnClassFuzzyKeywords = []struct {
	keys  []string
	class string
}{
	{[]string{"sql", "盲注"}, "sqli"},
	{[]string{"xss", "cross-site-script", "跨站脚本"}, "xss"},
	{[]string{"ssrf"}, "ssrf"},
	{[]string{"ssti", "template-injection", "模板注入"}, "ssti"},
	{[]string{"xxe"}, "xxe"},
	{[]string{"command-injection", "命令注入", "rce", "code-execution", "远程代码执行", "远程命令执行", "代码执行", "命令执行"}, "cmdi"},
	{[]string{"file-upload", "文件上传", "上传"}, "upload"},
	{[]string{"file-inclusion", "lfi", "rfi", "文件包含"}, "lfi"},
	{[]string{"deserial", "反序列化"}, "deser"},
	{[]string{"csrf"}, "csrf"},
	{[]string{"auth-bypass", "authentication-bypass", "认证绕过", "鉴权绕过"}, "auth-bypass"},
	{[]string{"brute", "爆破"}, "brute-force"},
	{[]string{"info-leak", "information-leak", "泄露", "泄漏"}, "info-leak"},
	{[]string{"privesc", "privilege-escalation", "提权"}, "privesc"},
	{[]string{"logic", "逻辑"}, "logic"},
}

// fuzzyVulnClass 对折叠后的 key 做关键词包含扫描,命中返回粗类。
func fuzzyVulnClass(key string) (string, bool) {
	for _, kw := range vulnClassFuzzyKeywords {
		for _, k := range kw.keys {
			if strings.Contains(key, k) {
				return kw.class, true
			}
		}
	}
	return "", false
}

// parenContents 提取折叠后 key 中各对(半角/全角)括号内的内容,用于模型惯用的
// "Stored Cross-Site Scripting (XSS)" / "任意文件上传 (CVE-2017-12615)" 写法。
func parenContents(key string) []string {
	var out []string
	for _, pair := range [][2]rune{{'(', ')'}, {'（', '）'}} {
		rest := key
		for {
			i := strings.IndexRune(rest, pair[0])
			if i < 0 {
				break
			}
			rest = rest[i+1:]
			j := strings.IndexRune(rest, pair[1])
			if j < 0 {
				break
			}
			if inner := strings.TrimSpace(rest[:j]); inner != "" {
				out = append(out, inner)
			}
			rest = rest[j+1:]
		}
	}
	return out
}

// NormalizeVulnClass 归一化漏洞类别到粗类,两段式:
//  1. 别名表:大小写/空白/连字符折叠后精确命中 vulnClassAliases;
//  2. 模糊段:未命中时,先取括号内内容、再取折叠后全文,各自先回查别名表、
//     再做关键词包含扫描(优先级见 vulnClassFuzzyKeywords);
//  3. 都不中:保持折叠后原样小写(仍参与判等,只是不做跨名目合并)。
func NormalizeVulnClass(v string) string {
	key := normalizeVulnClassKey(v)
	if canon, ok := vulnClassAliases[key]; ok {
		return canon
	}
	candidates := append(parenContents(key), key)
	for _, c := range candidates {
		if canon, ok := vulnClassAliases[c]; ok {
			return canon
		}
	}
	for _, c := range candidates {
		if canon, ok := fuzzyVulnClass(c); ok {
			return canon
		}
	}
	return key
}

// severityRank 把 severity 映射为可比大小;未知/空为 0(最低)。
func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// MaxSeverity 返回两者中更高的 severity;平级或都未知时返回 a。
func MaxSeverity(a, b string) string {
	if severityRank(b) > severityRank(a) {
		return b
	}
	return a
}

// appendMergeSegment 在 evidence 尾部追加一个带来源标记的段落;note 非空时
// 附加到段标记里(目前用于 vulnclass 措辞漂移审计)。
func appendMergeSegment(evidence, label, body string, now time.Time, intentID int64, worker, note string) string {
	marker := fmt.Sprintf("\n\n--- [%s] 合并于 %s", label, now.UTC().Format(time.RFC3339))
	if intentID > 0 {
		marker += fmt.Sprintf(", intent #%d", intentID)
	}
	if worker = strings.TrimSpace(worker); worker != "" {
		marker += ", by " + worker
	}
	if note != "" {
		marker += note
	}
	return evidence + marker + " ---\n" + body
}

// mergeFindingFields 计算合并后的 severity/summary/evidence(纯函数,可测):
// severity 取最高;新 summary 与旧不同则以"最新深度利用"为准替换,旧的挪进
// evidence 存档;新 evidence 作为带时间戳与 intent 标记的段落追加,原文保留。
// 若本次上报 vulnclass 原文与既有行不同(归一化后才相等,纯属模型措辞漂移),
// 在追加的段标记里标注原文,便于审计。
func mergeFindingFields(old DBFinding, in RecordFindingInput, now time.Time) (severity, summary, evidence string) {
	severity = MaxSeverity(old.Severity, in.Severity)
	summary = old.Summary
	evidence = old.Evidence
	drift := ""
	if iv, ov := strings.TrimSpace(in.VulnClass), strings.TrimSpace(old.VulnClass); iv != "" && ov != "" && iv != ov {
		drift = fmt.Sprintf(", 本次 vulnclass 原文 %q", iv)
	}
	appended := false
	if ns := strings.TrimSpace(in.Summary); ns != "" && ns != strings.TrimSpace(old.Summary) {
		if s := strings.TrimSpace(old.Summary); s != "" {
			evidence = appendMergeSegment(evidence, "原摘要", s, now, in.IntentID, in.Worker, drift)
			appended = true
		}
		summary = in.Summary
	}
	if e := strings.TrimSpace(in.Evidence); e != "" {
		evidence = appendMergeSegment(evidence, "新证据", in.Evidence, now, in.IntentID, in.Worker, drift)
		appended = true
	}
	// 无段落可挂但确有措辞漂移:单独留一个审计段,避免漂移痕迹丢失。
	if drift != "" && !appended {
		evidence = appendMergeSegment(evidence, "新证据", "(仅 vulnclass 措辞漂移,无新证据正文)", now, in.IntentID, in.Worker, drift)
	}
	return
}

// findMergeCandidateTx 在事务内(调用方须已持任务级证据锁)查可合并的既有
// finding:同 task、status IN ('confirmed','pending')(终态排除,依据见文件头
// 注释)、归一化 vulnclass 相同、主资产对等(候选 asset_ids->0 = 输入 AssetIDs
// 第一个)、且有探索节点。命中多行时取最新一条。
// 主资产对等而非包含:候选 [ep,svc] 与输入 [svc] 不是同一入口(svc 只是候选的
// 次资产),包含判等会把挂在 service 上的异入口漏洞误并进来;[ep,svc] 与 [ep]
// 主资产相同,仍正确合并。asset_ids 为空或输入主资产为空时 ->0 为 NULL,天然
// 不参与合并。
func findMergeCandidateTx(tx *sql.Tx, in RecordFindingInput) (*DBFinding, error) {
	if in.TaskID <= 0 || len(in.AssetIDs) == 0 {
		return nil, nil
	}
	want := NormalizeVulnClass(in.VulnClass)
	rows, err := tx.Query(`SELECT id, node_id, vulnclass, severity, summary, evidence
FROM findings
WHERE task_id=$1 AND status IN ($2, $3) AND node_id IS NOT NULL
  AND asset_ids->0 = to_jsonb($4::bigint)
ORDER BY id DESC
FOR UPDATE`, in.TaskID, FindingConfirmed, FindingPending, in.AssetIDs[0])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f DBFinding
		if err := rows.Scan(&f.ID, &f.NodeID, &f.VulnClass, &f.Severity, &f.Summary, &f.Evidence); err != nil {
			return nil, err
		}
		if NormalizeVulnClass(f.VulnClass) == want {
			return &f, rows.Err()
		}
	}
	return nil, rows.Err()
}

// mergeFindingTx 执行合并:更新既有 finding 行与节点 payload,追加血缘边与
// 流量快照,返回指向既有记录的 RecordedFinding(Merged=true)。
func mergeFindingTx(tx *sql.Tx, cand *DBFinding, in RecordFindingInput, prepared []PreparedTrafficEvidence) (*RecordedFinding, error) {
	severity, summary, evidence := mergeFindingFields(*cand, in, time.Now())
	if _, err := tx.Exec(`UPDATE findings SET severity=$2, summary=$3, evidence=$4 WHERE id=$1`,
		cand.ID, severity, summary, evidence); err != nil {
		return nil, err
	}
	// 节点 payload 同步(与 setFindingCol 同款 jsonb_set),保持按任务 发现 Tab
	// (读节点 payload)与 findings 表一致。
	if _, err := tx.Exec(`UPDATE exploration_nodes
SET payload = jsonb_set(jsonb_set(jsonb_set(payload, '{severity}', to_jsonb($2::text)),
	'{summary}', to_jsonb($3::text)), '{evidence,poc}', to_jsonb($4::text))
WHERE id=$1 AND kind='finding'`, *cand.NodeID, severity, summary, evidence); err != nil {
		return nil, err
	}
	// 血缘:新 intent 的 yields 边指向既有节点(保留"同一入口被反复加深"的历史)。
	// 目标节点必须属于本 exploration;边主键天然幂等。
	if in.IntentID > 0 {
		if _, err := tx.Exec(`INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id)
SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM exploration_nodes WHERE id=$4 AND exploration_id=$1)
ON CONFLICT DO NOTHING`, in.ExplorationID, in.IntentID, RelYields, *cand.NodeID); err != nil {
			return nil, err
		}
	}
	if err := AddFindingTrafficTx(tx, cand.ID, prepared); err != nil {
		return nil, err
	}
	// 文本证据/summary 变化同样使既有报告过期(与流量绑定共用一个版本列)。
	if err := bumpEvidenceVersionTx(tx, cand.ID); err != nil {
		return nil, err
	}
	traffic, err := FindingTrafficTx(tx, cand.ID)
	if err != nil {
		return nil, err
	}
	return &RecordedFinding{
		FindingID:           cand.ID,
		NodeID:              *cand.NodeID,
		Traffic:             traffic,
		Merged:              true,
		MergedIntoFindingID: cand.ID,
		MergedIntoNodeID:    *cand.NodeID,
	}, nil
}
