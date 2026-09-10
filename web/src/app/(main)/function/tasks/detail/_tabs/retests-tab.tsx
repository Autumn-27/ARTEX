"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpRightIcon, ChevronLeftIcon, ChevronRightIcon } from "lucide-react";

import { FindingRetestPanel } from "@/components/finding-retest-panel";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { api } from "@/lib/api";
import type { Finding, FindingsPage } from "@/lib/types";

const PAGE_SIZE = 20;

function findingLabel(finding: Finding) {
  return finding.name?.trim() || finding.vulnclass.trim() || "未分类";
}

export function RetestsTab({ taskId }: { taskId: string }) {
  const [page, setPage] = React.useState(1);
  const [data, setData] = React.useState<FindingsPage | null>(null);
  const [selectedId, setSelectedId] = React.useState("");
  const [error, setError] = React.useState("");
  const [refreshVersion, setRefreshVersion] = React.useState(0);
  const refresh = React.useCallback(() => setRefreshVersion((version) => version + 1), []);

  // Only this task's own findings, including those without graph nodes.
  // Fetch history only for the selection, and wait for each poll to finish.
  React.useEffect(() => {
    let disposed = false;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      try {
        const result = await api.findingsPage({ task: taskId, page, pageSize: PAGE_SIZE, sort: "time" });
        if (disposed) return;
        const lastPage = Math.max(1, Math.ceil(result.total / PAGE_SIZE));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setData(result);
        setSelectedId((current) =>
          result.items.some((finding) => finding.id === current) ? current : (result.items[0]?.id ?? ""),
        );
        setError("");
      } catch (e) {
        if (!disposed) setError((e as Error).message);
      } finally {
        if (!disposed) timer = setTimeout(() => void load(), 3000);
      }
    }
    void refreshVersion; // Refresh triage immediately after a retest completes.
    void load();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, [taskId, page, refreshVersion]);

  const loaded = data?.page === page;
  const findings = loaded ? data.items : [];
  const selected = findings.find((finding) => finding.id === selectedId);

  return (
    <div className="flex flex-col gap-4">
      {error ? (
        <Alert variant="destructive">
          <AlertDescription>
            加载任务漏洞失败：{error}
            <Button variant="outline" size="sm" onClick={refresh}>
              重试
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(16rem,22rem)_minmax(0,1fr)]">
        <Card className="min-w-0">
          <CardHeader>
            <CardTitle>选择漏洞{data ? ` · ${data.total}` : ""}</CardTitle>
            <CardDescription>查看本任务漏洞的复测记录，或发起新的复测。</CardDescription>
          </CardHeader>
          <CardContent className="flex max-h-[32rem] flex-col gap-2 overflow-y-auto">
            {!loaded && !error ? <Skeleton className="h-24 w-full" /> : null}
            {findings.map((finding) => (
              <Button
                key={finding.id}
                variant={finding.id === selectedId ? "secondary" : "ghost"}
                className="h-auto w-full shrink-0 flex-col items-start gap-2 whitespace-normal py-3 text-left"
                aria-label={`选择漏洞：${findingLabel(finding)}`}
                aria-pressed={finding.id === selectedId}
                onClick={() => setSelectedId(finding.id)}
              >
                <span className="line-clamp-2 break-words">{findingLabel(finding)}</span>
                <span className="flex flex-wrap items-center gap-2">
                  <StatusBadge domain="severity" value={finding.severity} dot />
                  <StatusBadge domain="finding" value={finding.status} dot />
                </span>
              </Button>
            ))}
            {loaded && findings.length === 0 ? (
              <Empty>
                <EmptyHeader>
                  <EmptyTitle>暂无可复测漏洞</EmptyTitle>
                  <EmptyDescription>本任务发现漏洞后，可在这里手动发起复测。</EmptyDescription>
                </EmptyHeader>
              </Empty>
            ) : null}
          </CardContent>
          {data && data.total > PAGE_SIZE ? (
            <CardFooter className="justify-between gap-2">
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="上一页漏洞"
                disabled={page === 1}
                onClick={() => setPage(page - 1)}
              >
                <ChevronLeftIcon />
              </Button>
              <span className="text-muted-foreground text-xs">
                第 {page} / {Math.ceil(data.total / PAGE_SIZE)} 页
              </span>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="下一页漏洞"
                disabled={page * PAGE_SIZE >= data.total}
                onClick={() => setPage(page + 1)}
              >
                <ChevronRightIcon />
              </Button>
            </CardFooter>
          ) : null}
        </Card>
        {selected ? (
          <div className="flex min-w-0 flex-col gap-4">
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <h2 className="min-w-0 flex-1 break-words font-medium">{findingLabel(selected)}</h2>
                <Button asChild variant="ghost" size="sm">
                  <Link href={`/function/findings/detail?id=${selected.finding_id || selected.id}`}>
                    漏洞详情 <ArrowUpRightIcon data-icon="inline-end" />
                  </Link>
                </Button>
              </div>
              <p className="line-clamp-3 break-words text-muted-foreground text-sm">{selected.summary}</p>
            </div>
            <FindingRetestPanel
              key={selected.id}
              findingId={selected.finding_id || selected.id}
              findingName={findingLabel(selected)}
              onCompleted={refresh}
            />
          </div>
        ) : null}
      </div>
    </div>
  );
}
