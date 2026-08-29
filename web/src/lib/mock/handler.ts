// Mock 路由：把 (method, path) 映射到 lib/mock/data 的静态数据。
// 未命中的一律返回安全默认（[] / {} / {ok:true}），保证任何页面都不崩。
// 只在 NEXT_PUBLIC_MOCK=1 时经由 api.ts 的 http() 短路进入这里。

import {
  classifyCompanyScopeLine,
  companyScopeRuleError,
  isCompanyScopeKind,
  MAX_COMPANY_SCOPE_RULES,
  normalizeCompanyScopeValue,
} from "../company-scope";
import type {
  Activity,
  ArchiveBatchItem,
  Asset,
  BatchControlItem,
  Company,
  CompanyScopeRule,
  Conversation,
  IntentAsset,
  ScopeRow,
  Task,
  TaskArchive,
  TaskAssetMutation,
  TaskAssetScopeMutation,
  TaskCategory,
  TaskLLMResolution,
  TaskScopeRow,
  TaskTemplate,
} from "../types";
import * as D from "./data";

const delay = (ms = 120) => new Promise((r) => setTimeout(r, ms));

// Requests mutate a runtime copy, never the exported fixtures. This keeps module
// initialization deterministic for tests/HMR while preserving state across mock calls.
const mockTasks = structuredClone(D.tasks);
const mockFindings = structuredClone(D.findings);
const mockLLMRecords = structuredClone(D.llmRecords);
const mockTaskTemplates = structuredClone(D.taskTemplates);
const mockTaskCategories = structuredClone(D.taskCategories);
const mockConversations = structuredClone(D.conversations);
const mockIntents = structuredClone(D.intents);
const mockCompanies = structuredClone(D.companies);
const mockAssets = structuredClone(D.assets);
const mockActivity = structuredClone(D.activity);
type MockTaskArchiveSnapshot = {
  task: Task;
  numericTaskID: number;
  assetIDs: number[];
  assetSources: Array<[number, MockTaskAssetSource]>;
};
type MockTaskArchive = TaskArchive & { snapshot: MockTaskArchiveSnapshot };
const mockTaskArchives: MockTaskArchive[] = [];
let nextMockTaskArchiveID = 1;
const mockTaskAssetIDs = new Map(D.tasks.map((task, index) => [task.id, index + 1]));
const mockTaskScopes = new Map<string, TaskScopeRow[]>();
type MockTaskAssetSource = Pick<Asset, "task_source" | "task_source_summary" | "task_source_node_id">;
const mockTaskAssetSources = new Map<string, MockTaskAssetSource>();
let nextMockTaskAssetID = D.tasks.length + 1;
let mockActiveTask = D.ACTIVE_TASK;

function mockTaskAssetSourceKey(taskID: string, assetID: number): string {
  return JSON.stringify([taskID, assetID]);
}

function publicMockTaskArchive(archive: MockTaskArchive): TaskArchive {
  const { snapshot: _snapshot, ...item } = archive;
  return structuredClone(item);
}

function mockArchiveTaskID(taskID: string): number {
  const numeric = Number(taskID);
  if (Number.isSafeInteger(numeric) && numeric > 0) return numeric;
  return mockTaskAssetID(taskID) ?? 0;
}

function mockTaskArchiveBlocker(taskID: string): string | undefined {
  return mockTasks.find(
    (candidate) =>
      candidate.id !== taskID &&
      (candidate.source_task_ids ?? []).map(String).includes(taskID) &&
      !mockTaskArchives.some(
        (archive) =>
          archive.snapshot.task.id === candidate.id &&
          (archive.state === "archive_queued" || archive.state === "archiving"),
      ),
  )?.id;
}

function publicMockTask(task: Task): Task {
  const item = structuredClone(task);
  const blocker = mockTaskArchiveBlocker(task.id);
  if (blocker) item.archive_blocked_by_task_id = blocker;
  else delete item.archive_blocked_by_task_id;
  return item;
}

function mockArchiveTask(taskID: string): MockTaskArchive {
  const task = mockTasks.find((item) => item.id === taskID);
  if (!task) throw new Error("任务不存在");
  if (!["paused", "done", "failed", "timeout"].includes(task.status) && !task.paused) {
    throw new Error(task.queued ? "排队中的任务必须先暂停" : "运行中的任务必须先暂停");
  }
  const existing = mockTaskArchives.find((item) => item.task_id === mockArchiveTaskID(taskID));
  if (existing) throw new Error("任务已经在归档队列中");
  const dependent = mockTasks.find(
    (candidate) =>
      candidate.id !== taskID &&
      (candidate.source_task_ids ?? []).map(String).includes(taskID) &&
      !mockTaskArchives.some(
        (archive) =>
          archive.snapshot.task.id === candidate.id &&
          (archive.state === "archive_queued" || archive.state === "archiving"),
      ),
  );
  if (dependent) throw new Error(`任务被未归档任务 #${dependent.id} 直接继承，暂不能归档`);

  const numericTaskID = mockArchiveTaskID(taskID);
  const assetIDs = mockAssets.filter((asset) => asset.task_ids.includes(numericTaskID)).map((asset) => asset.id);
  const assetSources: Array<[number, MockTaskAssetSource]> = [];
  for (const assetID of assetIDs) {
    const source = mockTaskAssetSources.get(mockTaskAssetSourceKey(taskID, assetID));
    if (source) assetSources.push([assetID, structuredClone(source)]);
  }
  const findings = mockFindings.filter((finding) => finding.task_id === taskID);
  const llmRecords = mockLLMRecords.filter((record) => record.task_id === taskID);
  const tokens = task.tokens ?? { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 };
  const now = new Date().toISOString();
  const archive: MockTaskArchive = {
    id: nextMockTaskArchiveID++,
    task_id: numericTaskID,
    state: "archive_queued",
    phase: "等待归档",
    progress: 0,
    format_version: 1,
    original_size: 0,
    compressed_size: 0,
    task_name: task.name ?? "",
    task_description: task.description,
    task_goal: task.goal,
    original_status: task.status,
    category_id: task.category_id,
    category_name: task.category_name,
    source_task_ids: (task.source_task_ids ?? []).map(mockArchiveTaskID).filter((id) => id > 0),
    remaining_timeout_seconds: 0,
    data_counts: {
      assets: assetIDs.length,
      findings: findings.length,
      llm_records: llmRecords.length,
    },
    aggregate_stats: {
      tokens: {
        calls: llmRecords.length,
        input_tokens: tokens.input_tokens,
        output_tokens: tokens.output_tokens,
        cache_read_tokens: tokens.cache_read_tokens,
        cache_write_tokens: tokens.cache_write_tokens,
      },
      findings: findings.reduce<Record<string, number>>((counts, finding) => {
        counts[finding.severity] = (counts[finding.severity] ?? 0) + 1;
        return counts;
      }, {}),
    },
    requested_at: now,
    created_at: now,
    updated_at: now,
    snapshot: { task: structuredClone(task), numericTaskID, assetIDs, assetSources },
  };
  mockTaskArchives.unshift(archive);
  setTimeout(() => {
    if (archive.state !== "archive_queued") return;
    archive.state = "archiving";
    archive.phase = "压缩任务数据";
    archive.progress = 55;
    archive.updated_at = new Date().toISOString();
  }, 100);
  setTimeout(() => {
    if (archive.state !== "archiving" && archive.state !== "archive_queued") return;
    archive.state = "ready";
    archive.phase = "归档完成";
    archive.progress = 100;
    archive.archived_at = new Date().toISOString();
    archive.updated_at = archive.archived_at;
    archive.original_size = Math.max(4096, JSON.stringify(archive.snapshot).length * 4);
    archive.compressed_size = Math.max(1024, Math.round(archive.original_size * 0.32));
    const index = mockTasks.findIndex((item) => item.id === taskID);
    if (index >= 0) mockTasks.splice(index, 1);
    for (const asset of mockAssets) asset.task_ids = asset.task_ids.filter((id) => id !== numericTaskID);
    deleteMockTaskAssetSources(taskID);
    if (mockActiveTask === taskID) {
      mockActiveTask = mockTasks[0]?.id ?? "";
      for (const item of mockTasks) item.active = item.id === mockActiveTask;
    }
  }, 500);
  return archive;
}

function mockRestoreArchive(archive: MockTaskArchive): void {
  if (archive.state !== "ready" && archive.state !== "restore_failed") throw new Error("当前归档状态不可还原");
  archive.state = "restore_queued";
  archive.phase = "等待还原";
  archive.progress = 0;
  archive.error = undefined;
  archive.updated_at = new Date().toISOString();
  setTimeout(() => {
    if (archive.state !== "restore_queued") return;
    archive.state = "restoring";
    archive.phase = "恢复任务数据";
    archive.progress = 60;
    archive.updated_at = new Date().toISOString();
  }, 100);
  setTimeout(() => {
    if (archive.state !== "restoring" && archive.state !== "restore_queued") return;
    const restored = structuredClone(archive.snapshot.task);
    restored.active = false;
    restored.queued = false;
    if (!mockTasks.some((task) => task.id === restored.id)) mockTasks.push(restored);
    mockTaskAssetIDs.set(restored.id, archive.snapshot.numericTaskID);
    for (const assetID of archive.snapshot.assetIDs) {
      const asset = mockAssets.find((candidate) => candidate.id === assetID);
      if (asset && !asset.task_ids.includes(archive.snapshot.numericTaskID)) {
        asset.task_ids.push(archive.snapshot.numericTaskID);
      }
    }
    for (const [assetID, source] of archive.snapshot.assetSources) setMockTaskAssetSource(restored.id, assetID, source);
    const index = mockTaskArchives.indexOf(archive);
    if (index >= 0) mockTaskArchives.splice(index, 1);
    sortMockTasks();
  }, 500);
}

function mockDeleteArchive(archive: MockTaskArchive): void {
  if (archive.state !== "ready" && archive.state !== "delete_failed") throw new Error("当前归档状态不可永久删除");
  const dependent = mockTaskArchives.find(
    (candidate) => candidate.id !== archive.id && candidate.source_task_ids.includes(archive.task_id),
  );
  if (dependent) throw new Error(`归档仍被任务 #${dependent.task_id} 依赖，无法永久删除`);
  archive.state = "delete_queued";
  archive.phase = "等待永久删除";
  archive.progress = 0;
  archive.error = undefined;
  archive.updated_at = new Date().toISOString();
  setTimeout(() => {
    if (archive.state !== "delete_queued") return;
    archive.state = "deleting";
    archive.phase = "删除归档包";
    archive.progress = 70;
  }, 100);
  setTimeout(() => {
    const index = mockTaskArchives.indexOf(archive);
    if (index >= 0) mockTaskArchives.splice(index, 1);
  }, 450);
}

function setMockTaskAssetSource(taskID: string, assetID: number, source: MockTaskAssetSource) {
  mockTaskAssetSources.set(mockTaskAssetSourceKey(taskID, assetID), source);
}

function mockAssetForTask(taskID: string, asset: Asset): Asset {
  const source = mockTaskAssetSources.get(mockTaskAssetSourceKey(taskID, asset.id));
  return source ? { ...asset, ...source } : asset;
}

function deleteMockTaskAssetSources(taskID: string, assetID?: number) {
  if (assetID !== undefined) {
    mockTaskAssetSources.delete(mockTaskAssetSourceKey(taskID, assetID));
    return;
  }
  for (const key of mockTaskAssetSources.keys()) {
    const [linkedTaskID] = JSON.parse(key) as [string, number];
    if (linkedTaskID === taskID) mockTaskAssetSources.delete(key);
  }
}

function mockTaskAssetID(taskID: string): number | undefined {
  const numeric = Number(taskID);
  if (Number.isInteger(numeric) && numeric > 0) return numeric;
  return mockTaskAssetIDs.get(taskID);
}

function mockAssetCounts(taskID?: string | null): Record<string, number> {
  const numericTaskID = taskID ? mockTaskAssetID(taskID) : undefined;
  return mockAssets.reduce<Record<string, number>>((counts, asset) => {
    if (taskID && (numericTaskID === undefined || !asset.task_ids.includes(numericTaskID))) return counts;
    counts[asset.type] = (counts[asset.type] ?? 0) + 1;
    return counts;
  }, {});
}

function mockAssetMatchesDSL(asset: Asset, dsl: string): boolean {
  const query = dsl
    .replaceAll(/[()"]/g, " ")
    .replaceAll(/\b(?:AND|OR)\b/gi, " ")
    .replaceAll(/\b[a-z_][a-z0-9_]*(?:==|!=|>=|<=|=|>|<)/gi, " ")
    .trim()
    .toLowerCase();
  if (!query) return true;
  const haystack = JSON.stringify(asset).toLowerCase();
  return query.split(/\s+/).every((term) => haystack.includes(term));
}

function mockFindingMatchesQuery(finding: (typeof mockFindings)[number], query: string | null): boolean {
  const needle = query?.trim().toLowerCase();
  if (!needle) return true;
  return [finding.name, finding.vulnclass, finding.summary, finding.evidence, finding.report].some((value) =>
    String(value ?? "")
      .toLowerCase()
      .includes(needle),
  );
}

function mockTaskCategorySnapshot(): TaskCategory[] {
  return mockTaskCategories.map((category) => ({
    ...category,
    task_count: mockTasks.filter((task) => task.category_id === category.id).length,
  }));
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
type MockWorkerMessage = {
  intentId: string;
  message: string;
  state: "open" | "running";
  activitySeq: number;
};

const mockWorkerMessages = new Map<string, MockWorkerMessage>();
let nextMockWorkerMessageActivitySeq = mockActivity.reduce((maximum, item) => Math.max(maximum, item.seq), 0) + 1;

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

function sendMockWorkerMessage(
  id: string,
  message: string,
  requestId: string,
): MockIntentControlResult & { activitySeq?: number; requestId?: string } {
  const normalizedMessage = message.trim();
  const normalizedRequestId = requestId.trim();
  if (!normalizedRequestId) return { ok: false, error: "request_id 不能为空" };
  const previous = mockWorkerMessages.get(normalizedRequestId);
  if (previous) {
    if (previous.intentId !== id || previous.message !== normalizedMessage) {
      return { ok: false, error: "request_id 已用于其他 Worker 消息" };
    }
    return {
      ok: true,
      state: previous.state,
      activitySeq: previous.activitySeq,
      requestId: normalizedRequestId,
    };
  }
  const intent = mockIntents.find((item) => item.id === id);
  if (!intent) return { ok: false, error: "意图不存在" };
  if (intent.inherited || (intent.state !== "running" && intent.state !== "paused")) {
    return { ok: false, state: intent.state, error: "Worker 状态已变化" };
  }
  if (!normalizedMessage) return { ok: false, state: intent.state, error: "消息不能为空" };
  if (Array.from(normalizedMessage).length > 4000) {
    return { ok: false, state: intent.state, error: "消息不能超过 4000 个字符" };
  }

  // The real endpoint stops the active turn, persists a normal user message,
  // and immediately reopens the same intent. Keep the mock's transition in sync,
  // then emulate a Worker reclaiming it shortly afterward.
  intent.state = "open";
  const activitySeq = nextMockWorkerMessageActivitySeq++;
  const workerMessage: MockWorkerMessage = {
    intentId: id,
    message: normalizedMessage,
    state: "open",
    activitySeq,
  };
  mockWorkerMessages.set(normalizedRequestId, workerMessage);
  mockActivity.push({
    seq: activitySeq,
    intent_id: id,
    worker: "user",
    ts: new Date().toISOString(),
    kind: "user",
    summary: normalizedMessage,
    detail: normalizedMessage,
  } satisfies Activity);
  setTimeout(() => {
    if (intent.state === "open") {
      intent.state = "running";
      workerMessage.state = "running";
    }
  }, 250);
  return {
    ok: true,
    state: workerMessage.state,
    activitySeq: workerMessage.activitySeq,
    requestId: normalizedRequestId,
  };
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

function sortMockTasks() {
  mockTasks.sort((a, b) => {
    const aPinned = a.pinned_at ? 1 : 0;
    const bPinned = b.pinned_at ? 1 : 0;
    if (aPinned !== bPinned) return bPinned - aPinned;
    if (a.pinned_at !== b.pinned_at) return (b.pinned_at ?? "").localeCompare(a.pinned_at ?? "");
    return b.id.localeCompare(a.id, undefined, { numeric: true });
  });
}

function normalizedTemplateName(value: unknown): string {
  return String(value ?? "")
    .trim()
    .split(/\s+/)
    .join(" ");
}

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

  // ── task cold archives ──
  if (path === "/task-archives" && m === "GET") {
    const page = Math.max(1, Number(q.get("page")) || 1);
    const size = Math.min(100, Math.max(1, Number(q.get("size")) || 20));
    const query = (q.get("q") ?? "").trim().toLowerCase();
    const state = (q.get("state") ?? "").trim();
    const filtered = mockTaskArchives.filter((archive) => {
      if (state && archive.state !== state) return false;
      if (!query) return true;
      return [archive.task_id, archive.task_name, archive.task_description].some((value) =>
        String(value ?? "")
          .toLowerCase()
          .includes(query),
      );
    });
    const offset = (page - 1) * size;
    return {
      items: filtered.slice(offset, offset + size).map(publicMockTaskArchive),
      total: filtered.length,
      page,
      size,
    };
  }
  if (seg[0] === "task-archives" && seg.length === 2 && m === "GET") {
    const archive = mockTaskArchives.find((item) => item.id === Number(seg[1]));
    if (!archive) throw new Error("归档不存在");
    return publicMockTaskArchive(archive);
  }
  if (seg[0] === "task-archives" && seg[2] === "restore" && seg.length === 3 && m === "POST") {
    const archive = mockTaskArchives.find((item) => item.id === Number(seg[1]));
    if (!archive) throw new Error("归档不存在");
    mockRestoreArchive(archive);
    return publicMockTaskArchive(archive);
  }
  if (seg[0] === "task-archives" && seg.length === 2 && m === "DELETE") {
    const archive = mockTaskArchives.find((item) => item.id === Number(seg[1]));
    if (!archive) throw new Error("归档不存在");
    mockDeleteArchive(archive);
    return publicMockTaskArchive(archive);
  }
  if (path === "/task-archives/restore/batch" && m === "POST") {
    const ids = bodyIDs(b.archive_ids).map(Number);
    const items = ids.map<ArchiveBatchItem>((id) => {
      const archive = mockTaskArchives.find((item) => item.id === id);
      if (!archive) return { id: String(id), archive_id: id, ok: false, queued: false, error: "归档不存在" };
      try {
        mockRestoreArchive(archive);
        return { id: String(id), archive_id: id, ok: true, queued: true };
      } catch (error) {
        return { id: String(id), archive_id: id, ok: false, queued: false, error: (error as Error).message };
      }
    });
    return { items };
  }
  if (path === "/task-archives/delete/batch" && m === "POST") {
    const ids = bodyIDs(b.archive_ids).map(Number);
    const items = ids.map<ArchiveBatchItem>((id) => {
      const archive = mockTaskArchives.find((item) => item.id === id);
      if (!archive) return { id: String(id), archive_id: id, ok: false, queued: false, error: "归档不存在" };
      try {
        mockDeleteArchive(archive);
        return { id: String(id), archive_id: id, ok: true, queued: true };
      } catch (error) {
        return { id: String(id), archive_id: id, ok: false, queued: false, error: (error as Error).message };
      }
    });
    return { items };
  }
  if (path === "/tasks/archive/batch" && m === "POST") {
    const requested = bodyIDs(b.task_ids);
    const selected = new Set(requested);
    const ordered: string[] = [];
    const visiting = new Set<string>();
    const visited = new Set<string>();
    const visit = (id: string) => {
      if (visited.has(id) || visiting.has(id)) return;
      visiting.add(id);
      for (const candidate of mockTasks) {
        if (!selected.has(candidate.id) || !(candidate.source_task_ids ?? []).map(String).includes(id)) continue;
        visit(candidate.id);
      }
      visiting.delete(id);
      visited.add(id);
      ordered.push(id);
    };
    for (const id of requested) visit(id);
    const byID = new Map<string, ArchiveBatchItem>();
    for (const id of ordered) {
      try {
        const archive = mockArchiveTask(id);
        byID.set(id, { id, archive_id: archive.id, ok: true, queued: true });
      } catch (error) {
        byID.set(id, { id, ok: false, queued: false, error: (error as Error).message });
      }
    }
    return { items: requested.map((id) => byID.get(id) ?? { id, ok: false, queued: false, error: "任务不存在" }) };
  }
  if (seg[0] === "tasks" && seg[2] === "archive" && seg.length === 3 && m === "POST") {
    return publicMockTaskArchive(mockArchiveTask(seg[1]));
  }

  // ── tasks ──
  if (path === "/tasks" && m === "GET") {
    sortMockTasks();
    return { tasks: mockTasks.map(publicMockTask), active: mockActiveTask };
  }
  if (path === "/tasks" && m === "POST") {
    let suffix = 1;
    while (mockTasks.some((item) => item.id === `t-new-${suffix}`)) suffix++;
    const id = `t-new-${suffix}`;
    const now = new Date();
    const profileIDs = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const sourceTaskIDs = [...((b.source_task_ids as string[] | undefined) ?? [])];
    const companyIDs = [...new Set((b.company_ids as number[] | undefined) ?? [])];
    if (companyIDs.some((companyID) => !mockCompanies.some((company) => company.id === companyID))) {
      throw new Error("关联企业不存在或无效");
    }
    const categoryID = typeof b.category_id === "number" ? b.category_id : undefined;
    const category = categoryID === undefined ? undefined : mockTaskCategories.find((item) => item.id === categoryID);
    if (categoryID !== undefined && !category) throw new Error("任务分类不存在");
    const created: Task = {
      id,
      name: String(b.name ?? ""),
      category_id: category?.id,
      category_name: category?.name,
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
    const numericTaskID = nextMockTaskAssetID++;
    mockTaskAssetIDs.set(id, numericTaskID);
    const selectedCompanies = new Set(companyIDs);
    for (const asset of mockAssets) {
      if (asset.company_id === undefined || !selectedCompanies.has(asset.company_id)) continue;
      if (!asset.task_ids.includes(numericTaskID)) asset.task_ids.push(numericTaskID);
      const company = mockCompanies.find((candidate) => candidate.id === asset.company_id);
      setMockTaskAssetSource(id, asset.id, {
        task_source: "company",
        task_source_summary: `任务创建时关联企业：${company?.name ?? `#${asset.company_id}`}`,
        task_source_node_id: undefined,
      });
    }
    for (const item of mockTasks) item.active = false;
    mockTasks.unshift(created);
    mockActiveTask = id;
    return created;
  }
  if (path === "/task-categories" && m === "GET") return { categories: mockTaskCategorySnapshot() };
  if (path === "/task-categories" && m === "POST") {
    const name = normalizedTemplateName(b.name);
    if (!name) throw new Error("分类名称不能为空");
    if (mockTaskCategories.some((category) => category.name.toLowerCase() === name.toLowerCase())) {
      throw new Error("分类名称已存在");
    }
    const now = new Date().toISOString();
    const category: TaskCategory = {
      id: mockTaskCategories.reduce((maximum, item) => Math.max(maximum, item.id), 0) + 1,
      name,
      task_count: 0,
      created_at: now,
      updated_at: now,
    };
    mockTaskCategories.push(category);
    return category;
  }
  if (seg[0] === "task-categories" && seg.length === 2 && m === "PATCH") {
    const category = mockTaskCategories.find((item) => item.id === Number(seg[1]));
    if (!category) throw new Error("任务分类不存在");
    const name = normalizedTemplateName(b.name);
    if (!name) throw new Error("分类名称不能为空");
    if (mockTaskCategories.some((item) => item.id !== category.id && item.name.toLowerCase() === name.toLowerCase())) {
      throw new Error("分类名称已存在");
    }
    category.name = name;
    category.updated_at = new Date().toISOString();
    for (const task of mockTasks) {
      if (task.category_id === category.id) task.category_name = name;
    }
    return { ...category, task_count: mockTasks.filter((task) => task.category_id === category.id).length };
  }
  if (seg[0] === "task-categories" && seg.length === 2 && m === "DELETE") {
    const categoryID = Number(seg[1]);
    const index = mockTaskCategories.findIndex((item) => item.id === categoryID);
    if (index < 0) throw new Error("任务分类不存在");
    mockTaskCategories.splice(index, 1);
    for (const task of mockTasks) {
      if (task.category_id !== categoryID) continue;
      task.category_id = undefined;
      task.category_name = undefined;
    }
    return { deleted: categoryID };
  }
  if (path === "/tasks/category/batch" && m === "POST") {
    const requested = Array.isArray(b.task_ids) ? b.task_ids.map(String) : [];
    const taskIDs = [...new Set(requested)];
    if (taskIDs.length === 0 || taskIDs.length > 100) throw new Error("task_ids 数量必须为 1-100");
    const categoryID = typeof b.category_id === "number" ? b.category_id : undefined;
    const category = categoryID === undefined ? undefined : mockTaskCategories.find((item) => item.id === categoryID);
    if (categoryID !== undefined && !category) throw new Error("任务分类不存在");
    const items = taskIDs.map((id) => {
      const task = mockTasks.find((item) => item.id === id);
      if (!task) return { id, ok: false, error: "task not found" };
      task.category_id = category?.id;
      task.category_name = category?.name;
      return { id, ok: true };
    });
    return {
      items,
      category: category ? mockTaskCategorySnapshot().find((item) => item.id === category.id) : null,
    };
  }
  if (seg[0] === "tasks" && seg[2] === "category" && seg.length === 3 && m === "PATCH") {
    const task = mockTasks.find((item) => item.id === seg[1]);
    if (!task) throw new Error("任务不存在");
    const categoryID = typeof b.category_id === "number" ? b.category_id : undefined;
    const category = categoryID === undefined ? undefined : mockTaskCategories.find((item) => item.id === categoryID);
    if (categoryID !== undefined && !category) throw new Error("任务分类不存在");
    task.category_id = category?.id;
    task.category_name = category?.name;
    return task;
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
  if (seg[0] === "tasks" && seg.length === 2 && m === "GET") {
    const task = mockTasks.find((item) => item.id === seg[1]);
    if (!task) throw new Error("任务不存在");
    return publicMockTask(task);
  }
  if (seg[0] === "tasks" && seg.length === 2 && m === "PATCH") {
    const task = mockTasks.find((item) => item.id === seg[1]);
    if (!task) throw new Error("任务不存在");
    if (typeof b.name === "string") task.name = b.name.trim();
    if (typeof b.pinned === "boolean") {
      task.pinned = b.pinned;
      task.pinned_at = b.pinned ? (task.pinned_at ?? new Date().toISOString()) : null;
    }
    sortMockTasks();
    return structuredClone(task);
  }
  if (seg[0] === "tasks" && seg.length === 2 && m === "DELETE") {
    const id = seg[1];
    const numericTaskID = mockTaskAssetID(id);
    const index = mockTasks.findIndex((item) => item.id === id);
    if (index >= 0) mockTasks.splice(index, 1);
    mockTaskAssetIDs.delete(id);
    deleteMockTaskAssetSources(id);
    if (numericTaskID !== undefined) {
      for (const asset of mockAssets) asset.task_ids = asset.task_ids.filter((taskID) => taskID !== numericTaskID);
    }

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
          activities: mockActivity.filter((item) => item.intent_id === id).length,
        },
      };
    }
    const action = b.action === "resume" ? "resume" : "pause";
    const result = controlMockIntent(id, action);
    if (!result.ok) throw new Error(result.error ?? "Worker 状态已变化");
    return { id: Number(id.replace(/\D/g, "")) || 0, state: result.state };
  }
  if (seg[0] === "tasks" && seg[2] === "intents" && seg[4] === "messages" && m === "POST") {
    const id = seg[3];
    const result = sendMockWorkerMessage(id, String(b.message ?? ""), String(b.request_id ?? ""));
    if (!result.ok) throw new Error(result.error ?? "Worker 状态已变化");
    return {
      id: Number(id.replace(/\D/g, "")) || 0,
      state: result.state ?? "open",
      accepted: true,
      activity_seq: result.activitySeq ?? 0,
      request_id: result.requestId ?? "",
    };
  }
  if (seg[0] === "tasks" && seg.length === 3 && seg[2] === "control" && m === "POST") {
    const action = b.action === "resume" ? "resume" : "pause";
    const result = controlMockTask(seg[1], action);
    if (!result.ok) throw new Error(result.error ?? "任务状态已变化");
    const task = mockTasks.find((item) => item.id === seg[1]);
    return { id: seg[1], paused: Boolean(task?.paused), queued: Boolean(task?.queued), status: task?.status ?? "" };
  }
  if (seg[0] === "tasks" && seg[2] === "chat" && seg[3] === "status") return { running: false };
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
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 3 && m === "GET") {
    return { scope: mockTaskScopes.get(seg[1]) ?? [] };
  }
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 3 && m === "POST") {
    const current = mockTaskScopes.get(seg[1]) ?? [];
    const kind = String(b.kind ?? "") as TaskScopeRow["kind"];
    const value = String(b.value ?? "").trim();
    const row: TaskScopeRow = {
      id: Date.now(),
      task_id: mockTaskAssetID(seg[1]) ?? Number(seg[1]),
      kind,
      source: "manual",
    };
    if (kind === "root_domain" || kind === "subdomain") row.domain = value;
    else if (kind === "ip" || kind === "cidr") row.net = value;
    else row.value = value;
    mockTaskScopes.set(seg[1], [...current, row]);
    return row;
  }
  if (seg[0] === "tasks" && seg[2] === "scope" && seg.length === 4 && m === "DELETE") {
    const id = Number(seg[3]);
    mockTaskScopes.set(
      seg[1],
      (mockTaskScopes.get(seg[1]) ?? []).filter((row) => row.id !== id),
    );
    return { ok: true };
  }

  // ── 全局 llm_usage 聚合（仪表盘新版视图，demo）──
  if (path === "/tokens/usage")
    return {
      by_profile: [
        {
          profile_name: "default",
          calls: 60,
          tasks: 4,
          input_tokens: 1570000,
          output_tokens: 110000,
          cache_read_tokens: 1120000,
          cache_write_tokens: 140000,
        },
      ],
      daily: [
        {
          profile_name: "default",
          date: "2026-08-18",
          input_tokens: 520000,
          output_tokens: 38000,
          cache_read_tokens: 370000,
        },
        {
          profile_name: "default",
          date: "2026-08-19",
          input_tokens: 640000,
          output_tokens: 45000,
          cache_read_tokens: 460000,
        },
        {
          profile_name: "default",
          date: "2026-08-20",
          input_tokens: 410000,
          output_tokens: 27000,
          cache_read_tokens: 290000,
        },
      ],
    };

  // ── 按模型 token 用量（demo：一条示例）──
  if (path === "/llm/records/by-model")
    return {
      models: [
        {
          model: "claude-opus-4-6",
          calls: 42,
          input_tokens: 1250000,
          output_tokens: 86000,
          cache_read_tokens: 940000,
          cache_write_tokens: 120000,
        },
        {
          model: "claude-haiku-4-5",
          calls: 18,
          input_tokens: 320000,
          output_tokens: 24000,
          cache_read_tokens: 180000,
          cache_write_tokens: 20000,
        },
      ],
    };

  // ── 工作空间文件管理器（demo：静态示例树；写/建/删走下方写兜底 {ok:true}）──
  if (path === "/workspace/list") return D.workspaceList(q.get("path") ?? "");
  if (path === "/workspace/read") return D.workspaceRead(q.get("path") ?? "");

  // ── stats ──
  if (path === "/stats") {
    return D.stats(task, { tasks: mockTasks, findings: mockFindings, activeTask: mockActiveTask });
  }

  // ── assets ──
  if (path === "/assets/counts") return mockAssetCounts(q.get("task_id"));
  if (path === "/assets" && m === "GET") {
    const type = q.get("type") ?? "";
    const taskID = q.get("task_id");
    const numericTaskID = taskID ? mockTaskAssetID(taskID) : undefined;
    const dsl = q.get("dsl") ?? "";
    const list = mockAssets.filter((asset) => {
      if (type && asset.type !== type) return false;
      if (taskID && (numericTaskID === undefined || !asset.task_ids.includes(numericTaskID))) return false;
      return !dsl || mockAssetMatchesDSL(asset, dsl);
    });
    const limit = Number(q.get("limit") ?? 50);
    const offset = Number(q.get("offset") ?? 0);
    const page = list.slice(offset, offset + limit).map((asset) => (taskID ? mockAssetForTask(taskID, asset) : asset));
    return { count: page.length, total: list.length, assets: page };
  }
  if (path === "/assets" && m === "DELETE") {
    const ids = new Set(Array.isArray(b.ids) ? b.ids.map(Number) : []);
    let deleted = 0;
    for (let index = mockAssets.length - 1; index >= 0; index--) {
      if (!ids.has(mockAssets[index].id)) continue;
      mockAssets.splice(index, 1);
      deleted++;
    }
    return { deleted };
  }
  if (seg[0] === "tasks" && seg[2] === "assets" && seg.length === 3 && m === "POST") {
    const task = mockTasks.find((item) => item.id === seg[1]);
    const numericTaskID = mockTaskAssetID(seg[1]);
    if (!task || numericTaskID === undefined) throw new Error("任务不存在");
    if (Array.isArray(b.scope)) {
      if (b.scope.length === 0 || b.scope.length > MAX_COMPANY_SCOPE_RULES) throw new Error("请填写有效测试范围");
      const rules: CompanyScopeRule[] = b.scope.map((candidate, index) => {
        if (typeof candidate === "string") {
          const issue = classifyCompanyScopeLine(candidate, index + 1);
          if (!issue.rule || issue.error) throw new Error(`第 ${index + 1} 条范围无效：${issue.error ?? "无法识别"}`);
          return issue.rule;
        }
        const item = candidate as { kind?: unknown; value?: unknown };
        const value = String(item?.value ?? "").trim();
        const rule =
          item?.kind && isCompanyScopeKind(item.kind)
            ? { kind: item.kind, value }
            : classifyCompanyScopeLine(value, index + 1).rule;
        const error = rule ? companyScopeRuleError(rule) : "无法识别";
        if (!rule || error) throw new Error(`第 ${index + 1} 条范围无效：${error}`);
        return rule;
      });
      const mutation: TaskAssetScopeMutation = {
        requested: rules.length,
        assets_linked: 0,
        assets_existing: 0,
        scopes_added: 0,
        scopes_existing: 0,
      };
      const currentScopes = mockTaskScopes.get(seg[1]) ?? [];
      const scopeKeys = new Set(
        currentScopes.map((row) => `${row.kind}|${row.domain ?? row.net ?? row.value ?? row.company_id ?? ""}`),
      );
      for (const rule of rules) {
        const normalized = normalizeCompanyScopeValue(rule);
        const scope: TaskScopeRow = {
          id: Date.now() + currentScopes.length,
          task_id: numericTaskID,
          kind: rule.kind === "domain" ? "root_domain" : rule.kind,
          source: "manual",
          reason: "用户在测试资产页手工新增",
        };
        if (rule.kind === "domain") scope.domain = normalized;
        else if (rule.kind === "ip") scope.net = `${normalized}/${normalized.includes(":") ? 128 : 32}`;
        else if (rule.kind === "cidr") scope.net = normalized;
        else scope.value = normalized;
        const scopeKey = `${scope.kind}|${scope.domain ?? scope.net ?? scope.value ?? ""}`;
        if (scopeKeys.has(scopeKey)) mutation.scopes_existing++;
        else {
          scopeKeys.add(scopeKey);
          currentScopes.push(scope);
          mutation.scopes_added++;
        }

        if (rule.kind !== "domain" && rule.kind !== "ip") continue;
        const type = rule.kind === "domain" ? "root_domain" : "ip";
        let asset = mockAssets.find((item) =>
          type === "root_domain"
            ? item.type === type && item.domain === normalized
            : item.type === type && item.ip === normalized,
        );
        const alreadyLinked = asset?.task_ids.includes(numericTaskID) ?? false;
        if (!asset) {
          const nextID = mockAssets.reduce((max, item) => Math.max(max, item.id), 0) + 1;
          asset = {
            id: nextID,
            type,
            task_ids: [],
            ...(type === "root_domain" ? { domain: normalized, root_domain: normalized } : { ip: normalized }),
            last_seen: new Date().toISOString(),
          };
          mockAssets.push(asset);
        }
        if (alreadyLinked) mutation.assets_existing++;
        else {
          asset.task_ids.push(numericTaskID);
          mutation.assets_linked++;
        }
        setMockTaskAssetSource(seg[1], asset.id, {
          task_source: "manual",
          task_source_summary: "用户在测试资产页手工新增",
          task_source_node_id: undefined,
        });
      }
      mockTaskScopes.set(seg[1], currentScopes);
      return mutation;
    }
    const ids = [...new Set(Array.isArray(b.asset_ids) ? b.asset_ids.map(Number) : [])];
    const sourceSummary = String(b.source_summary ?? "").trim();
    if (ids.length === 0 || ids.length > 100 || !sourceSummary) throw new Error("请选择资产并填写来源说明");
    const requestedAssets = ids.map((id) => mockAssets.find((asset) => asset.id === id));
    if (requestedAssets.some((asset) => !asset)) throw new Error("资产不存在");
    const mutation: TaskAssetMutation = { requested: ids.length, attached: 0, existing: 0 };
    for (const asset of requestedAssets) {
      if (!asset) continue;
      if (asset.task_ids.includes(numericTaskID)) mutation.existing++;
      else {
        asset.task_ids.push(numericTaskID);
        mutation.attached++;
      }
      setMockTaskAssetSource(seg[1], asset.id, {
        task_source: "manual",
        task_source_summary: sourceSummary,
        task_source_node_id: undefined,
      });
    }
    return mutation;
  }
  if (seg[0] === "tasks" && seg[2] === "assets" && seg.length === 4 && m === "DELETE") {
    const numericTaskID = mockTaskAssetID(seg[1]);
    const asset = mockAssets.find((item) => item.id === Number(seg[3]));
    if (numericTaskID === undefined || !asset) throw new Error("任务或资产不存在");
    if (!asset.task_ids.includes(numericTaskID)) throw new Error("资产未关联当前任务");
    asset.task_ids = asset.task_ids.filter((id) => id !== numericTaskID);
    deleteMockTaskAssetSources(seg[1], asset.id);
    return { detached: asset.id };
  }
  if (seg[0] === "tasks" && seg[2] === "intent-assets" && seg.length === 3 && m === "GET") {
    let mappings: Array<{ intentID: string; assetID: number; summary: string }> = [];
    if (seg[1] === "t-acme-web") {
      mappings = [
        { intentID: "i3", assetID: 6, summary: "后台功能枚举意图从前序子域发现中选定" },
        { intentID: "i5", assetID: 3, summary: "订单接口测试意图从 API 任务目标中选定" },
      ];
    } else if (seg[1] === "t-acme-api") {
      mappings = [{ intentID: "i5", assetID: 3, summary: "订单接口测试意图从 API 任务目标中选定" }];
    }
    const sourceTaskID = mockTaskAssetID(seg[1]) ?? 0;
    const assets: IntentAsset[] = mappings.flatMap((mapping) => {
      const storedAsset = mockAssets.find((item) => item.id === mapping.assetID);
      if (!storedAsset) return [];
      const asset = mockAssetForTask(seg[1], storedAsset);
      return [
        {
          intent_id: mapping.intentID,
          asset_id: asset.id,
          type: asset.type,
          label: asset.domain ?? asset.ip ?? asset.app_name ?? asset.url ?? asset.service_name ?? `#${asset.id}`,
          source: asset.task_source ?? "agent",
          source_summary: asset.task_source_summary ?? mapping.summary,
          source_node_id: asset.task_source_node_id,
          source_task_id: sourceTaskID,
          inherited: false,
        },
      ];
    });
    return { assets };
  }

  // ── companies ──
  if (path === "/companies" && m === "GET") return structuredClone(mockCompanies);
  if (path === "/companies" && m === "POST") {
    const name = String(b.name ?? "").trim();
    if (!name) throw new Error("企业名称不能为空");
    if (mockCompanies.some((company) => company.name.toLowerCase() === name.toLowerCase())) {
      throw new Error("企业已存在");
    }
    const id = mockCompanies.reduce((max, company) => Math.max(max, company.id), 0) + 1;
    const scopeResult = mockScopeRows(id, b.scope);
    const company: Company = { id, name, asset_count: 0, scope: scopeResult.rows };
    mockCompanies.push(company);
    return {
      id,
      created: true,
      scope_added: scopeResult.rows.length,
      scope_skipped: scopeResult.skipped,
      scope_invalid: scopeResult.invalid,
    };
  }
  if (seg[0] === "companies" && seg[2] === "scope" && m === "POST") {
    const company = mockCompanies.find((item) => item.id === Number(seg[1]));
    if (!company) throw new Error("企业不存在");
    const reset = b.reset === true;
    const scopeResult = mockScopeRows(company.id, b.scope, reset ? [] : (company.scope ?? []));
    if (reset && scopeResult.invalid > 0) throw new Error("企业范围包含无效规则，未覆盖原有范围");
    company.scope = reset ? scopeResult.rows : [...(company.scope ?? []), ...scopeResult.rows];
    return { added: scopeResult.rows.length, skipped: scopeResult.skipped, invalid: scopeResult.invalid };
  }
  if (seg[0] === "companies" && seg.length === 2 && m === "DELETE") {
    const id = Number(seg[1]);
    const index = mockCompanies.findIndex((item) => item.id === id);
    if (index < 0) throw new Error("企业不存在");
    mockCompanies.splice(index, 1);
    let assetsDeleted = 0;
    for (let assetIndex = mockAssets.length - 1; assetIndex >= 0; assetIndex--) {
      const asset: Asset = mockAssets[assetIndex];
      if (asset.company_id !== id) continue;
      if (b.delete_assets === true) {
        mockAssets.splice(assetIndex, 1);
        assetsDeleted++;
      } else {
        delete asset.company_id;
      }
    }
    return { deleted: 1, assets_deleted: assetsDeleted };
  }

  // ── exploration ──
  if (path === "/exploration/frontier") return D.frontier;
  if (path === "/exploration/findings/stats") {
    const vulnclasses = Array.from(new Set(mockFindings.map((f) => f.vulnclass))).sort();
    // 「按任务」下拉:有漏洞的任务 + 描述 + 条数(mock 任务 id 是字符串,直接当 id 用)。
    const taskMap = new Map<string, { name: string; description: string; count: number }>();
    for (const f of mockFindings) {
      if (!f.task_id) continue;
      const owner = mockTasks.find((candidate) => candidate.id === f.task_id);
      const cur = taskMap.get(f.task_id) ?? {
        name: owner?.name ?? "",
        description: f.task_description ?? "",
        count: 0,
      };
      cur.count++;
      taskMap.set(f.task_id, cur);
    }
    const tasks = Array.from(taskMap, ([id, v]) => ({ id, name: v.name, description: v.description, count: v.count }));
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
  if (path === "/exploration/findings/groups") {
    const severityOrder = { critical: 4, high: 3, medium: 2, low: 1 } as const;
    let list = mockFindings.slice();
    const filterSeverity = q.get("severity");
    const filterStatus = q.get("status");
    const filterVulnclass = q.get("vulnclass");
    const filterTask = q.get("task_id");
    list = list.filter((finding) => mockFindingMatchesQuery(finding, q.get("q")));
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
  // 单条 finding:GET 详情 / PATCH 改状态/严重度/名称/类别(demo 直接改内存对象)。
  if (seg[0] === "exploration" && seg[1] === "findings" && seg.length === 3 && seg[2] !== "stats") {
    const f = mockFindings.find((x) => x.id === seg[2]);
    if (!f) return {};
    if (m === "PATCH") {
      if (typeof b.status === "string") f.status = b.status as typeof f.status;
      if (typeof b.severity === "string") f.severity = b.severity as typeof f.severity;
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
    list = list.filter((finding) => mockFindingMatchesQuery(finding, q.get("q")));
    if (fSev) list = list.filter((f) => f.severity === fSev);
    if (fStatus) list = list.filter((f) => f.status === fStatus);
    if (fVuln) list = list.filter((f) => f.vulnclass === fVuln);
    if (fTask === "__unassigned__") list = list.filter((f) => !f.task_id);
    else if (fTask) list = list.filter((f) => f.task_id === fTask);
    list.sort((a, b) =>
      q.get("sort") === "severity"
        ? sev[b.severity] - sev[a.severity] || +new Date(b.ts) - +new Date(a.ts)
        : +new Date(b.ts) - +new Date(a.ts),
    );
    const rawPage = Number(q.get("page") ?? 1);
    const rawPageSize = Number(q.get("limit") ?? 20);
    const page = Number.isFinite(rawPage) && rawPage > 0 ? Math.floor(rawPage) : 1;
    const pageSize = Number.isFinite(rawPageSize) && rawPageSize > 0 ? Math.min(200, Math.floor(rawPageSize)) : 20;
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
      let list = mockIntents;
      if (before > 0) list = list.filter((intent) => Number(intent.id.replace(/\D/g, "") || intent.id) < before);
      return { items: list.slice(0, limit), has_more: list.length > limit };
    }
    return mockIntents;
  }
  if (path === "/exploration/tokens") {
    const selectedTask = task ? mockTasks.find((item) => item.id === task) : undefined;
    return {
      workers: D.tokenWorkers,
      sessions: D.tokenSessions,
      total: selectedTask?.tokens ?? D.tokenTotal,
    };
  }
  if (path === "/exploration/graph") return D.explorationGraph;
  if (path === "/exploration/activity" && seg.length === 2) {
    const since = Number(q.get("since") ?? 0);
    const limit = Math.max(1, Number(q.get("limit") ?? 300));
    const items = mockActivity.filter((item) => item.seq > since).slice(0, limit);
    return { items, cursor: items.length ? items[items.length - 1].seq : since };
  }
  if (seg[0] === "exploration" && seg[1] === "activity" && seg.length === 3) {
    const a = mockActivity.find((x) => x.seq === Number(seg[2]));
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
  if (path === "/chat")
    return { reply: "（demo）我已把该建议注入为一条高优意图，work agent 会尽快执行。", mode: "hint" };
  if (path === "/gc") return { removed: 0 };

  // ── 工具执行历史 ──
  if (path === "/commands" && m === "GET") return { commands: D.commandRecords, total: D.commandRecords.length };
  if (path === "/commands/stats" && m === "GET") {
    const tally = new Map<string, { tool: string; total: number; errors: number }>();
    for (const c of D.commandRecords) {
      const tool = c.tool || "-";
      const s = tally.get(tool) ?? { tool, total: 0, errors: 0 };
      s.total++;
      if (c.is_error) s.errors++;
      tally.set(tool, s);
    }
    return { stats: [...tally.values()].sort((a, b) => b.total - a.total || a.tool.localeCompare(b.tool)) };
  }

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
  if (path === "/llm/test")
    return { ok: true, latency_ms: 128, model: String(b.model ?? "claude-opus-4-8"), reply: "OK" };
  if (path === "/llm/profiles" && m === "GET") return { profiles: D.llmProfiles };
  if (path === "/llm/profiles" && m === "POST") return { id: Number(b.id) || 3 };
  if (path === "/llm/profiles/active") return { ok: true };
  if (path === "/llm/pool" && m === "GET") return D.llmPool;
  if (path === "/llm/pool/reset")
    return { ...D.llmPool, chain: D.llmPool.chain.map((c) => ({ ...c, state: "ok", fails: 0, cooldown_secs: 0 })) };
  if (seg[0] === "llm" && seg[1] === "profiles" && seg.length === 3 && m === "DELETE")
    return { deleted: Number(seg[2]) };

  // ── agents ──
  if (path === "/agents" && m === "GET") return { agents: D.agents };
  if (path === "/agents" && m === "POST")
    return {
      id: "9",
      key: String(b.key ?? "custom"),
      name: String(b.name ?? ""),
      role: "custom",
      builtin: false,
      enabled: true,
    };
  if (seg[0] === "agents" && seg.length === 2 && m === "GET") return D.agentDetail(seg[1]);
  if (seg[0] === "agents" && seg[2] === "triggers" && m === "GET") return { triggers: [] };
  if (seg[0] === "agents" && seg[2] === "prompts") return { versions: D.agentDetail(seg[1]).versions };
  if (seg[0] === "agents" && seg[2] === "variables") return { variables: D.agentDetail(seg[1]).variables };
  if (seg[0] === "agents" && seg[2] === "prompt" && seg[3] === "preview")
    return { rendered: String(b.template ?? "").replace(/\{\{\.(\w+)\}\}/g, "«$1»") };
  if (seg[0] === "agents" && seg[2] === "visibility" && m === "GET") return D.agentDetail(seg[1]).visibility;

  // ── conversations ──
  if (path === "/conversations" && m === "GET") {
    sortMockConversations();
    return { conversations: structuredClone(mockConversations) };
  }
  if (path === "/conversations" && m === "POST") {
    const now = new Date().toISOString();
    const title = String(b.title ?? "").trim() || "新对话";
    const conversation: Conversation = {
      id: mockConversations.reduce((max, item) => Math.max(max, item.id), 0) + 1,
      agent_key: String(b.agent_key ?? "mainagent"),
      title,
      llm_profile_id: typeof b.llm_profile_id === "number" ? b.llm_profile_id : undefined,
      pinned: false,
      created_at: now,
      updated_at: now,
    };
    mockConversations.unshift(conversation);
    return structuredClone(conversation);
  }
  if (seg[0] === "conversations" && seg.length === 2 && m === "PATCH") {
    const conversation = mockConversations.find((item) => item.id === Number(seg[1]));
    if (!conversation) return {};
    if (typeof b.title === "string") conversation.title = b.title.trim();
    if (typeof b.pinned === "boolean") {
      conversation.pinned = b.pinned;
      conversation.pinned_at = b.pinned ? (conversation.pinned_at ?? new Date().toISOString()) : null;
    }
    conversation.updated_at = new Date().toISOString();
    sortMockConversations();
    return structuredClone(conversation);
  }
  if (seg[0] === "conversations" && seg.length === 2 && m === "DELETE") {
    const id = Number(seg[1]);
    const index = mockConversations.findIndex((item) => item.id === id);
    if (index >= 0) mockConversations.splice(index, 1);
    return { deleted: id };
  }
  if (path === "/conversations/delete/batch" && m === "POST") {
    const ids = Array.isArray(b.ids)
      ? [...new Set(b.ids.map(Number).filter((id) => Number.isInteger(id) && id > 0))]
      : [];
    const items = ids.map((id) => {
      const index = mockConversations.findIndex((item) => item.id === id);
      if (index < 0) return { id, ok: false, error: "conversation not found" };
      mockConversations.splice(index, 1);
      return { id, ok: true };
    });
    return { items };
  }
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
  if (path === "/sync/scopesentry/status")
    return { exists: false, configured: false, enabled: false, reachable: false, tools: [] };
  if (path === "/sync/scopesentry/projects") return { projects: [], tag: {} };
  if (path === "/sync/scopesentry/tasks") return { tasks: [] };
  if (path === "/sync/scopesentry/sync") return { synced: {}, companies: null, warnings: null, errors: null };

  // ── skills ──
  if (path === "/skills" && m === "GET") return { skills: D.skills };
  if (path === "/skills/missing") return { missing: D.missingSkills };
  if (seg[0] === "skills" && seg[2] === "usage") return { calls: D.skillCalls };
  if (path === "/skills" && m === "POST") return { name: String(b.name ?? "new-skill") };
  if (seg[0] === "skills" && seg[2] === "files" && seg.length === 3) return { files: ["SKILL.md"] };
  if (seg[0] === "skills" && seg[2] === "files" && seg.length >= 4)
    return { content: "# SKILL.md\n\n（demo）这是该 skill 的说明文件示例。", file: seg.slice(3).join("/") };

  // ── visibility ──
  if (seg[0] === "visibility" && m === "GET") return { agents: [] };

  // ── intercept ──
  if (path === "/intercept/rules" && m === "GET") return { rules: D.interceptRules };
  if (seg[0] === "intercept" && seg[1] === "rules" && seg[3] === "toggle")
    return { ok: true, enabled: b.enabled ?? true };
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
  return /(\/(tasks|profiles|conversations|rules|history|projects|tokens|agents|servers|skills|tools|findings|intents)s?$)|s$/.test(
    path,
  )
    ? []
    : {};
}
