"use client";

import * as React from "react";

import { AtSignIcon, ChevronRightIcon, Loader2Icon, XIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { InputGroupTextarea } from "@/components/ui/input-group";
import { Popover, PopoverAnchor, PopoverContent } from "@/components/ui/popover";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import {
  activeMention,
  type ChatMention,
  mentionKinds,
  mentionSearch,
  mentionToken,
  selectedMentions,
} from "@/lib/chat-mentions";
import { cn } from "@/lib/utils";

type Props = Omit<React.ComponentProps<"textarea">, "value" | "onChange" | "ref"> & {
  value: string;
  onValueChange: (value: string) => void;
  inputGroup?: boolean;
};

export function MentionTextarea({ value, onValueChange, onKeyDown, className, inputGroup, disabled, ...props }: Props) {
  const textarea = React.useRef<HTMLTextAreaElement>(null);
  const composing = React.useRef(false);
  const pendingKeyboardIndex = React.useRef<number | null>(null);
  const [cursor, setCursor] = React.useState<number | null>(null);
  const [dismissed, setDismissed] = React.useState(false);
  const [activeIndex, setActiveIndex] = React.useState(0);
  const [result, setResult] = React.useState<{
    key: string;
    items: ChatMention[];
    next_cursor?: string;
    error?: string;
    loadingMore?: boolean;
    pageError?: string;
  } | null>(null);
  const request = React.useRef<{ key: string; controller: AbortController; loadingMore: boolean } | null>(null);
  const listId = React.useId();
  const active = cursor === null ? null : activeMention(value, cursor);
  const search = mentionSearch(active?.query ?? "");
  const open = !!active && !dismissed && !disabled;
  const categories = search.categories;
  const requestKey = open && !categories.length ? `${search.kind}:${search.query}` : "";
  const loading = !!requestKey && result?.key !== requestKey;
  const items = result?.key === requestKey ? result.items : [];
  const error = result?.key === requestKey ? result.error : undefined;
  const nextCursor = result?.key === requestKey ? result.next_cursor : undefined;
  const loadingMore = result?.key === requestKey && result.loadingMore;
  const pageError = result?.key === requestKey ? result.pageError : undefined;
  const count = categories.length || items.length;
  const selected = selectedMentions(value);

  React.useEffect(() => {
    if (!requestKey) return;
    setResult(null);
    const controller = new AbortController();
    request.current = { key: requestKey, controller, loadingMore: false };
    const timer = setTimeout(() => {
      api.chatMentions(search.kind, search.query, controller.signal).then(
        (page) => {
          if (!controller.signal.aborted) setResult({ key: requestKey, ...page });
        },
        (error: Error) => {
          if (!controller.signal.aborted) setResult({ key: requestKey, items: [], error: error.message });
        },
      );
    }, 180);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [requestKey, search.kind, search.query]);

  function loadMore() {
    const session = request.current;
    if (
      !nextCursor ||
      !session ||
      session.key !== requestKey ||
      session.controller.signal.aborted ||
      session.loadingMore
    )
      return;
    session.loadingMore = true;
    setResult((current) =>
      current?.key === requestKey ? { ...current, loadingMore: true, pageError: undefined } : current,
    );
    api
      .chatMentions(search.kind, search.query, session.controller.signal, nextCursor)
      .then(
        (page) => {
          if (session.controller.signal.aborted) return;
          setResult((current) => {
            if (current?.key !== session.key) return current;
            const seen = new Set(current.items.map((item) => `${item.kind}:${item.id}`));
            return {
              ...current,
              items: [...current.items, ...page.items.filter((item) => !seen.has(`${item.kind}:${item.id}`))],
              next_cursor: page.next_cursor,
              loadingMore: false,
            };
          });
        },
        (error: Error) => {
          if (!session.controller.signal.aborted)
            setResult((current) =>
              current?.key === session.key ? { ...current, loadingMore: false, pageError: error.message } : current,
            );
        },
      )
      .finally(() => {
        session.loadingMore = false;
      });
  }

  React.useEffect(() => {
    if (open) document.getElementById(`${listId}-${activeIndex}`)?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, listId, open]);

  React.useEffect(() => {
    if (open && pendingKeyboardIndex.current !== null && pendingKeyboardIndex.current < count) {
      document.getElementById(`${listId}-${pendingKeyboardIndex.current}`)?.scrollIntoView({ block: "nearest" });
      pendingKeyboardIndex.current = null;
    }
  }, [count, listId, open]);

  function replaceActive(text: string, close: boolean) {
    if (!active) return;
    const next = value.slice(0, active.start) + text + value.slice(active.end);
    const caret = active.start + text.length;
    onValueChange(next);
    setCursor(caret);
    setDismissed(close);
    setActiveIndex(0);
    requestAnimationFrame(() => {
      textarea.current?.focus();
      textarea.current?.setSelectionRange(caret, caret);
    });
  }

  function choose(index: number) {
    if (categories[index]) replaceActive(`@${categories[index].label} `, false);
    else if (items[index]) replaceActive(`${mentionToken(items[index])} `, true);
  }

  function keyDown(event: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (composing.current || event.nativeEvent.isComposing || event.keyCode === 229) return;
    if (open) {
      if (event.key === "Escape") {
        event.preventDefault();
        setDismissed(true);
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (event.key === "ArrowDown" && count && activeIndex >= count - 1 && nextCursor) {
          pendingKeyboardIndex.current = count;
          setActiveIndex(count);
          loadMore();
          return;
        }
        if (count) setActiveIndex((index) => (index + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
        return;
      }
      if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
        if (event.key === "Tab" && (loading || !count)) {
          setDismissed(true);
          return;
        }
        event.preventDefault();
        if (!loading && count) choose(Math.min(activeIndex, count - 1));
        return;
      }
    }
    onKeyDown?.(event);
  }

  const Control = inputGroup ? InputGroupTextarea : Textarea;
  return (
    <div className="min-w-0 flex-1 self-stretch">
      <Popover
        open={open}
        onOpenChange={(next) => {
          if (!next) setDismissed(true);
        }}
      >
        <PopoverAnchor asChild>
          <div>
            <Control
              {...props}
              ref={textarea}
              value={value}
              disabled={disabled}
              className={className}
              aria-label={props["aria-label"] ?? "消息，输入 @ 引用记录"}
              aria-autocomplete="list"
              aria-controls={open ? listId : undefined}
              aria-expanded={open}
              aria-activedescendant={open && count ? `${listId}-${Math.min(activeIndex, count - 1)}` : undefined}
              onChange={(event) => {
                pendingKeyboardIndex.current = null;
                onValueChange(event.target.value);
                setCursor(event.target.selectionStart);
                setDismissed(false);
                setActiveIndex(0);
              }}
              onSelect={(event) => setCursor(event.currentTarget.selectionStart)}
              onCompositionStart={() => {
                composing.current = true;
              }}
              onCompositionEnd={() => {
                composing.current = false;
              }}
              onKeyDown={keyDown}
            />
          </div>
        </PopoverAnchor>
        <PopoverContent
          side="top"
          align="start"
          className="w-[min(28rem,calc(100vw-2rem))] gap-1 p-1.5"
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
          onInteractOutside={(event) => {
            if (event.target === textarea.current) event.preventDefault();
          }}
          aria-label="选择引用记录"
        >
          <div className="flex items-center justify-between px-2 py-1 text-muted-foreground text-xs">
            <span>
              {categories.length
                ? "选择引用类型"
                : `搜索${mentionKinds.find((kind) => kind.kind === search.kind)?.label ?? "全部记录"}`}
            </span>
            <span>↑↓ 选择 · Enter 确认 · Esc 关闭</span>
          </div>
          <div
            id={listId}
            role="listbox"
            aria-label="引用候选"
            className="max-h-60 overflow-y-auto"
            onScroll={(event) => {
              const list = event.currentTarget;
              if (list.scrollHeight - list.scrollTop - list.clientHeight < 64) loadMore();
            }}
          >
            {categories.map((kind, index) => (
              <button
                key={kind.kind}
                id={`${listId}-${index}`}
                type="button"
                role="option"
                aria-selected={activeIndex === index}
                className={cn(
                  "flex w-full items-center gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-accent",
                  activeIndex === index && "bg-accent",
                )}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(index)}
              >
                <AtSignIcon className="size-4 text-muted-foreground" />
                <span>{kind.label}</span>
                <ChevronRightIcon className="ml-auto size-4 text-muted-foreground" />
              </button>
            ))}
            {!categories.length && loading && (
              <div role="status" className="flex items-center gap-2 p-3 text-muted-foreground text-sm">
                <Loader2Icon className="size-4 animate-spin" />
                搜索中…
              </div>
            )}
            {!categories.length && !loading && error && (
              <div role="alert" className="p-3 text-destructive text-sm">
                搜索失败：{error}。请重新输入重试。
              </div>
            )}
            {!categories.length && !loading && !error && !items.length && (
              <div role="status" className="p-3 text-muted-foreground text-sm">
                没有匹配记录，请更换名称、地址或 ID
              </div>
            )}
            {items.map((item, index) => (
              <button
                key={`${item.kind}:${item.id}`}
                id={`${listId}-${index}`}
                type="button"
                role="option"
                aria-selected={activeIndex === index}
                className={cn(
                  "flex w-full flex-col gap-0.5 rounded-md px-2 py-2 text-left hover:bg-accent",
                  activeIndex === index && "bg-accent",
                )}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(index)}
              >
                <span className="flex w-full items-center gap-2">
                  <Badge variant="outline">{mentionKinds.find((kind) => kind.kind === item.kind)?.label}</Badge>
                  <span className="truncate text-sm">{item.label}</span>
                  <span className="ml-auto shrink-0 text-muted-foreground text-xs">#{item.id}</span>
                </span>
                <span className="w-full truncate text-muted-foreground text-xs">{item.description}</span>
              </button>
            ))}
          </div>
          {!categories.length && (
            <p className="px-2 py-1 text-muted-foreground text-xs">
              {loadingMore && <span role="status">正在加载更多…</span>}
              {!loadingMore && nextCursor && (
                <button type="button" onMouseDown={(event) => event.preventDefault()} onClick={loadMore}>
                  {pageError ? "加载失败，点击重试" : `已显示 ${items.length} 条，向下滚动加载更多`}
                </button>
              )}
              {!loadingMore && !nextCursor && !loading && !error && items.length > 0 && `已显示全部 ${items.length} 条`}
              {!loadingMore &&
                !nextCursor &&
                (loading || !!error || items.length === 0) &&
                "输入名称、地址或 ID 搜索记录"}
            </p>
          )}
        </PopoverContent>
      </Popover>
      {selected.length > 0 && (
        <div className="flex flex-wrap items-center gap-1 px-1 pt-1">
          {selected.map((item) => (
            <Badge key={item.start} variant="secondary" className="max-w-full gap-1">
              <span className="max-w-64 truncate" title={item.label}>
                {item.label}
              </span>
              <button
                type="button"
                disabled={disabled}
                aria-label={`移除引用 ${item.label}`}
                onClick={() => {
                  onValueChange(value.slice(0, item.start) + value.slice(item.start + item.token.length));
                  setCursor(null);
                }}
              >
                <XIcon className="size-3" />
              </button>
            </Badge>
          ))}
          <span className="text-muted-foreground text-xs">发送时读取最新详情 · 最多 10 条</span>
        </div>
      )}
    </div>
  );
}
