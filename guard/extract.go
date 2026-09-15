package guard

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// hostTools are the common interaction tools whose positional argument is a
// target host / host:port / IP literal.
var hostTools = map[string]bool{
	"curl": true, "wget": true, "nc": true, "ncat": true, "netcat": true,
	"nmap": true, "ssh": true, "ping": true, "telnet": true, "dig": true,
	"host": true, "nslookup": true, "masscan": true, "httpx": true,
	"hydra": true, "smbclient": true, "rpcclient": true, "rdesktop": true,
	"xfreerdp": true,
}

// valueFlags take a non-host value as the next token; that token must not be
// mistaken for a target. 保守取向:宁缺勿滥。
var valueFlags = map[string]bool{
	"-p": true, "-o": true, "-oA": true, "-oX": true, "-oN": true, "-oG": true,
	"-iL": true, "-i": true, "-e": true, "-w": true, "-t": true, "-c": true,
	"-H": true, "-A": true, "-U": true, "-d": true, "-x": true, "-u": true,
	"-F": true, "-b": true, "-m": true, "-l": true, "-P": true,
	"--data": true, "--header": true, "--proxy": true, "--user-agent": true,
	"--connect-timeout": true, "--max-time": true, "--output": true,
	"--cacert": true, "--cert": true, "--key": true,
	"--top-ports": true, "--min-rate": true, "--max-rate": true,
}

var (
	reScheme = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s'"<>|\\^` + "`" + `]+`)
	// 主机名:至少一段点分且以字母 TLD 结尾(file.txt 这类误判由 valueFlags 兜住)。
	reHostName   = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]*\.)+[a-z]{2,}$`)
	reShellSplit = regexp.MustCompile(`[\s|;&<>]+`)
)

// ExtractHosts extracts likely interaction targets from a shell command line:
// hosts of URLs (scheme://...), host:port / IP literals following common tool
// names, and standalone IP literals. 保守取向:提取不出 = 无目标(不拦截)。
// Returned hosts are lowercased, port-stripped, deduplicated, in first-seen order.
func ExtractHosts(command string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(h string) {
		h = normalizeHost(h)
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		out = append(out, h)
	}

	// Pass 1: URLs anywhere in the command.
	for _, raw := range reScheme.FindAllString(command, -1) {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			add(u.Hostname())
		}
	}

	// Pass 2: token scan — tool positional args + standalone IP literals.
	curTool := false // 上一个词是 hostTools 工具名
	skipNext := false
	for _, tok := range reShellSplit.Split(command, -1) {
		tok = strings.Trim(tok, `"'`)
		if tok == "" {
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		base := baseOf(tok)
		if isShellKeyword(base) {
			curTool = false
			continue
		}
		if hostTools[base] {
			curTool = true
			continue
		}
		if curTool {
			if strings.HasPrefix(tok, "-") {
				if valueFlags[tok] {
					skipNext = true
				}
				continue
			}
			if at := strings.LastIndex(tok, "@"); at >= 0 {
				tok = tok[at+1:] // ssh user@host
			}
			if ip := parseBareIP(tok); ip != "" {
				add(ip)
			} else if reHostName.MatchString(stripPortMaybe(tok)) {
				add(tok)
			}
			continue
		}
		// 非工具参数位置:只收独立 IP 字面量(裸主机名歧义太大,宁缺勿滥)。
		if ip := parseBareIP(tok); ip != "" {
			add(ip)
		}
	}
	return out
}

func isShellKeyword(tok string) bool {
	switch tok {
	case "sudo", "doas", "env", "time", "nice", "xargs":
		return true
	}
	return false
}

func baseOf(tok string) string {
	if j := strings.LastIndex(tok, "/"); j >= 0 {
		return tok[j+1:]
	}
	return tok
}

// parseBareIP returns the normalized IP if tok is an IP literal or ip:port.
func parseBareIP(tok string) string {
	if host, _, err := net.SplitHostPort(tok); err == nil {
		tok = host
	}
	tok = strings.Trim(tok, "[]")
	if net.ParseIP(tok) != nil {
		return tok
	}
	return ""
}

// stripPortMaybe drops a trailing :<numeric-port> from host:port (best effort).
func stripPortMaybe(tok string) string {
	if host, port, err := net.SplitHostPort(tok); err == nil {
		if _, err := strconv.Atoi(port); err == nil {
			return host
		}
	}
	return tok
}
