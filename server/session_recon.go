// session_recon.go 内网被动侦察工具(内网渗透期 2,INTRANET-PIVOT-DESIGN.md §5 期 2):
// 经立足点会话逐条执行【只读】侦察命令包(ip addr/ip route/arp/hosts/netstat,
// 目标无 ip 命令自动回退 ifconfig/route -n),recon 包解析结构化结果后:
//
//	① 新 IP/邻居 → ip 资产、监听端口 → service 资产(复用 insert_assets 的落库路径);
//	② 高价值推断与结构化报告 record_fact 进探索图;
//	③ 返回摘要。
//
// 设计判据照 chains pivot 骨架「被动六表」(agent/chainskel/pivot.md):先不发包,
// 被动拿到什么算什么;逐条执行是为了绕开 session_exec 64KB 输出截断。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/recon"
	actool "github.com/Autumn-27/norma/tool"
)

// reconCommands 被动侦察命令包(全部只读;每条单独执行单独取输出,避开 64KB 截断)。
// shell 级 || 回退:目标无 ip/ss 时自动落 ifconfig/route -n/netstat。
var reconCommands = []struct {
	key string
	cmd string
}{
	{"nics", "ip -o -4 addr show 2>/dev/null || ifconfig -a 2>/dev/null"},
	{"routes", "ip route 2>/dev/null || route -n 2>/dev/null"},
	{"neighbors", "ip neigh 2>/dev/null || arp -a 2>/dev/null"},
	{"hosts", "cat /etc/hosts 2>/dev/null"},
	{"conns", "(ss -ant 2>/dev/null; ss -anu 2>/dev/null) || netstat -an 2>/dev/null"},
}

// sessionReconTool 经会话执行被动侦察命令包并结构化回库。
func (s *Server) sessionReconTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "session_recon",
		Description: "【被动优先】经立足点会话在目标侧执行【只读】被动侦察命令包(ip addr / ip route / arp -a / /etc/hosts / netstat," +
			"目标无 ip 命令自动回退 ifconfig/route -n),不发任何探测包。" +
			"输出结构化报告(网卡/路由/邻居/hosts/监听端口/已建立对端)+ 高价值推断(非默认静态路由指向的网段、" +
			"已证明可通的对端主机、多网卡跳板);" +
			"发现的存活 IP 自动登记为 ip 资产、监听端口登记为 service 资产,报告与推断 record_fact 进探索图。" +
			"被动信息不够再考虑主动慢扫(另过 guard)。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "integer", "description": "register_session 返回的会话 id"},
			},
			"required": []any{"session_id"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				SessionID int64 `json:"session_id"`
			}
			_ = json.Unmarshal(in, &a)
			sess, err := s.lookupSession(a.SessionID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 逐条执行:每条单独取输出(绕开 64KB 截断),失败不中断整体——
			// 被动侦察的原则是「拿到什么算什么」,一条命令挂了其余照常。
			outputs := map[string]string{}
			cmdErrs := map[string]string{}
			for _, c := range reconCommands {
				stdout, _, err := sess.Exec(ctx, c.cmd, 15*time.Second)
				if err != nil {
					cmdErrs[c.key] = err.Error()
					continue
				}
				outputs[c.key] = stdout
			}
			s.sessStore.Touch(ctx, a.SessionID)

			report := &recon.ReconReport{
				NICs:         recon.ParseNICsAuto(outputs["nics"]),
				Routes:       recon.ParseRoutesAuto(outputs["routes"]),
				Neighbors:    recon.ParseNeighbors(outputs["neighbors"]),
				HostsEntries: recon.ParseHosts(outputs["hosts"]),
			}
			report.Listening, report.Peers = recon.ParseConnTable(outputs["conns"])
			inferences := recon.Infer(report)

			ri := agent.RunInfoFrom(ctx)
			assetIDs, assetSummary, assetErrs := s.reconInsertAssets(ctx, ri.TaskID, report)
			factID := s.reconRecordFact(ri, a.SessionID, report, inferences, assetIDs)

			out := map[string]any{
				"report":     report,
				"inferences": inferences,
				"summary":    reconSummary(report, inferences),
			}
			if assetSummary != "" {
				out["assets"] = assetSummary
			}
			if factID > 0 {
				out["fact_id"] = factID
			}
			if len(cmdErrs) > 0 {
				out["command_errors"] = cmdErrs
			}
			if len(assetErrs) > 0 {
				out["asset_errors"] = assetErrs
			}
			return jsonResult(out)
		},
	})
}

// reconSummary 给模型的一句话摘要。
func reconSummary(r *recon.ReconReport, inf []recon.Inference) string {
	static := 0
	for _, rt := range r.Routes {
		if rt.IsStatic {
			static++
		}
	}
	return fmt.Sprintf("网卡 %d / 路由 %d(静态 %d)/ 邻居 %d / hosts %d / 监听 %d / 已建立对端 %d;高价值推断 %d 条",
		len(r.NICs), len(r.Routes), static, len(r.Neighbors), len(r.HostsEntries),
		len(r.Listening), len(r.Peers), len(inf))
}

// reconInsertAssets 把侦察发现落资产库(best-effort):邻居/对端/hosts/本机 NIC 的
// 新 IP → ip 资产;监听端口 → 本机主 IP 上的 service 资产。返回锚定资产 id 列表、
// 摘要与错误(逐条错误不中断整体)。
func (s *Server) reconInsertAssets(ctx context.Context, taskID int64, r *recon.ReconReport) ([]int64, string, []string) {
	if s.m.Assets() == nil || taskID <= 0 {
		return nil, "", nil
	}
	assets := s.m.Assets()
	ids := []int64{}
	errs := []string{}
	seenIP := map[string]bool{}
	ipCount := 0
	addIP := func(ip string) {
		if ip == "" || seenIP[ip] {
			return
		}
		seenIP[ip] = true
		id, err := assets.UpsertIP(db.UpsertIPReq{IP: ip, TaskID: taskID})
		if err != nil {
			errs = append(errs, fmt.Sprintf("ip %s: %v", ip, err))
			return
		}
		ids = append(ids, id)
		ipCount++
	}

	// 本机非回环 NIC IP(双网卡全部登记);主 IP(第一个)挂监听端口的 service 资产。
	hostIP := ""
	for _, n := range r.NICs {
		addr := n.IP
		if strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "169.254.") {
			continue
		}
		if hostIP == "" {
			hostIP = addr
		}
		addIP(addr)
	}
	for _, nb := range r.Neighbors {
		addIP(nb.IP)
	}
	for _, p := range r.Peers {
		addIP(p.IP)
	}
	for _, h := range r.HostsEntries {
		if strings.HasPrefix(h.IP, "127.") {
			continue
		}
		addIP(h.IP)
	}

	svcCount := 0
	if hostIP != "" {
		for _, l := range r.Listening {
			name := recon.ServiceOf(l.Port)
			if name == "" {
				name = fmt.Sprintf("unknown-%d", l.Port)
			}
			id, err := assets.UpsertOtherService(db.UpsertOtherServiceReq{
				IP: hostIP, Port: l.Port, ServiceName: name, TaskID: taskID,
			})
			if err != nil {
				errs = append(errs, fmt.Sprintf("service %s:%d: %v", hostIP, l.Port, err))
				continue
			}
			ids = append(ids, id)
			svcCount++
		}
	}
	summary := fmt.Sprintf("登记 ip 资产 %d 个、service 资产 %d 个", ipCount, svcCount)
	if len(ids) > 20 {
		ids = ids[:20] // 锚定列表控制规模
	}
	// 期 4:出当前授权范围的新 IP 按 /24 登记 pending_scope 等人工审批(见 pendingscope.go)。
	s.reconRegisterPendingScope(taskID, seenIP)
	return ids, summary, errs
}

// reconRecordFact 把侦察报告与推断写进探索图(record_fact 同路径:fact 节点 +
// intent→yields 边)。非任务运行/图不可用则跳过(返回 0)。
func (s *Server) reconRecordFact(ri agent.RunInfo, sessionID int64, r *recon.ReconReport, inf []recon.Inference, anchors []int64) int64 {
	if ri.ExplorationID <= 0 || s.m.pg == nil {
		return 0
	}
	lines := []string{}
	for _, i := range inf {
		line := "· " + i.Summary
		if i.Detail != "" {
			line += " — " + i.Detail
		}
		lines = append(lines, line)
	}
	detail := reconSummary(r, inf)
	if len(lines) > 0 {
		detail += "\n高价值推断:\n" + strings.Join(lines, "\n")
	}
	payload := map[string]any{
		"summary":    fmt.Sprintf("session %d 内网被动侦察:%s", sessionID, reconSummary(r, inf)),
		"detail":     detail,
		"evidence":   "session_recon 被动命令包(ip addr/ip route/arp -a/hosts/netstat,只读)",
		"confidence": "observed",
	}
	store := s.m.pg.Exploration(ri.ExplorationID)
	id, err := store.AddNode(db.KindFact, payload, 5, "confirmed", "worker", anchors)
	if err != nil {
		return 0
	}
	if ri.IntentID > 0 {
		if node, nerr := store.GetNode(ri.IntentID); nerr == nil && node != nil && node.Kind == db.KindIntent {
			_ = store.Link(ri.IntentID, db.RelYields, id)
		}
	}
	return id
}
