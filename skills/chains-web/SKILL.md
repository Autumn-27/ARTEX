---
name: chains-web
description: Web 漏洞反问决策链原文索引(SQLi/上传/反序列化/SSRF/SSTI/XXE/命令注入/逻辑越权/认证绕过/CMS/WAF 绕过/落地 shell)。打 Web 目标、payload 被拦、判定存疑、需要转向时查阅。
---

# chains-web · Web 漏洞反问决策链(L4 原文索引)

## 何时使用

- 拿到带参接口,要判定注不注得动、是哪种数据库/注入类型(web-sqli)
- payload 被拦(403/拦截页/限流),要定位拦截层级并选绕过维度(web-waf-bypass)
- 有上传/反序列化/SSTI/XXE/SSRF/命令注入入口,要定判定顺序与利用路径
- 认证会话类目标:JWT/OAuth/SSO 攻击、登录绕过(web-token-attack、web-auth-bypass)
- 识别出 CMS,要走"识别→已知漏洞→后台 getshell"捷径(web-cms)
- 已有 Web 漏洞,要落地成系统 shell(web-to-shell)

## 铁律

- 判注入看"可复现的真假分叉"而非"报错"——报错只说明语法变了,分叉才说明表达式进了执行逻辑(refs/web-sqli.md)
- 状态码 200 不等于打通:WAF 会伪造 200,先做良性/恶意对照再信响应(refs/web-waf-bypass.md)
- 上传先传良性文件看三件事:路径回显、重命名策略、可否直接访问,再谈 webshell(refs/web-upload.md)
- 单点打不动先换注入位置/参数面/协议入口(query→body→header→cookie→JSON),别死磕同一参数(refs/web-sqli.md)
- 落地 shell 前先填满三格:数据库类型、当前用户权限、DB 与 Web 是否同机(refs/web-to-shell.md)

## refs/ 原文清单

- `refs/web-auth-bypass.md` — 认证与会话绕过的思路
- `refs/web-cmdi.md` — 命令注入/代码注入的定位
- `refs/web-cms.md` — CMS 通用打法:识别→已知漏洞→后台 getshell
- `refs/web-deser.md` — 反序列化漏洞的判定与链构造
- `refs/web-logic.md` — 业务逻辑与越权漏洞
- `refs/web-sqli.md` — SQL 注入:判定→利用→读写→提权
- `refs/web-ssrf.md` — SSRF 的探测与打内网
- `refs/web-ssti.md` — SSTI 模板注入的识别与利用
- `refs/web-to-shell.md` — 从 Web 漏洞落地到系统 shell
- `refs/web-token-attack.md` — JWT/OAuth/SSO 等认证机制攻击
- `refs/web-upload.md` — 文件上传绕过决策
- `refs/web-waf-bypass.md` — WAF/过滤下的绕过决策
- `refs/web-xxe.md` — XXE 的识别与外带

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
