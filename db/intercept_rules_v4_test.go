package db

import (
	"regexp"
	"testing"
)

// 纯单测(不需要 PG):内网期 2 横向/喷洒底线规则的正则必须命中常见变体、
// 不误伤正常命令。hashcat 本地破密不拦的取舍见 db.go builtinLateralRules 注释。
func TestBuiltinLateralRulePatterns(t *testing.T) {
	hits := map[string][]string{
		"crackmapexec/nxc": {
			"crackmapexec smb 10.0.0.0/24 -u administrator -p P@ssw0rd",
			"cme smb 10.0.0.5 -u admin -H aad3b435b51404ee",
			"nxc smb 10.0.0.0/24 -u users.txt -p pass.txt",
			"netexec winrm 10.0.0.9 -u admin -p pass",
		},
		"hydra": {
			"hydra -l root -P rockyou.txt ssh://10.0.0.5",
			"hydra -L users.txt -p Summer2026 10.0.0.0/24 smb",
		},
		"medusa": {
			"medusa -h 10.0.0.5 -u root -P pass.txt -M ssh",
			"medusa -H hosts.txt -U users.txt -P pass.txt -M rdp",
		},
		"psexec 系": {
			"python3 psexec.py administrator@10.0.0.5",
			"psexec.py -hashes aad3b435:31d6cfe0 administrator@10.0.0.5",
			"impacket-wmiexec CORP/administrator@10.0.0.5",
			"wmiexec.py administrator@10.0.0.5 'whoami'",
			"smbexec.py administrator@10.0.0.5",
		},
		"kerbrute": {
			"kerbrute passwordspray -d corp.local users.txt 'Summer2026'",
			"kerbrute userenum --dc 10.0.0.1 -d corp.local users.txt",
		},
	}
	misses := []string{
		"hashcat -m 1000 ntlm.txt rockyou.txt",        // 本地破密,不拦(取舍见 db.go 注释)
		"john --wordlist=rockyou.txt shadow.txt",      // 本地破密
		"ssh administrator@10.0.0.5",                  // 正常登录
		"smbclient //10.0.0.5/share -U administrator", // 正常 SMB 访问
		"nmap -p 445,3389 10.0.0.0/24",                // 常规扫描
		"impacket-secretsdump administrator@10.0.0.5", // secretsdump 不在本批
		"python3 exploit.py 10.0.0.5",                 // 普通脚本
	}
	// 已知取舍:"cat medusa.jpg" 这类同名词文件会命中 hydra/medusa 的词边界匹配,
	// ask(非 deny)下误伤代价是一次人工确认,可接受;不为消灭它把正则写复杂。
	var rules []*regexp.Regexp
	for _, r := range builtinLateralRules {
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
