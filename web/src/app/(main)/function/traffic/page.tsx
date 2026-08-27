"use client";

import * as React from "react";

import {
  CheckIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  CopyIcon,
  ListChecksIcon,
  Loader2Icon,
  RadioTowerIcon,
  SearchIcon,
  Trash2Icon,
  WrapTextIcon,
} from "lucide-react";
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
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { api } from "@/lib/api";
import type { TrafficDetail, TrafficExchange, TrafficHost, TrafficResp } from "@/lib/types";
import { cn, copyText } from "@/lib/utils";

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function fmtBytes(n: number) {
  if (n <= 0) return "0 B";
  const units = ["B", "KB", "MB"];
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
  const v = n / 1024 ** i;
  return `${i === 0 ? v : v.toFixed(1)} ${units[i]}`;
}

function statusTone(status: number) {
  if (status >= 500) return "text-red-500";
  if (status >= 400) return "text-amber-500";
  if (status >= 300) return "text-blue-500";
  if (status >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}

function MethodBadge({ method }: { method: string }) {
  return <Badge className="shrink-0 font-mono">{method}</Badge>;
}

// Older captures may predate Host persistence because net/http keeps Host
// outside Request.Header. Fill it for display while newly recorded traffic is
// fixed at the recorder layer as well.
function requestWithHost(raw: string, exchange: TrafficExchange): string {
  if (!raw.trim() || /^host\s*:/im.test(raw)) return raw;
  let host = exchange.host;
  try {
    host = new URL(exchange.url).host || host;
  } catch {
    // Relative or legacy URLs fall back to the indexed host.
  }
  const newline = raw.includes("\r\n") ? "\r\n" : "\n";
  const firstLineEnd = raw.indexOf(newline);
  if (firstLineEnd < 0) return `${raw}${newline}Host: ${host}`;
  return `${raw.slice(0, firstLineEnd + newline.length)}Host: ${host}${newline}${raw.slice(firstLineEnd + newline.length)}`;
}

function StartLine({ line }: { line: string }) {
  const response = /^(HTTP\/\S+)(\s+)(\d{3})(.*)$/.exec(line);
  if (response) {
    return (
      <>
        <span className="text-muted-foreground">{response[1]}</span>
        {response[2]}
        <span className={statusTone(Number(response[3]))}>{response[3]}</span>
        {response[4]}
      </>
    );
  }

  const request = /^([A-Z]+)(\s+)(\S+)(\s+)(HTTP\/\S+)$/.exec(line);
  if (request) {
    return (
      <>
        <span className="font-semibold text-primary">{request[1]}</span>
        {request[2]}
        <span className="text-chart-2">{request[3]}</span>
        {request[4]}
        <span className="text-muted-foreground">{request[5]}</span>
      </>
    );
  }

  return line;
}

function HeaderLine({ line }: { line: string }) {
  const separator = line.indexOf(":");
  if (separator <= 0) return line;
  return (
    <>
      <span className="text-primary">{line.slice(0, separator)}</span>
      <span className="text-muted-foreground">:</span>
      <span className="text-chart-2">{line.slice(separator + 1)}</span>
    </>
  );
}

function JsonBody({ body }: { body: string }) {
  const parts: React.ReactNode[] = [];
  const tokens = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
  let cursor = 0;
  for (const match of body.matchAll(tokens)) {
    const index = match.index ?? 0;
    if (index > cursor) parts.push(body.slice(cursor, index));
    let className = "text-chart-4";
    if (match[1]) className = match[2] ? "text-primary" : "text-chart-2";
    else if (match[3]) className = "text-chart-3";
    parts.push(
      <span key={`${index}-${match[0].length}`} className={className}>
        {match[0]}
      </span>,
    );
    cursor = index + match[0].length;
  }
  if (cursor < body.length) parts.push(body.slice(cursor));
  return parts;
}

function MarkupBody({ body }: { body: string }) {
  const parts: React.ReactNode[] = [];
  const tags = /<\/?[A-Za-z][^>]*>|<!--[\s\S]*?-->/g;
  let cursor = 0;
  for (const match of body.matchAll(tags)) {
    const index = match.index ?? 0;
    if (index > cursor) parts.push(body.slice(cursor, index));
    parts.push(
      <span key={`${index}-${match[0].length}`} className="text-primary">
        {match[0]}
      </span>,
    );
    cursor = index + match[0].length;
  }
  if (cursor < body.length) parts.push(body.slice(cursor));
  return parts;
}

type BodyFormat = "json" | "markup" | "plain";

function detectBodyFormat(body: string): BodyFormat {
  const trimmed = body.trimStart();
  if (trimmed.startsWith("{") || trimmed.startsWith("[")) return "json";
  if (trimmed.startsWith("<")) return "markup";
  return "plain";
}

function HighlightedBody({ body, format }: { body: string; format: BodyFormat }) {
  if (format === "json") return <JsonBody body={body} />;
  if (format === "markup") return <MarkupBody body={body} />;
  return body;
}

function HttpCodeBlock({ raw }: { raw: string }) {
  const [wrapLines, setWrapLines] = React.useState(true);
  const [copied, setCopied] = React.useState(false);
  const value = raw || "（空）";
  const lines = value.replaceAll("\r\n", "\n").split("\n");
  const separator = lines.indexOf("");
  const body = separator >= 0 ? lines.slice(separator + 1).join("\n") : "";
  const bodyFormat = detectBodyFormat(body);

  React.useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const copyPacket = async () => {
    const ok = await copyText(value);
    if (ok) {
      setCopied(true);
      return;
    }
    toast.error("复制失败，请使用 Ctrl/Cmd+A 后复制");
  };

  const renderLine = (line: string, index: number) => {
    if (index === 0) return <StartLine line={line} />;
    if (separator < 0 || index < separator) return <HeaderLine line={line} />;
    if (index === separator) return null;
    return <HighlightedBody body={line} format={bodyFormat} />;
  };

  return (
    <div className="relative mx-3 my-4 overflow-hidden rounded-md border bg-background shadow-xs">
      <div className="absolute top-2 right-2 flex items-center gap-0.5 rounded-md border bg-background/95 p-0.5 shadow-xs">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={wrapLines ? "关闭自动换行" : "开启自动换行"}
              aria-pressed={wrapLines}
              onClick={() => setWrapLines((current) => !current)}
            >
              <WrapTextIcon />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{wrapLines ? "关闭自动换行" : "开启自动换行"}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={copied ? "已复制报文" : "复制报文"}
              onClick={() => void copyPacket()}
            >
              {copied ? <CheckIcon /> : <CopyIcon />}
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{copied ? "已复制" : "复制报文"}</TooltipContent>
        </Tooltip>
      </div>
      {/* biome-ignore lint/a11y/useSemanticElements: textarea cannot preserve line numbers and syntax-highlighting markup. */}
      <div
        role="textbox"
        aria-label="HTTP 报文代码"
        aria-multiline="true"
        aria-readonly="true"
        tabIndex={0}
        className="max-h-[calc(100vh-15rem)] min-w-0 overflow-auto bg-background py-3 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        onKeyDown={(event) => {
          if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "a") return;
          event.preventDefault();
          const selection = window.getSelection();
          if (!selection) return;
          const range = document.createRange();
          range.selectNodeContents(event.currentTarget);
          selection.removeAllRanges();
          selection.addRange(range);
        }}
      >
        {lines.map((line, index) => (
          <div
            key={`${index}-${line}`}
            data-line={index + 1}
            className="grid min-w-0 grid-cols-[1.5rem_minmax(0,1fr)] leading-relaxed before:sticky before:left-0 before:self-stretch before:border-r before:bg-muted/20 before:px-1 before:text-right before:text-muted-foreground/60 before:content-[attr(data-line)]"
          >
            <code
              className={cn(
                "min-h-[1lh] min-w-0 pr-16 pl-1.5 [tab-size:4]",
                wrapLines ? "break-words whitespace-pre-wrap" : "whitespace-pre",
              )}
            >
              {renderLine(line, index)}
            </code>
          </div>
        ))}
      </div>
    </div>
  );
}

// Fixed method set (server filters exact-match); avoids deriving options from a
// single page, which would only ever list the methods on that page.
const METHODS = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];
const PAGE_SIZES = [25, 50, 100, 200];

export default function TrafficPage() {
  const [page, setPage] = React.useState(0);
  const [size, setSize] = React.useState(50);
  const [host, setHost] = React.useState(""); // raw host input
  const [hostQ, setHostQ] = React.useState(""); // debounced → server
  const [query, setQuery] = React.useState(""); // raw free-text input
  const [queryQ, setQueryQ] = React.useState(""); // debounced → server
  const [method, setMethod] = React.useState("all");

  const [traffic, setTraffic] = React.useState<TrafficResp | null>(null);
  const [selected, setSelected] = React.useState<TrafficExchange | null>(null);
  const [detail, setDetail] = React.useState<TrafficDetail | null>(null);
  const [detailLoading, setDetailLoading] = React.useState(false);

  const [hosts, setHosts] = React.useState<TrafficHost[]>([]); // target picker
  const [selectedHosts, setSelectedHosts] = React.useState<string[]>([]); // checked in picker
  const [pickerOpen, setPickerOpen] = React.useState(false);

  const [deleteMode, setDeleteMode] = React.useState<"filter" | "selected" | null>(null); // null = dialog closed
  const [deleting, setDeleting] = React.useState(false);
  const [reloadTick, setReloadTick] = React.useState(0); // manual refetch trigger

  // Debounce both filters so we don't refetch on every keystroke.
  React.useEffect(() => {
    const t = setTimeout(() => setHostQ(host.trim()), 300);
    return () => clearTimeout(t);
  }, [host]);
  React.useEffect(() => {
    const t = setTimeout(() => setQueryQ(query.trim()), 300);
    return () => clearTimeout(t);
  }, [query]);

  // Any filter/size change resets to the first page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: these values intentionally trigger a page reset.
  React.useEffect(() => {
    setPage(0);
  }, [hostQ, queryQ, method, size]);

  // Load the current page. Auto-refresh only on page 0 (newest) so paging back
  // through history isn't yanked out from under the user.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadTick is an explicit manual-refetch trigger.
  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .traffic(page, size, hostQ, method, queryQ)
        .then((r) => {
          if (alive) setTraffic(r);
        })
        .catch(() => {
          // Keep the last successful snapshot during transient refresh failures.
        });
      api
        .trafficHosts()
        .then((r) => {
          if (alive) setHosts(r.hosts ?? []);
        })
        .catch(() => {
          // Keep the last successful host list during transient refresh failures.
        });
    };
    load();
    const t = setInterval(() => {
      if (page !== 0) return; // only auto-refresh the newest page
      load();
    }, 5000);
    return () => {
      alive = false;
      clearInterval(t);
    };
  }, [page, size, hostQ, method, queryQ, reloadTick]);

  // Delete traffic for the current host filter (substring) or the checked
  // hosts (exact batch), then refetch.
  const allSelected = hosts.length > 0 && hosts.every((h) => selectedHosts.includes(h.host));

  const confirmDelete = () => {
    setDeleting(true);
    const p = deleteMode === "selected" ? api.trafficDeleteHosts(selectedHosts) : api.trafficDeleteHost(hostQ);
    p.then(() => {
      setDeleteMode(null);
      setSelected(null);
      setDetail(null);
      if (deleteMode === "selected") {
        setSelectedHosts([]);
        setPickerOpen(false);
      }
      setPage(0);
      setReloadTick((t) => t + 1);
    })
      .catch(() => {
        // Keep the confirmation open so the user can retry a failed deletion.
      })
      .finally(() => setDeleting(false));
  };

  // Lazy-load the raw request/response for the selected exchange.
  React.useEffect(() => {
    if (!selected) {
      setDetail(null);
      return;
    }
    let alive = true;
    setDetailLoading(true);
    setDetail(null);
    api
      .trafficExchange(selected.id)
      .then((d) => {
        if (alive) setDetail(d);
      })
      .catch(() => {
        if (alive) setDetail({ req: "（无法加载报文）", resp: "" });
      })
      .finally(() => {
        if (alive) setDetailLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [selected]);

  const exchanges = React.useMemo(() => traffic?.exchanges ?? [], [traffic]);
  const total = traffic?.total ?? exchanges.length;
  const pageCount = Math.max(1, Math.ceil(total / size));
  const rangeStart = total === 0 ? 0 : page * size + 1;
  const rangeEnd = page * size + exchanges.length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold tracking-tight">流量</h1>
          <p className="text-muted-foreground text-sm">全局录制代理 · 所有 HTTP 往来</p>
        </div>
        <div className="flex items-center gap-4 text-sm">
          <span
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium",
              traffic?.enabled
                ? "border-emerald-500/20 bg-emerald-500/15 text-emerald-600 dark:text-emerald-400"
                : "border-transparent bg-muted text-muted-foreground",
            )}
          >
            <RadioTowerIcon className="size-3.5" />
            {traffic?.enabled ? "录制中" : "已停用"}
          </span>
          {traffic?.proxy && <span className="font-mono text-xs text-muted-foreground">{traffic.proxy}</span>}
          <span className="text-xs text-muted-foreground">
            共 <span className="tabular-nums">{traffic?.count ?? 0}</span> 条
          </span>
        </div>
      </div>

      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <Popover open={pickerOpen} onOpenChange={setPickerOpen}>
          <PopoverTrigger asChild>
            <Button variant="outline" size="sm" className="h-8">
              <ListChecksIcon className="size-3.5" />
              {selectedHosts.length > 0 ? `选择目标（${selectedHosts.length}）` : "选择目标…"}
            </Button>
          </PopoverTrigger>
          <PopoverContent
            className="w-80 p-0 data-open:animate-none data-closed:animate-none"
            align="start"
            collisionPadding={16}
          >
            <div className="flex items-center justify-between border-b px-3 py-2">
              <span className="text-xs font-medium text-muted-foreground">按目标批量删除</span>
              {hosts.length > 0 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-2 text-xs"
                  onClick={() => setSelectedHosts(allSelected ? [] : hosts.map((h) => h.host))}
                >
                  {allSelected ? "取消全选" : "全选"}
                </Button>
              )}
            </div>
            <div className="max-h-64 overflow-y-auto">
              {hosts.length === 0 ? (
                <div className="px-3 py-6 text-center text-xs text-muted-foreground">暂无流量记录</div>
              ) : (
                hosts.map((h, index) => (
                  <label
                    key={h.host}
                    htmlFor={`traffic-host-${index}`}
                    className="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-xs hover:bg-accent"
                  >
                    <Checkbox
                      id={`traffic-host-${index}`}
                      checked={selectedHosts.includes(h.host)}
                      onCheckedChange={() =>
                        setSelectedHosts((prev) =>
                          prev.includes(h.host) ? prev.filter((x) => x !== h.host) : [...prev, h.host],
                        )
                      }
                    />
                    <span className="truncate font-mono">{h.host}</span>
                    <span className="ml-auto shrink-0 tabular-nums text-muted-foreground">{h.count}</span>
                  </label>
                ))
              )}
            </div>
            <div className="border-t p-2">
              <Button
                variant="destructive"
                size="sm"
                className="w-full"
                disabled={selectedHosts.length === 0}
                onClick={() => {
                  setDeleteMode("selected");
                  setPickerOpen(false);
                }}
              >
                删除选中（{selectedHosts.length}）
              </Button>
            </div>
          </PopoverContent>
        </Popover>
        <div className="relative w-48">
          <Input placeholder="host…" value={host} onChange={(e) => setHost(e.target.value)} className="h-8" />
        </div>
        <Button
          variant="destructive"
          size="sm"
          className="h-8"
          disabled={!hostQ || deleting}
          title={hostQ ? undefined : "先在左侧选择目标或输入 host"}
          onClick={() => setDeleteMode("filter")}
        >
          <Trash2Icon className="size-3.5" />
          删除该目标
        </Button>
        <div className="relative max-w-sm flex-1">
          <SearchIcon className="absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            placeholder="搜索全部（URL / 方法 / 类型 / 状态码…）"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 pl-8"
          />
        </div>
        <Select value={method} onValueChange={setMethod}>
          <SelectTrigger size="sm" className="w-32">
            <SelectValue placeholder="方法" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">全部方法</SelectItem>
            {METHODS.map((m) => (
              <SelectItem key={m} value={m}>
                {m}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
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
            {page + 1} / {pageCount}
          </span>
          <Button
            variant="outline"
            size="icon"
            className="size-8"
            disabled={page + 1 >= pageCount}
            onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))}
          >
            <ChevronRightIcon />
          </Button>
        </div>
      </div>

      {/* History table */}
      <div className="flex h-[calc(100vh-15rem)] min-h-0 flex-col">
        <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
          <div className="min-h-0 flex-1 overflow-auto">
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead className="w-36">时间</TableHead>
                  <TableHead className="w-44">host</TableHead>
                  <TableHead className="w-20">方法</TableHead>
                  <TableHead>URL</TableHead>
                  <TableHead className="w-20">状态码</TableHead>
                  <TableHead className="w-36">content-type</TableHead>
                  <TableHead className="w-24 text-right">响应长度</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {exchanges.map((e) => (
                  <TableRow
                    key={e.id}
                    className={cn("cursor-pointer", selected?.id === e.id && "bg-accent hover:bg-accent")}
                    onClick={() => setSelected(e)}
                  >
                    <TableCell className="text-xs text-muted-foreground tabular-nums">{fmtTime(e.ts)}</TableCell>
                    <TableCell className="font-mono text-xs">{e.host}</TableCell>
                    <TableCell>
                      <MethodBadge method={e.method} />
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span className="block truncate font-mono text-xs">{e.url}</span>
                    </TableCell>
                    <TableCell>
                      <span className={cn("font-mono text-xs font-semibold tabular-nums", statusTone(e.status))}>
                        {e.status}
                      </span>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{e.content_type}</TableCell>
                    <TableCell className="text-right text-xs tabular-nums">{fmtBytes(e.resp_len)}</TableCell>
                  </TableRow>
                ))}
                {exchanges.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={7} className="py-12 text-center text-sm text-muted-foreground">
                      {traffic === null ? "加载中…" : "没有匹配的流量。"}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          </div>
        </Card>
      </div>

      <Sheet open={selected !== null} onOpenChange={(open) => !open && setSelected(null)}>
        <SheetContent className="w-full! max-w-none! gap-0 p-0 sm:w-[48rem]! sm:max-w-[48rem]!">
          {selected && (
            <>
              <SheetHeader className="border-b px-5 py-4">
                <div className="flex items-center gap-2 pr-8">
                  <MethodBadge method={selected.method} />
                  <Badge variant="secondary" className={cn("font-mono tabular-nums", statusTone(selected.status))}>
                    {selected.status}
                  </Badge>
                  <span className="ml-auto text-xs text-muted-foreground tabular-nums">{fmtTime(selected.ts)}</span>
                </div>
                <SheetTitle className="break-all font-mono">{selected.host}</SheetTitle>
                <SheetDescription className="break-all font-mono">{selected.url}</SheetDescription>
              </SheetHeader>
              <Tabs defaultValue="request" className="min-h-0 flex-1 gap-0">
                <TabsList className="mx-5 mt-4 grid w-auto grid-cols-2">
                  <TabsTrigger value="request">请求 Request</TabsTrigger>
                  <TabsTrigger value="response">响应 Response</TabsTrigger>
                </TabsList>
                <TabsContent value="request" className="min-h-0 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-5 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      加载报文…
                    </div>
                  ) : (
                    <HttpCodeBlock raw={requestWithHost(detail?.req ?? "", selected)} />
                  )}
                </TabsContent>
                <TabsContent value="response" className="min-h-0 overflow-auto">
                  {detailLoading ? (
                    <div className="flex items-center gap-2 p-5 text-xs text-muted-foreground">
                      <Loader2Icon className="size-3.5 animate-spin" />
                      加载报文…
                    </div>
                  ) : (
                    <HttpCodeBlock raw={detail?.resp ?? ""} />
                  )}
                </TabsContent>
              </Tabs>
            </>
          )}
        </SheetContent>
      </Sheet>

      <AlertDialog
        open={deleteMode !== null}
        onOpenChange={(o) => {
          if (!o) setDeleteMode(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {deleteMode === "selected"
                ? `删除选中的 ${selectedHosts.length} 个目标的全部流量？`
                : "删除该目标的全部流量？"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {deleteMode === "selected" ? (
                <>
                  将永久删除 <span className="font-semibold tabular-nums">{selectedHosts.length}</span> 个目标（
                  <span className="font-mono">
                    {selectedHosts.slice(0, 3).join("、")}
                    {selectedHosts.length > 3 ? "…" : ""}
                  </span>
                  ）的所有流量记录（含请求/响应原文），此操作不可撤销。
                </>
              ) : (
                <>
                  将永久删除 host 包含 <span className="font-mono font-semibold">{hostQ}</span>{" "}
                  的所有流量记录（含请求/响应原文），此操作不可撤销。
                </>
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
            <AlertDialogAction
              onClick={(e) => {
                e.preventDefault();
                confirmDelete();
              }}
              disabled={deleting}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleting ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
