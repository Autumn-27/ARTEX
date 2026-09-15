# 提权立足点 · Linux提权决策树

### 拿到低权 shell 后的枚举顺序怎么定
**Q1**: 刚落地一个 www-data / 普通用户的反弹 shell,东西太多,先看哪、按什么顺序枚举才不浪费时间?
**A1**: 遵循"低成本高命中优先"的漏斗。第一梯队(30 秒内、几乎零风险):`id`/`sudo -l`/`uname -a`/`hostname`/`ss -tlnp`。这四样决定了后面走哪条主干——sudo 有货就走 sudo,内核老就留内核为备胎,本地监听端口暴露内部服务就走服务面。第二梯队:SUID/SGID 清单、`getcap -r /`、cron、可写的敏感路径。第三梯队:全盘翻凭据。判据是:凡是"一条命令出结论"的先跑,凡是"要读一堆文件慢慢挖"的排后面。
**Q2**: 手动一条条敲太慢,直接上 linpeas 行不行?
**A2**: 行,但分场景。有出网/能落地文件、且不怕落地检测时,自动化脚本(linpeas/linenum)一把梭最快。但它输出几百行容易淹没重点,且落地大脚本有 EDR/AV 命中风险。稳妥做法:先手动跑第一梯队定方向,再用自动化脚本补盲区,重点看它标红的 95%/99% 项,不要被黄色噪音带偏。
**Q3**: 枚举出来一堆"可能的向量",怎么排优先级?
**A3**: 按"确定性 × 隐蔽性 × 稳定性"排。确定性:sudo NOPASSWD 某个 GTFOBins 二进制 > 理论上存在的内核 CVE。稳定性:配置类提权(sudo/suid/cron/cap)几乎不会崩,内核 exp 可能 panic。所以优先榨干配置类,内核 exp 作为最后手段。
**Q4**: 这条 shell 本身不稳定,一动就断,还怎么枚举?
**A4**: 先稳定化再枚举(见 tty 升级那条)。不稳定 shell 下不要跑重量级脚本,先手动跑第一梯队,把关键输出(sudo -l、suid 清单)存下来,即便断了也有情报。同时立刻在别处建立第二条更稳的通道(SSH key、web shell、计划任务回连)。
**Q5**: 全部枚举完,没有任何明显向量,这条路"被墙"了往哪转?
**A5**: 转正交维度:(1) 时间维度——上 pspy 监控 cron/root 进程,静态枚举看不到的定时任务会现形;(2) 凭据横向维度——放弃"本机纵向提权",转去翻密码/密钥做横向或跳板;(3) 权限组维度——重新审视 `id` 里的附加组(docker/lxd/disk/adm/shadow),组权限是最容易被忽略的提权面。宁可换维度也别死磕一个已经翻遍的面。

### sudo -l 有输出时怎么榨干
**Q1**: `sudo -l` 显示我能以 root 免密跑某个命令,怎么把它变成 root shell?
**A1**: 先看这个二进制在 GTFOBins 里有没有 `sudo` 条目——绝大多数常见程序(find/vim/less/awk/python/tar/env 等)都有现成的逃逸法,套用即可。核心思路:让这个以 root 运行的程序去执行任意命令或 spawn shell,或读写任意文件。判据:GTFOBins 命中即基本稳拿。
**Q2**: 那个二进制不在 GTFOBins,是个自定义脚本或没见过的程序怎么办?
**A2**: 手工分析它。若是脚本,`cat` 读它:看它调用了哪些子命令(相对路径的可 PATH 劫持)、有没有 `eval`/拼接用户输入、读写哪些可控文件。若是二进制,看它 `strings`/`ltrace` 调了什么外部程序或库。自定义程序的经典洞:调用子进程未用绝对路径、信任环境变量、写可预测的临时文件。
**Q3**: sudo 规则里带了 `env_keep` 或没设 `env_reset`,能利用吗?
**A3**: 能,这是高价值信号。保留了 `LD_PRELOAD`/`LD_LIBRARY_PATH` 就能加载恶意 so 提权;保留了 `PYTHONPATH`/`PERL5LIB` 就能劫持模块;保留 `BASH_ENV` 配合 sudo 跑 bash 脚本可注入。看到 env_keep 里有动态链接相关变量,基本等于送分。
**Q4**: sudo 规则限定了参数(比如只能 `sudo systemctl status X`),被参数卡住怎么办?
**A4**: 找参数允许范围内的"越界"点。systemctl status 会调 pager(less),less 里能 `!sh`;很多带 pager/editor 的命令都能这样逃。若规则用了通配符 `*`,尝试塞入额外参数或路径穿越。若真的锁死,转下一条 A5。
**Q5**: sudo 什么都不给、或者需要密码但我没有,这条路被墙了往哪转?
**A5**: 转向 sudo 自身的漏洞维度:看 `sudo --version`,老版本可能吃 Baron Samedit(堆溢出,不需要能跑任何 sudo 命令,只要 sudo 存在且有漏洞版本)之类的本地提权。若 sudo 版本也干净,彻底放弃 sudo 面,转 SUID/capabilities/cron/内核等其它正交面。

### SUID/SGID 二进制的排查与利用
**Q1**: `find / -perm -4000 2>/dev/null` 列出一堆 SUID,哪些值得看?
**A1**: 过滤掉系统默认的(passwd/su/mount/ping/pkexec 等标准集是"背景噪音",但 pkexec 要单独留意 PwnKit)。重点是:非标准路径的、名字眼生的、版本可查的。把清单和 GTFOBins 的 SUID 条目对照,命中的直接利用。眼生的自定义 SUID 程序是最肥的——往往是开发者图省事设的,常有命令注入或路径问题。
**Q2**: 一个常见程序(如 find/nmap 老版/cp)带了 SUID 位,怎么用?
**A2**: 直接查 GTFOBins 的 SUID 段。find 可 `-exec /bin/sh -p \;`(注意 `-p` 保持有效 uid);能写文件的可覆盖 /etc/passwd;能读文件的可读 shadow。关键细节:SUID shell 里要用 `-p` 防止 bash/dash 降权丢掉 euid。
**Q3**: 自定义 SUID 二进制,没源码,怎么找洞?
**A3**: `strings` 看它调了什么;`ltrace`/`strace` 跑一遍看 system()/exec 调用和文件访问。经典命中:它用 `system("service ...")` 这种未带绝对路径调子命令 → PATH 劫持;或把用户输入拼进命令 → 命令注入;或它 open 了一个你可写的配置/日志文件。
**Q4**: 它调用子命令但用了绝对路径,PATH 劫持不了,还有招吗?
**A4**: 转函数劫持——若程序动态链接且你能设 LD_PRELOAD(注意 SUID 会清普通 LD_PRELOAD,但若程序自己 setuid 前读了环境、或有 `LD_PRELOAD` 未被清的边角,或用 `LD_LIBRARY_PATH` 指向可写目录且 RUNPATH 允许),可劫持 libc 函数。或找它读的可写文件做数据注入。或看它有没有格式化字符串/缓冲区溢出的二进制漏洞。
**Q5**: SUID 面全部试完没突破,被墙了转哪?
**A5**: 转 capabilities 面——`getcap -r / 2>/dev/null`。很多现代系统用 cap 替代 SUID(cap_setuid/cap_dac_read_search/cap_dac_override 都能提权或任意读),这是 SUID 清单里看不到的独立向量。再不行转 cron/writable-service 时间面。

### capabilities 提权
**Q1**: `getcap -r /` 列出某二进制带 capability,哪些 cap 能直接提权?
**A1**: 高价值 cap:`cap_setuid`(直接 setuid(0),如带此 cap 的 python/perl 一行提权)、`cap_dac_read_search`(绕过读权限,任意读 shadow/root ssh key)、`cap_dac_override`(绕过写权限,改 /etc/passwd)、`cap_sys_admin`(近乎万能,可挂载/逃逸)、`cap_sys_ptrace`(注入 root 进程)、`cap_sys_module`(加载内核模块直接拿 ring0)。看到这些基本等于提权确定。
**Q2**: 带 `cap_setuid` 的是 python,怎么落地?
**A2**: `python -c 'import os; os.setuid(0); os.system("/bin/sh")'`。cap 让 setuid(0) 成功不需要原本的权限。perl/ruby/node 同理,看哪个解释器带的 cap。判据:该解释器能调 setuid 系统调用即可。
**Q3**: cap 是 `cap_dac_read_search`,只能读不能执行,怎么变现?
**A3**: 任意读转凭据。读 /etc/shadow 拿 root hash 离线爆破;读 /root/.ssh/id_* 直接 SSH 成 root;读数据库配置/历史命令拿凭据横向。读权限本身不给 shell,但读到密钥就等于拿到身份。
**Q4**: cap 是 `cap_dac_override`(可写),怎么用?
**A4**: 任意写转提权:往 /etc/passwd 加一个 UID=0 的账户(带已知密码 hash),或写 root 的 authorized_keys,或写一个 root 会执行的 cron/脚本。选哪个看哪条路径最快被触发且最稳。
**Q5**: getcap 什么都没列出来,被墙了转哪?
**A5**: cap 面为空很常见。转 SUID 面(若还没查)或组权限面。另外注意 cap 可能设在容器/命名空间层面而非文件——若在容器里,`capsh --print` 看进程自身 cap,`cap_sys_admin` 之类容器 cap 是逃逸向量,这属于容器逃逸维度而非文件 cap 维度。

### 内核 exploit 何时用、怎么控风险
**Q1**: `uname -a` 显示内核很老,想上内核 exp,现在该不该打?
**A1**: 内核 exp 是"最后手段"不是"首选",因为可能 panic 掉靶机、影响其它队伍/生产、被检测。决策:先确认配置类(sudo/suid/cap/cron/组)全部无路,再考虑内核。且优先选成熟稳定的 exp(PwnKit/DirtyPipe/DirtyCow/OverlayFS 这类经过大量验证的),避免用 PoC 质量差、易崩的。
**Q2**: 怎么把内核版本精确映射到可用 exp?
**A2**: 三要素:内核版本号(uname -r)、发行版及其补丁级别(cat /etc/os-release、内核 build 日期)、架构。发行版会 backport 补丁,所以"版本号在漏洞范围内"不代表没打补丁。用 linux-exploit-suggester 做初筛,但把它的结果当"候选"而非"结论",再逐个核对补丁状态。
**Q3**: 靶机上没编译器(no gcc),内核 exp 是 C 源码怎么办?
**A3**: 转"别处编译"——在本地或攻击机上用匹配的内核头/架构交叉编译成静态二进制,再传上去。或选已有预编译二进制/不需编译的 exp(有些是脚本或利用 pipe/文件原语)。或找发行版相同的机器编译。切忌在生产靶机装编译器。
**Q4**: 打了内核 exp 没成功但也没崩,怎么判断问题?
**A4**: 排查:架构/版本是否精确匹配、是否需要特定 config(如 user namespaces 开启、某 sysctl)、SMEP/SMAP/KASLR 等缓解是否让这个 exp 失效。有些 exp 需要多跑几次(竞态)。若确认缓解到位导致失效,别硬试,记录后转其它面。
**Q5**: 内核 exp 直接把机器打崩了 / 或全部内核 exp 都被墙,怎么办?
**A5**: 若崩了,评估能否等它重启恢复(生产靶慎重,可能影响计分)。战略上转正交面:回到横向移动——也许根本不需要本机 root,拿到本机的凭据去横向到一台配置更松的机器,或直接找域内路径,比死磕这台机器的内核更高效。

### cron 定时任务提权
**Q1**: 想找 cron 提权点,查哪里、看什么?
**A1**: `cat /etc/crontab`、`ls -la /etc/cron.*`、`/var/spool/cron/*`、各用户 crontab。重点找:以 root 运行的任务 + 它执行的脚本/程序我可写,或它调用的命令用相对路径(PATH 劫持),或它用了通配符(wildcard 注入)。判据:root 定时执行 + 任一环节可控 = 提权。
**Q2**: crontab 里 root 跑一个脚本,脚本文件我有写权限,怎么做?
**A2**: 往脚本里追加反弹 shell 或 `cp /bin/bash /tmp/rootbash; chmod +s /tmp/rootbash`,等它下次触发。追加而非覆盖以免破坏原功能被发现。触发后 `/tmp/rootbash -p` 拿 root。注意 cron 周期,耐心等一个周期。
**Q3**: 脚本本身不可写,但它调用的命令没写绝对路径,PATH 怎么劫持?
**A3**: 看 crontab 顶部的 `PATH=` 定义。若 PATH 里包含一个我可写的目录且排在系统目录前,就在该目录放同名恶意程序。若 PATH 不可控,看脚本里 `cd` 到的工作目录我是否可写(相对调用)。核心:让 root 在解析命令名时先撞到我的文件。
**Q4**: 我怀疑有 cron 但 crontab 文件里看不全(比如 systemd timer 或隐藏任务)?
**A4**: 转 pspy——无需 root 实时监控进程创建和 cron 执行,能看到 crontab 里读不到的 systemd timer、定时脚本、以及它们的完整命令行和路径。这是发现"隐形定时任务"的关键工具,静态枚举的盲区靠它补。
**Q5**: cron 面查遍了,没有可控环节,被墙转哪?
**A5**: 转 systemd 维度——`systemctl list-timers`、检查可写的 .service/.timer 单元文件、可写的 ExecStart 指向的二进制。systemd timer 是 cron 的现代替代,很多机器已无 cron 全在 systemd。再不行转文件监控看有没有其它自动化触发点。

### 可写文件系统路径与凭据搜集
**Q1**: 没有明显的 sudo/suid 向量,想翻凭据,先翻哪?
**A1**: 高命中位置:用户家目录(.bash_history、.ssh/、.config、.aws/、.git-credentials)、web 应用配置(数据库连接串、API key,如 wp-config.php、.env、settings.py)、/var/www、备份文件(.bak/.old/.swp)、日志。history 里常留明文密码(用户手滑把密码敲进命令行),配置文件里几乎必有 DB/服务凭据。
**Q2**: 找到一堆密码/hash,怎么用?
**A2**: 分类利用:明文密码先试 `su` 到其它用户和 root(密码复用极常见)、试 SSH、试 sudo。hash 拿去离线爆破(先判断类型)。DB 凭据登进数据库可能存更多凭据或本身以 root 跑可提权。密钥直接 SSH。判据:先横向复用(成本最低),再爆破。
**Q3**: 全盘搜密码用什么姿势不遗漏?
**A3**: `grep -rniE 'password|passwd|pwd|secret|api[_-]?key|token' /etc /var/www /opt /home 2>/dev/null`,配合搜 .env/.yml/.ini/.php/.py 配置文件,搜 .ssh 私钥,搜 history 文件。linpeas 有专门的凭据搜集模块可补漏。别忘了内存和进程命令行(ps 里可能有带密码的参数)。
**Q4**: 找到私钥但有 passphrase 打不开,或密码爆破跑不动,被墙怎么办?
**A4**: passphrase 私钥可 ssh2john 转格式后离线爆破(弱 passphrase 常能破)。若爆破整体无进展,转变思路:不追求破解现有凭据,转去找"不需要凭据"的提权面(suid/cron/内核),或用已有 shell 做横向到别的机器。凭据搜集是横向的燃料,纵向卡住时它往往是转横向的钥匙。
**Q5**: 什么都没翻到,凭据面被墙,转哪?
**A5**: 转进程/内存维度:`ps aux` 看有没有服务把密码放在命令行参数里;转网络维度:本地监听的服务(见服务面那条)可能有默认/弱口令;转文件监控:pspy 可能抓到某进程周期性带凭据启动。凭据不总在磁盘上,也在运行时。

### 本地监听服务提权(只对 127.0.0.1 开放的服务)
**Q1**: `ss -tlnp` 看到几个只监听 127.0.0.1 的端口(如 3306/6379/其它),这意味着什么?
**A1**: 这些是"内部服务",从外网打不到但我在机器内部能直连,且它们常以 root 或高权用户运行、常配置宽松(因为"反正只有本机能连")。这是被严重低估的提权面。逐个识别:数据库(mysql/postgres)、缓存(redis)、管理接口、消息队列、自研服务。
**Q2**: 有个 redis 只监听本地,怎么提权?
**A2**: 先无密码/弱密码连进去。redis 若以 root 跑,经典手法:写文件原语——设 dir 和 dbfilename 把 RDB 写到 authorized_keys / cron 目录 / web 根,或用模块加载。判据:redis 有写文件能力 + 以高权运行 + 我能控制写入路径 = 提权。
**Q3**: 是 mysql/postgres 以 root 跑,能提权吗?
**A3**: 能。若拿到 DB 高权账号(常从 web 配置里得到):mysql 可用 UDF(user defined function)执行系统命令、写文件(FILE 权限 + secure_file_priv 允许);postgres 可用 COPY ... PROGRAM 或语言扩展执行命令。DB 进程的 OS 用户是谁,你执行的命令就是谁,若是 root 直接提权。
**Q4**: 本地端口是个不认识的自研服务,怎么下手?
**A4**: 先摸清协议:`nc`/curl 连上去看 banner 和响应,抓它的通信格式。自研本地服务经典洞:无认证的管理命令、命令注入、路径遍历、反序列化。因为开发者假设"本地=可信",往往没做任何鉴权。若能触发它以 root 执行任意命令就是提权。
**Q5**: 本地服务都试过、要么强密码要么打不动,被墙转哪?
**A5**: 转"这些凭据能否横向"——DB 里存的用户/密码可能是别处的凭据。或转:该服务的配置文件/数据目录可能可读,里面有其它凭据或密钥。再不行放弃服务面,回到 suid/cron/cap 配置面。本地服务是重要但非唯一的面,不通就换。

### 组权限提权(docker/lxd/disk/adm/shadow 等)
**Q1**: `id` 显示我在一些非默认组里,哪些组等于提权?
**A1**: 危险组速查:`docker`(等于 root——可挂载宿主 / 起特权容器)、`lxd`/`lxc`(同理,挂载宿主盘)、`disk`(可直接读写块设备 = 读写整个文件系统绕过权限)、`shadow`(可读 /etc/shadow 拿所有 hash)、`adm`(可读日志,常含凭据)、`video`/`kmem`、`sudo`/`wheel`(需要密码但值得试)。看到 docker/lxd/disk 基本等于 root。
**Q2**: 我在 docker 组,怎么提权?
**A2**: `docker run -v /:/mnt --rm -it <任意镜像> chroot /mnt sh`——把宿主根挂进容器再 chroot,你就是宿主 root。若本地没镜像,用最小镜像或已有镜像即可。原理:docker 守护进程以 root 跑,docker 组成员能指挥它挂载任意宿主路径。
**Q3**: 我在 disk 组,没有 docker,怎么用?
**A3**: disk 组可读写 /dev/sdaX 等块设备。用 `debugfs /dev/sdaX` 直接读任意文件(包括 shadow、root ssh key),绕过文件权限;或直接编辑块设备写入。等于拥有整个文件系统的读写权,读密钥/hash 或改 passwd 都行。
**Q4**: 我在 lxd 组但系统里没有现成容器镜像,导入不了怎么办?
**A4**: 转"自带镜像"——用一个体积极小的 alpine 类 rootfs 作为镜像导入(可离线传上去),再起一个 `security.privileged=true` 且挂载宿主 `/` 的容器,在容器内即宿主 root。若无法传镜像,退一步看该组还有没有别的能力,或换其它提权面。
**Q5**: 组里没有任何危险组,组权限面被墙转哪?
**A5**: 转标准的 suid/cron/cap/内核面。组权限是"有就秒杀、没有就跳过"的快速检查项,`id` 一眼看完,别为它纠结,没有立刻转别的正交维度。

### writable /etc/passwd 或 /etc/shadow
**Q1**: 发现 /etc/passwd 我可写,怎么直接拿 root?
**A1**: 往 passwd 里加一行 UID=0 GID=0 的账户,密码字段填一个我已知明文对应的 hash(用 openssl passwd 或 mkpasswd 生成),然后 `su` 到这个新账户即成 root。或者更隐蔽:把某个现有账户第二字段的 `x` 替换成我生成的 hash,直接改现有 root 项的密码。判据:passwd 可写 = 立即提权。
**Q2**: 是 /etc/shadow 可写而非 passwd,怎么用?
**A2**: 生成一个已知密码的 hash,替换 shadow 里 root 那行的 hash 字段,然后 `su root` 用已知密码登入。shadow 可写等价于可设置任意账户密码。
**Q3**: /etc/passwd 只可读不可写,但我读到里面有账户的第二字段直接是 hash(而非 x)?
**A3**: 老式或误配的系统会把 hash 放 passwd(全局可读)。直接把这些 hash 拿去离线爆破,破出来就是那个用户/root 的密码。这是"读权限"变现的一种。
**Q4**: 我能写 passwd 但没有 openssl/mkpasswd 生成 hash 的工具,怎么办?
**A4**: 在攻击机上离线生成 hash 再粘贴进去,不需要靶机有工具。传统 DES/crypt 甚至可以手算或用预知的 `AzL1zWgcbGwU:0:0` 这类公开示例 hash(对应已知明文)。核心是 hash 在本地生成,靶机只负责被写入。
**Q5**: passwd/shadow 都不可写(正常情况),这条被墙转哪?
**A5**: 这本来就是小概率的误配检查项,不可写很正常。转向"能否间接获得写它的能力"——比如某个 cap_dac_override 二进制、某个 root cron、某个可控的 root 服务,能帮我写 passwd。若都没有,转其它正交面。

### 稳定化 shell(tty 升级)与工作面维护
**Q1**: 反弹 shell 是个哑 shell,不能用 tab 补全、Ctrl-C 直接断线、sudo 报错,先干嘛?
**A1**: 提权前先升级为交互式 tty,否则很多操作(sudo 交互输密码、su、vim 逃逸、ssh)做不了。标准流程:`python3 -c 'import pty;pty.spawn("/bin/bash")'` 拿到半交互,再 `Ctrl-Z` 到本地 `stty raw -echo; fg`,回来 `export TERM=xterm; stty rows.. cols..`。得到全交互 tty 后 Ctrl-C 不再断、能补全能翻历史。
**Q2**: 靶机没有 python 怎么升级 tty?
**A2**: 换原语:`script -qc /bin/bash /dev/null`、`socat`(若有,能给最完整的 pty)、perl `exec "/bin/sh"` via pty、或用 `expect`。都没有就退而求其次用 stty 手工调,至少让 sudo 能交互。判据:目标是拿到一个能承载交互程序的 pty。
**Q3**: 为什么提权前一定要稳定 shell?
**A3**: 因为很多提权动作依赖交互:sudo 要输密码、su 要 tty、GTFOBins 的 vim/less 逃逸要终端、内核 exp 有时要交互确认。哑 shell 下这些会静默失败或报 "must be run from a terminal",让你误判"这条路不通"。稳定化能排除"工具问题"伪装成的"向量问题"。
**Q4**: shell 很不稳定,几分钟就掉,提权还没做完怎么办?
**A4**: 立刻建冗余通道:写一个 SSH key 到当前用户 authorized_keys(若能)拿稳定 SSH;或加一个 cron/systemd 定时回连;或落地一个 web shell。有了稳定立足点再从容提权,别把所有希望押在一条随时会断的 tcp 上。
**Q5**: 落在一个 rbash / 受限 shell 里,命令都被限制,这算被墙吗,怎么转?
**A5**: 转受限 shell 逃逸维度:找允许执行的、能 spawn 子 shell 的程序(vi/vim `:!sh`、awk/find/less/man 的 shell 逃逸、ssh -t 强制命令、`bash --noprofile`、能带 `-c` 的解释器)。rbash 只限当前 shell 的 PATH 和重定向,一旦通过某程序 spawn 出无限制子进程就逃逸了。逃出后再正常枚举提权。

### 容器内 vs 物理机的判定与容器逃逸
**Q1**: 拿到 shell,怎么先判断我是在容器里还是物理/虚拟机上?
**A1**: 判据组合:`/.dockerenv` 文件存在、`cat /proc/1/cgroup` 里有 docker/lxc/kubepods、`ps` 里 PID 1 是应用而非 systemd/init、`hostname` 是随机十六进制、`mount` 看 overlay 文件系统、网络只有单一 veth。判定是否容器决定了提权目标:容器里"提权到容器 root"意义不大,真正目标是"逃逸到宿主"。
**Q2**: 确认在容器里且已是容器内 root,怎么找逃逸点?
**A2**: 检查:(1) 是否 `--privileged`(`capsh --print` 看有没有一堆 cap,尤其 cap_sys_admin);(2) 是否挂载了 docker.sock(`/var/run/docker.sock` 存在=可指挥宿主 docker);(3) 危险挂载(宿主 / 或 /proc 挂进来);(4) 危险 cap(sys_admin/sys_module/dac_read_search)。任一命中即有逃逸路径。
**Q3**: 容器里有 docker.sock,怎么逃?
**A3**: sock 存在等于容器内能调宿主 docker API。用 docker CLI 或直接 curl sock,起一个挂载宿主 `/` 的新容器并 chroot,即宿主 root。等价于宿主上的 docker 组权限。
**Q4**: 是特权容器(privileged),但没有 docker.sock,怎么逃?
**A4**: 特权容器可访问宿主设备。经典法:`fdisk -l` 找宿主磁盘设备,mount 到容器内直接读写宿主文件系统;或用 cgroup release_agent 手法(cgroup v1)让宿主以 root 执行脚本;或 cap_sys_module 时加载内核模块。特权 = 设备和 cap 全放开,逃逸路径很多。
**Q5**: 容器什么都没配错(非特权、无 sock、无危险挂载、cap 削减干净),逃逸被墙转哪?
**A5**: 转两个正交面:(1) 内核面——容器和宿主共享内核,一个内核 exp 可能直接从容器提到宿主 ring0,不依赖容器配置;(2) 横向面——容器内的应用凭据、环境变量、挂载的 secret、能连通的内网服务(其它容器、k8s API、宿主服务),用这些做横向往往比硬逃逸更实际。硬逃逸不通就打内核或打网络。

### 通配符注入(wildcard injection)
**Q1**: 看到一个 root cron 执行类似 `tar -czf backup.tar.gz *` 或 `chown -R ... *` 在某个我可写的目录里,能利用吗?
**A1**: 能,这是 wildcard 注入。`*` 会被 shell 展开成目录里的文件名,如果我能在该目录创建"看起来像命令行选项的文件名",这些文件名就会被当作参数塞给命令。判据:root 执行 + 命令带 `*` + 我对目录可写。
**Q2**: 具体怎么把文件名变成 tar 的提权参数?
**A2**: tar 支持 `--checkpoint` 和 `--checkpoint-action=exec=...`。在目录里创建名为 `--checkpoint=1`、`--checkpoint-action=exec=sh runme.sh` 的空文件,再放一个 runme.sh。tar 展开 `*` 时把这些文件名当选项解析,触发以 root 执行 runme.sh。
**Q3**: 命令不是 tar 而是 rsync/chown/chmod 等,也能注入吗?
**A3**: 看该命令有没有"能执行命令或改权限"的危险选项。rsync 有 `-e`/`--rsh`；chown/chmod 配合 `--reference=file` 可把权限对齐到我控制的文件。思路统一:查该命令的选项手册,找一个"通过参数触发副作用"的选项,再造对应文件名。
**Q4**: 目录里已有正常文件,我加的选项文件会不会破坏原命令导致报错被发现?
**A4**: 可能。tar 的 checkpoint 注入通常不影响打包成功,较隐蔽。但有些注入会让原任务报错。权衡:提权成功后立刻删掉这些伪装文件恢复现场;或选副作用最小的选项。红队讲究打完清痕。
**Q5**: 目录不可写、或命令没用通配符,这个向量被墙转哪?
**A5**: wildcard 注入依赖"可写目录 + 通配符 + root 执行"三者齐备,缺一即不适用,很正常。转回 cron 面的其它利用方式(脚本可写、PATH 劫持),或换别的正交面。这是个机会型向量,有就用,没有别强求。

### PwnKit / pkexec 与 polkit 类提权
**Q1**: 系统里有 pkexec(SUID)且没别的向量,值得试 PwnKit 吗?
**A1**: 值得——pkexec 几乎所有 Linux 桌面/服务器都装,PwnKit(CVE-2021-4034)影响面极广,是内存破坏但极其稳定、几乎不崩,是"内核 exp 太冒险、配置面又无路"时的黄金中间选项。先确认 pkexec 版本在受影响范围且未打补丁。
**Q2**: 怎么快速判断 pkexec 是否可能有 PwnKit?
**A2**: 看 polkit/pkexec 包版本和补丁日期。这个洞非常老且广泛,很多疏于更新的机器仍中招。可先跑一个探测型 PoC(不实际提权只判断),或直接上稳定 exp 试。因为它不 panic 内核,试错成本低于内核 exp。
**Q3**: pkexec 有,但打了补丁,还有别的 polkit 路吗?
**A3**: polkit 生态还有其它 CVE(如更早的 CVE-2021-3560,通过 dbus 竞态绕过认证给用户加管理员)。若 pkexec 补了,看 polkit/accountsservice/dbus 组合的其它已知洞。判据:目标是否跑着有漏洞版本的 policykit 守护进程。
**Q4**: exp 编译好跑了却提示无效,可能哪出错?
**A4**: PwnKit 类对环境敏感:检查是否真无补丁、glibc 版本、是否某些加固(如厂商给 pkexec 打了非官方缓解)。别急着判死刑,换一个实现版本再试(不同 PoC 质量差异大)。若确认补丁到位,记录并转其它面。
**Q5**: pkexec 根本不存在、或彻底补齐,这条被墙转哪?
**A5**: 转其它"广谱稳定本地提权"候选:DirtyPipe(CVE-2022-0847,内核 5.8+ 一段区间,任意写只读文件,极稳)、Baron Samedit(sudo)、OverlayFS 类。这些和 pkexec 是并列的"高稳定性提权库",一个补了试下一个,构成你的稳定提权武器库。

### DirtyPipe / DirtyCow 类内存原语提权
**Q1**: 内核版本落在 DirtyPipe(5.8–5.16 某区间)范围,想用它,它给我什么能力?
**A1**: DirtyPipe 提供"向只读文件任意写"的原语。它本身不直接给 shell,而是让你能改本该不可写的文件。变现路径:改 /etc/passwd(去掉 root 密码或加 UID0)、覆盖一个 root 会执行的 SUID 程序/脚本、改 sudoers。判据:内核在区间内 + 未打补丁。
**Q2**: DirtyPipe 具体怎么变成 root shell?
**A2**: 常见两条:(1) 覆写 /etc/passwd 把 root 那行的密码 hash 改成已知值再 su;(2) 劫持一个已存在的 SUID 二进制,把它的内容临时替换成你的 payload,运行拿 root 后恢复。选哪条看现场哪个更稳、更好清痕。
**Q3**: 内核更老,DirtyPipe 不适用,但落在 DirtyCow(CVE-2016-5195)范围呢?
**A3**: DirtyCow 是 copy-on-write 竞态,也给"写只读内存映射文件"的原语,变现类似(改 passwd 或劫持 SUID)。但 DirtyCow 是竞态,某些变体会让机器不稳定甚至崩,用相对稳定的实现(如改 passwd 的版本比 vDSO 劫持版更稳)。老内核优先考虑它。
**Q4**: 这类内存原语 exp 跑了没提权成功怎么排查?
**A4**: 检查:内核补丁级别(发行版 backport 会修但 uname 不变)、是否有加固导致原语失效、竞态类需多试几次、目标文件是否真的是攻击面(比如 /etc/passwd 用了别的认证后端)。换变现目标(passwd 不行换 SUID 劫持)再试。
**Q5**: 所有内存原语 exp 都被补丁墙掉,转哪?
**A5**: 内核面被补齐,说明这台机器补丁勤,继续磕内核收益低。战略转向:(1) 配置面重新精查(补丁勤的机器可能应用层配置反而松);(2) 横向——去找一台补丁不勤的机器;(3) 凭据面。补丁勤 ≠ 配置严,换维度。

### 写入 root 会执行/读取的文件
**Q1**: 我对某个"root 会读或执行的文件"有写权限(不是 passwd/cron,是别的),怎么判断能不能提权?
**A1**: 关键问自己两点:(1) root 会不会、何时、以什么方式使用这个文件;(2) 我写进去的内容会被"执行"还是仅"读取"。可执行类(脚本、被 source 的配置、systemd ExecStart 指向的文件、LD 配置)= 直接提权。仅数据类(root 读它做决策)= 看能否间接触发命令。
**Q2**: 是 root 用户的 shell 配置文件(.bashrc/.profile 之类)我可写?
**A2**: 若 root 会登录交互 shell(比如管理员会 su/ssh 进来),往其 .bashrc 追加 payload,下次 root 登录即触发。但 root 未必频繁登录,触发时机不定。作为"埋伏型"向量可行,配合社工/等待。也可看 /etc/profile.d/ 下可写脚本,影响所有登录。
**Q3**: 是一个 root 服务读取的配置文件(如某 daemon 的 conf)可写?
**A3**: 看该配置能否指定"执行的命令/加载的模块/脚本路径"。很多 daemon 配置支持 hook、plugin、exec 指令,把它指向我的 payload,再触发服务重载(若我能触发或等它重启)即以 root 执行。判据:配置项里有没有"命令/路径"型的可控字段。
**Q4**: 写进去了但迟迟不触发(root 不登录、服务不重启)怎么办?
**A4**: 找触发器:能否合法地让服务 reload(有些允许普通用户发信号或通过某接口)、能否制造服务崩溃让它被 systemd 自动重启从而重读配置、或系统有定时重启/日志轮转会触发。若完全无法触发,这是"被动埋伏"向量,转去找"主动触发"型的提权面(suid/内核)。
**Q5**: 可写的都是纯数据文件、root 读了也不会执行任何东西,这条被墙转哪?
**A5**: 转"数据影响决策"思路——root 读它后会不会基于内容做危险操作(比如读一个路径列表然后去 chown/执行)。若确实只是无害数据,放弃此文件,转其它面。可写文件的价值完全取决于 root 如何消费它,消费方式无害则无价值。

### PATH 劫持(SUID 脚本 / 环境继承)
**Q1**: 一个 SUID 程序或 root 脚本调用了子命令但没写绝对路径,我想 PATH 劫持,前提是什么?
**A1**: 前提:(1) 该程序以高权运行时使用了"我能控制的 PATH",或(2) 它 fork 出的进程继承了我可污染的环境。对 SUID C 程序,若它用 `system()`/`popen()` 调 `service`、`id`、`cat` 等而不带路径,我把 PATH 首位设为可写目录并放同名恶意文件即可。判据:高权执行 + 相对命令 + PATH 可控。
**Q2**: 怎么确认它调了哪些可劫持的命令?
**A2**: `strings` 看有没有像命令名的字符串;`ltrace -f` 跑一遍直接看 system/execvp 调了什么、用了相对还是绝对路径。看到 `sh -c "service apache ..."` 这种即锁定 `service` 为劫持目标。
**Q3**: 我造了同名恶意文件、改了 PATH,但没生效?
**A3**: 排查:(1) SUID 程序可能自己重置了 PATH(安全写法);(2) `system()` 用的是 `/bin/sh -c`,某些 sh 会用安全默认 PATH;(3) 恶意文件没加执行权限或 shebang 错。若程序硬编码了安全 PATH,PATH 劫持这条就废,转函数劫持或找别的洞。
**Q4**: 目标命令用了绝对路径,PATH 劫持彻底不通,转哪?
**A4**: 转 LD 层劫持:若程序动态链接、且它对 LD_LIBRARY_PATH/LD_PRELOAD 的处理有疏漏(或 sudo env_keep 保留了这些),用恶意 so 劫持它调用的库函数。或转数据面:它读的可写文件。绝对路径挡住 PATH,但挡不住库和数据。
**Q5**: PATH 和 LD 都被正确加固,这个 SUID 无懈可击,转哪?
**A5**: 说明这个二进制写得规范,别死磕。转其它 SUID/cap/cron 目标,或转二进制漏洞角度(若它有处理外部输入的逻辑,可能有溢出/注入)。一个加固好的 SUID 不代表全系统都加固,换目标。

### LD_PRELOAD / LD_LIBRARY_PATH 劫持
**Q1**: 什么条件下 LD_PRELOAD 能用来提权?
**A1**: 核心:能让一个"高权进程"加载我的恶意 so。典型场景:(1) `sudo -l` 显示 `env_keep+=LD_PRELOAD`——直接 `sudo LD_PRELOAD=/tmp/e.so <允许的命令>`,so 的构造函数以 root 跑;(2) 某 root 进程/脚本继承了我可设的环境。注意:普通 SUID 会清 LD_PRELOAD,所以主要靠 sudo env_keep 或非 SUID 的高权执行路径。
**Q2**: 恶意 so 怎么写?
**A2**: 写一个带 `__attribute__((constructor))` 的函数,里面 setuid(0)+execve("/bin/sh"),编译成共享库。加载时构造函数自动执行,以宿主进程的权限(root)起 shell。判据:so 被加载即触发,无需目标程序主动调用其中函数。
**Q3**: sudo 保留的是 LD_LIBRARY_PATH 而非 LD_PRELOAD,怎么用?
**A3**: LD_LIBRARY_PATH 让我控制库搜索路径。找出目标程序依赖的某个 .so 名字,在我的可写目录放一个同名恶意 so(导出它需要的符号 + 构造函数提权),设 LD_LIBRARY_PATH 指向该目录。程序加载我的假库即中招。比 LD_PRELOAD 多一步找依赖库名。
**Q4**: env_keep 没保留这俩,SUID 又清环境,LD 劫持不通,转哪?
**A4**: 转其它 env 型注入:env_keep 保留了 PYTHONPATH/PERL5LIB/RUBYLIB → 劫持脚本模块;保留 BASH_ENV → sudo 跑 bash 脚本时注入。看 sudoers 到底放行了哪个环境变量,不同变量对应不同劫持面。全无则放弃 env 面。
**Q5**: 所有环境变量注入面都被墙(env_reset 严格),转哪?
**A5**: 转不依赖环境的提权:GTFOBins 的直接逃逸(vim/less/命令自身功能)、二进制漏洞、内核、cron。env 注入只是众多面之一,env_reset 严格是好配置但不代表其它面也严。换正交维度。

### NFS no_root_squash 提权
**Q1**: 发现机器导出了 NFS 共享(`cat /etc/exports` 或 `showmount`),看什么决定能否提权?
**A1**: 看 export 选项有没有 `no_root_squash`。默认 root_squash 会把客户端 root 映射成 nobody;而 no_root_squash 保留客户端 root 身份——意味着我在自己控制的机器上以 root 挂载这个共享,创建的文件在服务器上就是真 root 属主。判据:no_root_squash + 我能从一台有 root 的机器挂载它。
**Q2**: 有了 no_root_squash 怎么落地提权?
**A2**: 在我可控的(有 root 的)机器上 mount 该 NFS 共享,以 root 在共享里放一个 SUID root 的 shell(`cp /bin/bash .; chmod +s`)。回到目标机(共享在它本地),执行这个 SUID bash `-p` 即拿到目标机 root。关键:利用两端共享同一份文件、且属主被保留为 root。
**Q3**: 我攻击机连不到 NFS 端口(防火墙),但目标机自己 export 的,怎么办?
**A3**: 若我在目标机上已有低权 shell 但连不出去搭 NFS,思路变:能否本地环回挂载、或利用目标机已挂载的其它 no_root_squash 共享(反向——它挂了别人的)。若网络完全隔离用不了 NFS 特性,这条不适用,转别的面。
**Q4**: 是 root_squash(默认安全)而非 no_root_squash,还能利用吗?
**A4**: 直接的 SUID 植入不行(root 被压成 nobody)。但仍可看:共享里已有的文件权限是否宽松(可写别人的脚本)、有没有可读的凭据。root_squash 只挡"以 root 写",不挡"读共享内容"和"以匹配的 uid 写"。价值降低但未必为零。
**Q5**: NFS 面完全用不上(无导出/全 squash/网络不通),转哪?
**A5**: NFS 是特定环境才有的机会向量,没有很正常。转本机标准面(suid/sudo/cron/cap)。若这是内网多机环境,把注意力转向其它共享协议(SMB)或横向,别在单一 NFS 特性上耗时间。

### 判断 root 后要不要立即固化立足点
**Q1**: 刚提权到 root,第一件事该做提权后利用还是先固化?
**A1**: 先固化——root 可能因 shell 断线、被踢、机器重启而丢。低成本固化:留一个稳定回连(SSH key 到 root、或一个隐蔽定时回连)、记录关键凭据(shadow、ssh key、DB 密码)以便随时恢复和横向。判据:root 权限是易失资源,拿到后 60 秒内建立可恢复性。
**Q2**: 固化时怎么平衡"稳"和"隐蔽/清痕"?
**A2**: 比赛/评估场景不需要 rootkit 级持久化,但要避免单点失效。折中:一个不显眼的 SSH key + 记下所有能重新拿 root 的路径(比如那个 SUID/cron 还在)。既然提权路径本身还在,它就是天然的"持久化",未必要新增可疑文件。清痕:删掉提权过程中落地的 exp、临时 SUID bash、伪装文件。
**Q3**: 拿到 root 后,提权任务算完了吗?
**A3**: 本机纵向完成,但战役未必。root 后立即做"root 才能做的收割":dump 所有 hash(为横向/爆破)、读所有 ssh key 和 DB 凭据、看这台机器与内网其它主机的信任关系(known_hosts、挂载、历史 ssh 命令)、抓内存凭据。root 是横向的燃料库,别停在"我 root 了"。
**Q4**: root 拿到但 shell 极不稳,固化动作还没做完就可能断,怎么办?
**A4**: 按优先级抢做:第一优先写一个稳定回连/SSH key(保命),第二 dump shadow 和关键 key(情报),其余延后。把最不可逆、最保命的操作放最前,假设下一秒就断线。
**Q5**: 提权全程失败,始终拿不到 root,这台机器"被墙",战略上怎么转?
**A5**: 转横向优先思维:root 不是唯一目标,当前低权账户可能已经握有横向所需的一切——凭据、内网可达性、共享访问。转去打内网其它机器、找域路径、用现有权限访问敏感数据。很多时候整个战役的胜负手在别的机器上,而不是把这台磕到 root。及时止损,换战场。

### pspy 进程监控:挖静态枚举看不到的东西
**Q1**: 静态枚举(cron 文件、SUID、cap)全翻遍没货,怀疑有隐藏的 root 活动,怎么看?
**A1**: 上 pspy(无 root 监控 /proc 里进程创建/退出)。它能抓到:cron 触发的短命进程、systemd timer、守护进程周期性调外部命令、以及最肥的——其它用户/脚本在命令行里明文传的密码(mysql -p、curl -u、ssh sshpass)。判据:静态文件看不到"正在发生什么",pspy 补上时间维度。
**Q2**: pspy 传不上去 / 没有出网,怎么替代?
**A2**: 手写监控循环:`while true; do ls -la /proc/*/cwd 2>/dev/null; cat /proc/*/cmdline; done` 或反复 diff `ps` 快照。粗糙但同样能抓瞬时进程。核心是"高频轮询 /proc",不一定非要 pspy 这个二进制。
**Q3**: pspy 抓到 root 周期性跑某脚本,下一步怎么把它变提权?
**A3**: 看这条命令的每一环哪个可写:脚本本身、脚本调的子命令(相对路径→PATH)、它读写的目录(通配符/文件注入)、它 source 的配置。把"观察到的 root 行为"逐环拆成"可控点"。
**Q4**: 抓到别的用户命令行里带了明文口令,怎么用?
**A4**: 直接记下,试这个口令:su 到那个用户、sudo(它可能有更好的 sudo 规则)、复用到 DB/SSH/其它主机。命令行泄露的口令是纯送分,别只当情报,立刻拿去横切身份。
**Q5**: 监控了很久什么都没抓到,这条路空了往哪转?
**A5**: 转"主动触发"而非"被动等待":有些 root 逻辑由事件触发(登录跑 motd、收邮件、文件到达某目录、服务重启)。制造这些事件去激活 root 代码路径,或者干脆放弃时间维度,回到静态的可写文件/组权限维度。

### sudo 会话复用与 sudo 版本漏洞
**Q1**: 当前用户有 sudo 权限但要密码,我没有密码,是不是就没戏?
**A1**: 先看两点。其一:该用户是否最近用过 sudo(sudo 有时间戳缓存,默认几分钟内免密)——若能劫持其活动终端/tty,可蹭上缓存直接 sudo。其二:看 `sudo --version`,老版本吃本地提权 CVE(Baron Samedit 堆溢出、sudoedit 的 CVE、-u#-1 的 CVE-2019-14287),这些不需要你知道密码。
**Q2**: 怎么判断能不能蹭到 sudo 缓存?
**A2**: 缓存按 tty/会话绑定。若你能进入该用户已认证的那个 shell 会话(tmux/screen 共享、或该用户是你已控账户),在同一会话里 sudo 可能免密。判据:是否存在一个"刚 sudo 过、还没超时"的同用户会话可搭。
**Q3**: sudo 版本落在漏洞区间,但不确定打没打补丁,怎么办?
**A3**: 发行版可能 backport 补丁而版本号不变,所以别只信版本号。低风险先做原理性探测(如 Baron Samedit 有非破坏性判断法),确认可利用再上 exp。谨慎:sudo 类堆溢出 exp 有崩溃风险,先在心里评估这台崩了的代价。
**Q4**: 用户根本没 sudo 权限、sudo 版本也干净,这条全废往哪转?
**A4**: 彻底离开 sudo 面。转 SUID/cap/cron/组权限等配置面,或者反过来钓密码:布一个假的 sudo(PATH 靠前的同名脚本)记录用户输入的口令,等真人来触发。后者是"钓"而非"提",适合有真人交互的目标。
**Q5**: 想钓 sudo 密码但目标没人交互(纯服务器无登录),钓不到怎么办?
**A5**: 放弃社工向量,回到纯技术面。无人交互的机器上,提权只能靠配置/内核/服务漏洞,不能指望骗到密码。把精力压回 SUID/cap/内核 exp/内部服务这些不依赖人的维度。

### 翻到口令后:密码复用与账户横切
**Q1**: 在配置文件/history/备份里翻到一个明文口令,该怎么用它提权?
**A1**: 别只对着一个账户试。系统性喷这个口令:su root(万一是 root 弱口令)、su 到本机每个有 shell 的用户、sudo(某用户配了 sudo)、以及本机所有服务(DB/SSH/web 后台)。口令复用是 Linux 提权最高频的真实路径之一。判据:一个口令值多个身份,逐个试。
**Q2**: su 到某用户成功了,但那用户也不是 root,白切了吗?
**A2**: 不白切。换个身份就换了一套提权面:新用户可能有不同的 sudo -l、属于危险组、拥有某些文件、有自己的 cron/服务。每切一次身份就重跑一遍第一梯队枚举(id/sudo -l),把"横切"当成"打开新提权面"。
**Q3**: 翻到的是 hash 不是明文,怎么办?
**A3**: 离线爆破(john/hashcat + 字典/规则)。破出来后回到口令复用流程。破不出就看能否 pass-the-hash(Linux 上少见,但 SSH/Kerberos/某些服务可能可用),或把 hash 作为情报留存,转别的路径。
**Q4**: 口令是某个应用/DB 的,不是系统账户的,还有用吗?
**A4**: 有——先看这个 DB/应用是不是以 root 或高权跑(那就走服务提权),再看这口令是否被人复用成了系统口令(常见运维习惯)。应用口令是通往服务提权和口令复用的双入口。
**Q5**: 所有翻到的口令喷完一个都不中,这条路断了往哪转?
**A5**: 转回"无凭据"的纯漏洞面(SUID/cap/cron/内核/内部服务),或者扩大凭据搜集范围:还没翻的地方(其它用户家目录只有提权后才可读、内存、浏览器/邮件凭据、git 仓库历史)。口令喷空往往只是"手上口令太少",而不是"复用这条路不通"。

### systemd 服务与 timer 提权
**Q1**: cron 面翻完没货,现代系统更可能用 systemd,该查什么?
**A1**: 查 systemd 的 service 与 timer:`systemctl list-timers`、看 /etc/systemd/system 和 /lib/systemd/system 下 unit 文件的权限。找:root 跑的 unit,其 unit 文件可写、或其 ExecStart 指向的脚本/二进制可写、或 WorkingDirectory/EnvironmentFile 可控。判据:任何 root unit 链条上有一环你能写。
**Q2**: 有个 root service 的 unit 文件我能写,怎么变 root?
**A2**: 改 ExecStart 指向你的 payload,然后触发重启该服务。若不能改文件但能 `systemctl edit`(有权限)或能控制它 EnvironmentFile 指的可写文件,同样可注入。改完想办法触发(服务本会重启、或你有权 restart)。
**Q3**: unit 文件不可写,但我发现能对某个 root service 执行 start/restart(sudo 或 polkit 允许),怎么用?
**A3**: 看这个 service 起来时读什么可控文件、跑什么可控脚本;或者结合"可写 ExecStart 目标"。单有 restart 权而链条全不可写时,转去找哪个 service 的执行体恰好落在你可写位置。
**Q4**: 我能创建自己的 systemd unit 吗?
**A4**: 看 /etc/systemd/system 或用户级 unit 目录是否可写、以及你能否 daemon-reload+start。若可写且能以 root 启动新 unit,直接写一个 ExecStart=你的 shell 的 unit。这比劫持现有 service 更干脆。
**Q5**: systemd 面全排完(unit 都不可写、无 start 权、建不了新 unit),往哪转?
**A5**: 转回其它触发框架:D-Bus/polkit 授权的操作、logrotate/motd 等脚本框架、或彻底离开"被触发执行"维度,回到 SUID/cap/内核的"主动执行"维度。别在 systemd 一棵树上吊死。

### 以 root 运行的数据库/服务提权
**Q1**: `ss -tlnp` 或 ps 看到本机跑着 MySQL/PostgreSQL/Redis 且属主是 root,怎么把它变提权?
**A1**: DB 以 root 跑 = 潜在任意文件写/命令执行。MySQL:若能连上(复用口令/socket 免认证),用 UDF 写 so 执行系统命令、或用 INTO OUTFILE 写 root 的 authorized_keys/cron。PostgreSQL:COPY ... TO/FROM PROGRAM 直接命令执行、或 LO 大对象写文件。Redis:未授权时 CONFIG SET 写 crontab/SSH key/module load。判据:能认证连上 + 服务以高权跑。
**Q2**: 连不上 DB(要密码,我没有),这条断了吗?
**A2**: 先找密码:web 应用配置文件、环境变量、history、备份里几乎必有 DB 口令。或找本地免认证入口:MySQL 的 unix socket、Redis 绑 127.0.0.1 常无密码。DB 提权的"墙"通常是认证,而认证凭据往往就散落在本机文件里。
**Q3**: Redis 未授权但绑在 127.0.0.1,我已经在本机,怎么打?
**A3**: 本机可达就直接 redis-cli。写 crontab、写 ~/.ssh/authorized_keys(若 Redis 属主家目录/权限允许)、或 MODULE LOAD 加载恶意模块执行命令。绑 127 对已在本机的你不是障碍,反而少了网络暴露被发现的风险。
**Q4**: DB 不是 root 跑的(比如 mysql 用户),写文件/命令执行还有意义吗?
**A4**: 有,但目标变了:此时命令执行拿到的是 mysql/postgres 用户,不是 root。把它当"横切到服务账户",再从那个账户重新找提权面(它可能有别的组、别的文件权限)。别以为非 root 跑就没价值。
**Q5**: DB 版本老,想直接打 DB 软件本身的 RCE 漏洞行不行?
**A5**: 可以作为备选,但优先级低于"合法功能滥用"(UDF/COPY PROGRAM 更稳、不崩服务)。软件 CVE 有崩库风险且未必有 exp。先榨干功能滥用,DB 软件 exp 是最后手段。
**Q6**: 本机没有任何以 root 跑的服务,内部服务面空了往哪转?
**A6**: 转回本地文件/权限维度(SUID/cap/cron/组),或者把本地监听但非 root 的服务当作"横切/信息"入口而非"提权"入口。服务面为空是常态,不是死路。

### ptrace 注入高权进程
**Q1**: 系统里有 root 拥有的进程一直在跑,我当前用户能不能注入它?
**A1**: 看 ptrace 能力与限制。若你有 CAP_SYS_PTRACE,或 yama 的 ptrace_scope=0(`cat /proc/sys/kernel/yama/ptrace_scope`)且你与目标同 uid,就能 attach。但注意:普通用户默认不能 ptrace root 进程(uid 不同)。判据:ptrace_scope 值 + 你是否有 cap 或是否同 uid。
**Q2**: ptrace_scope=0 但目标进程是 root、我是普通用户,attach 被拒,还有戏吗?
**A2**: uid 不同时 ptrace_scope=0 也拦不住内核的 uid 检查——普通用户 ptrace root 进程本就不允许。此路要求你要么有 CAP_SYS_PTRACE,要么先横切到与目标进程同 uid 的账户。所以先解决身份,再谈注入。
**Q3**: 我确实有 CAP_SYS_PTRACE,怎么落地成 root shell?
**A3**: attach 到一个 root 进程,注入 shellcode 或让它执行 execve/system 起你的 shell,或改它内存执行系统调用。工具化可用现成注入框架。cap_sys_ptrace 本质是"进入任意进程的地址空间",选个 root 进程当宿主即可。
**Q4**: 想注入但目标进程随时可能退出/不稳定,风险大怎么办?
**A4**: 选长期稳定的 root 守护进程当宿主(而非短命进程),且注入方式选"派生新进程"而非"劫持现有执行流",降低把关键服务搞崩的概率。红队要控崩溃面。
**Q5**: 没有 ptrace 能力也切不到同 uid,注入这条彻底不通,转哪?
**A5**: 转其它 capability(cap_setuid/cap_dac_read_search 更直接)或完全离开"进程注入"维度,回到 SUID/sudo/cron。ptrace 提权是小众路径,不通很正常,别恋战。

### /etc/ld.so.preload 与全局动态链接劫持
**Q1**: 除了 sudo 里的 LD_PRELOAD,还有没有"全局生效"的库劫持面?
**A1**: 有,查 /etc/ld.so.preload 和 /etc/ld.so.conf.d/ 的写权限。ld.so.preload 里列的 so 会被系统上每个动态链接程序加载——包括 root 跑的。若你能写这个文件,放一个恶意 so,等任意 root 进程/SUID 程序启动即以 root 执行你的构造函数。判据:这两处任一可写。
**Q2**: /etc/ld.so.preload 可写,恶意 so 怎么写才不把系统搞崩?
**A2**: so 的构造函数里要做好判断:只在 euid==0 时触发 payload、执行完恢复、避免每个进程都疯狂 spawn(会拖垮系统很显眼)。经典做法:preload 的库劫持 geteuid 之类,检测 root 上下文再动作,并尽量做成一次性。控噪音是关键。
**Q3**: ld.so.preload 不存在也不可写,但 ld.so.conf.d 可写,怎么用?
**A3**: 往 ld.so.conf.d 写一条指向你可控目录的库搜索路径,再放同名 so 覆盖某个被 root 程序加载的库(需要 ldconfig 刷新缓存——看你能否触发或等 root 触发)。比 preload 间接,但同样是全局链接劫持。
**Q4**: 这些文件都是 root only 不可写,劫持没入口,转哪?
**A4**: 缩小到"局部"链接劫持:某个具体 SUID 程序若从可写目录加载库(RUNPATH/RPATH 指向可写位、或 dlopen 相对路径),就针对它劫持。全局不行就找单点。都不行则彻底离开链接劫持维度。
**Q5**: 局部也没有可写库加载点,链接劫持整条废了,下一步?
**A5**: 转正交的执行原语:PATH 劫持(劫持命令而非库)、写 root 会读的文件(passwd/cron/authorized_keys)、或内核 exp。链接劫持只是"注入代码"的一种,注入不了就换"直接写关键文件"的思路。

### SUID 失效之谜:nosuid / no_new_privs / 只读挂载
**Q1**: 找到了完美的 SUID 提权向量,exp 却不生效、euid 始终不变,怎么回事?
**A1**: 高度怀疑挂载/内核限制。查 `mount` 看该二进制所在分区是否带 nosuid(如 /tmp、/home 常被挂 nosuid,SUID 位形同虚设)。再查进程是否处于 no_new_privs(NNP)状态——容器/systemd 加固常置位,一旦 NNP 生效,SUID 和 cap 提升全部失效。判据:mount 选项 + /proc/self/status 的 NoNewPrivs 字段。
**Q2**: 确认是 nosuid 挂载导致的,SUID 二进制在 /tmp 上跑不动,怎么办?
**A2**: 把利用挪到没有 nosuid 的分区。若某 SUID 程序在系统分区(通常 / 不带 nosuid),它照样能提;是你自己落地到 nosuid 目录的 SUID payload 才失效。所以:利用系统自带的 SUID,或把 payload 落到可写且非 nosuid 的位置。
**Q3**: 进程处于 no_new_privs=1,SUID/cap 全线失效,这几乎堵死了配置类提权,转哪?
**A3**: NNP 通常出现在容器/沙箱里——这暗示你可能在容器内。转两条线:一是容器逃逸维度(找 socket/挂载/特权容器/内核逃逸),二是不依赖权限提升的路径(直接读已挂载进来的宿主敏感文件、利用容器内 root 身份)。NNP 是"换战场"的强信号。
**Q4**: 怎么快速判断我是不是在受限的容器/命名空间里?
**A4**: 综合看:/.dockerenv、cgroup 内容、mount 里的 overlay、进程数极少、NNP=1、能力集被裁剪(capsh --print)。多个信号叠加基本确认。确认后提权思路整体从"本机 root"切到"逃逸到宿主"。
**Q5**: 既不是容器、也没 NNP,但 SUID 就是不生效,还有什么可能?
**A5**: 检查 SELinux/AppArmor 是否拦了(见 LSM 那条)、或该二进制自身 drop 了权限、或 fs.suid_dumpable 之类的怪配置。逐一排除后若仍无解,判定 SUID 面不可用,转 cron/服务/内核维度。

### Docker/containerd socket 文件暴露
**Q1**: 我不在 docker 组,但发现 /var/run/docker.sock 我能读写,这意味着什么?
**A1**: 等于 root。能访问 docker socket 就能通过 Docker API 起一个挂载了宿主 / 的特权容器,在里面对宿主文件系统为所欲为(写 authorized_keys、改 passwd、chroot 进宿主)。判据:对 docker.sock 有 rw(组成员、或 socket 权限配错、或你已在某容器内且 socket 被挂进来)。
**Q2**: 有 socket 访问权但机器上没装 docker CLI,怎么调 API?
**A2**: 直接用 curl 打 unix socket 的 HTTP API(--unix-socket),或用任何能发 HTTP 的工具构造 create/start 容器请求。CLI 只是封装,socket + HTTP 就够。判据:能发原始请求即可,不依赖 docker 二进制。
**Q3**: 我是在一个容器内发现 docker.sock 被挂进来了,和在宿主上发现有区别吗?
**A3**: 目标一致(控宿主),但这是典型的容器逃逸姿势:容器内访问宿主 docker socket → 起特权容器挂宿主根 → 逃逸。若挂进来的是 containerd/crio 的 socket,原理相同,换对应 API。
**Q4**: socket 权限严格拿不到,但我在 docker 组里,一样吗?
**A4**: 一样,docker 组成员对 socket 有权,直接 `docker run -v /:/host --privileged` 挂宿主。docker 组 == root 是公认结论。若组也不在、socket 也没权,这条才算断。
**Q5**: docker/containerd socket 都不可达,容器运行时这条路断了,转哪?
**A5**: 转其它逃逸/提权面:特权容器的 cap(cap_sys_admin 可挂 cgroup release_agent 逃逸)、挂进来的宿主目录、内核 exp(容器共享宿主内核,内核洞照样逃)。socket 只是逃逸的一种,runtime 不可达就换 cap/挂载/内核三条腿。

### 内核模块加载提权
**Q1**: 常规路都堵了,发现自己有加载内核模块的能力(CAP_SYS_MODULE 或能写 modprobe 相关),怎么用?
**A1**: 能加载 kmod 基本等于 root——内核态代码可任意改内核结构(把当前进程 cred 改成 root)。写一个恶意 .ko,insmod/modprobe 进去,在 init 里提权当前进程或 spawn root shell。判据:capsh 显示 cap_sys_module,或你能控制 /sbin/modprobe(kernel.modprobe sysctl 指向可写路径)。
**Q2**: 有 cap_sys_module 但目标机没装编译器/内核头,编译不了 .ko 怎么办?
**A2**: 在同版本内核的另一台机(或本地搭同版本环境)交叉编译好 .ko 再传进去——.ko 只要内核版本/vermagic 匹配就能加载。别在目标上编译,把编译搬到别处是常规操作(见 exp 编译那条)。
**Q3**: kernel.modprobe 这个 sysctl 指向的路径我能写,但没有 cap_sys_module,能提吗?
**A3**: 能。当内核需要加载某模块(比如触发一个未知 socket 协议族)时会调 kernel.modprobe 指的程序,以 root 执行。把它指向你的脚本,再触发模块自动加载,你的脚本就以 root 跑。这是"改 modprobe 路径"的经典利用。
**Q4**: vermagic 不匹配、模块死活加载不进,这条断了吗?
**A4**: 若能拿到目标精确内核版本和配置,理论可对齐 vermagic 重编;对不齐就放弃 kmod 路线。转其它 cap 或内核 exp(内核 exp 不需要加载模块,是打内核漏洞)。别在 vermagic 上耗太久。
**Q5**: 既无 cap_sys_module 也控不了 modprobe 路径,内核模块面无入口,转哪?
**A5**: 转内核漏洞 exp(不同机制:利用内核 bug 而非合法加载模块)或回到用户态配置面。内核模块加载是"合法但危险的功能",没这个功能就走"非法利用 bug"的内核 exp 或干脆离开内核维度。

### 框架触发类脚本提权(logrotate / motd / mail / hooks)
**Q1**: cron 和 systemd 都翻完了,还有没有别的"root 会自动跑脚本"的框架?
**A1**: 有一堆容易被忽略的:logrotate(轮转时以 root 跑,配 postrotate 脚本、且有历史 CVE 如 logrotten)、update-motd(SSH 登录时 root 跑 /etc/update-motd.d/ 下脚本)、/etc/profile.d、mail 触发的 procmail、git 仓库的 hooks、PAM 脚本、initramfs hooks。判据:任何"某事件发生→root 执行某目录下脚本"的机制,且脚本或其目录可写。
**Q2**: /etc/update-motd.d/ 下有脚本我能写,但没人 SSH 登录来触发怎么办?
**A2**: 自己制造登录(若你能 SSH 进这台、哪怕以低权用户登录,也会触发 motd 以 root 跑),或等真人登录。motd 提权的触发条件是"一次 SSH 登录",通常自己就能满足。
**Q3**: logrotate 配置或其处理的日志目录可写,怎么变提权?
**A3**: 两条:一是若能改 logrotate 配置加 postrotate,轮转时 root 执行你的命令;二是针对 logrotate 处理你可写日志时的竞态/符号链接问题(logrotten 类)构造利用。判据:能否影响 logrotate 以 root 执行的那一步。
**Q4**: 这些框架的脚本/目录全是 root only 不可写,触发面为空,转哪?
**A4**: 说明"被触发执行"这一大维度(cron+systemd+各框架)整体枯竭。转向"主动执行"维度:SUID/sudo/cap 让你自己以 root 跑,而不是等 root 来跑你的东西。两大维度正交,一边空了坚决换另一边。
**Q5**: 怎么系统性地不遗漏这些冷门触发点?
**A5**: 用"事件→执行体"的清单思维扫一遍:开机、登录、收邮件、日志轮转、包管理(apt/dpkg 的 pre/post 脚本)、备份任务、监控探针。每种事件都问"它以什么身份跑、跑哪个文件、我能写吗"。这比零散记忆各个工具名更不容易漏。

### 本地 exp 的编译与投递决策
**Q1**: 确定了要用某个本地提权 exp(内核/PwnKit/DirtyPipe),但要不要在目标机上直接 gcc 编译?
**A1**: 尽量别在目标上编译。原因:目标常没装编译器/内核头、编译产物落地噪音大、还可能触发监控。优先在本地或一台同发行版同内核版本的机器上编译好静态二进制,再传干净的可执行文件过去。判据:目标有无 gcc(`which gcc/cc`)+ 出网/落地条件。
**Q2**: 目标内核版本很特殊,本地没有对应环境编译,怎么办?
**A2**: 拉一个匹配版本的容器/VM(按 uname 精确对齐)编译,或找 exp 的预编译版本。对内核 exp 尤其要版本对齐。若实在配不出环境,再考虑目标本地编译作为退路,但接受它的噪音和失败风险。
**Q3**: 编译好了但目标不出网,exp 传不进去怎么办?
**A3**: 用已有 shell 通道传:base64 贴进去落地、通过现有 web 上传点、SCP/SFTP(若有凭据)、或把 payload 塞进已控的其它服务。传输是纯工程问题,别因为"没出网"就以为 exp 用不了——只要有任意文件写入通道就能投递。
**Q4**: exp 编译对了、传进去了,一跑却段错误/不提权,是 exp 废了吗?
**A4**: 先排环境因素再怀疑 exp:内核是否 backport 了补丁(版本号骗人)、有无 KASLR/SMEP/SMAP 等缓解、是否在容器里(NNP)、目标架构是否匹配(x86_64 vs arm)。很多"exp 不工作"是环境不符而非 exp 本身错。
**Q5**: 反复调 exp 就是不成、还几次差点把机器搞崩,该不该继续?
**A5**: 停。内核 exp 崩溃代价高(蓝屏丢立足点、暴露)。判据:同一 exp 试 2-3 次仍不稳就撤,转配置类提权(不崩)或横向。把"这台一定要内核提权"的执念换成"换更稳的路或换台机器"。

### LSM 约束(SELinux/AppArmor/seccomp)下的提权判定
**Q1**: 提权动作老是莫名被拒(能读的读不了、能执行的执行不了),怀疑有强制访问控制,先确认什么?
**A1**: 查 LSM 状态:`getenforce`/`sestatus`(SELinux)、`aa-status` 或 /sys/kernel/security/apparmor(AppArmor)。SELinux enforcing 下,即便你是 root、即便 SUID 对,策略也可能拦下操作。判据:enforcing/complain 状态 + 审计日志(/var/log/audit)里的 denied 记录。
**Q2**: 确认 SELinux enforcing 且它在拦我的提权,怎么绕?
**A2**: 找策略允许的路径:换一个 SELinux 域许可的操作/位置来达成同样目的(比如它不让你写 A 但允许写 B,而 B 也能提权)。或利用被授予宽松域的进程。SELinux 绕过靠"在策略允许的缝隙里走",而非硬顶。
**Q3**: AppArmor 给我这个进程套了 profile,限制了我能做的事,怎么办?
**A3**: profile 是按可执行路径绑定的——换一个不受该 profile 约束的可执行体来跑 payload(比如 profile 只限制了 /usr/bin/foo,那就别用 foo)。逃出 profile 约束常常只需换个"壳"进程。
**Q4**: 被 seccomp 过滤了系统调用(某些 exp 的 syscall 直接 EPERM),怎么判断和应对?
**A4**: 看 /proc/self/status 的 Seccomp 字段。被 seccomp 限制多见于容器/沙箱。应对:选不依赖被禁 syscall 的利用路径,或先逃出沙箱再提。seccomp 也是"你在受限环境里"的强信号,提示换成逃逸思路。
**Q5**: LSM 把配置类提权全拦死了,硬绕不动,转哪个维度?
**A5**: 转两处:一是内核 exp(若能在内核态改 cred 或直接改 SELinux enforcing 状态,可从底层掀翻 LSM),二是横向——受这么严策略约束的机器,本机提权性价比低,不如用现有权限做横向。别和成熟的 MAC 策略死磕。

### 提权全维度枯竭:纵向转横向的判定与执行
**Q1**: sudo/SUID/cap/cron/服务/内核/组权限全部翻遍,root 就是提不上来,怎么判定该收手转横向?
**A1**: 建立"枯竭清单":七大维度逐项标记"已排查且无货"。当七项全叉、内核 exp 因补丁/风险不可用、凭据也喷空,就判定本机纵向枯竭。此时继续磕的边际收益趋近于零,应把这台正式定位成"立足点/跳板"而非"要 root 的目标"。判据:是维度真排完了,还是只是某一维没深挖——先自查后者。
**Q2**: 转横向前,当前这个低权账户能给横向提供什么?
**A2**: 盘点手上资产:内网可达性(这台能连到哪些提不了权时够不着的主机/网段)、已翻到的凭据、known_hosts/history 暴露的信任关系、挂载进来的共享、这台在业务里的角色。低权 shell 常常已经是打内网的完美跳板,root 与否不影响它中转流量。
**Q3**: 具体第一步横向动作做什么?
**A3**: 以这台为 pivot:搭隧道/代理(把内网暴露给你的工具链)、用已有凭据向内网喷洒(SSH/DB/web/SMB),扫内网找一台提权面更好的机器。战役目标(域控/关键数据)常不在你卡住的这台上,换台入口往往柳暗花明。
**Q4**: 会不会是我"以为枯竭"其实漏了东西?怎么避免过早放弃?
**A4**: 转横向前做一次交叉验证:跑一遍自动化枚举(linpeas)补手动盲区、重看 id 附加组、重看 pspy 有无遗漏的时间维度任务、确认凭据搜集覆盖了所有家目录/配置/备份。用不同方法复查一遍,再宣布枯竭。防止把"没挖够"误判成"没有"。
**Q5**: 横向也暂时打不开局面,是不是整条线都死了?
**A5**: 不是。回到战役全局:也许别的初始立足点、别的入口更有价值。红队讲多点开花——一台卡死不代表战役卡死。把这台维持成稳定 pivot,精力转移到其它攻击面/其它主机,保留随时回来的能力。及时换战场是能力,不是认输。
