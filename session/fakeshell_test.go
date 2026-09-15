package session

// fakeshell_test.go 是验收测试的目标侧模拟：内存文件系统 + 迷你 sh/cmd 解释器,
// 足以执行驱动发出的全部命令形态（echo/base64/dd/sh/rm/cat/tr/id/whoami/
// certutil 等）。不依赖 PG、不依赖宿主机 shell,全部进程内完成。
// 注意：这是「协议族」级模拟——模拟 PHP 代码执行字段、JSP sh 回显、ASPX cmd
// 回显三种协议形态，不含任何特定靶场值。

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// fakeFS 是目标侧文件系统的内存模拟 + sh 解释器。
type fakeFS struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newFakeFS() *fakeFS { return &fakeFS{files: map[string][]byte{}} }

func (fs *fakeFS) readFile(p string) ([]byte, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	b, ok := fs.files[p]
	return append([]byte(nil), b...), ok
}

func (fs *fakeFS) writeFile(p string, data []byte, appendMode bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if appendMode {
		fs.files[p] = append(fs.files[p], data...)
	} else {
		fs.files[p] = append([]byte(nil), data...)
	}
}

func (fs *fakeFS) remove(p string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.files, p)
}

func (fs *fakeFS) hasPrefixMatch(prefix string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	for p := range fs.files {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// tokenize 按 sh 词法切词：单引号字面、双引号字面（不做变量展开）、反斜杠转义。
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, has := false, false, false
	flush := func() {
		if cur.Len() > 0 || has {
			out = append(out, cur.String())
			cur.Reset()
			has = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			} else {
				cur.WriteByte(c)
			}
		case inD:
			if c == '"' {
				inD = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inS, has = true, true
		case c == '"':
			inD, has = true, true
		case c == ' ' || c == '\t':
			flush()
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// splitQuoted 按顶层分隔符切分（引号内不切）。
func splitQuoted(s string, seps ...string) []string {
	var out []string
	inS, inD := false, false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			}
		case inD:
			if c == '"' {
				inD = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		default:
			for _, sep := range seps {
				if strings.HasPrefix(s[i:], sep) {
					out = append(out, s[start:i])
					i += len(sep) - 1
					start = i + 1
					goto next
				}
			}
		}
	next:
	}
	out = append(out, s[start:])
	return out
}

var arithRe = regexp.MustCompile(`\$\(\((\d+)\+(\d+)\)\)`)

// expandSh 模拟 POSIX sh 的算术展开（探针用）。
func expandSh(s string) string {
	return arithRe.ReplaceAllStringFunc(s, func(m string) string {
		g := arithRe.FindStringSubmatch(m)
		a, _ := strconv.Atoi(g[1])
		b, _ := strconv.Atoi(g[2])
		return strconv.Itoa(a + b)
	})
}

// run 执行一条复合 sh 命令（;、&&、|、>、>>、2>&1、2>/dev/null)。
// 返回合并输出与最后一段的成功标志。
func (fs *fakeFS) run(cmd string) (string, bool) {
	var out strings.Builder
	ok := true
	// 先按 && 再按 ; 切（&& 与 ; 同级,左结合,足够覆盖驱动形态）。
	segs := splitQuoted(cmd, "&&", ";")
	skip := false
	for i, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// 判断段前连接符：找本段在原串的位置太复杂,直接按切分顺序重扫。
		_ = i
		if skip {
			skip = false
			continue
		}
		o, k := fs.runSegment(expandSh(seg))
		out.WriteString(o)
		ok = k
		if !ok && strings.Contains(cmd, "&&") {
			// && 短路的近似：失败后跳过一个段（驱动形态里 && 只出现在
			// `base64 -d > f && sh f`,失败即停符合语义）。
			skip = true
		}
	}
	return out.String(), ok
}

// runSegment 执行一个管道段（可能含 | 与重定向）。
func (fs *fakeFS) runSegment(seg string) (string, bool) {
	stages := splitQuoted(seg, "|")
	input := ""
	out := ""
	ok := true
	for si, st := range stages {
		args, redirect, appendMode := parseRedirect(tokenize(strings.TrimSpace(st)))
		if len(args) == 0 {
			continue
		}
		out, ok = fs.runBuiltin(args, input)
		input = out
		if si == len(stages)-1 && redirect != "" {
			fs.writeFile(redirect, []byte(out), appendMode)
			out = ""
		}
		if !ok {
			break
		}
	}
	return out, ok
}

// parseRedirect 剥掉 2>&1 / 2>/dev/null,提取 > >> 目标。
func parseRedirect(args []string) (clean []string, target string, appendMode bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "2>&1" || a == "2>/dev/null" || strings.HasSuffix(a, "2>&1"):
			continue
		case a == ">" || a == ">>":
			if i+1 < len(args) {
				target = args[i+1]
				appendMode = a == ">>"
				i++
			}
		case strings.HasPrefix(a, ">>") && len(a) > 2:
			target, appendMode = a[2:], true
		case strings.HasPrefix(a, ">") && len(a) > 1:
			target = a[1:]
		default:
			clean = append(clean, a)
		}
	}
	return clean, target, appendMode
}

// tolerantB64Decode 容忍空白（real base64 -d 忽略换行）。
func tolerantB64Decode(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	return base64.StdEncoding.DecodeString(s)
}

func (fs *fakeFS) runBuiltin(args []string, stdin string) (string, bool) {
	switch args[0] {
	case "echo":
		// sh echo:默认输出换行;\n 不解释（驱动不依赖）。
		return strings.Join(args[1:], " ") + "\n", true
	case "printf":
		if len(args) < 2 {
			return "", true
		}
		format := strings.ReplaceAll(args[1], `\n`, "\n")
		var rest []any
		for _, a := range args[2:] {
			rest = append(rest, a)
		}
		return fmt.Sprintf(format, rest...), true
	case "cat":
		var sb strings.Builder
		for _, f := range args[1:] {
			b, ok := fs.readFile(f)
			if !ok {
				return sb.String() + "cat: " + f + ": No such file or directory\n", false
			}
			sb.Write(b)
		}
		return sb.String(), true
	case "base64":
		decode := false
		for _, a := range args[1:] {
			if a == "-d" {
				decode = true
			}
		}
		if decode {
			b, err := tolerantB64Decode(stdin)
			if err != nil {
				return "base64: invalid input\n", false
			}
			return string(b), true
		}
		return base64.StdEncoding.EncodeToString([]byte(stdin)) + "\n", true
	case "dd":
		var ifile string
		var bs, skip int
		for _, a := range args[1:] {
			kv := strings.SplitN(a, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "if":
				ifile = kv[1]
			case "bs":
				bs, _ = strconv.Atoi(kv[1])
			case "skip":
				skip, _ = strconv.Atoi(kv[1])
			}
		}
		b, ok := fs.readFile(ifile)
		if !ok {
			return "dd: failed to open '" + ifile + "': No such file or directory\n", false
		}
		off := bs * skip
		if off >= len(b) {
			return "", true
		}
		end := off + bs
		if end > len(b) || bs == 0 {
			end = len(b)
		}
		return string(b[off:end]), true
	case "sh":
		if len(args) < 2 {
			return "sh: missing script\n", false
		}
		b, ok := fs.readFile(args[1])
		if !ok {
			return "sh: 0: cannot open " + args[1] + ": No such file\n", false
		}
		return fs.run(string(b))
	case "rm":
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			fs.remove(a)
		}
		return "", true
	case "touch":
		for _, a := range args[1:] {
			if _, ok := fs.readFile(a); !ok {
				fs.writeFile(a, nil, false)
			}
		}
		return "", true
	case "id":
		return "uid=33(www-data) gid=33(www-data) groups=33(www-data)\n", true
	case "whoami":
		return "www-data\n", true
	case "uname":
		return "Linux\n", true
	case "tr":
		if len(args) >= 3 && args[1] == "a-z" && args[2] == "A-Z" {
			return strings.ToUpper(stdin), true
		}
		return stdin, true
	case "true":
		return "", true
	}
	return "sh: " + args[0] + ": not found\n", false
}

// ---------------- Windows cmd 模拟（ASPX 用） ----------------

// fakeCmd 模拟 cmd.exe /c 的最小语义:& 连接、未定义 %VAR% 展开为空、
// echo/type/certutil/del。
type fakeCmd struct{ fs *fakeFS }

var cmdVarRe = regexp.MustCompile(`%[A-Za-z_][A-Za-z0-9_]*%`)

func (c *fakeCmd) run(cmdline string) string {
	cmdline = cmdVarRe.ReplaceAllString(cmdline, "") // 批处理:未定义变量展开为空
	var out strings.Builder
	for _, seg := range strings.Split(cmdline, "&") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		out.WriteString(c.runOne(seg))
	}
	return out.String()
}

func (c *fakeCmd) runOne(seg string) string {
	args := tokenize(seg)
	var clean []string
	for _, a := range args {
		if a == "2>&1" {
			continue
		}
		clean = append(clean, a)
	}
	if len(clean) == 0 {
		return ""
	}
	switch strings.ToLower(clean[0]) {
	case "echo":
		return strings.Join(clean[1:], " ") + "\r\n"
	case "type":
		if len(clean) < 2 {
			return ""
		}
		b, ok := c.fs.readFile(clean[1])
		if !ok {
			return "The system cannot find the file specified.\r\n"
		}
		return string(b)
	case "certutil":
		// certutil -decode -f <in> <out>
		var in, out string
		for i := 1; i < len(clean); i++ {
			if strings.HasPrefix(clean[i], "-") {
				continue
			}
			if in == "" {
				in = clean[i]
			} else {
				out = clean[i]
			}
		}
		b, ok := c.fs.readFile(in)
		if !ok {
			return "CertUtil: The system cannot find the file specified.\r\n"
		}
		dec, err := tolerantB64Decode(string(b))
		if err != nil {
			return "CertUtil: -decode command failed.\r\n"
		}
		c.fs.writeFile(out, dec, false)
		return "CertUtil: -decode command completed successfully.\r\n"
	case "del":
		for _, a := range clean[1:] {
			if strings.HasPrefix(a, "/") {
				continue
			}
			c.fs.remove(a)
		}
		return ""
	case "whoami":
		return "nt authority\\network service\r\n"
	case "ver":
		return "Microsoft Windows [Version 10.0.20348.1]\r\n"
	}
	return "'" + clean[0] + "' is not recognized as an internal or external command\r\n"
}
