"use client";
"use no memo";

import * as React from "react";
import {
  Background,
  BackgroundVariant,
  Controls,
  type Edge as RFEdge,
  Handle,
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
  forceCenter,
  forceCollide,
  forceLink,
  forceManyBody,
  forceSimulation,
  forceX,
  forceY,
} from "d3-force";
import {
  AppWindow,
  Building2,
  Globe,
  Link2,
  type LucideIcon,
  MoreHorizontal,
  Radio,
  Server,
  Waypoints,
} from "lucide-react";

import { cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { api } from "@/lib/api";
import type { CoverageGraphEdge, CoverageGraphNode } from "@/lib/types";

// 每个父节点下、同一类型的子节点默认展示的数量；超出折叠，"展示更多"每次再拉这么多。
const FOLD_LIMIT = 20;
const FOLD_STEP = 20;

type Kind = CoverageGraphNode["kind"];

type KindMeta = { label: string; icon: LucideIcon; iconBg: string; hex: string };

const kindMeta: Record<Kind, KindMeta> = {
  company: { label: "公司", icon: Building2, iconBg: "bg-slate-500", hex: "#64748b" },
  root_domain: { label: "根域名", icon: Globe, iconBg: "bg-indigo-500", hex: "#6366f1" },
  subdomain: { label: "子域名", icon: Waypoints, iconBg: "bg-blue-500", hex: "#3b82f6" },
  ip: { label: "IP", icon: Server, iconBg: "bg-cyan-600", hex: "#0891b2" },
  service: { label: "服务", icon: Radio, iconBg: "bg-amber-500", hex: "#f59e0b" },
  app: { label: "App", icon: AppWindow, iconBg: "bg-fuchsia-500", hex: "#d946ef" },
  endpoint: { label: "端点", icon: Link2, iconBg: "bg-rose-500", hex: "#f43f5e" },
};

// ---------------------------------------------------------------------------
// Folding: turn the full graph into the currently-visible node/edge set.
// 折叠是自顶向下的：某节点被折叠隐藏时，它的整棵子树也不再展开。
// ---------------------------------------------------------------------------

type FoldNode = {
  fold: true;
  key: string;
  groupId: string;
  parentKey: string;
  kind: Kind;
  hidden: CoverageGraphNode[];
};
type AssetRenderNode = { fold: false; key: string; kind: Kind; node: CoverageGraphNode };
type RenderNode = AssetRenderNode | FoldNode;

// tested 优先，其次按 label。
function sortChildren(a: CoverageGraphNode, b: CoverageGraphNode): number {
  if (a.tested !== b.tested) return a.tested ? -1 : 1;
  return (a.label || "").localeCompare(b.label || "");
}

function computeVisible(
  nodes: CoverageGraphNode[],
  edges: CoverageGraphEdge[],
  expanded: Map<string, number>,
): { renderNodes: RenderNode[]; renderEdges: CoverageGraphEdge[] } {
  const byKey = new Map(nodes.map((n) => [n.key, n]));
  const parentOf = new Map<string, string>();
  const childrenOf = new Map<string, string[]>();
  for (const e of edges) {
    if (!byKey.has(e.src) || !byKey.has(e.dst)) continue;
    if (!parentOf.has(e.src)) parentOf.set(e.src, e.dst);
    const arr = childrenOf.get(e.dst);
    if (arr) arr.push(e.src);
    else childrenOf.set(e.dst, [e.src]);
  }

  const renderNodes: RenderNode[] = [];
  const visible = new Set<string>();
  const foldNodes: FoldNode[] = [];

  // 顶层：没有父节点的节点（公司 / 孤立根域名 / 孤立 ip…）。
  const queue: string[] = [];
  for (const n of nodes) {
    if (!parentOf.has(n.key)) {
      queue.push(n.key);
      visible.add(n.key);
    }
  }

  for (let qi = 0; qi < queue.length; qi++) {
    const parent = queue[qi];
    const kids = childrenOf.get(parent) ?? [];
    if (kids.length === 0) continue;
    // 按类型分组
    const groups = new Map<Kind, CoverageGraphNode[]>();
    for (const ck of kids) {
      const cn = byKey.get(ck);
      if (!cn) continue;
      const g = groups.get(cn.kind);
      if (g) g.push(cn);
      else groups.set(cn.kind, [cn]);
    }
    for (const [kind, arr] of groups) {
      arr.sort(sortChildren);
      const groupId = `${parent}::${kind}`;
      const shown = expanded.get(groupId) ?? FOLD_LIMIT;
      const visibleChildren = arr.slice(0, shown);
      const hidden = arr.slice(shown);
      for (const c of visibleChildren) {
        if (!visible.has(c.key)) {
          visible.add(c.key);
          queue.push(c.key);
        }
      }
      if (hidden.length > 0) {
        foldNodes.push({
          fold: true,
          key: `fold:${groupId}`,
          groupId,
          parentKey: parent,
          kind,
          hidden,
        });
      }
    }
  }

  for (const n of nodes) {
    if (visible.has(n.key)) renderNodes.push({ fold: false, key: n.key, kind: n.kind, node: n });
  }
  const renderEdges: CoverageGraphEdge[] = edges.filter(
    (e) => visible.has(e.src) && visible.has(e.dst),
  );
  for (const f of foldNodes) {
    renderNodes.push(f);
    renderEdges.push({ src: f.key, dst: f.parentKey });
  }
  return { renderNodes, renderEdges };
}

// ---------------------------------------------------------------------------
// d3-force 静态布局：一次性 tick 到收敛，得到每个节点坐标。
// ---------------------------------------------------------------------------
function layout(
  renderNodes: RenderNode[],
  renderEdges: CoverageGraphEdge[],
): Map<string, { x: number; y: number }> {
  const sim = renderNodes.map((n) => ({ key: n.key })) as Array<{
    key: string;
    x?: number;
    y?: number;
  }>;
  const links = renderEdges.map((e) => ({ source: e.src, target: e.dst }));
  const s = forceSimulation(sim)
    .force("charge", forceManyBody().strength(-420))
    .force(
      "link",
      forceLink(links)
        .id((d) => (d as unknown as { key: string }).key)
        .distance(96)
        .strength(0.55),
    )
    .force("center", forceCenter(0, 0))
    .force("x", forceX(0).strength(0.05))
    .force("y", forceY(0).strength(0.05))
    .force("collide", forceCollide(52))
    .stop();
  const ticks = Math.min(400, 120 + sim.length * 2);
  for (let i = 0; i < ticks; i++) s.tick();
  const pos = new Map<string, { x: number; y: number }>();
  for (const d of sim) pos.set(d.key, { x: d.x ?? 0, y: d.y ?? 0 });
  return pos;
}

// ---------------------------------------------------------------------------
// 节点视觉：已测=实色高亮；范围内未测=灰；范围外连接节点=更淡的灰 + 虚线。
// ---------------------------------------------------------------------------
function nodeTone(n: CoverageGraphNode): "tested" | "untested" | "context" {
  if (!n.in_scope) return "context";
  return n.tested ? "tested" : "untested";
}

type AssetNodeData = { rn: AssetRenderNode };
type FoldNodeData = { rn: FoldNode };
type AssetRF = RFNode<AssetNodeData, "asset">;
type FoldRF = RFNode<FoldNodeData, "fold">;

function AssetGraphNode({ data, selected }: NodeProps<AssetRF>) {
  const n = data.rn.node;
  const meta = kindMeta[n.kind];
  const Icon = meta.icon;
  const tone = nodeTone(n);

  return (
    <div
      className={cn(
        "w-[180px] cursor-pointer rounded-xl border-2 transition-colors",
        selected
          ? "border-blue-500"
          : tone === "tested"
            ? "border-emerald-400/70"
            : "border-transparent",
      )}
    >
      <div
        className={cn(
          "group relative flex items-center gap-2 rounded-[10px] border px-2.5 py-2 shadow-sm transition-shadow hover:shadow-md",
          tone === "context"
            ? "border-dashed border-neutral-300 bg-neutral-100/70 dark:border-neutral-700 dark:bg-neutral-900/60"
            : "border-black/[0.06] bg-white dark:border-white/10 dark:bg-neutral-900",
          tone === "untested" && "opacity-90",
        )}
      >
        <Handle type="target" position={Position.Top} className="!size-1.5 !border-0 !bg-neutral-300 opacity-0" />
        <span
          className={cn(
            "flex size-6 shrink-0 items-center justify-center rounded-lg shadow-sm",
            tone === "tested" ? meta.iconBg : "bg-neutral-400 dark:bg-neutral-600",
          )}
        >
          <Icon className="size-3.5 text-white" />
        </span>
        <div className="min-w-0 flex-1">
          <div
            className={cn(
              "truncate text-[12px] font-semibold",
              tone === "tested"
                ? "text-neutral-800 dark:text-neutral-100"
                : "text-neutral-500 dark:text-neutral-400",
            )}
            title={n.label}
          >
            {n.label || meta.label}
          </div>
          <div className="text-[10px] text-neutral-400">{meta.label}</div>
        </div>
        {tone === "tested" && (
          <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" title="已测试" />
        )}
        <Handle type="source" position={Position.Bottom} className="!size-1.5 !border-0 !bg-neutral-300 opacity-0" />
      </div>
    </div>
  );
}

function FoldGraphNode({ data, selected }: NodeProps<FoldRF>) {
  const f = data.rn;
  const meta = kindMeta[f.kind];
  return (
    <div
      className={cn(
        "cursor-pointer rounded-full border-2 transition-colors",
        selected ? "border-blue-500" : "border-transparent",
      )}
    >
      <div className="group relative flex items-center gap-1.5 rounded-full border border-dashed border-neutral-400 bg-neutral-50 px-3 py-1.5 shadow-sm hover:bg-neutral-100 dark:border-neutral-600 dark:bg-neutral-900 dark:hover:bg-neutral-800">
        <Handle type="target" position={Position.Top} className="!size-1.5 !border-0 !bg-neutral-300 opacity-0" />
        <MoreHorizontal className="size-4 text-neutral-500" />
        <span className="text-[11px] font-medium text-neutral-600 dark:text-neutral-300">
          还有 {f.hidden.length} 个{meta.label}
        </span>
      </div>
    </div>
  );
}

const nodeTypes = { asset: AssetGraphNode, fold: FoldGraphNode };

// ---------------------------------------------------------------------------
// 抽屉：资产节点看详情；折叠节点看隐藏列表 + "展示更多"。
// ---------------------------------------------------------------------------
function DetailRow({ label, children }: { label: string; children: React.ReactNode }) {
  if (children === undefined || children === null || children === "") return null;
  return (
    <div className="flex items-start gap-3 py-1 text-sm">
      <span className="text-muted-foreground w-16 shrink-0">{label}</span>
      <span className="text-foreground min-w-0 flex-1 break-words">{children}</span>
    </div>
  );
}

function AssetSheet({
  node,
  onOpenChange,
}: {
  node: CoverageGraphNode | null;
  onOpenChange: (open: boolean) => void;
}) {
  const meta = node ? kindMeta[node.kind] : null;
  const Icon = meta?.icon;
  const raw = node ? JSON.stringify(node, null, 2) : "";
  return (
    <Sheet open={node !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {node && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <div className="flex items-center gap-2.5 pr-8">
                <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-lg shadow-sm", meta.iconBg)}>
                  <Icon className="size-4 text-white" />
                </span>
                <div className="min-w-0">
                  <SheetTitle className="leading-tight">{meta.label}</SheetTitle>
                  <span className="text-muted-foreground truncate font-mono text-xs" title={node.label}>
                    {node.label}
                  </span>
                </div>
              </div>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex w-full min-w-0 flex-col gap-4 p-4">
                <section>
                  <h4 className="text-muted-foreground mb-1 text-xs font-medium">属性</h4>
                  <DetailRow label="类型">{meta.label}</DetailRow>
                  <DetailRow label="测试状态">
                    {node.in_scope ? (
                      node.tested ? (
                        <span className="text-emerald-600 dark:text-emerald-400">已测试</span>
                      ) : (
                        <span className="text-neutral-500">未测试</span>
                      )
                    ) : (
                      <span className="text-neutral-400">范围外（连接节点）</span>
                    )}
                  </DetailRow>
                  <DetailRow label="域名">{node.domain}</DetailRow>
                  <DetailRow label="根域名">{node.root_domain}</DetailRow>
                  <DetailRow label="IP">{node.ip}</DetailRow>
                  <DetailRow label="端口">{node.port ? node.port : undefined}</DetailRow>
                  <DetailRow label="URL">
                    {node.url ? (
                      <span className="font-mono text-xs break-all">{node.url}</span>
                    ) : undefined}
                  </DetailRow>
                  <DetailRow label="标题">{node.page_title}</DetailRow>
                  <DetailRow label="状态码">{node.status_code ? node.status_code : undefined}</DetailRow>
                  <DetailRow label="App">{node.app_name}</DetailRow>
                  <DetailRow label="资产ID">
                    {node.asset_id ? <span className="font-mono text-xs">{node.asset_id}</span> : undefined}
                  </DetailRow>
                </section>
                <section className="border-t pt-3">
                  <h4 className="text-muted-foreground mb-1.5 text-xs font-medium">原始数据</h4>
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

function FoldSheet({
  fold,
  onOpenChange,
  onShowMore,
  onPick,
}: {
  fold: FoldNode | null;
  onOpenChange: (open: boolean) => void;
  onShowMore: (groupId: string) => void;
  onPick: (n: CoverageGraphNode) => void;
}) {
  const meta = fold ? kindMeta[fold.kind] : null;
  const Icon = meta?.icon;
  return (
    <Sheet open={fold !== null} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="flex w-full flex-col gap-0 p-0 data-[side=right]:sm:max-w-md">
        {fold && meta && Icon && (
          <>
            <SheetHeader className="border-b p-4">
              <SheetTitle className="text-base">
                未展示的{meta.label}（{fold.hidden.length}）
              </SheetTitle>
              <p className="text-muted-foreground text-xs">
                已测优先展示。点「展示更多」把下一批拉进图里。
              </p>
            </SheetHeader>
            <ScrollArea className="min-h-0 flex-1">
              <div className="flex flex-col gap-1 p-3">
                {fold.hidden.map((n) => (
                  <button
                    key={n.key}
                    type="button"
                    onClick={() => onPick(n)}
                    className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm"
                  >
                    <span
                      className={cn(
                        "flex size-5 shrink-0 items-center justify-center rounded",
                        n.tested ? meta.iconBg : "bg-neutral-400 dark:bg-neutral-600",
                      )}
                    >
                      <Icon className="size-3 text-white" />
                    </span>
                    <span className="min-w-0 flex-1 truncate font-mono text-xs" title={n.label}>
                      {n.label}
                    </span>
                    {n.tested && <span className="size-1.5 shrink-0 rounded-full bg-emerald-500" />}
                  </button>
                ))}
              </div>
            </ScrollArea>
            <div className="border-t p-3">
              <Button className="w-full" variant="outline" onClick={() => onShowMore(fold.groupId)}>
                展示更多（+{FOLD_STEP}）
              </Button>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

// ---------------------------------------------------------------------------
function GraphInner({ taskId }: { taskId: string }) {
  const [data, setData] = React.useState<{
    nodes: CoverageGraphNode[];
    edges: CoverageGraphEdge[];
  } | null>(null);
  const [expanded, setExpanded] = React.useState<Map<string, number>>(new Map());
  const [selectedAsset, setSelectedAsset] = React.useState<CoverageGraphNode | null>(null);
  const [selectedFoldId, setSelectedFoldId] = React.useState<string | null>(null);

  const [rfNodes, setRfNodes, onNodesChange] = useNodesState<AssetRF | FoldRF>([]);
  const [rfEdges, setRfEdges, onEdgesChange] = useEdgesState<RFEdge>([]);

  React.useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .taskCoverageGraph(taskId)
        .then((g) => {
          if (cancelled) return;
          setData({ nodes: g.nodes ?? [], edges: g.edges ?? [] });
        })
        .catch(() => {
          /* 保留上一次数据 */
        });
    };
    load();
    const timer = setInterval(load, 8000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [taskId]);

  const { renderNodes, renderEdges } = React.useMemo(
    () =>
      data
        ? computeVisible(data.nodes, data.edges, expanded)
        : { renderNodes: [] as RenderNode[], renderEdges: [] as CoverageGraphEdge[] },
    [data, expanded],
  );

  // 布局只在「可见节点集合」变化时重算，避免轮询导致抖动。
  const sig = React.useMemo(
    () => `${renderNodes.map((n) => n.key).sort().join(",")}|${renderEdges.length}`,
    [renderNodes, renderEdges],
  );
  const posRef = React.useRef<Map<string, { x: number; y: number }>>(new Map());
  const lastSig = React.useRef("");
  if (sig !== lastSig.current) {
    posRef.current = layout(renderNodes, renderEdges);
    lastSig.current = sig;
  }

  React.useEffect(() => {
    const pos = posRef.current;
    setRfNodes(
      renderNodes.map((rn) => {
        const p = pos.get(rn.key) ?? { x: 0, y: 0 };
        return rn.fold
          ? ({ id: rn.key, type: "fold", position: p, data: { rn } } as FoldRF)
          : ({ id: rn.key, type: "asset", position: p, data: { rn } } as AssetRF);
      }),
    );
    setRfEdges(
      renderEdges.map((e, i) => ({
        id: `e${i}-${e.src}-${e.dst}`,
        source: e.src,
        target: e.dst,
        type: "default",
        style: { stroke: "#cbd5e1", strokeWidth: 1.5 },
      })),
    );
  }, [renderNodes, renderEdges, setRfNodes, setRfEdges]);

  const showMore = React.useCallback((groupId: string) => {
    setExpanded((prev) => {
      const m = new Map(prev);
      m.set(groupId, (m.get(groupId) ?? FOLD_LIMIT) + FOLD_STEP);
      return m;
    });
  }, []);

  const selectedFold =
    selectedFoldId != null
      ? (renderNodes.find((n) => n.fold && n.key === selectedFoldId) as FoldNode | undefined) ?? null
      : null;
  // 折叠节点全部展开后自动关闭抽屉。
  React.useEffect(() => {
    if (selectedFoldId != null && !selectedFold) setSelectedFoldId(null);
  }, [selectedFoldId, selectedFold]);

  const total = data?.nodes.length ?? 0;
  const tested = data?.nodes.filter((n) => n.in_scope && n.tested).length ?? 0;
  const inScope = data?.nodes.filter((n) => n.in_scope).length ?? 0;

  return (
    <>
      <ReactFlow
        nodes={rfNodes}
        edges={rfEdges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={(_, node) => {
          const rn = (node.data as AssetNodeData | FoldNodeData).rn;
          if (rn.fold) setSelectedFoldId(rn.key);
          else setSelectedAsset(rn.node);
        }}
        nodeTypes={nodeTypes}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        proOptions={{ hideAttribution: true }}
        minZoom={0.05}
        className="!bg-[#f0f2f7] dark:!bg-neutral-950"
      >
        <Background variant={BackgroundVariant.Dots} gap={16} size={1} className="text-neutral-400/50 dark:text-neutral-700/60" />
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
            const rn = (node.data as AssetNodeData | FoldNodeData | undefined)?.rn;
            if (!rn || rn.fold) return "#94a3b8";
            return rn.node.in_scope && rn.node.tested ? kindMeta[rn.node.kind].hex : "#cbd5e1";
          }}
          nodeStrokeWidth={0}
          nodeBorderRadius={4}
        />
        <Panel position="top-left">
          <div className="bg-card/95 flex flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
            {total === 0 && <span className="text-muted-foreground">暂无范围内资产（先锚定任务范围）</span>}
            {total > 0 && (
              <div className="text-muted-foreground">
                范围内 <span className="text-foreground font-semibold tabular-nums">{inScope}</span> · 已测{" "}
                <span className="font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{tested}</span>
              </div>
            )}
            <div className="flex flex-wrap gap-x-3 gap-y-1.5">
              {(Object.keys(kindMeta) as Kind[]).map((k) => {
                const m = kindMeta[k];
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
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded bg-emerald-500" /> 已测（高亮）
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded bg-neutral-400" /> 未测
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className="size-3 rounded border border-dashed border-neutral-400 bg-neutral-100" /> 范围外
              </span>
            </div>
          </div>
        </Panel>
      </ReactFlow>
      <AssetSheet node={selectedAsset} onOpenChange={(o) => !o && setSelectedAsset(null)} />
      <FoldSheet
        fold={selectedFold}
        onOpenChange={(o) => !o && setSelectedFoldId(null)}
        onShowMore={showMore}
        onPick={(n) => {
          setSelectedFoldId(null);
          setSelectedAsset(n);
        }}
      />
    </>
  );
}

export function CoverageGraphTab({ taskId }: { taskId: string }) {
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
