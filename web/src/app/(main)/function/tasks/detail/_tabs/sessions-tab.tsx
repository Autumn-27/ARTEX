"use client";

import * as React from "react";

import {
  BrainIcon,
  CircleCheckIcon,
  CircleSlashIcon,
  CircleXIcon,
  ClockIcon,
  HistoryIcon,
  Loader2Icon,
  PaperclipIcon,
  PauseIcon,
  PlayIcon,
  RadioIcon,
  RotateCwIcon,
  SendIcon,
  ShieldAlertIcon,
  SquareIcon,
  UserIcon,
  WifiOffIcon,
  XIcon,
  ZapOffIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TodoPopover } from "@/components/todo-popover";
import { Transcript } from "@/components/transcript";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupTextarea } from "@/components/ui/input-group";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { api, sseUrl } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import type {
  Activity,
  ChatAttachment,
  InterceptApprovalRow,
  Session,
  SessionStatus,
  TaskNode,
  TokenTotal,
} from "@/lib/types";
import { cn } from "@/lib/utils";

// fmtBytes renders a human file size for attachment chips (mirrors transcript.tsx).
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// ── Reliability model (see docs/task-session-history-sse-remediation.md) ──────────
// The task's activity is NO LONGER one unbounded `allActivity` array replayed from
// SSE since=0. Instead:
//   • Each UI session (main | plan | intent:<id>) has its own lazily-loaded, reverse-
//     paginated cache (SessionState below). Opening a session loads only its latest
//     page; scrolling up pages older history in.
//   • A single task-level SSE (opened at since=snapshot_cursor from the first history
//     page) tails ALL agents' new activity; frames are dispatched by session_key.
//   • History + SSE meet gap-free at snapshot_cursor and are merged by seq (dedup),
//     so refresh / tab-switch / sleep / reconnect never drop the newest records.

const PAGE = 200; // history page size
const SYSTEM_SCAN_PAGE = 500; // generic activity pages scanned to recover sparse system audit events
const MAX_KEEP = 4000; // per-session in-memory cap; older pages re-fetched on scroll-up
const STREAM_WINDOW_MS = 5000; // "live" = activity seen within this window

// One session's lazily-loaded, reverse-paginated cache. `lastTs`/`unread` are kept
// live even for sessions that were never opened, so the list shows liveness + unread
// without holding their full history.
type SessionState = {
  items: Activity[];
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean; // older history remains above the loaded window
  earliestSeq: number; // earliest loaded id — reverse-pagination anchor
  unread: number;
  lastTs: string; // most-recent activity time (drives live badge; updated even when unloaded)
  error?: string;
};
type SessionStore = Record<string, SessionState>;

function emptyState(): SessionState {
  return {
    items: [],
    loaded: false,
    loading: false,
    loadingMore: false,
    hasMore: false,
    earliestSeq: 0,
    unread: 0,
    lastTs: "",
  };
}

// sessionKeyOf routes an activity to its stable session key. worker="planner" covers
// BOTH the Goal Agent's round-0 decomposition and the Planner (single Plan session).
function sessionKeyOf(a: Activity): string {
  if (a.worker === "system" || a.kind === "llm_switch" || a.kind === "llm_failover") return "system";
  if (a.worker === "mainagent") return "main";
  if (a.worker === "planner") return "plan";
  if (a.intent_id) return `intent:${a.intent_id}`;
  return "unknown";
}

// mergeBySeq unions two activity lists by seq (dedup) in ascending seq order. Every
// data source — latest page, older page, SSE compensation, live tail — goes through
// this, so history responses can never clobber live records received meanwhile.
function mergeBySeq(current: Activity[], incoming: Activity[]): Activity[] {
  if (!incoming.length) return current;
  const bySeq = new Map<number, Activity>();
  for (const a of current) bySeq.set(a.seq, a);
  for (const a of incoming) bySeq.set(a.seq, a);
  return [...bySeq.values()].sort((p, q) => p.seq - q.seq);
}

// statusIcon maps a session status to its icon. Worker terminal states are
// distinct & color-coded: 完成(绿勾圈) / 取消停止(琥珀斜杠圈) / 出错(红叉圈) /
// 步数耗尽(紫). running=蓝色转圈, pending(待领取)=灰时钟.
function statusIcon(status: SessionStatus) {
  switch (status) {
    case "running": // 执行中
      return <Loader2Icon className="size-3.5 animate-spin text-blue-500" />;
    case "paused":
      return <PauseIcon className="size-3.5 text-amber-500" />;
    case "pending": // 待领取(open intent)
      return <ClockIcon className="size-3.5 text-muted-foreground" />;
    case "done": // 完成
      return <CircleCheckIcon className="size-3.5 text-emerald-500" />;
    case "stopped": // 取消/停止(被 planner 终止)
      return <CircleSlashIcon className="size-3.5 text-amber-500" />;
    case "blocked": // 出错
      return <CircleXIcon className="size-3.5 text-red-500" />;
    case "exhausted": // 步数耗尽(撞 max_turns)
      return <ZapOffIcon className="size-3.5 text-violet-500" />;
  }
}

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k";
  return String(n);
}

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

const roleMeta = {
  mainagent: { label: "主 Agent", icon: UserIcon },
  planner: { label: "规划 Planner", icon: BrainIcon },
  worker: { label: "Workers", icon: RadioIcon },
  system: { label: "系统审计", icon: HistoryIcon },
} as const;

// The main-agent session is the interactive entry point of this tab and has no
// dedicated backend "sessions" endpoint — it is a fixed UI affordance whose
// transcript is the main-agent activity stream (worker="mainagent") for the task.
const MAIN_ID = "s-main";
const MAIN_SESSION: Session = {
  id: MAIN_ID,
  role: "mainagent",
  title: "主 Agent · 编排会话",
  status: "running",
  live: true,
  last_activity: "",
};

// The planner session is, like the main-agent session, a fixed UI affordance with
// no dedicated backend "sessions" endpoint — its transcript is every activity step
// the planner emits (worker === "planner", which also carries the Goal Agent's
// round-0 decomposition; the planner carries no intent_id since it generates intents).
const PLANNER_ID = "s-planner";
const PLANNER_SESSION: Session = {
  id: PLANNER_ID,
  role: "planner",
  title: "规划 Planner · 态势研判",
  status: "running",
  live: true,
  last_activity: "",
};

// LLM provider switches are task-level audit events rather than agent output. The
// history endpoint has no "system" filter, so this fixed session is populated by
// scanning the generic incremental activity endpoint and then tailed by task SSE.
const SYSTEM_ID = "s-system";
const SYSTEM_SESSION: Session = {
  id: SYSTEM_ID,
  role: "system",
  title: "系统事件 · LLM 故障转移",
  status: "done",
  live: false,
  last_activity: "",
};

// keyForSession maps a UI Session → its stable store key (main | plan | intent:<id>).
function keyForSession(s: Session): string {
  if (s.role === "mainagent") return "main";
  if (s.role === "planner") return "plan";
  if (s.role === "system") return "system";
  return `intent:${s.intent_id}`;
}

// Map an exploration intent (TaskNode) state → a session status the UI renders.
function intentStatus(state: string): SessionStatus {
  switch (state) {
    case "done":
      return "done";
    case "blocked":
      return "blocked";
    case "exhausted":
      return "exhausted";
    case "stopped":
      return "stopped";
    case "paused":
      return "paused";
    case "open": // 待领取，区别于执行中
      return "pending";
    default: // running
      return "running";
  }
}

// Derive worker sessions from running/open intents — there is no backend
// sessions endpoint, so intents (≈ worker units) are the closest real source.
function intentToSession(n: TaskNode): Session {
  const label = (n.payload ?? "").trim();
  const state = intentStatus(n.state);
  return {
    id: n.id,
    role: "worker",
    title: label || `Intent ${n.id}`,
    status: state,
    live: !n.inherited && state === "running",
    last_activity: n.ts,
    intent_id: n.id,
    source_task_id: n.source_task_id,
    inherited: n.inherited,
  };
}

function SessionItem({
  s,
  active,
  displayTitle,
  hasPending,
  unread,
  onClick,
  onPause,
  onResume,
  onCancel,
  controlling,
}: {
  s: Session;
  active: boolean;
  displayTitle: string;
  hasPending?: boolean;
  unread?: number;
  onClick: () => void;
  onPause?: () => void;
  onResume?: () => void;
  onCancel?: () => void;
  controlling?: boolean;
}) {
  const icon =
    s.role === "worker" ? (
      statusIcon(s.status)
    ) : s.live ? (
      <Loader2Icon className="size-3.5 animate-spin text-blue-500" />
    ) : null;

  const controllable = s.role === "worker" && !s.inherited && (s.status === "running" || s.status === "paused");
  return (
    <div
      className={cn(
        "group/session flex w-full items-center rounded-md transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      <button type="button" onClick={onClick} className="flex min-w-0 flex-1 items-center gap-1 px-2 py-1.5 text-left text-sm">
        {icon ?? <span className="size-3.5 shrink-0" />}
        {s.intent_id && (
          <span className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
            #{s.intent_id}
          </span>
        )}
        {s.inherited && s.source_task_id && (
          <Badge variant="outline" className="shrink-0">
            来源 #{s.source_task_id}
          </Badge>
        )}
        <span className="min-w-0 flex-1 truncate">{displayTitle}</span>
        {hasPending && <ShieldAlertIcon className="size-3.5 shrink-0 text-amber-500" />}
        {!active && unread ? (
          <span className="inline-flex min-w-4 items-center justify-center rounded-full bg-blue-500/15 px-1 text-[10px] font-medium tabular-nums text-blue-600 dark:text-blue-400">
            {unread > 99 ? "99+" : unread}
          </span>
        ) : null}
        {s.live && (
          <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
            <span className="size-1 animate-pulse rounded-full bg-blue-500" />
            实时
          </span>
        )}
      </button>
      {controllable && (
        <div className="flex shrink-0 items-center gap-0.5 pr-1 opacity-100 sm:opacity-0 sm:transition-opacity sm:group-hover/session:opacity-100 sm:group-focus-within/session:opacity-100">
          {s.status === "running" ? (
            <Button type="button" variant="ghost" size="icon-xs" onClick={onPause} disabled={controlling} title="暂停 Worker" aria-label="暂停 Worker">
              {controlling ? <Loader2Icon className="animate-spin" /> : <PauseIcon />}
            </Button>
          ) : (
            <Button type="button" variant="ghost" size="icon-xs" onClick={onResume} disabled={controlling} title="恢复 Worker" aria-label="恢复 Worker">
              {controlling ? <Loader2Icon className="animate-spin" /> : <PlayIcon />}
            </Button>
          )}
          <Button type="button" variant="ghost" size="icon-xs" onClick={onCancel} disabled={controlling} title="取消并清理 Worker 数据" aria-label="取消并清理 Worker 数据" className="text-destructive hover:text-destructive">
            <Trash2Icon />
          </Button>
        </div>
      )}
    </div>
  );
}

export function SessionsTab({ taskId }: { taskId: string }) {
  const [activeId, setActiveId] = React.useState(MAIN_ID);
  // Per-session lazily-loaded caches, keyed by session_key (main | plan | intent:<id>).
  const [store, setStore] = React.useState<SessionStore>({});
  // Worker sessions derived from exploration intents (paged past the old 300 cap).
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [olderIntents, setOlderIntents] = React.useState<TaskNode[]>([]);
  const [intentsHasMore, setIntentsHasMore] = React.useState(false);
  const [loadingOlderIntents, setLoadingOlderIntents] = React.useState(false);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const [controllingIntent, setControllingIntent] = React.useState<string | null>(null);
  const [cancelIntent, setCancelIntent] = React.useState<Session | null>(null);
  // 方式1 文件上传:选好的附件(已落到任务工作目录 uploads/),随下条消息一起发。
  const [attachments, setAttachments] = React.useState<ChatAttachment[]>([]);
  const [uploading, setUploading] = React.useState(false);
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  async function pickFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    setUploading(true);
    try {
      const r = await api.chatUpload("task", taskId, Array.from(files));
      setAttachments((prev) => [...prev, ...r.attachments]);
    } catch (e) {
      toast.error("上传失败：" + (e as Error).message);
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  }

  const patchIntentState = React.useCallback((intentId: string, state?: string) => {
    const patch = (rows: TaskNode[]) =>
      state ? rows.map((row) => (row.id === intentId ? { ...row, state } : row)) : rows.filter((row) => row.id !== intentId);
    setIntents(patch);
    setOlderIntents(patch);
  }, []);

  const controlWorker = React.useCallback(async (session: Session, action: "pause" | "resume" | "cancel") => {
    if (!session.intent_id || session.inherited || controllingIntent) return;
    setControllingIntent(session.intent_id);
    try {
      const result = await api.controlIntent(taskId, session.intent_id, action);
      if (action === "pause") {
        patchIntentState(session.intent_id, "paused");
        toast.success(`Worker #${session.intent_id} 已暂停`);
      } else if (action === "resume") {
        patchIntentState(session.intent_id, "open");
        toast.success(`Worker #${session.intent_id} 已恢复，等待重新领取`);
      } else {
        patchIntentState(session.intent_id);
        if (activeId === session.id) setActiveId(MAIN_ID);
        const deleted = result.deleted;
        toast.success(
          deleted
            ? `Worker #${session.intent_id} 已取消，清理 ${deleted.facts} 条事实、${deleted.findings} 个漏洞`
            : `Worker #${session.intent_id} 已取消`,
        );
      }
    } catch (error) {
      toast.error("Worker 操作失败：" + (error as Error).message);
    } finally {
      setControllingIntent(null);
      setCancelIntent(null);
    }
  }, [activeId, controllingIntent, patchIntentState, taskId]);
  // SSE connection state — surfaced so a dropped realtime link is visible, never
  // silently shown as "no messages".
  const [sseLive, setSseLive] = React.useState(false);
  // Whole-task token total (all agents), polled from the backend aggregate.
  const [taskTokens, setTaskTokens] = React.useState<TokenTotal | null>(null);
  // Pending intercept requests for this task — used to show warning icons on sessions.
  const [pendingIntercepts, setPendingIntercepts] = React.useState<InterceptApprovalRow[]>([]);

  // Refs backing SSE/loading without re-render churn.
  const snapshotRef = React.useRef(0); // task-level snapshot cursor → SSE since=
  const esRef = React.useRef<EventSource | null>(null);
  const activeKeyRef = React.useRef("main"); // current session key (for SSE dispatch/unread)
  const atBottomRef = React.useRef(true); // transcript pinned to bottom?
  const llmToastSeqRef = React.useRef<Set<number>>(new Set());
  // Per-key request token: a stale response for a key is ignored (guards fast
  // latest/older interleaving). Writes are ALWAYS keyed, so a late response can only
  // touch its own session cache — never the currently-viewed one (see §7.5).
  const reqTokenRef = React.useRef<Record<string, number>>({});
  // Keys with a latest-page load in flight — dedups the double trigger where the
  // first-load effect and the active-session effect both want "main" on mount (the
  // former also opens the SSE, so it must not be pre-empted).
  const loadingKeysRef = React.useRef<Set<string>>(new Set());

  // ── store helpers ──────────────────────────────────────────────────────────────
  const patchStore = React.useCallback(
    (key: string, fn: (s: SessionState) => SessionState) => {
      setStore((prev) => ({ ...prev, [key]: fn(prev[key] ?? emptyState()) }));
    },
    [],
  );

  // Load a session's LATEST page (before=0) on first open. Keyed + request-token
  // guarded so a switch away can't corrupt the view.
  const loadSession = React.useCallback(
    (key: string) => {
      if (MOCK) return; // MOCK preloads everything up front
      if (loadingKeysRef.current.has(key)) return; // already in flight (e.g. main on mount)
      loadingKeysRef.current.add(key);
      const token = (reqTokenRef.current[key] ?? 0) + 1;
      reqTokenRef.current[key] = token;
      patchStore(key, (s) => ({ ...s, loading: true, error: undefined }));

      if (key === "system") {
        void (async () => {
          let since = 0;
          let systemItems: Activity[] = [];
          for (;;) {
            const page = await api.activity(taskId, { since, limit: SYSTEM_SCAN_PAGE });
            if (reqTokenRef.current[key] !== token) return;
            systemItems = mergeBySeq(
              systemItems,
              page.items.filter((item) => item.worker === "system" || item.kind === "llm_failover"),
            );
            if (page.items.length < SYSTEM_SCAN_PAGE || page.cursor <= since) break;
            since = page.cursor;
          }
          patchStore(key, (s) => {
            const items = mergeBySeq(systemItems, s.items);
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: false,
              earliestSeq: items.length ? items[0].seq : 0,
              unread: 0,
              lastTs: items.length ? items[items.length - 1].ts : s.lastTs,
              error: undefined,
            };
          });
        })()
          .catch((error) => {
            if (reqTokenRef.current[key] !== token) return;
            patchStore(key, (s) => ({
              ...s,
              loading: false,
              error: (error as Error).message || "加载失败",
            }));
          })
          .finally(() => loadingKeysRef.current.delete(key));
        return;
      }

      api
        .activityHistory(taskId, key, 0, PAGE)
        .then((r) => {
          if (reqTokenRef.current[key] !== token) return; // superseded
          if (r.snapshotCursor > snapshotRef.current) snapshotRef.current = r.snapshotCursor;
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items); // keep any live frames arrived meanwhile
            return {
              ...s,
              items,
              loaded: true,
              loading: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : 0,
              unread: 0,
              error: undefined,
            };
          });
        })
        .catch((e) => {
          if (reqTokenRef.current[key] !== token) return;
          patchStore(key, (s) => ({ ...s, loading: false, error: (e as Error).message || "加载失败" }));
        })
        .finally(() => loadingKeysRef.current.delete(key));
    },
    [taskId, patchStore],
  );

  // Load one older page (scroll-up) for a session, preserving scroll position.
  const loadEarlier = React.useCallback(
    (key: string, viewport: () => HTMLElement | null) => {
      const st = store[key];
      if (!st || st.loadingMore || !st.hasMore || !st.earliestSeq) return;
      const vp = viewport();
      const prevH = vp?.scrollHeight ?? 0;
      const prevTop = vp?.scrollTop ?? 0;
      patchStore(key, (s) => ({ ...s, loadingMore: true }));
      api
        .activityHistory(taskId, key, st.earliestSeq, PAGE)
        .then((r) => {
          patchStore(key, (s) => {
            const items = mergeBySeq(r.items, s.items);
            return {
              ...s,
              items,
              loadingMore: false,
              hasMore: r.hasMore,
              earliestSeq: items.length ? items[0].seq : s.earliestSeq,
            };
          });
          requestAnimationFrame(() => {
            const v = viewport();
            if (v) v.scrollTop = prevTop + (v.scrollHeight - prevH);
          });
        })
        .catch(() => {
          patchStore(key, (s) => ({ ...s, loadingMore: false }));
        });
    },
    [taskId, store, patchStore],
  );

  // ── task token total (whole task, all agents) ───────────────────────────────────
  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .tokenStats(taskId)
        .then((r) => {
          if (alive) setTaskTokens(r.total);
        })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [taskId]);

  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api.interceptTask(taskId)
        .then((rows) => { if (alive) setPendingIntercepts(rows.filter((r) => r.status === "pending")); })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => { alive = false; clearInterval(t); };
  }, [taskId]);

  // Re-render on a timer so "streaming" liveness recomputes as activity goes stale.
  const [, setTick] = React.useState(0);
  React.useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1500);
    return () => clearInterval(id);
  }, []);

  // ── first load + single task SSE ────────────────────────────────────────────────
  // On task open: load Main's latest page, take the task-level snapshot cursor from
  // it, THEN open ONE task SSE at since=snapshot_cursor. History covers id≤cursor and
  // the SSE covers id>cursor with no gap. The SSE tails ALL agents; frames are routed
  // by session_key. EventSource auto-reconnects and (via our `id:` lines → Last-Event-
  // ID) resumes from the DB, so a dropped realtime link self-heals; seq-merge dedups.
  React.useEffect(() => {
    setStore({});
    setSseLive(false);
    setActiveId(MAIN_ID); // a stale worker id from the previous task must not leak in
    snapshotRef.current = 0;
    reqTokenRef.current = {};
    // Reserve "main" so the active-session effect (which also fires for main on mount)
    // won't double-load it and pre-empt the SSE opened here.
    loadingKeysRef.current = new Set(["main"]);
    let alive = true;

    // MOCK demo: no SSE backend — pull one activity snapshot and bucket by session.
    if (MOCK) {
      api
        .activity(taskId)
        .then((r) => {
          if (!alive) return;
          const buckets: SessionStore = {};
          for (const a of r.items) {
            const k = sessionKeyOf(a);
            if (!buckets[k]) buckets[k] = emptyState();
            buckets[k].items.push(a);
          }
          for (const k of Object.keys(buckets)) {
            const st = buckets[k];
            st.items.sort((p, q) => p.seq - q.seq);
            st.loaded = true;
            st.hasMore = false;
            st.earliestSeq = st.items.length ? st.items[0].seq : 0;
            st.lastTs = st.items.length ? st.items[st.items.length - 1].ts : "";
          }
          buckets.main ??= { ...emptyState(), loaded: true };
          buckets.system ??= { ...emptyState(), loaded: true };
          setStore(buckets);
        })
        .catch(() =>
          setStore({
            main: { ...emptyState(), loaded: true },
            system: { ...emptyState(), loaded: true },
          }),
        );
      return () => {
        alive = false;
      };
    }

    const token = (reqTokenRef.current.main ?? 0) + 1;
    reqTokenRef.current.main = token;
    patchStore("main", (s) => ({ ...s, loading: true }));
    api
      .activityHistory(taskId, "main", 0, PAGE)
      .then((r) => {
        if (!alive || reqTokenRef.current.main !== token) return;
        snapshotRef.current = r.snapshotCursor;
        patchStore("main", (s) => {
          const items = mergeBySeq(r.items, s.items);
          return {
            ...s,
            items,
            loaded: true,
            loading: false,
            hasMore: r.hasMore,
            earliestSeq: items.length ? items[0].seq : 0,
            unread: 0,
          };
        });
        // Open the single task SSE from the snapshot cursor.
        const es = new EventSource(
          sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${snapshotRef.current}`),
        );
        esRef.current = es;
        es.onopen = () => setSseLive(true);
        es.onerror = () => setSseLive(false); // EventSource auto-reconnects; DB compensates the gap
        es.onmessage = (e) => {
          let a: Activity;
          try {
            a = JSON.parse(e.data) as Activity;
          } catch {
            return; // ignore malformed frame
          }
          if ((a.kind === "llm_switch" || a.kind === "llm_failover") && !llmToastSeqRef.current.has(a.seq)) {
            llmToastSeqRef.current.add(a.seq);
            const transition = a.metadata?.llm_transition;
            if (transition?.mode === "exhausted" || a.is_error) {
              toast.error(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            } else if (transition?.mode === "automatic") {
              toast.success(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            } else {
              toast.info(a.summary, { id: `task-${taskId}-llm-${a.seq}` });
            }
          }
          const k = sessionKeyOf(a);
          setStore((prev) => {
            const cur = prev[k] ?? emptyState();
            const activeK = activeKeyRef.current;
            // Merge into sessions that are loaded, actively loading, or the current
            // view (so returning is instant + a frame that lands mid-load isn't lost).
            // A cold, inactive session only tracks liveness + unread until it's opened.
            if (!cur.loaded && !cur.loading && k !== activeK) {
              return { ...prev, [k]: { ...cur, lastTs: a.ts, unread: cur.unread + 1 } };
            }
            let items = mergeBySeq(cur.items, [a]);
            // Memory bound: trim oldest when over cap (older re-fetched on scroll-up),
            // but never while the user is reading this session's history (scrolled up).
            let hasMore = cur.hasMore;
            let earliestSeq = cur.earliestSeq;
            const trimmable = k !== activeK || atBottomRef.current;
            if (trimmable && items.length > MAX_KEEP) {
              items = items.slice(items.length - MAX_KEEP);
              hasMore = true;
              earliestSeq = items[0].seq;
            }
            const unread = k === activeK ? 0 : cur.unread + 1;
            return { ...prev, [k]: { ...cur, items, lastTs: a.ts, unread, hasMore, earliestSeq } };
          });
        };
      })
      .catch((err) => {
        if (!alive || reqTokenRef.current.main !== token) return;
        patchStore("main", (s) => ({ ...s, loading: false, error: (err as Error).message || "加载失败" }));
      })
      .finally(() => loadingKeysRef.current.delete("main"));

    return () => {
      alive = false;
      esRef.current?.close();
      esRef.current = null;
    };
  }, [taskId, patchStore]);

  // ── worker (intent) session list — paged, poll first page lightly ───────────────
  React.useEffect(() => {
    let active = true;
    const load = () =>
      api
        .intentsPage(taskId, 0, 300)
        .then((r) => {
          if (!active) return;
          setIntents(r.items);
          setIntentsHasMore(r.hasMore);
        })
        .catch(() => {
          // Polling is best-effort; the next interval retries automatically.
        });
    void load();
    const t = setInterval(load, 5000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [taskId]);

  const loadOlderIntents = React.useCallback(() => {
    if (loadingOlderIntents) return;
    const all = [...intents, ...olderIntents];
    const minId = all.reduce((m, n) => Math.min(m, Number(n.id)), Infinity);
    if (!Number.isFinite(minId)) return;
    setLoadingOlderIntents(true);
    api
      .intentsPage(taskId, minId, 300)
      .then((r) => {
        setOlderIntents((prev) => {
          const seen = new Set([...intents, ...prev].map((n) => n.id));
          return [...prev, ...r.items.filter((n) => !seen.has(n.id))];
        });
        setIntentsHasMore(r.hasMore);
      })
      .catch(() => {
        // A later manual retry can fetch this page again.
      })
      .finally(() => setLoadingOlderIntents(false));
  }, [taskId, intents, olderIntents, loadingOlderIntents]);

  // Combined, de-duplicated worker list (newest first page + older loaded pages).
  const allIntents = React.useMemo(() => {
    const byId = new Map<string, TaskNode>();
    for (const n of olderIntents) byId.set(n.id, n);
    for (const n of intents) byId.set(n.id, n); // fresh poll wins over older snapshot
    return [...byId.values()].sort((a, b) => Number(b.id) - Number(a.id));
  }, [intents, olderIntents]);

  const workerSessions = React.useMemo(
    () => allIntents.map(intentToSession),
    [allIntents],
  );

  // For each worker session (intent), derive the display title from the intent
  // payload summary. Also store the full TaskNode for the hover-JSON tooltip.
  const sessionMeta = React.useMemo(() => {
    const map = new Map<string, { title: string; json: unknown }>();
    for (const node of allIntents) {
      let title = `Intent ${node.id}`;
      let parsedPayload: unknown = node.payload;
      if (node.payload) {
        try {
          const p = JSON.parse(node.payload);
          parsedPayload = p;
          if (p?.summary) title = String(p.summary);
        } catch {
          title = node.payload.trim() || title;
        }
      }
      const json = { ...node, payload: parsedPayload };
      map.set(node.id, { title, json });
    }
    return map;
  }, [allIntents]);

  // Returns true if any pending intercept belongs to this session.
  // Worker agent_name format: "work#N · #intentID". Main/planner match by role key.
  const hasPendingForSession = React.useCallback(
    (s: Session): boolean => {
      if (s.inherited) return false;
      if (!pendingIntercepts.length) return false;
      if (s.role === "mainagent") return pendingIntercepts.some((r) => r.agent_name === "mainagent");
      if (s.role === "planner")   return pendingIntercepts.some((r) => r.agent_name === "planner");
      // Worker: extract intent ID from "work#N · #<intentID>"
      return pendingIntercepts.some((r) => {
        const m = r.agent_name.match(/·\s*#(\d+)$/);
        return m ? m[1] === s.intent_id : false;
      });
    },
    [pendingIntercepts],
  );

  // Liveness from each session's last-seen activity time (kept fresh by the 1.5s tick).
  const recentLive = React.useCallback(
    (key: string) => {
      const ts = store[key]?.lastTs;
      if (!ts) return false;
      const t = Date.parse(ts);
      return t > 0 && Date.now() - t < STREAM_WINDOW_MS;
    },
    [store],
  );
  const mainLive = recentLive("main");
  const plannerLive = recentLive("plan");

  const sessions = React.useMemo(
    () => [
      { ...MAIN_SESSION, live: mainLive },
      { ...PLANNER_SESSION, live: plannerLive },
      ...workerSessions,
      SYSTEM_SESSION,
    ],
    [workerSessions, mainLive, plannerLive],
  );

  const grouped = {
    mainagent: sessions.filter((s) => s.role === "mainagent"),
    planner: sessions.filter((s) => s.role === "planner"),
    worker: sessions.filter((s) => s.role === "worker"),
    system: sessions.filter((s) => s.role === "system"),
  };

  const active = sessions.find((s) => s.id === activeId) ?? MAIN_SESSION;
  const isMain = active.role === "mainagent";
  const isPlanner = active.role === "planner";
  const isSystem = active.role === "system";
  const activeKey = keyForSession(active);
  const activeState = store[activeKey];

  // Keep the SSE dispatcher's notion of the active session current, and lazily load
  // + clear unread whenever the active session changes.
  // biome-ignore lint/correctness/useExhaustiveDependencies: cache updates must not reactivate the current session.
  React.useEffect(() => {
    activeKeyRef.current = activeKey;
    const st = store[activeKey];
    if (!st || (!st.loaded && !st.loading)) {
      loadSession(activeKey);
    } else if (st.unread) {
      patchStore(activeKey, (s) => ({ ...s, unread: 0 }));
    }
  }, [activeKey]);

  // Main agent is the human↔orchestrator CONSOLE: only the conversation (user msgs +
  // the main agent's own replies/steps). Planner session shows planner steps; a
  // worker session shows only its intent's activity, led by the intent objective.
  const activity = React.useMemo(() => {
    const items = activeState?.items ?? [];
    // The main-agent console renders purely from server data: the human turn is
    // persisted+broadcast by the backend BEFORE it returns, so it arrives over the
    // same SSE stream (worker="mainagent") as every agent step — no client-side
    // optimistic echo, hence no fabricated seq that could collide with real DB ids.
    if (isMain) return items;
    if (isPlanner || isSystem) return items;
    // Worker session: the intent leads the transcript as a right-aligned "user"-style
    // message (the task handed to this worker), followed by its execution steps.
    const intentTitle = sessionMeta.get(active.id)?.title ?? active.title;
    const intentMsg: Activity = {
      seq: -1, // sorts/leads before any real step (real seq ≥ 0)
      worker: items[0]?.worker ?? active.id, // reuse the lane so no worker chips appear
      ts: active.last_activity || "",
      kind: "intent", // LLM-generated objective — rendered as a distinct (non-human) bubble
      summary: intentTitle,
      source_task_id: active.source_task_id,
      inherited: active.inherited,
    };
    return [intentMsg, ...items];
  }, [
    isMain,
    isPlanner,
    isSystem,
    activeState,
    active.id,
    active.title,
    active.last_activity,
    active.source_task_id,
    active.inherited,
    sessionMeta,
  ]);

  // seq of this session's most-recent TodoWrite call — for the Todo popover.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = activity.length - 1; i >= 0; i--) {
      const a = activity[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [activity]);

  // Per-session token total, live. Sum COMPLETED runs' results + the current run's
  // latest usage. Computed over the loaded pages (the running/just-finished run whose
  // totals matter sits on the latest page). (activity is seq-sorted.)
  const tokenTotal = React.useMemo(() => {
    let i = 0, o = 0, cr = 0, cw = 0; // completed
    let li = 0, lo = 0, lcr = 0, lcw = 0; // live in-progress
    for (const a of activity) {
      if (a.kind === "result") {
        i += a.input_tokens ?? 0;
        o += a.output_tokens ?? 0;
        cr += a.cache_read_tokens ?? 0;
        cw += a.cache_write_tokens ?? 0;
        li = lo = lcr = lcw = 0; // run finished → drop its live partial
      } else if (a.kind === "usage") {
        li = a.input_tokens ?? 0;
        lo = a.output_tokens ?? 0;
        lcr = a.cache_read_tokens ?? 0;
        lcw = a.cache_write_tokens ?? 0;
      }
    }
    const I = i + li, O = o + lo, CR = cr + lcr, CW = cw + lcw;
    return { i: I, o: O, cr: CR, cw: CW, any: I + O + CR + CW > 0 };
  }, [activity]);

  // Run duration = span from this session's first step to its last (for a live
  // session, "now" so it ticks up — the 1.5s setTick above re-renders it).
  const runDuration = React.useMemo(() => {
    let min = Infinity, max = 0;
    for (const a of activity) {
      const t = Date.parse(a.ts);
      if (!Number.isFinite(t)) continue;
      if (t < min) min = t;
      if (t > max) max = t;
    }
    if (!Number.isFinite(min) || max === 0) return null;
    const end = active.live ? Date.now() : max;
    return Math.max(0, end - min);
  }, [activity, active.live]);

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const viewport = React.useCallback(
    () =>
      (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ??
      null,
    [],
  );
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally rebinds the scroll listener.
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
      if (vp.scrollTop <= 80) loadEarlier(activeKeyRef.current, viewport); // near top → older page
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, activeId, loadEarlier]);
  // open/switch a session → jump to the latest (bottom)
  // biome-ignore lint/correctness/useExhaustiveDependencies: activeId intentionally scrolls a newly selected session.
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [activeId, viewport]);
  // new activity → stick to bottom only if the user is already pinned there
  // biome-ignore lint/correctness/useExhaustiveDependencies: activity growth intentionally drives live-edge scrolling.
  React.useLayoutEffect(() => {
    if (!atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [activity, viewport]);

  function stop() {
    if (stopping) return;
    setStopping(true);
    void api.stopChat(taskId).finally(() => setStopping(false));
  }

  function send() {
    const text = input.trim();
    const atts = attachments;
    if ((!text && atts.length === 0) || sending) return;
    // No optimistic echo: the backend persists+broadcasts the human turn before it
    // returns, so it streams back over SSE (worker="mainagent") with its real DB
    // seq — the transcript renders it from server data like every other step. Clear
    // the composer eagerly for responsiveness; restore it if the send fails.
    setInput("");
    setAttachments([]);
    setSending(true);
    api
      .chat(text, taskId, atts.length > 0 ? atts : undefined)
      .catch((e) => {
        setInput(text); // restore so the user doesn't lose their text / attachments
        setAttachments(atts);
        toast.error("发送失败：" + ((e as Error).message || "请稍后重试"));
      })
      .finally(() => setSending(false));
  }

  const mainLoaded = !!store.main?.loaded;
  // What the transcript pane should show for the active session.
  const showLoader = !activeState || (activeState.loading && !activeState.loaded);

  return (
    <TooltipProvider delayDuration={300}>
    <div className="grid h-[calc(100vh-13rem)] grid-cols-1 gap-4 lg:grid-cols-[18rem_1fr]">
      {/* Left: session list */}
      <div className="flex flex-col overflow-hidden rounded-lg border bg-card">
        <div className="border-b px-3 py-2">
          <div className="flex items-center justify-between">
            <div className="text-xs font-medium text-muted-foreground">会话列表</div>
            {!MOCK && (
              <span
                className={cn(
                  "inline-flex items-center gap-1 text-[10px]",
                  sseLive ? "text-emerald-500" : "text-amber-500",
                )}
                title={sseLive ? "实时连接正常" : "实时连接中断，正在自动重连（历史仍可见）"}
              >
                {sseLive ? (
                  <>
                    <span className="size-1 animate-pulse rounded-full bg-emerald-500" />
                    实时
                  </>
                ) : (
                  <>
                    <WifiOffIcon className="size-3" />
                    重连中
                  </>
                )}
              </span>
            )}
          </div>
          {taskTokens && (
            <div
              className="mt-1 text-[11px] tabular-nums text-muted-foreground"
              title="整个任务所有 agent 的 token 合计"
            >
              任务总计 · input {fmtTokens(taskTokens.input_tokens)} · cache{" "}
              {fmtTokens(taskTokens.cache_read_tokens)} · output {fmtTokens(taskTokens.output_tokens)}
            </div>
          )}
        </div>
        <ScrollArea type="auto" className="min-h-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!">
          <div className="flex w-full flex-col gap-3 p-2">
            {(["mainagent", "planner", "system", "worker"] as const).map((role) => {
              const items = grouped[role];
              if (!items.length) return null;
              const Meta = roleMeta[role];
              return (
                <div key={role} className="flex flex-col gap-0.5">
                  <div className="flex items-center gap-1.5 px-2 py-1 text-xs font-medium text-muted-foreground">
                    <Meta.icon className="size-3.5" />
                    {Meta.label}
                  </div>
                  {items.map((s) => {
                    const meta = s.role === "worker" ? sessionMeta.get(s.id) : undefined;
                    return (
                      <SessionItem
                        key={s.id}
                        s={s}
                        active={s.id === activeId}
                        displayTitle={meta?.title ?? s.title}
                        hasPending={hasPendingForSession(s)}
                        unread={store[keyForSession(s)]?.unread}
                        onClick={() => setActiveId(s.id)}
                        controlling={controllingIntent === s.intent_id}
                        onPause={() => void controlWorker(s, "pause")}
                        onResume={() => void controlWorker(s, "resume")}
                        onCancel={() => setCancelIntent(s)}
                      />
                    );
                  })}
                  {role === "worker" && intentsHasMore && (
                    <button
                      type="button"
                      onClick={loadOlderIntents}
                      disabled={loadingOlderIntents}
                      className="mt-0.5 flex items-center justify-center gap-1 rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent/50"
                    >
                      {loadingOlderIntents ? (
                        <Loader2Icon className="size-3.5 animate-spin" />
                      ) : (
                        <RotateCwIcon className="size-3.5" />
                      )}
                      加载更早的 Worker
                    </button>
                  )}
                </div>
              );
            })}
            {mainLoaded && !workerSessions.length && (
              <div className="px-2 py-1 text-xs text-muted-foreground">
                暂无运行中的 Worker 会话。
              </div>
            )}
          </div>
        </ScrollArea>
      </div>

      {/* Right: transcript */}
      <div className="flex min-w-0 flex-col overflow-hidden rounded-lg border bg-card">
        <div className="flex items-center gap-2 border-b px-4 py-2.5">
          {(() => {
            const isWorker = active.role === "worker";
            const meta = isWorker ? sessionMeta.get(active.id) : undefined;
            // Worker: the intent moved into the transcript as a message, so the
            // header shows a stable generic label (intent JSON stays on hover).
            const title = isWorker ? "Worker 执行会话" : active.title;
            const titleEl = <span className="text-sm font-medium">{title}</span>;
            return meta?.json ? (
              <Tooltip>
                <TooltipTrigger asChild>{titleEl}</TooltipTrigger>
                <TooltipContent side="bottom" align="start" className="max-h-80 max-w-sm overflow-auto p-0">
                  <pre className="p-2 text-[10px] leading-relaxed">
                    {JSON.stringify(meta.json, null, 2)}
                  </pre>
                </TooltipContent>
              </Tooltip>
            ) : titleEl;
          })()}
          {active.inherited && active.source_task_id && (
            <Badge variant="outline">
              来源任务 #{active.source_task_id} · 只读历史
            </Badge>
          )}
          {active.live && (
            <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
              <span className="size-1 animate-pulse rounded-full bg-blue-500" />
              实时
            </span>
          )}
          {activeState?.hasMore && (
            <span className="text-[10px] text-muted-foreground" title="向上滚动加载更早历史">
              ↑ 更早历史
            </span>
          )}
          <div className="ml-auto flex items-center gap-3 text-xs text-muted-foreground">
            {tokenTotal.any && (
              <span title="input / cache(read) / output tokens">
                input {fmtTokens(tokenTotal.i)} · cache {fmtTokens(tokenTotal.cr)} · output {fmtTokens(tokenTotal.o)}
              </span>
            )}
            {runDuration != null && (
              <span
                className="inline-flex items-center gap-1"
                title="运行时长（首步 → 末步）"
              >
                <ClockIcon className="size-3" />
                {fmtDuration(runDuration)}
              </span>
            )}
            {isMain && <span>可交互</span>}
          </div>
        </div>
        {/* Force Radix's internal viewport wrapper (display:table, sizes to content)
            to block so wide/unbreakable steps (long commands, code, URLs) can't blow
            out the width and defeat the truncation below — the transcript wraps to
            the panel instead of overflowing horizontally. */}
        <ScrollArea
          type="auto"
          className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!"
        >
          <div className="min-w-0 max-w-full p-4" ref={contentRef}>
            {activeState?.loadingMore && (
              <div className="flex items-center justify-center gap-2 pb-2 text-xs text-muted-foreground">
                <Loader2Icon className="size-3.5 animate-spin" />
                加载更早历史…
              </div>
            )}
            {showLoader ? (
              <div className="flex items-center gap-2 pl-9 text-xs text-muted-foreground">
                <Loader2Icon className="size-3.5 animate-spin" />
                加载活动流…
              </div>
            ) : activeState?.error ? (
              <div className="flex items-center gap-2 pl-9 text-xs text-red-500">
                <CircleXIcon className="size-3.5" />
                加载失败：{activeState.error}
                <Button size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => loadSession(activeKey)}>
                  重试
                </Button>
              </div>
            ) : activity.length ? (
              <Transcript activity={activity} live={active.live} taskId={taskId} chat={isMain} />
            ) : (
              <div className="pl-9 text-xs text-muted-foreground">
                {isMain
                  ? "还没有对话。在下方给主 Agent 发消息，引导探索方向或介入流程。"
                  : "暂无活动记录。"}
              </div>
            )}
          </div>
        </ScrollArea>
        {isMain ? (
          <div className="border-t p-3">
            {attachments.length > 0 && (
              <div className="mb-2 flex flex-wrap gap-1.5">
                {attachments.map((a) => (
                  <div
                    key={a.path}
                    className="flex items-center gap-1.5 rounded-md border bg-muted/50 px-2 py-1 text-xs"
                    title={a.path}
                  >
                    <PaperclipIcon className="size-3 shrink-0 text-primary" />
                    <span className="max-w-[160px] truncate">{a.name}</span>
                    <span className="text-muted-foreground">{fmtBytes(a.size)}</span>
                    <button
                      type="button"
                      className="ml-0.5 text-muted-foreground hover:text-foreground"
                      onClick={() => setAttachments((p) => p.filter((x) => x.path !== a.path))}
                      title="移除"
                    >
                      <XIcon className="size-3" />
                    </button>
                  </div>
                ))}
              </div>
            )}
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => void pickFiles(e.target.files)}
            />
            <InputGroup className="min-h-9 items-end has-disabled:opacity-100">
              <InputGroupAddon align="inline-start" className="self-end">
                <InputGroupButton
                  size="icon-xs"
                  variant="ghost"
                  onClick={() => fileInputRef.current?.click()}
                  disabled={mainLive || uploading}
                  title="上传文件"
                  aria-label="上传文件"
                >
                  {uploading ? <Loader2Icon className="animate-spin" /> : <PaperclipIcon />}
                </InputGroupButton>
              </InputGroupAddon>
              <InputGroupTextarea
                rows={1}
                aria-label="给主 Agent 发消息"
                placeholder="给主 Agent 发消息，引导探索方向…"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key !== "Enter" || e.shiftKey || e.nativeEvent.isComposing) return;
                  e.preventDefault();
                  if (!mainLive) send();
                }}
                disabled={mainLive}
                className="max-h-36 min-h-9 overflow-y-auto"
              />
              <InputGroupAddon align="inline-end" className="self-end">
                {mainLive ? (
                  <InputGroupButton
                    size="icon-xs"
                    variant="destructive"
                    onClick={stop}
                    disabled={stopping}
                    title="停止当前执行"
                    aria-label="停止当前执行"
                  >
                    {stopping ? <Loader2Icon className="animate-spin" /> : <SquareIcon />}
                  </InputGroupButton>
                ) : (
                  <InputGroupButton
                    size="icon-xs"
                    variant="default"
                    onClick={send}
                    disabled={(!input.trim() && attachments.length === 0) || sending}
                    title="发送消息"
                    aria-label="发送消息"
                  >
                    {sending ? <Loader2Icon className="animate-spin" /> : <SendIcon />}
                  </InputGroupButton>
                )}
              </InputGroupAddon>
            </InputGroup>
          </div>
        ) : (
          <div className="flex items-center border-t px-4 py-2">
            <TodoPopover
              seq={latestTodoSeq}
              fetchDetail={(seq) => api.activityDetail(seq, taskId).then((r) => r.detail ?? "")}
            />
          </div>
        )}
      </div>
      <AlertDialog open={cancelIntent !== null} onOpenChange={(open) => !open && setCancelIntent(null)}>
        <AlertDialogContent className="max-w-[min(32rem,calc(100vw-2rem))]">
          <AlertDialogHeader>
            <AlertDialogTitle>取消 Worker #{cancelIntent?.intent_id}？</AlertDialogTitle>
            <AlertDialogDescription className="break-words whitespace-normal">
              取消后将删除该意图、执行记录，以及该意图直接登记的事实和漏洞；全局资产、流量和其他 Planner 意图不会删除。此操作不可撤销。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>返回</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={!cancelIntent || controllingIntent !== null}
              onClick={() => cancelIntent && void controlWorker(cancelIntent, "cancel")}
            >
              {controllingIntent ? <Loader2Icon className="animate-spin" /> : <Trash2Icon />}
              确认取消并清理
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
    </TooltipProvider>
  );
}
