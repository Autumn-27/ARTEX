// credential_score.go 是凭据复用打分(服务端版,INTRANET-PIVOT-DESIGN.md §4.4)。
// 参考 PivotHub cred.js:44-50 的前端 naive 打分,这里做成纯函数 + 数据表驱动规则:
// 每条规则一个分值与可读 reason,扩充规则只改表,不动逻辑。零 DB 依赖,直接单测。
package db

import (
	"fmt"
	"sort"
	"strings"
)

// ReuseCandidate 是复用打分的候选主机资产视图(调用方从 assets 表投影)。
type ReuseCandidate struct {
	AssetID  int64    `json:"asset_id"`
	IP       string   `json:"ip"`
	CSegment string   `json:"c_segment"` // 如 "10.0.0.0/24"
	Services []string `json:"services"`  // 资产上已发现的服务名(大小写不敏感)
}

// ReuseSuggestion 是一条复用建议:目标资产 + 得分 + 每条加分的可读理由。
type ReuseSuggestion struct {
	AssetID int64    `json:"asset_id"`
	IP      string   `json:"ip"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}

// sourceServiceHints 是「凭据来源描述关键词 → 服务名」的数据表:source 里出现
// 关键词即认为该凭据可能适用于对应服务(如 "ssh 私钥" → SSH)。扩充只加行。
var sourceServiceHints = []struct {
	Keyword string
	Service string
}{
	{"ssh", "SSH"},
	{"rdp", "RDP"},
	{"smb", "SMB"},
	{"winrm", "WinRM"},
	{"mysql", "MySQL"},
	{"mssql", "MSSQL"},
	{"postgres", "PostgreSQL"},
	{"redis", "Redis"},
	{"ftp", "FTP"},
	{"telnet", "Telnet"},
	{"vnc", "VNC"},
	{"ldap", "LDAP"},
	{"kerberos", "Kerberos"},
	{"http", "HTTP"},
}

// sourceServices 从 source 描述提取命中的服务名(去重,保序)。
func sourceServices(source string) []string {
	lower := strings.ToLower(source)
	out := []string{}
	seen := map[string]bool{}
	for _, h := range sourceServiceHints {
		if seen[h.Service] || !strings.Contains(lower, h.Keyword) {
			continue
		}
		seen[h.Service] = true
		out = append(out, h.Service)
	}
	return out
}

// reuseRule 是一条打分规则:check 返回命中时的可读 reason(空串 = 不命中)。
// 规则表驱动:加分项扩充只改 reuseRules,不动 ScoreReuse 主流程。
type reuseRule struct {
	points int
	check  func(cred *CredentialRecord, src, cand *ReuseCandidate) string
}

// reuseRules 打分规则表(分值来自设计 §4.4/任务约定):
var reuseRules = []reuseRule{
	{40, func(_ *CredentialRecord, src, cand *ReuseCandidate) string {
		if src != nil && src.CSegment != "" && src.CSegment == cand.CSegment {
			return fmt.Sprintf("与凭据来源主机同网段(%s)", cand.CSegment)
		}
		return ""
	}},
	{35, func(cred *CredentialRecord, _, _ *ReuseCandidate) string {
		if strings.TrimSpace(cred.Domain) != "" {
			return fmt.Sprintf("域凭据(domain=%s),可用于域内横向", cred.Domain)
		}
		return ""
	}},
	{20, func(cred *CredentialRecord, _, cand *ReuseCandidate) string {
		have := map[string]bool{}
		for _, s := range cand.Services {
			have[strings.ToUpper(s)] = true
		}
		overlap := []string{}
		for _, s := range sourceServices(cred.Source) {
			if have[strings.ToUpper(s)] {
				overlap = append(overlap, s)
			}
		}
		if len(overlap) > 0 {
			return "目标服务与凭据来源服务重叠:" + strings.Join(overlap, "/")
		}
		return ""
	}},
	{15, func(cred *CredentialRecord, _, _ *ReuseCandidate) string {
		if cred.CredType == CredHashNT {
			return "NTLM hash 可直接传递(PtH),无需明文"
		}
		return ""
	}},
	{25, func(cred *CredentialRecord, _, _ *ReuseCandidate) string {
		if cred.Verified {
			return "凭据已实测验证可用"
		}
		return ""
	}},
}

// ScoreReuse 对一条凭据 × 候选主机资产列表打分,返回按得分降序的建议(同分按
// asset_id 升序保证确定性)。src 是凭据来源主机的资产视图(可 nil——未知来源
// 主机时同网段规则自然不命中)。来源主机自身(score 无意义)与零分候选被过滤。
func ScoreReuse(cred *CredentialRecord, src *ReuseCandidate, cands []ReuseCandidate) []ReuseSuggestion {
	out := []ReuseSuggestion{}
	for i := range cands {
		cand := &cands[i]
		if cand.AssetID == cred.HostAssetID && cred.HostAssetID != 0 {
			continue // 来源主机自身不构成横向目标
		}
		score := 0
		reasons := []string{}
		for _, rule := range reuseRules {
			if reason := rule.check(cred, src, cand); reason != "" {
				score += rule.points
				reasons = append(reasons, fmt.Sprintf("%s(+%d)", reason, rule.points))
			}
		}
		if score <= 0 {
			continue
		}
		out = append(out, ReuseSuggestion{
			AssetID: cand.AssetID, IP: cand.IP, Score: score, Reasons: reasons,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].AssetID < out[j].AssetID
	})
	return out
}
