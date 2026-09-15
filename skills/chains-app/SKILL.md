---
name: chains-app
description: 移动/客户端应用反问决策链原文索引(APK 逆向/加固脱壳/Frida hook/协议加密还原/本地密钥/小程序/C-S 抓包)。分析 App、抓不出包、要 hook 或脱壳时查阅。
---

# chains-app · App 客户端反问决策链(L4 原文索引)

## 何时使用

- 拿到陌生 APK/IPA,要定静态侦察顺序与切入点(app-apk-recon、app-frida)
- 怀疑 App 加固,要判壳、识别厂商、选脱壳路线(app-unpack)
- 要 hook 加解密/校验逻辑,要定 hook 层级(Java 还是 native)(app-frida、app-proto-crypto)
- C/S 架构抓不到包或协议看不懂(app-cs-capture、app-proto-crypto)
- 要找客户端本地存储/硬编码的密钥凭据(app-local-secrets)
- 目标是小程序/H5/移动 API 攻击面(app-miniapp)

## 铁律

- 先静态侦察建地图(Manifest、语言栈、lib 下的特征 so),再决定 hook 什么;别一上来 frida-trace(refs/app-frida.md)
- 主 dex 找不到业务代码基本就是壳,别在壳的 Loader 上浪费时间读代码(refs/app-unpack.md)
- 加固厂商决定脱壳打法:靠 SO 名/包名/行为指纹定厂商,特征模糊就转行为指纹(refs/app-unpack.md)
- 静态看不出加密算法就转动态广谱 hook(Cipher/Mac/MessageDigest/Base64),让运行时告诉你算法(refs/app-frida.md)
- frida-server 与 host 端版本、架构必须严格匹配,`frida-ps -U` 能列进程才算通道通(refs/app-frida.md)

## refs/ 原文清单

- `refs/app-apk-recon.md` — APK 逆向抽接口与密钥
- `refs/app-cs-capture.md` — C/S 架构抓包与协议分析
- `refs/app-frida.md` — Frida/hook 动态分析决策
- `refs/app-local-secrets.md` — 客户端本地存储与硬编码利用
- `refs/app-miniapp.md` — 小程序/H5/移动 API 攻击面
- `refs/app-proto-crypto.md` — 通信协议与加密还原
- `refs/app-unpack.md` — 加固脱壳的判断

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
