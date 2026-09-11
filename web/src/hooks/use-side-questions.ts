"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import { toast } from "sonner";

import { sseUrl } from "@/lib/api";
import { isBtwCommand, type SideExchange, type SideHistory, sideAPI } from "@/lib/side-questions";

// crypto.randomUUID 仅在安全上下文可用(https/localhost);经 IP+http 访问时降级。
function newSideRequestID(): string {
  return (
    globalThis.crypto?.randomUUID?.() ?? `btw-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  );
}

function merge(old: SideExchange[], incoming: SideExchange[]) {
  const byID = new Map(old.map((item) => [item.id, item]));
  for (const item of incoming) {
    if ((byID.get(item.id)?.sequence ?? -1) <= item.sequence) byID.set(item.id, item);
  }
  return [...byID.values()].sort((a, b) => a.ordinal - b.ordinal);
}

export function useSideQuestions(parent: string | null) {
  const [stateParent, setStateParent] = useState(parent);
  const current = stateParent === parent;
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<SideExchange[]>([]);
  const [snapshot, setSnapshot] = useState<SideHistory["snapshot"]>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(false);
  const [nextCursor, setNextCursor] = useState(0);
  const [error, setError] = useState("");
  const [loadError, setLoadError] = useState("");
  const epoch = useRef(0);
  const [streamEpoch, setStreamEpoch] = useState(0);
  const cursor = useRef(0);
  const submitting = useRef(false);
  const retry = useRef<{ question: string; id: string } | null>(null);
  const accepted = useRef<{ id: string; question: string } | null>(null);
  const running = current ? items.find((item) => item.status === "running") : undefined;
  const runningID = running?.id;

  const restoreFailedDraft = useCallback((incoming: SideExchange[]) => {
    const pending = accepted.current;
    if (!pending) return;
    const item = incoming.find((entry) => entry.id === pending.id);
    if (!item || item.status === "running") return;
    accepted.current = null;
    if (item.status === "failed" || item.status === "interrupted") {
      setDraft((old) => old || pending.question);
    }
  }, []);

  const load = useCallback(
    async (before = 0) => {
      if (!parent) return;
      const version = epoch.current;
      try {
        const data = await sideAPI.history(parent, before);
        if (version !== epoch.current) return;
        setItems((old) => merge(old, data.items));
        restoreFailedDraft(data.items);
        setSnapshot(data.snapshot);
        setLoadError("");
        if (before || !cursor.current) {
          cursor.current = data.next_cursor;
          setNextCursor(data.next_cursor);
        }
      } catch (err) {
        if (version === epoch.current) setLoadError((err as Error).message);
      }
    },
    [parent, restoreFailedDraft],
  );

  useEffect(() => {
    epoch.current++;
    setStateParent(parent);
    setItems([]);
    setSnapshot(null);
    setNextCursor(0);
    setDraft("");
    setError("");
    setLoadError("");
    setBusy(false);
    submitting.current = false;
    retry.current = null;
    accepted.current = null;
    cursor.current = 0;
    if (!parent) {
      setOpen(false);
      setLoading(false);
      return;
    }
    const version = epoch.current;
    setLoading(true);
    void sideAPI
      .history(parent)
      .then((data) => {
        if (version !== epoch.current) return;
        setItems((old) => merge(old, data.items));
        setSnapshot(data.snapshot);
        setNextCursor(data.next_cursor);
        cursor.current = data.next_cursor;
      })
      .catch((err: Error) => {
        if (version === epoch.current) setError(err.message);
      })
      .finally(() => {
        if (version === epoch.current) setLoading(false);
      });
    return () => {
      epoch.current++;
    };
  }, [parent]);

  useEffect(() => {
    if (!open || !parent) return;
    void load();
    const timer = setInterval(() => void load(), 2000);
    return () => clearInterval(timer);
  }, [open, parent, load]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: Reconnect after a clear attempt invalidates older callbacks.
  useEffect(() => {
    if (!runningID) return;
    const version = epoch.current;
    const stream = new EventSource(sseUrl(`/api/side-questions/${runningID}/events`));
    stream.addEventListener("snapshot", (event) => {
      if (version !== epoch.current) return;
      try {
        const item = JSON.parse((event as MessageEvent).data) as SideExchange;
        if (item.id !== runningID) return;
        setItems((old) => merge(old, [item]));
        restoreFailedDraft([item]);
        if (item.status !== "running") stream.close();
      } catch {
        setError("旁路数据解析失败，请重新打开面板");
      }
    });
    stream.addEventListener("cleared", () => {
      stream.close();
      if (version === epoch.current) setItems([]);
    });
    return () => stream.close();
  }, [runningID, streamEpoch, restoreFailedDraft]);

  const ask = async (input: string) => {
    const question = input.trim();
    if (!parent || !question || submitting.current || running) return;
    const version = epoch.current;
    submitting.current = true;
    setBusy(true);
    setError("");
    setDraft(question);
    setOpen(true);
    if (retry.current?.question !== question) retry.current = { question, id: newSideRequestID() };
    try {
      const item = await sideAPI.ask(parent, question, retry.current.id);
      if (version !== epoch.current) return;
      setItems((old) => merge(old, [item]));
      accepted.current = { id: item.id, question };
      setDraft("");
      setError("");
      retry.current = null;
      void load();
    } catch (err) {
      if (version === epoch.current) {
        setError((err as Error).message);
      }
    } finally {
      if (version === epoch.current) {
        submitting.current = false;
        setBusy(false);
      }
    }
  };

  const handleCommand = (text: string, clear: () => void) => {
    if (!parent || !isBtwCommand(text)) return false;
    const question = text.trim().slice(4).trim();
    setOpen(true);
    clear();
    if (question) {
      setDraft(question);
      void ask(question);
    }
    return true;
  };

  const clear = async () => {
    if (!parent || submitting.current) return;
    submitting.current = true;
    setBusy(true);
    const version = ++epoch.current;
    try {
      await sideAPI.clear(parent);
      if (version !== epoch.current) return;
      setItems([]);
      setNextCursor(0);
      cursor.current = 0;
      setError("");
      retry.current = null;
      accepted.current = null;
    } catch (err) {
      if (version === epoch.current) toast.error((err as Error).message);
    } finally {
      if (version === epoch.current) {
        submitting.current = false;
        setBusy(false);
        setStreamEpoch((value) => value + 1);
        void load();
      }
    }
  };

  const stop = async () => {
    if (running) {
      try {
        await sideAPI.cancel(running.id);
      } catch (err) {
        toast.error((err as Error).message);
      }
    }
  };
  return {
    open,
    setOpen,
    items: current ? items : [],
    snapshot: current ? snapshot : null,
    draft: current ? draft : "",
    setDraft,
    busy: current && busy,
    loading: !current || loading,
    error: current ? error || loadError : "",
    running,
    nextCursor: current ? nextCursor : 0,
    load,
    ask,
    handleCommand,
    clear,
    stop,
    enabled: !!parent,
  };
}

export type SideQuestions = ReturnType<typeof useSideQuestions>;
