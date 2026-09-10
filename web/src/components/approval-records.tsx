"use client";

import * as React from "react";

import {
  BotIcon,
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ClipboardListIcon,
  CopyIcon,
  RefreshCwIcon,
  ShieldAlertIcon,
  XIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { InterceptApprovalRow, InterceptAudit, InterceptDetail } from "@/lib/types";
import { cn } from "@/lib/utils";

function fmtTime(value?: string) {
  if (!value) return "—";
  return new Date(value).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function source(row: InterceptApprovalRow) {
  if (row.decision_source) return row.decision_source;
  if (row.rule_id) return "rule";
  return row.reason?.startsWith("[模型]") ? "model" : "unknown";
}

function StatusBadge({ status }: { status: string }) {
  const labels: Record<string, string> = { pending: "待审批", allowed: "已允许", denied: "已拒绝", timeout: "已超时" };
  let variant: "default" | "destructive" | "secondary" | "outline" = "outline";
  if (status === "allowed") variant = "default";
  if (status === "denied") variant = "destructive";
  if (status === "pending") variant = "secondary";
  return <Badge variant={variant}>{labels[status] ?? status}</Badge>;
}

function MatchCell({ row, showReason = true }: { row: InterceptApprovalRow; showReason?: boolean }) {
  const reason = row.reason?.replace(/^\[模型\]\s*/, "");
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {source(row) === "model" ? (
        <Badge variant="outline">
          <BotIcon />
          模型判定
        </Badge>
      ) : (
        <span className="truncate">{row.rule_name || "规则未记录或已删除"}</span>
      )}
      {showReason ? (
        <p className="truncate text-muted-foreground text-xs" title={reason}>
          {reason || "未记录理由"}
        </p>
      ) : null}
    </div>
  );
}

function CodeBlock({ label, text, truncated = false }: { label: string; text: string; truncated?: boolean }) {
  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success("已复制");
    } catch {
      toast.error("复制失败，请手动选择内容复制");
    }
  }
  return (
    <section className="flex min-w-0 flex-col gap-2" aria-label={label}>
      <div className="flex items-center justify-between gap-2">
        <h3 className="font-medium text-muted-foreground text-xs">{label}</h3>
        {text ? (
          <Button variant="ghost" size="icon-xs" aria-label={`复制${label}`} onClick={() => void copy()}>
            <CopyIcon />
          </Button>
        ) : null}
      </div>
      <pre className="max-h-80 min-w-0 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-muted/60 p-3 font-mono text-xs leading-6 [overflow-wrap:anywhere]">
        {text || "未记录"}
      </pre>
      {truncated ? <p className="text-muted-foreground text-xs">内容已截断，以上为保存的片段。</p> : null}
    </section>
  );
}

const contextLabels: Record<string, string> = {
  user: "用户消息",
  assistant: "Agent 消息",
  text: "Agent 消息",
  tool_use: "工具请求",
  tool_result: "工具输出",
};

const actionLabels: Record<string, string> = { allow: "允许", ask: "转人工审批", deny: "拒绝" };
const executionLabels: Record<InterceptAudit["execution_status"], string> = {
  not_started: "尚未执行",
  not_executed: "未执行",
  awaiting_result: "已允许，等待执行结果",
  succeeded: "执行成功",
  failed: "执行失败",
  unknown: "执行结果未知",
};

type Decide = (id: number, decision: "allowed" | "denied") => Promise<void>;

function DecisionActions({ row, busy, decide }: { row: InterceptApprovalRow; busy: boolean; decide: Decide }) {
  if (row.status !== "pending") return null;
  return (
    <div className="flex flex-wrap gap-2">
      <Button size="sm" disabled={busy} onClick={() => void decide(row.id, "allowed")}>
        <CheckIcon data-icon="inline-start" />
        允许
      </Button>
      <Button size="sm" variant="destructive" disabled={busy} onClick={() => void decide(row.id, "denied")}>
        <XIcon data-icon="inline-start" />
        拒绝
      </Button>
    </div>
  );
}

function ApprovalDetail({
  row,
  busy,
  decide,
  revision,
}: {
  row: InterceptApprovalRow;
  busy: boolean;
  decide: Decide;
  revision: number;
}) {
  const [detail, setDetail] = React.useState<InterceptDetail | null>(null);
  const [error, setError] = React.useState("");
  const [retry, setRetry] = React.useState(0);
  const [more, setMore] = React.useState(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: Status, refresh and retry invalidate details without closing the panel.
  React.useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function load() {
      try {
        const next = await api.interceptDetail(row.id);
        if (cancelled) return;
        setDetail(next);
        setError("");
        if (next.status === "pending" || next.audit?.execution_status === "awaiting_result")
          timer = setTimeout(() => void load(), 5000);
      } catch (e) {
        if (!cancelled) setError((e as Error).message || "详情加载失败");
      }
    }
    void load();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [row.id, row.status, revision, retry]);

  // Row updates are authoritative until the lazy detail has caught up.
  const current = detail?.status === row.status ? detail : row;
  const audit = detail?.audit;
  let execution = audit ? executionLabels[audit.execution_status] : "未记录";
  if (row.status === "pending") execution = "尚未执行";
  if (audit && audit.correlation !== "exact" && audit.effective_action === "allow") execution = "未关联执行结果";
  const command = typeof row.tool_input?.command === "string" ? row.tool_input.command : undefined;
  let initialLabel = source(row) === "model" ? "模型初判" : "规则初判";
  if (audit?.model_fallback) initialLabel = "模型异常回退";

  return (
    <div className="flex min-w-0 flex-col gap-4 p-3 sm:p-5">
      <div className="grid min-w-0 gap-5 rounded-xl border bg-muted/20 p-4 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-3">
          <CodeBlock
            label={`${row.agent_name || row.conv_agent_key || "Agent"} · 工具请求`}
            text={JSON.stringify(row.tool_input ?? {}, null, 2)}
          />
          {command ? (
            <Collapsible>
              <CollapsibleTrigger asChild>
                <Button variant="ghost" size="sm">
                  <ChevronDownIcon data-icon="inline-start" />
                  查看命令内容
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-2">
                <CodeBlock label="命令内容" text={command} />
              </CollapsibleContent>
            </Collapsible>
          ) : null}
          <p className="text-muted-foreground text-xs">
            执行结果：<span className="text-foreground">{detail ? execution : "加载中…"}</span>
          </p>
        </div>
        <div className="flex min-w-0 flex-col gap-4 lg:border-l lg:pl-5">
          <h3 className="font-medium text-muted-foreground text-xs">
            {current.status === "pending" ? "审查状态" : "审批裁决"}
          </h3>
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge status={current.status} />
            <MatchCell row={current} showReason={false} />
          </div>
          <p className="whitespace-pre-wrap break-words text-sm leading-7 [overflow-wrap:anywhere]">
            {current.reason?.replace(/^\[模型\]\s*/, "") || "未记录审批理由"}
          </p>
          {audit?.decision_reason ? <p className="text-sm">{audit.decision_reason}</p> : null}
          {audit?.effective_action ? <p className="text-sm">最终动作：{actionLabels[audit.effective_action]}</p> : null}
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-xs">
            <dt className="text-muted-foreground">来源</dt>
            <dd className="break-words">
              {row.conv_title || (row.task_id ? `任务 ${row.task_id}` : row.agent_name || "未记录")}
            </dd>
            <dt className="text-muted-foreground">申请时间</dt>
            <dd>{fmtTime(row.created_at)}</dd>
            <dt className="text-muted-foreground">决定时间</dt>
            <dd>{fmtTime(current.decided_at)}</dd>
            {audit?.rule_name ? (
              <>
                <dt className="text-muted-foreground">规则快照</dt>
                <dd>{audit.rule_name}</dd>
              </>
            ) : null}
            {audit?.profile_id ? (
              <>
                <dt className="text-muted-foreground">审批模型配置</dt>
                <dd>#{audit.profile_id}</dd>
              </>
            ) : null}
          </dl>
          <DecisionActions row={current} busy={busy} decide={decide} />
        </div>
      </div>
      {error ? (
        <Alert variant="destructive">
          <AlertDescription>
            <div className="flex flex-wrap items-center gap-2">
              <span>详情加载失败：{error}</span>
              <Button variant="outline" size="sm" onClick={() => setRetry((v) => v + 1)}>
                重试详情
              </Button>
            </div>
          </AlertDescription>
        </Alert>
      ) : null}
      {!detail && !error ? <Skeleton className="h-8 w-60" /> : null}
      {detail && !audit ? (
        <Alert>
          <AlertDescription>此记录未保存审批详情快照，无法还原当时的上下文、模型初判和执行输出。</AlertDescription>
        </Alert>
      ) : null}
      {audit ? (
        <Collapsible open={more} onOpenChange={setMore}>
          <CollapsibleTrigger asChild>
            <Button variant="ghost" size="sm">
              <ChevronDownIcon data-icon="inline-start" className={cn(more && "rotate-180")} />
              {more ? "收起上下文" : "查看上下文与执行结果"}
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="pt-4">
            <div className="flex min-w-0 flex-col gap-5">
              <CodeBlock label="用户消息 / 当前轮输入" text={audit.user_message} truncated={audit.user_truncated} />
              <section className="flex min-w-0 flex-col gap-3">
                <h3 className="font-medium text-muted-foreground text-xs">可见会话上下文</h3>
                <p className="text-muted-foreground text-xs">
                  保存于 {fmtTime(audit.captured_at)} 的会话记录片段。模型审批目前仅接收工具名称和参数。
                </p>
                {audit.context_truncated ? (
                  <p className="text-muted-foreground text-xs">仅保存最近的上下文，部分内容已截断。</p>
                ) : null}
                {audit.context?.length ? (
                  audit.context.map((entry, index) => (
                    <CodeBlock
                      key={`${entry.kind}-${entry.tool_use_id || index}`}
                      label={`${contextLabels[entry.kind] ?? entry.kind}${entry.tool ? ` · ${entry.tool}` : ""}${entry.is_error ? " · 异常" : ""}`}
                      text={entry.text}
                      truncated={entry.truncated}
                    />
                  ))
                ) : (
                  <p className="text-muted-foreground text-sm">未记录可关联的上下文</p>
                )}
              </section>
              <CodeBlock
                label={`${initialLabel}：${actionLabels[audit.initial_action] ?? audit.initial_action}`}
                text={audit.initial_reason.replace(/^\[模型\]\s*/, "")}
              />
              <CodeBlock
                label="执行输出"
                text={audit.output ?? (audit.execution_status === "not_executed" ? "工具未执行。" : "尚无执行输出")}
                truncated={audit.output_truncated}
              />
              {audit.correlation !== "exact" ? (
                <Alert>
                  <AlertDescription>
                    {audit.correlation === "ambiguous"
                      ? "存在相同参数的并发调用，无法唯一关联工具调用；本记录不展示推测的执行结果。"
                      : "未记录可唯一关联的工具调用 ID。"}
                  </AlertDescription>
                </Alert>
              ) : null}
              <dl className="grid gap-2 text-muted-foreground text-xs [overflow-wrap:anywhere]">
                <div>工具调用 ID：{audit.tool_use_id || "未记录"}</div>
                <div>参数摘要 SHA-256：{audit.input_digest}</div>
                <div>审查配置指纹 SHA-256：{audit.config_digest || "未记录"}</div>
                {audit.execution_ended_at ? <div>结果记录时间：{fmtTime(audit.execution_ended_at)}</div> : null}
              </dl>
            </div>
          </CollapsibleContent>
        </Collapsible>
      ) : null}
    </div>
  );
}

// Hidden cells do not occupy table columns. Keep the detail span in sync so
// expansion cannot create empty columns and leave a gap in the selected row.
const approvalBreakpoints = ["(min-width: 40rem)", "(min-width: 48rem)", "(min-width: 64rem)", "(min-width: 80rem)"];
function subscribeColumns(onChange: () => void) {
  const queries = approvalBreakpoints.map((query) => window.matchMedia(query));
  for (const query of queries) query.addEventListener("change", onChange);
  return () => {
    for (const query of queries) query.removeEventListener("change", onChange);
  };
}
function visibleColumnCount() {
  const matches = approvalBreakpoints.map((query) => window.matchMedia(query).matches);
  return 3 + Number(matches[0]) + Number(matches[1]) + 2 * Number(matches[2]) + 2 * Number(matches[3]);
}
function serverColumnCount() {
  return 9;
}

function ApprovalTable({
  rows,
  busy,
  decide,
  revision,
  label,
}: {
  rows: InterceptApprovalRow[];
  busy: boolean;
  decide: Decide;
  revision: number;
  label: string;
}) {
  const [expanded, setExpanded] = React.useState<Set<number>>(() => new Set());
  const columns = React.useSyncExternalStore(subscribeColumns, visibleColumnCount, serverColumnCount);
  const prefix = React.useId();
  function toggle(id: number) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  return (
    <Table className="table-fixed" aria-label={label}>
      <TableHeader>
        <TableRow>
          <TableHead className="w-10">
            <span className="sr-only">展开详情</span>
          </TableHead>
          <TableHead className="hidden w-14 sm:table-cell">#</TableHead>
          <TableHead className="w-28">工具</TableHead>
          <TableHead className="hidden md:table-cell">来源</TableHead>
          <TableHead className="hidden lg:table-cell">匹配规则</TableHead>
          <TableHead className="hidden xl:table-cell">参数</TableHead>
          <TableHead className="w-24">状态</TableHead>
          <TableHead className="hidden w-36 lg:table-cell">申请时间</TableHead>
          <TableHead className="hidden w-36 xl:table-cell">决定时间</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row) => {
          const open = expanded.has(row.id);
          const panelID = `${prefix}-${row.id}`;
          return (
            <React.Fragment key={row.id}>
              <TableRow
                data-state={open ? "selected" : undefined}
                className="cursor-pointer"
                onClick={() => toggle(row.id)}
              >
                <TableCell>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`${open ? "收起" : "展开"}审批 #${row.id}`}
                    aria-expanded={open}
                    aria-controls={open ? panelID : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggle(row.id);
                    }}
                  >
                    {open ? <ChevronDownIcon /> : <ChevronRightIcon />}
                  </Button>
                </TableCell>
                <TableCell className="hidden text-muted-foreground sm:table-cell">{row.id}</TableCell>
                <TableCell>
                  <code className="block truncate text-xs">{row.tool_name}</code>
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  <div className="flex flex-col gap-1">
                    <span className="truncate" title={row.conv_title || row.task_id}>
                      {row.conv_title || row.task_id || "—"}
                    </span>
                    <span className="truncate text-muted-foreground text-xs">
                      {row.agent_name || row.conv_agent_key}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  <MatchCell row={row} />
                </TableCell>
                <TableCell className="hidden xl:table-cell">
                  <code className="block truncate text-muted-foreground text-xs">{JSON.stringify(row.tool_input)}</code>
                </TableCell>
                <TableCell>
                  <StatusBadge status={row.status} />
                </TableCell>
                <TableCell className="hidden text-muted-foreground text-xs lg:table-cell">
                  {fmtTime(row.created_at)}
                </TableCell>
                <TableCell className="hidden text-muted-foreground text-xs xl:table-cell">
                  {fmtTime(row.decided_at)}
                </TableCell>
              </TableRow>
              {open ? (
                <TableRow className="hover:bg-transparent has-aria-expanded:bg-transparent">
                  <TableCell colSpan={columns} className="whitespace-normal p-0">
                    <section id={panelID} aria-label={`审批详情 #${row.id}`}>
                      <ApprovalDetail row={row} busy={busy} decide={decide} revision={revision} />
                    </section>
                  </TableCell>
                </TableRow>
              ) : null}
            </React.Fragment>
          );
        })}
      </TableBody>
    </Table>
  );
}

export function ApprovalRecords({ taskId }: { taskId?: string }) {
  const [rows, setRows] = React.useState<InterceptApprovalRow[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [refreshing, setRefreshing] = React.useState(false);
  const [error, setError] = React.useState("");
  const [deciding, setDeciding] = React.useState(false);
  const [revision, setRevision] = React.useState(0);
  const request = React.useRef(0);
  const decisionLock = React.useRef(false);

  const load = React.useCallback(
    async (manual = false) => {
      const id = ++request.current;
      if (manual) setRefreshing(true);
      try {
        const items = taskId ? await api.interceptTask(taskId) : await api.interceptHistory();
        if (id !== request.current) return;
        setRows(items);
        setError("");
        if (manual) setRevision((v) => v + 1);
      } catch (e) {
        if (id === request.current) setError((e as Error).message || "加载失败");
      } finally {
        if (id === request.current) {
          setLoading(false);
          setRefreshing(false);
        }
      }
    },
    [taskId],
  );

  React.useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 5000);
    return () => {
      request.current++;
      clearInterval(timer);
    };
  }, [load]);

  const decide: Decide = async (id, decision) => {
    if (decisionLock.current) return;
    decisionLock.current = true;
    setDeciding(true);
    try {
      await api.interceptDecide(id, decision);
      // Invalidate a list request started before this decision.
      request.current++;
      setRows((prev) =>
        prev.map((row) => (row.id === id ? { ...row, status: decision, decided_at: new Date().toISOString() } : row)),
      );
      setRevision((v) => v + 1);
      toast.success(decision === "allowed" ? "已允许执行" : "已拒绝执行");
    } catch (e) {
      toast.error((e as Error).message);
      await load(true);
    } finally {
      decisionLock.current = false;
      setDeciding(false);
    }
  };

  const pending = rows.filter((row) => row.status === "pending");
  const title = taskId ? "拦截审批" : "审批记录";
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <ClipboardListIcon className="size-5 text-muted-foreground" />
          <h1 className="font-semibold text-xl">{title}</h1>
          {pending.length ? <Badge variant="secondary">{pending.length} 待审批</Badge> : null}
        </div>
        <Button variant="outline" size="sm" onClick={() => void load(true)} disabled={loading || refreshing}>
          <RefreshCwIcon data-icon="inline-start" className={cn(refreshing && "animate-spin")} />
          刷新
        </Button>
      </div>
      <p className="text-muted-foreground text-sm">展开记录查看工具请求和审批裁决，以及当时的上下文与执行结果。</p>
      {error ? (
        <Alert variant="destructive">
          <AlertDescription>记录加载失败：{error}。请点击刷新重试。</AlertDescription>
        </Alert>
      ) : null}
      {pending.length ? (
        <section className="overflow-hidden rounded-xl border">
          <div className="flex items-center gap-2 border-b bg-muted/40 px-4 py-3 font-medium text-sm">
            <ShieldAlertIcon className="size-4" />
            待处理（{pending.length}）<span className="text-muted-foreground text-xs">展开后允许或拒绝</span>
          </div>
          <ApprovalTable rows={pending} busy={deciding} decide={decide} revision={revision} label="待处理审批" />
        </section>
      ) : null}
      <section className="overflow-hidden rounded-xl border">
        <div className="border-b px-4 py-3 font-medium text-sm">
          {taskId ? "全部记录" : "最近记录"}（{rows.length}）
        </div>
        {loading ? (
          <div className="flex flex-col gap-3 p-4">
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
            <Skeleton className="h-10 w-full" />
          </div>
        ) : null}
        {!loading && !rows.length && !error ? (
          <Empty>
            <EmptyHeader>
              <EmptyMedia variant="icon">
                <ClipboardListIcon />
              </EmptyMedia>
              <EmptyTitle>暂无审批记录</EmptyTitle>
              <EmptyDescription>规则或模型作出审批决定后，记录将显示在这里。</EmptyDescription>
            </EmptyHeader>
          </Empty>
        ) : null}
        {rows.length ? (
          <ApprovalTable rows={rows} busy={deciding} decide={decide} revision={revision} label="审批记录列表" />
        ) : null}
      </section>
    </div>
  );
}
