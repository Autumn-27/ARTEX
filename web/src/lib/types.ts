// ARTEX domain model — types used across the UI.
// Derived from the functional spec (section 7: 关键数据形状).

export type TaskStatus = "created" | "running" | "paused" | "done" | "failed" | "timeout";
export type EngineMode = "exploring" | "paused" | "stalled" | "idle";

export interface Task {
  id: string;
  description: string;
  goal: string;
  status: TaskStatus;
  created_at: string;
  created_unix?: number; // created_at as unix seconds (run-duration calc)
  completed_at?: string; // RFC3339 finish time (done/failed); "" if unfinished
  completed_unix?: number; // completed_at as unix seconds (0/undef if unfinished)
  last_activity_unix?: number; // unix seconds of the last activity (0/undef if none)
  paused?: boolean;
  active?: boolean;
  in_flight?: number;
  last_activity?: string;
  stalled?: boolean;
  goals_total?: number;
  goals_met?: number;
  engine_mode?: EngineMode;
  tokens?: TokenTotal; // whole-task token consumption
  llm_profile_id?: number; // LLM profile used; absent = default profile
}

// ---- Asset graph (global, shared across tasks) ----
export type AssetType =
  | "company"
  | "domain"
  | "ip"
  | "port"
  | "service"
  | "site"
  | "endpoint"
  | "parameter"
  | "tech"
  | "credential"
  | "data";

export type NodeState = "observed" | "confirmed" | "tombstoned";

export interface AssetNode {
  id: string;
  type: AssetType;
  name: string;
  key: string; // nkey
  value?: string;
  company_id?: string; // 归属公司资产 id；空=未归属
  state: NodeState;
  confidence: number; // 0..1
  attrs?: Record<string, unknown>;
  first_seen: string;
  last_seen: string;
}

export type AssetRel =
  | "owns"
  | "resolves"
  | "exposes"
  | "runs"
  | "serves"
  | "has_endpoint"
  | "has_param"
  | "fingerprinted"
  | "authenticates_as"
  | "reachable"
  | "has_subdomain";

export interface Edge {
  src: string;
  dst: string;
  rel: AssetRel | ExploreRel;
}

// Task asset view — server-side enriched, paginated.
export interface TaskAssetRef {
  id: string;
  name?: string;
  key: string;
  attrs?: Record<string, unknown>;
}

export interface TaskAssetItem extends AssetNode {
  techs?: TaskAssetRef[];
  auth?: TaskAssetRef[];
  params?: TaskAssetRef[];
}

export interface TaskAssetView {
  counts: Record<string, number>;
  total: number;
  items: TaskAssetItem[];
}

// ---- New unified asset model (new backend) ----
export type NewAssetType = "root_domain" | "ip" | "subdomain" | "app" | "service" | "endpoint";

export interface Asset {
  id: number;
  type: NewAssetType;
  company_id?: number;
  task_ids: number[];
  domain?: string;
  root_domain?: string;
  ip?: string;
  c_segment?: string;
  port?: number;
  icp?: string;
  bound_domains?: string[];
  open_ports?: { port: number; service?: string }[];
  record_type?: string;
  record_value?: string[] | string;
  bundle_id?: string;
  app_name?: string;
  category?: string;
  app_description?: string;
  app_icp?: string;
  url?: string;
  service_type?: string;
  service_name?: string;
  favicon_mmh3?: string;
  status_code?: number;
  content_length?: number;
  page_title?: string;
  technologies?: string[];
  auth?: Record<string, unknown>[];
  method?: string;
  params?: Record<string, unknown>[];
  extra?: Record<string, unknown>;
  last_seen: string;
}

// ---- Asset coverage graph (per task) ----
// 力导向「资产覆盖图」的一个节点。key 唯一：资产="a:<id>"、公司="c:<id>"、
// 无资产行的根域名="r:<domain>"。in_scope=false 的是仅用于连线的灰色上下文节点。
export interface CoverageGraphNode {
  key: string;
  kind: "company" | "root_domain" | "subdomain" | "ip" | "service" | "app" | "endpoint";
  label: string;
  tested: boolean;
  in_scope: boolean;
  asset_id?: number;
  company_id?: number;
  domain?: string;
  root_domain?: string;
  ip?: string;
  url?: string;
  port?: number;
  service_type?: string;
  app_name?: string;
  page_title?: string;
  status_code?: number;
}

export interface CoverageGraphEdge {
  src: string;
  dst: string;
}

export interface CoverageGraphData {
  nodes: CoverageGraphNode[];
  edges: CoverageGraphEdge[];
}

// 公司资产范围规则的一条（归属唯一真值来源）。
export interface ScopeRow {
  id: number;
  company_id: number;
  kind: "domain" | "ip" | "cidr";
  domain?: string;  // kind=domain 时有值
  net?: string;     // kind=ip|cidr 时有值
  raw: string;      // 原始用户输入，用于显示和回填
  reason?: string;
}

// 企业：type=company 的资产节点 + 图标 + 资产计数 + 资产范围规则。
export interface Company {
  id: number;
  name: string;
  logo?: string; // 远程图标 URL；空则前端用名称首字母
  asset_count: number;
  scope: ScopeRow[];
}

// ---- Exploration graph (per task) ----
export type ExploreKind = "task" | "begin" | "goal" | "intent" | "fact" | "finding" | "hint";
export type GoalState = "open" | "met" | "abandoned";
export type IntentState = "open" | "running" | "done" | "blocked" | "exhausted" | "stopped";
export type FindingState = "confirmed" | "dismissed";
export type HintState = "active" | "consumed";
export type ExploreRel = "spawns" | "derived_from" | "yields" | "proves";

export interface TaskNode {
  id: string;
  type: ExploreKind;
  payload?: string;
  priority: number; // 0..10
  state: string; // GoalState | IntentState | FindingState | HintState
  origin: string;
  ts: string;
}

// ---- Findings ----
export type Severity = "high" | "medium" | "low";

export interface Finding {
  id: string;
  vulnclass: string;
  severity: Severity;
  summary: string;
  evidence: string;
  intent_id?: string;
  param_id?: string;
  task_id?: string;
  task_description?: string;
  ts: string;
}

// ---- Activity / sessions ----
export type ActivityKind =
  | "tool_use"
  | "tool_result"
  | "text"
  | "thinking"
  | "result"
  | "user"
  | "intent"            // LLM-generated exploration objective leading a worker session (UI-synthesized)
  | "round"             // planner round boundary marker (engine-emitted)
  | "usage"             // live cumulative token usage (per model turn); not rendered
  | "intercept_request"; // user-approval request from the intercept layer

export interface Activity {
  seq: number;
  intent_id?: string;
  worker: string; // session owner: planner | mainagent | work#1 ...
  ts: string;
  kind: ActivityKind;
  tool?: string;
  tool_use_id?: string;
  is_error?: boolean;
  summary: string;
  detail?: string;
  // token usage (present only on kind='result')
  input_tokens?: number;
  output_tokens?: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
}

// ---- Agent triggers (P3 调度，仅自定义 agent) ----
export interface AgentTrigger {
  id: number;
  agent_key: string;
  enabled: boolean;
  interval_sec: number; // 定时:每 N 秒(0=不定时)
  on_finding: boolean; // 任意任务发现 finding 时触发
  on_goal_met: boolean; // 任意任务达成目标时触发
  on_task_timeout: boolean; // 任意任务超时时触发
  on_tool_call: boolean; // 选中工具被调用(执行完成)时触发
  interval_message: string; // 各触发条件的独立用户消息
  finding_message: string;
  goal_message: string;
  task_timeout_message: string;
  tool_call_message: string;
  tool_names: string[]; // on_tool_call 选中的工具 key(至少一个)
  last_fire?: string;
}

// ---- Conversations (chat page) ----
export interface Conversation {
  id: number;
  agent_key: string;
  title: string;
  llm_profile_id?: number;
  created_at: string;
  updated_at: string;
}

// ---- Backend logs (/logs page) ----
export interface LogLine {
  seq: number;
  db_id?: number; // server_logs.id; present for DB-persisted lines
  ts: string;
  level: "info" | "warn" | "error";
  tag: string;
  text: string;
}

export type SessionRole = "mainagent" | "planner" | "worker";
export type SessionStatus = "running" | "done" | "blocked" | "exhausted" | "pending" | "stopped";

// Daily token aggregate bucket (GET /api/tokens/daily).
export interface DailyTokenBucket {
  date: string; // "YYYY-MM-DD"
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Per-worker token usage (GET /api/exploration/tokens).
export interface TokenUsage {
  worker: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// Whole-task (all agents) token aggregate.
export interface TokenTotal {
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

export interface Session {
  id: string;
  role: SessionRole;
  title: string;
  status: SessionStatus;
  live: boolean;
  last_activity: string;
  intent_id?: string;
}

// ---- Security ----
export interface AuditEntry {
  ts: string;
  tool: string;
  action: "allow" | "block";
  reason?: string;
  command?: string;
}

export interface Audit {
  entries?: AuditEntry[];
  attributions?: Record<string, number>;
}

// ---- Traffic ----
export interface TrafficExchange {
  id: string;
  ts: string;
  host: string;
  method: string;
  url: string;
  status: number;
  content_type: string;
  resp_len: number;
}

export interface TrafficResp {
  enabled: boolean;
  proxy?: string;
  count?: number; // global total (unfiltered)
  total?: number; // rows matching the current filter (for pagination)
  page?: number;
  size?: number;
  exchanges?: TrafficExchange[];
}

// Full raw request/response of one exchange (lazy-loaded on row select).
export interface TrafficDetail {
  req: string;
  resp: string;
}

// ---- App settings (runtime toggles) ----
export interface Settings {
  traffic_capture: boolean;
  llm_record: boolean; // LLM 录制开关（默认关）；关闭时不记录任何 LLM 调用
  // Web search. brave_key_set / tavily_key_set reflect whether a key is stored
  // (the values are never returned). On PUT, send the corresponding field to set/clear.
  web_search_enabled: boolean;
  web_search_backend: string; // "ddgs" | "brave-free" | "tavily"
  brave_key_set: boolean;
  tavily_key_set: boolean;
  // write-only: only sent on PUT to store/clear the key.
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
  // 独立出口代理(http/https/socks5)，用于访问搜索端点；与记录流量的 MITM 代理无关。空=直连。
  web_search_proxy?: string;
  python_interpreter?: string; // 自定义脚本工具的 python 解释器路径(空=运行时检测)
  workers?: number; // 并发工作 agent 数(默认3)；对之后启动的任务生效
}

// ---- LLM config ----
export interface LLMProfile {
  id: string;
  name: string;
  format: "openai" | "anthropic";
  base_url?: string;
  proxy?: string;
  model: string;
  api_key_hint?: string;
  rate_per_second: number;
  rate_per_minute: number;
  context_window_k?: number;
  // 思考模式: ""=默认(不发送) | "off"=关闭 | "low"/"medium"/"high"/"max"=开启并设强度
  reasoning_effort?: string;
  is_default: boolean;
}

// ---- Agents ----
export interface Agent {
  id: string;
  key: string; // 内置为 goals/planner/mainagent/worker；自定义为用户自定 key
  name: string;
  description?: string;
  role: string;
  builtin: boolean;
  enabled: boolean;
  max_turns?: number; // 0 = 无限制
  run_seconds?: number; // worker 单次运行墙钟上限(秒)；0 = 无限制
  web_search?: boolean; // 是否启用网络搜索(受系统全局开关门控)
  interactive_shell?: boolean; // 是否启用交互式 shell(持久 PTY 会话工具族)
  // P3 触发后处理策略(仅自定义 agent 有意义)
  trigger_run_mode?: "serial" | "parallel"; // 串行排队 / 每次触发各自并发一个会话
  trigger_merge_mode?: "by_task" | "all" | "none"; // 仅 serial：同任务合并 / 全部合并 / 不合并
  trigger_max_parallel?: number; // 仅 parallel：每 agent 并发上限；0=不限
  // 绑定数量(仅列表接口返回)：可见 MCP / 可见 Skill / 绑定工具
  mcp_count?: number;
  skill_count?: number;
  tool_count?: number;
}

export interface PromptVar {
  name: string;
  description: string;
  example: string;
  source: "exploration" | "runtime" | "distilled";
}

export interface PromptVersion {
  version: number;
  ts: string;
  note: string;
  template_text: string;
}

export interface AgentDetail {
  agent: Agent;
  prompt: string;
  variables: PromptVar[];
  versions: PromptVersion[];
  visibility: { mcp: number[]; skill: string[] };
  wrapup_prompt?: string; // 已保存的收尾提示词(空=用内置默认)
  wrapup_default?: string; // 内置默认收尾提示词(占位/恢复默认)
  wrapup_max_turns?: number; // 已保存的收尾轮数(0=用内置默认)
  wrapup_max_turns_default?: number; // 内置默认收尾轮数(供 "0=默认N" 提示)
  // 任务级超时收尾词(仅 worker/planner，task_timeout_wrapup_supported=true 时才显示该分区)
  task_timeout_wrapup_supported?: boolean;
  task_timeout_wrapup_prompt?: string;
  task_timeout_wrapup_default?: string;
  task_timeout_wrapup_max_turns?: number;
  task_timeout_wrapup_max_turns_default?: number;
}

// ---- MCP ----
export interface MCPServer {
  id: number;
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args: string[];
  env: Record<string, string>;
  url?: string;
  enabled: boolean;
  tools?: string[]; // mcp_tools_cache (names only, for the count)
}

export interface MCPTool {
  name: string;
  description: string;
}

// ---- Skills ----
// Fields align with the agentskills.io open specification.
// description covers both "what the skill does" and "when to use it".
export interface SkillItem {
  name: string;           // unique key = directory name
  description?: string;   // required per spec; covers what + when to use
  license?: string;       // optional: SPDX identifier or free text
  compatibility?: string; // optional: environment requirements
  mcps?: string[];        // MCP server names this skill unlocks on load
  files: string[];        // files in the skill directory
}

// ---- Tools (内置工具目录) ----
// key + handler live in Go; only these fields are page-editable. system tools lock
// the key and the parameter *structure* (name/type/required) — the per-param
// description/default and the agent binding are what move.
export interface Tool {
  key: string;
  system: boolean;
  description: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  schema: Record<string, any>; // full JSON-Schema (object with properties)
  agents: string[];            // bound agent keys
  enabled: boolean;
  kind?: "builtin" | "shell" | "command" | "script" | "http"; // 自定义工具类型
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  exec?: Record<string, any>;  // 自定义工具执行规格(kind!=builtin)
  deferred?: boolean;          // schema 延迟(SearchExtraTools/ExecuteExtraTool)
}

// ---- Stats ----
export interface Stats {
  assets: number;
  engine_mode: EngineMode;
  llm_configured: boolean;
  roe_enabled: boolean;
  findings_confirmed: number;
}

// ---- Intercept Rules ----
export type InterceptAction = "allow" | "deny" | "ask";
export type InterceptMatchTarget = "tool_name" | "tool_input";
export type InterceptMatchType = "string" | "regex";

export interface InterceptRule {
  id: number;
  name: string;
  enabled: boolean;
  priority: number;
  match_target: InterceptMatchTarget;
  match_type: InterceptMatchType;
  pattern: string;
  action: InterceptAction;
  message: string;
  timeout_enabled: boolean;
  timeout_seconds: number;
  timeout_action: "deny" | "allow";
  created_at: string;
  updated_at: string;
}

export interface InterceptPending {
  id: number;
  rule_id?: number;
  conversation_id?: number;
  task_id?: string;
  agent_name: string;
  tool_name: string;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  tool_input: Record<string, any>;
  status: "pending" | "allowed" | "denied" | "timeout";
  decided_at?: string;
  created_at: string;
}

// InterceptApprovalRow enriches InterceptPending with conversation/task and rule context.
export interface InterceptApprovalRow extends InterceptPending {
  conv_title: string;      // "" if no linked conversation
  conv_agent_key: string;  // "" if no linked conversation
  rule_name: string;       // "" if rule was deleted
}

// ── 资产同步 (ScopeSentry 数据源) ──────────────────────────────────────────────
export interface SSProject {
  id: string;        // MongoDB ObjectID — used as filter.project
  name: string;
  logo?: string;
  AssetCount?: number;
  tag?: string;
}

export interface SSTask {
  id: string;
  name: string;      // used as filter.task
  status?: number;
  progress?: number;
  creatTime?: string;
  endTime?: string;
}

// ConvTokenSummary — one conversation's token total (+ profile/date) for merging
// chat usage into the dashboard token stats. GET /api/tokens/conversations.
export interface ConvTokenSummary {
  llm_profile_id: number | null;
  created_at: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
}

// ---- Command recording (Bash execution history) ----
export interface CommandRecord {
  id: number;
  exploration_id: number;
  worker: string;
  tool: string;
  command: string; // raw tool input (JSON)
  output: string;
  is_error: boolean;
  created_at: string;
}

// ---- LLM recording ----
export interface LLMRecordItem {
  id: number;
  ts: string;
  model: string;
  profile_name: string;
  session_id: string;
  task_id: string;
  worker: string;
  latency_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read: number;
  cache_write: number;
  status: string;
  error?: string;
}

export interface LLMRecordDetail extends LLMRecordItem {
  request_body: string;
  response_body: string;
}
