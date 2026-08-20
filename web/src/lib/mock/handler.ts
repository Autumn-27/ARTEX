// Mock 路由：把 (method, path) 映射到 lib/mock/data 的静态数据。
// 未命中的一律返回安全默认（[] / {} / {ok:true}），保证任何页面都不崩。
// 只在 NEXT_PUBLIC_MOCK=1 时经由 api.ts 的 http() 短路进入这里。

import type { Task } from "../types";
import * as D from "./data";

const delay = (ms = 120) => new Promise((r) => setTimeout(r, ms));

// Requests mutate a runtime copy, never the exported fixtures. This keeps module
// initialization deterministic for tests/HMR while preserving state across mock calls.
const mockTasks = structuredClone(D.tasks);
const mockFindings = structuredClone(D.findings);
const mockLLMRecords = structuredClone(D.llmRecords);
let mockActiveTask = D.ACTIVE_TASK;

function parseBody(body?: BodyInit | null): Record<string, unknown> {
  if (typeof body !== "string") return {};
  try {
    return JSON.parse(body) as Record<string, unknown>;
  } catch {
    return {};
  }
}

export async function mockHandle<T>(method: string, rawPath: string, body?: BodyInit | null): Promise<T> {
  await delay();
  const [path, qs] = rawPath.split("?");
  const q = new URLSearchParams(qs ?? "");
  const seg = path.split("/").filter(Boolean); // ["exploration","activity"]
  const m = method.toUpperCase();
  const b = parseBody(body);
  return route(m, path, seg, q, b) as T;
}

function route(m: string, path: string, seg: string[], q: URLSearchParams, b: Record<string, unknown>): unknown {
  const task = q.get("task") ?? undefined;

  // ── auth：让 demo 直接进主界面 ──
  if (path === "/auth/status") return { initialized: true };
  if (path === "/auth/login" || path === "/auth/init") return { token: "mock-demo" };
  if (path === "/auth/change-password") return { ok: true };

  // ── tasks ──
  if (path === "/tasks" && m === "GET") return { tasks: mockTasks, active: mockActiveTask };
  if (path === "/tasks" && m === "POST") {
    let suffix = 1;
    while (mockTasks.some((item) => item.id === `t-new-${suffix}`)) suffix++;
    const id = `t-new-${suffix}`;
    const now = new Date();
    const profileIDs = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const sourceTaskIDs = [...((b.source_task_ids as string[] | undefined) ?? [])];
    const created: Task = {
      id,
      description: String(b.description ?? "新任务"),
      goal: String(b.goal ?? ""),
      status: "created",
      created_at: now.toISOString(),
      created_unix: Math.floor(now.getTime() / 1000),
      paused: false,
      active: true,
      in_flight: 0,
      stalled: false,
      goals_total: 0,
      goals_met: 0,
      engine_mode: "idle",
      tokens: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
      llm_profile_id: profileIDs[0],
      llm_profile_ids: profileIDs,
      active_llm_profile_id: profileIDs[0],
      llm_failover_state: profileIDs.length ? "ready" : "default",
      source_task_ids: sourceTaskIDs,
    };
    for (const item of mockTasks) item.active = false;
    mockTasks.unshift(created);
    mockActiveTask = id;
    return created;
  }
  if (seg[0] === "tasks" && seg.length === 2 && m === "DELETE") {
    const id = seg[1];
    const index = mockTasks.findIndex((item) => item.id === id);
    if (index >= 0) mockTasks.splice(index, 1);

    let findingsDeleted = 0;
    if (b.delete_findings) {
      for (let i = mockFindings.length - 1; i >= 0; i--) {
        if (mockFindings[i].task_id !== id) continue;
        mockFindings.splice(i, 1);
        findingsDeleted++;
      }
    }

    let llmRecordsDeleted = 0;
    if (b.delete_llm_records) {
      for (let i = mockLLMRecords.length - 1; i >= 0; i--) {
        if (mockLLMRecords[i].task_id !== id) continue;
        mockLLMRecords.splice(i, 1);
        llmRecordsDeleted++;
      }
    }

    if (mockActiveTask === id) {
      const nextActive = mockTasks[0]?.id ?? "";
      mockActiveTask = nextActive;
      for (const item of mockTasks) item.active = item.id === nextActive;
    }
    return {
      deleted: id,
      assets_deleted: b.delete_assets ? 1 : 0,
      assets_detached: 0,
      traffic_deleted: b.delete_traffic ? 1 : 0,
      files_deleted: Boolean(b.delete_files),
      findings_deleted: findingsDeleted,
      llm_records_deleted: llmRecordsDeleted,
    };
  }
  if (seg[0] === "tasks" && seg[2] === "llm" && m === "PUT") {
    const ids = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const activeID = typeof b.active_llm_profile_id === "number" ? b.active_llm_profile_id : ids[0];
    const target = mockTasks.find((item) => item.id === seg[1]);
    if (target) {
      target.llm_profile_ids = ids;
      target.llm_profile_id = ids[0];
      target.active_llm_profile_id = activeID;
      target.llm_failover_state = ids.length ? "ready" : "default";
      target.llm_failover_reason = undefined;
    }
    return {
      id: seg[1],
      llm_profile_ids: ids,
      active_llm_profile_id: activeID,
      llm_failover_state: ids.length ? "ready" : "default",
      reopened_intents: 0,
    };
  }
  if (seg[0] === "tasks" && seg[2] === "control") return { id: seg[1], paused: b.action === "pause" };
  if (seg[0] === "tasks" && seg[2] === "chat" && seg[3] === "stop") return { status: "stopped" };
  if (path === "/active") {
    const id = String(b.id ?? mockActiveTask);
    if (mockTasks.some((item) => item.id === id)) {
      mockActiveTask = id;
      for (const item of mockTasks) item.active = item.id === id;
    }
    return { active: mockActiveTask };
  }

  // ── 覆盖度 / 覆盖图 / 资产关联（任务维度）──
  if (seg[0] === "tasks" && seg[2] === "coverage" && seg.length === 3) return D.coverage;
  if (seg[0] === "tasks" && seg[2] === "coverage-graph") return D.coverageGraph;
  if (seg[0] === "tasks" && seg[2] === "asset-refs") return D.assetRefsFor(Number(q.get("asset_id") ?? 0));

  // ── 任务测试范围（增删查）──
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 3 && m === "GET") return { scope: [] };
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 3 && m === "POST")
    return { id: Date.now(), task_id: Number(seg[1]), kind: b.kind, domain: b.value, source: "manual" };
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 4 && m === "DELETE") return { ok: true };

  // ── 工作空间文件管理器（demo：静态示例树；写/建/删走下方写兜底 {ok:true}）──
  if (path === "/workspace/list") return D.workspaceList(q.get("path") ?? "");
  if (path === "/workspace/read") return D.workspaceRead(q.get("path") ?? "");

  // ── stats ──
  if (path === "/stats") {
    return D.stats(task, { tasks: mockTasks, findings: mockFindings, activeTask: mockActiveTask });
  }

  // ── assets ──
  if (path === "/assets/counts") return D.assetCounts;
  if (path === "/assets" && m === "GET") {
    const type = q.get("type") ?? "";
    const list = type ? D.assets.filter((a) => a.type === type) : D.assets;
    const limit = Number(q.get("limit") ?? 50);
    const offset = Number(q.get("offset") ?? 0);
    return { count: list.length, total: list.length, assets: list.slice(offset, offset + limit) };
  }
  if (path === "/assets" && m === "DELETE") return { deleted: (b.ids as unknown[])?.length ?? 0 };

  // ── companies ──
  if (path === "/companies" && m === "GET") return D.companies;
  if (path === "/companies" && m === "POST") return { id: 2, created: true, scope_added: 0 };
  if (seg[0] === "companies" && seg[2] === "scope") return { added: 0, skipped: 0, invalid: 0 };
  if (seg[0] === "companies" && seg.length === 2 && m === "DELETE") return { deleted: 1, assets_deleted: 0 };

  // ── exploration ──
  if (path === "/exploration/frontier") return D.frontier;
  if (path === "/exploration/findings/stats") {
    const vulnclasses = Array.from(new Set(mockFindings.map((f) => f.vulnclass))).sort();
    // 「按任务」下拉:有漏洞的任务 + 描述 + 条数(mock 任务 id 是字符串,直接当 id 用)。
    const taskMap = new Map<string, { description: string; count: number }>();
    for (const f of mockFindings) {
      if (!f.task_id) continue;
      const cur = taskMap.get(f.task_id) ?? { description: f.task_description ?? "", count: 0 };
      cur.count++;
      taskMap.set(f.task_id, cur);
    }
    const tasks = Array.from(taskMap, ([id, v]) => ({ id, description: v.description, count: v.count }));
    return {
      total: mockFindings.length,
      pending: mockFindings.filter((f) => f.status === "pending").length,
      critical: mockFindings.filter((f) => f.severity === "critical").length,
      high: mockFindings.filter((f) => f.severity === "high").length,
      medium: mockFindings.filter((f) => f.severity === "medium").length,
      low: mockFindings.filter((f) => f.severity === "low").length,
      vulnclasses,
      tasks,
    };
  }
  // 单条 finding:GET 详情 / PATCH 改状态/严重度/名称/类别(demo 直接改内存对象)。
  if (
    seg[0] === "exploration" &&
    seg[1] === "findings" &&
    seg.length === 3 &&
    seg[2] !== "stats"
  ) {
    const f = mockFindings.find((x) => x.id === seg[2]);
    if (!f) return {};
    if (m === "PATCH") {
      if (typeof b.status === "string") f.status = b.status as typeof f.status;
      if (typeof b.severity === "string")
        f.severity = b.severity as typeof f.severity;
      if (typeof b.name === "string") f.name = b.name;
      if (typeof b.vulnclass === "string") f.vulnclass = b.vulnclass;
    }
    const contextTaskId = q.get("context_task");
    const contextTask = contextTaskId ? mockTasks.find((item) => item.id === contextTaskId) : undefined;
    const inherited = !!(
      contextTask &&
      f.task_id &&
      f.task_id !== contextTask.id &&
      contextTask.source_task_ids?.includes(f.task_id)
    );
    return {
      ...f,
      finding_id: f.id,
      ...(inherited ? { inherited: true, source_task_id: f.task_id } : {}),
    };
  }
  if (path === "/exploration/findings") {
    // finding_id=id：真后端用独立表行 id 作为状态/详情句柄,mock 里用自身 id 顶上。
    // report 仅详情接口返回,列表剥掉(与后端一致)。
    const withFid = (f: (typeof mockFindings)[number]) => ({
      ...f,
      report: undefined,
      finding_id: f.id,
    });
    if (task) {
      const owner = mockTasks.find((item) => item.id === task);
      const sources = new Set(owner?.source_task_ids ?? []);
      return mockFindings
        .filter((f) => f.task_id === task || (!!f.task_id && sources.has(f.task_id)))
        .map((f) => ({
          ...withFid(f),
          ...(f.task_id !== task ? { inherited: true, source_task_id: f.task_id } : {}),
        }));
    }
    // 全局:带 page/limit → 分页对象;否则裸数组(dashboard)。
    if (!q.has("page") && !q.has("limit")) return mockFindings.map(withFid);
    const sev = { critical: 4, high: 3, medium: 2, low: 1 } as const;
    let list = mockFindings.slice();
    const fSev = q.get("severity");
    const fStatus = q.get("status");
    const fVuln = q.get("vulnclass");
    const fTask = q.get("task_id");
    if (fSev) list = list.filter((f) => f.severity === fSev);
    if (fStatus) list = list.filter((f) => f.status === fStatus);
    if (fVuln) list = list.filter((f) => f.vulnclass === fVuln);
    if (fTask) list = list.filter((f) => f.task_id === fTask);
    list.sort((a, b) =>
      q.get("sort") === "severity"
        ? sev[b.severity] - sev[a.severity] || +new Date(b.ts) - +new Date(a.ts)
        : +new Date(b.ts) - +new Date(a.ts),
    );
    const page = Number(q.get("page") ?? 1);
    const pageSize = Number(q.get("limit") ?? 20);
    return {
      items: list.slice((page - 1) * pageSize, page * pageSize).map(withFid),
      total: list.length,
      page,
      page_size: pageSize,
    };
  }
  if (path === "/exploration/intents") {
    if (q.has("page")) {
      const before = Number(q.get("before") ?? 0);
      const limit = Math.max(1, Number(q.get("limit") ?? 300));
      let list = D.intents;
      if (before > 0) list = list.filter((intent) => Number(intent.id.replace(/\D/g, "") || intent.id) < before);
      return { items: list.slice(0, limit), has_more: list.length > limit };
    }
    return D.intents;
  }
  if (path === "/exploration/tokens") return { workers: D.tokenWorkers, total: D.tokenTotal };
  if (path === "/exploration/graph") return D.explorationGraph;
  if (path === "/exploration/activity" && seg.length === 2) {
    const since = Number(q.get("since") ?? 0);
    const limit = Math.max(1, Number(q.get("limit") ?? 300));
    const items = D.activityForTask().filter((item) => item.seq > since).slice(0, limit);
    return { items, cursor: items.length ? items[items.length - 1].seq : since };
  }
  if (seg[0] === "exploration" && seg[1] === "activity" && seg.length === 3) {
    const a = D.activity.find((x) => x.seq === Number(seg[2]));
    return { detail: a?.detail ?? a?.summary ?? "" };
  }
  if (path === "/tokens/daily") return D.dailyTokens;
  if (path === "/tokens/conversations") return D.convTokens;

  // ── traffic / audit / settings ──
  if (path === "/audit") return D.audit;
  if (path === "/traffic" && m === "DELETE") return { deleted: 0 };
  if (path === "/traffic/hosts" && m === "DELETE") return { deleted: (b.hosts as unknown[])?.length ?? 0 };
  if (path === "/traffic/hosts") return { hosts: D.trafficHosts };
  if (path === "/traffic") return D.traffic;
  if (path === "/traffic/exchange") return D.trafficDetail;
  if (path === "/settings" && m === "GET") return D.settings;
  if (path === "/settings" && m === "PUT") return { ...D.settings, ...b };
  if (path === "/settings/web-search/test") return { ok: true, count: 5, backend: D.settings.web_search_backend };
  if (path === "/settings/python/detect") return { python_interpreter: "/usr/bin/python3" };
  if (path === "/chat") return { reply: "（demo）我已把该建议注入为一条高优意图，work agent 会尽快执行。", mode: "hint" };
  if (path === "/gc") return { removed: 0 };

  // ── 工具执行历史 ──
  if (path === "/commands" && m === "GET") return { commands: D.commandRecords, total: D.commandRecords.length };

  // ── LLM ──
  if (path === "/llm/records" && m === "GET") return { records: mockLLMRecords, total: mockLLMRecords.length };
  if (path === "/llm/records" && m === "DELETE") return { deleted: 0 };
  if (path === "/llm/records/tasks") {
    const counts = new Map<string, number>();
    for (const record of mockLLMRecords) {
      if (record.task_id) counts.set(record.task_id, (counts.get(record.task_id) ?? 0) + 1);
    }
    return { tasks: [...counts].map(([task_id, count]) => ({ task_id, count })) };
  }
  if (seg[0] === "llm" && seg[1] === "records" && seg.length === 3 && m === "GET") {
    return D.llmRecordDetail(Number(seg[2]), mockLLMRecords);
  }
  if (path === "/llm" && m === "GET") return D.llmConfig;
  if (path === "/llm" && m === "POST") return { ok: true };
  if (path === "/llm/test") return { ok: true, latency_ms: 128, model: String(b.model ?? "claude-opus-4-8") };
  if (path === "/llm/profiles" && m === "GET") return { profiles: D.llmProfiles };
  if (path === "/llm/profiles" && m === "POST") return { id: Number(b.id) || 3 };
  if (path === "/llm/profiles/active") return { ok: true };
  if (path === "/llm/pool" && m === "GET") return D.llmPool;
  if (path === "/llm/pool/reset")
    return { ...D.llmPool, chain: D.llmPool.chain.map((c) => ({ ...c, state: "ok", fails: 0, cooldown_secs: 0 })) };
  if (seg[0] === "llm" && seg[1] === "profiles" && seg.length === 3 && m === "DELETE") return { deleted: Number(seg[2]) };

  // ── agents ──
  if (path === "/agents" && m === "GET") return { agents: D.agents };
  if (path === "/agents" && m === "POST") return { id: "9", key: String(b.key ?? "custom"), name: String(b.name ?? ""), role: "custom", builtin: false, enabled: true };
  if (seg[0] === "agents" && seg.length === 2 && m === "GET") return D.agentDetail(seg[1]);
  if (seg[0] === "agents" && seg[2] === "triggers" && m === "GET") return { triggers: [] };
  if (seg[0] === "agents" && seg[2] === "prompts") return { versions: D.agentDetail(seg[1]).versions };
  if (seg[0] === "agents" && seg[2] === "variables") return { variables: D.agentDetail(seg[1]).variables };
  if (seg[0] === "agents" && seg[2] === "prompt" && seg[3] === "preview")
    return { rendered: String(b.template ?? "").replace(/\{\{\.(\w+)\}\}/g, "«$1»") };
  if (seg[0] === "agents" && seg[2] === "visibility" && m === "GET") return D.agentDetail(seg[1]).visibility;

  // ── conversations ──
  if (path === "/conversations" && m === "GET") return { conversations: D.conversations };
  if (path === "/conversations" && m === "POST") return { id: 3, agent_key: String(b.agent_key ?? "mainagent"), title: String(b.title ?? "新会话"), created_at: "2026-07-26T04:00:00Z", updated_at: "2026-07-26T04:00:00Z" };
  if (seg[0] === "conversations" && seg[2] === "messages" && seg.length === 3 && m === "GET") {
    const items = D.conversationMessages[Number(seg[1])] ?? [];
    return { items, cursor: items.length ? items[items.length - 1].seq : 0, running: false };
  }
  if (seg[0] === "conversations" && seg[2] === "messages" && seg.length === 4) {
    const msgs = D.conversationMessages[Number(seg[1])] ?? [];
    const a = msgs.find((x) => x.seq === Number(seg[3]));
    return { detail: a?.detail ?? a?.summary ?? "" };
  }
  if (seg[0] === "conversations" && seg[2] === "messages" && m === "POST") return { status: "ok" };
  if (seg[0] === "conversations" && seg[2] === "stop") return { status: "stopped" };

  // ── tools ──
  if (path === "/tools" && m === "GET") return { tools: D.tools };
  if (path === "/tools/custom" && m === "POST") return { key: String(b.key ?? "custom-tool") };
  if (path === "/tools/custom/test") return { output: "（demo）工具执行输出示例。", is_error: false };

  // ── mcp ──
  if (path === "/mcp" && m === "GET") return { servers: D.mcpServers };
  if (path === "/mcp" && m === "POST") return { id: 3 };
  if (seg[0] === "mcp" && seg[2] === "tools") return { tools: D.mcpToolsById[Number(seg[1])] ?? [] };
  if (seg[0] === "mcp" && seg[2] === "refresh") return { tools: D.mcpToolsById[Number(seg[1])] ?? [] };
  if (seg[0] === "mcp" && seg.length === 2 && m === "DELETE") return { deleted: Number(seg[1]) };

  // ── scopesentry（demo：未配置）──
  if (path === "/sync/scopesentry/status") return { exists: false, configured: false, enabled: false, reachable: false, tools: [] };
  if (path === "/sync/scopesentry/projects") return { projects: [], tag: {} };
  if (path === "/sync/scopesentry/tasks") return { tasks: [] };
  if (path === "/sync/scopesentry/sync") return { synced: {}, companies: null, warnings: null, errors: null };

  // ── skills ──
  if (path === "/skills" && m === "GET") return { skills: D.skills };
  if (path === "/skills" && m === "POST") return { name: String(b.name ?? "new-skill") };
  if (seg[0] === "skills" && seg[2] === "files" && seg.length === 3) return { files: ["SKILL.md"] };
  if (seg[0] === "skills" && seg[2] === "files" && seg.length >= 4)
    return { content: "# SKILL.md\n\n（demo）这是该 skill 的说明文件示例。", file: seg.slice(3).join("/") };

  // ── visibility ──
  if (seg[0] === "visibility" && m === "GET") return { agents: [] };

  // ── intercept ──
  if (path === "/intercept/rules" && m === "GET") return { rules: D.interceptRules };
  if (seg[0] === "intercept" && seg[1] === "rules" && seg[3] === "toggle") return { ok: true, enabled: b.enabled ?? true };
  if (path === "/intercept/pending" && m === "GET") return { pending: D.interceptPending };
  if (seg[0] === "intercept" && seg[1] === "pending" && seg[3] === "decide") return { ok: true };
  if (seg[0] === "intercept" && seg[1] === "pending" && seg.length === 3 && m === "GET")
    return D.interceptPending.find((p) => p.id === Number(seg[2])) ?? null;
  if (path === "/intercept/history") return { items: D.interceptHistory };
  if (seg[0] === "intercept" && seg[1] === "task")
    return { items: D.interceptHistory.filter((r) => r.task_id === seg[2]) };
  if (path === "/intercept/tool-config") return { enabled_tools: ["bash"] };

  // ── 写操作兜底：成功但不落库 ──
  if (["POST", "PUT", "PATCH", "DELETE"].includes(m)) return { ok: true };

  // ── 读兜底：集合类给 []，其余 {} ──
  return /(\/(tasks|profiles|conversations|rules|history|projects|tokens|agents|servers|skills|tools|findings|intents)s?$)|s$/.test(path) ? [] : {};
}
