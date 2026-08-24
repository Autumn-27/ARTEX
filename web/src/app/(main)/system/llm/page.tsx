"use client";

import * as React from "react";

import { Loader2Icon, PlugZapIcon, PlusIcon, RefreshCwIcon, SaveIcon, StarIcon, Trash2Icon } from "lucide-react";
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
import { api } from "@/lib/api";
import type { LLMProfile } from "@/lib/types";
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
                    <PopoverContent className="w-72 max-h-64 overflow-y-auto p-1" align="end">
                      {models.map((m) => (
                        <button
                          key={m}
                          type="button"
                          className="w-full rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
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
                <Label className="text-sm">思考模式 · Extended Thinking</Label>
                <p className="text-muted-foreground text-xs">
                  不发送=不带思考字段（兼容 MiniMax 等不支持该字段的模型）；显式关闭=发 disabled（默认开思考的模型才需要）；开启=先推理再作答。
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
  const [testing, setTesting] = React.useState(false);
  const [models, setModels] = React.useState<string[]>([]);
  const [loadingModels, setLoadingModels] = React.useState(false);
  const [modelsOpen, setModelsOpen] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const ps = await api.llmProfiles();
      setProfiles(ps);
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
    setApiKey("");
    setKeyHint(selected.api_key_hint ?? "");
  }, [selected]);

  async function loadModels() {
    if (loadingModels) return;
    setLoadingModels(true);
    setModels([]);
    try {
      const r = await api.fetchLLMModels(
        format,
        baseUrl,
        apiKey,
        proxy,
        selectedId ? Number(selectedId) : undefined,
      );
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
      });
      toast.success(selected?.is_default ? "已保存，激活配置即时生效，无需重启" : "已保存");
      setApiKey("");
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    }
  }

<<<<<<< Updated upstream
  async function activate(id: string) {
=======
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        className="flex flex-col gap-0 p-0 data-[side=right]:min-w-[420px] data-[side=right]:sm:max-w-xl"
        // 表单填到一半时点外面不该丢内容（Esc / ✕ 仍可关闭）。
        onInteractOutside={(e) => e.preventDefault()}
      >
        <SheetHeader className="px-4">
          <SheetTitle className="flex items-center gap-2">
            {isNew ? "新建模型配置" : `编辑：${profile?.name}`}
            {profile?.is_default && (
              <Badge variant="outline" className="border-amber-400/50 text-amber-500">
                激活中
              </Badge>
            )}
          </SheetTitle>
          <SheetDescription>
            {isNew
              ? "新建后不会自动激活，请在卡片上「设为激活」以启用。"
              : "修改后点击保存；激活配置保存后对全部 Agent 立即生效。"}
          </SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="p-name">名称</Label>
              <Input
                id="p-name"
                placeholder="例如：OpenAI 生产"
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
            <Label htmlFor="p-model">模型</Label>
            <div className="flex gap-2">
              <Input
                id="p-model"
                className="font-mono"
                placeholder="claude-opus-4-8"
                value={model}
                onChange={(e) => setModel(e.target.value)}
              />
              {/* modal: 这个 Popover 的内容被 portal 到 <body>，在 Sheet 的滚动锁之外，
                  不加 modal 时列表能渲染却滚不动。modal 让它自己持有最上层滚动锁。 */}
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

          <div className="grid gap-2">
            <Label htmlFor="p-base-url">Base URL（可选）</Label>
            <Input
              id="p-base-url"
              className="font-mono"
              placeholder="https://api.openai.com/v1"
              value={baseUrl}
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-proxy">代理（可选）</Label>
            <Input
              id="p-proxy"
              className="font-mono"
              placeholder="socks5://user:pass@127.0.0.1:1080 · http://127.0.0.1:8080"
              value={proxy}
              onChange={(e) => setProxy(e.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              仅 LLM 出站请求走此代理，支持 http/https/socks5，可带账号密码（如 socks5://user:pass@host:port，密码含特殊字符需 URL 编码）；留空表示不使用代理（直连）。
            </p>
          </div>

          <div className="grid gap-2">
            <Label htmlFor="p-api-key">API Key</Label>
            <Input
              id="p-api-key"
              type="password"
              placeholder={keyHint ? `已设置（${keyHint}），留空保持不变` : "sk-…"}
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-3">
            <div className="grid gap-2">
              <Label htmlFor="p-rps">每秒限速</Label>
              <Input id="p-rps" type="number" min={0} value={rps} onChange={(e) => setRps(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-rpm">每分钟限速</Label>
              <Input id="p-rpm" type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} />
            </div>
            <div className="grid gap-2">
              <Label htmlFor="p-cw">上下文窗口(K)</Label>
              <Input
                id="p-cw"
                type="number"
                min={0}
                max={1000}
                value={cw}
                onChange={(e) => setCw(e.target.value)}
                placeholder="200"
              />
            </div>
          </div>
          <p className="-mt-2 text-muted-foreground text-xs">
            限速 0 = 不限，全 Agent 共享。上下文窗口单位 K（千 token），0 = 默认 200K，上限 1000（即
            1M）；设太高会导致压缩不触发。
          </p>

          <div className="grid gap-3 rounded-lg border p-3">
            <div className="flex items-center justify-between gap-4">
              <div className="grid gap-0.5">
                <Label htmlFor="p-priority" className="text-sm">
                  轮询优先级
                </Label>
                <p className="text-muted-foreground text-xs">
                  数字越大越先被选中；激活配置恒为第 1 顺位，与本值无关。相同优先级的配置会轮流打头，天然分摊额度。
                </p>
              </div>
              <Input
                id="p-priority"
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
                <Label className="text-sm">思考开关 · thinking.type</Label>
                <p className="text-muted-foreground text-xs">
                  控制是否发送 thinking 字段。不发送=不带该字段（兼容 MiniMax 等不支持
                  的模型）；关闭=发 disabled；开启=发 enabled。与下面的强度互相独立。
                </p>
              </div>
              <Select value={thinkingType} onValueChange={setThinkingType}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {THINKING_TYPES.map((o) => (
                    <SelectItem key={o.value} value={o.value}>
                      {o.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center justify-between gap-4 border-t pt-3">
              <div className="grid gap-0.5">
                <Label className="text-sm">思考强度 · reasoning_effort</Label>
                <p className="text-muted-foreground text-xs">
                  独立的强度档位（OpenAI reasoning_effort / Anthropic output_config.effort）。
                  有些接口没有 thinking 字段、只靠强度即可激活思考，故可单独设置、不发送思考开关。
                </p>
              </div>
              <Select value={effort} onValueChange={setEffort}>
                <SelectTrigger className="w-32 shrink-0">
                  <SelectValue />
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
          </div>
        </div>

        <div className="flex gap-2 border-t px-4 py-3">
          <Button variant="outline" onClick={testConnection} disabled={testing}>
            {testing ? <Loader2Icon className="animate-spin" /> : <PlugZapIcon />}
            {testing ? "测试中…" : "测试连接"}
          </Button>
          <Button onClick={save} disabled={saving} className="flex-1">
            {saving && <Loader2Icon className="animate-spin" />}
            {!saving && (isNew ? <PlusIcon /> : <SaveIcon />)}
            {isNew ? "新建" : "保存"}
          </Button>
        </div>
      </SheetContent>
    </Sheet>
  );
}

// ─────────────────────────────────────────────────────────────────────────────

export default function LLMPage() {
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [pool, setPool] = React.useState<LLMPoolStatus | null>(null);
  const [poolOpen, setPoolOpen] = React.useState(false);
  // 抽屉的开关和内容分开存：关闭时 editing 保持不变，否则关闭动画期间标题会从
  // 「编辑 X」闪成「新建」。editing = null 表示新建。
  const [editOpen, setEditOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<LLMProfile | null>(null);
  const openEditor = React.useCallback((p: LLMProfile | null) => {
    setEditing(p);
    setEditOpen(true);
  }, []);

  const loadPool = React.useCallback(async () => {
    try {
      setPool(await api.llmPool());
    } catch {
      /* ignore */
    }
  }, []);

  const load = React.useCallback(async () => {
    try {
      setProfiles(await api.llmProfiles());
    } catch {
      /* ignore */
    }
    await loadPool();
  }, [loadPool]);

  React.useEffect(() => {
    void load();
  }, [load]);

  // 卡片上的健康徽章按 profile id 取轮询状态。
  const health = React.useMemo(() => {
    const m = new Map<string, LLMPoolMember>();
    for (const c of pool?.chain ?? []) m.set(c.profile_id, c);
    return m;
  }, [pool]);

  async function activate(id: string, name: string) {
>>>>>>> Stashed changes
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
                        <PopoverContent className="w-72 max-h-64 overflow-y-auto p-1" align="end">
                          {models.map((m) => (
                            <button
                              key={m}
                              type="button"
                              className="w-full rounded-md px-2 py-1.5 text-left font-mono text-xs hover:bg-accent hover:text-accent-foreground"
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
                      <Label className="text-sm">思考模式 · Extended Thinking</Label>
                      <p className="text-muted-foreground text-xs">
                        不发送=不带思考字段（兼容 MiniMax 等不支持该字段的模型）；显式关闭=发 disabled（默认开思考的模型才需要）；开启=先推理再作答。
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
