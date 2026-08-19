"use client";

import * as React from "react";

import {
  Loader2Icon,
  PlugZapIcon,
  PlusIcon,
  RefreshCwIcon,
  RotateCcwIcon,
  SaveIcon,
  StarIcon,
  Trash2Icon,
  ZapIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { LLMPoolStatus, LLMProfile } from "@/lib/types";
import { cn } from "@/lib/utils";

// 思考强度档位（仅在开关打开时生效）。
const EFFORT_LEVELS: { value: string; label: string }[] = [
  { value: "low", label: "低" },
  { value: "medium", label: "中" },
  { value: "high", label: "高" },
  { value: "max", label: "最大" },
];
// 思考模式三态 ↔ 存库字符串 reasoning_effort：
//   none→""（thinking / reasoning_effort 两个字段都【不发送】，兼容不支持该字段的模型，如 MiniMax）
//   off →"off"（发送 thinking:{type:disabled}，用于默认开启思考、需要显式关闭的模型）
//   on  →强度值（发送 thinking:{enabled} + reasoning_effort）
type ThinkMode = "none" | "off" | "on";
const THINK_MODES: { value: ThinkMode; label: string }[] = [
  { value: "none", label: "不发送（默认）" },
  { value: "off", label: "显式关闭" },
  { value: "on", label: "开启" },
];
const toStore = (mode: ThinkMode, effort: string) => (mode === "on" ? effort : mode === "off" ? "off" : "");
const modeFromStore = (v?: string): ThinkMode => (!v ? "none" : v === "off" ? "off" : "on");
const effortFromStore = (v?: string) => (v && v !== "off" ? v : "high");

// 熔断状态 → 徽章样式。degraded 是「在失败但还没到熔断阈值」的中间态。
const STATE_BADGE: Record<string, { label: string; cls: string }> = {
  ok: { label: "正常", cls: "border-emerald-500/50 text-emerald-600 dark:text-emerald-400" },
  degraded: { label: "异常", cls: "border-amber-500/50 text-amber-600 dark:text-amber-400" },
  tripped: { label: "已熔断", cls: "border-destructive/50 text-destructive" },
};

function cooldownText(secs: number) {
  if (secs <= 0) return "";
  if (secs < 60) return `${secs}s`;
  return `${Math.ceil(secs / 60)}min`;
}

// PoolCard 是轮询功能的总控：主开关、绑定兜底开关，以及后端算出来的**实际**链路顺序。
// 顺序预览很关键——「激活 + 优先级 + 不参与轮询」三个因素叠加后，光看单张配置卡片
// 是猜不出真实顺序的。
function PoolCard({ refreshKey }: { refreshKey: number }) {
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [busy, setBusy] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is a prop — bumping it is exactly how the parent asks for a refetch after a profile edit
  React.useEffect(() => {
    void load();
  }, [load, refreshKey]);
  // 冷却倒计时是后端算的剩余秒数，定时拉一次让它走起来（只在有熔断时才轮询）。
  React.useEffect(() => {
    if (!pool?.enabled || !pool.chain.some((m) => m.state !== "ok")) return;
    const t = setInterval(() => void load(), 10_000);
    return () => clearInterval(t);
  }, [pool, load]);

  async function toggle(patch: { llm_pool_enabled?: boolean; llm_pool_bind_fallback?: boolean }) {
    if (busy) return;
    setBusy(true);
    try {
      await api.setSettings(patch);
      await load();
      if (patch.llm_pool_enabled !== undefined) {
        toast.success(patch.llm_pool_enabled ? "已开启 LLM 轮询" : "已关闭 LLM 轮询");
      } else {
        toast.success("已更新绑定兜底设置");
      }
    } catch (e) {
      toast.error(`设置失败：${(e as Error).message}`);
    } finally {
      setBusy(false);
    }
  }

  async function recover(id?: string) {
    try {
      setPool(await api.resetLLMPool(id));
      toast.success(id ? "已恢复该配置" : "已恢复全部配置");
    } catch (e) {
      toast.error(`恢复失败：${(e as Error).message}`);
    }
  }

  const enabled = pool?.enabled ?? false;
  // 链路顺序 = 后端返回的顺序；不参与轮询的配置仍会列出（灰显并标注），
  // 这样用户能看见「它为什么不在队列里」，而不是凭空消失。
  const chain = pool?.chain ?? [];
  const inChain = chain.filter((m) => m.active || !m.excluded);
  const tripped = chain.filter((m) => m.state === "tripped");

  return (
    <Card>
      <CardHeader className="has-data-[slot=card-action]:grid-cols-[1fr_auto]">
        <CardTitle className="flex items-center gap-2">
          <ZapIcon className="size-4" /> LLM 轮询 · 故障转移
        </CardTitle>
        <CardDescription>
          开启后，<b>未指定模型</b>的 Agent 在当前配置不可用（余额不足 / Key 失效 / 限流 /
          服务异常）时自动切换到下一个配置。 指定了模型的 Agent 与任务默认不参与轮询。
        </CardDescription>
        <CardAction className="self-center">
          <Switch
            checked={enabled}
            disabled={busy}
            onCheckedChange={(v) => void toggle({ llm_pool_enabled: v })}
            aria-label="LLM 轮询开关"
          />
        </CardAction>
      </CardHeader>
      {enabled && (
        <CardContent className="grid gap-4">
          <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
            <div className="grid gap-0.5">
              <Label className="text-sm">指定模型失败时也兜底</Label>
              <p className="text-muted-foreground text-xs">
                默认关闭：Agent 或任务指定了某个配置就只用它，失败即失败（不会悄悄换成别的模型）。
                开启后，指定的配置失败时也会回落到下面的轮询链。
              </p>
            </div>
            <Switch
              checked={pool?.bind_fallback ?? false}
              disabled={busy}
              onCheckedChange={(v) => void toggle({ llm_pool_bind_fallback: v })}
              aria-label="绑定配置失败兜底开关"
            />
          </div>

          <div className="grid gap-2">
            <div className="flex items-center justify-between">
              <Label className="text-sm">轮询顺序</Label>
              {tripped.length > 0 && (
                <Button size="sm" variant="ghost" onClick={() => void recover()}>
                  <RotateCcwIcon /> 全部恢复
                </Button>
              )}
            </div>
            {inChain.length < 2 && (
              <p className="text-muted-foreground text-xs">
                当前只有 {inChain.length} 个可用配置，轮询不会生效——至少需要 2 个已填 API Key 且参与轮询的配置。
              </p>
            )}
            <div className="grid gap-2">
              {chain.map((m) => {
                const badge = STATE_BADGE[m.state] ?? STATE_BADGE.ok;
                const order =
                  m.excluded && !m.active ? null : inChain.findIndex((x) => x.profile_id === m.profile_id) + 1;
                return (
                  <div
                    key={m.profile_id}
                    className={cn(
                      "flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border p-2.5 text-sm",
                      m.excluded && !m.active && "opacity-55",
                      m.state === "tripped" && "border-destructive/40",
                    )}
                  >
                    <span className="w-6 shrink-0 text-center font-mono text-muted-foreground text-xs">
                      {order ?? "—"}
                    </span>
                    <span className="font-medium">{m.name}</span>
                    <code className="truncate font-mono text-muted-foreground text-xs">{m.model}</code>
                    {m.active && (
                      <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                        激活
                      </Badge>
                    )}
                    {m.excluded && !m.active && <Badge variant="outline">不参与轮询</Badge>}
                    {!m.active && <span className="text-muted-foreground text-xs">优先级 {m.priority}</span>}
                    <div className="ml-auto flex items-center gap-2">
                      {m.state === "tripped" && m.cooldown_secs > 0 && (
                        <span className="text-muted-foreground text-xs">冷却 {cooldownText(m.cooldown_secs)}</span>
                      )}
                      {m.state === "degraded" && (
                        <span className="text-muted-foreground text-xs">连续失败 {m.fails} 次</span>
                      )}
                      <Badge variant="outline" className={badge.cls}>
                        {badge.label}
                      </Badge>
                      {m.state !== "ok" && (
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-7"
                          aria-label="立即恢复"
                          title="立即恢复：清除熔断，下次调用重试该配置"
                          onClick={() => void recover(m.profile_id)}
                        >
                          <RotateCcwIcon className="size-3.5" />
                        </Button>
                      )}
                    </div>
                    {m.last_error && (
                      <p className="w-full truncate font-mono text-muted-foreground text-xs" title={m.last_error}>
                        {m.last_error}
                      </p>
                    )}
                  </div>
                );
              })}
              {chain.length === 0 && (
                <div className="rounded-lg border border-dashed p-4 text-center text-muted-foreground text-sm">
                  暂无配置
                </div>
              )}
            </div>
            <p className="text-muted-foreground text-xs">
              激活配置恒为第 1 顺位，其余按优先级从高到低。某个配置失败后进入冷却（60s → 5min → 30min），
              冷却期内被跳过；恢复后自动切回。上下文窗口装不下当前请求的配置会被跳过。
            </p>
          </div>
        </CardContent>
      )}
    </Card>
  );
}

function NewProfileDialog({ onCreated }: { onCreated: (id: string) => void }) {
  const [open, setOpen] = React.useState(false);
  const [name, setName] = React.useState("");
  const [format, setFormat] = React.useState<"anthropic" | "openai">("anthropic");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0");
  const [thinkMode, setThinkMode] = React.useState<ThinkMode>("none");
  const [effort, setEffort] = React.useState("high");
  const [priority, setPriority] = React.useState("0");
  const [poolExclude, setPoolExclude] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  function reset() {
    setName("");
    setFormat("anthropic");
    setModel("");
    setBaseUrl("");
    setProxy("");
    setApiKey("");
    setRps("0");
    setRpm("0");
    setCw("0");
    setThinkMode("none");
    setEffort("high");
    setPriority("0");
    setPoolExclude(false);
    setModels([]);
    setModelsOpen(false);
  }

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`已加载 ${r.models.length} 个模型`);
      } else {
        toast.error(`加载模型失败：${r.error ?? "未获取到模型"}`);
      }
    } catch (e) {
      toast.error(`加载模型出错：${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function create() {
    if (!name.trim() || !model.trim()) {
      toast.error("请填写名称与模型");
      return;
    }
    try {
      const { id } = await api.saveLLMProfile({
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        reasoning_effort: toStore(thinkMode, effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
      });
      toast.success(`已新建 Profile：${name.trim()}（在列表中「设为激活」以启用）`);
      reset();
      setOpen(false);
      onCreated(String(id));
    } catch (e) {
      toast.error(`新建失败：${(e as Error).message}`);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        if (!o) reset();
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant="outline">
          <PlusIcon /> 新建
        </Button>
      </DialogTrigger>
      <DialogContent
        className="sm:max-w-lg"
        // don't dismiss a half-filled form on an outside click (Esc / ✕ still close).
        onInteractOutside={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle>新建 Profile</DialogTitle>
          <DialogDescription>新建后不会自动激活，请在列表中「设为激活」以启用。</DialogDescription>
        </DialogHeader>
        <div className="grid gap-4 py-2">
          <div className="grid gap-2">
            <Label htmlFor="np-name">名称</Label>
            <Input
              id="np-name"
              placeholder="例如：OpenAI 生产"
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label>格式</Label>
              <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai")}>
                <SelectTrigger>
                  <SelectValue placeholder="选择格式" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="anthropic">Anthropic</SelectItem>
                  <SelectItem value="openai">OpenAI</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="np-model">模型</Label>
              <div className="flex gap-2">
                <Input
                  id="np-model"
                  className="font-mono"
                  placeholder="claude-opus-4-8"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                />
                {/* modal: this Popover lives inside a Dialog, and its content is
                    portaled to <body> — outside the Dialog's react-remove-scroll
                    lock, which then cancels every wheel event over it (the list
                    rendered but refused to scroll). modal makes the Popover own
                    the topmost scroll lock, so its own list scrolls again. */}
                <Popover open={modelsOpen} onOpenChange={setModelsOpen} modal>
                  <PopoverTrigger asChild>
                    <Button
                      type="button"
                      variant="outline"
                      size="icon"
                      className="shrink-0"
                      disabled={loadingModels}
                      onClick={loadModels}
                      title="从 API 加载可用模型"
                    >
                      {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                    </Button>
                  </PopoverTrigger>
                  {models.length > 0 && (
                    <PopoverContent className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1" align="end">
                      {models.map((m) => (
                        <button
                          key={m}
                          type="button"
                          className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                          onClick={() => {
                            setModel(m);
                            setModelsOpen(false);
                          }}
                        >
                          {m}
                        </button>
                      ))}
                    </PopoverContent>
                  )}
                </Popover>
              </div>
            </div>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="np-base-url">Base URL（可选）</Label>
            <Input
              id="np-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="np-proxy">代理（可选）</Label>
            <Input
              id="np-proxy"
              className="font-mono"
              placeholder="http://127.0.0.1:8080 · socks5://127.0.0.1:1080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="np-api-key">API Key</Label>
            <Input
              id="np-api-key"
              type="password"
              placeholder="sk-…"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="np-rps">每秒限速</Label>
              <Input id="np-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="np-rpm">每分钟限速</Label>
              <Input id="np-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="np-cw">上下文窗口(K)</Label>
              <Input
                id="np-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="np-priority" className="text-sm">
                  轮询优先级
                </Label>
                <p className="text-muted-foreground text-xs">数字越大越先被选中；激活配置恒为第 1 顺位。</p>
              </div>
              <Input
                id="np-priority"
                type="number"
                className="w-24 shrink-0"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">不参与轮询</Label>
                <p className="text-muted-foreground text-xs">
                  开启后不会被当作故障转移目标（仍可被 Agent / 任务显式指定使用）。
                </p>
              </div>
              <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="不参与轮询" />
            </div>
          </div>
          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label className="text-sm">思考模式 · Extended Thinking</Label>
                <p className="text-muted-foreground text-xs">
                  不发送=不带思考字段（兼容 MiniMax 等不支持该字段的模型）；显式关闭=发
                  disabled（默认开思考的模型才需要）；开启=先推理再作答。
                </p>
              </div>
              <Select value={thinkMode} onValueChange={(v) => setThinkMode(v as ThinkMode)}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINK_MODES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {thinkMode === "on" && (
              <div className="flex items-center justify-between gap-4 border-t pt-3">
                <div className="grid gap-0.5">
                  <Label className="text-sm">思考强度</Label>
                  <p className="text-muted-foreground text-xs">推理投入档位，越高越深入。</p>
                </div>
                <Select value={effort} onValueChange={setEffort}>
                  <SelectTrigger className="w-28 shrink-0">
                    <SelectValue placeholder="强度" />
                  </SelectTrigger>
                  <SelectContent>
                    {EFFORT_LEVELS.map((o) => (
                      <SelectItem key={o.value} value={o.value}>
                        {o.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
          </div>
        </div>
        <DialogFooter>
          <DialogClose asChild>
            <Button variant="outline">取消</Button>
          </DialogClose>
          <Button onClick={create}>新建</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [format, setFormat] = React.useState<"anthropic" | "openai">("anthropic");
  const [name, setName] = React.useState("");
  const [model, setModel] = React.useState("");
  const [baseUrl, setBaseUrl] = React.useState("");
  const [proxy, setProxy] = React.useState("");
  const [apiKey, setApiKey] = React.useState("");
  const [keyHint, setKeyHint] = React.useState("");
  const [rps, setRps] = React.useState("0");
  const [rpm, setRpm] = React.useState("0");
  const [cw, setCw] = React.useState("0"); // 上下文窗口(K tokens);0=默认200K
  const [thinkMode, setThinkMode] = React.useState<ThinkMode>("none");
  const [effort, setEffort] = React.useState("high");
  const [priority, setPriority] = React.useState("0"); // 轮询顺位;越大越先
  const [poolExclude, setPoolExclude] = React.useState(false); // true=不作为故障转移目标
  // 任何 profile 的改动都可能改变轮询链的形状，用它触发 PoolCard 重新拉取。
  const [poolKey, setPoolKey] = React.useState(0);
  const [testing, setTesting] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const ps = await api.llmProfiles();
      setProfiles(ps);
      setPoolKey((k) => k + 1); // 链路顺序可能变了，让 PoolCard 重拉
      setSelectedId((prev) => {
        if (prev && ps.some((p) => p.id === prev)) return prev;
        return ps.find((p) => p.is_default)?.id ?? ps[0]?.id ?? null;
      });
    } catch {
      /* ignore */
    }
  }, []);
  React.useEffect(() => {
    void load();
  }, [load]);

  // Fill the editor from the selected profile. Runs only on explicit reloads /
  // selection changes (this page does not poll), so it never clobbers edits.
  const selected = React.useMemo(() => profiles.find((p) => p.id === selectedId) ?? null, [profiles, selectedId]);
  React.useEffect(() => {
    if (!selected) return;
    setName(selected.name);
    setFormat(selected.format === "anthropic" ? "anthropic" : "openai");
    setModel(selected.model);
    setBaseUrl(selected.base_url ?? "");
    setProxy(selected.proxy ?? "");
    setRps(String(selected.rate_per_second ?? 0));
    setRpm(String(selected.rate_per_minute ?? 0));
    setCw(String(selected.context_window_k ?? 0));
    setThinkMode(modeFromStore(selected.reasoning_effort));
    setEffort(effortFromStore(selected.reasoning_effort));
    setPriority(String(selected.priority ?? 0));
    setPoolExclude(selected.pool_exclude ?? false);
    setApiKey("");
    setKeyHint(selected.api_key_hint ?? "");
  }, [selected]);

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(format, baseUrl, apiKey, proxy, selectedId ? Number(selectedId) : undefined);
      if (r.ok && r.models && r.models.length > 0) {
        setModels(r.models);
        setModelsOpen(true);
        toast.success(`已加载 ${r.models.length} 个模型`);
      } else {
        toast.error(`加载模型失败：${r.error ?? "未获取到模型"}`);
      }
    } catch (e) {
      toast.error(`加载模型出错：${(e as Error).message}`);
    } finally {
      setLoadingModels(false);
    }
  }

  async function testConnection() {
    if (testing) return;
    setTesting(true);
    try {
      // test with the SAME thinking params the profile will run with, so an
      // unsupported reasoning field fails here instead of only at run time.
      // 传当前选中 profile 的 id：输入框留空时，后端用该 profile 已存的 key 测试。
      const r = await api.testLLM(
        format,
        model,
        baseUrl,
        apiKey,
        proxy,
        toStore(thinkMode, effort),
        selectedId ? Number(selectedId) : undefined,
      );
      if (r.ok) toast.success(`连接成功 · ${r.latency_ms ?? "?"}ms · ${r.model ?? model}`);
      else toast.error(`连接失败：${r.error ?? "未知"}`);
    } catch (e) {
      toast.error(`测试出错：${(e as Error).message}`);
    } finally {
      setTesting(false);
    }
  }

  async function save() {
    if (!selectedId) {
      toast.error("请先在左侧选择或新建一个 Profile");
      return;
    }
    if (!name.trim() || !model.trim()) {
      toast.error("请填写名称与模型");
      return;
    }
    try {
      await api.saveLLMProfile({
        id: Number(selectedId),
        name: name.trim(),
        format,
        model: model.trim(),
        base_url: baseUrl.trim(),
        proxy: proxy.trim(),
        api_key: apiKey,
        rate_per_second: Number(rps) || 0,
        rate_per_minute: Number(rpm) || 0,
        context_window_k: Number(cw) || 0,
        reasoning_effort: toStore(thinkMode, effort),
        priority: Number(priority) || 0,
        pool_exclude: poolExclude,
      });
      toast.success(selected?.is_default ? "已保存，激活配置即时生效，无需重启" : "已保存");
      setApiKey("");
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    }
  }

  async function activate(id: string) {
    try {
      await api.activateLLMProfile(id);
      const p = profiles.find((x) => x.id === id);
      toast.success(`已激活 Profile：${p?.name ?? id}`);
      await load();
    } catch (e) {
      toast.error(`激活失败：${(e as Error).message}`);
    }
  }

  async function remove(p: LLMProfile) {
    if (p.is_default) {
      toast.error("无法删除当前激活的 Profile");
      return;
    }
    try {
      await api.deleteLLMProfile(p.id);
      toast.success(`已删除 Profile：${p.name}`);
      await load();
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
    }
  }

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">LLM</h1>
        <p className="text-muted-foreground text-sm">全 Agent 共享的格式 / 模型 / 限速配置</p>
      </div>
      <div className="flex flex-1 flex-col gap-4 md:gap-6">
        <PoolCard refreshKey={poolKey} />
        <div className="grid grid-cols-1 gap-4 md:gap-6 lg:grid-cols-3">
          <Card className="lg:order-2 lg:col-span-2">
            <CardHeader>
              <CardTitle>
                {selected ? (
                  <span className="flex items-center gap-2">
                    编辑 Profile：{selected.name}
                    {selected.is_default && (
                      <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                        激活中
                      </Badge>
                    )}
                  </span>
                ) : (
                  "格式配置"
                )}
              </CardTitle>
              <CardDescription>
                {selected
                  ? "修改后点击「保存」。若为激活 Profile，保存后对全部 Agent 立即生效。"
                  : "从左侧选择一个 Profile 进行编辑，或点击「新建」。"}
              </CardDescription>
            </CardHeader>
            {selected ? (
              <CardContent className="grid gap-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="grid gap-2">
                    <Label htmlFor="name">名称</Label>
                    <Input
                      id="name"
                      placeholder="Profile 名称"
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label>格式</Label>
                    <Select value={format} onValueChange={(v) => setFormat(v as "anthropic" | "openai")}>
                      <SelectTrigger>
                        <SelectValue placeholder="选择格式" />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="anthropic">Anthropic</SelectItem>
                        <SelectItem value="openai">OpenAI</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="model">模型</Label>
                  <div className="flex gap-2">
                    <Input
                      id="model"
                      className="font-mono"
                      placeholder="claude-opus-4-8"
                      value={model}
                      onChange={(e) => setModel(e.target.value)}
                    />
                    <Popover open={modelsOpen} onOpenChange={setModelsOpen}>
                      <PopoverTrigger asChild>
                        <Button
                          type="button"
                          variant="outline"
                          size="icon"
                          className="shrink-0"
                          disabled={loadingModels}
                          onClick={loadModels}
                          title="从 API 加载可用模型"
                        >
                          {loadingModels ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                        </Button>
                      </PopoverTrigger>
                      {models.length > 0 && (
                        <PopoverContent
                          className="max-h-72 w-72 gap-0 overflow-y-auto overscroll-contain p-1"
                          align="end"
                        >
                          {models.map((m) => (
                            <button
                              key={m}
                              type="button"
                              className="w-full shrink-0 rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
                              onClick={() => {
                                setModel(m);
                                setModelsOpen(false);
                              }}
                            >
                              {m}
                            </button>
                          ))}
                        </PopoverContent>
                      )}
                    </Popover>
                  </div>
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="base-url">Base URL（可选）</Label>
                  <Input
                    id="base-url"
                    className="font-mono"
                    placeholder="https://api.openai.com/v1"
                    value={baseUrl}
                    onChange={(e) => setBaseUrl(e.target.value)}
                  />
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="proxy">代理（可选）</Label>
                  <Input
                    id="proxy"
                    className="font-mono"
                    placeholder="http://127.0.0.1:8080 · socks5://127.0.0.1:1080"
                    value={proxy}
                    onChange={(e) => setProxy(e.target.value)}
                  />
                  <p className="text-muted-foreground text-xs">
                    仅 LLM 出站请求走此代理，支持 http/https/socks5；留空则用环境变量（HTTP_PROXY/HTTPS_PROXY）。
                  </p>
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="api-key">API Key</Label>
                  <Input
                    id="api-key"
                    type="password"
                    placeholder={keyHint ? `已设置（${keyHint}），留空保持不变` : "sk-…"}
                    value={apiKey}
                    onChange={(e) => setApiKey(e.target.value)}
                  />
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <div className="grid gap-2">
                    <Label htmlFor="rps">每秒限速</Label>
                    <Input id="rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="rpm">每分钟限速</Label>
                    <Input id="rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
                    <p className="text-muted-foreground text-xs">0 = 不限</p>
                  </div>
                </div>

                <div className="grid gap-2">
                  <Label htmlFor="cw">上下文窗口（K tokens）</Label>
                  <Input
                    id="cw"
                    type="number"
                    min={0}
                    max={1000}
                    value={cw}
                    onChange={(e) => setCw(e.target.value)}
                    placeholder="200"
                  />
                  <p className="text-muted-foreground text-xs">
                    模型上下文窗口，用于压缩阈值。单位 K（千 token）；0/留空 = 默认 200K；上限 1000（即
                    1M）。设太高会导致压缩不触发。
                  </p>
                </div>

                <div className="grid gap-3 rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="grid gap-0.5">
                      <Label htmlFor="priority" className="text-sm">
                        轮询优先级
                      </Label>
                      <p className="text-muted-foreground text-xs">
                        数字越大越先被选中；激活配置恒为第 1
                        顺位，与本值无关。相同优先级的配置会轮流打头，天然分摊额度。
                      </p>
                    </div>
                    <Input
                      id="priority"
                      type="number"
                      className="w-24 shrink-0"
                      value={priority}
                      onChange={(e) => setPriority(e.target.value)}
                    />
                  </div>
                  <div className="flex items-center justify-between gap-4 border-t pt-3">
                    <div className="grid gap-0.5">
                      <Label className="text-sm">不参与轮询</Label>
                      <p className="text-muted-foreground text-xs">
                        开启后不会被当作故障转移目标（仍可被 Agent / 任务显式指定使用）。 适合「只给某个 Agent
                        专用、不希望别人失败时烧掉」的昂贵配置。
                      </p>
                    </div>
                    <Switch checked={poolExclude} onCheckedChange={setPoolExclude} aria-label="不参与轮询" />
                  </div>
                </div>

                <div className="grid gap-3 rounded-lg border p-3">
                  <div className="flex items-center justify-between gap-4">
                    <div className="grid gap-0.5">
                      <Label className="text-sm">思考模式 · Extended Thinking</Label>
                      <p className="text-muted-foreground text-xs">
                        不发送=不带思考字段（兼容 MiniMax 等不支持该字段的模型）；显式关闭=发
                        disabled（默认开思考的模型才需要）；开启=先推理再作答。
                      </p>
                    </div>
                    <Select value={thinkMode} onValueChange={(v) => setThinkMode(v as ThinkMode)}>
                      <SelectTrigger className="w-32 shrink-0">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {THINK_MODES.map((o) => (
                          <SelectItem key={o.value} value={o.value}>
                            {o.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  {thinkMode === "on" && (
                    <div className="flex items-center justify-between gap-4 border-t pt-3">
                      <div className="grid gap-0.5">
                        <Label className="text-sm">思考强度</Label>
                        <p className="text-muted-foreground text-xs">
                          推理投入档位，越高越深入（OpenAI reasoning_effort / Anthropic output_config.effort）。
                        </p>
                      </div>
                      <Select value={effort} onValueChange={setEffort}>
                        <SelectTrigger className="w-28 shrink-0">
                          <SelectValue placeholder="强度" />
                        </SelectTrigger>
                        <SelectContent>
                          {EFFORT_LEVELS.map((o) => (
                            <SelectItem key={o.value} value={o.value}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  )}
                </div>

                <Separator />
                <div className="flex flex-wrap gap-2">
                  <Button variant="outline" onClick={testConnection} disabled={testing}>
                    {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
                    {testing ? "测试中…" : "测试连接"}
                  </Button>
                  <Button onClick={save}>
                    <SaveIcon /> 保存
                  </Button>
                </div>
              </CardContent>
            ) : (
              <CardContent>
                <div className="flex items-center justify-center rounded-lg border border-dashed py-16 text-muted-foreground text-sm">
                  还没有 Profile，点击左侧「新建」创建第一个。
                </div>
              </CardContent>
            )}
          </Card>

          <Card className="lg:order-1">
            <CardHeader className="has-data-[slot=card-action]:grid-cols-[1fr_auto]">
              <CardTitle>Profiles</CardTitle>
              <CardDescription>点击选中以编辑；全 Agent 用同一激活 Profile，限速全局共享。</CardDescription>
              <CardAction className="self-center">
                <NewProfileDialog
                  onCreated={(id) => {
                    setSelectedId(id);
                    void load();
                  }}
                />
              </CardAction>
            </CardHeader>
            <CardContent className="grid gap-3">
              {profiles.map((p) => (
                // biome-ignore lint/a11y/useSemanticElements: row wraps its own action buttons, so a native <button> would nest buttons (invalid HTML)
                <div
                  key={p.id}
                  role="button"
                  tabIndex={0}
                  onClick={() => setSelectedId(p.id)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      setSelectedId(p.id);
                    }
                  }}
                  aria-pressed={p.id === selectedId}
                  className={cn(
                    "cursor-pointer rounded-lg border p-3 text-left outline-none transition-colors",
                    p.is_default && "border-amber-400/50 bg-amber-400/5",
                    p.id === selectedId ? "ring-2 ring-primary" : "hover:border-foreground/30",
                  )}
                >
                  <div className="flex items-start gap-2">
                    <StarIcon
                      className={cn(
                        "mt-0.5 size-4 shrink-0",
                        p.is_default ? "fill-amber-400 text-amber-400" : "text-muted-foreground",
                      )}
                    />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate font-medium text-sm">{p.name}</span>
                        <Badge variant="outline" className="uppercase">
                          {p.format}
                        </Badge>
                      </div>
                      <code className="mt-1 block truncate font-mono text-muted-foreground text-xs">{p.model}</code>
                      <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-muted-foreground text-xs">
                        <span>{p.api_key_hint}</span>
                        <span>
                          {p.rate_per_second}/s · {p.rate_per_minute}/min
                        </span>
                        {p.proxy && <span className="truncate">代理 {p.proxy}</span>}
                        {p.reasoning_effort && (
                          <span>思考 {p.reasoning_effort === "off" ? "关" : p.reasoning_effort}</span>
                        )}
                        {!p.is_default &&
                          (p.pool_exclude ? <span>不参与轮询</span> : <span>优先级 {p.priority ?? 0}</span>)}
                      </div>
                    </div>
                  </div>
                  <div className="mt-2 flex gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      className="flex-1"
                      disabled={p.is_default}
                      onClick={(e) => {
                        e.stopPropagation();
                        void activate(p.id);
                      }}
                    >
                      {p.is_default ? "已激活" : "设为激活"}
                    </Button>
                    <Button
                      size="icon"
                      variant="outline"
                      aria-label="删除 Profile"
                      onClick={(e) => {
                        e.stopPropagation();
                        void remove(p);
                      }}
                    >
                      <Trash2Icon className="text-destructive" />
                    </Button>
                  </div>
                </div>
              ))}
              {profiles.length === 0 && (
                <div className="rounded-lg border border-dashed p-6 text-center text-muted-foreground text-sm">
                  暂无 Profile
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
