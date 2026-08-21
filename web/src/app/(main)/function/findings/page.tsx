"use client";

import * as React from "react";
import Link from "next/link";
import {
  ChevronRightIcon,
  ShieldAlertIcon,
  ArrowUpRightIcon,
  FileTextIcon,
  Trash2Icon,
  DownloadIcon,
} from "lucide-react";

import { StatusBadge } from "@/components/status-badge";
import { CopyButton } from "@/components/copy-button";
import { Markdown } from "@/components/markdown";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
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

const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];

const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];

const EMPTY_STATS: FindingStats = {
  total: 0,
  pending: 0,
  critical: 0,
  high: 0,
  medium: 0,
  low: 0,
  vulnclasses: [],
  tasks: [],
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
  const [task, setTask] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [total, setTotal] = React.useState(0);
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);

  // 勾选导出:按 finding_id(独立表 id)记选中项,跨页保留。
  const [selectedIds, setSelectedIds] = React.useState<Set<string>>(() => new Set());
  // 导出弹窗状态:范围(当前筛选/全部/选中) × 格式(md 单文件/md 分文件 zip/csv/json)。
  const [exportOpen, setExportOpen] = React.useState(false);
  const [exportScope, setExportScope] = React.useState<"filtered" | "all" | "selected">("filtered");
  const [exportFormat, setExportFormat] = React.useState<"md-single" | "md-zip" | "csv" | "json">("md-single");
  const [exporting, setExporting] = React.useState(false);

  const toggleSelected = React.useCallback((id: string, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  // 当前页可勾选的 finding_id(排除无 finding_id 的继承行)。
  const pageSelectableIds = React.useMemo(
    () => findings.map((f) => f.finding_id).filter((id): id is string => !!id),
    [findings],
  );
  const pageSelectedCount = pageSelectableIds.filter((id) => selectedIds.has(id)).length;
  let headerChecked: boolean | "indeterminate" = false;
  if (pageSelectableIds.length > 0 && pageSelectedCount === pageSelectableIds.length) {
    headerChecked = true;
  } else if (pageSelectedCount > 0) {
    headerChecked = "indeterminate";
  }
  const toggleSelectedPage = React.useCallback(
    (checked: boolean) => {
      setSelectedIds((prev) => {
        const next = new Set(prev);
        for (const id of pageSelectableIds) {
          if (checked) next.add(id);
          else next.delete(id);
        }
        return next;
      });
    },
    [pageSelectableIds],
  );

  // 打开导出弹窗时,若有勾选项则默认范围切到「选中」,否则「当前筛选」。
  function openExport() {
    setExportScope(selectedIds.size > 0 ? "selected" : "filtered");
    setExportOpen(true);
  }

  async function doExport() {
    setExporting(true);
    try {
      await api.exportFindings({
        format: exportFormat,
        scope: exportScope,
        filters: { severity, status, vulnclass, task, sort },
        ids: [...selectedIds],
      });
      setExportOpen(false);
      toast.success("已开始下载导出文件");
    } catch (e) {
      toast.error("导出失败：" + (e as Error).message);
    } finally {
      setExporting(false);
    }
  }

  // reset to page 1 whenever filters change
  React.useEffect(() => {
    setPage(1);
  }, [severity, status, vulnclass, task, sort]);

  // Server-driven list: paging + filtering + sorting all happen in SQL. Polls the
  // current page every 5s, and refetches immediately when any query input changes.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingsPage({ page, pageSize, severity, status, vulnclass, task, sort })
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
  }, [page, pageSize, severity, status, vulnclass, task, sort]);

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

  // 行内展开的详细报告缓存,按行 key(f.id)存。report 是大段 Markdown,列表查询不带它,
  // 故展开时才按 finding_id 单独拉取一次;done 且文本为空 = 该漏洞暂无报告。
  const [reports, setReports] = React.useState<
    Record<string, { status: "loading" | "done" | "error"; text: string }>
  >({});

  // 行内可编辑缓冲:当前展开行的名称/类别/严重等级,展开时用该行数据初始化,收起清空。
  // 单行展开,故一份缓冲即可。
  const [edit, setEdit] = React.useState<
    { name: string; vulnclass: string; severity: Severity } | null
  >(null);
  const [saving, setSaving] = React.useState(false);

  // toggle 展开/收起一行;新展开时初始化编辑缓冲,并(尚未取过时)按 finding_id 拉一次报告缓存。
  const toggleRow = React.useCallback(
    (f: Finding) => {
      const willOpen = expanded !== f.id;
      setExpanded(willOpen ? f.id : null);
      if (!willOpen) {
        setEdit(null);
        return;
      }
      setEdit({ name: f.name ?? "", vulnclass: f.vulnclass ?? "", severity: f.severity });
      if (!f.finding_id || reports[f.id]) return;
      const fid = f.finding_id;
      const key = f.id;
      setReports((r) => ({ ...r, [key]: { status: "loading", text: "" } }));
      api
        .getFinding(fid)
        .then((full) =>
          setReports((r) => ({ ...r, [key]: { status: "done", text: full.report ?? "" } })),
        )
        .catch(() =>
          setReports((r) => ({ ...r, [key]: { status: "error", text: "" } })),
        );
    },
    [expanded, reports],
  );

  // saveEdit 保存当前展开行的名称/类别/严重等级,回写本地列表并刷新统计(类别下拉/严重计数可能变)。
  const saveEdit = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id || !edit) return;
      setSaving(true);
      try {
        const updated = await api.updateFinding(f.finding_id, {
          name: edit.name.trim(),
          vulnclass: edit.vulnclass.trim(),
          severity: edit.severity,
        });
        setFindings((cur) =>
          cur.map((x) =>
            x.id === f.id
              ? { ...x, name: updated.name, vulnclass: updated.vulnclass, severity: updated.severity }
              : x,
          ),
        );
        toast.success("已保存");
        api.findingStats().then(setStats).catch(() => {});
      } catch (e) {
        toast.error("保存失败：" + (e as Error).message);
      } finally {
        setSaving(false);
      }
    },
    [edit],
  );

  // deleteFinding 删除一个漏洞(需二次确认):删成功后从列表移除、收起行、刷新统计。
  const deleteFinding = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id) return;
      try {
        await api.deleteFinding(f.finding_id);
        setFindings((cur) => cur.filter((x) => x.id !== f.id));
        setTotal((t) => Math.max(0, t - 1));
        setExpanded((cur) => (cur === f.id ? null : cur));
        toast.success("已删除漏洞");
        api.findingStats().then(setStats).catch(() => {});
      } catch (e) {
        toast.error("删除失败：" + (e as Error).message);
      }
    },
    [],
  );

  const statCards: { label: string; value: number; tone?: string }[] = [
    { label: "发现总数", value: stats.total },
    { label: "待处理", value: stats.pending, tone: "text-amber-500" },
    { label: "严重", value: stats.critical, tone: "text-rose-600" },
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
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
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
                ["critical", "严重"],
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

          <Select value={task} onValueChange={setTask}>
            <SelectTrigger size="sm" className="w-48">
              <SelectValue placeholder="任务" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部任务</SelectItem>
              {(stats.tasks ?? []).map((t) => {
                const id = String(t.id);
                const label = t.description || `任务 #${id}（已删除）`;
                return (
                  <SelectItem key={id} value={id}>
                    <span className="flex w-full items-center gap-2">
                      <span className="max-w-[14rem] truncate" title={label}>
                        {label}
                      </span>
                      <span className="text-muted-foreground tabular-nums">
                        {t.count}
                      </span>
                    </span>
                  </SelectItem>
                );
              })}
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

          <div className="ml-auto flex items-center gap-3">
            {selectedIds.size > 0 && (
              <span className="text-xs text-muted-foreground tabular-nums">
                已选 {selectedIds.size} 条
              </span>
            )}
            <span className="text-xs text-muted-foreground tabular-nums">
              共 {total} 条
            </span>
            <Button size="sm" variant="outline" onClick={openExport}>
              <DownloadIcon /> 导出
            </Button>
          </div>
        </div>

        <Card className="py-0">
          <CardContent className="px-0">
            {/* table-fixed:列宽由表头锁定,展开行那个 colSpan 单元格再宽也只能在固定宽度内
                换行/内部滚动,不会把整张表撑出横向滚动条。 */}
            <Table className="table-fixed">
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8">
                    <Checkbox
                      checked={headerChecked}
                      onCheckedChange={(c) => toggleSelectedPage(c === true)}
                      aria-label="选择本页全部"
                    />
                  </TableHead>
                  <TableHead className="w-8" />
                  <TableHead className="w-20">严重度</TableHead>
                  <TableHead>漏洞名称</TableHead>
                  <TableHead className="w-52">资产</TableHead>
                  <TableHead className="w-28">状态</TableHead>
                  <TableHead className="w-36 max-w-[9rem]">所属任务</TableHead>
                  <TableHead className="w-32">时间</TableHead>
                  <TableHead className="w-16 text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {findings.map((f) => {
                  const open = expanded === f.id;
                  return (
                    <React.Fragment key={f.id}>
                      <TableRow
                        className="cursor-pointer"
                        onClick={() => toggleRow(f)}
                      >
                        <TableCell onClick={(e) => e.stopPropagation()}>
                          {f.finding_id && (
                            <Checkbox
                              checked={selectedIds.has(f.finding_id)}
                              onCheckedChange={(c) =>
                                toggleSelected(f.finding_id as string, c === true)
                              }
                              aria-label="选择该漏洞"
                            />
                          )}
                        </TableCell>
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
                            {f.finding_id ? (
                              <Link
                                href={`/function/findings/detail?id=${f.finding_id}`}
                                onClick={(e) => e.stopPropagation()}
                                className="truncate font-medium hover:text-primary hover:underline"
                                title="查看发现详情"
                              >
                                {f.name || f.vulnclass || "未分类"}
                              </Link>
                            ) : (
                              <span className="truncate font-medium">
                                {f.name || f.vulnclass || "未分类"}
                              </span>
                            )}
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
                        <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
                          {f.finding_id && (
                            <AlertDialog>
                              <AlertDialogTrigger asChild>
                                <Button
                                  size="icon"
                                  variant="ghost"
                                  className="size-7 text-muted-foreground hover:text-destructive"
                                  aria-label="删除漏洞"
                                >
                                  <Trash2Icon className="size-4" />
                                </Button>
                              </AlertDialogTrigger>
                              <AlertDialogContent>
                                <AlertDialogHeader>
                                  <AlertDialogTitle>确认删除该漏洞？</AlertDialogTitle>
                                  <AlertDialogDescription className="break-words">
                                    「<span className="break-all">{f.name || f.vulnclass || f.summary || `#${f.finding_id}`}</span>」将被永久删除，
                                    同时从发现列表、任务发现 Tab 与探索图中移除，此操作不可撤销。
                                  </AlertDialogDescription>
                                </AlertDialogHeader>
                                <AlertDialogFooter>
                                  <AlertDialogCancel>取消</AlertDialogCancel>
                                  <AlertDialogAction onClick={() => deleteFinding(f)}>删除</AlertDialogAction>
                                </AlertDialogFooter>
                              </AlertDialogContent>
                            </AlertDialog>
                          )}
                        </TableCell>
                      </TableRow>
                      {open && (
                        <TableRow className="hover:bg-transparent">
                          {/* whitespace-normal 覆盖 TableCell 默认的 nowrap,否则展开区文字
                              被强制单行、直接溢出单元格。 */}
                          <TableCell colSpan={9} className="bg-muted/30 whitespace-normal">
                            <div className="flex flex-col gap-2 px-2 py-1">
                              {/* 行内编辑:名称/类别/严重等级,可改并保存(仅独立 finding 行)。 */}
                              {f.finding_id && edit && (
                                <div className="flex flex-wrap items-end gap-3 rounded-md border bg-background px-3 py-2.5">
                                  <div className="flex min-w-[12rem] flex-1 flex-col gap-1">
                                    <Label className="text-xs text-muted-foreground">漏洞名称</Label>
                                    <Input
                                      value={edit.name}
                                      onChange={(e) =>
                                        setEdit((s) => (s ? { ...s, name: e.target.value } : s))
                                      }
                                      placeholder="可读标题，留空回退类别"
                                    />
                                  </div>
                                  <div className="flex min-w-[10rem] flex-col gap-1">
                                    <Label className="text-xs text-muted-foreground">类别</Label>
                                    <Input
                                      value={edit.vulnclass}
                                      onChange={(e) =>
                                        setEdit((s) => (s ? { ...s, vulnclass: e.target.value } : s))
                                      }
                                      placeholder="如 SQL Injection"
                                    />
                                  </div>
                                  <div className="flex flex-col gap-1">
                                    <Label className="text-xs text-muted-foreground">严重等级</Label>
                                    <Select
                                      value={edit.severity}
                                      onValueChange={(v) =>
                                        setEdit((s) => (s ? { ...s, severity: v as Severity } : s))
                                      }
                                    >
                                      <SelectTrigger size="sm" className="w-28">
                                        <SelectValue />
                                      </SelectTrigger>
                                      <SelectContent>
                                        {SEVERITIES.map((sv) => (
                                          <SelectItem key={sv} value={sv}>
                                            {statusMeta("severity", sv).label}
                                          </SelectItem>
                                        ))}
                                      </SelectContent>
                                    </Select>
                                  </div>
                                  <Button size="sm" disabled={saving} onClick={() => saveEdit(f)}>
                                    {saving ? "保存中…" : "保存"}
                                  </Button>
                                </div>
                              )}
                              <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                                <ShieldAlertIcon className="size-3.5" />
                                证据
                                {f.vulnclass && (
                                  <span>
                                    · 类型：
                                    <code className="rounded bg-muted px-1.5 py-0.5 font-mono">
                                      {f.vulnclass}
                                    </code>
                                  </span>
                                )}
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

                              {/* 详细报告(Markdown):展开时按 finding_id 懒加载,免进详情页即可查看。 */}
                              {f.finding_id && (
                                <div className="flex flex-col gap-1.5">
                                  <div className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                                    <span className="flex items-center gap-2">
                                      <FileTextIcon className="size-3.5" />
                                      详细报告
                                    </span>
                                    {reports[f.id]?.status === "done" &&
                                      reports[f.id]?.text.trim() && (
                                        <CopyButton
                                          text={reports[f.id]?.text}
                                          successMessage="已复制详细报告"
                                          variant="ghost"
                                          className="h-6 px-2 text-xs"
                                        />
                                      )}
                                  </div>
                                  {(() => {
                                    const rep = reports[f.id];
                                    if (!rep || rep.status === "loading")
                                      return (
                                        <p className="text-xs text-muted-foreground">加载中…</p>
                                      );
                                    if (rep.status === "error")
                                      return (
                                        <p className="text-xs text-muted-foreground">
                                          报告加载失败。
                                        </p>
                                      );
                                    if (!rep.text.trim())
                                      return (
                                        <p className="text-xs text-muted-foreground">
                                          暂无详细报告。
                                        </p>
                                      );
                                    return (
                                      // break-words 会继承到段落/列表,pre 另加
                                      // whitespace-pre-wrap 让代码块也换行——否则长代码行/长 URL
                                      // 会撑宽 colSpan 单元格,把整张表挤出横向滚动条。
                                      <div className="min-w-0 break-words rounded-md border bg-background px-3 py-2 [&_pre]:whitespace-pre-wrap">
                                        <Markdown text={rep.text} />
                                      </div>
                                    );
                                  })()}
                                </div>
                              )}
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
                      colSpan={9}
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

      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>导出发现</DialogTitle>
            <DialogDescription>
              选择导出范围与格式,生成后浏览器会自动下载。
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-5 py-1">
            <div className="flex flex-col gap-2">
              <Label className="text-xs text-muted-foreground">导出范围</Label>
              <RadioGroup
                value={exportScope}
                onValueChange={(v) => setExportScope(v as typeof exportScope)}
              >
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="filtered" /> 导出当前筛选结果（共 {total} 条）
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="all" /> 导出全部
                </label>
                <label
                  className={cn(
                    "flex items-center gap-2 text-sm",
                    selectedIds.size === 0 && "text-muted-foreground",
                  )}
                >
                  <RadioGroupItem value="selected" disabled={selectedIds.size === 0} />
                  导出勾选的 {selectedIds.size} 条
                </label>
              </RadioGroup>
            </div>

            <div className="flex flex-col gap-2">
              <Label className="text-xs text-muted-foreground">导出格式</Label>
              <RadioGroup
                value={exportFormat}
                onValueChange={(v) => setExportFormat(v as typeof exportFormat)}
              >
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="md-single" /> Markdown 汇总报告（单个 .md 文件）
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="md-zip" /> Markdown 分文件（一漏洞一 .md,打包 .zip）
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="csv" /> CSV 表格（.csv）
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <RadioGroupItem value="json" /> JSON（.json）
                </label>
              </RadioGroup>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)} disabled={exporting}>
              取消
            </Button>
            <Button
              onClick={doExport}
              disabled={exporting || (exportScope === "selected" && selectedIds.size === 0)}
            >
              <DownloadIcon /> {exporting ? "导出中…" : "导出"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
