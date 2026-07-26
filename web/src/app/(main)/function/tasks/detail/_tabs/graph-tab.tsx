"use client";
"use no memo";

import * as React from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge as RFEdge,
  Handle,
  MarkerType,
  MiniMap,
  type Node as RFNode,
  type NodeProps,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import {
  Bug,
  Compass,
  Flag,
  FlaskConical,
  Lightbulb,
  type LucideIcon,
  Target,
} from "lucide-react";

import { cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { toneClasses, type Tone } from "@/lib/status";
import { api } from "@/lib/api";
import type { Edge, ExploreKind, TaskNode } from "@/lib/types";

const relLabel: Record<string, string> = {
  spawns: "派生",
  derived_from: "意图链",
  yields: "产出",
  proves: "证明",
};

const relColor: Record<string, string> = {
  spawns: "#10b981",
  derived_from: "#6366f1",
  yields: "#f59e0b",
  proves: "#ef4444",
};

type TypeMeta = {
  label: string;
  icon: LucideIcon;
  iconBg: string; // Dify 风格实心图标块底色
  chip: string; // 抽屉里用的柔和底色
  hex: string; // minimap / 连线取色
};

const typeMeta: Record<ExploreKind, TypeMeta> = {
  task: {
    label: "根任务",
    icon: Flag,
    iconBg: "bg-slate-500",
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
    hex: "#64748b",
  },
  begin: {
    label: "起点",
    icon: Flag,
    iconBg: "bg-slate-500",
    chip: "bg-slate-500/15 text-slate-600 dark:text-slate-300",
    hex: "#64748b",
  },
  goal: {
    label: "目标",
    icon: Target,
    iconBg: "bg-emerald-500",
    chip: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400",
    hex: "#10b981",
  },
  hint: {
    label: "提示",
    icon: Lightbulb,
    iconBg: "bg-violet-500",
    chip: "bg-violet-500/15 text-violet-600 dark:text-violet-400",
    hex: "#8b5cf6",
  },
  intent: {
    label: "意图",
    icon: Compass,
    iconBg: "bg-blue-500",
    chip: "bg-blue-500/15 text-blue-600 dark:text-blue-400",
    hex: "#3b82f6",
  },
  fact: {
    label: "事实",
    icon: FlaskConical,
    iconBg: "bg-amber-500",
    chip: "bg-amber-500/15 text-amber-600 dark:text-amber-400",
    hex: "#f59e0b",
  },
  finding: {
    label: "漏洞",
    icon: Bug,
    iconBg: "bg-rose-500",
    chip: "bg-rose-500/15 text-rose-600 dark:text-rose-400",
    hex: "#f43f5e",
  },
};

// The task root is now an origin fact (kind='fact', state='origin') rather than a
// dedicated 'begin' node. Render/layout it with the "起点" visual (the `begin`
// entry above) so the graph root still reads as the origin.
function viewKind(n: TaskNode): ExploreKind {
  return n.type === "fact" && n.state === "origin" ? "begin" : n.type;
}

function prioTone(p: number): Tone {
  return p >= 8 ? "red" : p >= 5 ? "amber" : "slate";
}

// 各类型节点在卡片上展示的字段：payload 通常是一段 JSON 字符串，
// 按类型取其中的 summary / text / description 作为显示内容。
const summaryFields: Record<ExploreKind, string[]> = {
  task: ["summary", "description"],
  begin: ["summary", "description"],
  goal: ["text"],
  hint: [],
  intent: ["summary"],
  fact: ["summary"],
  finding: ["summary"],
};

// 解析 payload 得到节点显示文案；解析失败或字段缺失时回退到原始 payload。
function nodeSummary(n: TaskNode): string {
  const raw = n.payload ?? "";
  const fields = summaryFields[viewKind(n)] ?? [];
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
      /* 非 JSON：按原始内容显示 */
    }
  }
  return raw;
}

const COL_W = 360;
const ROW_H = 140;

type ExploreNodeData = { node: TaskNode };
type ExploreRFNode = RFNode<ExploreNodeData, "explore">;

function pushMap(m: Map<string, string[]>, k: string, v: string) {
  const a = m.get(k);
  if (a) a.push(v);
  else m.set(k, [v]);
}

// 按连线的“递进深度”自动分层：顺着边从左往右排（意图→事实，事实/漏洞→再生意图…），
// 而不是按节点类型固定分列——这样反向/回环的边才不会互相缠绕。
//   1) DFS 断环：指向递归栈中节点的边视为回边，不参与分层（处理 意图↔事实 这类回环）；
//   2) 最长路径分层：谁在链路上更靠后，列就越靠右；
//   3) 重心排序：每层按相邻层邻居的平均位置重排，让有连线的节点对齐、减少交叉；
//   4) 每层整体垂直居中。
function computeLayout(
  nodes: TaskNode[],
  edges: Edge[],
): Map<string, { x: number; y: number }> {
  const order = nodes.map((n) => n.id);
  const idset = new Set(order);
  const valid = edges.filter((e) => idset.has(e.src) && idset.has(e.dst));
  const key = (s: string, d: string) => `${s}->${d}`;

  const out = new Map<string, string[]>();
  for (const e of valid) pushMap(out, e.src, e.dst);

  // 1) 迭代式 DFS 断环
  const seen = new Map<string, 0 | 1 | 2>();
  const back = new Set<string>();
  for (const root of order) {
    if ((seen.get(root) ?? 0) !== 0) continue;
    const stack: Array<{ u: string; i: number }> = [{ u: root, i: 0 }];
    seen.set(root, 1);
    while (stack.length) {
      const top = stack[stack.length - 1];
      const nbrs = out.get(top.u) ?? [];
      if (top.i >= nbrs.length) {
        seen.set(top.u, 2);
        stack.pop();
        continue;
      }
      const v = nbrs[top.i++];
      const s = seen.get(v) ?? 0;
      if (s === 1) back.add(key(top.u, v)); // 回边
      else if (s === 0) {
        seen.set(v, 1);
        stack.push({ u: v, i: 0 });
      }
    }
  }

  // 2) 最长路径分层（忽略回边）
  const dagSucc = new Map<string, string[]>();
  const indeg = new Map<string, number>(order.map((id) => [id, 0]));
  for (const e of valid) {
    if (back.has(key(e.src, e.dst))) continue;
    pushMap(dagSucc, e.src, e.dst);
    indeg.set(e.dst, (indeg.get(e.dst) ?? 0) + 1);
  }
  const layer = new Map<string, number>(order.map((id) => [id, 0]));
  const queue = order.filter((id) => (indeg.get(id) ?? 0) === 0);
  for (let qi = 0; qi < queue.length; qi++) {
    const u = queue[qi];
    for (const v of dagSucc.get(u) ?? []) {
      layer.set(v, Math.max(layer.get(v) ?? 0, (layer.get(u) ?? 0) + 1));
      const d = (indeg.get(v) ?? 0) - 1;
      indeg.set(v, d);
      if (d === 0) queue.push(v);
    }
  }

  // 3) 按层分列
  const maxLayer = Math.max(0, ...layer.values());
  const columns: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const id of order) columns[layer.get(id) ?? 0].push(id);

  // 重心排序：只统计相邻层之间的连线
  const colIndexOf = new Map<string, number>();
  columns.forEach((ids, c) => ids.forEach((id) => colIndexOf.set(id, c)));
  const neighbors = new Map<string, string[]>();
  for (const e of valid) {
    const cs = colIndexOf.get(e.src);
    const cd = colIndexOf.get(e.dst);
    if (cs === undefined || cd === undefined || Math.abs(cs - cd) !== 1) continue;
    pushMap(neighbors, e.src, e.dst);
    pushMap(neighbors, e.dst, e.src);
  }
  const sweep = (dir: 1 | -1) => {
    const start = dir === 1 ? 1 : columns.length - 2;
    const end = dir === 1 ? columns.length : -1;
    for (let c = start; c !== end; c += dir) {
      const ref = c - dir;
      const refIndex = new Map(columns[ref].map((id, i) => [id, i]));
      const scored = columns[c].map((id, i) => {
        const ns = (neighbors.get(id) ?? [])
          .map((nid) => refIndex.get(nid))
          .filter((v): v is number => v !== undefined);
        const bary = ns.length ? ns.reduce((a, b) => a + b, 0) / ns.length : i;
        return { id, bary, i };
      });
      scored.sort((a, b) => a.bary - b.bary || a.i - b.i);
      columns[c] = scored.map((s) => s.id);
    }
  };
  for (let iter = 0; iter < 4; iter++) {
    sweep(1);
    sweep(-1);
  }

  // 4) 垂直居中
  const maxCount = Math.max(1, ...columns.map((c) => c.length));
  const midline = ((maxCount - 1) * ROW_H) / 2;
  const pos = new Map<string, { x: number; y: number }>();
  columns.forEach((ids, c) => {
    const startY = midline - ((ids.length - 1) * ROW_H) / 2;
    ids.forEach((id, i) => pos.set(id, { x: c * COL_W, y: startY + i * ROW_H }));
  });
  return pos;
}

// Dify 工作流风格节点：白色圆角卡片 + 实心配色图标块 + 选中/运行蓝色描边。
function ExploreNode({ data, selected }: NodeProps<ExploreRFNode>) {
  const n = data.node;
  const kind = viewKind(n);
  const meta = typeMeta[kind] ?? typeMeta.intent;
  const Icon = meta.icon;
  const showState = n.type === "goal" || n.type === "intent";
  const showPriority = (n.type === "goal" || n.type === "intent") && n.priority > 0;
  const isLive = n.type === "intent" && n.state === "running";

  return (
    <div
      className={cn(
        "w-[240px] cursor-pointer rounded-2xl border-2 transition-colors",
        selected ? "border-blue-500" : isLive ? "border-blue-400/70" : "border-transparent",
      )}
    >
      <div className="group relative rounded-[14px] border border-black/[0.06] bg-white shadow-sm transition-shadow hover:shadow-lg dark:border-white/10 dark:bg-neutral-900">
        <Handle
          type="target"
          position={Position.Left}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
        {/* 头部：实心图标块 + 类型标题 + 状态点 / 优先级 */}
        <div className="flex items-center gap-2 px-3 pt-3 pb-1.5">
          <span
            className={cn(
              "flex size-6 shrink-0 items-center justify-center rounded-lg shadow-sm",
              meta.iconBg,
            )}
          >
            <Icon className="size-3.5 text-white" />
          </span>
          <span className="grow truncate text-[13px] font-semibold text-neutral-700 dark:text-neutral-100">
            {meta.label}
          </span>
          {isLive && (
            <span className="relative flex size-2 shrink-0">
              <span className="absolute inline-flex size-full animate-ping rounded-full bg-blue-400 opacity-70" />
              <span className="relative inline-flex size-2 rounded-full bg-blue-500" />
            </span>
          )}
          {showPriority && (
            <span
              className={cn(
                "shrink-0 rounded-md px-1.5 py-0.5 text-[10px] font-semibold tabular-nums",
                toneClasses[prioTone(n.priority)],
              )}
            >
              P{n.priority}
            </span>
          )}
        </div>
        {/* 摘要 */}
        <div className="line-clamp-3 px-3 pb-2 text-[13px] leading-snug text-neutral-500 dark:text-neutral-400">
          {nodeSummary(n) || meta.label}
        </div>
        {/* 底部：状态徽章 + 来源/ID */}
        <div className="flex items-center justify-between gap-2 px-3 pb-2.5">
          {showState ? (
            <StatusBadge
              domain={n.type === "goal" ? "goal" : "intent"}
              value={n.state}
              dot
              className="px-1.5 py-0 text-[10px]"
            />
          ) : (
            <span className="text-[10px] text-neutral-400 dark:text-neutral-500">
              {meta.label}
            </span>
          )}
          <span className="shrink-0 font-mono text-[10px] text-neutral-400 dark:text-neutral-500">
            #{n.id}
          </span>
        </div>
        <Handle
          type="source"
          position={Position.Right}
          className="!size-2 !border-2 !border-neutral-300 !bg-white opacity-0 transition-opacity group-hover:opacity-100 dark:!border-neutral-600 dark:!bg-neutral-800"
        />
      </div>
    </div>
  );
}

const nodeTypes = { explore: ExploreNode };

function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-3 py-1.5 text-sm">
      <span className="text-muted-foreground w-14 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

// 点击节点后弹出的抽屉：上半展示可读摘要，下半展示节点原始 JSON。
function NodeDetailSheet({
  node,
  onOpenChange,
}: {
  node: TaskNode | null;
  onOpenChange: (open: boolean) => void;
}) {
  const [copied, setCopied] = React.useState(false);
  const meta = node ? (typeMeta[viewKind(node)] ?? typeMeta.intent) : null;
  const Icon = meta?.icon;
  const raw = node ? JSON.stringify(node, null, 2) : "";

  const copy = () => {
    void navigator.clipboard?.writeText(raw).then(() => {
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md"
      >
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span
                  className={cn(
                    "flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm",
                    meta.iconBg,
                  )}
                >
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{meta.label}</SheetTitle>
                  <span className="text-muted-foreground font-mono text-xs">{node.id}</span>
                </div>
              </div>
            </SheetHeader>

            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">属性</h4>
                  <DetailRow label="类型">{meta.label}</DetailRow>
                  <DetailRow label="状态">
                    {node.type === "goal" || node.type === "intent" ? (
                      <StatusBadge
                        domain={node.type === "goal" ? "goal" : "intent"}
                        value={node.state}
                        dot
                        className="px-1.5 py-0 text-[10px]"
                      />
                    ) : (
                      <span className="font-mono text-xs">{node.state}</span>
                    )}
                  </DetailRow>
                  <DetailRow label="优先级">
                    <span className="tabular-nums">{node.priority}</span>
                  </DetailRow>
                  <DetailRow label="来源">
                    <span className="font-mono text-xs">{node.origin}</span>
                  </DetailRow>
                  <DetailRow label="时间">{new Date(node.ts).toLocaleString("zh-CN")}</DetailRow>
                </section>

                <section className="border-t pt-3">
                  <div className="mb-1.5 flex items-center justify-between">
                    <h4 className="text-muted-foreground text-xs font-medium">原始数据</h4>
                    <button
                      type="button"
                      onClick={copy}
                      className="text-muted-foreground hover:text-foreground text-xs transition-colors"
                    >
                      {copied ? "已复制" : "复制"}
                    </button>
                  </div>
                  <pre className="bg-muted/50 text-foreground max-w-full overflow-hidden rounded-md border p-3 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap">
                    {raw}
                  </pre>
                </section>
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function GraphInner({ taskId }: { taskId: string }) {
  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<ExploreRFNode>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<RFEdge>([]);
  const [isEmpty, setIsEmpty] = React.useState(true);
  const [selected, setSelected] = React.useState<TaskNode | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .explorationGraph(taskId)
        .then((g) => {
          if (cancelled) return;
          const nodes = g.nodes ?? [];
          const edges: Edge[] = g.edges ?? [];
          setIsEmpty(nodes.length === 0);
          // 抽屉打开时，让其中的数据随轮询保持最新。
          setSelected((cur) => (cur ? (nodes.find((n) => n.id === cur.id) ?? cur) : cur));

          // 目标指向「执行中」意图的连线做流动动画，直观展示活跃探索路径。
          const liveTargets = new Set(
            nodes.filter((n) => n.type === "intent" && n.state === "running").map((n) => n.id),
          );

          const layout = computeLayout(nodes, edges);
          setRfNodes((prev) => {
            const prevPos = new Map(prev.map((p) => [p.id, p.position]));
            return nodes.map((n) => ({
              id: n.id,
              type: "explore" as const,
              position: prevPos.get(n.id) ?? layout.get(n.id) ?? { x: 0, y: 0 },
              data: { node: n },
              sourcePosition: Position.Right,
              targetPosition: Position.Left,
            }));
          });
          setRfEdges(
            edges.map((e, i) => {
              const color = relColor[e.rel] ?? "#94a3b8";
              const animated = liveTargets.has(e.dst);
              return {
                id: `e${i}-${e.src}-${e.dst}`,
                source: e.src,
                target: e.dst,
                label: relLabel[e.rel] ?? e.rel,
                type: "default",
                animated,
                labelShowBg: false,
                labelStyle: { fontSize: 10, fontWeight: 600, fill: color },
                style: {
                  stroke: color,
                  strokeWidth: 2,
                  strokeOpacity: animated ? 0.9 : 0.55,
                },
                markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
              } satisfies RFEdge;
            }),
          );
        })
        .catch(() => {
          /* keep last good data */
        });
    };
    load();
    const timer = setInterval(load, 3000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [taskId, setRfNodes, setRfEdges]);

  return (
    <>
    <ReactFlow
      nodes={rfNodes}
      edges={rfEdges}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      onNodeClick={(_, node) => setSelected(node.data.node)}
      nodeTypes={nodeTypes}
      fitView
      fitViewOptions={{ padding: 0.2 }}
      proOptions={{ hideAttribution: true }}
      minZoom={0.1}
      className="!bg-[#f0f2f7] dark:!bg-neutral-950"
    >
      <Background
        variant={BackgroundVariant.Dots}
        gap={16}
        size={1}
        className="text-neutral-400/50 dark:text-neutral-700/60"
      />
      <Controls
        showInteractive={false}
        className="!rounded-lg !border !shadow-sm [&>button]:!border-border [&>button]:!bg-card [&>button:hover]:!bg-accent [&_svg]:!fill-foreground"
      />
      <MiniMap
        pannable
        zoomable
        className="!bg-card !rounded-lg !border !shadow-sm"
        maskColor="rgb(148 163 184 / 0.18)"
        nodeColor={(node) => {
          const data = node.data as ExploreNodeData | undefined;
          const k = data?.node ? viewKind(data.node) : "intent";
          return typeMeta[k]?.hex ?? "#94a3b8";
        }}
        nodeStrokeWidth={0}
        nodeBorderRadius={4}
      />
      <Panel position="top-left">
        <div className="bg-card/95 flex flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
          {isEmpty && <span className="text-muted-foreground">暂无探索数据</span>}
          <div className="flex flex-wrap gap-x-3 gap-y-1.5">
            {(["begin", "goal", "intent", "fact", "finding", "hint"] as ExploreKind[]).map((k) => {
              const m = typeMeta[k];
              const Icon = m.icon;
              return (
                <span key={k} className="text-foreground inline-flex items-center gap-1.5">
                  <span className={cn("flex size-4 items-center justify-center rounded", m.iconBg)}>
                    <Icon className="size-2.5 text-white" />
                  </span>
                  {m.label}
                </span>
              );
            })}
          </div>
          <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
            {Object.entries(relLabel).map(([k, v]) => (
              <span key={k} className="inline-flex items-center gap-1.5">
                <span className="h-0.5 w-4 rounded-full" style={{ backgroundColor: relColor[k] }} />
                {v}
              </span>
            ))}
          </div>
        </div>
      </Panel>
    </ReactFlow>
    <NodeDetailSheet node={selected} onOpenChange={(o) => !o && setSelected(null)} />
    </>
  );
}

export function GraphTab({ taskId }: { taskId: string }) {
  return (
    <Card>
      <CardContent className="p-0">
        <div className="h-[72vh] w-full overflow-hidden rounded-xl">
          <ReactFlowProvider>
            <GraphInner taskId={taskId} />
          </ReactFlowProvider>
        </div>
      </CardContent>
    </Card>
  );
}
