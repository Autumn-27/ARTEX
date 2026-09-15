---
name: chains-priv
description: 提权与立足点反问决策链原文索引(Linux/Windows 提权决策树/执行原语分级/disable_functions 绕过/容器云逃逸/立足点侦察/持久化)。拿到低权 shell、执行受限、要提权或站稳时查阅。
---

# chains-priv · 提权立足点反问决策链(L4 原文索引)

## 何时使用

- 刚落地低权 shell(www-data/普通用户),要定枚举顺序(priv-linux、priv-foothold-recon)
- 执行受限:disable_functions、沙箱、spawn 被拦,要选执行原语(priv-exec-primitive、priv-disablefunc)
- 要打 Linux/Windows 提权决策树,给枚举出的向量排优先级(priv-linux、priv-windows)
- 目标是容器/云环境,要判逃逸与提权可能(priv-container-cloud)
- shell 不稳定或要权衡持久化手段(priv-persistence)

## 铁律

- 枚举按"低成本高命中"漏斗:一条命令出结论的先跑(id/sudo -l/uname/ss),要翻一堆文件的排后面(refs/priv-linux.md)
- 向量按"确定性 × 隐蔽性 × 稳定性"排序:配置类提权(sudo/suid/cron/cap)榨干再考虑内核 exp(refs/priv-linux.md)
- 先给原语分级:文件读 < 文件写 < 受限求值 < 任意代码执行 < 命令执行,从能确证的一级出发规划,别默认最高级(refs/priv-exec-primitive.md)
- spawn 被拦就把"命令执行"执念降级成"要达成什么效果"(读凭据/横向连通/持久化),语言层原语覆盖多数刚需(refs/priv-exec-primitive.md)
- 盲态下带外(OOB)是最可靠的"原语存在性"证据,比时间盲注稳(refs/priv-exec-primitive.md)

## refs/ 原文清单

- `refs/priv-container-cloud.md` — 容器/云环境逃逸与提权判断
- `refs/priv-disablefunc.md` — disable_functions/受限执行绕过
- `refs/priv-exec-primitive.md` — 命令执行受限时执行原语的选择
- `refs/priv-foothold-recon.md` — 拿到立足点后的第一轮内网信息收集
- `refs/priv-linux.md` — Linux 提权决策树
- `refs/priv-persistence.md` — 持久化与稳定性取舍
- `refs/priv-windows.md` — Windows 提权决策树

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
