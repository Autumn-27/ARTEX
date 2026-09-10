"use client";

import * as React from "react";

import Link from "next/link";

import { RotateCcwIcon } from "lucide-react";

import { FindingRetestDialog } from "@/components/finding-retest-dialog";
import { Markdown } from "@/components/markdown";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { api } from "@/lib/api";
import type { FindingRetest } from "@/lib/types";

const statusLabels = {
  pending: "等待启动",
  running: "复测中",
  completed: "已完成",
  failed: "复测失败",
  stopped: "已停止",
};
const verdictLabels = { reproduced: "仍可复现", fixed: "已修复", inconclusive: "无法确认" };

function active(r: FindingRetest) {
  return r.status === "pending" || r.status === "running";
}

export function FindingRetestPanel({
  findingId,
  findingName,
  readOnly = false,
  onCompleted,
}: {
  findingId: string;
  findingName?: string;
  readOnly?: boolean;
  onCompleted?: () => void;
}) {
  const [items, setItems] = React.useState<FindingRetest[] | null>(null);
  const [error, setError] = React.useState("");
  const [open, setOpen] = React.useState(false);
  const requestSeq = React.useRef(0);
  const previousItems = React.useRef<FindingRetest[]>([]);

  const load = React.useCallback(async () => {
    const seq = ++requestSeq.current;
    try {
      const rows = await api.findingRetests(findingId);
      if (seq !== requestSeq.current) return;
      const completed = previousItems.current.some(
        (item) => active(item) && rows.some((row) => row.id === item.id && !active(row)),
      );
      previousItems.current = rows;
      setItems(rows);
      setError("");
      if (completed) onCompleted?.();
    } catch (e) {
      if (seq === requestSeq.current) setError((e as Error).message);
    }
  }, [findingId, onCompleted]);

  React.useEffect(() => {
    void load();
    return () => {
      requestSeq.current++;
    };
  }, [load]);

  React.useEffect(() => {
    if (error || !items?.some(active)) return;
    const timer = setTimeout(() => void load(), 3000);
    return () => clearTimeout(timer);
  }, [error, items, load]);

  const running = items?.find(active);

  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1.5">
          <CardTitle>漏洞复测</CardTitle>
          <CardDescription>在独立会话中验证当前状态，保留每次复测的结论与证据。</CardDescription>
        </div>
        {running?.conversation_id != null ? (
          <Button asChild variant="outline" size="sm">
            <Link href={`/chat?c=${running.conversation_id}`} title="查看正在进行的复测会话">
              <Spinner data-icon="inline-start" aria-hidden="true" />
              复测中
            </Link>
          </Button>
        ) : null}
        {!running && !readOnly ? (
          <Button size="sm" onClick={() => setOpen(true)} disabled={items === null || !!error}>
            <RotateCcwIcon data-icon="inline-start" />
            发起复测
          </Button>
        ) : null}
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {error ? (
          <Alert variant="destructive">
            <AlertDescription>
              加载复测记录失败：{error}
              <Button variant="outline" size="sm" onClick={() => void load()}>
                重试
              </Button>
            </AlertDescription>
          </Alert>
        ) : null}
        {!error && items === null ? <Skeleton className="h-16 w-full" /> : null}
        {!error && items?.length === 0 ? (
          <Empty>
            <EmptyHeader>
              <EmptyTitle>暂无复测记录</EmptyTitle>
              <EmptyDescription>修复部署完成后，可发起复测并比较新旧证据。</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : null}
        {!error && items
          ? items.map((item) => (
              <div key={item.id} className="flex min-w-0 flex-col gap-2 rounded-lg border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge
                    variant={item.status === "completed" && item.verdict === "reproduced" ? "destructive" : "secondary"}
                  >
                    {item.status === "completed" && item.verdict
                      ? verdictLabels[item.verdict]
                      : statusLabels[item.status]}
                  </Badge>
                  <span className="text-muted-foreground text-xs">
                    #{item.id} · {new Date(item.created_at).toLocaleString("zh-CN")}
                  </span>
                  {item.conversation_id != null ? (
                    <Button asChild variant="ghost" size="sm" className="ml-auto">
                      <Link href={`/chat?c=${item.conversation_id}`}>查看会话</Link>
                    </Button>
                  ) : (
                    <span className="text-muted-foreground text-xs">会话已删除</span>
                  )}
                </div>
                {item.status === "completed" && item.summary ? (
                  <p className="whitespace-pre-wrap break-words text-sm">{item.summary}</p>
                ) : null}
                {item.error ? (
                  <p className="whitespace-pre-wrap break-words text-destructive text-sm">{item.error}</p>
                ) : null}
                {item.notes ? (
                  <p className="whitespace-pre-wrap break-words text-muted-foreground text-xs">
                    补充说明：{item.notes}
                  </p>
                ) : null}
                {item.status === "completed" && item.evidence ? (
                  <details className="min-w-0">
                    <summary className="cursor-pointer text-sm">复测证据</summary>
                    <div className="mt-3 overflow-x-auto">
                      <Markdown text={item.evidence} />
                    </div>
                  </details>
                ) : null}
              </div>
            ))
          : null}
      </CardContent>
      {open ? (
        <FindingRetestDialog
          findingId={findingId}
          findingName={findingName}
          onClose={() => setOpen(false)}
          onStarted={(retest) => {
            requestSeq.current++;
            const rows = [retest, ...previousItems.current.filter((item) => item.id !== retest.id)];
            previousItems.current = rows;
            setItems(rows);
            setError("");
            void load();
          }}
        />
      ) : null}
    </Card>
  );
}
