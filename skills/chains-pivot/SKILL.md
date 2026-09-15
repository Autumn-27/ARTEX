---
name: chains-pivot
description: 隧道与内网可达性反问决策链原文索引(代理选型/无法回连/单向 webshell 建可达/链路被阻断排障/多层 pivot)。跳板转发、回连失败、隧道不稳时查阅。
---

# chains-pivot · 隧道 pivot 反问决策链(L4 原文索引)

## 何时使用

- 拿到跳板,要判单跳直连够不够、要不要上 SOCKS/tun(pivot-tunnel-choice)
- 反向 shell 打不回来,要判出站是全面断还是选择性断(pivot-no-reverse)
- 只有 webshell 单向命令通道,要建立到内网段的可达性(pivot-oneway-webshell)
- 转发链路不稳/被中间设备阻断,要排障与转向(pivot-tunnel-blocked)
- 要做深段发现与多层 pivot(pivot-deep-discovery)

## 铁律

- 能直连就不引入代理层:每多一跳多一倍失败面,先做最小可达性验证(refs/pivot-tunnel-choice.md)
- "目标数 × 端口数"任一为集合才升级 SOCKS/tun 级路由;单端口够用就别硬做全端口代理(refs/pivot-tunnel-choice.md)
- 网络可达(数据平面)与控制通道(控制平面)分开处理,别混在一起判断(refs/pivot-tunnel-choice.md)
- 回连失败先分层验证(DNS/HTTP/ICMP),判"协议维度"还是"目的地维度"阻断,再选伪装或中转(refs/pivot-no-reverse.md)
- "从内主动出"全封就方向反转:找目标已对外监听的服务做正向 shell 入口(refs/pivot-no-reverse.md)

## refs/ 原文清单

- `refs/pivot-deep-discovery.md` — 深段发现与多层 pivot
- `refs/pivot-no-reverse.md` — 出站方向受限(无法回连)时的连接方向决策
- `refs/pivot-oneway-webshell.md` — 仅有单向命令通道时建立内部网段可达性(注意:原文末尾 Q2 缺 A2,语料截断)
- `refs/pivot-tunnel-blocked.md` — 转发链路不稳或被中间设备阻断时的排障与转向
- `refs/pivot-tunnel-choice.md` — 代理转发方案的选型判据(时延/稳定性/协议适配)

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
