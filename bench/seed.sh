#!/usr/bin/env bash
# 通过 ARTEX REST API 一键 seed benchmark 配置：提示词 / 自定义 agent / 自定义工具 /
# 工具绑定 / 触发器 / LLM。所有密钥从环境变量读取，包内不留。
set -u
API="${API:-http://127.0.0.1:8787}"
DIR="$(cd "$(dirname "$0")" && pwd)"
PROMPTS="$DIR/prompts"
log() { echo "[seed] $*"; }

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

# ---------- 1) 自定义 agent（先建，后续绑定/提示词依赖它） ----------
log "创建自定义 agent ..."
echo '{"key":"tec_benchmark","name":"腾讯BENCHMARK自动化编排","description":"benchmark 自动化编排：拉题/建任务/推进/提交"}' | post /api/agents "agent tec_benchmark"
echo '{"key":"challenge_review","name":"题目REVIEW","description":"重复题目经验总结：查历史、复用经验、注入 hint"}' | post /api/agents "agent challenge_review"

# ---------- 2) 提示词覆盖 ----------
log "覆盖提示词 ..."
put_prompt() { # $1=agent-key  $2=file(已做好替换)
  jq -Rs '{template:., note:"bench seed"}' "$2" | post "/api/agents/$1/prompt" "prompt $1" PUT
}
put_prompt planner "$PROMPTS/planner.md"
put_prompt worker  "$PROMPTS/worker.md"
put_prompt challenge_review "$PROMPTS/challenge_review.md"
# tec_benchmark：把提示词占位符 {{BENCHMARK_TOKEN}} / {{BENCHMARK_BASE_URL}} 用运行时
# env 的 BENCHMARK_TOKEN / BENCHMARK_BASE_URL(平台自动分发)替换；再把 {{BENCHMARK_START_TIME}}
# 替换为 seed 运行时刻(≈ 测评开始)，格式与 ARTEX 注入的 {{.Now}} 一致，便于 agent 相减算已用时长。
# 注意：{{.Now}} 带点号，不会被这里的 sed 误替，留给 ARTEX 每轮实时渲染。
TEC_TMP="$(mktemp)"
BENCHMARK_START_TIME="${BENCHMARK_START_TIME:-$(date "+%Y-%m-%d %H:%M:%S %Z")}"
sed "s|{{BENCHMARK_TOKEN}}|${BENCHMARK_TOKEN:-}|g; s|{{BENCHMARK_BASE_URL}}|${BENCHMARK_BASE_URL:-}|g; s|{{BENCHMARK_START_TIME}}|${BENCHMARK_START_TIME}|g" \
  "$PROMPTS/tec_benchmark.md" > "$TEC_TMP"
put_prompt tec_benchmark "$TEC_TMP"; rm -f "$TEC_TMP"

# planner / worker 开交互式 shell（与 dev 一致：pwn/逆向常需持久 shell 会话）
log "开启 planner/worker 交互式 shell ..."
for k in planner worker; do
  echo '{"interactive_shell":true}' | post "/api/agents/$k/config" "config $k interactive_shell" PUT
done

# ---------- 3) 自定义工具 ----------
log "创建自定义工具 ..."
# kali_tools：shell 类型（bash 环境声明，仅告知模型这些工具可直接在 bash 调用，无需 exec/schema）
jq -n --rawfile desc "$DIR/kali_tools.txt" \
  '{key:"kali_tools",kind:"shell",enabled:true,description:$desc,agents:["planner","worker"]}' \
  | post /api/tools/custom "tool kali_tools"
# submit_flag：http 类型，url/token 从 env 注入
jq -n --arg url "${BENCHMARK_BASE_URL:-}/openapi/v1/challenges/submit" --arg tok "${BENCHMARK_TOKEN:-}" '{
  key:"submit_flag", kind:"http", enabled:true,
  description:"发现flag时立即调用该工具进行提交，提交 flag 到腾讯 TSec Benchmark 答题平台。unique_code=题目唯一码；flag=要提交的 flag 值(长度 1~4096)。",
  agents:["planner","worker","mainagent","tec_benchmark"],
  schema:{type:"object",required:["unique_code","flag"],properties:{
    unique_code:{type:"string",description:"题目唯一码 unique_code"},
    flag:{type:"string",description:"要提交的 flag 值（长度 1~4096）"}}},
  exec:{method:"POST",url:$url,
    headers:{"Content-Type":"application/json","BENCHMARK_TOKEN":$tok},
    body:"{\"unique_code\": \"{unique_code}\", \"flag\": \"{flag}\"}",
    proxy:"",use_recording_proxy:false}
}' | post /api/tools/custom "tool submit_flag"

# ---------- 4) 给已注册工具追加 agent 绑定（PUT 整体覆盖 → 先取现值再合并） ----------
log "绑定编排工具 ..."
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
for t in add_task_hint get_task_graph get_task_worker_trace list_task_findings \
         list_tasks list_task_worker_traces search_task_worker_traces; do
  bind_tool "$t" tec_benchmark challenge_review
done
for t in spawn_task pause_task delete_assets_by_host; do
  bind_tool "$t" tec_benchmark
done

# ---------- 5) 触发器 ----------
log "配置触发器 ..."
echo '{"enabled":true,"on_tool_call":true,"tool_names":["submit_flag"],"tool_call_message":"刚有一次 submit_flag 提交，核对结果并推进后续题目/收尾。"}' \
  | post /api/agents/tec_benchmark/triggers "trigger tec_benchmark on_tool_call"
echo '{"enabled":true,"interval_sec":600,"interval_message":"定时巡检：拉取题目列表，挑未完成题创建/推进任务，保持跑分不停。"}' \
  | post /api/agents/tec_benchmark/triggers "trigger tec_benchmark interval600"
echo '{"enabled":true,"on_task_timeout":true,"task_timeout_message":"有题目任务超时：检查任务进展和已有事实，必要时调整策略；"}' \
  | post /api/agents/tec_benchmark/triggers "trigger tec_benchmark on_task_timeout"
echo '{"enabled":true,"on_task_create":true,"task_create_message":"有新题目任务被创建：检查历史是否做过同一 unique_code，若有则复用其事实/经验，通过 hint 注入本任务（忽略 base url 差异）。"}' \
  | post /api/agents/challenge_review/triggers "trigger challenge_review on_task_create"

# ---------- 6) LLM：建两个 profile(plan 强模型 / work 弱模型) → 测连通 → 绑定 agent → 激活默认 ----------
# 共用同一网关/密钥/格式，仅模型名不同：planner 走强模型(plan)、worker 走弱模型(work)。
# 运行时模型解析优先级：Agent 绑定 > 任务/会话 pin > 全局激活。planner/worker 各自绑定后走
# 各自 profile；未绑定的 agent(编排 tec_benchmark / challenge_review 等)走全局激活的默认(work)。
log "配置 LLM ..."
# 模型名：优先用专用变量；缺省回退旧的单一 LLM_MODEL；再回退字面默认。
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

# 绑定 agent 级默认模型：planner→plan、worker→work(llm_profile_id 三态，此处传数字=绑定)。
[ -n "$PLAN_PID" ] && echo "{\"llm_profile_id\":$PLAN_PID}" | post /api/agents/planner/config "bind planner→plan#$PLAN_PID" PUT
[ -n "$WORK_PID" ] && echo "{\"llm_profile_id\":$WORK_PID}" | post /api/agents/worker/config  "bind worker→work#$WORK_PID"  PUT

# 全局激活默认 profile = work(弱模型)：未绑定的 agent(编排 tec_benchmark / challenge_review 等)都走它。
# planner/worker 已各自绑定，不受此默认影响(Agent 绑定优先级更高)。
if [ -n "$WORK_PID" ]; then
  echo "{\"id\":$WORK_PID}" | post /api/llm/profiles/active "activate default profile work#$WORK_PID"
fi

log "seed 完成。"
