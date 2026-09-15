package db

import (
	"regexp"
	"testing"
)

// 纯单测(不需要 PG):F13 底线规则的正则必须命中常见变体、不误伤正常命令。
func TestBuiltinHTTPServerRulePatterns(t *testing.T) {
	hits := map[string][]string{
		"python": {
			"python -m http.server 8000",
			"python3 -m http.server 8080 --bind 0.0.0.0",
			"python3.11 -m http.server",
			"sudo python -m SimpleHTTPServer 80",
			"cd /tmp && python3 -m http.server 8000 &",
		},
		"php": {
			"php -S 0.0.0.0:8000",
			"php -S 127.0.0.1:8080 -t /var/www",
			"php8.2 -S 0.0.0.0:80",
		},
		"busybox": {
			"busybox httpd -p 8080 -h /tmp",
			"busybox httpd -f -p 80",
		},
		"ruby": {
			"ruby -run -e httpd . -p 8000",
			"ruby -run -e httpd -- --port 8080",
		},
		"npx": {
			"npx serve -l 8080 .",
			"npx http-server -p 8000",
			"npx --yes serve .",
			"npx -y http-server .",
		},
	}
	misses := []string{
		"curl http://10.0.0.2:8787/s/abc/tool.sh -o /tmp/tool.sh", // stage_share 的正常拉取
		"python3 exploit.py",                    // 普通脚本
		"php -r 'echo 1;'",                      // php 单行
		"php artisan serve",                     // 框架子命令,不是 php -S
		"gem install httpd-tools",               // 含 httpd 字样
		"ruby script.rb",                        // 普通 ruby
		"npm install",                           // npm 不是 npx serve
		"cat httpd.conf",                        // 配置文件
		"nmap -p 80,8000-9000 10.0.0.2",         // 常规扫描
	}
	var rules []*regexp.Regexp
	for _, r := range builtinHTTPServerRules {
		re, err := regexp.Compile(r.pattern)
		if err != nil {
			t.Fatalf("规则 %q 正则编译失败: %v", r.name, err)
		}
		rules = append(rules, re)
	}
	matchAny := func(s string) bool {
		for _, re := range rules {
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	for group, cmds := range hits {
		for _, c := range cmds {
			if !matchAny(c) {
				t.Errorf("[%s] 应命中但未命中: %s", group, c)
			}
		}
	}
	for _, c := range misses {
		if matchAny(c) {
			t.Errorf("误伤正常命令: %s", c)
		}
	}
}
