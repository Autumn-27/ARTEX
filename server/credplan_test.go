package server

// credplan_test.go:复用打分资产投影(纯函数,不依赖 PG)。

import (
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestReuseCandidates(t *testing.T) {
	assets := []*db.Asset{
		{ID: 1, Type: "ip", IP: "10.0.0.5", CSegment: "10.0.0.0/24",
			OpenPorts: []map[string]any{
				{"port": 445, "service": "SMB"},
				{"port": 135}, // 无 service 名,跳过
			}},
		{ID: 2, Type: "ip", IP: "", CSegment: "10.0.0.0/24"}, // 无 IP,过滤
		nil, // 防御:nil 行
	}
	cands, byID := reuseCandidates(assets)
	if len(cands) != 1 || cands[0].AssetID != 1 || cands[0].IP != "10.0.0.5" {
		t.Fatalf("投影错误: %+v", cands)
	}
	if len(cands[0].Services) != 1 || cands[0].Services[0] != "SMB" {
		t.Fatalf("服务名投影错误: %+v", cands[0].Services)
	}
	if byID[1] == nil || byID[2] != nil {
		t.Fatalf("byID 索引错误: %+v", byID)
	}
}
