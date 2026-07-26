// Mock 路由：把 (method, path) 映射到 lib/mock/data 的静态数据。
// 未命中的一律返回安全默认（[] / {} / {ok:true}），保证任何页面都不崩。
// 只在 NEXT_PUBLIC_MOCK=1 时经由 api.ts 的 http() 短路进入这里。

import * as D from "./data";

const delay = (ms = 120) => new Promise((r) => setTimeout(r, ms));

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
  if (path === "/tasks" && m === "GET") return { tasks: D.tasks, active: D.ACTIVE_TASK };
  if (path === "/tasks" && m === "POST")
    return { ...D.tasks[0], id: "t-new", description: String(b.description ?? "新任务"), goal: String(b.goal ?? ""), status: "created" };
  if (seg[0] === "tasks" && seg.length === 2 && m === "DELETE") return { deleted: 1 };
  if (seg[0] === "tasks" && seg[2] === "control") return { id: seg[1], paused: b.action === "pause" };
  if (seg[0] === "tasks" && seg[2] === "chat" && seg[3] === "stop") return { status: "stopped" };
  if (path === "/active") return { active: String(b.id ?? D.ACTIVE_TASK) };

  // ── stats ──
  if (path === "/stats") return D.stats(task);

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
  if (path === "/exploration/findings") return task ? D.findings.filter((f) => f.task_id === task) : D.findings;
  if (path === "/exploration/intents") return D.intents;
  if (path === "/exploration/tokens") return { workers: D.tokenWorkers, total: D.tokenTotal };
  if (path === "/exploration/graph") return D.explorationGraph;
  if (path === "/exploration/activity" && seg.length === 2) {
    const items = D.activityForTask();
    return { items, cursor: items.length ? items[items.length - 1].seq : 0 };
  }
  if (seg[0] === "exploration" && seg[1] === "activity" && seg.length === 3)
    return { detail: "（demo）此活动的完整工具输入/输出明细。" };
  if (path === "/tokens/daily") return D.dailyTokens;
  if (path === "/tokens/conversations") return D.convTokens;

  // ── traffic / audit / settings ──
  if (path === "/audit") return D.audit;
  if (path === "/traffic") return D.traffic;
  if (path === "/traffic/exchange") return D.trafficDetail;
  if (path === "/settings" && m === "GET") return D.settings;
  if (path === "/settings" && m === "PUT") return { ...D.settings, ...b };
  if (path === "/settings/web-search/test") return { ok: true, count: 5, backend: D.settings.web_search_backend };
  if (path === "/settings/python/detect") return { python_interpreter: "/usr/bin/python3" };
  if (path === "/chat") return { reply: "（demo）我已把该建议注入为一条高优意图，work agent 会尽快执行。", mode: "hint" };
  if (path === "/gc") return { removed: 0 };

  // ── LLM ──
  if (path === "/llm" && m === "GET") return D.llmConfig;
  if (path === "/llm" && m === "POST") return { ok: true };
  if (path === "/llm/test") return { ok: true, latency_ms: 128, model: String(b.model ?? "claude-opus-4-8") };
  if (path === "/llm/profiles" && m === "GET") return { profiles: D.llmProfiles };
  if (path === "/llm/profiles" && m === "POST") return { id: Number(b.id) || 3 };
  if (path === "/llm/profiles/active") return { ok: true };
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
  if (seg[0] === "conversations" && seg[2] === "messages" && seg.length === 4)
    return { detail: "（demo）该消息的完整明细。" };
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
