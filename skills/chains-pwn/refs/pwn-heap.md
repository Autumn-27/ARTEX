# 逆向PWN · 堆漏洞(UAF/overflow)的判断

### 拿到二进制先判断"这题是不是堆题"
**Q1**: 手上一个 ELF,菜单类交互,还不知道走哪条利用路线,第一步怎么快速定性?
**A1**: 先跑 `checksec` + `file` + `strings | grep -i "gnu libc"`;再看 `nm/objdump` 里有没有 `malloc/free/calloc/realloc` 的引用,以及导入表里 `__libc_start_main`。如果菜单选项包含"add/create/new"和"delete/free/remove",大概率是堆题模板。
**Q2**: 如果没有明显的 add/delete 字样,只有一个大 buffer 循环读写,是不是就不是堆题?
**A2**: 不一定。看是否有 `malloc(user_controlled_size)`;有些题把菜单藏在 switch 里,选项号用数字。用 IDA 反编译 main 看有没有循环+switch+`malloc/free` 的组合,这才是判据。
**Q3**: 判断出是堆题后,下一步该干嘛?
**A3**: 立刻抓 libc 版本 —— 用 `ldd ./bin` 或题目附件里的 `libc.so.6` `strings | grep "GNU C Library"`。libc 版本决定后面所有的利用姿势(tcache 是否有 safe-linking、hook 是否被删)。
**Q4**: 如果这条思路走不通 —— 二进制里根本没链接 libc,是静态编译的 musl/uclibc,怎么办?
**A4**: 切换正交维度:静态编译意味着堆管理器可能是 musl 的 mallocng/oldmalloc、或自己实现的 allocator。不要套 glibc 打法,先 IDA 看 malloc 的实现,当成"未知 allocator 逆向"来做。musl mallocng 有专门的攻击面(meta 结构),要另起炉灶。
**Q5**: 逆向时看到菜单函数结构很奇怪,像有加密/混淆,值得深挖吗?
**A5**: 不值得先在混淆上耗。先确认"漏洞点"和"利用原语"存在与否 —— 混淆再重,漏洞逻辑最终会体现在 malloc/free/memcpy 附近。用动态跟踪(gdb 断在 malloc/free)反而更快。

### glibc 版本识别决定整套打法
**Q1**: 已经确认是堆题,拿到了 libc,但版本对不上任何我熟悉的分支,先看什么?
**A1**: 看四条水岭:①tcache(2.26+ 引入);②tcache safe-linking(2.32+);③__free_hook/__malloc_hook 是否存在(2.34 被删);④`_IO_wfile_jumps` 检查(2.24+ vtable check)。用这四点把 libc 归到"老中新超新"四档。
**Q2**: 判定为 2.34+ 之后,常规 hook 打法就废了,主打思路怎么调?
**A2**: 转向 FSOP(File Stream Oriented Programming)、`_IO_list_all` 攻击、`__exit_funcs` 篡改、或者直接改栈上返回地址(如果能 leak 栈)。核心思想:hook 没了就找"程序结束前一定会走到的间接调用点"。
**Q3**: 如果 libc 版本判断错了会怎样?
**A3**: 后果很严重 —— safe-linking 的 XOR 算法只在 2.32+ 生效,版本判错会导致 tcache poisoning 计算的地址是错的,fake chunk 打不出去还会触发 abort。判据:先本地起同版本 docker 或用 patchelf 换 libc 跑通再打远程。
**Q4**: 远程题不给 libc 文件,只给个 ip:port,怎么办?
**A4**: 转向"泄露判版本":先想办法泄露一个 libc 函数地址,拿到低 12 位偏移后去 libc.rip / libc-database 反查具体版本;或看 puts/printf 的实现末尾指令模式。判定版本前不要贸然打二段。
**Q5**: 拿到的 libc 是打过补丁的定制版怎么办?
**A5**: 换维度 —— 别硬套 CTF 姿势。用 IDA 看 `__malloc_hook`、`_IO_2_1_stdout_` 等关键符号周围有没有加校验;必要时把 malloc/free/exit 的关键路径反编译一遍,把定制版当"半未知 allocator"处理。

### 快速定位菜单四原语:add/delete/edit/show
**Q1**: 拿到堆题 IDA 里一堆 sub_XXX,怎么最快识别出增删改查?
**A1**: 抓特征:add 里必有 `malloc/calloc`,delete 里必有 `free`,edit 里有 `read/fgets` 且以数组索引开头,show 里有 `puts/printf/write` 输出堆内容。用交叉引用回溯到菜单 dispatcher,一次锁定四个函数。
**Q2**: 如果 show 函数不存在或者被删了,意味着什么?
**A2**: 意味着没有直接的 leak 原语 —— 这是题目在难度上的核心提示。转向:①uaf 后再 malloc 同大小,让 unsorted bin 残留的 fd/bk 落到某个 tcache 表项被 add 时读出;②靠 overflow 覆盖到一个还没释放的 chunk 上的 size,间接构造 leak。
**Q3**: 逆向发现 add 里 size 是硬编码常量,不是用户输入,怎么办?
**A3**: 说明 chunk 大小被锁死。看这个 size 落在哪个 bin(<0x90 tcache/fastbin;0x90-0x400 small;>0x400 large)。大小固定反而缩小了打法空间 —— 直接照那个 bin 的经典利用套路走。
**Q4**: edit 有个长度参数,但看着像有严格边界检查,是不是就没 overflow 了?
**A4**: 别下结论。检查:①边界比较是有符号还是无符号(有符号可以传负数);②长度 vs 实际 chunk 大小是不是 off-by-one;③是否用 strcpy/strcat 之类不看长度的函数。很多题的 overflow 就藏在"看似有检查"里。
**Q5**: 菜单里出现了看不懂的第 5、第 6 个选项(比如 gift/backdoor/debug)怎么办?
**A5**: 优先看!出题人加的非常规选项 90% 是关键原语 —— 可能给你一次任意读、任意写、或者直接 leak libc。先确认它的输入约束和副作用,再决定利用方案。
**Q6**: 如果整个二进制根本没有菜单,是一个 http server / 复杂业务逻辑,怎么办?
**A6**: 换维度:从"菜单题"思维切到"逻辑漏洞挖掘"思维。找业务里的 alloc/free 配对路径,画状态机图,寻找"某状态下 free 了但引用还在"的路径。这时逆向工作量大幅上升,考虑用 AFL/libfuzzer 辅助定位。

### 判断到底是 UAF 还是 overflow
**Q1**: 已经确定有堆漏洞,但不清楚具体是 UAF 还是溢出,怎么定性?
**A1**: 看 delete 函数 —— 释放后指针有没有置 NULL / 索引数组有没有清零。没清=UAF/double-free 可能;清了=大概率是 overflow 或 off-by-one。同时看 edit 允许写入的长度 vs 分配 size 是否严格相等。
**Q2**: 如果两个漏洞都存在,先打哪个?
**A2**: 优先打 UAF —— 通常 UAF 能直接拿到 fd 指针的修改能力,原语更强。overflow 要看能溢出多少字节,off-by-one 甚至 off-by-null 都需要精细堆布局。除非 UAF 被 tcache double-free 检查拦住,才回头打 overflow。
**Q3**: UAF 但 free 后没有 leak show 原语,判定难度陡增,怎么办?
**A3**: 转向"用 UAF 造 overflow":比如 UAF 覆盖某个 chunk 的 size,让它下次 free 时误合并到更大 chunk,从而构造 overlapping chunks,进而获得任意读写。UAF 是"元原语",不要局限于经典 tcache poisoning。
**Q4**: overflow 只有 1 字节 (off-by-one) 的情况下,判据怎么定?
**A4**: 看溢出的字节是不是恰好落在下一个 chunk 的 size 上,以及是 null-byte 溢出(off-by-null)还是任意值溢出。null-byte 溢出走 poison null byte / house of einherjar;任意值溢出走 unlink / chunk overlap。
**Q5**: 如果既不是标准 UAF 也不是 overflow,是"double free"呢?
**A5**: 2.29+ 有 tcache double-free 检查(key 字段)。判据:free 一次后 chunk[8] 位置会写 tcache_perthread 指针;二次 free 前要先想办法覆盖这个 key。这是"从 double-free 打法转向 tcache dup 打法"的信号。
**Q6**: 如果排查了所有常见漏洞都没找到,可能是自定义结构上的逻辑洞?
**A6**: 换维度:去看题目自己定义的 chunk header —— 很多题在 malloc 出的 buffer 里放 size/next/isused 之类字段,业务代码里"重复使用某个 index"就是逻辑 UAF。这类题不能靠 libc 常识,必须逆清楚数据结构。

### 无 show 原语时怎么泄露 libc
**Q1**: 有 UAF/overflow 原语但没有 show,无法直接 print 出堆内容,怎么泄露?
**A1**: 经典套路 —— 利用 unsorted bin 的 fd/bk 会指向 main_arena+0x60 附近的特性,把一个 unsorted bin chunk 的部分内容"透出到某个还能被 print 的地方"。方法:让一个业务字段(比如 name)和 chunk 内容 overlap。
**Q2**: 如果 chunk size 都 <0x90 只能进 tcache/fastbin,进不了 unsorted bin,怎么办?
**A2**: 转向"人为构造大 chunk":①连续 malloc 7 个占满 tcache 后再 free 第 8 个,就进 unsorted;②用 overflow 改 size 字段把小 chunk 伪造成大 chunk 再 free。这是无 show 题的基本功。
**Q3**: 上面的思路都用不了,题目限制 malloc 次数很少怎么办?
**A3**: 换维度:考虑不 leak libc 直接打 —— 比如 partial overwrite,只改低 2 字节,爆破高位(2^4=16 次尝试内 1/16 概率命中 one_gadget);或者靠 stack pivot 到已知位置的 shellcode。放弃"必须 leak"这个思维定势。
**Q4**: 判断"能否 partial overwrite"的关键条件是什么?
**A4**: ①目标地址与已知地址的高位对齐(比如都在 libc 或都在 heap);②可控的写入长度精确到字节;③爆破次数可接受(远程 1/16 概率,几分钟能打通)。三个都满足才值得走。
**Q5**: 有 stdout 但没有 puts 输出堆内容,能不能改 _IO_2_1_stdout_ 的 flags 来泄露?
**A5**: 可以走 house of orange 里的 FSOP leak —— 改 `_IO_2_1_stdout_` 的 `_IO_write_base` 让下次 puts 从更早的位置开始输出,顺带 leak 出后面的 libc 指针。前提:2.28-2.33 之间的版本,vtable check 已加但 stdout 结构没改。
**Q6**: 如果 sandbox 甚至禁用了所有输出,只有 alloc/free 可见,怎么办?
**A6**: 换维度:走 side channel —— 通过 alloc 是否失败(SIGSEGV/超时)、是否触发特定 abort message 来 oracle 出内存内容。这是极端难题的姿势,普通题不用考虑。

### 没有 edit 原语的困境
**Q1**: 有 add/delete/show 但没有 edit(只能整体写入一次),怎么打?
**A1**: 核心思路:"利用 free 本身的写入能力"。tcache/fastbin 的 free 会把 fd 指针写到 chunk 头,这就是隐式的 edit。UAF 后再 add 同大小 chunk,通过 add 时的初始化写入完成对 fd 的控制。
**Q2**: 如果 add 是 calloc 而非 malloc,会不会有额外坑?
**A2**: 是!calloc 不走 tcache,直接从 fastbin/smallbin 拿,并且会 memset 清零。这意味着 tcache poisoning 走 calloc 拿不出来。判据:如果 add 用 calloc,你必须构造让目标 chunk 走 unsorted 或 smallbin 路径。
**Q3**: 上一条思路失败 —— add 用 calloc 且 size 只能 tcache,怎么办?
**A3**: 转向:①先塞满 tcache(7 次 free 同 size),第 8 次进 fastbin/unsorted,再触发 calloc 就能拿;②改用 house of botcake 之类跨 bin 手法。放弃"直接 tcache poison"。
**Q4**: 如果 add 每次 size 都不同,还能塞满 tcache 吗?
**A4**: 不行 —— tcache 是按 size 分桶的。这时候要看题目允许的 size 范围,选择其中一个 size 集中操作;或者用 overflow 修改 chunk size 让不同大小合流。
**Q5**: add 时 size 有上限但可以负数吗?
**A5**: 看 size 变量的类型。有符号 int 传负数,malloc 内部当作巨大的 unsigned size_t 会失败并返回 NULL;但如果题目 add 后没检查返回值,就有"结构里指针为 NULL 却继续用"的空指针写入原语。这是一条正交路径。
**Q6**: 完全没有 edit 也没有 UAF 只有 overflow,还能打吗?
**A6**: 能,但难。转向 house of force(老 libc)、house of spirit、或者用 overflow 改隔壁 chunk 的 size 让 free 时进入错误的 bin。overflow-only 题的核心是"用 overflow 制造 free 时的行为畸变"。

### size 被限死在 tcache 范围
**Q1**: 题目只允许 size <0x80,全部走 tcache,fastbin/unsorted 都摸不到,怎么打?
**A1**: 别慌,tcache poisoning 本身就是最短路径:UAF 后改 fd → tcache 头指向任意地址 → 下次 malloc 直接返回。2.32+ 需要 safe-linking,记得对 fd 做 `(target >> 12) ^ target` 变换。
**Q2**: 但 tcache poisoning 需要 leak libc/heap,size 全在 tcache 又怎么 leak?
**A2**: 打破限制:先塞满 tcache(7 次 free)让第 8 次 free 走 unsorted → 出现 libc 指针;或塞满后第 8 次 free 前用 overflow 改 size 让它跨 bin。这是 tcache 题的通用破局。
**Q3**: safe-linking 下 leak heap 是必需的吗?
**A3**: 是。因为 fd 是 XOR 了 chunk 地址高位的 —— 你要写目标地址 T,必须知道 chunk 地址 P,写入 `P ^ T`。leak heap 优先级要提前到 leak libc 之前。
**Q4**: 如果 tcache poisoning 打过去但 malloc 时 abort 了 (aligned check),怎么办?
**A4**: 2.32+ 会检查 chunk 对齐(0x10)。你写的目标地址必须 16 字节对齐,不然 abort。判据:被拒说明目标要往前后微调找一个对齐点,通常 hook 前 8 字节的"垃圾字节"可容忍写入。
**Q5**: 想改的目标是 __free_hook 但 libc 版本 2.34+ 被删了,还有什么等价目标?
**A5**: 转向:①`_IO_list_all` → FSOP;②`stdout->vtable` → IO_wfile_jumps;③某个 `__exit_funcs` 元素;④直接改栈返回地址(需要额外 leak 栈)。tcache poisoning 是通用武器,目标要跟 libc 版本走。
**Q6**: 全部走 tcache 但 double-free 检查过不去怎么办?
**A6**: 换维度:先想办法清 tcache key —— UAF 后 add 一次会把 key 位置写业务数据,再 free 就能绕过。或者用 tcache stashing(smallbin 的绕过)完全避开 double-free 检查。

### 只能 free 一次的限制
**Q1**: 题目每个 chunk 只能 free 一次(delete 后置 NULL),UAF 打法废了,怎么办?
**A1**: 转向 overflow 或 house of einherjar / poison null byte,用溢出触发 free 时的 chunk 合并造成 overlapping chunks —— 效果等价于 UAF,不需要二次 free 同一个 chunk。
**Q2**: 如果 overflow 也只有 null byte,能干嘛?
**A2**: poison null byte:把下一个 chunk 的 size 低字节改成 0,导致 prev_size 校验失效,free 时错误合并。这是 off-by-null 的经典打法。前提是 size 高位是可控的、prev_size 字段可写。
**Q3**: 2.29+ 加了 chunk size vs prev_size 的一致性检查,poison null byte 直接打会 abort,怎么办?
**A3**: 转向 house of einherjar:精心构造 prev_size 和 PREV_INUSE 使检查通过。核心是先泄露 heap 基址,伪造出 prev_size = 目标 chunk 距离,让 free 时"消极合并"到自己控制的位置。
**Q4**: 单次 free 也没法触发合并(size 都在 tcache 里),怎么办?
**A4**: 换维度:①把 size 改大跨过 tcache 边界(0x420 以上直接进 unsorted/large);②走"逻辑漏洞"路线 —— 题目里可能有 idx 边界问题,能通过负索引访问不该访问的 chunk。
**Q5**: 判断"能否放弃 UAF/double-free 打法"的信号是什么?
**A5**: 三个信号中出现一个就该转向:①delete 后指针被清零且再无 use;②free 时 chunk 会被 memset 清空;③index 表项 free 后被完全销毁。这时把重心转到 overflow / 逻辑洞。

### tcache 中毒失败(safe-linking 拦截)
**Q1**: 2.32+ 打 tcache poisoning,malloc 返回后 abort 了,大概率哪里错了?
**A1**: safe-linking 校验失败。malloc 时会检查 fd 指向的 chunk 地址是否与当前 chunk 对齐相符。判据:①目标地址必须 0x10 对齐;②你写入的 fd 必须是 `pos ^ target`,不是原始 target;③爆破成功前需要精确的 heap base。
**Q2**: heap base leak 不到,只能爆破 12 位,值得吗?
**A2**: 值得。heap 基址中间 3 nibble 是随机的,mmap 层随机化。远程 1/4096 到 1/16 之间(取决于爆破策略),几百秒能拿。但优先想:能不能通过 unsorted bin 部分泄露 heap 地址。
**Q3**: 泄露了 heap 但 tcache poisoning 打过去还是崩,可能是啥?
**A3**: 检查:①chunk 地址算错了(没算 header 的 0x10 偏移);②目标不是 8 字节对齐;③tcache count 溢出。用 gdb + pwndbg 的 `tcache` 命令直接看链表状态,别靠脑补。
**Q4**: 如果 poisoning 反复失败准备放弃,转向什么?
**A4**: 转向 fastbin attack(size 0x20-0x80)、unsorted bin attack(任意写一个 main_arena+固定偏移到目标)、或直接 large bin attack。tcache 只是姿势之一,不要一根筋。
**Q5**: 打到 __free_hook 附近但触发 free 后没跳到 shell,可能是啥?
**A5**: ①hook 位置算错(不同 libc 版本 offset 不同);②hook 值写错(该写 system 但写成 malloc);③free 的参数不是 "/bin/sh" 字符串。检查:free(x) 会调 hook(x),x 必须指向 "/bin/sh" 或用 one_gadget 免参。

### 判断 chunk 落到哪个 bin
**Q1**: 逆向出 size 后,怎么快速判断这块 chunk free 后会落到哪个 bin?
**A1**: 记住四个阈值:size < 0x420(tcache);无 tcache 时 <=0x80 fastbin;<=0x400 smallbin;>=0x400 largebin;不匹配任何 bin 时先入 unsorted。注意 size 是"含 header"的 chunk size,而不是 malloc 请求的 size。
**Q2**: user 请求 malloc(0x18) 实际 chunk size 是多少?
**A2**: `chunk_size = (request + 0x10 + 0x0f) & ~0x0f`,最小 0x20。所以 malloc(0x18) → chunk 0x20。判据要用 chunk_size 不是 request_size。
**Q3**: 如果 tcache 满了会怎么落?
**A3**: tcache 每桶最多 7 个;满了之后同 size 的 free 走 fastbin(如果 size <=0x80)或 unsorted。这是"制造 unsorted"的核心技巧:塞满 tcache → 第 8 次 free 就进 unsorted → 拿到 libc leak。
**Q4**: 想让 chunk 进 large bin 需要多大?
**A4**: chunk >= 0x400。且 large bin 有排序,第一次 free 进 unsorted,只有下次 malloc 时才会被整理进 large bin。判据:必须先 free 一个大 chunk,再 malloc 一个更大的,才能触发整理。
**Q5**: 无论如何 size 都被卡在小范围,想触发大 bin 打法怎么办?
**A5**: 换维度:通过 overflow 或 UAF 篡改一个已经在 unsorted 里的 chunk 的 size,让它下次整理时被当成大 chunk 放到 large bin,然后打 large bin attack。这是不改 add size 也能触发大 bin 的姿势。

### __free_hook / __malloc_hook 被删的应对
**Q1**: libc 2.34+ hook 全删了,tcache poisoning 打哪?
**A1**: 优先级列表:①`_IO_list_all` (FSOP);②`stdout->vtable` + IO_wfile_jumps 打法;③`__exit_funcs` (需要触发 exit 或 return from main);④栈上返回地址(需要 leak 栈)。挨个试可行性。
**Q2**: FSOP 需要什么前置条件?
**A2**: ①能任意写一段可控数据(伪造 fake IO_FILE);②知道 libc 中某个函数地址(比如 system);③程序会走到 puts/exit 触发 stream flush。三者具备才能打。
**Q3**: 如果程序永远不 exit(死循环菜单),怎么触发 FSOP?
**A3**: 转向:①制造异常让 libc 内部调 malloc_printerr → 走到 stderr flush → 触发 vtable 调用;②改 `_dl_open_hook` 等 runtime 结构;③改 GOT 里的 puts/printf(如果非 Full RELRO)。
**Q4**: 打 `_IO_list_all` 后 abort 说明啥?
**A4**: 2.24+ 有 vtable check —— vtable 必须在 `__libc_IO_vtables` 区间内。绕过方式:①用 `_IO_wfile_jumps` 或 `_IO_str_jumps` 等合法 vtable + 精心构造字段;②走 house of apple(2.35+ 的主流打法),劫持 `_wide_data->_wide_vtable`。
**Q5**: 所有 IO 打法都失败,转向什么?
**A5**: 换维度:走 largebin attack + tls_dtor_list、或 `stderr` 的类似结构、或直接爆破 __environ 找栈然后覆盖 ret。IO 只是路径之一,不要死磕。

### Full RELRO 下的应对
**Q1**: checksec 显示 Full RELRO,GOT 不可写,打法有啥变化?
**A1**: 放弃 GOT hijack,转向:①改 libc 内的 hook(如果版本允许);②改栈上返回地址;③走 IO_FILE 攻击。Full RELRO 只是关了一扇门,不是关了所有门。
**Q2**: PIE + Full RELRO + 无 leak,还能打吗?
**A2**: 极难但能。看题目是不是给了 stack cookie 泄露路径,或者能不能用 partial overwrite 只改 libc 内低位。三者都不行就转向逻辑洞或其他 side channel。
**Q3**: PIE 但没开 Full RELRO 呢?
**A3**: 可以打 GOT。但打 GOT 前要 leak PIE 基址。判据:程序里有没有 puts(某个 got 里的地址)、或者堆里泄露到 PIE 指针的路径。
**Q4**: Full RELRO 下想改栈返回地址,怎么定位栈?
**A4**: 走 `environ` —— libc 里 `environ` 指向栈上的环境变量数组,任意读它就能 leak 栈。判据:tcache poisoning 打到 environ 位置,通过 add 读出栈地址,再算出 ret 地址位置。

### house of orange / large bin attack 选型
**Q1**: 什么情况下考虑 house of orange?
**A1**: 三个条件同时满足:①无 free 原语只有 malloc + overflow;②能改 top chunk 的 size;③libc 版本较老(2.24-2.28 之间最佳)。核心是通过篡改 top chunk 触发"自动 free"进 unsorted。
**Q2**: 2.29+ 加了 top chunk 校验,house of orange 还能打吗?
**A2**: 变难。转向 house of orange 的变种 —— 精确构造 fake top 让新 top 也过校验。或者放弃 house of orange 走 house of einherjar / botcake。
**Q3**: large bin attack 的核心原语是什么?
**A3**: 把一个 largebin chunk 的 `bk_nextsize` 改成 (目标地址 - 0x20),下次 malloc 触发 largebin 整理时,会在目标位置写入一个 chunk 地址。等价于"任意写一个 heap 指针"。
**Q4**: 拿这个"任意写 heap 指针"能干嘛?
**A4**: 写到 `_IO_list_all` 让它指向堆上伪造的 IO_FILE;或写到 `global_max_fast` 让所有 chunk free 都进 fastbin,把 fastbin 攻击面拉满。这是链条的一环,不是终点。
**Q5**: 想触发 largebin 但 size 上不去,怎么办?
**A5**: 换维度:先用 overflow 改一个中等 chunk 的 size 让它进入 largebin 范围,再触发整理。或者转到 tcache stashing —— 它也能"往任意位置写一个 heap 地址",且要求更低。

### 判断 IO_FILE 攻击链选型
**Q1**: 决定走 FSOP,面对多种 IO 攻击,怎么选?
**A1**: 按 libc 版本选:①2.23:直接改 vtable 无检查;②2.24-2.27:vtable check 但 hook 可用,IO 是备选;③2.28-2.33:hook + _IO_str_jumps / IO_wfile_jumps;④2.34+:house of apple 2 / house of some。
**Q2**: house of apple 2 需要什么条件?
**A2**: ①能任意写一大段可控内存(至少 0x100+ 字节);②leak libc 和 heap;③触发 exit 或 puts 走 stream flush。核心思路:劫持 `_wide_data->_wide_vtable->__doallocate` 指向 setcontext。
**Q3**: setcontext 打法链能拿 shell 但 sandbox 禁 execve 怎么办?
**A3**: 转向 ORW(open/read/write)链 —— setcontext 之后 pivot 栈到堆,堆上放 ROP 链 open/read/write flag。sandbox 检测要提前用 seccomp-tools 看 filter。
**Q4**: 打 IO 时 vtable check 过了但没跳转,咋回事?
**A4**: 检查 fake IO_FILE 的 flags、_lock、_mode 等字段。IO 内部有一堆隐式检查(_lock 不能为 0 之类)。用 gdb 单步看到底哪个字段拦住的。
**Q5**: 所有 IO 打法都试过了都不行,还有啥?
**A5**: 换维度:①`tls_dtor_list` 打法(需要 leak tls);②`__exit_funcs` 的 unprotect(2.34+ 加了 pointer encryption,需要额外 leak);③直接改 got 里的 free/malloc(非 Full RELRO)。

### 泄露 libc 基址失败的自救
**Q1**: 按套路走 unsorted bin leak 拿了个地址,但和 libc 基址对不上,怎么排?
**A1**: 先确认这个地址是不是 `main_arena+X`。main_arena 在 libc 数据段末尾,离 base 有 0x1e0000 附近的偏移(不同版本不同)。用 `libc.symbols['main_arena']` 或直接 gdb 里 `p &main_arena` 拿准确偏移。
**Q2**: 泄露的高位一直是 0x7f 但低位波动大,咋回事?
**A2**: 高位 0x7f 就是 libc 地址特征(64 位下 libc 通常在 0x7f... 区间)。低位波动是 ASLR。判据:多次运行看高位是否稳定 —— 稳定说明是 libc 内地址,不稳定要重新审视 leak 路径。
**Q3**: leak 出的数字看着像 heap 地址而不是 libc,啥情况?
**A3**: 说明 chunk 还没走 unsorted —— 它在 tcache/fastbin,fd 指向另一个 chunk。转向:塞满 tcache 让下一次 free 进 unsorted;或改 size 强制跨 bin。
**Q4**: 完全没有 leak 路径,怎么办?
**A4**: 换维度:①爆破部分地址(one_gadget 低 12 位不需 leak);②看能不能 leak 栈上残留的 libc 地址(比如 `__libc_start_main+X`);③走 stdout 的 partial overwrite 让 puts 泄露更多。leak 不是只有 unsorted 一条路。
**Q5**: leak 到的地址似乎对但 one_gadget 不 work,啥情况?
**A5**: one_gadget 需要满足特定寄存器约束(比如 `[rsp+0x30] == NULL`)。触发点不同约束不同 —— free_hook 触发时 rdi=chunk,可能条件不满足。换 one_gadget 编号或换 execve("/bin/sh", 0, 0) 的 ROP。

### 泄露 heap 基址的必要性判断
**Q1**: 什么时候必须 leak heap?
**A1**: ①safe-linking(2.32+)tcache poisoning;②largebin attack 需要精确 bk_nextsize;③IO 攻击里 fake IO_FILE 的地址;④house of einherjar 的 prev_size。这几种情况不 leak heap 打不动。
**Q2**: heap leak 通常怎么拿?
**A2**: ①tcache 的 fd 指针(2.32+ 是 XOR 后的,需要解);②smallbin/unsorted 的 fd/bk;③业务字段和 chunk 内容 overlap 时的顺带泄露。优先级:unsorted > smallbin > tcache。
**Q3**: 2.32+ tcache fd 是 XOR 后的,能反算 heap 地址吗?
**A3**: 能。`fd = (chunk_addr >> 12) ^ next`,如果 next 是已知的(比如 NULL 表示链表尾),fd 直接就是 (chunk_addr >> 12)。左移 12 位就是 heap 页起始。这是一个反算 leak 姿势。
**Q4**: 没有任何 leak 通道咋办?
**A4**: 换维度:爆破 —— heap 中间 3 nibble 随机,单机 1/4096,几秒钟能爆。但远程通常爆破成本高,先想别的路。

### off-by-one / off-by-null 特征识别
**Q1**: 逆向 edit 函数看到 `read(fd, buf, size)` 但 size 就是 chunk 分配的 size,是不是就没洞?
**A1**: 有可能有 off-by-one:`read(fd, buf, size+1)` 或 `buf[size]=0` 这类 null 终止操作。这是最容易漏的洞点 —— 一个 null byte 就够触发 poison null byte 打法。
**Q2**: 如果溢出的是任意 1 字节而非 null,怎么打?
**A2**: 转 house of einherjar 或直接改下一个 chunk 的 size 制造 chunk overlap。任意字节 off-by-one 比 null-byte 强,因为可以把 size 改大让 free 时的合并跨过多个 chunk。
**Q3**: 判定 off-by-one 是否可用的关键判据?
**A3**: ①chunk 是否紧邻(malloc 顺序连续);②下一个 chunk 是否被 free(prev_size 才生效);③size 检查是否严格。三者满足就是标准 poison null byte 场景。
**Q4**: off-by-one 但被 2.29+ size vs prev_size 检查挡住了,怎么办?
**A4**: 转 house of einherjar:通过精心构造 prev_size 让它指向自己伪造的 fake chunk,fake chunk 的 size 与 prev_size 一致过检查。前提:能 leak heap。
**Q5**: 排查完发现真的没有 off-by-one 但也没别的洞,怎么办?
**A5**: 换维度:重新审视 struct 布局 —— 有没有 union 字段的类型混淆、有没有 signed/unsigned 转换、有没有 realloc 的特殊行为。堆题的漏洞点常常不在明面上的 read/memcpy,而在业务逻辑。

### chunk 合并被 size mask 破坏
**Q1**: overflow 覆盖了下一个 chunk 的 size,free 时想触发合并,但直接 free 就 abort,咋回事?
**A1**: 合并触发的检查:①`chunk_size` 与 next chunk 的 `prev_size` 一致;②PREV_INUSE 位为 0;③2.29+ 还检查后一个 chunk 的 size。任何一个不满足就 abort。用 gdb 单步看是哪个 assert 挂的。
**Q2**: 想合并到"上方"chunk 怎么做?
**A2**: 触发方式:当前 chunk 的 prev_size 指向上一个 chunk,且 PREV_INUSE 为 0(表示上一个已 free)。手法:先 free 一个 chunk 制造"已 free"状态,再改中间 chunk 的 prev_size 触发向上合并。
**Q3**: 合并成功但没得到期望的 overlapping chunks,咋回事?
**A3**: 合并后的大 chunk 进 unsorted,下次 malloc 会切一小块出来,后半段的地址范围和原来的某个业务 chunk 重叠 —— 这才是 overlapping。要连续 malloc 到那个 size 才能拿到。
**Q4**: 合并到一半 tcache 里还有对应 size 的 entry,malloc 优先从 tcache 拿,拿不到大 chunk 咋办?
**A4**: 先耗尽 tcache 里的 entry(malloc 7 次同 size),再 malloc 就必须走 unsorted 大 chunk。判据:每次操作前用 `tcache` 命令(pwndbg/pwngdb)确认 tcache 状态。

### seccomp 沙箱识别与绕过
**Q1**: 拿到题先跑 `seccomp-tools dump ./bin`,发现禁了 execve 但允许 open/read/write,策略咋定?
**A1**: 走 ORW ROP 链。前提是能劫持栈或走 SROP。堆题里通常配合 tcache poisoning 打 __free_hook = setcontext+61 → 迁移栈 → ORW ROP。
**Q2**: 如果连 open 都禁了呢?
**A2**: 转 `openat` (较新 libc 用 openat 而不是 open)。判据:看 seccomp filter 具体规则,openat/open/execveat 三个至少有一个开着。
**Q3**: 全禁了系统调用只留 exit,还能拿 flag 吗?
**A3**: 换维度:走 side channel —— 读 flag 后不打印,而是通过 alloc 大小、超时时长逐位泄露(比如 flag[i]=='A' 就 sleep 1 秒)。是攻击难度很高的姿势,但可行。
**Q4**: sandbox 允许 execve 但不允许 execveat,啥意思?
**A4**: 意味着可以直接走经典 system("/bin/sh") 或 one_gadget。判据:看到 execve 白名单就优先走最简路径。

### orw 链的组装
**Q1**: 决定 orw,setcontext 已就位,rop 链怎么组?
**A1**: 三段:`open("./flag", 0)` → `read(3, buf, 0x100)` → `write(1, buf, 0x100)`。参数用 pop rdi/rsi/rdx gadget 装。libc 里的 pop rdx 通常在 `getcontext` 附近有 `pop rdx; ret`。
**Q2**: pop rdx 找不到怎么办?
**A2**: 用 `mov rdx, rXX; call rXX+X` 类 gadget(from ropper);或用 `mprotect` 让某段 rwx 后跳去 shellcode(shellcode 里可以随意 syscall)。
**Q3**: flag 文件名不确定咋办?
**A3**: 优先猜 `./flag`, `/flag`, `flag`, `flag.txt`。多试几次。或用 openat 遍历目录:先 open(".") 拿 fd → getdents 列目录 → 逐个 open。
**Q4**: read 后 write 出去但 stdout 被重定向或关了怎么办?
**A4**: fd 1 可能不通,用 fd 2 (stderr) 或直接反弹 socket。看题目环境:CTF 通常 stdout 通,一般不用担心。
**Q5**: ORW 链跑起来但 read 返回 -1,咋回事?
**A5**: ①open 返回的 fd 错了(不是 3,可能被前面的 setcontext 占用);②路径错;③参数寄存器被 setcontext 篡改。用 gdb 打断点看 open 返回值。转向:改用 syscall 直接调而不是 libc 的 read。

### environ 泄露栈地址的时机
**Q1**: 想改栈返回地址,怎么 leak 栈?
**A1**: libc 里 `environ` 符号是一个指向栈上环境变量数组的指针。任意读 environ 就能拿到栈地址。tcache poisoning 打到 environ - 8(为了让 malloc 返回 environ 位置)即可通过 add 读出栈值。
**Q2**: leak 栈后怎么算 ret 地址位置?
**A2**: environ 通常在栈的高位;当前函数的 ret 在低位。用 gdb 本地跑一次量出偏移(environ 到 return address 的差值)。远程通常也稳定(除非 stack padding 不同)。
**Q3**: 拿到 ret 地址后打 ROP 但没触发,啥情况?
**A3**: 只改 ret 一处不够,如果目标函数还没返回就没触发。要等程序自然 return。堆题菜单循环里 return 在 exit 时才触发,可能需要多次操作后 exit。
**Q4**: environ leak 拿不到有效值,啥可能?
**A4**: ①libc 版本对不上 environ offset 算错;②tcache poisoning 打偏了没落到 environ;③某些精简环境下 environ 为 NULL。转向:leak `__libc_argv` 或 `__environ` 的其他别名。

### calloc 特殊性带来的策略调整
**Q1**: add 用的是 calloc 不是 malloc,tcache poisoning 咋整?
**A1**: calloc 不从 tcache 拿(至少经典 glibc 是这样),会从 fastbin/smallbin/unsorted 里拿。策略:先让目标 chunk 从 tcache 溢出到 fastbin(塞满 7 个),再 calloc 就能拿到被污染的 chunk。
**Q2**: calloc 拿出来的 chunk 会 memset 清零,污染的 fd 会不会被清?
**A2**: 只清 user data 区,不清 chunk header 之外的空间。所以 tcache poisoning 已经把 chunk 指向目标了,calloc 出来的就是"目标地址+清零 user data",fd 已经完成了 poison 的使命。
**Q3**: 如果 calloc 请求的 size 恰好在 tcache 范围,fastbin 塞不动,咋办?
**A3**: 转向 unsorted:塞满 tcache 后释放一个大点的 chunk 进 unsorted,再切割出目标 size(calloc 会走这条路)。或者用 overflow 强制改 size 让 free 到 unsorted。
**Q4**: 完全无法绕过 calloc 的清零,能不能不用 tcache 打法?
**A4**: 换维度:走 largebin attack、house of orange 之类"不依赖 tcache 返回值"的姿势。calloc 只是让 tcache poisoning 变难,不是让所有堆打法都废。

### tcache stashing 攻击的适用判据
**Q1**: 什么时候考虑 tcache stashing 而不是 poisoning?
**A1**: 三个特征:①能操作 smallbin(size 0x90-0x400);②想"任意写一个 heap 指针到某位置"而不是"任意 malloc 到某位置";③double-free 检查过不去或 safe-linking 让 poisoning 变难。
**Q2**: tcache stashing 的原语是啥?
**A2**: smallbin 里的 chunk 被 stash 到 tcache 时,libc 会把它的 bk 位置的值当作有效 chunk 处理并写入某处(具体是 tcache_perthread 的对应桶或链表)。等价于"任意位置写一个 heap 指针"。
**Q3**: 有了这个原语想干嘛?
**A3**: 常见目标:①写到 `__free_hook` (老 libc,需要提前把 heap 上放 system 指针);②配合改 `_IO_list_all`;③改 `tcache_perthread_struct` 的 counts 让后续 tcache 打法生效。
**Q4**: tcache stashing 打过去没效果,咋排?
**A4**: 检查 smallbin 里的 chunk 顺序 —— stashing 是按 fifo 拿的,第一个 stash 走的是 head,你想控制的是 head 还是第二个要清楚。用 gdb 看 smallbin 状态。

### house of botcake 的适用条件
**Q1**: 什么时候用 house of botcake?
**A1**: 2.29+ 有 tcache double-free 检查(key 字段),但你想在 tcache 里 double free 时用。botcake 通过"塞满 tcache + unsorted 里 double-free"绕过检查,实现 tcache 里的 dup。
**Q2**: 具体步骤?
**A2**: ①malloc 7 个同 size 的 chunk;②free 前 6 个进 tcache;③malloc 一个 A 和 B 相邻;④free A B(此时 tcache 满,进 unsorted);⑤从 tcache 里 malloc 一个腾位置;⑥free B 再次(合法进 tcache);⑦再 free B(此时 tcache 有空位,再次进 tcache 触发 dup)。
**Q3**: 步骤太复杂容易错在哪?
**A3**: chunk 相邻性判定容易错。中间不能有 free 的间隙,否则 A、B free 时会合并导致 size 变化。用 gdb 每步看 bins 状态。
**Q4**: botcake 打不通,转向什么?
**A4**: 转向 house of einherjar / house of apple / large bin attack。botcake 只是 double-free 绕过的一种,不是唯一。或者从"要不要 double-free"这个前提反推 —— 也许根本不需要 double-free,用 UAF+overwrite 也能达到目的。

### 判断题目意图:拿 shell / orw / 局部修改
**Q1**: 拿到题不知道该冲 shell 还是 orw,怎么判断?
**A1**: 看 seccomp filter:①无 sandbox → 冲 shell(execve 允许);②禁 execve 允许 open/read/write → orw;③几乎全禁 → side channel。这一步定基调,不定错方向别开始写 exp。
**Q2**: 有些题目意图不是拿 flag 而是"修改某个全局变量",咋判?
**A2**: 极少见,通常是 CTF 里的"过 check" 类题。判据:main 里有 `if (var == secret) puts(flag)` 类逻辑。这时只需任意写把 var 改成 secret 即可,不用完整劫持流。
**Q3**: 判断错方向的代价?
**A3**: 冲 shell 但 sandbox 禁 execve,exp 打通 hook 但触发 SIGSYS,前功尽弃。判据:第一步就 seccomp-tools dump,别偷懒。
**Q4**: 中期发现方向错了,怎么调?
**A4**: 保留前半段(leak + hook 控制),换后半段的 payload(system → setcontext+ORW)。不用从头写,前半段的原语通常复用。

### 卡在 struct 布局逆向
**Q1**: 逆向 add 函数,看到 malloc 返回一个 buf,里面存业务字段,但字段偏移看不清,咋办?
**A1**: 动态调试胜过静态。gdb 断在 malloc 后,continue 一次,dump 出返回的 chunk 内容,人工填字段(name/size/ptr/next)。比 IDA 结构体推断快 10 倍。
**Q2**: 字段有 union / 位域, IDA 显示乱七八糟,咋办?
**A2**: 找该字段的所有交叉引用,看每个读写点的含义 —— union 的实际类型由业务上下文决定。多个读点解释成不同类型时,可能就是漏洞点(类型混淆)。
**Q3**: 结构里有函数指针字段,怎么打?
**A3**: 这是"内置 hook",直接 UAF/overflow 覆盖函数指针,调用时就控 rip。远比打 libc hook 简单,优先走这条。
**Q4**: 结构逆向卡了很久也搞不清,可以放弃逆向直接打吗?
**A4**: 换维度:模糊化打法 —— 用大量随机操作触发 crash,反推关键字段位置。或直接用 QEMU + libafl 半自动化。不是所有题都值得精细逆向。

### 堆布局(feng shui)不好使的应对
**Q1**: 想让 chunk A B C 按 A→B→C 顺序在堆上连续,但实际隔了别的 chunk,咋办?
**A1**: 检查是不是 malloc 时有隐式分配(比如第一次 malloc 时 libc 会分配 tcache_perthread_struct 0x290)。这些"寄生 chunk"会打乱顺序。判据:第一次 malloc 前先看 heap 里已经有啥。
**Q2**: 塞满 tcache 让下次 free 进 unsorted,但结果还是进 tcache,咋回事?
**A2**: tcache count 用的是 chunk_size 索引,不是请求 size。你 free 的可能是不同 chunk_size。检查:所有要塞进 tcache 的 chunk 的 size 是否完全一致。
**Q3**: 布局对了但 free 不触发预期合并,啥可能?
**A3**: PREV_INUSE 位没置对。free 的 chunk 相邻但下一个 chunk 的 PREV_INUSE 还是 1(表示上一个未 free),free 就不会合并。要看有没有 fastbin 陷阱(fastbin 里的 chunk PREV_INUSE 不会被清)。
**Q4**: 布局怎么调都不对,转向什么?
**A4**: 换维度:不追求精确布局,改用"泼水"打法 —— 大量 alloc 各种 size,统计概率上哪个成功。或者放弃当前打法链,改用不依赖布局的姿势(比如 largebin attack 只需要一个 largebin chunk)。

### 从利用往回追,重审漏洞点
**Q1**: 逆向出漏洞点,写了 exp 打不通,反复调都不行,咋办?
**A1**: 停止调 exp,回到源头:是不是漏洞点本身理解错了?重新在 gdb 里"手动触发漏洞",不带任何利用意图,只观察内存变化。很多时候你以为的 UAF 其实 free 时被 memset 了。
**Q2**: 手动触发确认漏洞真的存在,但 exp 还是不通,啥原因?
**A2**: 中间环节的 side effect —— malloc 时会分配 tcache_perthread_struct、free 时会更新 arena、show 会顺便触发 malloc/free。这些副作用会打乱预期。列一遍所有操作的副作用。
**Q3**: 排查过副作用还是不行,可能是啥?
**A3**: libc 版本细节 —— 你用的 libc 和实际远程差一个小版本,某个偏移或某个检查不同。用 patchelf 换本地 libc 到严格一致再打。
**Q4**: 全部排查都没问题但就是不通,最后手段?
**A4**: 换维度:换一条完全不同的利用路径。有时候某个打法链的某一环在这题恰好不 work(比如题目关了 stdout buffer 让 FSOP 失效),换路径反而快。别在一条路上死磕超过 2 小时。
**Q5**: 时间快用完了,exp 还没打通,怎么止损?
**A5**: 保留 leak 部分(如果已经能 leak libc/heap),尝试最简单的 partial overwrite 或爆破。放弃复杂链条,选一个概率 1/16 的爆破打通比一个 100% 但缺关键 leak 的链更值。

### 判断"是不是逻辑洞而非内存漏洞"
**Q1**: 花了很久没找到 UAF 或 overflow,是不是找错方向?
**A1**: 转向逻辑洞视角:①有没有 idx 边界(负数、超大)可以访问不该访问的 chunk;②有没有 realloc(0) 等价 free 的坑;③有没有 duplicate index 让两个 slot 指向同一个 chunk;④有没有整数溢出让 size 计算出错。
**Q2**: 找到一个 idx 越界读能干嘛?
**A2**: 越界读通常直接给 leak 原语。判据:index 表在 bss 段,越界读到附近的 libc 函数指针或栈地址,直接算基址。
**Q3**: 找到 duplicate index (两个 slot 指向同一 chunk) 咋利用?
**A3**: 等价于 UAF —— slot A 删除后 slot B 还能 use。所有 UAF 打法都适用。判据:delete 一次后另一个 slot 的 show 还能输出内容。
**Q4**: 整数溢出让 size 变得极大或极小,咋打?
**A4**: 极小(比如 0):malloc(0) 会返回一个最小 chunk,后续写入超过 0 字节就是 overflow。极大(比如 -1 → 0xffffffff):malloc 失败返回 NULL,后续解引用 NULL 是 crash 而非利用 —— 但 NULL 指针加偏移可以打(在有 mmap 到低地址的题里)。
**Q5**: 排查完这些都没有,还有啥可能?
**A5**: 换维度:题目本身可能是 misc/rev 混堆题。看 main 里的隐藏逻辑 —— 有没有隐藏 backdoor(未导出符号)、有没有基于时间/pid 的算法漏洞。堆题不等于必须堆漏洞。

### unsorted bin attack 的判据与替代
**Q1**: 什么时候应该考虑 unsorted bin attack?
**A1**: 有一个"任意写 main_arena+固定值(0x60 附近)到目标地址"的需求时。经典用途:改 `global_max_fast` 让所有 chunk 走 fastbin,或改一些全局标志。原语:改一个 unsorted bin chunk 的 bk 为 (target - 0x10),下次 malloc 时触发 unsorted 整理,libc 会把 chunk 地址写到 bk+0x10。
**Q2**: 2.28+ 加了 unsorted bin 完整性检查(fd/bk 一致性),还能打吗?
**A2**: 直接打不行了。转向 largebin attack —— 更严格但仍可打;或用 tcache poisoning 直接 malloc 到 global_max_fast 附近改。unsorted bin attack 在新版本主要作为组合链的一环,而非主打法。
**Q3**: unsorted bin attack 打完 target 变成了 main_arena+X 这个"垃圾值",这值本身有啥用?
**A3**: 判据:如果 target 是 `_IO_list_all`,那 main_arena+X 恰好指向一个 fake chunk 可控区域(通过精心选择让 X 匹配),就能触发 FSOP。这是 house of orange 老套路的关键一步。
**Q4**: 打完发现程序没崩也没利用效果,咋回事?
**A4**: 换维度:检查 target 处的值是不是真的被写入了。unsorted bin attack 只在 malloc 时触发,你要在改 bk 之后至少发起一次 malloc。判据:改完不做任何 alloc 是无效的。

### mmap chunk 的识别与利用
**Q1**: chunk size 极大(超过 128KB / M_MMAP_THRESHOLD)时会走什么路径?
**A1**: glibc 用 mmap 直接从内核申请,不进 arena。特征:chunk header 的 IS_MMAPPED (0x2) 位置位;free 时直接 munmap,不走 bin 逻辑。这类 chunk 打不了传统堆漏洞。
**Q2**: 如果 add 支持超大 size 且用户可控,能利用啥?
**A2**: 攻击 mmap chunk 的相对偏移 —— mmap 分配通常紧挨着 libc 或 heap(取决于 aslr policy),可以通过 leak mmap 地址反推 libc 基址。这是 leak 的一条捷径。
**Q3**: 想强制走 mmap 但 M_MMAP_THRESHOLD 太高,咋办?
**A3**: 转向:用 mallopt(M_MMAP_THRESHOLD, low_value) 需要程序调用点,通常没有。改走"多次 malloc 触发 arena 扩展",让 mmap sbrk 之外的辅助区域出现,间接影响布局。
**Q4**: 完全没法用 mmap chunk 攻击面,是不是就废了?
**A4**: 换维度:mmap 只是一条辅助路径,主打法还在 arena 内的 chunk 上。回归 UAF/overflow 主线,mmap chunk 当作"环境噪声"看待。

### 打通本地打不通远程的排查
**Q1**: 本地 exp 稳定打通,远程炸了,先看什么?
**A1**: 三查:①libc 版本是否精确一致(patchelf 换 libc 复现);②交互方式(recv/recvuntil 的时序 —— 远程网络延迟可能让 recv 提前返回);③是否吃了 EOF 或额外的输出(比如 banner 多了一行)。
**Q2**: 版本一致但还是不稳,咋办?
**A2**: 换 tube 类型:pwntools 的 remote/process 底层行为不同;试试用 `ssh` 上目标机器 process 一下,如果稳定就是网络层问题;不稳定就是环境差异(比如远程 stdout buffering 不同)。
**Q3**: recv 拿不到预期数据,咋排?
**A3**: 用 `context.log_level = 'debug'` 看实际字节流。经常是 recvuntil 匹配字符串多了个 tab / 少了个空格。远程 tty 行为和本地 pty 行为可能不同,导致 prompt 格式微妙差异。
**Q4**: 远程 exp 概率打通(比如 1/16),怎么优化?
**A4**: 换维度:写外层重试脚本,不 flag 就 kill 重来。用 pwntools 的 `try/except EOFError` 循环,记录成功率。别在单次上死磕稳定性,概率打法接受 3-5% 成功率也够拿 flag。
**Q5**: 远程会话建立不上或频繁断开,咋办?
**A5**: 检查:①目标 flag 是否限流(rate limit);②本地网络是否有代理丢包;③是否需要过 pow (proof-of-work) —— CTF 常见的哈希碰撞验证。看 banner 内容确认。

### exp 稳定性调优
**Q1**: exp 本地 10 次 5 次成功,咋提升稳定性?
**A1**: 定位不稳定环节 —— 加 print 在每一步,记录哪一步的返回值有差异。常见不稳:①bin 内 chunk 顺序依赖(fifo/lifo 混用);②tcache count 因异常 free 偏差;③ASLR 高位偶尔触发不对齐。
**Q2**: bin 顺序不稳的根源?
**A2**: unsorted bin 的整理是"取 head 到 tail",但整理过程中会把 chunk 移到 smallbin/largebin。你 malloc 的时机不同,拿到的 chunk 不同。判据:每次操作前用 gdb 快照 bin 状态,对比差异。
**Q3**: ASLR 让某些高位不确定咋办?
**A3**: 转向 partial overwrite —— 只改低 2-3 字节,不管高位。或者让 payload 兼容多种高位可能(比如 rop chain 里放多个 candidate 地址,靠越界执行触发正确的那个)。
**Q4**: 调了半天还是概率打法怎么办?
**A4**: 换维度:接受概率,做外层批量重试。或换一条更稳但更长的利用链(比如用完整 leak + 精确 tcache poisoning 替代 partial overwrite 爆破)。稳定性和链条长度经常 tradeoff。

### 判断 exp 该在哪个环节停下并 pivot
**Q1**: exp 打到某一步反复失败,判断"这条链不行了"的信号是什么?
**A1**: 三个信号:①同一个 crash pattern 出现 3 次以上;②改了参数还是同样错误;③gdb 里看到的实际状态和预期完全对不上(不是差一点,是 fundamentally 不同)。这时候链条本身有问题。
**Q2**: pivot 到什么方向优先级最高?
**A2**: 优先级:①相同漏洞点的其他打法(UAF → 打 tcache 不行 → 打 fastbin);②相同 leak 用其他利用目标(free_hook 不行 → IO_FILE);③换漏洞点(如果有多个漏洞)。最后才是"重新审视漏洞点本身"。
**Q3**: pivot 前应该保留什么?
**A3**: leak 阶段的产物(libc/heap/stack 基址)通常可复用。保留脚本前半段,只重写后半段。判据:leak 阶段能复现 = 前半段没问题。
**Q4**: 长时间陷在同一环节,时间在流失,怎么最后决策?
**A4**: 换维度 —— 完全跳出堆漏洞视角。看题目描述、附件里的其他文件、docker-compose 里的暴露端口。有时候堆题只是一个环节,真正的突破口在 web / rev / crypto 侧。跳出题目类型的思维定式。
