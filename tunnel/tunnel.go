// Package tunnel 是多层代理（内网期 3a）核心子系统：probe/plan/deploy/health
// 四件套（INTRANET-PIVOT-DESIGN.md §4.3）加受管资源原则（§4.3a)。
//
// 语义参考 PivotHub pivothub（Go 重写，非照抄），并修掉它的三个缺陷：
//  1. 部署参数完整持久化（deploy_params JSONB 含会话引用/auth/远端路径/验证目标），
//     断链自动重拉是真功能，不是名存实亡；
//  2. 销毁兜底查【远端】进程（经会话 kill -0 + /proc/<pid>/cmdline 核对），
//     不犯 PivotHub 查本机的错；
//  3. 死链不当活上游：只有隧道内决定性验证（经 socks5 真实拨测 / portfwd 真实访问
//     target:port）通过才标 alive；反向监听口 accept ≠ 目标可达。
//
// 隧道是长寿命受管资源：平台 server 进程 pid/命令行、目标 client pid、stage 投递
// 条目全部落 deploy_params 台账；用完必须 Teardown。agent 侧只见 tunnel_* 工具，
// 不允许用裸 Bash 自起隧道进程（会被端口审计/收割兜底）。
package tunnel

import (
	"fmt"
	"strconv"
	"strings"
)

// 隧道类型与适配器。
const (
	KindSocks     = "socks"
	KindPortfwd   = "portfwd"
	AdapterChisel = "chisel"
)

// DefaultPortRange 是平台侧隧道端口池默认值（env ARTEX_TUNNEL_PORT_RANGE 可覆盖）。
const DefaultPortRange = "20000-21000"

// ParsePortRange 解析 "min-max" 端口池。纯函数。
func ParsePortRange(s string) (min, max int, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = DefaultPortRange
	}
	a, b, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("端口池格式应为 min-max（如 %s)，实际 %q", DefaultPortRange, s)
	}
	min, err1 := strconv.Atoi(strings.TrimSpace(a))
	max, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil || min <= 0 || max > 65535 || min > max {
		return 0, 0, fmt.Errorf("非法端口池 %q（需 1-65535 且 min<=max)", s)
	}
	return min, max, nil
}

// AllocatePort 从端口池 [min,max] 挑一个既不在 used 集合、又能真实绑定成功的端口。
// canListen 注入真实绑定检查（生产用 net.Listen 试绑；测试用纯函数）。纯逻辑可测。
func AllocatePort(min, max int, used map[int]bool, canListen func(port int) bool) (int, error) {
	for p := min; p <= max; p++ {
		if used[p] {
			continue
		}
		if canListen != nil && !canListen(p) {
			continue
		}
		return p, nil
	}
	return 0, fmt.Errorf("端口池 %d-%d 无可用端口", min, max)
}
