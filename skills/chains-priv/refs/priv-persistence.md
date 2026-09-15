# 提权立足点 · 持久化与稳定性取舍

### 拿到低权 shell 先立足还是先冲提权
**Q1**: 反弹 shell 刚回来、身份是普通用户或服务账号,手很痒想直接跑提权 exp,该不该?
**A1**: 不该。第一步永远是【固定立足点 + 快照式枚举】。理由:一次失败的提权(尤其内核 exp)可能崩会话、可能触发 EDR,把唯一入口烧掉。先把 whoami/id/hostname/出网方向/EDR 存在性/可写目录搞清楚,再动手。
**Q2**: "立足点固定"的最低达标标准是什么?
**A2**: 判据:主动 kill 当前 shell,30 秒内能从另一条独立路径回到同等身份。手段可以是:用户级自启回连、独立 web 后门、复用凭据从另一服务重进。做不到这个,就是单点故障,不该继续冒险。
**Q3**: 目标根本不出网怎么固定?
**A3**: 把"锚"从"我连出去"翻成"内网中转"。转向:找一台已控且和它双向可达的机器做跳板,回连指内网跳板;或本机起仅内网可达的监听让跳板拉。稳定性从"依赖出网"改为"依赖已掌控的内网拓扑"。
**Q4**: 提权前的信息收集要做到什么程度?
**A4**: 一次性把所有提权判据抓齐(内核版本、SUID、sudo -l、cron、可写服务、capability、明文凭据痕迹),结果落到操作端。避免"提权失败 shell 断了、还得重新一遍枚举"。
**Q5**: 什么时候可以打破"先立足"、直接冲提权?
**A5**: 当持久化本身需要更高权限(写系统服务、改 /etc)时。折中:先做纯用户态兜底(用户 crontab、bashrc、用户级 systemd),再去追系统级。
**Q6**: 什么信号说明该放弃本机、把它当纯跳板?
**A6**: 机器价值低(DMZ 边缘、无凭据、无纵深)且提权面很干净时不要恋战。立足点价值 = 权限 × 可达性,权限提不动就榨可达性。

### 枚举顺序:低风险高收益优先
**Q1**: 内核、SUID、sudo、cron、凭据、capability 一堆判据,按什么顺序收集?
**A1**: 【零风险可能秒杀】的先看:sudo -l、SUID/SGID 异常、可写的 root cron/systemd unit、bash_history/config/env 里的明文凭据。命中即提权,不崩机、不告警。内核 exp 放最后。
**Q2**: 大而全的枚举脚本要不要一把甩上去?
**A2**: 分场景。老环境无 EDR,直接跑省时间;有 EDR/HIDS 时,海量文件读取 + 敏感路径访问本身就是强告警。此时改手动精准收集,一次只查一两类判据。
**Q3**: 收集完"啥都没有",是真没有还是漏了?
**A3**: "干净"往往是视野不够。补盲区:非标 SUID(自研程序)、root 运行的第三方服务及其配置目录 ACL、非直观定时(/etc/cron.d、systemd timer、anacron、logrotate 的 postrotate)、容器特权/挂载、其他用户 home 凭据。换视角:【从进程反查权限边界】。
**Q4**: 判据是什么时候算枚举够了?
**A4**: 能列出至少 3 条独立提权候选路径、按【成功率×动静×可恢复性】排完序。只有一条候选就是没查够。
**Q5**: 敏感结果怎么存最安全?
**A5**: 不在目标落地,一律回带操作端;不得已落地就登记清单、撤退前清干净。落地本身就是 DFIR 的告警点。

### Linux SUID 二进制的筛选与利用
**Q1**: find / -perm -4000 出来一堆,怎么筛?
**A1**: 三类分开:① 系统标品(passwd/su/mount)—— 除非有已知本地 CVE 一般跳过;② GTFOBins 名单里的(find/vim/less/awk/nmap 等)—— 查对应姿势;③ 【非标/自研】—— 最高优先,常见坑:未指定绝对路径调外部命令、可劫持 LD_PRELOAD、参数注入。
**Q2**: 自研 SUID 二进制怎么快速判洞?
**A2**: strings 看是否 system()/popen();ltrace/strace 看它 execve 了什么;ldd 看依赖库是否可写。如果它 popen("curl ...") 而 PATH 里我有可写目录,PATH 劫持就赢。
**Q3**: SUID 二进制只有【任意读】能力怎么办?
**A3**: 目标从"提权"改为"用它读 root 才能读的东西"—— shadow、/root/.ssh、systemd unit 里的密码字段。拿到凭据后 su/横向,一样等价提权。
**Q4**: SUID 一条不通、转哪里?
**A4**: 【三个正交维度】—— getcap -r / 找 cap_setuid/cap_dac_read_search;sudo -l 看免密条目;id 看 docker/lxd/disk/shadow 组。这三者和 SUID 不重叠,一条断另外三条大概率活。
**Q5**: 什么时候放弃 SUID 路线?
**A5**: 15 分钟没发现明显利用点就走。SUID 是"要么很快找到、要么根本没有",不值得死磕。

### sudo -l 有条目的利用决策
**Q1**: sudo -l 有几条 NOPASSWD,先看哪个?
**A1**: 优先级:① (ALL) NOPASSWD: ALL 白送;② 允许解释器/编辑器/带 shell 逃逸的(vim/less/awk/python/perl/git 等)查 GTFOBins;③ 允许自研脚本 —— 看脚本能否命令注入、能否写它读的配置。
**Q2**: 条目指向 root 拥有 644 的脚本,不可写,还有戏?
**A2**: 三条正交:① 脚本调外部命令是否走绝对路径 —— PATH 劫持;② 脚本读环境变量吗 —— sudo env_keep 保留的 env 注入;③ 脚本读【我能写的】配置文件吗 —— 改配置触发命令执行。
**Q3**: sudo 版本很老要不要打 sudo 本身的 CVE?
**A3**: 可以但放最后。sudo/内核这种系统组件的 exp 是重炮,失败大概率被 auditd/EDR 抓,放低成本路线之后。
**Q4**: 条目是 -u lowuser 而非 root,还有价值吗?
**A4**: 【中转跳板】—— 跳到 lowuser 重新做 sudo -l/组/home 枚举。侧向到另一身份常常打开完全不同的攻击面(比如 lowuser 是 db 账号能读明文库密码)。
**Q5**: 完全没条目、sudo 不能免密怎么办?
**A5**: 【转凭据路线】—— 目标改成拿到 root 或其他用户的密码,而不是绕 sudo。bash_history、.mysql_history、last、进程 cmdline、swap dump、备份文件是常见泄漏点。

### 内核 exp 的稳定性权衡
**Q1**: 前面路都断了、只剩内核 exp,该不该开?
**A1**: 四问:① 内核版本与 exp 精确匹配吗;② 是否生产核心机器(蓝屏 = 事故);③ 有 EDR/HIDS 检测 LPE 吗;④ 有回撤路径吗(exp 崩了 shell 是否还在)。四个里任一"是",都要再想想。
**Q2**: 怎么判 exp 稳不稳?
**A2**: 看作者 + issue 区 + "unstable/PoC only" 标记 + 本地起同版本内核跑一遍。绝不在目标"试试看"—— 真机就是一次机会。
**Q3**: exp 要编译但目标没 gcc?
**A3**: 【本地静态编译】—— 同版本内核头 + musl 静态,单文件二进制。上传到 /dev/shm 或 /tmp,chmod +x 后立刻 rm 保留内存 fd。或者找纯 shell/python 的 PoC。
**Q4**: exp 打完 shell 断了怎么办?
**A4**: 打之前就要有【至少两条独立回连】—— 一条 shell + 一条独立后门或 SSH key。exp 后必崩的场景更要提前准备。
**Q5**: 内核路彻底不能走(核心生产、太旧太不稳、有 HIDS),转哪?
**A5**: 【凭据 + 横向】—— 不追求本机提权,改为在本权限下能读到什么、能连到哪里。或者【容器逃逸/hypervisor 侧信道】完全绕开内核 LPE。

### cron/systemd timer 提权的判据
**Q1**: 发现 root 的 cron 跑一个脚本,怎么判能否利用?
**A1**: 三问:① 脚本本身我能写吗;② 脚本所在目录我能写吗(能就替换/覆盖);③ 脚本调的外部命令/配置/包含的其他脚本,我能影响任何一个吗?任一"能",就有路。
**Q2**: 脚本是绝对路径 644 不可写,还有戏?
**A2**: 看内容 —— source /etc/xxx?读环境变量?通配符展开(tar czf /backup/*.log 通配符注入经典)?相对路径调外部命令?这些都是【不改脚本本身】就能注入的口子。
**Q3**: 没找到明显 cron 弱点,还该看什么?
**A3**: 【非 cron 定时】—— systemd timer(systemctl list-timers)、anacron、at 队列、应用自带 scheduler(Jenkins、Airflow)、logrotate postrotate。cron 只是最显眼一种。
**Q4**: cron 是别的用户不是 root 也值得打?
**A4**: 值得。提权 = 【任何一次权限跃升】,不一定一步到 root。跳到那个用户重新枚举,可能它的 sudo/组/文件权限直接送下一步。
**Q5**: 完全没有可利用定时任务怎么转?
**A5**: 【长驻服务】—— 定时的本质是"root 周期性执行代码",服务是"root 常驻执行代码"。ps -ef | grep root 找进程,它们的配置、日志、pid、socket,任何一个能写就有戏。

### PATH 劫持与相对路径提权
**Q1**: 什么时候 PATH 劫持能奏效?
**A1**: 两个条件同时:① 有【会以高权限运行】的程序/脚本;② 它调外部命令时没写绝对路径。宿主常见:SUID 二进制、cron 脚本、sudo 白名单脚本、systemd unit 的 ExecStart。
**Q2**: 怎么验证一个二进制会不会中招?
**A2**: strace -f -e execve 跑一遍,看 execve 第一个参数是不是绝对路径。相对路径就有戏,写同名恶意脚本放 PATH 前面(比如 /tmp),export PATH=/tmp:$PATH,触发。
**Q3**: SUID 二进制的 PATH 劫持要注意什么?
**A3**: SUID 会 sanitize LD_* 但 PATH 常保留。注意:SUID 下 shell 脚本(#!/bin/sh)现代系统会降权或拒绝,用二进制包装或直接改成 execve 无 shell。
**Q4**: PATH 里没可写目录怎么办?
**A4**: 【转目标进程的 PATH】—— 别改我自己的,读 /proc/<pid>/environ 拿 root 进程的 PATH,想办法影响它(systemd Environment=、启动脚本)。
**Q5**: 目标脚本用了绝对路径怎么办?
**A5**: 【其他相对性】—— LD_LIBRARY_PATH 加载 so、python sys.path 加载 module、node require 查找、bash source 相对路径。同类"路径劫持"机会。

### 组权限提权(docker/lxd/disk 等)
**Q1**: id 显示我在 docker 组怎么打?
**A1**: docker 组 = root。特权容器把宿主 / mount 进去:docker run -v /:/mnt --rm -it alpine chroot /mnt。判据:能连 docker.sock 就 100%。
**Q2**: lxd/lxc 组呢?
**A2**: 类似 —— 起 privileged container,把宿主根挂进去。init 镜像 + config raw.idmap + security.privileged=true + mount root。
**Q3**: disk 组?
**A3**: 直接读写块设备。debugfs 或 tune2fs 绕过所有权限:改 /etc/shadow、加 SUID shell、种 SSH key,想干什么干什么。
**Q4**: shadow 组?
**A4**: 能读 /etc/shadow。目标从"提权"改为"离线爆破 root",hashcat 跑一晚,拿到密码 = 拿到 root。
**Q5**: adm、systemd-journal、sudo 空条目这种非常见组?
**A5**: adm/journal 能读大量日志 —— 里面常有密码/token 泄漏;sudo 组但空条目 —— 找 sudo CVE 或社工触发。先查这个组能读写哪些具体路径,再从路径反推能干什么。
**Q6**: 完全不在有用组怎么办?
**A6**: 【转组加入】—— 如果我能触发某个 root 脚本(比如 useradd/usermod 封装),把自己加进 docker/sudo。以有限能力换更强的组权限。

### Windows 低权初勘顺序
**Q1**: 拿到 Windows 低权 shell 先跑什么?
**A1**: whoami /all(重点看【权限列表】)、systeminfo(hotfix)、netstat -ano、tasklist /v(用户 + EDR)、net user、net localgroup administrators、reg query HKLM\Software\Policies。顺序:身份 → 补丁 → 网络 → 进程 → 用户组 → 组策略。
**Q2**: whoami /priv 里看到啥眼睛一亮?
**A2**: SeImpersonate/SeAssignPrimaryToken(Potato 家族,基本白送 SYSTEM);SeBackup/SeRestore(读 SAM 离线);SeDebug(读任意进程内存);SeLoadDriver(装恶意驱动)。这些是 Windows 的"糖"。
**Q3**: 组信息里看什么值得留意?
**A3**: Backup Operators、Server Operators、Print Operators、Account Operators —— AD 里这些内置组权限极强,常被当"低权"忽略;自定义组要看它对 OU/GPO/ACL 的权限。
**Q4**: 补丁全新、EDR 强怎么办?
**A4**: 【转错配】—— 服务弱 ACL、未引号服务路径、AlwaysInstallElevated、计划任务弱 ACL、Autorun 可写。不是漏洞是配置,EDR 一般不管。
**Q5**: 全干净怎么办?
**A5**: 【转凭据】—— 目标改为从这台机器抓到任何一个其他用户凭据(浏览器保存、cmdkey、DPAPI blob、.rdp 文件、config 里的连接串)。这个别人可能就是本地管理员。

### SeImpersonate 与 Potato 家族选型
**Q1**: 有 SeImpersonatePrivilege,用哪个 Potato?
**A1**: 看 OS 版本 + 补丁:老系统(2016 前)Juicy/Hot;新系统(2019/2022、Win10/11 打了补丁)RoguePotato/PrintSpoofer/GodPotato/EfsPotato。判据:exp 作者的适用范围 + 本地同版本靶机验证。
**Q2**: web/db 服务账号带 SeImpersonate 是常态吗?
**A2**: 是。这就是为什么 web/db 打进去大概率能 SYSTEM —— 微软给这些服务账号默认带。反过来,如果 web/db shell 没有 SeImpersonate,说明降权了,得走别的路。
**Q3**: Potato 打完新 shell 怎么稳?
**A3**: 拿到 SYSTEM 后立刻:抓 SAM/LSASS + 种独立持久化(服务/计划任务)+ 退出 Potato 进程。别停在 Potato 打出的 shell 里干活 —— 它依附父进程很脆。
**Q4**: Potato 被 EDR 拦怎么办?
**A4**: 换实现:PrintSpoofer 走打印、EfsPotato 走 EFS RPC、GodPotato 走不同 COM。EDR 常常只识别一种实现。或【转不依赖 impersonation 的路】—— 服务 ACL 错配。
**Q5**: 没有 SeImpersonate 怎么办?
**A5**: 【转服务错配 + 未打补丁 LPE】。SeImpersonate 是最爽的路,没有就老老实实找 CVE(PrintNightmare、SpoolFool、CLFS、AFD 系列)或错配。

### 未引号服务路径与服务 ACL 错配
**Q1**: 未引号服务路径怎么找?
**A1**: wmic service get name,pathname,startmode | findstr /i "auto",看 PathName 里【有空格但没引号】的。经典:C:\Program Files\Foo Bar\svc.exe 无引号,系统先试 C:\Program.exe、C:\Program Files\Foo.exe。任一中间路径可写就赢。
**Q2**: 服务有引号还能打吗?
**A2**: 看服务 ACL(sc sdshow / accesschk)。有 SERVICE_CHANGE_CONFIG 就 sc config binpath= 改成我的 exe;对 exe 文件本身有写权限一样。
**Q3**: exe 不可写、ACL 不能改还有戏?
**A3**: 看它加载的 DLL —— DLL 劫持是另一大类。procmon 跑一下看 CreateFile 失败但会 fallback 的路径,放同名 DLL。
**Q4**: 要重启服务但没权限重启?
**A4**: 三种触发:① 等系统重启(最慢);② 服务恢复策略(某些服务崩了自动重启,写好文件后杀当前进程);③ 找【会调用该服务】的低权可触发接口(spooler 可通过打印请求触发)。
**Q5**: 全部服务都干净怎么办?
**A5**: 【转计划任务同套路】—— 任务 ACL、任务里的脚本/exe 权限、任务 XML 权限。schtasks + accesschk 过一遍,原理和服务提权几乎一样。

### AlwaysInstallElevated 决策
**Q1**: 怎么快速验证 AlwaysInstallElevated 是否开?
**A1**: reg query HKCU\SOFTWARE\Policies\Microsoft\Windows\Installer /v AlwaysInstallElevated 和 HKLM 同名键。两个都 1 = 白送,msiexec /quiet /qn /i evil.msi 直接 SYSTEM。
**Q2**: 只有一边是 1、另一边没有,还能用吗?
**A2**: 必须【HKLM + HKCU 同时为 1】。但 HKCU 是我自己的,可以自己 reg add!所以【HKLM=1 且 HKCU 没设 = 白送】。
**Q3**: MSI 打进去被 EDR 杀了怎么办?
**A3**: 换 payload 生成方式:msfvenom 的 MSI 特征太强,自己用 WiX 写一个只调 CustomAction 起 cmd 的;或让 MSI 只做一件事 —— 加用户到 admins 组,不起 shell。
**Q4**: 打完后 MSI 留磁盘会不会暴露?
**A4**: 会。Windows Installer 会记 log。取舍:成功后立刻删 MSI + 清 %WINDIR%\Installer 下对应缓存 + 清事件日志(如果 SYSTEM 到手)。
**Q5**: AlwaysInstallElevated 不开怎么办?
**A5**: 【转 GPP cpassword】(如果域内)、或其他高权执行原语 —— DCOM MMC20、Excel 类 COM、任务计划的 UAC bypass 路径。

### Windows 凭据 dump 源的选择
**Q1**: 拿到 SYSTEM 后先抓什么?
**A1**: 优先级:① LSASS(内存里活的 hash、Kerberos ticket、明文 —— 最值钱);② SAM+SYSTEM+SECURITY 三件套(本地 hash + DCC2 + LSA secrets 里的服务账号密码);③ DPAPI master key + blob(浏览器、RDP、Wi-Fi、mstsc)。
**Q2**: LSASS 被 PPL/RunAsPPL 保护怎么办?
**A2**: 三条路:① BYOVD(自带带签名的 vuln driver)关 PPL;② comsvcs.dll MiniDump 或 taskmgr 转储,离线用 pypykatz 解析;③ 放弃 LSASS,只抓 SAM+SECURITY(不需要 LSASS 访问)。
**Q3**: 抓完全是本地账号横向不了怎么办?
**A3**: 【转 DCC2 + LSA secrets + Kerberos ticket】—— 缓存凭据里可能有域账号 DCC2(离线爆);LSA secrets 里的 _SC_ 服务账号密码是明文;ticket 直接 pass-the-ticket。
**Q4**: 所有 hash 都很复杂爆不动怎么办?
**A4**: 【放弃爆破,转 hash 直传】—— NTLM pass-the-hash、AES pass-the-key、TGT pass-the-ticket,不需要明文。仅当目标只接受明文(比如某些 web SSO)才必须爆。
**Q5**: EDR 严防 dump 怎么办?
**A5**: 【转 shadow copy】—— vssadmin create shadow,复制 SAM/SYSTEM 出来,不碰 LSASS。或【拿高权跳去别机器打】—— 反正有高权就能远程抓。

### UAC 绕过要不要打
**Q1**: 已经在本地管理员组但 UAC 中权限,该不该绕?
**A1**: 看目的:① 只要 SYSTEM 那 UAC bypass 只是中间一步,不如直接 Potato/MSI/服务替换;② 要长期驻留 High IL 更稳(装服务、写系统目录都需要),值得绕。
**Q2**: 常见 bypass 路子怎么选?
**A2**: fodhelper/computerdefaults 走注册表劫持,老系统好用;dll mock trusted dir 绕签名;silentcleanup 走计划任务。每个有版本适用范围,先本地打一遍。
**Q3**: 绕不过去(补丁全打、AMSI 死拦)怎么办?
**A3**: 【社工触发】—— 弹假 UAC 让用户点、hook 用户主动开的高权程序。无人值守机器就【转凭据】—— 拿到明文后 runas /user:admin 得 High IL,不用 bypass。
**Q4**: bypass 成功但 EDR 报警怎么办?
**A4**: 大多数 UAC bypass 有 IOC(注册表键、临时文件、进程链)。成功后立刻【清 IOC】:删键值、删临时、清日志。或【选小众 COM handler 劫持】,避开常见规则。
**Q5**: bypass 要不要留痕?
**A5**: 每次都留痕(4688/4104、AMSI log)。想干净就【只用一次拿 SYSTEM,然后靠 SYSTEM 清痕迹】,不要反复触发。

### 数据库为跳板的提权路线
**Q1**: web 打进去,发现 mysql/mssql 高权连接怎么用?
**A1**: 判身份 —— select current_user()/SELECT SYSTEM_USER。root/sa 就直接 OS 执行:mssql 开 xp_cmdshell、mysql 走 UDF(看 secure_file_priv 和 plugin 目录)、postgres 走 COPY FROM PROGRAM 或 CREATE FUNCTION。
**Q2**: sa 但 xp_cmdshell 开不了?
**A2**: 三条备选:① CLR assembly(sp_configure 'clr enabled' 后 CREATE ASSEMBLY 加载 .NET dll);② OLE Automation(sp_OACreate);③ Job Agent(msdb 的 sp_add_job)。
**Q3**: mysql 想上 UDF 但 plugin_dir 不可写?
**A3**: 检查 secure_file_priv(空最松 null 最严)。有值就只能写该目录 —— 目录里的东西是不是被别的服务用?反过来,Windows 上的 mysql db 用户往往是 SYSTEM 或有 SeImpersonate,直接 xp_cmdshell 起就是 SYSTEM。
**Q4**: db 用户是低权应用账号怎么办?
**A4**: 【转数据而不是命令执行】—— 从表里读应用凭据、API key、其他系统密码横向;或读 hash 字段爆破用户密码拿 web 登录态。db 不是只能执行命令。
**Q5**: db 是内网、只能通过 web 层 SQLi 打、回显有限怎么办?
**A5**: 【异步/带外】—— DNS OOB、HTTP OOB、时间盲注分段;或用 SQLi 写 webshell(select into outfile 到 web 可解析目录),把一次性通道升级为持续通道。

### 提权被 EDR 拦截后的转向
**Q1**: 提权 exp 打完 EDR 弹告警了怎么办?
**A1**: 冷静评估:① 告警是否被响应(有分析工具启动?连接异常断?新登录?);② 立足点还在不在(shell 活不代表安全,可能被观察);③ 有无其他独立立足点。
**Q2**: 该继续打还是撤?
**A2**: 判据:唯一立足点 + 蓝队响应强,立刻【降噪 + 转低特征操作】;有备用立足点,可以【用当前身份做完最后一件事(抓一次凭据)然后主动放弃】。
**Q3**: 必须提权、怎么绕 EDR?
**A3**: 换维度:① 不用 exp 用【错配】—— EDR 一般不管 sudo -l/服务 ACL/GPP cpassword;② 换 exp 实现 —— 同 CVE 多 PoC,EDR 常常只识别一种;③ 换 payload 交付 —— shellcode 手工 patch、AV/EDR sandbox evasion。
**Q4**: dump LSASS 被 EDR 检测怎么办?
**A4**: 【转不碰 LSASS 的凭据源】—— shadow copy 拷 SAM/SECURITY、DPAPI blob 离线解、DCSync(需域权限但不碰本机 LSASS)、密码喷洒(不 dump 直接猜)。
**Q5**: 什么时候必须主动放弃立足点?
**A5**: ① 已确认蓝队在响应;② 继续动作会牵连其他立足点;③ 时间成本超过收益。放弃不等于失败 —— 保住其他立足点、保留下次进入,比死守烧掉的点更重要。

### 反弹 shell 的稳定性升级
**Q1**: 拿到 nc 反弹 shell,不能补全、不能 ctrl-c,先升级到什么?
**A1**: 三步 pty:① python -c 'import pty;pty.spawn("/bin/bash")' 或 script /dev/null;② 本地 stty raw -echo、远端 export TERM=xterm、stty rows X columns Y;③ 现在能 ctrl-c、vim、sudo 交互输密码。没 python 就 perl/ruby/expect/socat。
**Q2**: 目标出网只允许 http/dns 怎么办?
**A2**: 【隧道换维度】—— DNS(iodine/dnscat2)、ICMP、HTTP(s) 反代(gost/chisel)、SMB pipe(域内)、走已有应用协议(Redis pub/sub 当命令通道)。别死磕原生 TCP 反连。
**Q3**: shell 频繁断线怎么办?
**A3**: 三应对:① 加 keepalive(socat 天然带;裸 nc 外面套 tmux/screen);② 换协议(TCP 短连接 → mTLS 长连接 + 心跳);③ 双通道冗余(shell + 独立后门)。
**Q4**: 会话稳但延迟高怎么办?
**A4**: 【降交互频次】—— 少 ls、多脚本化。批处理化命令一次跑完输出全拿。或干脆放持久化 beacon,不用一直挂 shell。
**Q5**: 通道彻底断了怎么办?
**A5**: 【被动 C2】—— beacon 主动出连、C2 只应答,断了自动重试;或【存活性极强的介质】—— 公有云对象存储、GitHub gist、社交平台 API 作 dead drop。TCP 反连是最脆,长期驻留不该依赖它。

### 是否留 webshell 的权衡
**Q1**: web 打进去了,该不该立刻留 webshell?
**A1**: 权衡:好处是任何时候能回来,坏处是蓝队应急首先看 web 目录、极易被发现。决策:【分层】—— 一个显眼做诱饵(必要时可弃)、一个隐蔽做备份(藏正常业务文件)、一个终极后手(内存马、不落地)。
**Q2**: 内存马和落地文件怎么选?
**A2**: 短平快 + 隐蔽优先 = 内存马(Java Agent、Filter、Servlet 注入);要重启后还活 + 蓝队查得不细 = 落地。理想:【内存马 + 一个休眠落地】,重启后落地拉起内存马。
**Q3**: webshell 藏在哪不容易被发现?
**A3**: ① 改静态资源(js/css/图片附加代码);② 藏 vendor/第三方库目录(蓝队默认不动);③ 分片(单文件看不出恶意,组合才生效);④ 改配置(log4j 的 config 里塞恶意 pattern)。绝不用 shell.php/cmd.jsp。
**Q4**: 密码怎么设、通信怎么加密?
**A4**: 复杂密码 + 动态密钥(冰蝎/哥斯拉类)避流量特征;或做成【触发式】—— 只有特定 Cookie/UA/header 匹配才响应恶意,平时表现正常,过流量审计。
**Q5**: 决定不留 webshell 靠什么维持?
**A5**: 【系统层持久化】—— cron/systemd/服务/注册表 Run,不依赖 web 目录。或【凭据】—— 拿到 web 服务器 SSH key 或管理员密码,以后从 SSH 进来,不走 web。

### Windows 持久化机制选型
**Q1**: 拿到 admin/SYSTEM 后持久化选什么?
**A1**: 权衡【触发条件、隐蔽性、EDR 感知】:① 计划任务(灵活但显眼);② 服务(常驻但易枚举);③ Run 键(用户登录触发、显眼);④ WMI 事件订阅(隐蔽但 EDR 强检测);⑤ COM 劫持(隐蔽、特定触发);⑥ Winlogon/AppInit_DLLs(旧、EDR 盯);⑦ LSA/authentication package(SYSTEM 级、隐蔽)。
**Q2**: 最难被发现的是哪几种?
**A2**: WMI 事件订阅 + 定制 filter —— 传统工具枚举不出;COM 劫持 —— 藏 HKCU 的 TreatAs/InprocServer32;LSA/authentication package —— SYSTEM 级、常规扫描看不到。但现代 EDR 全部会检测。
**Q3**: 有 EDR 怎么选?
**A3**: 【白利用而非自建】—— 劫持已有合法服务/计划任务(给已存在的服务加恶意 DLL 依赖)不新建;或【投机】—— 靠特定行为触发(Office 加载、COM 加载),不常驻内存。
**Q4**: 持久化对目标可用性影响?
**A4**: 有 —— 服务崩、任务失败、Run 键指向的 exe 找不到都会弹窗告警。载荷【必须自己做异常兜底】,静默失败别弹窗。测试标准:kill 我的进程、断网、重启,持久化路径不能报错。
**Q5**: 全部持久化路都被 EDR 拦怎么办?
**A5**: 【凭据 + 域信任】—— 与其在这台机器留后门,不如把域管凭据、AS-REP key、krbtgt hash 拿走。凭据本身就是最好的持久化。或【域级持久化】—— 黄金票据、AdminSDHolder、GPO、ADCS。

### Linux 持久化机制选型
**Q1**: root 拿到后 Linux 持久化选哪个?
**A1**: 选项:① cron/systemd timer(简单显眼);② systemd unit(常驻、可 socket activation);③ SSH authorized_keys(极简常见);④ bashrc/profile 插桩(用户登录触发);⑤ PAM 模块劫持(所有认证过一遍,极强);⑥ LD_PRELOAD 全局(risky);⑦ init/rc.local(老式);⑧ 内核 rootkit(最强但风险高)。
**Q2**: 隐蔽性排序?
**A2**: PAM 后门 ≈ 内核 rootkit > systemd 高级配置 > SSH key(藏非常规路径)> cron/rc.local > bashrc。但 PAM 和内核 rootkit 一旦被发现基本重装,前中期不建议用重的。
**Q3**: 想低成本高稳定选什么?
**A3**: SSH key + systemd user timer 双保险。SSH key 藏【非默认 authorized_keys 路径】(sshd_config 的 AuthorizedKeysFile 可自定义),运维 grep authorized_keys 找不到。timer 干周期心跳。
**Q4**: 有 auditd/osquery/EDR-for-Linux 怎么办?
**A4**: 【白利用】—— 改已有 systemd unit 的 ExecStartPost 加一条,不新建 unit;或【挂用户空间】—— 用户 crontab、systemd --user、.bashrc,HIDS 常常只监控 /etc、/root。
**Q5**: 持久化被清怎么办?
**A5**: 【多层冗余】—— 至少 3 个不同触发条件(用户登录 + 定时 + 系统事件)。一个被清另外两个还活,而且被清的那个告诉了蓝队"来源",另外两个反而能让我抢先。

### SSH key 持久化的实战细节
**Q1**: 直接往 ~/.ssh/authorized_keys 塞 key,会不会太显眼?
**A1**: 会。蓝队应急脚本必查这文件。三种规避:① 改 sshd_config AuthorizedKeysFile 指自定义路径;② 用 AuthorizedKeysCommand 让 sshd 从我控制的脚本拉 key;③ 借用户已有的 key 文件插入(隐藏在合法 key 中间)。
**Q2**: 用户 shell 是 /sbin/nologin 或 /bin/false 怎么办?
**A2**: SSH 可以 ForceCommand 强制、可以 -N 只做端口转发。或改 /etc/passwd 把 shell 换回来(root 权限时)。或者干脆不图 shell,只要一个能 -D 起 socks 代理的用户身份就够跳板了。
**Q3**: sshd 被限制(只允许密钥、DenyUsers 等)怎么办?
**A3**: 读 sshd_config,匹配它的规则挑符合要求的用户下手(比如 AllowUsers 里的),或【转换协议维度】—— 不走 sshd,自己起一个隐藏端口/knock 触发的后门。
**Q4**: 目标定期扫描 authorized_keys 变更怎么办?
**A4**: 【从 key 改为 CA 证书】—— sshd 支持 TrustedUserCAKeys,签一个证书给自己用,证书有效期短、指纹变化,签名 CA 藏得深就不容易被追。或者【转 PAM 后门】—— 不走 key 走认证模块。
**Q5**: SSH 服务本身被换成堡垒机代理、直连不通了怎么办?
**A5**: 【转堡垒机链路】—— 目标是通过堡垒机的合法路径进入,而不是绕过它。搞堡垒机上的账号凭据、审计脚本插桩、或利用堡垒机的自动登录会话劫持。

### AD 域内持久化选型
**Q1**: 拿到域管后域级持久化选什么?
**A1**: 从"能活多久 vs 有多显眼"排:① krbtgt hash → 黄金票据(能活到 krbtgt 改密两次为止,极强但一旦响应就废);② 目标服务 hash → 白银票据(单机粒度,隐蔽);③ DSRM 密码同步(单 DC 本地登录);④ AdminSDHolder ACL 后门(改一次影响长期);⑤ ADCS 证书持久化(合法证书,极难发现);⑥ Skeleton Key(内存驻留,DC 重启就没)。
**Q2**: 黄金票据要不要立刻用?
**A2**: 别频繁用。抓完 krbtgt 后存下来,只在【真的需要】时开一次。黄金票据的检测点是"不合理的 TGT lifetime + 不合理的用户"。用得越少越难被发现。
**Q3**: AdminSDHolder ACL 后门怎么埋?
**A3**: 给一个我控制的低权账号在 AdminSDHolder 上加 GenericAll/WriteDacl。每小时 SDProp 自动把这条 ACL 复制到所有受保护账号(Domain Admins 等)。之后即使管理员重置密码,我的低权账号仍能重新写他们。
**Q4**: 想最"合法"的持久化选什么?
**A4**: 【ADCS 证书】—— 给自己签一张长期证书,用证书 PKINIT 认证换 TGT。证书是 CA 签的、完全合规,除非专门审 CA log 否则发现不了。
**Q5**: 域管凭据丢了(密码改了、krbtgt 转了)怎么办?
**A5**: 【回归 ACL/GPO 后门】—— AdminSDHolder、GPO Delegation、OU ACL 这些是"权限而非凭据",不受密码轮转影响。这就是为什么打完域管一定要种一个非凭据类的备份。

### ADCS 证书路线(ESC 系列)
**Q1**: 域内看到 CA 服务在跑,先做什么?
**A1**: 用 Certipy/Certify 枚举模板,识别 ESC1-8 的哪几个存在。判据:ESC1(模板允许申请者提供 SAN + 客户端认证 EKU)是最白送的;ESC8(NTLM relay 到 CA web enrollment)动静大但强。
**Q2**: ESC1 能白嫖到什么?
**A2**: 直接申一张 SAN=domain admin@domain 的证书,用它 PKINIT 拿 TGT,拿完就是域管。判据:我当前身份能 enroll 那个模板 + 模板 flag 允许 SAN。
**Q3**: 用作持久化 vs 用作提权,怎么选?
**A3**: 提权:一次性签一张管理员证书用完就废;持久化:签一张【自己控制账号 SAN 为管理员】的证书,长期有效期(默认 1 年到 10 年),下次要用就 PKINIT。
**Q4**: 蓝队发现并撤销证书怎么办?
**A4**: 【多张 + 多账号】—— 别只签一张,给多个身份签多张分批用。CRL 检查慢,新签的证书生效比撤销传播快。或者【转其他 ESC】—— ESC 系列有 8 个,ESC4(模板 ACL 弱)可以直接改模板重签。
**Q5**: 没有 CA 或 CA 严格审计怎么办?
**A5**: 【转其他域持久化】—— AdminSDHolder、GPO、DCSync 权限、krbtgt。ADCS 只是众多手段之一,不是唯一。

### 反检测:beacon 心跳、jitter、流量特征
**Q1**: beacon 心跳设多长?
**A1**: 权衡"响应速度 vs 可探测性"。长驻期(信息收集完等待时机)心跳可以很长(数分钟到几小时),jitter 拉到 30%+;交战期(要操作)缩短到秒级。默认几秒的心跳在流量分析里是明显信标。
**Q2**: jitter 为什么重要?
**A2**: 固定周期心跳会在流量图上形成规律脉冲,IDS/NDR 有专门检测这个的算法。jitter(比如 ±50%)打乱周期。加上随机 payload 大小填充,就更像正常业务流量。
**Q3**: 流量特征怎么避?
**A3**: ① 走合规协议(https + 合规 TLS 指纹);② 走 CDN/知名云域名(malleable profile);③ 内容加密 + padding;④ 频率控制在业务基线内。核心是【看起来像目标环境本来就有的流量】。
**Q4**: 流量已经被识别了怎么办?
**A4**: 【切换通道 + 切换域名/IP】。预先准备多条备用通道:一条 https、一条 DNS、一条 SMB(域内)、一条 email/cloud storage dead drop。任何一条被封切下一条,别死磕。
**Q5**: 完全静默的通信怎么做?
**A5**: 【转 dead drop】—— 不主动通信,读写一个公共介质(GitHub gist、云对象存储的 metadata、某个论坛帖子),我和 beacon 都定时去读写。没有直接连接、没有反向通道,极难在网络流量里定位。

### 多 C2 冗余部署
**Q1**: 为什么要多 C2?
**A1**: 单 C2 = 单点故障。C2 服务器被打掉、域名被封、IP 被拉黑,所有 beacon 一起死。多 C2 保证一条挂了自动切下一条。
**Q2**: 多 C2 怎么设计?
**A2**: 分层:① primary(每天用)高质量域名 + 前置基础设施;② secondary(每周心跳)完全独立域名/IP;③ tertiary(dead drop,月级)极稀疏心跳,只用于 primary/secondary 全挂时找回。三层之间【不共享任何基础设施】,不然一起挂。
**Q3**: 一个 beacon 支持几个 C2 才够?
**A3**: 最少 2 个不同信道 + 不同域名。理想 3 个,分别是 https / DNS / dead drop。每个信道用不同 TLS 指纹、不同 UA、不同心跳节奏,避免关联。
**Q4**: 切换策略怎么设?
**A4**: 主动 + 被动组合:① 心跳 N 次失败自动切;② C2 主动下发切换指令;③ 时间片轮换(每小时切一次,反检测)。别只靠"失败自动切",蓝队可以做流量降级让 beacon 反复重连暴露基础设施。
**Q5**: 所有 C2 一起挂怎么办?
**A5**: 【dead drop + 兜底重连】—— beacon 内置最后手段:去一个公共平台(GitHub、Pastebin、某社交平台)拉配置。这个"配置源"是最重要的复活锚,要藏得极深、极稀疏访问。

### 持久化被发现的应急响应
**Q1**: 突然发现有个持久化被删/被隔离怎么办?
**A1**: 冷静评估:① 只是被删还是蓝队在追(查其他持久化点、有没有异常登录、有没有 EDR 部署时间戳);② 我的其他立足点是否也暴露(它们有关联特征吗)。
**Q2**: 该立刻放弃这台机器还是继续?
**A2**: 判据:如果被删的持久化和其他立足点【共用基础设施】(同 C2、同域名、同凭据),预设它们全都危险,立刻切基础设施;如果完全独立,可以继续观察。
**Q3**: 是不是要主动清痕迹?
**A3**: 不要。这时清痕迹反而是【二次告警】—— 蓝队正在看这台机器,你一动他就知道你还在。宁可放弃,让机器"看起来干净地被清完了",从其他立足点继续。
**Q4**: 蓝队在 hunt 但还没定位到我怎么办?
**A4**: 【降到最低活动】—— beacon 心跳拉长到小时级、停止一切主动操作、把 C2 切到 dead drop。等他们放松警惕(一般 1-2 周)再评估。
**Q5**: 全部立足点都在响应范围内怎么办?
**A5**: 【战略撤退,保留复活手段】—— 主动放弃所有主动通信的立足点,只留 dead drop 类的极稀疏后门。凭据类的("我知道 krbtgt hash")无法被应急抹掉,是最后的复活手段。

### 何时故意"不提权"
**Q1**: 什么场景该故意保低权、不提权?
**A1**: ① 目标可能是蜜罐 —— 提权触发是典型诱饵检测手段,低权持续潜伏更安全;② EDR 极强 —— 低权时它不管,提权时全监控;③ 目标是拿数据而非拿权 —— 数据在低权可读的地方,提权是不必要的暴露。
**Q2**: 怎么判断可能是蜜罐?
**A2**: 蛛丝马迹:① 明明是普通服务器却主动"送洞"(路径太明显、凭据太规整);② 没有正常业务痕迹(用户 home 空、log 全新、history 干净);③ 出网太宽松;④ 有明显的分析工具指纹(sysmon 严配、Falco、tracee)。有 2 条以上就该警觉。
**Q3**: 蜜罐怀疑但还是想榨点价值怎么办?
**A3**: 只做【零副作用信息收集】—— 只读、不写、不执行任何二进制。用现有 shell 内建命令读几个高价值文件(kerberos 缓存、DNS 缓存、arp 表)证实内网真实性,然后立刻撤。别种任何持久化。
**Q4**: EDR 强到不能提权,还能干什么?
**A4**: 【横向收集】—— 低权能连到哪些其他服务?能读到哪些配置文件?能不能横向到 EDR 更弱的机器?目标从"这台提权"改为"用这台当跳板找到 EDR 弱的目标提权"。
**Q5**: 长期潜伏低权账号怎么维持?
**A5**: 用【极稀疏 dead drop beacon】—— 每周甚至每月一次心跳,基本不会被任何异常检测捕获。做的少 = 被发现的概率低。有事再激活。

### 提权后立刻要做的三件事
**Q1**: 拿到 root/SYSTEM 那一刻先干什么?
**A1**: 【凭据快照】—— 立刻抓 hash/ticket/密码/私钥。理由:提权刚成功那一刻权限最高、系统状态最稳,是抓凭据最佳时刻;拖到后面 EDR 可能上线、系统可能重启。
**Q2**: 第二件事?
**A2**: 【独立持久化】—— 种一个和当前提权路径完全无关的后门。理由:当前 shell 是通过提权拿到的,一旦提权路径被封堵(补丁、配置修复)当前 shell 就没了。持久化要走另一条独立触发。
**Q3**: 第三件事?
**A3**: 【痕迹快照 + 清理规划】—— 记录我这次提权留下的所有痕迹(exp 文件、临时释放的 dll、事件日志条目、注册表键值),然后决定清哪些不清哪些。全清反而暴露,选择性清(比如清 exp 文件但保留正常系统日志)。
**Q4**: 三件事的顺序能变吗?
**A4**: 不能。凭据 > 持久化 > 清理。凭据一旦丢窗口(EDR 上、进程重启)就再也没了;持久化没做好就动清理,清完发现回不来。三件事在 5 分钟内完成。
**Q5**: 时间紧迫只能做一件呢?
**A5**: 【抓凭据】。有凭据就有下一次进入的可能,没凭据就是纯打赢一次就完。持久化和清理可以下次进来再做。

### 长期驻留 vs 速战速决的取舍
**Q1**: 什么时候该长期驻留?
**A1**: 目标是【持续观察 + 等待时机】(等待某个高价值行为发生、比如管理员登录、某月度任务运行、某内部会议)。长期驻留 = 极低活动 + 极稀疏心跳 + 多层冗余。
**Q2**: 什么时候该速战速决?
**A2**: 目标明确、时机已到、拖延成本高。比如需要在被打补丁前拿数据、需要在蓝队上班前完成操作。这时不做隐蔽,把所有能做的最快做完,最后连立足点一起放弃。
**Q3**: 长期驻留的核心矛盾是什么?
**A3**: "存在 vs 隐蔽"。想长期驻留就得少动;少动就用不到,那存在的意义就低。解:【冷备份 + 热工作】—— 长期驻留的后门是冷的(月级心跳),只用于关键时刻激活热工作后门(短期高频)。
**Q4**: 速战速决的核心风险是什么?
**A4**: 【暴露连带】—— 快速动作留下大量痕迹,不仅这个立足点烧掉,可能牵连基础设施(C2、域名、TTPs)以后都用不了。要控制在"愿意烧掉一整套基础设施"的心理预算内。
**Q5**: 中间态怎么选?
**A5**: 【分阶段】—— 前期长期驻留潜伏,后期确定要动手就切换到速战速决模式。切换的判据:目标价值明确、时间窗口出现、备份复活手段就位。切换后不再回头。

### 立足点丢失后的回归策略
**Q1**: 立足点被清了但我还有凭据/后门,怎么回来?
**A1**: 优先级:① 用凭据从别的服务重进(SSH key、VPN 账号、RDP 凭据);② 触发休眠后门(dead drop 里发唤醒指令);③ 从其他已控机器横向重进原目标。
**Q2**: 凭据也过期了怎么办?
**A2**: 【找哪些凭据不受轮转影响】—— 服务账号密码常年不换、备份文件里存着老快照、代码仓库里 hardcode 的、GPP cpassword 类历史遗留。或者 ADCS 证书 —— 只要 CA 没换、我的证书有效期内就能用。
**Q3**: 目标基础设施重装了怎么办?
**A3**: 换目标。重装意味着蓝队已经清干净,再打这台是硬打。转向【周边未响应的目标】—— 同网段其他机器、上下游服务、供应链。同批被打的机器如果没都重装,还有活路。
**Q4**: 所有已知路径都不通怎么办?
**A4**: 【回到侦察阶段】—— 承认这一轮结束,重新做外围扫描、社工、供应链侦察。别死磕。红队周期性"重启"是正常的,别陷入"一定要从原来的洞回去"。
**Q5**: 什么时候该真正放弃这个目标?
**A5**: ① 目标已经知道被打并进入长期监控;② 我的所有 TTPs 都被采录进 IOC;③ 时间成本远超预期收益。放弃单个目标不等于放弃项目,基础设施轮换 + 换 TTPs + 换目标才是长线运作。

### 内存马 vs 落地 shell 的取舍
**Q1**: web 服务器上想要长期后门,内存马和落地文件怎么选?
**A1**: 【生存期 vs 隐蔽性】—— 内存马隐蔽(不落地、传统扫描找不到)但重启就没;落地文件生存长但易被 grep 到。理想:【落地 + 内存马】,落地文件极其隐蔽,启动时把内存马注入自己,平时用内存马。
**Q2**: 内存马注入到什么位置最好?
**A2**: 通用高价值点:Servlet Filter(Java 所有请求经过)、Interceptor、Listener、Websocket。避免注入 Controller —— 太具体、覆盖面小。理想:注入到框架级组件,新增 Controller/Service 都自动经过我的 filter。
**Q3**: 内存马被 dump 分析怎么办?
**A3**: 【转分片 + 加密】—— 恶意逻辑分散到多个 filter 组合触发,单个 dump 看不出恶意。或者内存马代码本身运行时解密,dump 出来是加密字节码,反编译看不到明显特征。
**Q4**: 目标频繁重启(容器化、K8s)怎么办?
**A4**: 内存马配合【启动时注入机制】—— 改镜像/Dockerfile、改 K8s config、改 entrypoint 脚本。让每次启动都自动注入内存马。相当于把内存马的"持久性"外包给编排层。
**Q5**: 完全不能落地(严格文件完整性监控)怎么办?
**A5**: 【纯内存 + 外部激活】—— beacon 完全从远程内存加载(反射加载 dll/jar)、本地零文件;或【劫持已加载的合法进程】—— hook 一个合法长驻进程,只在内存里改行为。存活期就是进程存活期。

### rootkit 的使用决策
**Q1**: 什么场景可以考虑 rootkit?
**A1**: ① 目标极高价值需要几年隐蔽驻留;② 蓝队能力弱(不做内核审计、无 EDR);③ 我熟悉目标内核版本、有测试环境。三条同时满足才考虑。
**Q2**: rootkit 的风险是什么?
**A2**: ① 稳定性 —— 内核代码写错直接崩;② 版本适配 —— 内核升级 rootkit 可能失效或崩;③ 一旦发现 = 立即重装 + 全面事件调查,整个入侵链暴露。风险极高。
**Q3**: 什么时候明确不该用?
**A3**: ① 不熟目标内核;② 目标是短期项目;③ 有 kABI 检测/内核签名验证;④ 蓝队有内核级检测能力(内核 IMA、integrity)。这些情况用 rootkit 是自杀。
**Q4**: rootkit 的替代是什么?
**A4**: 用户态 hook + 用户态隐藏。LD_PRELOAD 全局 hook、ptrace 注入、动态修改 /proc 显示。虽然不如 rootkit 强大,但风险低得多、也够骗过大多数常规检测。
**Q5**: rootkit 被发现怎么办?
**A5**: 目标机器基本报废。立刻【放弃 + 切基础设施】—— 假设我所有 TTPs 已经被完整逆向。这就是为什么 rootkit 是重炮,只在做好"这一枪打完可能就要撤"的心理准备时才开。

### 影子账号 vs 加显式账号
**Q1**: 想在 Windows 上留一个后门账号,加新用户还是搞影子?
**A1**: 加新用户会被 net user 枚举 —— 除非蓝队从不看。影子账号(比如把已有账号 SID/属性做手脚,或者创建看似系统内置名字的账号)更隐蔽,但需要更精细的操作。
**Q2**: 什么叫"影子"?
**A2**: 几种玩法:① 用 $ 结尾的账号名(net user 默认过滤显示);② 直接改 SAM 让某个账号具备管理员 SID 但显示为普通;③ 复用一个已废弃/禁用的账号(改密启用,蓝队查活跃账号常常忽略禁用列表)。
**Q3**: 什么时候【就应该】加显式账号?
**A3**: 临时性、可弃、快速需要有一个稳定登录点时。反正打完就撤,追求可用性大于隐蔽性。用完立刻删。
**Q4**: 加了账号被应急发现怎么办?
**A4**: 预期之内。这就是为什么显式账号只做【一次性用】,不做主要持久化。持久化应该同时有多层,显式账号只是最快最容易的一层。
**Q5**: Linux 上对应的思路?
**A5**: ① 直接改 /etc/passwd 加 UID=0 的账号(极显眼、easy 被发现);② 把某个系统账号(比如 games、news、nobody)的 shell 改成 /bin/bash + 加密码或 key(常被忽略);③ 走 nsswitch/PAM 让某个特殊输入直接认证通过。第 3 种最隐蔽。

### DPAPI 与本地凭据的深度利用
**Q1**: 拿到 SYSTEM 但 LSASS 严防、SAM 也难抓,还能抓什么?
**A1**: 【DPAPI】—— Windows 上所有浏览器保存的密码、cmdkey 缓存、Wi-Fi 密码、RDP 保存的密码、Chrome/Edge 的 Cookie 都靠 DPAPI 加密。有 master key + blob 就全解开。
**Q2**: DPAPI master key 藏在哪?
**A2**: %APPDATA%\Microsoft\Protect\<SID>\<GUID>。解密 master key 需要用户密码或者 SYSTEM 的 pre-key。SYSTEM 权限下可以直接用 mimikatz dpapi::masterkey + /system 参数解。
**Q3**: 用户没登录、系统上没活的 master key 怎么办?
**A3**: 【转域备份密钥】—— 域环境下 master key 有一份用域备份密钥加密的副本。拿到域管权限后 lsadump::backupkeys 拿出域备份 key,可以解密该域内所有用户所有 master key。这是一次投入无限收益的杠杆。
**Q4**: DPAPI blob 能挖出什么高价值?
**A4**: ① Chrome/Edge 保存的所有密码(含 SSO 平台、云服务);② RDP 密码(横向直接用);③ Wi-Fi 密码(如果目标是笔记本,拿到用户家里/办公室 Wi-Fi 是有趣情报);④ Outlook 密码(邮箱是黄金);⑤ VPN 客户端保存的密码。
**Q5**: DPAPI 都抓不到怎么办?
**A5**: 【转配置文件】—— 用户 %APPDATA% 下各种应用的配置(FileZilla、WinSCP、各类 IDE 的 credentials、云 CLI 的 config)。很多应用自己加密,但加密弱(硬编码 key、简单 XOR),可离线解。

### 数据外传的稳定性设计
**Q1**: 抓到几十 GB 数据要外传,怎么做?
**A1**: 【分批 + 加密 + 时间打散】—— 一次性大流量必然触发 DLP/NDR。分小包(<10MB)、走加密通道、在业务高峰时段夹带,伪装成合法流量(HTTPS PUT/POST、DNS 隧道很慢但极难拦)。
**Q2**: 目标出网严格白名单只能上几个云怎么办?
**A2**: 【白名单本身就是通道】—— 白名单里的云对象存储、GitHub、公共 CDN 都能当外传通道。把数据 PUT 到公有云 bucket、push 到 github repo 的 issue/gist,不需要额外通道。
**Q3**: 外传中被 DLP 识别怎么办?
**A3**: 【转 stego 或深度混淆】—— 数据 base64 后塞到图片 EXIF、DNS TXT 记录、HTTP header、Cookie。数据被拆解成"看起来像元数据"就极难被内容识别的 DLP 抓住。速度慢但稳。
**Q4**: 大量文件要选筛选?
**A4**: 【预压缩 + 关键词过滤 + 分级】—— 高价值(密码、证书、内部文档)优先抓、立刻传;中价值(源代码、配置)打包分批;低价值(日志、公开材料)最后或不抓。别贪心。
**Q5**: 外传通道全被封了怎么办?
**A5**: 【转物理/带外】—— 极端场景下:U 盘、社工内部人员、走目标的外部合作伙伴通道。数据不一定必须通过我的原始 C2 走,思路要开阔。

### 什么算好的持久化:自检清单
**Q1**: 我种了个持久化,怎么自检它算不算好?
**A1**: 五问:① 目标重启后还活吗;② 我主动 kill 我所有进程后能重连回来吗;③ 蓝队 grep 常见 IOC(可疑 cron、可疑 Run 键、可疑 SUID)能被发现吗;④ 触发条件是否稳定(不依赖偶发事件);⑤ 我能远程唤醒吗还是被动等触发。
**Q2**: 达标标准?
**A2**: 前 3 项必须全过。第 4/5 项看目标 —— 长期驻留可以牺牲响应速度(极稀疏被动),交战期需要主动唤醒(带命令通道)。
**Q3**: 部署完立刻验一次的重要性?
**A3**: 极重要。当场 kill 所有 shell 后走"验证路径"重连一次,能重连才算成功。种完不验就走 = 大概率下次回不来。这是最常见的低级错误。
**Q4**: 如果只能通过"看起来还活"来判断而无法真验证怎么办?
**A4**: 【做多层冗余】—— 反正无法确认单点可靠,那就同时布 3 种不同触发的持久化。就算一种失效,另外两种大概率还活。
**Q5**: 持久化"好"的终极指标?
**A5**: 【蓝队实际做过一次完整应急、清完之后我还能回来】。达到这个标准的持久化,才算真正"持久"。绝大多数持久化只是"活到蓝队响应之前",真正的高质量持久化是"活过蓝队响应之后"。
