---
name: chains-recon
description: 信息收集反问决策链原文索引(资产测绘/指纹识别/JS 接口抽取/泄露猎取/OSINT/隐藏参数/何时够了的判断)。侦察阶段、扫描拿不准收手时机、要找入口时查阅。
---

# chains-recon · 信息收集反问决策链(L4 原文索引)

## 何时使用

- 只有一个 URL/IP,要定侦察优先级与顺序(recon-asset-surface、recon-fingerprint)
- 扫描结果一堆,纠结继续扫还是收手手工验证(recon-when-enough)
- 要挖泄露面:备份/.git/.env/swagger/JS 密钥(recon-leak-hunting、recon-js-endpoints)
- 要挖隐藏参数与未授权接口(recon-param-mining)
- 技术面榨干,要转 OSINT/人的攻击面(recon-osint)

## 铁律

- 收手判据不是"还有没有没扫的端口",而是"已拿到的东西里有没有能形成攻击假设的线索";扫描边际收益在第一轮后急剧衰减(refs/recon-when-enough.md)
- 先扫"暴露即高危"的确定性目标(.git/.svn/.env/常见备份/swagger),优先级高于宽泛目录爆破(refs/recon-leak-hunting.md)
- 字典随技术栈切换(Java 找 WEB-INF、PHP 找 .php~、Node 找 .env/source map),别一套字典打天下(refs/recon-leak-hunting.md)
- 手工验证按"最小验证成本"排序:一个请求能证真/证伪的先验,死胡同要在几分钟内判死(refs/recon-when-enough.md)
- 命中泄露点先做广度快照再挑收益最高的深挖,别陷进第一个坑(refs/recon-leak-hunting.md)

## refs/ 原文清单

- `refs/recon-asset-surface.md` — 外网资产测绘与攻击面确定
- `refs/recon-fingerprint.md` — 子域/端口/服务指纹的取舍与顺序
- `refs/recon-js-endpoints.md` — JS 与前端里的接口路径和敏感信息抽取
- `refs/recon-leak-hunting.md` — 泄露猎取:备份/.git/.env/swagger/JS 密钥
- `refs/recon-osint.md` — OSINT 与人的攻击面
- `refs/recon-param-mining.md` — 隐藏参数与未授权接口挖掘
- `refs/recon-when-enough.md` — "信息够了没、何时该动手"的判断

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
