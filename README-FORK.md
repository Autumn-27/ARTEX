# ARTEX-dev（二开分支说明）

> 本文是 [Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX) 二次开发分支的说明，写给上游作者 review。
> 二开目标：把 ARTEX 从「外网自动化探索平台」扩展为「**外网突破 → 立足点 → 内网纵深**」的全流程自主渗透平台，同时补齐实战化运维与稳定性短板。
> 上游原 README 见 [README.md](README.md)（架构、安装、双图原理等不再重复）。

---

## 一、本分支与上游的关系

- 代码基线：上游 main（单体 Go 后端 + 内嵌 Next.js 前端 + PostgreSQL + norma SDK），未动上游核心架构（双图、planner/worker、审批门）。
- 所有新增能力以**独立子系统/独立包**方式接入（`session/`、`tunnel/`、`stage/`、`tools/`、`guard/` 新增文件、`server/` 新增端点文件），与上游代码的交汇面集中在装配点（`server/assembly.go`、`server/engine.go`、`agent/worker.go` 的工具注入），**上游后续迭代 rebase 冲突面可控**。
- 数据库：`db/schema.sql` 追加新表（`sessions`/`tunnels`/`credentials`/`pending_scope` 等），全部 `CREATE TABLE IF NOT EXISTS` 幂等，与上游「重启即迁移」机制一致，不破坏既有库。

## 二、二开新增能力（按子系统）

### 1. 内网作战子系统（最大的一块）

| 能力 | 说明 | 位置 |
| --- | --- | --- |
| Webshell 会话管理 | PHP/JSP 加密一句话会话的注册、心跳、执行；会话台账页（探活/工作台/移交） | `session/http_shell*.go`、`server/session.go` |
| 反弹 shell | penelope 集成为受管子进程，`reverse_listen`/会话化管理，自带加密与维权 | `server/reverse.go` |
| 被动侦察 | `session_recon`：经立足点执行只读命令包（ip/route/neigh/netstat），解析落资产图、推断高价值网段 | `server/session_recon.go`、`recon/` |
| 多层代理隧道 | `tunnel_deploy/probe/list/teardown` 四件套；suo5（目标无出网）/chisel（能出网）自动选型；隧道状态变化**热切换任务级 MITM 上游**，后续 worker 流量自动走隧道 | `tunnel/`、`server/tunnel.go`、`server/taskproxy.go` |
| 内网拓扑 | 「内网作战」页：主机/网段/存活链路力导向图 + 会话/隧道/凭据三台账 | `web/src/app/(main)/intranet/` |
| 凭据库 | 凭据一等实体（捕获、来源血缘、复用） | `db/credentials.go` |

### 2. 阶段编排（外网 → 内网两段式）

- **待授权网段**：侦察发现 scope 外新 IP 时按 /24 聚合写入 `pending_scope`（幂等去重、活动流通知），任务详情页一键「批准扩 scope / 忽略」；批准即写入 `task_scope`，guard 现查即时生效。——把「能不能打这个网段」的决定权留给人。
- **外网→内网任务移交**：`POST /api/sessions/{id}/handoff` + 会话台账「移交内网」按钮，按模板创建子任务（继承源任务资产、初始 scope 仅立足点 /32、纪律写入 goal）。
- **内网 worker 提示词变体**：任务存在活隧道/存活会话时自动切换 `worker.intranet`（被动优先、隧道四件套、新网段先审批），关键纪律放代码固定尾，不可被 DB 编辑覆盖。

### 3. 安全与反测绘

- **伪装门控**（`server/gate.go`）：未过门控的请求（SPA/API/SSE/错误页）一律返回逐字节固定的 nginx 默认欢迎页，随机入口路径 + HMAC 签名 cookie + 每 IP 限速，防 fofa/shodan 指纹匹配。
- **一键放行**（`intercept/autoallow.go`）：内网阶段审批疲劳的解法——2/4/8h 时限内 ask 审批自动放行（deny 不豁免），UI 明示危害，到期自动恢复。
- RoE 范围强制三态（off/warn/strict），strict 模式复用 ask 审批流。

### 4. 武器库与部署链

- `packaging/tools-manifest.json`：17 项外部工具（gogo/naabu/httpx/katana/fscan/impacket/nxc/mimikatz/pypykatz/laZagne/ligolo/peass/penelope/suo5/chisel）的钉版清单，启动自检 sha256（不匹配只警告），未确认哈希一律留空不编造。
- `artex doctor`：7 项部署预检（PG/LLM/data 可写/隧道工具/军火库/门控/回连地址），FAIL 退出码 1，只读不启动服务。
- systemd unit（崩溃重拉、开机自启、与页面一键更新的退出码 75 换装兼容）。

### 5. 稳定性（实战中踩出来的）

- **deadline 冻结感知**：宿主机睡眠/VM 冻结（时钟跳变 >60s）时自动顺延任务 deadline，不再出现"睡一晚任务全烧死"。
- **LLM 调用硬墙钟**：单次 LLM 调用 600s 上限（`ARTEX_LLM_CALL_TIMEOUT` 可调），超时归类为可转移瞬时失败，planner/worker 不再被挂死的连接拖住。
- **会话探活**：手动探活按钮 + 启动卫生（陈旧 alive 会话异步并发探活，失败诚实标 dead）。

### 6. chains 场景化激活

- 反问思维链骨架（web/ad/app/priv/pwn/recon/rev/pivot/meta 九类）按意图 `chain_tags` 自动注入 worker 上下文，上限 2 类 8K 字符（`agent/chainskel/`）。
- **按漏洞类别的反证判据**（VerifierChecklist）：验证 finding 前先跑该类别的证伪检查，误报写 fact 回探索图、真漏洞才落 finding。
- **类别化转向提示**（PivotHints）：worker 停滞时按当前链条类别给出下一步候选方向。

## 三、验证情况（诚实版）

- **实战验证**：红日靶场 3（外网 Web → Linux 跳板 → Windows 成员机 → 域控）全流程通关，拿到域控 flag。诚实声明：域管口令为人工投喂一次，完整 A/B 对比与复盘见 `AB-REPORT-RED-SUN-3.md` / `POSTMORTEM-RED-SUN-3.md`。
- **部署验证**：Ub 18 + PG10 实体机连续运行；systemd 托管；`artex doctor` 全绿；启动自检 sha256 一致 14/14。
- **测试**：`go build/vet` 全过；`agent`、`db`、`tools`、`guard`、`intercept` 等包测试全过；`server` 包有 4 个既有环境性失败（缺 PG DSN ×3、Windows 路径分隔符 ×1），与改动无关。
- **已知不足**：① 效果强依赖 LLM 质量（弱模型会钻牛角尖/死磕错误凭据）；② 误报控制虽有反证判据+复测 agent，仍需更多靶场样本调优；③ 内网阶段动静控制与免杀未系统化；④ 测试环境样本有限，存在过拟合风险，功能均按通用场景实现但未经充分泛化验证。

## 四、给上游作者的请求

1. **希望开一个分支**（如 `dev-intranet`）承载本二开，便于 review 与协作。
2. 可拆分回馈上游的通用改进（与内网特性解耦、可单独成 PR）：systemd unit、LLM 调用墙钟、伪装门控、一键放行、`artex doctor`、工具钉版清单、会话探活/启动卫生。
3. 内网作战子系统体量较大，若上游有兴趣合并，建议按「会话管理 → 隧道 → 拓扑 → 阶段编排」的顺序分期 review；架构上已评估过分布式演进（server 大脑 + 远程 node 执行），见 `FUTURE.md`。

## 五、联系与许可

- 二开维护者：（待补充）
- 许可：遵循上游「仅供授权测试与研究使用」。
