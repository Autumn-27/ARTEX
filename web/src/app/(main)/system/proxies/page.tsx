"use client";

import * as React from "react";

import {
  CircleCheckIcon,
  CircleXIcon,
  DownloadIcon,
  Loader2Icon,
  PencilIcon,
  PlusIcon,
  RefreshCwIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { ProxyInput, ProxyNode, ProxyProtocol, ProxySource } from "@/lib/types";

const PROTOCOLS: ProxyProtocol[] = ["http", "https", "socks5"];

function emptyInput(): ProxyInput {
  return { protocol: "http", host: "", port: 8080, enabled: true, tags: [] };
}

// 健康徽标：健康显示延迟，不健康显示原因，未检测显示灰点。
function HealthBadge({ p }: { p: ProxyNode }) {
  if (p.check_count === 0) {
    return <Badge variant="outline">未检测</Badge>;
  }
  if (p.healthy) {
    return (
      <Badge variant="outline" className="text-emerald-600 dark:text-emerald-400">
        <CircleCheckIcon className="size-3" /> {p.latency_ms}ms
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="text-red-600 dark:text-red-400" title={p.last_error}>
      <CircleXIcon className="size-3" /> 失败
    </Badge>
  );
}

export default function ProxyPoolPage() {
  const [proxies, setProxies] = React.useState<ProxyNode[]>([]);
  const [total, setTotal] = React.useState(0);
  const [page, setPage] = React.useState(1);
  const [pageSize, setPageSize] = React.useState(20);
  const [sources, setSources] = React.useState<ProxySource[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [checking, setChecking] = React.useState<Set<number>>(new Set());
  const [fetching, setFetching] = React.useState<Set<string>>(new Set());
  const [editing, setEditing] = React.useState<ProxyNode | null>(null);
  const [form, setForm] = React.useState<ProxyInput | null>(null);
  const [importOpen, setImportOpen] = React.useState(false);
  const [importText, setImportText] = React.useState("");
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const [px, srcs] = await Promise.all([api.proxies({ page, limit: pageSize }), api.proxySources()]);
      setProxies(px.proxies);
      setTotal(px.total);
      setSources(srcs);
    } catch (e) {
      toast.error(`加载失败：${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, [page, pageSize]);

  React.useEffect(() => {
    void load();
  }, [load]);

  const openCreate = () => {
    setEditing(null);
    setForm(emptyInput());
  };
  const openEdit = (p: ProxyNode) => {
    setEditing(p);
    setForm({
      protocol: p.protocol,
      host: p.host,
      port: p.port,
      username: p.username ?? "",
      password: "", // 留空 = 不改（后端已打码）
      anonymity: p.anonymity ?? "",
      region: p.region ?? "",
      tags: p.tags,
      label: p.label ?? "",
      enabled: p.enabled,
    });
  };

  const save = async () => {
    if (!form) return;
    if (!form.host.trim() || form.port <= 0) {
      toast.error("请填写 host 和有效端口");
      return;
    }
    setSaving(true);
    try {
      if (editing) {
        await api.updateProxy(editing.id, form);
        toast.success("已保存");
      } else {
        await api.createProxy(form);
        toast.success("已添加");
      }
      setForm(null);
      setEditing(null);
      await load();
    } catch (e) {
      toast.error(`保存失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const remove = async (p: ProxyNode) => {
    setProxies((prev) => prev.filter((x) => x.id !== p.id));
    try {
      await api.deleteProxy(p.id);
      await load(); // 重拉当前页：修正 total 并从后页补位
    } catch (e) {
      toast.error(`删除失败：${(e as Error).message}`);
      await load();
    }
  };

  const toggleEnabled = async (p: ProxyNode, enabled: boolean) => {
    setProxies((prev) => prev.map((x) => (x.id === p.id ? { ...x, enabled } : x)));
    try {
      await api.updateProxy(p.id, {
        protocol: p.protocol,
        host: p.host,
        port: p.port,
        username: p.username,
        anonymity: p.anonymity,
        region: p.region,
        tags: p.tags,
        label: p.label,
        enabled,
      });
    } catch {
      await load();
    }
  };

  const check = async (p: ProxyNode) => {
    setChecking((prev) => new Set(prev).add(p.id));
    try {
      const res = await api.checkProxy(p.id);
      toast[res.ok ? "success" : "error"](res.ok ? `可用 · ${res.latency_ms}ms` : `不可用：${res.error}`);
      await load();
    } catch (e) {
      toast.error(`探活失败：${(e as Error).message}`);
    } finally {
      setChecking((prev) => {
        const next = new Set(prev);
        next.delete(p.id);
        return next;
      });
    }
  };

  const doImport = async () => {
    setSaving(true);
    try {
      const res = await api.importProxies(importText);
      toast.success(`已导入 ${res.added} 个${res.invalid.length ? `，${res.invalid.length} 行无法解析` : ""}`);
      setImportOpen(false);
      setImportText("");
      await load();
    } catch (e) {
      toast.error(`导入失败：${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  };

  const fetchSource = async (name: string) => {
    setFetching((prev) => new Set(prev).add(name));
    try {
      const res = await api.fetchProxySource(name);
      toast.success(`抓取 ${res.fetched} 个，新增 ${res.added} 个`);
      await load();
    } catch (e) {
      toast.error(`抓取失败：${(e as Error).message}`);
    } finally {
      setFetching((prev) => {
        const next = new Set(prev);
        next.delete(name);
        return next;
      });
    }
  };

  const toggleSource = async (name: string, enabled: boolean) => {
    setSources((prev) => prev.map((s) => (s.name === name ? { ...s, enabled } : s)));
    try {
      await api.setProxySource(name, enabled);
    } catch (e) {
      toast.error(`切换失败：${(e as Error).message}`);
      await load();
    }
  };

  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="text-xl font-semibold tracking-tight">代理池</h1>
        <p className="text-muted-foreground text-sm">
          出口代理管理：手动添加/导入可信代理，或开启免费代理源（默认全关）。后台定时验活，选择时优先稳定节点。
        </p>
      </div>

      {/* 免费代理源开关 */}
      <Card>
        <CardHeader>
          <CardTitle className="text-base">免费代理源</CardTitle>
          <CardDescription>
            默认全部关闭。免费代理不可信、易被目标封禁——出口默认只走手动/导入的可信代理（见系统配置的安全阀门）。
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-2 sm:grid-cols-2">
          {sources.map((s) => (
            <div key={s.name} className="flex items-center justify-between gap-3 rounded-md border px-3 py-2">
              <div className="min-w-0">
                <div className="truncate text-sm font-medium">{s.name}</div>
                <div className="text-muted-foreground text-xs">
                  {s.last_fetch_at
                    ? `上次抓取 ${s.last_count} 个${s.last_error ? ` · 错误：${s.last_error}` : ""}`
                    : "尚未抓取"}
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-1">
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label="立即抓取"
                  title="立即抓取一次"
                  disabled={fetching.has(s.name)}
                  onClick={() => fetchSource(s.name)}
                >
                  {fetching.has(s.name) ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                </Button>
                <Switch checked={s.enabled} onCheckedChange={(v) => toggleSource(s.name, v)} />
              </div>
            </div>
          ))}
        </CardContent>
      </Card>

      {/* 代理列表 */}
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle className="text-base">代理节点</CardTitle>
            <CardDescription>共 {total} 个</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setImportOpen(true)}>
              <DownloadIcon /> 批量导入
            </Button>
            <Button size="sm" onClick={openCreate}>
              <PlusIcon /> 添加代理
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          {loading && (
            <div className="text-muted-foreground flex items-center gap-2 py-8 text-sm">
              <Loader2Icon className="size-4 animate-spin" /> 加载中…
            </div>
          )}
          {!loading && proxies.length === 0 && (
            <div className="text-muted-foreground py-8 text-center text-sm">
              还没有代理，添加或导入，或开启上方免费源。
            </div>
          )}
          {!loading && proxies.length > 0 && (
            <div className="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>地址</TableHead>
                    <TableHead>协议</TableHead>
                    <TableHead>地区</TableHead>
                    <TableHead>来源</TableHead>
                    <TableHead>健康</TableHead>
                    <TableHead>启用</TableHead>
                    <TableHead className="text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {proxies.map((p) => (
                    <TableRow key={p.id}>
                      <TableCell className="font-mono text-xs">
                        {p.host}:{p.port}
                        {p.label ? <span className="text-muted-foreground ml-2">{p.label}</span> : null}
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline">{p.protocol}</Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground text-xs">{p.region || "—"}</TableCell>
                      <TableCell>
                        {p.trusted ? (
                          <Badge variant="outline" className="text-emerald-600 dark:text-emerald-400">
                            可信
                          </Badge>
                        ) : (
                          <Badge variant="outline" className="text-amber-600 dark:text-amber-400" title={p.source}>
                            免费源
                          </Badge>
                        )}
                      </TableCell>
                      <TableCell>
                        <HealthBadge p={p} />
                      </TableCell>
                      <TableCell>
                        <Switch checked={p.enabled} onCheckedChange={(v) => toggleEnabled(p, v)} />
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-0.5">
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label="探活"
                            title="探活"
                            disabled={checking.has(p.id)}
                            onClick={() => check(p)}
                          >
                            {checking.has(p.id) ? <Loader2Icon className="animate-spin" /> : <RefreshCwIcon />}
                          </Button>
                          <Button
                            size="icon"
                            variant="ghost"
                            aria-label="编辑"
                            title="编辑"
                            onClick={() => openEdit(p)}
                          >
                            <PencilIcon />
                          </Button>
                          <Button size="icon" variant="ghost" aria-label="删除" title="删除" onClick={() => remove(p)}>
                            <Trash2Icon className="text-destructive" />
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
          {!loading && total > 0 && (
            <div className="mt-3">
              <TablePagination
                page={page}
                pageSize={pageSize}
                total={total}
                onPageChange={setPage}
                onPageSizeChange={(s) => {
                  setPageSize(s);
                  setPage(1);
                }}
              />
            </div>
          )}
        </CardContent>
      </Card>

      {/* 新增/编辑 dialog */}
      <Dialog open={form !== null} onOpenChange={(o) => !o && setForm(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editing ? "编辑代理" : "添加代理"}</DialogTitle>
            <DialogDescription>手动添加的代理标记为可信，可进入主出口轮换。</DialogDescription>
          </DialogHeader>
          {form && (
            <div className="grid gap-3">
              <div className="grid grid-cols-[8rem_1fr] gap-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="p-proto">协议</Label>
                  <NativeSelect
                    id="p-proto"
                    value={form.protocol}
                    onChange={(e) => setForm({ ...form, protocol: e.target.value as ProxyProtocol })}
                  >
                    {PROTOCOLS.map((p) => (
                      <NativeSelectOption key={p} value={p}>
                        {p}
                      </NativeSelectOption>
                    ))}
                  </NativeSelect>
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="p-host">Host</Label>
                  <Input
                    id="p-host"
                    value={form.host}
                    placeholder="1.2.3.4"
                    onChange={(e) => setForm({ ...form, host: e.target.value })}
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="p-port">端口</Label>
                  <Input
                    id="p-port"
                    type="number"
                    value={form.port || ""}
                    onChange={(e) => setForm({ ...form, port: Number(e.target.value) })}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="p-region">地区（可选）</Label>
                  <Input
                    id="p-region"
                    value={form.region ?? ""}
                    placeholder="CN"
                    onChange={(e) => setForm({ ...form, region: e.target.value })}
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-2">
                <div className="grid gap-1.5">
                  <Label htmlFor="p-user">用户名（可选）</Label>
                  <Input
                    id="p-user"
                    value={form.username ?? ""}
                    onChange={(e) => setForm({ ...form, username: e.target.value })}
                  />
                </div>
                <div className="grid gap-1.5">
                  <Label htmlFor="p-pass">密码（可选）</Label>
                  <Input
                    id="p-pass"
                    type="password"
                    placeholder={editing ? "留空不修改" : ""}
                    value={form.password ?? ""}
                    onChange={(e) => setForm({ ...form, password: e.target.value })}
                  />
                </div>
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="p-label">备注（可选）</Label>
                <Input
                  id="p-label"
                  value={form.label ?? ""}
                  onChange={(e) => setForm({ ...form, label: e.target.value })}
                />
              </div>
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setForm(null)}>
              取消
            </Button>
            <Button onClick={save} disabled={saving}>
              {saving && <Loader2Icon className="animate-spin" />} 保存
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 批量导入 dialog */}
      <Dialog open={importOpen} onOpenChange={setImportOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>批量导入代理</DialogTitle>
            <DialogDescription>
              每行一个：<code>host:port</code> 或 <code>scheme://user:pass@host:port</code>。导入的标记为可信、去重。
            </DialogDescription>
          </DialogHeader>
          <Textarea
            rows={10}
            className="font-mono text-xs"
            placeholder={"1.2.3.4:8080\nsocks5://9.9.9.9:1080\nhttp://user:pass@10.0.0.1:3128"}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setImportOpen(false)}>
              取消
            </Button>
            <Button onClick={doImport} disabled={saving || !importText.trim()}>
              {saving && <Loader2Icon className="animate-spin" />} 导入
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
