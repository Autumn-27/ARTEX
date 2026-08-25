"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowDownIcon, ArrowUpIcon, ArrowUpRightIcon, ChevronRightIcon } from "lucide-react";
import { toast } from "sonner";

import { StatusBadge } from "@/components/status-badge";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import { useStoredSortPreference } from "@/lib/sort-preference";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

type FindingSortField = "time";

const FINDING_SORT_FIELDS: readonly FindingSortField[] = ["time"];
const FINDING_SORT_PREFERENCE_KEY = "artex_task_findings_sort";

function findingLabel(finding: Finding): string {
  if (finding.name?.trim()) return finding.name;
  if (finding.vulnclass?.trim()) return finding.vulnclass;
  return "未分类";
}

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

function Row({
  f,
  contextTaskId,
  onStatus,
}: {
  f: Finding;
  contextTaskId: string;
  onStatus: (f: Finding, next: FindingStatus) => void;
}) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="border-b last:border-b-0">
      <div className="flex w-full items-center gap-3 px-4 py-3 text-sm hover:bg-accent/40">
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="flex min-w-0 flex-1 items-center gap-3 text-left"
        >
          <ChevronRightIcon
            className={cn(
              "size-4 shrink-0 text-muted-foreground transition-transform",
              open && "rotate-90",
            )}
          />
          <StatusBadge domain="severity" value={f.severity} dot />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate font-medium">{findingLabel(f)}</span>
              {f.inherited && f.source_task_id && (
                <Badge variant="outline" className="shrink-0">
                  来源 #{f.source_task_id} · 只读
                </Badge>
              )}
            </div>
            <span className="truncate text-xs text-muted-foreground">
              {f.summary}
            </span>
          </div>
        </button>
        {f.assets && f.assets.length > 0 && (
          <div className="hidden shrink-0 flex-wrap justify-end gap-1 sm:flex">
            {f.assets.slice(0, 2).map((a) => (
              <code
                key={a.id}
                className="max-w-[10rem] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                title={`${a.type} · ${a.label}`}
              >
                {a.label}
              </code>
            ))}
            {f.assets.length > 2 && (
              <span className="text-xs text-muted-foreground">
                +{f.assets.length - 2}
              </span>
            )}
          </div>
        )}
        {f.finding_id && !f.inherited ? (
          <Select
            value={f.status}
            onValueChange={(v) => onStatus(f, v as FindingStatus)}
          >
            <SelectTrigger
              size="sm"
              className="h-7 w-28 shrink-0 border-none px-1 shadow-none focus-visible:ring-0"
            >
              <StatusBadge domain="finding" value={f.status} dot />
            </SelectTrigger>
            <SelectContent position="popper" align="end">
              <SelectGroup>
                {FINDING_STATUSES.map((st) => (
                  <SelectItem key={st} value={st}>
                    {statusMeta("finding", st).label}
                  </SelectItem>
                ))}
              </SelectGroup>
            </SelectContent>
          </Select>
        ) : (
          <StatusBadge domain="finding" value={f.status} dot />
        )}
        <span className="hidden shrink-0 text-xs text-muted-foreground md:block">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
        {f.finding_id && (
          <Link
            href={
              f.inherited
                ? `/function/findings/detail?id=${f.finding_id}&context_task=${contextTaskId}`
                : `/function/findings/detail?id=${f.finding_id}`
            }
            className="text-muted-foreground hover:text-primary inline-flex shrink-0 items-center gap-0.5 text-xs"
            title="查看漏洞详情"
          >
            详情
            <ArrowUpRightIcon className="size-3" />
          </Link>
        )}
      </div>
      {open && (
        <div className="bg-muted/30 px-4 pb-4 pl-11">
          <div className="mb-1 text-xs font-medium text-muted-foreground">
            证据 / PoC
          </div>
          <pre className="overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap">
            {f.evidence}
          </pre>
        </div>
      )}
    </div>
  );
}

export function FindingsTab({ taskId }: { taskId: string }) {
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [sortPreference, setSortPreference] = useStoredSortPreference(
    FINDING_SORT_PREFERENCE_KEY,
    FINDING_SORT_FIELDS,
    "time",
    "desc",
  );

  React.useEffect(() => {
    let active = true;
    const load = () => {
      api
        .findings(taskId)
        .then((fs) => {
          if (active) setFindings(fs);
        })
        .catch(() => {
          // Keep the last successful snapshot during transient refresh failures.
        });
    };
    load();
    const t = setInterval(load, 3000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [taskId]);

  const onStatus = React.useCallback(
    async (f: Finding, next: FindingStatus) => {
      if (f.inherited || !f.finding_id || next === f.status) return;
      const prev = f.status;
      setFindings((cur) =>
        cur.map((x) => (x.id === f.id ? { ...x, status: next } : x)),
      );
      try {
        await api.setFindingStatus(f.finding_id, next);
        toast.success(`已标记为「${statusMeta("finding", next).label}」`);
      } catch (e) {
        setFindings((cur) =>
          cur.map((x) => (x.id === f.id ? { ...x, status: prev } : x)),
        );
        toast.error("更新失败：" + (e as Error).message);
      }
    },
    [],
  );

  const items = findings
    .filter((f) => f.task_id === taskId || f.inherited)
    .sort((a, b) => {
      const compared = Date.parse(a.ts) - Date.parse(b.ts);
      if (compared !== 0) return sortPreference.direction === "asc" ? compared : -compared;
      return b.id.localeCompare(a.id, undefined, { numeric: true });
    });

  return (
    <Card className="overflow-hidden py-0">
      <CardContent className="px-0">
        <div className="flex items-center border-b px-4 py-2 text-xs text-muted-foreground">
          <span className="min-w-0 flex-1">漏洞</span>
          <button
            type="button"
            className="inline-flex items-center gap-1 outline-none focus-visible:underline"
            aria-label={`发现时间当前${sortPreference.direction === "asc" ? "正序" : "倒序"}，点击切换排序方向`}
            onClick={() =>
              setSortPreference((current) => ({
                field: "time",
                direction: current.direction === "asc" ? "desc" : "asc",
              }))
            }
          >
            <span>发现时间</span>
            {sortPreference.direction === "asc" ? (
              <ArrowUpIcon className="size-3.5" />
            ) : (
              <ArrowDownIcon className="size-3.5" />
            )}
          </button>
        </div>
        {items.map((f) => (
          <Row key={f.id} f={f} contextTaskId={taskId} onStatus={onStatus} />
        ))}
        {items.length === 0 && (
          <p className="px-4 py-8 text-center text-sm text-muted-foreground">
            本任务及直接关联任务暂无确认发现。
          </p>
        )}
      </CardContent>
    </Card>
  );
}
