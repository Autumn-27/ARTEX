"use client";

import * as React from "react";
import {
  TerminalIcon,
  SearchIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  Loader2Icon,
  XIcon,
} from "lucide-react";

import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { CommandRecord } from "@/lib/types";

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// extractCommand parses the tool_use detail (JSON like {"command":"..."}) and
// returns just the command string. Falls back to raw text if not JSON.
function extractCommand(raw: string): string {
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj.command === "string") return obj.command;
  } catch { /* not JSON */ }
  return raw;
}

// truncate clips a string to maxLen characters.
function truncate(s: string, maxLen: number): string {
  const first = s.split("\n")[0]; // single-line preview
  if (first.length <= maxLen) return first;
  return first.slice(0, maxLen) + "…";
}

const PAGE_SIZES = [25, 50, 100];
const CMD_MAX_LEN = 80;

export default function CommandsPage() {
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [query, setQuery] = React.useState("");
  const [queryQ, setQueryQ] = React.useState("");
  const [taskFilter, setTaskFilter] = React.useState("");

  const [commands, setCommands] = React.useState<CommandRecord[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(false);

  // Inline detail panel (like traffic page, not a dialog)
  const [selected, setSelected] = React.useState<CommandRecord | null>(null);

  // Debounce search input.
  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // Reset page on filter change.
  React.useEffect(() => {
    setPage(0);
  }, [queryQ, taskFilter, size]);

  // Load data.
  React.useEffect(() => {
    let alive = true;
    setLoading(true);
    api
      .commands({ task: taskFilter || undefined, q: queryQ || undefined, page, size })
      .then((r) => {
        if (!alive) return;
        setCommands(r.commands ?? []);
        setTotal(r.total ?? 0);
      })
      .catch(() => {})
      .finally(() => alive && setLoading(false));
    return () => { alive = false; };
  }, [page, size, queryQ, taskFilter]);

  const totalPages = Math.max(1, Math.ceil(total / size));
  const rangeStart = total === 0 ? 0 : page * size + 1;
  const rangeEnd = page * size + commands.length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      {/* Header */}
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <TerminalIcon className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold tracking-tight">命令记录</h1>
          <Badge variant="secondary">{total}</Badge>
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="搜索命令..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Input
          placeholder="任务 ID"
          className="h-8 w-28"
          value={taskFilter}
          onChange={(e) => setTaskFilter(e.target.value.replace(/\D/g, ""))}
        />
        <Select value={String(size)} onValueChange={(v) => setSize(Number(v))}>
          <SelectTrigger size="sm" className="w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PAGE_SIZES.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n} / 页
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <div className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
          <span className="tabular-nums">
            {rangeStart}–{rangeEnd} / {total}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page <= 0}
            onClick={() => setPage((p) => Math.max(0, p - 1))}
          >
            <ChevronLeftIcon />
          </Button>
          <span className="tabular-nums">
            {page + 1} / {totalPages}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page + 1 >= totalPages}
            onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>

      {/* History table + inline detail (Burp-style split) */}
      <div className="flex h-[calc(100vh-13rem)] min-h-0 flex-col gap-3">
        <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
          <div className="min-h-0 flex-1 overflow-auto">
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-[130px]">时间</TableHead>
                  <TableHead className="w-[60px]">任务</TableHead>
                  <TableHead className="w-[90px]">Worker</TableHead>
                  <TableHead>命令</TableHead>
                  <TableHead className="w-[60px]">状态</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {loading && commands.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-12 text-center">
                      <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                    </TableCell>
                  </TableRow>
                ) : commands.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-12 text-center text-sm text-muted-foreground">
                      暂无命令记录
                    </TableCell>
                  </TableRow>
                ) : (
                  commands.map((cmd) => (
                    <TableRow
                      key={cmd.id}
                      className={cn(
                        "cursor-pointer",
                        selected?.id === cmd.id && "bg-accent hover:bg-accent",
                      )}
                      onClick={() => setSelected(cmd)}
                    >
                      <TableCell className="text-xs text-muted-foreground tabular-nums">
                        {fmtTime(cmd.created_at)}
                      </TableCell>
                      <TableCell className="text-xs font-mono text-muted-foreground">
                        #{cmd.exploration_id}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className="text-xs font-mono">
                          {cmd.worker || "-"}
                        </Badge>
                      </TableCell>
                      <TableCell className="max-w-0">
                        <code className="block truncate font-mono text-xs">
                          {truncate(extractCommand(cmd.command), CMD_MAX_LEN)}
                        </code>
                      </TableCell>
                      <TableCell>
                        {cmd.is_error ? (
                          <Badge variant="destructive" className="text-xs">失败</Badge>
                        ) : (
                          <Badge variant="secondary" className="text-xs text-emerald-600">成功</Badge>
                        )}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
        </Card>

        {/* Inline detail panel */}
        {selected && (
          <Card className="flex h-[42%] min-h-0 flex-col overflow-hidden py-0">
            {/* Detail header */}
            <div className="flex items-center gap-2 border-b px-3 py-2">
              <Badge variant="outline" className="text-xs font-mono">
                #{selected.exploration_id}
              </Badge>
              <Badge variant="outline" className="text-xs font-mono">
                {selected.worker || "-"}
              </Badge>
              <span className="text-xs text-muted-foreground">
                {fmtTime(selected.created_at)}
              </span>
              {selected.is_error ? (
                <Badge variant="destructive" className="text-xs">失败</Badge>
              ) : (
                <Badge variant="secondary" className="text-xs text-emerald-600">成功</Badge>
              )}
              <Button
                variant="ghost"
                size="icon"
                className="ml-auto size-7 shrink-0"
                onClick={() => setSelected(null)}
              >
                <XIcon />
              </Button>
            </div>
            {/* Input / Output split */}
            <div className="grid min-h-0 flex-1 grid-cols-2 divide-x">
              <div className="flex min-h-0 min-w-0 flex-col">
                <div className="border-b px-3 py-1 text-[11px] font-medium text-muted-foreground">
                  输入 Command
                </div>
                <div className="min-h-0 flex-1 overflow-auto">
                  <pre className="p-3 font-mono text-xs break-all whitespace-pre-wrap">
                    {extractCommand(selected.command)}
                  </pre>
                </div>
              </div>
              <div className="flex min-h-0 min-w-0 flex-col">
                <div className="border-b px-3 py-1 text-[11px] font-medium text-muted-foreground">
                  输出 Output
                </div>
                <div className="min-h-0 flex-1 overflow-auto">
                  <pre className={cn(
                    "p-3 font-mono text-xs break-all whitespace-pre-wrap",
                    selected.is_error && "text-red-600 dark:text-red-400",
                  )}>
                    {selected.output || "（空）"}
                  </pre>
                </div>
              </div>
            </div>
          </Card>
        )}
      </div>
    </div>
  );
}
