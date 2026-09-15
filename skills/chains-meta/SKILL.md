---
name: chains-meta
description: 元决策反问决策链原文索引(卡住自检/观测真伪/正交转向/放弃判据/优先级排序/命门收口)。主要面向 planner:派意图、判卡住、决定转向或收口时查阅。
---

# chains-meta · 元决策反问决策链(L4 原文索引)

> 本类**主要面向 planner**:派工排序、监测 worker 是否卡住、判定转向/放弃/收口时机。
> worker 仅在需要自证观测真伪或卡住自检时查阅对应篇目。

## 何时使用

- worker 长期无产出,要判是真卡住还是在爬坡(meta-stuck-detect)
- 观测结果可疑(200 无回显、响应全同质),要判工具/环境是否在骗人(meta-trust-obs)
- 单点死磕过久,要定"换维度"阈值与转向轴(meta-orthogonal)
- 要判一个入口是否榨干、该不该放弃(meta-give-up)
- 侦察结束面对一堆目标,要排先打哪后打哪(meta-priority)
- 命门在手(getshell 原语已握),要强制收口防发散(meta-close-the-kill)

## 铁律

- 连续 15~25 次尝试响应完全同质(无新状态码/长度/时序差异)= 信息梯度为零,立即换维度,别凭"感觉快成了"续命(refs/meta-orthogonal.md)
- 换的是维度(轴),不是同一轴上的参数值;转向前留"挂起标记"(卡在哪、试过哪些、何时回来)(refs/meta-orthogonal.md)
- 正常/异常请求被抹平成同一页面 = 统一拦截层接住,请求根本没到应用层(refs/meta-stuck-detect.md)
- 回显阴性从不等于漏洞阴性:200 + 无回显先做阴性/阳性对照,排除 WAF 伪造响应再下结论(refs/meta-trust-obs.md)
- 命门在手先收口:能直接转 shell/flag 的原语握紧时,发散 = 自杀,确认打死才准发散(refs/meta-close-the-kill.md)

## refs/ 原文清单

- `refs/meta-close-the-kill.md` — 到了命门先收口:别在能拿 shell 的位置掉头发散(含平台耦合内容:s2 节"黑板 remember/recall"已在 JSON 标记 platform_specific)
- `refs/meta-give-up.md` — 何时放弃一条路、何时回头(覆盖矩阵判榨干)
- `refs/meta-orthogonal.md` — 换维度:正交路径的枚举
- `refs/meta-priority.md` — 性价比排序:先打哪后打哪
- `refs/meta-stuck-detect.md` — 卡住自检:被墙的信号识别
- `refs/meta-trust-obs.md` — 把观测当真还是当假:工具/环境是否在骗我

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
