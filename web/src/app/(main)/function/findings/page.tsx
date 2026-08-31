"use client";

import * as React from "react";

import Link from "next/link";

import {
  ArrowUpRightIcon,
  BugIcon,
  ChevronRightIcon,
  ClockIcon,
  DownloadIcon,
  InfoIcon,
  SearchIcon,
  ShieldAlertIcon,
  TriangleAlertIcon,
} from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { TablePagination } from "@/components/table-pagination";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupInput } from "@/components/ui/input-group";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { api } from "@/lib/api";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingGroup, FindingStats, FindingStatus, Severity } from "@/lib/types";
import { cn } from "@/lib/utils";

import {
  FINDING_STATUSES,
  type FindingEdit,
  type FindingReport,
  FindingsTable,
  findingRowKey,
  fmtTime,
  isSameFinding,
  SEVERITIES,
  UNASSIGNED_TASK,
} from "./_components/findings-table";

const FINDING_LIST_PREFERENCE_KEY = "artex_finding_list_preferences";

// 列表视图:flat = 跨任务平铺大表(默认);grouped = 按任务分组折叠。
type FindingView = "flat" | "grouped";

const FINDING_VIEWS: FindingView[] = ["flat", "grouped"];

// 分组视图里每个已展开任务组自带一份分页状态,彼此独立。
interface GroupFindingsState {
  items: Finding[];
  total: number;
  page: number;
  pageSize: number;
  loaded: boolean;
  loading: boolean;
}

// 平铺视图的页码单独放 state(而非塞进快照),筛选一变就能连带重置并触发重新加载。
interface FlatFindingsState {
  items: Finding[];
  total: number;
  loaded: boolean;
  loading: boolean;
}

const EMPTY_FLAT_STATE: FlatFindingsState = { items: [], total: 0, loaded: false, loading: false };

function findingGroupKey(group: FindingGroup) {
  return group.task_id === null ? UNASSIGNED_TASK : String(group.task_id);
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

export default function FindingsPage() {
  const [view, setView] = React.useState<FindingView>("flat");
  const [severity, setSeverity] = React.useState<"all" | Severity>("all");
  const [status, setStatus] = React.useState<"all" | FindingStatus>("all");
  const [vulnclass, setVulnclass] = React.useState<string>("all");
  const [task, setTask] = React.useState<string>("all");
  const [sort, setSort] = React.useState<"severity" | "time">("severity");
  const [search, setSearch] = React.useState("");
  const [query, setQuery] = React.useState("");
  const [expanded, setExpanded] = React.useState<string | null>(null);
  const [flat, setFlat] = React.useState<FlatFindingsState>(EMPTY_FLAT_STATE);
  const [flatPage, setFlatPage] = React.useState(1);
  const [flatPageSize, setFlatPageSize] = React.useState(20);
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
          view?: unknown;
          severity?: unknown;
          status?: unknown;
          vulnclass?: unknown;
          task?: unknown;
          sort?: unknown;
        };
        if (FINDING_VIEWS.includes(parsed.view as FindingView)) setView(parsed.view as FindingView);
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
    setLocalStorageValue(
      FINDING_LIST_PREFERENCE_KEY,
      JSON.stringify({ view, severity, status, vulnclass, task, sort }),
    );
  }, [preferencesHydrated, severity, sort, status, task, view, vulnclass]);

  React.useEffect(() => {
    const timer = window.setTimeout(() => setQuery(search.trim()), 300);
    return () => window.clearTimeout(timer);
  }, [search]);

  // setFindings 同时改写两个视图缓存里的同一条发现,切换视图不会看到过期状态。
  const setFindings = React.useCallback((update: (current: Finding[]) => Finding[]) => {
    setFlat((current) => ({ ...current, items: update(current.items) }));
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

  const flatRequest = React.useRef(0);
  const groupRequests = React.useRef<Record<string, number>>({});
  const groupsRequest = React.useRef(0);
  const flatStateRef = React.useRef(flat);
  const expandedGroupsRef = React.useRef(expandedGroups);
  const groupFindingsRef = React.useRef(groupFindings);
  const visibleGroupKeysRef = React.useRef<Set<string>>(new Set());
  flatStateRef.current = flat;
  expandedGroupsRef.current = expandedGroups;
  groupFindingsRef.current = groupFindings;
  visibleGroupKeysRef.current = new Set(groups.map(findingGroupKey));

  // loadFlat 拉取平铺视图的当前页;task 筛选交给后端,与分组视图共用同一批筛选条件。
  const loadFlat = React.useCallback(async () => {
    const requestFilter = filterFingerprint;
    if (activeFilterFingerprint.current !== requestFilter) return;
    const request = ++flatRequest.current;
    setFlat((current) => ({ ...current, loading: true }));
    try {
      const result = await api.findingsPage({
        page: flatPage,
        pageSize: flatPageSize,
        severity,
        status,
        vulnclass,
        task,
        query,
        sort,
      });
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      setFlat({ items: result.items, total: result.total, loaded: true, loading: false });
    } catch {
      if (request !== flatRequest.current || activeFilterFingerprint.current !== requestFilter) return;
      // Polling keeps the last successful snapshot visible.
      setFlat((current) => ({ ...current, loading: false }));
    }
  }, [filterFingerprint, flatPage, flatPageSize, severity, status, vulnclass, task, query, sort]);

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

  // 行内改动后刷新当前视图:平铺视图重拉当前页,分组视图刷组头 + 该发现所在的组。
  const refreshAfterMutation = React.useCallback(
    (finding: Finding, removed = false) => {
      if (view === "flat") {
        // 删空最后一页时,页码由越界修正 effect 回退并连带重新加载。
        void loadFlat();
        return;
      }
      void refreshGroups();
      const key = finding.task_id ?? UNASSIGNED_TASK;
      const state = groupFindingsRef.current[key];
      if (state?.loaded) {
        const nextTotal = Math.max(0, state.total - (removed ? 1 : 0));
        const lastPage = Math.max(1, Math.ceil(nextTotal / state.pageSize));
        void loadGroup(key, Math.min(state.page, lastPage), state.pageSize);
      }
    },
    [loadFlat, loadGroup, refreshGroups, view],
  );

  // Reset every view's pagination and expansion when a shared finding filter changes.
  React.useEffect(() => {
    void filterFingerprint;
    setPage(1);
    setExpanded(null);
    setExpandedGroups(new Set());
    setGroupFindings({});
    setFlatPage(1);
    setFlat(EMPTY_FLAT_STATE);
  }, [filterFingerprint]);

  // 只轮询当前视图:平铺视图刷当前页,分组视图刷组头与每个已展开的组(其分页彼此独立)。
  // 等偏好水合后再发首个请求,否则会先按默认视图/筛选白拉一次。
  React.useEffect(() => {
    if (!preferencesHydrated) return;
    const refresh = () => {
      if (view === "flat") {
        if (!flatStateRef.current.loading) void loadFlat();
        return;
      }
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
  }, [loadFlat, loadGroup, preferencesHydrated, refreshGroups, view]);

  React.useEffect(() => {
    const lastPage = Math.max(1, Math.ceil(groupTotal / pageSize));
    if (page > lastPage) setPage(lastPage);
  }, [groupTotal, page, pageSize]);

  React.useEffect(() => {
    if (!flat.loaded) return;
    const lastPage = Math.max(1, Math.ceil(flat.total / flatPageSize));
    if (flatPage > lastPage) setFlatPage(lastPage);
  }, [flat.loaded, flat.total, flatPage, flatPageSize]);

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
          setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        }
        refreshAfterMutation(f);
      } catch (e) {
        setFindings((cur) => cur.map((x) => (isSameFinding(x, f) ? { ...x, status: prev } : x)));
        toast.error(`更新失败：${(e as Error).message}`);
      }
    },
    [refreshAfterMutation, setFindings, status],
  );

  // 行内展开的详细报告缓存按全局稳定行键存。report 是大段 Markdown,列表查询不带它,
  // 故展开时才按 finding_id 单独拉取一次;done 且文本为空 = 该漏洞暂无报告。
  const [reports, setReports] = React.useState<Record<string, FindingReport>>({});

  // 行内可编辑缓冲:当前展开行的名称/类别/严重等级,展开时用该行数据初始化,收起清空。
  // 单行展开,故一份缓冲即可。
  const [edit, setEdit] = React.useState<FindingEdit | null>(null);
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
        refreshAfterMutation(f);
      } catch (e) {
        toast.error(`保存失败：${(e as Error).message}`);
      } finally {
        setSaving(false);
      }
    },
    [edit, refreshAfterMutation, setFindings],
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
        setFlat((cur) => ({ ...cur, total: Math.max(0, cur.total - 1) }));
        const rowKey = findingRowKey(f);
        setExpanded((cur) => (cur === rowKey ? null : cur));
        toast.success("已删除漏洞");
        api
          .findingStats()
          .then(setStats)
          .catch(() => {
            // The deletion remains valid even if the aggregate refresh fails.
          });
        refreshAfterMutation(f, true);
      } catch (e) {
        toast.error(`删除失败：${(e as Error).message}`);
      }
    },
    [refreshAfterMutation, setFindings],
  );

  const openDeepen = React.useCallback((f: Finding) => {
    setDeepenFinding(f);
    setDeepenDescription("");
  }, []);

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
      refreshAfterMutation(deepenFinding);
      setDeepenFinding(null);
      setDeepenDescription("");
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

  // 导出弹窗里「当前筛选」的条数:两个视图的筛选一致,只是统计口径来源不同。
  const filteredTotal = view === "flat" ? flat.total : total;

  const rowProps = {
    selectedIds,
    onToggleSelected: toggleSelected,
    onToggleSelectedPage: toggleSelectedPage,
    expandedKey: expanded,
    onToggleRow: toggleRow,
    reports,
    edit,
    onEditChange: setEdit,
    saving,
    onSave: saveEdit,
    onStatusChange: updateStatus,
    onDeepen: openDeepen,
    onDelete: deleteFinding,
  };

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">发现</h1>
          <p className="text-muted-foreground text-sm">跨任务漏洞汇总</p>
        </div>
        <Tabs value={view} onValueChange={(v) => setView(v as FindingView)}>
          <TabsList>
            <TabsTrigger value="flat">全部发现</TabsTrigger>
            <TabsTrigger value="grouped">按任务分组</TabsTrigger>
          </TabsList>
        </Tabs>
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

        {view === "flat" ? (
          <Card className="gap-0 py-0">
            <CardContent className="px-0">
              {flat.loading && !flat.loaded ? (
                <div className="flex min-h-36 items-center justify-center">
                  <Spinner />
                </div>
              ) : (
                <>
                  <FindingsTable items={flat.items} selectAllLabel="选择当前页全部" {...rowProps} />
                  <TablePagination
                    page={flatPage}
                    pageSize={flatPageSize}
                    total={flat.total}
                    onPageChange={setFlatPage}
                    onPageSizeChange={(nextSize) => {
                      setFlatPageSize(nextSize);
                      setFlatPage(1);
                    }}
                    pageSizeOptions={[10, 20, 50, 100]}
                  />
                </>
              )}
            </CardContent>
          </Card>
        ) : (
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
                        <span className="text-xs tabular-nums text-muted-foreground">
                          {fmtTime(group.last_found_at)}
                        </span>
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
                          <FindingsTable items={state.items} selectAllLabel="选择本组当前页全部" {...rowProps} />
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
        )}
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
                  <RadioGroupItem id="export-scope-filtered" value="filtered" /> 导出当前筛选结果（共 {filteredTotal}{" "}
                  条）
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
