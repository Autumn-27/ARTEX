// Package db is the PostgreSQL data source for ARTEX (取代旧 graph 单文件 SQLite)。
// 它打开连接、应用 schema、并 seed 内置 agent 与变量目录。
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver ("pgx")
)

//go:embed schema.sql
var schemaSQL string

const schemaMigrationLockKey int64 = 7337741001

var schemaDeadlockRetryDelays = [...]time.Duration{
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
}

type schemaExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func isPostgresDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func applySchemaWithRetry(ctx context.Context, execer schemaExecer, sleep func(time.Duration)) error {
	for attempt := 0; ; attempt++ {
		if _, err := execer.ExecContext(ctx, schemaSQL); err != nil {
			if !isPostgresDeadlock(err) || attempt >= len(schemaDeadlockRetryDelays) {
				return err
			}
			sleep(schemaDeadlockRetryDelays[attempt])
			continue
		}
		return nil
	}
}

// withSchemaMigrationLock pins the session-level lock to one checked-out
// connection. Running pg_advisory_lock through *sql.DB is incorrect because a
// later schema or unlock call may use a different pooled PostgreSQL session.
func withSchemaMigrationLock(ctx context.Context, sqlDB *sql.DB, action func(*sql.Conn) error) (err error) {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("advisory lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, schemaMigrationLockKey); unlockErr != nil && err == nil {
			err = fmt.Errorf("advisory unlock: %w", unlockErr)
		}
	}()
	return action(conn)
}

// coordinateWithSchemaMigration makes long, multi-table archive transactions
// mutually exclusive with startup DDL while allowing ordinary runtime queries
// to continue normally.
func coordinateWithSchemaMigration(tx *sql.Tx) error {
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, schemaMigrationLockKey); err != nil {
		return fmt.Errorf("coordinate with schema migration: %w", err)
	}
	return nil
}

// DSN resolves the PostgreSQL connection string and reports where it came from.
// Precedence: env ARTEX_PG_DSN > config file (config.json). There is no
// built-in default — it errors if neither source is configured.
func DSN() (dsn, source string, err error) {
	return config.PostgresDSN()
}

// Probe 轻量只读预检（artex doctor 用）：连接 + ping，通了就顺手只读统计
// LLM profile 数。与 Open 不同，这里不做任何迁移/seed/写操作。
// 返回 (llmProfiles, err)：err 非 nil 是连接失败；llmProfiles < 0 表示连上了
// 但查不到（如库还没初始化，llm_profiles 表不存在）。
func Probe(dsn string) (int, error) {
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return -1, err
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return -1, err
	}
	var n int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM llm_profiles`).Scan(&n); err != nil {
		return -1, nil
	}
	return n, nil
}

// DB wraps the shared *sql.DB. PG handles its own connection pool + concurrency
// (MVCC), so unlike the old SQLite store there is no process-wide write mutex.
type DB struct{ *sql.DB }

// ensureDatabase connects to the postgres system database and creates the target
// database if it does not exist. dsn must be a postgres:// URL.
func ensureDatabase(dsn string) error {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil // unparseable DSN — let the normal Open fail with a clear error
	}
	dbName := strings.TrimPrefix(u.Path, "/")
	if dbName == "" || dbName == "postgres" {
		return nil
	}
	// connect to the postgres maintenance database instead
	adminDSN := *u
	adminDSN.Path = "/postgres"
	admin, err := sql.Open("pgx", adminDSN.String())
	if err != nil {
		return nil // best-effort; let Open surface the real error
	}
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		return nil
	}
	var exists bool
	_ = admin.QueryRow(`SELECT true FROM pg_database WHERE datname=$1`, dbName).Scan(&exists)
	if !exists {
		if _, err := admin.Exec(`CREATE DATABASE "` + dbName + `"`); err != nil {
			return fmt.Errorf("create database %q: %w", dbName, err)
		}
	}
	return nil
}

// Open connects, applies the schema (idempotent), and seeds builtin rows.
func Open(dsn string) (*DB, error) {
	if err := ensureDatabase(dsn); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping postgres (%s): %w", config.Redact(dsn), err)
	}
	d := &DB{sqlDB}
	// pgx runs multi-statement Exec via the simple protocol when there are no args.
	// Keep the dedicated lock connection checked out until both DDL and seeding
	// finish so concurrent application instances cannot initialize out of order.
	err = withSchemaMigrationLock(context.Background(), sqlDB, func(conn *sql.Conn) error {
		if err := applySchemaWithRetry(context.Background(), conn, time.Sleep); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		if err := d.seedBuiltins(); err != nil {
			return fmt.Errorf("seed builtins: %w", err)
		}
		return nil
	})
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// builtinAgent describes one of the fixed agents and its prompt-variable catalog.
type builtinAgent struct {
	key, name, role, desc string
	vars                  []promptVar
	interactiveShell      bool // 建行时的默认交互式 shell 开关；ON CONFLICT 不覆盖用户后续手动开关
	runSeconds            *int // 建行时的单次 run 墙钟上限(秒)；nil=用种子默认(1200)，0=不限时
}

type promptVar struct{ name, desc, example, source string }

// intp 返回 v 的指针，用于给 builtinAgent 可选字段(如 runSeconds)显式取值。
func intp(v int) *int { return &v }

// builtinAgents mirrors docs §5(a). 内置工具不入库；这里只 seed agent + 变量目录。
// 注：planner/worker/mainagent/auto 的交互式 shell 默认由下方 interactive_shell_default_v1
// 块统一置 true（尊重后续 toggle）；这里的 interactiveShell 只给需要「建行即默认开」的新 agent。
var builtinAgents = []builtinAgent{
	{"goals", "目标拆解", "goals", "把渗透任务目标拆解成若干独立、可验证的子目标。", []promptVar{
		{"EngagementDescription", "任务描述（测试对象/背景）", "测试 example.com 站点", "exploration"},
		// Now 是全局 runtime 变量(见 server.globalPromptVars),不再在各 agent 目录里
		// 重复定义,否则 withGlobalVars 追加时会与全局项撞名。
	}, false, nil},
	{"planner", "规划", "planner", "读取态势、判定目标，只在确有未覆盖的新方向时补充探索意图（每任务一个规划循环）。", []promptVar{
		{"Goal", "任务总目标", "拿下 example.com 的管理员权限", "exploration"},
		{"AssetSummary", "资产计数/类型分布摘要(可选)", "domain:3 ip:5 site:2", "distilled"},
	}, false, nil},
	{"mainagent", "主", "main", "人机接口：观察进展，把人的意图落成 hint 或高优先级意图。", []promptVar{
		{"Goal", "当前任务目标", "拿下 example.com 的管理员权限", "exploration"},
		{"AssetSummary", "开局态势摘要(可选)", "domain:3 ip:5", "distilled"},
		{"FindingsSummary", "已确认漏洞摘要(可选)", "high:1 medium:2", "distilled"},
	}, false, nil},
	{"worker", "执行", "worker", "领取一条意图执行，把发现的事实/漏洞写回知识图谱后停止。", []promptVar{
		{"ProxyAddr", "记录代理地址(驱动 if 双文案)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "worker 自我标识(可选)", "worker-1", "runtime"},
		{"Intranet", "是否内网期(有立足点会话/活跃隧道)", "true", "runtime"},
	}, false, nil},
	// worker.intranet:期 4 内网期 worker 提示词变体——不是独立 agent,只是 worker
	// 正文的 DB 覆盖槽位(有立足点/隧道的任务渲染时优先取它,见 agent.renderSystemVariant)。
	{"worker.intranet", "执行·内网", "worker", "worker 的内网期提示词变体：任务存在立足点会话/活跃隧道时代替 worker 正文。", []promptVar{
		{"ProxyAddr", "记录代理地址(驱动 if 双文案)", "127.0.0.1:8080", "runtime"},
		{"WorkerName", "worker 自我标识(可选)", "worker-1", "runtime"},
		{"Intranet", "是否内网期(有立足点会话/活跃隧道)", "true", "runtime"},
	}, false, nil},
	// Auto:内置「平台操作」agent。不参与渗透编排循环,经对话页驱动,用工具操作平台。
	{"auto", "Auto", "assistant", "平台操作助手：用工具管理任务(建/看/暂停/给提示)与资产，并可创建/修改 skill、自定义工具、MCP。", nil, false, nil},
	// 渗透测试:内置「独立渗透」agent。经对话页驱动,一人从侦察到收尾走完整条渗透链,自己规划自己执行自己验证。默认开启交互式 shell。
	{"pentest", "渗透测试", "assistant", "独立渗透 agent：一人从侦察→找攻击面→深入利用→验证→收尾走完整条链，自己规划、自己执行、自己对抗式验证。", nil, true, intp(0)},
}

// seedBuiltins inserts the fixed built-in agents and their variable catalog (idempotent).
func (d *DB) seedBuiltins() error {
	for _, a := range builtinAgents {
		var agentID int64
		err := d.QueryRow(`
INSERT INTO agents(key, name, description, role, builtin, enabled, interactive_shell, run_seconds)
VALUES ($1, $2, NULLIF($3,''), $4, true, true, $5, COALESCE($6, 1200))
ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description
RETURNING id`, a.key, a.name, a.desc, a.role, a.interactiveShell, a.runSeconds).Scan(&agentID)
		if err != nil {
			return fmt.Errorf("agent %s: %w", a.key, err)
		}
		for _, v := range a.vars {
			if _, err := d.Exec(`
INSERT INTO agent_prompt_vars(agent_id, var_name, description, example, source)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, var_name) DO UPDATE
  SET description = EXCLUDED.description, example = EXCLUDED.example, source = EXCLUDED.source`,
				agentID, v.name, v.desc, v.example, v.source); err != nil {
				return fmt.Errorf("agent %s var %s: %w", a.key, v.name, err)
			}
		}
	}
	// Drop catalog entries for variables that were renamed, so the white-list no
	// longer advertises a name templates can't resolve (EngagementTitle→Description).
	// 'Now' 从各 agent 目录提升为全局 runtime 变量后,旧库里 goals 仍残留一条 'Now'
	// 会与全局项撞名(前端变量列表 key 重复);一并清掉。
	if _, err := d.Exec(`DELETE FROM agent_prompt_vars WHERE var_name IN ('EngagementTitle', 'CoverageGaps', 'Now')`); err != nil {
		return fmt.Errorf("cleanup renamed vars: %w", err)
	}
	// Default-on interactive_shell for the runtime agents (planner/worker/mainagent/auto)
	// ONCE — respects a later user toggle-off (guarded by a settings flag). goals(one-shot
	// decomposer) stays off. Runs after the column exists (schema applied before seed).
	if v, _, _ := d.GetSetting("interactive_shell_default_v1"); v != "true" {
		if _, err := d.Exec(`UPDATE agents SET interactive_shell=true WHERE key IN ('planner','worker','mainagent','auto')`); err != nil {
			return fmt.Errorf("seed interactive_shell defaults: %w", err)
		}
		_ = d.SetSetting("interactive_shell_default_v1", "true")
	}
	// Seed the built-in browser (Playwright) MCP once — DISABLED by default (用户
	// 需要时自行启用), no proxy by default. The traffic-capture toggle injects/strips
	// the recording proxy + CA at runtime (server.Manager.syncBrowserMCPProxy).
	// Insert only if absent so we never clobber user edits (args/env/enabled/
	// visibility) on restart.
	if _, err := d.Exec(`
INSERT INTO mcp_servers(name, transport, command, args, env, enabled)
VALUES ('browser', 'stdio', 'npx', $1, '{}', false)
ON CONFLICT (name) DO NOTHING`,
		`["@playwright/mcp","--headless"]`); err != nil {
		return fmt.Errorf("seed browser mcp: %w", err)
	}
	// NOTE: the placeholder ScopeSentry data-source MCP (empty URL + empty X-API-Key,
	// disabled) is seeded directly in schema.sql §F so a raw `psql < schema.sql` init
	// also gets it. schema.sql is Exec'd on every startup, so it stays idempotent.
	if err := d.seedBuiltinSkillVisibility(); err != nil {
		return fmt.Errorf("seed skill visibility: %w", err)
	}
	if err := d.seedDefaultInterceptRules(); err != nil {
		return fmt.Errorf("seed intercept rules: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV2(); err != nil {
		return fmt.Errorf("seed intercept rules v2: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV3(); err != nil {
		return fmt.Errorf("seed intercept rules v3: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV4(); err != nil {
		return fmt.Errorf("seed intercept rules v4: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV5(); err != nil {
		return fmt.Errorf("seed intercept rules v5: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV6(); err != nil {
		return fmt.Errorf("seed intercept rules v6: %w", err)
	}
	if err := d.seedDefaultInterceptRulesV7(); err != nil {
		return fmt.Errorf("seed intercept rules v7: %w", err)
	}
	return nil
}

// builtinSkillVisibility maps a shipped skill's directory name → the built-in
// agent keys that should see it by default. The skill FILES themselves live on the
// filesystem (SkillDir, loaded by norma at runtime); DB only carries this visibility
// binding. Skills omitted here (e.g. playwright-cli, scopesentry) ship invisible by
// default — the user turns them on per-agent when needed. scopesentry additionally
// declares `mcps: ScopeSentry`, which only takes effect once it's made visible and
// that MCP is enabled/configured.
var builtinSkillVisibility = map[string][]string{
	"api-recon": {"auto", "pentest", "worker"},
}

// seedBuiltinSkillVisibility binds the shipped built-in skills to their default
// agents. Insert-if-absent (ON CONFLICT DO NOTHING) so a user's later toggle-off is
// never resurrected on restart — matches the browser-MCP / intercept-rule seed policy.
func (d *DB) seedBuiltinSkillVisibility() error {
	for skillName, agentKeys := range builtinSkillVisibility {
		for _, key := range agentKeys {
			if _, err := d.Exec(`
INSERT INTO agent_skill_visibility(agent_id, skill_name, enabled)
SELECT id, $2, true FROM agents WHERE key=$1
ON CONFLICT (agent_id, skill_name) DO NOTHING`, key, skillName); err != nil {
				return fmt.Errorf("skill %s → agent %s: %w", skillName, key, err)
			}
		}
	}
	return nil
}

// seedDefaultInterceptRules inserts built-in safety intercept rules once on
// first startup. The seed is gated by a settings flag so user edits (disable,
// delete, re-order) are never overwritten on subsequent restarts.
func (d *DB) seedDefaultInterceptRules() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v1"); v == "done" {
		return nil
	}
	type rule struct {
		name     string
		target   string // tool_name | tool_input
		typ      string // string | regex
		pattern  string
		action   string
		message  string
		priority int
	}
	rules := []rule{
		// ── 系统破坏性命令 (priority 100) ──────────────────────────────────
		{
			name:     "[内置] 递归强制删除 rm -rf",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
			action:   "deny",
			message:  "禁止执行递归强制删除（rm -rf / rm --recursive），可能永久损坏系统或靶机环境",
			priority: 100,
		},
		{
			name:     "[内置] 删除系统关键目录",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
			action:   "deny",
			message:  "禁止删除系统关键路径",
			priority: 100,
		},
		{
			name:     "[内置] 磁盘格式化 mkfs",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bmkfs\b`,
			action:   "deny",
			message:  "禁止格式化磁盘（mkfs）",
			priority: 100,
		},
		{
			name:     "[内置] 覆写磁盘设备 dd",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "禁止使用 dd 覆写磁盘设备",
			priority: 100,
		},
		{
			name:     "[内置] Fork 炸弹",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `:\(\)\s*\{[^}]*:\|:`,
			action:   "deny",
			message:  "禁止执行 Fork 炸弹",
			priority: 100,
		},
		{
			name:     "[内置] 关机 / 重启",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
			action:   "deny",
			message:  "禁止执行关机或重启命令",
			priority: 100,
		},
		{
			name:     "[内置] 杀死全部进程",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`,
			action:   "deny",
			message:  "禁止 kill -9 -1 或 killall -9（杀死所有进程）",
			priority: 100,
		},
		{
			name:     "[内置] 磁盘擦除 shred / wipe",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`,
			action:   "deny",
			message:  "禁止对磁盘设备执行 shred/wipe 擦除",
			priority: 100,
		},
		{
			name:     "[内置] 清空防火墙规则",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`,
			action:   "deny",
			message:  "禁止清空防火墙规则（iptables -F / nft flush）",
			priority: 100,
		},
		// ── 数据库破坏性操作 (priority 90) ─────────────────────────────────
		{
			name:     "[内置] SQL DROP DATABASE / TABLE / SCHEMA",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`,
			action:   "deny",
			message:  "禁止执行 DROP 操作，可能不可逆地销毁数据库对象",
			priority: 90,
		},
		{
			name:     "[内置] SQL TRUNCATE",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`,
			action:   "deny",
			message:  "禁止执行 TRUNCATE，可能清空数据表所有数据",
			priority: 90,
		},
		{
			name:     "[内置] MongoDB drop / dropDatabase",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`,
			action:   "deny",
			message:  "禁止执行 MongoDB drop 操作",
			priority: 90,
		},
		{
			name:     "[内置] Redis FLUSHALL / FLUSHDB",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:FLUSHALL|FLUSHDB)\b`,
			action:   "deny",
			message:  "禁止执行 Redis FLUSHALL / FLUSHDB，可能清空全部缓存数据",
			priority: 90,
		},
		// ── HTTP 破坏性请求 (priority 80) ──────────────────────────────────
		// Agent 发送 DELETE 请求的三种常见方式：
		//   1. curl -X DELETE / --request DELETE（Bash 工具直接执行或写入脚本）
		//   2. Python HTTP 客户端 .delete() 方法
		//   3. JS/通用脚本里的 method: 'DELETE' / method="DELETE"
		{
			name:     "[内置] curl / wget 发送 DELETE 请求",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
			action:   "deny",
			message:  "禁止通过 curl/wget 发送 HTTP DELETE 请求，可能删除目标系统数据",
			priority: 80,
		},
		{
			name:     "[内置] Python HTTP 客户端 DELETE（requests/httpx/aiohttp）",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
			action:   "deny",
			message:  "禁止使用 Python HTTP 客户端发送 DELETE 请求",
			priority: 80,
		},
		{
			name:     "[内置] 脚本中声明 HTTP DELETE 方法（JS/通用）",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
			action:   "deny",
			message:  "禁止在脚本中声明并发送 HTTP DELETE 请求",
			priority: 80,
		},
		{
			name:     "[内置] 批量清空 / 清除接口路径",
			target:   "tool_input",
			typ:      "regex",
			pattern:  `(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
			action:   "deny",
			message:  "禁止调用批量清空或销毁类接口（/clear /wipe /flush /purge 等）",
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, true, $2, $3, $4, $5, $6, $7, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.priority, r.target, r.typ, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v1", "done")
}

// seedDefaultInterceptRulesV2 migrates the two safety patterns that used to be
// hard-coded in guard.go (destructive shell + data-exfil pipe) into ordinary
// intercept rules. Gated by its own flag so it also lands on DBs that already ran
// v1. Unlike the old guard.go floor, these are plain [内置] rules — the user can
// disable or delete them. The exfil rule ships DISABLED by default (its
// curl/wget/nc pipe pattern mis-fires on legitimate CTF/pentest reverse-shell and
// data-transfer pipes); enable it manually when exfil gating is actually wanted.
func (d *DB) seedDefaultInterceptRulesV2() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v2"); v == "done" {
		return nil
	}
	rules := []struct {
		name     string
		pattern  string
		action   string
		message  string
		enabled  bool
		priority int
	}{
		{
			name:     "[内置] 破坏性系统命令",
			pattern:  `(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
			action:   "deny",
			message:  "破坏性命令被拒绝（rm -rf / / mkfs / dd / fork bomb / 关机重启 / 覆写磁盘设备）",
			enabled:  true,
			priority: 100,
		},
		{
			name:     "[内置] 数据外泄管道",
			pattern:  `(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
			action:   "deny",
			message:  "疑似数据外泄管道被拒绝（命令输出经 curl/wget/nc 外传）",
			enabled:  false,
			priority: 80,
		},
	}
	for _, r := range rules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, 'tool_input', 'regex', $4, $5, $6, false, 60, 'deny')
ON CONFLICT DO NOTHING`,
			r.name, r.enabled, r.priority, r.pattern, r.action, r.message,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v2", "done")
}

// stageShareHint 是临时 HTTP 服务拦截规则的统一放行提示(指向受管替代方案)。
const stageShareHint = "临时 HTTP 服务易遗忘暴露(无 TTL、任务结束即成孤儿、目录里常混有战利品与凭据)。请改用 stage_share 工具投递文件(受管暂存:随机 token 路径、硬 TTL 到期自动销毁、可一次性即焚)。"

// builtinHTTPServerRules 是 F13 的平台底线规则(pattern 均为 tool_input 正则,
// action=ask, builtin=true)。提成包级变量:既给 seed 用,也给无 PG 的正则单测用。
var builtinHTTPServerRules = []struct {
	name    string
	pattern string
}{
	{
		name:    "[内置] 临时 HTTP 服务: python -m http.server",
		pattern: `(?i)\bpython[0-9.]*\s+-m\s+(?:http\.server|SimpleHTTPServer)\b`,
	},
	{
		name:    "[内置] 临时 HTTP 服务: php -S",
		pattern: `(?i)\bphp[0-9.]*\s+-S\b`,
	},
	{
		name:    "[内置] 临时 HTTP 服务: busybox httpd",
		pattern: `(?i)\bbusybox\s+httpd\b`,
	},
	{
		name:    "[内置] 临时 HTTP 服务: ruby -run -e httpd",
		pattern: `(?i)\bruby\b[^\n|;]{0,60}-e\s+httpd\b`,
	},
	{
		name:    "[内置] 临时 HTTP 服务: npx serve / http-server",
		pattern: `(?i)\bnpx\s+(?:-y\s+|--yes\s+)?(?:serve|http-server)\b`,
	},
}

// seedDefaultInterceptRulesV3 堵「野路子 HTTP 服务投递」(F13):worker 用 Bash 自行
// python3 -m http.server / php -S / busybox httpd / npx serve 起临时服务投递武器,
// 平台零感知、无 TTL,任务结束即成对全网开放的孤儿。这批是【平台底线规则】:
// builtin=true,禁止删除/禁用(DeleteInterceptRule/ToggleInterceptRule 拒绝)。
// action=ask 而非 deny:确有合理场景(如目标必须回连特定端口)可人工放行一次。
func (d *DB) seedDefaultInterceptRulesV3() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v3"); v == "done" {
		return nil
	}
	for _, r := range builtinHTTPServerRules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, builtin)
VALUES ($1, true, 85, 'tool_input', 'regex', $2, 'ask', $3, false, 60, 'deny', true)
ON CONFLICT DO NOTHING`,
			r.name, r.pattern, stageShareHint,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v3", "done")
}

// lateralHint 是横向/喷洒拦截规则的统一提示(内网 blast radius 远大于外网:
// 喷洒可锁死全域账号、SMB 打爆终端,见 INTRANET-PIVOT-DESIGN.md §6.3)。
const lateralHint = "横向移动/口令喷洒属高动静动作(可锁死账号、触发告警),需人工确认,且须确认目标在 RoE 范围内(新网段≠已授权,先登记 task scope)。"

// builtinLateralRules 是内网期 2 的平台底线规则(§4.4:喷洒/爆破动作必须过 guard
// ask;F13 同模式:pattern 均为 tool_input 正则,action=ask,builtin=true)。
//
// 取舍说明:hashcat 不列入——它是本地破密工具,只有命令行同时出现目标 host 才该
// 拦,而「任意 hashcat 参数里含 IP/域名」用正则表达会大量误伤本地破解场景
// (字典/掩码路径常含点分数字),宁可不列、在工具与提示侧约束,也不做半吊子拦截。
var builtinLateralRules = []struct {
	name    string
	pattern string
}{
	{
		name:    "[内置] 口令喷洒/爆破: crackmapexec/nxc",
		pattern: `(?i)\b(?:crackmapexec|cme|nxc|netexec)\b`,
	},
	{
		name:    "[内置] 口令喷洒/爆破: hydra",
		pattern: `(?i)\bhydra\b`,
	},
	{
		name:    "[内置] 口令喷洒/爆破: medusa",
		pattern: `(?i)\bmedusa\b`,
	},
	{
		name:    "[内置] 横向执行: psexec/wmiexec/smbexec",
		pattern: `(?i)\b(?:psexec|wmiexec|smbexec|atexec|dcomexec)(?:\.py)?\b`,
	},
	{
		name:    "[内置] 域 Kerberos 枚举/喷洒: kerbrute",
		pattern: `(?i)\bkerbrute\b`,
	},
}

// builtinTunnelBinaryRules 拦「裸用隧道二进制」(内网期 3 实战教训):worker 绕过
// tunnel_deploy 直接用 chisel/frp 等建隧道——无台账、无健康看护、无自动回收,
// 违反受管资源原则(INTRANET-PIVOT-DESIGN.md 4.3a)。平台底线规则,builtin=true。
var builtinTunnelBinaryRules = []struct {
	name    string
	pattern string
}{
	{
		name: "[内置] 裸用隧道工具(改用 tunnel_deploy): chisel/frp/ligolo/suo5 等",
		// 只拦"作为命令执行"(行首/管道/分号/sudo 后,可带路径),
		// 不拦文本中提及该词(grep/cat/readme 诊断场景,实战误伤教训)。
		// suo5 在列:平台 tunnel_deploy 自己起 CLI 走 Go exec,不过 tool_input 拦截,
		// 天然豁免;worker 裸 Bash 起 suo5 仍拦(无台账/看护/回收)。
		pattern: `(?i)(?:^|[|;&]\s*|&&\s*|\|\|\s*|sudo\s+)(?:\S*/)?(?:chisel|frpc|frps|ligolo(?:-ng)?|nps|npc|stowaway|iodine|dnscat2?|suo5)\b`,
	},
}

// tunnelBinaryHint 是裸用隧道工具被 ask 拦截时给模型的人工审批提示。
const tunnelBinaryHint = "裸用隧道二进制没有台账/健康看护/自动回收,违反受管资源原则。" +
	"建隧道请改用 tunnel_deploy(内建投递/验证/看护),临时文件投递用 stage_share。" +
	"确需裸用的授权场景可人工放行一次。"

// builtinProtocolClientRules 拦「起手就手写协议客户端」(工具纪律的 guard 兜底):
// 红日3 复盘里 worker 手写 MySQL/SMB/TDS/Kerberos 裸 socket 客户端,产出错误 fact 污染
// 规划。已装工具(gogo/naabu/httpx/katana/impacket 等)与平台工具必须先用,手搓是最后
// 妥协。action=ask 而非 deny:确有授权场景(工具全覆盖不到)可人工放行一次。
var builtinProtocolClientRules = []struct {
	name    string
	pattern string
}{
	{
		name:    "[内置] 自研协议客户端(先用已装工具): socket+协议特征",
		pattern: `(?is)(import\s+socket|from\s+socket\s+import|socket\.create_connection)[\s\S]{0,600}(smb2?|mysql|postgres|mssql|tds|kerberos|krb5|ldap|ntlm|dcerpc|ncacn|drsuapi)`,
	},
}

// protocolClientHint 是自研协议客户端被 ask 拦截时给模型的人工审批提示。
const protocolClientHint = "检测到自研协议客户端迹象。工具纪律:协议交互/扫描必须先用已装工具(gogo/naabu/httpx/katana/impacket)与平台工具。" +
	"确认这些工具都无法完成后,人工可放行一次;放行时请在 fact 写明哪个工具不行、为什么。"

// seedDefaultInterceptRulesV6 拦「起手手写协议客户端」(工具纪律 guard 兜底,平台底线规则)。
func (d *DB) seedDefaultInterceptRulesV6() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v6"); v == "done" {
		return nil
	}
	for _, r := range builtinProtocolClientRules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, builtin)
VALUES ($1, true, 88, 'tool_input', 'regex', $2, 'ask', $3, false, 60, 'deny', true)
ON CONFLICT DO NOTHING`,
			r.name, r.pattern, protocolClientHint,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v6", "done")
}

// seedDefaultInterceptRulesV5 拦「裸用隧道二进制」(内网期 3):隧道是长寿命受管资源,
// 必须走 tunnel_deploy 进入台账,裸 Bash 起的隧道进程平台无法看护与回收。
func (d *DB) seedDefaultInterceptRulesV5() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v5"); v == "done" {
		return nil
	}
	for _, r := range builtinTunnelBinaryRules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, builtin)
VALUES ($1, true, 87, 'tool_input', 'regex', $2, 'ask', $3, false, 60, 'deny', true)
ON CONFLICT DO NOTHING`,
			r.name, r.pattern, tunnelBinaryHint,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v5", "done")
}

// seedDefaultInterceptRulesV7 把 suo5 并入「裸用隧道工具」底线规则（suo5 适配器上线）:
// 平台 tunnel_deploy 自己起 suo5 CLI 走 Go exec，不过 tool_input 拦截，天然豁免；
// worker 裸 Bash 起 suo5 无台账/看护/回收，仍拦。已 seed 过 V5 的老库在此改名+换
// 正则；新库 V5 已用新规则，UPDATE 0 行后 ON CONFLICT 兜底为空操作。
func (d *DB) seedDefaultInterceptRulesV7() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v7"); v == "done" {
		return nil
	}
	r := builtinTunnelBinaryRules[0]
	res, err := d.Exec(`
UPDATE intercept_rules SET name=$2, pattern=$3, message=$4
WHERE name=$1 AND builtin=true`,
		"[内置] 裸用隧道工具(改用 tunnel_deploy): chisel/frp/ligolo 等", r.name, r.pattern, tunnelBinaryHint)
	if err != nil {
		return fmt.Errorf("rule %q: %w", r.name, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, builtin)
VALUES ($1, true, 87, 'tool_input', 'regex', $2, 'ask', $3, false, 60, 'deny', true)
ON CONFLICT DO NOTHING`,
			r.name, r.pattern, tunnelBinaryHint); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v7", "done")
}

// seedDefaultInterceptRulesV4 拦「横向移动/口令喷洒类工具」(内网期 2):worker 直接
// crackmapexec/nxc/hydra/medusa/psexec.py/kerbrute 打内网,动静大、可锁死全域账号,
// 必须人工确认且在 RoE 范围内。这批是【平台底线规则】:builtin=true,禁止删除/禁用;
// action=ask:确有授权场景可人工放行一次。
func (d *DB) seedDefaultInterceptRulesV4() error {
	if v, _, _ := d.GetSetting("intercept_default_rules_v4"); v == "done" {
		return nil
	}
	for _, r := range builtinLateralRules {
		if _, err := d.Exec(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action, builtin)
VALUES ($1, true, 86, 'tool_input', 'regex', $2, 'ask', $3, false, 60, 'deny', true)
ON CONFLICT DO NOTHING`,
			r.name, r.pattern, lateralHint,
		); err != nil {
			return fmt.Errorf("rule %q: %w", r.name, err)
		}
	}
	return d.SetSetting("intercept_default_rules_v4", "done")
}
