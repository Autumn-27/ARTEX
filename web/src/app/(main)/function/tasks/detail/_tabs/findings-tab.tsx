"use client";

import * as React from "react";
import { ChevronRightIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import { StatusBadge } from "@/components/status-badge";
import { api } from "@/lib/api";
import type { Finding } from "@/lib/types";

function Row({ f }: { f: Finding }) {
  const [open, setOpen] = React.useState(false);
  return (
    <div className="border-b last:border-b-0">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-3 px-4 py-3 text-left text-sm hover:bg-accent/40"
      >
        <ChevronRightIcon
          className={cn(
            "size-4 shrink-0 text-muted-foreground transition-transform",
            open && "rotate-90",
          )}
        />
        <StatusBadge domain="severity" value={f.severity} dot />
        <span className="w-20 shrink-0 font-medium">{f.vulnclass}</span>
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          {f.summary}
        </span>
        <span className="shrink-0 text-xs text-muted-foreground">
          {new Date(f.ts).toLocaleString("zh-CN")}
        </span>
      </button>
      {open && (
        <div className="bg-muted/30 px-4 pb-4 pl-11">
          <div className="mb-1 text-xs font-medium text-muted-foreground">
            证据 / PoC
          </div>
          <pre className="overflow-auto rounded-md border bg-background p-3 font-mono text-xs whitespace-pre-wrap">
            {f.evidence}
          </pre>
        </div>
      )}
    </div>
  );
}

export function FindingsTab({ taskId }: { taskId: string }) {
  const [findings, setFindings] = React.useState<Finding[]>([]);

  React.useEffect(() => {
    let active = true;
    const load = () => {
      api
        .findings(taskId)
        .then((fs) => {
          if (active) setFindings(fs);
        })
        .catch(() => {});
    };
    load();
    const t = setInterval(load, 3000);
    return () => {
      active = false;
      clearInterval(t);
    };
  }, [taskId]);

  const items = findings
    .filter((f) => f.task_id === taskId)
    .sort((a, b) => {
      const order = { high: 0, medium: 1, low: 2 };
      return order[a.severity] - order[b.severity];
    });

  return (
    <Card className="overflow-hidden py-0">
      <CardContent className="px-0">
        {items.map((f) => (
          <Row key={f.id} f={f} />
        ))}
        {items.length === 0 && (
          <p className="px-4 py-8 text-center text-sm text-muted-foreground">
            本任务暂无确认发现。
          </p>
        )}
      </CardContent>
    </Card>
  );
}
