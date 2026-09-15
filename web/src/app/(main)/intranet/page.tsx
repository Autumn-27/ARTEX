"use client";

import * as React from "react";

import { Loader2Icon, NetworkIcon, RefreshCwIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { Credential, IntranetTopology, ShellSession, Task, TunnelInfo } from "@/lib/types";
import { cn } from "@/lib/utils";

import { CredsTab } from "./_components/creds-tab";
import { SessionWorkbench } from "./_components/session-workbench";
import { type HostFilter, SessionsTab } from "./_components/sessions-tab";
import { TopologyGraph } from "./_components/topology-graph";
import { TunnelsTab } from "./_components/tunnels-tab";

const SESSIONS_POLL_MS = 10_000;

export default function IntranetPage() {
  const [topology, setTopology] = React.useState<IntranetTopology | null>(null);
  const [sessions, setSessions] = React.useState<ShellSession[]>([]);
  const [tunnels, setTunnels] = React.useState<TunnelInfo[]>([]);
  const [credentials, setCredentials] = React.useState<Credential[]>([]);
  const [loadingTopology, setLoadingTopology] = React.useState(true);
  const [loadingSessions, setLoadingSessions] = React.useState(true);
  const [loadingTunnels, setLoadingTunnels] = React.useState(true);
  const [loadingCreds, setLoadingCreds] = React.useState(true);

  const [tab, setTab] = React.useState("sessions");
  const [hostFilter, setHostFilter] = React.useState<HostFilter | null>(null);
  const [workbenchSession, setWorkbenchSession] = React.useState<ShellSession | null>(null);

  // 任务过滤:"all" = 全局视图(后端不传 task_id);四个台账 + 拓扑共用同一选择。
  const [taskId, setTaskId] = React.useState("all");
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const taskParam = taskId === "all" ? undefined : taskId;

  React.useEffect(() => {
    api
      .tasks()
      .then((r) => setTasks(r.tasks))
      .catch(() => {
        /* 任务列表拉取失败不阻塞台账 */
      });
  }, []);

  const loadTopology = React.useCallback(() => {
    setLoadingTopology(true);
    api
      .intranetTopology(taskParam)
      .then(setTopology)
      .catch(() => {
        /* 保留上一次数据 */
      })
      .finally(() => setLoadingTopology(false));
  }, [taskParam]);

  // 会话列表 10s 轮询；签名相同则跳过 setState，避免打开工作台时无谓重渲染。
  const sigRef = React.useRef("");
  const loadSessions = React.useCallback(
    (initial = false) => {
      if (initial) setLoadingSessions(true);
      api
        .shellSessions(taskParam)
        .then((items) => {
          const sig = JSON.stringify(items);
          if (sig !== sigRef.current) {
            sigRef.current = sig;
            setSessions(items);
          }
        })
        .catch(() => {
          /* 保留上一次数据 */
        })
        .finally(() => initial && setLoadingSessions(false));
    },
    [taskParam],
  );

  const loadTunnels = React.useCallback(() => {
    setLoadingTunnels(true);
    api
      .tunnels(taskParam)
      .then(setTunnels)
      .catch(() => {
        /* 保留上一次数据 */
      })
      .finally(() => setLoadingTunnels(false));
  }, [taskParam]);

  const loadCreds = React.useCallback(() => {
    setLoadingCreds(true);
    api
      .credentials(taskParam)
      .then(setCredentials)
      .catch(() => {
        /* 保留上一次数据 */
      })
      .finally(() => setLoadingCreds(false));
  }, [taskParam]);

  React.useEffect(() => {
    loadTopology();
    loadTunnels();
    loadCreds();
    sigRef.current = ""; // 切换任务后强制刷新会话列表
    loadSessions(true);
    const timer = setInterval(() => loadSessions(), SESSIONS_POLL_MS);
    return () => clearInterval(timer);
  }, [loadTopology, loadTunnels, loadCreds, loadSessions]);

  const refreshAll = () => {
    loadTopology();
    loadTunnels();
    loadCreds();
    sigRef.current = "";
    loadSessions(true);
  };

  // 拓扑节点点击 → 切到「会话」Tab 并按该主机过滤。
  const onSelectHost = React.useCallback(
    (nodeId: string) => {
      const node = topology?.nodes.find((n) => n.id === nodeId);
      setHostFilter({ id: nodeId, ip: node?.ip ?? "" });
      setTab("sessions");
    },
    [topology],
  );

  // 轮询刷新后，工作台下那条会话可能已被删除/状态变化：同步最新记录。
  React.useEffect(() => {
    setWorkbenchSession((cur) => {
      if (!cur) return cur;
      return sessions.find((s) => s.id === cur.id) ?? cur;
    });
  }, [sessions]);

  const aliveSessions = sessions.filter((s) => s.status === "alive").length;

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4">
      {/* Header */}
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <NetworkIcon className="h-5 w-5 text-muted-foreground" />
          <h1 className="text-xl font-semibold tracking-tight">内网作战</h1>
          <Badge variant="secondary" className="tabular-nums">
            会话 {aliveSessions}/{sessions.length}
          </Badge>
          <Badge variant="secondary" className="tabular-nums">
            隧道 {tunnels.length}
          </Badge>
          <Badge variant="secondary" className="tabular-nums">
            凭据 {credentials.length}
          </Badge>
        </div>
        <div className="flex items-center gap-2">
          {/* 任务过滤:照 findings 页下拉范式;选中值同时作用于拓扑与三台账(含 10s 轮询)。 */}
          <Select value={taskId} onValueChange={setTaskId}>
            <SelectTrigger size="sm" className="w-48">
              <SelectValue placeholder="任务" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">全部任务</SelectItem>
              {tasks.map((t) => {
                const id = String(t.id);
                const label = t.name || t.description || `任务 #${id}`;
                return (
                  <SelectItem key={id} value={id}>
                    <span className="max-w-[14rem] truncate" title={label}>
                      {label}
                    </span>
                  </SelectItem>
                );
              })}
            </SelectContent>
          </Select>
          <Button variant="outline" size="sm" className="h-8 gap-1" onClick={refreshAll}>
            <RefreshCwIcon className={cn("size-3.5", loadingTopology && "animate-spin")} />
            刷新
          </Button>
        </div>
      </div>

      {/* 上半部：内网拓扑 */}
      <Card>
        <CardContent className="p-0">
          <div className="h-[42vh] w-full overflow-hidden rounded-xl">
            <TopologyGraph
              data={topology}
              loading={loadingTopology}
              onRefresh={loadTopology}
              onSelectHost={onSelectHost}
            />
          </div>
        </CardContent>
      </Card>

      {/* 下半部：会话 / 隧道 / 凭据 */}
      <Tabs value={tab} onValueChange={setTab} className="flex min-h-0 flex-1 flex-col gap-0">
        <TabsList className="w-fit">
          <TabsTrigger value="sessions">会话</TabsTrigger>
          <TabsTrigger value="tunnels">隧道</TabsTrigger>
          <TabsTrigger value="creds">凭据</TabsTrigger>
        </TabsList>
        <TabsContent value="sessions" className="flex min-h-0 flex-1 flex-col pt-2">
          <SessionsTab
            sessions={sessions}
            loading={loadingSessions}
            hostFilter={hostFilter}
            onClearHostFilter={() => setHostFilter(null)}
            onOpenWorkbench={setWorkbenchSession}
            onChanged={() => {
              sigRef.current = "";
              loadSessions(true);
              loadTopology();
            }}
          />
        </TabsContent>
        <TabsContent value="tunnels" className="flex min-h-0 flex-1 flex-col pt-2">
          <TunnelsTab
            tunnels={tunnels}
            loading={loadingTunnels}
            onRefresh={() => {
              loadTunnels();
              loadTopology();
            }}
          />
        </TabsContent>
        <TabsContent value="creds" className="flex min-h-0 flex-1 flex-col pt-2">
          {loadingCreds && credentials.length === 0 ? (
            <div className="py-12 text-center">
              <Loader2Icon className="mx-auto h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <CredsTab credentials={credentials} loading={loadingCreds} />
          )}
        </TabsContent>
      </Tabs>

      <SessionWorkbench
        session={workbenchSession}
        onOpenChange={(o) => !o && setWorkbenchSession(null)}
        onDeleted={() => {
          sigRef.current = "";
          loadSessions(true);
          loadTopology();
        }}
      />
    </div>
  );
}
