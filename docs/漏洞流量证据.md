# 漏洞多流量证据

漏洞详情的「关联流量」支持跨页多选、用途和说明、排序及解除绑定。流量页也可多选记录，一次关联到一个已有漏洞。任务继承的漏洞证据只读，修改需进入来源任务。

系统设置中的「Agent 自动绑定流量」默认关闭，读取和修改接口为 `/api/settings` 的 `agent_traffic_binding`。开启会增加查阅请求/响应、工具调用及提示词带来的 Token 消耗；下一轮 Agent 使用新设置。关闭时隐藏自动绑定参数和补绑工具，不注入自动绑定指引，并拒绝已运行会话新提交的自动绑定。人工绑定、流量捕获、已保存证据读取和导出不受影响。

开启后，默认流程是「发现入库 → 自动触发报告 Agent → 核对并绑定流量 → 按最新证据版本写报告」。上报者在 `evidence` 保留验证命令、关键输出、已有真实流量 ID 及用途；报告 Agent 结合漏洞详情和执行记录，用 `traffic_search` / `traffic_get` 核实后调用 `bind_finding_traffic`，再读取最新 `version` 保存报告。关闭开关时，报告 Agent 不额外获得原始流量检索/读取工具，也不自动绑定；仍能读取人工绑定的快照并生成报告。

兼容已有调用者：`report_finding` 的 `traffic_refs` / `evidence_hint_id` 仍可显式即时绑定。绑定可选：TCP 等非 HTTP 漏洞、未采集或找不到确切记录时，省略引用仍可正常上报和撰写报告；保留命令输出、日志等其他可验证证据，建议说明未绑定原因，不新增必填字段。提交的 ID 必须全部有效且正文完整。任何一条失败都会回滚本次绑定操作；显式随上报绑定失败时整次上报回滚。重复追加同一快照不增加绑定，也不覆盖说明。

## Agent 编号与报告版本

`report_finding` 新增可选参数，数组顺序即初始证据顺序：

```json
{
  "traffic_refs": [
    {"traffic_id": "真实流量ID", "role": "baseline", "note": "正常账户请求"},
    {"traffic_id": "另一个真实流量ID", "role": "proof", "note": "复现请求"}
  ]
}
```

用途为 `baseline`（正常对照）、`proof`（漏洞证明）、`verification`（补充验证）、`supporting`（辅助证据，默认）。先用 `traffic_search` / `traffic_get` 核对真实记录；域名和时间仅用于候选筛选，不推定任务归属。

提示词参考 [CyberStrikeAI 的漏洞上报工具指引](https://github.com/RuoJi6/CyberStrikeAI/blob/54d56774b8bd285817d16d48b70a4a5e6e0963f7/internal/app/vulnerability_tools.go)，结合本项目的可选绑定约定，不引入无包原因必填校验。不得猜测 ID，也不应仅为补包重复探测。

返回第一行仍为 `finding recorded: <探索节点 ID>`；随后 JSON 提供独立漏洞记录 `finding_id`、探索节点 `finding_node_id` 和绑定摘要。

- `get_finding_traffic(finding_id)` 使用**独立漏洞记录 ID**，返回有序清单及 `version`；传 `binding_id`、`side=request|response`、`offset`、`length` 可分段读取，每段最多 8192 字节。
- `update_finding_report` 的 `finding_id` **继续使用探索节点 ID**。新增 `evidence_version` 填实际读取的版本；生成期间证据变化会拒绝旧版本写入，必须重新读取并生成。
- 旧报告调用未传版本时，不宣称覆盖已有流量证据。绑定、说明、用途或排序变化后，已有报告提示待更新。

开启自动绑定后，`add_hint` / `add_task_hint` 支持在单条提示或批量 `hints` 的每个元素中保存 `traffic_refs`。Planner 代为上报可传 `evidence_hint_id`，明确选取本任务对应提示中的引用，不能引用继承提示。系统不会按域名、时间或浏览记录猜测绑定。提交失败不生成部分漏洞或提前触发报告。

已有漏洞漏绑时可用 `bind_finding_traffic(finding_id, traffic_refs)` 补绑，无需重复登记。`list_findings` / `list_task_findings` / `node_detail` / `get_task_node_detail` 返回明确的 `finding_id` 和 `finding_node_id`；旧 `id` 保持探索节点语义。

启动时仅为旧工具 schema 增加可选属性，原始默认流量工具绑定扩展至报告 Agent；补绑工具默认交给报告 Agent。自定义绑定列表、提示词、描述及启用状态保留。指引在最终工具装配后统一加入：上报角色负责交接已有证据，报告 Agent 负责核对、绑定和写报告。平台对话缺少任务上下文时应通过结构化提示交接给任务 Agent，不直接上报。判定任务完成前应先交接已有证据；无包不强制等待。失败的 `report_finding` 不触发报告 Agent。

## API

基础路径：`/api/exploration/findings/{finding_id}/traffic`，使用独立漏洞 ID。沿用认证；`context_task` 校验任务可见性及继承只读。

| 方法 / 相对路径 | 请求 / 返回 |
| --- | --- |
| `GET` | 有序摘要、证据版本、报告采用版本 |
| `POST` | `{"traffic_refs":[...]}` 整批追加 |
| `PATCH /{binding_id}` | `{"version":1,"role":"proof","note":"说明"}` |
| `DELETE /{binding_id}` | `{"version":1}` |
| `PUT /order` | `{"version":1,"binding_ids":["2","1"]}`，必须是完整列表 |
| `GET /{binding_id}` | 快照元数据和有界正文预览 |
| `GET /{binding_id}/body` | `side`、`offset`、`length`；`download=1` 下载完整原字节 |

版本/排序集合冲突、归档中写入返回 `409`；继承写入 `403`；不存在或不属于漏洞的绑定 `404`；流量/附件读取及校验失败明确返回错误。

## 存储与迁移

启动幂等迁移 PostgreSQL：新增 `traffic_evidence_snapshots`、`finding_traffic_bindings`，以及 `findings.evidence_version` / `report_evidence_version`（默认 0）。不根据历史文字猜测补绑。

快照保存原流量 ID、采集时间、URL、方法、状态、请求/响应头、正文长度和 SHA-256。正文按哈希存放在 `<data>/evidence/blobs/<前两位>/<hash>.bin`，与可清理的 `data/traffic` 独立，多个漏洞可共享快照/正文。快照不提供内容更新接口，校验不一致时读取和导出失败。

在原流量写锁下读取完整正文，包括大正文 blob 和旧目录记录。先持久化并校验文件，再用单个 PostgreSQL 事务写入探索节点、意图关系、漏洞、快照和绑定；提交后才通知规划者。失败可能遗留无引用文件，但不产生部分业务记录。

PostgreSQL advisory lock `7337741004` 协调证据文件和 SQL 引用；任务行锁禁止归档排队后的证据修改。恢复从正文安装到元数据提交全程保持证据锁。删除漏洞级联移除绑定。

清理器每小时运行，只回收无引用且非进行中操作的内容，至少延迟 24 小时。普通流量清理不触及证据目录。备份热数据时同时备份 PostgreSQL 和 `data/evidence`。

## 导出与归档

Markdown 包含有序证据清单及版本；JSON 包含元数据；CSV 增加数量和绑定 ID。`md-zip` 保留漏洞 Markdown，并提供：

```text
evidence/<finding_id>/<binding_id>/
  manifest.json
  request.http
  response.http
  request.bin
  response.bin
```

Markdown 通过相对链接引用报文。发送下载前完成附件复制、哈希校验、压缩、磁盘同步及全部 ZIP 条目的 CRC 读取校验；缺失/损坏使整个下载失败。完整附件保留二进制原字节。

归档 v3 按漏洞绑定关系收集快照和正文，不依赖原流量或域名。包校验完成后才清理热数据；共享证据继续保留。恢复先校验安装正文，再事务恢复元数据和绑定，支持失败重试。v1/v2 继续可恢复，缺失的新字段显式补为 0。

## 验证与边界

为每个测试包设置独立、新建的 PostgreSQL 测试库，通过 `ARTEX_PG_DSN` 指定，避免残留任务/模型夹具触发后台运行。运行相关包完整测试，并确认没有配置缺失导致的跳过：

```sh
# 每个包运行前将 ARTEX_PG_DSN 设为对应的独立测试库；显式配置失败必须报错。
go test ./<package> -count=1
go test -race -p 1 ./evidence ./db ./agent ./server -run 'TestEvidence|TestFindingTraffic|TestFindingEvidence|TestReportFindingAtomicContract|TestTaskArchive'
```

前端验证包含 `npx tsc --noEmit`、受影响文件的 Biome 检查、Webpack 构建和 `NEXT_EXPORT=1` 静态导出。使用独立缓存目录，避免覆盖运行中的开发服务。

本机端到端验收使用独立端口、受控 HTTP / 域名 HTTPS 目标和临时数据目录，覆盖两个绑定入口、跨页选择、排序/说明、错误提示、继承只读、下载，以及删除原流量后导出、归档、回收热正文、恢复并校验哈希。

首版采用全局证据协调锁；大批量绑定/导出或慢速附件下载期间，其他证据操作可能等待。缺少录制记录或完整正文时不能补造证据。本功能不改变捕获开关，也不处理 HTTPS 直接访问 IP 的证书问题。
