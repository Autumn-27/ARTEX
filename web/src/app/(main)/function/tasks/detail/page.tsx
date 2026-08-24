"use client";

import * as React from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { toast } from "sonner";
import {
  ArrowLeftIcon,
  PauseIcon,
  PlayIcon,
  BrainIcon,
} from "lucide-react";

import { SidebarTrigger } from "@/components/ui/sidebar";
import { Separator } from "@/components/ui/separator";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { StatusBadge } from "@/components/status-badge";
import { api } from "@/lib/api";
import type { Task } from "@/lib/types";

import { SessionsTab } from "./_tabs/sessions-tab";
import { OverviewTab } from "./_tabs/overview-tab";
import { GraphTab } from "./_tabs/graph-tab";
import { FindingsTab } from "./_tabs/findings-tab";
import { ReportTab } from "./_tabs/report-tab";
import { InterceptTab } from "./_tabs/intercept-tab";
import { AssetsTab } from "./_tabs/assets-tab";
import { CoverageGraphTab } from "./_tabs/coverage-graph-tab";

const TABS = [
  { value: "sessions",  label: "会话" },
  { value: "overview",  label: "总览" },
  { value: "graph",     label: "探索链路" },
  { value: "findings",  label: "发现" },
  { value: "assets",    label: "测试资产" },
  { value: "coverage",  label: "资产覆盖图" },
  { value: "intercept", label: "拦截审批" },
  { value: "report",    label: "报告" },
];

function TaskDetailInner() {
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const [task, setTask] = React.useState<Task | null>(null);
  const [paused, setPaused] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState("sessions");
  const [interceptPendingCount, setInterceptPendingCount] = React.useState(0);

  React.useEffect(() => {
    let alive = true;
    const load = () =>
      api.interceptTask(id)
        .then((rows) => { if (alive) setInterceptPendingCount(rows.filter((r) => r.status === "pending").length); })
        .catch(() => {});
    load();
    const t = setInterval(load, 5000);
    return () => { alive = false; clearInterval(t); };
  }, [id]);

  const load = React.useCallback(() => {
    Promise.all([
      api.tasks(),
      api.stats(id).catch(() => null),
    ])
      .then(([r, s]) => {
        const base = r.tasks.find((t) => t.id === id) ?? null;
        const at = (s as { active_task?: Partial<Task> & { paused?: boolean } } | null)?.active_task;
        if (base && at) {
          base.in_flight = at.in_flight;
          base.goals_total = at.goals_total;
          base.goals_met = at.goals_met;
          base.engine_mode = at.engine_mode;
          base.paused = at.paused;
        }
        setTask(base);
        setPaused(at?.paused ?? base?.paused ?? false);
      })
      .catch(() => {})
      .finally(() => setLoaded(true));
  }, [id]);
  React.useEffect(() => { load(); }, [load]);

  async function togglePause() {
    const next = !paused;
    try {
      await api.controlTask(id, next ? "pause" : "resume");
      setPaused(next);
      toast.success(next ? "已暂停探索" : "已恢复探索");
    } catch (e) {
      toast.error("操作失败：" + (e as Error).message);
    }
  }

  if (!task) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? `未找到任务 ${id}` : "加载中…"}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/tasks">
              <ArrowLeftIcon /> 返回任务列表
            </Link>
          </Button>
        )}
      </div>
    );
  }

  const engineMode = paused ? "paused" : task.engine_mode ?? "idle";

  return (
    <Tabs
      value={tab}
      onValueChange={setTab}
      className="flex flex-1 flex-col gap-0"
    >
      {/* Top fixed area */}
      <header className="sticky top-0 z-10 flex flex-col gap-2 border-b bg-background/95 px-4 py-2.5 backdrop-blur lg:px-6">
        <div className="flex items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon" className="size-7">
            <Link href="/function/tasks">
              <ArrowLeftIcon />
            </Link>
          </Button>
<<<<<<< Updated upstream
          <h1 className="max-w-md truncate text-sm font-semibold" title={task.description}>
            {task.description}
=======
          <h1 className="min-w-0 flex-1 truncate text-sm font-semibold" title={task.name || task.description}>
            {task.name || task.description}
>>>>>>> Stashed changes
          </h1>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
            {task.id}
          </code>
          <Separator orientation="vertical" className="mx-1 h-4" />
          {/* status pills */}
          <span className="inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs">
            <BrainIcon className="size-3.5 text-emerald-500" /> LLM 已配置
          </span>
          <StatusBadge domain="engine" value={engineMode} dot />
          <div className="ml-auto">
            <Button
              size="sm"
              variant={paused ? "default" : "outline"}
              onClick={togglePause}
            >
              {paused ? <PlayIcon /> : <PauseIcon />}
              {paused ? "恢复" : "暂停"}
            </Button>
          </div>
        </div>
        <p className="truncate text-xs text-muted-foreground">{task.goal}</p>
        {/* Tabs */}
        <TabsList variant="default">
          {TABS.map((t) => (
            <TabsTrigger key={t.value} value={t.value}>
              {t.label}
              {t.value === "intercept" && interceptPendingCount > 0 && (
                <span className="ml-1.5 inline-flex h-4 min-w-[16px] items-center justify-center rounded-full bg-amber-500 px-1 text-[10px] font-semibold leading-none text-white">
                  {interceptPendingCount > 99 ? "99+" : interceptPendingCount}
                </span>
              )}
            </TabsTrigger>
          ))}
        </TabsList>
      </header>

      {/* Tab content */}
      <div className="flex-1 p-4 lg:p-6">
        <TabsContent value="sessions" className="mt-0">
          <SessionsTab taskId={id} />
        </TabsContent>
        <TabsContent value="overview" className="mt-0">
          <OverviewTab taskId={id} />
        </TabsContent>
        <TabsContent value="graph" className="mt-0">
          <GraphTab taskId={id} />
        </TabsContent>
        <TabsContent value="findings" className="mt-0">
          <FindingsTab taskId={id} />
        </TabsContent>
        <TabsContent value="assets" className="mt-0">
          <AssetsTab taskId={id} />
        </TabsContent>
        <TabsContent value="coverage" className="mt-0">
          <CoverageGraphTab taskId={id} coverageEnabled={task?.coverage_enabled !== false} />
        </TabsContent>
        <TabsContent value="intercept" className="mt-0">
          <InterceptTab taskId={id} />
        </TabsContent>
        <TabsContent value="report" className="mt-0">
          <ReportTab taskId={id} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function TaskDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <TaskDetailInner />
    </React.Suspense>
  );
}
