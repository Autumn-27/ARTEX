# App客户端 · C/S架构抓包与协议分析

### 起手三问:先抓包 还是 先逆向 还是 先看权限清单
**Q1**: 拿到一个陌生的 App(Android/iOS/PC 富客户端)想打接口,时间有限,该从哪切入?
**A1**: 三条路并行判断成本:(a) 先透明代理抓一遍看流量走不走 HTTP,20 分钟能出结论;(b) 拿 apk/ipa 静态看 AndroidManifest / Info.plist、看 usesCleartextTraffic、network_security_config、有没有 pin 声明,10 分钟出结论;(c) 看有没有公开的 API 文档或 web 版同源接口。三条同时开,谁先出线索走谁。
**Q2**: 三条都不太顺,继续硬啃哪条?
**A2**: 优先补齐【指纹】:APK 用 apktool + jadx 打开,搜关键字 okhttp/retrofit/pinner/certificate/x509/protobuf/grpc/websocket/xxtea/aes,15 分钟内定位它的网络栈到底是啥;协议栈定了,后续所有工具选择才有依据。
**Q3**: 静态里全是壳(360/爱加密/娜迦/梆梆)完全看不到源码怎么办?
**A3**: 转到运行时:装真机,先跑一遍抓 logcat,看崩溃/日志泄露;同时用 frida-ps / objection 探测运行时进程,能不能 attach 决定后续所有的 hook 策略。
**Q4**: 静态动态都被拦,连 attach 都不允许?
**A4**: 正交转向:不打客户端,打服务端。用抓到的哪怕一条请求,提取域名/接口路径,直接对 web 面/H5 版/小程序版做接口枚举——这些通常共用后端,pin 更弱。
**Q5**: 判据是什么?什么时候放弃 App 端转打服务端?
**A5**: 判据:2~4 小时内没能让明文流量出现在代理里 + 静态高度混淆 + 无越狱 iOS。这时继续磕客户端的边际收益迅速下降,直接转打后端更划算。

### HTTPS 装证书后 App 一启动就断网
**Q1**: 代理和 CA 证书都装好了,浏览器能抓,App 一开就"网络异常"。第一反应该判什么?
**A1**: 三种可能同时排查:(1) App 有 SSL Pinning;(2) App 走了 SNI 或走了非 HTTP 协议根本没经过代理;(3) 系统级用户 CA 在 Android 7+ 不被 App 信任(需要装到系统 CA 或改 network_security_config)。用 mitmproxy 的 tls_passthrough 观察握手是不是被客户端主动 RST。
**Q2**: 看到握手完成但立刻 close_notify,谁在关?
**A2**: 大概率是客户端 pin 校验失败主动断。看 logcat 有没有 SSLHandshakeException / certificate pinning failure / trust anchor 类日志,这类堆栈就是 pin 命中的铁证。
**Q3**: 握手根本没到代理,说明什么?
**A3**: 说明 App 走了自己的 TCP/UDP 通道或用了 SNI over TLS 之外的协议(比如 QUIC/自建协议)。转向抓 tcpdump 看真实端口和协议特征。
**Q4**: 确认是 pin,但不想改 apk 重打包,有没有更快的路?
**A4**: 优先 objection --gadget xxx explore + android sslpinning disable 一把梭,通杀常见 okhttp / trustmanager / conscrypt,通率 60% 以上;不通再上定制 frida 脚本。
**Q5**: objection 通杀不成功,栈里没有它 hook 的那些类,怎么办?
**A5**: 转向【native 层】,pin 校验被下沉到 so 里(SSL_CTX_set_verify / SSL_get_verify_result)。用 frida 的 Interceptor.attach + Module.findExportByName 找 boringssl 的 SSL_verify 函数直接返回 1。
**Q6**: 这条路被墙(so 加壳、导出符号剥离)呢?
**A6**: 正交转向:不绕 pin,而是【替换服务端证书链】——如果 App pin 的是根 CA 或中间 CA 的公钥,而不是叶子证书,可以拿系统 CA 签一张同 CN 的证书;或者直接【降级协议路径】,看 App 有没有 HTTP fallback(某些 App 判断到 HTTPS 反复失败会回落到 HTTP)。

### 判断 SSL Pinning 的具体类型
**Q1**: 已经确认有 pin,决定用哪种绕过方式前,先要知道 pin 打在哪一层。怎么快速判?
**A1**: 分四层排查:(a) Java 层 X509TrustManager 自实现;(b) OkHttp CertificatePinner;(c) Conscrypt/TrustManagerImpl 层挂 hook;(d) Native so 里 BoringSSL/OpenSSL 的 SSL_CTX_set_verify 或自写校验。用 frida 逐层打日志,哪一层进了 checkServerTrusted / verify 谁就是主战场。
**Q2**: 四层都被 hook 打了 log,一个都没进,请求就断了?
**A2**: 说明 pin 在更早的阶段——可能是【证书链解析前】的原始 DER 字节比对,或者干脆比对了 SPKI 哈希。转向 hook read/recv 或 SSL_read 看握手阶段的原始字节,定位比对逻辑。
**Q3**: 客户端不是校验证书,而是校验【服务端返回的业务字段签名】,pin 只是幌子?
**A3**: 转向业务层反调,搜 verifySign / checkResponse / RSA.verify,这时候光绕 TLS 层没用,得连业务响应也一起伪造签名。

### 双向 TLS(mTLS)客户端证书从哪来
**Q1**: 抓到握手,发现服务端要求 Certificate,客户端也乖乖出示了。第一步做什么?
**A1**: 提取客户端证书和私钥。三个可能藏点:(1) apk/ipa assets 里的 .p12/.pfx/.bks/.jks 文件;(2) 硬编码在 so 里的 DER 字节;(3) 运行时从服务端下发后存 keystore。先 grep -r "BEGIN CERTIFICATE" 和 grep 特征字节 30 82(DER 头)。
**Q2**: 找到了 p12 但要密码?
**A2**: hook KeyStore.load 的 char[] password 参数直接打印;或 hook PKCS12 相关调用把 password 拦下来。iOS 上 hook SecPKCS12Import。
**Q3**: 证书是运行时动态申请的(设备指纹换证书)?
**A3**: 转向复用会话:hook SSLContext.init 后拿到已经初始化好的 KeyManager,导出内存里的 PrivateKey.getEncoded(),或者直接把整个 SSLContext dump 出来在自己代理里复用。
**Q4**: 证书在 TEE / SE / Keychain 里,私钥不可导出,签名操作只能在设备里做?
**A4**: 正交转向:不复用私钥,而是【把设备变成签名预言机】——保持 App 进程活着,让 App 帮你完成 TLS 握手或数据签名,自己只操纵它发什么(通过 frida rpc / 中间人劫持业务函数入参)。

### 抓到明文了但 body 是密文
**Q1**: TLS 绕过了,请求也进代理了,但 body 是一坨看不懂的 base64/hex。怎么判加密类型?
**A1**: 看长度和熵。(a) 长度是 16 的倍数 → 大概率 AES-CBC/ECB;(b) 长度不定但有固定前 12/16 字节 → GCM/CCM 带 nonce;(c) 前面有一段 128/256 字节的定长块 → RSA/ECDH 包裹的 session key;(d) 高熵完全随机 → 已经是最终密文,得找密钥来源。
**Q2**: 密钥找不到硬编码,是运行时协商的?
**A2**: hook 加密函数入口。搜 SecretKeySpec/Cipher.init/Cipher.doFinal(Java),CCCryptorCreate(iOS),EVP_EncryptInit_ex(native),把 key 和 iv 打出来。抓到 key 就能离线解一切。
**Q3**: 加密下沉到 so,函数名被剥离且 VMP 保护?
**A3**: 转向【不逆向,只观察数据流】:hook 网络出口(SSL_write / send)和加密函数出口的字节,同时 hook 明文入口(App 里业务层调用 encrypt 前的 String 参数),从明文到密文的路径不用理解代码,把两头字节对齐就等于知道加解密结果。
**Q4**: 明文入口也拿不到(比如是 protobuf 序列化后立刻加密)?
**A4**: 正交转向:把 App 变成【加解密预言机】。写 frida rpc,给它任意输入让它返回加密结果 / 解密结果,自己在外部构造攻击 payload,不需要知道算法。
**Q5**: 判据什么时候停止磕加密?
**A5**: 如果 2~3 小时无法定位到 key,而 App 又能持续被 hook 当预言机用,直接转成预言机模式往前推——省下的时间打业务逻辑。

### 请求签名字段(sign/token)反向定位
**Q1**: 请求里有 sign=xxx 或 X-Signature 头,重放时改一个字节就 400。怎么定位签名生成点?
**A1**: 三个入口同时下钩:(1) hook java.security.MessageDigest.update/digest 打 MD5/SHA 输入;(2) hook javax.crypto.Mac.doFinal 抓 HMAC 输入;(3) frida 搜 "sign" / "signature" 字符串引用。签名生成前必有一次 update,把 update 的输入按调用顺序拼起来就是签名原文。
**Q2**: 打出来一堆 update,不知道哪一次是它?
**A2**: 用【回溯法】——先取 sign 的值,在 hook 里筛"哪次 digest 的返回值等于这个 sign",那一次的输入就是签名原文。
**Q3**: 签名不走标准库,是自写的 xxtea / murmur / crc 变种?
**A3**: 从字符串常量反推:搜 magic 数(比如 xxtea 的 0x9E3779B9)、搜 salt 字符串、搜与用户信息拼接的固定前后缀。native 层就搜 .rodata。
**Q4**: 签名原文有一部分是设备指纹/时间戳/nonce,离线复算总差几位?
**A4**: 逐字段对齐。写脚本枚举:时间戳精度是秒还是毫秒?nonce 是不是 UUID?deviceId 是 IMEI 还是 androidId 还是自建 UUID?字段顺序按参数名字典序还是按接口固定顺序?一次差就检查这四点。
**Q5**: 全部对齐还差,说明什么?
**A5**: 存在【隐藏字段】——签名原文里混进了没在请求里出现的东西(app 版本、渠道号、上一次响应的 token)。转向 dump 签名函数调用栈上下文所有 String 变量,一定能找到。
**Q6**: 签名逻辑在 so 里、VMP,且拿不到入参?
**A6**: 转预言机模式:hook 签名函数,喂任意原文得任意签名,自己在外部构造。

### 私有二进制协议 从零逆向
**Q1**: tcpdump 抓到 TCP 流量,不是 HTTP,是自定义二进制。第一刀怎么切?
**A1**: 找【长度前缀】和【magic】。前 4 字节看是不是等于剩余长度(大/小端都试),前 2/4 字节看是不是固定 magic(比如 0xDEADBEEF、0xAABBCCDD)。找到帧边界才能谈其他。
**Q2**: 长度前缀对不上,一条 TCP 消息里塞了多帧?
**A2**: 观察多个连接的首字节分布,通常头部结构一致。用统计法:每条消息的前 N 字节做频次分析,固定值的位置是 magic/type,变化但相关的位置是 length/seq。
**Q3**: 协议里字段是 TLV 结构但 tag 号不认识?
**A3**: 结合 App 逆向。在 native 层 hook write/send 前的 buffer 构造函数,通常有 pack_uint32 / write_varint 之类的辅助函数,里面把 tag 和 value 分开写入,hook 它们就能重建 tag 语义。
**Q4**: 协议整体加密,握手前几包就是 DH/ECDH 交换?
**A4**: hook 客户端的 EC_POINT_mul / X25519 / DH_compute_key,拿到 shared secret 就能解后续所有帧。ECDH 曲线看 native 里搜 P256/curve25519。
**Q5**: 协议看似私有,其实是标准协议(MQTT/AMQP/RTMP/自研套 protobuf)?
**A5**: 前 3 字节和第 5~10 字节和标准协议 magic 对比一遍,能省几天。protobuf 序列化的特征是 tag<<3|wiretype,首字节常是 0x08/0x10/0x1A。
**Q6**: 完全没头绪、动态分析也不给上?
**A6**: 正交转向:观察【服务端行为】。不同请求引发不同响应大小、不同延迟,可以推断协议结构。或者查这家厂商有没有开源过类似 SDK / 有没有 GitHub 泄露的老版本代码。

### protobuf 无 proto 文件字段还原
**Q1**: 确认是 protobuf,但没有 .proto 文件。怎么还原字段?
**A1**: 用 protoc --decode_raw 或 blackboxprotobuf 先把字节解成 field number + wiretype + value 三元组树。此时字段【类型】和【嵌套结构】就出来了,只差字段名。
**Q2**: 字段名影响后续 fuzz 吗?
**A2**: 不影响构造,只影响可读性。攻击时按 field number 就能构造。字段名只在做业务语义猜测时有用。
**Q3**: 想知道字段语义?
**A3**: 从 App 侧反查:jadx 里搜 protobuf 生成的类(带 GeneratedMessageLite 或 protobuf 相关基类的),类里的字段名映射到 field number,直接读得懂。或者 hook protobuf 类的 getXxx() 方法看返回值。
**Q4**: 客户端也是纯二进制没生成类,是 nanopb 或 C++ 直接编解码?
**A4**: 转向观察相关性:发一次请求改一个参数(比如登录改用户名长度),看 protobuf 里哪个字段变化,一一对应。
**Q5**: 字段变化太混杂看不出对应?
**A5**: 用差分抓包 + 变量固化——每次只改一个变量,连续抓 10 组样本,做字段级 diff,只有那个字段在变的就是目标。

### gRPC / HTTP2 抓包
**Q1**: 抓到的是 HTTP/2,body 二进制且 :path 是 /svc/method 形式,判定 gRPC。怎么解?
**A1**: mitmproxy 支持 HTTP/2,payload 前 5 字节是 gRPC 帧头(1 字节压缩标志 + 4 字节长度),后面是 protobuf。按 protobuf 流程解。
**Q2**: gRPC over TLS 且带客户端证书?
**A2**: 走 mTLS 那一条链的解法,把客户端证书搞出来复用。
**Q3**: 用了 gRPC-Web 或 Connect 协议变体?
**A3**: 特征是 content-type: application/grpc-web 或 application/connect+proto。用对应 proxy(比如 Envoy 或 grpcurl)做转发,普通 mitmproxy 可能截不干净。

### WebSocket 长连接抓与重放
**Q1**: App 主要业务走 WebSocket,抓包只看到 upgrade 后就没了明文?
**A1**: 用 mitmproxy 的 websocket 模式,或 Charles/Burp 的 WS 面板,能显示每一帧文本/二进制。TLS 绕过后 WS 帧就直接可见。
**Q2**: WS 里的消息也是加密/自定义封装的?
**A2**: 走【body 密文】那条链,先定位 WS onMessage / send 前后的加解密函数。
**Q3**: WS 有心跳且服务端会踢无效客户端,重放怎么办?
**A3**: 不能离线重放,得【维持一个真实连接】。写脚本:先让 App 建连,拿到 sessionid / cookie,然后自己用 wscat / websockets 库接管连接,或者让 App 保持在线自己用 frida 注入指定消息。
**Q4**: WS 带序号且服务端严格递增?
**A4**: hook 客户端的 send 函数拿到当前 seq,自己接管时从这个 seq 继续;或干脆全程通过 frida rpc 让 App 帮发。

### QUIC / HTTP3 抓不到
**Q1**: 代理里啥也没有,tcpdump 看到大量 UDP:443,判定 QUIC。怎么办?
**A1**: 三条路:(1) 强制降级——iptables/防火墙 drop UDP:443,App 通常会回落到 HTTP/2 over TCP;(2) 用支持 QUIC 的 mitm(比如 mitmproxy 新版、或专用 QUIC proxy);(3) 用 SSLKEYLOGFILE 环境变量导出会话密钥后 Wireshark 解密。
**Q2**: App 不回落,只跑 QUIC 且带 pin?
**A2**: 转向 hook 客户端 QUIC 库(常见 cronet / quiche),在应用层拿明文,不在网络层解密。
**Q3**: 客户端是 cronet 静态编译进 so?
**A3**: 直接 hook cronet 的高层 API(UrlRequest.write / onResponseStarted),在这一层字节还是明文。

### 模拟器识别导致无法调试
**Q1**: App 一进模拟器就退,提示"不支持模拟器"。怎么办?
**A1**: 判断它检测什么:build.prop 里 ro.product.model / ro.hardware / ro.kernel.qemu、/dev 下的 qemu 相关设备、CPU 信息里的 Goldfish/Ranchu、传感器数量、蓝牙、SIM 卡。用 magisk + 修改 build.prop 或 frida hook Build 类的静态字段。
**Q2**: 改完还退,说明它有 native 层检测?
**A2**: hook __system_property_get,把 qemu 相关的 key 返回值全篡改。或者干脆上真机+root,一劳永逸。
**Q3**: 真机也识别为"异常环境"?
**A3**: 大概率检测到了 magisk/root/frida。转向 hidden 方案:Magisk DenyList、Zygisk、shamiko;frida 用 frida-gadget 静态注入而不是 attach。
**Q4**: 都被识别,想快速验证不想恋战?
**A4**: 正交转向:不修客户端环境,而是【拿一台真实干净设备】做真机操作 + 用 tcpdump/pcap 在网络层抓,不动客户端进程,pin 依然要处理但检测这一关直接绕开。

### Root/越狱检测让 App 拒启
**Q1**: App 检测到 root 直接闪退。三分钟内定位怎么做?
**A1**: hook 常见检测点:File.exists 判 /system/xbin/su、Runtime.exec("su")、getprop ro.build.tags 是否 test-keys、PackageManager 查 Magisk/Xposed 包名。iOS 类似:stat /Applications/Cydia.app、fork() != -1、dyld 里搜 substrate/substitute。
**Q2**: 检测点太多,一个个 hook 太慢?
**A2**: 上成品:RootBeer / anti-detect frida script、objection android root disable、iOS 上 A-Bypass / Shadow 越狱检测绕过插件。8 成 App 直接过。
**Q3**: 成品不过,说明它有自研 SDK 层检测(比如乐固/易盾)?
**A3**: 抓字符串定位那个 SDK 的检测函数,一般叫 checkEnv / isSafe / getSecInfo,返回 bool 或错误码,hook 强制返回"安全"。
**Q4**: 那个函数在 VMP 里,hook 不掉?
**A4**: 转向【劫持后续消费点】——不管它怎么检测,最后一定要根据结果决定"退不退",找那个 System.exit / abort / 弹窗调用点,把上游条件短路。
**Q5**: 检测 + 退出全在 native 且强关联,还有别的路吗?
**A5**: 正交转向:不 root,用【无 root 方案】——iOS 用 sideload + TrollStore、Android 用 VirtualApp/太极 阳/Xposed 无 root 方案让 App 跑在受控容器里。

### Frida 被检测/被杀
**Q1**: attach 上去 App 立刻退,或者根本 attach 不上?
**A1**: 常见检测:/proc/self/maps 里搜 frida/gum/gadget、扫描端口 27042、检测 D-Bus、检测线程名。用 frida-gadget 改端口 + 改进程/线程名 + hook 相关 syscall(openat 拦 /proc/self/maps 请求返回过滤后的内容)。
**Q2**: 上面这些常规绕过都被针对?
**A2**: 换 hook 引擎:用 Xposed / LSPosed(基于 zygote,不注入 gadget),或者用 stalker + 硬件断点这种不侵入的方案。
**Q3**: 想要更轻量?
**A3**: 转 unicorn/qiling 做离线仿真,把 so 里的加密函数拉出来在自己进程里跑,不需要动 App 进程。适合"只想解某个函数"的场景。
**Q4**: 目标函数依赖 JNIEnv / 外部状态,仿真跑不起来?
**A4**: 正交转向:整 App 逆向找纯算法实现,重写一份 Python/JS 版本;或者拿一台犯规设备当预言机,通过 adb + socket 让它帮算。

### 硬编码 key 找不到 全靠运行时组合
**Q1**: 静态搜遍字符串,没有像 key 的东西,肯定是运行时拼出来的?
**A1**: 三种拼法排查:(1) 用户信息 + 常量 salt 拼哈希;(2) 服务端首次响应下发;(3) 从 native 里的字节数组解密出来。分别 hook Cipher.init 的 key 参数、hook onResponse 里的字段解析、hook so 加载时的解密函数。
**Q2**: key 是从服务端下发的,那第一次请求怎么加密?
**A2**: 一定有【引导协议】——通常首个请求用固定 key(硬编码/RSA 加密的临时 key)完成握手,之后切换到 session key。找那个引导包的加密函数,那里是死点必然可解。
**Q3**: 每次启动 key 都不同,离线复现困难?
**A3**: 放弃离线,走【实时代理】模式:mitmproxy addon 在中间拿到 key 后当场解密,不追求离线可重放。
**Q4**: 想要离线也能解?
**A4**: 把 key 落地——hook Cipher.init 时把 key + 请求 id 存 sqlite,后续离线分析用请求 id 反查 key。

### iOS 无越狱环境如何抓
**Q1**: 客户是 iOS App,没越狱设备,预算不够买。怎么办?
**A1**: 三条路:(1) 用 sideloadly / AltStore 重签一个带 SSKS(SSL Kill Switch)dylib 的版本,把 pin 干掉后再装到普通设备;(2) 用 TrollStore(特定 iOS 版本)几乎等价越狱;(3) 找一台过保的旧 iPhone 直接越狱。
**Q2**: 目标 App 有反重签检测(校验签名或 embedded.mobileprovision)?
**A2**: 转 hook 检测点:代码签名验证一般走 SecCodeCopySelf 或直接读 __TEXT 段哈希。dylib 层面 hook 掉。
**Q3**: 完全没条件 iOS?
**A3**: 正交转向:Android 版本通常和 iOS 共用后端 API,抓 Android 就等于抓了协议。除非是纯 iOS 独占功能。

### 客户端反重放 (timestamp+nonce+sign) 挡住重放
**Q1**: 抓到的请求 5 秒后重放就报"请求过期"或"nonce 重复"?
**A1**: 三个字段是抵抗因子:timestamp(有效期一般几秒到几分钟)、nonce(单调递增或随机去重)、sign(前两者的哈希)。要重放必须能【重签】,而重签需要 key。回到"签名字段定位"那一条链。
**Q2**: 拿到签名算法可以重签了,但服务端还是拒?
**A2**: 检查 nonce 策略。如果是服务端存了 nonce 集合去重,同一个 nonce 只能用一次——每次重放必须换新 nonce。如果是客户端单调 seq,得预测服务端记录的最新 seq。
**Q3**: nonce 是用户会话相关的 UUID,推不出来?
**A3**: 转向【会话内构造】——不用离线重放,而是接管当前 App 的会话:hook 或 mitm 修改【当前正在发送】的请求内容,timestamp/nonce 都用 App 自己生成的合法值。

### JSBridge / WebView 混合应用抓包
**Q1**: 目标 App 主要业务是 WebView 里跑 H5,Native 只做壳。怎么抓?
**A1**: WebView 有代理设置就走系统代理(容易);pin 通常在 WebViewClient.onReceivedSslError 里 proceed,好绕。真正的活儿在 JSBridge——native 和 JS 通信的接口就是攻击面。
**Q2**: 怎么枚举 JSBridge 方法?
**A2**: 搜 addJavascriptInterface,把注册的对象名和方法列出来;iOS 上搜 WKUserContentController.add。枚举完直接从 JS console 里调用,权限往往超预期。
**Q3**: WebView 用了自定义 URL 拦截协议(比如 xxbridge://...)?
**A3**: hook shouldInterceptRequest / shouldOverrideUrlLoading 打印所有 URL,反向出协议格式。
**Q4**: JSBridge 加了 token 校验只有可信 H5 才能调?
**A4**: 转向【劫持可信 H5 域】——中间人替换该域返回的 JS,把你的 payload 塞进去;或者利用 WebView 允许 file:// 加载本地资源的漏洞。

### 抓包环境正常但业务功能异常
**Q1**: 抓包一切顺利,但登录/支付这类关键接口在抓包环境下就是不通,不抓包又能通?
**A1**: 大概率触发了【风控上报】。App 在异常环境下故意让功能失败,不给你可用的返回样本。看有没有额外请求发到风控接口(常见几家云厂商的风控 SDK 域名)。
**Q2**: 找到风控上报点,想干掉?
**A2**: 三种打法:(1) hook 上报请求让它常返回"环境正常";(2) hook 上报结果处理,让业务层永远拿到 safe;(3) 干脆断网上报——iptables 挡住那个域名,某些 App 断网时会 fallback 为"信任"。
**Q3**: 服务端不信任端上信号,自己有二次判断(设备指纹反查)?
**A3**: 转向【设备指纹伪造】——统一伪造 IMEI/androidId/mac/ 屏幕分辨率/时区,让服务端认为是同一台常规设备。
**Q4**: 打不动风控,想快速拿业务样本?
**A4**: 正交转向:换真机 + 不抓包,用【物理层记录】——手机屏幕录像 + adb logcat + Charles 只作为 http 观察方(不解 TLS),拿到能跑的样本再离线分析。

### 短连接 vs 长连接混跑
**Q1**: App 同时用了 HTTP 短连接(登录、查询)和长连接(推送、聊天),想抓全?
**A1**: HTTP 走标准代理;长连接一般 socket 或 WS,需要抓包工具支持任意 TCP。用 mitmproxy 的 tcp mode + wireshark 双工具。
**Q2**: 长连接是自定义 TLS 上的私有协议?
**A2**: 走"私有二进制协议"那条链。TLS 绕过后进入应用层字节流分析。
**Q3**: 短长两端字段有关联(短连接下发的 token 用在长连接鉴权)?
**A3**: 抓包顺序很重要——先记录短连接响应,再看长连接首个握手包里是否包含相同 token,建立映射关系,后续用短连接的 token 撬长连接。

### 客户端把大量业务放本地,服务端接口很薄
**Q1**: 抓包发现关键业务(比如价格计算、优惠券判断)在客户端完成,服务端接口不校验?
**A1**: 天赐良机——直接篡改本地或篡改请求。判断哪些字段是"客户端算出来给自己看的",哪些是"要传回服务端的"。前者改本地即可,后者要看服务端信不信。
**Q2**: 服务端只信自己的数据不理客户端上报?
**A2**: 那么客户端本地篡改只能骗自己 UI。转向找服务端【读取本地数据后不校验就用】的接口,通常在支付/下单/积分兑换等业务中。
**Q3**: 服务端严格校验,客户端本地也做二次校验?
**A3**: 转向业务逻辑漏洞——竞态、金额传负数、优惠券二次核销、订单状态越权。这类跟抓包关系反而不大。

### 心跳/keepalive 结构逆向
**Q1**: 长连接每隔几秒发一个小包,内容看起来固定或极简。有必要分析吗?
**A1**: 有——心跳里常包含时间同步、会话 token、位置或设备信息,是研究协议头结构的天然样本。抓 20 个心跳做 diff,变化位就是【动态字段】,固定位是【协议骨架】。
**Q2**: 心跳完全固定字节,提供不了信息?
**A2**: 那它只是保活作用。转去看【心跳后紧跟的第一个业务包】——通常握手 / 补包会在心跳建立后触发。
**Q3**: 心跳周期本身就是防抓包信号(周期漂移检测环境)?
**A3**: 转向严格保持网络时序:代理不要人为延迟,或者用透明代理(不走 proxy 而是路由改写)减少可观测差异。

### TLS 指纹 (JA3/JA4) 识别客户端环境
**Q1**: 服务端根据 JA3 拒绝 mitmproxy/curl,只信真实 App 的 TLS 栈?
**A1**: 特征:真实 App 走能通,自己 curl/burp 走的握手就 403。判据是响应差异只出现在【重放方】不同时。
**Q2**: 怎么绕?
**A2**: 用 curl-impersonate / mitmproxy 的 tls_start hook 或专门伪造指纹的库(比如 utls),把 client hello 里 cipher_suites / extensions / groups 顺序调成和真机一致。
**Q3**: 服务端连 HTTP/2 SETTINGS 帧、window_size 都在指纹里?
**A3**: 转向【物理设备中转】——在真机上部署代理 App(Termux + mitmproxy 或自建 socks),外部机器请求经这台真机出网,天然指纹一致。

### 完全逆向不动 转向服务端接口攻击
**Q1**: 客户端保护做得太狠,一周还没啃动 pin/加密/风控三层。要不要放弃?
**A1**: 判据:如果攻击目标是"访问服务端数据/权限",客户端只是入口,那么它不动了就转打服务端。因为服务端通常是团队里更薄弱的一环。
**Q2**: 转打服务端,手里没接口文档,能怎么起手?
**A2**: 从其他信息源:(1) H5/小程序/Web 版通常有 open api;(2) 抓到的哪怕一条明文请求都能作为种子做接口枚举;(3) Github/搜索引擎搜域名 + swagger/openapi/actuator/druid。
**Q3**: 服务端也严防死守(WAF + 强鉴权)?
**A3**: 转向【子域名/边缘服务】——主域名严防,子域测试/管理/内部/文档站往往裸奔。递归子域枚举 + 端口扫描。
**Q4**: 判据什么时候彻底放弃这个 App 目标?
**A4**: 客户端不动 + 服务端所有暴露面都无缝隙 + 无社工/供应链入口。这时候按红队规则记录、留存证据、写不可利用结论,不硬打不合规目标。

### 抓包时 App 有主动检测代理设置
**Q1**: 系统代理一开 App 就报"网络异常",代理一关又能用?
**A1**: App 用 ProxySelector / System.getProperty("http.proxyHost") 或 NetworkInfo 检测。绕法:用【透明代理】而不是系统代理——iptables redirect 到 mitmproxy 的 transparent 端口,App 完全感知不到。
**Q2**: 透明代理也不通,说明它检测更深(比如网卡异常/连接源 IP)?
**A2**: 转向【网关级抓包】:把手机连到自己控制的 WiFi,在网关(OpenWrt + tcpdump / mitmproxy)抓,App 从任何角度看都是正常出网。
**Q3**: 全都不行,App 有证书 pin?
**A3**: 回到 pin 绕过链。抓包工具层面已经透明了,剩下的就是 pin 的正面对抗。

### 定位加密函数在 Native 层的具体地址
**Q1**: 已经知道加密在 so 里,但函数名剥离/静态分析花时间。怎么快?
**A1**: 用行为定位——hook libc 的 send/write,拿到密文的调用栈(Thread.backtrace),栈里最上层的 so 地址就是加密函数附近;偏移到符号表(哪怕 stripped 也有一定范围)。
**Q2**: 栈上都是 libc/libart,看不到业务 so?
**A2**: 说明中间有异步(线程/handler)分割。改为 hook Java 层能触发加密的最上层调用(比如 request 构造),再顺着 JNI 调用往下追。
**Q3**: JNI 层用了动态注册(RegisterNatives),看不到函数名对应?
**A3**: hook RegisterNatives 本身,把 (name, signature, fnPtr) 三元组打印出来,直接得到 java 方法名到 native 地址的完整映射表。

### 抓到数据后如何优雅构造 Fuzz
**Q1**: 已经能任意加解密,想 fuzz 服务端接口,从哪些参数下手?
**A1**: 优先级:(1) 数值类型(id、amount、offset、limit)——边界、负数、超大;(2) 字符串——SQLi/SSTI/XXE 特征;(3) 状态字段(status、role、type)——越权;(4) 未在文档里出现但客户端偶尔带的隐藏字段(debug=1、mock=true)。
**Q2**: 服务端每次拒绝,分不清是签名坏了还是 fuzz payload 触发了 WAF?
**A2**: 固定基准。先发一次已知合法请求确认签名和链路都好,再单独改一个字段,响应有差异就是这个字段的语义反馈。控制变量法。
**Q3**: fuzz 中触发疑似 SQL 报错但不完整?
**A3**: 转专项——上 sqlmap 或自写盲注,针对该参数;把签名部分做成可自动重算的中间件(mitmproxy addon),sqlmap 的每个请求都自动重签。
**Q4**: 服务端限流严格,fuzz 一开就 429?
**A4**: 正交转向:降低速率 + 走多账号/多 IP 轮换;或者放弃通用 fuzz,精读文档/接口做定点漏洞挖掘。

### 客户端有热更新/远程配置
**Q1**: 抓包发现启动后有一个大 zip/js/lua 下发,可能是热更新?
**A1**: 保存下来。里面可能包含新接口、测试功能、隐藏配置、调试开关。热更新包解密 key 通常也在客户端。
**Q2**: 热更新包签名校验严格,篡改后客户端拒绝加载?
**A2**: 若目的是【读】就够了,不需要篡改;若要投毒,转向 hook 校验函数返回 true。
**Q3**: 热更新触发条件是特定用户/AB test?
**A3**: 转向修改客户端上报的 user_group / abtest_id / version 参数,让服务端下发实验组配置。

### 分析多设备/多账号相关的会话隔离
**Q1**: 抓到 A 账号的会话,想用到 B 账号,发现服务端严格绑定 deviceId?
**A1**: 说明服务端做了设备-账号绑定校验。改 deviceId 需要连带改 pushToken、androidId 等全套指纹,单改一个不够。
**Q2**: 拿到 B 账号的会话样本对比一下,差异字段都是什么?
**A2**: diff 出所有字段,分类:【真正的会话凭证】(token)、【设备维度】(deviceId, uuid)、【业务维度】(uid, roleId)。要跨账号访问只需前两类整体替换。
**Q3**: 服务端会主动比对 A 请求发来的 deviceId 和历史绑定,冒用会触发告警?
**A3**: 转向【获取 B 账号真实设备信息】——如果目标是拿数据不是攻击 B,别硬冒用,而是找【垂直越权】(用 A 账号访问 B 的资源 id)。

### 抓到看起来是密文实际是压缩
**Q1**: body 高熵,以为是加密,gzip/zstd 头也没有?
**A1**: 别急着当加密。看前 2 字节:0x1F8B 是 gzip、0x789C/0x78DA 是 zlib、0x28B52FFD 是 zstd、0x04224D18 是 lz4。有些 App 会剥掉 magic 直接传 raw deflate,试试直接 inflate(-15) 解。
**Q2**: 解压后是明文?
**A2**: 那压根不是加密,只是压缩。攻击者赚到——直接分析即可。
**Q3**: 解压后还是二进制?
**A3**: 那是"压缩后加密"或"加密后压缩",按加密链继续走。

### 决定用 Frida 还是 Xposed 还是 静态 patch
**Q1**: 想做一次持久化的绕过,frida / xposed / 静态修改 apk,选哪个?
**A1**: 场景决定:(a) 短期调试/多次尝试 → frida,快速迭代;(b) 长期使用 + 多次启动 → xposed/lsposed,持久稳定;(c) 分享给别人用/不想装 root → 静态 patch apk 重打包。
**Q2**: 目标只想跑一次拿数据,选 frida 但 App 有反 frida?
**A2**: 转 frida-gadget 静态注入(把 frida runtime 打包进 apk),规避运行时检测。
**Q3**: 想在生产上让某功能长期可用?
**A3**: 静态 patch smali 是最稳的,但要过签名校验——重签名后需要绕签名检测(hook PackageManager.getPackageInfo 的 signatures 字段返回原签名)。

### 数据关联分析 一次操作背后有多少请求
**Q1**: 点击一次功能,App 发出好几个请求,哪个才是"关键那个"?
**A1**: 按响应体大小、状态码、路径关键字排序。真正干活的接口一般带业务动词(order/create/submit/pay/confirm),辅助接口带日志/上报动词(report/track/log/stat)。
**Q2**: 关键接口依赖前几个的返回值(比如 preOrder → order)?
**A2**: 建立依赖链——手动执行一次,记录每个请求的输入输出关系,标出哪些字段是"上一个响应带下来的"。这就是完整业务流,重放时必须按序。
**Q3**: 想跳过前置步骤直接调用最后一步?
**A3**: 尝试。如果服务端每一步都会写状态(sessionStorage/redis),缺前置就报错;如果只是客户端自己的引导,直接跳过。判据:响应错误信息里有没有"上一步未完成"类语义。

### 抓包环境正常但只有某类请求上不去
**Q1**: 大部分请求都通,只有某个域名/接口不通?
**A1**: 该接口可能:(1) 走了独立域名的 pin;(2) 用了不同的加密通道(比如支付走独立 SDK);(3) 强制使用某个 IP 段/CDN。分别用 nslookup + tcpdump 定位。
**Q2**: 是独立 SDK(比如支付/风控)自己的通道?
**A2**: 单独绕它——支付 SDK 通常有自己的 pin 和加密,hook 目标是那个 SDK 的类,不是宿主 App。
**Q3**: 这个接口极度重要绕不掉、时间紧?
**A3**: 正交转向:不打这个接口,查它的【上游/下游】——支付一定有下单前的接口、下单后的接口,前后两端往往鉴权弱,可以从那里横切。

### 判断某字段是【时间戳】还是【递增 id】还是【随机 nonce】
**Q1**: 请求里有个 13/10 位数字字段,不知道是啥?
**A1**: 长度判定——10 位是秒级 timestamp,13 位是毫秒。多次采样看差值,差值等于两次请求的实际间隔 → 时间戳;差值极小且总在递增 → seq;差值杂乱 → 随机 nonce。
**Q2**: 长度既不是 10 也不是 13,是 16 位?
**A2**: 可能是 timestamp+随机后缀,或纳秒级 timestamp,或雪花算法 id。看高位是否跟着时间走。
**Q3**: 字段是十六进制或 base64 的短串?
**A3**: 大概率是 uuid 变体或客户端 request id,通常只用于日志追踪,不参与鉴权。
**Q4**: 拿不准影响不影响鉴权?
**A4**: 二分测试——把该字段清空发一次,或改一个字节发一次,看响应:(a) 直接成功 → 服务端不校验此字段;(b) 报签名错 → 参与签名但未必参与鉴权;(c) 报"过期/重复" → 参与反重放。据此决定要不要花力气伪造。
