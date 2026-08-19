"use client";

import * as React from "react";
import Link from "next/link";
import { toast } from "sonner";
import {
  PlusIcon,
  Trash2Icon,
  ArrowRightIcon,
  StarIcon,
  SearchIcon,
  XIcon,
  ChevronRightIcon,
  PaperclipIcon,
  Loader2Icon,
  SlidersHorizontalIcon,
} from "lucide-react";

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
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { TablePagination } from "@/components/table-pagination";
import { api } from "@/lib/api";
import type { Task, TaskStatus, LLMProfile, ChatAttachment } from "@/lib/types";

// fmtBytes renders a human file size for the upload manifest.
function fmtBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`;
  return `${n} B`;
}

// UPLOAD_MARKER labels the auto-appended block of uploaded-file paths inside the task
// description, so re-uploads append under the same block instead of adding a new header.
const UPLOAD_MARKER = "【上传文件（绝对路径）】";

// appendUploads folds newly-uploaded files' ABSOLUTE paths into the description as a
// Read/Bash-friendly manifest — the worker opens them by path. Keeps one marked block:
// first upload adds the header, later uploads append bullets under it.
function appendUploads(desc: string, atts: ChatAttachment[]): string {
  const bullets = atts.map((a) => `- ${a.abs ?? a.path}（${fmtBytes(a.size)}）`).join("\n");
  if (desc.includes(UPLOAD_MARKER)) {
    return `${desc.replace(/\s*$/, "")}\n${bullets}\n`;
  }
  const head = desc.trim() ? `${desc.replace(/\s*$/, "")}\n\n` : "";
  return `${head}${UPLOAD_MARKER} worker 可用 Read/Bash 按路径打开：\n${bullets}\n`;
}

// ACTIVE_PROFILE is the sentinel Select value for "use the global active profile".
const ACTIVE_PROFILE = "__active__";

// POLL_MS is the task-list refresh interval. Task state moves on the server (planner /
// worker), so the list has to be pulled; 10s is plenty for status / 进度 / token 变化.
const POLL_MS = 10_000;

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k";
  return String(n);
}

// fmtDuration renders a run duration in seconds as a compact human string.
function fmtDuration(sec: number): string {
  if (sec <= 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

// taskDuration is the task's run duration in seconds: created → now while running,
// created → completion for a finished task, else created → last activity. 0 when it
// never ran (no activity yet).
function taskDuration(task: Task, nowSec: number): number {
  const start = task.created_unix ?? 0;
  if (!start) return 0;
  const end =
    task.status === "running"
      ? nowSec
      : task.completed_unix && task.completed_unix > 0
        ? task.completed_unix
        : task.last_activity_unix ?? 0;
  return end > start ? end - start : 0;
}

// fmtDateTime renders a unix-seconds timestamp as a compact local date-time
// (MM-DD HH:mm), or "—" when unset.
function fmtDateTime(unix?: number): string {
  if (!unix || unix <= 0) return "—";
  const d = new Date(unix * 1000);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

const STATUS_OPTIONS: { value: TaskStatus; label: string }[] = [
  { value: "created", label: "已创建" },
  { value: "queued", label: "排队中" },
  { value: "running", label: "运行中" },
  { value: "paused", label: "已暂停" },
  { value: "done", label: "已完成" },
  { value: "failed", label: "失败" },
  { value: "timeout", label: "已超时" },
];

export default function TasksPage() {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [query, setQuery] = React.useState("");
  const [statusFilter, setStatusFilter] = React.useState<TaskStatus | "all">("all");
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [nowSec, setNowSec] = React.useState(() => Math.floor(Date.now() / 1000));

  const filtered = React.useMemo(() => {
    const q = query.trim().toLowerCase();
    return tasks.filter((t) => {
      if (statusFilter !== "all" && t.status !== statusFilter) return false;
      if (!q) return true;
      return (
        t.description.toLowerCase().includes(q) ||
        t.goal.toLowerCase().includes(q) ||
        t.id.toLowerCase().includes(q)
      );
    });
  }, [tasks, query, statusFilter]);

  // reset to page 1 whenever filters change
  React.useEffect(() => { setPage(1); }, [query, statusFilter]);

  const paginated = React.useMemo(
    () => filtered.slice((page - 1) * pageSize, page * pageSize),
    [filtered, page, pageSize],
  );

  // lastRef holds the previous poll's serialized payload: the list is re-fetched every
  // POLL_MS but usually comes back unchanged, and setTasks on an identical payload would
  // re-render the whole page for nothing. Bail out when it matches.
  const lastRef = React.useRef<string>("");

  const load = React.useCallback(() => {
    api.tasks()
      .then((r) => {
        const next = r.tasks.map((t) => (t.id === r.active ? { ...t, active: true } : t));
        const sig = JSON.stringify(next);
        if (sig === lastRef.current) return;
        lastRef.current = sig;
        setTasks(next);
      })
      .catch(() => {});
  }, []);

  React.useEffect(() => {
    load();
    const i = setInterval(load, POLL_MS);
    return () => clearInterval(i);
  }, [load]);

  // 只在确有 running 任务时才每秒 tick——其余情况「运行时长」是静态值,空转的 tick 会白白
  // 重渲染整张表。
  const hasRunning = React.useMemo(() => tasks.some((t) => t.status === "running"), [tasks]);

  // tick every second so running tasks' 运行时长 counts up live.
  React.useEffect(() => {
    if (!hasRunning) return;
    setNowSec(Math.floor(Date.now() / 1000));
    const i = setInterval(() => setNowSec(Math.floor(Date.now() / 1000)), 1000);
    return () => clearInterval(i);
  }, [hasRunning]);

  const deleteTask = React.useCallback(async (id: string) => {
    try {
      await api.deleteTask(id);
      toast.success("任务已删除（全局资产图保留）");
      load();
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }, [load]);

  return (
    <Card>
      <CardContent className="flex flex-col gap-4 px-0 pt-6">
        <div className="flex flex-wrap items-center gap-2 px-4 lg:px-6">
          <div className="relative w-full sm:max-w-xs">
            <SearchIcon className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              placeholder="搜索描述 / 目标 / ID"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="pl-8"
            />
            {query && (
              <button
                type="button"
                onClick={() => setQuery("")}
                aria-label="清除搜索"
                className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
              >
                <XIcon className="size-4" />
              </button>
            )}
          </div>
          <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v as TaskStatus | "all")}>
            <SelectTrigger className="w-36">
              <SelectValue placeholder="状态" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部状态</SelectItem>
              {STATUS_OPTIONS.map((s) => (
                <SelectItem key={s.value} value={s.value}>
                  {s.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <span className="text-muted-foreground text-xs tabular-nums">
            {filtered.length}/{tasks.length} 条
          </span>
          <ConcurrencySettingsDialog />
          <CreateTaskSheet onCreated={load} />
        </div>

        {tasks.length === 0 ? (
          <div className="text-muted-foreground mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-sm lg:mx-6">
            暂无任务，点击右上角「新建任务」开始。
          </div>
        ) : filtered.length === 0 ? (
          <div className="text-muted-foreground mx-4 flex items-center justify-center rounded-lg border border-dashed py-20 text-sm lg:mx-6">
            没有匹配的任务。
          </div>
        ) : (
          <Table className="**:data-[slot='table-cell']:px-4 **:data-[slot='table-head']:px-4">
            <TableHeader className="[&_tr]:border-t">
              <TableRow>
                <TableHead className="font-mono">ID</TableHead>
                <TableHead>描述</TableHead>
                <TableHead>目标</TableHead>
                <TableHead>状态</TableHead>
                <TableHead className="text-center">目标进度</TableHead>
                <TableHead className="text-right">创建时间</TableHead>
                <TableHead className="text-right">运行时长</TableHead>
                <TableHead className="text-right">Token</TableHead>
                <TableHead className="sticky right-0 z-10 bg-card text-right shadow-[-1px_0_0_0_hsl(var(--border))]">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {paginated.map((task) => (
                <TaskRow
                  key={task.id}
                  task={task}
                  // running 任务才吃 nowSec;其余行传 0 —— props 不变,memo 就能拦下每秒 tick
                  // 带来的整表重渲染,只让在跑的那几行走时长。
                  nowSec={task.status === "running" ? nowSec : 0}
                  onDelete={deleteTask}
                />
              ))}
            </TableBody>
          </Table>
        )}
        <TablePagination
          page={page}
          pageSize={pageSize}
          total={filtered.length}
          onPageChange={setPage}
          onPageSizeChange={setPageSize}
        />
      </CardContent>
    </Card>
  );
}

// TaskRow renders one row of the task table. Memoized so the per-second 运行时长 tick and
// the POLL_MS list refresh only re-render the rows whose data actually moved — a table page
// is 20 rows × (StatusBadge + Link + a Radix AlertDialog tree), far too heavy to rebuild
// wholesale on every parent render.
const TaskRow = React.memo(function TaskRow({
  task,
  nowSec,
  onDelete,
}: {
  task: Task;
  nowSec: number;
  onDelete: (id: string) => void;
}) {
  return (
    <TableRow className="group border-border/60">
      <TableCell>
        <code className="bg-muted rounded px-1.5 py-0.5 font-mono text-xs">{task.id}</code>
      </TableCell>
      <TableCell className="font-medium">
        <div className="flex max-w-xs items-center gap-2">
          <span className="truncate" title={task.description}>
            {task.description}
          </span>
          {task.active && (
            <StarIcon className="size-4 shrink-0 fill-amber-400 text-amber-400" />
          )}
        </div>
      </TableCell>
      <TableCell className="text-muted-foreground max-w-xs truncate">{task.goal}</TableCell>
      <TableCell>
        <StatusBadge domain="task" value={task.status} dot />
      </TableCell>
      <TableCell className="text-muted-foreground text-center text-xs tabular-nums">
        {typeof task.goals_total === "number" && task.goals_total > 0
          ? `${task.goals_met}/${task.goals_total}`
          : <span className="text-muted-foreground">—</span>}
      </TableCell>
      <TableCell className="text-muted-foreground text-right text-xs whitespace-nowrap tabular-nums">
        {fmtDateTime(task.created_unix)}
      </TableCell>
      <TableCell className="text-right text-xs whitespace-nowrap tabular-nums">
        {(() => {
          const secs = taskDuration(task, nowSec);
          if (secs <= 0) return <span className="text-muted-foreground">—</span>;
          return (
            <span className={task.status === "running" ? "text-foreground" : "text-muted-foreground"}>
              {fmtDuration(secs)}
            </span>
          );
        })()}
      </TableCell>
      <TableCell
        className="text-right text-xs whitespace-nowrap tabular-nums"
        title={
          task.tokens
            ? `输入 ${task.tokens.input_tokens} · 缓存 ${task.tokens.cache_read_tokens} · 输出 ${task.tokens.output_tokens}`
            : undefined
        }
      >
        {task.tokens ? (
          <span className="text-muted-foreground">
            入 <span className="text-foreground">{fmtTokens(task.tokens.input_tokens)}</span>
            {" · 缓 "}
            <span className="text-foreground">{fmtTokens(task.tokens.cache_read_tokens)}</span>
            {" · 出 "}
            <span className="text-foreground">{fmtTokens(task.tokens.output_tokens)}</span>
          </span>
        ) : (
          "—"
        )}
      </TableCell>
      <TableCell className="sticky right-0 z-10 bg-card text-right shadow-[-1px_0_0_0_hsl(var(--border))] group-hover:bg-muted/50">
        <div className="flex items-center justify-end gap-2">
          <Button asChild size="sm">
            <Link href={`/function/tasks/detail?id=${task.id}`}>
              进入 <ArrowRightIcon />
            </Link>
          </Button>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button size="icon" variant="outline" aria-label="删除任务">
                <Trash2Icon className="text-destructive" />
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>确认删除任务 #{task.id}？</AlertDialogTitle>
                <AlertDialogDescription className="break-words">
                  {task.description ? (
                    <>
                      「
                      <span className="break-all">
                        {task.description.length > 80
                          ? task.description.slice(0, 80) + "…"
                          : task.description}
                      </span>
                      」
                    </>
                  ) : (
                    "该任务"
                  )}
                  的执行记录、会话与产物将被删除，此操作不可撤销（全局资产图保留）。
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>取消</AlertDialogCancel>
                <AlertDialogAction onClick={() => onDelete(task.id)}>
                  删除
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </TableCell>
    </TableRow>
  );
});

// ConcurrencySettingsDialog is the 齿轮 button left of 新建任务: toggle the
// simultaneous-running-task cap and pick the limit. Default off; default limit 5.
// Persisted globally via /api/settings — applies to tasks created afterwards.
function ConcurrencySettingsDialog() {
  const [open, setOpen] = React.useState(false);
  const [enabled, setEnabled] = React.useState(false);
  const [limit, setLimit] = React.useState("5");
  const [loading, setLoading] = React.useState(false);
  const [saving, setSaving] = React.useState(false);

  // 每次打开都拉取当前值,避免显示陈旧状态。
  React.useEffect(() => {
    if (!open) return;
    setLoading(true);
    api
      .settings()
      .then((s) => {
        setEnabled(!!s.task_concurrency_enabled);
        setLimit(String(s.task_concurrency_limit ?? 5));
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [open]);

  async function save() {
    const n = Math.max(1, Math.floor(Number(limit) || 5));
    setSaving(true);
    try {
      await api.setSettings({ task_concurrency_enabled: enabled, task_concurrency_limit: n });
      toast.success(enabled ? `已开启并发限制：最多同时运行 ${n} 个任务` : "已关闭任务并发限制");
      setOpen(false);
    } catch (e) {
      toast.error("保存失败：" + (e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm" variant="outline" className="ml-auto" aria-label="任务并发设置">
          <SlidersHorizontalIcon /> 并发设置
        </Button>
      </DialogTrigger>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>任务并发限制</DialogTitle>
          <DialogDescription>
            限制同时「运行中」的任务数量。关闭时不限制；开启后新建任务若已达上限，会进入「排队中」，有空位时自动开始。
          </DialogDescription>
        </DialogHeader>

        <div className="grid gap-5 py-2">
          <div className="flex items-center justify-between gap-4">
            <div className="grid gap-1">
              <Label htmlFor="conc-enabled">开启任务并发限制</Label>
              <span className="text-muted-foreground text-xs">默认关闭（不限制并发）</span>
            </div>
            <Switch id="conc-enabled" checked={enabled} onCheckedChange={setEnabled} disabled={loading} />
          </div>

          {enabled && (
            <div className="grid gap-2">
              <Label htmlFor="conc-limit">并发数量（同时运行的任务上限）</Label>
              <Input
                id="conc-limit"
                type="number"
                min={1}
                className="w-32"
                value={limit}
                onChange={(e) => setLimit(e.target.value)}
                disabled={loading}
              />
              <span className="text-muted-foreground text-xs">开启后默认 5，最小 1。</span>
            </div>
          )}
        </div>

        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline">取消</Button>
          </DialogClose>
          <Button onClick={save} disabled={loading || saving}>
            {saving ? <Loader2Icon className="animate-spin" /> : null}
            保存
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// CreateTaskSheet is the 新建任务 drawer. Its form state lives HERE, not in TasksPage: with
// 描述/目标 held by the page component every keystroke re-rendered the whole task table
// behind the drawer (plus its sticky column and 20 AlertDialog trees), which showed up as
// input lag. Now typing only re-renders the drawer.
function CreateTaskSheet({ onCreated }: { onCreated: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [description, setDescription] = React.useState("");
  const [goal, setGoal] = React.useState("");
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [llmProfile, setLlmProfile] = React.useState<string>(ACTIVE_PROFILE); // sentinel = active
  const [timeoutMin, setTimeoutMin] = React.useState(""); // 任务级超时(分钟);空/0 = 不限时
  const [heartbeatMin, setHeartbeatMin] = React.useState("10"); // planner 心跳(分钟);默认10,下限10(与后端一致)
  const [seedFirstIntent, setSeedFirstIntent] = React.useState(false); // 创建时下发种子意图,worker 免等首轮 planner 直接开跑;默认关闭,走标准先规划再执行
  // 方式1 文件上传:建任务前把文件暂存到 drafts/<draftId>/uploads/,拿回绝对路径追加进描述。
  const [uploading, setUploading] = React.useState(false);
  const [uploadCount, setUploadCount] = React.useState(0);
  const draftIdRef = React.useRef<string>("");
  const fileInputRef = React.useRef<HTMLInputElement>(null);

  // load LLM profiles once for the create-task profile picker.
  React.useEffect(() => {
    api.llmProfiles().then(setProfiles).catch(() => setProfiles([]));
  }, []);

  // pickFiles uploads the chosen files into this draft's staging dir and appends their
  // absolute paths to the description; the task's agents open them by path via Read/Bash.
  async function pickFiles(files: FileList | null) {
    if (!files || files.length === 0) return;
    // crypto.randomUUID 仅在安全上下文可用(https/localhost);经 IP+http 访问时降级。
    if (!draftIdRef.current) {
      draftIdRef.current =
        globalThis.crypto?.randomUUID?.() ??
        `d${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
    }
    setUploading(true);
    try {
      const r = await api.chatUpload("staging", draftIdRef.current, Array.from(files));
      setDescription((prev) => appendUploads(prev, r.attachments));
      setUploadCount((n) => n + r.attachments.length);
    } catch (e) {
      toast.error("上传失败：" + (e as Error).message);
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = ""; // allow re-picking the same file
    }
  }

  async function createTask() {
    if (!description.trim() || !goal.trim()) {
      toast.error("请填写描述与目标");
      return;
    }
    try {
      const pid = llmProfile === ACTIVE_PROFILE ? undefined : Number(llmProfile);
      const timeoutSec = Math.max(0, Math.floor(Number(timeoutMin) || 0)) * 60;
      const heartbeatSec = Math.max(10, Math.floor(Number(heartbeatMin) || 10)) * 60; // 下限 10min，与后端归一一致
      await api.createTask(description.trim(), goal.trim(), pid, timeoutSec, seedFirstIntent, heartbeatSec);
      toast.success("任务已创建");
      setDescription("");
      setGoal("");
      setLlmProfile(ACTIVE_PROFILE);
      setTimeoutMin("");
      setHeartbeatMin("10");
      setSeedFirstIntent(false);
      setUploadCount(0);
      draftIdRef.current = "";
      setOpen(false);
      onCreated();
    } catch (e) {
      toast.error("创建失败：" + (e as Error).message);
    }
  }

  return (
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetTrigger asChild>
          <Button size="sm">
            <PlusIcon /> 新建任务
          </Button>
        </SheetTrigger>
        {/* 45vw 宽的右侧抽屉:整屏高度可滚动,长表单不再受弹窗高度限制。窄屏退化为全宽。
            内容为 flex 列:头/脚固定,中间字段区 flex-1 独立滚动。 */}
        <SheetContent
          side="right"
          className="w-full! max-w-none! gap-0 p-0 sm:w-[45vw]! sm:max-w-[45vw]!"
        >
          <SheetHeader className="border-b p-6">
            <SheetTitle>新建任务</SheetTitle>
            <SheetDescription>填写测试对象与目标，高级参数可按需展开。</SheetDescription>
          </SheetHeader>

          <div className="flex-1 overflow-y-auto p-6">
            <div className="grid gap-5">
              <div className="grid gap-2">
                <Label htmlFor="description">描述</Label>
                <Textarea
                  id="description"
                  className="min-h-32"
                  placeholder="测试对象与背景，例如：测试 example.com 这个站点"
                  value={description}
                  onChange={(e) => setDescription(e.target.value)}
                />
                {/* 上传文件(可多选):暂存到 drafts/,把绝对路径追加进上方描述,worker 据此 Read/Bash 打开。 */}
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    ref={fileInputRef}
                    type="file"
                    multiple
                    className="hidden"
                    onChange={(e) => void pickFiles(e.target.files)}
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => fileInputRef.current?.click()}
                    disabled={uploading}
                  >
                    {uploading ? (
                      <Loader2Icon className="animate-spin" />
                    ) : (
                      <PaperclipIcon />
                    )}
                    上传文件
                  </Button>
                  <span className="text-muted-foreground text-xs">
                    {uploadCount > 0
                      ? `已上传 ${uploadCount} 个文件，绝对路径已追加到描述末尾（可编辑）`
                      : "可多选；上传后把文件的绝对路径追加到描述，供 worker 用 Read/Bash 打开"}
                  </span>
                </div>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="goal">目标</Label>
                <Textarea
                  id="goal"
                  className="min-h-32"
                  placeholder="要达成什么，例如：拿下后台管理权限、获取服务器权限"
                  value={goal}
                  onChange={(e) => setGoal(e.target.value)}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="llm-profile">LLM 配置</Label>
                <Select value={llmProfile} onValueChange={setLlmProfile}>
                  <SelectTrigger id="llm-profile">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ACTIVE_PROFILE}>使用激活配置（默认）</SelectItem>
                    {profiles.map((p) => (
                      <SelectItem key={p.id} value={String(p.id)}>
                        {p.name}
                        {p.is_default ? "（激活）" : ""} · {p.model}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-muted-foreground text-xs">
                  本任务的 planner/worker 使用所选配置运行；对话仍用激活配置。
                </p>
              </div>

              {/* 高级参数默认折叠:超时/心跳/首个意图,展开才占空间,常用路径保持清爽。 */}
              <Collapsible>
                <CollapsibleTrigger className="group flex w-full items-center gap-2 border-t pt-4 text-sm font-medium">
                  <ChevronRightIcon className="text-muted-foreground size-4 transition-transform group-data-[state=open]:rotate-90" />
                  高级设置
                  <span className="text-muted-foreground ml-auto text-xs font-normal">
                    超时 · 心跳 · 首个意图
                  </span>
                </CollapsibleTrigger>
                <CollapsibleContent className="grid gap-5 pt-5">
                  <div className="grid gap-2">
                    <Label htmlFor="timeout-min">任务超时（分钟，可选）</Label>
                    <Input
                      id="timeout-min"
                      type="number"
                      min={0}
                      className="w-40"
                      placeholder="留空 = 不限时"
                      value={timeoutMin}
                      onChange={(e) => setTimeoutMin(e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">
                      到点后触发优雅收尾（各 agent 写回 + planner 终局判定），任务进入 timeout 终态。
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="heartbeat-min">planner 心跳（分钟）</Label>
                    <Input
                      id="heartbeat-min"
                      type="number"
                      min={10}
                      className="w-40"
                      placeholder="默认 10"
                      value={heartbeatMin}
                      onChange={(e) => setHeartbeatMin(e.target.value)}
                    />
                    <p className="text-muted-foreground text-xs">
                      距上轮规划结束/任务开始满该时长且期间无触发，自动触发一轮规划（兜底卡死 + 唤醒去监督在跑的 worker）。下限 10 分钟。
                    </p>
                  </div>
                  <div className="grid gap-2">
                    <label className="flex items-center gap-2 text-sm">
                      <Checkbox checked={seedFirstIntent} onCheckedChange={(v) => setSeedFirstIntent(!!v)} />
                      直接下发首个意图（描述+目标）
                    </label>
                    <p className="text-muted-foreground text-xs">
                      开启后创建即把「描述+目标」作为一条意图下发，worker 免等首轮规划直接开跑，跑完再由 planner 接手判定/补充。CTF 等常一个 work 直接解决的场景推荐开启；关闭则走标准的先规划再执行。
                    </p>
                  </div>
                </CollapsibleContent>
              </Collapsible>
            </div>
          </div>

          <SheetFooter className="flex-row justify-end gap-2 border-t p-4">
            <SheetClose asChild>
              <Button variant="outline">取消</Button>
            </SheetClose>
            <Button onClick={createTask}>创建</Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
  );
}
