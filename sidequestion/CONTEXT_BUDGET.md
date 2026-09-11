# 旁路长对话与上下文预算

2026-09-11 修复。原实现将请求 JSON 的字符数直接视为 token，并继承主任务的 32K 输出预留，因此 HTML、JS 和工具结果较多时会提前拒绝正常旁路问题。

## 开源实现核对

- [Grok CLI 旁路上下文](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739)：从最近的用户、助手文本中摘取片段，字符预算约 2000，每条至多截取 400 字符。未在此路径维护连续旁路问答历史。
- [Grok CLI 独立请求](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts)：独立取消信号；模型支持时输出上限为 2048 tokens；不提供工具。
- [Grok CLI 主会话压缩](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts)：估算 token、保留近期内容、把新内容更新进旧摘要，并处理跨回合截断。
- [OpenCode 会话压缩](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts)：完整请求估算、输出/缓冲预留、近期内容加滚动摘要、无工具摘要请求；该实现默认近期预算 8000、摘要输出上限 4096 tokens。
- [OpenCode 溢出恢复](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts)：尚未开始助手输出时才尝试溢出恢复，恢复后的调用不再进入同一溢出恢复路径。

ARTEX 借鉴独立输出预算、近期内容与滚动摘要、有限恢复的做法。保持 norma v0.3.6 的结构化消息与工具配对，不照搬 Grok 的文本摘录；不把 OpenCode 的主会话压缩事件写入 ARTEX 主 transcript。

## 请求预算与执行

- 消息沿用 norma 的按内容块 UTF-8 字节估算及 4/3 余量；额外计入系统提示、工具 schema 和消息封装开销。估算不是模型精确 token 计数。
- 旁路输出默认最多 8192 tokens，也不超过主配置已设的输出上限。可用服务环境变量 `ARTEX_BTW_MAX_OUTPUT_TOKENS` 设置 256–32768 的上限；不会修改产品默认模型或主任务参数。
- 输入预算为上下文窗口减去输出上限和安全余量；未知窗口使用平台默认 200K。安全余量为窗口的 5%，最小 128、最大 8192 tokens。
- 成功问答按递增序号每批最多加载 20 组。最多保留 20 组原文，其 token 预算最多为输入预算的 1/4，且不超过 16K。
- 超额问答更新到滚动摘要。摘要带历史来源与上下文时间；历史助手回答不等同于新的工具证据，冲突时优先依据最新主快照。
- 主上下文仍过长时，仅摘要副本中的旧消息，近期最多保留 8K tokens；截取点不会拆开工具调用与结果。过大的单组会整体进入摘要。
- 摘要输入按实际剩余窗口进行 UTF-8 安全分块，输出上限 2048 tokens；空摘要、截断、工具调用或超出摘要预算均不写缓存。单次旁路最多 12 次摘要调用，并受同一 120 秒超时限制；达到上限会明确失败，不无限循环。
- 若模型首次返回上下文超限且尚未输出文本或工具调用，进一步缩减后最多重试一次；若估算大小没有下降，立即停止恢复。其他模型错误和部分流式输出不触发该恢复。
- 所有已取得用量，包括摘要、失败尝试和取消时的用量，累加到同一旁路请求。Provider 不返回用量时仍只能记录零，不能把估算伪装成实际用量。

## 持久化与界面

`side_question_sessions.memory` 保存旧问答摘要、覆盖的 ordinal，以及按快照身份缓存的主上下文摘要。`side_question_requests.context_info` 保存准备阶段、实际回放组数、摘要使用情况和预算估算。

摘要只在原请求仍运行且清理版本匹配时保存；清空同时清除缓存，迟到写入不会恢复已清理数据。新快照不复用旧快照摘要。摘要字段随 v3 任务归档保存；恢复旧 v3 缺失字段时补空对象，v1/v2 继续兼容。

POST 先接纳并返回请求，准备与压缩在后台执行，不持有准入锁或数据库事务。SSE/历史显示准备、整理问答、压缩副本、回答阶段；压缩失败作为该旁路请求的失败终态保存。前端保留错误并恢复本次失败问题的草稿，不用悬浮通知遮挡输入框；历史轮询不再清掉提交错误。

## 验证记录

- 19/20/21/50 组回放、跨 20 组旧结论保留、摘要缓存重启复用：自动化通过。
- 超长中文回答和代码上下文、分块请求预算、工具配对、快照不变、新快照缓存失效：自动化通过。
- 摘要失败/取消/截断/超长/工具返回、清空竞争、调用上限、仅一次溢出恢复及部分流不重试：自动化通过。
- 独立 PostgreSQL 的分页、重启、v1/v2/v3 归档、摘要缓存与预算元数据归档恢复、旧 v3 缺少新字段、20 个父会话共享四个并发名额：通过。
- Go 候选服务构建、前端 TypeScript 检查、修改组件的 Biome 检查，以及独立目录中的 Next.js 生产构建：通过。
- 内置浏览器使用独立 UI 夹具，在 1280×720 和 390×844 验证整理阶段、摘要范围提示、失败后草稿恢复、无悬浮错误通知、无横向溢出和控制台无错误。临时夹具已移除。
- 只读回放本机现有 Worker 快照，新预算检查通过；例如 Worker #3 的 293085 字符快照不再被本地字符计数误判拒绝。此项没有外部模型调用。
- 本次将私有 Worker 快照发送到 Grok 的真实对话测试被自动审批审查拒绝，未执行，不作为通过项。
- 2026-09-11 00:37 按用户要求重启本机后端，沿用原数据库、数据目录和登录配置。运行文件与候选二进制 SHA-256 一致；后端及前端代理的 `/api/health` 均返回正常。

验证命令（仅使用独立测试库）：

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

前端生产构建使用独立副本，避免覆盖当前预览的 `.next`。候选服务位于 `/private/tmp/artex-btw-budget-candidate`，已复制到 `/private/tmp/artex-btw-preview/artex` 并启动；原二进制备份为同目录下的 `artex.before-context-budget`。
