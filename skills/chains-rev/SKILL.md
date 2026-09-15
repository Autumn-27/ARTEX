---
name: chains-rev
description: 逆向分析反问决策链原文索引(静态切入/去混淆反反调试/动态调试试断点策略/算法加密还原)。拿到陌生二进制、加壳混淆、逆向卡住时查阅。
---

# chains-rev · 逆向分析反问决策链(L4 原文索引)

## 何时使用

- 拿到陌生 ELF/PE,要定第一分钟的切入顺序(rev-static-entry、rev-deobf)
- 怀疑加壳/混淆,要分诊标准壳 vs 定制混淆(rev-deobf)
- static+stripped 无符号,要做符号恢复与语义定位(rev-deobf)
- 要定动态调试与断点策略(rev-dynamic-debug)
- 要还原算法/加密逻辑(rev-algo-recover)

## 铁律

- 第一分钟零成本三连:file(架构/链接/strip)→ checksec(保护)→ strings(线索),这三步决定后面所有路线(refs/rev-static-entry.md)
- static+stripped 是常态而非异常:从入口(__libc_start_main)和常量反推,而非按名字找(refs/rev-deobf.md)
- 高熵(>7.2)大段 + 特征节名(UPX0/UPX1)= 加壳,先脱壳再回到静态流程(refs/rev-static-entry.md)
- 标准壳看入口 stub 可秒脱;定制混淆(花指令/控制流平坦化)要长期作战,投入天差地别先分诊(refs/rev-deobf.md)
- 菜单文本/格式串是宝贝:交叉引用直接落到命令分发与核心逻辑(refs/rev-static-entry.md)

## refs/ 原文清单

- `refs/rev-algo-recover.md` — 算法/加密还原的思路
- `refs/rev-deobf.md` — 去混淆与反反调试
- `refs/rev-dynamic-debug.md` — 动态调试与断点策略
- `refs/rev-static-entry.md` — 逆向静态切入与目标函数定位

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
