"use client";

import * as React from "react";

import type { Graph as G6Graph } from "@antv/g6";
import { RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/button";
import type { IntranetSegment, IntranetTopology } from "@/lib/types";
import { cn } from "@/lib/utils";

// 网段配色盘：按 segments 顺序取色，超出循环复用。
const SEGMENT_PALETTE = [
  "#3b82f6",
  "#10b981",
  "#f59e0b",
  "#8b5cf6",
  "#f43f5e",
  "#06b6d4",
  "#84cc16",
  "#f97316",
  "#ec4899",
  "#14b8a6",
];

// 与资产覆盖图同一套做法：lucide 图标（v1.22 路径）渲染成白色 data URI 作节点图标，
// 手写内嵌以避免 react-dom/server 在 React19/Next 客户端打包的问题。
function svgUri(inner: string, filled = false): string {
  const attrs = filled
    ? 'fill="#fff" stroke="none"'
    : 'fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"';
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" ${attrs}>${inner}</svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

// 普通主机 = 显示器；平台自身（回连端）= 实心五角星。
const HOST_ICON = svgUri(
  '<rect x="2" y="3" width="20" height="14" rx="2"/><line x1="8" x2="16" y1="21" x2="21"/><line x1="12" x2="12" y1="17" y2="21"/>',
);
const CALLBACK_ICON = svgUri(
  '<path d="M12 2.5l2.9 6.1 6.6.8-4.9 4.6 1.3 6.6-5.9-3.3-5.9 3.3 1.3-6.6-4.9-4.6 6.6-.8z"/>',
  true,
);

export function segmentColor(segments: IntranetSegment[], segment: string): string {
  const idx = Math.max(
    0,
    segments.findIndex((s) => s.name === segment),
  );
  return SEGMENT_PALETTE[idx % SEGMENT_PALETTE.length];
}

// G6 的类型把自定义字段归在 data 下，但官方 force 示例（及运行时）按顶层读 d.<field>。
// 回调形参用 unknown 满足 G6 签名，内部用 nd()/ed() 强转回我们的顶层结构。
type G6NodeDatum = {
  id: string;
  lbl: string;
  segment: string;
  hasSession: boolean;
  alive: number;
  isCallback: boolean;
  size: number;
};
type G6EdgeDatum = { source: string; target: string; state: string };

const nd = (d: unknown) => d as G6NodeDatum;
const ed = (d: unknown) => d as G6EdgeDatum;

function toG6(data: IntranetTopology): { nodes: G6NodeDatum[]; edges: G6EdgeDatum[] } {
  return {
    nodes: (data.nodes ?? []).map((n) => ({
      id: n.id,
      lbl: n.alive_sessions > 0 ? `${n.ip} (${n.alive_sessions})` : n.ip || n.id,
      segment: n.segment,
      hasSession: n.has_session,
      alive: n.alive_sessions,
      isCallback: n.is_callback,
      size: n.is_callback ? 34 : 26,
    })),
    edges: (data.edges ?? []).map((e) => ({ source: e.from, target: e.to, state: e.state })),
  };
}

function TopologyLegend({
  data,
  loading,
  onRefresh,
}: {
  data: IntranetTopology | null;
  loading: boolean;
  onRefresh: () => void;
}) {
  const segments = data?.segments ?? [];
  const nodeCount = data?.nodes.length ?? 0;
  const aliveEdges = data?.edges.filter((e) => e.state === "alive").length ?? 0;
  return (
    <div className="bg-card/95 pointer-events-auto absolute top-3 left-3 flex max-w-[360px] flex-col gap-2.5 rounded-lg border p-3 text-xs shadow-sm backdrop-blur">
      <div className="flex items-center justify-between gap-3">
        {data ? (
          <span className="text-muted-foreground">
            主机 <span className="text-foreground font-semibold tabular-nums">{nodeCount}</span> · 存活链路{" "}
            <span className="font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">{aliveEdges}</span>
          </span>
        ) : (
          <span className="text-muted-foreground">{loading ? "加载中…" : "暂无拓扑数据"}</span>
        )}
        <Button variant="ghost" size="icon" className="size-6" onClick={onRefresh} title="刷新">
          <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
        </Button>
      </div>
      {segments.length > 0 && (
        <div className="flex flex-wrap gap-x-3 gap-y-1.5">
          {segments.map((s) => (
            <span key={s.name} className="text-foreground inline-flex items-center gap-1.5">
              <span className="size-3 rounded-full" style={{ backgroundColor: segmentColor(segments, s.name) }} />
              {s.name}
            </span>
          ))}
        </div>
      )}
      <div className="border-border/60 text-muted-foreground flex flex-wrap gap-x-3 gap-y-1.5 border-t pt-2">
        <span className="inline-flex items-center gap-1.5">
          <span className="size-3 rounded-full border-2 border-emerald-600 bg-neutral-300" /> 有存活会话
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="size-3 rounded-full border border-white/70 bg-neutral-400" /> 无会话
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="flex size-3 items-center justify-center rounded-full bg-slate-500">
            <svg viewBox="0 0 24 24" className="size-2 fill-white" aria-hidden="true">
              <path d="M12 2.5l2.9 6.1 6.6.8-4.9 4.6 1.3 6.6-5.9-3.3-5.9 3.3 1.3-6.6-4.9-4.6 6.6-.8z" />
            </svg>
          </span>
          平台自身
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-0.5 w-4 rounded-full bg-emerald-500" /> 存活隧道
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-0 w-4 border-t-2 border-dashed border-neutral-400" /> 停止/失败
        </span>
      </div>
      <p className="text-muted-foreground/80 border-border/60 border-t pt-2 leading-relaxed">
        力导向布局，可拖拽节点、滚轮缩放；点击主机节点可在下方「会话」页签中过滤该主机的会话。
      </p>
    </div>
  );
}

// TopologyGraph 渲染内网主机拓扑：节点=主机（按网段着色、有会话加边框、平台自身星标），
// 边=隧道（alive 实线绿 / stopped·error 虚线灰）。点击节点回调 onSelectHost。
// 复用资产覆盖图的 G6 d3-force 模式（动态 import 避开 SSR/静态导出期的 window 依赖）。
export function TopologyGraph({
  data,
  loading,
  onRefresh,
  onSelectHost,
}: {
  data: IntranetTopology | null;
  loading: boolean;
  onRefresh: () => void;
  onSelectHost: (nodeId: string) => void;
}) {
  const containerRef = React.useRef<HTMLDivElement>(null);
  const graphRef = React.useRef<G6Graph | null>(null);
  const gDataRef = React.useRef<{ nodes: G6NodeDatum[]; edges: G6EdgeDatum[] }>({ nodes: [], edges: [] });
  const onSelectHostRef = React.useRef(onSelectHost);
  onSelectHostRef.current = onSelectHost;
  // 建图回调（闭包）里读最新 segments 顺序解析网段色，保证图与图例同色。
  const segmentsRef = React.useRef<IntranetSegment[]>([]);
  segmentsRef.current = data?.segments ?? [];

  // 结构签名：节点/边集合或其关键状态变化时才重灌数据重跑布局，避免轮询期无谓抖动。
  const sig = React.useMemo(() => {
    if (!data) return "";
    const ns = data.nodes
      .map((n) => `${n.id}:${n.has_session ? "s" : "-"}${n.alive_sessions}${n.is_callback ? "c" : ""}`)
      .sort()
      .join(",");
    const es = data.edges
      .map((e) => `${e.from}>${e.to}:${e.state}`)
      .sort()
      .join(",");
    return `${ns}|${es}`;
  }, [data]);

  gDataRef.current = data ? toG6(data) : { nodes: [], edges: [] };

  const applyData = React.useCallback(() => {
    const graph = graphRef.current;
    if (!graph || graph.destroyed) return;
    graph.setData(gDataRef.current);
    // render() 异步跑 d3-force 布局；组件在布局落地前卸载时 G6 会在已清空的 context 上
    // 访问 transform 抛错（纯 teardown 竞态），吞掉即可；真正的渲染错误仍打日志。
    void graph.render().catch((err) => {
      if (!graph.destroyed) console.error("[intranet-topology] render:", err);
    });
  }, []);

  // 建图（一次）。
  React.useEffect(() => {
    let destroyed = false;
    let graph: G6Graph | null = null;
    void (async () => {
      const { Graph } = await import("@antv/g6");
      if (destroyed || !containerRef.current) return;
      graph = new Graph({
        container: containerRef.current,
        autoResize: true,
        autoFit: "view",
        background: "#f0f2f7",
        node: {
          style: {
            size: (d: unknown) => nd(d).size,
            fill: (d: unknown) => {
              const n = nd(d);
              return n.isCallback ? "#475569" : segmentColor(segmentsRef.current, n.segment);
            },
            stroke: (d: unknown) => (nd(d).hasSession ? "#059669" : "rgba(255,255,255,0.85)"),
            lineWidth: (d: unknown) => (nd(d).hasSession ? 3 : 1.5),
            iconSrc: (d: unknown) => (nd(d).isCallback ? CALLBACK_ICON : HOST_ICON),
            iconWidth: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            iconHeight: (d: unknown) => Math.max(12, nd(d).size * 0.55),
            labelText: (d: unknown) => nd(d).lbl,
            labelFontSize: 10,
            labelPlacement: "bottom",
            labelFill: "#475569",
            labelBackground: true,
            labelBackgroundFill: "rgba(255,255,255,0.75)",
            labelBackgroundRadius: 3,
            labelPadding: [1, 3],
          },
        },
        edge: {
          style: {
            stroke: (d: unknown) => (ed(d).state === "alive" ? "#10b981" : "#94a3b8"),
            lineWidth: 1.6,
            lineDash: (d: unknown) => (ed(d).state === "alive" ? [0] : [5, 4]),
            endArrow: false,
          },
        },
        layout: {
          type: "d3-force",
          collide: { radius: (d: unknown) => nd(d).size + 10 },
          link: { distance: 110 },
          manyBody: { strength: (d: unknown) => (nd(d).isCallback ? -320 : -160) },
        },
        behaviors: ["drag-element-force", "drag-canvas", "zoom-canvas"],
      });
      graph.on("node:click", (evt: unknown) => {
        const id = (evt as { target?: { id?: string } }).target?.id;
        if (id) onSelectHostRef.current(id);
      });
      graphRef.current = graph;
      applyData();
    })();
    return () => {
      destroyed = true;
      // 先停布局再销毁：尽量缩短「布局在飞、context 被清空」的竞态窗口。
      try {
        graph?.stopLayout();
      } catch {
        /* 图可能尚未建成或已无布局上下文 */
      }
      graph?.destroy();
      graphRef.current = null;
    };
  }, [applyData]);

  // 数据变化 → 重新灌数据 + 布局。sig 只作为重排触发器（applyData 读 gDataRef）。
  // biome-ignore lint/correctness/useExhaustiveDependencies: sig 是刻意的重排触发依赖
  React.useEffect(() => {
    applyData();
  }, [sig, applyData]);

  return (
    <div className="relative h-full w-full">
      <div ref={containerRef} className="h-full w-full" />
      <TopologyLegend data={data} loading={loading} onRefresh={onRefresh} />
    </div>
  );
}
