---
name: chains-ad
description: AD 域渗透反问决策链原文索引(内网测绘/Kerberos/委派/ADCS/凭据薅取/口令喷洒/DCSync/免认证 CVE/直捣域控 vs 横向)。拿到域内立足点、要选攻击路径或转向时查阅。
---

# chains-ad · AD 域渗透反问决策链(L4 原文索引)

## 何时使用

- 刚在域内主机拿到 shell,要定第一轮收集顺序(ad-recon)
- 手上有域凭据,要走 Kerberoast/AS-REP/委派/ADCS 选路(ad-kerberos、ad-constrained-delegation、ad-adcs)
- 要薅本机/凭据库(mimikatz/SAM/票据)或做口令喷洒(ad-cred-harvest、ad-password-spray)
- 纠结"直捣域控还是先横向薅凭据"(ad-direct-vs-lateral)
- 遇到 Zerologon 类免认证洞,要评估适用条件与风险(ad-noauth-cve)
- 已到收尾阶段,要 DCSync/拿 flag(ad-dcsync-flag);链路受限要调操作节奏(ad-tunnel-friendly)

## 铁律

- 内网侦察先做零主动流量的本机自省(whoami/ipconfig/netstat/arp),被动信息答完"域名、DC 在哪、网段、有无域凭据"四问再考虑主动扫(refs/ad-recon.md)
- 优先"查域"而非"扫网":net view /domain、SRV 记录定位 DC,域本身就是建好的资产库(refs/ad-recon.md)
- Kerberoast 前先离线过滤 SPN(排除机器账号/krbtgt,优先服务账号),盲扫 = 给 SOC 送完整攻击画像(refs/ad-kerberos.md)
- 凭据开打前先验证三前提:时钟偏差 ≤5 分钟、能 kinit、能读 LDAP SPN(refs/ad-kerberos.md)
- 离线爆破不是唯一变现:Roast 被墙果断转 AS-REP/委派链/ADCS/口令喷洒(refs/ad-kerberos.md)

## refs/ 原文清单

- `refs/ad-adcs.md` — ADCS ESC 证书攻击选路
- `refs/ad-constrained-delegation.md` — 委派滥用:约束/非约束/RBCD 打到域管
- `refs/ad-cred-harvest.md` — 凭据薅取:mimikatz/SAM/票据
- `refs/ad-dcsync-flag.md` — DCSync 与拿 flag 收尾
- `refs/ad-direct-vs-lateral.md` — 直捣域控 vs 横向薅凭据的转向决策
- `refs/ad-kerberos.md` — Kerberos 攻击:Kerberoast/AS-REP/委派
- `refs/ad-noauth-cve.md` — Zerologon/免认证漏洞的适用与风险
- `refs/ad-password-spray.md` — 弱口令/口令喷洒/复用的节奏
- `refs/ad-recon.md` — 内网测绘与主机分类排序
- `refs/ad-tunnel-friendly.md` — 受限链路下的操作节奏与请求往返开销权衡

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
