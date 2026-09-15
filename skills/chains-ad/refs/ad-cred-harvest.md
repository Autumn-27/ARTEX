# AD内网 · 凭据薅取:mimikatz/SAM/票据

### 拿到本机 SYSTEM,第一步先薅什么
**Q1**: 已经 SYSTEM 了,LSASS 里可能有域用户/服务账号明文和 NTLM,但我要先做哪一步才不浪费这次权限?
**A1**: 先做"取样再操作":快照 `whoami /all`、`klist`、当前进程列表、AV/EDR 名字(`Get-MpComputerStatus`、tasklist `/svc`)、以及 LSA Protection 是否开(RunAsPPL 注册表键)。因为一旦触发 EDR,LSASS 落地会被拦、进程会被杀,连观察窗口都没了。先摸清楚防御姿态,再决定用直接 dump 还是绕道。
**Q2**: 看到有主流 EDR 常驻,直接 minidump LSASS 大概率被杀,怎么办?
**A2**: 转向"不直接碰 LSASS"这条路:先看有没有其他富矿——SAM+SYSTEM 注册表 hive(reg save)、DPAPI master key + 用户凭据/Chrome/RDP 保存、`cmdkey /list`、任务计划里的账号、IIS applicationHost.config、SQL Server 连接字符串、SYSVOL 里的 cpassword 遗留。这些走 Win32 正常 API,EDR 告警面小得多。
**Q3**: 判据是什么——什么时候才值得冒险碰 LSASS?
**A3**: 当"离域控还差一个域用户/域管凭据"、其他富矿都翻过、且能确认 EDR 无内存扫描或 PPL 未开时才值得。否则每次触发一次 LSASS 读都是一次报警。
**Q4**: 那 dump 出来的产物怎么处理?
**A4**: 不在目标机上跑 mimikatz,把 minidump 拖回攻击机离线 `sekurlsa::minidump` 解析。目标机上只保留"读文件"这个动作。
**Q5**: 拿到 hash 后先做什么?
**A5**: 先分类:本地账号 hash(只对本机+可能横向复用)、域账号 hash(横向+域内查询)、机器账号 hash(可做 S4U/RBCD/DCSync 条件之一)。不同类型走不同下一步,别一股脑全试。
**Q6**: 如果 LSASS 里啥也没有,只有 SYSTEM 本身的 token?
**A6**: 说明这台机没人交互登录过——转向"引流":等计划任务/服务/管理员登录、或用 coerce(PetitPotam/PrinterBug)把别的账号骗过来认证。凭据薅取薅不到,就制造凭据流。

### mimikatz 被 AV 秒杀,如何绕
**Q1**: `mimikatz.exe` 一落地就被杀,connect back 都来不及,先怎么判断卡在哪一层?
**A1**: 三层判据分开测:①静态特征——换个不含 mimikatz 字符串的 loader/编译版看是否还被杀;②行为——`sekurlsa::logonpasswords` 这个 API 调用序列本身被 hook;③内存——AMSI/ETW 有没有拦。分别用最小样本(比如只做 `version`)去打,看哪一步先炸。
**Q2**: 只是静态被杀,怎么最小成本绕?
**A2**: 优先级:①用 BOF/inline .NET 版本(SharpKatz、nanodump)避开 PE 落地;②源码微改字符串+重编译;③加壳/shellcode loader;④白利用侧加载。选一条就别叠 buff,叠多了反而增加 IoC。
**Q3**: 是 AMSI/ETW 拦的,怎么处理?
**A3**: 判据:PowerShell 版 Invoke-Mimikatz 一加载就报,但 exe 版能跑 → AMSI;C# 版 Reflection.Load 就死 → CLR AMSI。分别对症:AMSI patch(内存改 AmsiScanBuffer 返回)、ETW patch(NtTraceEvent)。注意这类 patch 也是 EDR 重点监控点,权衡是否值得。
**Q4**: 全部被拦,LSASS 读不到,这条路要不要放弃?
**A4**: 放弃"当场解析",转向"取回来离线":用签名工具(procdump、comsvcs.dll MiniDump、任务管理器右键、silent process exit debugger 等)拿到 minidump 或整个 lsass 内存,离线在攻击机跑 pypykatz/mimikatz。EDR 对 procdump 的告警等级通常低于 mimikatz。
**Q5**: procdump 也被 EDR 判黑了,还有什么招?
**A5**: 转向"不 dump LSASS 这个进程"这个正交维度——绕过整个 LSASS:走 SAM/SECURITY hive、DPAPI 离线、Kerberoast/AS-REP roast、ADCS 证书滥用拿 PKINIT。凭据不一定非要从内存里来。
**Q6**: 如果这台机 LSA Protection (RunAsPPL) 开着呢?
**A6**: 用户态 dump 全废,要么改注册表重启(动静大、可能触发告警和用户察觉),要么用可签名易受攻击驱动(BYOVD)从内核里读——但这条路 IoC 极大,只在最后一步、目标是 DA 或高价值账号时才用。
**Q7**: 如果时间不够、EDR 强,快速止损策略是什么?
**A7**: 停止在这台机上继续消耗,转向 SYSVOL/GPP、打印机/CSV 里的老 cpassword、SCCM 网络访问账号、backup 服务账号等"不碰终端也能拿凭据"的路径。

### SAM/SYSTEM hive 抢救本地凭据
**Q1**: 有 SYSTEM 但 LSASS 读不到,想走 SAM,怎么最小噪声抓 hive?
**A1**: `reg save HKLM\SAM ...` + `HKLM\SYSTEM ...` + `HKLM\SECURITY ...`,三个都要:SAM 里是本地账号 NTLM,SYSTEM 是 boot key(解 SAM 用),SECURITY 里是 LSA secrets(服务账号明文/DPAPI 密钥/机器账号 hash 等)。别只 dump SAM。
**Q2**: 拿回后本地怎么解?
**A2**: impacket 的 `secretsdump.py -sam -system -security LOCAL`(离线模式),或 pypykatz `registry`。产物:本地账号 NTLM、cached domain logons(DCC2,离线爆破)、LSA secrets(可能有服务明文口令)。
**Q3**: DCC2 hash 拿到手,值得爆吗?
**A3**: 判据:hash 类型(mscash2 慢)、密码复杂度先验(域策略、目标企业常见口令风格)、时间成本。DCC2 慢一个量级,只有当这个账号大概率是活跃用户/管理员时才值得排队爆破;否则先把 LSA secrets 里的服务账号明文用了再说。
**Q4**: SECURITY hive 里没有 LSA secrets 或者全是空的?
**A4**: 说明这机器没配过服务账号/没启用过 DPAPI 保护的服务。转向:任务计划(schtasks /query /xml 里可能有 `Run As` 的凭据引用)、Windows Credential Manager(cmdkey /list + vaultcmd)、Chrome/Edge/Firefox 密码库(需要用户 DPAPI key)、PuTTY/WinSCP 注册表。
**Q5**: 只有本地账号 hash,横向能用吗?
**A5**: 关键判据:本地 Administrator 的 NTLM 是否在多台机器上复用(镜像部署/LAPS 未启用)。用 CrackMapExec/nxc `--local-auth` 拿这个 hash 扫一段 /24,复用率高就是横向金矿;LAPS 启用则单机作废,转向域账号。
**Q6**: 如果 reg save 被 EDR 告警呢?
**A6**: 转向卷影副本(vssadmin create shadow → 从 shadow 复制 SAM/SYSTEM 文件),或直接 esentutl 复制被占用文件。也可用 diskshadow 脚本模式。三种手段 IoC 谱不同,轮着试。

### DPAPI 全家桶:主密钥、凭据、Chrome
**Q1**: 想解某用户保存的 Chrome 密码/RDP 保存的凭据,已知用户 SID,下一步?
**A1**: DPAPI 解密链:用户密码(或 NTLM)→ 用户 master key(在 `%APPDATA%\Microsoft\Protect\<SID>\`)→ blob。所以先看能不能拿到该用户的登录密码/NTLM;拿不到就走域 backup key。
**Q2**: 拿不到用户密码/NTLM,只能走域 backup key,前置条件是什么?
**A2**: 需要域管等价权限(通常是 DA)才能从域控用 `lsadump::backupkeys` 导出。判据:如果都已经 DA 了,还需不需要费劲解这个用户的 DPAPI?——需要,因为这可能给你新的凭据面(Azure/云/第三方 SaaS 保存的密码),扩大战果。
**Q3**: 拿到 master key 后怎么用?
**A3**: `dpapi::masterkey` 解出明文 master key → `dpapi::chrome` / `dpapi::cred` / `dpapi::rdg` 分别对付不同 blob。注意 Chrome 从 v80 开始还叠了 AES key(local state 里的 encrypted_key,同样用 DPAPI 保护)。
**Q4**: 目标是离线机器/离职员工桌面,只有硬盘镜像?
**A4**: DPAPI 完全可离线:从 NTUSER.DAT 拿不到密码,但可以从 SYSTEM/SECURITY hive 拿到域机器账号 secret,再走 domain backup key 路径(需要能碰域控或已缓存);或直接爆用户密码后解 master key。
**Q5**: 用户是智能卡登录/无本地密码怎么办?
**A5**: DPAPI 用户 master key 用的是 NTLM 派生 key。如果是 PKINIT 智能卡,可能仍有 NTLM 哈希被缓存(供 SSO 用)。转向 `sekurlsa::pku2u` 或看该用户的 msDS-KeyCredentialLink(Shadow Credentials 攻击路径)。
**Q6**: 如果 DPAPI 路径整个走不通,凭据还想扩面?
**A6**: 转向浏览器 cookie(不需要密码,session token 直接用)、SSH known_hosts + 私钥、KeePass/1Password 数据库(需要主密码,但可 keylog 或找缓存)、企业 IM 客户端本地缓存(可能带 OAuth token)。

### Kerberoast:什么账号值得打
**Q1**: 我是普通域用户,想 Kerberoast,先找什么?
**A1**: 找有 SPN 的用户账号(不是机器账号,机器账号 hash 是 128 位随机,爆不了):`GetUserSPNs.py` 或 LDAP filter `(&(samAccountType=805306368)(servicePrincipalName=*))`。优先看 admincount=1 的、命名带 svc/sql/backup 的。
**Q2**: SPN 一个没有,怎么办?
**A2**: 转向"targeted Kerberoast":如果我对某个目标用户对象有 GenericWrite/GenericAll,可以给它临时写一个 SPN,再 roast,完了删掉。这是权限提升链的一环,不是无条件的。
**Q3**: roast 到票据了,加密类型是 AES256,爆得动吗?
**A3**: 判据:AES 比 RC4 慢一个数量级,而且需要企业密码策略较弱才有戏。如果目标账号强制 AES(msDS-SupportedEncryptionTypes),先看能不能"降级 roast":某些场景下客户端可以请求 RC4 (etype 23);或者干脆放弃 roast 这条路。
**Q4**: 全部账号都是 AES 且密码复杂,爆一周没结果?
**A4**: 放弃"跑字典"这个正交维度,转向"不需要爆破的凭据获取":AS-REP roasting(找 DONT_REQ_PREAUTH 账号)、ADCS ESC1-8(证书就是身份)、Unconstrained Delegation coerce、RBCD。
**Q5**: 爆出一个明文口令,发现只是低权服务账号,失望怎么办?
**A5**: 别急,先做三件事:①用这个口令去喷别的账号(密码复用/规律)、②看这个服务账号有没有本地管理员或对其他对象的 ACL、③看能不能滥用它去做 S4U(如果有 SPN)或访问其保护的资源(SQL/共享)。低权凭据也能撬动。
**Q6**: 判据:什么时候该停 Kerberoast 转别的?
**A6**: 三个信号任一出现就停:①目标域全 AES 且密码策略强(14+ 复杂);②已经跑 24h+ 无进展;③域内 SPN 账号 <10 个且都爆过。转 AS-REP、证书、DCSync 前置条件收集。

### AS-REP Roasting 的判据
**Q1**: 想 AS-REP roast,不需要有效凭据也能做吗?
**A1**: 不需要,只要能连 88 端口和知道用户名列表。用 `GetNPUsers.py -no-pass`。所以枚举用户名是前置——先做 RID cycling(null session)、SMB 匿名、LDAP 匿名、Kerberoast 侦察 GetUserSPNs 副产、外网泄漏用户名。
**Q2**: 打了一圈,所有账号都启用了预认证,哈希拿不到?
**A2**: 现代 AD 默认开预认证,只有历史遗留/兼容旧应用才会关。判据:这条路是"低成本捡漏",拿不到就直接放弃,不要投入更多时间。转向 Kerberoast 或密码喷洒。
**Q3**: 拿到一堆 AS-REP hash,先爆哪个?
**A3**: 排序策略:①命名像人名 > 命名像服务(服务口令通常随机);②admincount=1 的优先;③账号创建时间老的(老账号密码可能更弱);④近期改过密码的(说明账号活跃)。
**Q4**: 有一个 AS-REP 爆出来了,但用户看起来是普通员工,值得深挖吗?
**A4**: 值得。第一步:该用户是不是本地管理员(通过组策略/常规部署)、是不是某台工作站的"主用户"(LAPS 前时代常有个人机管理员权限);第二步:BloodHound 里跑他的 outbound path 到 DA 有几跳。
**Q5**: hash 类型是 etype 17/18 (AES),爆速极慢,怎么办?
**A5**: 判据同"高强度加密+强口令基本放弃在线爆"的通则(参见 Kerberoast 段的判据),这里额外的正交动作:对拿到 AS-REP hash 的账号做"用户画像"(创建时间/命名习惯/所在 OU),把爆破算力只花在画像上"最有可能是弱口令"的少数账号,别全表跑。
**Q6**: AS-REP 也失败,还有什么"无凭据"起手?
**A6**: 转向:LLMNR/NBNS/mDNS 投毒 → Responder 抓 NetNTLMv2;IPv6 mitm6 + ntlmrelay 打 LDAPS/ADCS;Web 端泄漏(Outlook Web Access 用户名验证、Autodiscover)。这些都不需要初始凭据。

### 无凭据抓 NetNTLMv2:Responder 时机
**Q1**: 想在同网段抓 NetNTLMv2,先看什么?
**A1**: 判据三问:①同广播域吗(不同 VLAN 就洗洗睡);②是否有 SMB 签名强制(强制签名 relay 就废,但 hash 抓取仍可);③晚上白天(用户活跃时段 hash 多)。先被动嗅探 10 分钟,看 LLMNR/NBNS 请求量,再决定要不要开污染。
**Q2**: 请求量很少,毒污没鱼上钩?
**A2**: 主动"引流":WPAD 相关请求(浏览器代理自动发现)、SMB 空共享路径(Word 里嵌 UNC、邮件里塞图片 UNC)、web 应用里的 SSRF 打自己 SMB。制造流量再钓。
**Q3**: 抓到 hash,爆不出来,还有什么用?
**A3**: 转向 relay:ntlmrelayx 打 LDAP(改 ACL、加机器)、打 ADCS Web Enrollment(拿证书)、打 SMB(需要签名未强制)。抓 hash 只是入口,relay 才是杀招。
**Q4**: 全网 SMB 强制签名 + EPA + LDAP 签名,relay 也废?
**A4**: 转向"降签名"或换协议维度:MS-DFSCOERCE / PetitPotam / PrinterBug 强制机器账号回连(机器账号回连 LDAP 不带签名限制);或走 HTTP → LDAPS 跨协议(签名协商差异)。
**Q5**: 判据:什么时候该停止投毒?
**A5**: 蓝队高度可能装了 Responder-detection(假名毒污请求)。看到自己伪造的 hostname 请求回来了就立刻停,这是蜜罐信号。
**Q6**: 完全没法抓/毒/relay,还有什么内网无凭据路径?
**A6**: 转向未认证 CVE:zerologon(补丁看时间线)、PrintNightmare、Exchange ProxyLogon/Shell、SMBGhost、EternalBlue(老网可能有)。凭据薅取跑不通就走漏洞维度。

### 银票 vs 金票:场景怎么选
**Q1**: 拿到域用户 hash,想用票据类攻击,银票还是金票?
**A1**: 银票=服务账号 hash 伪造 ST,只对该服务生效,不需要域管;金票=krbtgt hash 伪造 TGT,全域通吃,但需要 DCSync/域管前置。判据:如果只是要访问某台 SQL/CIFS,银票就够;如果要长期后门,金票。
**Q2**: 银票的隐蔽性和局限性?
**A2**: 隐蔽性高(不经过 KDC 走 TGS),但一旦服务账号改密码就废;而且 PAC 校验开启的域(2019+)会被 KDC 二次验证——需要 pac 里的用户真实存在或用 S-1-5-21-...-500 这类。
**Q3**: 金票拿到 krbtgt hash,是否立刻打?
**A3**: 别立刻打。金票是"核弹级"IoC,一旦用就意味着被检测风险陡增。先想:是要一次性用、还是留后门?留后门就把 hash 保存好,退出场景,不留活动;要用就配合正常业务时间、正常主机、正常账号名做伪装。
**Q4**: krbtgt hash 拿不到,想做类似效果?
**A4**: 转向"钻石票据"/"蓝宝石票据"(diamond/sapphire ticket)——不需要 krbtgt hash,通过修改 KDC 返回的合法 TGT 里的 PAC,伪造权限,检测面比金票小。前置:需要能拦截或伪装 KDC 通信 + 有效账号。
**Q5**: 服务账号 hash 有了但 PAC 校验拦了银票怎么办?
**A5**: 转向 S4U2self + S4U2proxy(如果账号可约束委派)伪造为任意用户访问该服务;或直接用该服务账号本身的权限访问(可能就够)。
**Q6**: 票据类都被检测/失败,回到根本怎么办?
**A6**: 换维度到 ACL 滥用:GenericAll/WriteDACL/AddMember on 高权组、Shadow Credentials(msDS-KeyCredentialLink 写)、RBCD (msDS-AllowedToActOnBehalfOfOtherIdentity)。不用票据也能拿域。

### DCSync 前置权限判据
**Q1**: 想 DCSync 拿 krbtgt,当前账号够格吗?
**A1**: 需要 DS-Replication-Get-Changes 和 DS-Replication-Get-Changes-All 两个扩展权限。默认拥有:Domain Admins、Enterprise Admins、Administrators、Domain Controllers、Read-Only Domain Controllers 部分权限。用 BloodHound 或 `dsacls` 查当前身份是否 inherited 或 explicit 有这两个权限。
**Q2**: 没有 DCSync 权限,能提到吗?
**A2**: 找中间跳板:①对域根对象有 WriteDACL 的账号(能给自己加权限);②对某组有 AddMember 的账号(加到高权组);③某遗忘的服务账号历史上被授权过。BloodHound 的 shortest path to DA 就是找这个。
**Q3**: 拿到 krbtgt hash 后立即 DCSync 更多账号吗?
**A3**: 不要在 DC 上重复动作。一次连接 dump 全部关注账号:krbtgt、Administrator、DA 组成员、目标业务账号、机器账号(高价值服务器)。批量一次,减少 4662 日志次数。
**Q4**: DCSync 被 SACL 监控/被 4662 告警怎么办?
**A4**: 转向"离线抓"——如果能触及 DC 磁盘(vSphere 快照、备份文件、shadow copy),直接 dump `ntds.dit` + SYSTEM hive,离线 secretsdump。无网络流量。
**Q5**: DC 全部有 EDR + LSA Protection + 备份加密?
**A5**: 转向不 dump DC 也拿域权:ADCS ESC 系列直接伪造证书当 DA、Golden SAML(如果域联邦到 ADFS 且拿到 token 签名密钥)、Azure AD Connect Sync 账号(如果 hybrid)。凭据薅取有域外路径。
**Q6**: 拿到 krbtgt 但发现是 read-only DC(RODC)?
**A6**: RODC 的 krbtgt 是独立的、只能签发 RODC 缓存过的用户 TGT。判据:如果 RODC 缓存策略允许某高价值账号,就伪造那个用户;否则这个 hash 价值有限,转向找可写 DC。

### ntds.dit 离线导出的三条路
**Q1**: 已在 DC 上,想拿 ntds.dit,选哪条最安静?
**A1**: 三选一:①`ntdsutil "ac i ntds" "ifm" "create full C:\temp"`(创建 IFM 备份,官方工具,IoC 低);②卷影副本 vssadmin/diskshadow 复制;③直接 esentutl copy 加载中的 ntds.dit。都需要 SYSTEM hive 一起才能解密。判据:ntdsutil 最白但事件 ID 明显;shadow 中等;esentutl 直接 copy 有时会失败。
**Q2**: ntdsutil 触发 4104/事件日志怎么办?
**A2**: 转 diskshadow 脚本模式:`create` → `add volume` → `expose` → `copy`,配合 `-tempfolder` 隐藏路径。或者用 WMI 远程 shadow,不在 DC 上落 shell。
**Q3**: 拿回 ntds.dit + SYSTEM,怎么解?
**A3**: `secretsdump.py -ntds ntds.dit -system SYSTEM LOCAL`,输出所有域账号 NTLM + Kerberos key。海量,别都爆——按 admincount、组、命名过滤。
**Q4**: DC 上没管理员权限,只是有对某备份系统的访问?
**A4**: 转向"从备份里抢":Veeam/Commvault/NetBackup 备份存储里几乎必有近期 DC 系统状态备份,拿到备份 = 拿到 ntds.dit。这是绕过 DC 直连的经典路径。
**Q5**: 备份也拿不到、DC 也上不去?
**A5**: 转向"不需要 ntds.dit 也拿域"路径:ADCS 证书打全域(ESC1/6/8 尤其致命)、Coerce+relay 到 LDAP 改 ACL、找 tier-0 服务(SCCM、Exchange、Azure AD Connect)间接拿 DA。
**Q6**: 拿到 ntds.dit 后长期利用的姿势?
**A6**: 记录 krbtgt hash 生成金票(备用后门,但慎用),记录所有高权 hash 做未来重放,并注意 krbtgt 密码轮换周期——一旦目标 rotate 两次(需要两次因为兼容),金票失效。

### 机器账号 hash 的价值挖掘
**Q1**: LSASS 里翻到本机 `<HOSTNAME>$` 的 hash,能干啥?
**A1**: 机器账号 hash 天生不能爆(128 字节随机),但可以:①做该机器的银票(伪造访问自己上的服务)、②S4U/RBCD 相关攻击、③PKINIT UnPAC-the-hash 反查、④直接 pass-the-hash 认证到本机的服务。别丢。
**Q2**: 机器账号能不能加入到其他机器的 RBCD?
**A2**: 关键:每个域用户/机器默认能创建 10 个机器账号(ms-DS-MachineAccountQuota),即使不是 DA,拿到普通域凭据就能造一个新机器账号 → 配置 RBCD 到目标机器 → S4U 成任意用户到目标机器。经典权限提升链。
**Q3**: MachineAccountQuota=0(锁死)怎么办?
**A3**: 转向"劫持已有机器账号"这个维度:如果对某机器对象有 GenericWrite/GenericAll(如某维护组),写 msDS-AllowedToActOnBehalfOfOtherIdentity 或 msDS-KeyCredentialLink,达到同等效果,不需要造新账号。
**Q4**: 机器账号 hash 能横向吗?
**A4**: 可以但受限:pass-the-hash 用 `<HOST>$` 认证到其他机器,通常权限是 Authenticated Users,除非该机器账号本身在其他机器的本地管理员组(罕见但存在,尤其 SCCM/集群场景)。
**Q5**: 拿到 DC 的机器账号 hash 呢?
**A5**: 等价于 DCSync 权限——因为 DC 机器账号有复制权限。所以 dump DC 上 LSASS 或 SAM 拿到自己的机器账号 hash 就能 DCSync,不需要"域管"这个身份标签。
**Q6**: 机器账号 hash 突然认证失败?
**A6**: 机器账号会自动轮换密码(默认 30 天),hash 有保鲜期。判据:如果距离 pwdLastSet 快到 30 天,立刻用,别囤。或者用 Delegate2Thyself 等技巧拿新 hash。

### GPP cpassword:老矿脉的价值判据
**Q1**: 值不值得翻 SYSVOL 找 GPP cpassword?
**A1**: 值得,零权限即可访问(只需域用户读 SYSVOL 权限),5 分钟成本:`findstr /S /I cpassword \\<domain>\sysvol\<domain>\policies\*.xml`。命中的话 AES key 是公开的,直接解。
**Q2**: 没命中?
**A2**: 老矿脉一般 2014 补丁后就清了,但还有一些遗留位置:登录脚本 (.bat/.vbs) 里硬编码密码、Group Policy Preferences 除 cpassword 外的 Drives.xml/ScheduledTasks.xml、GPP 的 Registry.xml、SYSVOL 里的部署脚本、DFS 共享里遗留部署包。
**Q3**: SYSVOL 翻完,还想低权限找口令?
**A3**: 转向 LDAP `description`/`info` 字段:老 AD 里管理员爱把密码写在这里当便签。`Get-ADUser -Filter * -Properties description | ? { $_.description -match 'pass|pwd|密码' }`。
**Q4**: LDAP 描述也没有?
**A4**: 转向"共享文件级":网络共享匿名/域用户可读的目录,搜 config/xml/ini/txt 里的密码字段,或 `\\<fs>\backup\` 里的历史配置。SMB search 工具:snaffler / manspider。
**Q5**: 什么时候该停止找"文件里的密码"?
**A5**: 判据:已经 grep 覆盖了域用户可访问的 SYSVOL + 至少 3 个大共享 + 关键服务器的 C$/Admin$(如果有权),仍没结果 → 转向凭据源(浏览器/终端/服务)。
**Q6**: 找到一个明文口令但账号早离职?
**A6**: 别丢:①测密码复用(在职账号是否用了相似规律);②看这个账号有没有被禁用而只是密码过期(可能 NoPassExp 属性);③记录该企业的密码构造习惯(公司名+年份+特殊符号)喂给密码生成器。

### 浏览器凭据薅取
**Q1**: 用户机上有 Chrome,想抓保存的密码,前置是什么?
**A1**: 必须以该用户身份运行(或有其 DPAPI master key),因为 Chrome v80+ 的 encrypted_key 用 DPAPI 加密。SYSTEM 也不行,得 impersonate 用户 token 或走域备份 key。
**Q2**: 用户没登录,只有磁盘怎么办?
**A2**: 走离线 DPAPI 链:需要 master key + 用户密码/NTLM 或域 backup key。所以先解决"怎么拿这个用户的凭据",转个圈回来。
**Q3**: 抓到密码,但发现是同步过的 Google 账号密码?
**A3**: 高价值——Google 账号通常关联工作邮箱、企业 SSO(SAML)、云资源。看是否在企业 Google Workspace 里能横向到 GCP/Gmail/Drive。凭据薅取的目的不止是内网。
**Q4**: Chrome cookie 呢?
**A4**: 更有价值——cookie 直接免密登录,绕过 MFA。目标 cookie:企业 SSO(Okta/Auth0/AzureAD)、云控制台(AWS/Azure/GCP)、代码仓库(GitHub/GitLab)、内部管理台。同样需要 DPAPI 解 encrypted_value。
**Q5**: Edge/Firefox 呢?
**A5**: Edge 同 Chrome (DPAPI + AES-GCM)。Firefox 用 key4.db + logins.json,主密码可能未设(默认无主密码),用 firefox_decrypt 之类工具直接解。
**Q6**: 浏览器都没有保存凭据?
**A6**: 转向"其他保存凭据源":WSL 里的 .git-credentials、~/.aws/credentials、~/.ssh/、%APPDATA% 下各种 IM/开发工具、注册表 HKCU\Software 遍历 password 字段、Outlook 缓存邮件搜索"password/账号"关键字。

### token impersonation 的路子
**Q1**: 有 SYSTEM,想借用某域管的 token,他刚登录过这台机器,怎么做?
**A1**: 枚举当前进程的 token 所有者:`token::list`(mimikatz)、`incognito`(Meterpreter)、`SharpToken`。找有 SeImpersonatePrivilege 且属于目标用户的进程,`token::impersonate` 或 `steal_token <pid>`。
**Q2**: 他登录过但已注销,token 还在吗?
**A2**: 不在 process token 层了,但可能在 LSASS 缓存里(logon session)——转 mimikatz `sekurlsa::logonpasswords` 或 `sekurlsa::tickets` 拿他残留的 TGT/hash,而不是 token。
**Q3**: token 拿到了,但只能本地用,想远程?
**A3**: token 是本地进程属性,不能带走。要远程用:①从 token 关联的 logon session 里抠出 TGT(`sekurlsa::tickets /export`),然后 pass-the-ticket;②或从 LSASS 里挖 NTLM/明文,pass-the-hash/明文认证到其他机器。
**Q4**: SeImpersonatePrivilege 我没有,怎么办?
**A4**: 转向"提到有 impersonate":Potato 系列(Rogue/Juicy/Print/Rotten)本质是把网络服务的 NTLM auth 反射回来给自己,前提是有 SeImpersonate(服务账号常有)或某些 COM/DCOM 触发条件。没 impersonate 就要先 SYSTEM。
**Q5**: 用了 token 之后忘记 revert,会有什么后果?
**A5**: 后续操作以该用户身份被审计,可能"背锅"或触发行为异常告警(该用户从不该登录的机器发起动作)。用完立刻 `rev2self`。
**Q6**: 目标机器上一个高权用户都没登过,token 无鱼可钓?
**A6**: 转向"制造登录":引诱管理员登录(伪造工单/系统故障)、hijack RDP session (query session + tscon,需要 SYSTEM,能接管断开的会话拿到该用户 token 全套)、劫持服务让高权账号跑我的进程。

### RDP 会话劫持
**Q1**: 看到 `query user` 里有断开的管理员 RDP 会话,想劫持,怎么做?
**A1**: SYSTEM 权限下 `tscon <sessionid> /dest:<mysession>`——直接接管,不需要该用户密码。这是 Windows 特性不是漏洞,但极其致命。判据:必须 SYSTEM(不是管理员),所以先提到 SYSTEM。
**Q2**: 会不会被察觉?
**A2**: 会——原会话用户如果重连会发现异常,而且日志里有 4779/4778(session reconnected)。用之前先 `logoff` 我自己 session,再劫持,尽量在下班后。
**Q3**: 只有活跃(active)会话没有断开的?
**A3**: 转向"等断开"——可以监控 `query session`,或制造断开(RDP disconnect 有专门 API,或强断该用户网络)。也可以劫持活跃会话,但会立刻踢掉原用户 → 立刻被发现。
**Q4**: 目标机不允许多会话/tscon 被组策略禁?
**A4**: 转向不需要 tscon 的方案:用该用户已登录事实,直接 impersonate 其 token(前面链)、mimikatz 从 LSASS 抠他的凭据、进程注入到该用户 explorer.exe 拿身份。
**Q5**: 劫持后我在他的桌面,他重登进来了怎么办?
**A5**: 提前准备:进程注入 explorer 后立刻退出被劫持会话,让身份 impersonation 或凭据保留在 background;或者立即启动一个隐蔽持久化(计划任务/服务)确保退出后仍能回来。
**Q6**: 完全没 RDP 用户?
**A6**: 换 pipe hijack(命名管道模拟)、smb impersonation、或者干脆钓鱼路线让高权用户主动登录目标机(社工/伪造运维需求)。

### coerce 强制回连:PetitPotam 家族
**Q1**: 拿到普通域凭据,想让某台机器/DC 主动认证到我,怎么选 coerce 方法?
**A1**: 家族:PetitPotam (MS-EFSR)、PrinterBug (MS-RPRN)、DFSCoerce (MS-DFSNM)、ShadowCoerce (MS-FSRVP)。判据:目标补丁状况不同,依次尝试,一个失败换下一个。DFSCoerce 补丁面较小,优先。
**Q2**: coerce 成功,收到 NTLM 认证,拿去干嘛?
**A2**: 三个方向:①relay 到 LDAP/LDAPS(改 ACL、给自己加 DCSync);②relay 到 ADCS Web Enrollment(拿机器证书,再 UnPAC hash);③relay 到 SMB(如果签名未强制)。ADCS relay (ESC8) 是重头戏。
**Q3**: 目标是 DC,但 DC 有补丁+签名强制?
**A3**: 补丁堵一个洞,别的可能还开着(补丁体系不完整)。逐个 API 测。签名强制堵 SMB relay 但不堵 LDAPS relay(需要 EPA 才能堵)。ADCS Web 默认不启 EPA,是 ESC8 的经典 sink。
**Q4**: 没有 ADCS 环境?
**A4**: 转向 LDAP/LDAPS relay + 添加 msDS-AllowedToActOnBehalfOfOtherIdentity 到目标机器(RBCD 攻击链),然后 S4U 拿目标机器的 admin ticket。
**Q5**: coerce API 全被堵,MS-EFSRPC/MS-RPRN 都返回 access denied?
**A5**: 转向"WebDAV coerce"(如 WebClient 服务开着,可以让机器走 HTTP 认证,而 HTTP relay 到 LDAPS 不受签名限制);或者放弃 coerce 转向凭据窃取路径。
**Q6**: 判据:什么时候放弃 relay 转其他?
**A6**: 已经尝试 3+ coerce API × 3+ relay target 都失败 → 说明目标环境防御完整,转向漏洞维度(ADCS ESC1/ESC7/CVE)、或者社工/物理。

### Kerberos 委派滥用的选择树
**Q1**: BloodHound 显示某账号有 unconstrained delegation,怎么用?
**A1**: 引诱域管到该机器认证,LSASS 里就会有 DA 的 TGT。coerce DC 认证过来(PrinterBug 到 DC),然后从我们控制的 unconstrained 机器上 sekurlsa::tickets 拿 DC$ TGT → DCSync。
**Q2**: 只有 constrained delegation (with protocol transition) 呢?
**A2**: S4U2self + S4U2proxy 组合:先给自己签一个"任意用户"到该账号自己的 ticket,再用它 S4U2proxy 到委派允许的目标服务。判据:允许的服务清单在 msDS-AllowedToDelegateTo。
**Q3**: 目标账号是 Protected Users 组成员,S4U 失败?
**A3**: Protected Users 禁止委派、禁止 NTLM、禁止 DES/RC4。S4U 目标是这类账号直接废。转向:攻击可以委派到该用户的对象(反向找)、或者不走委派走别的(ADCS)。
**Q4**: RBCD 场景,前提是什么?
**A4**: 需要:①对目标机器有 GenericAll/GenericWrite 或 WriteProperty on msDS-AllowedToActOnBehalfOfOtherIdentity;②有一个能创建的 SPN 账号(自己造机器账号或已控账号)。前提缺就补前提(找 ACL 路径)。
**Q5**: 委派全部关闭/被 Authentication Silo 隔离?
**A5**: Authentication Policy Silo 会限制 tier-0 账号只能从 tier-0 机器登录,委派也受限。转向 ADCS(证书身份不走 Kerberos 委派限制)、或者拿 tier-0 主机再说。
**Q6**: 委派拿到的 ticket 只对某一个服务有效,想扩?
**A6**: 用委派拿到 CIFS/HOST ticket 就等价该机器完整访问 → 从该机器再拿 LSASS/凭据 → 继续横向。ticket 是入口不是终点。

### Shadow Credentials (msDS-KeyCredentialLink)
**Q1**: 对某目标账号有 GenericWrite,除了改密码还能做什么?
**A1**: 改密码会破坏账号功能(用户会发现)。转向 Shadow Credentials:写 msDS-KeyCredentialLink 一个我的公钥 → PKINIT 用私钥换该账号的 TGT + NT hash(UnPAC)。零破坏,极隐蔽。
**Q2**: 前置条件是什么?
**A2**: 域内有 KDC 支持 PKINIT(基本都有)+ AD Schema >= 2016(WHfBTools 场景)。工具 pyWhisker / Whisker / ShadowSpray。
**Q3**: 打完想清理痕迹?
**A3**: 从 msDS-KeyCredentialLink 里移除我的 KeyCredential,还原属性。因为默认账号可能就一个也可能有多个(TPM/生物识别),别误删原有的。
**Q4**: 目标账号 msDS-KeyCredentialLink 属性写入被拒?
**A4**: 可能 schema 不支持或者 KRBTGT 相关保护。转向经典 RBCD、targeted Kerberoast(改密码/加 SPN)、或直接改密码(破坏性但有效)。
**Q5**: 全域用户都可以对自己写 msDS-KeyCredentialLink?
**A5**: 是的——ShadowSpray 场景:如果发现"authenticated users"或"self"对该属性有写权限(某些 misconfig),任何域用户都能给自己后门。批量喷检查环境是否弱配。
**Q6**: 打完 UnPAC 拿到 NT hash 之后?
**A6**: 就是标准 hash 后续 → PtH 或再滚一轮:如果是高权账号,拿去 DCSync;如果是服务账号,访问其服务;如果是普通用户,BloodHound outbound path。

### 密码喷洒的节奏和判据
**Q1**: 手上有用户名列表想 spray,首先看什么?
**A1**: 三件事:①域锁定策略(通过 `net accounts` 或 rpcclient/samr 查 lockout threshold/duration/observation window);②当前是否已被锁的账号(避免继续戳);③域业务时间(工作时间喷,登录失败混在正常噪声里)。
**Q2**: 密码怎么选?
**A2**: 三档:①公司/品牌相关(组织缩写+年份/数字后缀、缩写+特殊符号)、②季节/季度相关(季节/季度+当年份+符号)、③行业通用弱口令模式(通用词+数字/符号,来自常见公开字典)。每轮换一个,周期长于 observation window。具体值按目标 OSINT 结果生成,不预设。
**Q3**: 喷了一轮都不中,继续吗?
**A3**: 判据:如果密码策略 lockout=5,observation=30min,一天最多 4~5 轮 × 用户数。喷 3 轮无果 → 密码列表策略错误(公司文化偏离常识)、或者用户名列表污染 → 停下重新收集情报。
**Q4**: 一个账号突然全部尝试都锁,别的正常?
**A4**: 蜜罐账号信号(honey user)——这个账号是蓝队故意留的诱饵,任何认证都告警。立刻从列表移除,并且反思是否已被追溯。
**Q5**: 喷到一个凭据,是低权用户,继续喷还是深入?
**A5**: 深入优先:用这个凭据做 LDAP 完整枚举、BloodHound 收集、shares 探测、Kerberoast/AS-REP roast(现在有凭据了)。喷是入口,不是终点。
**Q6**: 密码策略强 + lockout 严 + 蜜罐,完全喷不动?
**A6**: 转向"无认证攻击":coerce、责任者协议欺骗、未认证 CVE、ADCS ESC8(HTTP relay)。或者用少量已知信息发起 targeted phishing 换真凭据。

### LSA secrets 的宝藏
**Q1**: SECURITY hive 里 LSA secrets 具体能挖什么?
**A1**: 服务账号明文(如果服务配了域账号 run-as)、DPAPI SYSTEM key、DefaultPassword(自动登录时写这里)、机器账号缓存密码(NL$KM 等前缀)。impacket secretsdump 会自动分类输出。
**Q2**: 看到 `_SC_服务名` 开头的,值得吗?
**A2**: 极值得——这是"服务运行身份"的密码明文。命名带 svc/backup/sql 的都要关注,可能就是域内服务账号,权限往往在多台机器上有本地管理员。
**Q3**: 看到 `NL$KM` 只有二进制?
**A3**: 那是 domain cached credentials 的加密 key,配合 `$MACHINE.ACC` 使用。secretsdump 会自动整理输出 cached DCC2 hash 用于离线爆破。
**Q4**: 一个 LSA secret 都没有?
**A4**: 说明该机没配任何"以域身份运行"的服务,是纯客户端机。转向用户 profile 层的凭据(浏览器/Credential Manager/RDP 保存)、或该机器上文件系统里的配置文件。
**Q5**: 拿到服务账号明文,直接横向?
**A5**: 先枚举:该账号在域里的 memberOf、servicePrincipalName、AdminCount。看它是不是关键权限持有者;然后测本地管理员复用面(spray 到关键服务器)。
**Q6**: 服务账号被禁用了,明文还有用吗?
**A6**: 有——测试是不是"禁用但可用"(某些老配置)、测试历史密码是否被别的账号沿用、作为密码模式样本喂给字典生成器。

### Windows Credential Manager 的低垂果实
**Q1**: 普通用户会话下,想快速看有没有保存凭据?
**A1**: `cmdkey /list` 列出 Windows Vault + Credential Manager 条目,`vaultcmd /list` 看 web/generic 类。有 target 是内部服务器名/邮箱域名的都值得关注。
**Q2**: 看到有条目但拿不到明文,怎么办?
**A2**: 转 mimikatz `vault::cred /patch` + `dpapi::cred`,需要该用户 DPAPI master key(在其登录会话里可直接解;离线则前面 DPAPI 链)。
**Q3**: 有 RDP 保存的凭据(TERMSRV/...)?
**A3**: 直接 mimikatz `dpapi::rdg` 或者在该用户会话下直接 mstsc 用保存凭据登录,不需要解 → 直接横向到目标机(该目标机通常是他常用的服务器,大概率高权)。
**Q4**: cmdkey list 完全空?
**A4**: 转向浏览器/其他应用的密码存储:VS Code (Git credentials)、SSMS(SQL server 连接)、Outlook (mapi profile)、企业 VPN 客户端(通常存 %APPDATA%)。
**Q5**: 有条目但都是 web 凭据不是网络凭据,能用吗?
**A5**: 有用——这些通常是内部 SharePoint、Confluence、Jira、GitLab 的账号密码,拿到后横向到内部代码/文档平台,可能翻到更多凭据或敏感数据(源代码里的硬编码 key)。
**Q6**: 想批量抓所有已登录用户的 Vault?
**A6**: 需要 SYSTEM + 逐个 impersonate 每个 logon session 的 token 去解 Vault(用户绑定)。或者夜里等一夜看多少不同用户登录过、批量收。

### SCCM/MECM 网络访问账号
**Q1**: 发现目标有 SCCM(MECM)客户端(`ccmexec.exe`),意味着什么?
**A1**: 巨大宝藏。SCCM 有几个高价值凭据:Network Access Account(NAA)、Task Sequence 凭据、Client Push Installation Account、Site System Installation Account。NAA 尤其常见,存在 WMI 里可提取。
**Q2**: 怎么抓 NAA?
**A2**: 从 CCM WMI 类里读加密的 NAA blob,配合本机 DPAPI 解密。工具:SharpSCCM、cmloot。必须在装了 SCCM 客户端的机器上跑,DPAPI 依赖本机 SYSTEM。
**Q3**: 拿到 NAA 后?
**A3**: NAA 通常是配来给客户端从 DP(分发点)拉部署包的域账号,权限有限但——大概率在多台机器有,是横向面广的账号;而且如果配错了权限,可能是本地管理员/域管。
**Q4**: SCCM 服务器本身有更多可玩的?
**A4**: 有。SCCM Site Server 数据库(MSSQL)里有加密的 client push 账号、有 policy 里的敏感数据。走 SQL 认证 or 域权限访问,配合 SCCMHunter/misconfig-manager。
**Q5**: 目标环境没 SCCM,类似的可挖?
**A5**: MDT(部署共享里的 CustomSettings.ini/Bootstrap.ini 有凭据)、Intune、Ansible Tower / AWX、Chef/Puppet、Jenkins 凭据管理器、Vault(如果 misconfig)、GPP。企业运维基础设施都是凭据聚集地。
**Q6**: SCCM 客户端在,但 WMI/DPAPI 抓不到 NAA?
**A6**: 可能没配 NAA(现代 SCCM 推荐用 Enhanced HTTP + 客户端证书代替 NAA)。转向抓 SCCM 客户端证书本身,或 relay SCCM Web 端点。

### AdminSDHolder 与 sIDHistory
**Q1**: 想长期后门域,除了金票还有什么方式?
**A1**: 几种:①AdminSDHolder 加 ACE(SDProp 每 60min 反向下推到所有 protected group,难清除);②sIDHistory 塞进域管 SID(需要 DA 一次,后续该账号永远 elevated);③修改 KRBTGT 密码(限制金票范围)但同时植入 backup 账号。
**Q2**: sIDHistory 具体怎么做?
**A2**: 已 DA 时,给某低权账号写 sIDHistory 属性包含 DA 组 SID(需要用 DSInternals/mimikatz `misc::addsid`)。之后该账号即便不在 DA 组,Kerberos ticket 里也带 DA SID。
**Q3**: 会被检测吗?
**A3**: 现代 EDR/AD audit 有 sIDHistory 修改事件(4738)。判据:如果是要极隐蔽长期后门,风险高;短期利用即可,不要留。或者利用合法信任域场景下的 SIDHistory,混在正常之中。
**Q4**: 想用 sIDHistory 提权(不是持久化)可行吗?
**A4**: 需要 DA 权限才能写自己或别人的 sIDHistory → 已经 DA 了没必要用它提权。sIDHistory 通常是持久化用。
**Q5**: 目标域启了 SID Filtering(信任域间)?
**A5**: 跨域 sIDHistory 攻击(经典 golden ticket + external trust)被 SID filter quarantine 阻挡。转向 forest trust attack(如果是同 forest 不同 domain,SID filter 弱)。
**Q6**: 长期后门被安全评估会翻出来吗?
**A6**: 大概率会——现代 AD 健康检查工具(PingCastle/BloodHound Enterprise)会扫 AdminSDHolder ACL 异常、sIDHistory 异常。判据:留后门就要能承受被清算的风险,或者做得足够像"合法遗留配置"。

### 卷影副本(VSS)的多用途
**Q1**: 想抓被锁定的 SAM/ntds.dit 但直接 copy 失败,VSS 怎么用?
**A1**: `vssadmin create shadow /for=C:` 创建 → 记下 shadow copy 路径(GLOBALROOT\Device\HarddiskVolumeShadowCopy1) → 从这个只读快照复制文件。绕过独占锁。
**Q2**: vssadmin 需要管理员,SYSTEM 可以吗?
**A2**: SYSTEM 可以创建 shadow,但会有 8193/8194 事件日志。判据:如果目标已有其他 shadow(备份任务遗留),优先复用已有 shadow 避免创建新事件。
**Q3**: 想 stealth,能用什么替代?
**A3**: `diskshadow` 脚本模式(交互工具但可自动化),wbadmin(某些场景可 dump ntds 到备份文件),或 PowerShell WMI Win32_ShadowCopy.Create。选噪声最小的。
**Q4**: 打完想清理 shadow?
**A4**: `vssadmin delete shadows /shadow=<id>`。注意:删 shadow 也是勒索软件常见动作,可能触发 EDR ransomware 保护规则。判据:如果删 shadow 触发告警,不如留着装作系统正常快照。
**Q5**: VSS 服务被禁?
**A5**: 转向直接读硬盘扇区(需要驱动或特殊工具:libguestfs/RawCopy),或者把机器加入现有备份系统看备份产物。
**Q6**: shadow 里的文件时间戳会暴露我?
**A6**: shadow 快照有独立时间点,访问 shadow 里的文件不改主卷时间戳;但创建 shadow 本身的时间是可见的。判据:在业务快照时段附近创建可以混淆。

### 打印机凭据宝库
**Q1**: 网络里发现多台打印机(网络扫到 631/9100/515),值得看吗?
**A1**: 极值得——多功能打印机(MFP)常配 LDAP 集成(扫描到邮箱/文件夹),存了域账号明文口令。管理界面通常 admin/admin 或 default。
**Q2**: 具体怎么抓?
**A2**: 登 Web 管理界面 → 找 "Scan to Email" / "LDAP settings" / "Address Book",通常有密码字段(有的直接明文显示,有的星号但表单里可见,有的需要 firmware 特定漏洞 dump 配置文件)。
**Q3**: 打印机存的账号有什么权限?
**A3**: 通常是"扫描到共享"账号,权限有限,但——常常是域账号且不常 rotate,而且配置到多台打印机(密码复用面广)。作为初始入口极佳。
**Q4**: 打印机管理界面需要认证进不去?
**A4**: 三条路:①默认凭据大字典(各厂商公开的出厂默认账号/口令);②厂商漏洞(SNMP OID 读配置、未认证接口 dump);③物理接触(打印机后面贴纸常有默认账号)。
**Q5**: SNMP 打印机 community=public 也是宝?
**A5**: 是——很多打印机 SNMP 直接暴露 LDAP 配置和密码。`snmpwalk -c public <ip>` 扫遍 OID。
**Q6**: 全部打印机都在独立管理 VLAN,进不去?
**A6**: 转向其他 IoT 类设备:VoIP 电话(常配 LDAP)、视频会议(Polycom/Cisco)、门禁系统、UPS 管理卡、iLO/iDRAC/IPMI(前面几个都可能有域集成凭据)。

### Azure/Entra 混合环境的凭据面
**Q1**: 目标域有 Azure AD Connect(hybrid identity),额外凭据面在哪?
**A1**: Azure AD Connect 服务器本身是一个 tier-0 目标——上面运行的 MSOL_ 账号在 AD 里有 DCSync 权限;而且服务器上有 ADSync 数据库(LocalDB),里面有解 hybrid 密码的密钥。
**Q2**: 具体怎么薅?
**A2**: 在 AAD Connect 服务器上(需 SYSTEM),用 AADInternals 或手动 dump LocalDB 里的 keyset,配合 DPAPI 解出 MSOL_ 账号明文 + Azure Service Account (Sync_) 凭据。
**Q3**: MSOL_ 有什么用?
**A3**: 域内 DCSync 权限——等价拿到 DA。而且很多环境把 AAD Connect 服务器忘了纳入 tier-0 保护,是个大漏。
**Q4**: Sync_@<tenant>.onmicrosoft.com 呢?
**A4**: 云端权限——通常有 Directory Synchronization Accounts role,可以改用户密码、加 admin。是从 on-prem 打到 云 的经典跳板。
**Q5**: 目标是 pass-through auth 或 federated,不用 password hash sync?
**A5**: 转向 PTA agent 服务器(有 credential harvesting 攻击可拦截认证);或 ADFS server 上的 token signing certificate(Golden SAML)。
**Q6**: 想直接从 on-prem 用户 hash 打云?
**A6**: PHS 场景下 on-prem hash 不能直接登云(hash of hash),但拿到 hash 后离线爆破 → 用明文登云。或者用 Seamless SSO 的 AZUREADSSOACC$ 机器账号 hash 做 silver ticket → 云 SSO。

### MSSQL 链接服务器与 xp_cmdshell 拿凭据
**Q1**: 内网发现 MSSQL 且能用某个域账号连,怎么挖凭据面?
**A1**: 三步:①`SELECT SYSTEM_USER, IS_SRVROLEMEMBER('sysadmin')` 看权限;②`EXEC sp_linkedservers` 看有没有 linked server 到其他 SQL(常见 misconfig:linked 用 sa 或高权账号,可以跳过去);③如果 sysadmin,`xp_cmdshell` 直接命令执行拿 LSASS。
**Q2**: 不是 sysadmin,只有 public,还能挖啥?
**A2**: 转向:①UNC 路径回连——`xp_dirtree \\<my>\a` 强制 MSSQL 服务账号 NetNTLMv2 回连,抓 hash;②TRUSTWORTHY + db_owner 提到 sysadmin(经典 misconfig);③impersonate 其他登录(`EXECUTE AS LOGIN='sa'` 如果被授权)。
**Q3**: MSSQL 跑的账号是什么?
**A3**: 默认可能 NT Service\MSSQLSERVER(SYSTEM 等价 on host)或域账号(如 svc_sql)。如果域账号 + 多台 SQL 复用 = 横向到多台;如果 SYSTEM 且服务器有 unconstrained delegation = 天选跳板。
**Q4**: linked server 链接到别的 SQL 走的什么身份?
**A4**: 看 sysservers 里的 self-mapping:如果是"current context"就走当前用户;如果配了固定登录(常见 sa/远程管理员),就等价拿到远端权限。`SELECT * FROM OPENQUERY(<linked>, 'SELECT SYSTEM_USER, IS_SRVROLEMEMBER(''sysadmin'')')` 试。
**Q5**: xp_cmdshell 被禁,还有命令执行路径吗?
**A5**: `sp_configure` 打开(需 sysadmin)、CLR 存储过程加载 .NET assembly、Ole Automation Procedures、代理作业(SQL Agent job with cmdexec type)。多条路。
**Q6**: MSSQL 完全打不动怎么办?
**A6**: 转向其他数据库服务面:Oracle(默认 dbsnmp/system 弱口令)、MySQL(root 无密/低权文件读)、PostgreSQL(信任认证 misconfig),各有凭据/命令执行路径;或者转向应用配置文件(web.config 里的连接字符串)。

### 邮件服务器上的凭据洞
**Q1**: 目标环境有 Exchange,凭据薅取面在哪?
**A1**: 多层:①Exchange 服务器本身 tier-0 前时代常被忽略,上面 LSASS 有大量交互登录;②Exchange 服务账号 EWS/Autodiscover 认证可 relay;③邮箱内容本身(搜索 password/账号 等关键字);④OAB 里可能有旧密码文件。
**Q2**: 拿到某用户邮箱访问权,怎么快速挖凭据?
**A2**: 搜索规则:`password / pwd / 密码 / 口令 / vpn / 账号 / credentials / .kdbx / .rdp`,以及附件 zip/rar/xlsx 里的账号本。IT 运维/HR 邮箱尤其肥。
**Q3**: 通过 EWS/OWA/Graph 只有普通用户权限,能扩吗?
**A3**: 看 ApplicationImpersonation RBAC role(如果目标账号有,一号打全公司邮箱)、看 mailbox delegate 权限(某助理 delegate 到 CEO)、Autodiscover 泄漏用户列表用于喷洒。
**Q4**: Exchange 有历史 CVE 可利用(ProxyLogon/ProxyShell/ProxyNotShell)?
**A4**: 判据:先看外部资产是否可达 + Exchange 版本 build 号 + 补丁时间。如果不能利用,转向 Exchange 服务账号 relay:HTTP 认证 push 到 LDAPS。
**Q5**: 无 Exchange 但有其他邮件系统(Zimbra/Mailu/腾讯企业邮/飞书邮箱)?
**A5**: 转向对应的洞和凭据点:Zimbra 有大量历史 CVE;云邮件走 API token(内部管理台可能能签发 impersonation token)。凭据薅取的通用道理是"人的凭据都在邮箱里出现过一次"。
**Q6**: 邮箱访问不到,能不能通过 SMTP relay 钓?
**A6**: 转向"发内部钓鱼邮":内部 SMTP 常允许免认证发信,发一封伪装为 IT 部门的密码重置邮件,链接指向自己搭的 fake OWA/SSO,采到明文。凭据薅取有社工侧路径。

### Kerberos AS-REQ 用户名枚举 + 加密降级
**Q1**: 想枚举有效用户名(为后续 spray/AS-REP roast),什么最快?
**A1**: Kerberos AS-REQ 用户名枚举:发不带预认证的 AS-REQ,KDC 对存在/不存在/需要预认证的用户返回不同错误码(KDC_ERR_C_PRINCIPAL_UNKNOWN vs KDC_ERR_PREAUTH_REQUIRED)。工具 kerbrute。快而且不产生锁定。
**Q2**: 用户名从哪来?
**A2**: OSINT 公司邮件格式(领英/官网)、内网 LDAP 匿名或已知凭据枚举、SMB null session RID cycling、GPO 相关文件里出现的用户名、邮件签名。
**Q3**: 枚举出用户名后,除了 spray 还能干嘛?
**A3**: ①AS-REP roast 那些没预认证的;②timeroast(Windows 特有,老 KDC 允许 unauth 请求某些 principal 的 ticket 探测存在性);③精准 targeted phishing(有真实工号)。
**Q4**: 枚举完发现所有账号 msDS-SupportedEncryptionTypes 只允许 AES,爆破成本高,怎么处理?
**A4**: 判据:当加密策略统一收紧(2019+ / 安全基线),说明这是有意加固的环境 → 放弃"AS-REQ 侧优化"这个正交维度,把用户名情报转到"非爆破路径"(证书类攻击、委派滥用、ACL 提权、精准 phishing)。降级已在别处讨论过,不重复。
**Q5**: 用户名列表拿到后,想立刻做"低成本试错组合"?
**A5**: 组合矩阵:枚举出的用户名 × (无预认证 → AS-REP roast) × (spray 极低轮次 → 抓 admin 邮箱前缀式弱口令) × (kerbrute password auth 判断存活但不锁定的边界)。每步都要对齐 lockout 策略,别把枚举成果反过来锁死账号。
**Q6**: 想在网上抓 Kerberos 流量做离线分析?
**A6**: MITM(ARP spoof/DHCPv6 mitm6)拦 88 端口流量;或从 pcap 里翻已抓到的 AS-REP 里 encrypted timestamp 做爆破(但需要该账号错配无预认证 or 弱口令)。这条路 ROI 低,通常放弃。

### FIDO2/Passkey/WHfB 时代的应对
**Q1**: 目标用户用 Windows Hello for Business (PIN + TPM) 登录,LSASS 里没 NTLM 怎么办?
**A1**: WHfB 是证书+TPM,用户 NTLM 不在内存里(除非 SSO 需要),但 TGT 有——转向 sekurlsa::tickets 拿 TGT 做 pass-the-ticket。或者用户 msDS-KeyCredentialLink 里的证书是自己的公钥,不能直接反向拿密钥(在 TPM 里)。
**Q2**: 想拿该用户 NT hash(为 PtH 到不支持 Kerberos 的服务)?
**A2**: UnPAC-the-hash:用 WHfB 证书走 PKINIT 拿 TGT,TGT 的 PAC 里的 PAC_CREDENTIAL_INFO 包含 NTLM。前提:自己得有该证书(通常你没有,除非你控 CA)。或用 kekeo 的相关模块。
**Q3**: 用户是纯 FIDO2 硬件 key 登录,连 TPM 都没,可乘之机?
**A3**: 转向"用户登录后的 downstream":TGT 在,session cookie 在,DPAPI master key 已解锁在内存 → 从内存/进程角度,还是能薅到用户身份的操作能力,只是不能"拿密码本身"。
**Q4**: 应用只认 Kerberos + PKINIT,不认 NTLM?
**A4**: 现代趋势(Disable NTLM 已开始铺开)。判据:接受这个现实,主打 Kerberos 手法(PtT、S4U、certificate abuse)。NTLM 相关技术(relay/PtH)前景递减。
**Q5**: passkey/SSO 环境下,还有什么低垂果实?
**A5**: 转向 session token / refresh token(浏览器 cookie、Windows PRT/Primary Refresh Token)、Azure AD Broker Plugin cookies(在 Windows 上抓 PRT 就能免密登云所有 SSO 应用)。
**Q6**: PRT 怎么抓?
**A6**: `dsregcmd /status` 看是否加入 AzureAD;用 ROADtools/AADInternals/dirkjanm 的 pipe 相关技巧,SYSTEM 权限下从 LSASS 抠 PRT + session key,加密带走登云。这是"云凭据薅取"的核心。

### 备份系统:被遗忘的凭据金矿
**Q1**: 目标环境有 Veeam/CommVault/NetBackup/Rubrik 之一,值得优先打吗?
**A1**: 极值得——备份系统天然需要"能访问所有资产"的权限,通常持有 DA 或域备份账号 hash;而且往往被排除在 EDR 之外(避免影响备份性能)。是横向和凭据薅取双料矿。
**Q2**: Veeam 具体怎么薅?
**A2**: Veeam Backup 服务器有 SQL/PostgreSQL 后端存储凭据,加密 key 在注册表(Veeam 有历史 CVE 让未认证读凭据)。另外 Veeam 用一个 backup account,常是 DA 或本地管理员。
**Q3**: 拿到备份账号 hash 后,除了当域凭据用,还能做什么?
**A3**: 用备份账号去请求"恢复"任意机器的最新备份 → 恢复到备份服务器可挂载的位置 → 从恢复的镜像里离线抓 SAM/ntds.dit/LSASS 转储。零触碰目标机。
**Q4**: 备份存储在磁带/离线介质,没法直接恢复?
**A4**: 转向近期在线的增量备份/快照——大多数环境保留最近 7-30 天的在线快照(RPO 要求)。
**Q5**: 备份系统在独立管理网,进不去?
**A5**: 转向"备份代理"(agent):被备份的机器上装了 agent,通常和备份服务器双向认证,agent 服务账号常有横向能力。或者攻击"备份配置存储库"(可能是文件共享)。
**Q6**: 完全无备份系统 access?
**A6**: 转向类似"横向面广"的角色:监控系统(Zabbix/Nagios/PRTG 常配管理员 agent)、部署系统(Ansible/SCCM)、AV/EDR 管理台(SentinelOne/CrowdStrike 管理员账号可以推任意 payload)。

### GMSA/sMSA 与 KDS root key
**Q1**: 域里有 GMSA (Group Managed Service Account),怎么用?
**A1**: GMSA 密码由 KDC 每 30 天自动 rotate,存在 msDS-ManagedPassword blob 里,只有 msDS-GroupMSAMembership 允许的主体能读。判据:如果我控制的账号在允许列表里,可以在线读 GMSA 密码。
**Q2**: 具体怎么读?
**A2**: `Get-ADServiceAccount -Identity <gmsa> -Properties msDS-ManagedPassword` 或 GMSApasswordReader / gMSADumper。返回二进制,解出 current + previous NTLM。
**Q3**: 不在允许读的列表里,能提到吗?
**A3**: 找对 GMSA 对象有 WriteProperty on msDS-GroupMSAMembership 的账号(把自己加进去),或者对该属性的 ACE 有 WriteDACL。
**Q4**: 更狠的:拿到 KDS root key 之后?
**A4**: KDS root key 存在 DC 上,DA 可读。有它就能离线计算任何时刻的任何 GMSA 密码(不需要一直有读权限)。工具 GoldenGMSA。
**Q5**: GMSA 用来跑什么服务?
**A5**: 通常是集群/负载均衡的关键服务:IIS 应用池、SQL Server、SharePoint、Exchange DAG。拿到 GMSA hash 等于该服务的最高身份。
**Q6**: 没有 GMSA,只有 sMSA(单机 MSA)呢?
**A6**: sMSA 密码存本机 LSA secrets,加密方式和普通 MSA 一样 → 本机 SYSTEM 就能 dump。转向前面"LSA secrets"那一链。

### 云上凭据:云控制台 metadata 的类比
**Q1**: 内网机器如果是"IaaS 虚机"(比如自建 vSphere 但接了 Terraform,或者跑在私有云),有类似云 metadata 的凭据入口吗?
**A1**: 有——检查 `~/.vmware/`、cloud-init logs、启动脚本里可能有引导凭据(bootstrap key、agent token)。私有云也会用 config drive/nocloud 挂载配置。
**Q2**: 目标机器是 Azure/AWS/GCP VM(即使是"混合"AD 场景)?
**A2**: 打云 metadata:AWS IMDSv1 (v2 需 token) `169.254.169.254`、Azure IMDS `169.254.169.254/metadata/`、GCP `metadata.google.internal`。返回临时 STS credential,配合 SSRF/命令执行也能远程打。
**Q3**: 拿到云 credential 后,如何与 AD 凭据串起来?
**A3**: 看 AD 里有没有"云管理员账号"——SSO 到云的账号;或者相反,云上 SSM/SecretsManager 里可能存了 AD 服务账号密码(用来跑 domain join 之类)。
**Q4**: 云 VM 上的 managed identity 可以读 KeyVault?
**A4**: 如果目标 VM 的 managed identity 有 KeyVault 权限,`az keyvault secret list` 一把,里面可能有 domain admin/service accounts 明文。
**Q5**: 完全隔离,内网机器无法访问 metadata endpoint?
**A5**: 转向机器上的 CLI 配置:`~/.aws/credentials`、`~/.azure/`、`~/.config/gcloud/` 常有长效 access key/refresh token。
**Q6**: 想从 on-prem 打到云凭据面,起点是什么?
**A6**: 前面 Azure AD Connect 那链 + Golden SAML + PRT 抓取。凭据薅取到云端就是攻击面从"内网"扩到"租户"。

### 记录与撤退:凭据薅到后的自律
**Q1**: 一次行动薅到几十个 hash + 一些明文,怎么整理?
**A1**: 结构化:账号名 / 类型(用户/机器/服务)/ hash 类型(NTLM/DCC2/AES)/ 来源(哪台机)/ 该账号已知权限 / 时间戳。避免混乱重复利用,也避免遗漏高价值账号。
**Q2**: 什么时候该停止继续横向,先撤?
**A2**: 判据:①目标达成(拿到指定资产/DA);②检测风险陡增(EDR 告警可能已发出);③凭据面已经饱和(继续挖收益递减)。三种任一 → 记录+撤+持久化。
**Q3**: 撤之前做什么?
**A3**: 清理:删除 dump 文件、清 event 日志(注意清日志本身告警)、恢复注册表改动、移除持久化以外的植入。别贪。
**Q4**: 持久化用什么账号?
**A4**: 别用最高权账号做持久化——用一个不起眼的服务账号或次高权账号做定期 beacon 回来,DA 留作"关键时刻用一次"的核弹。分层。
**Q5**: 万一凭据被 rotate 了怎么快速恢复访问?
**A5**: 至少两条独立入口:①凭据(可能失效);②Shadow Credentials / KeyCredentialLink(基于公钥,不受密码 rotate 影响,一次配置长期有效);③计划任务/服务持久化(不依赖凭据)。
**Q6**: 判据:什么时候承认"这次凭据薅取失败"?
**A6**: 尝试了 LSASS + SAM + LSA secrets + DPAPI + 浏览器 + 服务账号 + 文件系统搜 + coerce/relay + spray 全维度 → 都无进展 → 转"凭据薅取"为"漏洞路径"(未认证 CVE、web 应用)、或者社工路径(钓鱼、水坑)。不要在一个维度死磕。
