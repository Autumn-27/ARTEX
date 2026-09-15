// plan_test.go 是计划组装的纯函数单测：端口池解析、端口分配、命令行组装
// (auth 不进命令行）、回滚步骤顺序。
package tunnel

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestParsePortRange(t *testing.T) {
	cases := []struct {
		in      string
		wantMin int
		wantMax int
		wantErr bool
	}{
		{"20000-21000", 20000, 21000, false},
		{"", 20000, 21000, false}, // 空 = 默认池
		{" 8080 - 8085 ", 8080, 8085, false},
		{"20000", 0, 0, true},   // 缺 -
		{"0-100", 0, 0, true},   // min 非法
		{"100-99", 0, 0, true},  // min>max
		{"1-70000", 0, 0, true}, // max 超界
		{"abc-def", 0, 0, true},
	}
	for _, c := range cases {
		min, max, err := ParsePortRange(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParsePortRange(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && (min != c.wantMin || max != c.wantMax) {
			t.Errorf("ParsePortRange(%q) = %d-%d, want %d-%d", c.in, min, max, c.wantMin, c.wantMax)
		}
	}
}

func TestAllocatePort(t *testing.T) {
	used := map[int]bool{20000: true, 20001: true}
	closed := map[int]bool{20002: true} // 空闲但被别的进程占着
	canListen := func(p int) bool { return !closed[p] }
	p, err := AllocatePort(20000, 20010, used, canListen)
	if err != nil {
		t.Fatal(err)
	}
	if p != 20003 {
		t.Errorf("AllocatePort = %d, want 20003（跳过台账占用与绑定失败）", p)
	}
	// 全部不可用 → 明确错误
	if _, err := AllocatePort(20000, 20002, used, canListen); err == nil {
		t.Error("端口池耗尽应报错")
	}
	// nil used / nil canListen 也能工作
	p, err = AllocatePort(21000, 21001, nil, nil)
	if err != nil || p != 21000 {
		t.Errorf("AllocatePort(nil) = %d, %v", p, err)
	}
}

func testOpts() *PlanOpts {
	return &PlanOpts{
		TaskID: 7, ViaSessionID: 3, CallbackHost: "10.0.0.5",
		PortMin: 20000, PortMax: 20100, UsedPorts: map[int]bool{},
		CanListen: func(int) bool { return true },
		DataDir:   "/data/tunnels",
	}
}

func TestPlanSocksAssembly(t *testing.T) {
	p, err := PlanSocks(testOpts())
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindSocks || p.Adapter != AdapterChisel {
		t.Errorf("kind/adapter = %s/%s", p.Kind, p.Adapter)
	}
	if p.ListenPort < 20000 || p.ListenPort > 20100 || p.SocksPort < 20000 || p.SocksPort > 20100 {
		t.Errorf("端口越池： listen=%d socks=%d", p.ListenPort, p.SocksPort)
	}
	if p.ListenPort == p.SocksPort {
		t.Error("listen 与 socks 端口必须不同")
	}
	if p.PlatformAddr != "10.0.0.5:"+strconv.Itoa(p.ListenPort) {
		t.Errorf("PlatformAddr = %s（应为 callback host + listen port)", p.PlatformAddr)
	}
	// server 命令行组装
	joined := strings.Join(p.ServerArgs, " ")
	for _, want := range []string{"server", "--host 0.0.0.0", "-p " + strconv.Itoa(p.ListenPort), "--reverse", "--authfile"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ServerArgs 缺 %q: %s", want, joined)
		}
	}
	// auth 不得出现在任何命令行（server 走 --authfile,client 走 AUTH env)
	if strings.Contains(joined, p.AuthPass) {
		t.Error("auth 泄露进 ServerArgs")
	}
	if strings.Contains(strings.Join(p.ClientArgs, " "), p.AuthPass) {
		t.Error("auth 泄露进 ClientArgs")
	}
	if len(p.AuthPass) != 32 { // 16 字节 hex
		t.Errorf("AuthPass 长度 %d, want 32(crypto/rand 16 字节 hex)", len(p.AuthPass))
	}
	// client remote 形式：反向 socks 绑平台回环
	wantRemote := "R:127.0.0.1:" + strconv.Itoa(p.SocksPort) + ":socks"
	found := false
	for _, a := range p.ClientArgs {
		if a == wantRemote {
			found = true
		}
	}
	if !found {
		t.Errorf("ClientArgs 缺 remote %q: %v", wantRemote, p.ClientArgs)
	}
	// authfile 必须是 chisel 期望的 JSON users 格式 {"user:pass": [".*"]}
	var users map[string][]string
	if err := json.Unmarshal([]byte(p.AuthfileContent()), &users); err != nil {
		t.Fatalf("AuthfileContent 不是合法 JSON: %v", err)
	}
	key := p.AuthUser + ":" + p.AuthPass
	if got, ok := users[key]; !ok || len(got) != 1 || got[0] != ".*" {
		t.Errorf("AuthfileContent users = %v, want {%q: [\".*\"]}", users, key)
	}
}

func TestPlanPortfwdAssembly(t *testing.T) {
	o := testOpts()
	o.TargetHost, o.TargetPort = "172.16.1.8", 3306
	p, err := PlanPortfwd(o)
	if err != nil {
		t.Fatal(err)
	}
	wantRemote := "R:127.0.0.1:" + strconv.Itoa(p.LocalPort) + ":172.16.1.8:3306"
	found := false
	for _, a := range p.ClientArgs {
		if a == wantRemote {
			found = true
		}
	}
	if !found {
		t.Errorf("ClientArgs 缺 remote %q: %v", wantRemote, p.ClientArgs)
	}
	// 缺 target 必须报错
	if _, err := PlanPortfwd(testOpts()); err == nil {
		t.Error("portfwd 缺 target 应报错")
	}
	// 缺 callback host 必须报错
	o2 := testOpts()
	o2.CallbackHost = ""
	if _, err := PlanSocks(o2); err == nil {
		t.Error("缺 callback host 应报错")
	}
}

func TestPlanSocksPortPoolExhausted(t *testing.T) {
	o := testOpts()
	o.PortMin, o.PortMax = 20000, 20000 // 只有一个口，socks 需要两个
	if _, err := PlanSocks(o); err == nil {
		t.Error("端口池不够分配两个口应报错")
	}
}

func TestRollbackStepsOrder(t *testing.T) {
	p, err := PlanSocks(testOpts())
	if err != nil {
		t.Fatal(err)
	}
	p.RemotePID, p.ServerPID, p.StageToken = 1234, 5678, "tok"
	steps := p.RollbackSteps()
	wantOrder := []string{"remote_process", "remote_files", "server_process", "stage_entry", "authfile", "ledger_row"}
	if len(steps) != len(wantOrder) {
		t.Fatalf("回滚步数 %d, want %d", len(steps), len(wantOrder))
	}
	for i, layer := range wantOrder {
		if steps[i].Layer != layer {
			t.Errorf("回滚第 %d 步 = %s, want %s（先目标侧后平台侧，台账最后删）", i, steps[i].Layer, layer)
		}
	}
}
