"use client";

import * as React from "react";

import { Loader2Icon, RefreshCwIcon, UnplugIcon } from "lucide-react";
import { toast } from "sonner";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { TunnelInfo } from "@/lib/types";
import { cn } from "@/lib/utils";

import { fmtTime } from "./format";

function StateBadge({ state }: { state: string }) {
  const cls =
    state === "alive"
      ? "text-emerald-600"
      : state === "error"
        ? "text-red-600 dark:text-red-400"
        : "text-muted-foreground";
  const dot = state === "alive" ? "bg-emerald-500" : state === "error" ? "bg-red-500" : "bg-neutral-400";
  const label = state === "alive" ? "存活" : state === "error" ? "失败" : state === "stopped" ? "已停" : state || "—";
  return (
    <Badge variant={state === "alive" ? "secondary" : "outline"} className={cn("gap-1.5 text-xs", cls)}>
      <span className={cn("size-1.5 rounded-full", dot)} />
      {label}
    </Badge>
  );
}

// 隧道台账：状态颜色区分，存活隧道可二次确认后回收（teardown）。
export function TunnelsTab({
  tunnels,
  loading,
  onRefresh,
}: {
  tunnels: TunnelInfo[];
  loading: boolean;
  onRefresh: () => void;
}) {
  const [pendingTeardown, setPendingTeardown] = React.useState<TunnelInfo | null>(null);
  const [tearingDown, setTearingDown] = React.useState(false);

  const confirmTeardown = () => {
    const target = pendingTeardown;
    if (!target) return;
    setTearingDown(true);
    api
      .teardownTunnel(target.id)
      .then(() => {
        toast.success(`隧道 #${target.id} 已回收`);
        setPendingTeardown(null);
        onRefresh();
      })
      .catch((e: unknown) => toast.error(e instanceof Error ? e.message : "回收失败"))
      .finally(() => setTearingDown(false));
  };

  return (
    <>
      <div className="flex items-center justify-end gap-2 px-1 pb-2">
        <Button variant="outline" size="sm" className="h-7 gap-1" onClick={onRefresh} disabled={loading}>
          <RefreshCwIcon className={cn("size-3.5", loading && "animate-spin")} />
          刷新
        </Button>
      </div>
      <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
        <div className="min-h-0 flex-1 overflow-auto">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead className="w-[70px]">ID</TableHead>
                <TableHead className="w-[90px]">任务</TableHead>
                <TableHead className="w-[110px]">类型</TableHead>
                <TableHead className="w-[90px]">状态</TableHead>
                <TableHead className="w-[180px]">入口</TableHead>
                <TableHead>目标</TableHead>
                <TableHead className="w-[110px]">经会话</TableHead>
                <TableHead className="w-[140px]">最近探活</TableHead>
                <TableHead className="w-[160px]">错误</TableHead>
                <TableHead className="w-[90px]">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loading && tunnels.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="py-12 text-center">
                    <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                  </TableCell>
                </TableRow>
              ) : tunnels.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="py-12 text-center text-sm text-muted-foreground">
                    暂无隧道
                  </TableCell>
                </TableRow>
              ) : (
                tunnels.map((t) => {
                  // 后端字段是 listen_host/listen_port/target_host/target_port。
                  const entry = t.listen_host ? `${t.listen_host}:${t.listen_port}` : "—";
                  const target = t.target_host ? `${t.target_host}:${t.target_port}` : "—";
                  return (
                  <TableRow key={t.id}>
                    <TableCell className="font-mono text-xs text-muted-foreground">#{t.id}</TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {t.task_id ? `#${t.task_id}` : "—"}
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary" className="font-mono text-xs">
                        {t.kind || "-"}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <StateBadge state={t.state} />
                    </TableCell>
                    <TableCell className="max-w-0">
                      <code className="block truncate font-mono text-xs" title={entry}>
                        {entry}
                      </code>
                    </TableCell>
                    <TableCell className="max-w-0">
                      <code className="block truncate font-mono text-xs" title={target}>
                        {target}
                      </code>
                    </TableCell>
                    <TableCell className="font-mono text-xs text-muted-foreground">
                      {t.via_session_id ? `#${t.via_session_id}` : "—"}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground tabular-nums">
                      {fmtTime(t.last_check)}
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span
                        className={cn(
                          "block truncate text-xs",
                          t.error ? "text-red-600 dark:text-red-400" : "text-muted-foreground",
                        )}
                        title={t.error ?? ""}
                      >
                        {t.error || "—"}
                      </span>
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="outline"
                        size="sm"
                        className="h-7 gap-1"
                        disabled={t.state !== "alive"}
                        title={t.state === "alive" ? "回收该隧道" : "仅存活隧道可回收"}
                        onClick={() => setPendingTeardown(t)}
                      >
                        <UnplugIcon className="size-3.5" />
                        回收
                      </Button>
                    </TableCell>
                  </TableRow>
                  );
                })
              )}
            </TableBody>
          </Table>
        </div>
      </Card>

      <AlertDialog open={pendingTeardown !== null} onOpenChange={(o) => !o && setPendingTeardown(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>回收隧道 #{pendingTeardown?.id}？</AlertDialogTitle>
            <AlertDialogDescription>
              将拆除 {pendingTeardown?.kind} 隧道（入口{" "}
              {pendingTeardown?.listen_host
                ? `${pendingTeardown.listen_host}:${pendingTeardown.listen_port}`
                : "—"}
              ），正在经它代理的连接会立即中断。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={tearingDown}>取消</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={tearingDown}
              onClick={(e) => {
                e.preventDefault();
                confirmTeardown();
              }}
            >
              {tearingDown ? "回收中…" : "确认回收"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
