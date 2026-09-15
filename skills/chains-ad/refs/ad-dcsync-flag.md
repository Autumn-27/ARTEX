# AD内网 · DCSync与拿flag收尾

### 手里有一个域账户,先判断它能不能直接DCSync
**Q1**: 我通过某种方式拿到了一个域用户的凭据(明文/hash/票据),此刻想直奔DCSync,第一步该确认什么?
**A1**: 不要盲目发起DCSync。DCSync本质是伪装成DC调用DRSUAPI的GetNCChanges,成功前提是该主体在域根对象上拥有`DS-Replication-Get-Changes`+`DS-Replication-Get-Changes-All`扩展权限。先用PowerView的`Get-DomainObjectAcl -Identity "DC=..." -ResolveGUIDs`过滤这两个GUID,看当前主体或其所在组是否在ACL里。判据:命中即可直接同步;不命中就得先提权或换主体。
**Q2**: 我不确定这个账户到底属于哪些组,怎么快速判断它的复制权限来源?
**A2**: 用`whoami /groups`看令牌里的组SID,再对照域内高权限组(Domain Admins/Enterprise Admins/Administrators/DC组以及自定义委派组)。注意组嵌套:很多环境把复制权限委派给了不起眼的自定义组,用BloodHound的DCSync边或递归展开memberof。
**Q3**: 权限不够时直接跑会怎样?
**A3**: mimikatz的`lsadump::dcsync`会返回`0x00002105 (ERROR_DS_DRA_ACCESS_DENIED)`。看到这个错误立刻停手,别反复重试触发4662告警。
**Q4**: 这条路(当前账户直接同步)被权限墙挡住了,往哪个正交维度转?
**A4**: 转向"如何获得复制权限"而不是继续磨这个账户。三条独立支线:(1)把账户提权到DA;(2)找一个已拥有复制权限的账户接管(委派组成员、备份账户、Azure AD Connect的MSOL账户常自带复制权);(3)用ACL攻击给自己加权限(域对象上的WriteDacl/GenericAll→自授Replication权限)。
**Q5**: 用ACL攻击自授权,判据是什么?
**A5**: 若当前主体对`DC=domain,DC=com`有WriteDacl或GenericAll,可用`Add-DomainObjectAcl -TargetIdentity "DC=..." -PrincipalIdentity <me> -Rights DCSync`临时授予,同步完立刻Remove还原以减痕。判据:WriteDacl在域根对象上=离DCSync只差一步。

### DCSync到底同步谁,别一上来就全量dump
**Q1**: 已确认有复制权限,现在要跑,该同步整个域还是单个账户?
**A1**: 先定点同步而非全量。全量`/all`会遍历成千上万对象、产生大量DRSUAPI流量和4662事件,极易触发SOC。先只同步`krbtgt`(为金票)和`Administrator`(为域控落地),这两个是收尾的核心。语法:`lsadump::dcsync /domain:X /user:krbtgt`。
**Q2**: 我为什么优先要krbtgt而不是直接抓DA?
**A2**: krbtgt的NTLM hash是金票(Golden Ticket)的签发密钥,拿到它=可离线伪造任意用户任意时长的TGT,等于对整个域的持久化控制,不依赖某个具体DA账户是否改密。这是"收尾"的战略资产。
**Q3**: 同步单个用户成功了,但我还想批量拿所有用户hash做横向,怎么权衡?
**A3**: 用`/all /csv`一次性导出但接受更高告警风险;或者只挑目标高价值账户(服务账户、其他管理员)逐个同步。权衡判据:比赛/实战若已接近拿flag,单点同步够用就别全量;若需大范围横向再考虑批量。
**Q4**: 同步时报错说找不到用户或域名解析失败,怎么办?
**A4**: DCSync需要能解析并连到DC。确认`/domain`用的是FQDN、DNS指向DC、RPC(135+高端口)可达。若跨域同步,`/user`可能要写`DOMAIN\user`。
**Q5**: 如果DRSUAPI流量被网络层封了/端口不通,这条路怎么转?
**A5**: 转向"在DC上本地取材"这个正交维度:落地到DC后直接`lsadump::lsa /inject`或`sekurlsa`、抓`NTDS.dit`+SYSTEM离线解析、或做卷影拷贝。DCSync是远程手法,被网络封时改走"人已经在DC上"的本地路线。

### 只有NTLM hash,没明文,怎么走到DCSync
**Q1**: 我拿到的是一个高权限账户的NTLM hash而非明文,能发起DCSync吗?
**A1**: 能。DCSync走的是Kerberos/RPC认证,不需要明文。用`sekurlsa::pth`起一个带该hash的进程,或用impacket的`secretsdump.py -hashes LM:NT domain/user@DC`直接远程DCSync。判据:有hash即可PtH到DCSync,无需破解出明文。
**Q2**: impacket的secretsdump和mimikatz的dcsync选哪个?
**A2**: 从Linux/跨平台跳板或没落地Windows时用impacket的secretsdump(纯远程、无需上传二进制);已在Windows且要配合PtH起进程做后续操作时用mimikatz。judged by你当前的立足点在哪个OS。
**Q3**: PtH起进程后dcsync仍拒绝,可能哪里错了?
**A3**: 检查:(1)hash对应的账户是否真有复制权限;(2)是否被NTLM限制/`Protected Users`组导致hash不可用;(3)时间偏差导致Kerberos失败。逐项排除。
**Q4**: 目标账户在Protected Users组或强制AES,PtH被墙了,往哪转?
**A4**: 转向"用Kerberos密钥而非NTLM"这个正交维度:抓该账户的AES256密钥做`ptt`(overpass-the-hash),或直接抓TGT票据注入。NTLM被禁时Kerberos路线往往仍通。
**Q5**: 如果连Kerberos也过不去(强双因素/智能卡),还有出路吗?
**A5**: 转向"用机器账户或委派"的正交维度。机器账户`机器名$`常不受Protected Users限制,若有对应RBCD/委派可用s4u链拿TGS。或者放弃这个账户,用其他有复制权限的账户重来。

### 拿到krbtgt后,金票怎么落到"拿flag"上
**Q1**: 我已经DCSync出krbtgt的hash,下一步为了拿flag该怎么用?
**A1**: 别为了金票而金票。先问flag在哪:如果flag在某台特定服务器/DC的文件里,金票只是"以任意身份访问任意服务"的手段。用`kerberos::golden`伪造一张Administrator的TGT,注入后直接`dir \\目标\c$`或PsExec过去读flag。金票是通行证,不是终点。
**Q2**: 造金票需要哪些参数,漏了会怎样?
**A2**: 需要krbtgt的NTLM(或AES)、域SID、域名、伪造的用户名/RID。域SID用`whoami /user`取当前域SID去掉末段RID,或`Get-DomainSID`。漏SID或写错会导致票据无效但不报错、访问时才失败,难排查,务必核对。
**Q3**: 金票造好注入了,访问目标却还是拒绝?
**A3**: 排查:(1)目标是否在同域(跨域要用跨域信任票或Enterprise Admin的SID History);(2)是否有防护检测异常票据(超长有效期是特征,建议设成合理时长如10小时而非默认10年);(3)注入是否到了正确会话`kerberos::ptt`后用`klist`确认。
**Q4**: 如果这台机器有EDR专门检测金票(异常TGT特征),这条路被墙怎么转?
**A4**: 转向"用真实凭据而非伪造票据"这个正交维度:既然已有krbtgt能DCSync,那也能DCSync出目标机器本地管理员或域管的真实hash,用真hash做PtH横向,不触发金票检测。或退一步用钻石票(Diamond Ticket,改真实TGT而非凭空造)规避特征。

### flag可能不在DC上——先定位flag再决定要不要DCSync
**Q1**: 我快到DA了,但停下来想:拿flag真的需要DCSync吗?
**A1**: 反向思考。DCSync是"控域"级手段,成本和告警都高。先确认flag的位置和读取所需权限:flag可能在一台普通业务机的web目录、某用户桌面、数据库里。如果一个本地管理员或某业务账户就能读到,根本不必惊动DC。判据:所需最小权限 < 域管时,别用DCSync这把牛刀。
**Q2**: 怎么快速判断flag在哪台机器?
**A2**: 从题面/业务逻辑倒推(flag常在"皇冠资产":核心DB、文件服务器、DC、特定标注的靶机)。技术上用`Find-InterestingDomainShareFile`扫共享里的flag.txt/*.flag、翻web根目录、查数据库。先扫再打。
**Q3**: 我扫了一圈没找到明显flag文件,是不是就得上DC?
**A3**: 不一定。flag可能:(1)在需要更高权限才能列举的目录;(2)在注册表/环境变量/计划任务;(3)是"证明控域"本身(题目要求提交krbtgt hash或DA权限截图)。若是第三种,那DCSync/控域才是真目标。先读题意判断"flag=文件"还是"flag=控制权证明"。
**Q4**: 如果目标机在DC上但DC我进不去,这条正面路堵了怎么转?
**A4**: 转向"用域权限间接读DC上的文件"这个正交维度:有DA/复制权限后,不必交互登录DC——可用WMI/WinRM/SMB远程读文件、用`reg`远程读注册表、或DCSync出DC本地管理员hash后SMB连C$。把"登录DC拿flag"降级成"远程读DC上那个文件"。

### secretsdump跑完一堆hash,怎么从里面找到能拿flag的那把钥匙
**Q1**: 我DCSync/secretsdump导出了全域几百个账户hash,面对一大堆hash该先看谁?
**A1**: 别逐个爆破。优先级排序:(1)`Administrator`和内置域管;(2)`krbtgt`;(3)名字带admin/svc/backup/sql/exchange的服务账户;(4)目标业务机的机器账户(`机器名$`)。先按"离flag最近的资产的管理者"排序,而非从上往下扫。
**Q2**: 我怎么知道哪个账户能登进放flag的那台机?
**A2**: 用BloodHound导入后查"到目标机的最短路径"或`Find-LocalAdminAccess`确认哪些账户对目标机有本地管理员权限。有权限的那把hash就是钥匙,其余暂时无关。
**Q3**: hash拿到了但目标机不接受PtH(禁用NTLM或本地账户过滤)?
**A3**: 本地账户远程PtH可能被`LocalAccountTokenFilterPolicy`或UAC远程限制挡住。改用域账户hash,或走Kerberos(overpass-the-hash用AES密钥),或用机器账户hash做`s4u`。
**Q4**: 所有hash对目标机都没本地管理员权限,横向被墙,怎么转?
**A4**: 转向"改变目标机的访问控制"这个正交维度:既然已控域,用GPO给自己加本地管理员、用RBCD(基于资源的约束委派)配置对目标机的委派后s4u拿票、或改目标机所属OU的GPO。控域后横向不该被单机ACL挡住,换成"从域层面下推权限"。

### 落地DC后不用mimikatz,还有哪些抓hash的路
**Q1**: 我已经是DC上的管理员会话,但EDR盯着mimikatz,怎么抓域hash?
**A1**: 不必非用mimikatz。四条常用绕道:(1)`ntdsutil "ac i ntds" "ifm" "create full C:\Temp\a" q q`一条命令生成NTDS.dit+SYSTEM+SECURITY;(2)`vssadmin create shadow`卷影拷贝后从影子设备复制`NTDS.dit`;(3)`reg save HKLM\SYSTEM`+`reg save HKLM\SECURITY`+`reg save HKLM\SAM`拿本地;(4)diskshadow脚本自动化卷影。落到跳板后用impacket `secretsdump.py -ntds NTDS.dit -system SYSTEM LOCAL`离线解析。
**Q2**: ntdsutil和vssadmin都是Windows自带,为什么EDR还可能拦?
**A2**: 现代EDR对`ntdsutil ac i ntds`命令行字符串直接匹配。可用diskshadow脚本(把命令写文件里`diskshadow /s script.txt`)或PowerShell反射调用`WMI Win32_ShadowCopy.Create`绕字符串匹配。判据:选那个"命令行特征最不敏感"的路径。
**Q3**: 我不想在DC本地落文件留痕,能纯远程抓吗?
**A3**: 能。impacket `secretsdump.py -just-dc-ntlm DOMAIN/user@DC`纯远程DCSync;或`wmiexec`+`vssadmin`触发卷影后SMB拉走(还是有落地)。彻底不落地就用DCSync。
**Q4**: NTDS.dit拿到但解析失败/hash出不来,怎么办?
**A4**: 常见坑:SYSTEM hive和NTDS.dit必须是同一时刻的一致快照;从错时间点混用会解不出。重取一次一致快照。或用`impacket-secretsdump -ntds ... -system ... -history`看是否密钥历史问题。
**Q5**: 如果DC上磁盘只读/写不了`C:\Temp`,ntdsutil失败怎么转?
**A5**: 转向"内存里读活的LSASS"或"远程DCSync"这两个正交路径。内存路线用`Rubeus`/`SafetyKatz`等LSASS转储工具但要过EDR;远程路线不落DC盘。或者写到另一个可写目录(如DC上某个共享),避开系统盘配额。

### DCSync被EDR拦截了,绕过的决策树
**Q1**: 我发起dcsync一瞬间就被EDR杀掉/进程终止,怎么办?
**A1**: 先分清是"命令行/进程名/签名被杀"还是"DRSUAPI调用行为被拦"。前者换实现(用impacket、用自写dcsync代码、用C#工具)就行;后者需要更隐蔽的调用方式或改用其他抓hash路径。判据:换一个工具再试,若同样时机被杀→行为检测;若能跑但结果异常→网络/权限问题。
**Q2**: 换工具的具体优先级?
**A2**: (1)impacket的`secretsdump.py -just-dc`纯Python,不容易被静态特征命中;(2)C#实现的DCSync工具(SharpKatz、DSInternals)编译后AMSI/AV签名较新;(3)自己调用DRSUAPI的Rust/C二进制。判据:哪个"没在EDR黑名单里"就先试哪个。
**Q3**: 如果所有工具的DRSUAPI调用都被行为检测拦(异常IP发起复制)?
**A3**: 转向"从合法源头发起"的思路:入侵一台已存在的DC或AzureADConnect服务器,从它的IP发起复制看起来像正常同步。或者不用DCSync,改用ntdsutil/卷影本地抓。
**Q4**: 时间紧,来不及绕过,这条被墙的路彻底放弃往哪转?
**A4**: 转向"不走DCSync的收尾路径":(1)已有DA就直接落地DC本地抓;(2)找flag的具体位置而非域控级凭据;(3)用Kerberoasting/AS-REP拿服务账户,若服务账户在目标机有权限就够了。DCSync只是路径之一,不是唯一。

### DC不出网/无法连通,怎么做DCSync
**Q1**: 我在跳板上跑secretsdump连DC,连接失败或超时,如何诊断?
**A1**: DCSync走RPC(TCP 135 + 动态高端口)+ SMB(445)。先`nc -zv DC 135 445`确认端口,再看是否需要经过跳板SOCKS代理。常见问题:(1)网段隔离,DC只对内网内某VLAN开放;(2)防火墙只放行DC之间的复制流量。
**Q2**: 端口通了但DCSync仍失败?
**A2**: 检查代理:impacket需要`proxychains`或socks的正确配置,RPC动态端口很多代理不透传全端口。改用`ntlmrelayx`+`--socks`保持长连接,或者在DMZ的某台已控主机上直接跑secretsdump,不隔一层。
**Q3**: 只能从外网跳板,而DC严格只对内网复制端口开放?
**A3**: 转向"控制内网一台机器再从它发起"的模式。用Cobalt Strike/sliver在内网机器上建beacon,从beacon里跑DCSync(工具已经在内网,不需要外网穿透RPC全端口)。
**Q4**: 内网也没有可用跳板,只有一个DMZ的webshell?
**A4**: 转向"tunnel+proxy"正交维度:用reGeorg/frp/ligolo建SOCKS通道,然后在通道里跑secretsdump。这条链的关键是让工具的所有RPC动态端口都能透传,ligolo/wg隧道优于HTTP转发。
**Q5**: 如果连隧道都建不起来(DMZ出网严格)?
**A5**: 彻底转向"不做DCSync"。DMZ webshell场景下DCSync代价太大,改先横向到能内网通DC的机器再决定。或者放弃控域,直接找flag可能存放的DMZ内业务系统。

### Kerberos时钟偏差导致DCSync失败
**Q1**: DCSync报`KRB_AP_ERR_SKEW`或"clock skew too great",什么原因?
**A1**: Kerberos要求客户端和KDC时间差<5分钟(默认)。跳板机时间和DC不同步就败。常发生在跨时区/Linux跳板容器没同步NTP。判据:看到skew/时钟相关错误立刻怀疑时间。
**Q2**: 怎么快速对齐时间?
**A2**: Linux下`sudo ntpdate DC_IP`或`sudo rdate -n DC_IP`;impacket脚本设`-dc-ip`并保证本机时间和DC一致。若无root,`faketime`包装命令行伪造时间。
**Q3**: NTP流量出不去/DC不接受NTP查询,怎么办?
**A3**: 从Kerberos失败信息里的DC时间反推:观察`KRB_AP_ERR_SKEW`错误里有时会带DC时间。或者先请求一个AS-REQ看响应包时间戳。然后用`faketime`微调本地时间。
**Q4**: 时间对齐了但换个工具又skew,怎么彻底解决?
**A4**: 检查容器/WSL时间是否漂移(常见问题:容器不同步宿主NTP)。在shell里`export FAKETIME=...`统一;或用`chrony`保持长期同步。判据:多次尝试都skew=时钟服务本身没跑好。
**Q5**: 若确实无法对齐时间(严格网络),这条路完全走不通怎么转?
**A5**: 转向"用NTLM而非Kerberos"这个正交维度。DCSync能用NTLM认证(secretsdump默认可fallback),避开时钟依赖。或换用不需时间同步的抓hash手段(远程注册表、卷影)。

### BloodHound建图后到DCSync的最短路径决策
**Q1**: 我采集了BloodHound数据,想从当前账户找到DCSync,该查什么查询?
**A1**: 用内置`Shortest Paths to Domain Admins`和`Find Principals with DCSync Rights`。再自定义Cypher查询:`MATCH p=shortestPath((n {name:"当前账户"})-[*1..]->(m:Domain)) WHERE ANY(r IN relationships(p) WHERE type(r) IN ["DCSync","GetChanges","GetChangesAll","GenericAll","WriteDacl","Owns"]) RETURN p`。这告诉你最少几步到DCSync权限。
**Q2**: 图上一大堆路径,怎么选最优的?
**A2**: 优先"边数少+边类型稳定"的路径。稳定边:AddMember到组、GenericAll on user、ForceChangePassword,这些一次操作就成。不稳的边:HasSession(需要用户上线)、CanRDP(需要凭据)、ExecuteDCOM(需要网络+防护弱)。判据:能"离线操作即完成"的边>"要等条件发生"的边。
**Q3**: 图里显示某组是关键中转,但那个组我没看到怎么加入?
**A3**: 检查边类型。AddMember/GenericAll on group意味着我可以把自己加进去。若边是"AddSelf",直接一步加。若是嵌套的委派组,可能需要先控组owner。
**Q4**: BloodHound数据是旧的,现场权限可能已变,数据不可靠怎么办?
**A4**: 重新采集或做小规模验证。用SharpHound `--CollectionMethods DCOnly`快速刷新对象和ACL数据(不需要遍历会话)。判据:图给的是方向,细节还得当场核实。
**Q5**: 图上根本没有从当前主体到DCSync的路径,是不是死路?
**A5**: 不是,转向"图外的攻击面"。BloodHound不包含:CVE(Zerologon/noPac/PrintNightmare)、ADCS ESC(需专门收集)、Coerce+Relay链、密码复用横向。用bloodhound-ce或ADCS collector补数据,或直接考虑CVE路径。图空=换维度。

### Kerberoasting 拿到SPN服务账户能走到DCSync吗
**Q1**: 我普通域用户,能不能通过Kerberoasting拿高权限账户?
**A1**: 有可能。任何域用户可请求域内SPN账户的TGS,离线用hashcat模式13100破解。判据:找`Get-DomainUser -SPN | ?{$_.MemberOf -match "Admin"}`——如果某个服务账户属于高权组且密码弱,一破即到DA。
**Q2**: 破出来的密码能干什么?
**A2**: 拿到明文/hash后:(1)若是DA或复制权限主体,直接DCSync;(2)若在特定机器有本地管理员,先横向再看有没有更高凭据;(3)服务账户常有超长有效期密码没换,复用价值高。
**Q3**: SPN账户密码强破不出来?
**A3**: 别磨。转向:(1)AS-REP roasting看有没有关闭预认证的账户;(2)找其他攻击面。判据:强口令Kerberoasting无解,时间成本换维度更值。
**Q4**: SPN列表里根本没有高权账户,只有低权限服务账户?
**A4**: 转向"低权限服务账户能干什么"。它可能在特定服务器有本地管理员,或有对象ACL(如WriteDacl on some group)间接通向DA。用BloodHound或手工查它的outbound权限。低权账户+ACL链常是意想不到的通路。
**Q5**: 如果域内根本没有可用SPN(或全是MSA/gMSA),这条路彻底堵住怎么转?
**A5**: 转向AS-REP、NTLM Relay、CVE路线(Zerologon/noPac)、或ADCS ESC。Kerberoasting只是"服务账户口令弱"的赌注,赌不上就换牌桌。

### AS-REP Roasting拿低权账户后升到DCSync
**Q1**: 我发现某账户`DONT_REQ_PREAUTH`,一发AS-REQ就返回加密的TGT,能干什么?
**A1**: 拿这份加密数据离线破(hashcat模式18200)。破出明文即拿到该账户凭据。判据:找到关闭预认证的账户=一份可离线破的hash到手,不影响后续认证。
**Q2**: 破出来的账户通常权限不高,怎么用来接近DCSync?
**A2**: 三步:(1)用它登录扩大枚举面(有些ACL/组信息普通匿名读不到);(2)用它跑Kerberoasting/BloodHound;(3)看它在ACL图里的位置,可能是某组member,链上有委派。判据:AS-REP账户价值=多一条枚举入口和ACL起点,不是终点。
**Q3**: 没有账户关闭预认证怎么办?
**A3**: 试`GetNPUsers.py -no-pass -usersfile <name-list>`盲探。域内用户命名规律(名字首字母.姓、或员工工号)常可枚举出账户列表。找不到就换路。
**Q4**: 找到但破不出来密码,怎么转?
**A4**: 转向"用它做username enumeration"作为副产物:能返回TGT本身证明账户存在,构造出确认存在的用户列表用于后续密码喷洒。或彻底换CVE路线。

### NTLM Relay到LDAP授予DCSync权限
**Q1**: 我在内网抓到某高权账户或DC的NTLM认证,能不能直接relay到LDAP做DCSync?
**A1**: 能,但有条件。目标LDAP必须没有强制签名(默认DC强制LDAP签名,但389端口的非签名LDAP和一些老配置可能允许)。用ntlmrelayx `-t ldaps://DC --escalate-user 当前账户`将relay进来的高权认证转成"给当前账户加DCSync ACL"。判据:成功后当前账户即可DCSync。
**Q2**: LDAP有签名怎么办?
**A2**: 转向LDAPS(636/3269),LDAPS默认不受SMB Signing/LDAP Signing策略约束,但需要channel binding关闭。用`-t ldaps://`。判据:LDAPS 636可用=relay可能仍通,先试。
**Q3**: 认证源怎么获得?被动等还是主动强制?
**A3**: 主动强制:PetitPotam/PrinterBug/DFSCoerce强制DC或目标主机对我方SMB发起NTLM认证。被动:mitm6劫持WPAD/IPv6应答让机器主动发认证。判据:实战优先主动强制(PetitPotam走EFSRPC,常仍开)。
**Q4**: relay成功但权限授予后DCSync仍失败?
**A4**: 检查escalate授的到底是什么权限。ntlmrelayx默认授DCSync权限或加进内置admin组。用`Get-DomainObjectAcl "DC=..."`确认自己新加的ACE生效。可能需等LDAP复制传播,或选另一台DC试。
**Q5**: 全域强制LDAP签名+channel binding,relay路完全堵怎么转?
**A5**: 转向"relay到ADCS Web Enrollment"(ESC8):把coerce来的机器账户认证relay到`/certsrv`签一张机器账户证书,然后用证书PKINIT拿TGT走S4U2Self拿DA。或转向coerce+利用其他配置漏洞的路径。

### PetitPotam/DFSCoerce强制DC认证的选型
**Q1**: 我要coerce一台机器发起认证,几种手法(SpoolSample/PetitPotam/DFSCoerce/ShadowCoerce)怎么选?
**A1**: 按"哪个服务默认开+补丁难打"排序:(1)DFSCoerce(MS-DFSNM)——默认开,难关;(2)PetitPotam(EFSRPC)——补丁覆盖后仍有其他RPC接口可用;(3)SpoolSample(Print Spooler)——PrintNightmare后很多环境禁了打印后台;(4)ShadowCoerce(FSRVP)——依赖File Server VSS服务。判据:先DFSCoerce,失败降级PetitPotam,再SpoolSample。
**Q2**: coerce到底强制的是谁的机器账户?
**A2**: 强制谁,谁就以SYSTEM身份对我方发SMB/NTLM认证,拿到的是"目标机器$"的hash或relay它的认证。coerce DC=拿DC$的机器账户认证=relay到ADCS/LDAP可拿DA。
**Q3**: coerce发出去了但我方没收到认证?
**A3**: 排查:(1)防火墙/网段是否可达我方IP的445;(2)UNC路径是不是走了WebDAV(需要WebClient服务开);(3)有些coerce要求触发者能解析我方hostname,用IP直接触发。判据:coerce工具输出"success"不代表认证真到,tcpdump抓包看有无入站445。
**Q4**: coerce被打补丁挡了(全套接口都补了),怎么转?
**A4**: 转向"不需要coerce的relay":mitm6的IPv6劫持让机器主动认证(不依赖RPC接口);或者放弃relay,转向Kerberos delegation/ADCS ESC1模板滥用等其他DA路径。

### ADCS ESC1: 弱模板一步到DA
**Q1**: 我发现域内有CA,能不能通过证书路走到DCSync?
**A1**: 可以。用Certify/certipy枚举模板,找`ESC1`特征:模板允许"客户端认证"+"Enrollee可提供SAN"+"低权用户可申请"。命中即用当前账户申请一张SAN=Administrator的证书,PKINIT登录即DA。
**Q2**: certipy find/Certify.exe find输出一堆模板,怎么快速识别ESC1?
**A2**: 看这四条并列:(1)`Client Authentication`或`Any Purpose`EKU;(2)`ENROLLEE_SUPPLIES_SUBJECT`标志;(3)Enroll rights包含当前用户或其组;(4)管理员审批未启用。四条全中即ESC1。
**Q3**: 拿到证书后怎么变成DCSync?
**A3**: PKINIT认证:`certipy auth -pfx admin.pfx`拿到TGT+NT hash(UnPAC-the-Hash)。再用hash做DCSync,或直接用TGT访问DC资源。
**Q4**: ESC1模板存在但当前账户没enroll权限?
**A4**: 转向其他ESC:ESC2(Any Purpose EKU)、ESC3(登记代理)、ESC4(可写模板ACL)、ESC6(EDITF_ATTRIBUTESUBJECTALTNAME2)、ESC7(CA管理员)、ESC8(HTTP端点relay)、ESC9/ESC10/ESC11。判据:ADCS共十几个ESC,一个不通就下一个,别死磕。
**Q5**: 域内根本没ADCS怎么转?
**A5**: 转向非ADCS路径。ADCS是"如果有就非常香"的加分项,不是必需路径。回到Kerberoast/AS-REP/ACL/Coerce+Relay/CVE的标准菜单。

### ADCS ESC8: coerce+relay到web enrollment
**Q1**: LDAP relay被强签名挡了,能不能relay到ADCS Web Enrollment?
**A1**: 能。CA的`/certsrv`默认NTLM认证,常年不打签名/channel binding。用`ntlmrelayx -t http://CA/certsrv/certfnsh.asp --template DomainController`把coerce来的机器认证relay过去,签一张目标机器账户证书。判据:ADCS Web Enrollment HTTP端点开+coerce能触发认证=ESC8可用。
**Q2**: 签什么模板?
**A2**: 签`DomainController`或`Machine`模板(默认所有域机器可申请),给DC$签一张。拿到证书后PKINIT拿DC$的TGT+hash,DC$对自身有复制权=直接DCSync。
**Q3**: relay到`/certsrv`失败,报401?
**A3**: 检查:(1)certsrv是否禁用了NTLM;(2)是否要HTTPS(试`--adcs`选项);(3)coerce的目标机器账户是否被排除。或换端点:`/certsrv/certfnsh.asp` vs `/certsrv/certnew.asp`。
**Q4**: ADCS Web端点没开(纯DCOM enrollment)?
**A4**: 转向ESC11(如果启用了DCOM enrollment上的relay)或纯ESC1/ESC4等不需要web的路径。或找其他能签发的入口(NDES/SCEP)。

### 影子凭据 (Shadow Credentials / msDS-KeyCredentialLink)
**Q1**: 我对某高权对象有GenericWrite,不用ForceChangePassword的话有更隐蔽的路吗?
**A1**: 有:Shadow Credentials。往目标的`msDS-KeyCredentialLink`属性里写一个我们控制的Key Trust凭据(证书公钥),然后用私钥PKINIT登录该账户拿TGT+hash。工具:pywhisker/Whisker+Rubeus。相比改密码不影响业务、目标用户完全无感。
**Q2**: 影子凭据的前提条件?
**A2**: (1)域功能级别≥2016;(2)域内有KDC支持PKINIT(几乎所有现代域);(3)有对目标对象的`GenericAll`/`GenericWrite`/`WriteProperty on msDS-KeyCredentialLink`。判据:三条都满足→比强改密更隐蔽的首选。
**Q3**: 写入后PKINIT失败?
**A3**: 检查:(1)证书是否正确关联到KeyCredentialLink条目;(2)时钟同步;(3)目标是否在Protected Users(会限制Kerberos)。
**Q4**: 目标是Protected Users或不满足域功能级别,影子路走不通?
**A4**: 转向"强改密码+抓TGT+改回"的老路(RBCD攻击对computer对象也可以),或RBCD路径。判据:影子凭据不通=用其他ACL利用手法,不是死路。

### RBCD (基于资源的约束委派) 从计算机对象接管
**Q1**: 我对目标计算机对象有GenericWrite或`WriteProperty on msDS-AllowedToActOnBehalfOfOtherIdentity`,能干什么?
**A1**: RBCD:把一个我可控的机器账户(自建或已控)加到目标的`msDS-AllowedToActOnBehalfOfOtherIdentity`,然后从可控机器账户对目标做S4U2Self+S4U2Proxy拿一张假装是DA的服务票据。工具:impacket rbcd.py + getST.py。等效于本地管理员登录目标。
**Q2**: 我怎么"造一个可控机器账户"?
**A2**: 域内普通用户默认能加10个机器到域(MachineAccountQuota=10)。用impacket `addcomputer.py`加一个新机器,拿它的密码/hash作为S4U发起方。判据:MAQ>0=一步造机器。
**Q3**: MAQ被改成0怎么办?
**A3**: 换任何"已知密码/hash的机器账户"或"有ServicePrincipalName的普通账户"作为发起方。或者利用已控的其他计算机对象。
**Q4**: 拿到DA的S4U票据,能DCSync吗?
**A4**: 该票据只针对特定service,通常是CIFS/HTTP等。可直接SMB/WinRM登录目标机拿本地,再从目标机DCSync(如果目标是DC)。或直接对DC做RBCD拿DC的DA票→通吃。
**Q5**: 若既没GenericWrite也无法加机器,RBCD路彻底堵住,怎么转?
**A5**: 转向"改现有委派配置"或"影子凭据"或"传统ACL链"。RBCD只是ACL利用之一,不通就换。

### ACL自授DCSync权限后的时序、痕迹与踩坑
**Q1**: 我已按ACL攻击授了自己DCSync权限,操作顺序上要注意什么才最不留痕?
**A1**: 顺序是"授权→立刻dcsync→立刻Remove还原"。三个步骤之间不做任何多余动作。ACL修改会写事件5136,单个"授权+还原"配对不必然引人怀疑,但如果中间夹了大量其他行为、时间线太长,SOC回溯时容易被拎出来。判据:授权和撤销之间的时间窗越窄越隐蔽。
**Q2**: 授权成功但DCSync立刻拒绝?
**A2**: ACL可能还未复制到发起DCSync的那台DC。等30秒到几分钟或显式指定"同一台我刚授权时用的DC"作为同步源。或者选另一台已同步的DC。判据:多DC环境下ACL传播有滞后,别在同步刚失败时怀疑权限本身。
**Q3**: 修改域根ACL直接被拒?
**A3**: 排查:(1)会话认证过期(重新pth/kinit);(2)在RODC上没写权限(切主DC);(3)AdminSDHolder保护周期内、SDPROP刚跑过导致目标属于受保护组时权限被回滚。判据:同一命令换主DC/换认证会话再试,能定位是"我"还是"这台DC"的问题。
**Q4**: 授DCSync后能读所有hash吗还是仅限某些账户?
**A4**: `GetChanges`+`GetChangesAll`两条一起=能同步所有属性包括密码hash。若只加了`GetChanges`没加`GetChangesAll`,能同步元数据但hash字段被过滤,dcsync报错但看起来像权限不足。判据:两条ACE缺一不可,别只授一半。
**Q5**: 这条ACL路径完全走不通(WriteDACL/GenericAll都没有,或都被拒),怎么转?
**A5**: 转向"改而不是授"的正交维度:(1)对目标身份对象(某高权用户/组)有WriteProperty的话,改它的属性(如member加自己进DA组、或改msDS-KeyCredentialLink做影子凭据);(2)对某OU有GenericAll,改OU下GPO反过来推权限;(3)彻底放弃ACL链,走委派/证书/CVE。判据:域根ACL不是唯一入口,身份对象/组/OU/GPO都是并行的授权面。

### Backup Operators 组的隐藏特权
**Q1**: 当前账户在Backup Operators组,离DA还有多远?
**A1**: 很近。Backup Operators在DC上有`SeBackupPrivilege`+`SeRestorePrivilege`,可绕过ACL读取任何文件包括`NTDS.dit`+SYSTEM。落到DC后用`robocopy /B`或wbadmin做备份,拉走NTDS.dit离线解hash。判据:Backup Operators = 事实上的DCSync等价物,只是路径是"离线NTDS"。
**Q2**: 但Backup Operators不能远程登录DC,怎么落地?
**A2**: 有几种:(1)`wbadmin start backup`可远程触发备份任务落文件到共享;(2)通过Task Scheduler远程创建任务以Backup Operators身份跑;(3)`reg save`远程调用(需要SeBackupPrivilege+远程注册表服务)。判据:选目标机上"接受这个组远程操作"的接口。
**Q3**: DC上远程注册表和任务计划都关了?
**A3**: 转向"用Backup Operators对其他关键资产做备份"。Backup Operators可能对文件服务器也有权,能读到其他敏感文件(包括GPP密码文件、备份包里的凭据)。判据:Backup Operators权限=读遍任何ACL屏蔽的文件,用它读flag可能比控域更快。
**Q4**: 若备份权限被审计严格监控,怎么转?
**A4**: 转向"用备份权限抓其他凭据然后普通登录"。备份C:\Windows\System32\config\SAM+SYSTEM拿本地账户,备份LSA secrets,备份任务计划保存的凭据文件。或读注册表里保存的自动登录凭据。

### 拿到DA后怎么隐蔽地登陆目标机拿flag
**Q1**: 我拿到DA,目标机器上有flag,该怎么执行读取?WMI/WinRM/SMB/RDP选哪个?
**A1**: 按告警等级排序:(1)WMI(wmiexec/`Invoke-WmiMethod`)行为常见,日志4688+网络日志;(2)WinRM(evil-winrm/PSSession)——最"官方"的远程管理,如果环境本就用WinRM则融于日常;(3)SMB远程C$读文件(不exec,只读文件)——最静;(4)PsExec——落driver+服务,最响;(5)RDP交互——鼠标键盘痕迹重。判据:只读flag文件优先SMB `\\目标\C$\...`,不exec就不启告警链。
**Q2**: flag文件所在目录ACL拒绝了我(即使DA)?
**A2**: DA默认对NTFS大部分ACL有绕过权,但可能被"拒绝ACE"显式挡。加`SeTakeOwnershipPrivilege`夺所有权、或用`SeBackupPrivilege`绕ACL(icacls+PS或`Set-Acl`前先takeown)。判据:DA遇拒不是终点,特权提权后可读。
**Q3**: 目标机有EFS加密或BitLocker,读到文件是密文?
**A3**: 若EFS加密,以该用户/DA身份登录目标(而非纯远程SMB)让EFS解密。若BitLocker,一般机器运行时透明解密不影响读,除非离线场景。
**Q4**: 若目标机对DA远程连接被网络ACL拦(如某VLAN),这条路怎么转?
**A4**: 转向"用GPO推一个从目标机反向连回来的任务"这个正交维度。DA可改GPO,给目标机推一个计划任务/启动脚本,反向连接我方——不需要我到目标机的入站连接。或用WMI事件订阅在目标机上部署。
**Q5**: 目标机没在域内(独立机)?
**A5**: DA权限无效,换本地凭据。若从域账户抓到过它的本地管理员hash或密码(共用密码/LAPS外泄/RDP凭据抓取),直接PtH。否则找它的其他攻击面(web/服务/物理)。

### 落到目标机后怎么快速找flag
**Q1**: 我登录/远程到了目标机,flag路径未知,先找哪里?
**A1**: 按业务型别扫:(1)`C:\Users\<所有用户>\Desktop|Documents|Downloads`——CTF/HW常见;(2)`C:\`根目录+`C:\Flag*`;(3)Web根`C:\inetpub\wwwroot`、`C:\phpstudy`等;(4)数据库文件夹;(5)`%USERPROFILE%\AppData`里的浏览器书签/邮件缓存。命令:`dir C:\ /s /b | findstr /i "flag"`。
**Q2**: 全盘找flag太慢,有更聪明的过滤?
**A2**: `dir /a-d /o-d /t:w /s C:\Users\*.txt`按最后写时间排;或找最近被打开的文件`Get-ChildItem -Recurse | Sort-Object LastAccessTime`。假设flag被布置者近期创建,时间戳是最好过滤器。
**Q3**: flag可能不是文件而是环境变量/注册表?
**A3**: 试`set`看环境变量、`reg query HKLM /f flag /s /d`全注册表搜、`schtasks /query /fo LIST /v | findstr /i flag`看计划任务、`Get-ItemProperty HKLM:\SOFTWARE\...`扫软件配置。
**Q4**: 全找了都没有,是不是位置错了?
**A4**: 转向"读题意/业务逻辑"。flag可能:(1)在另一台机器的服务上,当前机只是跳板;(2)是"合成的凭据",需从注册表+浏览器凭据拼出;(3)在Docker容器/WSL子系统里(找`docker.exe`、`wsl`)。判据:全盘扫无果=定位错了,回头看题意。

### 域文件共享和SYSVOL里翻凭据/flag
**Q1**: 我不想马上打DA,先在域共享里扫可能有价值的东西,该扫什么?
**A1**: 三大目标:(1)SYSVOL共享(所有域用户可读)里的Group Policy Preferences XML里可能有加密的cpassword;(2)全域共享的`Find-InterestingDomainShareFile`(PowerView),关键词flag/password/pass/secret/backup/*.kdbx;(3)常见管理员遗留:`\\server\share\backup\*`。
**Q2**: GPP cpassword怎么解密?
**A2**: `Get-GPPPassword`(PowerView)或`gpp-decrypt`(Kali)。AES密钥是微软公开的,拿到cpassword即明文。判据:2014年前旧域常留cpassword,新域已修补但历史备份可能仍在。
**Q3**: 共享太多扫不完?
**A3**: 用`ShareFinder`列出全部共享,再用`Invoke-ShareFinder`+过滤"非默认+可读"缩小范围。默认C$/ADMIN$/IPC$跳过,重点看命名奇怪的共享(如"Backup"、"IT"、"Public")。
**Q4**: 共享读权限被过度收紧,普通用户啥都读不到怎么转?
**A4**: 转向"用SPN/ADCS/CVE路径直接提权"跳过共享扫描。共享空=SysAdmin做了收紧,但可能其他攻击面(如ADCS配置)反倒忘了收。

### LAPS密码读取路径
**Q1**: 域内启用了LAPS,我怎么读到目标机的本地管理员密码?
**A1**: LAPS密码存在计算机对象的`ms-Mcs-AdmPwd`属性,读取权限通常授给IT组。查ACL:`Get-DomainObjectAcl -SearchBase "OU=..." -ResolveGUIDs | ?{$_.ObjectAceType -like "*ms-Mcs-AdmPwd*"}`。判据:找到读权限主体后接管它=拿全域本地管理员密码。
**Q2**: 当前账户没有ms-Mcs-AdmPwd读权限?
**A2**: 三种转向:(1)找有读权限的账户走ACL链接管;(2)用某台机器的机器账户(它对自己的AdmPwd有读权限)——如果我控了这台机;(3)Windows LAPS(新版)存到不同属性`msLAPS-Password`,单独检查。
**Q3**: 拿到本地管理员密码后怎么用于DCSync?
**A3**: 本地管理员本身不能DCSync。但用它登录目标机后,可抓LSASS里的域凭据、DPAPI等。判据:LAPS密码是横向工具,不是直接的DCSync钥匙,是链条的一环。
**Q4**: LAPS密码字段是空的?
**A4**: 该机可能未启用LAPS/密码未轮换/机器脱域。转向读非LAPS机器的本地凭据(SAM),或找其他信息源。

### DPAPI主密钥解密浏览器/RDP凭据找flag线索
**Q1**: 我在一台用户机上拿到会话,DPAPI里可能有什么有用东西?
**A1**: DPAPI保护:(1)浏览器保存的密码/cookie(Chrome/Edge);(2)RDP连接文件里的加密密码(`*.rdg`、`Credential Manager`);(3)Wi-Fi密码;(4)VPN密码;(5)一些应用自定义凭据。目标账户可能用某处凭据登录了藏flag的资产。
**Q2**: DPAPI怎么解密?
**A2**: 当前用户会话下`sekurlsa::dpapi`+`dpapi::masterkey`用当前用户上下文自动解。离线:抓走`%APPDATA%\Microsoft\Protect\<SID>\`下的masterkey文件+用户密码/hash,mimikatz `dpapi::masterkey /in:... /sid:... /password:...`离线解。
**Q3**: 用户密码不知道?
**A3**: 用域备份密钥(域自动备份所有用户DPAPI主密钥到DC)。DA可用`lsadump::backupkeys`导出域DPAPI备份RSA私钥,一密解所有用户所有主密钥。判据:控域=控全域DPAPI。
**Q4**: 若目标用户从没在本机登录过(纯服务账户机),DPAPI里没东西?
**A4**: 转向"用户机"而不是"服务器"找DPAPI。开发者/管理员的日常机器才是DPAPI宝库。或转向数据库/邮箱等业务系统。

### 无约束委派主机接管
**Q1**: BloodHound显示某普通服务器开了无约束委派(TrustedForDelegation),能做什么?
**A1**: 无约束委派机器:任何用户认证到它时,它会拿到该用户的可转发TGT并存在内存。若能让DA/DC$认证过来,就抓到DA的TGT做DCSync/横向。判据:无约束委派机+可强制DC认证(coerce)=金链条。
**Q2**: 怎么让DC认证过来?
**A2**: 从已控的委派机上跑SpoolSample/PetitPotam指向DC$,让DC对我方SMB发认证。这个认证以Kerberos形式回到委派机,委派机内存里就有DC$的TGT。用mimikatz `sekurlsa::tickets /export`导出。
**Q3**: 拿到DC$的TGT后?
**A3**: DC$对自己有复制权→用DC$ TGT直接dcsync。或renew成TGT做后续操作。
**Q4**: 无约束委派机上没admin,进不去?
**A4**: 转向"先横向到该委派机"作为子目标。或换RBCD/约束委派路径。判据:无约束委派是宝但需要机器上SYSTEM/admin,不能到手就放弃。
**Q5**: 域内没有无约束委派机怎么转?
**A5**: 转向约束委派S4U链、RBCD、ADCS ESC等。无约束委派是2016+推荐禁用的历史特性,现代域可能没有。

### 约束委派S4U链拿DA
**Q1**: 某服务账户配了`msDS-AllowedToDelegateTo`(约束委派),我控了它,怎么用?
**A1**: S4U2Self+S4U2Proxy:以该服务账户身份,请求一张伪造"任意用户"作为客户端访问指定后端服务(委派目标)的TGS。用impacket getST.py或Rubeus s4u。若委派目标包含CIFS/DC,即可以"任意用户"身份读DC文件。
**Q2**: 委派目标只包含某台机器的CIFS,能扩到其他服务吗?
**A2**: 可以。约束委派配置若没勾选"仅Kerberos",允许协议转换(S4U2Self不需目标用户密码),而委派service限制是"目标机+服务类型",但许多环境写全了machine没写具体service→整机的所有SPN都可target。
**Q3**: 约束委派目标不是DC,拿不到DC上的东西?
**A3**: 转向"横向到委派目标机,再从那台机器上寻找二次委派或凭据"。委派链是逐段跳的,每一段都可能拿到新的DA相关凭据。
**Q4**: 目标账户是`Sensitive`或Protected Users,委派对它无效?
**A4**: 该保护会让S4U2Self失败对`Administrator`等特权账户。改target一个非敏感高权账户(如某个自定义DA用户没设sensitive标记),或转向影子凭据/RBCD等其他路径。

### Zerologon (CVE-2020-1472) 判断和利用
**Q1**: 我看到域控OS版本较老,想试Zerologon,先怎么判据?
**A1**: 用`zerologon_tester.py`或Nmap脚本探测。原理:利用Netlogon AES-CFB8的IV为0导致平均256次尝试即可把DC机器账户密码置空。判据:2020年8月安装了补丁的DC免疫,老补丁级别DC可能中招。
**Q2**: 利用成功会怎样,直接就有DCSync?
**A2**: 把DC$密码在AD中置空,然后用空密码以DC$身份发起DCSync(DC$对自身有复制权)。一步到krbtgt。
**Q3**: 打完必须恢复DC$密码,否则会怎样?
**A3**: DC与其他DC复制失败、域服务不稳定,SOC必查。恢复:用DCSync出的DC$原hash通过`Reset-ComputerMachinePassword`或直接RPC写回原hash。判据:Zerologon利用+恢复是配套动作,不恢复会炸域。
**Q4**: Zerologon被补了怎么办?
**A4**: 转向noPac/PetitPotam+ADCS/ADCS ESC等其他一击致命CVE。或走标准ACL/委派路径。判据:CVE路径是彩票,补了就换,别磨。

### noPac (sAMAccountName spoofing CVE-2021-42287/42278)
**Q1**: 补丁级别看着像2021年秋前的DC,可以试noPac吗?
**A1**: 可以。前提:任何域用户+MAQ>0允许加机器。原理:加一个机器账户后改sAMAccountName为DC名(去掉$),用它请求TGT,再改回自己,S4U2Self以DC身份拿TGT for DA。判据:补丁前2021-11以前,一发即到DA。
**Q2**: MAQ=0或不能加机器怎么办?
**A2**: 用任何已知密码的已存在机器账户/computer object,只要能改它的sAMAccountName。或找SPN账户改name(需要相应写权限)。
**Q3**: 打上去TGS_REQ被拒绝?
**A3**: 补丁已上或已启用KDC签名保护。转向其他CVE或标准路径。
**Q4**: 成功拿到DA票据后怎么变DCSync?
**A4**: 直接以DA身份dcsync krbtgt做长期持久化。或抓其他高权账户hash。

### 拿了flag后的痕迹和收尾
**Q1**: 拿到flag后,是不是就完事了?
**A1**: 比赛/评估场景可能还需:(1)保留证明(截图/文件hash);(2)清理关键痕迹以免影响后续测试(如临时授的DCSync ACL、加入的admin组、金票文件);(3)记录攻击路径以便报告。判据:拿flag=技术目标达成,但流程还有收尾。
**Q2**: 清理该到什么程度?
**A2**: 至少还原:(1)自己加的域对象ACL(用Remove-DomainObjectAcl);(2)加进的组(Remove-DomainGroupMember);(3)自建机器账户/证书(Remove-ADObject或revoke证书);(4)mimikatz/工具落地文件`del`。不做全量日志抹除(反倒引发更强告警)。
**Q3**: 如果比赛要求"隐蔽度评分",金票和骨票要不要保留?
**A3**: 金票和DSRM密码等持久化后门在评估结束前保留有价值(应对DA密码轮换),但要清理生成过程中的落地文件(krbtgt.kirbi等)。判据:后门可留,痕迹要清。
**Q4**: 如果时间紧来不及清理,怎么优先?
**A4**: 优先清"高判分痕迹":(1)非默认ACE(容易人工审计发现);(2)非默认组成员;(3)大文件落盘;(4)mimikatz进程。日志类的4624/4662/5136不用清,反倒会引发比对告警。

### 跨林/跨域信任拿flag
**Q1**: 我控了子域DA,但flag在父域或另一林,怎么办?
**A1**: 判断信任类型:(1)子父域:自动双向可传递,直接用子域DA的信任密钥`sekurlsa::trust`造跨域金票(以Enterprise Admins的SID History身份)到父域;(2)林间信任:默认过滤SID history,难跨,需找漏洞。判据:同林子域=一步跨,跨林=复杂。
**Q2**: 造跨域金票的关键参数?
**A2**: 需要子父域间的信任密钥(`sekurlsa::trust`导出`Inter-Realm Key`),`kerberos::golden`加`/sids:S-1-5-21-...-519`(父域Enterprise Admins的SID)。判据:SID history是跨域权限传播的钥匙。
**Q3**: 跨林信任,SID filtering开着无法用SID history怎么转?
**A3**: 转向"找信任本身的漏洞":跨林金票(CVE-2020-0665类)、TrustAccount$凭据滥用、或从父林用户已委派对子林机器的权限反过来打。或走"控信任双方共同管理的资产"迂回。
**Q4**: 完全不同域没有信任怎么转?
**A4**: 转向纯网络层攻击面。不同域=不同凭据体系,只能从服务/CVE/凭据复用打进去,和AD无关。判据:无信任=不是AD问题,是外网/横向问题。

### DCSync完成但抓的hash用不了(密钥错了)
**Q1**: 我DCSync出`krbtgt`,做金票时机器说无效,可能什么原因?
**A1**: 常见原因:(1)krbtgt在最近被轮换过,拿到的其实是历史hash("krbtgt password history"),需要看NTLM history的第一个;(2)域用AES强制,NTLM金票被拒,该用AES256密钥造票;(3)域名或SID写错。判据:金票签发不报错但注入后访问被拒→hash对应关系错。
**Q2**: 怎么确认拿到的是当前有效的krbtgt hash?
**A2**: 看secretsdump输出的"pwdLastSet"或者对比不同时刻两次同步是否一致。若两次不一样,说明期间发生轮换。或在DC上跑`Get-ADUser krbtgt -Properties PasswordLastSet`看密码何时改的。
**Q3**: 拿到的是AES key不是NTLM怎么造票?
**A3**: mimikatz金票支持AES:`kerberos::golden /aes256:<key>`替代`/rc4:<ntlm>`。现代域禁用RC4后必须用AES。
**Q4**: 若目标环境完全禁RC4且拿不到AES(工具没dump出),怎么转?
**A4**: 重新同步指定包含Kerberos keys(`secretsdump -just-dc-user krbtgt`默认含所有key)。或者放弃金票走"用DA真实hash做PtT/PtH"路径。

### 走完全程后,如何为下一次DCSync做持久化
**Q1**: 我拿了flag,但环境还要用,想留个能反复DCSync的入口,怎么做最省事?
**A1**: 三档持久化:(1)金票(krbtgt hash在手,可任意身份任意时长,最"高维",但检测手法多);(2)自建域账户+加DCSync ACL(容易被审计发现);(3)AdminSDHolder ACL修改(每小时刷回所有受保护对象,影响面广,极隐蔽但极高危)。判据:靶场用金票,实战偏向AdminSDHolder。
**Q2**: 金票有效期怎么设合理?
**A2**: 默认10年是检测特征。改成10小时或与域最长票据策略一致(通常8-10小时)。判据:混在正常票据流量里。
**Q3**: 若krbtgt被主动轮换(双次),金票失效,怎么补救?
**A3**: 若还有原DCSync能力(某账户仍有复制权),重新同步一次拿新hash。若丢了复制权,回到"从当前立足点重新提权到DA"的老路。判据:金票持久化不是永久,要备份路径。
**Q4**: 蓝队响应能力强,任何持久化都会被清,是不是彻底放弃?
**A4**: 转向"信息类持久化"而非"权限类":抓一份全域NTDS hash+GPP密码+DPAPI备份到本地,即便被踢也可根据这些凭据在未来的密码轮换周期外重入。判据:凭据是最难吊销的持久化。
