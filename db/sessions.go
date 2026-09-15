// sessions.go 是立足点会话的落库层（schema.sql §J)。secret（连接参数 JSON)
// 用 AES-GCM 加密存储：密钥由 jwt.key 派生（SHA-256 域分离）,参照 server/auth.go
// 的密钥管理模式——master key 只存在于 keyDir/jwt.key,DB 里不落明文。
package db

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// SessionRecord 是 sessions 表的一行。Secret 在 Go 层是明文 JSON;Create 加密、
// Get/List 解密，密钥不出 SessionStore。
type SessionRecord struct {
	ID              int64           `json:"id"`
	Kind            string          `json:"kind"` // http_php/http_jsp/http_aspx/ssh/reverse(预留)
	HostAssetID     int64           `json:"host_asset_id,omitempty"`
	URL             string          `json:"url"`
	Secret          json.RawMessage `json:"secret"` // 明文 JSON;落库时加密
	Lang            string          `json:"lang"`
	Status          string          `json:"status"` // alive | dead
	CreatedByTask   int64           `json:"created_by_task,omitempty"`
	CreatedByIntent int64           `json:"created_by_intent,omitempty"`
	LastBeat        time.Time       `json:"last_beat"`
	CreatedAt       time.Time       `json:"created_at"`
}

const (
	SessionAlive = "alive"
	SessionDead  = "dead"

	// secretPrefix 标记密文格式版本：gcm1:<base64(nonce|ciphertext)>。
	secretPrefix = "gcm1:"
)

// SessionSecretKey 由 master key(jwt.key 内容）派生会话加密密钥：
// SHA-256 域分离，与 JWT 签名用途隔离。
func SessionSecretKey(master []byte) []byte {
	sum := sha256.Sum256(append([]byte("artex/sessions/v1\x00"), master...))
	return sum[:]
}

// SessionStore 是 sessions 表的访问层。key 为 32 字节 AES 密钥
// （经 SessionSecretKey 派生）;key 缺失时加解密直接报错（诚实失败，不落明文）。
type SessionStore struct {
	d   *DB
	key []byte
}

// NewSessionStore 构造访问层。masterKey 传 jwt.key 内容；内部自行派生。
func NewSessionStore(d *DB, masterKey []byte) *SessionStore {
	var key []byte
	if len(masterKey) > 0 {
		key = SessionSecretKey(masterKey)
	}
	return &SessionStore{d: d, key: key}
}

func (s *SessionStore) encrypt(plain []byte) (string, error) {
	if len(s.key) != 32 {
		return "", fmt.Errorf("会话加密密钥未初始化，拒绝明文落库")
	}
	gcm, err := newGCM(s.key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func (s *SessionStore) decrypt(stored string) ([]byte, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	if len(s.key) != 32 {
		return nil, fmt.Errorf("会话加密密钥未初始化，无法解密")
	}
	if !hasPrefix(stored, secretPrefix) {
		return nil, fmt.Errorf("未知 secret 格式（缺 gcm1: 前缀）,拒绝按明文读取")
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(secretPrefix):])
	if err != nil {
		return nil, fmt.Errorf("secret base64 解码失败: %w", err)
	}
	gcm, err := newGCM(s.key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, fmt.Errorf("secret 密文过短")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return nil, fmt.Errorf("secret 解密失败（密钥不符或数据被篡改）: %w", err)
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// nilIfZero 把 0 转成 NULL（可空外键列）。
func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// Create 插入一条会话并返回主键。Secret 加密存储。
func (s *SessionStore) Create(ctx context.Context, rec *SessionRecord) (int64, error) {
	if rec.Kind == "" {
		return 0, fmt.Errorf("kind 必填")
	}
	enc, err := s.encrypt(rec.Secret)
	if err != nil {
		return 0, err
	}
	status := rec.Status
	if status == "" {
		status = SessionAlive
	}
	var id int64
	err = s.d.QueryRowContext(ctx, `
INSERT INTO sessions(kind, host_asset_id, url, secret, lang, status, created_by_task, created_by_intent)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id`,
		rec.Kind, nilIfZero(rec.HostAssetID), rec.URL, enc, rec.Lang, status,
		nilIfZero(rec.CreatedByTask), nilIfZero(rec.CreatedByIntent)).Scan(&id)
	return id, err
}

const sessionCols = `id, kind, COALESCE(host_asset_id,0), url, secret, lang, status,
	COALESCE(created_by_task,0), COALESCE(created_by_intent,0), last_beat, created_at`

func (s *SessionStore) scan(row interface{ Scan(...any) error }) (*SessionRecord, error) {
	var r SessionRecord
	var enc string
	if err := row.Scan(&r.ID, &r.Kind, &r.HostAssetID, &r.URL, &enc, &r.Lang, &r.Status,
		&r.CreatedByTask, &r.CreatedByIntent, &r.LastBeat, &r.CreatedAt); err != nil {
		return nil, err
	}
	plain, err := s.decrypt(enc)
	if err != nil {
		return nil, fmt.Errorf("session %d: %w", r.ID, err)
	}
	r.Secret = plain
	return &r, nil
}

// Get 取一条（含解密 secret);不存在返回 (nil, nil)。
func (s *SessionStore) Get(ctx context.Context, id int64) (*SessionRecord, error) {
	r, err := s.scan(s.d.QueryRowContext(ctx,
		`SELECT `+sessionCols+` FROM sessions WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// List 取全部（按 id 升序）;status 非空时按状态过滤。
func (s *SessionStore) List(ctx context.Context, status string) ([]*SessionRecord, error) {
	query := `SELECT ` + sessionCols + ` FROM sessions`
	args := []any{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	rows, err := s.d.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*SessionRecord{}
	for rows.Next() {
		r, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateStatus 更新 alive/dead 状态。
func (s *SessionStore) UpdateStatus(ctx context.Context, id int64, status string) error {
	if status != SessionAlive && status != SessionDead {
		return fmt.Errorf("非法会话状态 %q（alive|dead)", status)
	}
	_, err := s.d.ExecContext(ctx, `UPDATE sessions SET status=$2 WHERE id=$1`, id, status)
	return err
}

// Touch 刷新 last_beat（每次使用会话时调用）。
func (s *SessionStore) Touch(ctx context.Context, id int64) {
	_, _ = s.d.ExecContext(ctx, `UPDATE sessions SET last_beat=now() WHERE id=$1`, id)
}

// SetHostAsset 回填 webshell 关联的 endpoint 资产 id。
func (s *SessionStore) SetHostAsset(ctx context.Context, id, assetID int64) {
	_, _ = s.d.ExecContext(ctx, `UPDATE sessions SET host_asset_id=$2 WHERE id=$1`, id, assetID)
}

// Delete 删除一条。
func (s *SessionStore) Delete(ctx context.Context, id int64) error {
	_, err := s.d.ExecContext(ctx, `DELETE FROM sessions WHERE id=$1`, id)
	return err
}
