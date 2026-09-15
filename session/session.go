// Package session 是立足点会话子系统（内网渗透期 1a,见 INTRANET-PIVOT-DESIGN.md
// §4.2)。所有驱动（http_shell 一句话马、ssh/reverse 预留）实现同一 Session 抽象,
// agent 只看到「会话工具」,目标侧命令的唯一出口是 Session.Exec/ReadFile/WriteFile。
//
// 设计语义借鉴 PivotHub pivothub/session(Go 重写，非照抄）:
//   - 哨兵同步：随机 marker 包裹 stdout,防回显污染/粘连/误命中；
//   - 引号规避：复杂命令 base64 落盘到目标临时文件再 sh 执行，临时文件随机前缀、
//     用完清理；
//   - 断线诚实：disable_functions / WAF 拦截一律返回带语义的错误，不伪造成功。
package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Session 是一切立足点会话的统一契约。实现必须并发安全（同一会话可能被多个
// worker 意图并发使用；HTTP 马无状态，天然可并发）。
type Session interface {
	// ID 是 sessions 表主键；未落库前为 0。
	ID() int64
	// Kind 是会话类型：http_php / http_jsp / http_aspx / ssh / reverse(预留)。
	Kind() string
	// Test 做连通性探测（无害探针，如 echo 哨兵）。失败返回带原因的错误。
	Test(ctx context.Context) error
	// Exec 在目标侧执行一条 shell 命令。webshell 通道无法分离 stderr,
	// 实现统一把 2>&1 合并进 stdout,stderr 恒为空（诚实语义，不假装能区分）。
	// timeout <= 0 时用驱动默认超时。网络失败/执行函数被禁/被拦截都经 err 返回。
	Exec(ctx context.Context, cmd string, timeout time.Duration) (stdout, stderr string, err error)
	// ReadFile 读目标侧文件（大文件内部分块）。
	ReadFile(ctx context.Context, path string) ([]byte, error)
	// WriteFile 写目标侧文件（二进制安全，大文件内部分块 append)。
	WriteFile(ctx context.Context, path string, data []byte) error
	// Close 释放资源（HTTP 会话无资源，reverse 预留）。
	Close(ctx context.Context) error
}

// Error 是会话层错误：网络失败/协议失败/目标不支持。文本面向 agent,带语义
// （什么原因、可能是什么拦截）,不伪造成功。
type Error struct {
	Op  string // test/exec/read/write/probe
	Err string
}

func (e *Error) Error() string { return fmt.Sprintf("session %s: %s", e.Op, e.Err) }

func opError(op, format string, args ...any) error {
	return &Error{Op: op, Err: fmt.Sprintf(format, args...)}
}

// ErrSessionNotFound 是 Registry 查无会话的错误。
var ErrSessionNotFound = errors.New("session 不存在或已关闭")

// ---------------- 调用方命名空间前缀（复盘 R5) ----------------

// ctxTmpPrefixKey 是目标侧临时文件命名空间前缀的 context 键。
type ctxTmpPrefixKey struct{}

// defaultTmpPrefix 是目标侧临时文件默认前缀（未注入调用方前缀时）。
const defaultTmpPrefix = "/tmp/.artex_"

// WorkerTmpPrefix 构造 per-worker 命名空间前缀：/tmp/.artex_<task>_<intent>_。
// 多 worker 共用同一立足点时，各自的落盘脚本/标记文件不再互相覆盖（复盘 R5:
// 硬编码共享路径导致输出串台、哨兵误报「马失效」)。
func WorkerTmpPrefix(taskID, intentID int64) string {
	return fmt.Sprintf("/tmp/.artex_%d_%d_", taskID, intentID)
}

// WithTmpPrefix 给 ctx 挂目标侧临时文件命名空间前缀（server 层按 worker 注入）。
// prefix 只接受绝对路径形态且字符集限于 [A-Za-z0-9_./-]（要内联进 sh 命令，
// 拒绝引号/空白/分号等元字符）,非法输入回落默认前缀。
func WithTmpPrefix(ctx context.Context, prefix string) context.Context {
	return context.WithValue(ctx, ctxTmpPrefixKey{}, sanitizeTmpPrefix(prefix))
}

// tmpPrefixFrom 取调用方前缀，未注入时用默认前缀。
func tmpPrefixFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxTmpPrefixKey{}).(string); ok && v != "" {
		return v
	}
	return defaultTmpPrefix
}

func sanitizeTmpPrefix(p string) string {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") {
		return defaultTmpPrefix
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == '/':
		default:
			return defaultTmpPrefix
		}
	}
	return p
}

// Registry 是进程内会话注册表：sessions 表 id → 活会话。并发安全。启动时由
// server 从 DB 恢复加载（restore_reverse_listeners 模式）。
type Registry struct {
	mu       sync.RWMutex
	sessions map[int64]Session
}

func NewRegistry() *Registry {
	return &Registry{sessions: map[int64]Session{}}
}

// Add 登记会话（同 id 覆盖并关闭旧会话）。
func (r *Registry) Add(s Session) {
	r.mu.Lock()
	old := r.sessions[s.ID()]
	r.sessions[s.ID()] = s
	r.mu.Unlock()
	if old != nil && old != s {
		_ = old.Close(context.Background())
	}
}

// Get 取会话。
func (r *Registry) Get(id int64) (Session, bool) {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	return s, ok
}

// Remove 移除并关闭会话。
func (r *Registry) Remove(id int64) bool {
	r.mu.Lock()
	s, ok := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if ok {
		_ = s.Close(context.Background())
	}
	return ok
}

// List 返回全部活会话（顺序不定）。
func (r *Registry) List() []Session {
	r.mu.RLock()
	out := make([]Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	r.mu.RUnlock()
	return out
}
