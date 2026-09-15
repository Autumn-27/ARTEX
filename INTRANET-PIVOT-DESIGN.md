# ARTEX 原生内网渗透能力设计(两阶段作战编想)

> 日期:2026-09-13。状态:**方向已确认**(作者提出,经评估可行)。
> 前置阅读:`PIVOTHUB-AUDIT-AND-FUSION-EVALUATION.md`(PivotHub 原理与可借鉴点)、`AUDIT-REPORT.md`、`CHAINS-INTEGRATION-DESIGN.md`(chains 四层架构)。
> 定位:**不做系统对接**。参考 PivotHub 的工作原理/工作过程,在代码层面为 ARTEX 增加原生内网渗透能力,实现"外网突破 → 立足点移交 → 内网纵深"两阶段自主作战。

## 1. 作战编想(作者原意)

- **阶段一(外网突破)**:现有 ARTEX 形态。planner 派 worker 群打外网 Web 面,目标是拿到 shell / webshell 立足点。
- **移交**:立足点(webshell/反弹 shell/SSH)注册为平台管理的**会话资产**,内网侦察发现新网段,经**人工授权检查点**确认范围扩展。
- **阶段二(内网渗透)**:后方另起一波 agent,经立足点建多层代理隧道,以"网络位置前移 + 目标侧执行"双模式打 L2/L3 内网,凭据收割与复用驱动横向,直至目标达成(如域控)。

## 2. 可行性结论(证据)

**能做到。原因是编想的每个环节在 ARTEX 已有机制里都有同构映射:**

| 编想环节 | ARTEX 现成机制 | 证据 |
|---|---|---|
| 立足点产生后自动移交 | `agent_triggers` 触发器(on_finding / on_tool_call),Scheduler 水位线轮询后起会话 | `server/scheduler.go:152-182,284-320` |
| "后方另起一波 agent" | 关联任务 + 黑板继承机制(已有专门测试) | `agent/blackboard_inheritance_test.go` |
| 新态势唤醒规划 | fact 写回 → debounce 唤醒 planner → 派新意图 | `agent/worker.go:362` notifyFinding |
| 内网知识(怎么横向/怎么选隧道) | chains pivot-\*(5)/ad-\*(10)/priv-\*(7),四层接入设计已定稿 | `CHAINS-INTEGRATION-DESIGN.md` L3 路由 |
| 内网操作的审批与留痕 | guard PreToolUse 钩子 + activity 全程记录,会话类工具调用天然被覆盖 | `guard/guard.go:103-124` |
| 隧道消费端 | `SetUpstreamProxy` 支持 socks5 带认证、原子热切换 | `traffic/traffic.go:205-233` |
| 交互式 shell 设施 | `shell_open` 已明确面向 "ssh 交互/nc 反弹" 场景 | `server/assembly.go:238,312-314` |

**真正缺失、必须新建的只有三块**:① 会话子系统(立足点的一等管理);② 隧道编排子系统(探测→选型→部署→验证→健康→拆除);③ 数据模型的内网扩展(host 归并/网段/链路/凭据)。这三块正是 PivotHub 验证过的三样东西——**借鉴其算法与判据,Go 重写其实现**。

## 3. 从 PivotHub 借鉴什么(原则,不是代码)

| PivotHub 原理 | 证据 | ARTEX 采纳方式 |
|---|---|---|
| 会话唯一出口:一切目标侧命令经 `SessionBase`(test/exec/file ops) | `pivothub/session/base.py:164` | 新建 Go `session` 包,同形接口;所有驱动实现同一抽象,agent 只看到"会话工具" |
| 出网探测四探针 → 证据分档选型 | `service/probe.py:262-291,432-470` | 探测作为**会话上的工具**实现,报告结构化写回 fact,选型规则进 chains L3 骨架(pivot-tunnel-choice 合流) |
| 多级中继自动推导(双网卡取第二段、找 alive 上游、三步命令) | `service/relay.py:31-135` | 同算法 Go 重写;**修正其缺陷**:上游活性必须真实探活(PivotHub 只信 DB status,`relay.py:49`),部署参数完整持久化以便自动重拉(PivotHub 未存 shellId) |
| 隧道内决定性验证:本机通不算通,跳板侧实测才算 | `adapters/chisel.py:449-453` | 直接采纳为 deploy 的必要步骤;与验证 agent 的反证思想同源 |
| 失败逐层回滚不留孤儿 | `adapters/chisel.py:489-509` | 直接采纳为 deploy 事务语义 |
| filestage:随机 token + 15min TTL + 字节数校验 | `service/filestage.py:271-279,420` | 直接采纳为隧道二进制投递机制 |
| 提权规则 verify/expect:"无验证步骤永不可标已执行" | `service/privesc.py:154-158` | 规则 JSON 格式可直接复用,匹配引擎 Go 重写,规则数据并入 chains |
| 哨兵同步、引号规避、断线诚实 | `session/reverse.py:42-52`、`base.py:34-61` | 实现会话驱动时照抄这些血泪细节 |
| 心跳只播状态不探活(缺陷) | `app.py:41-46` | **反面教材**:ARTEX 的链路健康必须真实探活 |

## 4. 架构设计

### 4.1 总览:新增三个子系统 + 两处现有机制扩展

```
┌─ 阶段一(现状)─────────────┐   移交    ┌─ 阶段二(新增)────────────────┐
│ planner + worker 群        │ ───────► │ 内网任务(关联任务,继承黑板)   │
│ 外网 Web 渗透              │ 立足点   │ planner + worker 群           │
│                            │ 注册会话 │  ├─ session 工具(目标侧执行)  │
└────────────────────────────┘          │  ├─ tunnel 工具(网络位置前移) │
                                        │  └─ 凭据库(收割/复用/喷洒)   │
                                        └───────────────────────────────┘
新增子系统:session/(会话抽象与驱动)  tunnel/(探测·选型·部署·健康)  数据模型扩展(host/网段/链路/凭据)
扩展现有:proxyEnv 按任务注入(取代全局唯一代理)  guard 增内网专项规则  chains L3 路由 pivot/ad/priv
```

### 4.2 子系统一:session(立足点一等管理)

- **接口**:`Session { Test / Exec / ExecStream / ReadFile / WriteFile / Upload / Close }`,驱动:http_shell(php/jsp/aspx/asp)、reverse(监听器+回连登记)、ssh(用 `golang.org/x/crypto/ssh`——go.mod 已有 x/crypto 依赖,`go.mod:14`)。
- **落库**:新表 `sessions`(kind/host_asset_id/连接参数加密/health/last_beat/created_by_intent),启动恢复监听器(PivotHub 的 `restore_reverse_listeners` 模式,`api/shells.py:929-950`)。
- **agent 触达面**:新增工具 `register_session`(worker 拿到 webshell 后登记:URL+密码+语言)、`session_exec`、`session_read/write`、`session_list`——**全部是普通工具调用,自动过 guard 审批、自动进 activity 留痕**,这是相对 PivotHub 通道的本质优势(回答 fusion 评估的"问题一")。
- **攻击机地址概念**:反弹回连需要"目标能连到的 ARTEX 地址"(PivotHub 的 attackIp,README §9)。ARTEX 跑在 Docker 时这是真实部署约束:需要配置项 `ARTEX_CALLBACK_ADDR` + compose 暴露监听端口,启动时校验可达性并显式告警。
- **安全底线**(PivotHub 的教训):回连监听必须带一次性口令校验(其 `_accept_loop` 来者不拒,`reverse.py:140-149`);会话连接参数加密落库(其全明文,`models/credential.py:18`)。
- **立足点策略(foothold ladder,2026-09-13 与作者确认)**:**webshell 优先,反弹 shell 降级为临时通道**。理由:① webshell 复用入站 HTTP 通道,零出网依赖(反弹的前提"目标能连回"恰是最常被拦的,见 chains `pivot-no-reverse`/`pivot-oneway-webshell`);② 反弹长连接+非常用端口流量特征高,webshell 混在业务 HTTPS 里;③ http_shell 会话请求驱动,平台侧零长命进程——**session 层由此从 ROADMAP C1 冲突中摘出**,反弹监听不再是必须豁免的长寿资源(C1 仅剩隧道这一本质长命资源,受管台账兜底);④ webshell 命令经录制代理发出时**双重留痕**(工具调用进 activity + HTTP 进 MITM),反弹裸 TCP 完全绕开录制体系。标准动作链:初始 RCE → 优先写 webshell 建立持久立足点(主通道)→ 需要交互性(PTY 提权/大文件/隧道调试)时临时拉起反弹/交互通道,**用完即收(受管 TTL,不常驻)** → 隧道经主通道投递。红线:写 webshell 是高危写操作,必须过 RoE/guard ask;webshell 落盘有被查杀引擎发现的检测面,chains 应补内存马/落地混淆判据。判据知识由 chains L3 pivot-\* 骨架承载。
- **implant 策略(2026-09-13 与作者确认)**:不接入外部 webshell 平台(vshell/哥斯拉/冰蝎)——双体系并存且协议已被签名库覆盖,接入=付自研的钱吃不到自研的肉;不现在自研完整 C2——那是按月计的对抗工程,当前平台缺口在内网纵深而非 implant 对抗,决策推迟到期 3 隧道跑通后用真实数据再定(session 接口驱动无关,未来加二进制 implant 不动上层)。**近期动作:给 http_shell 驱动加自研加密马型(排期在期 2 之后,2-3 天)**——马体内置解密、每会话独立 AES-GCM 密钥、请求/响应全密文+随机填充,杀流量签名;"功能单一"由加密马+隧道(期 3)+交互通道(期 5)的组合解决,不依赖巨型 C2。

### 4.3 子系统二:tunnel(多层代理编排)

- **四件套**:`probe`(经会话在目标侧执行四探针,结构化报告)→ `plan`(中继推导,输入是资产图里的网卡/网段数据)→ `deploy`(经会话投递 chisel 二进制=filestage 模式,远端拉起,**隧道内决定性验证**,失败逐层回滚)→ `health`(真实探活,死了标 error 并触发重拉——重拉用持久化的部署参数,不像 PivotHub 名存实亡)。
- **agent 触达面**:`tunnel_probe / tunnel_plan / tunnel_deploy / tunnel_list / tunnel_teardown` 工具。plan 输出结构化 hops(JSON),planner 可据此派"打下一网段"的意图。
- **隧道消费**:tunnel 建成 = 一个 socks5 端点。worker 的 `proxyEnv` 注入从"全局唯一 globalProxy"扩展为**按任务(或按意图)绑定上游**(`agent/worker.go:194-214` 改为从任务 runtime 取):worker 的 Bash 流量 = 本地 MITM 录制代理 → socks5 隧道 → 内网目标,**留痕链不断**(本地 MITM 仍然录),这是架构上最漂亮的一点——录制与隧道在拓扑上天然串联。
- **进程看护**:隧道进程登记台账,settlement/任务停止时清理(修掉 P6 发现的"worker 起的后台进程无人管"问题);断链真实探活 + 自动重拉 + planner 通知(链路断是重要态势事件)。

### 4.3a 受管资源原则与 stage_share(2026-09-13 新增;回应"AI 起临时 HTTP 服务投递武器后忘关导致泄露"通病)

**架构原则:凡带外部暴露面的资源——HTTP 暂存、隧道进程、反弹监听、临时防火墙规则——必须由平台登记台账、强制 TTL、归属任务、平台回收。没有例外资源。** 根因不是 AI 记性,而是资源在平台账本之外:临时 HTTP 服务是一条 Bash 命令的副作用,平台零感知,且 settlement 不清后台进程,意图结束/上下文压缩后即成对全网开放的孤儿(目录常混有战利品凭据)。prompting is not enforcement,必须用机制:

1. **`stage_share` 受管投递服务**(内置工具,与隧道二进制投递共用同一暂存实现,即 4.3 的 filestage 模式):24 字节随机 token 路径、无目录列举、硬 TTL 默认 15 分钟到期自动销毁、支持一次性下载即焚、默认 loopback(需对目标可见时绑指定接口或走隧道)、全程 activity 记账(谁投递/何时过期/被何源 IP 拉走)。
2. **guard 内置不可删除规则**拦截 `python -m http.server` / `php -S` / `busybox httpd` / `npx serve` 等野路子,拦截理由写明"请改用 stage_share"——正道比野路子好用,绕过动机自然降低。
3. **兜底收割**:worker Bash 跑独立进程组,意图结束/任务停止整组收割;定期扫本机监听端口,台账外监听者告警并可配自动 kill。
4. **投递分级**:`stage_share` 只往外送工具;战利品/凭据回流只走会话读取/evidence 存储;可选 payload 加密投递。

对应修复方案条目:`FIXPLAN-AND-EVALUATION.md` F13。实施纳入期 1(session 子系统同期,共享受管资源台账框架)。

### 4.4 子系统三:数据模型内网扩展(最大的手术)

P6 的证据:当前 `uq_av2_ip` 使一个 IP 全局一行(`db/schema.sql:84`),双网卡主机拆成两条互不相干的资产;边关系只有 5 种(`db/schema.sql:251`)。需要:

- **host 实体**:新表或资产新类型 `host`,多个 ip/nic 资产归并其下(nic 表:ip+segment+iface);双网卡归并靠"同一会话上报的 ifconfig"或 MAC/主机名指纹。
- **网段实体**:`segments`(cidr,经哪条链路可达)——目前只有派生列 `c_segment`(`db/schema.sql:55`)。
- **链路边**:资产层新表 `links`(kind=socks/portfwd/relay, src_host/dst_segment, adapter, state, deploy_params)——不要塞进 exploration_edges,链路是跨任务的全局事实,属于资产图。
- **凭据一等实体**:新表 `credentials`(type=password/hash/ticket/key, secret 加密, source_host, scope 推断);复用打分用程序规则(PivotHub 前端打分 naive,`cred.js:44-50`,ARTEX 应做服务端+LLM 双轨);**喷洒/爆破动作必须过 guard ask 规则**(RoE 硬约束)。
- **覆盖度统计扩展**:分母从"task_scope 命中资产"扩展为"已发现网段中的存活资产",covered 定义增加"经会话/隧道实测"。

### 4.5 阶段编排(两阶段怎么切)

1. 阶段一照常。worker 拿到 webshell → `register_session` → 写 fact("立足点@hostX")+ finding。
2. fact 唤醒 planner;planner 经 chains pivot 骨架(L3 路由,`CHAINS-INTEGRATION-DESIGN.md`)得知"先侦察再扩线":派 `session_exec`(ifconfig/arp/路由)侦察意图 → 新网段资产入库。
3. **人工授权检查点(硬性)**:发现新网段 ≠ 有权打新网段。内网 RoE 与外网几乎必然不同,任务进入"内网阶段"应由 guard ask + 人工确认后登记新 scope(`db/task_scope.go`)——这与修复方案 F5(代码层 RoE 强制)是同一个闸门,**F5 因此从"建议"变为内网功能的前置依赖**。
4. 确认后创建**关联任务**(继承机制已存在):scope=内网段,worker 工具面增加 session/tunnel 工具,proxyEnv 绑隧道,提示词换内网词汇(chains pivot/ad/priv 经 L3 路由按意图注入),planner 用 pivot/priv/ad 骨架规划纵深。
5. 凭据收割 → 凭据库 → 复用打分 → 横向意图(ssh/wmi/psexec 经 session 或隧道)——循环直至 prove_goal。

### 4.6 提示词与 chains 的角色

- worker 内网形态提示词:目前全是 Web 语汇(`agent/worker.go:220-233`),需要内网变体(横向、隧道、凭据、OPSEC)。
- chains L3 路由(`CHAINS-INTEGRATION-DESIGN.md`)按意图 tag 注入 pivot-tunnel-choice / priv-linux / ad-kerberos 骨架——**内网功能是 chains 设计的首要受益场景**,两个项目互为前提。
- L1 代码判据在内网同样适用(同质响应→WAF/蜜罐识别;蜜罐在内网更常见,chains 里 meta-trust-obs 有对应判据)。

## 5. 实施分期(建议)

| 期 | 内容 | 依赖 | 量级 |
|---|---|---|---|
| 0 | 修复方案 P1(guard fail-closed、RoE 代码强制 F5)——内网功能的安全前置 | 无 | 2-3 天 |
| 1 | session 子系统:http_shell(php 先行)+ ssh 驱动、register_session/session_exec 工具、sessions 表、guard 覆盖验证 | 期 0 | 1-2 周 |
| 2 | 凭据一等实体 + 复用打分 + session 内网侦察意图(ifconfig/arp/路由解析,PivotHub `recon.py` 的解析器可移植) | 期 1 | 1 周 |
| 3 | tunnel 子系统:probe/plan/deploy(chisel 先行)/health/台账清理;proxyEnv 按任务绑定 | 期 1-2 | 1-2 周 |
| 4 | 阶段编排:授权检查点 + 关联任务模板 + 内网 worker 提示词 + chains L3 pivot/ad/priv 路由 | 期 3 + chains ETL | 1 周 |
| 5 | reverse 驱动 + 反弹监听(含一次性口令)+ `ARTEX_CALLBACK_ADDR` 配置 | 期 1 | 3-5 天 |
| 6 | 原生部署链:release 附带 chisel(钉版+sha256 校验)、`artex doctor` 预检、重依赖可选降级 | 期 3 | 2-3 天 |

总计约 5-7 周。期 1 完成后 ARTEX 即具备"立足点管理+目标侧执行";期 3 完成后具备"多层代理纵深";期 4 完成后才是完整编想。

## 7. 实施进度记录

- 期 0(安全前置):**完成**(F3/F4/F5 guard fail-closed、planner/mainagent hooks、RoE 三态强制 off/warn/strict,已上线)。
- 期 1(session 子系统):**完成并验收**——http_shell 驱动(eval/assert/JSP/ASPX 多马型+函数降级)、sessions 表(secret AES-GCM)、register_session/session_exec/read/write 工具、RoE 登记校验;任务 #9 端到端通过(打 CVE→写马→登记→目标侧执行 root 命令)。
- 期 2(凭据+侦察):**完成**——credentials 表、服务端复用打分、被动侦察解析器(ip/ifconfig/route/arp/netstat/Windows 变体)、session_recon 工具、v4 横向喷洒内置 ask 规则。
- 加密马型(2026-09-13 implant 策略落子):**完成并验收**——AES-256-GCM/CBC+HMAC 双路径、随机填充、每会话独立密钥、generate_webshell/register_session(phpenc/jspenc);任务 #11 端到端通过。
- 期 3(tunnel 子系统):**完成并验收**(任务 #12-16)——四件套 probe/plan/deploy/health、按任务 MITM 上游绑定(taskproxy)、portfwd 与 socks 双形态 alive+teardown 干净。实战修掉 6 个 bug:① hostTools 漏挂 tunnel 工具;② authfile 须为 JSON users 格式;③ chisel client 选项必须先于 server/remote;④ 分块 512KB→64KB(Linux MAX_ARG_STRLEN=128KB);⑤ probe 三态(refused 算通)+端口池采样;⑥ socks 验证目标在 NAT/端口映射下失效→自环候选(callbackHost:listenPort)。另有 v5 裸隧道二进制内置 ask 规则(含 grep 文本误伤修正)。
- 期 5(reverse 反弹 shell handler):**完成**——受管 penelope 进程(`server/reverse.go`:`--mcp` 仅绑 127.0.0.1 + 随机 Bearer token、`-C` 无 TTY 不 attach、自动 PTY 升级保留、会话日志落盘 dataDir/reverse/),单例懒起 `EnsureHandler`、进程退出即台账清理(端口注销 + 会话诚实标 dead)、平台退出回收进程;MCP 桥(mcphttp 客户端)把 penelope 会话对账进 sessions 台账(kind=reverse,secret 存 penelope 会话标识+token,url=reverse://源地址)并注册 Session 适配器进 Registry(Exec=exec_in_session、Read=download_from_session 读回本地落盘、Write=upload_to_session 经本地暂存,remote_path 必须目录——实测语义);reverse_listen 工具(默认绑 worker)返回监听口+bash/python/nc payload(host=ARTEX_CALLBACK_ADDR);监听默认绑 127.0.0.1,目标直连需显式 iface。死亡重生:会话消失→台账标 dead,重生交给下次 reverse_listen。实测结论(penelope MCPServer):POST / 单端点、Bearer hmac 认证、响应为普通 JSON(非 SSE)、无 Mcp-Session-Id、6 工具(list/get_info/exec/kill/upload/download)、结果均为 text block 内嵌 JSON、文件传输需持久 shell(自动 PTY 满足)、死会话=JSON-RPC -32602 且从 list 消失。
- 期 3.5(suo5 隧道适配器,2026-09-13 新增并实测):**完成**——`tunnel/suo5.go`:按 session 马型选 payload(php/jsp/aspx,加密马用明文 payload)→ session.WriteFile 随机名投递到马目录 → 平台直连探 URL → `suo5 -t <url> -l 127.0.0.1:<port> --auth <u>:<p>`(auth 必启用)→ 决定性验证(带 auth 经 socks CONNECT 宿主自身);失败逐层回滚,deploy_params 全量持久化供自动重拉,health/Teardown 全接入。选型:auto=probe 驱动(TCP 通→chisel,其余→suo5);suo5 仅 socks,portfwd 强制 chisel;taskproxy 上游 URL 内嵌 user:pass;guard 规则把 suo5 纳入(平台 Go exec 天然豁免,worker 裸用照拦)。实测(Apache+mod_php):握手 3s、socks 数据面通、auth 生效;注意 php -S 单线程撑不起 half 模式(目标侧环境属性,已在注释标明)。
- 验收环境现状(VPS):DVWA(8081)、Tomcat CVE-2017-12615(8082)、内网站点(172.17.0.1:18090)、chisel 1.11.3 已置 data/tools;ARTEX_CALLBACK_ADDR=172.18.0.1(容器回连用,生产按实际拓扑配)。

## 6. 风险与诚实声明

1. **工作量大头在工程质量,不在算法**。PivotHub 证明了算法不难(中继推导百行级),难的是会话/隧道在生产对抗环境的健壮性(哨兵、编码、断线、回滚)——PivotHub 的这些细节是用事故换来的,重写时直接继承其注释里的教训,不要重新交学费。
2. **部署形态:原生二进制为内网场景推荐形态(2026-09-13 更新,取代原"Docker 硬约束"判断)**。反弹回连、隧道服务端要求"目标能连到 ARTEX"——原生部署下监听直接绑宿主机真实网卡,callback 地址=本机地址,约束基本消失;Docker 下则需 compose 新增端口映射 + callback 地址配置,云/NAT 场景可能不可用。ARTEX 本就支持单二进制原生部署(go:embed 前端 + start.sh 守护 + 自更新),摆脱 Docker 需补三件事:① 内网关键二进制(chisel 先行)打进 release zip,钉版本+记录 sha256+启动校验(借鉴 PivotHub `tools/` 做法但补齐其缺失的供应链校验);② 新增 `artex doctor` 预检(PG 可达/工具在位/callback 配置/CA 就绪,缺什么打印安装命令);③ 重依赖(playwright/浏览器 MCP)可选降级,探测不到即从工具清单隐藏。隔离代价要诚实:原生运行=LLM 生成的命令直跑宿主机,必须专用渗透 VM + 非 root + 期 0 的 guard 闸门兜底;唯一硬外部依赖仍是 PostgreSQL(`config/config.go:137-149`,PG 特性深度使用,不建议换库)。
3. **内网阶段 blast radius 质变**。外网 Web 渗透失手是打坏一个站点,内网横向失手(喷洒锁死全域账号、SMB 打爆终端)是业务事故。期 0(RoE 代码强制 + guard fail-closed)不是可选项;喷洒/爆破类命令必须有不可删除的内置 ask 规则。
4. **双模式并存是现实**:能建隧道走隧道(kali 工具全可用),目标不出网时只能靠会话执行(工具受限)。planner 的选型判据直接来自 chains `pivot-tunnel-choice.md` + PivotHub probe 规则——这正是两者合流的价值点。
5. **不要试图一步到位做阶段 4 的模型大改后又回头改**。建议期 1-2 先跑通单立足点场景(靶场,可直接借 PivotHub 的 `scripts/lab` 三层靶场做验收环境——它端口全绑 loopback、双网卡 L2/L3 纵深,是现成的验收靶场),再按真实反馈定模型扩展的粒度。
