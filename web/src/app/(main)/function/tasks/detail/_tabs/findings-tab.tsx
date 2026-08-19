"use client";

import * as React from "react";
import Link from "next/link";
import { ArrowUpRightIcon, ChevronRightIcon, Trash2Icon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { api } from "@/lib/api";
import { toast } from "sonner";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus } from "@/lib/types";

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
  onStatus,
  onDelete,
}: {
  f: Finding;
  onStatus: (f: Finding, next: FindingStatus) => void;
  onDelete: (f: Finding) => void;
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
          <div className="flex min-w-0 flex-col">
            <span className="truncate font-medium">
              {f.name || f.vulnclass || "未分类"}
            </span>
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
        {f.finding_id ? (
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
        <span className="hidden shrink-0 text-xs text-muted-foreground md:block">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
        {f.finding_id && (
          <Link
            href={`/function/findings/detail?id=${f.finding_id}`}
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
          <div className="mb-1 flex items-center justify-between gap-2">
            <span className="text-xs font-medium text-muted-foreground">证据 / PoC</span>
            {f.finding_id && (
              <AlertDialog>
                <AlertDialogTrigger asChild>
                  <Button size="sm" variant="outline" className="h-7 text-destructive">
                    <Trash2Icon /> 删除
                  </Button>
                </AlertDialogTrigger>
                <AlertDialogContent>
                  <AlertDialogHeader>
                    <AlertDialogTitle>确认删除该漏洞？</AlertDialogTitle>
                    <AlertDialogDescription>
                      「{f.name || f.vulnclass || f.summary || `#${f.finding_id}`}」将被永久删除，
                      同时从发现列表、任务发现 Tab 与探索图中移除，此操作不可撤销。
                    </AlertDialogDescription>
                  </AlertDialogHeader>
                  <AlertDialogFooter>
                    <AlertDialogCancel>取消</AlertDialogCancel>
                    <AlertDialogAction onClick={() => onDelete(f)}>删除</AlertDialogAction>
                  </AlertDialogFooter>
                </AlertDialogContent>
              </AlertDialog>
            )}
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

  React.useEffect(() => {
    let active = true;
    const load = () => {
      api
        .findings(taskId)
        .then((fs) => {
          if (active) setFindings(fs);
        })
        .catch(() => {});
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
      if (!f.finding_id || next === f.status) return;
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

  const onDelete = React.useCallback(async (f: Finding) => {
    if (!f.finding_id) return;
    try {
      await api.deleteFinding(f.finding_id);
      setFindings((cur) => cur.filter((x) => x.id !== f.id));
      toast.success("已删除漏洞");
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }, []);

  const items = findings
    .filter((f) => f.task_id === taskId)
    .sort((a, b) => {
      const order = { critical: 0, high: 1, medium: 2, low: 3 };
      return order[a.severity] - order[b.severity];
    });

  return (
    <Card className="overflow-hidden py-0">
      <CardContent className="px-0">
        {items.map((f) => (
          <Row key={f.id} f={f} onStatus={onStatus} onDelete={onDelete} />
        ))}
        {items.length === 0 && (
          <p className="px-4 py-8 text-center text-sm text-muted-foreground">
            本任务暂无确认发现。
          </p>
        )}
      </CardContent>
    </Card>
  );
}
