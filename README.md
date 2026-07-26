# ARTEX

**A**gentic **R**ed-**T**eam **E**xploration and e**X**ecution — AI 自主渗透测试系统，以 [Norma](https://github.com/Autumn-27/Norma) 为内核。

独立 Go 模块（`github.com/Autumn-27/artex`，依赖 Norma），把存储 /
agent / 安全边界 / 流量 / 报告与 shadcn/ui 前端关在自己模块里，保持 Norma 零依赖。

## 运行

**探索由真实 LLM agent 驱动，必须配置 provider**（无 key 时建任务只生成根/目标节点，引擎空闲、不探索）：

```bash
export ANTHROPIC_API_KEY=sk-...        # 或 OPENAI_API_KEY
# 可选：ARTEX_LLM_PROVIDER / ARTEX_LLM_MODEL / ARTEX_LLM_BASE_URL
# 可选：ARTEX_LLM_PROXY=socks5://127.0.0.1:1080  # LLM 出站代理(http/https/socks5)；
#       也可在 UI 的 LLM 配置页填写；留空则回退 HTTP_PROXY/HTTPS_PROXY 环境变量
```

### 开发模式（热更新）

后端与前端分开跑，前端 `next dev` 有 HMR、`/api` 反代到后端：

```bash
./dev.sh   # 后端(:8787) + 流量代理(:8788) + 前端 next dev(:5173) → http://localhost:5173
# 或手动：
go run ./cmd/artex -addr :8787 -proxy :8788   # 不内嵌前端；并发 work agent 数在「系统设置」里配置(默认3)
cd web && npm run dev
```

### 单二进制（前端内嵌，一个进程一个端口）

前端静态导出后用 `//go:embed` 打进二进制，前端页面 + `/api` 同端口，运行时无需 Node：

```bash
cd web && npm run build:static            # 静态导出 → web/out
cp -r web/out ../server/webui/dist         # 拷进内嵌目录（已 gitignore）
cd .. && go build -tags embedui -o artex ./cmd/artex
./artex                                     # 打开 http://localhost:8787
```

> 不带 `-tags embedui` 的普通 `go build` / `go run` 不内嵌前端（走开发模式）。
> `server/webui/dist/` 与产出的 `artex` 均已在 `.gitignore`，不进仓库。

### 纯静态部署（可选，扔进 nginx）

`web/out/` 是一个纯静态站点，可直接放进 nginx web 根目录；此时需把 `/api/*` 反代到 Go 后端（`:8787`）。

构建：`go build ./...`、`cd web && npm run build`。测试：`go test ./...`。

## 已实现（覆盖设计文档 P0–P6）

**后端（Go）**

- `graph/` — **双 SQLite 图**：资产图 `assets.db`（跨任务共享）+ 探索图 `task-<id>.db`。
  node+edge + anchors（跨图承诺键）+ coverage（行级 CAS + 租约）；内容寻址稳定 ID +
  幂等 upsert；URL 模板化；ATTACH 跨图；**资产 GC**（P5）。
- `traffic/` — **go-mitmproxy 内嵌代理** + 可浏览文件树（host/method/模板/时间分桶/交换目录）
  + sqlite 旁路索引（分页查询）+ 内容寻址 blob 仓。全量、明文、只记目标 HTTP（§10）。
- `guard/` — **安全边界**（P1）：scope-as-code（解析 Bash 实参 host/URL/CIDR，越界拒绝）、
  破坏性/外泄门控、审计日志、**Observer 失败归因**（G5，PostToolUse 分类 blocked/error）。
  RoE 支持热改（mutex 保护，运行中收紧即时生效）。
- `agent/` — **真实 LLM agent**（接 agent-core）：
  - **规划者**（P2）：事件驱动（图变化→去抖动→唤醒），读探索路线 + 工具查资产，**自行判断目标达成**
    （`list_goals`/`prove_goal`，把证明目标的发现连到目标节点并标 met，非规则判定），生成意图入 frontier。
  - **work agent**（P3）：claim 意图 → Bash 跑 Kali 工具（走代理）+ 图写回工具，Guard 门控，
    **tradecraft 记忆**（G4）。
  - **主 agent**（P3）：人在环路对话，注入 hint→规划者 / 直注高优意图。
  - **RoE 编译器**（P6）：自然语言授权 → scope 规则（LLM，含正则兜底）。
- `report/` — **报告生成**（P5）：覆盖矩阵 + 证据链 → Markdown。
- `server/` — 事件驱动引擎（**纯 LLM，无模拟**；无 provider 则空闲）+ JSON API（任务、资产、
  探索图、frontier、覆盖、发现、流量、scope、审计、报告、chat、gc）。探索链路边由 agent 经工具织成。

**前端（shadcn/ui + React + Next.js + Tailwind）** — 实时控制台：
创建任务、统计卡 + 引擎模式徽章、发现、覆盖矩阵热力、资产图、意图队列、
**人在环路对话**、**流量记录**、**安全边界（scope/RoE 编译/审计/失败归因）**、**报告**。

## 测试

```
go test ./graph/ ./guard/ -v
```
覆盖：稳定 ID、模板化、幂等 upsert + 邻接、覆盖 CAS、frontier claim、scope 越界拦截、破坏性门控。

## 后续 / 经 MCP 接入

- **OAST 带外**（G2）、Kali 专用工具等经 MCP server 接入 work agent（`Worker.extraTools` 是接入点，
  `agent-core/mcp` 的 stdio/进程内 server 产出的 CoreTool 直接塞入）。
- 真实工具执行需 Kali 环境 + 代理 CA 信任（`data/traffic/_ca`）。
