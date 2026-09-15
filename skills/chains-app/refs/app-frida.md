# App客户端 · Frida/hook动态分析决策

### 拿到一个未知 APK/IPA 先做什么
**Q1**: 手上一个陌生客户端安装包,目标是分析它和后端的通信/破解某个校验,第一步该干什么而不是急着 hook?
**A1**: 先做静态侦察建立地图,再决定 hook 什么。顺序:解包看目录结构、`AndroidManifest.xml`(权限、Activity/Service、`android:debuggable`、`networkSecurityConfig`)、identify 语言栈(Java/Kotlin/Flutter/RN/Unity/原生 so)。判据:如果 `lib/` 下全是 `libflutter.so`/`libil2cpp.so`,说明业务逻辑不在 Java 层,后面 hook 策略要整个换维度。别一上来就 `frida-trace`,先知道逻辑在哪一层。
**Q2**: 怎么快速判断逻辑主要在 Java 层还是 native 层?
**A2**: 反编译(jadx/apktool)后看 Java 代码密度和 `System.loadLibrary` 数量。如果关键函数体是 `native` 声明、Java 层只是壳,或大量 JNI 调用,逻辑沉到 so 里了。此时 Java hook 只能看到入参出参边界,内部得上 native hook(`Interceptor.attach` + 模块基址+偏移)。
**Q3**: 静态看不出加密算法怎么办?
**A3**: 转动态。先用广谱 hook 撒网:hook `javax.crypto.Cipher`、`Mac`、`MessageDigest`、`java.util.Base64`,把每次调用的算法名、key、iv、明文/密文 dump 出来。让运行时告诉你它用了什么,而不是逆向死磕。
**Q4**: 环境准备的坑?
**A4**: frida-server 版本必须和 host 端 frida 版本严格匹配,架构(arm64/arm)要对。root/越狱环境、SELinux、frida-server 端口(默认 27042)。判据:`frida-ps -U` 能列进程说明通道通了。

### App 直接检测到 root/越狱就退出
**Q1**: 启动 App 立刻弹"检测到风险环境"然后闪退,连主界面都进不去,怎么破?
**A1**: 这是 root/越狱检测。先定位检测点:hook 常见指示函数——`Runtime.exec`(找 su)、`File.exists`(查 /system/xbin/su、Magisk 路径)、`getprop`(ro.debuggable、ro.secure)、`PackageManager` 查已知 root 管理器包名。判据:哪个函数返回后紧接着走了退出分支,那就是检测点。
**Q2**: hook 单点绕过了一个,又弹另一个检测?
**A2**: 检测通常多重冗余。别一个个 hook,改用成熟去检测方案:Magisk Hide/Zygisk + DenyList、或 frida 脚本批量 stub 掉一类检测。同时 hook `System.exit`/`Process.killProcess` 兜底——即使检测触发,拦住它的自杀动作。
**Q3**: 换了方案还是被检测,而且发现是 native 层查的?
**A3**: 转 native 维度。检测逻辑在 so 里直接 `fopen("/system/bin/su")` 或读 `/proc/mounts`。hook `libc` 的 `fopen`/`open`/`stat`/`access`,对敏感路径返回 ENOENT。这是绕过底层检测的正交打法。
**Q4**: 连 frida 都被检测到(反调试)?
**A4**: 见"App 检测到 frida 注入"那条,这属于另一个正交问题,先把 root 检测和反 frida 分开处理,别混在一起调。

### App 检测到 frida 注入直接崩溃
**Q1**: frida attach 上去 App 秒崩,或者运行几秒后崩,怀疑反 frida,怎么确认和绕过?
**A1**: 先确认是不是 frida 检测:用 spawn 模式而非 attach,看崩在启动还是启动后。常见检测:扫描 `/proc/self/maps` 找 `frida`/`gum-js-loop`/`gadget` 字样、扫端口 27042、检测 `frida-server` 进程名、检测线程名 `gmain`/`gdbus`。判据:改用非默认端口 `frida-server -l 0.0.0.0:PORT` 后不崩了,就是端口检测。
**Q2**: 改端口还崩,是扫 maps 字符串?
**A2**: 用魔改版 frida(改掉内存中的特征字符串、改 pipe 名)、或 `frida-gadget` 重打包注入而非 server 模式。判据:gadget 内嵌进程内,没有独立 server 进程和默认端口特征。
**Q3**: spawn 时机太早 hook 不到,attach 又被检测,怎么平衡?
**A3**: 用 `spawn` + `gating`/`-f` 挂起再注入脚本再 resume,在检测代码执行前就 hook 掉检测函数。核心是抢在检测之前完成 hook。可 hook `pthread_create` 提前拦截检测线程的创建。
**Q4**: 全试了还是绕不过反调试怎么办?
**A4**: 转向不注入的正交路径:改用中间人抓包(见 SSL pinning 条)从网络侧拿数据,或用静态插桩重打包(smali patch 掉检测)后再上 frida,或换 objection 的 patchapk。别在同一个注入维度死磕。

### 抓不到 HTTPS 流量(证书固定)
**Q1**: 配了系统代理、装了 CA 证书,浏览器能抓 App 的 HTTPS 却抓不到,大概率 SSL Pinning,怎么定位?
**A1**: 先确认是 pinning 而非代理没走。看报错:App 里网络失败但抓包工具无请求=流量没走代理或握手被拒。判据:hook `SSLContext`/`TrustManager`/`X509TrustManager.checkServerTrusted`,看是否有自定义实现抛异常。
**Q2**: 确认是 pinning,怎么绕?
**A2**: 分层试。OkHttp 用 `CertificatePinner`——hook 它的 `check` 方法直接返回。通用方案上 objection 的 `android sslpinning disable` 或成熟的 universal unpinning frida 脚本(覆盖 TrustManager、OkHttp、Conscrypt、WebView 多种)。判据:脚本加载后代理能看到明文请求。
**Q3**: 通用脚本不生效,pinning 在 native 层(BoringSSL/自研)?
**A3**: 转 native。Flutter/自研网络栈在 `libssl.so`/`boringssl` 里做 `ssl_verify`。hook `SSL_get_verify_result` 返回 0,或 hook `ssl_crypto_x509_session_verify_cert_chain`。Flutter 还不走系统代理,得用透明代理(iptables 重定向)+ hook `BoringSSL`。
**Q4**: 证书校验绕过了但 App 不走我的代理?
**A4**: 这是正交问题——代理感知。App 可能硬编码不读系统代理(Flutter/某些 SDK)。转用 VPN 模式抓包(如 tun2socks/ProxyDroid)或 iptables `REDIRECT` 到透明代理端口,强制流量落到你的抓包器。
**Q5**: 还是失败,双向证书(mTLS)?
**A5**: 客户端也带证书。从 App 里 dump 出客户端证书和私钥(hook `KeyStore.load`/`PrivateKey`,或找 `.p12`/`.bks` 文件+口令),把它导入抓包工具做客户端认证。

### 找不到关键函数该 hook 哪里
**Q1**: 想破解某个 VIP 判断/签名生成,但代码混淆严重,不知道 hook 哪个方法,怎么快速定位?
**A1**: 从"可观测的锚点"反推。UI 有提示文字?搜字符串资源找到引用它的类。有网络请求?从请求参数名反查生成它的方法。判据:先锁定一个你能触发的行为,再顺着调用链找。
**Q2**: 字符串被加密搜不到?
**A2**: 用运行时栈回溯。hook 一个必经的底层函数(如加密、`toString`、`StringBuilder`),打印 `Thread.currentThread().getStackTrace()`,从调用栈里看是哪个业务类调过来的。让运行时暴露调用关系。
**Q3**: 类名方法名全是 a/b/c 混淆,怎么锁定?
**A3**: 用 `frida-trace` 或 objection 的 `android hooking search classes/methods` 按关键词撒网,配合触发操作看哪些方法在你点击时被调用。或 hook `ClassLoader.loadClass` 看动态加载了什么。也可以按方法签名特征(参数类型、返回类型)筛。
**Q4**: 撒网 hook 太多噪声淹没了信号?
**A4**: 缩小时间窗口。在触发操作前一刻开始 trace,操作后立刻停,只看这段的调用。或用差分:做 A 操作记录一组调用,做 B 操作记录另一组,取差集。
**Q5**: 完全定位不到,方法在 native 或动态生成?
**A5**: 转维度。可能是 DEX 动态加载/热修复/加壳,类根本不在原 APK 里。hook `DexClassLoader`/`InMemoryDexClassLoader` dump 运行时 DEX,或用脱壳工具(FRIDA-DEXDump)从内存捞出真实 DEX 再静态分析。

### 加固壳导致代码看不见
**Q1**: jadx 打开只有一个壳 Application 和几个类,真实业务代码全没了,加壳了,怎么办?
**A1**: 先识别壳厂商(360、腾讯乐固、爱加密、梆梆等有特征 so 名和目录)。判据:`lib/` 下的特征 so、`assets/` 里的加密 dex。不同壳脱法不同,但通用思路是内存 dump——壳最终要把真实 DEX 解密加载进内存。
**Q2**: 具体怎么从内存脱?
**A2**: hook DEX 加载点。ART 上 hook `OpenMemory`/`DefineClass`/`DexFile` 相关,或用现成脱壳 frida 脚本(FRIDA-DEXDump)扫描进程内存里的 dex magic(`dex\n035`)整片 dump。判据:dump 出的 DEX 能用 jadx 打开看到真实逻辑。
**Q3**: 一代壳好脱,遇到函数抽取(二代壳)dump 出来方法体是空的?
**A3**: 二代壳把方法体运行时才回填(instruction 抽取)。需要主动调用触发每个方法回填,或 hook 解释器执行入口(`ExecuteSwitchImpl`/`artInterpreterToInterpreterBridge`)逐方法 dump CodeItem 再回填补全。这是更重的正交打法。
**Q4**: VMP 壳(指令虚拟化)彻底 dump 不出?
**A4**: 转策略——放弃还原代码,改做行为观测。既然静态还原不了,就在其调用系统 API 的边界 hook(crypto、网络、文件),从输入输出黑盒推断逻辑。红队目标通常是拿到数据/绕过校验,不一定非要看懂代码。

### hook 上了但方法有重载调不动
**Q1**: 写了 hook,frida 报错说方法有多个重载(overload),怎么处理?
**A1**: 必须显式指定重载签名:`.overload('java.lang.String', 'int').implementation = ...`。判据:先用 `.overloads` 打印所有重载,或先 `.overload('...')` 不确定时用 `Java.enumerateMethods` 看完整签名。参数类型要写全限定名。
**Q2**: 不知道具体调的是哪个重载?
**A2**: 用 `.overloads.forEach` 把所有重载都 hook 上,每个打印自己的签名和参数,运行时看哪个被触发。定位后再精确 hook 目标那个。
**Q3**: hook 上了但 implementation 里 `this.method()` 调原方法报错?
**A3**: 在 overload 上下文调原实现要用 `this.methodName.overload(...).call(this, args)` 或直接 `this.methodName.apply(this, arguments)`,注意保持 this 绑定。返回值类型也要匹配,基本类型别返回 undefined。
**Q4**: 内部类/匿名类方法 hook 不到?
**A4**: 内部类用 `$` 连接:`Outer$Inner`,匿名类是 `Outer$1`。用 `Java.enumerateLoadedClasses` 或 `enumerateMethods` 确认真实类名再 hook。

### 时机太早类还没加载
**Q1**: spawn 模式注入,hook 目标类报 ClassNotFoundException,类还没加载,怎么办?
**A1**: 类是运行时才加载的。别在脚本顶层直接 `Java.use`,包在 `Java.perform` 里,并延迟到类加载后。判据:如果启动即报错说明类尚未由 ClassLoader 加载。
**Q2**: 怎么等到类加载再 hook?
**A2**: hook `ClassLoader.loadClass`,当加载到目标类名时再执行你的 hook。或对动态加载的 DEX,用 `Java.enumerateClassLoaders` 找到承载目标类的那个 ClassLoader,用 `Java.classFactory.loader = 那个loader` 切换后再 `Java.use`。
**Q3**: 多 ClassLoader 环境(插件化/热修)`Java.use` 找不到类?
**A3**: 默认 factory 只搜默认 loader。遍历所有 classloader 找到能 load 目标类的那个,创建对应的 `Java.ClassFactory` 实例来 use。这是插件化 App 的常见坑。
**Q4**: 还是抓不到加载时机?
**A4**: 转被动观测——不主动找,而是 hook 目标方法所在类必经的父类/接口方法,或干脆 hook `DexFile.defineClass` 全量记录,让类加载事件自己冒出来。

### 参数/返回值改了不生效
**Q1**: hook 里改了返回值想绕过校验,但 App 行为没变,怎么回事?
**A1**: 先确认 hook 真的命中——在 implementation 里加日志。若没打印,hook 点错了(重载错、类错、根本没走这个方法)。若打印了但改值无效,可能校验结果被缓存或有二次校验。判据:日志是排查一切的起点。
**Q2**: 确认命中了但改返回值 App 不认?
**A2**: 可能改晚了——真正的判断用的是别的中间变量,或结果在 native 层已定。往上游 hook,或找真正消费这个值的地方。也可能有完整性自校验(见签名校验条)发现被 hook 后走了降级逻辑。
**Q3**: 改基本类型返回值报错?
**A3**: 类型要严格匹配:`boolean` 返回 `true` 不是 `1`,`long` 别用 JS number(超 2^53 精度丢失)要用 `.valueOf` 或 Frida 的 Int64/UInt64。返回对象时构造要正确。
**Q4**: 参数是复杂对象改不动?
**A4**: 对象要通过其方法/字段修改,或构造新实例替换。改字段用 `obj._fieldName.value = ...`(私有字段加下划线或用反射)。数组、ByteArray 用 `Java.array` 处理。
**Q5**: 全对了还是不生效,怀疑逻辑不在这层?
**A5**: 转 native。Java 层这个方法可能只是转发,真正判断在 JNI。上 `Interceptor.attach` hook 对应 native 导出函数,改 native 返回值(`retval.replace()`)。

### 只能 hook 到边界看不到内部
**Q1**: 关键逻辑是个 native 函数,Java hook 只能看到 JNI 边界的入参出参,想看内部怎么办?
**A1**: 转 native hook。先定位:`Module.getBaseAddress('libxxx.so')` + 函数偏移(导出函数用 `Module.findExportByName`,非导出用 IDA/Ghidra 算偏移)。`Interceptor.attach` 在 `onEnter`/`onLeave` 读寄存器和内存。
**Q2**: 函数没导出,只有偏移地址,怎么 attach?
**A2**: `base.add(0xOFFSET)` 得到绝对地址再 attach。注意 Thumb 模式地址要 `|1`(arm32)。判据:offset 从静态分析(IDA)拿,base 从运行时拿,两者相加。PIE 下每次 base 变,必须运行时取。
**Q3**: 参数是指针/结构体,读不懂?
**A3**: 用 `args[n]` 拿寄存器值,`Memory.read*` 读内存:`ptr.readCString()`、`readByteArray(len)`、`readPointer()`。结构体按偏移逐字段读。ARM64 参数在 x0-x7。想看数据流可 `hexdump(args[0])`。
**Q4**: so 有反调试/校验自身完整性,attach 就崩?
**A4**: so 可能校验 `.text` 段 CRC 检测 inline hook。转用 `Stalker` 做指令级 trace(不改内存)、或先 hook 掉它的完整性校验函数、或用硬件断点(`Process.setExceptionHandler`)代替 inline patch。

### 加密参数逆不出算法
**Q1**: 请求里有个 sign/token 参数不知道怎么生成的,想伪造,逆向算法太慢,有没有捷径?
**A1**: 红队不必逆出算法,做 RPC 黑盒调用即可。用 frida 把 App 内的签名函数暴露成远程可调接口(`rpc.exports`),你传参数它返回签名。判据:能稳定输入 payload 得到正确 sign,就等于拥有了签名能力,无需理解算法。
**Q2**: 签名函数依赖 App 内部状态(设备指纹、session)?
**A2**: 保持 App 运行态做 RPC,让它带着真实上下文算。或先 hook 把这些依赖 dump 出来固化。判据:RPC 返回的 sign 拿去请求服务端能通过验证。
**Q3**: 想大批量调用但 App 单实例慢?
**A3**: 转规模化:多开 App 实例 + frida 池化,或把关键 so 抽出来用 unidbg 在纯模拟环境批量执行(脱离真机)。unidbg 适合可移植的纯计算 so。
**Q4**: unidbg 跑不起来(依赖太多系统调用)?
**A4**: 补环境或退回真机 RPC。unidbg 需要补 JNI/syscall 环境,依赖重时成本高。判据:如果 so 大量依赖 Android framework,老实用真机 frida RPC 更快。

### 想主动调用一个内部函数
**Q1**: 定位到一个内部方法(如解密、生成邀请码),想直接调用它拿结果,不走 UI,怎么做?
**A1**: 主动调用。静态方法直接 `Java.use('Cls').method(args)` 包在 `Java.perform` 里。实例方法需要一个实例。判据:静态且无副作用依赖的最好调。
**Q2**: 是实例方法,没有现成实例?
**A2**: 两条路:`Java.choose('Cls', {onMatch})` 从堆里抓一个已存在的活实例来调(带真实状态);或 `Java.use('Cls').$new(...)` 自己 new 一个(需构造参数正确)。判据:依赖内部状态的选 choose,无状态的可 $new。
**Q3**: `Java.choose` 在堆里找不到实例?
**A3**: 实例可能还没创建或已回收。先在 UI 触发让它产生,或 hook 构造函数把实例存到全局变量供后续用。native 侧对象则用 hook 缓存指针。
**Q4**: 调用抛异常/崩溃?
**A4**: 多半是 JNI/线程上下文问题。主动调用要在 `Java.perform` 且在正确线程。涉及 UI 的方法需 attach 到主线程(`Java.scheduleOnMainThread`)。参数类型不匹配也会崩,逐个核对签名。

### WebView / 混合 App 逻辑在 JS 里
**Q1**: App 界面是 WebView 装的 H5,业务逻辑在 JS 里,frida hook Java 看不到东西,怎么分析?
**A1**: 换战场到 Web 层。开 WebView 远程调试:hook `WebView.setWebContentsDebuggingEnabled(true)` 强制开启,然后 Chrome `chrome://inspect` 连上去,直接下断点看 JS。判据:能在 devtools 看到页面 DOM 和脚本即成功。
**Q2**: App 禁了 debugging 且检测?
**A2**: 强制 hook 打开开关(上面),并 hook 检测点。或 hook `WebView.loadUrl`/`evaluateJavascript` 看加载的 URL 和注入的脚本。JS 与原生桥接看 `addJavascriptInterface`——hook 它能看到暴露给 JS 的原生方法。
**Q3**: 关键逻辑在原生和 JS 的桥接调用里?
**A3**: hook `@JavascriptInterface` 注解的方法(JS 调原生的入口)和 `evaluateJavascript`/`WebMessage`(原生调 JS)。这两个方向覆盖了所有 hybrid 通信。
**Q4**: H5 是加密/加壳的 JS?
**A4**: 转前端逆向维度——devtools 里运行时 deobfuscate、下 XHR 断点抓请求生成、或直接在 console 里调它暴露的全局函数做 RPC。和纯 Web 逆向同法。

### Flutter App 常规 hook 全失效
**Q1**: 目标是 Flutter 写的,Java hook 抓不到业务、代理抓不到包,常规套路全废,怎么下手?
**A1**: 认清 Flutter 特性:业务逻辑编译进 `libapp.so`(Dart AOT),不走系统 proxy,自带 BoringSSL 做 TLS。所以两条主线:抓包要透明代理 + hook BoringSSL,逻辑分析要啃 native so。判据:代理无流量但设备联网正常=Flutter 不读系统代理。
**Q2**: 怎么抓 Flutter 的 HTTPS?
**A2**: iptables/tun 把流量透明重定向到抓包器,再 hook `libflutter.so` 里的 `ssl_verify_result`(用 reFlutter 或社区脚本定位偏移)禁用证书校验。reFlutter 工具可重打包让流量走代理并 dump。
**Q3**: 想分析 `libapp.so` 里的 Dart 逻辑?
**A3**: 用 Dart AOT 专用工具(如 reFlutter、Doldrums、blutter)解析 Dart snapshot,还原类名方法名结构。纯 IDA 看 Dart runtime 很痛苦,专用工具能恢复符号。
**Q4**: 版本太新工具不支持?
**A4**: 转黑盒——放弃还原,直接透明代理看网络行为,或 hook `libflutter` 的 HTTP 层函数 dump 明文请求。红队要数据不一定要还原全部代码。

### frida 加载脚本无响应/卡死
**Q1**: 脚本注入后 App 卡死或 frida 无输出,怎么排查是脚本问题还是环境问题?
**A1**: 二分法。先注入空脚本或只 `console.log('ok')`,若正常说明环境通、是脚本逻辑问题;若空脚本也卡,是环境/注入问题。判据:空脚本能否输出。
**Q2**: 空脚本正常,加了 hook 就卡?
**A2**: 常见死因:hook 了高频函数(如 `StringBuilder.toString`)导致 IO 风暴;implementation 里同步阻塞;死循环;主线程里做重活。缩小 hook 范围,去掉高频 hook,console.log 减量或异步。
**Q3**: attach 成功但目标方法从不触发?
**A3**: 可能没走这个代码路径、类名/重载错、或被内联优化。加日志确认 hook 是否 attach 成功(attach 时打印一次),再确认方法是否真被调用(换个必经方法验证 hook 机制本身没问题)。
**Q4**: 偶发性,时好时坏?
**A4**: 竞态——spawn 注入时机和类加载竞争。改用挂起注入(`-f` + 脚本里延迟/等类加载)。或 frida-server 不稳,重启 server、换稳定版本。

### 数据在内存里明文但要精准抓取
**Q1**: 知道敏感数据(密钥、明文)某刻在内存里,但不知道被哪个函数处理,想抓,怎么办?
**A1**: 两条路:已知数据特征(如固定前缀/长度)用 `Memory.scan` 扫内存匹配;不知具体值就 hook 数据必经的处理函数。判据:有明文样本或特征模式就直接 scan 定位地址,再下内存断点看谁访问它。
**Q2**: 扫到地址后想知道谁读写它?
**A2**: 用 `MemoryAccessMonitor` 或硬件断点监控该地址的读写,触发时打印调用栈,定位到访问它的代码。这是"从数据反推代码"的打法。
**Q3**: 数据用完立刻被清零抓不到?
**A3**: 抢时机——hook 清零函数(`memset`/Arrays.fill)前一刻抓,或 hook 它的生产者函数在生成瞬间 dump。别等它进内存再扫,直接在源头拦。
**Q4**: 内存里是加密的,扫不到明文?
**A4**: 转到解密边界。既然内存里也是密文,说明解密发生在使用的最后一刻。hook crypto 解密函数,在 `onLeave` 抓解密后的明文输出。

### 反调试导致 attach 后进程被 kill
**Q1**: attach 上去几秒后进程自己死了,日志显示被反调试踢掉,怎么保住调试会话?
**A1**: 定位反调试机制。常见:`ptrace(PTRACE_TRACEME)` 自我保护、检测 `/proc/self/status` 的 TracerPid、定时检测 frida 特征线程。判据:TracerPid 非 0 触发退出。hook `ptrace` 让它返回成功但不真调用,hook `fopen("/proc/self/status")` 篡改 TracerPid 为 0。
**Q2**: 有独立看门狗线程定时检测?
**A2**: hook `pthread_create`,识别看门狗线程的入口函数,拦截其创建或让其函数体空转。或找到检测函数直接 `Interceptor.replace` 成空实现。
**Q3**: native 层 `ptrace` 检测在 so 初始化(JNI_OnLoad)就跑,hook 晚了?
**A3**: 抢在 so 加载前 hook。hook `dlopen`/`android_dlopen_ext`,在目标 so 加载完成、`JNI_OnLoad` 执行前插入你的 hook。或 hook `linker` 的 `call_constructors`。时机是关键。
**Q4**: 全绕不过,反调试太顽固?
**A4**: 转不依赖 attach 的路径:静态 patch 掉反调试 smali/机器码后重打包,或用模拟器/unidbg 脱离真机反调试环境,或从网络侧 MITM 拿数据。换维度别硬刚。

### 想批量自动化操作 App(薅接口)
**Q1**: 分析清楚了签名逻辑,想批量刷接口/自动化,但每次手点太慢,怎么规模化?
**A1**: 分两层:签名能力用 frida RPC 暴露(前面条已述),批量请求用外部脚本(Python)组织。App 只负责算签名,请求由脚本发。判据:脱离 UICI 用纯 HTTP + RPC 签名跑通一次。
**Q2**: 有设备指纹/风控绑定单设备?
**A2**: hook 指纹采集点(IMEI、Android ID、MAC、设备型号)统一伪造成一套,或多开多套指纹。风控维度要具体分析它采集什么,逐项可控。
**Q3**: 服务端有频率限制/行为风控?
**A3**: 这转到业务风控对抗维度:控制请求节奏拟人化、轮换 IP(代理池)、轮换设备指纹和 token。别用同一指纹高频打。
**Q4**: RPC 并发时 App 崩/串号?
**A4**: 单 App 实例非线程安全易串号。改多实例隔离(每实例一套上下文),或串行化 RPC 调用加锁。或抽 so 用 unidbg 无状态并发算签名。

### 抓包看到加密 body 但要改包重放
**Q1**: 抓到请求了但 body 是加密的,想篡改参数重放测越权/改价,加密改不了怎么办?
**A1**: 不在网络层改,在加密前改。hook 加密函数的 `onEnter` 拿到明文 payload,在这里篡改参数,让 App 用你改后的明文重新加密发出。判据:抓到的密文变了且服务端接受=改包成功。
**Q2**: 想在响应上改(比如把服务端返回的"无权限"改成"有权限")?
**A2**: hook 解密函数的 `onLeave`,拿到解密后的明文响应,篡改后返回给业务逻辑。这是客户端侧越权测试的常用点。但注意这只影响本地展示,真越权要看服务端。
**Q3**: 加密带时间戳/nonce 防重放,重放失败?
**A3**: 用 RPC 实时重新签名而非重放旧包。每次请求现算 sign+时间戳。防重放机制要求你拥有签名能力(见 RPC 条),而不是简单重放。
**Q4**: 改了明文但有 body 完整性 HMAC 校验对不上?
**A4**: HMAC 也在客户端算,hook 它的输入用改后的 body,让它算出匹配的新 HMAC。只要密钥/算法在客户端,所有校验都能顺着重算。

### 签名/完整性校验发现被篡改
**Q1**: 重打包(patch smali)后 App 启动报"应用被篡改"退出,签名校验触发,怎么过?
**A1**: 定位校验点:App 校验自身签名(`PackageManager.getPackageInfo` 拿 signatures 比对哈希)。hook `getPackageInfo`/`GET_SIGNATURES` 返回原版签名信息。判据:hook 后不再报篡改。
**Q2**: 校验在 native 层读 APK 文件算 hash?
**A2**: 转 native。so 里 `fopen` APK 自己算 CRC/hash 比对。hook 文件读取返回原始内容,或 hook 比对函数返回相等。或者干脆不重打包,改用 frida 运行时 patch(不改文件就不触发文件完整性校验)。
**Q3**: 有 SafetyNet/Play Integrity 远程证明?
**A3**: 这是服务端侧校验,本地 hook 治标不治本。转向:用能过 attestation 的环境(硬件级难绕)、或攻击验证逻辑本身(服务端如何用这个 attestation 结果)、或接受降级功能。硬件证明是正交的强防御。
**Q4**: 多重校验(签名+CRC+SafetyNet)叠加?
**A4**: 优先选不触发校验的路径:纯 frida 运行时注入而非重打包,从根上避开文件完整性和签名校验。能不改文件就不改文件是绕过一切静态完整性校验的上策。

### iOS 越狱环境下的 hook 差异
**Q1**: 目标是 iOS App,在越狱机上分析,和 Android 有什么决策差异,先做什么?
**A1**: iOS 逻辑主要在 Objective-C/Swift 的 Mach-O 里。先解密(App Store 包是加密的):用 frida-ios-dump/dumpdecrypted 脱壳拿到明文可执行文件。判据:class-dump 能列出类和方法说明脱壳成功。
**Q2**: OC 方法怎么 hook?
**A2**: frida 的 `ObjC.classes['ClassName']['- methodName:'].implementation` hook,或用 `ObjC.chooseSync` 抓实例。OC 运行时可反射,比 Android 更易 hook。用 `frida-trace -m '-[Class method]'` 撒网。
**Q3**: Swift 方法(无 OC runtime)hook 不到?
**A3**: 纯 Swift 无 OC 桥接的方法要按符号/偏移做 native hook(`Interceptor.attach` + `Module.findExportByName`)。或 hook 它调用的 OC/Foundation 边界。Swift 名字 mangled,用 demangler 还原。
**Q4**: SSL pinning / 越狱检测?
**A4**: 同 Android 思路但工具不同:越狱检测 hook `fork`/`stat`(查 /Applications/Cydia.app)/`dyld` 查注入库;pinning 用 SSL Kill Switch 或 frida unpinning。检测原理相通,只是 API 换成 iOS 的。
**Q5**: 非越狱机怎么办?
**A5**: 转重签名注入:用 frida-gadget 重签名打包(需开发者证书),或用 objection patchipa。无越狱下用 gadget 内嵌方式,牺牲便利换可行性。

### hook 位置对但被内联/优化掉了
**Q1**: 想 hook 一个小函数但从没触发,怀疑被编译器内联了,怎么确认和应对?
**A1**: native 小函数常被 inline,调用点直接展开,没有独立函数入口可 attach。判据:IDA 里看不到对该函数的 call、或函数很小。应对:hook 它的调用者(caller)在展开处理的位置,或用 Stalker 指令级 trace 找到对应指令段。
**Q2**: Java 层方法被 ART 优化(AOT 编译成机器码)hook 不稳?
**A2**: frida Java hook 依赖解释器/JIT 入口,AOT 方法可能绕过。用 `frida --runtime` 相关设置,或强制反优化。objection/frida 新版对 AOT 有处理。也可 hook 更上层未被优化的入口。
**Q3**: 想 hook 但函数没有稳定入口地址?
**A3**: 用 Stalker 对整个模块/线程做指令跟踪,定位目标代码段执行,在指令级别观测寄存器变化。Stalker 不需要函数边界,适合被内联/混淆到没有清晰入口的代码。
**Q4**: Stalker 太慢淹没数据?
**A4**: 限定范围——只 follow 特定线程、只在触发操作的时间窗内跟踪、用 `Stalker.exclude` 排除无关模块。或先粗定位模块再局部精跟。

### 多进程 App 注入错了进程
**Q1**: hook 没反应,发现 App 是多进程的(如 :push、:remote 独立进程),注入的是主进程但逻辑在子进程,怎么办?
**A1**: 先枚举进程:`frida-ps -Ua` 看该 App 的所有进程。判据:目标功能所在进程可能不是主进程(推送、支付常独立进程)。找到承载目标类的进程再 attach。
**Q2**: 子进程是动态 fork 出来的,还没启动怎么 attach?
**A2**: 用 frida 的 child gating(`enable_child_gating`)在 fork/spawn 子进程时暂停并注入。或 hook `Process.fork`/`Runtime.exec` 捕获子进程创建。这样能抓到刚诞生的子进程。
**Q3**: 不确定逻辑在哪个进程?
**A3**: 每个进程都注一个探针脚本(`Java.enumerateLoadedClasses` grep 目标类名),哪个进程能枚举到目标类,逻辑就在那。用进程列表逐个排查。
**Q4**: 进程间通信(AIDL/Binder)才是关键?
**A4**: 转 IPC 维度——hook Binder 事务(`transact`/`onTransact`)看跨进程传的数据,或 hook `Messenger`/`ContentProvider` 边界。逻辑可能分散在多进程协作里,抓 IPC 数据流最直接。

### 时间/环境依赖导致行为不可复现
**Q1**: 某个 bug/功能只在特定条件触发(特定时间、特定地区、特定版本),想稳定复现来分析,怎么办?
**A1**: hook 环境采集点统一伪造。时间依赖 hook `System.currentTimeMillis`/`Date`;地区 hook `Locale`/`TelephonyManager` 的国家码;版本判断 hook 版本读取函数。把环境变量变成你可控的常量。
**Q2**: 依赖服务端下发的开关(feature flag)?
**A2**: hook 配置解析处,把服务端返回的开关字段改成想要的值。找到读取该 flag 的方法,在返回时篡改。这样本地强开被灰度的功能。
**Q3**: 依赖 A/B 分组,你的设备没进实验组?
**A3**: hook 分组判定逻辑或分组 ID 生成(常基于设备 ID hash),伪造成目标组的值。或直接 hook "是否在实验组"的判断函数返回 true。
**Q4**: 全是服务端控制,客户端 hook 无效?
**A4**: 转服务端视角——客户端改不动就直接构造带目标参数的请求打服务端接口,绕过客户端灰度。灰度往往只是客户端展示控制,接口本身可直连。

### frida 检测 App 用了双进程守护互拉
**Q1**: App 有守护进程,你 kill 掉主进程或注入导致主进程异常,守护进程立刻把它拉起,状态全丢,怎么稳定分析?
**A1**: 先识别守护机制:双进程互相监听、`JobScheduler`/`AlarmManager` 定时拉活、native `fork` 守护。判据:进程被杀立即重生。应对:同时冻结/注入两个进程,或先 hook 拉活逻辑禁用它。
**Q2**: 守护进程也检测调试,联动退出?
**A2**: 用 child gating 同时接管父子进程,统一注入去检测脚本。两个进程当一个整体处理,别只顾一个。
**Q3**: 拉活太快抓不住时机?
**A3**: 先 hook 守护/拉活的触发点(`AlarmManager.set`、`startService`、native fork)让守护失效,断掉重生链,再从容分析主进程。先拆守护再分析。
**Q4**: 守护在系统级(账号同步、无障碍)难禁?
**A4**: 转环境隔离——在受控模拟器里断网/冻结特定组件,或用 Xposed/LSPosed 模块级别接管,从框架层压制拉活行为,而非在进程里对抗。

### 抓到的数据要判断是客户端还是服务端漏洞
**Q1**: 通过 hook 发现客户端有"是否 VIP"的本地判断且能改,改了本地就解锁了,这算漏洞吗,下一步怎么定?
**A1**: 关键判据:改客户端后功能是否真生效且服务端认账。本地 hook 让 UI 解锁但请求受限数据服务端仍拒=纯客户端展示绕过,价值低。若服务端也放行=真越权,价值高。必须发实际请求验证服务端态度。
**Q2**: 怎么区分"客户端假解锁"和"服务端真漏洞"?
**A2**: 做对照实验:hook 解锁后触发受限操作,抓包看服务端响应。服务端返回真实受限数据=服务端信任了客户端(漏洞);服务端返回拒绝=只是本地绕过。永远以服务端响应为准。
**Q3**: 发现服务端也信任客户端传的"vip=true"?
**A3**: 转服务端越权测试——这说明服务端把授权判断下放给了客户端,是经典的客户端信任漏洞。直接构造请求改这个字段,批量测试各接口的服务端鉴权是否都这么松。
**Q4**: 想扩大战果?
**A4**: 从这个信任缺陷横向枚举:客户端还传了哪些"本该服务端判断"的字段(价格、用户 ID、权限位)?逐个测服务端是否校验。客户端逆向的价值在于暴露服务端接口和参数,供服务端侧深挖。

### 密钥硬编码但取不到明文
**Q1**: 怀疑加密密钥硬编码在 App 里,但静态搜不到、字符串是运行时拼的,怎么拿到真实 key?
**A1**: 别静态逆拼接逻辑,动态在使用点抓。hook `SecretKeySpec` 构造函数、`Cipher.init`,密钥作为参数传入时直接 dump 出来。判据:hook 到 key 的字节数组即拿到明文密钥。
**Q2**: key 在 native 层生成从不进 Java?
**A2**: 转 native。hook native crypto 库(`libcrypto`/自研)的 key 设置函数(如 `AES_set_encrypt_key`、`EVP_EncryptInit`),从参数读 key。或在加密函数入口 dump 密钥缓冲区。
**Q3**: key 用白盒密码(白盒 AES)没有独立 key?
**A3**: 白盒把 key 融进查找表,没有可提取的 key。转黑盒——放弃提取 key,直接 RPC 调用它的加解密函数当预言机用。有加解密能力就够了,不必拥有 key。
**Q4**: key 存在硬件 keystore(TEE)?
**A4**: TEE 里的 key 导不出来。同样转 RPC/预言机思路——让 App 用 keystore 里的 key 帮你加解密。或分析业务是否真依赖这个 key 的机密性,攻其使用逻辑而非 key 本身。

### 逆向发现自定义协议(非 HTTP)
**Q1**: 抓包发现 App 和后端不走 HTTP,是 TCP 上的自定义二进制协议,抓包工具解不了,怎么分析?
**A1**: 从 socket 边界 hook。hook `SocketOutputStream.write`/`InputStream.read`(Java)或 `send`/`recv`(native),dump 收发的原始字节。判据:拿到明文字节流后分析协议结构(长度头、类型、payload)。
**Q2**: payload 还加密/序列化(protobuf 等)?
**A2**: 往上游 hook——在序列化/加密之前的业务对象层抓。hook protobuf 的 `toByteArray` 前的对象,或加密前的明文。socket 层是最外层,真正可读的数据在业务层。
**Q3**: 协议有握手/心跳难以离线重放?
**A3**: 用 RPC 保持 App 内活连接,借它的连接发你构造的包。或还原协议后用脚本实现完整握手。自定义协议往往用 RPC 借道最省事。
**Q4**: 完全解不开二进制格式?
**A4**: 转差分分析——固定其他条件,只改一个操作,对比字节流差异,逐字段推断含义。配合 hook 序列化点看字段名到字节的映射。黑盒差分比硬逆协议格式快。

### 该用 frida 还是 Xposed/LSPosed
**Q1**: 分析任务里,什么时候该用 frida,什么时候转 Xposed/LSPosed,怎么选?
**A1**: 判据看场景。快速迭代调试、native hook、临时探索选 frida(改脚本即时生效、无需重启)。需要持久化、开机自启、框架级稳定接管、长期挂着选 Xposed/LSPosed。红队侦察阶段 frida,固化利用/长期驻留用 Xposed。
**Q2**: frida 被检测太狠,想换更隐蔽的?
**A2**: 转 LSPosed(基于 Zygisk,进程内 hook,无独立 server/端口特征)相对隐蔽,或 Xposed 模块。或魔改 frida 去特征。选型本身就是一种反检测转向。
**Q3**: 两者都被检测?
**A3**: 转静态插桩——直接改 smali/dex 重打包实现 hook 效果,运行时无任何注入框架特征。牺牲灵活性换零运行时特征。或用 VirtualApp/沙箱容器加载目标 App 在受控环境 hook。
**Q4**: 想兼顾灵活和隐蔽?
**A4**: frida-gadget 以库形式重打包进 App,无独立进程;或用定制 hook 框架。根据检测强度在"灵活-隐蔽"轴上选点,没有万能解,按对抗烈度调整。

### App 关键操作要人机验证/滑块
**Q1**: 自动化时遇到滑块/行为验证码,hook 也过不去,卡在人机验证,怎么转?
**A1**: 先分析验证码怎么集成:是 SDK(极验/腾讯防水墙类)还是自研。判据看请求里的验证 token 从哪来。若是标准 SDK,尝试 hook 其结果回调直接返回"验证通过",看服务端是否只认这个 token。
**Q2**: hook 通过回调但服务端二次校验 token 有效性?
**A2**: 说明验证是服务端强绑定的,本地 hook 无效。转向:分析验证 SDK 的请求生成能否 RPC 复用(让 App 真过一次验证拿 token),或降低自动化对该操作的依赖。
**Q3**: 完全绕不过验证码?
**A3**: 转攻击面——绕过需要验证的那个接口,找不需要验证的同功能接口(有时其他端/旧版本 API 无验证)。或验证只在关键操作,把自动化目标改到无需验证的环节。
**Q4**: 只是想分析不是刷量?
**A4**: 那不必绕过,手动过验证后用 frida 观测后续逻辑即可。区分目标:分析用手动过码+hook观测;批量才需要绕验证。别把分析问题当成对抗问题。

### 版本更新后偏移全失效
**Q1**: 之前写好的 native hook 脚本(基于 so 偏移),App 更新后全部失效,每次更新重算偏移太累,怎么办?
**A1**: 用动态特征定位替代硬编码偏移。用字节码模式(pattern)`Memory.scan` 搜函数特征、或按导出符号、或按字符串引用交叉定位函数入口。判据:换版本后无需改脚本仍能定位=特征匹配成功。
**Q2**: 函数没符号、特征也随版本变?
**A2**: 用相对定位——从一个稳定锚点(常量字符串、系统调用、稳定导出函数)出发,按调用关系或相对偏移推导目标。锚点越稳定,脚本越抗更新。
**Q3**: 想彻底摆脱偏移依赖?
**A3**: 尽量上移到有符号/有稳定 API 的层。Java 层方法名比 native 偏移稳定;系统 API(libc、JNI)比业务 so 稳定。在稳定层 hook 能扛住业务更新。
**Q4**: 必须 hook 易变 native 函数?
**A4**: 转自动化定位——写脚本从静态分析(IDA/Ghidra 脚本按特征找函数)自动产出偏移喂给 frida,把"每次手算"变成"每次自动算"。工程化对抗版本迭代。

### 只有线上环境不敢乱测
**Q1**: 目标 App 只有生产环境,直接自动化/改包怕触发风控封号或影响真实数据,怎么谨慎推进?
**A1**: 先做只读观测再动手。第一阶段纯 hook 观测(读参数、看逻辑)不发异常请求,建立理解。判据:观测阶段不产生任何异常流量,零风险摸清机制。
**Q2**: 必须发请求验证漏洞?
**A2**: 用测试账号、小额、可回滚的操作先验证,别拿主账号打高危操作。控制影响面:先证明漏洞存在(单次小样本),再评估是否扩大。红队也要控制附带损害。
**Q3**: 担心 hook 行为本身被风控标记?
**A3**: 拟人化+低频+隔离环境。用独立设备/账号、模拟正常使用节奏、避免机器特征暴露。风控对抗是伴随始终的约束,不是最后才考虑。
**Q4**: 环境实在太敏感?
**A4**: 转离线/镜像环境——脱出关键 so 用 unidbg 离线跑、或搭本地 mock 服务端复现协议、或申请测试环境。把破坏性验证移到不影响生产的沙盒里。

### 分析陷入僵局该怎么系统性转向
**Q1**: 一个客户端分析卡了很久,该 hook 的都 hook 了还是拿不到想要的,怎么系统性判断往哪转而不是瞎试?
**A1**: 回到"逻辑在哪一层"的坐标系检查:Java 层 → native so → Dart/Unity/JS → 服务端。当前卡在哪层就往相邻层找。判据:如果 Java 边界看到数据进 native 就出不来,答案在 native;如果 native 也是转发,答案在服务端。逐层排除。
**Q2**: 层都排查过了还卡?
**A2**: 换观测维度:从"改代码逻辑"转"观测数据流"(hook crypto/socket/文件 IO 三大边界),从"静态还原"转"动态黑盒 RPC",从"客户端"转"服务端接口直连"。三组正交维度总有一个能突破。
**Q3**: 怀疑方向从一开始就错了?
**A3**: 重新审视目标本质。要的是数据?→ 抓数据边界不必懂代码。要的是绕过校验?→ 找校验点不必懂算法。要签名能力?→ RPC 不必逆算法。很多僵局源于把"红队目标"错当成"完整逆向目标"。
**Q4**: 时间成本太高该止损吗?
**A4**: 评估这条路的边际收益。若目标能从其他攻击面(Web端、小程序、开放 API、旧版本 App)更省力达到,就转攻击面。客户端逆向是手段不是目的,哪条路到目标最短走哪条。
**Q5**: 彻底没思路了?
**A5**: 回到侦察——重新做一遍信息收集,常有第一遍遗漏的线索(未分析的 so、隐藏 Activity、调试接口、注释里的测试地址、其他端点)。卡死时,补侦察往往比在原地加 hook 更有效。
