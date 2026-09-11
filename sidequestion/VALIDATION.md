# `/btw` 验证记录

日期：2026-09-10。分支：`codex/btw-side-question`。基线：`8dae851b9b622f2ff2631f332fde9719d0b16fba`。

使用独立 PostgreSQL 测试库和数据目录；真实模型凭据只注入独立测试环境，没有写入代码或此记录，也没有修改产品默认模型。Go 1.26.3、norma v0.3.6、Next.js 16.2.9。

实际模型对话、返回对象、工程断言及 Qwen 原始审查文本保存在 [validation-2026-09-10.json](validation-2026-09-10.json)，其中没有 API 凭据。

## 工程检查

| 范围 | 结果 | 证据 |
| --- | --- | --- |
| 结构化消息、工具参数深拷贝 | 通过 | `TestCheckpointDeepCopyAndBoundaries` |
| 摘要 / 压缩请求不覆盖、完整回复和终态发布、半段回复排除 | 通过 | `TestCheckpointDeepCopyAndBoundaries`、`TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 实际模型池成员身份 | 通过 | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 工具配对、20 组回放、预算裁剪与超限错误 | 通过 | `TestBuildRequestCompactionToolPairingAndBudget` |
| 主旁路并行、双向取消隔离 | 通过 | 阻塞式 Provider，`TestMainSideConcurrencyAndIndependentCancellation` |
| 无工具执行、流式 / 非流式、失败时已有用量 | 通过 | `TestServiceNoToolsAndUsageOnFailure` |
| 真实 norma ChatAgent + 本地 Read 工具、主 transcript / 活动隔离 | 通过 | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`，流式和非流式子用例 |
| 持久化、分页、幂等、重启保留部分回答 | 通过 | `TestSideHistoryIdempotencyPagingAndRecovery` |
| 清空与迟到写入竞争、父资源删除、版本比较 | 通过 | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker 归档与恢复，v1/v2/v3 | 通过 | `TestSideTaskArchiveVersions` |
| 三种父接口、认证、资源归属、Worker 逻辑删除 | 通过 | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`、`TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 忙碌主会话可旁路、独立 SSE 重连 / 断开、取消、清空 | 通过 | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 每父会话 1 / 全局 4 并发 | 通过 | 两个 `TestSideHTTP…` 用例 |
| 提交前快照落库、重启续问、旧会话不可伪造快照 | 通过 | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 缓存中的配置被删除或模型改变后拒绝继续 | 通过 | `TestSideRejectsDeletedOrChangedCachedProfile` |
| 归档前取消并等待最终回答和用量落库 | 通过 | `TestSideTaskDrainPersistsBeforeArchive` |
| 流式消费者提前取消时只记一次用量及旁路归属 | 通过 | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| 重启自动恢复的 Worker / deadline 运行上下文继续发布新快照 | 通过 | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| 相关包 race 检查 | 通过 | 下列命令 |
| TypeScript 与生产构建 | 通过 | `npx tsc --noEmit`、`npm run build` |
| 新增前端模块 Biome | 通过 | `biome check`，3 个新增模块 |

在单独的可丢弃数据库中配置 `ARTEX_PG_DSN` 后，可以复现自动化检查（不要指向生产库）：

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

全量 Go 回归不是全绿：`server` 包有两个既有测试在临时目录清理阶段失败，均报 `TempDir RemoveAll … directory not empty`：

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

从上述未修改基线导出源码后，在相同隔离环境重跑 `server` 包，也复现这两个清理失败。基线运行另出现 `TestCoreTaskLifecyclePG` 的目标节点数量断言失败；最终修改后的 `server` 回归没有该断言失败。其他包通过，本次旁路相关用例及 race 检查通过。没有将基线问题标为本次验收通过，也没有为隐藏问题修改既有断言。

Next.js 构建输出已有的多 lockfile / workspace root 推断警告；构建完成且所有页面生成成功。

## 浏览器检查

使用 Codex In-app Browser，连接独立本地 Go 服务和 Next.js 开发服务器。桌面与 390 × 844 窄屏完成以下人工自动化操作，检查截图和浏览器日志：

- 普通聊天运行期间输入 `/btw`，主内容和旁路同时显示；桌面侧栏正常。
- 连续追问；旁路停止后保留已生成部分；主流程继续。
- 关闭面板时请求继续，重开后恢复完成的回答；刷新页面后空 `/btw` 恢复历史。
- 窄屏 Drawer 的输入、按钮、历史和关闭操作正常，无横向溢出。
- 清空使用确认弹窗，清空后历史消失，主 transcript 和快照保留。
- 任务 MainAgent 与两个 Worker 分别提问并切换，Agent 标签和历史未串话。
- 阻塞式本地模型夹具保持 Worker 运行；从 Worker 主输入框提交 `/btw`，停止旁路后 Worker 仍显示实时运行和自己的暂停按钮，旁路保存部分回答。
- 浏览器错误 / 警告日志为空。

可控夹具用于精确验证并发时序，不依赖真实模型的输出速度。调试期间两次 Worker 运行时检查未形成有效并发窗口（任务已结束 / 回答提前结束），修正夹具后重做并通过；不将这些初始操作记作有效通过。

## 真实模型对话

优先探测 `grok-4.6`，OpenAI 兼容接口 `http://127.0.0.1:12580/tingly/openai`。探测 HTTP 200，返回模型名 `grok-4.6` 和 `READY`，耗时 2.82 秒。首选可用，因此没有启用 Tingly `glm` 或智谱 `glm-5.3` 备用链；这两个备用服务本次没有验证。

| 场景 | 实际结果 |
| --- | --- |
| 主会话运行期间询问资产、目标、标记 | 返回 `redhaze.top`、首页读取和总结目标、`BTW-REAL-0910`；旁路完成，16.97 秒 |
| 主会话完成首页读取后询问工具依据 | 正确引用 WebFetch 200、curl 跳转 301 → 302 → 200、页面标题；7.24 秒 |
| 旁路要求 Bash 创建测试文件 | 拒绝执行，目标文件未创建；7.74 秒 |
| 完成后的旁路不改变主上下文 | 主 transcript SHA-256 与主活动记录保持一致；旁路工具执行次数为 0 |
| 真正停止 / 重启 Go 服务后续问 | 保留先前 3 条旁路历史，直接从持久化快照回答资产、标记和标题，未重跑主 Agent |
| 新会话使用 Grok 非流式配置 | 正确回答资产和 `ATOMIC-0910`；返回并保存用量：input 11734、output 138、cache_read 11520 |

资产案例的主会话使用 WebFetch 和 Bash/curl 读取公开首页，落地页为 `https://id.redhaze.top/home`，标题为“红幕科技 RedHaze Group · 全球综合集团门户”。Bash 把响应暂存于本地测试文件；未向远端执行写入。该事实与“旁路没有执行工具”分开核验。

主 transcript 校验值：`e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`。

**用量限制：** Tingly 的 Grok 流式响应没有返回 usage。另行直接发送 `stream_options.include_usage=true` 验证，HTTP 200、12 个数据帧、0 个 usage 帧。因此流式测试中的 0 表示端点没有提供用量，不能解释为没有计费。非流式用量以及夹具的失败 / 取消用量都正确保存。

## Qwen 审查

审查模型 `qwen-flash`，OpenAI 兼容接口 `https://dashscope.aliyuncs.com/compatible-mode/v1`，HTTP 200。提供了前三项真实旁路对话、主会话工具依据及工程断言；返回 `verdict: accept`、`concerns: []`，认为回答与资产、标记、页面读取证据一致，旁路工具拒绝符合约束。审查用量：prompt 6625、completion 312、total 6937。

这次 Qwen 审查范围不包含后来追加的服务重启和非流式测试。Qwen 对“无写入”的概括过宽：主会话 curl 确实创建了本地响应临时文件，上文已明确记录。并发、零工具执行和 transcript 隔离由工程断言判断，模型审查只辅助评估答案质量。
