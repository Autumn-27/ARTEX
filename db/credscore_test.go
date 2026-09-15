package db

// credscore_test.go:复用打分纯函数单测(规则命中/排序/过滤/同分确定性)。

import (
	"strings"
	"testing"
)

func TestScoreReuse(t *testing.T) {
	cred := &CredentialRecord{
		HostAssetID: 1, Username: "administrator", CredType: CredHashNT,
		Domain: "CORP", Verified: true, Source: "10.0.0.5 mimikatz lsass dump smb",
	}
	src := &ReuseCandidate{AssetID: 1, IP: "10.0.0.5", CSegment: "10.0.0.0/24"}
	cands := []ReuseCandidate{
		{AssetID: 1, IP: "10.0.0.5", CSegment: "10.0.0.0/24", Services: []string{"SMB"}},      // 来源主机自身,应被过滤
		{AssetID: 2, IP: "10.0.0.9", CSegment: "10.0.0.0/24", Services: []string{"SMB"}},      // 40+35+20+15+25=135
		{AssetID: 3, IP: "172.16.0.9", CSegment: "172.16.0.0/24", Services: []string{"HTTP"}}, // 35+15+25=75
		{AssetID: 4, IP: "192.168.1.9", CSegment: "192.168.1.0/24"},                           // 35+15+25=75
	}
	sugs := ScoreReuse(cred, src, cands)
	if len(sugs) != 3 {
		t.Fatalf("期望 3 条建议(来源主机被过滤),得 %d: %+v", len(sugs), sugs)
	}
	if sugs[0].AssetID != 2 || sugs[0].Score != 135 {
		t.Fatalf("top1 应为 asset 2 / 135 分: %+v", sugs[0])
	}
	// 同分按 asset_id 升序(确定性)。
	if sugs[1].AssetID != 3 || sugs[2].AssetID != 4 || sugs[1].Score != 75 {
		t.Fatalf("同分排序错误: %+v", sugs[1:])
	}
	// 每条加分都有可读 reason。
	joined := strings.Join(sugs[0].Reasons, " ")
	for _, kw := range []string{"同网段", "域凭据", "重叠", "PtH", "已实测验证"} {
		if !strings.Contains(joined, kw) {
			t.Errorf("top1 reason 缺 %q: %s", kw, joined)
		}
	}
}

func TestScoreReuseNoDomainUnverified(t *testing.T) {
	// 本地明文口令、未验证:只剩同网段与服务重叠分。
	cred := &CredentialRecord{
		HostAssetID: 1, Username: "root", CredType: CredPassword,
		Source: "10.0.0.5 /etc/shadow ssh 登录",
	}
	src := &ReuseCandidate{AssetID: 1, IP: "10.0.0.5", CSegment: "10.0.0.0/24"}
	cands := []ReuseCandidate{
		{AssetID: 2, IP: "10.0.0.9", CSegment: "10.0.0.0/24", Services: []string{"ssh"}}, // 40+20=60
		{AssetID: 3, IP: "172.16.0.9", CSegment: "172.16.0.0/24"},                        // 0 分,过滤
	}
	sugs := ScoreReuse(cred, src, cands)
	if len(sugs) != 1 || sugs[0].AssetID != 2 || sugs[0].Score != 60 {
		t.Fatalf("本地口令打分错误: %+v", sugs)
	}
}

func TestScoreReuseNilSource(t *testing.T) {
	// 未知来源主机(host_asset_id 未填):同网段规则不命中,其余照常。
	cred := &CredentialRecord{CredType: CredHashNT, Source: "sqli dump users 表"}
	cands := []ReuseCandidate{
		{AssetID: 9, IP: "10.0.0.9", CSegment: "10.0.0.0/24"},
	}
	sugs := ScoreReuse(cred, nil, cands)
	if len(sugs) != 1 || sugs[0].Score != 15 {
		t.Fatalf("无来源主机时打分错误(应只有 PtH +15): %+v", sugs)
	}
}
