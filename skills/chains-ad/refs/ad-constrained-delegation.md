# AD内网 · 委派滥用:约束/非约束/RBCD 打到域管

> tags: 委派, 约束委派, 非约束委派, RBCD, S4U, S4U2Self, S4U2Proxy, 机器账户, 服务账户, 域提权, 域管, DA, impersonate, msDS-AllowedToDelegateTo, getST, kerberoast, 爆不动, 离线爆破, access_denied, pass-the-hash, dcsync被拒

### 什么时候该想到"委派"这条路(别死磕离线爆破)
**Q1**: 我在域成员机上拿了 SYSTEM、也 reg save 抓到了本机【机器账户 hash】,但机器账户在 DC 上 access_denied、Kerberoast 出来的 hash 又没字典爆不动,是不是没路了?
**A1**: 不是。这正是该【转委派】的信号。机器账户 hash 本身就是 S4U/RBCD 的钥匙之一——委派滥用【不需要爆破任何密码】,直接用 hash 走 Kerberos S4U 就能冒充域管。死磕单个 Kerberoast hash 离线爆破(尤其没字典时)是典型的沉没成本陷阱:同一件事试第 N 种爆法不会有新结果,该跳到正交路径。判据:你已经握有一个【机器账户或服务账户】的 hash/明文,就先查它有没有委派属性,别在爆破上耗。
**Q2**: 手里除了机器账户什么用户凭据都没有,委派也用不上吧?
**A2**: 那就先【薅到一个域用户凭据】再谈——顺序是 LSASS dump(拿交互登录过的域用户明文/NTLM)→ DCC2 cached(离线)→ 拿到任一域用户口令后【密码喷射】全域拿更多账号。委派是"有了一个立足账号后如何提权到 DA"的手段,不是凭空起手。见 [[ad-cred-harvest]] [[ad-password-spray]]。run 复盘:模型只 reg save 拿机器账户就扎进 Kerberoast,【从没 dump LSASS】拿真实用户凭据、【从没喷射】,把唯一的机器账户当成了死胡同——其实它是委派的入场券。

### 三类委派的识别与选路
**Q1**: 怎么快速判断域里有没有可滥用的委派、是哪一类?
**A1**: 查三个属性分诊:①`userAccountControl` 含 `TRUSTED_FOR_DELEGATION` → 非约束委派(unconstrained);②对象有 `msDS-AllowedToDelegateTo`(指向某 SPN)→ 约束委派(constrained);③目标对象的 `msDS-AllowedToActOnBehalfOfOtherIdentity` 可写 → 资源型委派(RBCD)。用 LDAP/PowerView(`Get-DomainComputer -TrustedToAuth`、`-Unconstrained`)一把梭查全域。
**Q2**: 手里是【机器账户】,它配了约束委派(msDS-AllowedToDelegateTo 指向 DC 的某服务),怎么用?
**A2**: 走 S4U2Self+S4U2Proxy:用机器账户 hash 申请"以任意用户(挑域管)身份访问自己"的票(S4U2Self),再换成"访问 msDS-AllowedToDelegateTo 里那个目标 SPN"的票(S4U2Proxy)。拿到的是【冒充域管访问 DC 目标服务】的 ST。若目标 SPN 是 cifs/host,直接拿到 DC 的文件/命令面。impacket:`getST.py -spn <目标SPN> -impersonate Administrator -hashes :<机器hash> domain/machine$`。
**Q3**: 约束委派限定了 SPN(比如只允许 time 服务),但我想要 cifs 拿 shell,被限死了?
**A3**: 用 SPN-less / altservice 技巧:S4U2Proxy 返回的票里 sname 可改(经典的 "任意服务" 滥用),把 time/xxx 改成 cifs/host/ldap 同主机的其它服务——同一账号同一主机的不同服务共享密钥,票能被目标接受。这样约束委派的"限定服务"形同虚设。
**Q4**: 没有现成委派,但我在某台机上是本地管理员 / 能改某对象的属性?
**A4**: 转 RBCD:找一个你能控制的、有 SPN 的账号(或自己 addcomputer 建一个机器账户),把它写进目标机的 `msDS-AllowedToActOnBehalfOfOtherIdentity`,然后用这个受控账号对目标做 S4U 冒充域管。写属性通常需要对目标有 GenericWrite/GenericAll——这也是为什么委派常和 ACL 滥用连用。
**Q5**: 是非约束委派的机器,怎么打?
**A5**: 非约束委派机器会把来访者的 TGT 缓存在内存——用 coerce(PrinterBug/PetitPotam)逼 DC 的机器账户来认证,你在这台非约束机上 dump 到 DC$ 的 TGT,拿着它 DCSync。这是另一条正交路,不需要爆破。

### 收口:委派拿到 DA 之后
**Q1**: S4U 拿到冒充域管访问 DC cifs 的票了,下一步直接怎么拿 flag?
**A1**: 用这张票(export KRB5CCNAME 后 -k -no-pass)对 DC:①`secretsdump.py -k dc.domain` DCSync 抽 krbtgt/域管 hash;②或 `wmiexec/psexec -k` 直接在 DC 上落命令读 flag;③或 smbclient -k 读 DC 上的 Administrator 桌面/指定路径。拿到 DA 等价物就别再发散,直奔 flag 常见位置。见 [[ad-dcsync-flag]]。
**Q2**: 票据类操作老在网络层失败(socket error/access denied),但我在立足点机上有 SYSTEM?
**A2**: 【就在域内立足点机上跑】,别从攻击机穿脆弱隧道打 DC。立足点是域成员、能原生和 DC 通 Kerberos——把 getST/Rubeus 的动作放在这台机上执行(它本来就在内网段),比 SOCKS 转发 Kerberos/SMB 稳得多。穿隧道打 DC 反复失败时,第一反应是"换成在立足点就近执行",而不是继续修隧道。见 [[ad-tunnel-friendly]] [[pivot-deep-discovery]]。
