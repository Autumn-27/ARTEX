// handoff.go 期 4:外网→内网任务移交。以一个立足点会话为起点建子任务:
// 初始 scope 只登记立足点主机 /32(source='manual'),只读继承原任务、parent_ref
// 父子关联,走 launchTask 同款建后流程。新网段一律走 pending_scope 人工审批
// (见 pendingscope.go),移交 goal 里把这条纪律写明。
package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
)

// handoffHostIP 确定立足点主机 IP:优先 host_asset_id 指向的资产(资产带解析后
// 的 IP),回落会话 URL 的 IP 字面量 host。两者都没有 → ""。
func (s *Server) handoffHostIP(rec *db.SessionRecord) string {
	if rec.HostAssetID > 0 && s.m.Assets() != nil {
		if assets, err := s.m.Assets().GetByIDs([]int64{rec.HostAssetID}); err == nil {
			for _, a := range assets {
				if a.IP != "" {
					return a.IP
				}
			}
		}
	}
	if u, err := url.Parse(rec.URL); err == nil {
		if h := u.Hostname(); net.ParseIP(h) != nil {
			return h
		}
	}
	return ""
}

// handoffGoal 构造移交 goal 文本(中文模板):立足点信息 + 已发现但未授权的
// pending 网段 + 已授权 scope + 内网纪律。
func handoffGoal(rec *db.SessionRecord, hostIP string, pending []db.PendingScope, scope []db.TaskScope) string {
	var b strings.Builder
	fmt.Fprintf(&b, "内网横向:以会话 #%d(%s,%s)所在的立足点主机 %s 为起点,向内网纵深拓展。\n",
		rec.ID, rec.Kind, rec.URL, hostIP)
	fmt.Fprintf(&b, "立足点:session_id=%d kind=%s url=%s host_asset_id=%d。\n",
		rec.ID, rec.Kind, rec.URL, rec.HostAssetID)
	if len(pending) > 0 {
		segs := make([]string, 0, len(pending))
		for _, p := range pending {
			segs = append(segs, p.Value)
		}
		fmt.Fprintf(&b, "原任务已发现但【未授权】的网段(严禁直接动,需人工批准):%s。\n", strings.Join(segs, "、"))
	}
	if len(scope) > 0 {
		items := make([]string, 0, len(scope))
		for _, sc := range scope {
			v := sc.Domain + sc.Net + sc.Value
			if v != "" {
				items = append(items, fmt.Sprintf("%s=%s", sc.Kind, v))
			}
		}
		if len(items) > 0 {
			fmt.Fprintf(&b, "原任务已授权 scope(仅背景参考,不自动继承授权):%s。\n", strings.Join(items, "、"))
		}
	}
	b.WriteString("\n纪律:\n")
	b.WriteString("1. 本任务初始授权 scope 只有立足点主机 " + hostIP + "/32;被动侦察(session_recon)发现的新网段一律先登记 pending_scope 等人工审批,批准前不得对新网段主动探测/发包。\n")
	b.WriteString("2. 被动优先:先用 session_recon 只读命令包拿网卡/路由/邻居/监听,被动信息不够再考虑主动慢扫(另过 guard 审批)。\n")
	b.WriteString("3. 需要出立足点的网络访问走 tunnel_deploy 四件套(部署隧道 → 任务级代理自动经 socks 进内网),不要直接在立足点外盲目扫描。\n")
	b.WriteString("4. 动静控制:经会话的命令注意 OPSEC(日志/进程/网络留痕),避免大批量高噪声动作。\n")
	return b.String()
}

// POST /api/sessions/{id}/handoff — 外网→内网任务移交,返回 {task_id}。
func (s *Server) sessionHandoff(w http.ResponseWriter, r *http.Request) {
	if !s.intranetSessionsReady(w) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rec, err := s.sessStore.Get(r.Context(), id)
	if err != nil {
		writeErr(w, 500, "会话读取失败: "+err.Error())
		return
	}
	if rec == nil {
		writeErr(w, 404, fmt.Sprintf("会话 %d 不存在", id))
		return
	}
	srcTask, ok := s.m.Task(strconv.FormatInt(rec.CreatedByTask, 10))
	if rec.CreatedByTask <= 0 || !ok {
		writeErr(w, 404, fmt.Sprintf("会话 %d 的来源任务 #%d 不存在", id, rec.CreatedByTask))
		return
	}
	hostIP := s.handoffHostIP(rec)
	if hostIP == "" {
		writeErr(w, 400, "无法确定立足点主机 IP(host_asset 无 IP 且会话 URL 非 IP 字面量)")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, 503, "asset store 未启用")
		return
	}
	pending, err := as.ListPendingScope(rec.CreatedByTask, false)
	if err != nil {
		writeErr(w, 500, "pending_scope 读取失败: "+err.Error())
		return
	}
	scope, err := as.ListTaskScope(rec.CreatedByTask)
	if err != nil {
		writeErr(w, 500, "task_scope 读取失败: "+err.Error())
		return
	}

	desc := fmt.Sprintf("内网横向(立足点会话 #%d %s)", rec.ID, hostIP)
	goal := handoffGoal(rec, hostIP, pending, scope)
	// 与编排 spawn_task 同路径:只读继承原任务 + parent_ref 父子关联 + launchTask。
	t, err := s.m.CreateTaskWithOptions(desc, goal, db.TaskCreateOptions{
		SourceTaskIDs: []int64{rec.CreatedByTask},
	})
	if err != nil {
		writeErr(w, 500, "建子任务失败: "+err.Error())
		return
	}
	t.ParentRef = srcTask.ID
	if newID, e := strconv.ParseInt(t.ID, 10, 64); e == nil {
		_ = s.m.PG().SetParentRef(newID, srcTask.ID)
		// 初始 scope 只登记立足点主机 /32(人工来源;guard 现查 scope,即时生效)。
		if _, e := as.AddAgentScope(newID, "ip", hostIP,
			fmt.Sprintf("handoff 立足点(session #%d)", rec.ID), "manual"); e != nil {
			writeErr(w, 500, "登记立足点 scope 失败: "+e.Error())
			return
		}
	}
	s.launchTask(t, desc+" "+goal, false)
	s.auditIntranet(r, "handoff session=%d host=%s → task=%s(来源任务 %s)", rec.ID, hostIP, t.ID, srcTask.ID)
	writeJSON(w, 200, map[string]any{"task_id": t.ID})
}
