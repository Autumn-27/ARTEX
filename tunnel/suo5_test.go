// suo5_test.go 是 suo5 适配器纯函数的单测（真实部署属 VPS e2e 验收）。
package tunnel

import (
	"strings"
	"testing"
)

func TestSuo5PayloadFile(t *testing.T) {
	cases := []struct {
		kind string
		want string
	}{
		{"http_php", "suo5.php"},
		{"http_phpcmd", "suo5.php"},  // PHP 命令马
		{"http_phpenc", "suo5.php"},  // 加密马用明文 php payload
		{"php", "suo5.php"},          // 直接给 lang 也接受
		{"http_jsp", "suo5.jsp"},
		{"http_jspenc", "suo5.jsp"},
		{"http_aspx", "suo5.aspx"},
		{"ASPX", "suo5.aspx"},
	}
	for _, c := range cases {
		got, err := Suo5PayloadFile(c.kind)
		if err != nil || got != c.want {
			t.Errorf("Suo5PayloadFile(%q) = %q, %v; want %q", c.kind, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "http_node", "ssh", "suo5"} {
		if _, err := Suo5PayloadFile(bad); err == nil {
			t.Errorf("Suo5PayloadFile(%q) 应报错", bad)
		}
	}
}

func TestSuo5PayloadURL(t *testing.T) {
	cases := []struct {
		name     string
		shellURL string
		file     string
		want     string
	}{
		{"同目录换名", "http://10.0.0.5:8082/upload/shell.jsp", "a1b2c3d4.jsp", "http://10.0.0.5:8082/upload/a1b2c3d4.jsp"},
		{"根路径马", "http://10.0.0.5/shell.php", "ff00ff00.php", "http://10.0.0.5/ff00ff00.php"},
		{"查询串清掉", "http://10.0.0.5/shell.php?x=1", "a1b2c3d4.php", "http://10.0.0.5/a1b2c3d4.php"},
		{"https 保留", "https://web.example.com/app/s.jsp", "a1b2c3d4.jsp", "https://web.example.com/app/a1b2c3d4.jsp"},
	}
	for _, c := range cases {
		got, err := Suo5PayloadURL(c.shellURL, c.file)
		if err != nil || got != c.want {
			t.Errorf("%s: Suo5PayloadURL = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "://nope", "shell.php", "http:///x.php"} {
		if _, err := Suo5PayloadURL(bad, "a.php"); err == nil {
			t.Errorf("Suo5PayloadURL(%q) 应报错", bad)
		}
	}
}

func TestSuo5Args(t *testing.T) {
	args := Suo5Args("http://10.0.0.5/u/a1b2c3d4.php", 20123, "artex", "deadbeef")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-t http://10.0.0.5/u/a1b2c3d4.php", "-l 127.0.0.1:20123", "--auth artex:deadbeef"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Suo5Args 缺 %q: %v", want, args)
		}
	}
}

func TestJoinRemotePath(t *testing.T) {
	cases := []struct{ dir, name, want string }{
		{"/var/www/html", "a.php", "/var/www/html/a.php"},
		{"/var/www/html/", "a.php", "/var/www/html/a.php"},
		{`C:\inetpub\wwwroot`, "a.aspx", `C:\inetpub\wwwroot\a.aspx`},
	}
	for _, c := range cases {
		if got := joinRemotePath(c.dir, c.name); got != c.want {
			t.Errorf("joinRemotePath(%q,%q) = %q, want %q", c.dir, c.name, got, c.want)
		}
	}
}

func TestParsePwd(t *testing.T) {
	if got, err := parsePwd("\n  /var/www/html \n"); err != nil || got != "/var/www/html" {
		t.Errorf("parsePwd 常规 = %q, %v", got, err)
	}
	if got, err := parsePwd(`C:\inetpub\wwwroot`); err != nil || got != `C:\inetpub\wwwroot` {
		t.Errorf("parsePwd windows = %q, %v", got, err)
	}
	for _, bad := range []string{"", "  \n ", "/tmp/$(x)", "/tmp/'x'", "relative/dir"} {
		if _, err := parsePwd(bad); err == nil {
			t.Errorf("parsePwd(%q) 应报错", bad)
		}
	}
}

func TestRemoteRemoveCmd(t *testing.T) {
	if got := remoteRemoveCmd("suo5.php", "/var/www/html/a.php"); got != "rm -f '/var/www/html/a.php'" {
		t.Errorf("posix remove = %q", got)
	}
	if got := remoteRemoveCmd("suo5.aspx", `C:\inetpub\wwwroot\a.aspx`); got != `del /f "C:\inetpub\wwwroot\a.aspx"` {
		t.Errorf("windows remove = %q", got)
	}
}

func TestPlanSuo5(t *testing.T) {
	p, err := PlanSuo5(&Suo5PlanOpts{
		TaskID: 7, ViaSessionID: 3,
		ShellURL:    "http://10.0.0.5:8082/upload/shell.jsp",
		SessionKind: "http_jsp",
		VerifyAddr:  "10.0.0.5:8082",
		PortMin:     20000, PortMax: 20010,
		UsedPorts: map[int]bool{20000: true},
		CanListen: func(int) bool { return true },
		DataDir:   "/tmp/x",
	})
	if err != nil {
		t.Fatalf("PlanSuo5: %v", err)
	}
	if p.Adapter != AdapterSuo5 || p.Kind != KindSocks {
		t.Errorf("adapter/kind = %q/%q", p.Adapter, p.Kind)
	}
	if p.ListenPort != 20001 || p.SocksPort != 20001 || p.ListenHost != "127.0.0.1" {
		t.Errorf("端口分配 = %d/%d host=%q（应跳过已占的 20000)", p.ListenPort, p.SocksPort, p.ListenHost)
	}
	if p.PayloadFile != "suo5.jsp" || !strings.HasSuffix(p.PayloadName, ".jsp") || len(p.PayloadName) != 12 {
		t.Errorf("payload 文件/名 = %q/%q", p.PayloadFile, p.PayloadName)
	}
	wantURL := "http://10.0.0.5:8082/upload/" + p.PayloadName
	if p.PayloadURL != wantURL {
		t.Errorf("PayloadURL = %q, want %q", p.PayloadURL, wantURL)
	}
	joined := strings.Join(p.ServerArgs, " ")
	if !strings.Contains(joined, "--auth "+p.AuthUser+":"+p.AuthPass) {
		t.Errorf("ServerArgs 缺 auth: %v", p.ServerArgs)
	}
	if p.VerifyAddr != "10.0.0.5:8082" {
		t.Errorf("VerifyAddr = %q", p.VerifyAddr)
	}
	// 缺 shell_url / 未知会话类型要报明确错误。
	if _, err := PlanSuo5(&Suo5PlanOpts{SessionKind: "http_jsp"}); err == nil {
		t.Error("缺 ShellURL 应报错")
	}
	if _, err := PlanSuo5(&Suo5PlanOpts{ShellURL: "http://a/s.jsp", SessionKind: "ssh"}); err == nil {
		t.Error("未知会话类型应报错")
	}
}

func TestSocks5AuthCodec(t *testing.T) {
	if got := BuildSocks5GreetingAuth(); len(got) != 3 || got[2] != 0x02 {
		t.Errorf("greeting auth = %v", got)
	}
	b, err := BuildSocks5UserPass("artex", "deadbeef")
	if err != nil {
		t.Fatalf("BuildSocks5UserPass: %v", err)
	}
	if b[0] != 0x01 || b[1] != 5 || string(b[2:7]) != "artex" || b[7] != 8 || string(b[8:]) != "deadbeef" {
		t.Errorf("userpass 报文 = %v", b)
	}
	if _, err := BuildSocks5UserPass("", "x"); err == nil {
		t.Error("空用户名应报错")
	}
	if !ParseSocks5UserPassReply([]byte{0x01, 0x00}) {
		t.Error("auth 成功应答应判 true")
	}
	if ParseSocks5UserPassReply([]byte{0x01, 0x01}) || ParseSocks5UserPassReply([]byte{0x05, 0x00}) {
		t.Error("auth 失败/非子协商应答应判 false")
	}
}
