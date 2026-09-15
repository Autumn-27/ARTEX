package selfupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// sigAsset 是 release.yml 用 minisign 对 SHA256SUMS 生成的签名文件。
const sigAsset = sumsAsset + ".minisig"

// embeddedPubKey 是 ARTEX Release 的 minisign 公钥（base64，对应 minisign -P 的输出）。
// 私钥不入库，只保存在 GitHub Secrets 的 MINISIGN_KEY；换 key 时这里与 secret 同步更换。
const embeddedPubKey = "RWSXIi1nUNeBgfwSraBVDfhBz6DC3SE0sWAK1z5CaLMaqAY7NAcerhIa"

// skipSignEnv 是逃生门：置 1 时跳过验签（仅降级兼容旧 Release 等紧急场景使用，
// 会打警告）。默认 fail-closed：签名缺失或验不过都拒绝安装。
const skipSignEnv = "ARTEX_UPDATE_SKIP_SIGN"

// minisign 文件格式（参考 https://jedisct1.github.io/minisign/ 与 minisign 源码）：
//
//	公钥: base64( "Ed" || keynum(8) || pubkey(32) )                     42 字节
//	签名: base64( "ED" || keynum(8) || ed25519_sig(64) )                74 字节
//
// 算法标记大小写不同：公钥固定为 "Ed"；签名里 "ED"（大写 D）表示预哈希模式，
// 签名对象是 Blake2b-512(message)——minisign ≥0.9 只产出这种。小写 "Ed" 是
// 上古版本直接签原始消息的格式，本实现不支持。
const (
	minisignAlgPubKey    = "Ed"
	minisignAlgPrehashed = "ED"
	minisignKeynumLen    = 8
)

// parsePubKey 解析 minisign 公钥行（base64），返回 keynum 与 Ed25519 公钥。
func parsePubKey(b64 string) ([minisignKeynumLen]byte, ed25519.PublicKey, error) {
	var keynum [minisignKeynumLen]byte
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return keynum, nil, fmt.Errorf("公钥 base64 解码失败: %w", err)
	}
	if len(raw) != 2+minisignKeynumLen+ed25519.PublicKeySize || string(raw[:2]) != minisignAlgPubKey {
		return keynum, nil, fmt.Errorf("公钥格式无法识别（期望 minisign Ed25519 公钥）")
	}
	copy(keynum[:], raw[2:2+minisignKeynumLen])
	return keynum, ed25519.PublicKey(raw[2+minisignKeynumLen:]), nil
}

// parseMinisig 从 .minisig 文本里取出主签名。只认第一个 base64 行
// （trusted comment 及其全局签名不参与校验——更新链路不消费注释内容）。
func parseMinisig(raw string) ([minisignKeynumLen]byte, []byte, error) {
	var keynum [minisignKeynumLen]byte
	for line := range strings.Lines(raw) {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, ":") { // 注释行形如 "untrusted comment: …"
			continue
		}
		dec, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return keynum, nil, fmt.Errorf("签名 base64 解码失败: %w", err)
		}
		if len(dec) != 2+minisignKeynumLen+ed25519.SignatureSize || string(dec[:2]) != minisignAlgPrehashed {
			return keynum, nil, fmt.Errorf("签名格式无法识别（期望预哈希 Ed25519 签名）")
		}
		copy(keynum[:], dec[2:2+minisignKeynumLen])
		return keynum, dec[2+minisignKeynumLen:], nil
	}
	return keynum, nil, fmt.Errorf("%s 里没有签名数据", sigAsset)
}

// verifyMinisig 用 pubB64 校验 sigRaw 是否为 msg 的有效 minisign 签名。
func verifyMinisig(pubB64, sigRaw string, msg []byte) error {
	wantKeynum, pub, err := parsePubKey(pubB64)
	if err != nil {
		return err
	}
	gotKeynum, sig, err := parseMinisig(sigRaw)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotKeynum[:], wantKeynum[:]) {
		return fmt.Errorf("签名 keynum 与内置公钥不匹配（不是本项目的发布 key 签的）")
	}
	digest := blake2b.Sum512(msg)
	if !ed25519.Verify(pub, digest[:], sig) {
		return fmt.Errorf("minisign 验签失败：%s 被篡改或签名者与内置公钥不符", sumsAsset)
	}
	return nil
}

// verifySumsSignature 下载 sigAsset 并对 sums（SHA256SUMS 原始字节）验签。
// 验签与 SHA256 比对串行叠加：签名保证清单来源可信，哈希保证 zip 字节完整。
func verifySumsSignature(ctx context.Context, c *http.Client, rel *Release, sums []byte, prog Progress) error {
	if os.Getenv(skipSignEnv) == "1" {
		if prog != nil {
			prog(PhaseVerify, -1, fmt.Sprintf("警告：%s=1，已跳过发布签名验证（仅限紧急兼容场景）", skipSignEnv))
		}
		return nil
	}

	asset, ok := rel.FindAsset(sigAsset)
	if !ok {
		return fmt.Errorf("该 Release 没有 %s，无法验证来源，拒绝升级（紧急情况下可设 %s=1 跳过）", sigAsset, skipSignEnv)
	}
	body, err := get(ctx, c, asset.URL)
	if err != nil {
		return fmt.Errorf("下载 %s: %w", sigAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<16)) // 签名文件正常只有几百字节
	if err != nil {
		return fmt.Errorf("读取 %s: %w", sigAsset, err)
	}
	return verifyMinisig(embeddedPubKey, string(raw), sums)
}
