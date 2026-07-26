"use client";

import * as React from "react";
import { toast } from "sonner";
import { Bot, ChevronDownIcon, PlusIcon, SendIcon, Square, Trash2Icon, ZapIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { Transcript } from "@/components/transcript";
import { TodoPopover } from "@/components/todo-popover";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { api } from "@/lib/api";
import type { Activity, Agent, Conversation, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

// fmtTokens renders a compact token count (1234 → 1.2k, 2_000_000 → 2M).
function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(n >= 10_000_000 ? 0 : 1) + "M";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k";
  return String(n);
}

// fmtDuration renders an elapsed milliseconds span compactly (90s → 1m30s).
function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m${String(s % 60).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h${String(m % 60).padStart(2, "0")}m`;
}

// LiveBadge is the small pulsing "实时" chip reused from the task's main-agent
// console — shown while a turn is streaming.
function LiveBadge() {
  return (
    <span className="inline-flex items-center gap-1 rounded bg-blue-500/15 px-1.5 py-0.5 text-[10px] font-medium text-blue-600 dark:text-blue-400">
      <span className="size-1 animate-pulse rounded-full bg-blue-500" />
      实时
    </span>
  );
}

// Composer is the shared bottom input (textarea grows to a cap, Enter sends,
// Shift+Enter newlines) — the same affordance across DraftChat and ChatView.
function Composer({
  value,
  onChange,
  onSend,
  disabled,
  placeholder,
  leftSlot,
  running,
  onStop,
  stopDisabled,
}: {
  value: string;
  onChange: (v: string) => void;
  onSend: () => void;
  disabled: boolean;
  placeholder: string;
  leftSlot?: React.ReactNode;
  running?: boolean;
  onStop?: () => void;
  stopDisabled?: boolean;
}) {
  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      onSend();
    }
  }
  return (
    <div className="border-t p-3">
      <div className="flex items-end gap-2">
        {leftSlot}
        <Textarea
          className="max-h-40 min-h-10 flex-1 resize-none"
          rows={1}
          placeholder={placeholder}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
        />
        {running ? (
          // while a run is in flight the send button becomes a stop button —
          // aborts just this session (the trigger queue keeps going).
          <Button size="icon" variant="destructive" onClick={onStop} disabled={stopDisabled} title="停止本次运行">
            <Square className="size-3.5 fill-current" />
          </Button>
        ) : (
          <Button size="icon" onClick={onSend} disabled={disabled || !value.trim()}>
            <SendIcon className="size-4" />
          </Button>
        )}
      </div>
    </div>
  );
}

// LLMProfileRow shows the active LLM config below the composer and lets the user
// switch it via a Popover. `selected` is the profile id or null for default.
function LLMProfileRow({
  profiles,
  selected,
  onChange,
  disabled,
  rightSlot,
}: {
  profiles: LLMProfile[];
  selected: number | null;
  onChange: (id: number | null) => void;
  disabled?: boolean;
  rightSlot?: React.ReactNode;
}) {
  const [open, setOpen] = React.useState(false);
  const activeDefault = profiles.find((p) => p.is_default);
  const current = selected != null ? profiles.find((p) => Number(p.id) === selected) : null;
  const label = current ? current.name : `默认${activeDefault ? `（${activeDefault.name}）` : ""}`;

  return (
    <div className="flex items-center gap-1 px-1 pb-1 pt-0.5">
      <ZapIcon className="text-muted-foreground/50 size-3 shrink-0" />
      <span className="text-muted-foreground/70 text-xs">{label}</span>
      <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            disabled={disabled}
            className="text-primary flex items-center gap-0.5 text-xs hover:underline disabled:pointer-events-none disabled:opacity-40"
          >
            更换
            <ChevronDownIcon className="size-3" />
          </button>
        </PopoverTrigger>
        <PopoverContent align="start" className="w-64 p-1">
          <p className="text-muted-foreground px-2 py-1 text-[11px] font-medium">选择 LLM 配置</p>
          {/* default option */}
          <button
            type="button"
            onClick={() => { onChange(null); setOpen(false); }}
            className={cn(
              "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
              selected == null && "bg-accent",
            )}
          >
            <span className="text-sm">默认{activeDefault ? `（${activeDefault.name}）` : ""}</span>
            {activeDefault && (
              <span className="text-muted-foreground text-[11px]">{activeDefault.format} · {activeDefault.model}</span>
            )}
          </button>
          {profiles.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => { onChange(Number(p.id)); setOpen(false); }}
              className={cn(
                "flex w-full flex-col rounded px-2 py-1.5 text-left hover:bg-accent",
                selected === Number(p.id) && "bg-accent",
              )}
            >
              <span className="text-sm">{p.name}</span>
              <span className="text-muted-foreground text-[11px]">{p.format} · {p.model}</span>
            </button>
          ))}
        </PopoverContent>
      </Popover>
      {rightSlot}
    </div>
  );
}

// DraftChat is the default right-pane view: a fresh chat (agent picker in the
// header, centered empty state, composer) with NO conversation created yet. The
// conversation is created lazily on the first send (ChatGPT-style), then the
// parent switches to the real ChatView.
function DraftChat({
  agents,
  profiles,
  onStarted,
}: {
  agents: Agent[];
  profiles: LLMProfile[];
  onStarted: (c: Conversation) => void;
}) {
  const [agentKey, setAgentKey] = React.useState("");
  const [llmProfileId, setLlmProfileId] = React.useState<number | null>(null);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);

  // default the agent to Auto once agents load.
  React.useEffect(() => {
    if (!agentKey && agents.some((a) => a.key === "auto")) setAgentKey("auto");
  }, [agentKey, agents]);

  const agent = agents.find((a) => a.key === agentKey);

  async function send() {
    const msg = input.trim();
    if (!msg || !agentKey || sending) return;
    setSending(true);
    try {
      const c = await api.createConversation(agentKey, "", llmProfileId);
      await api.sendConversationMessage(c.id, msg);
      onStarted(c);
    } catch (e) {
      toast.error("发送失败：" + (e as Error).message);
      setSending(false);
    }
  }

  const agentPicker = (
    <Select value={agentKey} onValueChange={setAgentKey}>
      <SelectTrigger size="sm" className="w-40 shrink-0">
        <SelectValue placeholder="选择 Agent…" />
      </SelectTrigger>
      <SelectContent>
        {agents.map((a) => (
          <SelectItem key={a.key} value={a.key}>
            <span className="flex items-center gap-2">
              <Bot className="size-3.5" />
              {a.name}
              {!a.builtin && (
                <Badge variant="outline" className="px-1 py-0 text-[9px]">自定义</Badge>
              )}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );

  return (
    <>
      {/* empty / landing state fills the panel */}
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-2 px-4 text-center">
        <div className="bg-primary/10 flex size-12 items-center justify-center rounded-full">
          <Bot className="text-primary size-6" />
        </div>
        <div className="text-sm font-medium">开始和「{agent?.name ?? "Agent"}」对话</div>
        {agent?.description && (
          <p className="text-muted-foreground max-w-md text-xs">{agent.description}</p>
        )}
      </div>

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={sending || !agentKey}
        placeholder="输入消息，Enter 发送，Shift+Enter 换行"
        leftSlot={agentPicker}
      />
      <LLMProfileRow
        profiles={profiles}
        selected={llmProfileId}
        onChange={setLlmProfileId}
        disabled={sending}
      />
    </>
  );
}

// ChatView is the right pane for one conversation — mirrors the task detail's
// main-agent console: header with agent + live badge + token/duration meta, a
// scroll-stick transcript (chat mode), and the composer.
function ChatView({
  conv,
  agents,
  profiles,
  onTitleMaybeChanged,
  onConvUpdated,
}: {
  conv: Conversation;
  agents: Agent[];
  profiles: LLMProfile[];
  onTitleMaybeChanged: () => void;
  onConvUpdated: () => void;
}) {
  const [messages, setMessages] = React.useState<Activity[]>([]);
  const [running, setRunning] = React.useState(false);
  const [input, setInput] = React.useState("");
  const [sending, setSending] = React.useState(false);
  const [stopping, setStopping] = React.useState(false);
  const cursorRef = React.useRef(0);
  const agent = agents.find((a) => a.key === conv.agent_key);
  const currentProfileId = conv.llm_profile_id ?? null;

  async function changeProfile(id: number | null) {
    try {
      await api.updateConversationProfile(conv.id, id);
      onConvUpdated();
    } catch (e) {
      toast.error("切换 LLM 失败：" + (e as Error).message);
    }
  }

  // conversation-scoped detail fetcher for the reused Transcript renderer.
  const fetchDetail = React.useCallback(
    (seq: number) => api.conversationMsgDetail(conv.id, seq),
    [conv.id],
  );

  // seq of the most-recent TodoWrite tool call (for the Todo popover); null if none.
  const latestTodoSeq = React.useMemo(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const a = messages[i];
      if (a.kind === "tool_use" && a.tool === "TodoWrite") return a.seq;
    }
    return null;
  }, [messages]);

  // reset + load whenever the selected conversation changes.
  React.useEffect(() => {
    cursorRef.current = 0;
    setMessages([]);
    setRunning(false);
    let live = true;
    api
      .conversationMessages(conv.id, 0)
      .then((r) => {
        if (!live) return;
        setMessages(r.items);
        cursorRef.current = r.cursor;
        setRunning(r.running);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [conv.id]);

  // poll while a turn is running: pull new steps after the cursor.
  React.useEffect(() => {
    if (!running) return;
    let live = true;
    const tick = async () => {
      try {
        const r = await api.conversationMessages(conv.id, cursorRef.current);
        if (!live) return;
        if (r.items.length) {
          setMessages((prev) => [...prev, ...r.items]);
          cursorRef.current = r.cursor;
        }
        setRunning(r.running);
        if (!r.running) onTitleMaybeChanged(); // first-turn auto-title landed
      } catch {
        /* transient — keep polling */
      }
    };
    const h = setInterval(tick, 1000);
    return () => {
      live = false;
      clearInterval(h);
    };
  }, [running, conv.id, onTitleMaybeChanged]);

  // ---- transcript auto-scroll (open → bottom; stick to bottom unless scrolled up) ----
  const contentRef = React.useRef<HTMLDivElement | null>(null);
  const atBottomRef = React.useRef(true);
  const viewport = React.useCallback(
    () =>
      (contentRef.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null) ??
      null,
    [],
  );
  React.useEffect(() => {
    const vp = viewport();
    if (!vp) return;
    const onScroll = () => {
      atBottomRef.current = vp.scrollTop + vp.clientHeight >= vp.scrollHeight - 60;
    };
    vp.addEventListener("scroll", onScroll, { passive: true });
    return () => vp.removeEventListener("scroll", onScroll);
  }, [viewport]);
  // open/switch a conversation → jump to the latest (bottom)
  React.useLayoutEffect(() => {
    const vp = viewport();
    if (vp) {
      vp.scrollTop = vp.scrollHeight;
      atBottomRef.current = true;
    }
  }, [conv.id, viewport]);
  // new activity → stick to bottom only if the user is already pinned there
  React.useLayoutEffect(() => {
    if (!atBottomRef.current) return;
    const vp = viewport();
    if (vp) vp.scrollTop = vp.scrollHeight;
  }, [messages, running, viewport]);

  // Per-conversation token total, live — same accounting as the main-agent
  // console: completed runs' `result` sum + the in-progress run's latest `usage`.
  const tokenTotal = React.useMemo(() => {
    let i = 0, o = 0, cr = 0;
    let li = 0, lo = 0, lcr = 0;
    let turns = 0; // agent 循环轮次 = 模型调用次数（每次一条 kind='usage'）
    for (const a of messages) {
      if (a.kind === "result") {
        i += a.input_tokens ?? 0;
        o += a.output_tokens ?? 0;
        cr += a.cache_read_tokens ?? 0;
        li = lo = lcr = 0;
      } else if (a.kind === "usage") {
        turns += 1;
        li = a.input_tokens ?? 0;
        lo = a.output_tokens ?? 0;
        lcr = a.cache_read_tokens ?? 0;
      }
    }
    const I = i + li, O = o + lo, CR = cr + lcr;
    return { i: I, o: O, cr: CR, turns, any: I + O + CR > 0 };
  }, [messages]);

  async function send() {
    const msg = input.trim();
    if (!msg || sending || running) return;
    setSending(true);
    setInput("");
    try {
      await api.sendConversationMessage(conv.id, msg);
      setRunning(true);
      // pull the just-persisted human turn immediately.
      const r = await api.conversationMessages(conv.id, cursorRef.current);
      setMessages((prev) => [...prev, ...r.items]);
      cursorRef.current = r.cursor;
    } catch (e) {
      toast.error("发送失败：" + (e as Error).message);
      setInput(msg); // restore so the user doesn't lose their text
    } finally {
      setSending(false);
    }
  }

  // stop aborts the in-flight run for this conversation. running flips to false on
  // the next 1s poll once the backend unwinds the agent; the trigger queue is not
  // affected — the agent's next queued fire still starts.
  async function stop() {
    if (stopping) return;
    setStopping(true);
    try {
      await api.stopConversation(conv.id);
    } catch (e) {
      toast.error("停止失败：" + (e as Error).message);
    } finally {
      setStopping(false);
    }
  }

  return (
    <>
      {/* header: which agent + live + token meta */}
      <div className="flex items-center gap-2 border-b px-4 py-2.5">
        <Bot className="text-muted-foreground size-4 shrink-0" />
        <span className="shrink-0 text-sm font-medium">{agent?.name ?? conv.agent_key}</span>
        <span className="text-muted-foreground shrink-0 font-mono text-xs">{conv.agent_key}</span>
        {agent && !agent.builtin && (
          <Badge variant="outline" className="shrink-0 px-1.5 py-0 text-[10px]">
            自定义
          </Badge>
        )}
        {agent?.description && (
          <span className="text-muted-foreground min-w-0 truncate text-xs">{agent.description}</span>
        )}
        {running && <LiveBadge />}
        <div className="text-muted-foreground ml-auto flex shrink-0 items-center gap-3 text-xs">
          {tokenTotal.turns > 0 && (
            <span title="agent 循环轮次（模型调用次数）" className="tabular-nums">
              {tokenTotal.turns} 轮
            </span>
          )}
          {tokenTotal.any && (
            <span title="input / cache(read) / output tokens" className="tabular-nums">
              input {fmtTokens(tokenTotal.i)} · cache {fmtTokens(tokenTotal.cr)} · output {fmtTokens(tokenTotal.o)}
            </span>
          )}
        </div>
      </div>

      {/* messages */}
      <ScrollArea
        type="auto"
        className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!"
      >
        <div className="min-w-0 max-w-full px-4 py-3" ref={contentRef}>
          {messages.length === 0 && !running ? (
            <div className="text-muted-foreground py-10 text-center text-sm">
              开始和「{agent?.name ?? conv.agent_key}」对话
            </div>
          ) : (
            <Transcript activity={messages} live={running} chat fetchDetail={fetchDetail} />
          )}
        </div>
      </ScrollArea>

      <Composer
        value={input}
        onChange={setInput}
        onSend={send}
        disabled={running}
        placeholder={running ? "Agent 正在回复…" : "输入消息，Enter 发送，Shift+Enter 换行"}
        running={running}
        onStop={stop}
        stopDisabled={stopping}
      />
      <LLMProfileRow
        profiles={profiles}
        selected={currentProfileId}
        onChange={changeProfile}
        disabled={running || sending}
        rightSlot={<TodoPopover seq={latestTodoSeq} fetchDetail={fetchDetail} />}
      />
    </>
  );
}

// ConversationItem is one row in the left rail — status-ish icon, title + agent
// subtitle, inline rename, hover delete. Modeled on the console's SessionItem.
function ConversationItem({
  conv,
  agent,
  active,
  renaming,
  renameText,
  onSelect,
  onStartRename,
  onRenameText,
  onCommitRename,
  onDelete,
}: {
  conv: Conversation;
  agent?: Agent;
  active: boolean;
  renaming: boolean;
  renameText: string;
  onSelect: () => void;
  onStartRename: () => void;
  onRenameText: (v: string) => void;
  onCommitRename: () => void;
  onDelete: () => void;
}) {
  return (
    <div
      className={cn(
        "group flex min-w-0 items-center gap-1 rounded-md pr-1 transition-colors",
        active ? "bg-accent text-accent-foreground" : "hover:bg-accent/50",
      )}
    >
      {renaming ? (
        <input
          autoFocus
          value={renameText}
          onChange={(e) => onRenameText(e.target.value)}
          onBlur={onCommitRename}
          onKeyDown={(e) => {
            if (e.key === "Enter") onCommitRename();
            if (e.key === "Escape") onCommitRename();
          }}
          className="border-input bg-background min-w-0 flex-1 rounded-md border px-2 py-1 text-sm"
        />
      ) : (
        <button
          type="button"
          onClick={onSelect}
          onDoubleClick={onStartRename}
          title="双击重命名"
          className="min-w-0 flex-1 rounded-md px-2 py-1.5 text-left"
        >
          <div className="truncate text-sm">{conv.title || "新对话"}</div>
          <div className="text-muted-foreground flex min-w-0 items-center gap-1 text-[11px]">
            <Bot className="size-3 shrink-0" />
            <span className="min-w-0 truncate">{agent?.name ?? conv.agent_key}</span>
            <span className="shrink-0">·</span>
            <span className="shrink-0">{new Date(conv.created_at).toLocaleDateString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}</span>
            <span className="shrink-0 opacity-60">#{conv.id}</span>
          </div>
        </button>
      )}
      <AlertDialog>
        <AlertDialogTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground hover:text-destructive shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
          >
            <Trash2Icon className="size-3.5" />
          </Button>
        </AlertDialogTrigger>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除对话「{conv.title || "新对话"}」？</AlertDialogTitle>
            <AlertDialogDescription>此操作不可撤销。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction onClick={onDelete}>删除</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

export default function ChatPage() {
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [convs, setConvs] = React.useState<Conversation[]>([]);
  const [selectedId, setSelectedId] = React.useState<number | null>(null);
  const [renamingId, setRenamingId] = React.useState<number | null>(null);
  const [renameText, setRenameText] = React.useState("");

  const reloadConvs = React.useCallback(() => {
    api.conversations().then(setConvs).catch(() => setConvs([]));
  }, []);
  React.useEffect(() => {
    api.agents().then(setAgents).catch(() => {});
    api.llmProfiles().then(setProfiles).catch(() => {});
    reloadConvs();
  }, [reloadConvs]);

  const selected = convs.find((c) => c.id === selectedId) ?? null;
  // conversation agents: custom agents + conversational built-ins (role=assistant,
  // e.g. Auto / 渗透测试). The orchestration built-ins (goals/planner/mainagent/worker)
  // are task-specific and stay hidden from the chat page.
  const chatAgents = agents.filter((a) => !a.builtin || a.role === "assistant");

  async function del(id: number) {
    try {
      await api.deleteConversation(id);
      if (selectedId === id) setSelectedId(null);
      reloadConvs();
    } catch (e) {
      toast.error("删除失败：" + (e as Error).message);
    }
  }

  function startRename(c: Conversation) {
    setRenamingId(c.id);
    setRenameText(c.title || "");
  }
  async function commitRename() {
    const id = renamingId;
    const title = renameText.trim();
    setRenamingId(null);
    if (!id || !title) return;
    try {
      await api.renameConversation(id, title);
      reloadConvs();
    } catch (e) {
      toast.error("重命名失败：" + (e as Error).message);
    }
  }

  return (
    <div
      data-content-padding="false"
      className="flex h-[calc(100svh-3rem)] flex-col overflow-hidden p-4 md:h-[calc(100svh-4rem)] md:p-6"
    >
      <div className="grid min-h-0 flex-1 grid-cols-[18rem_1fr] grid-rows-[minmax(0,1fr)] gap-4">
        {/* left: conversation list */}
        <div className="bg-card flex flex-col overflow-hidden rounded-lg border">
          <div className="border-b p-2">
            <Button size="sm" className="w-full" onClick={() => setSelectedId(null)}>
              <PlusIcon /> 新建对话
            </Button>
          </div>
          <ScrollArea type="auto" className="min-h-0 min-w-0 flex-1 [&_[data-slot=scroll-area-viewport]>div]:block!">
            <div className="flex min-w-0 flex-col gap-0.5 p-2">
              {convs.length === 0 && (
                <p className="text-muted-foreground px-2 py-6 text-center text-xs">暂无对话</p>
              )}
              {convs.map((c) => (
                <ConversationItem
                  key={c.id}
                  conv={c}
                  agent={agents.find((a) => a.key === c.agent_key)}
                  active={selectedId === c.id}
                  renaming={renamingId === c.id}
                  renameText={renameText}
                  onSelect={() => setSelectedId(c.id)}
                  onStartRename={() => startRename(c)}
                  onRenameText={setRenameText}
                  onCommitRename={commitRename}
                  onDelete={() => del(c.id)}
                />
              ))}
            </div>
          </ScrollArea>
        </div>

        {/* right: chat view */}
        <div className="bg-card flex min-w-0 flex-col overflow-hidden rounded-lg border">
          {selected ? (
            <ChatView
              key={selected.id}
              conv={selected}
              agents={agents}
              profiles={profiles}
              onTitleMaybeChanged={reloadConvs}
              onConvUpdated={reloadConvs}
            />
          ) : (
            <DraftChat
              agents={chatAgents}
              profiles={profiles}
              onStarted={(c) => {
                reloadConvs();
                setSelectedId(c.id);
              }}
            />
          )}
        </div>
      </div>
    </div>
  );
}
