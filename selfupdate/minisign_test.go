package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// signLikeMinisig 用一对临时 key 按 minisign 的预哈希格式签名，返回公钥行
// 与 .minisig 文本。这是验签方的镜像实现，只用于测试。
func signLikeMinisig(t *testing.T, msg []byte) (pubB64, minisig string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成临时 key: %v", err)
	}
	var keynum [minisignKeynumLen]byte
	if _, err := rand.Read(keynum[:]); err != nil {
		t.Fatalf("生成 keynum: %v", err)
	}

	pubRaw := append([]byte(minisignAlgPubKey), keynum[:]...)
	pubRaw = append(pubRaw, pub...)
	pubB64 = base64.StdEncoding.EncodeToString(pubRaw)

	digest := blake2b.Sum512(msg)
	sig := ed25519.Sign(priv, digest[:])
	sigRaw := append([]byte(minisignAlgPrehashed), keynum[:]...)
	sigRaw = append(sigRaw, sig...)
	return pubB64, "untrusted comment: test\n" + base64.StdEncoding.EncodeToString(sigRaw) + "\n" +
		"trusted comment: test\n"
}

func TestVerifyMinisigRoundTrip(t *testing.T) {
	msg := []byte("deadbeef  artex-0.0.0-linux-amd64.zip\n")
	pub, sig := signLikeMinisig(t, msg)
	if err := verifyMinisig(pub, sig, msg); err != nil {
		t.Fatalf("自签自验应通过: %v", err)
	}
}

// 与真实 minisign 工具的兼容性向量：.minisig 由官方 minisign.exe 0.12 用
// 本仓库内嵌公钥对应的私钥签出（SUMS 内容固定）。验不过说明格式解析与
// 真实发布流程脱节，CI 签出来的东西用户端会全部拒装。
func TestVerifyMinisigRealMinisignVector(t *testing.T) {
	sums := []byte("abc123  artex-0.0.0-test.zip\n")
	const realSig = `untrusted comment: signature from minisign secret key
RUSXIi1nUNeBgQlNNluEYr0xUtbczexTc7tErAAcT9PewbUgc5yR/SqhkoptD4XymJGhEnTDMaY9FK5FSGsvPIae5oW1oRf+Igg=
trusted comment: timestamp:1789274763	file:SUMS	hashed
hPfyMYz7wNoDz1PWTzLZc9KGIo0zFAsvOjgqBxwWqkDSy7+ySAuiqac0pIdGDr+tHTOW+CokYXmZI8VPPHMsBQ==
`
	if err := verifyMinisig(embeddedPubKey, realSig, sums); err != nil {
		t.Fatalf("官方 minisign 签出的签名应能通过: %v", err)
	}
}

func TestVerifyMinisigRejectsTamperedMessage(t *testing.T) {
	msg := []byte("deadbeef  artex-0.0.0-linux-amd64.zip\n")
	pub, sig := signLikeMinisig(t, msg)

	tampered := []byte("aaaaaaab  artex-0.0.0-linux-amd64.zip\n") // 篡改清单里一个摘要
	if err := verifyMinisig(pub, sig, tampered); err == nil {
		t.Fatal("消息被篡改后验签应失败")
	}
}

func TestVerifyMinisigRejectsTamperedSignature(t *testing.T) {
	msg := []byte("x\n")
	pub, sig := signLikeMinisig(t, msg)
	// 解码、翻转签名一个字节、重新编码，模拟传输损坏/手工改动。
	lines := strings.Split(sig, "\n")
	raw, _ := base64.StdEncoding.DecodeString(lines[1])
	raw[len(raw)-1] ^= 0x01
	lines[1] = base64.StdEncoding.EncodeToString(raw)
	if err := verifyMinisig(pub, strings.Join(lines, "\n"), msg); err == nil {
		t.Fatal("签名被篡改后验签应失败")
	}
}

func TestVerifyMinisigRejectsWrongKey(t *testing.T) {
	msg := []byte("x\n")
	_, sig := signLikeMinisig(t, msg)
	otherPub, _ := signLikeMinisig(t, msg) // 另一对 key，keynum 与公钥都不同
	if err := verifyMinisig(otherPub, sig, msg); err == nil {
		t.Fatal("用别的 key 验签应失败")
	}
}

func TestVerifyMinisigRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"",
		"not a signature at all",
		"untrusted comment: only comments\ntrusted comment: nothing here\n",
		base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		if err := verifyMinisig(embeddedPubKey, raw, []byte("x")); err == nil {
			t.Errorf("畸形输入 %q 应被拒绝", raw)
		}
	}
}

// 缺签名文件必须 fail-closed：没有 .minisig 资产时直接拒绝，不发起任何网络请求。
func TestVerifySumsSignatureRejectsMissingAsset(t *testing.T) {
	rel := &Release{Assets: []Asset{{Name: sumsAsset}}}
	err := verifySumsSignature(t.Context(), nil, rel, []byte("x"), nil)
	if err == nil || !strings.Contains(err.Error(), sigAsset) {
		t.Fatalf("缺少 %s 时应拒绝升级，得到: %v", sigAsset, err)
	}
}

// 逃生门：ARTEX_UPDATE_SKIP_SIGN=1 时连签名资产都不找，但要通过 prog 打警告。
func TestVerifySumsSignatureSkipEnv(t *testing.T) {
	t.Setenv(skipSignEnv, "1")
	rel := &Release{} // 连 SHA256SUMS 都没有，不跳过的话必然失败
	warned := false
	err := verifySumsSignature(t.Context(), nil, rel, []byte("x"), func(_ Phase, _ int, msg string) {
		if strings.Contains(msg, "警告") {
			warned = true
		}
	})
	if err != nil {
		t.Fatalf("设了 %s=1 应跳过验签: %v", skipSignEnv, err)
	}
	if !warned {
		t.Error("跳过验签时必须打警告")
	}
}
