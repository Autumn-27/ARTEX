// Mock 路由：把 (method, path) 映射到 lib/mock/data 的静态数据。
// 未命中的一律返回安全默认（[] / {} / {ok:true}），保证任何页面都不崩。
// 只在 NEXT_PUBLIC_MOCK=1 时经由 api.ts 的 http() 短路进入这里。

import * as D from "./data";

const delay = (ms = 120) => new Promise((r) => setTimeout(r, ms));

<<<<<<< Updated upstream
=======
// Requests mutate a runtime copy, never the exported fixtures. This keeps module
// initialization deterministic for tests/HMR while preserving state across mock calls.
const mockTasks = structuredClone(D.tasks);
const mockFindings = structuredClone(D.findings);
const mockLLMRecords = structuredClone(D.llmRecords);
const mockTaskTemplates = structuredClone(D.taskTemplates);
const mockConversations = structuredClone(D.conversations);
const mockIntents = structuredClone(D.intents);
const mockCompanies = structuredClone(D.companies);
const mockAssets = structuredClone(D.assets);
let mockActiveTask = D.ACTIVE_TASK;

function mockAssetCounts(): Record<string, number> {
  return mockAssets.reduce<Record<string, number>>((counts, asset) => {
    counts[asset.type] = (counts[asset.type] ?? 0) + 1;
    return counts;
  }, {});
}

function mockScopeRows(
  companyID: number,
  input: unknown,
  existing: ScopeRow[] = [],
): { rows: ScopeRow[]; invalid: number; skipped: number } {
  if (!Array.isArray(input)) return { rows: [], invalid: 0, skipped: 0 };
  if (input.length > MAX_COMPANY_SCOPE_RULES) throw new Error(`企业范围最多支持 ${MAX_COMPANY_SCOPE_RULES} 条`);
  let nextID =
    mockCompanies.flatMap((company) => company.scope ?? []).reduce((max, row) => Math.max(max, row.id), 0) + 1;
  const rows: ScopeRow[] = [];
  let invalid = 0;
  let skipped = 0;
  const keys = new Set(
    existing.map((row) => `${row.kind}|${row.domain ?? row.net ?? row.value ?? row.raw.trim().toLowerCase()}`),
  );
  for (const [index, candidate] of input.entries()) {
    let rule: CompanyScopeRule | undefined;
    if (typeof candidate === "string") {
      rule = classifyCompanyScopeLine(candidate, index + 1).rule;
    } else if (candidate && typeof candidate === "object") {
      const item = candidate as { kind?: unknown; value?: unknown };
      const value = String(item.value ?? "").trim();
      if (item.kind === undefined || item.kind === "") rule = classifyCompanyScopeLine(value, index + 1).rule;
      else if (isCompanyScopeKind(item.kind)) rule = { kind: item.kind, value };
    }
    if (!rule || companyScopeRuleError(rule)) {
      invalid++;
      continue;
    }
    const normalized = normalizeCompanyScopeValue(rule);
    const row: ScopeRow = { id: nextID++, company_id: companyID, kind: rule.kind, raw: rule.value.trim() };
    if (rule.kind === "domain") row.domain = normalized;
    else if (rule.kind === "ip") row.net = `${normalized}/${normalized.includes(":") ? 128 : 32}`;
    else if (rule.kind === "cidr") row.net = normalized;
    else row.value = normalized;
    const key = `${row.kind}|${row.domain ?? row.net ?? row.value}`;
    if (keys.has(key)) {
      skipped++;
      continue;
    }
    keys.add(key);
    rows.push(row);
  }
  if (existing.length + rows.length > MAX_COMPANY_SCOPE_RULES) {
    throw new Error(`企业范围规则过多：每个企业最多 ${MAX_COMPANY_SCOPE_RULES} 条`);
  }
  return { rows, invalid, skipped };
}

function mockProfileResolution(profileID: number | undefined, source: TaskLLMResolution["source"]): TaskLLMResolution {
  const profile = D.llmProfiles.find((item) => Number(item.id) === profileID);
  if (!profile) {
    return { name: "", format: "", model: "", source, available: false, reason: "LLM 配置不存在" };
  }
  return {
    profile_id: Number(profile.id),
    name: profile.name,
    format: profile.format,
    model: profile.model,
    source,
    available: Boolean(profile.api_key_hint),
    reason: profile.api_key_hint ? undefined : "LLM 配置未设置 API Key",
  };
}

// Mirrors the backend precedence in server/task_resolution.go:
// Agent 绑定 → 任务 LLM 配置链 → 全局配置 → 环境配置。
function mockRoleResolution(task: Task, agentKey: "mainagent" | "planner" | "worker"): TaskLLMResolution {
  const agent = D.agents.find((item) => item.key === agentKey);
  if (agent?.llm_profile_id) {
    const bound = mockProfileResolution(agent.llm_profile_id, "agent_binding");
    if (bound.available) return bound;
  }
  if (task.llm_profile_ids?.length) {
    if (task.active_llm_profile_id === undefined) {
      return {
        name: "",
        format: "",
        model: "",
        source: "task_chain",
        available: false,
        reason: "任务 LLM 配置链额度已耗尽",
      };
    }
    return mockProfileResolution(task.active_llm_profile_id, "task_chain");
  }
  const globalProfile = D.llmProfiles.find((item) => item.is_default);
  if (globalProfile) return mockProfileResolution(Number(globalProfile.id), "global_profile");
  return {
    name: "全局配置",
    format: D.llmConfig.provider,
    model: D.llmConfig.model,
    source: "environment",
    available: true,
  };
}

function bodyIDs(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const ids = [...new Set(value.map((item) => String(item)).filter(Boolean))];
  if (ids.length > 100) throw new Error("批量操作最多支持 100 个 ID");
  return ids;
}

type MockIntentControlResult = { ok: boolean; state?: string; error?: string };

function controlMockIntent(id: string, action: "pause" | "resume"): MockIntentControlResult {
  const intent = mockIntents.find((item) => item.id === id);
  if (!intent) return { ok: false, error: "意图不存在" };
  const requiredState = action === "pause" ? "running" : "paused";
  if (intent.inherited || intent.state !== requiredState) {
    return { ok: false, state: intent.state, error: "Worker 状态已变化" };
  }
  intent.state = action === "pause" ? "paused" : "open";
  return { ok: true, state: intent.state };
}

function controlMockTask(id: string, action: "pause" | "resume"): BatchControlItem {
  const task = mockTasks.find((item) => item.id === id);
  if (!task) return { id, ok: false, error: "任务不存在" };
  if (task.status === "done" || task.status === "failed" || task.status === "timeout") {
    return { id, ok: false, status: task.status, error: "终态任务不可控制" };
  }
  if (action === "pause") {
    if (task.paused || task.status === "paused") {
      return { id, ok: false, status: task.status, error: "任务已经暂停" };
    }
    task.paused = true;
    task.queued = false;
    task.status = "paused";
    task.engine_mode = "paused";
  } else {
    if (!task.paused && task.status !== "paused") {
      return { id, ok: false, status: task.status, error: "任务未暂停" };
    }
    task.paused = false;
    task.queued = false;
    task.status = "running";
    task.engine_mode = "exploring";
  }
  return { id, ok: true, status: task.status, queued: false };
}

function sortMockConversations() {
  mockConversations.sort((a, b) => {
    const aPinned = a.pinned_at ? 1 : 0;
    const bPinned = b.pinned_at ? 1 : 0;
    if (aPinned !== bPinned) return bPinned - aPinned;
    const aTime = a.pinned_at ?? a.updated_at;
    const bTime = b.pinned_at ?? b.updated_at;
    return bTime.localeCompare(aTime) || b.id - a.id;
  });
}

function normalizedTemplateName(value: unknown): string {
  return String(value ?? "")
    .trim()
    .split(/\s+/)
    .join(" ");
}

>>>>>>> Stashed changes
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
<<<<<<< Updated upstream
  if (path === "/tasks" && m === "GET") return { tasks: D.tasks, active: D.ACTIVE_TASK };
  if (path === "/tasks" && m === "POST")
    return { ...D.tasks[0], id: "t-new", description: String(b.description ?? "新任务"), goal: String(b.goal ?? ""), status: "created" };
  if (seg[0] === "tasks" && seg.length === 2 && m === "DELETE") return { deleted: 1 };
  if (seg[0] === "tasks" && seg[2] === "control") return { id: seg[1], paused: b.action === "pause" };
=======
  if (path === "/tasks" && m === "GET") return { tasks: mockTasks, active: mockActiveTask };
  if (path === "/tasks" && m === "POST") {
    let suffix = 1;
    while (mockTasks.some((item) => item.id === `t-new-${suffix}`)) suffix++;
    const id = `t-new-${suffix}`;
    const now = new Date();
    const profileIDs = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const sourceTaskIDs = [...((b.source_task_ids as string[] | undefined) ?? [])];
    const companyIDs = [...((b.company_ids as number[] | undefined) ?? [])];
    const created: Task = {
      id,
      name: String(b.name ?? ""),
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
      company_ids: companyIDs,
    };
    for (const item of mockTasks) item.active = false;
    mockTasks.unshift(created);
    mockActiveTask = id;
    return created;
  }
  if (path === "/task-templates" && m === "GET") return { templates: mockTaskTemplates };
  if (path === "/task-templates" && m === "POST") {
    const now = new Date().toISOString();
    const name = normalizedTemplateName(b.name);
    const description = String(b.description ?? "").trim();
    const goal = String(b.goal ?? "").trim();
    if (!name || !description || !goal) throw new Error("请填写模板名称、描述和目标");
    if (
      mockTaskTemplates.some((template) => normalizedTemplateName(template.name).toLowerCase() === name.toLowerCase())
    ) {
      throw new Error("模板名称已存在");
    }
    const nextID = mockTaskTemplates.reduce((max, template) => Math.max(max, template.id), 0) + 1;
    const created: TaskTemplate = {
      id: nextID,
      name,
      description,
      goal,
      created_at: now,
      updated_at: now,
    };
    mockTaskTemplates.unshift(created);
    return created;
  }
  if (seg[0] === "task-templates" && seg.length === 2 && m === "PATCH") {
    const template = mockTaskTemplates.find((item) => item.id === Number(seg[1]));
    if (!template) return {};
    const name = typeof b.name === "string" ? normalizedTemplateName(b.name) : template.name;
    const description = typeof b.description === "string" ? b.description.trim() : template.description;
    const goal = typeof b.goal === "string" ? b.goal.trim() : template.goal;
    if (!name || !description || !goal) throw new Error("请填写模板名称、描述和目标");
    if (
      mockTaskTemplates.some(
        (item) => item.id !== template.id && normalizedTemplateName(item.name).toLowerCase() === name.toLowerCase(),
      )
    ) {
      throw new Error("模板名称已存在");
    }
    template.name = name;
    template.description = description;
    template.goal = goal;
    template.updated_at = new Date().toISOString();
    mockTaskTemplates.sort((a, b) => b.updated_at.localeCompare(a.updated_at) || b.id - a.id);
    return template;
  }
  if (seg[0] === "task-templates" && seg.length === 2 && m === "DELETE") {
    const id = Number(seg[1]);
    const index = mockTaskTemplates.findIndex((item) => item.id === id);
    if (index >= 0) mockTaskTemplates.splice(index, 1);
    return { deleted: id };
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
    } else {
      // PostgreSQL uses ON DELETE SET NULL for retained findings. Keep the mock
      // grouped view consistent by moving them into the unassigned/deleted bucket.
      for (const finding of mockFindings) {
        if (finding.task_id !== id) continue;
        finding.task_id = undefined;
        finding.task_description = "";
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
  if (seg[0] === "tasks" && seg[2] === "llm" && seg[3] === "resolution" && m === "GET") {
    const task = mockTasks.find((item) => item.id === seg[1]);
    if (!task) throw new Error("任务不存在");
    return {
      mainagent: mockRoleResolution(task, "mainagent"),
      planner: mockRoleResolution(task, "planner"),
      worker: mockRoleResolution(task, "worker"),
    };
  }
  if (path === "/tasks/control/batch" && m === "POST") {
    const action = b.action === "resume" ? "resume" : "pause";
    return { items: bodyIDs(b.task_ids).map((id) => controlMockTask(id, action)) };
  }
  if (seg[0] === "tasks" && seg[2] === "intents" && seg[4] === "control" && m === "POST") {
    const id = seg[3];
    if (b.action === "cancel") {
      const index = mockIntents.findIndex((item) => item.id === id);
      const intent = mockIntents[index];
      if (!intent || intent.inherited || (intent.state !== "running" && intent.state !== "paused")) {
        throw new Error("Worker 状态已变化");
      }
      mockIntents.splice(index, 1);
      return {
        id: Number(id.replace(/\D/g, "")) || 0,
        state: "cancelled",
        deleted: {
          intents: 1,
          facts: 1,
          findings: 1,
          activities: D.activity.filter((item) => item.intent_id === id).length,
        },
      };
    }
    const action = b.action === "resume" ? "resume" : "pause";
    const result = controlMockIntent(id, action);
    if (!result.ok) throw new Error(result.error ?? "Worker 状态已变化");
    return { id: Number(id.replace(/\D/g, "")) || 0, state: result.state };
  }
  if (seg[0] === "tasks" && seg.length === 3 && seg[2] === "control" && m === "POST") {
    const action = b.action === "resume" ? "resume" : "pause";
    const result = controlMockTask(seg[1], action);
    if (!result.ok) throw new Error(result.error ?? "任务状态已变化");
    const task = mockTasks.find((item) => item.id === seg[1]);
    return { id: seg[1], paused: Boolean(task?.paused), queued: Boolean(task?.queued), status: task?.status ?? "" };
  }
  if (seg[0] === "tasks" && seg[2] === "chat" && seg[3] === "status") return { running: false };
>>>>>>> Stashed changes
  if (seg[0] === "tasks" && seg[2] === "chat" && seg[3] === "stop") return { status: "stopped" };
  if (path === "/active") return { active: String(b.id ?? D.ACTIVE_TASK) };

  // ── 覆盖度 / 覆盖图 / 资产关联（任务维度）──
  if (seg[0] === "tasks" && seg[2] === "coverage" && seg.length === 3) return D.coverage;
  if (seg[0] === "tasks" && seg[2] === "coverage-graph") return D.coverageGraph;
  if (seg[0] === "tasks" && seg[2] === "asset-refs") return D.assetRefsFor(Number(q.get("asset_id") ?? 0));

  // ── 工作空间文件管理器（demo：静态示例树；写/建/删走下方写兜底 {ok:true}）──
  if (path === "/workspace/list") return D.workspaceList(q.get("path") ?? "");
  if (path === "/workspace/read") return D.workspaceRead(q.get("path") ?? "");

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
  if (path === "/exploration/findings/stats") {
    const vulnclasses = Array.from(new Set(D.findings.map((f) => f.vulnclass))).sort();
    // 「按任务」下拉:有漏洞的任务 + 描述 + 条数(mock 任务 id 是字符串,直接当 id 用)。
<<<<<<< Updated upstream
    const taskMap = new Map<string, { description: string; count: number }>();
    for (const f of D.findings) {
=======
    const taskMap = new Map<string, { name: string; description: string; count: number }>();
    for (const f of mockFindings) {
>>>>>>> Stashed changes
      if (!f.task_id) continue;
      const owner = mockTasks.find((candidate) => candidate.id === f.task_id);
      const cur = taskMap.get(f.task_id) ?? { name: owner?.name ?? "", description: f.task_description ?? "", count: 0 };
      cur.count++;
      taskMap.set(f.task_id, cur);
    }
    const tasks = Array.from(taskMap, ([id, v]) => ({ id, name: v.name, description: v.description, count: v.count }));
    return {
      total: D.findings.length,
      pending: D.findings.filter((f) => f.status === "pending").length,
      critical: D.findings.filter((f) => f.severity === "critical").length,
      high: D.findings.filter((f) => f.severity === "high").length,
      medium: D.findings.filter((f) => f.severity === "medium").length,
      low: D.findings.filter((f) => f.severity === "low").length,
      vulnclasses,
      tasks,
    };
  }
<<<<<<< Updated upstream
=======
  if (path === "/exploration/findings/groups") {
    const severityOrder = { critical: 4, high: 3, medium: 2, low: 1 } as const;
    let list = mockFindings.slice();
    const filterSeverity = q.get("severity");
    const filterStatus = q.get("status");
    const filterVulnclass = q.get("vulnclass");
    const filterTask = q.get("task_id");
    if (filterSeverity) list = list.filter((finding) => finding.severity === filterSeverity);
    if (filterStatus) list = list.filter((finding) => finding.status === filterStatus);
    if (filterVulnclass) list = list.filter((finding) => finding.vulnclass === filterVulnclass);
    if (filterTask === "__unassigned__") list = list.filter((finding) => !finding.task_id);
    else if (filterTask) list = list.filter((finding) => finding.task_id === filterTask);

    const grouped = new Map<string, typeof list>();
    for (const finding of list) {
      const key = finding.task_id ?? "__unassigned__";
      grouped.set(key, [...(grouped.get(key) ?? []), finding]);
    }
    const groups = Array.from(grouped, ([key, items]) => {
      const owner = mockTasks.find((candidate) => candidate.id === key);
      return {
        task_id: key === "__unassigned__" ? null : key,
        task_name: owner?.name ?? "",
        task_description: owner?.description ?? items[0]?.task_description ?? "",
        task_status: owner?.status ?? "",
        count: items.length,
        critical: items.filter((finding) => finding.severity === "critical").length,
        high: items.filter((finding) => finding.severity === "high").length,
        medium: items.filter((finding) => finding.severity === "medium").length,
        low: items.filter((finding) => finding.severity === "low").length,
        last_found_at: items.reduce((latest, finding) => (finding.ts > latest ? finding.ts : latest), ""),
        max_severity: Math.max(...items.map((finding) => severityOrder[finding.severity])),
      };
    });
    groups.sort((left, right) =>
      q.get("sort") === "severity"
        ? right.max_severity - left.max_severity || right.last_found_at.localeCompare(left.last_found_at)
        : right.last_found_at.localeCompare(left.last_found_at),
    );
    const rawPage = Number(q.get("page") ?? 1);
    const rawPageSize = Number(q.get("limit") ?? 10);
    const page = Number.isFinite(rawPage) && rawPage > 0 ? Math.floor(rawPage) : 1;
    const pageSize = Number.isFinite(rawPageSize) && rawPageSize > 0 ? Math.min(100, Math.floor(rawPageSize)) : 10;
    return {
      items: groups.slice((page - 1) * pageSize, page * pageSize),
      total: groups.length,
      finding_total: list.length,
      page,
      page_size: pageSize,
    };
  }
  if (seg[0] === "exploration" && seg[1] === "findings" && seg[3] === "deepen" && m === "POST") {
    const finding = mockFindings.find((candidate) => candidate.id === seg[2]);
    const description = String(b.description ?? "").trim();
    if (!description) throw new Error("description is required");
    if ([...description].length > 4000) throw new Error("description must be at most 4000 characters");
    if (!finding) throw new Error("finding not found");
    if (!finding.task_id || !mockTasks.some((candidate) => candidate.id === finding.task_id)) {
      throw new Error("finding origin task or node is no longer available");
    }
    return {
      task_id: finding.task_id,
      intent_id: `mock-deepen-${Date.now()}`,
      state: "open",
      queued: false,
    };
  }
>>>>>>> Stashed changes
  // 单条 finding:GET 详情 / PATCH 改状态/严重度/名称/类别(demo 直接改内存对象)。
  if (
    seg[0] === "exploration" &&
    seg[1] === "findings" &&
    seg.length === 3 &&
    seg[2] !== "stats"
  ) {
    const f = D.findings.find((x) => x.id === seg[2]);
    if (!f) return {};
    if (m === "PATCH") {
      if (typeof b.status === "string") f.status = b.status as typeof f.status;
      if (typeof b.severity === "string")
        f.severity = b.severity as typeof f.severity;
      if (typeof b.name === "string") f.name = b.name;
      if (typeof b.vulnclass === "string") f.vulnclass = b.vulnclass;
    }
    return { ...f, finding_id: f.id };
  }
  if (path === "/exploration/findings") {
    // finding_id=id：真后端用独立表行 id 作为状态/详情句柄,mock 里用自身 id 顶上。
    // report 仅详情接口返回,列表剥掉(与后端一致)。
    const withFid = (f: (typeof D.findings)[number]) => ({
      ...f,
      report: undefined,
      finding_id: f.id,
    });
    if (task) return D.findings.filter((f) => f.task_id === task).map(withFid);
    // 全局:带 page/limit → 分页对象;否则裸数组(dashboard)。
    if (!q.has("page") && !q.has("limit")) return D.findings.map(withFid);
    const sev = { critical: 4, high: 3, medium: 2, low: 1 } as const;
    let list = D.findings.slice();
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
  if (path === "/exploration/intents") return D.intents;
  if (path === "/exploration/tokens") return { workers: D.tokenWorkers, total: D.tokenTotal };
  if (path === "/exploration/graph") return D.explorationGraph;
  if (path === "/exploration/activity" && seg.length === 2) {
    const items = D.activityForTask();
    return { items, cursor: items.length ? items[items.length - 1].seq : 0 };
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
  if (path === "/llm/records" && m === "GET") return { records: D.llmRecords, total: D.llmRecords.length };
  if (path === "/llm/records" && m === "DELETE") return { deleted: 0 };
  if (path === "/llm/records/tasks") return { tasks: D.llmTasks };
  if (seg[0] === "llm" && seg[1] === "records" && seg.length === 3 && m === "GET") return D.llmRecordDetail(Number(seg[2]));
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
  if (path === "/intercept/judge" && m === "GET")
    return {
      enabled: false,
      profile_id: 0,
      prompt: "",
      timeout_seconds: 15,
      fail_action: "allow",
      ask_timeout_seconds: 300,
      ask_timeout_action: "deny",
    };
  if (path === "/intercept/judge" && m === "PUT") return { ok: true };

  // ── 写操作兜底：成功但不落库 ──
  if (["POST", "PUT", "PATCH", "DELETE"].includes(m)) return { ok: true };

  // ── 读兜底：集合类给 []，其余 {} ──
  return /(\/(tasks|profiles|conversations|rules|history|projects|tokens|agents|servers|skills|tools|findings|intents)s?$)|s$/.test(path) ? [] : {};
}
