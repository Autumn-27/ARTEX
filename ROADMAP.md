# ARTEX 需求总纲与实施路线图(ROADMAP)

> 日期:2026-09-13。本文汇总 2026-09-12~13 全部讨论成果,是五份专题文档的索引与实施总纲。
> 专题文档:`AUDIT-REPORT.md`(审核)、`FIXPLAN-AND-EVALUATION.md`(修复方案 F1-F14 + 需求一/二评估)、`CHAINS-INTEGRATION-DESIGN.md`(反问思维链四层架构)、`PIVOTHUB-AUDIT-AND-FUSION-EVALUATION.md`(PivotHub 审核与融合评估)、`INTRANET-PIVOT-DESIGN.md`(内网渗透能力设计)。

## 1. 需求清单与可实现性总评

| # | 需求 | 出处 | 可实现性 | 工程量 | 前置依赖 |
|---|---|---|---|---|---|
| 1 | 高危缺陷修复(F1-F14) | FIXPLAN Part 1 | ✅ 全部可修 | ~2 周(P0-P4)+ F13/F14 各 1-2 天 | 无 |
| 2 | 同入口 findings 合并/去重 | FIXPLAN 需求一 | ✅ 可做,挂载点干净(`db.RecordFindingTx`) | 档 A 1-2 天;档 B(语义判重,可选)+2-3 天 | 需求 3(先验证后合并) |
| 3 | 反证验证 agent(占位→验证→落图) | FIXPLAN 需求二 | ✅ 可做,地基好(status 枚举/dismissed/trigger/retester 模板全现成) | 方案甲 1-2 天;与 retester 管线合一 +1-2 天 | 无硬依赖;chains L3 可强化其反证 checklist |
| 4 | 反问思维链(chains)接入 | CHAINS-DESIGN | ✅ 可做,四层架构已定稿(L1 代码判据/L2 铁律常驻/L3 骨架路由/L4 原文兜底) | ETL 0.5 天;L4 skills 化当天;L2 蒸馏数天(人工);L3+L1 1-2 周 | 无硬依赖 |
| 5 | PivotHub 融合 | PIVOTHUB-EVAL | ⚠️ 重新定义为"原理借鉴,不做系统对接" | —(已并入需求 6) | — |
| 6 | 原生内网渗透(两阶段编想) | INTRANET-DESIGN | ✅ 可做,但**是最大工程**:session/tunnel/数据模型三块新建 | 期 0-6 共 5-7 周 | F3/F4/F5(期 0 安全前置)、需求 4 的 chains L3 |
| 7 | 摆脱 Docker(原生部署) | INTRANET 期 6 + 风险 2 | ✅ 可做,本就是原生支持,补齐部署链即可 | 2-3 天 | 与需求 6 期 3 同步价值最大 |
| 8 | 临时 HTTP 服务忘关泄露(受管资源) | FIXPLAN F13 + INTRANET 4.3a | ✅ 可做,有 PivotHub filestage 现成范式 | 2-3 天 | 无;纳入内网期 1 的受管资源框架更经济 |
| 9 | 反测绘伪装门控 | FIXPLAN F14 | ✅ 可做,纯中间件 | 1-2 天 | 无 |

**结论:9 项全部可实现,没有一项被判不可行。** 差异只在成本、依赖顺序与风险。三句话概括判断:

- **需求 2/3/4 全部落在现有架构的"设计引力"上**(触发器、retester 模板、skills、status 枚举),是低垂果实;
- **需求 1(修复)是一切的地基**,尤其 F3/F4/F5(guard fail-closed、审批覆盖 planner/mainagent、RoE 代码强制)是需求 6 的法定前置;
- **需求 6 是唯一的大工程**,它的价值上限取决于需求 4(chains 给判据)和需求 1(闸门给安全)的完成度。

## 2. 依赖关系(为什么是这个顺序)

```
F1-F14 修复(地基)                    chains ETL(0.5 天,最先做)
  │                                    │
  ├─ F3/F4/F5 ──────────────┐          ├─ L4 skills 化(当天见效)
  │                          ▼          ├─ L2 meta 蒸馏 → planner/worker system
  ▼                          │          └─ L3 骨架 + chain_tags 路由 ──┐
验证 agent(需求 3)           │                    │                  │
  │(先验证)                  │                    ├─ 验证 agent 的反证 checklist
  ▼                          │                    │                  │
findings 合并(需求 2)        ▼                    ▼                  ▼
                     内网渗透 期 0-6(需求 6)◄── chains L3 pivot/ad/priv
                          │
                          ├─ 期 1 含 stage_share 受管资源框架(需求 8 并入)
                          └─ 期 5/6 原生部署链(需求 7 并入)
F14 伪装门控(需求 9):独立,随时可做,VPS 公网部署前必须做
```

关键依赖逻辑:

1. **需求 2 必须在需求 3 之后**:合并只作用于 `status='confirmed'` 的 finding,否则误报会污染聚合键(FIXPLAN 需求一)。
2. **内网期 0 = F3/F4/F5**:内网横向的 blast radius 是业务事故级,RoE 代码强制与 fail-closed 不是可选项(INTRANET §6.3)。
3. **chains L3 是内网 planner 的知识来源**:pivot/ad/priv 共 22 篇骨架没接入前,内网 agent 是"有枪无弹"(INTRANET §4.6)。
4. **需求 8 提前做不难,但纳入内网期 1 更经济**:受管资源台账框架一次成型,stage_share/隧道进程/反弹监听共用。
5. **F14 与其他一切正交**:它是入站伪装层,VPS 公网部署的话越早越好。

## 3. 统一路线图(按优先级,可逐阶段交付)

### 阶段 0:地基加固(约 2 周,单人)
F1/F2(监听收敛+代理鉴权,1 天)→ F3/F4/F5(guard fail-closed+审批全覆盖+RoE 强制,2-3 天,**内网前置**)→ F6/F7(会话凭证,2 天)→ F8/F9(签名+CI 门,3-4 天)→ F10/F11(注入标注+数据治理,2-3 天)→ F12(顺手项)。F14 若 VPS 公网部署插队到最前。

### 阶段 1:数据可信度 + chains 快速收益(约 2 周)
1. chains ETL 解析脚本(0.5 天,全部后续工作的前提);
2. chains L4:9 个 skill 目录 + 索引(当天见效);
3. 验证 agent 方案甲(1-2 天)+ 与 retester 合并为 finding_checks 管线(1-2 天),verifier 提示词引用 chains 判定章节;
4. findings 合并档 A(1-2 天)+ 手动拆分操作;
5. chains L2:meta 六篇蒸馏(人工,数天,作者主导)→ planner/worker system。

**阶段 1 交付后**:误报率下降、重复漏洞聚合、agent 有内网/各漏洞类的判据可查——使用体验的三个痛点(重复、误报、无知识)全部缓解。

### 阶段 2:chains 深度接入(约 1-2 周)
L3:技术链骨架蒸馏 + `chain_tags` 意图路由;L1:同质响应/统一拦截/tarpit 等代码判据挂引擎;Settlement 收尾 Q 清单自检。

### 阶段 3:内网渗透能力(约 5-7 周,INTRANET-DESIGN 期 0-6)
期 0 已在阶段 0 完成 → 期 1 session 子系统(含 stage_share/受管资源框架,需求 8 并入)→ 期 2 凭据一等实体 → 期 3 tunnel 子系统 + proxyEnv 按任务绑定 → 期 4 阶段编排(授权检查点+关联任务+chains L3 内网路由)→ 期 5 reverse 驱动 → 期 6 原生部署链(需求 7 并入,chisel 钉版+`artex doctor`+重依赖降级)。
验收环境:PivotHub `scripts/lab` 三层靶场(现成)。

### 全程穿插(随时可做的小项)
F14 伪装门控(1-2 天);F13 若不并入期 1 可独立提前(2-3 天);vshell MCP 过渡(零代码,期 1 前顶 webshell 管理);PivotHub 知识合流进 chains L3(ETL 时顺手)。

## 4. 总工期与人力估计

单人全职:阶段 0-2 约 5-6 周,阶段 3 约 5-7 周,**合计约 3 个月全部落地**;其中"今天就能开工且当天见效"的是:F1、F3、F14、chains ETL+L4。

## 5. 贯穿全程的三条纪律(从讨论中沉淀的原则)

1. **Prompting is not enforcement**:安全边界(RoE/审批/资源回收)一律代码强制,提示词只做正向引导。来源:H2 教训、F5、F13、chains L1。
2. **受管资源原则**:凡带外部暴露面的资源(暂存/隧道/反弹监听/防火墙规则)必须平台登记台账、强制 TTL、归属任务、平台回收。来源:F13/INTRANET 4.3a。
3. **诚实性工程**:学 PivotHub——状态不伪造(alive 必须真探活)、验证必须决定性(隧道内实测/verify+expect)、文档如实记已知限制。来源:PIVOTHUB-EVAL §3.3。


## 6. 需求间冲突评估与代码可行性核验(2026-09-13 补)

### 6.1 真冲突(互相打架,已给出解法)

| # | 冲突 | 解法 |
|---|---|---|
| C1 | F13 进程组收割 × 隧道/反弹监听需长命:一刀切收割会杀掉 agent 正经建的隧道 | 立规矩:长寿资源禁止裸 Bash 起,必须走受管工具(`tunnel_deploy`/`reverse_listen`)注册台账豁免收割;guard 规则引导。**2026-09-13 缓解**:立足点策略定为 webshell-first(`INTRANET-PIVOT-DESIGN.md` 4.2),反弹监听降级为临时 TTL 通道,session 层从本冲突摘出,C1 仅剩隧道这一本质长命资源 |
| C2 | 合并 × 验证:delta 合并进已 confirmed 的 finding,新证据搭"已验证"便车 | 每次合并触发对该 delta 的增量重验(复用 finding_checks 管线) |
| C3 | 按任务绑隧道 × MITM 单实例单上游(`traffic/traffic.go:158-186`):MITM 分不清连接属于哪个任务 | 每任务独立 MITM 监听端口(各有上游);**go-mitmproxy 多实例可行性未验证**,退路:按代理认证用户分流上游 |
| C4 | pending 占位节点 × planner 语义:planner 会把未验证 finding 当"已突破" | 硬性规则:pending 节点不计入 prove_goal、但参与意图去重;写进 planner 工具与提示词 |

### 6.2 设计一致性张力(不致命,需意识到)

- 新增 session/tunnel/stage 工具群 × "worker 工具面刻意收敛"哲学 → `AugmentTools` 按阶段绑定,仅内网 worker 挂载。
- stage_share 对目标可见需绑外部接口 × F1 loopback 收敛 → TTL/token/防火墙小孔缓解,文档化。
- F5 RoE 对自由 Bash 只能尽力而为(混淆命令无法可靠解析目标)→ RoE 真正的强制点是结构化工具(`session_exec`/`tunnel_*` 的 host 参数),Bash 靠 guard ask 兜底;内网工具面设计需配合。

### 6.3 代码级未验证项(2026-09-13 已全部 spike 验证完毕)

1. ~~norma SDK 对 planner/mainagent 的 hooks 支持~~ **已验证(VPS spike,agent-18)**:SDK `agentcore.Options.Hooks` 字段原生存在,harness `execOne` 统一执行 PreToolUse(`harness/tools.go:60-66`),三类 agent 共享同一执行器——**ARTEX 侧改 2 个 Options 字面量 + setter 约 15 行即可,SDK 零改动**(接线点 `server/intercept.go:19-21` 现成)。Settlement 自检:自检工具平时挂载+不进 DisabledTools+收尾词指令即可,纯 ARTEX 侧;硬保证(未自检不许 finish)才需 SDK 加 `Settlement.RequiredTools`(`harness/query.go:454`)。
2. ~~go-mitmproxy 多实例共存~~ **已验证(实测程序 `.vps/spike-mitm/`)**:同进程 3 实例并存,各自独立 CA/上游/addon 全部正确,无全局状态冲突(`upstreamProxy` 是实例字段,`proxy/proxy.go:100`)。注意:每任务独立 CaRootPath 防首建竞争;`Proxy.Close()` 有 attacker goroutine 泄漏(实例生命周期与任务绑定即可);日志按实例打标;https/socks5 路径源码同构未实测。
3. 工程量估计均为单人熟练开发口径,未含联调返工。

### 6.4 已验证无冲突(不再讨论)

F14×F6(SSE 同源 cookie);F14×MCP/自更新/自定义工具(出站不过门控);需求 7×F9(release CI 顺带校验 chisel 哈希);chains L2×prompt 缓存(静态前缀稳定);L1×verifier(共享信号源);合并挂载点/验证地基/status 枚举/triggers/AugmentTools/proxyEnv/SetUpstreamProxy(x/crypto ssh)均有 file:line 实证。
