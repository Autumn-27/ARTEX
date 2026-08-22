"use client";

import * as React from "react";

import { CpuIcon, KeyboardIcon, RadioTowerIcon, SearchIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import {
  CHAT_SEND_MODE_OPTIONS,
  type ChatSendMode,
  setChatSendMode,
  useChatSendMode,
} from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

export default function SystemSettingsPage() {
  const [trafficCapture, setTrafficCapture] = React.useState(false);
  const [webSearch, setWebSearch] = React.useState(false);
  const [backend, setBackend] = React.useState("ddgs");
  const [braveKeySet, setBraveKeySet] = React.useState(false);
  const [braveKeyInput, setBraveKeyInput] = React.useState("");
  const [tavilyKeySet, setTavilyKeySet] = React.useState(false);
  const [tavilyKeyInput, setTavilyKeyInput] = React.useState("");
  const [savingTavilyKey, setSavingTavilyKey] = React.useState(false);
  const [proxyInput, setProxyInput] = React.useState("");
  const [savingProxy, setSavingProxy] = React.useState(false);
  const [testing, setTesting] = React.useState(false);
  const [loaded, setLoaded] = React.useState(false);
  const [saving, setSaving] = React.useState(false);
  const [savingKey, setSavingKey] = React.useState(false);
  const [pyInterp, setPyInterp] = React.useState("");
  const [workers, setWorkers] = React.useState("3");
  const [savingWorkers, setSavingWorkers] = React.useState(false);
  // 纯前端偏好：不走 /api/settings，直接读写 localStorage。
  const sendMode = useChatSendMode();

  const apply = React.useCallback((s: Settings) => {
    setTrafficCapture(!!s.traffic_capture);
    setWebSearch(!!s.web_search_enabled);
    setBackend(s.web_search_backend || "ddgs");
    setBraveKeySet(!!s.brave_key_set);
    setTavilyKeySet(!!s.tavily_key_set);
    setProxyInput(s.web_search_proxy || "");
    setPyInterp(s.python_interpreter || "");
    setWorkers(String(s.workers ?? 3));
  }, []);

  const saveWorkers = () => {
    const n = Number(workers);
    if (!Number.isInteger(n) || n <= 0) {
      toast.error("并发数必须是大于 0 的整数");
      return;
    }
    setSavingWorkers(true);
    api
      .setSettings({ workers: n })
      .then((s) => {
        apply(s);
        toast.success("已保存并发工作 agent 数（对之后启动的任务生效）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingWorkers(false));
  };

  const savePython = () => {
    setSaving(true);
    api
      .setSettings({ python_interpreter: pyInterp.trim() })
      .then((s) => {
        apply(s);
        toast.success("已保存 Python 解释器配置");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSaving(false));
  };
  const detectPython = () => {
    setSaving(true);
    api.detectPython().then((r) => setPyInterp(r.python_interpreter)).catch(() => undefined).finally(() => setSaving(false));
  };

  React.useEffect(() => {
    api
      .settings()
      .then(apply)
      .catch(() => undefined)
      .finally(() => setLoaded(true));
  }, [apply]);

  const toggleTraffic = (v: boolean) => {
    setTrafficCapture(v); // optimistic
    setSaving(true);
    api
      .setSettings({ traffic_capture: v })
      .then(apply)
      .catch(() => setTrafficCapture(!v)) // revert on failure
      .finally(() => setSaving(false));
  };

  // Persist a web-search patch (enable and/or backend). Optimistic with refetch.
  const saveWebSearch = (patch: Partial<Settings>) => {
    setSaving(true);
    api
      .setSettings(patch)
      .then((s) => {
        apply(s);
        toast.success("已保存网络搜索配置");
      })
      .catch((e) => {
        toast.error("保存失败：" + (e as Error).message);
        api
          .settings()
          .then(apply)
          .catch(() => undefined);
      })
      .finally(() => setSaving(false));
  };

  const saveBraveKey = () => {
    setSavingKey(true);
    api
      .setSettings({ brave_search_api_key: braveKeyInput })
      .then((s) => {
        apply(s);
        setBraveKeyInput("");
        toast.success("已保存 Brave API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingKey(false));
  };

  const saveTavilyKey = () => {
    setSavingTavilyKey(true);
    api
      .setSettings({ tavily_search_api_key: tavilyKeyInput })
      .then((s) => {
        apply(s);
        setTavilyKeyInput("");
        toast.success("已保存 Tavily API Key");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingTavilyKey(false));
  };

  const saveProxy = () => {
    setSavingProxy(true);
    api
      .setSettings({ web_search_proxy: proxyInput.trim() })
      .then((s) => {
        apply(s);
        toast.success(proxyInput.trim() ? "已保存出口代理" : "已清除出口代理（改为直连）");
      })
      .catch((e) => toast.error("保存失败：" + (e as Error).message))
      .finally(() => setSavingProxy(false));
  };

  // Run a real "test" search ("test") against the CURRENT form values (backend +
  // proxy + entered key), falling back to saved values server-side. Toasts result.
  const runTest = () => {
    setTesting(true);
    api
      .testWebSearch({
        web_search_backend: backend,
        web_search_proxy: proxyInput.trim(),
        brave_search_api_key: braveKeyInput,
        tavily_search_api_key: tavilyKeyInput,
      })
      .then((r) => {
        if (r.ok) toast.success(`搜索测试成功 · ${r.backend} 返回 ${r.count} 条结果`);
        else toast.error("搜索测试失败：" + (r.error || "未知错误"));
      })
      .catch((e) => toast.error("搜索测试失败：" + (e as Error).message))
      .finally(() => setTesting(false));
  };

  // brave-free selected but no key stored and none being entered → tool stays off.
  const braveNeedsKey = webSearch && backend === "brave-free" && !braveKeySet;

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">系统配置</h1>
        <p className="text-muted-foreground text-sm">全局运行时开关</p>
      </div>

      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <RadioTowerIcon className="size-4" />
            流量捕获
          </CardTitle>
          <CardDescription>
            开启后，所有 Agent 的 HTTP 流量经记录代理全量落库，并向 Agent 注入 traffic_search / traffic_get
            工具与代理配置（提示词含代理说明）。
            <br />
            关闭（默认）时不记录任何流量：Agent
            <b>不会</b>拿到代理配置与流量工具，提示词也<b>不含</b>代理相关内容。切换后会即时重建 Agent 生效。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center justify-between gap-4">
          <Label htmlFor="traffic-capture" className="text-sm font-normal text-muted-foreground">
            {trafficCapture ? "已开启 · 正在记录流量并注入代理" : "已关闭 · 不记录、不注入代理"}
          </Label>
          <Switch
            id="traffic-capture"
            checked={trafficCapture}
            disabled={!loaded || saving}
            onCheckedChange={toggleTraffic}
          />
        </CardContent>
      </Card>

      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <SearchIcon className="size-4" />
            网络搜索
          </CardTitle>
          <CardDescription>
            这是网络搜索的<b>总开关 + 来源配置</b>。开启后，才能在<b>每个 Agent 的配置</b>里单独选择是否启用
            <b>web_search</b>（仅返回标题/链接/摘要，不抓取正文；抓取由 WebFetch 负责）。网络搜索<b>不走</b>记录代理，独立于流量捕获。
            <br />
            来源可选 <b>DuckDuckGo（ddgs）</b>（无需 Key）、<b>Brave（免费版）</b>（需填写 Brave API Key）或 <b>Tavily</b>（需填写 Tavily API Key）。总开关关闭时，各 Agent 的网络搜索开关不可用。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="web-search" className="text-sm font-normal text-muted-foreground">
              {webSearch ? "总开关已开启 · 可在各 Agent 配置里单独启用" : "已关闭 · 各 Agent 无法启用网络搜索"}
            </Label>
            <Switch
              id="web-search"
              checked={webSearch}
              disabled={!loaded || saving}
              onCheckedChange={(v) => {
                setWebSearch(v); // optimistic
                saveWebSearch({ web_search_enabled: v });
              }}
            />
          </div>

          {webSearch && (
            <div className="flex items-center justify-between gap-4">
              <Label className="text-sm font-normal text-muted-foreground">搜索来源</Label>
              <Select
                value={backend}
                disabled={!loaded || saving}
                onValueChange={(v) => {
                  setBackend(v); // optimistic
                  saveWebSearch({ web_search_backend: v });
                }}
              >
                <SelectTrigger className="w-48 shrink-0">
                  <SelectValue placeholder="选择来源" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ddgs">DuckDuckGo（ddgs · 免费无 Key）</SelectItem>
                  <SelectItem value="brave-free">Brave（免费版 · 需 Key）</SelectItem>
                  <SelectItem value="tavily">Tavily（需 Key）</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}

          {webSearch && backend === "brave-free" && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="brave-key" className="text-sm font-normal text-muted-foreground">
                Brave Search API Key
                {braveKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
              </Label>
              <div className="flex items-center gap-2">
                <Input
                  id="brave-key"
                  type="password"
                  autoComplete="off"
                  placeholder={braveKeySet ? "已配置（留空则不变）" : "输入 Brave API Key"}
                  value={braveKeyInput}
                  disabled={!loaded || savingKey}
                  onChange={(e) => setBraveKeyInput(e.target.value)}
                />
                <Button
                  type="button"
                  onClick={saveBraveKey}
                  disabled={!loaded || savingKey || braveKeyInput.trim() === ""}
                >
                  保存
                </Button>
              </div>
              {braveNeedsKey && (
                <p className="text-xs text-amber-500">
                  已选择 Brave 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                </p>
              )}
              <p className="text-muted-foreground text-xs">
                免费版额度约 2,000 次/月。前往 https://brave.com/search/api/ 获取 Key。
              </p>
            </div>
          )}

          {webSearch && backend === "tavily" && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="tavily-key" className="text-sm font-normal text-muted-foreground">
                Tavily Search API Key
                {tavilyKeySet && <span className="ml-2 text-xs text-emerald-500">已配置</span>}
              </Label>
              <div className="flex items-center gap-2">
                <Input
                  id="tavily-key"
                  type="password"
                  autoComplete="off"
                  placeholder={tavilyKeySet ? "已配置（留空则不变）" : "输入 Tavily API Key（tvly-…）"}
                  value={tavilyKeyInput}
                  disabled={!loaded || savingTavilyKey}
                  onChange={(e) => setTavilyKeyInput(e.target.value)}
                />
                <Button
                  type="button"
                  onClick={saveTavilyKey}
                  disabled={!loaded || savingTavilyKey || tavilyKeyInput.trim() === ""}
                >
                  保存
                </Button>
              </div>
              {webSearch && backend === "tavily" && !tavilyKeySet && (
                <p className="text-xs text-amber-500">
                  已选择 Tavily 但尚未配置 Key —— 在保存 Key 之前，搜索工具不会启用。
                </p>
              )}
              <p className="text-muted-foreground text-xs">
                前往 https://tavily.com 注册并获取 API Key。
              </p>
            </div>
          )}

          {webSearch && (
            <div className="flex flex-col gap-2">
              <Label htmlFor="ws-proxy" className="text-sm font-normal text-muted-foreground">
                出口代理（可选）
              </Label>
              <div className="flex items-center gap-2">
                <Input
                  id="ws-proxy"
                  autoComplete="off"
                  placeholder="http://host:port 或 socks5://host:port（留空=直连）"
                  value={proxyInput}
                  disabled={!loaded || savingProxy}
                  onChange={(e) => setProxyInput(e.target.value)}
                />
                <Button type="button" onClick={saveProxy} disabled={!loaded || savingProxy}>
                  保存
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">
                独立出口代理，仅用于访问搜索端点（VPN/SOCKS 等）。与记录流量的 MITM 代理无关；网络不通时经此代理访问。
              </p>
            </div>
          )}

          {webSearch && (
            <div className="flex items-center justify-between gap-4 border-t pt-4">
              <p className="text-muted-foreground text-xs">
                用当前配置（来源 + 代理 + Key）实际搜索一次「test」，验证是否可用。
              </p>
              <Button type="button" variant="outline" onClick={runTest} disabled={!loaded || testing} className="shrink-0">
                {testing ? "测试中…" : "测试搜索"}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <RadioTowerIcon className="size-4" />
            自定义脚本 · Python 解释器
          </CardTitle>
          <CardDescription>
            自定义 <b>script</b> 类型工具用它跑 Python。开机会自动检测（python3 优先）；此处可手填 venv /
            特定版本的绝对路径，留空则运行时自动检测。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <Input
              className="font-mono text-sm"
              placeholder="/usr/bin/python3（留空=自动检测）"
              value={pyInterp}
              disabled={!loaded || saving}
              onChange={(e) => setPyInterp(e.target.value)}
            />
            <Button variant="outline" onClick={detectPython} disabled={!loaded || saving}>
              重新检测
            </Button>
            <Button onClick={savePython} disabled={!loaded || saving}>
              保存
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <CpuIcon className="size-4" />
            工作并发 · Work Agent 数
          </CardTitle>
          <CardDescription>
            每个任务并发运行的工作 agent 数量（默认 3）。数值越大并发探测越多、消耗也越高。修改后<b>对之后启动的任务生效</b>，正在运行的任务不受影响。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <div className="flex items-center gap-2">
            <Input
              type="number"
              min={1}
              className="w-32 font-mono text-sm"
              placeholder="3"
              value={workers}
              disabled={!loaded || savingWorkers}
              onChange={(e) => setWorkers(e.target.value)}
            />
            <Button onClick={saveWorkers} disabled={!loaded || savingWorkers}>
              保存
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base">
            <KeyboardIcon className="size-4" />
            会话输入框发送键位
          </CardTitle>
          <CardDescription>
            对话页与任务详情的主 Agent 会话输入框共用此设置，选择后立即生效、无需保存。
            <br />
            该偏好<b>只存在本浏览器</b>，不随账号同步，换浏览器或清理站点数据后需重新设置。
          </CardDescription>
        </CardHeader>
        <CardContent className="flex items-center justify-between gap-4">
          <Label htmlFor="chat-send-mode" className="text-sm font-normal text-muted-foreground">
            发送方式
          </Label>
          <Select value={sendMode} onValueChange={(v) => setChatSendMode(v as ChatSendMode)}>
            <SelectTrigger id="chat-send-mode" className="w-72">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {CHAT_SEND_MODE_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </CardContent>
      </Card>
    </div>
  );
}
