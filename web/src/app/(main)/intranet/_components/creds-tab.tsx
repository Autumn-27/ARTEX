"use client";

import * as React from "react";

import { CheckCircle2Icon, Loader2Icon, MinusCircleIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import type { Credential } from "@/lib/types";

import { fmtTime } from "./format";

// 凭据台账：密钥只做脱敏展示。后端提供 task_id 字段时出现任务过滤框。
export function CredsTab({ credentials, loading }: { credentials: Credential[]; loading: boolean }) {
  const [taskFilter, setTaskFilter] = React.useState("");

  // 后端 task_id 是 JSON number(0 = 未关联),不是字符串。
  const hasTaskField = credentials.some((c) => typeof c.task_id === "number" && c.task_id > 0);
  const q = taskFilter.trim();
  const visible = q ? credentials.filter((c) => String(c.task_id ?? "").includes(q)) : credentials;

  return (
    <>
      {hasTaskField && (
        <div className="flex items-center gap-2 px-1 pb-2">
          <Input
            placeholder="按任务 ID 过滤..."
            className="h-8 w-56"
            value={taskFilter}
            onChange={(e) => setTaskFilter(e.target.value)}
          />
          {q && (
            <span className="text-xs text-muted-foreground tabular-nums">
              {visible.length} / {credentials.length}
            </span>
          )}
        </div>
      )}
      <Card className="flex min-h-0 flex-1 flex-col overflow-hidden py-0">
        <div className="min-h-0 flex-1 overflow-auto">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-card">
              <TableRow>
                <TableHead className="w-[70px]">ID</TableHead>
                <TableHead className="w-[110px]">类型</TableHead>
                <TableHead>用户名</TableHead>
                <TableHead className="w-[140px]">域</TableHead>
                <TableHead>密钥（脱敏）</TableHead>
                <TableHead className="w-[160px]">来源</TableHead>
                <TableHead className="w-[80px]">已验证</TableHead>
                <TableHead className="w-[140px]">时间</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {loading && visible.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={8} className="py-12 text-center">
                    <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
                  </TableCell>
                </TableRow>
              ) : visible.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={8} className="py-12 text-center text-sm text-muted-foreground">
                    {q ? "该任务下暂无凭据" : "暂无凭据"}
                  </TableCell>
                </TableRow>
              ) : (
                visible.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell className="font-mono text-xs text-muted-foreground">#{c.id}</TableCell>
                    <TableCell>
                      <Badge variant="secondary" className="font-mono text-xs">
                        {c.cred_type || "-"}
                      </Badge>
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span className="block truncate font-mono text-xs" title={c.username}>
                        {c.username || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span className="block truncate font-mono text-xs" title={c.domain}>
                        {c.domain || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="max-w-0">
                      <code className="block truncate font-mono text-xs" title={c.secret_masked}>
                        {c.secret_masked || "—"}
                      </code>
                    </TableCell>
                    <TableCell className="max-w-0">
                      <span className="block truncate text-xs text-muted-foreground" title={c.source}>
                        {c.source || "—"}
                      </span>
                    </TableCell>
                    <TableCell>
                      {c.verified ? (
                        <CheckCircle2Icon className="size-4 text-emerald-500" aria-label="已验证" />
                      ) : (
                        <MinusCircleIcon className="size-4 text-muted-foreground" aria-label="未验证" />
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground tabular-nums">
                      {fmtTime(c.created_at)}
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </div>
      </Card>
    </>
  );
}
