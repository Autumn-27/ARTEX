"use client";

import * as React from "react";
import { CheckIcon, ClipboardListIcon, RefreshCwIcon, ShieldAlertIcon, XIcon } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { InterceptApprovalRow } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

// ---- helpers ----------------------------------------------------------------

function fmtTime(s: string) {
  if (!s) return "—";
  const d = new Date(s);
  return d.toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function SourceCell({ row }: { row: InterceptApprovalRow }) {
  if (row.task_id) {
    return (
      <div className="space-y-0.5">
        <div className="font-mono text-xs font-medium">{row.task_id}</div>
        <div className="text-[11px] text-muted-foreground">{row.agent_name || "—"}</div>
      </div>
    );
  }
  if (row.conversation_id) {
    const agent = row.conv_agent_key || "—";
    const title = row.conv_title || `对话 #${row.conversation_id}`;
    return (
      <div className="space-y-0.5">
        <div className="font-medium">{title}</div>
        <div className="text-[11px] text-muted-foreground">Agent: {agent}</div>
      </div>
    );
  }
  return <span className="text-muted-foreground">—</span>;
}

function StatusBadge({ status }: { status: string }) {
  if (status === "pending") return <Badge variant="outline" className="border-amber-400 text-amber-600">待审批</Badge>;
  if (status === "allowed") return <Badge variant="outline" className="border-emerald-500 text-emerald-600">已允许</Badge>;
  if (status === "denied")  return <Badge variant="outline" className="border-red-500 text-red-600">已拒绝</Badge>;
  return <Badge variant="outline" className="text-muted-foreground">已超时</Badge>;
}

function InputPreview({ input }: { input: Record<string, unknown> }) {
  const full = JSON.stringify(input, null, 2);
  const s = JSON.stringify(input);
  const short = s.length > 120 ? s.slice(0, 120) + "…" : s;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <code className="block w-full cursor-default truncate font-mono text-[11px] text-muted-foreground">
          {short}
        </code>
      </TooltipTrigger>
      <TooltipContent side="top" align="start" className="max-w-sm">
        <pre className="whitespace-pre-wrap break-all font-mono text-xs">{full}</pre>
      </TooltipContent>
    </Tooltip>
  );
}

// ---- main component ---------------------------------------------------------

export default function ApprovalsPage() {
  const [rows, setRows] = React.useState<InterceptApprovalRow[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [deciding, setDeciding] = React.useState<number | null>(null);

  async function load() {
    try {
      const items = await api.interceptHistory();
      setRows(items);
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  React.useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, []);

  async function decide(id: number, decision: "allowed" | "denied") {
    if (deciding !== null) return;
    setDeciding(id);
    try {
      await api.interceptDecide(id, decision);
      toast.success(decision === "allowed" ? "已允许执行" : "已拒绝执行");
      setRows((prev) =>
        prev.map((r) =>
          r.id === id ? { ...r, status: decision, decided_at: new Date().toISOString() } : r,
        ),
      );
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setDeciding(null);
    }
  }

  const pending = rows.filter((r) => r.status === "pending");
  const decided = rows.filter((r) => r.status !== "pending");

  return (
    <TooltipProvider delayDuration={300}>
    <div className="flex flex-1 flex-col gap-6 p-6">
      {/* header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <ClipboardListIcon className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold">审批记录</h1>
          {pending.length > 0 && (
            <Badge className="rounded-full bg-destructive text-destructive-foreground">{pending.length} 待审批</Badge>
          )}
        </div>
        <Button variant="outline" size="sm" onClick={load} disabled={loading}>
          <RefreshCwIcon className="mr-1.5 h-3.5 w-3.5" />
          刷新
        </Button>
      </div>

      {/* pending section */}
      {pending.length > 0 && (
        <section className="rounded-lg border border-amber-400/40 bg-amber-50/30 dark:bg-amber-950/10">
          <div className="flex items-center gap-2 border-b border-amber-400/30 px-4 py-3 text-sm font-medium text-amber-700 dark:text-amber-400">
            <ShieldAlertIcon className="h-4 w-4" />
            待处理 ({pending.length})
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-12">#</TableHead>
                <TableHead>工具</TableHead>
                <TableHead>来源</TableHead>
                <TableHead>匹配规则</TableHead>
                <TableHead className="w-48">参数</TableHead>
                <TableHead>时间</TableHead>
                <TableHead className="w-28 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pending.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="text-muted-foreground">{row.id}</TableCell>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-xs font-mono">{row.tool_name}</code>
                  </TableCell>
                  <TableCell><SourceCell row={row} /></TableCell>
                  <TableCell>
                    {row.rule_name
                      ? <span className="text-sm">{row.rule_name}</span>
                      : <span className="text-muted-foreground">—</span>}
                  </TableCell>
                  <TableCell className="w-48 max-w-[12rem]"><InputPreview input={row.tool_input} /></TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.created_at)}</TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1.5">
                      <Button
                        size="sm"
                        className="h-7 px-2 text-xs"
                        disabled={deciding === row.id}
                        onClick={() => decide(row.id, "allowed")}
                      >
                        <CheckIcon className="mr-1 h-3 w-3" />
                        允许
                      </Button>
                      <Button
                        size="sm"
                        variant="destructive"
                        className="h-7 px-2 text-xs"
                        disabled={deciding === row.id}
                        onClick={() => decide(row.id, "denied")}
                      >
                        <XIcon className="mr-1 h-3 w-3" />
                        拒绝
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </section>
      )}

      {/* history section */}
      <section className="rounded-lg border">
        <div className="border-b px-4 py-3 text-sm font-medium text-muted-foreground">
          全部记录 ({rows.length})
        </div>
        {rows.length === 0 && !loading ? (
          <div className="py-16 text-center text-sm text-muted-foreground">暂无审批记录</div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-12">#</TableHead>
                <TableHead>工具</TableHead>
                <TableHead>来源</TableHead>
                <TableHead>匹配规则</TableHead>
                <TableHead className="w-48">参数</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>申请时间</TableHead>
                <TableHead>决定时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={row.id} className={row.status === "pending" ? "opacity-40" : ""}>
                  <TableCell className="text-muted-foreground">{row.id}</TableCell>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-xs font-mono">{row.tool_name}</code>
                  </TableCell>
                  <TableCell><SourceCell row={row} /></TableCell>
                  <TableCell>
                    {row.rule_name
                      ? <span className="text-sm">{row.rule_name}</span>
                      : <span className="text-muted-foreground">—</span>}
                  </TableCell>
                  <TableCell className="w-48 max-w-[12rem]"><InputPreview input={row.tool_input} /></TableCell>
                  <TableCell><StatusBadge status={row.status} /></TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">{fmtTime(row.created_at)}</TableCell>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                    {row.decided_at ? fmtTime(row.decided_at) : "—"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>
    </div>
    </TooltipProvider>
  );
}
