"use client";

import * as React from "react";
import {
  RadioIcon,
  BrainIcon,
  UserIcon,
  Loader2Icon,
  ZapOffIcon,
  SendIcon,
  SquareIcon,
  ClockIcon,
  CircleCheckIcon,
  CircleXIcon,
  CircleSlashIcon,
  ShieldAlertIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Transcript } from "@/components/transcript";
import { TodoPopover } from "@/components/todo-popover";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
  InterceptApprovalRow,
  Session,
  SessionStatus,
  TaskNode,
  TokenTotal,
} from "@/lib/types";

// statusIcon maps a session status to its icon. Worker terminal states are
// distinct & color-coded: 完成(绿勾圈) / 取消停止(琥珀斜杠圈) / 出错(红叉圈) /
// 步数耗尽(紫). running=蓝色转圈, pending(待领取)=灰时钟.
function statusIcon(status: SessionStatus) {
  switch (status) {
    case "running": // 执行中
      return <Loader2Icon className="size-3.5 animate-spin text-blue-500" />;
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
} as const;

// The main-agent session is the interactive entry point of this tab and has no
// dedicated backend "sessions" endpoint — it is a fixed UI affordance whose
// transcript is the full live activity stream for the task.
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
// the planner emits (worker === "planner", which carries no intent_id since the
// planner is what generates intents).
const PLANNER_ID = "s-planner";
const PLANNER_SESSION: Session = {
  id: PLANNER_ID,
  role: "planner",
  title: "规划 Planner · 态势研判",
  status: "running",
  live: true,
  last_activity: "",
};

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
    live: state === "running",
    last_activity: n.ts,
    intent_id: n.id,
  };
}

function SessionItem({
  s,
  active,
  displayTitle,
  hasPending,
  onClick,
}: {
  s: Session;
  active: boolean;
  displayTitle: string;
  hasPending?: boolean;
  onClick: () => void;
}) {
  const icon =
    s.role === "worker" ? (
      statusIcon(s.status)
    ) : s.live ? (
      <Loader2Icon className="size-3.5 animate-spin text-blue-500" />
    ) : null;

  return (
    <button
      onClick={onClick}
      className={cn(
        "flex w-full items-center gap-1 rounded-md px-2 py-1.5 text-left text-sm transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {icon ?? <span className="size-3.5 shrink-0" />}
      {s.intent_id && (
        <span className="shrink-0 rounded bg-muted px-1 py-0.5 font-mono text-[10px] tabular-nums text-muted-foreground">
          #{s.intent_id}
        </span>
      )}
      <span className="min-w-0 flex-1 truncate">{displayTitle}</span>
      {hasPending && (
        <ShieldAlertIcon className="size-3.5 shrink-0 text-amber-500" />
      )}
      {s.live && (
        <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
          <span className="size-1 animate-pulse rounded-full bg-blue-500" />
          实时
        </span>
      )}
    </button>
  );
}

export function SessionsTab({ taskId }: { taskId: string }) {
  const [activeId, setActiveId] = React.useState(MAIN_ID);
  // Live activity stream — the primary data for this tab.
  const [allActivity, setAllActivity] = React.useState<Activity[]>([]);
  // Worker sessions derived from exploration intents.
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  // Local-only chat messages overlaid onto the main-agent transcript.
  const [chatExtra, setChatExtra] = React.useState<Activity[]>([]);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  // Whole-task token total (all agents), polled from the backend aggregate.
  const [taskTokens, setTaskTokens] = React.useState<TokenTotal | null>(null);
  // Pending intercept requests for this task — used to show warning icons on sessions.
  const [pendingIntercepts, setPendingIntercepts] = React.useState<InterceptApprovalRow[]>([]);

  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api
        .tokenStats(taskId)
        .then((r) => {
          if (alive) setTaskTokens(r.total);
        })
        .catch(() => {});
    load();
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
        .catch(() => {});
    load();
    const t = setInterval(load, 5000);
    return () => { alive = false; clearInterval(t); };
  }, [taskId]);

  // Re-render on a timer so "streaming" liveness recomputes as activity goes stale.
  const [, setTick] = React.useState(0);
  React.useEffect(() => {
    const id = setInterval(() => setTick((t) => t + 1), 1500);
    return () => clearInterval(id);
  }, []);
  // A session is "streaming" only if its agent emitted activity in the last few
  // seconds (i.e. SSE actually in flight) — not a hardcoded always-on flag.
  const STREAM_WINDOW_MS = 5000;
  const streamingFor = React.useCallback(
    (w: string) => {
      let latest = 0;
      for (const a of allActivity) {
        if (a.worker === w) {
          const t = Date.parse(a.ts);
          if (t > latest) latest = t;
        }
      }
      return latest > 0 && Date.now() - latest < STREAM_WINDOW_MS;
    },
    [allActivity],
  );
  const mainLive = streamingFor("mainagent");
  const plannerLive = streamingFor("planner");

  // Live activity stream via SSE: the endpoint replays history after since=0 then
  // tails each new step. EventSource auto-reconnects; on reconnect history replays
  // and the seq dedup below keeps it gap-free without duplicates.
  React.useEffect(() => {
    setAllActivity([]);
    setLoaded(false);
    // Mock demo：无 SSE 后端，一次性拉取活动快照（执行过程）。
    if (MOCK) {
      api
        .activity(taskId)
        .then((r) => setAllActivity(r.items))
        .catch(() => {})
        .finally(() => setLoaded(true));
      return;
    }
    const es = new EventSource(
      sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=0`),
    );
    es.onopen = () => setLoaded(true);
    es.onmessage = (e) => {
      try {
        const a = JSON.parse(e.data) as Activity;
        setAllActivity((prev) =>
          prev.some((x) => x.seq === a.seq)
            ? prev
            : [...prev, a].sort((p, q) => p.seq - q.seq),
        );
      } catch {
        /* ignore malformed frame */
      }
    };
    return () => es.close();
  }, [taskId]);

  // Worker session list derives from exploration intents — poll lightly (changes
  // far less often than the activity stream).
  React.useEffect(() => {
    let active = true;
    const load = () =>
      api.intents(taskId).then((nodes) => {
        if (active) setIntents(nodes);
      }).catch(() => {});
    load();
    const t = setInterval(load, 5000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [taskId]);

  const workerSessions = React.useMemo(
    () => intents.map(intentToSession),
    [intents],
  );

  // For each worker session (intent), derive the display title from the latest
  // activity summary (so the list shows what's actually happening, not the raw
  // intent payload). Fall back to payload when no activity exists yet.
  // Also store the full TaskNode for the hover-JSON tooltip.
  const sessionMeta = React.useMemo(() => {
    const map = new Map<string, { title: string; json: unknown }>();
    for (const node of intents) {
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
      // Hover JSON: full node with payload replaced by parsed object for readability
      const json = { ...node, payload: parsedPayload };
      map.set(node.id, { title, json });
    }
    return map;
  }, [intents]);

  // Returns true if any pending intercept belongs to this session.
  // Worker agent_name format: "work#N · #intentID". Main/planner match by role key.
  const hasPendingForSession = React.useCallback(
    (s: Session): boolean => {
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

  const sessions = React.useMemo(
    () => [
      { ...MAIN_SESSION, live: mainLive },
      { ...PLANNER_SESSION, live: plannerLive },
      ...workerSessions,
    ],
    [workerSessions, mainLive, plannerLive],
  );

  const grouped = {
    mainagent: sessions.filter((s) => s.role === "mainagent"),
    planner: sessions.filter((s) => s.role === "planner"),
    worker: sessions.filter((s) => s.role === "worker"),
  };

  const active = sessions.find((s) => s.id === activeId) ?? MAIN_SESSION;
  const isMain = active.role === "mainagent";
  const isPlanner = active.role === "planner";

  // Main agent is the human↔orchestrator CONSOLE: it shows only the conversation
  // (the user's messages + the main agent's own replies/steps), used to steer the
  // run — NOT the planner/worker activity firehose (those live in their own
  // sessions). The planner session shows only planner steps; a worker session
  // shows only the activity for its intent.
  const activity = React.useMemo(() => {
    if (isMain) {
      // The conversation is persisted server-side in the activity stream
      // (worker="mainagent"), so it survives reloads. chatExtra is only an
      // optimistic local echo — drop any entry the persisted (SSE) copy already
      // covers, so messages don't show twice once the backend round-trips.
      const mine = allActivity.filter((a) => a.worker === "mainagent");
      const seen = new Set(mine.map((a) => `${a.kind} ${a.summary}`));
      const pending = chatExtra.filter(
        (a) => !seen.has(`${a.kind} ${a.summary}`),
      );
      return [...mine, ...pending].sort((a, b) => a.seq - b.seq);
    }
    if (isPlanner) {
      return allActivity.filter((a) => a.worker === "planner");
    }
    // Worker session: the intent is NOT shown as the header title — instead it
    // leads the transcript as a right-aligned "user"-style message (the task
    // handed to this worker), followed by its execution steps.
    const steps = allActivity.filter(
      (a) => a.intent_id === active.intent_id || a.worker === active.id,
    );
    const intentTitle = sessionMeta.get(active.id)?.title ?? active.title;
    const intentMsg: Activity = {
      seq: -1, // sorts/leads before any real step (real seq ≥ 0)
      worker: steps[0]?.worker ?? active.id, // reuse the lane so no worker chips appear
      ts: active.last_activity || "",
      kind: "intent", // LLM-generated objective — rendered as a distinct (non-human) bubble
      summary: intentTitle,
    };
    return [intentMsg, ...steps];
  }, [isMain, isPlanner, allActivity, chatExtra, active.intent_id, active.id, active.title, active.last_activity, sessionMeta]);

  // seq of this session's most-recent TodoWrite call — for the Todo popover shown in
  // the worker/planner footer (in place of the old read-only replay notice).
  const latestTodoSeq = React.useMemo(() => {
    for (let i = activity.length - 1; i >= 0; i--) {
      const a = activity[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [activity]);

  // Per-session token total, live. Each run (one captureRunSession) reports
  // cumulative usage per model turn via kind='usage', then a final kind='result'.
  // Total = sum of COMPLETED runs' results + the current in-progress run's latest
  // usage. A 'result' adds to the sum and clears the live partial; a 'usage'
  // overwrites the live partial. So a running work counts up live; a finished one
  // shows its final result. (activity is seq-sorted.)
  const tokenTotal = React.useMemo(() => {
    let i = 0,
      o = 0,
      cr = 0,
      cw = 0; // completed
    let li = 0,
      lo = 0,
      lcr = 0,
      lcw = 0; // live in-progress
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
    const I = i + li,
      O = o + lo,
      CR = cr + lcr,
      CW = cw + lcw;
    return { i: I, o: O, cr: CR, cw: CW, any: I + O + CR + CW > 0 };
  }, [activity]);

  // Run duration = span from this session's first step to its last (for a live
  // session, "now" so it ticks up — the 1.5s setTick above re-renders it). Derived
  // purely from activity timestamps (Activity.ts), the only per-step time we have.
  const runDuration = React.useMemo(() => {
    let min = Infinity,
      max = 0;
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
  const atBottomRef = React.useRef(true);
  const viewport = React.useCallback(
    () =>
      (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ??
      null,
    [],
  );
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport, activeId]);
  // open/switch a session → jump to the latest (bottom)
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [activeId, viewport]);
  // new activity → stick to bottom only if the user is already pinned there
  React.useLayoutEffect(() => {
    if (!atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [activity, viewport]);

  function stop() {
    if (stopping) return;
    setStopping(true);
    api.stopChat(taskId).finally(() => setStopping(false));
  }

  function send() {
    const text = input.trim();
    if (!text || sending) return;
    const base = [...allActivity, ...chatExtra];
    const nextSeq = Math.max(0, ...base.map((a) => a.seq)) + 1;
    const userMsg: Activity = {
      seq: nextSeq,
      worker: "mainagent",
      ts: new Date().toISOString(),
      kind: "user",
      summary: text,
    };
    // optimistic echo of the human turn only; the agent's steps + final answer
    // stream back live via SSE (worker="mainagent"), so we don't append the reply.
    setChatExtra((prev) => [...prev, userMsg]);
    setInput("");
    setSending(true);
    api
      .chat(text, taskId)
      .catch(() => {
        setChatExtra((prev) => [
          ...prev,
          {
            seq: nextSeq + 1,
            worker: "mainagent",
            ts: new Date().toISOString(),
            kind: "text",
            summary: "（发送失败，请稍后重试）",
          },
        ]);
      })
      .finally(() => setSending(false));
  }

  return (
    <TooltipProvider delayDuration={300}>
    <div className="grid h-[calc(100vh-13rem)] grid-cols-1 gap-4 lg:grid-cols-[18rem_1fr]">
      {/* Left: session list */}
      <div className="flex flex-col overflow-hidden rounded-lg border bg-card">
        <div className="border-b px-3 py-2">
          <div className="text-xs font-medium text-muted-foreground">会话列表</div>
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
            {(["mainagent", "planner", "worker"] as const).map((role) => {
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
                        onClick={() => setActiveId(s.id)}
                      />
                    );
                  })}
                </div>
              );
            })}
            {loaded && !workerSessions.length && (
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
          {active.live && (
            <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
              <span className="size-1 animate-pulse rounded-full bg-blue-500" />
              实时
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
            {!loaded ? (
              <div className="flex items-center gap-2 pl-9 text-xs text-muted-foreground">
                <Loader2Icon className="size-3.5 animate-spin" />
                加载活动流…
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
          <div className="flex items-center gap-2 border-t p-3">
            <Input
              placeholder="给主 Agent 发消息，引导探索方向…"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !mainLive) send();
              }}
              disabled={mainLive}
            />
            {mainLive ? (
              <Button size="icon" variant="destructive" onClick={stop} disabled={stopping} title="停止当前执行">
                {stopping ? <Loader2Icon className="animate-spin" /> : <SquareIcon />}
              </Button>
            ) : (
              <Button size="icon" onClick={send} disabled={!input.trim() || sending}>
                {sending ? <Loader2Icon className="animate-spin" /> : <SendIcon />}
              </Button>
            )}
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
    </div>
    </TooltipProvider>
  );
}
