"use client";
import * as React from "react";

import { CheckIcon, CopyIcon, WrapTextIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn, copyText } from "@/lib/utils";

function statusTone(status: number) {
  if (status >= 500) return "text-red-500";
  if (status >= 400) return "text-amber-500";
  if (status >= 300) return "text-blue-500";
  if (status >= 200) return "text-emerald-500";
  return "text-muted-foreground";
}

function StartLine({ line }: { line: string }) {
  const response = /^(HTTP\/\S+)(\s+)(\d{3})(.*)$/.exec(line);
  if (response) {
    return (
      <>
        <span className="text-muted-foreground">{response[1]}</span>
        {response[2]}
        <span className={statusTone(Number(response[3]))}>{response[3]}</span>
        {response[4]}
      </>
    );
  }

  const request = /^([A-Z]+)(\s+)(\S+)(\s+)(HTTP\/\S+)$/.exec(line);
  if (request) {
    return (
      <>
        <span className="font-semibold text-primary">{request[1]}</span>
        {request[2]}
        <span className="text-chart-2">{request[3]}</span>
        {request[4]}
        <span className="text-muted-foreground">{request[5]}</span>
      </>
    );
  }

  return line;
}

function HeaderLine({ line }: { line: string }) {
  const separator = line.indexOf(":");
  if (separator <= 0) return line;
  return (
    <>
      <span className="text-primary">{line.slice(0, separator)}</span>
      <span className="text-muted-foreground">:</span>
      <span className="text-chart-2">{line.slice(separator + 1)}</span>
    </>
  );
}

function JsonBody({ body }: { body: string }) {
  const parts: React.ReactNode[] = [];
  const tokens = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g;
  let cursor = 0;
  for (const match of body.matchAll(tokens)) {
    const index = match.index ?? 0;
    if (index > cursor) parts.push(body.slice(cursor, index));
    let className = "text-chart-4";
    if (match[1]) className = match[2] ? "text-primary" : "text-chart-2";
    else if (match[3]) className = "text-chart-3";
    parts.push(
      <span key={`${index}-${match[0].length}`} className={className}>
        {match[0]}
      </span>,
    );
    cursor = index + match[0].length;
  }
  if (cursor < body.length) parts.push(body.slice(cursor));
  return parts;
}

function MarkupBody({ body }: { body: string }) {
  const parts: React.ReactNode[] = [];
  const tags = /<\/?[A-Za-z][^>]*>|<!--[\s\S]*?-->/g;
  let cursor = 0;
  for (const match of body.matchAll(tags)) {
    const index = match.index ?? 0;
    if (index > cursor) parts.push(body.slice(cursor, index));
    parts.push(
      <span key={`${index}-${match[0].length}`} className="text-primary">
        {match[0]}
      </span>,
    );
    cursor = index + match[0].length;
  }
  if (cursor < body.length) parts.push(body.slice(cursor));
  return parts;
}

type BodyFormat = "json" | "markup" | "plain";

function detectBodyFormat(body: string): BodyFormat {
  const trimmed = body.trimStart();
  if (trimmed.startsWith("{") || trimmed.startsWith("[")) return "json";
  if (trimmed.startsWith("<")) return "markup";
  return "plain";
}

function HighlightedBody({ body, format }: { body: string; format: BodyFormat }) {
  if (format === "json") return <JsonBody body={body} />;
  if (format === "markup") return <MarkupBody body={body} />;
  return body;
}

export function HttpCodeBlock({ raw }: { raw: string }) {
  const [wrapLines, setWrapLines] = React.useState(true);
  const [copied, setCopied] = React.useState(false);
  const value = raw || "（空）";
  const lines = value.replaceAll("\r\n", "\n").split("\n");
  const separator = lines.indexOf("");
  const body = separator >= 0 ? lines.slice(separator + 1).join("\n") : "";
  const bodyFormat = detectBodyFormat(body);

  React.useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(timer);
  }, [copied]);

  const copyPacket = async () => {
    const ok = await copyText(value);
    if (ok) {
      setCopied(true);
      return;
    }
    toast.error("复制失败，请使用 Ctrl/Cmd+A 后复制");
  };

  const renderLine = (line: string, index: number) => {
    if (index === 0) return <StartLine line={line} />;
    if (separator < 0 || index < separator) return <HeaderLine line={line} />;
    if (index === separator) return null;
    return <HighlightedBody body={line} format={bodyFormat} />;
  };

  return (
    <div className="relative mx-3 my-4 overflow-hidden rounded-md border bg-background shadow-xs">
      <div className="absolute top-2 right-2 flex items-center gap-0.5 rounded-md border bg-background/95 p-0.5 shadow-xs">
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={wrapLines ? "关闭自动换行" : "开启自动换行"}
              aria-pressed={wrapLines}
              onClick={() => setWrapLines((current) => !current)}
            >
              <WrapTextIcon />
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{wrapLines ? "关闭自动换行" : "开启自动换行"}</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              aria-label={copied ? "已复制报文" : "复制报文"}
              onClick={() => void copyPacket()}
            >
              {copied ? <CheckIcon /> : <CopyIcon />}
            </Button>
          </TooltipTrigger>
          <TooltipContent side="bottom">{copied ? "已复制" : "复制报文"}</TooltipContent>
        </Tooltip>
      </div>
      {/* biome-ignore lint/a11y/useSemanticElements: textarea cannot preserve line numbers and syntax-highlighting markup. */}
      <div
        role="textbox"
        aria-label="HTTP 报文代码"
        aria-multiline="true"
        aria-readonly="true"
        tabIndex={0}
        className="max-h-[calc(100vh-15rem)] min-w-0 overflow-auto bg-background py-3 font-mono text-xs outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        onKeyDown={(event) => {
          if (!(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "a") return;
          event.preventDefault();
          const selection = window.getSelection();
          if (!selection) return;
          const range = document.createRange();
          range.selectNodeContents(event.currentTarget);
          selection.removeAllRanges();
          selection.addRange(range);
        }}
      >
        {lines.map((line, index) => (
          <div
            key={`${index}-${line}`}
            data-line={index + 1}
            className="grid min-w-0 grid-cols-[1.5rem_minmax(0,1fr)] leading-relaxed before:sticky before:left-0 before:self-stretch before:border-r before:bg-muted/20 before:px-1 before:text-right before:text-muted-foreground/60 before:content-[attr(data-line)]"
          >
            <code
              className={cn(
                "min-h-[1lh] min-w-0 pr-16 pl-1.5 [tab-size:4]",
                wrapLines ? "break-words whitespace-pre-wrap" : "whitespace-pre",
              )}
            >
              {renderLine(line, index)}
            </code>
          </div>
        ))}
      </div>
    </div>
  );
}
