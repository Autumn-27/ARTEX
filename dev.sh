#!/usr/bin/env bash
# 开发模式：后端(:8787) + 流量代理(:8788) 与 前端 next dev(:5173) 一起跑。
# 前端 /api 反代到后端；Ctrl-C 一并退出。
#
# 单二进制（前端内嵌）方式见 README「单二进制」一节，不走这个脚本。
set -euo pipefail
cd "$(dirname "$0")"

# 退出时结束本进程组内的所有子进程（后端 + 前端）。
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 后端（普通 go run，不内嵌前端）；并发 work agent 数在「系统设置」里配置。
go run ./cmd/artex -addr :8787 -proxy :8788 &

# 前端热更新（Vite/Next dev server，/api 反代到 :8787）。
( cd web && npm run dev ) &

echo "[dev] 后端 :8787 / 代理 :8788 / 前端 http://localhost:5173  (Ctrl-C 退出)"
wait
