// credentials.go 装配凭据一等实体的三个 host 工具(内网渗透期 2,
// INTRANET-PIVOT-DESIGN.md §4.4):add_credential/list_credentials/
// credential_reuse_plan。与期 1a 会话工具同模式:普通工具调用,自动过 guard
// 审批、自动进 activity 留痕;seed 进 tools 表、默认绑 worker。
package server

import (
	"context"
	"encoding/json"
	"log"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// initCredentials 建凭据落库访问层(secret AES-GCM,密钥由 jwtKey 派生,
// 与 sessions 不同域标签)。失败只记日志,不拖垮启动。
func (s *Server) initCredentials() {
	if s.m.pg == nil {
		return
	}
	s.credStore = db.NewCredentialStore(s.m.pg, s.jwtKey)
}

// credentialTools 返回三个凭据工具。credStore 为 nil(DB 未就绪)时不提供。
func (s *Server) credentialTools() []actool.CoreTool {
	if s.credStore == nil {
		return nil
	}
	return []actool.CoreTool{
		s.addCredentialTool(),
		s.listCredentialsTool(),
		s.credentialReusePlanTool(),
	}
}

// addCredentialTool 把从目标收割的凭据登记为平台管理的一等实体(加密落库)。
func (s *Server) addCredentialTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "add_credential",
		Description: "【凭据登记】把从目标读到的口令/hash/密钥/ticket 登记为平台管理的凭据(secret AES-GCM 加密落库)。" +
			"一切收割到的凭据都应登记:复用打分(credential_reuse_plan)依赖它推断可横向的目标。" +
			"来源(source)写清楚从哪来(如 \"sqli dump users 表\" / \"/etc/shadow\" / \"mimikatz lsass dump\")," +
			"打分会把 source 里的 ssh/rdp/smb 等服务关键词纳入匹配。实测能登录后用 verify=true 重新登记或让平台标记 verified。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"username":      strParam("用户名(可空,如 ticket/token 无用户名时留空)"),
				"cred_type":     strParam("凭据类型:password / hash_nt / hash_lm / hash_sha1 / ticket / ssh_key / token"),
				"secret":        strParam("凭据内容(口令明文/hash/私钥/ticket 串;加密存储,不明文落库)"),
				"domain":        strParam("域(可选,如 CORP 或 corp.local;域凭据在复用打分中大幅加分)"),
				"source":        strParam("来源描述(必填):从哪台主机/什么方式得到,如 \"10.0.0.5 /etc/shadow\"、\"sqli dump users 表\""),
				"host_asset_id": map[string]any{"type": "integer", "description": "凭据来源主机的资产 id(可选;用于同网段打分)"},
			},
			"required": []any{"cred_type", "secret", "source"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				Username    string `json:"username"`
				CredType    string `json:"cred_type"`
				Secret      string `json:"secret"`
				Domain      string `json:"domain"`
				Source      string `json:"source"`
				HostAssetID int64  `json:"host_asset_id"`
			}
			_ = json.Unmarshal(in, &a)
			if !db.ValidCredType(a.CredType) {
				return actool.Errorf("非法 cred_type " + a.CredType + "(password/hash_nt/hash_lm/hash_sha1/ticket/ssh_key/token)"), nil
			}
			if a.Secret == "" {
				return actool.Errorf("secret 必填"), nil
			}
			if a.Source == "" {
				return actool.Errorf("source 必填(复用打分与溯源都依赖它)"), nil
			}
			ri := agent.RunInfoFrom(ctx)
			if ri.TaskID <= 0 {
				return actool.Errorf("凭据必须在任务上下文登记(非任务运行无 task 归属)"), nil
			}
			rec := &db.CredentialRecord{
				TaskID: ri.TaskID, HostAssetID: a.HostAssetID, Username: a.Username,
				CredType: a.CredType, Secret: a.Secret, Domain: a.Domain, Source: a.Source,
			}
			id, err := s.credStore.Create(ctx, rec)
			if err != nil {
				return actool.Errorf("凭据落库失败:" + err.Error()), nil
			}
			return jsonResult(map[string]any{"credential_id": id})
		},
	})
}

// listCredentialsTool 返回本任务凭据的脱敏列表(secret 只露前后各 2 字符)。
func (s *Server) listCredentialsTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "list_credentials",
		Description: "列出本任务已登记的全部凭据(脱敏:secret 只返回前后 2 字符掩码)。" +
			"横向前先看已有凭据,避免重复收割;要打具体目标用 credential_reuse_plan 拿复用建议。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    func(json.RawMessage) bool { return true },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, _ json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			ri := agent.RunInfoFrom(ctx)
			if ri.TaskID <= 0 {
				return actool.Errorf("list_credentials 需要任务上下文"), nil
			}
			creds, err := s.credStore.ListByTask(ctx, ri.TaskID)
			if err != nil {
				return actool.Errorf("读取凭据失败:" + err.Error()), nil
			}
			items := []map[string]any{}
			for _, c := range creds {
				items = append(items, map[string]any{
					"id": c.ID, "username": c.Username, "cred_type": c.CredType,
					"secret_masked": db.MaskSecret(c.Secret),
					"domain":        c.Domain, "source": c.Source, "verified": c.Verified,
					"host_asset_id": c.HostAssetID, "created_at": c.CreatedAt,
				})
			}
			return jsonResult(map[string]any{"credentials": items, "count": len(items)})
		},
	})
}

// credentialReusePlanTool 对本任务凭据 × 本任务资产跑服务端复用打分,返回 top 10。
func (s *Server) credentialReusePlanTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "credential_reuse_plan",
		Description: "【凭据复用打分】对本任务已登记凭据 × 本任务已发现主机资产跑复用打分(纯规则:同网段/域凭据/服务重叠/PtH/已验证)," +
			"返回得分最高的 top 10 条横向建议(凭据 × 目标资产 + 每条加分的理由)。建议只代表成功率先验," +
			"实际横向(ssh/wmi/psexec/喷洒)属高动静动作,会过 guard 人工确认且须在 RoE 范围内。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    func(json.RawMessage) bool { return true },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, _ json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			ri := agent.RunInfoFrom(ctx)
			if ri.TaskID <= 0 {
				return actool.Errorf("credential_reuse_plan 需要任务上下文"), nil
			}
			creds, err := s.credStore.ListByTask(ctx, ri.TaskID)
			if err != nil {
				return actool.Errorf("读取凭据失败:" + err.Error()), nil
			}
			if len(creds) == 0 {
				return actool.Text("本任务还没有登记凭据(add_credential 先登记),无复用建议"), nil
			}
			assets, err := s.m.pg.Assets().QueryByTask(ri.TaskID, "ip", 2000, 0)
			if err != nil {
				return actool.Errorf("读取资产失败:" + err.Error()), nil
			}
			cands, byID := reuseCandidates(assets)
			type row struct {
				CredID   int64    `json:"cred_id"`
				Username string   `json:"username"`
				CredType string   `json:"cred_type"`
				AssetID  int64    `json:"asset_id"`
				IP       string   `json:"ip"`
				Score    int      `json:"score"`
				Reasons  []string `json:"reasons"`
			}
			flat := []row{}
			for _, c := range creds {
				var src *db.ReuseCandidate
				if c.HostAssetID != 0 {
					src = byID[c.HostAssetID]
				}
				for _, sug := range db.ScoreReuse(c, src, cands) {
					flat = append(flat, row{c.ID, c.Username, c.CredType, sug.AssetID, sug.IP, sug.Score, sug.Reasons})
				}
			}
			// 全局排序:得分降序,同分 cred_id/asset_id 升序(确定性)。
			for i := 0; i < len(flat); i++ {
				for j := i + 1; j < len(flat); j++ {
					if flat[j].Score > flat[i].Score ||
						(flat[j].Score == flat[i].Score && (flat[j].CredID < flat[i].CredID ||
							(flat[j].CredID == flat[i].CredID && flat[j].AssetID < flat[i].AssetID))) {
						flat[i], flat[j] = flat[j], flat[i]
					}
				}
			}
			if len(flat) > 10 {
				flat = flat[:10]
			}
			return jsonResult(map[string]any{
				"suggestions": flat, "credential_count": len(creds), "asset_count": len(cands),
			})
		},
	})
}

// reuseCandidates 把 ip 资产投影为打分候选(服务名取自 open_ports)。
func reuseCandidates(assets []*db.Asset) ([]db.ReuseCandidate, map[int64]*db.ReuseCandidate) {
	out := []db.ReuseCandidate{}
	byID := map[int64]*db.ReuseCandidate{}
	for _, a := range assets {
		if a == nil || a.IP == "" {
			continue
		}
		services := []string{}
		for _, p := range a.OpenPorts {
			if svc, ok := p["service"].(string); ok && svc != "" {
				services = append(services, svc)
			}
		}
		out = append(out, db.ReuseCandidate{
			AssetID: a.ID, IP: a.IP, CSegment: a.CSegment, Services: services,
		})
		byID[a.ID] = &out[len(out)-1]
	}
	return out, byID
}

// seedCredentialTools 把三个凭据工具 seed 进 tools 表、默认绑定 worker
// (与 seedSessionTools 同一套 SeedTool 首插入语义:老库的用户编辑不被覆盖)。
func (s *Server) seedCredentialTools() {
	workerAgents, _ := json.Marshal([]string{"worker"})
	for _, t := range s.credentialTools() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.PG().SeedTool(t.Name(), t.Description(), schema, workerAgents); err != nil {
			log.Printf("[tools] seed %s 失败: %v", t.Name(), err)
		}
	}
}
