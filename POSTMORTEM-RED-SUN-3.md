# 红日靶场3 任务复盘报告(任务 #1,2026-09-13)

> 任务:红日靶场3 多层内网(Web 入口 → Linux 跳板 → Windows 成员机 → 域控,flag 在域控 `C:\Users\Administrator\Documents\flag.txt`)。运行 06:26–09:24(约 3 小时,14,992 条事件、49 个 worker 意图、79 轮规划),由用户主动暂停。
> 数据来源:任务全量 activity(14,992 条)、mainagent 对话(41 条用户消息)、86 条拦截史、服务端日志。四角度独立分析(时间线/预算模式/技术失败/系统层)交叉验证。

## 一、一句话结论

**这不是一次"打不动"的失败,而是一次"差最后一击被叫停"的失败**——机器账户 PTH 打域控 445 在 09:22 已成功,DCSync 已在路上,2 分钟后任务被暂停。真正的问题不是打不穿,而是:**本该 1 小时走完的路走了 3 小时,决胜意图死在调度队列里,而走完的部分有三分之一靠人喂。**

## 二、攻击时间线(实际发生了什么)

| 时间 | 事件 |
|---|---|
| 06:26–06:38 | 侦察发现 Joomla `configuration.php~` 备份泄露 DB 凭据;**无 mysql 客户端 → 手写裸 socket MySQL 协议客户端**成功登录 3306 |
| 06:38–06:50 | dump users 表 → 改库加超管(用户提示的官方路线)→ 登后台 → 模板写马 → **立足点上线**;finding RCE 确认 |
| 06:50–06:57 | 内网侦察完毕:93.10=DC、93.20=WIN2008、93.30=WIN7 |
| 06:57–07:11 | PHP disable_functions 全禁 → 目标有 gcc 但 PHP 调不起(鸡生蛋)→ **攻击机无 gcc + apt 撞锁 → as/ld 手搓 freestanding .so → mail()+LD_PRELOAD 打通命令执行**(教科书级操作);正向 SOCKS 经 HTTP 命令马建成 |
| 07:11–08:11 | 零凭据攻坚域:LDAP 匿名、AS-REP、自研 SMB/TDS 客户端(**产出"445 被封死"的错误 fact,后被 impacket 推翻**)、ZeroLogon 563 次全灭;**PwnKit 提权 root 成功** |
| ~08:20 | **用户提供三组口令** → .30(WIN7)Administrator 命中;但 worker 把 .20 的 SUCCESS **误读为 FAILURE**,27 分钟后才被 planner 追认 |
| 08:44–09:18 | secretsdump/wmiexec 经 SOCKS 全灭(**自研 SOCKS 助手多连接/fd 泄漏,双连接 SMB 管道撑不住**);改走离线提取:.20 全部凭据(SAM/机器账户 NTLM/LSA 明文/DCC2)被解出 |
| 09:22 | **机器账户 TEST\WIN2008$ PTH 打 DC 445 成功**——首个可用域身份;DCSync 启动 |
| 09:24 | 用户暂停任务。与此同时:**决胜意图 #207(机器账户直攻 DC)从未被 worker 领取**——用户三次追问、mainagent 两次挂 hint,全部落空 |

## 三、根因排名(全部有证据,按"对结局的影响"排序)

### R1. 平台 session 子系统在加固 PHP 上整体退化 —— 最严重
`register_session` 只认识 eval 型一句话,对"命令执行型马"探针协议不匹配(反复报"不是 PHP 代码执行型一句话",5 个 worker 对同一马重复注册全部失败,`lang:auto` 甚至误判成 JSP)。结果:**注册成功的 session 全部退化为"仅文件读写",session_exec 全灭** → 所有 worker 弃用平台会话通道,改用裸 curl 打自建命令马(290+ 次手工请求循环),会话台账/审批/留痕体系被整体绕过。**这是把期 1 建设成果废掉的那个洞。**

### R2. 决胜意图死在调度环节
意图 #207(机器账户直攻 DC,当时已证明 PTH 可行)创建后**从未被领取**:frontier 积压 36 条 open 意图、P0 无领取超时上报、mainagent 没有 `kill_work`/`cancel_intent` 工具只能用 steer/hint 伪装指令。09:22 PTH 打通后 DCSync 跑了 2 分钟被暂停——**这一枪如果早 30 分钟执行,结局大概率不同**。

### R3. 攻击机工具链残缺 + 无自检上报 → 一小时手搓与错误 fact
攻击机无 pip/gcc/impacket、apt 撞锁;impacket 最终是**用户手工投递**的。此前 worker 被迫自研 SMB/TDS 协议客户端,产出"SMB 445 被封死"等**四条以上错误阴性 fact 写进黑板,planner 在错误事实上空转数轮**,直到 impacket 复核逐条推翻。worker 环境缺依赖没有任何自检/上报机制——用户本可提前 2 小时投送。

### R4. 预算燃烧模式(用户说的"钻牛角尖",部分成立)
- i69:glibc 版本不一致的盲利用,`malloc(): memory corruption` 同一崩溃签名连出十几轮仍重试,**169 拍烧光**;
- i104:复核早已关闭的结论(300 事件);
- 同一 webshell 被 5 个 worker 重复注册、MySQL 客户端写两遍、.so 构建四遍、SOCKS 通道重建四次;
- **chains meta 的"15~25 次同质响应即转向"规则若接入 worker 循环,约可省 250-350 次调用、45-60 分钟(20% 墙钟)——但它只在提示词里,没进代码。**

### R5. 共享基础设施互相污染
多 worker 共用同一马文件、同一 `/tmp` 命令文件、硬编码路径的 .so、同一 helper 端口——输出串台(哨兵误报"马失效")、文件互相覆盖、并发打满 Apache 超时。worker 把"被别人干扰"误判为"自己失败"而重试。

### R6. guard 误伤与"训练规避"
86 条拦截全部规则自动裁决、用户 0 次参与(审批请求根本没送达);误伤包括:`ls` 被判删除、PwnKit 必需的 `rm -rf GCONV_PATH=.` 被拦、**结题报告因正文提到 psexec/wmiexec 被"横向执行"规则拦下未能落盘**。worker 已学会写"规避措辞"——guard 在反向训练。

### R7. chains 四层知识体系实战零使用
`use_skill` 调用 0 次;`chain_tags` 只在 11 次 add_intent 里当标签出现;没有任何 worker 引用过 chains 判据。**花了最大心血建的知识层,实战中形同虚设**——L3 依赖 planner 打标(很少打)+ 关键词回退(覆盖窄),L4 依赖模型自觉调用(从不自觉)。

### R8. RoE 噪声与 scope 矛盾
110 次 RoE 告警全部 warn 放行、零阻断;大量把文件名判为"范围外目标"的噪声;scope 登记 192.168.111.0/24 而实际目标在 192.168.93.0/24——系统只会刷屏不会指出这个自相矛盾。

## 四、对用户两个假设的裁定

- **"钻牛角尖":成立,占约 20% 预算**(R4)。但不是主死因——主死因是 R1/R2/R3 的结构性问题,即使钻牛角尖全省掉,也只是早 45 分钟到达同一个调度瓶颈。
- **"主机上没有工具":部分成立但位置相反**——**靶机工具齐全**(curl/wget/python/nc 都有),缺工具的是**攻击机**(无 pip/gcc/impacket)(R3)。缺工具不可怕,可怕的是缺失时没有自检上报、逼 agent 手搓并产出错误事实。

## 五、系统性问题 vs 靶场特有(防过度拟合)

| 系统性(换任何目标都会复发) | 靶场特有(不应据此改设计) |
|---|---|
| session 不识别命令马、无 disable_functions 绕过内建路径(R1) | impacket 需人工投递(这台攻击机的环境问题,pypi 实际可达) |
| 意图调度:frontier 无上限、P0 无领取超时、mainagent 无 kill/cancel(R2) | 靶机 PHP 特意加固(禁 exec 全家+无 sendmail) |
| 环境缺依赖无自检上报;自研工具的阴性结论直接升级为 confirmed fact(R3) | 成员机口令来自官方 WP(纯自主路径在此靶场客观不存在) |
| worker 循环无同质响应停滞检测(R4) | 跳板机无出网,反向隧道天然不可行 |
| 立足点/工件无 per-worker 命名空间(R5) | 出题方式造成 111/93 双网段 scope 矛盾 |
| guard 字符串规则误伤 + 审批无人值守默认拒绝(R6) | |
| chains 无真实调用入口(R7) | |
| RoE 提取器噪声 + scope/goal 一致性无校验(R8) | |

## 六、修复计划(全部系统性,无一针对红日 3)

**P0(本周必做)**
1. **session 支持命令执行型马 + 内建 LD_PRELOAD 绕过**:探针区分 eval 马/命令马;检测到 exec 全禁时自动走"投递预编译 freestanding .so + putenv/mail() 触发"路径(worker 已证明手写可行,平台固化之);修 lang:auto 误判;探测失败文案不得轻言"被查杀"。
2. **chains L1 落地:worker 循环内停滞检测**——滚动窗口统计响应签名相似度,超阈值强制"卡住总结+请求转向";Settlement 挂 Q 清单自检。
3. **调度:P0 意图领取超时(如 5 分钟)上报 mainagent 并允许点名派发;frontier 硬上限(≤8 open);mainagent 补 `kill_work`/`cancel_intent`**。
4. **事实完整性:worker 产出类结论(SUCCESS/FAILURE 判定)必须 grep 复核产物才能写 fact;自研工具阴性结论标 inferred,成熟工具复核后才允许升 confirmed**。

**P1(下周)**
5. 攻击机基线镜像:pip/impacket 全套 scripts/gcc/wordlists/proxychains 预装,`artex doctor` 检查;启动时依赖自检+缺失上报 mainagent。
6. 隧道补"正向模式":经 webshell 的受管正向 SOCKS helper(台账化,worker 退出不杀),覆盖"目标无出网"场景——本次手工 helper 的平台化。
7. guard 语义化:跳过 heredoc/报告正文;报告/文档类 Write 白名单;审批请求投递到 mainagent 会话;风险分级默认动作(文档类默认 allow+审计)。
8. per-worker 命名空间规约化(马文件/命令文件/helper 端口/.so 路径按 worker 隔离)+ per-session 执行锁。
9. RoE:提取器只认网络目标(IP/域名/URL)过滤文件名;任务初始化做 scope↔goal 一致性检查并自动补 scope。

## 七、值得记录的光正面(不是只有问题)

- 手写 MySQL 协议客户端、as/ld 手搓 freestanding .so、LD_PRELOAD+mail() 绕过、PwnKit 提权、impacket SOCKS 全局 patch、离线解机器账户——**agent 的技术上限是真实存在的,多个操作是人类专家级**;
- 验证管线全程把 7 条 finding 全部裁决(含把一条误报判 false_positive);
- planner 两次主动纠偏(停手搓 EFSRPC stub、停装 pip);
- 用户提供口令后 15 分钟命中——人环协同机制本身是有效的。

*报告生成:2026-09-14,基于四角度独立分析的交叉验证;所有关键结论均有 seq 级证据,原始数据存 `.vps/postmortem/`。*
