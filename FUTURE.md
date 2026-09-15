# FUTURE —— 分布式改造全面评估

> 状态：评估完成，未动工。本文只做架构判断与设计预案，不涉及当前平台的 bug/优化（那些走 todo）。
> 评估日期：2026-09-15。评估依据：全库静态走查 + 针对性耦合点调查（证据均附文件:行号）。

## 1. 动机与目标形态

目标：把现在"单 server 跑一切"的形态，演进为 **server（大脑）+ 若干 node（手脚）**：

- server：Web UI、LLM 调用（llmpool）、planner、审批流、探索图/资产图（PG）、调度器。
- node：只执行具体工具调用（bash/HTTP/扫描/投递），outbound 回连 server 领活，无需公网入站。
- 参考形态：市面上用云沙箱做执行节点的产品（server 下发任务、沙箱执行后回收）。但要看到本质差异——**那些产品做的是无状态一次性工具执行，ARTEX 做的是持续作战**：会话、隧道、监听器都是长期有状态资产，调度模型完全不同（见 §5）。

## 2. 现状：执行面与 server 本机的耦合点清单

当前 worker 不是独立进程：`Engine.Run` 在 server 进程内为每任务起 1 个 planner + N 个 worker goroutine（`server/engine.go:823-830`)，`worker.Execute` 是直接 Go 调用（`server/engine.go:1118-1120`)。所以"worker 挪到 node"等于把整条 agent loop 搬出进程，以下绑定会逐一断裂：

| # | 耦合点 | 证据 | 性质 |
|---|--------|------|------|
| 1 | 会话注册表是纯内存 map,worker 拿 session_id 由 server 侧查表解析 | `session/session.go:104-109`、`server/session.go:540-549` | 内存状态 |
| 2 | reverse listener(penelope）是 server 受管子进程，MCP 绑回环，会话活在该进程内存、重启不可恢复 | `server/reverse.go:276-286, 257-266, 80-88` | 本机进程 |
| 3 | 隧道管理器是 server 本机进程台账，端口池靠本机 `net.Listen` 试绑 | `tunnel/deploy.go:41-57, 137-144` | 本机进程+端口 |
| 4 | socks/MITM 地址写死 `127.0.0.1`，消费链 worker→任务 MITM→socks 全是 loopback 假设 | `server/tunnel.go:348`、`server/taskproxy.go:83`、`agent/worker.go:209-216` | 写死寻址 |
| 5 | 流量录制 MITM 在 server 进程内，CA 私钥在 server 磁盘，HTTPS 重签发生在 server；录制落本地文件树+SQLite，不过 PG | `server/taskproxy.go:132-167`、`traffic/traffic.go:141-190`、`agent/worker.go:242-249` | 本机进程+文件 |
| 6 | 审批阻塞-唤醒是纯内存 channel(`map[int64]chan bool`)，没有任何远程等待原语 | `intercept/intercept.go:86-116, 610, 641-679` | 内存原语 |
| 7 | 文件面全假设 worker 与 server 同文件系统：runDir 工作区、stage PutFile 直接读本地路径、transcript 续跑读本地文件、chat uploads | `agent/worker.go:323-330, 516`、`server/stage.go:195-228`、`server/server.go:444`、`server/chatupload.go:42-44` | 共享文件系统假设 |
| 8 | 工具二进制与"kali 工具链"= server 宿主机的本地命令环境 | `server/tunnel.go:35-46`、`server/reverse.go:216-224` | 本机环境 |
| 9 | engine↔worker 控制面（pause/kill/steer/deadline）基于进程内 `context.CancelCause` 与 channel | `server/engine.go:167-171, 410-435` | 进程内原语 |

**有利面（天然可共享，几乎零改动）**：探索图/资产/意图/事实/finding、sessions/tunnels 台账、intercept 规则与 pending 记录、settings、LLM profiles 全在 PG；意图认领已有 CAS(`ClaimIntent`,`server/engine.go:1333-1341`)——多节点抢活不会重。**这是 ARTEX 比多数同类更适合分布式的根本原因：唯一事实源已经是外部 PG，不是进程内存。**

**两条干净的抽象缝（改造的关键抓手）**：

- 所有工具收敛到 `actool.CoreTool.Call(ctx, json.RawMessage, ...)` 单一 JSON 进/出形状——天然的 RPC 边界。
- worker 只拿 `llm.Provider` 接口（`agent/worker.go:165`),key 在 server 内存装配（`server/llmpool.go:60-130`)——"LLM key 不出 server"在接口层天然成立，缺的只是一个 server 侧 LLM 代理端点。

## 3. 目标架构

```
            ┌──────────────────────────── server ────────────────────────────┐
            │  Web UI │ planner │ llmpool(key 不出进程) │ guard/审批 │ PG    │
            │  调度器:意图认领分发、node 台账、亲和路由、节点审批吊销          │
            └──────▲──────────────────────────▲───────────────────▲─────────┘
                   │ mTLS:工具调用+结果回传    │ mTLS:审批 long-poll│ mTLS:控制面
        ┌──────────┴───┐            ┌─────────┴────┐              │
        │  node-1      │            │  node-2      │        (更多 node)
        │ agent loop   │            │ agent loop   │
        │ bash/HTTP/扫描│            │ 武器库(manifest│
        │ 会话/隧道(亲和)│            │  钉版分发+校验) │
        └──────────────┘            └──────────────┘
```

职责切分原则：**大脑留 server、手脚在 node**。node 收到的是单条工具调用（一条 bash、一次 HTTP、一次投递），不回传 LLM 上下文全貌；LLM key、审批决策、探索图写入权全部不出 server。

## 4. 节点协议设计要点

- **传输与认证（安全基线，不可裁剪）**:TLS 1.3 + mTLS 双向认证。server 自签 CA(`jwt.key`/`gate.key` 同款落盘套路，`server/auth.go:46`);node 用一次性注册令牌换客户端证书，证书指纹出现在 UI"待批准节点"，人工批准才激活（TOFU)；吊销=删指纹。节点监听口独立于 Web 门控，不挂伪装页——mTLS 本身就是更强的隐匿（无客户端证书连握手都过不去，测绘扫到的是黑盒端口）。候选替代：WireGuard（内核级但沙箱节点密钥分发麻烦）、chisel 反连（静态密码粒度太粗，一票否决）。
- **工具调用面**：复用 `actool.CoreTool` 的 JSON 边界做 RPC。domain tools(`insert_assets/record_fact/add_intent` 等 30+ 个，现持有 `*db.*Store` 具体类型，`agent/tools.go:76-82`)**留在 server** 当平台服务，node 远程调用；执行类工具（Bash/Read/Write/WebFetch）在 node 本地跑。
- **控制面（隐藏大头，比工具 RPC 工作量大）**：意图认领（复用 PG CAS)、心跳、取消（替代进程内 `CancelCause`)、纠偏投递（steer)、deadline 收尾，需要一套明确的执行协议。
- **审批流**:pending 记录已在 PG（数据落点正确），把内存 channel 阻塞换成 node 对 server 的 long-poll/WS 等待；fail-closed 语义、ctx 取消传播、超时竞争（`intercept/intercept.go:644-679`）需逐项重验。guard hook 随意图下发到 node 侧执行，规则/RoE 远程读——**审批不下方就形同虚设，这是 M1 的硬前置**。
- **结果可信记账**:node 回传的每条结果带节点身份，落库记 `node_id`；某节点被控后可按 `node_id` 精确标记其产出存疑，而不是整个库不可信。

## 5. 调度与节点亲和（与"无状态沙箱"模式的本质区别）

会话驱动、隧道进程、socks 入口、reverse listener 都是**创建在哪个节点就死在哪个节点**的有状态资产。调度器必须：

1. 会话/隧道/监听器登记 `node_id`;
2. 依赖这些资产的后续意图（走隧道打内网、session_exec）**亲和路由回原节点**——否则 socks 地址根本够不到；
3. 节点掉线：其上有状态资产诚实标 dead（复用现有探活机制，`server/session.go:95-103` 的启动卫生可直接改造）;
4. 无状态工具调用（扫描、HTTP 探测）才可自由 round-robin 到任意空闲节点。

## 6. 文件面方案

工作区、stage 投递、transcript 续跑、武器库二进制现在都假设同文件系统。方案：

- **武器库分发**:node provisioning 时按 `packaging/tools-manifest.json`（期 6 已建，17 项钉版+sha256）分发并校验——这是现成的基础设施，分布式反而是它的主要受益场景。
- **工作区/产物**:node 本地工作区 + 产物按需回传 server（大文件走 stage 同款随机 token 通道）；transcript 续跑改为 server 侧存取（现在已是文件，挪成 PG bytea 或 server 文件 API 皆可，改动可控）。
- **stage PutFile 读本地路径**的假设要改成"node 上传→server 暂存→目标拉取"三段式。

## 7. 分阶段路线与工作量评估

| 阶段 | 内容 | 关键前置 | 工作量 |
|------|------|----------|--------|
| M0 | 详细设计文档（协议字段、亲和表结构、guard 下放方案、迁移兼容） | — | 小（纯设计） |
| M1 | 远程执行节点：`artex-node` 二进制，mTLS 注册，工具 RPC,guard 钩子下发，审批 long-poll | M0 | **大**（控制面协议是新设计） |
| M2 | 节点亲和调度：有状态资产登记 `node_id`、亲和路由、节点掉线标 dead | M1 | 中 |
| M3 | 弹性 sandbox:node 容器化镜像、按需开销、用完即焚回收 | M1+M2 | 中（主要是工程化） |

风险排序（来自 §2 耦合点难度）:engine↔worker 控制面远程化 > 隧道消费链 loopback 假设 > 流量录制下沉/回传 > 文件面 > 工具 RPC（缝已在）> PG 状态（零改动）。

## 8. 风险与开放问题

- **流量录制**:HTTPS 重签 CA 在 server;node 直连 server MITM（要网络可达+CA 分发）还是录制下沉 node 再回传（新管道）？倾向后者，但会动 `traffic/` 的落盘假设。
- **reverse 出口下沉？** 最省力是 session_exec/reverse 永远留 server(RPC 调用），目标侧命令出口仍在 server；要出口下沉 node,Registry/锁/penelope 单例都要重设计。建议 M1-M2 不下沉，M3 再议。
- **多 node 抢同一目标**的噪声与封禁风险：需要任务级并发上限策略（现在有 workers=6 的全局限流，分布式后要按"每目标"维度再限一道）。
- **node 被控的爆炸半径**：设计上 node 不持 LLM key、不持 PG DSN（只走 server API)、武器库按任务最小分发；但 node 必然能看到它执行的目标与部分战利品，单节点被控≈该节点参与的任务上下文泄露，这是分布式固有权衡，只能靠 §4 的 `node_id` 记账控制事后影响。

## 9. 结论

**可行，且底子好于多数同类**：状态已中心化（PG）、工具面已有 JSON 抽象缝、LLM key 边界已在接口层成立、武器库钉版清单已就绪。但这不是"加几个 API"的改造——真正的大头在 **engine↔worker 控制面协议** 和 **隧道/流量/文件三面写死的 loopback 假设**，估计 M1 一个里程碑的改动量就超过期 1-6 任何单期。建议在当前平台（单机版）完成靶场验证、功能稳定后再启动 M0。
