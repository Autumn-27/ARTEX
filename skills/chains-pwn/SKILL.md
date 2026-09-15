---
name: chains-pwn
description: PWN 二进制利用反问决策链原文索引(栈溢出/堆漏洞 UAF/利用链与保护绕过)。做 pwn 题或二进制利用、要定利用总路线时查阅。
---

# chains-pwn · 二进制利用反问决策链(L4 原文索引)

## 何时使用

- 拿到未知 ELF,要用保护机制定利用总路线(pwn-stack)
- 菜单类交互程序,要判是不是堆题、定 libc 版本(pwn-heap)
- 保护全开要规划"先 leak 后打"的两阶段利用(pwn-stack)
- 利用链构造中要绕过 Canary/NX/PIE/RELRO(pwn-exploit-chain)

## 铁律

- checksec 五个开关(Canary/NX/PIE/RELRO/符号)决定利用骨架,总路线先于逆向细节(refs/pwn-stack.md)
- 保护全开 = "必须先 leak 后打"两阶段;保护全关就别往复杂里做,利用复杂度应与保护强度成正比(refs/pwn-stack.md)
- 别只信 checksec:用 vmmap 在运行时确认段权限,假设放到运行时验证(refs/pwn-stack.md)
- 菜单含 add/delete + malloc/free 组合基本定性堆题;定性后先抓 libc 版本,版本决定全部利用姿势(refs/pwn-heap.md)
- 静态编译/musl/uclibc 别套 glibc 打法,当"未知 allocator 逆向"另起炉灶(refs/pwn-heap.md)

## refs/ 原文清单

- `refs/pwn-exploit-chain.md` — 利用链构造与保护绕过决策
- `refs/pwn-heap.md` — 堆漏洞(UAF/overflow)的判断
- `refs/pwn-stack.md` — 栈溢出漏洞定位与利用取舍

## 用法

先用本索引定位篇目与场景节(`### 场景句`),再 Read 对应文件;回答/决策时优先遵循其中的判据与转向规则。worker 无 Glob/Grep,按上面的精确相对路径直接 Read。
