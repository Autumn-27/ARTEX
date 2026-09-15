# AD内网 · ADCS ESC 证书攻击选路

### 拿到域内立足点后先判断域里到底有没有 ADCS
**Q1**: 我刚在域内普通用户上下文拿到 shell,想走证书路提权。第一步该确认什么?
**A1**: 别急着跑利用。先确认域里是否部署了企业 CA。判据:LDAP 里 `CN=Enrollment Services,CN=Public Key Services,CN=Services,CN=Configuration,<domain>` 下是否有对象;有则存在企业 CA,记下每个 CA 的 `dNSHostName`(CA 主机)和已发布模板。没有这个容器就意味着没有企业级 ADCS,证书提权基本免谈,直接转别的维度。
**Q2**: LDAP 匿名或受限查不全怎么办?
**A2**: 用当前域凭据跑 Certipy `find`。`find -vulnerable -stdout` 会一次性把 CA、模板、ACL、EKU、flag 拉出来并标注 ESC1-16。没有 Python 环境就在 Windows 上 `certutil -config - -ping` 和 `certutil -template` 摸清模板。
**Q3**: 只想确认 CA 是否在线可达?
**A3**: `certutil -config "CA主机\CA名" -ping`,或直接测 CA 主机的 RPC(135/445)与证书 Web 端点(certsrv、certfnsh.asp)。CA 离线则走签发的路全断,只能考虑 ESC7 管理权限或 ESC8 relay 前置。
**Q4**: 怎么把 Certipy 输出转成"下一步打哪个 ESC"?
**A4**: 看 vulnerable 段的 ESC 编号,按"前置条件已满足、利用复杂度低"排序:ESC1/ESC4 通常最直给,ESC8 需能触发机器认证,ESC6 看 CA 的 EDITF flag。
**Q5**: 这条路被墙了——域里根本没 ADCS 或 CA 全离线怎么办?
**A5**: 转正交维度:回经典 AD 提权面——Kerberoast/AS-REP roast、ACL 滥用、Zerologon/PetitPotam+NTLM relay、GPO 滥用、委派(约束/非约束/RBCD)。证书只是众多路之一,没 CA 别死磕。
**Q6**: 担心扫描动静大被 EDR 抓?
**A6**: Certipy 的 LDAP 查询本身很安静(就是正常目录读),真正有声的是后续 enroll。可先只做 `find` 侦察、离线分析,确定唯一利用点再一次性出手,减少无谓的证书申请日志。

### 判断某个模板是不是 ESC1(可控 SAN + 客户端认证 EKU)
**Q1**: Certipy 标了个模板疑似 ESC1,我怎么确认它真能打?
**A1**: 三个条件同时成立才是 ESC1:模板 EKU 含 Client Authentication(或 Smart Card Logon / PKINIT / Any Purpose)、模板设了 `CT_FLAG_ENROLLEE_SUPPLIES_SUBJECT`(申请者可自填 SAN)、且当前身份对模板有 Enroll 权限、CA 对该请求不要求 Manager Approval 也不要求授权签名。
**Q2**: 确认了,怎么打?
**A2**: `certipy req -u me@dom -p pass -ca CA名 -template 模板 -upn administrator@dom`(或 `-sid` 指定目标 SID)。拿到 pfx 后 `certipy auth -pfx admin.pfx` 走 PKINIT 换 TGT / 或直接拿 NT hash。
**Q3**: 申请时报 SAN 被拒或证书里没带上我填的 UPN?
**A3**: 说明该模板其实没开 enrollee-supplies-subject,或 CA 打了强制 SAN 映射补丁。改用 `-sid` 走 SID 映射,或检查是不是该走 ESC6(CA 全局 EDITF_ATTRIBUTESUBJECTALTNAME2)而非模板级。
**Q4**: 目标账户我填 domain admin,但 auth 回来的票据没权限?
**A4**: 检查你填的 UPN/SID 是否真存在且是特权账户;PKINIT 后若域强制了证书强映射(2022 补丁后),弱映射会失败,需 `-sid` 带上目标对象的 objectSid 做强映射。
**Q5**: 这模板打不动(要审批/要授权签名)怎么办?
**A5**: 转向:找同 CA 下别的可 enroll 模板;或看该模板是否同时命中 ESC2/ESC3;或转 ESC4(如果你对模板有写权限,直接改配置把它变成 ESC1)。
**Q6**: 全域没有一个 enrollee-supplies-subject 模板怎么办?
**A6**: 放弃 SAN 注入这一支,转 ESC3(申请代理/Enrollment Agent)、ESC6(CA 级 SAN)、ESC7(CA 管理)、ESC8(relay),这些不依赖模板自填 SAN。

### 区分 ESC2 与 ESC3 该走哪条
**Q1**: 我看到一个模板 EKU 是 Any Purpose 或为空,和另一个是 Certificate Request Agent,分别怎么用?
**A1**: EKU 为 Any Purpose(2.5.29.37.0)或干脆没有 EKU = ESC2,证书可用于任意用途,包括客户端认证,和 ESC1 类似地能直接认证(前提能控 SAN 或做代理)。Certificate Request Agent EKU = ESC3,它本身不能直接登录,但能作为"注册代理"替别人申请证书。
**Q2**: ESC3 具体怎么两步走?
**A2**: 第一步用有 Enrollment Agent EKU 的模板给自己申一张代理证书;第二步 `certipy req ... -template 某客户端认证模板 -on-behalf-of 'DOM\administrator' -pfx 代理.pfx`,替管理员申一张能登录的证书。
**Q3**: on-behalf-of 被拒?
**A3**: 目标模板可能配了 Application Policy 限制,只接受特定注册代理,或 CA 限制了 Enrollment Agent 可代理的账户/模板范围。换目标模板,或查 CA 的 enrollment agent restrictions。
**Q4**: ESC2 的模板拿到证书但登录失败?
**A4**: Any Purpose 能认证,但若 SAN 不可控就只代表你自己。要么它同时可控 SAN(退化成 ESC1),要么把 ESC2 证书当代理证书用(Any Purpose 也涵盖 Certificate Request Agent 用途)去做 on-behalf-of。
**Q5**: 两条都被审批/权限卡死怎么办?
**A5**: 转向:优先看 ESC1(更直接),再看 ESC4(改模板 ACL),再看 ESC7/ESC8。ESC2/ESC3 更适合作为已具备某模板 enroll 权限时的补充路径,而非首选。

### 模板本身可写时走 ESC4
**Q1**: Certipy 说我对某模板有 WriteDacl/WriteProperty/GenericAll,这怎么变现?
**A1**: 这是 ESC4——你能改模板配置。思路:把一个安全模板临时改成 ESC1(加上 enrollee-supplies-subject flag、设客户端认证 EKU、去掉审批和授权签名要求、给自己 enroll 权限),申一张管理员证书,然后改回去清痕迹。
**Q2**: 用什么工具改?
**A2**: `certipy template -template 模板 -write-default-configuration`(Certipy 新版有一键把模板改成易受攻击态并可保存原配置以便还原)。或手动经 LDAP 改 `msPKI-Certificate-Name-Flag`、`pKIExtendedKeyUsage`、`msPKI-Enrollment-Flag`。
**Q3**: 改完立刻 enroll 却报模板未生效?
**A3**: CA 有模板缓存,配置传播需要时间;可等一会儿或触发 CA 重新加载。也确认你改的是 AD 里的模板对象,而 CA 已发布该模板。
**Q4**: 只有 WriteDacl 没有直接 WriteProperty?
**A4**: 先用 WriteDacl 给自己加 WriteProperty/GenericAll,再改配置。ESC4 的本质就是"对模板对象的写权限",WriteDacl 是万能的第一跳。
**Q5**: 改模板这动作太危险不想留痕/怕告警怎么办?
**A5**: 转向 ESC7(如你也有 CA 管理权,直接批自己的挂起请求更干净)或直接找现成的 ESC1 模板。若必须改,务必记录原始 `msPKI-*` 值并在拿到证书后立即还原,别把模板长期留在脆弱态。

### CA 全局开了 SAN 扩展时走 ESC6
**Q1**: 没有 enrollee-supplies-subject 模板,但我怀疑 CA 层有洞,怎么判断 ESC6?
**A1**: 查 CA 的策略标志是否含 `EDITF_ATTRIBUTESUBJECTALTNAME2`。`certutil -config "CA\name" -getreg policy\EditFlags` 里含该位,或 Certipy find 会标 "User Specified SAN: Enabled"。开了就意味着任意模板申请时都能通过 SAN 属性注入任意 UPN。
**Q2**: 怎么利用?
**A2**: 拿任一可 enroll 的客户端认证模板,申请时带 `-upn administrator@dom`。因为 CA 全局接受 SAN,即使模板没开自填 SAN 也会被写进证书。
**Q3**: 2022 年 5 月补丁(KB5014754)后 ESC6 还灵吗?
**A3**: 补丁后 CA 默认强映射并忽略弱 SAN,ESC6 常失效。若失效,检查 CA 的 `StrongCertificateBindingEnforcement` 注册表值是否被降到兼容模式(1 或 0);仍是 2 则这支基本废了。
**Q4**: ESC6 失效后往哪转?
**A4**: 转 ESC1(找模板级自填 SAN)、ESC7(CA 管理)、ESC8/ESC11(relay)。ESC6 在打了补丁的现代域里成功率下降,不要当主路。
**Q5**: 想改 EDITF 让 ESC6 成立需要什么权限?
**A5**: 那已经是 ESC7 的范畴——需要 CA 的 ManageCA 权限用 `certutil -setreg` 或 Certipy 打开 EDITF flag。若你有 ManageCA,直接走 ESC7 批请求更直接,没必要先开 ESC6。

### 拿到 CA 的 ManageCA/ManageCertificates 权限时走 ESC7
**Q1**: 我发现某账户对 CA 有 ManageCA 或 Manage Certificates 权限(我能拿到该账户),怎么变现?
**A1**: ESC7。两种玩法:一是用 ManageCA 打开 CA 的 EDITF_ATTRIBUTESUBJECTALTNAME2 制造 ESC6,再申带任意 SAN 的证书;二是提交一个会被挂起的请求,然后用 Manage Certificates(Issue 权限)手动批准自己的挂起请求。
**Q2**: 只有 ManageCA 没有 Manage Certificates 怎么办?
**A2**: ManageCA 可以给自己添加 officer(证书管理员)角色,从而获得 Issue 能力;Certipy `ca -add-officer` 就是干这个的。拿到 Issue 权限后再批自己的请求。
**Q3**: 具体批挂起请求的流程?
**A3**: `certipy req` 提交后拿到 request id 且状态 pending,再 `certipy ca -issue-request <id>`,最后 `certipy req -retrieve <id>` 取回证书。
**Q4**: 我还能用 ManageCA 启用一个默认没发布的脆弱模板?
**A4**: 能。ManageCA 可以让 CA 发布新模板(`ca -enable-template`)。如果 AD 里存在但未发布的 SubCA 或脆弱模板,启用后即可 enroll,这是 ESC7 的一个强变体。
**Q5**: 权限操作被审计/回滚了怎么办?
**A5**: 转向:ESC7 的每个动作(加 officer、改 EDITF、发布模板)都会写 CA 日志,若环境监控严,改走一次性的 relay(ESC8/ESC11)或纯模板级 ESC1,少碰 CA 配置。用完 officer 角色记得 remove。
**Q6**: 根本拿不到有 ManageCA 的账户怎么办?
**A6**: 那 ESC7 前置就不成立,转回找可 enroll 的脆弱模板,或用 ESC8 把机器账户认证 relay 到 CA 的 HTTP 端点(不需要预先的 CA 管理权)。

### CA 的 Web 注册端点可 relay 时走 ESC8
**Q1**: CA 开了 certsrv 的 HTTP/HTTPS 注册端点,我怎么利用?
**A1**: ESC8——把某台机器账户的 NTLM 认证 relay 到 CA 的 `/certsrv/certfnsh.asp` 去替它申请证书。经典组合:PetitPotam/PrinterBug 强制 DC 机器账户向我认证,ntlmrelayx 把认证转发到 CA Web 端点申一张 DC 的机器证书。
**Q2**: relay 用什么模板?
**A2**: 默认用 Machine/DomainController 模板(机器认证)。`ntlmrelayx -t http://CA/certsrv/certfnsh.asp -smb2support --adcs --template DomainController`。拿到 DC 证书后 PKINIT 拿 DC 的 TGT,进而 DCSync。
**Q3**: 端点是 HTTPS 且要求 EPA/Channel Binding 怎么办?
**A3**: 若 CA 强制了 Extended Protection for Authentication,HTTP relay 会被 channel binding 挡。转 HTTP(80)端点如果开着,或转 ESC11(ICPR RPC relay,走 RPC 而非 HTTP,绕过 HTTP 的 EPA)。
**Q4**: 强制认证(coerce)被打了补丁怎么办?
**A4**: PetitPotam(EfsRpc)常被补,换 PrinterBug(MS-RPRN)、DFSCoerce(MS-DFSNM)、ShadowCoerce、或 Coercer 工具轮询多种方法。总有一个 RPC coerce 面没关全。实在不行用能触发认证的其他动作(WebDAV、登录脚本)。
**Q5**: relay 目标 CA Web 端点根本没开怎么办?
**A5**: 转 ESC11:如果 CA 的 ICertPassage RPC 接口(ICPR)未强制 RPC 加密/签名,可以 relay 到 RPC 而非 HTTP。或者放弃 relay 支,回模板级 ESC1-4。
**Q6**: 我 relay 到的账户不是特权机器怎么办?
**A6**: coerce 时要指定让高价值主机(DC、CA 本身、Exchange)向你认证,而不是随便一台。relay 的价值取决于被强制认证那台机器的权限。选 DC 机器账户 = 直通 DCSync。

### 判断该 coerce 谁、relay 到哪
**Q1**: 我想走 relay 但不知道该逼哪台机器向我认证。怎么选?
**A1**: 选目标原则:被 coerce 的机器权限越高越好(DC 机器账户能 DCSync,CA 机器账户能签发,Exchange 机器账户在很多域里有高 ACL)。relay 落点则看你要什么——落 ADCS 换证书(ESC8/11),落 LDAP 做 RBCD/shadow credentials,落 SMB 做本地管理。
**Q2**: 一次 coerce 能不能同时喂多个 relay 目标?
**A2**: ntlmrelayx 支持多目标,但同一个认证只被消费一次。实战里更稳的是想清楚要哪条链,针对性 coerce,避免认证被错误目标吃掉。
**Q3**: DC 强制自己向 DC relay(loopback)会被拦吗?
**A3**: 现代补丁对 NTLM reflection/loopback 有防护,DC relay 回自身多半失败。所以 relay 到"另一台"服务(CA、另一 DC、LDAP)才靠谱。这也是 ESC8 选 CA 作为 relay 落点的原因。
**Q4**: coerce 成功但 relay 立刻断连?
**A4**: 检查 SMB 签名(relay 到 SMB 需目标不强制签名)、LDAP 签名/channel binding(relay 到 LDAP 需未强制)。ADCS HTTP relay 不受 SMB 签名影响,这也是它常成为首选落点的原因。
**Q5**: 所有 relay 面(SMB 签名开、LDAP 强制、ADCS EPA 开)都封死了怎么办?
**A5**: 转离 NTLM relay 这整个维度:走 Kerberos 侧(RBCD 需要能改目标机器账户的 msDS-AllowedToActOnBehalfOf,或 shadow credentials 改 msDS-KeyCredentialLink),或纯凭据侧(Kerberoast、明文口令复用)。

### 拿到证书后 PKINIT 失败的排错
**Q1**: 我申到了目标身份的 pfx,但 `certipy auth` 换 TGT 报 KDC_ERR 或映射失败,怎么办?
**A1**: 大概率是 2022 强证书映射补丁。域升级到强制模式后,KDC 要求证书里带 SID 扩展(szOID 1.3.6.1.4.1.311.25.2)才认。解法:申证书时用 `-sid <目标objectSid>` 让 Certipy 把 SID 塞进证书。
**Q2**: 目标模板不让我塞 SID 怎么办?
**A2**: 若走 ESC1 自填 SAN,`-sid` 依赖 SAN 的 URL 扩展写入;若 CA 拒绝,可能需要经 ESC6/ESC7 在 CA 侧放行,或改打一个允许的映射方式。也可回退到 Schannel/LDAP 认证而非 Kerberos PKINIT。
**Q3**: PKINIT 完全走不通,但我手里有合法证书?
**A3**: 转 Schannel:用 `certipy auth -pfx x.pfx -ldap-shell` 走 LDAP over TLS 的证书认证,拿到 LDAP shell 后可做 shadow credentials、RBCD、加机器等,而不经 Kerberos PKINIT。
**Q4**: 想从证书直接拿 NT hash?
**A4**: PKINIT 拿到 TGT 后可通过 U2U + PAC 里的 NTLM_SUPPLEMENTAL_CREDENTIAL 解出该账户 NT hash(Certipy auth 自动做)。前提是账户有 NT hash 且 KDC 返回该结构。纯托管服务账户可能拿不到 hash,只能用票据。
**Q5**: 证书有效但目标账户被禁用/改密了?
**A5**: 证书认证不依赖当前口令,但依赖账户启用状态。目标禁用则 PKINIT 失败,换一个仍启用的特权目标。这也是为什么优先选活跃管理员或机器账户而非可能被禁的历史账户。

### 证书有效期长——当作持久化凭据用
**Q1**: 我拿到一张管理员证书,除了立刻用还能怎么榨取价值?
**A1**: 证书是"离线凭据",有效期常达 1-2 年甚至更长,且改口令不影响它。把它当持久化:即便对方发现入侵改了所有口令,只要不吊销这张证书,你仍能随时 PKINIT 回来。妥善离线保存 pfx。
**Q2**: 怎么给自己长期发一张证书而不惊动人?
**A2**: 用普通用户模板给自己(当前账户)申一张客户端认证证书,这在很多域是正常操作、几乎不告警。它让你在丢失口令后仍能认证,是低调的用户级持久化(THEFT 类)。
**Q3**: 我担心证书被吊销?
**A3**: 吊销需要管理员主动在 CA 上 revoke 并等 CRL 更新;多数蓝队不监控也不吊销个别证书。风险在于整表吊销或 CA 重建。为稳妥,同时保留多种持久化(shadow credentials、票据、口令)不把鸡蛋放一个篮子。
**Q4**: 能不能伪造证书做黄金证书?
**A4**: 若攻陷了 CA 主机本身,可导出 CA 私钥(`certipy ca -backup` 或 mimikatz),用它离线签发任意身份的证书=Golden Certificate,不经 CA 在线、无签发日志。这是 ADCS 攻击链的顶点持久化。
**Q5**: CA 私钥被 HSM/TPM 保护导不出怎么办?
**A5**: 转向:导不出私钥就做不了黄金证书,退而求其次用在线签发的长期证书,或攻陷后在 CA 上改配置放行未来签发,或转 DPAPI/DCSync 等其他域持久化维度。

### 机器账户 shadow credentials(ESC 之外的证书相邻路)
**Q1**: 我对某目标账户有写 msDS-KeyCredentialLink 的权限(GenericWrite/GenericAll),和证书什么关系?
**A1**: 这是 Shadow Credentials(Whisker/pyWhisker)。往目标的 KeyCredentialLink 写一对 KeyCredential(公钥),然后用对应私钥走 PKINIT 认证成该目标。本质也是证书/密钥认证,依赖 ADCS 部署了 KDC 证书(PKINIT 可用),但不需要脆弱模板。
**Q2**: 什么时候优先它而不是 RBCD?
**A2**: 当你只需要"变成这个账户"而非"以它身份访问某服务"时,shadow credentials 更直接干净(直接拿 TGT 和 NT hash)。RBCD 需要额外一台受控机器账户且落到特定服务。
**Q3**: 写入后 PKINIT 报没有合适的 KDC 证书?
**A3**: 说明域没给 DC 签发 PKINIT 用的域控证书,PKINIT 不可用,shadow credentials 认证就失败。转 RBCD(纯 Kerberos 委派,不需 PKINIT)或直接用写权限做别的。
**Q4**: 目标 KeyCredentialLink 已有内容,我怕破坏?
**A4**: 追加而非覆盖,操作前用工具 list 备份原值,用完 remove 自己加的那条。别清空人家原有的,会引起故障告警。
**Q5**: 我没有对任何高价值账户的写权限怎么办?
**A5**: 转回找写权限来源:ACL 攻击链(BloodHound 找 GenericWrite/WriteDacl 路径)、或先通过 ESC1/relay 拿一个能改 ACL 的身份,再回来做 shadow credentials。

### 面对一堆脆弱模板不知先打哪
**Q1**: Certipy 一次报了 ESC1/ESC2/ESC3/ESC4 好几个,我该按什么顺序打?
**A1**: 排序判据:①前置权限是否已满足(我现在的身份就能 enroll 的排前);②直达程度(ESC1 一步拿管理员证书 > ESC3 两步 > ESC4 要改配置);③留痕与风险(不改 CA/模板配置的优先);④是否需要额外触发(relay 类要 coerce,排后)。综合选"我现在就能一步打通且改动最小"的那个。
**Q2**: 有个模板命中多个 ESC 怎么算?
**A2**: 命中越多说明配置越松,通常越好打,但也可能是蜜罐诱饵。真实环境优先选看起来"业务正常但恰好配置疏忽"的模板,而非明显异常敞开的。
**Q3**: 我怎么知道当前身份对模板有没有 enroll 权限?
**A3**: Certipy find 会解析模板 ACL 里的 enrollment principals 并和你所在组比对并标注 "Enrollee"。也可看模板的 `msPKI-Enrollment` 相关 ACE 是否含 Authenticated Users / Domain Users / 你所在组。
**Q4**: 全部模板我都没 enroll 权限怎么办?
**A4**: 转向:先横向拿一个有 enroll 权限的账户(域内多数用户模板对 Domain Users 开放,先确认这个);或走不依赖模板 enroll 权限的 ESC8/ESC11 relay。
**Q5**: 我打了一个 ESC 成功拿到管理员了,还需要管其它的吗?
**A5**: 拿到 DA/EA 后其余 ESC 不必逐个打,但值得记录留作持久化和报告。作为红队要覆盖"如果这条被封还有哪条",在报告里点明多路径以体现纵深。

### 目标模板要求 Manager Approval 时怎么绕
**Q1**: 心仪的脆弱模板配了 `CT_FLAG_PEND_ALL_REQUESTS`(需要人工审批),申请后一直 pending,怎么办?
**A1**: 单靠 enroll 权限过不了审批。判断你是否另有 CA 侧的 Manage Certificates(Issue)权限——有就自己批(ESC7 玩法)。没有就这个模板走不通,别干等。
**Q2**: 能不能找不需要审批的等价模板?
**A2**: 优先。同 CA 通常有多个客户端认证模板,找一个没开 PEND_ALL_REQUESTS 的。审批要求是模板级配置,换模板即可绕过。
**Q3**: 如果我有 ESC4(模板写权限)?
**A3**: 直接把 `msPKI-Enrollment-Flag` 里的 PEND_ALL_REQUESTS 位关掉,申请变自动签发,拿完再还原。ESC4 能一并解决审批、SAN、EKU 多个限制。
**Q4**: 挂起的请求会不会被管理员看到而暴露?
**A4**: 会。pending 请求出现在 CA 管理台,可能触发人工审查。如果你没法自己批,留一堆 pending 反而暴露意图。要么别申这种模板,要么走能自动签发的路。
**Q5**: 所有可用模板都要审批怎么办?
**A5**: 转向 ESC6(CA 全局 SAN,若开着可用任意自动签发模板)、ESC7(拿 Issue 权限自批)、或彻底转 relay/非证书路。审批墙是模板级防御,正交跳到 CA 级或非模板路即可。

### 模板要求授权签名(Authorized Signature)时的判断
**Q1**: 模板设了 `msPKI-RA-Signature >= 1`,要求申请必须带一个具备特定 Application Policy 的签名,这挡住我了吗?
**A1**: 这要求申请前先有一张满足指定 Application Policy(如 Certificate Request Agent)的证书来签名。如果你能先拿到那张代理证书(可能来自 ESC3 或另一个脆弱模板),就能满足签名要求继续申请。
**Q2**: 我没有满足策略的签名证书?
**A2**: 找发放该 Application Policy 的模板并 enroll 一张。链式:先攻脆弱的代理模板拿签名证书 → 再用它签名申请这个目标模板。这正是 ESC3 的组合形态。
**Q3**: 签名要求 + 我有 ESC4?
**A3**: ESC4 直接把 `msPKI-RA-Signature` 改成 0,去掉签名要求。写权限压倒一切模板级约束。
**Q4**: 这条链太绕,不想凑签名怎么办?
**A4**: 转向别的不要求授权签名的模板,或 CA 级(ESC6/7)、relay 级(ESC8/11)。授权签名是较强的模板防御,存在更省事的旁路就别硬凑。

### 只有 Schannel/LDAP 想用证书拿域控制权
**Q1**: PKINIT 环境不理想,但我有一张高权限证书,想不经 Kerberos 直接操作 AD,怎么办?
**A1**: 走 Schannel:证书可用于 LDAPS 客户端认证。`certipy auth -pfx x.pfx -ldap-shell` 或用 PassTheCert 直接以证书连 LDAP。拿到 LDAP 会话后做 RBCD、shadow credentials、改 DACL、加计算机等。
**Q2**: PassTheCert 连上后能干嘛最狠?
**A2**: 若证书身份是 DC 机器账户或有高 ACL 的账户,可给自己账户加 RBCD 到 DC、或直接改 domain 对象 DACL 赋予自己 DCSync 权限(加 DS-Replication-Get-Changes),然后 secretsdump 拖 hash。
**Q3**: LDAPS 要求 channel binding 导致证书认证被拒?
**A3**: 试标准 636/LDAPS 与 StartTLS;若强制 channel binding 且实现不匹配,退回 PKINIT(如果可用)。两条证书认证通道(Kerberos PKINIT / Schannel LDAP)是互补的,一条被封试另一条。
**Q4**: 两条都不通怎么办?
**A4**: 转非证书维度:该证书身份如果对应一个账户,尝试 U2U 解 NT hash 后走 SMB/WMI/口令复用;或把证书价值转为持久化留存,先另找路提权。

### CA 主机本身被你拿下后怎么榨干
**Q1**: 我通过其它路拿到了 CA 服务器的本地管理员/SYSTEM,能得到什么终极能力?
**A1**: CA 主机 = 证书王座。导出 CA 的私钥和证书(`certipy ca -backup -ca name` 或 mimikatz `crypto::certificates /export`),用它离线签发任意身份、任意 EKU 的证书(黄金证书),完全绕过在线签发、无请求日志、无需脆弱模板。
**Q2**: 私钥导出被拒?
**A2**: 私钥可能标记不可导出或存 HSM。mimikatz 有 patch CryptoAPI 使不可导出私钥可导出的技巧(`crypto::capi`/`crypto::cng`);HSM 保护的则真导不出,只能在机上"借用"它在线签发。
**Q3**: 黄金证书怎么用?
**A3**: 用 ForgeCert/Certipy forge 以 CA 私钥签一张 administrator 的证书,再 PKINIT。因为签名合法且链到受信 CA,KDC 完全接受。这是最隐蔽的域持久化之一,口令重置也无效。
**Q4**: 黄金证书会被怎样反制?
**A4**: 唯一硬反制是吊销 CA 根/中间证书或重建 PKI——代价极大蓝队通常不做。但引入新 SID 强映射要求后,伪造证书也需带正确 SID;forge 时把目标 objectSid 加进去即可。
**Q5**: 我拿了 CA 但只想临时用一次不想留后门?
**A5**: 那就在线用现成模板签一张就走,别 backup 私钥(备份操作本身有日志)。红队按交战规则,能不留最高危后门就克制,黄金证书写进报告作为"可达能力"证明即可。

### ESC9/ESC10 弱证书映射的利用
**Q1**: Certipy 提示某模板有 ESC9(no security extension)或环境有 ESC10(弱映射注册表),这两个怎么用?
**A1**: ESC9:模板设了 `CT_FLAG_NO_SECURITY_EXTENSION`,证书不含 SID 扩展,于是 KDC 回退到基于 UPN 的弱映射。配合你能改某账户 UPN 的权限,把受控账户 UPN 改成目标(如 administrator),申证书,认证时被映射成目标。ESC10 是 CA 侧注册表把映射降级(StrongCertificateBindingEnforcement=0 或 CertificateMappingMethods 含弱位)导致同类弱映射。
**Q2**: 利用 ESC9 的具体链?
**A2**: 需要对一个账户(如你控制的机器账户或有 GenericWrite 的用户)改 userPrincipalName 为 `administrator`(去掉 @domain 或用目标 sAMAccountName),用 ESC9 模板申证书,PKINIT/Schannel 认证被弱映射到真 administrator,然后把 UPN 改回避免冲突。
**Q3**: 改 UPN 被拒或冲突?
**A3**: UPN 需全域唯一,若目标 UPN 已占用会失败。用不带域后缀的形式(纯 sAMAccountName)绕唯一性,或改一个当前无人用 UPN 的目标。需要对该账户的写属性权限作为前置。
**Q4**: 这两个和 2022 补丁的关系?
**A4**: ESC9/ESC10 恰恰是"绕过强映射"的手法——它们利用配置没把强映射打满的缝隙。若环境严格强制强映射(enforcement=2 且模板都带 SID 扩展),这两支就失效。
**Q5**: 失效后转哪?
**A5**: 转 ESC1(带 SID 的直接 SAN)、ESC6/7(CA 级)、relay 类。ESC9/10 是特定配置缝隙,不通就回主流脆弱模板路。

### ESC11(ICPR RPC relay)何时上场
**Q1**: CA 的 HTTP 注册端点没开或有 EPA,ESC8 走不了,ESC11 怎么救场?
**A1**: ESC11 relay 到 CA 的 MS-ICPR RPC 接口(证书注册的 RPC 通道)。前提:该 RPC 接口的 `IF_ENFORCEENCRYPTICERTREQUEST` 未启用(即未强制加密/包完整性)。此时可把 coerce 来的 NTLM 认证 relay 到 RPC 申证书,绕过 HTTP 侧限制。
**Q2**: 用什么工具?
**A2**: 打了 ESC11 补丁支持的 ntlmrelayx(带 -icpr/rpc-mode 的分支)或 certipy 相关模块。流程同 ESC8:coerce 高价值机器 → relay 到 CA RPC → 拿机器证书 → PKINIT。
**Q3**: RPC 强制了加密怎么办?
**A3**: 那 ESC11 前置不成立。回 ESC8(如 HTTP 端点某个还开着且无 EPA),或彻底放弃 relay 走模板级/CA 级。
**Q4**: 怎么快速判断该走 ESC8 还是 ESC11?
**A4**: 判据:HTTP 端点开且无 EPA → ESC8 更成熟;HTTP 关或有 EPA 但 RPC 未强制加密 → ESC11。Certipy find 的 CA 段会标 "Enforce Encryption for Requests" 与 Web enrollment 状态,据此二选一。

### 打之前先分清"我要提权还是要横向/持久化"
**Q1**: 面对 ADCS 攻击面,我怎么定目标以免乱打?
**A1**: 先明确当前诉求。要提权到域管:找能签发高权限身份证书的 ESC(1/3/4/6/7/8)。要横向到某台机器:证书身份对应机器账户即可 PKINIT 成机器。要持久化:给自己/机器申长期证书或黄金证书。目标不同,选的 ESC 和目标 SAN 就不同。
**Q2**: 我并不需要 DA,只想拿某台特定服务器?
**A2**: 那就申请那台机器账户(host$)的证书或走 RBCD,而非一味打 administrator。选最小够用的目标,减少动静。
**Q3**: 诉求是长期潜伏?
**A3**: 优先低告警操作:用正常用户模板给自己申长期证书(几乎无异常),避免高危的改 CA 配置和黄金证书(除非必要)。把高危能力记录为"可达"而非实际部署。
**Q4**: 打偏了(拿了不需要的高权限反而触发告警)怎么办?
**A4**: 复盘选路:证书攻击很多步骤会写 CA 日志(请求者、模板、SAN),打高危目标更易被 threat hunting 命中。转向更贴合诉求的最小路径,并清理不必要的挂起/已发证书痕迹。

### 证书攻击的痕迹与规避
**Q1**: 我要走证书路但担心蓝队从日志抓到,哪些动作最响?
**A1**: 最响的:证书请求事件(Event 4886/4887,含请求者与模板)、SAN 与请求者 sAMAccountName 不一致(经典 ESC1 指纹)、短时间大量或异常模板的 enroll、CA 配置更改(EDITF/officer/模板发布)、coerce 触发的认证异常。
**Q2**: 怎么降低 ESC1 的 SAN 不一致告警?
**A2**: 这是硬指纹很难完全消除。可选择目标 SID 映射方式、控制申请频率、用看起来合理的模板;但要认识到成熟蓝队专门监控"请求者≠SAN 主体"。评估被抓风险后决定是否走这支还是更隐蔽的 relay/shadow creds。
**Q3**: relay 类会留下什么?
**A3**: coerce 会在被强制机器上留 RPC 调用与向异常主机的认证记录;relay 落 CA 会留一条机器证书请求。蓝队若关联"某机器突然申请了自己的证书 + 之前有 coerce"就能定位。尽量缩短窗口、用完即走。
**Q4**: 想事后清理?
**A4**: CA 日志与 Windows 事件日志清理需要 CA 主机管理员权限且清日志本身高危(4104/1102)。红队一般不硬清,而是靠低调选路和最小动作降低命中率,把清理留到确有把握。
**Q5**: 被蓝队盯上正在响应怎么办?
**A5**: 立即切换持久化立足点(别只靠刚暴露的证书),评估该证书是否会被吊销,预备好正交回归通道(另一个身份/另一台机的立足点),避免单点被拔即出局。

### 域内没脆弱模板但有 CA——还有戏吗
**Q1**: Certipy find 没报任何脆弱模板,CA 也没 EDITF 洞,这条路是不是死了?
**A1**: 未必。还有不依赖模板配置的支:ESC7(如能拿到 CA 管理权账户)、ESC8/11(如注册端点可 relay)、CA 主机被拿下后的黄金证书、以及 shadow credentials(只要 PKINIT 可用,不需脆弱模板)。先逐一核对这些前置。
**Q2**: 这些前置也都不满足?
**A2**: 那 ADCS 这个维度暂时无解,坦然转正交:回 BloodHound 找 ACL/委派路径、Kerberoast、口令喷洒、已有凭据的横向、漏洞面(未打补丁的 EternalBlue/Zerologon/PrintNightmare 等)。
**Q3**: 会不会是我侦察不全漏了模板?
**A3**: 换个身份再 find(不同用户对模板 ACL 可见性不同);或直接 `certutil -template` 在 Windows 上枚举可能更全。有时脆弱模板对特定组开放,拿到那个组成员身份后才显现。
**Q4**: 值不值得为证书路继续投入?
**A4**: 判据:若已有更快的非证书提权路(如现成 Kerberoast 弱口令),先走它。证书路强在隐蔽持久,但当下不通就别沉没成本,拿到域控后再回头把 ADCS 作为持久化补上。

### 拿到证书身份后想 DCSync 的落地
**Q1**: 我 PKINIT 成了 DC 机器账户或 DA,下一步 DCSync 怎么最稳?
**A1**: 有了 TGT 后 `secretsdump -k -no-pass DC` 或 mimikatz `lsadump::dcsync /user:krbtgt`。DC 机器账户默认有复制权限,足以 DCSync。先抓 krbtgt 做黄金票据作为终极持久化,再抓目标账户 hash。
**Q2**: DCSync 报权限不足?
**A2**: 确认你的证书身份确实有 Replicating Directory Changes (All) 权限。普通用户证书不行;要 DA、DC 机器账户或被显式授予复制权的对象。若身份不够,回去挑对的 relay/申请目标。
**Q3**: 只想要单个账户 hash 别惊动?
**A3**: DCSync 指定单一 /user 比全量拖库动静小,4662 事件也少。要 krbtgt 就单拉 krbtgt,别 dump 整个域。
**Q4**: DCSync 被监控(4662 复制权限告警)拦了?
**A4**: 转向:不走复制协议,改在 DC 上本地 dump(需 DC 上代码执行)、或用 ntds.dit + system hive 离线导出(卷影副本),或用刚拿的高权限做 DPAPI/LSASS 抓活跃凭据。DCSync 只是拿 hash 的一种,别在被监控的协议上硬撞。

### 证书 EKU 决定"能用来干嘛"——别拿错刀
**Q1**: 我拿到一张证书但用它认证失败,怀疑 EKU 不对,怎么核?
**A1**: 看证书的 Extended Key Usage:要做域认证必须含 Client Authentication(1.3.6.1.5.5.7.3.2)、Smart Card Logon(1.3.6.1.4.1.311.20.2.2)、PKINIT Client Auth(1.3.6.1.5.2.3.4)或 Any Purpose 之一。只含 Server Authentication、Code Signing、EFS 等的证书不能用于登录。
**Q2**: 模板 EKU 是 Server Authentication 能干嘛?
**A2**: 不能 PKINIT 登录,但可用于中间人/伪造 TLS 服务端(如伪造 LDAPS、内部 HTTPS 服务)或某些依赖服务端证书信任的攻击。选路时别拿它去打认证。
**Q3**: EKU 是 Any Purpose 或空?
**A3**: Any Purpose(ESC2)涵盖客户端认证可直接用;EKU 完全为空在很多实现里等同 Any Purpose 也可认证。这类模板价值高,优先。
**Q4**: 只有非认证 EKU 的模板,证书路是不是废了?
**A4**: 就认证提权而言这刀不对。转找含 Client Auth 的模板;或若你有 ESC4 写权限,直接给模板加上 Client Authentication EKU。EKU 不对时换模板或改模板,而不是硬用错刀。

### 会话/凭据形态转换:证书 ↔ 票据 ↔ hash
**Q1**: 我手上是证书,但当前工具/横向手法要的是 NT hash 或 TGT,怎么转?
**A1**: 证书 → TGT:PKINIT(certipy auth)。TGT → NT hash:U2U 解 PAC 里的 NTLM 补充凭据(certipy auth 自动出 hash)。有了 hash 就能 pass-the-hash 横向;有了 TGT 就能 pass-the-ticket。三种形态按需互转,别被单一形态卡住。
**Q2**: 拿到 hash 后想回落成明文?
**A2**: hash 一般不可逆,但可 pass-the-hash 直接用,或对弱口令离线爆破,或用 hash 请求 TGT 再运营。多数横向不需要明文,PtH/PtT 足矣。
**Q3**: 只有票据但工具要 ccache/kirbi 格式不匹配?
**A3**: 用 impacket ticketConverter 在 kirbi(Windows/Rubeus)与 ccache(Linux/impacket)间转换。跨平台运营时格式转换是常见卡点,备好转换工具。
**Q4**: 全都转不动、认证一直失败?
**A4**: 回到最根本判据:目标账户是否启用、证书是否过期/被吊销、时钟是否同步(Kerberos 对时间敏感,偏差>5 分钟直接失败)。先排这些基础项再怀疑高级映射问题。

### 机器账户配额与"自造机器账户"配合证书
**Q1**: 很多证书/RBCD 链要求我有一个受控机器账户,普通用户能造吗?
**A1**: 看 `ms-DS-MachineAccountQuota`,默认 10,即普通域用户可加最多 10 台计算机账户。用 impacket addcomputer 或 PowerMad 造一个,拿到它的凭据后用于 RBCD、shadow credentials 或作为 relay/证书链里的"受控身份"。
**Q2**: 配额被设为 0 怎么办?
**A2**: 造不了机器账户,链断。转向:找现成可控的机器账户(某台已拿下主机的 host$),或找有 CreateChild 计算机对象权限的身份,或改走不需要机器账户的支(纯 ESC1、ESC8 直接 relay DC)。
**Q3**: 造了机器账户但 RBCD 设置失败?
**A3**: 设 msDS-AllowedToActOnBehalfOfOtherIdentity 需要对目标机器对象的写权限。若你只是造了机器账户但对目标 DC 没写权限,RBCD 到 DC 不成立。这时 shadow credentials(改目标 KeyCredentialLink,同样需写权限)或证书直申更合适。
**Q4**: 这一大套都不通?
**A4**: 说明你缺"对目标对象的写权限"这个核心前置。转回权限获取:BloodHound 找谁能写目标、或先用其它 ESC 提权拿到能写的身份,再回来组装机器账户链。

### 判断是"真脆弱"还是蜜罐/诱饵模板
**Q1**: 域里赫然一个配置极其敞开的模板(Domain Users 可 enroll + 自填 SAN + 客户端认证 + 无审批),太顺了反而心虚,怎么判断?
**A1**: 现代蓝队会布 ADCS 蜜罐模板专抓这种"太完美"的 ESC1。警惕信号:模板名可疑、创建时间很新、无正常业务对应、Certipy 之外无历史使用。判据不是不打,而是评估触发监控的概率。
**Q2**: 怎么低风险验证?
**A2**: 可先申一个低敏感目标(自己账户)的证书试水,观察是否有异常响应(账户被禁、会话被切、蓝队动作),而不是一上来就申 administrator。若是蜜罐,申 DA 会立刻触发高优告警。
**Q3**: 确认是蜜罐后?
**A3**: 避开它,转别的证书路或非证书路。同时这本身是情报:说明蓝队在监控 ADCS,后续所有证书操作都要更谨慎、更最小化。
**Q4**: 无法判断真假但时间紧?
**A4**: 权衡交战目标:比赛/评估要拿分就快速利用并接受暴露风险;真实红队重隐蔽则宁可绕行。把决策记入报告说明为何选/不选该模板。

### 跨域/跨林场景下证书攻击的边界
**Q1**: 我在子域拿到 DA,想经证书路上打到父域/林根,证书能跨域吗?
**A1**: 企业 CA 通常发布在配置分区,对整个林可见,一个 CA 可服务多域。若脆弱模板允许申请林内其它域账户的证书(SAN/SID 指向父域账户),就能跨域。关键看模板 ACL 与 CA 是否接受跨域 SAN。
**Q2**: 想直接申请林根 administrator 的证书?
**A2**: 需要该 CA 信任并处理跨域身份,且你能塞入目标域的 SID(强映射下)。子域 DA 通常已足够经 SID History/信任票据打林根,证书是补充手段之一。
**Q3**: 跨林(外部/林信任)呢?
**A3**: 证书信任不自动跨林,除非显式配置了 PKI 跨林信任。多数情况跨林打不了证书路,转跨林的经典手法(信任账户、外部信任枚举、SID filtering 绕过)。
**Q4**: 跨域证书申请被拒?
**A4**: 转向林内提权的成熟路:子域 DA → 伪造跨域引荐票据(利用 krbtgt of child + 目标 SID)打林根,或 SID History 注入。证书跨域受限时,Kerberos 信任路更通用。

### 对 CA 服务器对象/PKI 容器有写权限时走 ESC5
**Q1**: BloodHound/Certipy 提示我(或我可控组)对 CA 的计算机对象、`pKIEnrollmentService` 对象、`NTAuthCertificates` 或 `Public Key Services` 容器有写权限,这是 ESC5,怎么变现?
**A1**: ESC5 是 PKI 控制面的 ACL 面,比单个模板范围更大。分支判断:①能写 CA 主机计算机对象 → 走 RBCD 或 shadow credentials 拿下 CA 主机 SYSTEM,再导私钥做黄金证书;②能写 `NTAuthCertificates` → 把你自签的恶意 CA 证书塞进受信 NTAuth,让你伪造的证书全域被信任;③能写 `pKIEnrollmentService` → 篡改 CA 发布的模板列表。判据:对 PKI 关键对象的 Write/GenericAll。
**Q2**: 三个分支优先哪个?
**A2**: 优先"能直达 CA 主机控制"的那条(写 CA 计算机对象),因为拿下 CA 主机后一切皆可(在线签发+离线伪造)。写 NTAuthCertificates 更隐蔽但需要你能生成并注入 CA 证书,链更长。
**Q3**: 把恶意 CA 塞进 NTAuth 后证书还是不被信任?
**A3**: NTAuth 更新需要复制到各 DC,且 DC 有本地缓存(certutil -enterprise NTAuth),传播有延迟;还需你的伪造证书链正确且带 SID 强扩展。核对复制是否完成、链是否闭合。
**Q4**: ESC5 的写权限太边缘拼不出完整链怎么办?
**A4**: 把它交回 BloodHound 做全局路径规划——ESC5 常是更长 ACL 链的一环。若 PKI 语义用不出来,退回通用视角:这把写权限还能写哪些高价值对象?别死磕证书,转 RBCD/DCSync 授权等其它 ACL 变现。

### 证书里的 issuance policy 关联到特权组时走 ESC13
**Q1**: Certipy 标某模板带一个 issuance policy(certificate-issuance policy)OID,且该 OID 通过 `msDS-OIDToGroupLink` 链接到一个特权 AD 组,这是 ESC13,怎么用?
**A1**: ESC13 的核心:当某 issuance policy OID 被 OIDToGroupLink 绑到一个组(如某高权限组),任何持有带该 policy 的证书的账户,认证后其令牌里会"获得"该组成员身份。所以只要你能 enroll 这个带特权 policy 的模板,拿证书认证即隐式提升为该组成员。判据:模板的 `msPKI-Certificate-Policy` 指向的 OID 在 AD 里有 group link 且组是特权组。
**Q2**: 怎么落地利用?
**A2**: 用当前身份 enroll 该模板拿证书(不需要自填 SAN,主体还是你自己),然后 PKINIT/Schannel 认证。KDC/DC 依据 OIDToGroupLink 在你的 PAC 里加进那个特权组。你无需成为管理员,凭组成员资格即可访问该组能访问的资源。
**Q3**: 认证后令牌里没出现预期的组?
**A3**: 检查:OIDToGroupLink 是否真的生效(需 DC 支持且组类型/范围合法,通常要 Universal 组);模板的 policy OID 是否和链接的 OID 完全一致;你是否真有该模板 enroll 权限。任一不匹配则组不注入。
**Q4**: ESC13 走不通往哪转?
**A4**: 转其它模板级 ESC(1/2/3/9)或直接找该特权组的常规接管路径(ACL/组成员写)。ESC13 依赖特定 OID-组绑定,是较新且少见的配置,不通就回主流。

### altSecurityIdentities 弱显式映射时走 ESC14
**Q1**: 我发现能写某账户的 `altSecurityIdentities` 属性(或环境里存在弱显式映射),这是 ESC14,和 ESC9/10 的隐式弱映射有何不同,怎么用?
**A1**: ESC14 打的是"显式证书映射":altSecurityIdentities 属性可把一张证书显式绑定到某账户。若你能写目标(如 administrator)的该属性,就把一张你能控制/签发的证书的标识(如 Issuer+Subject、或弱格式如仅 Subject/仅 Issuer)写进去,然后用对应证书认证即被映射成目标。判据:对目标 altSecurityIdentities 有写权限,或环境里已存在弱格式(X509IssuerSubject/X509SubjectOnly 等易伪造的)映射。
**Q2**: 弱格式具体指什么、为什么危险?
**A2**: 强格式是 X509IssuerSerialNumber、X509SKI、X509SHA1PublicKey(绑定到唯一证书);弱格式如 X509SubjectOnly、X509IssuerSubject、X509RFC822(邮箱)可被"任意一张 Subject/Issuer 匹配的证书"满足,攻击者只要弄到一张 Subject 匹配的证书即可冒充。
**Q3**: 我没有对目标的写权限,只是发现存量弱映射?
**A3**: 那就想办法搞到一张满足该弱映射条件的证书(Subject/Issuer 匹配即可,可能通过某个允许自定义 Subject 的模板签发),用它认证成目标。前置是弱映射的匹配条件你能满足。
**Q4**: ESC14 前置(写属性或存量弱映射)都不成立怎么办?
**A4**: 转回隐式映射路 ESC9/10(改 UPN)、或直接 shadow credentials(改 KeyCredentialLink,也是写目标属性但语义更直接)。它们都属于"改目标某属性以冒充"这一族,择前置最易满足者。

### v1 模板可注入 Application Policies 时走 ESC15/EKUwu
**Q1**: 目标是一个 schema version 1 的模板(如默认 WebServer),我有 enroll 权限但它 EKU 是服务端认证不能登录,还有戏吗?
**A1**: 有——ESC15/EKUwu(CVE-2024-49019)。v1 模板在处理 CSR 时会把请求里携带的 Application Policies 一并带进签发的证书。于是可以在 CSR 里注入 Client Authentication 甚至 Certificate Request Agent 的 application policy,让本来只做服务端认证的证书获得客户端认证/代理能力。判据:模板 schemaVersion=1 且允许自定义(部分 v1 模板如 WebServer 常对域用户开放)。
**Q2**: 怎么打?
**A2**: 用支持 -application-policies 的 Certipy(新版)向该 v1 模板申请,注入 Client Authentication 策略拿到能登录的证书;若模板同时允许自填 SAN,配合注入还能像 ESC1 一样冒充目标 UPN。进一步注入 Certificate Request Agent 策略可退化成 ESC3 代理链。
**Q3**: 注入的 application policy 没进证书或认证仍失败?
**A3**: 确认 CA 未打对应补丁(补丁后 CA 会忽略请求侧 application policy);确认注入的是 application policy 扩展而非普通 EKU 扩展(v1 逻辑吃的是前者);确认强证书映射是否要求 SID(需一并注入)。
**Q4**: CA 已打补丁 / 没有可控 v1 模板怎么办?
**A4**: 转其它模板级 ESC 或 CA 级路。ESC15 是较新的 v1 处理缺陷,补丁后即失效,把它当"遇到 v1 模板时多试一手"的补充手段,不作主路。

### Certipy 被 EDR 拦或环境无 Python 时的转向
**Q1**: 我在一台受控 Windows 上想打 ADCS,但没法上传 Certipy/impacket 或它们被 EDR 秒杀,怎么办?
**A1**: 转纯原生手法,用系统自带的 certutil/certreq/PowerShell PKI 模块,不落第三方二进制。枚举:`certutil -config - -ping`、`certutil -template`、`Get-CertificateTemplate`(PSPKI 模块)。申请:构造 INF 文件用 `certreq -new` 生成 CSR、`certreq -submit -config CA 模板` 提交。这些是合法管理工具,EDR 命中率远低。
**Q2**: ESC1 的自填 SAN 用原生怎么塞?
**A2**: 在 certreq 的 INF/CSR 里通过 `[Extensions]` 段写 `2.5.29.17`(SubjectAltName)扩展,或提交时用 `-attrib "SAN:upn=administrator@dom"`(当 CA 允许属性 SAN,即 ESC6 场景)。拿到证书后用 Rubeus `asktgt /certificate:` 走 PKINIT,全程原生+单个 Rubeus。
**Q3**: 连 Rubeus 都被拦?
**A3**: 用 PowerShell 直接调 Windows API 或 certutil 导出的 pfx 走 `certutil -MergePFX`,再用系统 klist/自带机制;或把 pfx 带出到自己 Linux 机上用 impacket/Certipy 认证,把"申请"和"认证"分离到不同环境,规避单机 EDR。
**Q4**: 原生工具也被应用白名单/AMSI 卡死?
**A4**: 转执行方式维度:换一台防护弱的受控主机做证书操作,或把 CA 通信通过隧道代理到你的 Linux 攻击机纯远程打(证书攻击大多是网络协议,不必在目标机本地跑)。工具被拦是"执行落地"问题,换落地点即可,别和 EDR 死磕。

### 从已控主机窃取现存证书与私钥(THEFT 类)
**Q1**: 我拿下一台主机,想不申请新证书、直接偷用户/机器已有的证书,怎么做?
**A1**: THEFT 路线,不触发 CA 签发日志。来源:①用户/机器证书存储(`certutil -store My`、mimikatz `crypto::certificates /export`)导出带私钥的 pfx;②DPAPI 保护的私钥(在 `%APPDATA%\Microsoft\Crypto` 与 masterkey,用 SharpDPAPI/mimikatz 解);③机器账户证书在 SYSTEM 上下文导出。偷到能客户端认证的证书即可 PKINIT 冒充该主体。
**Q2**: 私钥标记为不可导出?
**A2**: 用 mimikatz `crypto::capi` / `crypto::cng` patch CryptoAPI/CNG,把不可导出私钥变可导出后再 export。这需要证书在本机且你有足够权限(用户级证书用用户上下文,机器证书需 SYSTEM)。
**Q3**: 证书存在但 DPAPI masterkey 解不开?
**A3**: 用户 DPAPI masterkey 需要用户口令/hash 或 domain backup key。转向:用 DCSync 拿到的 domain DPAPI backup key 解任意用户 masterkey;或直接在用户活跃会话里以其上下文导出免解密。
**Q4**: 这台机器上根本没有有价值的证书怎么办?
**A4**: 转向别的主机(找有管理员登录过、或部署了证书认证应用的机器),或放弃 THEFT 改走主动申请(ESC1 等)。THEFT 依赖"目标机上恰好有值钱证书",没有就换机器或换路线。
**Q5**: 偷来的证书快过期了?
**A5**: 用它趁有效期内立刻 PKINIT 换 TGT/解 NT hash,把短命证书转成更持久的凭据形态(hash 可 PtH、TGT 可续),或用得到的权限再申一张长期证书。证书本身过期不影响你已换出的 hash。

### 收尾:证书攻击链的清理与交付
**Q1**: 我用证书路拿到了域控,交战收尾该处理什么?
**A1**: 清点你制造的证物:申请的所有证书(记 request id/序列号)、临时改过的模板配置(是否已还原)、加过的 CA officer 角色、造的机器账户、写过的 KeyCredentialLink/RBCD 属性。逐一还原或列入报告供防守方吊销/清除。
**Q2**: 哪些必须还原、哪些留给蓝队?
**A2**: 破坏性/持久性的(改的模板、加的 officer、造的机器账户、shadow credential 条目)应还原以免留隐患;发出去的证书列清单请对方吊销。红队交付要让防守方能完整关闭你打开的每一扇门。
**Q3**: 报告里证书路怎么写才有价值?
**A3**: 写清具体是哪个 ESC、前置配置错在哪(如某模板同时开了自填 SAN 和客户端认证 EKU 且对 Domain Users 开放)、修复建议(收紧模板 ACL/关自填 SAN/打强映射补丁/开审批/关 CA Web 端点或上 EPA)。给可落地的修复而非只证明能打。
**Q4**: 如果客户问"我们怎么防住整类 ESC"?
**A4**: 给纵深建议:定期用 Certipy/PSPKIAudit 自查全部 ESC、最小化模板权限与 EKU、启用 Manager Approval、强制强证书映射(KB5014754 enforcement)、CA Web/RPC 端点上 EPA 或关闭、监控 4886/4887 与 SAN 不一致、限制 MachineAccountQuota、保护 CA 私钥(HSM)。这是把整条选路树从根上收窄。
