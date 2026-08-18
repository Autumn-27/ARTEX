"use client";

import * as React from "react";
import {
  BugIcon,
  CompassIcon,
  FlagIcon,
  FlaskConicalIcon,
  LightbulbIcon,
  type LucideIcon,
  MapPinIcon,
  CornerDownRightIcon,
} from "lucide-react";

import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";

// viewKind renders the task-root fact (origin) as a distinct "起点".
type ViewKind = ExploreKind | "begin";
function viewKind(n: TaskNode): ViewKind {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

const KIND_META: Record<ViewKind, { label: string; icon: LucideIcon; cls: string }> = {
  begin: { label: "起点", icon: MapPinIcon, cls: "text-slate-500 border-slate-500/30 bg-slate-500/10" },
  task: { label: "任务", icon: MapPinIcon, cls: "text-slate-500 border-slate-500/30 bg-slate-500/10" },
  goal: { label: "目标", icon: FlagIcon, cls: "text-violet-600 border-violet-500/30 bg-violet-500/10 dark:text-violet-400" },
  intent: { label: "意图", icon: CompassIcon, cls: "text-blue-600 border-blue-500/30 bg-blue-500/10 dark:text-blue-400" },
  fact: { label: "事实", icon: FlaskConicalIcon, cls: "text-amber-600 border-amber-500/30 bg-amber-500/10 dark:text-amber-400" },
  finding: { label: "漏洞", icon: BugIcon, cls: "text-rose-600 border-rose-500/30 bg-rose-500/10 dark:text-rose-400" },
  hint: { label: "提示", icon: LightbulbIcon, cls: "text-emerald-600 border-emerald-500/30 bg-emerald-500/10 dark:text-emerald-400" },
};

const REL_LABEL: Record<string, string> = {
  spawns: "派生",
  derived_from: "意图链",
  yields: "产出",
  proves: "证明",
};

const SUMMARY_FIELDS: Record<ViewKind, string[]> = {
  task: ["summary", "description"],
  begin: ["summary", "description"],
  goal: ["text"],
  hint: ["text"],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["name", "summary"],
};

// nodeSummary pulls the display text out of the node's JSON payload.
function nodeSummary(n: TaskNode): string {
  const raw = n.payload ?? "";
  const fields = SUMMARY_FIELDS[viewKind(n)] ?? [];
  if (fields.length && raw.trim()) {
    try {
      const obj: unknown = JSON.parse(raw);
      if (obj && typeof obj === "object") {
        for (const f of fields) {
          const v = (obj as Record<string, unknown>)[f];
          if (typeof v === "string" && v.trim()) return v;
        }
      }
    } catch {
      /* 非 JSON：原样显示 */
    }
  }
  return raw || "（无内容）";
}

// orderByDepth topologically layers the lineage nodes by their longest distance
// from a root (a node with no incoming edge inside the set), so the chain renders
// origin → … → finding top-down. Cycle-safe (caps iterations).
function orderByDepth(nodes: TaskNode[], edges: Edge[]): TaskNode[] {
  const ids = new Set(nodes.map((n) => n.id));
  const parents = new Map<string, string[]>();
  for (const e of edges) {
    if (ids.has(e.src) && ids.has(e.dst)) {
      parents.set(e.dst, [...(parents.get(e.dst) ?? []), e.src]);
    }
  }
  const depth = new Map<string, number>();
  const visiting = new Set<string>();
  const calc = (id: string): number => {
    const cached = depth.get(id);
    if (cached !== undefined) return cached;
    if (visiting.has(id)) return 0; // 回边：不再深入
    visiting.add(id);
    const ps = parents.get(id) ?? [];
    const d = ps.length === 0 ? 0 : 1 + Math.max(...ps.map(calc));
    visiting.delete(id);
    depth.set(id, d);
    return d;
  };
  return [...nodes].sort((a, b) => {
    const d = calc(a.id) - calc(b.id);
    return d !== 0 ? d : Number(a.id) - Number(b.id);
  });
}

export function FindingLineageView({ findingId }: { findingId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const [loaded, setLoaded] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    api
      .findingLineage(findingId)
      .then((g) => {
        if (!alive) return;
        setNodes(g.nodes);
        setEdges(g.edges);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [findingId]);

  const ordered = React.useMemo(() => orderByDepth(nodes, edges), [nodes, edges]);
  const byId = React.useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);
  // 每个节点的入边(父节点 + 关系),用于在卡片上方标注"从哪来、什么关系"。
  const incoming = React.useMemo(() => {
    const m = new Map<string, { from: string; rel: string }[]>();
    const ids = new Set(nodes.map((n) => n.id));
    for (const e of edges) {
      if (ids.has(e.src) && ids.has(e.dst)) {
        m.set(e.dst, [...(m.get(e.dst) ?? []), { from: e.src, rel: e.rel }]);
      }
    }
    return m;
  }, [nodes, edges]);

  if (!loaded) {
    return <p className="text-muted-foreground p-6 text-sm">加载中…</p>;
  }
  if (nodes.length === 0) {
    return (
      <p className="text-muted-foreground p-6 text-sm">
        无链路可展示（该漏洞未关联探索节点，或所属任务已删除）。
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-0">
      <p className="text-muted-foreground mb-3 text-xs">
        从任务初始节点回溯到本漏洞节点的探索链路（共 {nodes.length} 个节点）。
      </p>
      {ordered.map((n, i) => {
        const vk = viewKind(n);
        const meta = KIND_META[vk] ?? KIND_META.fact;
        const Icon = meta.icon;
        const isFinding = vk === "finding";
        const ins = incoming.get(n.id) ?? [];
        return (
          <div key={n.id} className="flex flex-col">
            {/* 入边关系(除根外)：从哪个父节点、什么关系而来 */}
            {i > 0 && ins.length > 0 && (
              <div className="text-muted-foreground ml-4 flex flex-col gap-0.5 border-l-2 py-1.5 pl-4 text-xs">
                {ins.map((e, k) => {
                  const parent = byId.get(e.from);
                  const pKind = parent ? viewKind(parent) : "fact";
                  return (
                    <div key={`${e.from}-${e.rel}-${k}`} className="flex items-center gap-1">
                      <CornerDownRightIcon className="size-3 shrink-0" />
                      <span className="rounded bg-muted px-1 py-0.5 font-medium">
                        {REL_LABEL[e.rel] ?? e.rel}
                      </span>
                      <span className="truncate">
                        来自 {KIND_META[pKind]?.label ?? pKind}
                        {parent && (
                          <span className="opacity-70"> · {nodeSummary(parent).slice(0, 40)}</span>
                        )}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
            {/* 节点卡片 */}
            <div
              className={cn(
                "rounded-md border p-3",
                isFinding ? "border-rose-500/60 bg-rose-500/5 ring-1 ring-rose-500/30" : "bg-card",
              )}
            >
              <div className="flex items-center gap-2">
                <span
                  className={cn(
                    "inline-flex shrink-0 items-center gap-1 rounded border px-1.5 py-0.5 text-xs font-medium",
                    meta.cls,
                  )}
                >
                  <Icon className="size-3" />
                  {meta.label}
                </span>
                {n.state && n.state !== "origin" && (
                  <span className="text-muted-foreground text-[10px] uppercase">{n.state}</span>
                )}
                {isFinding && (
                  <span className="text-rose-600 dark:text-rose-400 text-xs font-medium">← 本漏洞</span>
                )}
                <span className="text-muted-foreground ml-auto font-mono text-[10px]">#{n.id}</span>
              </div>
              <p className="mt-1.5 text-sm whitespace-pre-wrap">{nodeSummary(n)}</p>
            </div>
          </div>
        );
      })}
    </div>
  );
}
