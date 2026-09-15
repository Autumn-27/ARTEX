# AD内网 · 直捣域控 vs 横向薅凭据的转向决策

### 刚上岸一台机器,先摸身份还是先摸网络
**Q1**: 我通过某种立足点进了一台 Windows,shell 拿到了,该先做什么?
**A1**: 第一动作永远是 `whoami /all` + `net config workstation` + `nltest /dsgetdc:`。判据:这机器是不是域成员?我是本地用户还是域用户?我在哪个域、DC 是谁?这三个答案直接决定后续所有路径分叉——如果不是域成员,别急着打 AD;是域成员但我是本地 shell,要先本地提权拿 SYSTEM 才能碰 lsass/sam。
**Q2**: 如果 `whoami /all` 显示我是低权域用户呢?
**A2**: 那就是黄金起点。低权域用户就能对 LDAP 只读枚举整个域(用户/组/GPO/委派/ACL),这是 BloodHound 的门槛。此刻别急着提权本机,先把域画像抓下来,一次 LDAP 查询可能就看到清晰的横向路径。
**Q3**: 如果是本地用户、机器压根不在域里?
**A3**: 立刻转向"这台机器有没有域的痕迹"——查 `cmdkey /list`、DPAPI 保管库、浏览器保存的 SMB/RDP 凭据、`.rdp` 文件、`runas /savecred` 记录、hosts 里手写的域内名字。孤岛机器往往是维护跳板,凭据管理器里躺着通往域内的门。
**Q4**: 域画像和本机凭据都空,机器又完全孤立怎么办?
**A4**: 转向正交维度——这台机器的网络位置本身就是资产。arp/route/netstat 找它能到达的其他网段,它可能是 DMZ↔内网 的双网卡跳板。此时价值不是"它能给我域",而是"它能让我打到能给我域的东西"。

### 直捣 DC 前的第一次冷静判断
**Q1**: 手上有域内立足点了,我要不要立刻上 zerologon/PrintNightmare 打 DC?
**A1**: 先冷静。判据:DC 补丁状态未知、蓝队是否有全流量、行动窗口有多大。zerologon 会把 DC 机器账号密码清零,不做恢复会直接把整个域搞崩,业务侧一小时内必然告警。除非目标就是"证明拿到域管",否则第一次评估应偏向"能不能薅凭据无声上位"。
**Q2**: 那什么时候直捣 DC 是合理的?
**A2**: 三个条件同时满足:(1) 指纹显示 DC 明显没补丁(SMB 版本、SPN 里带旧特征、`nmap --script smb2-security-mode` 出老版本);(2) 时间紧、必须速通;(3) 有恢复计划(zerologon 打完能 restore machine password)。缺一个都应该转向凭据薅取路线。
**Q3**: 如果一开始就选薅凭据慢推,结果发现内网权限森严凭据难拿,再回头打 DC 来得及吗?
**A3**: 通常来得及,但要评估"我这次侦察是否已经让 DC 产生告警"。如果只做了 LDAP 只读、没触发过 4625/4771,回头切爆破式路线代价不大。反过来,如果一开始就上 zerologon 打崩了,再想低调走凭据路线基本不可能——不可逆动作要放最后。
**Q4**: 完全打不动 DC 也薅不到凭据怎么办?
**A4**: 转向"信任关系"这个正交维度。看有没有外部/森林信任、有没有子域、有没有 Azure AD Connect 同步。经常主域打不动,但子域或者对等的合作方森林是薄弱环节,从旁边打进来再借信任回捅主域比硬啃 DC 舒服得多。

### 有没有域用户凭据决定整个玩法
**Q1**: 我到底该走"无凭据阶段"还是"有凭据阶段"的枚举?这是完全两套动作。
**A1**: 判据非常干脆:能不能 `ldapsearch` 匿名或者用手上账号读到域对象?能读=有凭据阶段,不能读=无凭据阶段。别混着做,混着做就会用有凭据阶段的动作触发无凭据阶段的告警。
**Q2**: 无凭据阶段该做什么?
**A2**: 三件事:(1) 无认证 SMB 枚举可能的用户名(RID cycling、NULL session,老域偶尔通);(2) 用常见姓名字典去打 AS-REP roasting(不需要密码就能拿到不预认证账号的哈希);(3) 打 Kerberos 用户枚举(不同错误码区分账号存在与否)。目的都是把"无"变成"有"。
**Q3**: 有凭据阶段最优先做什么?
**A3**: 只有一件事——完整 BloodHound 采集。用户/组/ACL/GPO/委派/会话全抓。因为有凭据之后决策空间瞬间爆炸,不做全景图就是在瞎打。BloodHound 出来后 shortest path to Domain Admins 常常直接告诉你走哪条最短路径。
**Q4**: 有凭据但账号被限制、连 LDAP 都读不全怎么办?
**A4**: 转向"凭据升级"维度。这个账号能干什么?试 SMB 枚举共享(常常 LDAP 被限但 SMB 能读)、试 WinRM/RDP 登录任意机器(哪怕只能登一台也能开局)、试 MSSQL 服务账号连接(有些账号只能连数据库不能查 AD)。凭据的"权限面"是多维的,LDAP 卡死不等于全卡。

### AS-REP roasting 值不值得先跑
**Q1**: 我拿到用户名列表(甚至只是猜的),要不要立刻打 AS-REP?
**A1**: 判据两条:(1) 我有没有用户名候选(哪怕是从 OSINT 猜的姓名格式);(2) 我离 KDC 的网络路径通不通。都通就应该跑,因为 AS-REP roasting 完全不需要有效密码,失败也只是 KDC 日志里多几条 4768,风险极低。
**Q2**: 跑出来的哈希爆不开怎么办?
**A2**: 别死磕。AS-REP 用的是 RC4 哈希,弱口令用户几秒到几小时能爆,强口令基本不可能。爆不开就是这批账号密码不弱,转向下一维度而不是加大字典。
**Q3**: 下一维度是什么?
**A3**: 有两个正交方向:(a) 密码喷洒——用同一个季节+年份+公司名字的口令去喷所有账号,专抓"密码策略执行不严的账号"而不是"预认证关闭的账号";(b) Kerberoasting——如果我已经有任意一个有效域账号,就能对所有 SPN 账号请求 TGS,服务账号密码通常比人肉账号弱得多。
**Q4**: 一个用户名都猜不到怎么办?
**A4**: 转向"用户名收集"这个前置维度。用 kerbrute 打常见姓名字典试探(Kerberos 预认证错误码能区分账号存在),或用 OWA/Exchange 前台的时序差异枚举邮箱前缀,或者从公司官网/GitHub/招聘信息刮员工英文名格式。用户名不是目标,是弹药。

### Kerberoasting 该不该马上打
**Q1**: 我已经是低权域用户,BloodHound 显示有几十个 SPN 账号,该立刻批量打 Kerberoast 吗?
**A1**: 该打,但要挑。原则:优先请求"高特权组成员的 SPN 账号"的 TGS(BloodHound 直接标出来)、优先 RC4 加密的 TGS(4769 里 encryption type 0x17,好爆)、避开"最近改过密码"的账号(pwdLastSet 太新的爆不开是浪费)。
**Q2**: 全部 SPN 都是 AES 加密(0x11/0x12)怎么办?
**A2**: AES TGS 也能爆,但速度比 RC4 慢一到两个数量级。判据:值不值得跑取决于目标账号价值。如果 SPN 属于 Domain Admin,再慢也上;如果只是普通服务账号,直接换维度。
**Q3**: 打了半天全部爆不开怎么办?
**A3**: 承认现实,别加字典了。转向两个方向:(a) 找服务账号跑起来的具体机器,在那台机器上抓明文/DPAPI(服务经常以 LogonType 5 交互式跑,内存里有明文);(b) 看这些服务账号在 BloodHound 里的 ACL 权限——有时候账号密码爆不开,但它对某个对象有 GenericWrite/WriteDACL,直接 abuse 权限比爆密码快。
**Q4**: 蓝队开启了 AES Only + FAST(Kerberos Armoring)怎么办?
**A4**: Kerberoasting 基本报废,转向完全正交的维度:凭据薅取(mimikatz/DPAPI)、委派滥用、ADCS 证书路径、GPO 权限滥用。不要在被完全对抗掉的路径上耗时。

### DC 版本指纹显示老到掉牙,直接砸门?
**Q1**: nmap 扫 DC 显示 SMB1 开着、系统版本 2008 R2,是不是可以直接 MS17-010 一波?
**A1**: 停。三个反问先答:(1) 这机器真的是 DC 吗,还是名字里带 DC 但只是文件服务器?(2) EternalBlue 打崩了 DC 我有没有恢复方案?(3) 蓝队有没有 EDR,老系统不代表没 EDR。都过关再动手,老系统 + 现代 EDR 反而是蓝队常见的"蜜罐化"打法。
**Q2**: 确认是 DC 且没 EDR,该打吗?
**A2**: EternalBlue 打 DC 极高概率蓝屏。要打就用现代变种(内存注入而非 DoublePulsar 那种老手法),或者退一步——既然 SMB1 都开着,先看有没有 zerologon(CVE-2020-1472)或者 PrintNightmare(CVE-2021-34527),这两个不崩机、更隐蔽、更彻底。
**Q3**: 都尝试了,都失败或都不适用怎么办?
**A3**: 转向"打 DC 周边"而不是打 DC 本身。DC 上跑的其他服务(WSUS、CA、Exchange、SCCM)常常在 DC 或 DC 邻居机器上,它们中任何一个被拿下往往等价于拿下域。ADCS 尤其值得看——一个错配的证书模板等于一张万能通行证。
**Q4**: DC 周边也都硬,还能怎么办?
**A4**: 转向"人"这个正交维度。域管账号会登录哪些机器?在那些机器上等他,而不是在 DC 前门死磕。BloodHound 的 session 数据、事件日志里的 4624 都能反推域管落脚点,拿下一个域管日常用的运维跳板等于拿下域管。

### ADCS 存不存在,决定要不要走证书路线
**Q1**: 我怎么快速判断这个域有没有 ADCS,以及有没有 ESC 漏洞?
**A1**: 三条:(1) LDAP 查 `objectClass=pKIEnrollmentService` 或 `certificationAuthority`,有对象就是有 CA;(2) 用 certipy/Certify 一把梭 `find`,直接列出所有模板+ESC 类型标记;(3) 看有没有 Web Enrollment(HTTP 端口 80/443 上的 /certsrv),有的话 ESC8 relay 直接可玩。
**Q2**: 发现有 ESC1(SAN 可控)但我当前账号权限申请不了模板怎么办?
**A2**: 判断"我缺的是什么"——是模板 ACL 不让我 enroll?还是我不在允许申请的组里?对应两条路:(a) 找 BloodHound 里我能 abuse 到某个"能 enroll 该模板"的组,链式提权到能申请;(b) 转另一个 ESC 类型,ESC1-ESC13 场景不同,有 CA 就大概率至少中一个。
**Q3**: 所有 ESC 都不成立怎么办?
**A3**: 别放弃 ADCS,转向"CA 服务器本身"。CA 通常也是 Windows 服务器,它的 CA 私钥文件在磁盘上,如果能拿到 CA 服务器本地管理员,就能导出 CA 私钥自己签任意证书(ESC 之外的终极玩法)。所以路径变成"打 CA 服务器"而不是"打模板配置"。
**Q4**: 完全没有 ADCS 怎么办?
**A4**: 直接从工具箱里划掉证书路线,不要继续想。转向 Kerberos 委派、ACL 滥用、GPO abuse 三大传统 AD 提权面之一。ADCS 是"有就香、没有就走"的可选路径,不必强求。

### 拿到本地管理员但不是域管,DCSync 还是横向
**Q1**: 我拿下一台机器的本地 SYSTEM,但当前登录票据里没有域管账号,该往哪走?
**A1**: 先分类。本地 SYSTEM 能做三件事:(a) dump 本机 lsass 拿本机上其他人的凭据;(b) dump 本机 SAM/LSA/DPAPI 拿本地口令和保存的域凭据;(c) 用本机机器账号做部分域内操作(比如去解密自己的 GPP、去 Kerberos 请求票据)。挑最快出结果的先做。
**Q2**: lsass 里只有一个低权域用户的 TGT,没有域管痕迹?
**A2**: 判断这机器有没有"高特权账号访问过"的证据——事件日志 4624 里 LogonType 2/10 的用户列表、C:\Users\ 下的用户目录、任务计划里的 RunAs 用户。如果曾经有域管登录过但现在下线,把机器变成"陷阱"等他回来(等重启后运维上号)。
**Q3**: 完全没有高权用户痕迹,该继续横向吗?
**A3**: 判据:BloodHound 上这台机器到 Domain Admins 的最短路径是几跳?一跳直连就横向;三跳以上性价比很低,应该转维度。
**Q4**: 换维度换到哪?
**A4**: 转向"这台机器的机器账号"能干什么。机器账号(MACHINENAME$)在 AD 里是有身份的,能查 LDAP、能被配置委派、能被 RBCD abuse。比如 Resource-Based Constrained Delegation 的经典链就是"我控制机器 A,给自己配 RBCD 到目标机器 B,然后以任意用户身份访问 B"。这条路和"横向登录"完全正交。

### SYSVOL 里发现 Group Policy Preferences 残留
**Q1**: 我在 SYSVOL 的 Policies 目录下发现 Groups.xml、Services.xml,里面有 cpassword 字段,这值不值得挖?
**A1**: 值得,但要迅速判断是不是残留的诱饵。cpassword 是 Microsoft 用固定 AES 密钥加密的,任何工具都能秒解。判据:解出来的口令看起来像"运维真的会用的口令"(有可读性、有公司特征、含真实业务/时段词)还是那种通用字典里排前十的样板口令?后者八成是蓝队故意留的。
**Q2**: 解出来是个域账号密码,该拿去干什么?
**A2**: 先测这账号还有效吗——`ldapsearch` 绑定一次,能读就还活着。然后立刻查 BloodHound,这账号是什么组、能登录哪些机器、有没有 ACL 特权。GPP 泄露的账号通常是老运维账号,权限往往超预期。
**Q3**: GPP 都是 cpassword,但完全解不出人看得懂的口令,或者账号已被禁用怎么办?
**A3**: 转向 SYSVOL 里的其他线索。SYSVOL 是所有域内认证用户都能读的,不止 GPP。找登录脚本(.bat/.ps1/.vbs),里面经常硬编码明文密码、UNC 路径、内网服务器名。找 MDT/SCCM 相关的配置文件,里面可能有 unattend.xml 一类的初始化密码。
**Q4**: SYSVOL 完全没有可利用信息怎么办?
**A4**: 转向"NETLOGON 共享 + 其他默认共享"。或者更彻底地转向 LDAP 里的"字段泄密"——用户对象的 description、info、comment 字段经常被运维用来记密码提示甚至明文,一次 `Get-ADUser -Filter * -Properties description` 常能挖到宝。

### EDR 挡 mimikatz 抓 lsass
**Q1**: 我已经是本地 SYSTEM,想抓 lsass,一跑 mimikatz 立刻被杀,该怎么办?
**A1**: 分离"抓"和"解析"两个动作。EDR 拦的是"读 lsass 内存的进程 + mimikatz 特征",把两步分开:先用尽量正常的手段(comsvcs.dll 的 MiniDump、任务管理器 dump、silent process exit 等)把 lsass 内存 dump 成文件,把文件传出去在本地用 pypykatz 解析。EDR 对"文件落地"往往比对"内存注入"宽容。
**Q2**: 连 dump lsass 内存都被拦(比如 PPL、RunAsPPL 开着)怎么办?
**A2**: PPL 挡的是"从用户态读取被保护进程内存"。绕法:(a) 加载签名驱动(比如 RTCore64、mimidrv)从内核关掉 PPL 标志;(b) 使用 shtinkering 类打法先让 lsass 进 silent dump 再取件;(c) 完全避开 lsass,改抓 SAM/SECURITY 注册表 hive(HKLM\SAM、HKLM\SECURITY),里面有本地账号哈希和 LSA secrets。
**Q3**: SAM 和 LSA secrets 也被现代 EDR 盯着怎么办?
**A3**: 转向 DPAPI 这条完全正交的通道。DPAPI 是 Windows 的凭据保险箱,浏览器保存的密码、RDP 保存的密码、Wi-Fi 密码全在里面。用当前用户的 masterkey 就能解自己的 vault,SYSTEM 能解所有用户的。EDR 对 DPAPI 相关的正常 API 调用几乎无感。
**Q4**: DPAPI 也空、lsass 也拿不到怎么办?
**A4**: 承认"这台机器榨干了",转向另一台机器或者另一维度。别一直磕。选择:(a) 横向到 EDR 覆盖薄弱的机器(测试机、老旧服务器,BloodHound 里 lastLogon 明显旧的);(b) 转向 coercion(PetitPotam/PrinterBug),让别的机器主动来认证我,把凭据从别人身上拉过来。

### Kerberos 委派枚举:哪种委派值得先看
**Q1**: BloodHound 给出无约束/约束/RBCD 三种委派对象,我该按什么优先级看?
**A1**: 从威力和可达性一起排:无约束(TrustedForDelegation) > RBCD(msDS-AllowedToActOnBehalfOfOtherIdentity 可写) > 约束(msDS-AllowedToDelegateTo)。无约束一旦有目标登录,我就能拿到他的 TGT;RBCD 只要我能写目标机器的 `msDS-AllowedToActOnBehalfOfOtherIdentity` 就能全套 S4U 打通;约束最受限,要 protocol transition 或者用户交互配合。
**Q2**: 找到一台机器有无约束委派但没人登它怎么办?
**A2**: 用 coercion 强制别人来登它——PetitPotam 让目标机器 A 强制向我控制的机器发起 SMB 认证,再配合无约束委派把 TGT 收下来。判据:目标机器上是否有 WebClient 服务(WebClient 允许 HTTP 认证被 relay 到 LDAP,组合更爽)、Print Spooler 服务是否在跑。
**Q3**: 想打 RBCD 但当前账号对目标机器对象没有 GenericWrite/GenericAll?
**A3**: 转向"我能不能先拿到某个对目标机器有写权限的账号"。BloodHound 反向查 outbound object control 的边,通常几跳就能到。或者用另一种玩法:如果我控制一台域内机器 X,X 的机器账号默认对自己的属性有写权限(某些配置下),就能自己给自己配 RBCD 做本地提权。
**Q4**: 完全没有可利用的委派配置怎么办?
**A4**: 转向 ACL 滥用维度。委派本质是 AD 里一堆特殊 ACL 的组合,没有委派不代表没有可写 ACL。用 BloodHound 查 GenericAll/GenericWrite/WriteDacl/AddMember 这些边,常常有普通用户对高权组或高权用户有意外的写权限,这些和委派完全正交。

### 一台服务器有域管登录痕迹
**Q1**: 我在某台服务器上看到事件 4624 里域管账号最近登录过,该立刻横向到这台机器还是继续枚举?
**A1**: 立刻转全部注意力到这台机器,别让它凉。判据:LogonType 是啥?2/10 意味着交互式/RDP,内存里几乎肯定有明文/TGT;3 是网络登录,可能只有哈希残留;5 是服务登录,机器账号或服务账号,不是本人。前两种优先级最高。
**Q2**: 我怎么进这台机器?
**A2**: 顺 BloodHound 找一条到这台机器的最短路径。如果直接有账号能登(WinRM/RDP/SMB admin),立刻登;不能登的话,先看这机器上跑什么服务(MSSQL/IIS/其他),可能通过服务侧提权到本机 SYSTEM 更快。
**Q3**: 进去了但 lsass 里没有域管的凭据,只有登录事件?
**A3**: 域管可能已经登出且清了内存。判据:LogonType 10 的 RDP 断开有两种,"注销"清得干净,"仅断开"内存还在。如果域管是"仅断开"式退出,他的 TGT 还在 lsass。如果都清了,布置一个陷阱等他下次登陆——修改这台机器的某个日常服务让它触发登录、或者把机器故意搞一点小故障让运维上号排查。
**Q4**: 域管永远不会再登这台机器了(比如是一次性排障)怎么办?
**A4**: 转向"用这台机器作为跳板"而不是"从这台机器拿域管"。这台机器一定处于内网某个特殊位置(能到 DC、能到关键业务系统),把它当成横向枢纽,而不是终点。回到 BloodHound 从这台机器出发看它能到哪。

### MSSQL 服务账号打 Kerberoast 还是 xp_cmdshell
**Q1**: 我在 BloodHound 里发现一堆 MSSQL SPN 账号,该走密码爆破还是走数据库入口?
**A1**: 双线并行,但优先级看当前手上有什么。手上有域账号就先请求 TGS 挂着爆(反正后台跑),同时前台去连 MSSQL——因为 MSSQL 默认允许所有域用户连接,登录不代表能查所有库,但能让我看到实例存在。
**Q2**: 连上 MSSQL 但没 sysadmin 权限,xp_cmdshell 用不了?
**A2**: 走 MSSQL 链接服务器(linked server)——一个实例常常配置了到其他实例的信任,链下去可能落在一个 sysadmin 上下文里。用 PowerUpSQL 的 `Get-SQLServerLinkCrawl` 一把梭。判据:一路链下去每一跳的 `SRV_LOGIN` 是谁,可能最后落在 sa 上。
**Q3**: 拿到 xp_cmdshell 了,该以什么身份执行?
**A3**: MSSQL 服务账号身份。这个身份可能是 LocalService/NetworkService/域账号。判据:`SELECT SYSTEM_USER, SUSER_NAME()` 加 `xp_cmdshell 'whoami /all'` 一看便知。如果是域账号,恭喜——直接 dump lsass 就有一个域账号的完整凭据。
**Q4**: MSSQL 服务是 NetworkService 且机器上没别的凭据怎么办?
**A4**: 转向"机器账号身份"这条正交路径。NetworkService 在网络上以机器账号身份出现,所以它能做机器账号能做的所有事——请求任意 SPN 的 TGS、被 RBCD 配置、认证到其他 SMB 等等。别把 NetworkService 当成"低权限的死胡同",它其实是机器账号的化身。

### 域信任发现:该跨域还是先固化本域
**Q1**: 我用 `nltest /domain_trusts` 或 BloodHound 发现好几个域信任,该跨过去还是先在本域拿到域管?
**A1**: 先固化本域。原因:跨信任攻击往往需要本域的高权凭据(比如 krbtgt 才能打 golden across trust、需要 SIDHistory 注入),没有本域域管就跨不了。但可以并行做"信任侦察",把外域的 DC、账号、名字空间摸清楚,为后续做准备。
**Q2**: 本域一直拿不下来,但外域看起来薄弱,该反向操作吗?
**A2**: 可以。判据:外域和本域的信任方向是什么。双向或者外域信任本域的话,本域账号能查外域信息,反之亦然。如果外域有弱点(比如没打补丁),从外域打起,拿到外域域管后借信任反捅本域。
**Q3**: 遇到 SID Filtering 开着,跨信任 golden ticket 不好使怎么办?
**A3**: 现代 Windows 森林信任默认开 SID Filtering,SIDHistory 那种老玩法废了。转向 TGT delegation across trust(如果 forest trust 开了 tgtDelegation)或者 PAM Trust(shadow admin)那种更新颖的路径。或者最简单:在外域也搞一套凭据,不依赖信任提权,而是两个域独立打。
**Q4**: 信任其实是访问隔离的诱饵怎么办?
**A4**: 转向"信任之外的连接方式"——两个域之间常常还有 VPN、专线、共享的中间业务系统(比如共用的 Exchange/文件服务器)。这些非 AD 的横向面往往比信任本身脆弱得多。别把域信任当成唯一的域间桥梁。

### LAPS 有没有部署,影响横向策略
**Q1**: 我怎么判断域里有没有 LAPS,以及它影响我什么?
**A1**: 查 LDAP 里的 `ms-Mcs-AdmPwd` 属性(经典 LAPS)或 `msLAPS-Password` 属性(Windows LAPS)。有对象就是有 LAPS。影响:本地管理员密码机器与机器不同,pth/plaintext 从一台机器传染到另一台的传统横向路径断了。
**Q2**: 有 LAPS 我该怎么办?
**A2**: 关键是"谁能读 ms-Mcs-AdmPwd"。这个属性有 ACL 保护,默认只有域管、Helpdesk 组、机器所属 OU 的 admin 能读。找 BloodHound 边 `ReadLAPSPassword`,拿到能读的账号后就能一台一台读明文本地管理员密码,反而比传统 pth 更精准。
**Q3**: 谁都不能读、我也没权限提到能读的账号怎么办?
**A3**: LAPS 保护的是"本地管理员密码",没保护"域账号在本地登陆过留下的凭据"。转向:横向不靠 pth,靠"进机器后抓机器上的域账号缓存"。事件日志 4624 告诉我谁上过这台机器,MSCache 里存着他们的域账号 hash。
**Q4**: 机器 EDR 太严抓不到 MSCache 怎么办?
**A4**: 转向完全非 pth 的横向——用 Kerberos。有一个域账号密码或哈希后,请求任意机器的 CIFS TGS,直接以 Kerberos 身份连过去,不走 NTLM,不需要本地管理员密码。LAPS 只挡 NTLM 传染,挡不住 Kerberos。

### 无 SMB 445 但有 WinRM 5985,横向工具怎么切
**Q1**: 目标机器 445 关了但 5985 开着,psexec/smbexec 都用不了怎么办?
**A1**: 直接切 Evil-WinRM 或者原生 `Enter-PSSession`。判据:我手上有账号能通过 WinRM 认证吗?WinRM 默认只允许 Remote Management Users 组和管理员组的账号连接,普通域用户默认连不上,别浪费时间在没权限的账号上。
**Q2**: 账号能连但 EDR 拦 WinRM 侧的命令执行?
**A2**: WinRM 认证阶段不会触发 EDR,触发的是执行阶段。改用 WinRM 传输"数据"而不是"命令":比如通过 WinRM 复制文件、通过 WinRM 修改注册表触发别的路径。或者用 AMSI bypass 之类的手段绕过 PowerShell 层的检测。
**Q3**: WinRM 也不开怎么办?
**A3**: 依次试:RDP(3389,慢但绕大多数命令行检测)、WMI(135+动态高端口,以 wmiexec 方式)、DCOM(MMC20.Application、ShellWindows,冷门但可用)、SCM 远程创建服务(需要 445 端口不完全禁,或者 RPC over HTTP)。
**Q4**: 所有远程执行通道都被防火墙挡了怎么办?
**A4**: 转向"让目标机器主动出来找我"。往它能读到的共享里放一个恶意 LNK/Word/HTA/URL 文件,等运维/用户点开;或者滥用 GPO——如果我有 GPO 写权限,可以给目标机器下发一个"启动时执行"任务,等它重启。反向连接完全绕开入站防火墙。

### NTLM relay 前的可行性判断
**Q1**: 我打算做 NTLM relay,先该判断什么?
**A1**: 三个开关:(1) 目标服务是否强制 signing。SMB signing 强制则 relay 到 SMB 报废;LDAP signing 强制则 relay 到 LDAP 报废;LDAPS 有 channel binding(EPA)则 relay 到 LDAPS 也报废。(2) 我控制的机器能不能被目标机器访问到(反向 SMB/HTTP 认证)。(3) 我有没有 coercion 手段能触发认证。
**Q2**: 用什么命令快速摸 signing 状态?
**A2**: `crackmapexec smb <target> --gen-relay-list` 直接筛出所有 signing not required 的机器,是最快的地图。这份地图和 BloodHound 结合,能一眼看出"我 relay 到哪台机器最有价值"。
**Q3**: signing 都开着,relay 到 SMB/LDAP 都不成怎么办?
**A3**: 转向 relay 到 ADCS Web Enrollment(ESC8)。ADCS 的 /certsrv 走 HTTP,默认不做 channel binding,relay 一个机器账号认证过去申请证书就能拿到该机器的完整代理凭据。这是当前最鲜活的 relay 路径。
**Q4**: ADCS 也没有或者也加固了怎么办?
**A4**: 转向"拿哈希爆破"这条完全不同的路。抓到 NetNTLMv2 之后不 relay,直接离线爆破。如果口令不强,几分钟就出。或者转向 SMB relay 中的 Kerberos relay 新玩法(比如打 LOCAL 机器的 CIFS SPN),这些是绕过 signing 的新研究方向。

### 抓到 NetNTLMv2 hash 后:破解 vs relay
**Q1**: responder/mitm6 抓到一堆 NetNTLMv2,该扔 hashcat 爆还是当场 relay?
**A1**: 有一个原则:能 relay 就 relay,不能 relay 才爆。原因:relay 直接得到目标系统上的执行,不需要爆破口令;爆破成功也只得到明文,还要额外一步去用。判据:目标环境的 signing 状态(见前一条)。
**Q2**: 决定爆破,先爆哪个 hash?
**A2**: 挑账号价值高的先爆(域管、高权组成员),挑机器账号 hash 不爆(NetNTLMv2 的机器账号 hash 是随机长口令,爆不开)。判据:hash 里 username 字段能看出账号名。
**Q3**: 爆了一晚上都没出,或者环境全部禁 NTLM 只用 Kerberos怎么办?
**A3**: 转向对 Kerberos 的类似攻击——AS-REP roasting、Kerberoasting、Kerberos relay。它们和 NTLM relay 是完全平行的路径,思路一样但底层协议不同。抓不到 NTLM 就抓 Kerberos。
**Q4**: 环境完全没有可欺骗的名字解析(mitm6/LLMNR/NBNS 全关)怎么办?
**A4**: 转向"主动触发"——用 coercion(PetitPotam/PrinterBug/DFSCoerce)让目标机器主动认证到我这里,不再依赖被动嗅探。coercion 是主动式的抓 hash,不需要蓝队犯错。

### GPO 修改权限:直接 abuse 还是留后手
**Q1**: BloodHound 显示我控制的账号对某个 GPO 有 GenericWrite/WriteDacl,该立刻 abuse 吗?
**A1**: 判断 GPO 的"作用域"——它链接到哪个 OU、影响多少机器/用户。链接到 Domain Controllers OU 的 GPO 修改一下就能在所有 DC 上执行代码,这是最猛的 abuse。链接到某个业务 OU 的 GPO 影响面小,可以留作后手。
**Q2**: 我具体怎么 abuse GPO?
**A2**: 三种典型玩法:(a) 加一个 Immediate Scheduled Task 下发到目标机器立刻执行;(b) 修改 Restricted Groups 把自己加入本地管理员组;(c) 部署一个 Startup Script。用 SharpGPOAbuse 或者 pyGPOAbuse 一把梭。判据:目标机器多久刷新一次 GPO(默认 90 分钟+随机 30 分钟)。
**Q3**: 修改 GPO 后蓝队立刻回滚怎么办?
**A3**: 说明有 GPO 变更监控。转向更隐蔽的路径:不改现有 GPO,而是创建一个新的、看起来无害的 GPO,链接到一个不显眼的 OU;或者只在窗口期临时改 GPO 触发一次执行然后立刻改回来,让审计日志看起来像误操作。
**Q4**: GPO abuse 完全被检测系统盯着怎么办?
**A4**: 转向 GPO 之外的域控制机制。逻辑上等价于 GPO 的还有:登录脚本(SYSVOL 里的 .bat/.ps1)、SCCM 部署、WSUS 恶意补丁下发。这些"下发机制"和 GPO 正交,监控盲区往往就在这里。

### 域管在线登录一台失陷机
**Q1**: 我拿下一台机器,发现有域管账号当前正在线,该 token impersonate 还是等他退?
**A1**: 立刻 impersonate,别等。判据:当前用户会话什么时候消失完全不可控,可能下一秒就注销。用 `incognito` 或 SharpImpersonation 在他还在的时候借用他的 token 干活,或者直接从 lsass 里抠他的 TGT/明文当场做 DCSync。
**Q2**: 我借他 token 该干什么最有效率?
**A2**: 一次动作解决问题:立刻 DCSync 拉 krbtgt 哈希。有 krbtgt 就能签任意黄金票据,以后不需要再依赖这个域管账号,即使他改密码也不影响。别贪心做多余的事,一次动作、快进快出。
**Q3**: 想 DCSync 但当前网络到 DC 的 RPC 通道不通怎么办?
**A3**: 转向"就地兑现"——如果 DCSync 走不通,退一步用 impersonate 后的身份做任意 SMB/WMI 操作,当场加自己进 Domain Admins 组、或者给自己配一个新的 GPO 后门、或者把某个我控制的账号加入 Enterprise Admins。别指望网络问题短时间修复。
**Q4**: 域管当场就注销了怎么办?
**A4**: 转向"引诱回来"的机会——把这台机器伪装成有故障,让他/别的运维再登。或者转向刚才 impersonate 时抓到但没用完的其他信息(缓存的 NTLM、DPAPI masterkey、浏览器 cookie),这些都是他登录时留下的残余财富。

### BloodHound 数据量爆炸怎么筛
**Q1**: BloodHound 采到一万个对象,shortest path 图看不清怎么办?
**A1**: 别看图,用 Cypher 查询。核心问题就三类:(a) `MATCH p=shortestPath((n:User {owned:true})-[*1..]->(m:Group {name:"DOMAIN ADMINS@X"})) RETURN p` 找我到域管的路径;(b) `MATCH (n)-[r:GenericAll|GenericWrite|WriteDacl]->(m) WHERE m.highvalue RETURN n,r,m` 找对高价值目标的可写 ACL;(c) 找委派、找可读 LAPS 的账号。这三类查询覆盖 90% 决策。
**Q2**: 全部账号都没有 owned 标记(我刚开始),shortest path 用不了怎么办?
**A2**: 标记起点。把我当前拥有的账号手动标 owned(BloodHound 里右键即可),再查最短路径。或者反向查:从 Domain Admins 出发,找"最容易被外部账号 abuse 的一跳",逆向定位我该先拿谁。
**Q3**: 路径都涉及"需要交互式登录到某台机器",但目标机器我进不去怎么办?
**A3**: 转向 BloodHound 里 HasSession 边——查看这个目标账号还在哪些其他机器上有 session。同一个账号常常同时登录多台,总有一台我能进的。
**Q4**: BloodHound 采集本身就出错(超时、权限不足、断连)怎么办?
**A4**: 分批采,或者转向轻量替代品。ldapdomaindump、adalanche、SharpHound 的不同 collection method 各有轻重(DCOnly 只查 DC,不惊动其他机器)。或者退回到手动 LDAP 查询,只查我需要的几个对象。别让"工具跑不动"变成"没有情报"。

### 没有 BloodHound 权限,手动怎么摸
**Q1**: BloodHound 采集器全被拦、我账号也不能匿名 LDAP 查大量对象怎么办?
**A1**: 用最小暴露的手动查询。核心几条 LDAP filter:`(&(objectCategory=user)(adminCount=1))` 找特权用户(adminCount 是 AdminSDHolder 的痕迹)、`(&(objectCategory=computer)(userAccountControl:1.2.840.113556.1.4.803:=524288))` 找无约束委派机器、`(&(objectCategory=user)(servicePrincipalName=*))` 找 SPN 账号。每次只查一类,不整批拉。
**Q2**: 连这些查询也超权限怎么办?
**A2**: 转向 DNS 侧面枚举。域内 DNS 常常匿名可查,`nslookup -type=srv _ldap._tcp.dc._msdcs.<domain>` 直接给出所有 DC。用 DNS 反查子网里的机器名往往比 LDAP 更宽松。
**Q3**: DNS 也锁了怎么办?
**A3**: 转向 SMB 层的枚举——`net view /domain`、`net group /domain "Domain Admins"` 这些 net 命令走的是 SAMR/LSARPC,权限模型和 LDAP 不同,有时 LDAP 不给的 net 给。加上 `net user <name> /domain` 一个一个查用户信息。
**Q4**: 所有官方枚举通道都锁了怎么办?
**A4**: 转向"从共享和文件里读情报"。SYSVOL 的策略文件、共享上残留的 Excel/txt 描述内网结构、SCCM/WSUS 的日志文件。当协议查询走不动,静态文件里往往写着一切。

### 抓到明文但 pth 不好使:AES Only 环境
**Q1**: 我抓到域账号的明文口令,想 pth 结果 NTLM 全被域策略禁了怎么办?
**A1**: 判据先明确:是"KDC 不接受 RC4"还是"整个域禁 NTLM"?前者只影响 Kerberos ticket 加密,pth over NTLM 还能用;后者(NtlmMinClientSec/NtlmMinServerSec 高、NTLM auditing = block)彻底禁 NTLM,需要走 Kerberos。
**Q2**: 那怎么把明文变成可用的 Kerberos 票据?
**A2**: 用明文直接 `getTGT.py`(impacket)请求 TGT。impacket 支持指定加密算法,如果 KDC 只接 AES,请求时用 AES key(可以从明文经过 PBKDF2 派生,或者直接抓 AES 哈希做 pass-the-key)。
**Q3**: 手上只有 NTLM hash 没有明文,AES Only 环境下怎么办?
**A3**: 转向 pass-the-key 而不是 pth。pass-the-key 用 AES256/AES128 key 请求 TGT,和 pth 语义类似但底层协议是 Kerberos。缺 AES key 就想办法拿(在能抓到明文/AES key 的机器上先抓)。或者最直接的转向:如果爆得开,离线爆破明文再走 A2 路径。
**Q4**: 完全没有可用 Kerberos 通道怎么办?
**A4**: 转向不需要横向凭据的路径——ACL abuse、GPO abuse、委派 abuse 这些操作只需要一次认证(单纯查 LDAP + 写属性)就完成,不依赖 pth/pass-the-key。或者转向 ADCS 的证书身份,证书自成一套 PKINIT 认证,和 NTLM/传统 pth 无关。

### 外部森林信任的跨森林打法
**Q1**: 我拿到本域域管,想跨过 forest trust 打到父森林/兄弟森林,该怎么做?
**A1**: 判据先看信任类型和方向。Trust type = Forest 且方向双向就有跨森林路径;single-domain trust 玩法不同。经典打法:如果 forest trust 没开 SID Filtering(旧环境),用 SIDHistory 注入把子域用户假装成父森林的 Enterprise Admin;开了 SID Filtering(现代默认)就废这条。
**Q2**: SID Filtering 开着怎么办?
**A2**: 转向 TGT delegation across trust——某些配置下 forest trust 允许 TGT 转发,配合无约束委派能拿到跨森林用户的 TGT。或者更实际的转向:寻找两个森林共用的账号(比如运维同一批人在两个森林都有账号,口令喷洒常常一喷一个准)。
**Q3**: 完全没有跨森林技术路径怎么办?
**A3**: 转向"业务侧共享"——两个森林之间常常有共享的 Exchange、SharePoint、文件服务器、Jenkins 之类的应用系统。这些应用系统里存着两边的凭据/配置,拿下它就跨了森林,不依赖 AD 信任本身。
**Q4**: 目标森林根本无需跨,是我判断错了怎么办?
**A4**: 冷静评估目标定义。红队目标是"拿域管"还是"拿具体业务数据"?如果是后者,业务数据可能就在本森林,跨森林是在浪费时间。回到目标定义,判断"要不要跨"本身就是一个决策点。

### 拿到域管但 krbtgt 还没到手
**Q1**: 我成为域管了,该立刻打 krbtgt 做黄金票据吗?
**A1**: 该。原因:域管账号会被改密码/被禁用,krbtgt 哈希是持久化的终极后手。做 DCSync 拿 krbtgt 只需要一次操作、几秒钟,风险极低。原则:拿到域管后第一件事就是 krbtgt DCSync + 保存哈希备用。
**Q2**: 除了 krbtgt 还该抓什么账号哈希?
**A2**: 抓这几类:(a) 全域用户 hash(离线可爆);(b) 关键服务账号 hash;(c) DC 本机机器账号(用于机器身份持久);(d) trust account hash(用于跨信任持久)。一次 DCSync 全套拿下,不留漏。
**Q3**: 拿到 krbtgt 后立刻打黄金票据用不用?
**A3**: 别立刻打。黄金票据在有现代日志分析的环境里是能被抓的(4769 里 TGS 的 TicketLifetime 超过策略上限就异常)。原则:krbtgt 是"应急后手",日常横向用别的账号,黄金票据只在别的账号全部失效时才用。
**Q4**: 打了 krbtgt 之后蓝队怎么响应我怎么办?
**A4**: 蓝队的响应是"改两次 krbtgt 密码"(改两次因为它保留旧密码用于历史票据兼容)。我的对应:同样保留后手在其他维度——委派后门、GPO 后门、Skeleton Key、SIDHistory 注入、ADCS 证书。持久化不能只靠一个鸡蛋一个篮子。

### 打不动 DC 但读写共享很随意
**Q1**: DC 打不动、Kerberos 层加固严密,但发现内网大量共享是 Everyone 可写,该怎么用?
**A1**: 这是低成本高杠杆。判据:哪些共享是被用户/服务定期访问的?这决定我放的恶意文件能不能被"消费"。看共享上文件的最近修改时间和访问频率,选活跃的下手。
**Q2**: 具体放什么恶意文件?
**A2**: 挑投递面:(a) LNK 文件指向 UNC 路径,能触发 SMB 认证,配合 relay 收哈希;(b) 恶意 Office/PDF,需要用户点开;(c) SCF 文件历史上能触发认证(新系统已修但老域偶尔中);(d) 替换共享上本来就有的合法脚本(比如某个部门的日常 .bat),下次运行就是我的。
**Q3**: Everyone 可写但没人点没人跑怎么办?
**A3**: 转向"能触发认证"的诱饵而不是"能执行代码"的诱饵。UNC 路径类文件(LNK/URL/desktop.ini/library-ms)只要文件被列出就触发认证,不需要点击。放在活跃共享的根目录,进目录就中招。
**Q4**: 共享上没有敏感操作发生、蓝队也不点怎么办?
**A4**: 转向"主动触发"——找一个我能控制的用户账号(哪怕低权),在他账户下模拟一次访问,让他自己变成受害者。或者转向共享内容本身——就算没人点,共享上的文件里可能有配置文件、日志、备份文件,直接读它们就有情报,不需要执行。

### 域控在隔离段,直连不通
**Q1**: 我扫内网发现 DC 在一个隔离子网,我这段过不去,该怎么办?
**A1**: 判据先明:是网络层隔离(路由不通/防火墙拦)还是协议层隔离(端口不通)?对应两条路。网络层:找双网卡跳板机(比如运维终端可能同时通两段);协议层:看 DC 段哪些端口对我这段开放(RPC/LDAP/HTTPS 之类)。
**Q2**: 找到一台双网卡机器但没权限进它怎么办?
**A2**: 转向让这台机器成为下一个横向目标——它是"必须拿下"的关键节点。哪怕它上面没有域管痕迹,它的网络位置本身就值得所有努力。BloodHound + 双网卡目标 = 明确的攻坚方向。
**Q3**: 完全没有双网卡机器怎么办?
**A3**: 转向应用层跨段。DC 段可能有 web 应用、Exchange、SharePoint 之类是允许从我这段访问的,借这些应用做打洞。或者转向利用被认证协议本身实现跨段——比如 DC 段的服务向我这段做 LDAP/HTTP 查询,借它出来的通道打回去。
**Q4**: 隔离彻底、无任何跨段路径怎么办?
**A4**: 承认"直接打不到 DC 段",转向"打 DC 段管理员"。管理员运维终端才是弱点,他们从办公段用 RDP/SSH 跳到 DC 段。拿下运维终端 = 拿到进 DC 段的令牌/RDP 连接/RDP 保存的凭据。跳板永远比直接穿墙容易。

### 有服务账号密码但登不了机器
**Q1**: 我爆开一个服务账号密码,但用它 RDP/WinRM 全部拒绝,该干什么?
**A1**: 服务账号常常被限制"不能交互登录"(Deny logon locally / Deny logon through RDP),但这不代表账号没用。判据:这个账号在 LDAP 里有什么权限、有什么 SPN、能连什么服务。
**Q2**: 具体能做什么?
**A2**: 至少四件事:(a) 用它跑 LDAP 查询,可能它比我原来的账号权限大;(b) 请求任意 SPN 的 TGS(Kerberoast 收集);(c) 如果它有委派配置,借它的委派身份获得别的账号 TGT;(d) 如果它对某些对象有 ACL,直接 abuse ACL 而不登录任何机器。
**Q3**: 都不行,这账号纯粹是低权服务账号怎么办?
**A3**: 转向"这账号跑在哪台机器上"。服务账号背后一定有个进程,那个进程在的机器上,内存里有它的凭据。反向找到那台机器,那台机器上的其他凭据(其他服务、其他登录用户)才是宝藏。账号本身是路标,机器才是矿。
**Q4**: 都找不到对应机器怎么办?
**A4**: 转向密码复用检测。服务账号的口令常常和别的账号复用(运维图省事),用这个明文去喷所有其他账号(包括个人账号、其他服务账号)。密码复用是弱势环境里最好的杠杆。

### Print Spooler / WebClient coercion 选哪个
**Q1**: 我想 coerce 目标机器认证,PetitPotam、PrinterBug、DFSCoerce 哪个先试?
**A1**: 判据是"目标机器什么服务在跑"。Print Spooler 服务开着就 PrinterBug;EFSRPC 端点开着就 PetitPotam;有 DFS 就 DFSCoerce。查询用 rpcdump/impacket 一把梭列端点。
**Q2**: 都能触发,该 coerce 到哪台机器?
**A2**: 不是"coerce 谁认证",而是"让它认证到我控制的哪个 relay 点"。判据是我 relay 的下家能干什么(见 relay 决策链)。让目标 DC 认证到我 relay 到 ADCS Web,ESC8 直接拿 DC 证书,一步到位。
**Q3**: 目标机器把这些 coercion 端点都关了怎么办?
**A3**: 转向 WebClient 服务的 HTTP coercion。WebClient 允许 WebDAV,如果目标开了(常见于安装 Office 的机器),就能强制 HTTP 认证,relay 到 LDAP 甚至比 SMB relay 更好用(HTTP 没有 signing 概念)。
**Q4**: 所有 coercion 都被堵怎么办?
**A4**: 转向被动式 poisoning——放置陷阱文件(LNK/URL 触发认证)在共享上,或者用 mitm6 做 IPv6 DNS 欺骗抢答 WPAD 请求。这些不需要 coercion 触发,是内网 IPv6 default enabled 的天然缺陷。

### 撤退时清什么、留什么
**Q1**: 目标已达成,该清理什么、留什么?
**A1**: 分两层:攻击痕迹要清、持久化要留。攻击痕迹指临时上传的工具、修改的配置(未预期的);持久化指埋好的后手(至少三条:krbtgt 手上、一个域管账号被我隐藏加进特权组、一个 GPO/Startup Script 后门、一个 ADCS 证书身份)。判据:蓝队复盘我能不能悄悄回来。
**Q2**: 具体清什么?
**A2**: 清工具文件、清事件日志里明显异常条目(4624/4672/4688 里我的进程)、清 Kerberos 票据缓存(klist purge 避免留 TGT)、清临时创建的账号(除非是持久化用的)。别过度清——大量事件日志被清本身就是异常,反而暴露。
**Q3**: 蓝队上线开始应急响应了怎么办?
**A3**: 立刻从"扩大战果"切到"保住持久化 + 撤离"。判据:蓝队响应速度决定我能干多久。停止所有噪音大的动作(端口扫描、爆破、大规模登录),只保留一条最隐蔽的通道(比如 ADCS 证书身份,不走 NTLM 不走 pth,日志几乎不留痕),观察蓝队怎么清理。
**Q4**: 蓝队已经开始改 krbtgt、禁账号、封 IP 怎么办?
**A4**: 承认"这轮结束了"是最重要的决策之一。转向复盘:哪些持久化被撤了、哪些还活着、下一次攻击面能不能重新用同一条路径。别恋战。红队真正的胜利不在于"一直待着",而在于"每次目标达成、复盘成本最小、下次能重来"。

### 域内 DNS 反推关键资产
**Q1**: 我进域后除了 LDAP 还有哪个通道能低噪音地摸出高价值机器?
**A1**: DNS。DC 一般兼作 DNS,域用户默认能查询 SRV 记录和大量 A 记录。判据:`_ldap._tcp.dc._msdcs.<domain>` 直接列所有 DC;`_kerberos._tcp.<domain>` 给 KDC;`_gc._tcp.<domain>` 给全局编录;还有各种 sql/exchange/autodiscover 的 SRV 记录直接告诉我关键业务在哪。
**Q2**: 我怎么找不在 SRV 里的关键机器(比如 SCCM、Jenkins、跳板机)?
**A2**: 转向 DNS 域名词典枚举——用常见运维命名(mgmt/jump/bastion/sccm/wsus/jenkins/backup/ca01/dc01)组合公司前缀去查 A 记录。DNS 查询本身几乎不留可疑日志(和正常应用查询无区别),是最静的枚举方式。
**Q3**: 域内启用了 DNS scavenging 或者限制普通用户查询怎么办?
**A3**: 转向反向解析。zone transfer 常被禁但反向 zone 常常开着,`dnsrecon -r <subnet>` 反查整段能拿到所有活跃机器的名字,而且反向查询看起来更像日常流量。
**Q4**: DNS 全线锁死怎么办?
**A4**: 转向 mDNS/LLMNR/NBNS 的被动嗅探。这些协议在内网默认开着,响应流量会自然透露机器名和用户会话信息。而且这些协议同时是 poisoning 攻击面——被动嗅探不成还能主动 poison,一举两得。

### DHCP/mitm6 IPv6 攻击面判断
**Q1**: 我该不该在内网启动 mitm6?这个动作噪音有多大?
**A1**: 判据:目标网络是否启用 IPv6?现代 Windows 默认优先 IPv6,内网 90% 的域环境即使"没配 IPv6"也响应 IPv6 请求。风险:mitm6 是主动欺骗,一旦被 IDS 抓到 DHCPv6 大量响应会立刻告警。窗口期短,收益要匹配。
**Q2**: 什么时候值得开?
**A2**: 两个前置条件:(1) 我有配合的 relay 目标(比如 LDAP 或 ADCS ESC8);(2) 我处在一个能收到域内机器 IPv6 请求的网段。mitm6 单独跑没用,必须和 ntlmrelayx 配合把抓到的机器认证 relay 到 AD/ADCS。
**Q3**: 蓝队关了客户端 IPv6 或者部署了 IPv6 RA Guard 怎么办?
**A3**: 转向 IPv4 侧的等价路径——LLMNR/NBNS poisoning(responder)。原理相似,都是抢答名字解析请求收 NTLM 认证。IPv4 poisoning 的抓取率通常比 IPv6 低一些,但更普适。
**Q4**: 所有名字解析层攻击都被检测/关闭怎么办?
**A4**: 转向"不需要 poisoning 的认证获取"——coercion(见前面 PetitPotam 那条)、共享陷阱、错配的 SPN 服务。名字解析欺骗是 opportunistic 的,coercion 是 deterministic 的,后者更可靠。

### 判断"是否真的需要拿域管"
**Q1**: 我打了很久还没到域管,该继续磕还是重新评估目标?
**A1**: 停下来问:红队的实际目标是什么?域管只是"手段",不是"目的"。如果目标是拿到某个财务系统数据、某个源代码库、某个客户数据库,那么这些系统的本地管理员/应用管理员往往比域管更直接。
**Q2**: 怎么判断"局部权限"够不够用?
**A2**: 列出目标数据/系统的访问路径:(a) 需要什么身份能读?(b) 那个身份有多少人拥有?(c) 我离那些人多远?BloodHound 可以按目标对象反向查最短路径,不必设终点为 Domain Admins。
**Q3**: 目标就是"证明拿到域管"(比如比赛/演习)怎么办?
**A3**: 那必须走域管路径,但仍然要评估"哪条最短、噪音最小"。BloodHound 里 shortest path 常常给出令人意外的短链——三跳内可达比死磕 DC 补丁面要现实得多。
**Q4**: 目标定义本身就模糊(客户没说清)怎么办?
**A4**: 转向可衡量的里程碑:第一天目标是"任意一个高特权账号或一台高价值机器",不追求域管。有阶段性成果再和客户对齐,把模糊目标转成明确目标。别在目标不明的情况下把所有筹码押在"必须域管"上。

### 卡住太久:判断是否要重开始
**Q1**: 我已经磕在某一条路径上超过合理时间(比如几个小时/一天),该怎么决策?
**A1**: 设一个硬性时间阈值,超了就强制换路径。判据不是"我快成功了",而是"过去 N 小时我有没有实质性推进"——有没有多拿一个凭据、多进一台机器、多枚举出一批对象。全是"零推进"就是必须换路的信号。
**Q2**: 换到哪条路?怎么保证不是随便乱换?
**A2**: 列一张"未探索维度"清单,每个大类打勾:凭据薅取(内存/DPAPI/浏览器/共享文件)、Kerberos 攻击面(AS-REP/Kerberoast/委派)、ADCS、ACL abuse、GPO abuse、coercion+relay、信任、密码喷洒、社工/物理层。挑一个我今天完全没试过的类别切进去。
**Q3**: 所有大类我都碰过了但都没进展怎么办?
**A3**: 转向"重新做侦察"。可能问题不在打法,而在情报——BloodHound 数据是老的?网络又扩了新段?有新上线的机器我没扫到?重新采集一轮情报常常发现之前忽略的目标。
**Q4**: 侦察也重做了还是没突破怎么办?
**A4**: 这是"该给自己一个 hard stop"的时刻。红队最重要的能力之一是"承认此路不通"。要么向队友求助换视角,要么记录状态、留后手撤离、下次带新工具/新研究再来。硬磕耗掉的时间是最贵的成本。
