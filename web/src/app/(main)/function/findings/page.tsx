"use client";

import * as React from "react";
import Link from "next/link";
import {
  ChevronRightIcon,
  ShieldAlertIcon,
  ArrowUpRightIcon,
} from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { TablePagination } from "@/components/table-pagination";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import { toast } from "sonner";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStats, FindingStatus, Severity } from "@/lib/types";

const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "false_positive",
  "ignored",
  "resolved",
];

const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
};

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export default function FindingsPage() {
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [status, setStatus] = React.useState<"all" | FindingStatus>("all");
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [total, setTotal] = React.useState(0);
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);

  // reset to page 1 whenever filters change
  React.useEffect(() => {
    setPage(1);
  }, [severity, status, vulnclass, sort]);

  // Server-driven list: paging + filtering + sorting all happen in SQL. Polls the
  // current page every 5s, and refetches immediately when any query input changes.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingsPage({ page, pageSize, severity, status, vulnclass, sort })
        .then((res) => {
          if (!alive) return;
          setFindings(res.items);
          setTotal(res.total);
        })
        .catch(() => {});
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [page, pageSize, severity, status, vulnclass, sort]);

  // Whole-table aggregates (stat cards + vuln-class options) — independent of the
  // current page, so they stay exact.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingStats()
        .then((s) => {
          if (alive) setStats(s);
        })
        .catch(() => {});
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  // updateStatus optimistically flips one finding's triage state, reverting on error.
  const updateStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (!f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) =>
        cur.map((x) => (x.id === f.id ? { ...x, status: next } : x)),
      );
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(`已标记为「${statusMeta("finding", next).label}」`);
        // refresh stat cards (pending count) and drop the row if it no longer matches the status filter
        api.findingStats().then(setStats).catch(() => {});
        if (status !== "all" && next !== status) {
          setFindings((cur) => cur.filter((x) => x.id !== f.id));
          setTotal((t) => Math.max(0, t - 1));
        }
      } catch (e) {
        setFindings((cur) =>
          cur.map((x) => (x.id === f.id ? { ...x, status: prev } : x)),
        );
        toast.error("更新失败：" + (e as Error).message);
      }
    },
    [status],
  );

  const statCards: { label: string; value: number; tone?: string }[] = [
    { label: "发现总数", value: stats.total },
    { label: "待处理", value: stats.pending, tone: "text-amber-500" },
    { label: "高危", value: stats.high, tone: "text-red-500" },
    { label: "中危", value: stats.medium, tone: "text-amber-500" },
    { label: "低危", value: stats.low, tone: "text-slate-500" },
  ];

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">发现</h1>
        <p className="text-muted-foreground text-sm">跨任务漏洞汇总</p>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
          {statCards.map((s) => (
            <Card key={s.label} className="gap-1 py-4">
              <CardHeader className="px-4">
                <CardDescription>{s.label}</CardDescription>
                <CardTitle className={cn("text-2xl tabular-nums", s.tone)}>
                  {s.value}
                </CardTitle>
              </CardHeader>
            </Card>
          ))}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-1 rounded-md border p-0.5">
            {(
              [
                ["all", "全部"],
                ["high", "高危"],
                ["medium", "中危"],
                ["low", "低危"],
              ] as const
            ).map(([val, label]) => (
              <button
                key={val}
                type="button"
                onClick={() => setSeverity(val)}
                className={cn(
                  "rounded px-2.5 py-1 text-xs font-medium transition-colors",
                  severity === val
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground hover:bg-muted",
                )}
              >
                {label}
              </button>
            ))}
          </div>

          <Select
            value={status}
            onValueChange={(v) => setStatus(v as "all" | FindingStatus)}
          >
            <SelectTrigger size="sm" className="w-32">
              <SelectValue placeholder="状态" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              {FINDING_STATUSES.map((st) => (
                <SelectItem key={st} value={st}>
                  {statusMeta("finding", st).label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={vulnclass} onValueChange={setVulnclass}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue placeholder="漏洞类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部类型</SelectItem>
              {stats.vulnclasses.map((vc) => (
                <SelectItem key={vc} value={vc}>
                  {vc}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select
            value={sort}
            onValueChange={(v) => setSort(v as "severity" | "time")}
          >
            <SelectTrigger size="sm" className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="severity">按严重度</SelectItem>
              <SelectItem value="time">按时间</SelectItem>
            </SelectContent>
          </Select>

          <span className="ml-auto text-xs text-muted-foreground tabular-nums">
            共 {total} 条
          </span>
        </div>

        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8" />
                  <TableHead className="w-20">严重度</TableHead>
                  <TableHead>漏洞名称</TableHead>
                  <TableHead className="w-52">资产</TableHead>
                  <TableHead className="w-28">状态</TableHead>
                  <TableHead className="w-36 max-w-[9rem]">所属任务</TableHead>
                  <TableHead className="w-32">时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {findings.map((f) => {
                  const open = expanded === f.id;
                  return (
                    <React.Fragment key={f.id}>
                      <TableRow
                        className="cursor-pointer"
                        onClick={() => setExpanded(open ? null : f.id)}
                      >
                        <TableCell>
                          <ChevronRightIcon
                            className={cn(
                              "size-4 text-muted-foreground transition-transform",
                              open && "rotate-90",
                            )}
                          />
                        </TableCell>
                        <TableCell>
                          <StatusBadge domain="severity" value={f.severity} dot />
                        </TableCell>
                        <TableCell className="max-w-md">
                          <div className="flex min-w-0 flex-col gap-0.5">
                            <span className="truncate font-medium">
                              {f.vulnclass || "未分类"}
                            </span>
                            <span className="truncate text-xs text-muted-foreground">
                              {f.summary}
                            </span>
                          </div>
                        </TableCell>
                        <TableCell className="w-52">
                          {f.assets && f.assets.length > 0 ? (
                            <div className="flex flex-wrap gap-1">
                              {f.assets.slice(0, 3).map((a) => (
                                <code
                                  key={a.id}
                                  className="max-w-[12rem] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                                  title={`${a.type} · ${a.label}`}
                                >
                                  {a.label}
                                </code>
                              ))}
                              {f.assets.length > 3 && (
                                <span className="text-xs text-muted-foreground">
                                  +{f.assets.length - 3}
                                </span>
                              )}
                            </div>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell onClick={(e) => e.stopPropagation()}>
                          {f.finding_id ? (
                            <Select
                              value={f.status}
                              onValueChange={(v) =>
                                updateStatus(f, v as FindingStatus)
                              }
                            >
                              <SelectTrigger
                                size="sm"
                                className="h-7 w-full border-none px-1 shadow-none focus-visible:ring-0"
                              >
                                <StatusBadge
                                  domain="finding"
                                  value={f.status}
                                  dot
                                />
                              </SelectTrigger>
                              <SelectContent position="popper" align="end">
                                {FINDING_STATUSES.map((st) => (
                                  <SelectItem key={st} value={st}>
                                    {statusMeta("finding", st).label}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          ) : (
                            <StatusBadge domain="finding" value={f.status} dot />
                          )}
                        </TableCell>
                        <TableCell className="w-36 max-w-[9rem]">
                          {f.task_id ? (
                            <Link
                              href={`/function/tasks/detail?id=${f.task_id}`}
                              onClick={(e) => e.stopPropagation()}
                              className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
                              title={f.task_description}
                            >
                              <span className="truncate">
                                {f.task_description}
                              </span>
                              <ArrowUpRightIcon className="size-3 shrink-0" />
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">—</span>
                          )}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground tabular-nums">
                          {fmtTime(f.ts)}
                        </TableCell>
                      </TableRow>
                      {open && (
                        <TableRow className="hover:bg-transparent">
                          <TableCell colSpan={7} className="bg-muted/30">
                            <div className="flex flex-col gap-2 px-2 py-1">
                              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                <ShieldAlertIcon className="size-3.5" />
                                证据 · {f.id}
                                {f.param_id && (
                                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono">
                                    {f.param_id}
                                  </code>
                                )}
                                {f.assets && f.assets.length > 0 && (
                                  <span className="flex flex-wrap items-center gap-1">
                                    · 资产：
                                    {f.assets.map((a) => (
                                      <code
                                        key={a.id}
                                        className="rounded bg-muted px-1.5 py-0.5 font-mono"
                                        title={a.type}
                                      >
                                        {a.label}
                                      </code>
                                    ))}
                                  </span>
                                )}
                              </div>
                              <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap">
                                {f.evidence}
                              </pre>
                            </div>
                          </TableCell>
                        </TableRow>
                      )}
                    </React.Fragment>
                  );
                })}
                {findings.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={7}
                      className="py-12 text-center text-sm text-muted-foreground"
                    >
                      没有匹配的发现。
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
            <TablePagination
              page={page}
              pageSize={pageSize}
              total={total}
              onPageChange={setPage}
              onPageSizeChange={setPageSize}
            />
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
