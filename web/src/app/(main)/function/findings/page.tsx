"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  BugIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  FileTextIcon,
  FlaskConicalIcon,
  InfoIcon,
  SearchIcon,
  ShieldAlertIcon,
  Trash2Icon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
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
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingGroup, FindingStats, FindingStatus, Severity } from "@/lib/types";
import { cn } from "@/lib/utils";

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

const UNASSIGNED_TASK = "__unassigned__";
const FINDING_LIST_PREFERENCE_KEY = "artex_finding_list_preferences";

interface GroupFindingsState {
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
}

function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
}

// Exploration node ids are only unique inside a task. Prefer the persisted
// finding id and otherwise namespace the node id by task so editing one group
// cannot update a similarly-named node in another expanded group.
function findingRowKey(finding: Finding): string {
  if (finding.finding_id) return `finding:${finding.finding_id}`;
  return `node:${finding.task_id ?? UNASSIGNED_TASK}:${finding.id}`;
}

function isSameFinding(left: Finding, right: Finding): boolean {
  return findingRowKey(left) === findingRowKey(right);
}

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
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [groups, setGroups] = React.useState<FindingGroup[]>([]);
  const [groupTotal, setGroupTotal] = React.useState(0);
  const [expandedGroups, setExpandedGroups] = React.useState<Set<string>>(() => new Set());
  const [groupFindings, setGroupFindings] = React.useState<Record<string, GroupFindingsState>>({});
  const [total, setTotal] = React.useState(0);
  const [stats, setStats] = React.useState<FindingStats>(EMPTY_STATS);
  const [statsLoaded, setStatsLoaded] = React.useState(false);
  const [preferencesHydrated, setPreferencesHydrated] = React.useState(false);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(10);
  const [deepenFinding, setDeepenFinding] = React.useState<Finding | null>(null);
  const [deepenDescription, setDeepenDescription] = React.useState("");
  const [deepening, setDeepening] = React.useState(false);
  const filterFingerprint = JSON.stringify([severity, status, vulnclass, task, sort, query]);
  const activeFilterFingerprint = React.useRef(filterFingerprint);
  activeFilterFingerprint.current = filterFingerprint;

  React.useEffect(() => {
    const raw = getLocalStorageValue(FINDING_LIST_PREFERENCE_KEY);
    if (raw) {
      try {
        const parsed = JSON.parse(raw) as {
          severity?: unknown;
          status?: unknown;
          vulnclass?: unknown;
          task?: unknown;
          sort?: unknown;
        };
        if (parsed.severity === "all" || SEVERITIES.includes(parsed.severity as Severity)) {
          setSeverity(parsed.severity as "all" | Severity);
        }
        if (parsed.status === "all" || FINDING_STATUSES.includes(parsed.status as FindingStatus)) {
          setStatus(parsed.status as "all" | FindingStatus);
        }
        if (typeof parsed.vulnclass === "string" && parsed.vulnclass) setVulnclass(parsed.vulnclass);
        if (typeof parsed.task === "string" && parsed.task) setTask(parsed.task);
        if (parsed.sort === "severity" || parsed.sort === "time") setSort(parsed.sort);
      } catch {
        // Ignore malformed or legacy preferences and retain the defaults.
      }
    }
    setPreferencesHydrated(true);
  }, []);

  React.useEffect(() => {
    if (!preferencesHydrated) return;
    setLocalStorageValue(FINDING_LIST_PREFERENCE_KEY, JSON.stringify({ severity, status, vulnclass, task, sort }));
  }, [preferencesHydrated, severity, sort, status, task, vulnclass]);

  React.useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  const setFindings = React.useCallback((update: (current: Finding[]) => Finding[]) => {
    setGroupFindings((current) => {
      const next: Record<string, GroupFindingsState> = {};
      for (const [key, state] of Object.entries(current)) {
        next[key] = { ...state, items: update(state.items) };
      }
      return next;
    });
  }, []);

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

  const toggleSelectedPage = React.useCallback((ids: string[], checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) {
        if (checked) next.add(id);
        else next.delete(id);
      }
      return next;
    });
  }, []);

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
        filters: { severity, status, vulnclass, task, query, sort },
        ids: [...selectedIds],
      });
      setExportOpen(false);
      toast.success("已开始下载导出文件");
    } catch (e) {
      toast.error(`导出失败：${(e as Error).message}`);
    } finally {
      setExporting(false);
    }
  }

  const groupRequests = React.useRef<Record<string, number>>({});
  const groupsRequest = React.useRef(0);
  const expandedGroupsRef = React.useRef(expandedGroups);
  const groupFindingsRef = React.useRef(groupFindings);
  const visibleGroupKeysRef = React.useRef<Set<string>>(new Set());
  expandedGroupsRef.current = expandedGroups;
  groupFindingsRef.current = groupFindings;
  visibleGroupKeysRef.current = new Set(groups.map(findingGroupKey));

  const refreshGroups = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++groupsRequest.current;
    try {
      const result = await api.findingGroups({
        page,
        pageSize,
        severity,
        status,
        vulnclass,
        task,
        query,
        sort,
      });
      if (request !== groupsRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setGroups(result.items);
      setGroupTotal(result.total);
      setTotal(result.finding_total);
    } catch {
      // Polling keeps the last successful snapshot visible.
    }
  }, [filterFingerprint, page, pageSize, severity, status, vulnclass, task, query, sort]);

  const loadGroup = React.useCallback(
    async (key: string, groupPage: number, groupPageSize: number) => {
      const request = (groupRequests.current[key] ?? 0) + 1;
      const requestFilter = filterFingerprint;
      if (activeFilterFingerprint.current !== requestFilter) return;
      groupRequests.current[key] = request;
      setGroupFindings((current) => ({
        ...current,
        [key]: {
          items: current[key]?.items ?? [],
          total: current[key]?.total ?? 0,
          page: groupPage,
          pageSize: groupPageSize,
          loaded: current[key]?.loaded ?? false,
          loading: true,
        },
      }));
      try {
        const result = await api.findingsPage({
          page: groupPage,
          pageSize: groupPageSize,
          severity,
          status,
          vulnclass,
          task: key,
          query,
          sort,
        });
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            items: result.items,
            total: result.total,
            page: result.page,
            pageSize: result.page_size,
            loaded: true,
            loading: false,
          },
        }));
      } catch {
        if (groupRequests.current[key] !== request || activeFilterFingerprint.current !== requestFilter) return;
        setGroupFindings((current) => ({
          ...current,
          [key]: {
            ...(current[key] ?? {
              items: [],
              total: 0,
              page: groupPage,
              pageSize: groupPageSize,
              loaded: false,
            }),
            loading: false,
          },
        }));
      }
    },
    [filterFingerprint, severity, status, vulnclass, query, sort],
  );

  React.useEffect(() => {
    for (const [key, state] of Object.entries(groupFindings)) {
      if (!state.loaded || state.loading) continue;
      const lastPage = Math.max(1, Math.ceil(state.total / state.pageSize));
      if (state.page > lastPage) void loadGroup(key, lastPage, state.pageSize);
    }
  }, [groupFindings, loadGroup]);

  const toggleGroup = React.useCallback(
    (key: string) => {
      const opening = !expandedGroups.has(key);
      const next = new Set(expandedGroups);
      if (opening) next.add(key);
      else next.delete(key);
      setExpandedGroups(next);
      const state = groupFindings[key];
      if (opening && !state?.loaded && !state?.loading) {
        void loadGroup(key, state?.page ?? 1, state?.pageSize ?? 10);
      }
    },
    [expandedGroups, groupFindings, loadGroup],
  );

  const reloadGroupForFinding = React.useCallback(
    (finding: Finding, removed = false) => {
      const key = finding.task_id ?? UNASSIGNED_TASK;
      const state = groupFindings[key];
      if (state?.loaded) {
        const nextTotal = Math.max(0, state.total - (removed ? 1 : 0));
        const lastPage = Math.max(1, Math.ceil(nextTotal / state.pageSize));
        void loadGroup(key, Math.min(state.page, lastPage), state.pageSize);
      }
    },
    [groupFindings, loadGroup],
  );

  // Reset both pagination levels when a shared finding filter changes.
  React.useEffect(() => {
    void filterFingerprint;
    setPage(1);
    setExpanded(null);
    setExpandedGroups(new Set());
    setGroupFindings({});
  }, [filterFingerprint]);

  // Task-group paging is independent from every expanded group's finding page.
  React.useEffect(() => {
    const refresh = () => {
      void refreshGroups();
      for (const key of expandedGroupsRef.current) {
        if (!visibleGroupKeysRef.current.has(key)) continue;
        const state = groupFindingsRef.current[key];
        if (state?.loaded && !state.loading) void loadGroup(key, state.page, state.pageSize);
      }
    };
    refresh();
    const timer = setInterval(refresh, 5000);
    return () => clearInterval(timer);
  }, [loadGroup, refreshGroups]);

  React.useEffect(() => {
    const lastPage = Math.max(1, Math.ceil(groupTotal / pageSize));
    if (page > lastPage) setPage(lastPage);
  }, [groupTotal, page, pageSize]);

  // Whole-table aggregates (stat cards + vuln-class options) — independent of the
  // current page, so they stay exact.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .findingStats()
        .then((s) => {
          if (alive) {
            setStats(s);
            setStatsLoaded(true);
          }
        })
        .catch(() => {
          // Keep the previous aggregate snapshot until the next poll.
        });
    };
    load();
    const t = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, []);

  React.useEffect(() => {
    if (!statsLoaded) return;
    if (vulnclass !== "all" && !stats.vulnclasses.includes(vulnclass)) setVulnclass("all");
    if (
      task !== "all" &&
      task !== UNASSIGNED_TASK &&
      !(stats.tasks ?? []).some((option) => String(option.id) === task)
    ) {
      setTask("all");
    }
  }, [stats, statsLoaded, task, vulnclass]);

  // updateStatus optimistically flips one finding's triage state, reverting on error.
  const updateStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (!f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: next } : x)));
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(`已标记为「${statusMeta("finding", next).label}」`);
        // refresh stat cards (pending count) and drop the row if it no longer matches the status filter
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The row update remains valid even if the aggregate refresh fails.
          });
        if (status !== "all" && next !== status) {
          setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
          setTotal((t) => Math.max(0, t - 1));
        }
        void refreshGroups();
        reloadGroupForFinding(f);
      } catch (e) {
        setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: prev } : x)));
        toast.error(`更新失败：${(e as Error).message}`);
      }
    },
    [reloadGroupForFinding, refreshGroups, setFindings, status],
  );

  // 行内展开的详细报告缓存按全局稳定行键存。report 是大段 Markdown,列表查询不带它,
  // 故展开时才按 finding_id 单独拉取一次;done 且文本为空 = 该漏洞暂无报告。
  const [reports, setReports] = React.useState<Record<string, { status: "loading" | "done" | "error"; text: string }>>(
    {},
  );

  // 行内可编辑缓冲:当前展开行的名称/类别/严重等级,展开时用该行数据初始化,收起清空。
  // 单行展开,故一份缓冲即可。
  const [edit, setEdit] = React.useState<{ name: string; vulnclass: string; severity: Severity } | null>(null);
  const [saving, setSaving] = React.useState(false);

  // toggle 展开/收起一行;新展开时初始化编辑缓冲,并(尚未取过时)按 finding_id 拉一次报告缓存。
  const toggleRow = React.useCallback(
    (f: Finding) => {
      const key = findingRowKey(f);
      const willOpen = expanded !== key;
      setExpanded(willOpen ? key : null);
      if (!willOpen) {
        setEdit(null);
        return;
      }
      setEdit({ name: f.name ?? "", vulnclass: f.vulnclass, severity: f.severity });
      if (!f.finding_id || reports[key]) return;
      const fid = f.finding_id;
      setReports((r) => ({ ...r, [key]: { status: "loading", text: "" } }));
      api
        .getFinding(fid)
        .then((full) => setReports((r) => ({ ...r, [key]: { status: "done", text: full.report ?? "" } })))
        .catch(() => setReports((r) => ({ ...r, [key]: { status: "error", text: "" } })));
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
            isSameFinding(x, f)
              ? { ...x, name: updated.name, vulnclass: updated.vulnclass, severity: updated.severity }
              : x,
          ),
        );
        toast.success("已保存");
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The edit remains valid even if the aggregate refresh fails.
          });
        void refreshGroups();
        reloadGroupForFinding(f);
      } catch (e) {
        toast.error(`保存失败：${(e as Error).message}`);
      } finally {
        setSaving(false);
      }
    },
    [edit, reloadGroupForFinding, refreshGroups, setFindings],
  );

  // deleteFinding 删除一个漏洞(需二次确认):删成功后从列表移除、收起行、刷新统计。
  const deleteFinding = React.useCallback(
    async (f: Finding) => {
      if (!f.finding_id) return;
      try {
        await api.deleteFinding(f.finding_id);
        setFindings((cur) => cur.filter((x) => !isSameFinding(x, f)));
        setSelectedIds((current) => {
          const next = new Set(current);
          next.delete(f.finding_id as string);
          return next;
        });
        setTotal((t) => Math.max(0, t - 1));
        const rowKey = findingRowKey(f);
        setExpanded((cur) => (cur === rowKey ? null : cur));
        toast.success("已删除漏洞");
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The deletion remains valid even if the aggregate refresh fails.
          });
        void refreshGroups();
        reloadGroupForFinding(f, true);
      } catch (e) {
        toast.error(`删除失败：${(e as Error).message}`);
      }
    },
    [reloadGroupForFinding, refreshGroups, setFindings],
  );

  async function submitDeepen() {
    if (!deepenFinding?.finding_id || !deepenDescription.trim() || deepening) return;
    setDeepening(true);
    try {
      const result = await api.deepenFinding(deepenFinding.finding_id, deepenDescription.trim());
      toast.success(
        result.queued
          ? `深入意图 #${result.intent_id} 已进入任务队列`
          : `已创建高优先级 Worker 意图 #${result.intent_id}`,
      );
      setDeepenFinding(null);
      setDeepenDescription("");
      reloadGroupForFinding(deepenFinding);
      void refreshGroups();
    } catch (error) {
      toast.error(`提交失败：${(error as Error).message}`);
    } finally {
      setDeepening(false);
    }
  }

  const statCards = [
    { label: "发现总数", value: stats.total, icon: BugIcon },
    { label: "待处理", value: stats.pending, tone: "text-amber-500", icon: ClockIcon },
    { label: "严重", value: stats.critical, tone: "text-rose-600", icon: ShieldAlertIcon },
    { label: "高危", value: stats.high, tone: "text-red-500", icon: TriangleAlertIcon },
    { label: "中危", value: stats.medium, tone: "text-amber-500", icon: TriangleAlertIcon },
    { label: "低危", value: stats.low, tone: "text-slate-500", icon: InfoIcon },
  ];

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">发现</h1>
        <p className="text-muted-foreground text-sm">跨任务漏洞汇总</p>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {statCards.map((stat) => {
            const StatIcon = stat.icon;
            return (
              <Card key={stat.label} className="gap-1 py-4">
                <CardHeader className="px-4">
                  <CardDescription>{stat.label}</CardDescription>
                  <CardTitle className={cn("flex items-center gap-2 text-2xl tabular-nums", stat.tone)}>
                    <StatIcon className="size-5" aria-hidden="true" />
                    {stat.value}
                  </CardTitle>
                </CardHeader>
              </Card>
            );
          })}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <InputGroup className="w-full sm:w-72">
            <InputGroupInput
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="检索漏洞内容"
              aria-label="检索漏洞内容"
            />
            <InputGroupAddon>
              <SearchIcon aria-hidden="true" />
            </InputGroupAddon>
          </InputGroup>

          <ToggleGroup
            type="single"
            value={severity}
            onValueChange={(value) => value && setSeverity(value as "all" | Severity)}
            variant="outline"
            size="sm"
            spacing={0}
          >
            {(
              [
                ["all", "全部"],
                ["critical", "严重"],
                ["high", "高危"],
                ["medium", "中危"],
                ["low", "低危"],
              ] as const
            ).map(([val, label]) => (
              <ToggleGroupItem key={val} value={val} aria-label={`按${label}等级筛选`}>
                {label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>

          <Select value={status} onValueChange={(v) => setStatus(v as "all" | FindingStatus)}>
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
              <SelectItem value={UNASSIGNED_TASK}>未关联 / 任务已删除</SelectItem>
              {(stats.tasks ?? []).map((t) => {
                const id = String(t.id);
                const label = t.name || t.description || `任务 #${id}（已删除）`;
                return (
                  <SelectItem key={id} value={id}>
                    <span className="flex w-full items-center gap-2">
                      <span className="max-w-[14rem] truncate" title={label}>
                        {label}
                      </span>
                      <span className="inline-flex items-center gap-1 text-muted-foreground tabular-nums">
                        <BugIcon className="size-3.5" aria-hidden="true" />
                        {t.count}
                      </span>
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>

          <Select value={sort} onValueChange={(v) => setSort(v as "severity" | "time")}>
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
              <span className="text-xs text-muted-foreground tabular-nums">已选 {selectedIds.size} 条</span>
            )}
            <Button size="sm" variant="outline" onClick={openExport}>
              <DownloadIcon /> 导出
            </Button>
          </div>
        </div>

        <div className="flex flex-col gap-3">
          {groups.map((group) => {
            const key = findingGroupKey(group);
            const groupOpen = expandedGroups.has(key);
            const state = groupFindings[key] ?? {
              items: [],
              total: group.count,
              page: 1,
              pageSize: 10,
              loaded: false,
              loading: false,
            };
            const selectableIds = state.items
              .map((finding) => finding.finding_id)
              .filter((id): id is string => Boolean(id));
            const selectedCount = selectableIds.filter((id) => selectedIds.has(id)).length;
            let groupChecked: boolean | "indeterminate" = false;
            if (selectableIds.length > 0 && selectedCount === selectableIds.length) {
              groupChecked = true;
            } else if (selectedCount > 0) {
              groupChecked = "indeterminate";
            }
            return (
              <Card key={key} className="gap-0 py-0">
                <CardHeader className="px-4 py-3">
                  <div className="flex min-w-0 flex-wrap items-center gap-3">
                    <button
                      type="button"
                      className="flex min-w-0 flex-1 items-center gap-3 text-left"
                      aria-expanded={groupOpen}
                      onClick={() => toggleGroup(key)}
                    >
                      <ChevronRightIcon
                        className={cn(
                          "size-4 shrink-0 text-muted-foreground transition-transform",
                          groupOpen && "rotate-90",
                        )}
                      />
                      <div className="flex min-w-0 flex-col gap-1">
                        <CardTitle className="truncate text-sm">
                          {group.task_id === null
                            ? "未关联 / 任务已删除"
                            : group.task_name
                              ? `${group.task_name}（任务 #${group.task_id}）`
                              : `任务 #${group.task_id}`}
                        </CardTitle>
                        <CardDescription className="truncate" title={group.task_description}>
                          {group.task_description || "来源任务不可用"}
                        </CardDescription>
                      </div>
                    </button>
                    <div className="flex flex-wrap items-center gap-2">
                      {group.task_status && <StatusBadge domain="task" value={group.task_status} dot />}
                      {SEVERITIES.map((level) => {
                        const count = group[level];
                        if (count === 0) return null;
                        return (
                          <span key={level} className="inline-flex items-center gap-1">
                            <StatusBadge domain="severity" value={level} dot />
                            <span className="text-xs tabular-nums text-muted-foreground">{count}</span>
                          </span>
                        );
                      })}
                      <span className="text-xs tabular-nums text-muted-foreground">{fmtTime(group.last_found_at)}</span>
                      {group.task_id !== null && (
                        <Button size="icon-sm" variant="ghost" asChild>
                          <Link
                            href={`/function/tasks/detail?id=${group.task_id}`}
                            aria-label={`查看任务 #${group.task_id}`}
                          >
                            <ArrowUpRightIcon />
                          </Link>
                        </Button>
                      )}
                    </div>
                  </div>
                </CardHeader>
                {groupOpen && (
                  <CardContent className="px-0">
                    {state.loading && !state.loaded ? (
                      <div className="flex min-h-36 items-center justify-center">
                        <Spinner />
                      </div>
                    ) : (
                      <>
                        {/* table-fixed:列宽由表头锁定,展开行那个 colSpan 单元格再宽也只能在固定宽度内
                换行/内部滚动,不会把整张表撑出横向滚动条。 */}
                        <Table className="table-fixed">
                          <TableHeader>
                            <TableRow>
                              <TableHead className="w-8">
                                <Checkbox
                                  checked={groupChecked}
                                  onCheckedChange={(checked) => toggleSelectedPage(selectableIds, checked === true)}
                                  aria-label="选择本组当前页全部"
                                />
                              </TableHead>
                              <TableHead className="w-8" />
                              <TableHead className="w-20">严重度</TableHead>
                              <TableHead>漏洞名称</TableHead>
                              <TableHead className="w-52">资产</TableHead>
                              <TableHead className="w-28">状态</TableHead>
                              <TableHead className="w-36 max-w-[9rem]">所属任务</TableHead>
                              <TableHead className="w-32">时间</TableHead>
                              <TableHead className="w-32 text-right">操作</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {state.items.map((f) => {
                              const rowKey = findingRowKey(f);
                              const open = expanded === rowKey;
                              return (
                                <React.Fragment key={rowKey}>
                                  <TableRow
                                    className="cursor-pointer"
                                    role="button"
                                    tabIndex={0}
                                    aria-expanded={open}
                                    onClick={() => toggleRow(f)}
                                    onKeyDown={(event) => {
                                      if (
                                        event.target !== event.currentTarget ||
                                        (event.key !== "Enter" && event.key !== " ")
                                      )
                                        return;
                                      event.preventDefault();
                                      toggleRow(f);
                                    }}
                                  >
                                    <TableCell onClick={(e) => e.stopPropagation()}>
                                      {f.finding_id && (
                                        <Checkbox
                                          checked={selectedIds.has(f.finding_id)}
                                          onCheckedChange={(c) => toggleSelected(f.finding_id as string, c === true)}
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
                                        <span className="truncate text-xs text-muted-foreground">{f.summary}</span>
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
                                          onValueChange={(v) => updateStatus(f, v as FindingStatus)}
                                        >
                                          <SelectTrigger
                                            size="sm"
                                            className="h-7 w-full border-none px-1 shadow-none focus-visible:ring-0"
                                          >
                                            <StatusBadge domain="finding" value={f.status} dot />
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
                                    <TableCell className="text-right" onClick={(e) => e.stopPropagation()}>
                                      <div className="flex items-center justify-end gap-1">
                                        {f.finding_id && f.task_id && (
                                          <Button
                                            size="sm"
                                            variant="ghost"
                                            onClick={() => {
                                              setDeepenFinding(f);
                                              setDeepenDescription("");
                                            }}
                                          >
                                            <FlaskConicalIcon data-icon="inline-start" />
                                            深入
                                          </Button>
                                        )}
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
                                                  「
                                                  <span className="break-all">
                                                    {f.name || f.vulnclass || f.summary || `#${f.finding_id}`}
                                                  </span>
                                                  」将被永久删除， 同时从发现列表、任务发现 Tab
                                                  与探索图中移除，此操作不可撤销。
                                                </AlertDialogDescription>
                                              </AlertDialogHeader>
                                              <AlertDialogFooter>
                                                <AlertDialogCancel>取消</AlertDialogCancel>
                                                <AlertDialogAction onClick={() => deleteFinding(f)}>
                                                  删除
                                                </AlertDialogAction>
                                              </AlertDialogFooter>
                                            </AlertDialogContent>
                                          </AlertDialog>
                                        )}
                                      </div>
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
                                                {reports[rowKey]?.status === "done" && reports[rowKey]?.text.trim() && (
                                                  <CopyButton
                                                    text={reports[rowKey]?.text}
                                                    successMessage="已复制详细报告"
                                                    variant="ghost"
                                                    className="h-6 px-2 text-xs"
                                                  />
                                                )}
                                              </div>
                                              {(() => {
                                                const rep = reports[rowKey];
                                                if (!rep || rep.status === "loading")
                                                  return <p className="text-xs text-muted-foreground">加载中…</p>;
                                                if (rep.status === "error")
                                                  return (
                                                    <p className="text-xs text-muted-foreground">报告加载失败。</p>
                                                  );
                                                if (!rep.text.trim())
                                                  return (
                                                    <p className="text-xs text-muted-foreground">暂无详细报告。</p>
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
                            {state.items.length === 0 && (
                              <TableRow>
                                <TableCell colSpan={9} className="py-12 text-center text-sm text-muted-foreground">
                                  没有匹配的发现。
                                </TableCell>
                              </TableRow>
                            )}
                          </TableBody>
                        </Table>
                        <TablePagination
                          page={state.page}
                          pageSize={state.pageSize}
                          total={state.total}
                          onPageChange={(nextPage) => void loadGroup(key, nextPage, state.pageSize)}
                          onPageSizeChange={(nextSize) => void loadGroup(key, 1, nextSize)}
                        />
                      </>
                    )}
                  </CardContent>
                )}
              </Card>
            );
          })}
          {groups.length === 0 && (
            <Card>
              <CardContent className="py-12 text-center text-sm text-muted-foreground">没有匹配的发现。</CardContent>
            </Card>
          )}
          <TablePagination
            page={page}
            pageSize={pageSize}
            total={groupTotal}
            onPageChange={setPage}
            onPageSizeChange={(nextPageSize) => {
              setPageSize(nextPageSize);
              setPage(1);
            }}
            pageSizeOptions={[5, 10, 20]}
          />
        </div>
      </div>

      <Dialog
        open={deepenFinding !== null}
        onOpenChange={(open) => {
          if (open || deepening) return;
          setDeepenFinding(null);
          setDeepenDescription("");
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>深入利用漏洞</DialogTitle>
            <DialogDescription className="break-words">
              将在原任务 #{deepenFinding?.task_id} 中创建优先级 10 的 Worker 意图，基于当前漏洞开展二次验证：
              {deepenFinding?.name || deepenFinding?.vulnclass || deepenFinding?.summary}
            </DialogDescription>
          </DialogHeader>
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="finding-deepen-description">利用描述</FieldLabel>
              <Textarea
                id="finding-deepen-description"
                value={deepenDescription}
                onChange={(event) => setDeepenDescription(event.target.value)}
                maxLength={4000}
                placeholder="描述需要验证的利用路径、边界条件、目标或期望证据"
                disabled={deepening}
              />
              <FieldDescription className="flex justify-between gap-3">
                <span>新意图会继承该漏洞的资产锚点。</span>
                <span className="shrink-0 tabular-nums">{deepenDescription.length} / 4000</span>
              </FieldDescription>
            </Field>
          </FieldGroup>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setDeepenFinding(null);
                setDeepenDescription("");
              }}
              disabled={deepening}
            >
              取消
            </Button>
            <Button onClick={submitDeepen} disabled={deepening || !deepenDescription.trim()}>
              {deepening && <Spinner data-icon="inline-start" />}
              创建深入意图
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={exportOpen} onOpenChange={setExportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>导出发现</DialogTitle>
            <DialogDescription>选择导出范围与格式,生成后浏览器会自动下载。</DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-5 py-1">
            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">导出范围</span>
              <RadioGroup value={exportScope} onValueChange={(v) => setExportScope(v as typeof exportScope)}>
                <label htmlFor="export-scope-filtered" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-filtered" value="filtered" /> 导出当前筛选结果（共 {total} 条）
                </label>
                <label htmlFor="export-scope-all" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-scope-all" value="all" /> 导出全部
                </label>
                <label
                  htmlFor="export-scope-selected"
                  className={cn("flex items-center gap-2 text-sm", selectedIds.size === 0 && "text-muted-foreground")}
                >
                  <RadioGroupItem id="export-scope-selected" value="selected" disabled={selectedIds.size === 0} />
                  导出勾选的 {selectedIds.size} 条
                </label>
              </RadioGroup>
            </div>

            <div className="flex flex-col gap-2">
              <span className="text-xs text-muted-foreground">导出格式</span>
              <RadioGroup value={exportFormat} onValueChange={(v) => setExportFormat(v as typeof exportFormat)}>
                <label htmlFor="export-format-md-single" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-single" value="md-single" /> Markdown 汇总报告（单个 .md 文件）
                </label>
                <label htmlFor="export-format-md-zip" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-md-zip" value="md-zip" /> Markdown 分文件（一漏洞一 .md,打包 .zip）
                </label>
                <label htmlFor="export-format-csv" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-csv" value="csv" /> CSV 表格（.csv）
                </label>
                <label htmlFor="export-format-json" className="flex items-center gap-2 text-sm">
                  <RadioGroupItem id="export-format-json" value="json" /> JSON（.json）
                </label>
              </RadioGroup>
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setExportOpen(false)} disabled={exporting}>
              取消
            </Button>
            <Button onClick={doExport} disabled={exporting || (exportScope === "selected" && selectedIds.size === 0)}>
              <DownloadIcon /> {exporting ? "导出中…" : "导出"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
