// tunnel.go 装配多层代理隧道子系统（内网渗透期 3a,INTRANET-PIVOT-DESIGN.md
// §4.3/4.3a):tunnel.Manager（进程看护 + 健康巡检）+ 四个 host 工具
// (tunnel_probe/tunnel_deploy/tunnel_list/tunnel_teardown，默认绑 worker)。
// 隧道是长寿命受管资源：平台 server 进程、目标 client 进程、stage 投递条目全部
// 落 tunnels 台账；工具描述明确「用完 teardown，不要用裸 Bash 自起隧道进程」。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/tunnel"
	actool "github.com/Autumn-27/norma/tool"
)

// initTunnels 建隧道运行时：chisel 二进制解析（ARTEX_CHISEL_PATH > data/tools/chisel)、
// 台账访问层、端口审计挂钩、平台重启孤儿行标 error（诚实，不装活）、健康巡检启动。
// 失败只记日志，不拖垮启动。
func (s *Server) initTunnels(dataDir string) {
	if s.m.pg == nil || s.sessReg == nil {
		return
	}
	chiselPath := strings.TrimSpace(os.Getenv("ARTEX_CHISEL_PATH"))
	if chiselPath == "" {
		chiselPath = filepath.Join(dataDir, "tools", "chisel")
	}
	suo5Path := strings.TrimSpace(os.Getenv("ARTEX_SUO5_PATH"))
	if suo5Path == "" {
		suo5Path = filepath.Join(dataDir, "tools", "suo5")
	}
	suo5PayloadsDir := strings.TrimSpace(os.Getenv("ARTEX_SUO5_PAYLOADS_DIR"))
	if suo5PayloadsDir == "" {
		suo5PayloadsDir = filepath.Join(dataDir, "tools", "suo5-payloads")
	}
	mgr, err := tunnel.NewManager(tunnel.Options{
		Store:           db.NewTunnelStore(s.m.pg),
		Stage:           s.stage,
		Sessions:        s.sessReg,
		ChiselPath:      chiselPath,
		Suo5Path:        suo5Path,
		Suo5PayloadsDir: suo5PayloadsDir,
		DataDir:         dataDir,
	})
	if err != nil {
		log.Printf("[tunnel] disabled: %v", err)
		return
	}
	// 隧道 server 监听口登记进端口审计台账（受管资源原则：台账外监听才告警）。
	mgr.OnListenStart = registerManagedPort
	mgr.OnListenStop = unregisterManagedPort
	// 期 3b:隧道状态变化(新增/死亡/重拉/teardown)→ 热切换该任务 MITM 实例上游。
	mgr.OnStateChange = s.m.RefreshTaskProxy
	s.tunnels = mgr
	mgr.MarkOrphansError(s.ctx) // 平台重启 = server 进程全灭，诚实标 error，巡检按 deploy_params 重拉
	mgr.StartHealth(s.ctx)
	log.Printf("[tunnel] 多层代理隧道子系统已就绪（端口池 %s，巡检 %v)", config.TunnelPortRange(), tunnel.HealthInterval)
}

// callbackHost 取目标回连平台用的主机地址（ARTEX_CALLBACK_ADDR 的 host 部分）。
func callbackHost() (string, error) {
	addr := config.CallbackAddr()
	if addr == "" {
		return "", fmt.Errorf("未配置平台回连地址：设 ARTEX_CALLBACK_ADDR=<目标可达的平台IP:端口> 后重试")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, nil //  bare host（无端口）也接受
	}
	return host, nil
}

// roeCheckHost 校验目标 host 是否在本任务 RoE 范围内（与 register_session 同一套：
// strict 拦截、warn 告警放行、fail-open)。返回告警文本与拦截错误。
func (s *Server) roeCheckHost(ctx context.Context, host string) (string, error) {
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 || s.m.pg == nil || host == "" {
		return "", nil
	}
	mode, _, _ := s.m.pg.GetSetting(settingRoEEnforcement)
	if strings.EqualFold(strings.TrimSpace(mode), "off") {
		return "", nil
	}
	rows, err := s.m.pg.Assets().ListTaskScope(ri.TaskID)
	if err != nil {
		return "", nil // 读取失败按未登记范围处理（fail-open，与 guard 一致）
	}
	verdict := guard.NewScopeMatcher(scopeRulesFromRows(rows)).Check(host)
	if verdict != guard.ScopeOut {
		return "", nil
	}
	msg := fmt.Sprintf("RoE 授权范围检查：隧道目标 %s 不在本任务(task %d)已登记范围内。"+
		"如需放行：把目标加入任务范围(add_task_scope)，或在系统设置把 roe_enforcement 改为 warn/off。",
		host, ri.TaskID)
	if strings.EqualFold(strings.TrimSpace(mode), "strict") {
		return "", fmt.Errorf("%s", msg)
	}
	return "⚠ " + msg, nil
}

// sessionVerifyAddr 取立足点会话的 webshell host:port 作为 socks 决定性验证目标
// （目标一定能回连自己的 web 端口——这是「隧道真的通」的最硬证据）。
func (s *Server) sessionVerifyAddr(ctx context.Context, sessionID int64) string {
	if s.sessStore == nil {
		return ""
	}
	rec, err := s.sessStore.Get(ctx, sessionID)
	if err != nil || rec == nil || rec.URL == "" {
		return ""
	}
	u, err := url.Parse(rec.URL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// --- host 工具（默认绑 worker;seed 进 tools 表，按 agent 过滤） ---

// tunnelTools 返回隧道四工具。tunnels 为 nil(DB 未就绪）时不提供。
func (s *Server) tunnelTools() []actool.CoreTool {
	if s.tunnels == nil {
		return nil
	}
	return []actool.CoreTool{
		s.tunnelProbeTool(),
		s.tunnelDeployTool(),
		s.tunnelListTool(),
		s.tunnelTeardownTool(),
	}
}

// tunnelProbeTool 出网探测 + 选型建议（四件套之 probe)。
func (s *Server) tunnelProbeTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "tunnel_probe",
		Description: "【隧道四件套 1/4·probe】经立足点会话在目标侧实测出网能力：TCP 回连平台常用口(443/80/53)" +
			"与隧道端口池采样口（tunnel_port_range 起始终端）、HTTP 出网、DNS 递归，可选 ICMP。" +
			"TCP 探针三态：连上或 refused 拒连（RST 证明路径通，仅暂无监听）都算通；超时才算被过滤。" +
			"返回每探针证据 + 证据分档选型结论（含 suggested_adapter):" +
			"能 TCP 出→建议 tunnel_deploy adapter=chisel 反向 socks；出网全断但 webshell 可用→" +
			"建议 tunnel_deploy adapter=suo5(HTTP 隧道，不依赖出网）；只 DNS→命令通道级说明。" +
			"起隧道前先跑这个，别盲建。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "integer", "description": "立足点会话 id(register_session 返回）"},
				"icmp":       map[string]any{"type": "boolean", "description": "是否追加 ICMP 探针（噪声大，默认 false)"},
			},
			"required": []any{"session_id"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				SessionID int64 `json:"session_id"`
				ICMP      bool  `json:"icmp"`
			}
			_ = json.Unmarshal(in, &a)
			sess, err := s.lookupSession(a.SessionID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			host, err := callbackHost()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// 探测目标 = 常用回连口 443/80/53 + 隧道端口池采样（起始终端各 1 个）:
			// 隧道实际要用的监听口必须被探测到，否则「常用口被过滤」会误判 TCP 出不去。
			addrs := tunnel.ProbeAddrs(host, config.TunnelPortRange())
			rep, err := tunnel.Probe(ctx, sess, addrs, a.ICMP)
			if err != nil {
				return actool.Errorf("探测失败：" + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"report":     rep,
				"conclusion": tunnel.Conclude(rep),
			})
		},
	})
}

// tunnelDeployTool 计划+部署+决定性验证（四件套之 plan+deploy)。
func (s *Server) tunnelDeployTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "tunnel_deploy",
		Description: "【隧道四件套 2/4·plan+deploy】经立足点会话部署反向隧道。两个适配器：" +
			"adapter=chisel（默认能力）：目标能 TCP 出网时用，平台起 chisel server、二进制经受管投递到目标回连；" +
			"adapter=suo5（目标无出网场景，红日3 复盘缺口）:HTTP 隧道，payload(suo5.jsp/php/aspx，按会话语言自动选）" +
			"写到 webshell 同目录，平台 suo5 CLI 经入向 HTTP 建 socks5，完全不依赖目标出网，只支持 kind=socks；" +
			"adapter=auto（默认）：现场跑 probe，能 TCP 出网→chisel，否则→suo5。" +
			"kind=socks 建反向 socks5（返回平台回环 socks_addr);kind=portfwd 建单端口转发（仅 chisel,target=host:port 必填）。" +
			"流程：部署 → 隧道内决定性验证（经 socks 真实 CONNECT webshell 宿主），通才标 alive；任一失败逐层回滚。" +
			"【隧道是长寿命受管资源】用完必须 tunnel_teardown 回收；不要用裸 Bash 自己起隧道进程（无台账，会被收割）。" +
			"portfwd 的 target host 会过 RoE 任务范围校验。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "integer", "description": "立足点会话 id"},
				"kind":       strParam("隧道类型：socks（反向 socks5) / portfwd（单端口转发，仅 chisel)，必填"),
				"target":     strParam("portfwd 必填：经隧道访问的内网目标 host:port"),
				"adapter":    strParam("适配器：auto（默认，probe 结论驱动） / chisel（目标能出网） / suo5（目标无出网，仅 socks)"),
			},
			"required": []any{"session_id", "kind"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				SessionID int64  `json:"session_id"`
				Kind      string `json:"kind"`
				Target    string `json:"target"`
				Adapter   string `json:"adapter"`
			}
			_ = json.Unmarshal(in, &a)
			sess, err := s.lookupSession(a.SessionID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			cbHost, err := callbackHost()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			kind := strings.TrimSpace(a.Kind)
			adapter := strings.ToLower(strings.TrimSpace(a.Adapter))
			if adapter == "" {
				adapter = "auto"
			}
			switch adapter {
			case "auto", tunnel.AdapterChisel, tunnel.AdapterSuo5:
			default:
				return actool.Errorf(fmt.Sprintf("adapter 非法： %q(auto|chisel|suo5)", a.Adapter)), nil
			}
			if adapter == tunnel.AdapterSuo5 && kind != tunnel.KindSocks {
				return actool.Errorf("suo5 适配器只支持 kind=socks（无出网场景的 HTTP socks 隧道）;portfwd 请用 chisel"), nil
			}
			if adapter == "auto" {
				// probe 结论驱动：portfwd 只能 chisel;socks 现场跑 probe,
				// 能 TCP 出网→chisel，全 filtered/仅 DNS/仅 ICMP→suo5（不依赖出网）。
				adapter = tunnel.AdapterChisel
				if kind == tunnel.KindSocks {
					rep, perr := tunnel.Probe(ctx, sess, tunnel.ProbeAddrs(cbHost, config.TunnelPortRange()), false)
					if perr != nil {
						return actool.Errorf("auto 选型探测失败（可显式 adapter=chisel|suo5 跳过）：" + perr.Error()), nil
					}
					if concl := tunnel.Conclude(rep); concl.Grade != tunnel.GradeTCPEgress {
						adapter = tunnel.AdapterSuo5
						log.Printf("[tunnel] auto 选型：probe 分档 %s（目标无 TCP 出网）→ adapter=suo5", concl.Grade)
					}
				}
			}
			ri := agent.RunInfoFrom(ctx)
			opts := &tunnel.PlanOpts{
				TaskID:       ri.TaskID,
				ViaSessionID: a.SessionID,
				CallbackHost: cbHost,
				DataDir:      s.tunnels.PlanDataDir(),
			}
			opts.PortMin, opts.PortMax, err = tunnel.ParsePortRange(config.TunnelPortRange())
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			used, err := s.tunnels.OccupiedPorts(ctx)
			if err != nil {
				return actool.Errorf("端口占用汇总失败：" + err.Error()), nil
			}
			opts.UsedPorts = used
			opts.CanListen = tunnel.CanListen
			var roeWarn string
			var plan *tunnel.Plan
			if adapter == tunnel.AdapterSuo5 {
				shellURL := s.sessionShellURL(ctx, a.SessionID)
				if shellURL == "" {
					return actool.Errorf(fmt.Sprintf("取不到会话 %d 的 webshell URL(suo5 payload 要写同目录），无法部署 suo5", a.SessionID)), nil
				}
				plan, err = tunnel.PlanSuo5(&tunnel.Suo5PlanOpts{
					TaskID:       ri.TaskID,
					ViaSessionID: a.SessionID,
					ShellURL:     shellURL,
					SessionKind:  sess.Kind(),
					VerifyAddr:   s.sessionVerifyAddr(ctx, a.SessionID),
					PortMin:      opts.PortMin,
					PortMax:      opts.PortMax,
					UsedPorts:    opts.UsedPorts,
					CanListen:    opts.CanListen,
					DataDir:      opts.DataDir,
				})
			} else {
				switch kind {
				case tunnel.KindSocks:
					opts.VerifyAddr = s.sessionVerifyAddr(ctx, a.SessionID)
					plan, err = tunnel.PlanSocks(opts)
				case tunnel.KindPortfwd:
					host, portStr, perr := net.SplitHostPort(strings.TrimSpace(a.Target))
					if perr != nil || host == "" {
						return actool.Errorf("portfwd 需要 target=host:port（如 10.0.0.8:3306)"), nil
					}
					port, perr := strconv.Atoi(portStr)
					if perr != nil || port <= 0 {
						return actool.Errorf("target 端口非法： " + portStr), nil
					}
					warn, roeErr := s.roeCheckHost(ctx, host)
					if roeErr != nil {
						return actool.Errorf(roeErr.Error()), nil
					}
					roeWarn = warn
					opts.TargetHost, opts.TargetPort = host, port
					plan, err = tunnel.PlanPortfwd(opts)
				default:
					return actool.Errorf("kind 必填：socks | portfwd"), nil
				}
			}
			if err != nil {
				return actool.Errorf("计划生成失败：" + err.Error()), nil
			}
			rec, err := s.tunnels.Deploy(ctx, plan)
			if err != nil {
				return actool.Errorf("部署失败（已逐层回滚）：" + err.Error()), nil
			}
			out := map[string]any{
				"tunnel_id": rec.ID,
				"kind":      rec.Kind,
				"adapter":   rec.Adapter,
				"state":     rec.State,
				"note": "隧道为受管长寿命资源：用完 tunnel_teardown 回收；断链平台自动重拉（上限 3 次）。" +
					"auth token 只存台账 deploy_params。",
			}
			if rec.Kind == tunnel.KindSocks {
				addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(plan.SocksPort))
				out["socks_addr"] = addr
				out["socks_url"] = "socks5://" + addr // 期 3b 按任务 proxyEnv 直接消费此形式
				if rec.Adapter == tunnel.AdapterSuo5 {
					// suo5 入口带 --auth 鉴权：消费方必须带口令（台账 deploy_params 有，平台内部消费用）。
					out["socks_url"] = "socks5://" + plan.AuthUser + ":" + plan.AuthPass + "@" + addr
					out["payload_url"] = plan.PayloadURL
				}
			} else {
				out["forward_addr"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(plan.LocalPort))
				out["forward_to"] = net.JoinHostPort(plan.TargetHost, strconv.Itoa(plan.TargetPort))
			}
			if roeWarn != "" {
				out["roe_warning"] = roeWarn
			}
			return jsonResult(out)
		},
	})
}

// sessionShellURL 取立足点会话的 webshell URL(suo5 payload 定位同目录用）。
func (s *Server) sessionShellURL(ctx context.Context, sessionID int64) string {
	if s.sessStore == nil {
		return ""
	}
	rec, err := s.sessStore.Get(ctx, sessionID)
	if err != nil || rec == nil {
		return ""
	}
	return strings.TrimSpace(rec.URL)
}

// tunnelListTool 列隧道台账（四件套之 health 的可见面）。
func (s *Server) tunnelListTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "tunnel_list",
		Description: "【隧道四件套 3/4】列本平台隧道台账：id/类型/状态（deploying/alive/error/stopped)/" +
			"入口端口/目标/经哪条会话/错误信息/最近探活时间。任务内默认只列本任务的；非任务运行列全部未终结的。",
		Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		ReadOnly:    func(json.RawMessage) bool { return true },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			ri := agent.RunInfoFrom(ctx)
			var recs []*db.TunnelRecord
			var err error
			if ri.TaskID > 0 {
				recs, err = s.tunnels.TaskRecords(ctx, ri.TaskID)
			} else {
				recs, err = s.tunnels.ActiveRecords(ctx)
			}
			if err != nil {
				return actool.Errorf("台账读取失败：" + err.Error()), nil
			}
			out := []map[string]any{}
			for _, r := range recs {
				var p tunnel.Plan
				_ = json.Unmarshal(r.DeployParams, &p)
				row := map[string]any{
					"tunnel_id":      r.ID,
					"kind":           r.Kind,
					"adapter":        r.Adapter,
					"state":          r.State,
					"listen_port":    r.ListenPort,
					"via_session_id": r.ViaSessionID,
					"last_check":     r.LastCheck.Format("2006-01-02 15:04:05"),
				}
				if r.Kind == tunnel.KindSocks && p.SocksPort > 0 {
					row["socks_addr"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.SocksPort))
				}
				if r.Kind == tunnel.KindPortfwd {
					row["forward_addr"] = net.JoinHostPort("127.0.0.1", strconv.Itoa(p.LocalPort))
					row["target"] = net.JoinHostPort(r.TargetHost, strconv.Itoa(r.TargetPort))
				}
				if r.Error != "" {
					row["error"] = r.Error
				}
				out = append(out, row)
			}
			return jsonResult(map[string]any{"tunnels": out, "count": len(out)})
		},
	})
}

// tunnelTeardownTool 回收隧道（四件套之受管回收）。
func (s *Server) tunnelTeardownTool() actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: "tunnel_teardown",
		Description: "【隧道四件套 4/4】回收一条隧道：经会话杀目标侧 client 进程（查远端、核对命令行防误杀）→ " +
			"杀平台侧 server → 删 stage 投递条目 → 删 authfile → 台账标 stopped。隧道用完必须调这个；" +
			"不要让隧道裸奔，也不要用裸 Bash 自己杀/起隧道进程。",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tunnel_id": map[string]any{"type": "integer", "description": "tunnel_deploy/tunnel_list 返回的隧道 id"},
			},
			"required": []any{"tunnel_id"},
		},
		ReadOnly:    func(json.RawMessage) bool { return false },
		Permissions: sessionPerm,
		Run: func(ctx context.Context, in json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var a struct {
				TunnelID int64 `json:"tunnel_id"`
			}
			_ = json.Unmarshal(in, &a)
			if a.TunnelID <= 0 {
				return actool.Errorf("tunnel_id 必填且 > 0"), nil
			}
			if err := s.tunnels.Teardown(ctx, a.TunnelID); err != nil {
				return actool.Errorf("回收失败：" + err.Error()), nil
			}
			return jsonResult(map[string]any{"tunnel_id": a.TunnelID, "state": db.TunnelStopped})
		},
	})
}

// seedTunnelTools 把隧道四工具 seed 进 tools 表、默认绑定 worker
// （与 sessionTools 同一套 SeedTool 首插入语义：老库的用户编辑不被覆盖）。
func (s *Server) seedTunnelTools() {
	if s.tunnels == nil {
		return
	}
	workerAgents, _ := json.Marshal([]string{"worker"})
	for _, t := range s.tunnelTools() {
		schema, _ := json.Marshal(t.InputSchema())
		if err := s.m.PG().SeedTool(t.Name(), t.Description(), schema, workerAgents); err != nil {
			log.Printf("[tools] seed %s 失败： %v", t.Name(), err)
		}
	}
}
