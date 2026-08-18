"use client";

import * as React from "react";
import {
  ActivityIcon,
  AlertTriangleIcon,
  BugIcon,
  ClockIcon,
  RefreshCwIcon,
  ShieldCheckIcon,
  TargetIcon,
} from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { StatusBadge } from "@/components/status-badge";
import { api } from "@/lib/api";
import type { Task, Stats, TaskNode, Finding } from "@/lib/types";

function StatCard({
  label,
  value,
  sub,
  icon: Icon,
}: {
  label: string;
  value: React.ReactNode;
  sub?: string;
  icon: React.ElementType;
}) {
  return (
    <Card className="gap-1.5">
      <CardHeader className="pb-0">
        <CardDescription className="flex items-center gap-1.5">
          <Icon className="size-3.5" /> {label}
        </CardDescription>
        <CardTitle className="text-2xl tabular-nums">{value}</CardTitle>
      </CardHeader>
      {sub && (
        <CardContent className="text-xs text-muted-foreground">{sub}</CardContent>
      )}
    </Card>
  );
}

export function OverviewTab({ taskId }: { taskId: string }) {
  const [task, setTask] = React.useState<Task | null>(null);
  const [stats, setStats] = React.useState<Stats | null>(null);
  const [intents, setIntents] = React.useState<TaskNode[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [coverage, setCoverage] = React.useState<{
    scope_rows: number;
    denominator: number;
    tested: number;
    pct: number | null;
    by_type: { type: string; total: number; tested: number }[];
  } | null>(null);
  // 正在重跑的意图 id（含 "__all__" 表示批量），用于禁用按钮 + 转圈。
  const [rerunning, setRerunning] = React.useState<Set<string>>(new Set());

  const markRerun = (key: string, on: boolean) =>
    setRerunning((prev) => {
      const next = new Set(prev);
      if (on) next.add(key);
      else next.delete(key);
      return next;
    });

  // 重跑单条：置回 open（乐观更新本地 state，3s 轮询兜底），worker 会重新认领、从头再跑。
  const rerunOne = async (id: string) => {
    markRerun(id, true);
    try {
      await api.rerunIntent(taskId, id);
      setIntents((prev) =>
        prev.map((i) => (i.id === id ? { ...i, state: "open" } : i)),
      );
    } catch {
      // 失败忽略：下次轮询仍显示 blocked，用户可再点
    } finally {
      markRerun(id, false);
    }
  };

  // 批量重跑本任务全部 blocked。
  const rerunAll = async () => {
    markRerun("__all__", true);
    try {
      await api.rerunBlocked(taskId);
      setIntents((prev) =>
        prev.map((i) => (i.state === "blocked" ? { ...i, state: "open" } : i)),
      );
    } catch {
      // ignore
    } finally {
      markRerun("__all__", false);
    }
  };

  React.useEffect(() => {
    let cancelled = false;

    const load = async () => {
      try {
        const [tasksResp, statsResp, intentsResp, findingsResp] =
          await Promise.all([
            api.tasks(),
            api.stats(taskId),
            api.intents(taskId),
            api.findings(taskId),
          ]);
        if (cancelled) return;
        setTask(tasksResp.tasks.find((t) => t.id === taskId) ?? null);
        setStats(statsResp);
        setIntents(intentsResp);
        setFindings(findingsResp);
        // coverage is independent + may 503 when no asset store — fetch separately so
        // its failure never blocks the others.
        api
          .taskCoverage(taskId)
          .then((c) => {
            if (!cancelled) setCoverage(c);
          })
          .catch(() => {});
      } catch {
        // transient errors are ignored; the next poll will retry
      }
    };

    load();
    const timer = setInterval(load, 3000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [taskId]);

  const running = intents.filter((i) => i.state === "running");
  const open = intents.filter((i) => i.state === "open");
  const blocked = intents.filter((i) => i.state === "blocked");
  const taskFindings = findings.filter((f) => f.task_id === taskId);
  const goalsPct = task?.goals_total
    ? Math.round(((task.goals_met ?? 0) / task.goals_total) * 100)
    : 0;

  return (
    <div className="flex flex-col gap-4">
      {/* 原始任务描述与目标(创建时填写的),置顶便于随时回看。 */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <TargetIcon className="size-4 text-primary" /> 任务描述与目标
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div className="flex flex-col gap-1.5">
            <div className="text-xs font-medium text-muted-foreground">描述</div>
            <p className="text-sm whitespace-pre-wrap break-words">
              {task?.description?.trim() || "—"}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <div className="text-xs font-medium text-muted-foreground">目标</div>
            <p className="text-sm whitespace-pre-wrap break-words">
              {task?.goal?.trim() || "—"}
            </p>
          </div>
        </CardContent>
      </Card>
      {coverage && coverage.scope_rows > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-base">
              <TargetIcon className="size-4 text-emerald-500" /> 资产测试覆盖度
              <span className="text-muted-foreground text-xs font-normal">（粗估，仅供参考）</span>
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex items-baseline gap-3">
              <span className="text-2xl font-semibold tabular-nums">
                {coverage.pct != null ? Math.round(coverage.pct * 100) + "%" : "—"}
              </span>
              <span className="text-muted-foreground text-sm">
                已测 {coverage.tested} / 范围内 {coverage.denominator}
              </span>
            </div>
            {coverage.pct != null && <Progress value={Math.round(coverage.pct * 100)} />}
            {coverage.by_type.length > 0 && (
              <div className="flex flex-wrap gap-1.5 text-xs">
                {coverage.by_type.map((b) => (
                  <span key={b.type} className="bg-muted rounded px-1.5 py-0.5">
                    <span className="text-muted-foreground">{b.type}</span>{" "}
                    <span className="tabular-nums font-medium">
                      {b.tested}/{b.total}
                    </span>
                  </span>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      )}
      {/* Heartbeat */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <ActivityIcon className="size-4 text-blue-500" /> 心跳
          </CardTitle>
        </CardHeader>
        <CardContent className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <div className="text-xs text-muted-foreground">引擎态</div>
            <StatusBadge
              domain="engine"
              value={stats?.engine_mode ?? task?.engine_mode ?? "idle"}
              dot
              className="mt-1"
            />
          </div>
          <div>
            <div className="text-xs text-muted-foreground">运行中 Worker</div>
            <div className="mt-1 text-lg font-semibold tabular-nums">
              {running.length}
            </div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">最近活动</div>
            <div className="mt-1 inline-flex items-center gap-1 text-sm">
              <ClockIcon className="size-3.5" />
              {task?.last_activity
                ? new Date(task.last_activity).toLocaleTimeString("zh-CN")
                : "—"}
            </div>
          </div>
          <div>
            <div className="text-xs text-muted-foreground">
              目标 {task?.goals_met ?? 0}/{task?.goals_total ?? 0}
            </div>
            <Progress value={goalsPct} className="mt-2" />
          </div>
          {task?.completed_unix && task.completed_unix > 0 ? (
            <div>
              <div className="text-xs text-muted-foreground">完成时间</div>
              <div className="mt-1 inline-flex items-center gap-1 text-sm">
                <ClockIcon className="size-3.5" />
                {new Date(task.completed_unix * 1000).toLocaleString("zh-CN")}
              </div>
            </div>
          ) : null}
        </CardContent>
      </Card>

      {/* Work set */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <TargetIcon className="size-4" /> 进行中意图
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {running.slice(0, 6).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
              </div>
            ))}
            {running.length === 0 && (
              <p className="text-sm text-muted-foreground">暂无进行中意图</p>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-amber-500" /> 需要关注
            </CardTitle>
          </CardHeader>
          <CardContent className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <div className="text-2xl font-semibold tabular-nums text-red-600">
                {taskFindings.length}
              </div>
              <div className="text-xs text-muted-foreground">确认漏洞</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums text-blue-600">
                {running.length}
              </div>
              <div className="text-xs text-muted-foreground">执行中</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums">
                {open.length}
              </div>
              <div className="text-xs text-muted-foreground">frontier 待领</div>
            </div>
            <div>
              <div className="text-2xl font-semibold tabular-nums text-red-600">
                {blocked.length}
              </div>
              <div className="text-xs text-muted-foreground">被拦意图</div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm">
              <BugIcon className="size-4 text-red-500" /> 最近发现
            </CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {taskFindings.slice(0, 6).map((f) => (
              <div key={f.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="severity" value={f.severity} dot />
                <span className="min-w-0 flex-1 truncate">{f.summary}</span>
              </div>
            ))}
            {taskFindings.length === 0 && (
              <p className="text-sm text-muted-foreground">暂无发现</p>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Blocked intents — 出错/被拦(如 LLM 网络问题)的意图，可一键重跑：置回 open，
          worker 会重新认领、从头再跑（已写回图谱的数据保留）；任务若已终态/暂停会自动复活。 */}
      {blocked.length > 0 && (
        <Card className="border-red-500/30">
          <CardHeader className="flex-row items-center justify-between gap-2 space-y-0">
            <CardTitle className="flex items-center gap-2 text-sm">
              <AlertTriangleIcon className="size-4 text-red-500" /> 被拦/出错意图
              <span className="text-xs font-normal text-muted-foreground">
                （共 {blocked.length} 条，可重跑）
              </span>
            </CardTitle>
            <Button
              size="sm"
              variant="outline"
              disabled={rerunning.has("__all__")}
              onClick={() => void rerunAll()}
            >
              <RefreshCwIcon
                className={`size-3.5 ${rerunning.has("__all__") ? "animate-spin" : ""}`}
              />
              全部重跑
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-2">
            {blocked.slice(0, 20).map((i) => (
              <div key={i.id} className="flex items-center gap-2 text-sm">
                <StatusBadge domain="intent" value={i.state} />
                <span className="min-w-0 flex-1 truncate">{i.payload}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 shrink-0 px-2 text-xs"
                  disabled={rerunning.has(i.id)}
                  onClick={() => void rerunOne(i.id)}
                >
                  <RefreshCwIcon
                    className={`size-3 ${rerunning.has(i.id) ? "animate-spin" : ""}`}
                  />
                  重跑
                </Button>
              </div>
            ))}
            {blocked.length > 20 && (
              <p className="text-xs text-muted-foreground">
                仅显示前 20 条，点「全部重跑」处理剩余 {blocked.length - 20} 条。
              </p>
            )}
          </CardContent>
        </Card>
      )}

      {/* Stat cards */}
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <StatCard
          label="待领意图"
          value={open.length}
          icon={ShieldCheckIcon}
          sub="frontier 开放"
        />
        <StatCard
          label="确认发现"
          value={taskFindings.length}
          icon={BugIcon}
          sub="本任务"
        />
        <StatCard
          label="意图总数"
          value={intents.length}
          icon={AlertTriangleIcon}
          sub="本任务全部意图"
        />
      </div>
    </div>
  );
}
