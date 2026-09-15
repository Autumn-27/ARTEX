// health.go 是四件套之 health：周期真实探活（平台侧 server 进程 + 隧道入口
// 可连 + 每若干轮一次经 socks 的实际拨测），死了标 error 并用 deploy_params
// 重新 Deploy（指数退避，上限 3 次）——死链绝不当活上游。另含 Teardown（远端
// 进程经会话按 pid 杀、杀平台 server、删 stage、state=stopped）与 Cleanup
// （任务停止/进程退出时供 manager 调用）。
package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/session"
)

// 巡检参数。
const (
	HealthInterval     = 30 * time.Second
	decisiveEveryNTick = 5                // 每 5 轮做一次经 socks 的决定性拨测
	maxRedeploys       = 3                // 自动重拉上限
	redeployBaseDelay  = 30 * time.Second // 退避基数：30s → 60s → 120s
)

type redeployState struct {
	count     int
	nextAt    time.Time
	exhausted bool // 已达上限（日志只打一次）
}

// StartHealth 启动周期巡检 goroutine,ctx 取消即退出（随 server 生命周期）。
func (m *Manager) StartHealth(ctx context.Context) {
	retry := map[int64]*redeployState{}
	tick := 0
	go func() {
		t := time.NewTicker(HealthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				tick++
				m.healthRound(ctx, tick%decisiveEveryNTick == 0, retry)
			}
		}
	}()
}

// healthRound 巡检一轮：alive 行做进程+入口探活（decisive 轮加实际拨测），失败
// 标 error 并进重拉；error 行（含平台重启后的孤儿）按退避预算尝试重拉。
func (m *Manager) healthRound(ctx context.Context, decisive bool, retry map[int64]*redeployState) {
	recs, err := m.store.ListActive(ctx)
	if err != nil {
		log.Printf("[tunnel] 巡检读台账失败： %v", err)
		return
	}
	for _, rec := range recs {
		if rec.State == db.TunnelDeploying {
			continue // 部署进行中（同步在工具调用里），不插手
		}
		var p Plan
		if err := json.Unmarshal(rec.DeployParams, &p); err != nil {
			_ = m.store.UpdateState(ctx, rec.ID, db.TunnelError, "deploy_params 解析失败： "+err.Error())
			m.notifyState(rec.TaskID)
			continue
		}
		if rec.State == db.TunnelAlive {
			err = m.checkAlive(&p)
			if err == nil && decisive {
				err = m.checkDecisive(&p)
			}
			if err == nil {
				continue
			}
			log.Printf("[tunnel] 隧道 %d 探活失败： %v", rec.ID, err)
			_ = m.store.UpdateState(ctx, rec.ID, db.TunnelError, err.Error())
			m.notifyState(rec.TaskID)
		}
		m.maybeRedeploy(ctx, rec.ID, &p, retry)
	}
}

// checkAlive 轻量探活：平台 server 进程在 + 隧道入口可连。
func (m *Manager) checkAlive(p *Plan) error {
	if p.ServerPID > 0 && !processAlive(p.ServerPID) {
		return fmt.Errorf("平台侧 chisel server(pid %d）已退出", p.ServerPID)
	}
	conn, err := net.DialTimeout("tcp", m.entryAddr(p), 2*time.Second)
	if err != nil {
		return fmt.Errorf("隧道入口 %s 不可连（目标回连已断）: %w", m.entryAddr(p), err)
	}
	_ = conn.Close()
	return nil
}

// checkDecisive 决定性拨测：socks 场景经隧道真实 CONNECT 验证目标。
// suo5 入口带 user/pass 鉴权（--auth 必启用），用鉴权版拨测。
func (m *Manager) checkDecisive(p *Plan) error {
	if p.Kind != KindSocks || p.VerifyAddr == "" {
		return nil
	}
	if p.Adapter == AdapterSuo5 {
		return socks5DialAuth(m.entryAddr(p), p.VerifyAddr, p.AuthUser, p.AuthPass, verifyTimeout)
	}
	return socks5Dial(m.entryAddr(p), p.VerifyAddr, verifyTimeout)
}

// maybeRedeploy 用持久化的 deploy_params 重新 Deploy，指数退避，上限 3 次。
func (m *Manager) maybeRedeploy(ctx context.Context, id int64, p *Plan, retry map[int64]*redeployState) {
	rs := retry[id]
	if rs == nil {
		rs = &redeployState{}
		retry[id] = rs
	}
	if rs.count >= maxRedeploys {
		if !rs.exhausted {
			rs.exhausted = true
			log.Printf("[tunnel] 隧道 %d 已达重拉上限 %d 次，保持 error，等人工 tunnel_teardown/重部署", id, maxRedeploys)
		}
		return
	}
	if time.Now().Before(rs.nextAt) {
		return
	}
	rs.count++
	rs.nextAt = time.Now().Add(redeployBaseDelay << (rs.count - 1)) // 30s/60s/120s
	sess, ok := m.sessions.Get(p.ViaSessionID)
	if !ok {
		log.Printf("[tunnel] 隧道 %d 重拉失败：会话 %d 不在注册表", id, p.ViaSessionID)
		return
	}
	log.Printf("[tunnel] 隧道 %d 第 %d/%d 次自动重拉（deploy_params 持久化参数）…", id, rs.count, maxRedeploys)
	// 重拉 = 同一计划重新部署：先清旧层（远端可能残留），再起新层。
	if p.Adapter == AdapterSuo5 {
		// suo5 无目标侧进程；重拉 = 杀旧 CLI + 重投 payload（目标 payload 被删
		// 的场景在此自然覆盖）+ 重起 CLI + 重新决定性验证。
		m.killServer(id, p)
		if err := m.deploySuo5Into(ctx, id, sess, p); err != nil {
			_ = m.store.UpdateState(ctx, id, db.TunnelError, "重拉失败： "+err.Error())
			m.killServer(id, p)
			m.removePayload(ctx, sess, p)
			return
		}
	} else {
		m.killRemote(ctx, sess, p)
		m.killServer(id, p)
		if err := m.deployInto(ctx, id, sess, p); err != nil {
			_ = m.store.UpdateState(ctx, id, db.TunnelError, "重拉失败： "+err.Error())
			m.rollbackKeepRow(ctx, id, sess, p)
			return
		}
	}
	m.saveParams(ctx, id, p)
	if err := m.store.UpdateState(ctx, id, db.TunnelAlive, ""); err == nil {
		rs.count = 0 // 复活成功，退避计数清零
		log.Printf("[tunnel] 隧道 %d 重拉成功，已恢复 alive", id)
	}
	m.notifyState(p.TaskID)
}

// rollbackKeepRow 重拉失败时清层但保留台账行（error 状态等下一轮）。
func (m *Manager) rollbackKeepRow(ctx context.Context, id int64, sess session.Session, p *Plan) {
	if p.RemotePID > 0 {
		m.killRemote(ctx, sess, p)
	}
	m.killServer(id, p)
}

// Teardown 回收一条隧道：远端进程经会话按 pid 杀（查远端、核对 cmdline)→
// 杀平台 server → 删 stage 条目 → 删 authfile → state=stopped。幂等：已 stopped
// 直接返回。
func (m *Manager) Teardown(ctx context.Context, id int64) error {
	rec, err := m.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("隧道 %d 不存在", id)
	}
	if rec.State == db.TunnelStopped {
		return nil
	}
	var p Plan
	_ = json.Unmarshal(rec.DeployParams, &p)
	if p.Adapter == AdapterSuo5 {
		// suo5：删目标 payload + 杀平台 CLI（无目标侧进程可杀）。
		if p.PayloadPath != "" {
			if sess, ok := m.sessions.Get(p.ViaSessionID); ok {
				m.removePayload(ctx, sess, &p)
			} else {
				log.Printf("[tunnel] Teardown: 会话 %d 不在注册表，目标 payload %s 无法经会话清理", p.ViaSessionID, p.PayloadPath)
			}
		}
		m.killServer(id, &p)
		err = m.store.UpdateState(ctx, id, db.TunnelStopped, "")
		m.notifyState(rec.TaskID)
		return err
	}
	if p.RemotePID > 0 {
		if sess, ok := m.sessions.Get(p.ViaSessionID); ok {
			m.killRemote(ctx, sess, &p)
			if p.RemoteDir != "" {
				_, _, _ = sess.Exec(ctx, "rm -rf "+p.RemoteDir, probeTimeout)
			}
		} else {
			log.Printf("[tunnel] Teardown: 会话 %d 不在注册表，远端 pid %d 无法经会话清理（可能已随会话消亡）", p.ViaSessionID, p.RemotePID)
		}
	}
	m.killServer(id, &p)
	if p.StageToken != "" && m.stage != nil {
		m.stage.Delete(p.StageToken)
	}
	if p.AuthFile != "" {
		_ = os.Remove(p.AuthFile)
	}
	err = m.store.UpdateState(ctx, id, db.TunnelStopped, "")
	m.notifyState(rec.TaskID)
	return err
}

// Cleanup 回收一批隧道（任务停止/进程退出时供 manager 调用）。taskID=0 收全部
// 未终结隧道。best-effort，单条失败只记日志。
func (m *Manager) Cleanup(ctx context.Context, taskID int64) {
	var recs []*db.TunnelRecord
	var err error
	if taskID > 0 {
		recs, err = m.store.ListByTask(ctx, taskID)
	} else {
		recs, err = m.store.ListActive(ctx)
	}
	if err != nil {
		log.Printf("[tunnel] Cleanup 读台账失败： %v", err)
		return
	}
	for _, r := range recs {
		if r.State == db.TunnelStopped {
			continue
		}
		if err := m.Teardown(ctx, r.ID); err != nil {
			log.Printf("[tunnel] Cleanup 隧道 %d 失败： %v", r.ID, err)
		}
	}
}

// MarkOrphansError 平台重启后调用：所有 alive/deploying 台账行的平台侧进程已随
// 重启消亡，诚实标 error（不装活）；健康巡检会按 deploy_params 自动重拉。
func (m *Manager) MarkOrphansError(ctx context.Context) {
	recs, err := m.store.ListActive(ctx)
	if err != nil {
		return
	}
	for _, r := range recs {
		_ = m.store.UpdateState(ctx, r.ID, db.TunnelError, "平台重启，server 进程已消亡，待健康巡检自动重拉")
		m.notifyState(r.TaskID)
	}
}

// processAlive 检测平台侧进程存活（linux:/proc;windows:FindProcess 失败即不在；
// 其它 unix:Signal(0))。
func processAlive(pid int) bool {
	if runtime.GOOS == "linux" {
		_, err := os.Stat("/proc/" + strconv.Itoa(pid))
		return err == nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return true // FindProcess 打不开句柄时已在上一步返回
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// serverCmdlineMatches 杀台前核对 /proc/<pid>/cmdline 确为台账里的隧道进程
// （防 pid 复用误杀；非 linux 平台保守返回 false，只靠本进程台账句柄）。
// chisel 核对 authfile 路径；suo5 核对 CLI 名与 payload URL。
func serverCmdlineMatches(p *Plan) bool {
	if runtime.GOOS != "linux" || p.ServerPID <= 0 {
		return false
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(p.ServerPID) + "/cmdline")
	if err != nil {
		return false
	}
	cl := strings.ReplaceAll(string(data), "\x00", " ")
	if p.Adapter == AdapterSuo5 {
		return strings.Contains(cl, "suo5") && (p.PayloadURL == "" || strings.Contains(cl, p.PayloadURL))
	}
	return strings.Contains(cl, "chisel") && strings.Contains(cl, p.AuthFile)
}
