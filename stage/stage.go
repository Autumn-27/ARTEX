// stage 是受管文件投递暂存服务（F13；架构原则见 INTRANET-PIVOT-DESIGN.md 4.3a，
// 语义参照 PivotHub filestage）：worker 把「工作区里已生成的工具/payload」交给平台，
// 平台以 24 字节随机 token 路径（/s/<token>/<name>）提供下载，无目录列举、硬 TTL
// （默认 15 分钟、上限 2h、到期自动销毁）、可选一次性下载即焚，全程记账
// （Put/下载/过期/销毁）。下载路径无需 JWT（目标机器没有 token），用随机 token
// + 每 token 速率限制防枚举；错误 token 与不存在路径的响应逐字节一致。
//
// 边界：暂存只用于【向外投递工具】；战利品/凭据回流一律走会话读取/evidence，
// 制度上保证暂存服务被打穿也没有敏感数据可泄。
package stage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTTL 是未显式指定时的存活期；MaxTTL 是硬上限——暂存不是存储，
	// 超期即孤儿（python -m http.server 忘关的通病就是从这里来的）。
	DefaultTTL = 15 * time.Minute
	MaxTTL     = 2 * time.Hour
	minTTL     = time.Minute

	tokenBytes = 24 // crypto/rand → base64url 32 字符

	gcInterval = time.Minute

	// 下载防枚举限速：单 token 每分钟尝试上限 + 单源 IP 每分钟尝试上限。
	// 命中限速与「token 不存在」响应一致（同一个 404），不给枚举者任何信号。
	tokenRateLimit  = 10
	ipRateLimit     = 60
	rateLimitWindow = time.Minute
)

// PutOpts 是一次暂存的选项。TTL<=0 取 DefaultTTL，超过 MaxTTL 被夹回 MaxTTL。
type PutOpts struct {
	TTL     time.Duration
	OneShot bool   // 首次成功完整下载后即焚
	TaskID  string // 归属任务（activity 记账用；空 = 无任务上下文）
}

// Entry 是一条暂存记录的对外视图。
type Entry struct {
	Token     string    `json:"token"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	URL       string    `json:"url"` // baseURL 已知时为绝对地址，否则为 /s/ 相对路径
	ExpiresAt time.Time `json:"expires_at"`
	OneShot   bool      `json:"one_shot"`
	TaskID    string    `json:"task_id,omitempty"`
	Hits      int64     `json:"hits"`        // 成功完整下载次数
	Bytes     int64     `json:"bytes_served"` // 累计实际写出字节数（含不完整下载）
	CreatedAt time.Time `json:"created_at"`
}

// Event 是一次记账事件（Put/下载/过期/销毁）。由 OnEvent 回调交给宿主写
// activity / 日志；Kind 取值见 EventKind* 常量。
type Event struct {
	Kind   string // put | download | expire | destroy
	Entry  Entry
	Remote string // 下载来源 IP（仅 download）
	Bytes  int64  // 本次写出字节数（仅 download）
}

const (
	EventPut      = "put"
	EventDownload = "download"
	EventExpire   = "expire"
	EventDestroy  = "destroy"
)

// Manager 是进程内暂存管理器。条目只活在本进程内存里——重启后 data/stage/
// 下的残留文件已无台账，New 时会整体清空（过期资源绝不跨进程续命）。
type Manager struct {
	dir string

	mu      sync.Mutex
	entries map[string]*Entry

	baseURL string // 可选绝对基址（如 http://10.0.0.2:8787），决定 Entry.URL 形态

	// OnEvent 非 nil 时每个记账事件同步回调一次。回调必须快（在锁外调用，
	// 但慢回调会拖住调用方 goroutine）。写 activity / 日志由宿主实现。
	OnEvent func(Event)

	rateMu   sync.Mutex
	tokenHit map[string][]time.Time // token → 最近的下载尝试时间
	ipHit    map[string][]time.Time

	stopGC context.CancelFunc
}

// New 创建管理器：目录 0700、清空上一进程的残留文件、启动周期清理 goroutine
// （ctx 取消时退出并停表）。
func New(ctx context.Context, dir string) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("stage dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // 已存在的目录可能权限更宽
		return nil, fmt.Errorf("stage dir chmod: %w", err)
	}
	// 台账是进程内的，重启即全部作废——残留文件没有存在的理由，清掉。
	if errs := clearDir(dir); errs != nil {
		return nil, fmt.Errorf("stage dir sweep: %w", errs[0])
	}
	m := &Manager{
		dir:      dir,
		entries:  map[string]*Entry{},
		tokenHit: map[string][]time.Time{},
		ipHit:    map[string][]time.Time{},
	}
	gcCtx, cancel := context.WithCancel(ctx)
	m.stopGC = cancel
	go m.gcLoop(gcCtx)
	return m, nil
}

// Close 停掉后台清理 goroutine（不删除未过期条目，进程退出即台账作废）。
func (m *Manager) Close() {
	if m.stopGC != nil {
		m.stopGC()
	}
}

// SetBaseURL 设置对外绝对基址（拼进 Entry.URL 与工具返回）。空 = 只给相对路径，
// 由调用方自行补主机（目标机器到平台的可达地址平台侧无从权威得知）。
func (m *Manager) SetBaseURL(u string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseURL = strings.TrimRight(u, "/")
}

// PutFile 暂存磁盘上已有的文件（按流拷贝，不吃内存）。name 取源文件 basename。
func (m *Manager) PutFile(srcPath string, o PutOpts) (*Entry, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", srcPath)
	}
	name := filepath.Base(srcPath)
	return m.put(name, src, o)
}

// PutContent 暂存一段内存内容，name 必填（会取 basename，防路径逃逸）。
func (m *Manager) PutContent(name string, data []byte, o PutOpts) (*Entry, error) {
	return m.put(name, bytes.NewReader(data), o)
}

func (m *Manager) put(name string, r io.Reader, o PutOpts) (*Entry, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return nil, errors.New("invalid file name")
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < minTTL {
		ttl = minTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(m.dir, token)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	size, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		if copyErr != nil {
			return nil, copyErr
		}
		return nil, closeErr
	}
	now := time.Now()
	e := &Entry{
		Token:     token,
		Name:      name,
		Size:      size,
		ExpiresAt: now.Add(ttl),
		OneShot:   o.OneShot,
		TaskID:    o.TaskID,
		CreatedAt: now,
	}
	m.mu.Lock()
	m.entries[token] = e
	e.URL = m.urlLocked(e)
	m.mu.Unlock()
	m.emit(Event{Kind: EventPut, Entry: *e})
	return e, nil
}

// List 返回当前存活条目（按创建时间倒序），供管理 API 使用。
func (m *Manager) List() []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Entry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, *e)
	}
	// 简单插入排序即可——暂存条目量级很小。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.After(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Delete 提前销毁一个条目（管理 API）。返回是否命中。
func (m *Manager) Delete(token string) bool {
	e := m.remove(token, EventDestroy)
	return e != nil
}

// Download 服务一次下载（挂载在 GET /s/{token}/{name}）。防枚举要点：
// token 不存在 / name 不匹配 / 已过期 / 命中限速——全部返回同一个 404。
func (m *Manager) Download(w http.ResponseWriter, r *http.Request, token, name string) {
	ip := remoteIP(r)
	if !m.allowAttempt(token, ip) {
		notFound(w)
		return
	}
	m.mu.Lock()
	e, ok := m.entries[token]
	if !ok || e.Name != name || time.Now().After(e.ExpiresAt) {
		m.mu.Unlock()
		notFound(w)
		return
	}
	m.mu.Unlock()

	path := filepath.Join(m.dir, token, name)
	f, err := os.Open(path)
	if err != nil {
		notFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", e.Name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", e.Size))
	n, _ := io.Copy(w, f)
	// 显式关闭而非 defer:一次性即焚下面马上要删文件,Windows 删不掉还开着的句柄。
	_ = f.Close()

	m.mu.Lock()
	cur, alive := m.entries[token]
	if alive {
		cur.Bytes += n
	}
	complete := n == e.Size
	if alive && complete {
		cur.Hits++
	}
	snap := e
	oneShotBurn := alive && complete && e.OneShot
	m.mu.Unlock()

	m.emit(Event{Kind: EventDownload, Entry: *snap, Remote: ip, Bytes: n})
	if oneShotBurn {
		// 一次性即焚：首次成功完整下载后立即销毁（记账里的 Hits 已先落上）。
		m.remove(token, EventDestroy)
	}
}

// remove 摘除条目并删文件，返回被删条目（nil = 不存在）。kind 为记账事件类型。
func (m *Manager) remove(token, kind string) *Entry {
	m.mu.Lock()
	e, ok := m.entries[token]
	if ok {
		delete(m.entries, token)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	_ = os.RemoveAll(filepath.Join(m.dir, token))
	m.emit(Event{Kind: kind, Entry: *e})
	return e
}

func (m *Manager) gcLoop(ctx context.Context) {
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.sweep(now)
		}
	}
}

// sweep 销毁全部到期条目。拆出来供测试直接调用。
func (m *Manager) sweep(now time.Time) {
	m.mu.Lock()
	var expired []string
	for token, e := range m.entries {
		if now.After(e.ExpiresAt) {
			expired = append(expired, token)
		}
	}
	m.mu.Unlock()
	for _, token := range expired {
		m.remove(token, EventExpire)
	}
}

// allowAttempt 实现下载尝试限速：单 token 与单源 IP 双维度滑动窗口。
// 过期窗口顺手清理，map 不会无限膨胀。
func (m *Manager) allowAttempt(token, ip string) bool {
	now := time.Now()
	cutoff := now.Add(-rateLimitWindow)
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	if len(m.tokenHit) > 4096 { // 防内存被打爆：极限情况下直接整体重置
		m.tokenHit = map[string][]time.Time{}
	}
	if len(m.ipHit) > 4096 {
		m.ipHit = map[string][]time.Time{}
	}
	th := append(prune(m.tokenHit[token], cutoff), now)
	m.tokenHit[token] = th
	ih := append(prune(m.ipHit[ip], cutoff), now)
	m.ipHit[ip] = ih
	return len(th) <= tokenRateLimit && len(ih) <= ipRateLimit
}

func prune(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && ts[i].Before(cutoff) {
		i++
	}
	return ts[i:]
}

func (m *Manager) emit(ev Event) {
	if m.OnEvent != nil {
		m.OnEvent(ev)
	}
}

func (m *Manager) urlLocked(e *Entry) string {
	p := "/s/" + e.Token + "/" + urlPathEscape(e.Name)
	if m.baseURL == "" {
		return p
	}
	return m.baseURL + p
}

func newToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// urlPathEscape 转义文件名进 URL 路径段（/ 与 % 都必须编掉）。
func urlPathEscape(name string) string {
	r := strings.NewReplacer("%", "%25", "/", "%2F", "?", "%3F", "#", "%23", " ", "%20")
	return r.Replace(name)
}

func notFound(w http.ResponseWriter) {
	// 逐字节一致的 404：错误 token、错误 name、已过期、限速命中全走这里。
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "404 page not found\n")
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// clearDir 删除 dir 下全部内容（保留 dir 本身）。逐条收集错误。
func clearDir(dir string) []error {
	items, err := os.ReadDir(dir)
	if err != nil {
		return []error{err}
	}
	var errs []error
	for _, it := range items {
		if err := os.RemoveAll(filepath.Join(dir, it.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
