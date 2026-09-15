package guard

import (
	"reflect"
	"testing"
)

func TestExtractHosts(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"curl URL", `curl https://api.example.com:8443/v1/login`, []string{"api.example.com"}},
		{"curl URL 管道", `curl -s http://a.example.com/ | grep x`, []string{"a.example.com"}},
		{"nmap IP", `nmap -sV -p 80,443 10.0.0.7`, []string{"10.0.0.7"}},
		{"nmap CIDR 字面量按主机对待", `nmap 192.168.1.0/24`, nil}, // 含 / 的字面量不提取(保守)
		{"ssh user@host", `ssh admin@jump.example.com`, []string{"jump.example.com"}},
		{"ssh user@ip", `ssh root@172.16.0.5`, []string{"172.16.0.5"}},
		{"nc host port", `nc 10.0.0.9 4444`, []string{"10.0.0.9"}},
		{"host:port 参数", `ncat example.com 443`, []string{"example.com"}},
		{"ping 主机名", `ping db.internal.example.com`, []string{"db.internal.example.com"}},
		{"wget URL", `wget http://files.example.com/a.sh -O /tmp/a.sh`, []string{"files.example.com"}},
		{"多个目标", `curl http://one.example.com/ && nmap 10.1.1.1 && ssh u@two.example.com`,
			[]string{"one.example.com", "10.1.1.1", "two.example.com"}},
		{"无目标命令", `ls -la /tmp`, nil},
		{"无目标命令2", `cat /etc/passwd | grep root`, nil},
		{"flag 值不当目标", `nmap -iL targets.txt`, nil},
		{"flag 值不当目标2", `curl -H example.com http://real.example.com/`, []string{"real.example.com"}},
		{"独立 IP 字面量", `echo 8.8.8.8`, []string{"8.8.8.8"}},
		{"工具路径全名", `/usr/bin/nmap 10.2.2.2`, []string{"10.2.2.2"}},
		{"sudo 前缀", `sudo nmap 10.3.3.3`, []string{"10.3.3.3"}},
		{"去重", `ping 10.0.0.1; nmap 10.0.0.1`, []string{"10.0.0.1"}},
		{"空命令", ``, nil},
	}
	for _, c := range cases {
		got := ExtractHosts(c.cmd)
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ExtractHosts(%q) = %v, want %v", c.name, c.cmd, got, c.want)
		}
	}
}
