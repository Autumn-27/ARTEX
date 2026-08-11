#!/usr/bin/env bash
# ARTEX benchmark 容器入口：起 Postgres → 起 artex → 健康检查 → REST API 一键 seed → 保活。
# 全部密钥从运行时环境变量读取，不落包内。
set -u

log() { echo "[entrypoint] $*"; }

# ---------- 环境变量默认值 ----------
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-artexbench}"
export LLM_MODEL="${LLM_MODEL:-deepseek-v4-flash}"
export LLM_FORMAT="${LLM_FORMAT:-openai}"
# BENCHMARK_TOKEN / BENCHMARK_BASE_URL 由平台自动注入(本地测在 bench/.env 里填)，
# seed.sh 直接读取，无需在此桥接。
API="http://127.0.0.1:8787"

# ---------- 1) 启动 Postgres ----------
PGVER="$(ls /etc/postgresql 2>/dev/null | sort -V | tail -1)"
if [ -z "$PGVER" ]; then log "找不到 PostgreSQL 集群"; exit 1; fi
PGHBA="/etc/postgresql/$PGVER/main/pg_hba.conf"
# 一次性沙箱：本机 127.0.0.1 走 trust，免密码麻烦
grep -q "127.0.0.1/32 trust" "$PGHBA" || echo "host all all 127.0.0.1/32 trust" >> "$PGHBA"
log "启动 PostgreSQL $PGVER ..."
pg_ctlcluster "$PGVER" main start || service postgresql start
for i in $(seq 1 30); do su postgres -c "pg_isready -q" && break; sleep 1; done

# 建库 + 角色
su postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='artex'\"" | grep -q 1 \
  || su postgres -c "psql -c \"CREATE ROLE artex LOGIN SUPERUSER PASSWORD 'artex'\""
su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='artex'\"" | grep -q 1 \
  || su postgres -c "createdb -O artex artex"

export ARTEX_PG_DSN="postgres://artex:artex@127.0.0.1:5432/artex?sslmode=disable"

# ---------- 2) 启动 artex（后台） ----------
log "启动 artex ..."
/app/artex -addr :8787 -proxy :8788 &
ARTEX_PID=$!

# ---------- 3) 等健康 ----------
log "等待 artex /api/health ..."
for i in $(seq 1 60); do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$API/api/health" || true)
  [ "$code" = "200" ] && { log "artex 就绪"; break; }
  if ! kill -0 "$ARTEX_PID" 2>/dev/null; then log "artex 进程退出"; exit 1; fi
  sleep 1
done

# ---------- 4) seed 配置 ----------
API="$API" /app/bench/seed.sh || log "seed 出现非致命错误（见上）"

# ---------- 5) 保活 ----------
log "seed 完成，容器持续运行（编排 agent 的触发器将自动驱动解题）"
wait "$ARTEX_PID"
