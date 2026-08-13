#!/usr/bin/env bash
# seed v2 —— 编排改由 scheduler.py（确定性算法）驱动，不再用 tec_benchmark 编排 agent。
# =============================================================================
# 与 v1(seed.sh) 的差异：
#   ✗ 不再创建 tec_benchmark 编排 agent、不注入其提示词、不配其触发器、不给它绑编排工具。
#     （拉题/建任务/起停容器/暂停恢复/提交对账 全部由 bench/scheduler.py 直接走 REST 完成。）
#   ✓ 保留 challenge_review（题目复用/经验注入 hint）——含其读工具与 on_task_create 触发器。
#   ✓ 保留 planner / worker —— scheduler.py 通过 POST /api/tasks 建的任务仍走它们解题。
#   ✓ submit_flag 仍由 worker(/planner) 调用；scheduler.py 只读 correct_flag_count 调度，不自己提交。
#
# 前置：scheduler.py 需要在容器里被拉起（entrypoint 里 seed 之后启动它，见文末说明）。
# 所有密钥从环境变量读取，包内不留。
set -u
API="${API:-http://127.0.0.1:8787}"
DIR="$(cd "$(dirname "$0")" && pwd)"
PROMPTS="$DIR/prompts"
log() { echo "[seed-v2] $*"; }

# ---------- 登录拿 token ----------
tok_json="$(curl -s "$API/api/auth/init" -H 'Content-Type: application/json' -d "{\"password\":\"$ADMIN_PASSWORD\"}")"
TOKEN="$(echo "$tok_json" | jq -r '.token // empty')"
if [ -z "$TOKEN" ]; then
  TOKEN="$(curl -s "$API/api/auth/login" -H 'Content-Type: application/json' \
            -d "{\"username\":\"ARTEX\",\"password\":\"$ADMIN_PASSWORD\"}" | jq -r '.token // empty')"
fi
[ -z "$TOKEN" ] && { log "获取 token 失败，终止 seed"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"
CT="Content-Type: application/json"
post() { curl -s -o /dev/null -w "  $2 %{http_code}\n" -X "${3:-POST}" "$API$1" -H "$AUTH" -H "$CT" --data-binary @-; }

# ---------- 0) 删除全部拦截规则 ----------
# benchmark/CTF 解题会大量用到 DELETE 请求、清空类接口等操作，破坏性拦截规则会误拦
# 这些正常渗透动作。这里逐条删除所有拦截规则(含内置)，放开防护，避免 worker 被 deny 卡住。
# 无 bulk delete 接口，故先 list 再逐个 DELETE。
del_all_intercept_rules() {
  local rules ids id n=0
  rules="$(curl -s "$API/api/intercept/rules" -H "$AUTH")"
  ids="$(echo "$rules" | jq -r '(.rules // [])[] | .id')"
  for id in $ids; do
    curl -s -o /dev/null -X DELETE "$API/api/intercept/rules/$id" -H "$AUTH"
    n=$((n+1))
  done
  log "已删除 $n 条拦截规则"
}
del_all_intercept_rules

# ---------- 1) 自定义 agent（仅 challenge_review；编排交给 scheduler.py） ----------
log "创建自定义 agent（仅 challenge_review）..."
echo '{"key":"challenge_review","name":"题目REVIEW","description":"重复题目经验总结：查历史、复用经验、注入 hint"}' | post /api/agents "agent challenge_review"

# ---------- 2) 提示词覆盖（planner / worker / challenge_review；无 tec_benchmark） ----------
log "覆盖提示词 ..."
put_prompt() { # $1=agent-key  $2=file
  jq -Rs '{template:., note:"bench seed v2"}' "$2" | post "/api/agents/$1/prompt" "prompt $1" PUT
}
put_prompt planner          "$PROMPTS/planner.md"
put_prompt worker           "$PROMPTS/worker.md"
put_prompt challenge_review "$PROMPTS/challenge_review.md"

# planner / worker 开交互式 shell（pwn/逆向常需持久 shell 会话）
log "开启 planner/worker 交互式 shell ..."
for k in planner worker; do
  echo '{"interactive_shell":true}' | post "/api/agents/$k/config" "config $k interactive_shell" PUT
done

# ---------- 3) 自定义工具 ----------
log "创建自定义工具 ..."
# kali_tools：shell 类型（bash 环境声明，告知模型这些工具可直接在 bash 调用）
jq -n --rawfile desc "$DIR/kali_tools.txt" \
  '{key:"kali_tools",kind:"shell",enabled:true,description:$desc,agents:["planner","worker"]}' \
  | post /api/tools/custom "tool kali_tools"
# submit_flag：http 类型，url/token 从 env 注入。由 worker/planner 提交；不再绑 tec_benchmark。
jq -n --arg url "${BENCHMARK_BASE_URL:-}/openapi/v1/challenges/submit" --arg tok "${BENCHMARK_TOKEN:-}" '{
  key:"submit_flag", kind:"http", enabled:true,
  description:"发现flag时立即调用该工具进行提交，提交 flag 到腾讯 TSec Benchmark 答题平台。unique_code=题目唯一码；flag=要提交的 flag 值(长度 1~4096)。",
  agents:["planner","worker","mainagent"],
  schema:{type:"object",required:["unique_code","flag"],properties:{
    unique_code:{type:"string",description:"题目唯一码 unique_code"},
    flag:{type:"string",description:"要提交的 flag 值（长度 1~4096）"}}},
  exec:{method:"POST",url:$url,
    headers:{"Content-Type":"application/json","BENCHMARK_TOKEN":$tok},
    body:"{\"unique_code\": \"{unique_code}\", \"flag\": \"{flag}\"}",
    proxy:"",use_recording_proxy:false}
}' | post /api/tools/custom "tool submit_flag"

# ---------- 4) 工具绑定（只给 challenge_review 绑读/hint 工具；编排工具不再绑给任何 agent） ----------
# scheduler.py 通过 REST 直接做 spawn/pause/resume/close/delete_assets，不经 LLM 工具，故 Group B 全部省去。
log "绑定 challenge_review 工具 ..."
TOOLS="$(curl -s "$API/api/tools" -H "$AUTH")"
bind_tool() { # $1=key  其余=要追加的 agent
  local key="$1"; shift
  local row; row="$(echo "$TOOLS" | jq -c --arg k "$key" '((.tools // .))[] | select(.key==$k)')"
  if [ -z "$row" ]; then log "  [warn] 工具不存在(跳过): $key"; return; fi
  local add; add="$(printf '%s\n' "$@" | jq -R . | jq -cs .)"
  echo "$row" | jq -c --argjson add "$add" \
    '{description:.description, schema:.schema, agents:((.agents//[])+$add|unique), enabled:true}' \
    | post "/api/tools/$key" "bind $key ← $*" PUT
}
# challenge_review 需要：查历史任务/事实/worker 轨迹 + 注入 hint
for t in add_task_hint get_task_graph get_task_worker_trace list_task_findings \
         list_tasks list_task_worker_traces search_task_worker_traces; do
  bind_tool "$t" challenge_review
done

# ---------- 5) 触发器（仅 challenge_review；tec_benchmark 的定时/超时/提交触发全部移除） ----------
# 编排循环由 scheduler.py 自己的轮询驱动，不再依赖 ARTEX 触发器。
log "配置触发器（仅 challenge_review）..."
echo '{"enabled":true,"on_task_create":true,"task_create_message":"有新题目任务被创建：检查历史是否做过同一 unique_code，若有则复用其事实/经验，通过 hint 注入本任务（忽略 base url 差异）。"}' \
  | post /api/agents/challenge_review/triggers "trigger challenge_review on_task_create"

# ---------- 6) LLM：plan(强)/work(弱) 两个 profile → 测连通 → 绑 planner/worker → 激活默认 ----------
log "配置 LLM ..."
LLM_MODEL_PLAN="${LLM_MODEL_PLAN:-${LLM_MODEL:-deepseek-pro}}"
LLM_MODEL_WORK="${LLM_MODEL_WORK:-${LLM_MODEL:-deepseek-flash}}"

make_profile() { # $1=profile 名  $2=模型名  → stdout: 新建 profile 的 id
  jq -n --arg n "$1" --arg f "$LLM_FORMAT" --arg m "$2" --arg u "${LLM_BASE_URL:-}" --arg k "${LLM_API_KEY:-}" \
    '{name:$n,format:$f,model:$m,base_url:$u,api_key:$k,reasoning_effort:""}' \
  | curl -s -X POST "$API/api/llm/profiles" -H "$AUTH" -H "$CT" --data-binary @- | jq -r '.id // empty'
}
test_llm() { # $1=模型名  → stdout: 连通测试结果
  jq -n --arg f "$LLM_FORMAT" --arg m "$1" --arg u "${LLM_BASE_URL:-}" --arg k "${LLM_API_KEY:-}" \
    '{provider:$f,model:$m,base_url:$u,api_key:$k,reasoning_effort:""}' \
  | curl -s -X POST "$API/api/llm/test" -H "$AUTH" -H "$CT" --data-binary @-
}

PLAN_PID="$(make_profile plan "$LLM_MODEL_PLAN")"
WORK_PID="$(make_profile work "$LLM_MODEL_WORK")"
log "  plan profile id=$PLAN_PID model=$LLM_MODEL_PLAN"
log "  work profile id=$WORK_PID model=$LLM_MODEL_WORK"
log "  测试 plan 连通: $(test_llm "$LLM_MODEL_PLAN")"
log "  测试 work 连通: $(test_llm "$LLM_MODEL_WORK")"

# 绑定 agent 级默认模型：planner→plan、worker→work。
[ -n "$PLAN_PID" ] && echo "{\"llm_profile_id\":$PLAN_PID}" | post /api/agents/planner/config "bind planner→plan#$PLAN_PID" PUT
[ -n "$WORK_PID" ] && echo "{\"llm_profile_id\":$WORK_PID}" | post /api/agents/worker/config  "bind worker→work#$WORK_PID"  PUT

# 全局激活默认 = work(弱模型)：未绑定的 agent(challenge_review)走它。
if [ -n "$WORK_PID" ]; then
  echo "{\"id\":$WORK_PID}" | post /api/llm/profiles/active "activate default profile work#$WORK_PID"
fi

# 把 plan profile id 落盘，供 scheduler.py 用 PLAN_PROFILE_ID 给硬题任务 pin 强模型（可选）。
DATA_DIR="${ARTEX_DATA_DIR:-/app/data}"
if [ -n "$PLAN_PID" ]; then
  mkdir -p "$DATA_DIR" 2>/dev/null && echo "$PLAN_PID" > "$DATA_DIR/plan_profile_id" 2>/dev/null \
    && log "  plan profile id 已写入 $DATA_DIR/plan_profile_id（scheduler 可 export PLAN_PROFILE_ID=\$(cat $DATA_DIR/plan_profile_id)）" \
    || log "  [warn] 无法写入 $DATA_DIR/plan_profile_id（不影响 seed，仅 scheduler pin 强模型时需要）"
fi

log "seed v2 完成。编排请启动 scheduler.py（见 entrypoint 说明），本 seed 不再驱动解题循环。"
