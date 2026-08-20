"use client";

import * as React from "react";
import {
  GlobeIcon,
  NetworkIcon,
  LayoutTemplateIcon,
  LinkIcon,
  KeyRoundIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent } from "@/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import type { Asset } from "@/lib/types";

const PAGE_SIZES = [25, 50, 100, 200];

const METHOD_COLOR: Record<string, string> = {
  GET: "bg-emerald-100 text-emerald-700",
  POST: "bg-blue-100 text-blue-700",
  PUT: "bg-amber-100 text-amber-700",
  PATCH: "bg-orange-100 text-orange-700",
  DELETE: "bg-red-100 text-red-700",
  HEAD: "bg-purple-100 text-purple-700",
  OPTIONS: "bg-slate-100 text-slate-600",
};

function MethodBadge({ method }: { method: string }) {
  const m = method.toUpperCase();
  return (
    <span className={cn("inline-block rounded px-1.5 py-0.5 font-mono text-[10px] font-semibold leading-none", METHOD_COLOR[m] ?? "bg-muted text-muted-foreground")}>
      {m || "—"}
    </span>
  );
}

function statusTone(code: number) {
  if (code >= 500) return "text-red-500";
  if (code >= 400) return "text-amber-500";
  if (code >= 300) return "text-blue-500";
  if (code >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}

function fmtBytes(n?: number | null) {
  if (!n || !Number.isFinite(n) || n <= 0) return "—";
  const u = ["B", "KB", "MB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
  return `${i === 0 ? n : (n / 1024 ** i).toFixed(1)} ${u[i]}`;
}

function Chips({ items, mono }: { items: string[]; mono?: boolean }) {
  const clean = items.filter(Boolean);
  if (clean.length === 0) return <span className="text-xs text-muted-foreground">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {clean.map((s, i) => (
        <Badge key={i} variant="outline" className={cn("text-[10px]", mono && "font-mono")}>{s}</Badge>
      ))}
    </div>
  );
}

function AssetCard({
  cols, total, page, size, onSize, onPage, children,
}: {
  cols: string[];
  total: number;
  page: number;
  size: number;
  onSize: (v: number) => void;
  onPage: (fn: (p: number) => number) => void;
  children: React.ReactNode;
}) {
  const rows = React.Children.toArray(children);
  const pageCount = Math.max(1, Math.ceil(total / size));
  const start = total === 0 ? 0 : page * size + 1;
  const end = page * size + rows.length;
  return (
    <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
      <div className="min-h-0 flex-1 overflow-auto scrollbar-thin scrollbar-track-transparent">
        <Table>
          <TableHeader className="sticky top-0 z-10 bg-card">
            <TableRow>
              {cols.map((c) => <TableHead key={c}>{c}</TableHead>)}
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.length > 0 ? rows : (
              <TableRow>
                <TableCell colSpan={cols.length} className="py-12 text-center text-sm text-muted-foreground">
                  暂无数据
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
      {total > 0 && (
        <div className="flex shrink-0 items-center gap-2 border-t px-3 py-1.5 text-xs text-muted-foreground">
          <Select value={String(size)} onValueChange={(v) => onSize(Number(v))}>
            <SelectTrigger size="sm" className="h-7 w-24"><SelectValue /></SelectTrigger>
            <SelectContent>
              {PAGE_SIZES.map((n) => <SelectItem key={n} value={String(n)}>{n} / 页</SelectItem>)}
            </SelectContent>
          </Select>
          <span className="tabular-nums">{start}–{end} / {total}</span>
          {pageCount > 1 && (
            <div className="flex items-center gap-2">
              <Button variant="outline" size="icon" className="size-7" disabled={page <= 0} onClick={() => onPage((p) => Math.max(0, p - 1))}>
                <ChevronLeftIcon />
              </Button>
              <span className="tabular-nums">{page + 1} / {pageCount}</span>
              <Button variant="outline" size="icon" className="size-7" disabled={page + 1 >= pageCount} onClick={() => onPage((p) => Math.min(pageCount - 1, p + 1))}>
                <ChevronRightIcon />
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

const TABS: { key: string; label: string; icon: LucideIcon }[] = [
  { key: "root_domain", label: "根域名", icon: GlobeIcon },
  { key: "ip",          label: "IP",     icon: NetworkIcon },
  { key: "subdomain",   label: "子域名", icon: GlobeIcon },
  { key: "service",     label: "服务",   icon: LayoutTemplateIcon },
  { key: "endpoint",    label: "接口",   icon: LinkIcon },
];

export function AssetsTab({ taskId }: { taskId: string }) {
  const [rows, setRows]       = React.useState<Asset[]>([]);
  const [total, setTotal]     = React.useState(0);
  const [counts, setCounts]   = React.useState<Record<string, number>>({});
  const [loaded, setLoaded]   = React.useState(false);
  const [tab, setTab]         = React.useState("root_domain");
  const [page, setPage]       = React.useState(0);
  const [size, setSize]       = React.useState(50);

  React.useEffect(() => {
    setPage(0);
    setRows([]);
  }, [tab, size]);

  React.useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const [cur, nextCounts] = await Promise.all([
          api.taskAssets(taskId, tab, size, page * size),
          api.assetCounts(taskId),
        ]);
        if (!alive) return;
        setRows(cur.assets);
        setTotal(cur.total);
        setCounts(nextCounts ?? {});
        setLoaded(true);
      } catch {
        if (alive) setLoaded(true);
      }
    };
    load();
    const timer = setInterval(load, 10_000);
    return () => { alive = false; clearInterval(timer); };
  }, [taskId, tab, page, size]);

  React.useEffect(() => {
    const maxPage = Math.max(0, Math.ceil(total / size) - 1);
    if (page > maxPage) setPage(maxPage);
  }, [total, size, page]);

  const totalAll = TABS.reduce((n, t) => n + (counts[t.key] ?? 0), 0);

  if (!loaded) {
    return <div className="py-20 text-center text-sm text-muted-foreground">加载中…</div>;
  }
  if (totalAll === 0) {
    return (
      <div className="py-20 text-center text-sm text-muted-foreground">
        该任务暂无涉及资产
        <p className="mt-1 text-xs">资产在任务执行过程中由 agent 发现并记录</p>
      </div>
    );
  }

  return (
    <Tabs orientation="vertical" value={tab} onValueChange={setTab} className="items-start gap-4">
      {/* left nav */}
      <div className="w-36 shrink-0 rounded-lg border bg-card p-1 shadow-sm">
        {TABS.map((t) => (
          <button
            key={t.key}
            onClick={() => setTab(t.key)}
            className={cn(
              "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-sm transition-colors",
              tab === t.key
                ? "bg-accent text-accent-foreground font-medium"
                : "text-muted-foreground hover:bg-accent/60 hover:text-foreground",
            )}
          >
            <t.icon className="size-3.5 shrink-0" />
            <span className="flex-1 text-left">{t.label}</span>
            <span className="tabular-nums text-xs opacity-70">{counts[t.key] ?? 0}</span>
          </button>
        ))}
      </div>

      {/* right content */}
      <div className="min-w-0 flex-1 flex flex-col gap-4">

        <TabsContent value="root_domain" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["域名", "ICP 备案"]} total={total} page={page} size={size} onSize={setSize} onPage={setPage}>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="font-mono text-xs font-medium">{a.domain}</TableCell>
                <TableCell className="text-xs">{a.icp || "—"}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="ip" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["IP", "C段", "绑定域名", "开放端口"]} total={total} page={page} size={size} onSize={setSize} onPage={setPage}>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="font-mono text-xs font-medium">{a.ip}</TableCell>
                <TableCell className="font-mono text-xs">{a.c_segment || "—"}</TableCell>
                <TableCell><Chips items={a.bound_domains ?? []} mono /></TableCell>
                <TableCell>
                  <Chips
                    items={(a.open_ports ?? []).map((p) =>
                      p.service ? `${p.port}/${p.service}` : String(p.port),
                    )}
                    mono
                  />
                </TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="subdomain" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["域名", "根域名", "解析类型", "解析值"]} total={total} page={page} size={size} onSize={setSize} onPage={setPage}>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="font-mono text-xs font-medium">{a.domain}</TableCell>
                <TableCell className="font-mono text-xs">{a.root_domain || "—"}</TableCell>
                <TableCell className="text-xs">{a.record_type || "—"}</TableCell>
                <TableCell className="max-w-xs truncate font-mono text-xs">{(Array.isArray(a.record_value) ? a.record_value.join(", ") : a.record_value) || "—"}</TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

        <TabsContent value="service" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard
            cols={["地址 / 服务", "状态码", "标题", "响应长度", "指纹", "认证"]}
            total={total}
            page={page}
            size={size}
            onSize={setSize}
            onPage={setPage}
          >
            {rows.map((a) => {
              const isHttp = a.service_type === "http";
              const addr = isHttp
                ? (a.url || "")
                : (a.service_name || [a.ip || a.domain, a.port].filter(Boolean).join(":"));
              return (
                <TableRow key={a.id}>
                  <TableCell className="max-w-xs truncate font-mono text-xs" title={addr}>{addr || "—"}</TableCell>
                  <TableCell>
                    {a.status_code != null ? (
                      <span className={cn("font-mono text-xs font-semibold tabular-nums", statusTone(a.status_code))}>
                        {a.status_code}
                      </span>
                    ) : "—"}
                  </TableCell>
                  <TableCell className="max-w-[12rem] truncate text-xs">{a.page_title || "—"}</TableCell>
                  <TableCell className="text-xs tabular-nums">{fmtBytes(a.content_length)}</TableCell>
                  <TableCell><Chips items={a.technologies ?? []} /></TableCell>
                  <TableCell>
                    {(a.auth ?? []).length === 0 ? (
                      <span className="text-xs text-muted-foreground">—</span>
                    ) : (
                      (a.auth ?? []).map((authItem, i) => {
                        const item = authItem as Record<string, string>;
                        return (
                          <span key={i} className="inline-flex items-center gap-1 text-[11px]">
                            <KeyRoundIcon className="size-3 text-muted-foreground" />
                            <span className="font-mono">{item.type || item.username || "认证"}</span>
                          </span>
                        );
                      })
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </AssetCard>
        </TabsContent>

        <TabsContent value="endpoint" className="mt-0 flex min-h-0 flex-1 flex-col">
          <AssetCard cols={["方法", "完整地址", "参数"]} total={total} page={page} size={size} onSize={setSize} onPage={setPage}>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell className="w-16"><MethodBadge method={a.method || ""} /></TableCell>
                <TableCell className="max-w-sm truncate font-mono text-xs" title={a.url}>{a.url || "—"}</TableCell>
                <TableCell>
                  <Chips
                    items={(a.params ?? []).map((p) => {
                      const item = p as Record<string, string>;
                      return item.name ? `${item.name}(${item.location || "?"})` : "?";
                    })}
                    mono
                  />
                </TableCell>
              </TableRow>
            ))}
          </AssetCard>
        </TabsContent>

      </div>
    </Tabs>
  );
}
