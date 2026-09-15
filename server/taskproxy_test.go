package server

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/tunnel"
)

// fakeTunnelLister 注入式隧道台账(taskProxyManager 的 ListByTask 数据源)。
type fakeTunnelLister struct {
	recs []*db.TunnelRecord
	err  error
}

func (f *fakeTunnelLister) ListByTask(context.Context, int64) ([]*db.TunnelRecord, error) {
	return f.recs, f.err
}

func socksRec(id int64, state string, socksPort int) *db.TunnelRecord {
	raw, _ := json.Marshal(tunnel.Plan{Kind: tunnel.KindSocks, SocksPort: socksPort})
	return &db.TunnelRecord{ID: id, Kind: tunnel.KindSocks, State: state, DeployParams: raw}
}

// suo5Rec 造一条带 --auth 的 suo5 隧道台账行（上游 URL 必须内嵌 user:pass)。
func suo5Rec(id int64, state string, socksPort int) *db.TunnelRecord {
	raw, _ := json.Marshal(tunnel.Plan{Kind: tunnel.KindSocks, SocksPort: socksPort, AuthUser: "artex", AuthPass: "deadbeef"})
	return &db.TunnelRecord{ID: id, Kind: tunnel.KindSocks, Adapter: tunnel.AdapterSuo5, State: state, DeployParams: raw}
}

// pickUpstream:最新 alive socks 隧道胜出;无隧道回落全局代理;portfwd/非 alive 不算。
func TestPickUpstream(t *testing.T) {
	const global = "http://127.0.0.1:9000"
	cases := []struct {
		name string
		recs []*db.TunnelRecord
		want string
	}{
		{"无隧道回落全局", nil, global},
		{"alive socks 当选", []*db.TunnelRecord{socksRec(1, db.TunnelAlive, 20801)}, "socks5://127.0.0.1:20801"},
		{"多条取 id 最大", []*db.TunnelRecord{
			socksRec(1, db.TunnelAlive, 20801), socksRec(3, db.TunnelAlive, 20803), socksRec(2, db.TunnelAlive, 20802),
		}, "socks5://127.0.0.1:20803"},
		{"error/stopped 不算", []*db.TunnelRecord{
			socksRec(1, db.TunnelError, 20801), socksRec(2, db.TunnelStopped, 20802),
		}, global},
		{"portfwd 不是上游", []*db.TunnelRecord{{
			ID: 1, Kind: tunnel.KindPortfwd, State: db.TunnelAlive,
			DeployParams: json.RawMessage(`{"kind":"portfwd","local_port":20810}`),
		}}, global},
		{"deploy_params 缺 socks_port 跳过", []*db.TunnelRecord{
			{ID: 1, Kind: tunnel.KindSocks, State: db.TunnelAlive, DeployParams: json.RawMessage(`{}`)},
		}, global},
		{"suo5 上游内嵌 auth", []*db.TunnelRecord{suo5Rec(9, db.TunnelAlive, 20809)}, "socks5://artex:deadbeef@127.0.0.1:20809"},
	}
	for _, c := range cases {
		if got := pickUpstream(c.recs, global); got != c.want {
			t.Errorf("%s: pickUpstream = %q, want %q", c.name, got, c.want)
		}
	}
	// 全局为空(直连)时无隧道上游也是空。
	if got := pickUpstream(nil, ""); got != "" {
		t.Errorf("无隧道+无全局代理应为直连(空), got %q", got)
	}
}

// freePort 拿一个当前可绑的端口(放还给系统;与隧道的 CanListen 同款的微小竞争,
// 测试可接受)。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("找空闲端口: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func managedPortRegistered(port int) bool {
	_, ok := dynManagedPorts.Load(port)
	return ok
}

// 懒建 → 端口登记 → 上游热切换(隧道出现/消失)→ CloseTask 清理 → 再懒建。
func TestTaskProxyLifecycle(t *testing.T) {
	port := freePort(t)
	lister := &fakeTunnelLister{}
	m := newTaskProxyManager(t.TempDir(), lister, func() string { return "http://127.0.0.1:9000" }, port, port)
	ctx := context.Background()

	addr, ca := m.ForTask(ctx, 42)
	if addr == "" || ca == "" {
		t.Fatalf("ForTask 应返回实例地址与 CA, got (%q, %q)", addr, ca)
	}
	if !strings.Contains(addr, ":"+strconv.Itoa(port)) {
		t.Errorf("实例地址应含分配端口 %d: %s", port, addr)
	}
	if _, err := os.Stat(ca); err != nil {
		t.Errorf("实例 CA 证书应已落盘 %s: %v", ca, err)
	}
	if !managedPortRegistered(port) {
		t.Error("实例端口应登记进端口审计台账(registerManagedPort)")
	}
	inst := m.proxies[42]
	if inst == nil {
		t.Fatal("实例应已入表")
	}
	if got := inst.tr.UpstreamProxy(); got != "http://127.0.0.1:9000" {
		t.Errorf("无隧道时上游应回落全局代理, got %q", got)
	}

	// 幂等:再次 ForTask 复用同一实例(端口不变)。
	addr2, _ := m.ForTask(ctx, 42)
	if addr2 != addr {
		t.Errorf("重复 ForTask 不应重建实例: %q → %q", addr, addr2)
	}

	// 隧道出现 → Refresh 热切换到 socks 上游。
	lister.recs = []*db.TunnelRecord{socksRec(7, db.TunnelAlive, 20807)}
	m.Refresh(ctx, 42)
	if got := inst.tr.UpstreamProxy(); got != "socks5://127.0.0.1:20807" {
		t.Errorf("隧道 alive 后上游应热切换, got %q", got)
	}

	// 隧道死亡 → 回落全局代理。
	lister.recs = []*db.TunnelRecord{socksRec(7, db.TunnelError, 20807)}
	m.Refresh(ctx, 42)
	if got := inst.tr.UpstreamProxy(); got != "http://127.0.0.1:9000" {
		t.Errorf("隧道死亡后上游应回落全局代理, got %q", got)
	}

	// 无实例的任务 Refresh 是 no-op,不触发懒建。
	m.Refresh(ctx, 99)
	if m.proxies[99] != nil {
		t.Error("Refresh 不应为无实例任务建实例(懒建语义)")
	}

	// CloseTask:端口注销、实例出表、目录(CA/录制树)保留。
	m.CloseTask(42)
	if managedPortRegistered(port) {
		t.Error("CloseTask 后端口应注销")
	}
	if m.proxies[42] != nil {
		t.Error("CloseTask 后实例应出表")
	}
	if _, err := os.Stat(ca); err != nil {
		t.Errorf("CloseTask 后 CA 目录应保留(留痕), stat %s: %v", ca, err)
	}
	m.CloseTask(42) // 幂等,不应 panic

	// 再懒建:拿新实例(同一端口池只剩一个口,复用同端口)。
	addr3, _ := m.ForTask(ctx, 42)
	if addr3 == "" {
		t.Fatal("CloseTask 后应能重新懒建实例")
	}
	m.Close()
	if managedPortRegistered(port) {
		t.Error("Close 后端口应全部注销")
	}
}
