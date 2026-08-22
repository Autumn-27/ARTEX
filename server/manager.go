package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/agent"
	pgdb "github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/enrich"
	"github.com/Autumn-27/artex/guard"
	"github.com/Autumn-27/artex/intercept"
	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

// Task is one engagement: a description + goal + its own exploration store,
// sharing the process-wide asset store. ID is the PG task id as a string; ExpID
// is the exploration the task owns.
type Task struct {
	ID          string `json:"id"`
	ExpID       int64  `json:"exploration_id"`
	Description string `json:"description"`
	Goal        string `json:"goal"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at,omitempty"` // 进入终态的 unix 秒;0=未完成
	Paused      bool   `json:"paused"`
	Queued      bool   `json:"queued"` // 因并发上限被挂起、等待空位自动启动;true=尚未开跑
	// QueuedAt is an internal Unix-nanosecond ordering key. It is deliberately
	// finer than CreatedAt so several tasks enqueued in the same second retain
	// their real FIFO order.
	QueuedAt           int64   `json:"queued_at,omitempty"`
	QueueMode          string  `json:"queue_mode,omitempty"`
	ParentRef          string  `json:"parent_ref,omitempty"`     // 父任务 id(编排 spawn 记录)
	LLMProfileID       *int64  `json:"llm_profile_id,omitempty"` // 指定运行本任务 planner/worker 的 LLM 配置;nil=用全局激活配置
	LLMProfileIDs      []int64 `json:"llm_profile_ids,omitempty"`
	ActiveLLMProfileID *int64  `json:"active_llm_profile_id,omitempty"`
	LLMChainRevision   int64   `json:"-"`
	LLMFailoverState   string  `json:"llm_failover_state,omitempty"`
	LLMFailoverReason  string  `json:"llm_failover_reason,omitempty"`
	SourceTaskIDs      []int64 `json:"source_task_ids,omitempty"`
	CompanyIDs         []int64 `json:"company_ids,omitempty"`
	Status             string  `json:"status"` // persisted lifecycle status (done/failed/timeout 为终态；空/其它则由运行态推导)
	// 任务级超时(见 docs/任务级超时与收尾设计.md)。DeadlineAt/FirstRunAt 为 unix 秒,0=未设/未运行。
	TimeoutSeconds       int                    `json:"timeout_seconds"`
	PlanHeartbeatSeconds int                    `json:"plan_heartbeat_seconds"` // planner 心跳触发间隔(秒)
	CoverageEnabled      bool                   `json:"coverage_enabled"`       // 资产覆盖度功能开关(创建时定,默认开)
	FirstRunAt           int64                  `json:"first_run_at,omitempty"`
	DeadlineAt           int64                  `json:"deadline_at,omitempty"`
	Store                *pgdb.ExplorationStore `json:"-"`
	Guard                *guard.Guard           `json:"-"`
	notify               chan struct{}
	lifecycleMu          sync.RWMutex
	llmMu                sync.RWMutex

	// pendingTriggers accumulates the concrete changes (worker done / finding) that
	// fired planning rounds since the last one consumed them. The debounce coalesces
	// a burst into one round, so several may pile up before drainTriggers() clears them.
	trigMu          sync.Mutex
	pendingTriggers []agent.TriggerEvent
}

// taskLifecycleState is an internally consistent view of the mutable task
// lifecycle and inherited-scope context. Callers must use lifecycleSnapshot and
// updateLifecycle instead of reading or writing the corresponding Task fields
// directly after the task has been published by Manager.
type taskLifecycleState struct {
	Status        string
	Paused        bool
	Queued        bool
	QueuedAt      int64
	QueueMode     string
	CompletedAt   int64
	FirstRunAt    int64
	DeadlineAt    int64
	SourceTaskIDs []int64
	CompanyIDs    []int64
}

func (t *Task) lifecycleSnapshot() taskLifecycleState {
	if t == nil {
		return taskLifecycleState{}
	}
	t.lifecycleMu.RLock()
	defer t.lifecycleMu.RUnlock()
	return t.lifecycleSnapshotLocked()
}

func (t *Task) lifecycleSnapshotLocked() taskLifecycleState {
	return taskLifecycleState{
		Status:        t.Status,
		Paused:        t.Paused,
		Queued:        t.Queued,
		QueuedAt:      t.QueuedAt,
		QueueMode:     t.QueueMode,
		CompletedAt:   t.CompletedAt,
		FirstRunAt:    t.FirstRunAt,
		DeadlineAt:    t.DeadlineAt,
		SourceTaskIDs: append([]int64(nil), t.SourceTaskIDs...),
		CompanyIDs:    append([]int64(nil), t.CompanyIDs...),
	}
}

func (t *Task) updateLifecycle(update func(*taskLifecycleState)) {
	if t == nil || update == nil {
		return
	}
	t.lifecycleMu.Lock()
	state := t.lifecycleSnapshotLocked()
	update(&state)
	t.Status = state.Status
	t.Paused = state.Paused
	t.Queued = state.Queued
	t.QueuedAt = state.QueuedAt
	t.QueueMode = state.QueueMode
	t.CompletedAt = state.CompletedAt
	t.FirstRunAt = state.FirstRunAt
	t.DeadlineAt = state.DeadlineAt
	t.SourceTaskIDs = append(t.SourceTaskIDs[:0], state.SourceTaskIDs...)
	t.CompanyIDs = append(t.CompanyIDs[:0], state.CompanyIDs...)
	t.lifecycleMu.Unlock()
}

type taskLLMState struct {
	ProfileID      *int64
	ProfileIDs     []int64
	ActiveID       *int64
	ChainRevision  int64
	FailoverState  string
	FailoverReason string
}

func cloneInt64Ptr(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (t *Task) llmStateSnapshot() taskLLMState {
	t.llmMu.RLock()
	defer t.llmMu.RUnlock()
	return taskLLMState{
		ProfileID:      cloneInt64Ptr(t.LLMProfileID),
		ProfileIDs:     append(make([]int64, 0, len(t.LLMProfileIDs)), t.LLMProfileIDs...),
		ActiveID:       cloneInt64Ptr(t.ActiveLLMProfileID),
		ChainRevision:  t.LLMChainRevision,
		FailoverState:  t.LLMFailoverState,
		FailoverReason: t.LLMFailoverReason,
	}
}

func (t *Task) setLLMState(profileID, activeID *int64, profileIDs []int64, revision int64, state, reason string) bool {
	t.llmMu.Lock()
	defer t.llmMu.Unlock()
	if revision < t.LLMChainRevision {
		return false
	}
	t.LLMProfileID = cloneInt64Ptr(profileID)
	t.ActiveLLMProfileID = cloneInt64Ptr(activeID)
	t.LLMProfileIDs = append(t.LLMProfileIDs[:0], profileIDs...)
	t.LLMChainRevision = revision
	t.LLMFailoverState = state
	t.LLMFailoverReason = reason
	return true
}

// DeleteTaskOptions controls cleanup of data stored outside the task's own
// exploration graph. All options default to false for backward compatibility.
type DeleteTaskOptions struct {
	DeleteAssets     bool `json:"delete_assets"`
	DeleteTraffic    bool `json:"delete_traffic"`
	DeleteFiles      bool `json:"delete_files"`
	DeleteFindings   bool `json:"delete_findings"`
	DeleteLLMRecords bool `json:"delete_llm_records"`
}

// DeleteTaskResult makes destructive cleanup auditable to API callers.
type DeleteTaskResult struct {
	Deleted           string `json:"deleted"`
	AssetsDeleted     int64  `json:"assets_deleted"`
	AssetsDetached    int64  `json:"assets_detached"`
	TrafficDeleted    int64  `json:"traffic_deleted"`
	FilesDeleted      bool   `json:"files_deleted"`
	FindingsDeleted   int64  `json:"findings_deleted"`
	LLMRecordsDeleted int64  `json:"llm_records_deleted"`
	CleanupWarning    string `json:"cleanup_warning,omitempty"`
}

// Manager owns the PostgreSQL data source (asset graph + every task's exploration
// graph + config) and the in-memory set of task handles.
type Manager struct {
	dir         string
	pg          *pgdb.DB
	assets      *pgdb.AssetStore
	traffic     *traffic.Traffic       // process-wide recording proxy (may be nil)
	enrich      *enrich.Engine         // engine-side asset auto-completion (DNS/HTTP)
	interceptor *intercept.Interceptor // user-configured tool-call interception rules

	companyMu sync.Mutex // serializes task/company-scope commits with live handle registration
	// taskStateMu preserves commit order between PostgreSQL lifecycle writes and
	// their in-memory mirrors. lifecycleMu makes snapshots race-free, but without
	// this outer write lock an older request could commit first and publish last.
	taskStateMu sync.Mutex
	mu          sync.RWMutex
	tasks       map[string]*Task
	active      string
	trafficOn   bool // 流量捕获开关（默认关；settings.traffic_capture）
	llmRecOn    bool // LLM 录制开关（默认关；settings.llm_record）
	// 联网搜索开关与来源（默认关；settings.web_search_*）。brave-free 需要 braveKey；tavily 需要 tavilyKey。
	// webSearchProxy 是独立出口代理(http/https/socks5)，与记录流量的 MITM 代理无关。
	webSearchOn      bool
	webSearchBackend string
	braveKey         string
	tavilyKey        string
	webSearchProxy   string
}

// Settings keys the UI toggles at runtime.
const (
	settingTrafficCapture   = "traffic_capture"
	settingWebSearchOn      = "web_search_enabled"
	settingWebSearchBackend = "web_search_backend"
	settingBraveKey         = "brave_search_api_key"
	settingTavilyKey        = "tavily_search_api_key"
	settingWebSearchProxy   = "web_search_proxy"
	settingWorkers          = "workers"
	settingLLMRecord        = "llm_record"
	// LLM 轮询(故障转移)。默认关闭——开启后走「全局激活配置」的 agent 在当前配置
	// 不可用(余额不足/key 失效/限流/服务异常)时自动切到下一个配置。
	// settingLLMPoolBindFallback 仅在轮询开启时有意义:默认关闭,即 agent/任务显式
	// 绑定了某个配置就只用它、失败即失败;开启后绑定的配置失败也会回落到轮询链。
	settingLLMPoolOn           = "llm_pool_enabled"
	settingLLMPoolBindFallback = "llm_pool_bind_fallback"
	// 任务并发上限:开关 + 上限数。默认关闭;开启后默认上限 5(见 defaultConcurrencyLimit)。
	settingConcurrencyOn    = "task_concurrency_enabled"
	settingConcurrencyLimit = "task_concurrency_limit"
	// defaultWebSearchBackend is used when web search is on but no backend was picked.
	defaultWebSearchBackend = "ddgs"
	// defaultWorkers is the concurrent work-agent count when the setting is unset.
	defaultWorkers = 3
	// defaultConcurrencyLimit is the simultaneous-running-task cap when the feature
	// is enabled but no explicit limit was saved.
	defaultConcurrencyLimit = 5
)

// ConcurrencyLimit returns whether the simultaneous-running-task cap is enabled and
// its limit (default 5 when enabled but unset). limit is always >=1 when enabled.
func (m *Manager) ConcurrencyLimit() (enabled bool, limit int) {
	on, _, _ := m.pg.GetSetting(settingConcurrencyOn)
	if strings.TrimSpace(on) != "true" {
		return false, 0
	}
	limit = defaultConcurrencyLimit
	if v, ok, _ := m.pg.GetSetting(settingConcurrencyLimit); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 1 {
			limit = n
		}
	}
	return true, limit
}

// SetConcurrency persists the running-task concurrency cap. limit<1 is clamped to 1.
func (m *Manager) SetConcurrency(enabled bool, limit int) error {
	if limit < 1 {
		limit = defaultConcurrencyLimit
	}
	if err := m.pg.SetSetting(settingConcurrencyLimit, strconv.Itoa(limit)); err != nil {
		return err
	}
	return m.pg.SetSetting(settingConcurrencyOn, strconv.FormatBool(enabled))
}

// Workers returns the configured concurrent work-agent count (default 3). Read
// per-task at engine.Run, so a change applies to tasks started afterwards.
func (m *Manager) Workers() int {
	v, ok, err := m.pg.GetSetting(settingWorkers)
	if err != nil || !ok {
		return defaultWorkers
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return defaultWorkers
	}
	return n
}

// SetWorkers persists the concurrent work-agent count. Values <=0 are rejected.
func (m *Manager) SetWorkers(n int) error {
	if n <= 0 {
		return fmt.Errorf("workers 必须 >0")
	}
	return m.pg.SetSetting(settingWorkers, strconv.Itoa(n))
}

// Enrich returns the asset auto-completion engine (may be nil if init failed).
func (m *Manager) Enrich() *enrich.Engine { return m.enrich }

// NewManager connects to PostgreSQL and, if proxyAddr is non-empty, starts the
// traffic-recording proxy. PostgreSQL is required (it is the single data source).
func NewManager(dir, proxyAddr string) (*Manager, error) {
	// Resolve the data dir to an ABSOLUTE path up front. Every data path derives
	// from it — notably the MITM CA cert, whose path is injected into worker shells
	// (SSL_CERT_FILE/CURL_CA_BUNDLE) and read by WebFetch. A relative path (the
	// default is "./data" under `go run`) only resolves when the current working
	// directory happens to match, so curl/WebFetch in a different CWD fail to load
	// the CA → TLS to the proxy breaks (curl 000 / EOF). Absolute makes it CWD-proof.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	dsn, source, err := pgdb.DSN()
	if err != nil {
		return nil, err
	}
	log.Printf("[pg] 数据库配置来源: %s", source)
	pg, err := pgdb.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		log.Printf("[llmrec] create table: %v", err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		log.Printf("[llmusage] create table: %v", err)
	}
	m := &Manager{dir: dir, pg: pg, assets: pg.Assets(), tasks: map[string]*Task{}, interceptor: intercept.New(pg)}
	if proxyAddr != "" {
		tr, err := traffic.Open(filepath.Join(dir, "traffic"), proxyAddr)
		if err != nil {
			log.Printf("[traffic] disabled: %v", err)
		} else {
			m.traffic = tr
			go func() {
				log.Printf("[traffic] recording proxy on %s (set HTTP_PROXY=%s + trust _ca CA)", proxyAddr, tr.ProxyAddr())
				if err := tr.Start(); err != nil {
					log.Printf("[traffic] proxy stopped: %v", err)
				}
			}()
		}
	}
	// Asset auto-completion engine (§5): HTTP probes routed through the recording
	// proxy (via m.ProxyAddr, which honors the traffic-capture toggle).
	m.trafficOn = pg.GetBool(settingTrafficCapture, false)
	// LLM 录制开关（默认关）。录制器每次调用时读取此标志。
	m.llmRecOn = pg.GetBool(settingLLMRecord, false)
	// Load persisted web-search config (default: off, ddgs).
	m.webSearchOn = pg.GetBool(settingWebSearchOn, false)
	if v, ok, _ := pg.GetSetting(settingWebSearchBackend); ok && v != "" {
		m.webSearchBackend = v
	} else {
		m.webSearchBackend = defaultWebSearchBackend
	}
	if v, ok, _ := pg.GetSetting(settingBraveKey); ok {
		m.braveKey = v
	}
	if v, ok, _ := pg.GetSetting(settingTavilyKey); ok {
		m.tavilyKey = v
	}
	if v, ok, _ := pg.GetSetting(settingWebSearchProxy); ok {
		m.webSearchProxy = v
	}
	m.enrich = enrich.New(m.assets, m.ProxyAddr, 4)
	// Reconcile the seeded browser MCP with the persisted capture state, so a
	// restart with capture already on keeps Playwright routed through the proxy.
	m.syncBrowserMCPProxy()
	return m, nil
}

// TrafficEnabled reports whether traffic capture is on (default off). When off,
// no proxy/traffic tools/prompt are injected into agents (nothing is recorded).
func (m *Manager) TrafficEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trafficOn
}

// SetTrafficEnabled persists and applies the traffic-capture toggle. Callers must
// rebuild the agents (applyLLM) afterwards so the new proxy/tools/prompt take hold.
func (m *Manager) SetTrafficEnabled(on bool) error {
	if err := m.pg.SetBool(settingTrafficCapture, on); err != nil {
		return err
	}
	m.mu.Lock()
	m.trafficOn = on
	m.mu.Unlock()
	// Inject (on) or strip (off) the recording proxy + CA on the browser MCP so
	// Playwright routes through the MITM. Must run after the flag flip above, since
	// ProxyAddr/ProxyCACert honor it. putSettings rebuilds agents next (applyLLM),
	// which re-spawns the MCP with the new args/env.
	m.syncBrowserMCPProxy()
	return nil
}

// LLMRecordEnabled reports whether LLM request/response recording is on
// (默认关；settings.llm_record). The recorder consults this per call, so the
// toggle takes effect immediately without rebuilding agents.
func (m *Manager) LLMRecordEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmRecOn
}

// SetLLMRecordEnabled persists and applies the LLM-record toggle. Effective at
// once — no applyLLM needed, since the recorder reads the flag on every call.
func (m *Manager) SetLLMRecordEnabled(on bool) error {
	if err := m.pg.SetBool(settingLLMRecord, on); err != nil {
		return err
	}
	m.mu.Lock()
	m.llmRecOn = on
	m.mu.Unlock()
	return nil
}

// LLMPoolEnabled reports whether LLM failover ("轮询") is on (默认关；
// settings.llm_pool_enabled). Read when the provider chain is built (applyLLM),
// so a change requires a rebuild — putSettings does that.
func (m *Manager) LLMPoolEnabled() bool {
	if m.pg == nil {
		return false
	}
	return m.pg.GetBool(settingLLMPoolOn, false)
}

// SetLLMPoolEnabled persists the failover toggle. Callers rebuild agents
// (applyLLM) afterwards so it takes effect.
func (m *Manager) SetLLMPoolEnabled(on bool) error { return m.pg.SetBool(settingLLMPoolOn, on) }

// LLMPoolBindFallback reports whether an agent/task that is BOUND to a specific
// profile still falls back to the chain when that profile fails (默认关：绑定即
// 独占，失败即失败). Only meaningful while LLMPoolEnabled.
func (m *Manager) LLMPoolBindFallback() bool {
	if m.pg == nil {
		return false
	}
	return m.pg.GetBool(settingLLMPoolBindFallback, false)
}

// SetLLMPoolBindFallback persists the bound-profile fallback toggle. Callers
// rebuild agents (applyLLM) afterwards.
func (m *Manager) SetLLMPoolBindFallback(on bool) error {
	return m.pg.SetBool(settingLLMPoolBindFallback, on)
}

// WebSearch returns the current web-search config: whether it is enabled, the
// backend ("ddgs" | "brave-free" | "tavily"), the Brave API key, the Tavily API
// key (each empty unless set), and the dedicated egress proxy (empty = direct).
func (m *Manager) WebSearch() (on bool, backend, braveKey, tavilyKey, proxy string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	backend = m.webSearchBackend
	if backend == "" {
		backend = defaultWebSearchBackend
	}
	return m.webSearchOn, backend, m.braveKey, m.tavilyKey, m.webSearchProxy
}

// WebSearchOpts returns the config as the agent-package struct the server pushes
// into each agent. Disabled when off, or when a keyed backend is selected without
// its key (so a half-configured backend never silently drops the tool at session build).
func (m *Manager) WebSearchOpts() agent.WebSearchOpts {
	on, backend, braveKey, tavilyKey, proxy := m.WebSearch()
	if on && backend == "brave-free" && strings.TrimSpace(braveKey) == "" {
		on = false
	}
	if on && backend == "tavily" && strings.TrimSpace(tavilyKey) == "" {
		on = false
	}
	return agent.WebSearchOpts{Enabled: on, Backend: backend, BraveKey: braveKey, TavilyKey: tavilyKey, Proxy: proxy}
}

// SetWebSearch persists and applies the web-search settings. braveKey, tavilyKey, and
// proxy are each left untouched when nil (so toggling the switch doesn't wipe a saved
// key/proxy; pass a pointer to "" to clear). Callers must rebuild agents (applyLLM)
// afterwards so the settings take effect.
func (m *Manager) SetWebSearch(on bool, backend string, braveKey, tavilyKey, proxy *string) error {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		backend = defaultWebSearchBackend
	}
	if err := m.pg.SetBool(settingWebSearchOn, on); err != nil {
		return err
	}
	if err := m.pg.SetSetting(settingWebSearchBackend, backend); err != nil {
		return err
	}
	m.mu.Lock()
	m.webSearchOn = on
	m.webSearchBackend = backend
	m.mu.Unlock()
	if braveKey != nil {
		if err := m.pg.SetSetting(settingBraveKey, *braveKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.braveKey = *braveKey
		m.mu.Unlock()
	}
	if tavilyKey != nil {
		if err := m.pg.SetSetting(settingTavilyKey, *tavilyKey); err != nil {
			return err
		}
		m.mu.Lock()
		m.tavilyKey = *tavilyKey
		m.mu.Unlock()
	}
	if proxy != nil {
		p := strings.TrimSpace(*proxy)
		if err := m.pg.SetSetting(settingWebSearchProxy, p); err != nil {
			return err
		}
		m.mu.Lock()
		m.webSearchProxy = p
		m.mu.Unlock()
	}
	return nil
}

// browserMCPName is the seeded Playwright MCP whose proxy args + CA env are kept
// in sync with the traffic-capture toggle.
const browserMCPName = "browser"

// syncBrowserMCPProxy reconciles the seeded browser MCP's proxy args + CA env with
// the current traffic-capture state: capture on → route Playwright through the
// recording proxy (--proxy-server) and trust its MITM CA (NODE_EXTRA_CA_CERTS);
// capture off → strip both. Idempotent, and a no-op if the user deleted/renamed the
// MCP. Must be called WITHOUT m.mu held (ProxyAddr/ProxyCACert take the lock).
func (m *Manager) syncBrowserMCPProxy() {
	servers, err := m.pg.ListMCP()
	if err != nil {
		log.Printf("[mcp] browser 代理同步: 读取 MCP 列表失败: %v", err)
		return
	}
	var srv *pgdb.MCPServer
	for _, s := range servers {
		if s.Name == browserMCPName {
			srv = s
			break
		}
	}
	if srv == nil {
		return // user removed/renamed it — leave it alone
	}

	proxy := m.ProxyAddr()  // "" when capture off
	cert := m.ProxyCACert() // "" when capture off

	args := stripProxyArgs(decodeStrSlice(srv.Args))
	env := decodeStrMap(srv.Env)
	delete(env, "NODE_EXTRA_CA_CERTS")
	if proxy != "" {
		args = append(args, "--proxy-server", proxy)
		if cert != "" {
			env["NODE_EXTRA_CA_CERTS"] = cert
		}
	}
	srv.Args = encodeJSON(args)
	srv.Env = encodeJSON(env)
	if _, err := m.pg.SaveMCP(srv); err != nil {
		log.Printf("[mcp] browser 代理同步失败: %v", err)
		return
	}
	if proxy != "" {
		log.Printf("[mcp] browser MCP 已挂捕获代理 %s (CA %s)", proxy, cert)
	} else {
		log.Printf("[mcp] browser MCP 已移除捕获代理配置")
	}
}

// stripProxyArgs removes any --proxy-server/--proxy-bypass flags (both "--flag val"
// and "--flag=val" forms) so they can be re-added cleanly from current state,
// without mutating the input slice.
func stripProxyArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--proxy-server" || a == "--proxy-bypass" {
			i++ // skip the following value too
			continue
		}
		if strings.HasPrefix(a, "--proxy-server=") || strings.HasPrefix(a, "--proxy-bypass=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func decodeStrSlice(raw json.RawMessage) []string {
	var out []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func decodeStrMap(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func encodeJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// HostTools are runtime host-provided tools added to EVERY agent's base list (via
// ToolAugment); the tools table then filters them per-agent binding. Currently the
// traffic tools, gated by the global capture switch: empty when capture is off, so
// no agent gets traffic_search/traffic_get regardless of binding.
func (m *Manager) HostTools() []actool.CoreTool {
	if m.traffic == nil || !m.TrafficEnabled() {
		return nil
	}
	return m.traffic.Tools()
}

func (m *Manager) Assets() *pgdb.AssetStore  { return m.assets }
func (m *Manager) PG() *pgdb.DB              { return m.pg }
func (m *Manager) Traffic() *traffic.Traffic { return m.traffic }

// ProxyAddr returns the recording proxy address agents route through — empty when
// traffic capture is off, so no proxy is injected (agent runs direct, no recording).
func (m *Manager) ProxyAddr() string {
	if m.traffic == nil || !m.TrafficEnabled() {
		return ""
	}
	return m.traffic.ProxyAddr()
}

// ProxyCACert returns the recording proxy's CA cert path (empty when no proxy or
// traffic capture is off), which WebFetch trusts to verify HTTPS through the MITM.
func (m *Manager) ProxyCACert() string {
	if m.traffic == nil || !m.TrafficEnabled() {
		return ""
	}
	return m.traffic.CACertPath()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.traffic != nil {
		m.traffic.Close()
	}
	return m.pg.Close()
}

// isTerminalStatus reports whether a task status is terminal (done/failed/timeout).
// Package-local shim over db.IsTerminal so all server files share one definition.
func isTerminalStatus(status string) bool { return pgdb.IsTerminal(status) }

// unixOrZero returns t's unix seconds, or 0 when the time is nil.
func unixOrZero(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}

func unixNanoOrZero(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixNano()
}

func taskFromPG(pt *pgdb.Task, store *pgdb.ExplorationStore, ic *intercept.Interceptor) *Task {
	return &Task{
		ID: strconv.FormatInt(pt.ID, 10), ExpID: pt.ExplorationID,
		Description: pt.Description, Goal: pt.Goal, CreatedAt: pt.CreatedAt.Unix(), Paused: pt.Paused, Queued: pt.Queued,
		QueuedAt: unixNanoOrZero(pt.QueuedAt), QueueMode: pt.QueueMode,
		CompletedAt: unixOrZero(pt.CompletedAt), Status: pt.Status, ParentRef: pt.ParentRef,
		LLMProfileID:  pt.LLMProfileID,
		LLMProfileIDs: append([]int64(nil), pt.LLMProfileIDs...), ActiveLLMProfileID: pt.ActiveLLMProfileID,
		LLMChainRevision: pt.LLMChainRevision,
		LLMFailoverState: pt.LLMFailoverState, LLMFailoverReason: pt.LLMFailoverReason,
		SourceTaskIDs:  append([]int64(nil), pt.SourceTaskIDs...),
		CompanyIDs:     append([]int64(nil), pt.CompanyIDs...),
		TimeoutSeconds: pt.TimeoutSeconds, PlanHeartbeatSeconds: pt.PlanHeartbeatSeconds,
		CoverageEnabled: pt.CoverageEnabled,
		FirstRunAt:      unixOrZero(pt.FirstRunAt), DeadlineAt: unixOrZero(pt.DeadlineAt),
		Store: store, Guard: guard.NewWithInterceptor(ic), notify: make(chan struct{}, 1),
	}
}

// CreateTask creates a task + its exploration and makes it active.
// timeoutSeconds is the task-level wall-clock budget (0 = 不限时).
func (m *Manager) CreateTask(description, goal string, llmProfileID *int64, timeoutSeconds, planHeartbeatSeconds int) (*Task, error) {
	var ids []int64
	if llmProfileID != nil {
		ids = []int64{*llmProfileID}
	}
	return m.CreateTaskWithOptions(description, goal, pgdb.TaskCreateOptions{
		LLMProfileIDs: ids, TimeoutSeconds: timeoutSeconds, PlanHeartbeatSeconds: planHeartbeatSeconds,
	})
}

func (m *Manager) CreateTaskWithOptions(description, goal string, opts pgdb.TaskCreateOptions) (*Task, error) {
	if len(opts.CompanyIDs) > 0 {
		m.companyMu.Lock()
		defer m.companyMu.Unlock()
	}
	pt, err := m.pg.CreateTaskWithOptions(description, goal, opts)
	if err != nil {
		return nil, err
	}
	t := taskFromPG(pt, m.pg.Exploration(pt.ExplorationID), m.interceptor)
	m.mu.Lock()
	m.tasks[t.ID] = t
	m.active = t.ID
	m.mu.Unlock()
	return t, nil
}

// DeleteCompanyWithAssets keeps the database cascade and live task handles in
// one manager-level critical section. This closes the gap where a task could
// commit its company scope immediately before registration and miss the
// post-delete in-memory sweep.
func (m *Manager) DeleteCompanyWithAssets(id int64, deleteAssets bool) (int64, error) {
	m.companyMu.Lock()
	defer m.companyMu.Unlock()
	assetsDeleted, err := m.pg.Companies().DeleteCompanyWithAssets(id, deleteAssets)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	for _, task := range m.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			companyIDs := make([]int64, 0, len(state.CompanyIDs))
			for _, companyID := range state.CompanyIDs {
				if companyID != id {
					companyIDs = append(companyIDs, companyID)
				}
			}
			state.CompanyIDs = companyIDs
		})
	}
	m.mu.Unlock()
	return assetsDeleted, nil
}

// ReplaceTaskLLMProfiles resets a non-terminal task's ordered provider chain and
// mirrors the committed state onto the live task handle.
func (m *Manager) ReplaceTaskLLMProfiles(id string, profileIDs []int64, activeProfileID int64) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, err
	}
	if err := m.pg.ReplaceTaskLLMProfiles(n, profileIDs, activeProfileID); err != nil {
		return 0, err
	}
	pt, err := m.pg.GetTask(n)
	if err != nil || pt == nil {
		return 0, err
	}
	m.mu.Lock()
	if task := m.tasks[id]; task != nil {
		task.setLLMState(pt.LLMProfileID, pt.ActiveLLMProfileID, pt.LLMProfileIDs, pt.LLMChainRevision, pt.LLMFailoverState, pt.LLMFailoverReason)
	}
	m.mu.Unlock()
	if task, ok := m.Task(id); ok {
		reopened, reopenErr := task.Store.ReopenIntentsByBlockedReason(pgdb.IntentBlockedLLMQuota)
		if reopenErr != nil {
			return reopened, reopenErr
		}
		return reopened, nil
	}
	return 0, nil
}

// LoadExisting rebuilds in-memory task handles from the PG task registry.
func (m *Manager) LoadExisting() []*Task {
	pts, err := m.pg.ListTasks()
	if err != nil {
		log.Printf("[manager] reload: %v", err)
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var loaded []*Task
	for _, pt := range pts {
		id := strconv.FormatInt(pt.ID, 10)
		if _, ok := m.tasks[id]; ok {
			continue
		}
		t := taskFromPG(pt, m.pg.Exploration(pt.ExplorationID), m.interceptor)
		m.tasks[id] = t
		loaded = append(loaded, t)
	}
	if m.active == "" {
		var newest *Task
		for _, t := range m.tasks {
			if newest == nil || t.CreatedAt > newest.CreatedAt {
				newest = t
			}
		}
		if newest != nil {
			m.active = newest.ID
		}
	}
	if len(loaded) > 0 {
		log.Printf("[manager] reloaded %d task(s) from PG", len(loaded))
	}
	return loaded
}

// SetTaskPaused persists a task's paused state.
func (m *Manager) SetTaskPaused(id string, paused bool) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	if err := m.pg.SetPaused(n, paused); err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Paused = paused
		})
	}
	m.mu.Unlock()
	return nil
}

// ApplyTaskAdmission atomically commits the lifecycle fields controlled by the
// concurrency scheduler. Keeping status, paused and queue metadata in one UPDATE
// prevents a failed resume from leaving a task half-revived (for example running
// but still user-paused, or dequeued without an Engine start).
//
// preservePosition applies only when the row is already queued. A repeated
// admission keeps its FIFO timestamp; a task that was explicitly paused and is
// now re-queued receives a fresh tail position.
func (m *Manager) ApplyTaskAdmission(id, expectedStatus, status string, queued bool, mode string, preservePosition bool) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	if queued {
		if mode != "bootstrap" && mode != "resume" {
			return fmt.Errorf("invalid queue mode %q", mode)
		}
	} else {
		mode = ""
	}

	var queuedAt, completedAt, firstRunAt, deadlineAt sql.NullTime
	var committedMode string
	err = m.pg.QueryRow(`UPDATE tasks
	SET status=$2,
	    completed_at=CASE
	        WHEN $2 IN ('done','failed','timeout') THEN COALESCE(completed_at, now())
	        ELSE NULL
	    END,
	    paused=false,
	    queued=$3,
	    queued_at=CASE
	        WHEN NOT $3 THEN NULL
	        WHEN $5 AND queued THEN COALESCE(queued_at, now())
	        ELSE now()
	    END,
	    queue_mode=CASE
	        WHEN NOT $3 THEN ''
	        WHEN ($5 AND queued AND queue_mode='bootstrap') OR $4='bootstrap' THEN 'bootstrap'
	        ELSE 'resume'
	    END,
	    first_run_at=CASE
	        WHEN $6='timeout' AND $2 NOT IN ('done','failed','timeout') THEN NULL
	        ELSE first_run_at
	    END,
	    deadline_at=CASE
	        WHEN $6='timeout' AND $2 NOT IN ('done','failed','timeout') THEN NULL
	        ELSE deadline_at
	    END
	WHERE id=$1 AND deleted_at IS NULL AND status=$6
	RETURNING queued_at, queue_mode, completed_at, first_run_at, deadline_at`, n, status, queued, mode, preservePosition, expectedStatus).
		Scan(&queuedAt, &committedMode, &completedAt, &firstRunAt, &deadlineAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s lifecycle changed before admission (expected status %q)", id, expectedStatus)
	}
	if err != nil {
		return err
	}

	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = status
			state.Paused = false
			state.Queued = queued
			state.QueueMode = committedMode
			state.QueuedAt = 0
			if queuedAt.Valid {
				state.QueuedAt = queuedAt.Time.UnixNano()
			}
			state.CompletedAt = 0
			if completedAt.Valid {
				state.CompletedAt = completedAt.Time.Unix()
			}
			state.FirstRunAt = 0
			if firstRunAt.Valid {
				state.FirstRunAt = firstRunAt.Time.Unix()
			}
			state.DeadlineAt = 0
			if deadlineAt.Valid {
				state.DeadlineAt = deadlineAt.Time.Unix()
			}
		})
	}
	m.mu.Unlock()
	return nil
}

// ApplyTaskPause atomically removes a task from the admission queue and records
// the user pause. queue_mode is intentionally retained so resuming a never-run
// bootstrap task still performs goal decomposition, but the next enqueue receives
// a new queued_at timestamp and therefore moves to the FIFO tail.
func (m *Manager) ApplyTaskPause(id string) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	var mode string
	err = m.pg.QueryRow(`UPDATE tasks
		SET paused=true, queued=false, queued_at=NULL
		WHERE id=$1 AND deleted_at IS NULL AND paused=false
		  AND status NOT IN ('done','failed','timeout')
		RETURNING COALESCE(queue_mode,'')`, n).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s is unavailable for pause", id)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Paused = true
			state.Queued = false
			state.QueuedAt = 0
			state.QueueMode = mode
		})
	}
	m.mu.Unlock()
	return nil
}

// EnqueueTask persists the concurrency hold and syncs the in-memory handle.
func (m *Manager) EnqueueTask(id, mode string) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	if mode != "bootstrap" && mode != "resume" {
		return fmt.Errorf("invalid queue mode %q", mode)
	}
	var queuedAt time.Time
	var committedMode string
	err = m.pg.QueryRow(`UPDATE tasks
		SET queued=true,
		    queued_at=CASE WHEN queued THEN COALESCE(queued_at, now()) ELSE now() END,
		    queue_mode=CASE
		        WHEN queue_mode='bootstrap' OR $2='bootstrap' THEN 'bootstrap'
		        ELSE 'resume'
		    END
		WHERE id=$1 AND deleted_at IS NULL
		RETURNING queued_at, queue_mode`, n, mode).Scan(&queuedAt, &committedMode)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s is unavailable for enqueue", id)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.QueuedAt = queuedAt.UnixNano()
			state.Queued = true
			state.QueueMode = committedMode
		})
	}
	m.mu.Unlock()
	return nil
}

// DequeueTask removes the concurrency hold. clearMode=false is used when a user
// pauses a queued task so a later resume still knows whether bootstrap is needed.
func (m *Manager) DequeueTask(id string, clearMode bool) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	if err := m.pg.Dequeue(n, clearMode); err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Queued = false
			state.QueuedAt = 0
			if clearMode {
				state.QueueMode = ""
			}
		})
	}
	m.mu.Unlock()
	return nil
}

// TaskStatus returns a task's current in-memory status (empty if unknown).
func (m *Manager) TaskStatus(id string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if t := m.tasks[id]; t != nil {
		return t.lifecycleSnapshot().Status
	}
	return ""
}

// StampTaskFirstRun stamps first_run_at + deadline_at on the first real run (idempotent
// in DB) and mirrors deadline_at on the live handle. Returns the deadline unix (0 = 不限).
func (m *Manager) StampTaskFirstRun(id string) (int64, error) {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, err
	}
	m.mu.RLock()
	timeout := 0
	if t := m.tasks[id]; t != nil {
		timeout = t.TimeoutSeconds
	}
	m.mu.RUnlock()
	dl, err := m.pg.StampFirstRun(n, timeout)
	if err != nil {
		return 0, err
	}
	var dlUnix int64
	if dl != nil {
		dlUnix = dl.Unix()
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			if state.FirstRunAt == 0 {
				state.FirstRunAt = time.Now().Unix()
			}
			state.DeadlineAt = dlUnix
		})
	}
	m.mu.Unlock()
	return dlUnix, nil
}

// SetTaskStatusGuarded sets a TERMINAL status only if the task isn't already terminal
// (resolves the completed↔timeout race — first terminal writer wins). Reflects the
// won status on the live handle. won=false means another terminal already stuck.
func (m *Manager) SetTaskStatusGuarded(id, status string) (won bool, err error) {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return false, err
	}
	won, err = m.pg.SetTerminalStatusGuarded(n, status)
	if err != nil || !won {
		return won, err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = status
			if state.CompletedAt == 0 {
				state.CompletedAt = time.Now().Unix()
			}
		})
	}
	m.mu.Unlock()
	return true, nil
}

// SetTaskStatus persists a task's lifecycle status (e.g. "done") and reflects it
// on the in-memory handle so the derived DTO status shows it without a reload.
func (m *Manager) SetTaskStatus(id, status string) error {
	m.taskStateMu.Lock()
	defer m.taskStateMu.Unlock()
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}
	if err := m.pg.SetStatus(n, status); err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.tasks[id]; t != nil {
		t.updateLifecycle(func(state *taskLifecycleState) {
			state.Status = status
			// Mirror the DB's completed_at stamp on the live handle so the DTO shows
			// the finish time without a reload (terminal -> stamp once; else clear).
			if pgdb.IsTerminal(status) {
				if state.CompletedAt == 0 {
					state.CompletedAt = time.Now().Unix()
				}
			} else {
				state.CompletedAt = 0
			}
		})
	}
	m.mu.Unlock()
	return nil
}

// DeleteTask removes a task and optionally its related global data. Traffic has
// no task-id column, so related exchanges are resolved by exact hosts from the
// task's asset rows. Files are staged before the database operation; traffic is
// staged while PostgreSQL excludes asset/anchor writers. Both are restored on a
// database failure and purged only after its commit.
func (m *Manager) DeleteTask(id string, opts DeleteTaskOptions) (DeleteTaskResult, error) {
	result := DeleteTaskResult{Deleted: id}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return result, err
	}
	registered, err := m.pg.GetTask(n)
	if err != nil {
		return result, err
	}
	if registered == nil {
		m.forgetTask(id, n)
		return result, nil
	}

	var fileStage *taskFileDeleteStage
	if opts.DeleteFiles {
		fileStage, err = stageTaskFiles(m.dir, id, registered.ExplorationID)
		if err != nil {
			return result, err
		}
		result.FilesDeleted = fileStage.deleted
	}

	var trafficStage *traffic.HostDeleteStage
	var prepare func(pgdb.TaskDeletePreparation) error
	if opts.DeleteTraffic && m.traffic != nil {
		prepare = func(p pgdb.TaskDeletePreparation) error {
			if len(p.TrafficHosts) == 0 {
				return nil
			}
			trafficStage, err = m.traffic.StageDeleteHostsExact(p.TrafficHosts)
			if err != nil {
				return err
			}
			result.TrafficDeleted = trafficStage.Deleted()
			return nil
		}
	}

	dbResult, err := m.pg.DeleteTaskCascadePrepared(
		n, opts.DeleteAssets, opts.DeleteFindings, opts.DeleteLLMRecords, prepare,
	)
	if err != nil {
		return result, rollbackTaskDelete(err, trafficStage, fileStage)
	}
	result.AssetsDeleted = dbResult.AssetsDeleted
	result.AssetsDetached = dbResult.AssetsDetached
	result.FindingsDeleted = dbResult.FindingsDeleted
	result.LLMRecordsDeleted = dbResult.LLMRecordsDeleted

	// PostgreSQL is now authoritative: finalize the staged external deletion and
	// forget the live task even if a final purge reports an error. Such errors are
	// typed so the HTTP layer can still tear down the task runtime instead of
	// incorrectly reviving a task whose database row is already gone.
	var finalizeErrs []error
	if trafficStage != nil {
		if err := trafficStage.Commit(); err != nil {
			finalizeErrs = append(finalizeErrs, fmt.Errorf("finalize traffic deletion: %w", err))
		}
	}
	if fileStage != nil {
		if err := fileStage.commit(); err != nil {
			finalizeErrs = append(finalizeErrs, fmt.Errorf("finalize task file deletion: %w", err))
		}
	}
	m.forgetTask(id, n)
	if err := errors.Join(finalizeErrs...); err != nil {
		return result, &taskDeleteCommittedError{err: err}
	}
	return result, nil
}

// taskDeleteCommittedError means PostgreSQL deletion succeeded but purging one
// of the recoverable staging directories failed. The task must stay deleted.
type taskDeleteCommittedError struct{ err error }

func (e *taskDeleteCommittedError) Error() string {
	return "task deletion committed; external cleanup incomplete: " + e.err.Error()
}

func (e *taskDeleteCommittedError) Unwrap() error { return e.err }

func rollbackTaskDelete(cause error, trafficStage *traffic.HostDeleteStage, fileStage *taskFileDeleteStage) error {
	errs := []error{cause}
	// Reverse the preparation order. Both restorations are attempted even if the
	// first one fails, and errors.Join preserves the original PostgreSQL error.
	if trafficStage != nil {
		if err := trafficStage.Rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore traffic after task delete failure: %w", err))
		}
	}
	if fileStage != nil {
		if err := fileStage.rollback(); err != nil {
			errs = append(errs, fmt.Errorf("restore task files after task delete failure: %w", err))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) forgetTask(id string, numericID int64) {
	m.mu.Lock()
	delete(m.tasks, id)
	for _, task := range m.tasks {
		task.updateLifecycle(func(state *taskLifecycleState) {
			kept := make([]int64, 0, len(state.SourceTaskIDs))
			for _, sourceID := range state.SourceTaskIDs {
				if sourceID != numericID {
					kept = append(kept, sourceID)
				}
			}
			state.SourceTaskIDs = kept
		})
	}
	if m.active == id {
		m.active = ""
		for _, t := range m.tasks {
			m.active = t.ID
			break
		}
	}
	m.mu.Unlock()
}

type stagedTaskPath struct {
	source string
	staged string
}

type taskFileDeleteStage struct {
	stageDir string
	moves    []stagedTaskPath
	deleted  bool
	done     bool
}

// stageTaskFiles atomically renames the task workspace and owned transcripts to
// a same-filesystem staging directory. The trailing dash in the transcript
// prefix is significant: exploration 12 must not match exploration 123.
func stageTaskFiles(dataDir, taskID string, explorationID int64) (*taskFileDeleteStage, error) {
	stage := &taskFileDeleteStage{}
	var targets []string
	taskDir := filepath.Join(dataDir, "tasks", taskID)
	if _, err := os.Lstat(taskDir); err == nil {
		targets = append(targets, taskDir)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	transcriptDir := filepath.Join(dataDir, "transcripts")
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		entries = nil
	}
	prefix := fmt.Sprintf("exp%d-", explorationID)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (!entry.IsDir() && !strings.HasSuffix(name, ".jsonl")) {
			continue
		}
		targets = append(targets, filepath.Join(transcriptDir, name))
	}
	if len(targets) == 0 {
		stage.done = true
		return stage, nil
	}

	parent := filepath.Join(dataDir, ".delete-staging")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	stage.stageDir, err = os.MkdirTemp(parent, "task-"+taskID+"-")
	if err != nil {
		return nil, err
	}
	for _, source := range targets {
		staged := filepath.Join(stage.stageDir, fmt.Sprintf("%d-%s", len(stage.moves), filepath.Base(source)))
		if err := os.Rename(source, staged); err != nil {
			cause := fmt.Errorf("stage task file %s: %w", source, err)
			if restoreErr := stage.rollback(); restoreErr != nil {
				return nil, errors.Join(cause, fmt.Errorf("restore partially staged task files: %w", restoreErr))
			}
			return nil, cause
		}
		stage.moves = append(stage.moves, stagedTaskPath{source: source, staged: staged})
	}
	stage.deleted = true
	return stage, nil
}

func (s *taskFileDeleteStage) commit() error {
	if s == nil || s.done {
		return nil
	}
	err := os.RemoveAll(s.stageDir)
	s.done = true
	return err
}

func (s *taskFileDeleteStage) rollback() error {
	if s == nil || s.done {
		return nil
	}
	var errs []error
	for i := len(s.moves) - 1; i >= 0; i-- {
		move := s.moves[i]
		if _, err := os.Lstat(move.source); err == nil {
			errs = append(errs, fmt.Errorf("restore destination already exists: %s", move.source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("inspect restore destination %s: %w", move.source, err))
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.source), 0o755); err != nil {
			errs = append(errs, fmt.Errorf("create restore parent for %s: %w", move.source, err))
			continue
		}
		if err := os.Rename(move.staged, move.source); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", move.source, err))
		}
	}
	if len(errs) == 0 && s.stageDir != "" {
		if err := os.RemoveAll(s.stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove task file stage: %w", err))
		}
	}
	s.done = true
	return errors.Join(errs...)
}

// deleteTaskFiles retains the standalone helper contract used by focused tests.
func deleteTaskFiles(dataDir, taskID string, explorationID int64) (bool, error) {
	stage, err := stageTaskFiles(dataDir, taskID, explorationID)
	if err != nil {
		return false, err
	}
	deleted := stage.deleted
	if err := stage.commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

func (m *Manager) Task(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	return t, ok
}

// ActiveTask returns the currently active task (or nil).
func (m *Manager) ActiveTask() *Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == "" {
		return nil
	}
	return m.tasks[m.active]
}

// SetActive switches the active task. Returns false if the id is unknown.
func (m *Manager) SetActive(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[id]; !ok {
		return false
	}
	m.active = id
	return true
}

func (m *Manager) ResolveTask(id string) *Task {
	if id == "" || id == "active" {
		return m.ActiveTask()
	}
	t, _ := m.Task(id)
	return t
}

func (m *Manager) List() []*Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Notify signals that the asset/exploration graph changed (debounced consumer
// wakes the planner). Non-blocking.
func (t *Task) Notify() {
	select {
	case t.notify <- struct{}{}:
	default:
	}
}

// NotifyDone is Notify plus a hint: a worker just finished intentID and that is
// what triggered this wake-up. The planner reads the accumulated triggers next
// round so it can spell out which intent finished (+ its output). Events pile up
// (debounce) until the round drains them via drainTriggers.
func (t *Task) NotifyDone(intentID int64) {
	if intentID > 0 {
		t.trigMu.Lock()
		t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "done", IntentID: intentID})
		t.trigMu.Unlock()
	}
	t.Notify()
}

// NotifyFinding records that a worker reported a finding on intentID (summary),
// then wakes the planner — so the round spells out which intent found what.
func (t *Task) NotifyFinding(intentID int64, summary string) {
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "finding", IntentID: intentID, Detail: summary})
	t.trigMu.Unlock()
	t.Notify()
}

// NotifyGoal records that one OR MORE goals were added in a single set_goals call —
// by the human via the main agent — then wakes the planner, so the next round spells
// out "人新增了 N 个目标：…" instead of the planner having to spot new open goals in
// the overview. One call → one trigger event (set_goals 的一次批量算一条，不逐条刷屏).
// The event survives an early-returning terminal round (drain happens after the gate),
// so a set_goals that revives a done task still surfaces it once the task is running.
func (t *Task) NotifyGoal(texts []string) {
	if len(texts) == 0 {
		return
	}
	t.trigMu.Lock()
	t.pendingTriggers = append(t.pendingTriggers, agent.TriggerEvent{Kind: "goal", Goals: texts})
	t.trigMu.Unlock()
	t.Notify()
}

// drainTriggers returns and clears the trigger events accumulated since the last round.
func (t *Task) drainTriggers() []agent.TriggerEvent {
	t.trigMu.Lock()
	defer t.trigMu.Unlock()
	ev := t.pendingTriggers
	t.pendingTriggers = nil
	return ev
}
