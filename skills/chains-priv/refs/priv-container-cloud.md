# 提权立足点 · 容器/云环境逃逸与提权判断

### 拿到 shell 第一步:我到底在不在容器里
**Q1**: 刚打进一个 Linux 环境拿到低权 shell,我该先花力气打内核提权,还是先判断这是不是容器?
**A1**: 先判断环境形态,方向错了后面全是无用功。判据从轻到重:`cat /proc/1/cgroup` 里出现 docker/kubepods/lxc 字样;`ls -la /.dockerenv` 或 `/run/.containerenv` 存在;`hostname` 是随机 hex;`mount` 里 overlay 作为根文件系统;`cat /proc/1/comm` 是应用进程而非 systemd/init。任一命中基本就是容器。
**Q2**: 如果 cgroup 已被 cgroup v2 统一层级、看不出明显字样怎么办?
**A2**: 转正交信号:进程数极少(`ps aux` 只有寥寥几个)、没有常见系统服务、`/proc/1/root` 指向异常、`capsh --print` 显示 capability 被裁剪、`nsenter` 不可用。这些"环境很干净很小"的味道本身就是容器指纹。
**Q3**: 判断是容器之后,提权目标该怎么重新定义?
**A3**: 目标从"root in this box"升级为两层:一是容器内 root(为逃逸做准备),二是宿主机 root(真正的立足点)。很多容器进来就是 root,别浪费时间在容器内提权,直接查逃逸面。
**Q4**: 如果发现自己在容器且已经是 root,下一步查什么?
**A4**: 立刻枚举逃逸四要素:capabilities(`capsh --print`)、挂载(`mount`、`cat /proc/mounts` 找 hostPath/docker.sock)、设备(`ls /dev` 找 /dev/sda 之类块设备)、是否 privileged。这四条任一松动就有逃逸路。
**Q5**: 如果确认是宿主机裸机(非容器)呢?
**A5**: 那走传统 Linux 提权链:SUID→sudo→cron→capability→内核。不要再纠结逃逸概念,把精力放在配置错误优先、内核 exploit 兜底。
**Q6**: 这条路彻底卡住——既不像标准容器也提不了权,往哪转?
**A6**: 转横向而非纵向:当前权限下枚举本机可达的其他服务、凭据文件(`~/.aws`、`~/.kube/config`、history、env)、内网可达主机。提权不是唯一出路,拿到一份高价值凭据往往比死磕本机 root 更快立足。

### 识别容器运行时类型决定逃逸手法
**Q1**: 确认在容器里了,但 docker/containerd/CRI-O/k8s pod 手法差异很大,怎么快速定型?
**A1**: 看进程与 socket。`/proc/mounts` 里有 `/var/run/secrets/kubernetes.io` → 这是 k8s pod;能访问 `/var/run/docker.sock` → docker;环境变量里有 `KUBERNETES_SERVICE_HOST` → k8s。定型后才知道该找 SA token 还是 docker API。
**Q2**: 既是 k8s pod 又想逃逸,优先级怎么排?
**A2**: k8s 场景优先查 serviceaccount token(`/var/run/secrets/.../token`)+ RBAC 权限,因为控制面路径往往比内核逃逸更稳更隐蔽。内核/挂载逃逸作为 token 权限不足时的备选。
**Q3**: 发现是 rootless container(userns 重映射)怎么办?
**A3**: rootless 下容器内 root 其实是宿主机的普通 uid,很多逃逸失效。判据:`cat /proc/self/uid_map` 非 `0 0 4294967295`。此时放弃"容器 root=宿主 root"的幻想,转向挂载点、socket 或宿主机侧漏洞。
**Q4**: 运行时判断不出来、socket 都没挂,是不是就死路了?
**A4**: 不是。转向 capabilities 与内核维度——即便没有任何挂载暴露,只要 CAP_SYS_ADMIN/CAP_SYS_MODULE 在,或宿主内核有可利用 CVE,依然能逃。先 `capsh --print` 再 `uname -r` 比对已知逃逸 CVE。
**Q5**: 如何避免在错误的运行时手法上浪费时间?
**A5**: 设一个 10 分钟枚举窗口:token、mounts、caps、socket、内核版本五项拉平看一遍,再决定主攻方向,而不是逮住一个手法一条道走到黑。

### privileged 特权容器判断与利用
**Q1**: 怀疑当前容器是 privileged,怎么确认?
**A1**: 特权容器几乎"全 capability + 能看到宿主设备"。判据:`capsh --print` 显示接近全量 cap;`ls /dev` 里出现 sda/nvme 等宿主块设备;`cat /proc/1/status` 里 CapEff 是 `0000003fffffffff` 一类满值。命中即 privileged。
**Q2**: 确认 privileged 后最直接的逃逸路是什么?
**A2**: 最干净的一条:直接挂载宿主根磁盘。`mkdir /mnt/host && mount /dev/sda1 /mnt/host`,然后往宿主的 cron、authorized_keys、SUID 二进制写入,或直接 chroot。有块设备可见时这比 cgroup 技巧更稳。
**Q3**: 块设备看不到但 privileged,还有别的路吗?
**A3**: 有,走 cgroup release_agent 逃逸(需 CAP_SYS_ADMIN,privileged 天然满足):在容器内挂一个 cgroup 控制器,写 release_agent 指向一个在宿主执行的脚本,触发进程退出即以宿主 root 执行。
**Q4**: 我不确定磁盘分区名(sda1?vda?),盲挂会不会搞坏?
**A4**: 先 `cat /proc/partitions` 或 `lsblk`(如有)确认分区,再只读挂载 `mount -o ro` 侦察,确认是宿主根后再读写。绝不对不明设备直接写,避免破坏目标。
**Q5**: privileged 逃逸命令执行了却没反应/被拦,往哪转?
**A5**: 转 AppArmor/seccomp 维度排查——即便 privileged,宿主也可能叠加了 LSM 限制。看 `cat /proc/self/attr/current`。若被 LSM 挡,转向 docker.sock(若挂载)或 SA token 等控制面路径,绕开内核态动作。

### CAP_SYS_ADMIN 等危险 capability 的利用选择
**Q1**: 非 privileged,但 `capsh --print` 显示有 CAP_SYS_ADMIN,能干嘛?
**A1**: CAP_SYS_ADMIN 是"半个 root",最经典是 cgroup release_agent 逃逸和挂载操作。先确认 cgroup 是否可挂(v1 更好利用),再走 release_agent 链。这是不依赖内核 CVE 的稳妥逃逸。
**Q2**: 有 CAP_SYS_MODULE 呢?
**A2**: 那是直逃:可以 `insmod` 加载恶意内核模块,在宿主内核态执行任意代码。构造一个极简 LKM,init 里跑反弹或写宿主文件。前提是宿主内核版本/编译环境匹配,先 `uname -r` 对齐。
**Q3**: 有 CAP_DAC_READ_SEARCH 或 CAP_DAC_OVERRIDE 但没 SYS_ADMIN?
**A3**: DAC_READ_SEARCH 可配合 open_by_handle_at 做宿主任意文件读(shocker 类手法),偷宿主敏感文件/凭据;DAC_OVERRIDE 能无视文件权限位读写。这类不是直接执行,但足以拿凭据再横向。
**Q4**: 有 CAP_NET_ADMIN/CAP_NET_RAW,对提权有用吗?
**A4**: 对本机提权帮助有限,但对横向极有用:可嗅探、ARP 欺骗、改路由做中间人。当纵向逃逸卡住,这就是转向"劫持宿主/邻居流量拿凭据"的入口。
**Q5**: 一堆 cap 都在但每条利用都失败,如何判断是不是被 seccomp 阻断?
**A5**: capability 允许≠系统调用允许。seccomp 会挡掉 mount、init_module、open_by_handle_at 等关键 syscall。写个小程序单独调用目标 syscall 看是否 EPERM/返回 -1,确认是 seccomp 挡的,再转其他不受限的 cap 或挂载路径。

### docker.sock 挂进容器的利用
**Q1**: `mount` 里看到 `/var/run/docker.sock` 挂进了容器,这意味着什么?
**A1**: 基本等于宿主 root。docker.sock 是 Docker 守护进程的控制入口,能通它创建任意容器。先测 `curl --unix-socket /var/run/docker.sock http://localhost/version` 或有 docker 客户端就 `docker -H unix://... ps`。
**Q2**: 容器内没装 docker 客户端怎么办?
**A2**: 直接用 curl 走 HTTP API:POST /containers/create 建一个挂载宿主根 `/` 到 /host、且 privileged 的新容器,再 start、exec。全程 REST,不需要客户端。API 端点是稳定的公共接口。
**Q3**: 具体怎么落地拿宿主 root?
**A3**: 新容器 HostConfig 里 Binds 设 `["/:/host"]`,启动后在其中 chroot /host 即宿主 root 视角,往 cron/authorized_keys/passwd 写入或直接反弹。核心是用 docker 的合法能力把宿主盘挂给自己。
**Q4**: docker.sock 请求返回 403/权限拒绝,是不是没戏了?
**A4**: 可能有 authz 插件或 socket 权限限制。转向排查同类 socket:containerd.sock、crio.sock、podman.sock,或 k8s 场景的 kubelet。一个被锁不代表全被锁,横向枚举所有 runtime socket。
**Q5**: 什么都没挂但我知道宿主跑 docker,怎么转?
**A5**: 转网络维度:探测 Docker 远程 API 端口(2375/2376)是否在内网可达。未加密的 2375 暴露=远程无认证控制。这把"本地无 socket"问题转成"内网服务暴露"问题。

### hostPath / 宿主目录挂载的利用
**Q1**: `mount` 显示某个宿主目录被挂进容器(比如 /data、/var/log),怎么评估价值?
**A1**: 看挂载点是否触及"能改行为的路径"。挂了 `/` 或 `/etc`、`/root`、`/var/spool/cron` 是致命的;挂 `/var/log` 也可能通过写宿主进程会读的日志/配置间接利用。先 `mount | grep -v overlay` 全量看挂载源。
**Q2**: 挂进来的是宿主整个 `/` 或 `/host`,直接怎么用?
**A2**: 直接写宿主提权点:root 的 authorized_keys、`/etc/cron.d/` 放定时任务、给某宿主二进制加 SUID(chmod u+s)、或改 `/etc/passwd` 加 root 用户。任选一条最隐蔽的。
**Q3**: 只挂了一个业务目录、非敏感路径,还有戏吗?
**A3**: 有,查这个目录里宿主进程是否会加载其中文件:配置、脚本、被 cron 引用的东西、web 根目录。往这些"宿主会主动读/执行"的文件注入,借宿主进程的手执行。挂载点不敏感不等于不可利用,要看数据流向。
**Q4**: 挂载是只读(ro)的,写不进去怎么办?
**A4**: 只读就转"读"的价值:里面有没有宿主凭据、密钥、token、配置里的口令。ro 挂载常暴露 secret。读到凭据后转横向/控制面提权,别死磕写。
**Q5**: hostPath 是 k8s 场景,但当前 pod 没挂敏感路径,怎么转?
**A5**: 转 RBAC:如果 SA 有创建 pod 权限,自己造一个挂 hostPath `/` 的 pod 调度到目标 node。把"我这个 pod 没挂"变成"我有权造一个挂了的 pod"。控制面权限 > 当前 pod 配置。

### /proc、/sys 暴露与 core_pattern / release_agent
**Q1**: 容器里 `/proc` 或 `/sys` 是宿主的、可写,怎么利用?
**A1**: 可写 `/proc/sys/kernel/core_pattern` 是经典:写成 `|/path/to/evil` 让宿主在进程崩溃时以 root 执行你的程序,再故意触发 core dump。前提是 /proc 未被容器重新挂载遮蔽。
**Q2**: core_pattern 那条要触发 crash,怎么稳定触发?
**A2**: 在容器内跑一个必崩的小程序(比如故意段错误),core_pattern 的管道处理是在宿主 init 命名空间执行的,所以你的 handler 拿宿主 root。确认 handler 路径在宿主可见(结合挂载)。
**Q3**: `/sys/fs/cgroup` 可写呢?
**A3**: 走 release_agent:notify_on_release + release_agent 组合,进程退出时宿主执行指定脚本。与 CAP_SYS_ADMIN 那条同源,区别在这里是靠 /sys 挂载暴露而非 cap。两条判据独立,任一满足即可。
**Q4**: core_pattern 写不进(EACCES),是不是没戏?
**A4**: 说明 /proc 被以只读或重新挂载保护了。转 release_agent(走 /sys)或反过来查 uevent_helper(`/sys/kernel/uevent_helper`)——同样是"内核回调宿主执行"的族,换一个未被保护的入口。
**Q5**: /proc、/sys 全被容器安全挂载遮蔽了,整族失效,往哪转?
**A5**: 转 capability + 内核 CVE 组合:遮蔽 /proc/sys 挡的是"配置型逃逸",挡不住内核内存破坏型 exploit。查 `uname -r` 比对 dirtypipe/dirtycow/OverlayFS 等宿主内核 CVE。

### 内核 CVE 逃逸与 runc/containerd CVE 的取舍
**Q1**: 配置层面找不到逃逸口,准备上内核 CVE,先做什么?
**A1**: 先精确取内核版本 `uname -r` 和发行版,再比对已知逃逸类 CVE(如 DirtyPipe、DirtyCOW、OverlayFS 提权、以及 runc 的 CVE-2019-5736 覆写 runc)。别盲打 exploit,先确认版本落在受影响区间。
**Q2**: 内核 exploit 有翻车风险(panic),要不要打?
**A2**: 权衡:内存破坏型 exploit 有崩机概率,红队重隐蔽时应把它排在配置型逃逸之后。若已无其他路且授权允许,选成熟稳定的 PoC,先在同版本环境验证再上目标。
**Q3**: CVE-2019-5736 这类"覆写 runc"的手法适用条件是什么?
**A3**: 需要在容器内能诱导一次 `docker exec`/attach,利用时机覆盖宿主 runc 二进制。判据:目标运维会进容器操作、runc 版本在受影响范围。若是无人交互的静默容器,这条时机不成立,换路。
**Q4**: exploit 编译不了(容器内无 gcc/无网络)怎么办?
**A4**: 转"外面编译、里面运行":在同版本环境静态编译好二进制,通过已有通道传入。或选择纯脚本/无需编译的 PoC。编译环境缺失不该成为放弃 exploit 的理由,是传输问题。
**Q5**: 打了 exploit 没提权、也没崩,如何判断失败原因?
**A5**: 逐项排除:内核有无补丁(即便版本号在范围也可能已 backport 修复)、seccomp 是否挡了 exploit 用的 syscall、KASLR/保护是否生效。确认是缓解措施挡的,就转配置型或控制面路径,别在同一 exploit 上反复试。

### Linux SUID 提权枚举与判读
**Q1**: 裸机低权 shell,开始查 SUID,命令和判读思路是什么?
**A1**: `find / -perm -4000 -type f 2>/dev/null`。逐个对照:是不是 GTFOBins 里已知可提权的(带 shell 逃逸的 find/vim/nmap 老版本/env 等),或是不是自研 SUID 二进制(更可能有注入/路径问题)。系统自带正常 SUID 忽略。
**Q2**: 列表全是标准系统二进制,没有明显 GTFOBins 项,怎么办?
**A2**: 转两个方向:一是查版本(某些标准二进制老版本有已知 SUID 提权 CVE);二是重点看非标准路径下的 SUID(/opt、/usr/local、家目录)——那些是运维自己加的,审计其调用的外部命令、相对路径、参数注入。
**Q3**: 发现一个自研 SUID 程序,怎么找它的利用点?
**A3**: 看它调用什么。`strings`/`ltrace` 看是否用 system()/popen() 调外部命令且用相对路径或可控参数→PATH 劫持或参数注入;是否读可控配置文件;是否有缓冲区问题。核心是"它以 root 身份信任了什么可被我控制的东西"。
**Q4**: SUID 程序调用了外部命令但用了绝对路径、参数也不可控,卡住了,转哪?
**A4**: 转 capability 文件(`getcap -r / 2>/dev/null`)——有些提权点不是 SUID 位而是文件 capability(比如某二进制带 cap_setuid)。SUID 一条线走死,capability 是并行独立的一条线。
**Q5**: SUID 和 capability 都干净,还有哪些"以 root 运行"的入口?
**A5**: 转向 root 拥有的运行中进程与服务:cron、systemd 单元、以 root 监听的本地服务(数据库、消息队列)。提权本质是"借一个 root 上下文",SUID 只是其中一类载体。

### sudo 配置错误提权
**Q1**: `sudo -l` 能跑,列出了可免密执行的命令,怎么判读?
**A1**: 找"能派生 shell 或读写任意文件的命令":允许 sudo 跑 vi/less/awk/find/env/tcpdump 等 → GTFOBins 直接逃逸出 root shell;允许跑某脚本 → 看脚本是否可被我改或调用可控内容。
**Q2**: sudo 允许跑的是一个具体二进制、参数还带了限制,怎么绕?
**A2**: 看能否用通配符/相对路径钻空:规则里 `*` 常可被追加参数利用;若命令支持加载配置/插件/预加载,可注入。也留意 sudo 版本本身(有过 sudoedit、Baron Samedit 一类堆溢出 CVE),`sudo --version` 比对。
**Q3**: sudo 版本在 Baron Samedit(CVE-2021-3156)范围,但不确定是否已打补丁?
**A3**: 直接用无损探测方式验证漏洞是否存在(触发不崩的判定路径),确认后再上完整 exploit。别一上来打完整 PoC 冒崩机风险。
**Q4**: `sudo -l` 需要密码而我没有密码,怎么办?
**A4**: 转向不需要已知密码的路径:sudo 版本 CVE(Baron Samedit 无需密码)、或彻底放弃 sudo 线,回到 SUID/cron/capability/内核。`sudo -l` 要密码不代表 sudo 维度全废,只是免密清单这条废了。
**Q5**: env_keep 里保留了 LD_PRELOAD/LD_LIBRARY_PATH 怎么用?
**A5**: 那是直提:构造恶意 .so,导出被调用的函数(如 geteuid),sudo 执行任意允许命令时 LD_PRELOAD 加载它以 root 运行。前提是 sudoers 有 env_keep+=LD_PRELOAD 或 Defaults 未清理,`sudo -l` 输出里能看到。

### cron / 定时任务提权
**Q1**: 想查 cron 提权,先看哪里?
**A1**: `cat /etc/crontab`、`ls -la /etc/cron.*`、`/var/spool/cron/`,重点找 root 跑的任务里"引用了我能写的文件/目录/脚本",或用了通配符、相对路径。系统默认任务忽略。
**Q2**: 发现 root cron 跑了一个脚本,但脚本我改不了,怎么办?
**A2**: 看脚本内部:是否 cd 到某目录后用通配符(`tar *`、`chown *` 通配符注入)、是否 source 一个我可写的配置、是否调 PATH 里我能劫持的命令。root 不可写≠链条上每一环都不可写。
**Q3**: 看不到 crontab 内容(权限不足)但怀疑有 cron,怎么确认?
**A3**: 转旁路观测:用 pspy 一类无权限进程监控工具看周期性以 root 启动的进程,反推 cron 任务和它调用的文件路径。看不到配置就看行为。
**Q4**: cron 任务都很干净、无可写引用,转哪?
**A4**: 转其他"周期性 root 执行"载体:systemd timer、anacron、以及应用自带的定时逻辑。cron 只是定时提权的一种,timer 常被忽略。
**Q5**: 我写入了 cron 载荷但迟迟不触发,如何排查?
**A5**: 检查时间字段是否理解错、cron 服务是否在跑(`systemctl status cron`)、载荷是否有语法错误让整条任务失败。若确实不可控触发,转即时性更强的路径(可写 service 立刻重启、SUID),别干等。

### 可写 systemd unit / service 提权
**Q1**: 怀疑有可写的服务文件能提权,怎么定位?
**A1**: 找权限松的 unit:`find /etc/systemd/ /lib/systemd/ -writable 2>/dev/null`,以及查 root 服务的 ExecStart 指向的二进制/脚本是否我可写。命中"root 服务加载的东西我能改"即可。
**Q2**: unit 文件本身可写,怎么落地?
**A2**: 改 ExecStart 为你的载荷,然后触发重启该服务(`systemctl restart`,若无权就等宿主重启或找能重启它的途径)。要点是能否触发重载,不然改了也不生效。
**Q3**: unit 不可写但它 ExecStart 的脚本可写呢?
**A3**: 那更直接,改脚本内容即可,下次服务启动以 root 跑。判据始终是"root 上下文最终执行的那个文件谁能写",顺着 ExecStart→脚本→脚本内引用一层层往下找可写点。
**Q4**: 我没权 restart 服务、宿主也不会很快重启,卡住了怎么办?
**A4**: 转即时触发的提权面:SUID、sudo、能立刻触发的 socket 服务。service 提权受"何时重启"制约,当触发不可控时它优先级下降,换一个我能主动触发的载体。
**Q5**: 能创建新 unit 但不能改现有的,有用吗?
**A5**: 有用,如果我有权 `systemctl link`/enable 并 start 一个新 unit,就等于直接以 root 执行。前提是当前用户对 systemctl 有相应授权(常配合 sudo/polkit 错误配置),先测能不能 start。

### NFS no_root_squash 提权
**Q1**: 发现本机或内网有 NFS 导出,怎么判断能否用于提权?
**A1**: 关键看导出选项有没有 `no_root_squash`。`cat /etc/exports`(本机)或 `showmount -e <server>`(远程)。no_root_squash 意味着客户端 root 写入的文件在服务端仍是 root 属主,可植入 SUID。
**Q2**: 确认 no_root_squash 后怎么提权?
**A2**: 在一台我有 root 的机器上挂载该导出,放一个 SUID 的 shell 二进制(chown root、chmod 4755),再到目标机上执行它即得 root。核心是借 NFS 把"我的 root 属主"投影到目标。
**Q3**: 我在目标机上没有 root,无法在别处造 SUID,怎么办?
**A3**: 转链式:先用别的手段拿到任一台可挂载客户端的低成本 root(或用容器/本地虚机),再走 NFS。若完全没有可控 root 端,这条 no_root_squash 用不上,回退其他提权面。
**Q4**: showmount 被防火墙挡、看不到导出列表,是不是没戏?
**A4**: 不一定。转向直接探测:mountd/nfs 端口是否开,尝试已知导出路径盲挂。showmount 被挡只是枚举受限,服务本身可能仍可挂载。
**Q5**: 导出带 root_squash(有 squash),彻底没用吗?
**A5**: 提权用途受限,但转数据价值:仍可读写普通文件,若导出里有其他用户可读的凭据、密钥、备份,照样能拿来横向。提权失败不等于该 NFS 无价值。

### Kubernetes ServiceAccount token 的发现与用法
**Q1**: 确认在 k8s pod 里,先怎么拿 SA token?
**A1**: `cat /var/run/secrets/kubernetes.io/serviceaccount/token`,同目录有 ca.crt 和 namespace。这是 pod 默认挂载的身份。拿到后用它对 API server 认证,能干什么取决于绑定的 RBAC。
**Q2**: 有了 token,怎么快速探这个 SA 的权限边界?
**A2**: `kubectl auth can-i --list`(带 token)或直接 curl API server 的 SelfSubjectRulesReview。重点看有没有 create pods、pods/exec、get secrets、create clusterrolebinding 这类高危动词。
**Q3**: token 权限很小(啥都 can-i not),是不是废了?
**A3**: 先别丢。转两点:一是枚举其他 namespace/pod 是否挂了更高权 SA(拿到别的 pod 就换身份);二是这个小权限里有没有 get secrets——即便不能建 pod,能读 secret 也可能拿到更强凭据。
**Q4**: SA 有 create pod 权限,怎么升到 node/宿主?
**A4**: 造一个 privileged 或挂 hostPath `/` 的 pod,nodeName 指定到目标节点,进去后 chroot 宿主盘即 node root。有建 pod 权限基本等于有全 node 的 root,这是 k8s 里最高性价比的提权。
**Q5**: API server 用 token 请求返回 401/403,怎么排查?
**A5**: 401 是 token 无效(过期/被删/挂载的是投影短期 token 需刷新)→重新读文件;403 是认证过但 RBAC 不允许→回到枚举权限、换 SA。区分 401/403 决定是"换 token"还是"换权限路径"。
**Q6**: API server 网络不可达呢?
**A6**: 转 kubelet(见专条)或本地逃逸。控制面够不着时,pod→node 的本地逃逸(hostPath/privileged/内核)就是替代路线。别在够不着的 API 上耗。

### k8s RBAC 枚举到提权路径的推理
**Q1**: 拿到一个中等权限的 SA,怎么系统地找提权链而不是瞎试?
**A1**: 把 RBAC 当图来走。列出我能 create/update 的资源,问每个:"它能否让我获得更高身份或代码执行?"典型高危边:create pods(选任意 SA 挂上)、create rolebinding/clusterrolebinding(自绑 admin)、update 某控制器(接管其 SA)、escalate/bind 动词。
**Q2**: 有 create rolebinding 但没有 escalate 权限,能自我提权吗?
**A2**: 受限。RBAC 有防提权设计:不能绑定超出自己权限的 role,除非有 escalate/bind 显式授予。判据:先看有没有 bind 到已存在高权 ClusterRole 的口子。若被 escalate 检查挡住,转 create pods+挂高权 SA 这条绕过路径。
**Q3**: 能 create pod 但不能指定任意 SA,怎么办?
**A3**: 转"找命名空间里已有的高权 SA"并把 pod 的 serviceAccountName 设成它——只要我能在该 ns 建 pod 且该 SA 存在,通常就能借用它的身份。前提是我对该 SA 无需额外 use 限制(检查是否有 admission 限制)。
**Q4**: 所有直接提权动词都没有,只有一堆 get/list,转哪?
**A4**: 转情报:list secrets/configmaps 找硬编码凭据、list pods 看别的 pod 挂了什么、get nodes 找版本比对 CVE。只读权限的价值是"找出下一个可攻击对象",把 RBAC 死路转成侦察成果。
**Q5**: 怎么判断当前是不是已经到顶(cluster-admin)?
**A5**: `can-i '*' '*' --all-namespaces` 返回 yes 即到顶。到顶后目标从提权转为立足固化与横向(拿 etcd、拿各节点、拿云凭据),别再纠结 RBAC。

### kubelet API(10250)未授权利用
**Q1**: pod 到不了 API server,但能连到 node 的 10250,怎么用?
**A1**: kubelet 只读/读写 API 若配置了 anonymous-auth 或授权松,可直接列 pod 并 exec。先 `curl -sk https://<node>:10250/pods` 看能否无认证列出 pod。能列即大概率能 exec。
**Q2**: 能列 pod 后怎么拿到代码执行/凭据?
**A2**: 用 kubelet 的 exec 接口在目标 pod 里执行命令(选一个高权 SA 的 pod),读它的 token 再回打 API server,或直接在敏感 pod 里操作。等于把 kubelet 当跳板拿更强身份。
**Q3**: 10250 返回 401/Unauthorized 怎么办?
**A3**: 说明开了 Webhook 授权。转 10255(只读 kubelet,常无认证)看能否泄露 pod 列表/环境变量里的凭据;或回到本地逃逸。10250 锁了不代表整个 node 面板锁了。
**Q4**: 10255 也关了、10250 也要认证,这条整体废了吗?
**A4**: 是,kubelet 路废。转正交维度:本 pod 的本地逃逸(hostPath/privileged/caps/内核),或云 metadata 服务拿 node 的云身份。node 的攻击面不止 kubelet 一个端口。
**Q5**: 怎么判断值不值得为 kubelet 花时间?
**A5**: 先一条 curl 探 /pods 的响应即可定性:无认证列出=高价值必打;需认证=顺手试 10255 后即转向,不恋战。

### 云 metadata 服务(169.254.169.254)利用
**Q1**: 打进一台云上主机/容器,第一时间该不该打 metadata 服务?
**A1**: 该,这是云环境提权的头号入口。`curl http://169.254.169.254/latest/meta-data/`(AWS)或对应厂商路径。能返回即可枚举实例角色、临时凭据。它常是从"主机权限"跳到"云身份"的桥。
**Q2**: 拿到 metadata 里的临时凭据后怎么用?
**A2**: AWS 下取 `iam/security-credentials/<role>` 得 AccessKey/SecretKey/Token,配到本地 aws cli 或直接调 API。此时你的权限=该实例角色的 IAM 权限,下一步枚举这个角色能干什么。
**Q3**: 目标是 AWS 且 metadata 返回 401,像是 IMDSv2,怎么办?
**A3**: IMDSv2 需要先 PUT 拿 token 再带 token GET:先 `curl -X PUT .../latest/api/token -H "X-aws-ec2-metadata-token-ttl-seconds: 21600"`,再用返回的 token 作为 header 请求。401 往往只是没走 v2 握手,不是真不可达。
**Q4**: metadata 完全不可达(被 iptables/hop-limit 或代理挡),转哪?
**A4**: 转其他云身份来源:主机上的 `~/.aws/credentials`、环境变量里的云凭据、k8s 的 IRSA/workload identity 投影 token、CI/CD 注入的密钥文件。metadata 只是云身份的一个入口,凭据常也落在磁盘和环境里。
**Q5**: 这是容器,metadata 拿到的是 node 的角色还是 pod 的?
**A5**: 默认是 node 的实例角色(权限往往更大),除非集群用了 IRSA/Workload Identity 给 pod 单独身份。判断:有没有投影的 web identity token。能拿 node 角色通常比 pod 角色更值,优先。

### 云 IAM 权限枚举与提权
**Q1**: 拿到一组云凭据,怎么判断它能不能提权、能横向到哪?
**A1**: 先 `aws sts get-caller-identity` 确认身份,再枚举权限。不要盲调,先看有没有 iam:ListAttachedRolePolicies 等自省权限;没有就靠"试探性调用+报错信息"推断边界。目标是找到 IAM 提权原语。
**Q2**: 常见的 IAM 提权原语有哪些要重点找?
**A2**: iam:CreatePolicyVersion(改自己策略)、iam:PassRole+相关服务(把高权角色传给 lambda/ec2/glue 让它替你执行)、iam:AttachUserPolicy(自附 AdminAccess)、更新 lambda 代码、UpdateAssumeRolePolicy(改信任策略让自己可 assume 高权角色)。命中任一即可垂直提权。
**Q3**: 有 PassRole 但不确定能传给哪个服务,怎么试?
**A3**: 枚举可 pass 的高权角色,再找一个我有 create/invoke 权限的服务(lambda/ec2/ecs)去承接。PassRole 单独无用,必须配一个"愿意替我执行且能被我控制"的服务落地。
**Q4**: 枚举时疯狂报 AccessDenied,权限太小,转哪?
**A4**: 转数据面而非控制面:这组小权限能不能读 S3 桶、Secrets Manager、SSM Parameter Store、DynamoDB——那里常存着更高权的凭据/口令。IAM 提不动就去翻密钥仓库,链式换更强身份。
**Q5**: 怎么避免枚举动作触发 GuardDuty/CloudTrail 告警?
**A5**: 红队重隐蔽时,减少高噪动作(大规模 List、失败调用会留 AccessDenied 记录),优先用被动信息(metadata、磁盘凭据、已知服务),用最少的 API 调用锁定一条提权链后精准下手,而非全量爆破权限。

### 泄露云凭据后的横向与持久化判断
**Q1**: 拿到长期 AccessKey(非临时),和临时凭据处理上有什么不同?
**A1**: 长期 key 无过期、更适合持久化,但也更受监控。先判断它是用户还是角色的:用户 key(AKIA 开头)可长期用;角色临时凭据(ASIA)会过期需刷新。定性后决定是"稳住持久访问"还是"抓紧时间在 TTL 内行动"。
**Q2**: 想在云上建持久化后门,哪些动作性价比高又相对隐蔽?
**A2**: 优先低噪:给现有用户加一个访问密钥、创建一个不起眼的 IAM 用户、或改某 role 信任策略让外部账号可 assume。避免建明显的 AdministratorAccess 用户。要点是混入正常资源命名。
**Q3**: 这组凭据被禁用/轮换了,访问突然全 403,怎么办?
**A3**: 转其他已收集的身份:之前 dump 的其他 key、metadata 可再取的临时凭据、其他主机上的凭据文件。单点被吊销时,凭据的"冗余来源"就是你的韧性,所以前期要多点采集。
**Q4**: 怎么判断该横向到哪个云服务?
**A4**: 顺着这个身份"能读能改"的资源走:能读 S3/Secrets→取数据与二级凭据;能改 Lambda/EC2 userdata→代码执行;能操作 IAM→提权。按"离目标数据/更高权限最近"排序,而不是逮着一个服务猛打。
**Q5**: 是多云或混合(云凭据+落地主机),优先哪边?
**A5**: 看目标价值密度。云控制面一旦拿到高权 IAM,往往能横扫大量资源,通常优先;但若目标数据在特定主机/内网,则以云身份为跳板打回内网。两者互为杠杆,别只盯一边。

### Windows 提权:令牌与服务权限
**Q1**: Windows 上拿到低权 shell(如 IIS/服务账户),先枚举什么?
**A1**: 先看当前身份和特权:`whoami /priv`、`whoami /groups`。服务账户常带 SeImpersonatePrivilege/SeAssignPrimaryToken——这直接指向 Potato 系列令牌提权(以 SYSTEM 执行)。有这特权,提权基本稳。
**Q2**: 有 SeImpersonatePrivilege,怎么落地?
**A2**: 走 Potato 家族(RoguePotato/PrintSpoofer/GodPotato 等,视 Windows 版本和是否有 DCOM/Spooler 而定),诱导一个 SYSTEM 令牌来 impersonate。选哪个看目标版本与可用触发面,先探哪个组件可用。
**Q3**: whoami /priv 里那些特权都是 Disabled,还能用吗?
**A3**: 能。Disabled 只是当前未启用,提权工具会自行启用它;真正没有(Not listed)才是没有。别被 Disabled 误导放弃 SeImpersonate 这条金路。
**Q4**: 没有任何令牌类特权,转哪?
**A4**: 转配置面:服务权限(可改的服务、未引用服务路径)、AlwaysInstallElevated、计划任务、可写的自启目录、DLL 劫持、未打补丁的内核 CVE。令牌路走死就走 Windows 提权的另外几条经典线。
**Q5**: 怎么快速摸清 Windows 提权全貌而不遗漏?
**A5**: 跑 WinPEAS/PrivescCheck 一类枚举脚本做基线,再人工核对高价值项。自动枚举给面,人工判读定点,避免手工逐条查漏项。

### Windows 未引用服务路径 / 可改服务提权
**Q1**: 想查 Windows 服务类提权,先看哪些?
**A1**: 三类:未引用服务路径(Unquoted Service Path 且路径含空格)、服务二进制/目录可写、服务配置可改(可改 binPath)。`sc qc <svc>` 看路径与账户,配 accesschk 看权限。目标是找"以 SYSTEM 运行且某环节我可控"的服务。
**Q2**: 找到一个 unquoted path 且中间目录我可写,怎么用?
**A2**: 在空格截断点放同名 exe(比如 `C:\Program.exe`),服务启动时会先尝试执行它,以服务账户(常 SYSTEM)运行。前提是我对那个上级目录有写权且能触发服务重启。
**Q3**: 能改服务 binPath 但不能重启服务,怎么办?
**A3**: 看服务启动类型:若是 Auto 可等重启,或找能触发它的方式;若我有 SERVICE_START 权限就直接 `sc start`。改了 binPath 却触发不了,就转一个我能立即触发的项(可写自启、计划任务)。
**Q4**: accesschk 显示服务都锁得很死,转哪?
**A4**: 转 DLL 劫持:找 SYSTEM 进程/服务加载的、位于可写目录或按搜索顺序可被我抢先的 DLL。服务配置本身锁死,但它加载的 DLL 路径可能没锁。
**Q5**: 怎么判断这台机器值不值得深挖服务提权?
**A5**: 先自动枚举列出所有"可写路径的 SYSTEM 服务"和 unquoted path,若一条都没有就快速转令牌/内核/凭据面。服务提权高度依赖配置疏漏,没漏就别硬找。

### Windows AlwaysInstallElevated / 计划任务提权
**Q1**: 怎么判断 AlwaysInstallElevated 可用?
**A1**: 查两个注册表键(HKCU 和 HKLM 的 `SOFTWARE\Policies\Microsoft\Windows\Installer\AlwaysInstallElevated`)都为 1 才生效。命中即可用一个恶意 MSI 以 SYSTEM 安装执行,是很省事的一条提权。
**Q2**: 两个键都为 1,怎么落地?
**A2**: 生成一个执行载荷的 MSI,`msiexec /quiet /i evil.msi` 以 SYSTEM 运行。要点是 MSI 里的动作会被提升执行。生成时选低检测的载荷方式以过 AV。
**Q3**: 只有一个键为 1(另一个 0 或不存在),能用吗?
**A3**: 不能,必须两个同时为 1。转计划任务:`schtasks /query /fo LIST /v` 找以高权运行、且执行的脚本/程序我可写或路径可劫持的任务。这是另一条独立线。
**Q4**: 计划任务都指向锁死的系统路径,转哪?
**A4**: 转可写自启位置(启动文件夹、Run 键)、以及凭据搜集:Windows 上翻 unattend.xml、Group Policy Preferences(cpassword)、注册表里的自动登录口令、DPAPI/凭据管理器。提权受阻就转"找现成高权凭据"。
**Q5**: 这些配置类提权全空,最后的兜底是什么?
**A5**: 转内核 exploit:`systeminfo` 取补丁级别,用 Watson/WES-NG 比对缺失补丁对应的本地提权 CVE。配置面全干净时,未打补丁的内核漏洞是 Windows 提权的最终兜底。

### 容器逃逸成功后的宿主立足点固化
**Q1**: 刚从容器逃到宿主 root,第一件事该做提权巩固还是横向?
**A1**: 先固化立足点再扩张,否则容器一重启/被清理就失去入口。在宿主(非容器内)建持久访问:一个隐蔽的持久化,并确认它不依赖那个易失的容器。
**Q2**: 在宿主上做持久化,怎么选才隐蔽又稳?
**A2**: 优先"混入正常运维"的方式:已有服务的配置、既存计划任务/systemd、SSH 授权。避免在容器文件系统里留东西(会随容器销毁而丢),要落到宿主持久卷/宿主根。
**Q3**: 逃逸用的是"临时创建的特权容器",做完会不会留下明显痕迹?
**A3**: 会。特权容器、异常 pod、docker API 调用都会被记录。固化后应清理这些临时产物(删掉临时容器/pod),把持久化转移到低调载体,减少被溯源的面。
**Q4**: 如果宿主是 k8s node,固化和单机有何不同?
**A4**: node 上的东西可能被集群管理(污点、自动重建、GitOps 覆盖),直接改文件可能被还原。转向集群层持久化(高权 SA、恶意 admission webhook、DaemonSet)或 node 上不受编排管理的路径。
**Q5**: 固化动作触发了 EDR 告警,怎么办?
**A5**: 立即停手评估,转最低噪的存活手段(纯凭据访问、已有合法通道),放弃高危持久化技术。宁可退回"用偷来的凭据低调进出",也不要为持久化暴露整个立足点。

### seccomp / AppArmor / SELinux 限制的识别与应对
**Q1**: 逃逸手法在别处能用、这里却各种 EPERM,怀疑有 LSM/seccomp,怎么确认?
**A1**: 查 `cat /proc/self/status | grep Seccomp`(2=filter 生效)、`cat /proc/self/attr/current`(AppArmor profile,非 unconfined 即受限)、`getenforce`(SELinux)。先定位是哪一层在挡,才知道绕哪层。
**Q2**: 确认 seccomp 挡掉了 mount/init_module 等,怎么绕?
**A2**: seccomp 是 syscall 白/黑名单,绕不了就换不依赖被禁 syscall 的路径:用 docker.sock/SA token 等用户态控制面逃逸,完全不碰被禁的内核 syscall。用高层能力替代底层 syscall。
**Q3**: AppArmor profile 限制了文件访问和挂载,怎么应对?
**A3**: 看 profile 具体禁了啥(能读到 profile 更好)。它常只针对预期路径,存在覆盖不到的入口。转它没管到的 capability、network、或直接找能加载新 profile/complain 模式的口子。
**Q4**: 三层(seccomp+AppArmor+SELinux)全上、逃逸面被压得很死,值得硬绕吗?
**A4**: 通常不值,成本极高。转正交:放弃本机逃逸,转向凭据横向(读 token/密钥去打控制面)、或利用应用层漏洞在更高权限的相邻服务里落脚。加固好的沙箱,绕内核不如换战场。
**Q5**: 怎么判断某个限制是不是"配置默认"从而可能有疏漏?
**A5**: 默认 profile(docker 默认 seccomp/AppArmor)是公开已知的,能查到它放行了什么;自定义 profile 才需现场试探。先假设是默认并对照公开白名单找放行的危险 syscall/cap,比盲试高效。

### 用户命名空间(userns)对提权判断的影响
**Q1**: 逃逸前为什么要先看 user namespace?
**A1**: 因为 userns 决定"容器 root 是不是真 root"。`cat /proc/self/uid_map`:若为 `0 0 4294967295` 是同一 userns(容器 root=宿主 root,好逃);若映射到高 uid(如 `0 100000 65536`)则容器 root 在宿主只是普通用户,很多逃逸失效。
**Q2**: 处在重映射 userns 里,哪些逃逸还能用?
**A2**: 依赖挂载暴露(docker.sock、hostPath)、或宿主侧漏洞的路径仍可能有效,因为它们不靠"容器 root=宿主 root"这个前提。反而 cgroup release_agent 等需要真 CAP_SYS_ADMIN over host 的会失效。判据是"这条路是否假设宿主级 root"。
**Q3**: 我能不能自己创建一个新 userns 来获取 capability?
**A3**: 在新 userns 里你对该 ns 拥有全 cap,但这些 cap 对宿主资源无效。它对逃逸帮助有限,主要用于某些需要 cap 才能触发的本地内核漏洞(在 userns 里获得 CAP_SYS_ADMIN 去打有漏洞的 syscall)。
**Q4**: userns 让常规逃逸都失效,转哪个维度?
**A4**: 转内核 CVE(尤其历史上大量提权 CVE 正是通过非特权 userns 触发的)、以及凭据/控制面路径。userns 挡的是"身份型"逃逸,挡不住"内存破坏型"漏洞。
**Q5**: 怎么一眼判断目标是否用了 userns 加固?
**A5**: uid_map 非 `0 0 ...` 且进程 CapEff 虽满但对宿主无效,就是加固过。看到这个立即降低对"容器内 root"的期望,把主攻转到挂载暴露与内核漏洞。

### 提权动作被 EDR / 审计拦截时的转向
**Q1**: 提权 exploit 一落地就被杀/被告警,怎么判断是被静态查杀还是行为检测拦的?
**A1**: 换个特征试:同一 exploit 换编码/加壳还被杀=行为检测(看动作);改了特征就能落地=静态查杀。定性决定对策——静态查杀改免杀,行为检测则要换"不触发那个行为"的手法。
**Q2**: 是行为检测(比如注入、令牌操作被 EDR 盯),怎么绕?
**A2**: 转"合法外观"的提权:用系统自带工具/正常管理动作达成同样效果(living-off-the-land),避免调用高危 API/注入。或转配置型提权(改配置文件借服务重启),它比内存注入低调得多。
**Q3**: 每次尝试都在给蓝队送样本,担心暴露,该继续试吗?
**A3**: 停。提权是高噪动作,反复失败=持续告警=可能触发封锁和溯源。退一步先固化现有权限、评估是否非提不可。很多目标用当前权限+横向凭据就能达成,不必强提本机 root。
**Q4**: 必须提权但环境监控严,怎么降低单次尝试的暴露?
**A4**: 先在离线的同版本环境把 exploit 调稳、做好免杀,只在目标上打一次成功的;避免在目标上反复试错。侦察充分、一击命中,是对抗 EDR 的核心节奏。
**Q5**: EDR 太强、本机提权这条路整体放弃,往哪转?
**A5**: 转横向与凭据:用当前权限抓凭据(内存、配置、票据)去打监控更松的相邻主机或控制面,从别处绕回来。提权不是必须在这台机器上完成,换战场往往比硬碰 EDR 划算。

### PATH 劫持与相对路径注入
**Q1**: 有一个以高权运行的程序/脚本调用了不带绝对路径的命令,怎么利用?
**A1**: 这是 PATH 劫持点:在我可控且排在 PATH 前面的目录放一个同名恶意程序,高权进程调用时会先命中我的。判据是"高权上下文 + 相对命令名 + 我能影响它的 PATH 或工作目录"。
**Q2**: 我改不了目标进程的 PATH 环境变量怎么办?
**A2**: 转工作目录相对路径:若程序用 `./cmd` 或依赖当前目录,且它在一个我可写的 cwd 下运行(常见于 cron 切到某目录),放同名文件即可。PATH 改不了不代表相对路径注入不了。
**Q3**: 怎么发现这类"调用了相对命令"的高权程序?
**A3**: 对可疑 SUID/服务二进制用 strings/ltrace 看它 system()/execvp() 调了什么;用 pspy 观察 root 进程实际执行的命令行。看到不带路径的命令名就是候选。
**Q4**: 找到了但那个目录我恰好没写权限,卡住了怎么办?
**A4**: 转它调用链上其他可写环节:配置文件、被 source 的脚本、依赖的库。PATH 劫持只是"注入可控内容"的一种形式,顺着数据流找任一我能写的输入点。
**Q5**: 整条链都锁死,PATH 维度废了,转哪?
**A5**: 回到并行的提权面:SUID GTFOBins、sudo、capability、cron、内核。PATH 劫持依赖特定编码疏漏,不通就快速切换,不在单点上耗。

### 文件 capability(getcap)提权
**Q1**: SUID 都干净,为什么还要单独跑 getcap?
**A1**: 因为提权点可能藏在文件 capability 而非 SUID 位上。`getcap -r / 2>/dev/null` 找带 cap 的二进制。cap_setuid、cap_dac_override、cap_dac_read_search 这类挂在某程序上,能绕过普通权限达成提权/任意读。
**Q2**: 发现某二进制带 cap_setuid+ep,怎么用?
**A2**: 若这个程序本身能执行代码/脚本(如带 cap 的 python/perl/node),直接用它把 uid 设 0 起 shell。判据是"带危险 cap 的程序是否可控其行为"。带 cap 的解释器基本等于 root。
**Q3**: 带 cap 的是个功能单一、行为不可控的二进制呢?
**A3**: 看 cap 类型换用法:cap_dac_read_search→任意文件读(读 shadow/密钥);cap_net_raw→嗅探;cap_dac_override→改任意文件。不能直接起 shell 也能拿到提权所需的读写原语。
**Q4**: getcap 全空、什么 cap 文件都没有,转哪?
**A4**: 转 SUID/sudo/cron/内核。capability 文件提权是并行的一条独立线,空了就回主流提权面,别当唯一希望。
**Q5**: 容器里 getcap 找到的 cap 和逃逸有关系吗?
**A5**: 有,容器场景里进程/文件 cap 直接关系到能否逃逸(见 CAP_SYS_ADMIN 条)。在容器里跑 getcap 与 capsh --print 一起看,前者看文件维度、后者看进程维度,两个都要查。

### 数据库/服务账户到系统提权(UDF、Redis 等)
**Q1**: 我拿到一个数据库/中间件的高权访问(如 DB root、Redis 未授权),怎么转成系统 shell?
**A1**: 问"这个服务以什么系统账户跑、有没有写文件或执行的能力"。MySQL 可 UDF 提权或 INTO OUTFILE 写 webshell/cron;Redis 未授权可写文件(写 authorized_keys/cron/webshell)。核心是把服务的文件写能力接到系统执行点。
**Q2**: MySQL 想走 UDF,前提条件是什么?
**A2**: 需要 FILE 权限、能写到 plugin 目录、且服务账户对该目录可写。逐条验证 `SELECT @@plugin_dir`、`@@secure_file_priv`。secure_file_priv 非空会限制写路径——这是最常见的拦路条件。
**Q3**: secure_file_priv 限制了写路径,UDF 和 OUTFILE 都受阻,怎么办?
**A3**: 转不依赖文件写的路径:数据库里的凭据/hash 直接 dump 去横向、连接串里的其他账户口令、或该 DB 账户在别处复用。服务提权受限时,服务里的"数据"本身常是更快的横向燃料。
**Q4**: Redis 未授权但它不是 root 跑、写目录受限,怎么办?
**A4**: 看它以什么用户跑就往那个用户的可写点写(该用户的 crontab、authorized_keys)。拿到的是服务账户而非 root 也算立足,再从服务账户走本机提权。别因为不能一步到 root 就放弃。
**Q5**: 服务在容器里,写文件只影响容器怎么办?
**A5**: 转逃逸判断:先在容器内立足(服务账户→容器 root),再走容器逃逸四要素。服务提权拿到的是"容器内的执行",要到宿主还得叠加逃逸这一步,别把两步当一步。

### 低权 web shell 升级为稳定交互 shell 再提权
**Q1**: 只有一个功能受限的 web shell(命令回显、无交互),该先提权还是先升级 shell?
**A1**: 先升级。半残 shell 下提权枚举都困难(无 tty、sudo 要不了密码、交互 exploit 跑不了)。先弹一个稳定反弹 shell、升级成全 tty(python pty / script / stty),再谈提权。工欲善其事。
**Q2**: 反弹总失败,怀疑出网被限,怎么判断?
**A2**: 逐个测出站:不同端口 TCP、DNS、ICMP、HTTP 代理。看哪个方向能出。判据是"哪条出网通道没被防火墙 ACL 挡"。全不通就转不出网的立足方式。
**Q3**: 目标完全不能出网(全向拦截),shell 升级还怎么做?
**A3**: 转正向/隧道:让目标监听端口我去连(若入站可达),或走已有的 web 通道做 HTTP 隧道(把交互封装进 HTTP 请求)。出网被墙就把数据通道复用到已经允许的协议上。
**Q4**: 拿到全 tty 后,提权枚举的顺序怎么排最高效?
**A4**: 先跑自动枚举(linpeas/winpeas)拿全景,同时手工优先查"低成本高收益"项:sudo -l、SUID、可写敏感文件、凭据文件、内核版本。自动铺面+人工挑尖,避免逐条手查耗时。
**Q5**: web shell 所在账户权限太低,连枚举都被限(受限 shell rbash)怎么办?
**A5**: 先破受限 shell:找允许的命令里能逃逸的(vi/awk/能指定 shell 的程序)、或用 `bash --noprofile`、SSH ForceCommand 绕过。突破 rbash 是提权的前置步骤,受限环境下这一步优先于枚举。

### 云函数 / Serverless / CI 环境的提权与横向
**Q1**: 打进的是一个 Serverless/函数计算或 CI runner 环境,提权思路和普通主机有何不同?
**A1**: 这类环境没有"传统宿主 root"概念,提权目标是"拿到它绑定的云身份/密钥"。第一时间读环境变量、metadata、注入的凭据文件——函数/流水线的执行角色往往权限不小,那才是提权目标。
**Q2**: CI runner 里能拿到什么高价值凭据?
**A2**: 流水线注入的部署密钥、云凭据、制品仓库/镜像仓库 token、代码仓库写权限。拿到 CI 的部署身份常等于能改上线制品(供应链)。优先枚举 env 和挂载的 secret。
**Q3**: 拿到 CI 写仓库/改流水线的权限,怎么扩大战果?
**A3**: 往流水线注入恶意步骤,借它更高的部署权限在生产环境执行——这是"用 CI 身份提权到生产"的经典链。要点是流水线本身跑在更高信任域。谨慎评估授权边界再动。
**Q4**: 函数环境是短生命周期,立足点一会儿就没了,怎么办?
**A4**: 别指望驻留在函数实例里。把重心放在"用这次执行拿到的持久凭据/身份"去别处建立足点。易失环境的价值是它的身份,不是它本身,采完凭据立刻转移。
**Q5**: 环境变量和 metadata 都被清理/隔离了,转哪?
**A5**: 转它能访问的下游服务:函数常有权访问某数据库/队列/存储,顺着它的网络可达性和已配置连接串横向。执行环境隔离了身份,业务连接往往还留着可用凭据。

### 提权前的战场评估:何时该停止纵向、转横向
**Q1**: 在一台机器上提权卡了很久,怎么判断该不该继续死磕?
**A1**: 设时间盒并问三问:提权对当前目标是不是必需?当前权限能否已满足任务(拿数据/横向)?这台机器是不是通往目标的唯一路径?若"非必需/已够用/有旁路",就停手转横向,别为 root 而 root。
**Q2**: 怎么判断当前权限其实已经够用?
**A2**: 对齐任务目标:若目标是特定数据、特定凭据、或作为跳板,而当前权限已能读到/连到,那 root 只是锦上添花。红队看"是否达成目标",不是"是否拿到最高权限"。
**Q3**: 决定转横向,从当前低权能采什么去横向?
**A3**: 采不需要 root 的东西:当前用户的 history、ssh key、`~/.aws`/`~/.kube`、浏览器/配置里的口令、内网可达服务、ARP/连接表暴露的邻居。这些常足以横向到下一台,绕开本机提权。
**Q4**: 横向也暂时打不开,是不是回头继续提权?
**A4**: 先补侦察再决定,不要盲目回摆。重新枚举被忽略的面(其他用户目录、计划任务、内网新发现的服务)。纵向和横向都卡时,问题常出在"信息不够"而非"手法不够",补情报优先。
**Q5**: 什么情况下提权确实是必须优先的?
**A5**: 当目标数据/动作明确需要 root(读 shadow/内存 dump、装持久化、抓其他用户票据),或提权是打开横向的钥匙(拿本机域账户 hash 去打域)。此时提权是路径依赖,值得投入;否则它只是可选项。

### hostPID / hostNetwork / hostIPC 共享命名空间的利用
**Q1**: k8s pod 里发现 `ps aux` 能看到宿主所有进程,这意味着什么?
**A1**: 说明 pod 开了 hostPID(共享宿主 PID 命名空间)。这本身就是逃逸面:能看到宿主进程,就能读它们的 `/proc/<pid>/environ`(偷凭据)、`/proc/<pid>/root`(访问其他容器/宿主文件),配合合适 cap 还能 nsenter 进宿主命名空间。
**Q2**: 有 hostPID 且当前是特权/有 CAP_SYS_ADMIN,怎么直接进宿主?
**A2**: `nsenter --target 1 --mount --uts --ipc --net --pid -- /bin/bash`,进入 PID 1(宿主 init)的所有命名空间即宿主 root shell。hostPID 让你能定位到宿主进程,nsenter 完成切入。前提是有进入命名空间所需的 cap。
**Q3**: 只有 hostPID 但没有 nsenter 所需的 cap,还能榨出什么?
**A3**: 转"读"的价值:遍历 `/proc/*/environ` 和 `/proc/*/cmdline` 偷宿主进程里的口令/token/连接串,遍历 `/proc/*/root/` 翻其他容器文件。即便进不去,共享 PID 命名空间的信息泄露也足以拿凭据横向。
**Q4**: 是 hostNetwork(共享宿主网络)呢,怎么用?
**A4**: 你直接在宿主网络位面上:能访问宿主 localhost 上只监听 127.0.0.1 的服务(kubelet、etcd、云 metadata、本地无认证管理端口),还能嗅探/绑定宿主端口。转向"打那些以为自己只对本机开放"的服务。
**Q5**: 这些共享命名空间一个都没开,逃逸卡住了,转哪?
**A5**: 回到逃逸四要素的其余项:挂载(hostPath/socket)、capabilities、内核 CVE。共享命名空间只是逃逸面之一,没开就查其他三面,别在这一条上停留。

### polkit / pkexec / D-Bus 本地提权(PwnKit 类)
**Q1**: Linux 常规提权面都干净,还有哪些"系统自带高权组件"值得一查?
**A1**: 查本地 IPC/权限代理组件:pkexec(polkit)、D-Bus 服务、以及 setuid 的系统管理工具。这些以 root 运行、接受本地低权调用,历史上出过多枚通杀本地提权(如 pkexec 的 PwnKit CVE-2021-4034)。存在即值得比对版本。
**Q2**: 系统里有 pkexec 且是 SUID,怎么判断能不能用 PwnKit?
**A2**: PwnKit 是逻辑/内存破坏漏洞、几乎不挑发行版,判据主要是 polkit 版本是否已打补丁。先看 pkexec 是否 SUID(`ls -l $(which pkexec)`),再确认包版本落在受影响区间;成熟 PoC 稳定、崩机风险低,是很划算的一枚。
**Q3**: pkexec 被移除或已打补丁,polkit 这条线还有别的吗?
**A3**: 转 polkit 的授权逻辑漏洞(如 CVE-2021-3560 的 D-Bus 竞争,通过快速断开触发以 root 创建用户)、或枚举其他允许低权触发高权动作的 D-Bus 服务。同一 polkit/D-Bus 生态里不止 pkexec 一个入口。
**Q4**: 这类 exploit 在受限/容器环境里为什么可能不生效?
**A4**: 因为它们依赖 polkit/D-Bus 守护进程在运行、且以 root 存在于当前命名空间。容器里常没装或没跑这些服务。先确认服务在跑(`ps` 找 polkitd/dbus-daemon),没跑就直接排除,别浪费 exploit。
**Q5**: polkit/pkexec 全打不动,转哪个兜底?
**A5**: 回到内核 exploit 兜底(uname 比对本地提权 CVE)与凭据横向。系统组件提权是"配置/版本疏漏"型,和内核内存破坏型、凭据型是三条正交线,一条封死切下一条。