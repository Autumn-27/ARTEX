"use client";

import * as React from "react";

import { toast } from "sonner";

import { CapturedTrafficViewer } from "@/components/traffic-evidence-viewer";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Field, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { FindingTrafficBinding, TrafficResp } from "@/lib/types";

const METHODS = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];

export function TrafficPickerDialog({
  findingId,
  contextTask,
  bound,
  onClose,
  onBound,
}: {
  findingId: string;
  contextTask?: string;
  bound: FindingTrafficBinding[];
  onClose: () => void;
  onBound: () => void;
}) {
  const [host, setHost] = React.useState("");
  const [method, setMethod] = React.useState("all");
  const [query, setQuery] = React.useState("");
  const [page, setPage] = React.useState(0);
  const [data, setData] = React.useState<TrafficResp | null>(null);
  const [selected, setSelected] = React.useState<Set<string>>(() => new Set());
  const [preview, setPreview] = React.useState<string | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const alreadyBound = new Set(bound.map((b) => b.snapshot.source_traffic_id));
  React.useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    const timer = setTimeout(() => {
      api
        .traffic(page, 25, host, method === "all" ? "" : method, query)
        .then((d) => {
          if (active) setData(d);
        })
        .catch((e: Error) => {
          if (active) setError(e.message);
        })
        .finally(() => {
          if (active) setLoading(false);
        });
    }, 250);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [page, host, method, query]);

  const rows = data?.exchanges ?? [];
  const selectable = rows.filter((e) => !alreadyBound.has(e.id));
  function toggle(id: string, checked: boolean) {
    setSelected((previous) => {
      const next = new Set(previous);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }
  async function save() {
    setBusy(true);
    setError("");
    try {
      await api.bindFindingTraffic(
        findingId,
        [...selected].map((traffic_id) => ({ traffic_id })),
        contextTask,
      );
      toast.success(`已绑定 ${selected.size} 条流量`);
      onBound();
      onClose();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open && !busy) onClose();
        }}
      >
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-5xl">
          <DialogHeader>
            <DialogTitle>绑定流量</DialogTitle>
            <DialogDescription>
              筛选并多选请求/响应，已选记录会跨页保留。绑定后可设置用途、说明和顺序。
            </DialogDescription>
          </DialogHeader>
          <FieldGroup className="flex flex-col gap-3 sm:flex-row">
            <Field>
              <FieldLabel htmlFor="evidence-host">目标 host</FieldLabel>
              <Input
                id="evidence-host"
                value={host}
                placeholder="域名或 IP"
                onChange={(e) => {
                  setHost(e.target.value);
                  setPage(0);
                }}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="evidence-method">请求方法</FieldLabel>
              <Select
                value={method}
                onValueChange={(v) => {
                  setMethod(v);
                  setPage(0);
                }}
              >
                <SelectTrigger id="evidence-method">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="all">全部方法</SelectItem>
                    {METHODS.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor="evidence-query">关键词</FieldLabel>
              <Input
                id="evidence-query"
                value={query}
                placeholder="URL / 正文关键词"
                onChange={(e) => {
                  setQuery(e.target.value);
                  setPage(0);
                }}
              />
            </Field>
          </FieldGroup>
          {error ? (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          ) : null}
          <div className="max-h-[45vh] overflow-auto rounded-md border" aria-busy={loading}>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>
                    <Checkbox
                      aria-label="选择本页未绑定流量"
                      disabled={loading || busy || selectable.length === 0}
                      checked={selectable.length > 0 && selectable.every((e) => selected.has(e.id))}
                      onCheckedChange={(checked) =>
                        setSelected((previous) => {
                          const next = new Set(previous);
                          for (const e of selectable) {
                            if (checked === true) next.add(e.id);
                            else next.delete(e.id);
                          }
                          return next;
                        })
                      }
                    />
                  </TableHead>
                  <TableHead>方法 / URL</TableHead>
                  <TableHead>时间</TableHead>
                  <TableHead>状态码</TableHead>
                  <TableHead>操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((e) => (
                  <TableRow key={e.id}>
                    <TableCell>
                      <Checkbox
                        aria-label={`选择流量 ${e.id}`}
                        checked={selected.has(e.id) || alreadyBound.has(e.id)}
                        disabled={busy || loading || alreadyBound.has(e.id)}
                        onCheckedChange={(checked) => toggle(e.id, checked === true)}
                      />
                    </TableCell>
                    <TableCell>
                      <span className="font-mono text-xs">
                        {e.method} {e.url}
                      </span>
                      {alreadyBound.has(e.id) ? <Badge variant="secondary">已绑定</Badge> : null}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs">
                      {new Date(e.ts).toLocaleString("zh-CN")}
                    </TableCell>
                    <TableCell>{e.status}</TableCell>
                    <TableCell>
                      <Button variant="ghost" size="sm" onClick={() => setPreview(e.id)}>
                        预览
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
                {!rows.length ? (
                  <TableRow>
                    <TableCell colSpan={5} className="py-8 text-center">
                      {loading ? "加载中…" : "没有匹配的流量"}
                    </TableCell>
                  </TableRow>
                ) : null}
              </TableBody>
            </Table>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-sm">
              已选 {selected.size} 条 · 共 {data?.total ?? 0} 条
            </span>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={loading || page === 0}
                onClick={() => setPage((p) => p - 1)}
              >
                上一页
              </Button>
              <span className="text-xs">第 {page + 1} 页</span>
              <Button
                variant="outline"
                size="sm"
                disabled={loading || (page + 1) * 25 >= (data?.total ?? 0)}
                onClick={() => setPage((p) => p + 1)}
              >
                下一页
              </Button>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={onClose}>
              取消
            </Button>
            <Button disabled={busy || selected.size === 0} onClick={() => void save()}>
              {busy ? "保存中…" : `绑定 ${selected.size} 条流量`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <CapturedTrafficViewer id={preview} onClose={() => setPreview(null)} />
    </>
  );
}
