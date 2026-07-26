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
import type { Finding, Severity } from "@/lib/types";

const SEVERITY_ORDER: Record<Severity, number> = { high: 3, medium: 2, low: 1 };

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
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);

  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findings()
        .then((fs) => {
          if (alive) setFindings(fs);
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

  const vulnclasses = React.useMemo(
    () => Array.from(new Set(findings.map((f) => f.vulnclass))).sort(),
    [findings],
  );

  const counts = React.useMemo(() => {
    const c = { high: 0, medium: 0, low: 0 };
    for (const f of findings) c[f.severity]++;
    const tasks = new Set(findings.map((f) => f.task_id)).size;
    return { ...c, total: findings.length, tasks };
  }, [findings]);

  const rows = React.useMemo(() => {
    let list = findings.slice();
    if (severity !== "all") list = list.filter((f) => f.severity === severity);
    if (vulnclass !== "all")
      list = list.filter((f) => f.vulnclass === vulnclass);
    list.sort((a, b) => {
      if (sort === "severity") {
        const d = SEVERITY_ORDER[b.severity] - SEVERITY_ORDER[a.severity];
        if (d !== 0) return d;
      }
      return new Date(b.ts).getTime() - new Date(a.ts).getTime();
    });
    return list;
  }, [findings, severity, vulnclass, sort]);

  // reset to page 1 whenever filters change
  React.useEffect(() => { setPage(1); }, [severity, vulnclass, sort]);

  const paginated = React.useMemo(
    () => rows.slice((page - 1) * pageSize, page * pageSize),
    [rows, page, pageSize],
  );

  const statCards: { label: string; value: number; tone?: string }[] = [
    { label: "发现总数", value: counts.total },
    { label: "高危", value: counts.high, tone: "text-red-500" },
    { label: "中危", value: counts.medium, tone: "text-amber-500" },
    { label: "低危", value: counts.low, tone: "text-slate-500" },
    { label: "涉及任务", value: counts.tasks },
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
                <CardTitle
                  className={cn("text-2xl tabular-nums", s.tone)}
                >
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

          <Select value={vulnclass} onValueChange={setVulnclass}>
            <SelectTrigger size="sm" className="w-40">
              <SelectValue placeholder="漏洞类型" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部类型</SelectItem>
              {vulnclasses.map((vc) => (
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
            共 {rows.length} 条
          </span>
        </div>

        <Card className="py-0">
          <CardContent className="px-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8" />
                  <TableHead className="w-20">严重度</TableHead>
                  <TableHead className="w-28">漏洞类型</TableHead>
                  <TableHead>摘要</TableHead>
                  <TableHead className="w-36 max-w-[9rem]">所属任务</TableHead>
                  <TableHead className="w-32">时间</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {paginated.map((f) => {
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
                        <TableCell>
                          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">
                            {f.vulnclass}
                          </code>
                        </TableCell>
                        <TableCell className="max-w-md">
                          <span className="line-clamp-1">{f.summary}</span>
                        </TableCell>
                        <TableCell className="w-36 max-w-[9rem]">
                          {f.task_id ? (
                            <Link
                              href={`/function/tasks/detail?id=${f.task_id}`}
                              onClick={(e) => e.stopPropagation()}
                              className="inline-flex max-w-full items-center gap-1 text-primary hover:underline"
                              title={f.task_description}
                            >
                              <span className="truncate">{f.task_description}</span>
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
                          <TableCell colSpan={6} className="bg-muted/30">
                            <div className="flex flex-col gap-2 px-2 py-1">
                              <div className="flex items-center gap-2 text-xs text-muted-foreground">
                                <ShieldAlertIcon className="size-3.5" />
                                证据 · {f.id}
                                {f.param_id && (
                                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono">
                                    {f.param_id}
                                  </code>
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
                {rows.length === 0 && (
                  <TableRow>
                    <TableCell
                      colSpan={6}
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
              total={rows.length}
              onPageChange={setPage}
              onPageSizeChange={setPageSize}
            />
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
