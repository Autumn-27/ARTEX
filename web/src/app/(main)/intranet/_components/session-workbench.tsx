"use client";

import * as React from "react";

import { FileIcon, FolderIcon, Loader2Icon, PlayIcon, RefreshCwIcon, Trash2Icon } from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { SessionFsEntry, ShellSession } from "@/lib/types";
import { cn } from "@/lib/utils";

import { fmtBytes } from "./format";

const TIMEOUT_OPTIONS = [10, 30, 60, 120];

// UTF-8 安全的 base64 编解码（btoa/atob 只认 Latin-1）。
function toBase64(s: string): string {
  return btoa(String.fromCharCode(...new TextEncoder().encode(s)));
}
function fromBase64(b64: string): Uint8Array {
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}

// ---------------------------------------------------------------------------
// 命令终端：历史保留在组件态（会话切换时随 key 重置），输出等宽字体。
// ---------------------------------------------------------------------------
interface TermEntry {
  seq: number;
  cmd: string;
  stdout?: string;
  stderr?: string;
  ms?: number;
  timedOut?: boolean;
  /** HTTP 请求本身失败（网络/鉴权等）。 */
  error?: string;
  /** 通道级错误：HTTP 成功但后端在响应里带 error（如会话已断）。 */
  execError?: string;
}

function Terminal({ session }: { session: ShellSession }) {
  const [history, setHistory] = React.useState<TermEntry[]>([]);
  const [cmd, setCmd] = React.useState("");
  const [timeoutSec, setTimeoutSec] = React.useState(30);
  const [running, setRunning] = React.useState(false);
  const seqRef = React.useRef(0);
  const scrollRef = React.useRef<HTMLDivElement>(null);

  // 新输出落地后滚到底部。
  React.useEffect(() => {
    const el = scrollRef.current;
    if (el && history.length > 0) el.scrollTop = el.scrollHeight;
  }, [history]);

  const run = () => {
    const command = cmd.trim();
    if (!command || running) return;
    setRunning(true);
    setCmd("");
    api
      .sessionExec(session.id, command, timeoutSec)
      .then((r) => {
        seqRef.current += 1;
        setHistory((h) => [
          ...h,
          {
            seq: seqRef.current,
            cmd: command,
            stdout: r.stdout,
            stderr: r.stderr,
            ms: r.ms,
            timedOut: r.timed_out,
            execError: r.error,
          },
        ]);
      })
      .catch((e: unknown) => {
        seqRef.current += 1;
        setHistory((h) => [
          ...h,
          { seq: seqRef.current, cmd: command, error: e instanceof Error ? e.message : "执行失败" },
        ]);
      })
      .finally(() => setRunning(false));
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto bg-neutral-950 p-3 font-mono text-xs">
        {history.length === 0 && <div className="text-neutral-500">输入命令开始执行（经当前 webshell 通道）。</div>}
        {history.map((e) => (
          <div key={e.seq} className="mb-3 last:mb-0">
            <div className="text-emerald-400">$ {e.cmd}</div>
            {e.error ? (
              <pre className="text-red-400 break-all whitespace-pre-wrap">请求失败：{e.error}</pre>
            ) : (
              <>
                {e.execError && (
                  <pre className="text-red-400 break-all whitespace-pre-wrap">执行错误：{e.execError}</pre>
                )}
                {e.stdout && <pre className="text-neutral-200 break-all whitespace-pre-wrap">{e.stdout}</pre>}
                {e.stderr && <pre className="text-red-400 break-all whitespace-pre-wrap">{e.stderr}</pre>}
                {!e.execError && !e.stdout && !e.stderr && <div className="text-neutral-600">（无输出）</div>}
                <div className={cn("text-[10px]", e.timedOut ? "text-amber-400" : "text-neutral-600")}>
                  {e.timedOut ? `超时（>${e.ms ?? 0}ms，可能被截断）` : `${e.ms ?? 0}ms`}
                </div>
              </>
            )}
          </div>
        ))}
      </div>
      <form
        className="flex items-center gap-2 border-t p-2"
        onSubmit={(e) => {
          e.preventDefault();
          run();
        }}
      >
        <span className="pl-1 font-mono text-xs text-emerald-600">$</span>
        <Input
          value={cmd}
          onChange={(e) => setCmd(e.target.value)}
          placeholder="whoami / ipconfig / ls ..."
          className="h-8 flex-1 font-mono text-xs"
          disabled={running}
        />
        <Select value={String(timeoutSec)} onValueChange={(v) => setTimeoutSec(Number(v))}>
          <SelectTrigger size="sm" className="w-24" title="超时">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TIMEOUT_OPTIONS.map((n) => (
              <SelectItem key={n} value={String(n)}>
                {n}s 超时
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button type="submit" size="sm" className="h-8 gap-1" disabled={running || !cmd.trim()}>
          {running ? <Loader2Icon className="size-3.5 animate-spin" /> : <PlayIcon className="size-3.5" />}
          执行
        </Button>
      </form>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 文件管理器：路径输入 + 列目录 / 读 / 写。
// ---------------------------------------------------------------------------

// joinPath 按现有路径风格（\ 或 /）拼接子路径。
function joinPath(base: string, name: string): string {
  const sep = base.includes("\\") ? "\\" : "/";
  const trimmed = base.endsWith(sep) ? base.slice(0, -1) : base;
  return `${trimmed}${sep}${name}`;
}

function FileManager({ session }: { session: ShellSession }) {
  const [path, setPath] = React.useState("");
  const [entries, setEntries] = React.useState<SessionFsEntry[] | null>(null);
  const [listing, setListing] = React.useState(false);

  // 读/写面板：选中文件后加载内容到编辑器，可直接改后写回。
  const [filePath, setFilePath] = React.useState("");
  const [fileContent, setFileContent] = React.useState("");
  const [fileMeta, setFileMeta] = React.useState<{ truncated: boolean; binary: boolean } | null>(null);
  const [fileBusy, setFileBusy] = React.useState(false);
  const [writing, setWriting] = React.useState(false);

  const list = (p: string) => {
    const target = p.trim();
    if (!target || listing) return;
    setListing(true);
    api
      .sessionList(session.id, target)
      .then((es) => {
        setEntries(es);
        setPath(target);
      })
      .catch((e: unknown) => toast.error(e instanceof Error ? e.message : "列目录失败"))
      .finally(() => setListing(false));
  };

  const read = (p: string) => {
    if (fileBusy) return;
    setFileBusy(true);
    api
      .sessionRead(session.id, p)
      .then((r) => {
        setFilePath(p);
        setFileMeta({ truncated: r.truncated, binary: !r.content && !!r.content_base64 });
        if (typeof r.content === "string") {
          setFileContent(r.content);
        } else if (r.content_base64) {
          // 二进制：尝试按 UTF-8 解码展示，失败则提示为二进制。
          try {
            setFileContent(new TextDecoder("utf-8", { fatal: true }).decode(fromBase64(r.content_base64)));
          } catch {
            setFileContent("");
          }
        } else {
          setFileContent("");
        }
      })
      .catch((e: unknown) => toast.error(e instanceof Error ? e.message : "读取失败"))
      .finally(() => setFileBusy(false));
  };

  const write = () => {
    const target = filePath.trim();
    if (!target || writing) return;
    setWriting(true);
    api
      .sessionWrite(session.id, target, toBase64(fileContent))
      .then((r) => toast.success(`已写入 ${r.bytes} 字节 → ${target}`))
      .catch((e: unknown) => toast.error(e instanceof Error ? e.message : "写入失败"))
      .finally(() => setWriting(false));
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col divide-y">
      {/* 目录浏览 */}
      <div className="flex min-h-0 flex-1 flex-col">
        <form
          className="flex items-center gap-2 p-2"
          onSubmit={(e) => {
            e.preventDefault();
            list(path);
          }}
        >
          <Input
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="路径，如 C:\inetpub\wwwroot 或 /var/www/html"
            className="h-8 flex-1 font-mono text-xs"
          />
          <Button type="submit" size="sm" className="h-8 gap-1" disabled={listing || !path.trim()}>
            {listing ? <Loader2Icon className="size-3.5 animate-spin" /> : <RefreshCwIcon className="size-3.5" />}
            列目录
          </Button>
        </form>
        <div className="min-h-0 flex-1 overflow-auto">
          {entries === null ? (
            <div className="py-10 text-center text-xs text-muted-foreground">输入路径后点「列目录」。</div>
          ) : entries.length === 0 ? (
            <div className="py-10 text-center text-xs text-muted-foreground">空目录</div>
          ) : (
            <Table>
              <TableHeader className="sticky top-0 z-10 bg-card">
                <TableRow>
                  <TableHead>名称</TableHead>
                  <TableHead className="w-[90px] text-right">大小</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {entries.map((en) => (
                  <TableRow
                    key={en.name}
                    className="cursor-pointer"
                    onClick={() => (en.is_dir ? list(joinPath(path, en.name)) : read(joinPath(path, en.name)))}
                  >
                    <TableCell className="max-w-0">
                      <span className="flex items-center gap-2">
                        {en.is_dir ? (
                          <FolderIcon className="size-3.5 shrink-0 text-amber-500" />
                        ) : (
                          <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
                        )}
                        <span className="truncate font-mono text-xs" title={en.name}>
                          {en.name}
                        </span>
                      </span>
                    </TableCell>
                    <TableCell className="text-right font-mono text-xs text-muted-foreground tabular-nums">
                      {en.is_dir ? "—" : fmtBytes(en.size)}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      </div>

      {/* 读 / 写 */}
      <div className="flex min-h-0 flex-1 flex-col gap-2 p-2">
        <div className="flex items-center gap-2">
          <Input
            value={filePath}
            onChange={(e) => setFilePath(e.target.value)}
            placeholder="文件路径（点上面文件自动填入，也可手输）"
            className="h-8 flex-1 font-mono text-xs"
          />
          <Button
            variant="outline"
            size="sm"
            className="h-8"
            disabled={fileBusy || !filePath.trim()}
            onClick={() => read(filePath.trim())}
          >
            {fileBusy ? <Loader2Icon className="size-3.5 animate-spin" /> : "读取"}
          </Button>
          <Button size="sm" className="h-8" disabled={writing || !filePath.trim()} onClick={write}>
            {writing ? <Loader2Icon className="size-3.5 animate-spin" /> : "写入"}
          </Button>
        </div>
        {fileMeta?.binary && (
          <p className="text-[11px] text-amber-600 dark:text-amber-400">
            二进制文件：无法按文本展示时编辑器为空；直接写入会覆盖原文件，谨慎操作。
          </p>
        )}
        {fileMeta?.truncated && (
          <p className="text-[11px] text-amber-600 dark:text-amber-400">内容过长已被后端截断，写入前请确认完整性。</p>
        )}
        <Textarea
          value={fileContent}
          onChange={(e) => setFileContent(e.target.value)}
          placeholder="文件内容（读取后可编辑，点「写入」保存到目标）"
          className="min-h-0 flex-1 resize-none font-mono text-xs"
        />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 工作台 Sheet：终端 / 文件两个页签 + 删除会话。历史等状态靠 key=session.id 随会话重置。
// ---------------------------------------------------------------------------
export function SessionWorkbench({
  session,
  onOpenChange,
  onDeleted,
}: {
  session: ShellSession | null;
  onOpenChange: (open: boolean) => void;
  onDeleted: () => void;
}) {
  const [confirming, setConfirming] = React.useState(false);
  const [deleting, setDeleting] = React.useState(false);

  const doDelete = () => {
    if (!session) return;
    setDeleting(true);
    api
      .deleteSession(session.id)
      .then(() => {
        toast.success(`会话 #${session.id} 已删除`);
        setConfirming(false);
        onOpenChange(false);
        onDeleted();
      })
      .catch((e: unknown) => toast.error(e instanceof Error ? e.message : "删除失败"))
      .finally(() => setDeleting(false));
  };

  return (
    <Sheet open={session !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full! max-w-none! gap-0 p-0 sm:w-[52rem]! sm:max-w-[52rem]!">
        {session && (
          <>
            <SheetHeader className="border-b px-5 py-4">
              <SheetTitle className="pr-8">会话工作台 #{session.id}</SheetTitle>
              <SheetDescription className="truncate font-mono text-xs">{session.url}</SheetDescription>
              <div className="flex flex-wrap items-center gap-2 pt-1">
                <Badge variant="secondary" className="font-mono text-xs">
                  {session.kind || "-"}
                </Badge>
                {session.lang && (
                  <Badge variant="outline" className="font-mono text-xs">
                    {session.lang}
                  </Badge>
                )}
                {session.platform && (
                  <Badge variant="outline" className="text-xs">
                    {session.platform}
                  </Badge>
                )}
                <Badge variant="outline" className="font-mono text-xs">
                  {session.host_asset_id}
                </Badge>
                <Button
                  variant="ghost"
                  size="sm"
                  className="ml-auto h-7 gap-1 text-muted-foreground hover:text-red-600"
                  onClick={() => setConfirming(true)}
                >
                  <Trash2Icon className="size-3.5" />
                  删除会话
                </Button>
              </div>
            </SheetHeader>
            <Tabs defaultValue="terminal" className="flex min-h-0 flex-1 flex-col gap-0">
              <TabsList className="mx-5 mt-3 w-fit">
                <TabsTrigger value="terminal">命令终端</TabsTrigger>
                <TabsTrigger value="files">文件管理</TabsTrigger>
              </TabsList>
              <TabsContent value="terminal" className="flex min-h-0 flex-1 flex-col px-5 pt-3 pb-5">
                <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border">
                  <Terminal key={session.id} session={session} />
                </div>
              </TabsContent>
              <TabsContent value="files" className="flex min-h-0 flex-1 flex-col px-5 pt-3 pb-5">
                <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border">
                  <FileManager key={session.id} session={session} />
                </div>
              </TabsContent>
            </Tabs>

            <AlertDialog open={confirming} onOpenChange={setConfirming}>
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>删除会话 #{session.id}？</AlertDialogTitle>
                  <AlertDialogDescription>
                    将断开并移除该 webshell 会话，目标上的马文件不会被清除。此操作不可撤销。
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel disabled={deleting}>取消</AlertDialogCancel>
                  <AlertDialogAction
                    variant="destructive"
                    disabled={deleting}
                    onClick={(e) => {
                      e.preventDefault();
                      doDelete();
                    }}
                  >
                    {deleting ? "删除中…" : "确认删除"}
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
