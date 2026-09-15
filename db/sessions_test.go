package db

// sessions_test.go:sessions 落库层测试。加解密 roundtrip 不依赖 PG;
// CRUD 走现有 PG-gated skip 模式（无库即跳）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionSecretCrypto 不依赖 PG:AES-GCM roundtrip、错密钥失败、
// 未初始化密钥拒绝明文落库、未知格式拒绝按明文读取。
func TestSessionSecretCrypto(t *testing.T) {
	master := []byte("test-jwt-key-material-32bytes!!!")
	key := SessionSecretKey(master)
	if len(key) != 32 {
		t.Fatalf("派生密钥长度 = %d", len(key))
	}
	// 域分离:派生密钥不得等于 master 本身或其直接哈希之外的可用形式。
	other := SessionSecretKey([]byte("another-master-key-32bytes!!!!"))
	if string(key) == string(other) {
		t.Fatal("不同 master 派生出相同密钥")
	}

	store := &SessionStore{key: key}
	secret := json.RawMessage(`{"url":"http://example.local/shell.php","password":"cmd","lang":"php"}`)
	enc, err := store.encrypt(secret)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, secretPrefix) {
		t.Fatalf("密文缺版本前缀: %q", enc[:16])
	}
	if strings.Contains(enc, "shell.php") || strings.Contains(enc, "cmd") {
		t.Fatal("密文不得包含明文片段")
	}
	dec, err := store.decrypt(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(dec) != string(secret) {
		t.Fatalf("roundtrip 不一致: %q", dec)
	}
	// 两次加密密文不同（随机 nonce)。
	enc2, _ := store.encrypt(secret)
	if enc == enc2 {
		t.Fatal("nonce 未随机化：两次密文相同")
	}
	// 错密钥解密必须失败（诚实，不返回垃圾）。
	wrong := &SessionStore{key: other}
	if _, err := wrong.decrypt(enc); err == nil {
		t.Fatal("错密钥解密应失败")
	}
	// 未初始化密钥:拒绝加密（不落明文）也拒绝解密。
	noKey := &SessionStore{}
	if _, err := noKey.encrypt(secret); err == nil {
		t.Fatal("密钥未初始化应拒绝加密")
	}
	if _, err := noKey.decrypt(enc); err == nil {
		t.Fatal("密钥未初始化应拒绝解密")
	}
	// 无版本前缀的历史/异常数据：拒绝按明文读取。
	if _, err := store.decrypt(`{"url":"x"}`); err == nil {
		t.Fatal("未知格式应拒绝按明文读取")
	}
	// 篡改密文:GCM 认证失败。
	tampered := enc[:len(enc)-4] + "AAAA"
	if _, err := store.decrypt(tampered); err == nil {
		t.Fatal("篡改密文应认证失败")
	}
}

// TestSessionStoreCRUD 走 live PG（现有 skip 模式：无库即跳）。
func TestSessionStoreCRUD(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	store := NewSessionStore(d, []byte("test-jwt-key-material-32bytes!!!"))
	ctx := context.Background()

	rec := &SessionRecord{
		Kind: "http_php", URL: "http://target.local/s.php",
		Secret:          json.RawMessage(`{"url":"http://target.local/s.php","password":"cmd","lang":"php","variant":"eval","funcs":["exec"]}`),
		Lang:            "php",
		CreatedByTask:   0, // 可空列
		CreatedByIntent: 0,
	}
	id, err := store.Create(ctx, rec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = store.Delete(context.Background(), id) })

	// DB 里不得是明文。
	var raw string
	if err := d.QueryRow(`SELECT secret FROM sessions WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, secretPrefix) || strings.Contains(raw, "target.local") {
		t.Fatalf("落库 secret 应为 gcm1 密文: %q", raw[:24])
	}

	got, err := store.Get(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Kind != "http_php" || got.Status != SessionAlive || got.Lang != "php" {
		t.Fatalf("got = %+v", got)
	}
	if !strings.Contains(string(got.Secret), `"password":"cmd"`) {
		t.Fatalf("secret 解密失败: %q", got.Secret)
	}

	// List + status 过滤。
	alive, err := store.List(ctx, SessionAlive)
	if err != nil || len(alive) == 0 {
		t.Fatalf("List alive: %v", err)
	}
	found := false
	for _, r := range alive {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List(alive) 未包含新建会话")
	}

	// UpdateStatus + Touch + SetHostAsset。
	if err := store.UpdateStatus(ctx, id, SessionDead); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := store.UpdateStatus(ctx, id, "bogus"); err == nil {
		t.Fatal("非法状态应报错")
	}
	store.Touch(ctx, id)
	store.SetHostAsset(ctx, id, 12345)
	got, _ = store.Get(ctx, id)
	if got.Status != SessionDead || got.HostAssetID != 12345 {
		t.Fatalf("状态/资产回填失败: %+v", got)
	}
	dead, err := store.List(ctx, SessionDead)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, r := range dead {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatal("List(dead) 未包含新建会话")
	}

	// Delete + Get miss。
	if err := store.Delete(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = store.Get(ctx, id)
	if err != nil || got != nil {
		t.Fatalf("删除后 Get 应为 (nil,nil), got %v, %v", got, err)
	}
}
