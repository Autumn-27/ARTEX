# AD内网 · Kerberos攻击:Kerberoast/AS-REP/委派

### 落地一个域内shell,先决定走Kerberoast还是先做别的
**Q1**: 我拿到一个普通域用户的凭据/会话,想快速把攻击面转成"离线爆破 hash"。第一步该做什么?
**A1**: 先确认三件事——(1) 时钟与 DC 偏差是否 ≤5 分钟,(2) 手上凭据能否成功 kinit/请求 TGT,(3) LDAP 是否能读 servicePrincipalName 属性。三个都通过再谈 Kerberoast,否则先解决前置。
**Q2**: 为什么不是直接扔 Rubeus/GetUserSPNs 一把梭?
**A2**: 因为 Kerberoast 触发 4769 事件,盲扫所有 SPN 会给 SOC 送一份"完整攻击者画像"。要先离线过滤:排除机器账号(密码是 120 字符随机)、排除 krbtgt、优先服务账号(sAMAccountType=805306368 且 SPN 非空)。
**Q3**: LDAP 查询被限速或返回极少字段怎么办?
**A3**: 转向"匿名 SPN 枚举"备选路径——用已知的 TGT 直接 sname 猜测请求 TGS(某些老域允许对未知 SPN 返回 KDC_ERR_S_PRINCIPAL_UNKNOWN,可用作存在性 oracle),或改用 DNS SRV/SCP 记录反推服务。
**Q4**: 这条路整个被墙(域启用了 FAST/Kerberos Armoring 或所有服务账号都用 AES256 且强密码)怎么办?
**A4**: 果断【正交转向】—— 放弃 Roast,转:(a) AS-REP Roasting 找 DONT_REQ_PREAUTH 账号,(b) 委派链利用(非约束/约束/RBCD),(c) ADCS 证书模板滥用(ESC1/ESC8),(d) 密码喷洒到 OWA/SMB,离线爆破不是唯一变现方式。
**Q5**: 我怎么知道现在的凭据"够用"到可以枚举?
**A5**: 判据是能不能 `ldapsearch` 到自己所在 OU 的对象且 objectClass=user 的数量 >0,且能读到至少一个 servicePrincipalName 属性非空的对象。读不到就是被 ACL 限死了,得先横向拿另一枚 token。

### Kerberoast 拿到一堆 TGS,先爆哪个
**Q1**: 手上 30+ 个 $krb5tgs$ hash,离线算力有限,选哪个先跑?
**A1**: 排序策略:(1) 加密类型 etype=23 (RC4-HMAC) 优先,爆破速度是 AES 的数十倍;(2) 用户名带 svc/sql/backup/iis/web/mssql 等业务前缀的优先,业务账号密码复杂度普遍低于管理账号;(3) pwdLastSet 早于 3 年前的优先,老密码更可能是弱口令。
**Q2**: 目标域强制 AES only,拿到的全是 etype=17/18,离线速度掉了两个数量级怎么办?
**A2**: 缩字典而不是硬扛。用 OSINT + 目标域名 + 公司缩写生成小而精的规则字典(公司名+年份+!@#、部门缩写+123),别上 rockyou 全跑。同时并行开另一条线,不吊死在算力上。
**Q3**: 全部跑完一个都没爆出来,是不是这条路死了?
**A3**: 不一定,先检查三点:(a) hash 格式是否被工具错误截断(Rubeus/impacket 输出格式版本差异),(b) 是否漏了 msDS-AllowedToDelegateTo 上间接暴露的 SPN,(c) 是否有 gMSA 账号(msDS-ManagedPassword 可读则完全绕过爆破)。
**Q4**: 确认都试过还是零结果,下一个方向?
**A4**: 【转向】AS-REP + 委派 + ADCS 三选一。特别是如果枚举时看到有 msDS-AllowedToActOnBehalfOfOtherIdentity 或 TRUSTED_FOR_DELEGATION 标志,直接跳委派路线,ROI 比继续爆 hash 高得多。
**Q5**: 爆出来一个是"低权限服务账号密码",怎么升级?
**A5**: 不要停在这个账号本身。用它去请求它自己能访问的资源、看它在哪些机器的 local admin 组、看它有没有对高价值对象的 GenericWrite/WriteDACL,以及它是否是某个约束委派链的中间跳板。密码本身是钥匙,不是终点。

### AS-REP Roasting:没账号列表怎么起手
**Q1**: 我完全没有任何域账号凭据,只知道域名和一个 DC IP,想 AS-REP Roasting,从哪儿要用户名?
**A1**: 三条路并行:(1) 从 OSINT/公司官网/LinkedIn 收员工名单,按 firstname.lastname / f.lastname / flastname 三种模式生成候选;(2) SMB 空会话/RID 循环枚举(若 SMB 允许 null session);(3) Kerberos 用户名枚举——对 KDC 发 AS-REQ,存在返回 KDC_ERR_PREAUTH_REQUIRED,不存在返回 KDC_ERR_C_PRINCIPAL_UNKNOWN,可当 oracle。
**Q2**: 用 Kerberos 枚举 5000 个用户名会不会触发告警?
**A2**: 会。速率必须 ≤ 每秒 1~2 次,且分散源 IP;更好的做法是把候选名单先用 OSINT 精修到 ≤ 200 再打。默认 Rubeus 全速枚会瞬间点亮 SIEM。
**Q3**: 枚举出一批存在的用户名,但对每个发 AS-REQ 都返回需要预认证,没人是 DONT_REQ_PREAUTH,怎么办?
**A3**: 意料之中,通常只有个位数账号关了预认证(历史遗留服务账号常见)。如果一个都没有,判定 AS-REP 路线死,【转向】密码喷洒:用 2~3 个语义弱口令模板(组织名/缩写+年份+符号、季节+年份、"欢迎/初始"类词+数字)喷全员,别爆单点(会锁账号)。具体值按目标语境即时生成,不要硬编码。
**Q4**: 拿到一个 $krb5asrep$ hash,离线爆破一直不出,是不是密码就是很强?
**A4**: 未必。检查 (a) 是否加密类型是 AES(某些工具默认请求 RC4,可显式指定 etype=23 让 KDC 回 RC4 版本,前提是策略允许),(b) 用户名大小写与 realm 大写是否与 hash 里一致(有些爆破器对此敏感)。
**Q5**: 这条路彻底死了,下一个正交方向?
**A5**: 【转向】无凭据条件下,Kerberos 侧还剩:(1) 打印机 spooler bug + 非约束委派机器捕获 TGT,(2) NTLM 中继(PetitPotam / Coerce → LDAP/ADCS/SMB),(3) CVE 级(Zerologon/PrintNightmare/samAccountName),(4) 尝试匿名 LDAP。不要在 AS-REP 上死磕。

### 非约束委派:识别到了怎么变现
**Q1**: LDAP 枚举发现某台服务器 userAccountControl 包含 TRUSTED_FOR_DELEGATION(非约束委派),下一步?
**A1**: 目标不是"利用这台机器",而是"诱导一个高权限账号向它发起认证,让 TGT 落到它的 LSASS 里"。前提:我要有这台机器的本地管理员权限(能 dump 内存)或能以 SYSTEM 运行。所以第一步先解决"上这台机"的问题。
**Q2**: 已经是这台机的 admin,怎么诱导 DC 或 DA 认证过来?
**A2**: 经典是 Spooler bug(SpoolSample/PrinterBug)让目标机器账号回连;也可以 PetitPotam/DFSCoerce/ShadowCoerce 强制机器认证。触发后,在本机内存里等 TGT。
**Q3**: 触发了但内存里没看到 TGT,只看到 TGS,怎么办?
**A3**: 说明诱导对象是走 S4U 或已被约束委派限制,或是本机的 Kerberos SSPI 没缓存 TGT(只有 forwardable 的才会缓存)。检查 (a) 触发目标是否真的解析到本机,(b) 目标 SPN 是否指向本机,(c) 目标账号是否被标记为"敏感,不可委派"(Protected Users 组或 NOT_DELEGATED 标志)。
**Q4**: 目标 DA 被拉入 Protected Users 或打了 NOT_DELEGATED 标志,拿不到可转发 TGT,怎么办?
**A4**: 【转向】换目标——不是非要 DA。找同样有 DCSync 权限但不在 Protected Users 里的账号(Exchange、备份服务、老运维账号);或【正交转向】RBCD/ADCS ESC 路线。
**Q5**: 我怎么区分"非约束委派机器"和"看起来像但其实是 RODC"?
**A5**: RODC 也有类似标志但不能签发有用的 TGT。判据:(a) primaryGroupID 是否 521(RODC),(b) msDS-RevealOnDemandGroup / msDS-NeverRevealGroup 是否非空——是就是 RODC,别浪费时间。

### 约束委派 S4U:拿到低权服务账号后
**Q1**: 我拿到一个服务账号的密码/hash,它的 msDS-AllowedToDelegateTo 指向 CIFS/DC01.domain,能干嘛?
**A1**: 这就是【约束委派 + Protocol Transition】变现窗口。用 S4U2Self 以任意用户名(比如 Administrator)向自己申请票据,再 S4U2Proxy 换成对 CIFS/DC01 的 TGS。前提是该账号有 TRUSTED_TO_AUTH_FOR_DELEGATION(T2A4D)标志,否则 S4U2Self 拿不到 forwardable 票据。
**Q2**: 没有 T2A4D 标志,S4U2Self 拿到的是 non-forwardable,S4U2Proxy 报 KRB_AP_ERR_BADMATCH,怎么办?
**A2**: 用 CVE-2020-17049(Bronze Bit)——在 S4U2Proxy 请求里手动翻转 forwardable 位,补丁前的 KDC 会接受。域没打补丁就通,打了就走另一条路。
**Q3**: 补丁打了,而且目标用户是 Protected Users,S4U2Self 直接被拒,怎么办?
**A3**: 【转向】RBCD 或找委派链。委派可以级联:如果 A 能委派到 B 的 CIFS,而 B 上跑着的服务账号又能委派到 DC,就串起来。用 BloodHound 的 CanRDP/AllowedToDelegate 边找链。
**Q4**: msDS-AllowedToDelegateTo 里的 SPN 是 http/xxx 而不是 cifs/xxx,能不能变文件访问?
**A4**: 严格来说 SPN 类型不校验(客户端会按请求的 SPN 走),但 KDC 在打补丁版本上会校验 sname 匹配。可以尝试 sname substitution(Rubeus /altservice),把 http 换成 cifs/host/ldap,老版本 KDC 允许。
**Q5**: 这条链完全走不通,下一个方向?
**A5**: 【转向】从 SPN 反推——既然这个服务账号有委派权限,它自己也大概率是高价值资产的 owner,直接找它对哪些对象有 GenericWrite/WriteDACL,可能直接改别人的 msDS-AllowedToActOnBehalfOfOtherIdentity 做 RBCD。

### 基于资源的约束委派 RBCD:什么时候是最优解
**Q1**: 我拿到一个能对某台目标机器对象做 GenericWrite/GenericAll/WriteProperty 的账号,为什么优先考虑 RBCD?
**A1**: 因为 RBCD 是"受害者侧"配置,不需要 DA 权限就能改。写入 msDS-AllowedToActOnBehalfOfOtherIdentity,让我可控的一个"带 SPN 的账号"能够以任意用户身份访问目标。
**Q2**: 我没有"带 SPN 的账号",怎么造一个?
**A2**: 默认域策略允许普通用户创建 10 个机器账号(ms-DS-MachineAccountQuota=10)。用 impacket 的 addcomputer.py 建一个机器账号,机器账号自带 SPN,直接可用。若 quota 被改成 0,就找一个已有的、我能改密码的服务账号顶上。
**Q3**: MachineAccountQuota 已经是 0,而且我没有可控 SPN 账号,怎么办?
**A3**: 检查我控制的账号自己身上是否有 SPN(可自己给自己加 SPN,需要 ValidatedWrite/servicePrincipalName 权限);再不行【转向】改用 shadow credentials(msDS-KeyCredentialLink)+ PKINIT,不依赖 RBCD 拿目标 hash。
**Q4**: 写入 msDS-AllowedToActOnBehalfOfOtherIdentity 后 S4U 报错 KRB_AP_ERR_MODIFIED,怎么办?
**A4**: 常见原因:(a) 目标机器缓存了旧的委派配置,需要等 KDC ticket 缓存过期或等 15 分钟,(b) 我用了错误的 KDC(应该指向目标域的 DC),(c) 时钟偏差。逐一排除,不要以为是路线错了。
**Q5**: 目标是 DC,可以对 DC 做 RBCD 吗?
**A5**: 技术上可以(如果我对 DC 对象有 write 权),但通常 DC 的 ACL 严守。如果真有,直接 S4U 到 DC 的 cifs/ldap,然后 DCSync 完事。这是"一击必杀"级别的机会,别浪费。
**Q6**: 都不通怎么办?
**A6**: 【正交转向】shadow credentials(利用 msDS-KeyCredentialLink)只需要对目标对象的 GenericWrite,直接换出 NT hash,比 RBCD 更干净、动静小。

### Kerberos 时钟偏差被 KRB_AP_ERR_SKEW 拒
**Q1**: 请求 TGT 时反复报 clock skew too great,我的攻击机跟 DC 差了 8 分钟,怎么办?
**A1**: Kerberos 默认容忍 ±5 分钟。用 `rdate`/`ntpdate`/`net time \\dc` 同步到 DC 时间。别用公网 NTP,可能和目标域时间不一致(域可能故意偏移或走内网 NTP)。
**Q2**: 我没权限改本机时钟(容器/沙箱),怎么办?
**A2**: 用 faketime 之类的工具在进程级伪造时间,或修改工具让它在协议层用 DC 的时间戳(impacket 支持 -k 但仍依赖系统时间)。最省事的是把攻击机时间同步到 DC。
**Q3**: 同步到 DC 之后还是报 skew,是不是被防御识别了?
**A3**: 检查 (a) 时区,Kerberos 用 UTC 但工具可能带本地偏移;(b) 是否走了错误 KDC(比如打到了跨域的 DC);(c) DC 本身是否是虚拟机时间漂了(少见但存在)。
**Q4**: 时钟修不了怎么办?
**A4**: 【转向】走 NTLM 路线,NTLM 对时钟不敏感;或找一台已经在域里、时钟正确的中转机,把 Kerberos 交互放在那台上。

### 拿到 TGT/TGS 后怎么落地用
**Q1**: 手上有一个 .ccache 或 .kirbi 票据,想在 Linux 攻击机上用它跑 impacket,怎么做?
**A1**: `export KRB5CCNAME=/path/to/ticket.ccache`,然后所有 impacket 脚本加 `-k -no-pass`,并且 `-dc-ip` 指定目标 DC,同时 /etc/hosts 里给目标机器和 DC 加上 FQDN 解析(Kerberos 严格按 SPN 走,IP 直连会失败)。
**Q2**: kirbi 转 ccache 用什么工具?
**A2**: impacket 的 ticketConverter.py,或 Rubeus 直接 /ptt 注入到当前会话。注意 kirbi 是小端存储的 ASN.1,不要手改。
**Q3**: 注入了票据但 `klist` 显示为空,怎么办?
**A3**: 检查 (a) KRB5CCNAME 是否指向对了(有的系统默认 /tmp/krb5cc_UID),(b) 权限位是否 600,(c) ccache 格式是否为 FILE:(有些系统默认 KEYRING:)。
**Q4**: 目标 SMB 报 KDC_ERR_S_PRINCIPAL_UNKNOWN,票据没问题啊?
**A4**: SPN 不匹配。请求票据时的 sname 必须和访问时的 SPN 一致(cifs/hostname.fqdn 而不是 cifs/IP)。改用 FQDN 且大小写与 SPN 注册一致。
**Q5**: 走了半天都是协议层错,是不是根本环境不支持?
**A5**: 【转向】放弃 Kerberos 落地,用 PtH/NTLM 落地。Kerberos 洁癖是自找的,如果场景允许 NTLM 就用 NTLM,别为了纯粹强撑。

### Silver Ticket:拿到服务账号 hash 后的一步登天
**Q1**: 我爆出一个 MSSQL 服务账号的 NT hash,可以直接干什么?
**A1**: 伪造 Silver Ticket 直接访问该 SPN 对应的服务,绕过 KDC,不产生 4768/4769 事件(只有目标机上的 4624)。适合"低调、单点、深挖数据"的场景。
**Q2**: 用 Silver Ticket 访问 CIFS 报错,是不是 hash 不对?
**A2**: 检查 (a) 是不是这个账号真的注册了 cifs/ SPN(而不是 mssql/),Silver Ticket 是按 SPN 伪造的,SPN 不对就用不了;(b) 域 SID 是否正确;(c) 服务账号是否启用了 AES,若是要用 aes256 key 而不是 NT hash。
**Q3**: 目标启用了 PAC 签名校验(KB5008380/CVE-2022-37967),Silver Ticket 还能用吗?
**A3**: 补丁后 PAC 完整签名和扩展 KDC 签名需要 krbtgt key,单纯服务账号 hash 伪造的 Silver Ticket 会被拒。【转向】用真实 TGS(通过 overpass-the-hash 走正常 KDC 换)或换 Golden Ticket。
**Q4**: 那这个 hash 还有什么用?
**A4**: 至少能 overpass-the-hash 请求真 TGS,横向到该账号 local admin 的机器;也可能是解开一堆 GPP 密码或旧应用连字符串的钥匙。别把 hash 只当 Silver Ticket 材料。

### Golden Ticket 什么时候该造、什么时候别造
**Q1**: 拿到 krbtgt hash 了,现在就造 Golden Ticket 全域漫游?
**A1**: 先冷静。Golden Ticket 极其显眼(默认 10 年有效期、PAC 里的 group 全塞满、TGT 用不常见 etype),现代 EDR/SIEM 都有专门规则。造之前:(a) 确认目标域没有 PAC 校验补丁强制,(b) 有效期改成合理值(几小时到几天),(c) 加密类型和用户名与真实 KDC 输出一致。
**Q2**: 造完用不了,报 KRB_AP_ERR_MODIFIED,怎么办?
**A2**: 90% 是 domain SID 错、krbtgt 版本(kvno)错、或者 krbtgt 密码已经被轮换过一次(hash 已作废)。核对 SID 用 `whoami /user` 的域部分,kvno 用 mimikatz 从 DC 上读。
**Q3**: 造 Golden Ticket 后不到 5 分钟就被踢,怎么办?
**A3**: 说明有实时监控(可能是基于 4769 中 TGS 请求源为伪造 TGT 的判别)。【转向】少用 Golden 多用 Silver,或走 DCSync + Skeleton Key,或干脆用 krbtgt hash 静默做隐蔽性更好的 Diamond/Sapphire Ticket(伪造只改必要字段,PAC 从真 TGT 复制)。
**Q4**: 什么情况下 Golden Ticket 是最优解?
**A4**: 一次性任务(比赛/演练的"拿旗子"阶段)、跨域漫游需要伪造成任意用户、krbtgt 已经拿到但对方明天就要重启域的窗口期。常规红队实战更倾向 Silver + DCSync 组合,持久更隐蔽。

### DCSync 权限判据
**Q1**: BloodHound 告诉我某账号有 GetChanges + GetChangesAll,我要立刻 DCSync 吗?
**A1**: 是,但先算成本收益。DCSync 会在 DC 上产生 4662 事件(带 DS-Replication-Get-Changes GUID),防御方规则很标准。要 DCSync 就要有从 DC 拉数据到攻击机的通道,通道能不能过防火墙先确认。
**Q2**: 通道不通(DC 出口被封),怎么办?
**A2**: 在域内一台机器上落 impacket-secretsdump,把 DIT 拉到中转机再取。或者只同步单个用户(secretsdump 支持 -just-dc-user krbtgt),减少数据量和特征。
**Q3**: DCSync 报 DRSUAPI RPC access denied,权限明明有,怎么办?
**A3**: 检查 (a) 是否同时需要 GetChangesInFilteredSet(域启用了 confidentiality bit 时读某些属性要求),(b) 是不是 RODC(RODC 不能给),(c) 是不是走错了 DC(应该走目标域的 rw DC)。
**Q4**: 权限确认没有,能不能"曲线获得"?
**A4**: 【转向】(a) 找一个对 Domain 对象有 WriteDACL 的账号,给自己加 DCSync 权限(动静大),(b) 直接从 DC 主机取 NTDS.dit + SYSTEM(需要机器 admin),(c) 走 shadow copy / ntdsutil。

### 委派+ADCS 联动:什么时候切换
**Q1**: 我在做委派链,但发现域装了 ADCS 且有 Enterprise CA,要不要切?
**A1**: 强烈建议评估。ADCS ESC1/ESC4/ESC8 有些能"任意用户变 DA"一步到位,委派链要两三跳。判据:(a) certutil 或 Certipy 枚举出可申请的模板,(b) 有 UPN 可自定义/ClientAuth EKU/低申请权限,选一个 ESC 打。
**Q2**: 没有 ADCS 或模板都硬,继续委派对吗?
**A2**: 对。别为了新奇技术强行切,委派链在 ADCS 缺席的域是最稳定的提权方式。
**Q3**: 两条路都能走,选哪条?
**A3**: 隐蔽性:ADCS 请求证书事件更少见于常规告警,但 4886/4887 会被专业防御捕获;委派链 4769 请求频繁,但淹没在正常流量里。要看目标域的日志成熟度。
**Q4**: 走 ADCS 一半发现 CA 服务器不在线,怎么办?
**A4**: 【转向】立刻切回委派或 shadow credentials(不依赖 CA,只需要 DC PKINIT)。不要在等一个服务修好这种事情上耗时间。

### 打印机 Spooler 强制认证被禁
**Q1**: 我想用 SpoolSample 让 DC 回连一台非约束委派机器,但 spooler 服务被关了,怎么办?
**A1**: 【正交转向】其他 coerce 技术:PetitPotam(EFSRPC)、DFSCoerce(NetrDfsAddStdRoot)、ShadowCoerce(FSRVP)、CheeseOunce(WebClient)。每种依赖不同服务和补丁版本,枚举 DC 开放的 named pipe / 服务后选。
**Q2**: 所有 coerce 都试了没反应,是不是被 WebClient 类补丁堵了?
**A2**: 检查 (a) DC 的补丁版本(KB 号),(b) SMB 签名是否强制(强制则 relay 也走不了),(c) 有没有装 EDR 主动阻断 EFSRPC。都堵死就【转向】不依赖 coerce 的路线:AS-REP、密码喷洒、ADCS。
**Q3**: coerce 成功但没抓到 hash/ticket?
**A3**: 检查监听端(ntlmrelayx / Responder / krbrelayx)配置是否正确、监听端口是否被防火墙拦、SMB 签名要求。抓不到就是链没闭环,不是 coerce 失败。

### samAccountName Spoofing(CVE-2021-42287/42278)可用性判据
**Q1**: 我有一个普通域用户,DC 没打 11 月 2021 之后的补丁,能一步到 DA 吗?
**A1**: 有戏。前提:(a) MachineAccountQuota > 0 允许我建机器账号,(b) 我能改自己账号的 sAMAccountName 属性。noPac 攻击流程:建 fake 机器账号 → 改其名为 DC(去掉 $)→ 请求带 PAC 的 TGT 时 KDC 会把 PAC 里 sid 设为真 DC → S4U2Self 拿 DA 票据。
**Q2**: 补丁打了(2021-11 之后)还能用吗?
**A2**: 基本封死。别浪费时间,【转向】其他 CVE(Zerologon 也可能没打,先试)或 ADCS/委派。
**Q3**: MachineAccountQuota=0 怎么办?
**A3**: 找一个我能改 sAMAccountName 的已有账号(需要 WriteProperty),或者一个 kerberoast 出来的服务账号,套用同样的技巧。核心不是"创建机器账号",是"控制一个能被 KDC 当作机器的账号"。
**Q4**: 建了机器账号但改名报错?
**A4**: 检查 (a) 命名冲突(DC 主机名已存在),要用不冲突的过渡名,(b) sAMAccountName 属性是否被 GPO 限制修改,(c) 是否被 ACL 拒绝写入。

### 密码喷洒和 Kerberos 结合:先 SMB 还是先 Kerberos
**Q1**: 想密码喷洒但担心 SMB 触发账号锁定策略,能走 Kerberos 吗?
**A1**: 可以,而且推荐。用 Kerberos AS-REQ 做喷洒(kerbrute passwordspray),协议层少一两级,产生 4771 而不是 4625,但 badPwdCount 仍会累加。
**Q2**: 那锁定风险呢?
**A2**: 仍然有,badPwdCount 达到阈值同样锁账号。策略:每轮尝试 <= (lockout threshold - 2),两轮之间等 observation window 过去(默认 30 分钟)。速度慢是必要的代价。
**Q3**: 域没有明确锁定策略(阈值 0)怎么办?
**A3**: 起飞。但仍要控制并发,4771 洪峰依然会告警。合理速率是每分钟 <=10 次,分散源。
**Q4**: 喷了一轮全 preauth failed,连一个都没成?
**A4**: 检查 (a) 用户名格式是否要 UPN(user@REALM)而不是 sAMAccountName,(b) 是不是把机器账号一起喷了(全部失败),(c) realm 大小写。整体零命中往往是格式而不是密码错。

### 拿到 gMSA 密码的路径
**Q1**: LDAP 枚举到 gMSA 账号(msDS-ManagedServiceAccount),它比普通服务账号有什么不同?
**A1**: 密码由 KDC 每 30 天自动轮换,复杂度极高(240 字节),Kerberoast 基本无望离线爆破。但 msDS-ManagedPassword 属性可被特定 group(PrincipalsAllowedToRetrieveManagedPassword)读取,直接拿明文 blob。
**Q2**: 我不在 PrincipalsAllowedToRetrieveManagedPassword 里,怎么办?
**A2**: 找一个是的账号,或找一个对 gMSA 对象有 WriteProperty 权的账号把自己加进去。gMSAdumper 工具能读 blob 并转 NT hash。
**Q3**: 读到 blob 但 gMSAdumper 报错,怎么办?
**A3**: 检查 (a) LDAP over SSL 是否连通(msDS-ManagedPassword 是 confidential attribute,必须 LDAPS),(b) 时钟偏差,(c) 是否读的是 currentPassword 段(blob 结构有 current/previous/query interval)。
**Q4**: 完全不能读怎么办?
**A4**: 【转向】gMSA 通常挂在某台机器上跑服务,拿下那台机器的 SYSTEM 权限后,机器自己就能读 blob。绕过 ACL 的方式是"到用得着这个 gMSA 的机器上去"。

### 跨域/跨林信任下的 Kerberos 攻击
**Q1**: 我拿下 sub.corp.local,目标是 corp.local(父域),Kerberos 路径怎么走?
**A1**: 经典路径是伪造带 SID History 的 Golden Ticket。前提:拿到子域 krbtgt hash,伪造 TGT 时把 --sids 加上父域的 Enterprise Admins SID(S-1-5-21-...-519)。补丁前 KDC 不校验 SID Filtering。
**Q2**: 父域启用了 SID Filtering(quarantine)怎么办?
**A2**: 【转向】(a) 用子域 DA 到父域找信任账号(TDO,trust key)拿 trust ticket 然后 forge inter-realm TGT,(b) 走 unconstrained 委派诱导父域 DA 回连,(c) 走 ADCS 若父域 CA 信任子域用户。
**Q3**: 林信任(不是域信任)呢?
**A3**: 林内父子域信任默认双向可传递,SID History 通;林间信任默认 SID Filtering 开,难度陡增。林间要走 unconstrained 委派 + coerce,或者利用配置错误的 TGT delegation trust 属性。
**Q4**: 都不通,横向到目标林还有必要吗?
**A4**: 有,但可能不走 Kerberos。查看两林间是否有共享的 Web/SSO/Federation(ADFS/Azure AD Connect),那些是 Kerberos 之外的横向通道。

### Kerberos 请求走代理:走 SOCKS 还是 impacket -k
**Q1**: 我在 C2 里,只能走 SOCKS 到内网,impacket -k 能行吗?
**A1**: 能,但坑多。Kerberos 严格依赖 DNS 和时间。SOCKS 必须支持 TCP(某些 proxychains 版本对 UDP 支持有限,Kerberos AS-REQ 走 UDP 88 会失败),用 proxychains-ng 且强制 TCP 88(KDC 大部分都支持 TCP fallback)。
**Q2**: TCP 88 走通了但 DNS 走不通怎么办?
**A2**: 手动 /etc/hosts 加 FQDN,并在环境变量或 krb5.conf 里指定 KDC 地址,绕过 DNS SRV 查找。
**Q3**: 代理带宽小,DCSync 拉全域很慢怎么办?
**A3**: 只 dump 关键账号(krbtgt、DA 组成员),用 secretsdump 的 -just-dc-user。别一把梭全域。
**Q4**: 代理不稳一段就断,Kerberos 交互总失败?
**A4**: 【转向】把 Kerberos 那步放到内网中转机,C2 只发命令。远程执行拿到的 hash/票据落到中转机磁盘,再单文件回传,减少往返。

### Kerberoast 输出 hash 格式不识别
**Q1**: Rubeus 输出的 $krb5tgs$23$ 格式 hashcat 不认,怎么办?
**A1**: 版本问题。检查 hashcat 版本(-m 13100)和 hash 首行格式,某些 Rubeus 版本有字段顺序或校验和位置的输出差异。可以用 impacket 的 GetUserSPNs.py 再抓一次比对。
**Q2**: hash 格式对但 hashcat 说 no hashes loaded?
**A2**: 常见:文件编码是 UTF-16(Rubeus 输出到 Windows console 可能带 BOM)。用 dos2unix / iconv 转 UTF-8 无 BOM。
**Q3**: 一切都对但爆破没结果?
**A3**: 换 john,某些 etype=18 的 hash john 支持更好;或确认 hash 里的 realm、user 大小写与真实一致。
**Q4**: 都换了还爆不出?
**A4**: 说明这个账号密码真的复杂或已经用 gMSA,该收工换目标了,别在算力上耗光时间。

### 在没有 mimikatz 的机器上抽 TGT
**Q1**: 目标机装了 EDR,mimikatz 立刻死,怎么抽内存里的 TGT?
**A1**: 【转向】不用 mimikatz:(a) Rubeus 的 monitor / triage / dump 从 LSA 抽,但也会被 EDR 拦,(b) 走 comsvcs.dll MiniDump procdump lsass.exe 拿 dmp 回本地解析,(c) 用 nanodump/dumpert 之类的 syscall 直调工具,(d) 硬盘取 registry hive(SAM/SECURITY),里面没有 TGT 但有账号 hash。
**Q2**: LSASS 被 PPL 保护 dump 不了怎么办?
**A2**: (a) 加载 mimidrv 驱动绕(需要 admin,动静大),(b) 找 signed driver LOL(自带漏洞驱动 BYOVD),(c) 直接放弃 dump,走 shadow credentials 或 delegation 从协议层拿票据。
**Q3**: 都不行怎么办?
**A3**: 【正交】从密码入手而不是从票据。键盘记录、浏览器凭据、DPAPI、SCCM 客户端配置文件里的明文密码,这些不经过 LSASS。

### 只有机器账号 hash 能做什么
**Q1**: 拿到 MACHINE$ 的 NT hash(不是普通用户),能做什么?
**A1**: 机器账号也能请求自己的 TGT。可以:(a) 伪装成机器身份访问它有权访问的资源,(b) 如果这台机是 DC,直接 DCSync,(c) 如果这台机被配置了 RBCD/委派链,继续利用。
**Q2**: 机器账号密码随机 120 字符怎么用 hash?
**A2**: hash 就够了,不需要明文。overpass-the-hash 用 hash 请求 TGT,或直接 pass-the-hash 走 SMB(机器账号在自己机器上就是 SYSTEM 等价)。
**Q3**: 机器 hash 一段时间后失效,怎么办?
**A3**: 机器账号默认每 30 天轮换密码。要保鲜:(a) 提取后立刻用,(b) 或者禁用轮换(改 GPO 或注册表 DisablePasswordChange,动静大),(c) 更好的做法是拿到 hash 立刻做持久化(建 shadow credential、加 RBCD),不依赖 hash 本身。
**Q4**: 机器账号 hash 不能横向到其他机器怎么办?
**A4**: 意料之中,机器账号默认只对自己有权。它的价值在"用它做 S4U/RBCD 的中间桥",而不是直接横向。

### shadow credentials(msDS-KeyCredentialLink)什么时候用
**Q1**: 我对某账号有 GenericWrite,拿 hash 用 targetedKerberoast 还是 shadow credentials?
**A1**: shadow credentials 更强:直接给对方账号加一个我控制的公钥,然后 PKINIT 换出 NT hash 和 TGT,不用等对方登录、不用爆破。前提:域装了 ADCS 或 DC 是 2016+ 支持 PKINIT。
**Q2**: 域没装 ADCS 呢?
**A2**: 只要 DC 支持 PKINIT 就行,不一定需要 ADCS(DC 有自签证书用于 KDC 认证)。用 pywhisker/certipy 添加 msDS-KeyCredentialLink,然后 gettgtpkinit.py 换 TGT + PAC_REQUESTOR 拿 NT hash。
**Q3**: PKINIT 请求报错 KDC_ERR_CLIENT_NOT_TRUSTED?
**A3**: 检查 (a) 我添加的 keyCredential 结构是否正确(DeviceID/KeyMaterial),(b) DC 时间,(c) DC 是否禁用了 PKINIT(极少见)。或直接换个可写目标试。
**Q4**: 目标账号 msDS-KeyCredentialLink 已经有值,我覆盖会不会打破正常登录?
**A4**: 它是多值属性,追加不会覆盖。但操作后要清理,红队讲究可回滚。

### 服务账号密码策略推断
**Q1**: 打了半天服务账号都密码复杂,是不是策略强?
**A1**: 检查域密码策略(net accounts /domain 或 LDAP 读 pwdProperties)。同时看细粒度密码策略(PSO)是否针对服务账号 OU 单独放宽。有时候域整体强,但历史遗留服务账号 OU 有个 4 位数字策略。
**Q2**: PSO 我没权限读,怎么推?
**A2**: 从爆出来的 pattern 反推——如果爆出的都是 8 位,策略最小很可能就是 8;都带大小写数字,复杂度启用;都不带特殊字符,可能没启用符号要求。样本积累有用。
**Q3**: 完全爆不出来怎么办?
**A3**: 【转向】看谁能改这些账号的密码——如果我控制了一个对服务账号有 ForceChangePassword 权限的账号,直接改而不是爆。BloodHound 有专门的边。

### Kerberos over HTTPS(Kerberos KDC Proxy)可用吗
**Q1**: 外网只暴露 443,能不能通过 KDC Proxy 从公网做 Kerberos?
**A1**: 可以,如果目标域启用了 KKDCP(Kerberos Key Distribution Center Proxy Protocol,MS-KKDCP)。判据:是否有 /KdcProxy 端点开放(HTTPS POST)。
**Q2**: 端点开但请求被拒?
**A2**: KKDCP 严格要求 realm 匹配和证书验证。有时目标只给自家 VPN 客户端用,IP 白名单。
**Q3**: KKDCP 不可用,还想外网打 Kerberos?
**A3**: 【转向】ADFS/Azure AD 混合身份的路线,或者干脆先建 VPN/tunnel 到内网再打。别硬撑。

### 打完 Kerberos 攻击后的清理
**Q1**: 用 ptt 注入了票据,做完事怎么清?
**A1**: `klist purge`(Windows)或 `kdestroy` / rm ccache 文件(Linux)。别留在磁盘,内存注入的重启即失。
**Q2**: DC 上 4768/4769/4771 日志已经产生了,能清吗?
**A2**: 通常不能——要清 DC 事件日志需要 DA 权限且动静巨大,清 log 本身是极强告警信号。红队策略是"节奏藏进正常流量",而不是清 log。
**Q3**: 我伪造的 Golden Ticket 用完,krbtgt 是否需要重置来"清除痕迹"?
**A3**: 不该我做。红队不做防御方的活儿。但要意识到:一次 krbtgt 泄露,防御方需要连续重置 krbtgt 两次(kvno+2)才能真正清除。我如果做过持久化利用这一点,还有窗口。

### Kerberos 攻击遇到 EDR 内联 hook 全灭
**Q1**: Rubeus 在 EDR 面前秒死,ticket 都请求不了,怎么办?
**A1**: 转协议层实现:impacket 从 Linux 攻击机走 SOCKS 到内网,不在 Windows 上跑任何 Rubeus/mimikatz。所有 Kerberos 交互从代理另一端完成。
**Q2**: SOCKS 不通,一定要在 Windows 上做怎么办?
**A2**: (a) Rubeus 源码级修改字符串+编译,(b) 用 execute-assembly through C2 内存加载,(c) 换 SharpKatz/其它冷门实现,(d) 自己写小工具只做需要的那一步(比如只做 S4U)。
**Q3**: 上述都被杀怎么办?
**A3**: 【正交转向】不做 Kerberos。改走 LDAP 攻击(GenericWrite ACL abuse 只需要 LDAP,不碰 KDC),或 ADCS 走 HTTPS,或 SMB 走 relay。Kerberos 不是唯一维度。

### 委派链规划:BloodHound 边怎么读
**Q1**: BloodHound 图里,AllowedToDelegate/AllowedToAct/AddAllowedToAct/ForceChangePassword/HasSPN 怎么组合最优?
**A1**: 目标是从"我"到"目标"的最短路径。路径评分:每条边有"代价"(需要几个操作)。ForceChangePassword = 1 步(改密码,但影响业务),AllowedToDelegate = 1 步(直接 S4U),AddAllowedToAct = 2 步(改 RBCD + S4U)。选代价小且业务影响小的。
**Q2**: BloodHound 数据老了(3 天前),现在还准吗?
**A2**: ACL 变化频繁,组成员变化更频繁。关键跳点重新枚(用 SharpHound -c Session,DACL)确认。别信旧图直接打。
**Q3**: BloodHound 找不到路径,是真没有还是数据不全?
**A3**: 大概率数据不全。SharpHound 默认不采某些边(ACL 深度、GPO 链)。用 -c All 重采,或手动 LDAP 查 nTSecurityDescriptor 补齐。

### Kerberos etype 降级:强制 RC4
**Q1**: 目标账号支持 AES,但我想强制 KDC 用 RC4 加密返回,能做到吗?
**A1**: 客户端在 AS-REQ/TGS-REQ 里指定 etype 顺序,KDC 会选客户端支持且账号支持的第一个。所以在请求里只声明 etype=23,如果账号的 msDS-SupportedEncryptionTypes 允许 RC4,KDC 会给 RC4 加密的 hash。
**Q2**: 账号被强制 AES only(msDS-SupportedEncryptionTypes 只有 aes)怎么办?
**A2**: 打不了 RC4 降级,只能爆 AES,或【转向】其他账号。
**Q3**: 域策略"配置不为使用 Kerberos RC4"启用了怎么办?
**A3**: KDC 会拒绝 RC4 请求。判据是 KDC_ERR_ETYPE_NOSUPP。这种域一般防御成熟度较高,考虑整体切路线。

### 攻击 RODC 反噬风险
**Q1**: 我识别到一台 RODC,可以直接 dump 它拿域内所有用户 hash 吗?
**A1**: 不能。RODC 只缓存 msDS-RevealedList 里的账号(通常是本站点的少量用户),不缓存 DA。dump RODC 只能拿到部分账号。
**Q2**: 那 RODC 有什么用?
**A2**: (a) RODC 上有 KRBTGT_XXXXX 账号(带编号),这个 hash 可以造"部分域"的 Golden Ticket,签的 TGT 只能被 RODC 认可,不能被 rw DC 认可——除非配合已知漏洞绕过校验,(b) 从 RODC 看 msDS-Reveal-OnDemand-Group 反推被暴露的敏感账号。
**Q3**: 打 RODC 出手轻还是重?
**A3**: 轻。RODC 通常在分支办公室,监控没有总部严。但它的 hash 泄露不能直接漫游全域,不要期望过高。

### 委派链遇到 Kerberos Armoring(FAST)
**Q1**: 域启用了 Kerberos Armoring / Compound Identity,委派攻击是不是全废?
**A1**: 未废但难度大增。FAST 会把 AS-REQ 用一个 armor TGT 包一层,预认证部分不再有 offline crackable material,AS-REP Roasting 直接死。但 Kerberoast(TGS-REP)仍可能可用,S4U 也仍可用。
**Q2**: 判据是什么?
**A2**: GPO 里"KDC 支持声明,复合身份验证和 Kerberos Armoring"设置为"始终提供声明"或"失败未装甲的身份验证请求"。可用 klist 或抓包看 AS-REQ 是否带 armor field。
**Q3**: 有 FAST 但我还想做 AS-REP 类操作,怎么办?
**A3**: 【转向】走密码喷洒(4771 仍会产生但没有可爆 blob)或走完全绕过 Kerberos 的路径(NTLM relay 到 LDAP/HTTP/ADCS)。

### 一个"看似有 SPN"的账号请求 TGS 失败
**Q1**: LDAP 显示某账号有 SPN,但我请求它的 TGS 报 KDC_ERR_S_PRINCIPAL_UNKNOWN,怎么办?
**A1**: 可能 (a) SPN 注册在这个账号但 servicePrincipalName 属性数据脏(重复/大小写异常),(b) 账号被 disabled 但 SPN 没清,KDC 会拒,(c) SPN 里的主机名 DNS 不解析。
**Q2**: 都排查完还是失败?
**A2**: 换用 UPN 请求(sname 用 sAMAccountName@REALM 而不是 SPN),或用 Rubeus 的 targetedKerberoast 直接给账号临时加 SPN 后请求(需要写权限)。
**Q3**: 想给自己加 SPN 但被拒?
**A3**: 检查 ValidatedWrite/SPN 相关的写权,或看是否有 SPN duplicate 检查。有时候域强制"SPN 全局唯一",随便加会失败。

### 遇到 Protected Users 组的高价值目标
**Q1**: 我发现 DA 组成员都在 Protected Users 组,还能打吗?
**A1**: Protected Users 组员有多重限制:不能被委派、不能用 RC4、TGT 只有 4 小时、不能被 NTLM 认证、DPAPI 不能 offline 解。委派路线基本封死。
**Q2**: 那我怎么办?
**A2**: 【转向】(a) 找不在 Protected Users 但有 DCSync 权限的账号,(b) 走机器账号 + RBCD(机器账号通常不在 Protected Users),(c) 走 ADCS ESC(证书认证不受 Protected Users 影响),(d) 直接落地在 DC 上抓 lsass。
**Q3**: 判据先确认吗?
**A3**: 是,LDAP 查 memberOf=CN=Protected Users,不要凭猜。有时候 Protected Users 组存在但没被使用(空组)。

### 手上 hash 一堆但不知道哪台机能上
**Q1**: 爆出 10 个账号 hash,不知道哪个能 local admin 到哪台机,怎么办?
**A1**: 用 crackmapexec / netexec 批量:hash + 一份主机名列表,并发探测 SMB。返回 Pwn3d! 的就是 local admin。不要用密码明文,直接 pass-the-hash。
**Q2**: 全部返回 STATUS_ACCESS_DENIED,是不是 hash 没用?
**A2**: 未必。检查 (a) 是否是域账号但 UAC 远程限制阻挡(需要 --local-auth 关闭或走 named pipe),(b) SMB signing 是否强制(不影响 pth 但影响 relay),(c) 目标是否加入了不同的域/工作组。
**Q3**: 都没 local admin,hash 还有用吗?
**A3**: 有。用它们请求 TGT(overpass-the-hash),然后走 Kerberos 请求各种服务的 TGS,可能这些账号有对某个应用服务、SQL、SharePoint 的访问权,业务侧变现。

### 时间预算:Kerberos 路线什么时候该放弃
**Q1**: 已经在 Kerberoast/AS-REP/委派上花了 6 小时,一无所获,应该继续还是转?
**A1**: 判据:(a) 是不是每一步都遇到"防御生效"的信号(FAST/AES only/Protected Users)——是则整体防御成熟,转;(b) 是不是只是"运气差没爆出弱口令"——是则可能再花 2 小时值,但设硬上限。
**Q2**: 转向哪里最省时间?
**A2**: 优先级:ADCS ESC 枚举(15 分钟出结果) > BloodHound ACL 路径(30 分钟出结果) > NTLM relay + coerce(1 小时) > Web 面到 Windows Auth 应用。别死磕单一维度。
**Q3**: 什么时候是"真死"的信号?
**A3**: 域完全 Tier 化、有 PAW、Protected Users 全员、AES only、FAST 强制、ADCS 无 ESC、无可 coerce 服务、EDR 内联 hook 全 lsass 保护——出现 4 项以上,考虑换目标而非换手法。
