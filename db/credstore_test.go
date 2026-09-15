package db

// credstore_test.go:凭据落库层测试。加解密 roundtrip 不依赖 PG;
// CRUD 走现有 PG-gated skip 模式(无库即跳)。

import (
	"context"
	"strings"
	"testing"
)

// TestCredentialSecretCrypto 不依赖 PG:AES-GCM roundtrip、与 sessions 的域分离、
// 错密钥失败、未初始化密钥拒绝明文落库、篡改认证失败。
func TestCredentialSecretCrypto(t *testing.T) {
	master := []byte("test-jwt-key-material-32bytes!!!")
	key := CredentialSecretKey(master)
	if len(key) != 32 {
		t.Fatalf("派生密钥长度 = %d", len(key))
	}
	// 域分离:与 sessions 派生密钥不同(同 master、不同域标签)。
	if string(key) == string(SessionSecretKey(master)) {
		t.Fatal("credentials 与 sessions 派生密钥相同,域分离失效")
	}

	store := &CredentialStore{key: key}
	enc, err := store.encrypt("P@ssw0rd!")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, secretPrefix) {
		t.Fatalf("密文缺版本前缀: %q", enc[:16])
	}
	if strings.Contains(enc, "P@ssw0rd") {
		t.Fatal("密文不得包含明文片段")
	}
	dec, err := store.decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != "P@ssw0rd!" {
		t.Fatalf("roundtrip 不一致: %q", dec)
	}
	// 两次加密密文不同(随机 nonce)。
	enc2, _ := store.encrypt("P@ssw0rd!")
	if enc == enc2 {
		t.Fatal("nonce 未随机化:两次密文相同")
	}
	// 错密钥(含 sessions 密钥)解密必须失败。
	wrong := &CredentialStore{key: CredentialSecretKey([]byte("another-master-key-32bytes!!!!"))}
	if _, err := wrong.decrypt(enc); err == nil {
		t.Fatal("错密钥解密应失败")
	}
	cross := &CredentialStore{key: SessionSecretKey(master)}
	if _, err := cross.decrypt(enc); err == nil {
		t.Fatal("sessions 密钥解 credentials 密文应失败(域分离)")
	}
	// 未初始化密钥:拒绝加密(不落明文)也拒绝解密。
	noKey := &CredentialStore{}
	if _, err := noKey.encrypt("x"); err == nil {
		t.Fatal("密钥未初始化应拒绝加密")
	}
	if _, err := noKey.decrypt(enc); err == nil {
		t.Fatal("密钥未初始化应拒绝解密")
	}
	// 无版本前缀:拒绝按明文读取。
	if _, err := store.decrypt("P@ssw0rd!"); err == nil {
		t.Fatal("未知格式应拒绝按明文读取")
	}
	// 篡改密文:GCM 认证失败。
	tampered := enc[:len(enc)-4] + "AAAA"
	if _, err := store.decrypt(tampered); err == nil {
		t.Fatal("篡改密文应认证失败")
	}
}

// TestMaskSecret 脱敏:前后各 2 字符,过短全掩码。
func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"P@ssw0rd!": "P@****d!",
		"abcdef":    "ab****ef",
		"abcde":     "ab****de",
		"abcd":      "****",
		"a":         "****",
		"":          "****",
	}
	for in, want := range cases {
		if got := MaskSecret(in); got != want {
			t.Errorf("MaskSecret(%q) = %q,期望 %q", in, got, want)
		}
	}
}

// TestValidCredType 类型全集校验。
func TestValidCredType(t *testing.T) {
	for _, ty := range []string{"password", "hash_nt", "hash_lm", "hash_sha1", "ticket", "ssh_key", "token"} {
		if !ValidCredType(ty) {
			t.Errorf("%s 应合法", ty)
		}
	}
	for _, ty := range []string{"", "ntlm", "PASSWORD", "key"} {
		if ValidCredType(ty) {
			t.Errorf("%q 不应合法", ty)
		}
	}
}

// TestCredentialStoreCRUD 走 live PG(现有 skip 模式:无库即跳)。
func TestCredentialStoreCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	store := NewCredentialStore(d, []byte("test-jwt-key-material-32bytes!!!"))
	ctx := context.Background()

	// 校验:非法类型/空 secret/缺 task 直接报错,不落库。
	if _, err := store.Create(ctx, &CredentialRecord{TaskID: 1, CredType: "ntlm", Secret: "x", Source: "t"}); err == nil {
		t.Fatal("非法 cred_type 应报错")
	}
	if _, err := store.Create(ctx, &CredentialRecord{TaskID: 1, CredType: "password", Source: "t"}); err == nil {
		t.Fatal("空 secret 应报错")
	}
	if _, err := store.Create(ctx, &CredentialRecord{CredType: "password", Secret: "x", Source: "t"}); err == nil {
		t.Fatal("缺 task_id 应报错")
	}

	rec := &CredentialRecord{
		TaskID: 999999001, Username: "administrator", CredType: "hash_nt",
		Secret: "aad3b435b51404eeaad3b435b51404ee", Domain: "CORP",
		Source: "10.0.0.5 mimikatz lsass dump",
	}
	id, err := store.Create(ctx, rec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), id) })

	// DB 里不得是明文。
	var raw string
	if err := d.QueryRow(`SELECT secret FROM credentials WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, secretPrefix) || strings.Contains(raw, "aad3b435") {
		t.Fatalf("落库 secret 应为 gcm1 密文: %q", raw[:24])
	}

	got, err := store.Get(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Secret != rec.Secret || got.Domain != "CORP" || got.Verified {
		t.Fatalf("got = %+v", got)
	}

	// ListByTask 只回本任务。
	other := &CredentialRecord{TaskID: 999999002, CredType: "password", Secret: "x12345", Source: "t"}
	oid, err := store.Create(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), oid) })
	list, err := store.ListByTask(ctx, 999999001)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("ListByTask = %v, %v", list, err)
	}

	// SetVerified。
	if err := store.SetVerified(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Get(ctx, id)
	if !got.Verified {
		t.Fatal("SetVerified 未生效")
	}

	// Delete + Get miss。
	if err := store.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, id)
	if err != nil || got != nil {
		t.Fatalf("删除后 Get 应为 (nil,nil), got %v, %v", got, err)
	}
}
