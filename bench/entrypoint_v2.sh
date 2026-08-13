#!/usr/bin/env bash
# ARTEX benchmark 容器入口 v2：起 Postgres → 起 artex → 健康检查 → seed_v2 → 起 scheduler.py 编排。
# 与 v1(entrypoint.sh) 的差异：
#   - seed 用 seed_v2.sh（不建 tec_benchmark 编排 agent、保留 challenge_review）。
#   - seed 完成后**后台拉起 bench/scheduler.py**（确定性算法编排），替代原来的编排 agent 触发器循环。
# 全部密钥从运行时环境变量读取，不落包内。
set -u

log() { echo "[entrypoint-v2] $*"; }

# ---------- 环境变量默认值 ----------
export ADMIN_PASSWORD="${ADMIN_PASSWORD:-artexbench}"
export LLM_MODEL="${LLM_MODEL:-deepseek-v4-flash}"
export LLM_FORMAT="${LLM_FORMAT:-openai}"
# BENCHMARK_TOKEN / BENCHMARK_BASE_URL 由平台自动注入(本地测在 bench/.env 里填)。
API="http://127.0.0.1:8787"
DATA_DIR="${ARTEX_DATA_DIR:-/app/data}"

# ---------- 1) 启动 Postgres ----------
PGVER="$(ls /etc/postgresql 2>/dev/null | sort -V | tail -1)"
if [ -z "$PGVER" ]; then log "找不到 PostgreSQL 集群"; exit 1; fi
PGHBA="/etc/postgresql/$PGVER/main/pg_hba.conf"
grep -q "127.0.0.1/32 trust" "$PGHBA" || echo "host all all 127.0.0.1/32 trust" >> "$PGHBA"
log "启动 PostgreSQL $PGVER ..."
pg_ctlcluster "$PGVER" main start || service postgresql start
for i in $(seq 1 30); do su postgres -c "pg_isready -q" && break; sleep 1; done

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

# ---------- 4) seed 配置（v2） ----------
API="$API" /app/bench/seed_v2.sh || log "seed_v2 出现非致命错误（见上）"

# ---------- 5) 起 scheduler.py 确定性编排（后台） ----------
# scheduler.py 只依赖 Python 标准库；VPN 未连通时它会自行检测并退出（提示重连后重启）。
# 可选：seed_v2 已把 plan profile id 落盘，用它给硬题任务 pin 强模型。
if [ -f "$DATA_DIR/plan_profile_id" ]; then
  export PLAN_PROFILE_ID="$(cat "$DATA_DIR/plan_profile_id" 2>/dev/null)"
  log "scheduler 将用 PLAN_PROFILE_ID=$PLAN_PROFILE_ID 给硬题 pin 强模型"
fi
export ARTEX_API="${ARTEX_API:-$API}"
export EVENTS_CSV="${EVENTS_CSV:-$DATA_DIR/scheduler-events.csv}"
log "启动 scheduler.py 编排（事件写入 $EVENTS_CSV）..."
python3 /app/bench/scheduler.py &
SCHED_PID=$!

# ---------- 6) 保活 ----------
log "seed_v2 + scheduler 已启动，容器持续运行。artex_pid=$ARTEX_PID scheduler_pid=$SCHED_PID"
wait "$ARTEX_PID"
