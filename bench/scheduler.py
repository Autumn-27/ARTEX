#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
CWF-DR 调度器 —— TSec Benchmark 自动化跑分的确定性编排脚本
============================================================
替代 tec_benchmark 编排 agent，用确定性算法调度 3 个靶场名额，
把「编排」从解题里剥离，让难题拿到不中断的长连续窗口。

方案全称：Continuous-Window Feasibility with Dynamic Reflow
  · 双记账保证不超 6h / 1080 槽·分钟（3 槽 × 360min）
  · 阶段1：54 易题三槽滚动收割，跑到解出即回收，看门狗防死槽
  · 阶段2：9 硬题连续长跑，事件驱动余量瀑布回流
      - b 族(b01/b02/b03)：按历史耗时定制 floor、只延不切、深链延窗预留
      - 6 赌注题：等额起步(自由池÷剩余赌注题数)，按进度信号动态加码
  · 墙钟可行性闸：任何窗口 ≤ 该槽到软截止能连续消费的量（防幽灵预算）

用法：
  # 真实跑分（需 VPN 已连、ARTEX 已 seed、平台 token 就绪）
  BENCHMARK_TOKEN=xxx BENCHMARK_BASE_URL=http://platform \\
    ADMIN_PASSWORD=artexbench python3 scheduler.py

  # 本地模拟（不碰靶场/平台/ARTEX，用历史耗时演练调度逻辑）
  python3 scheduler.py --simulate           # 顺利走法
  python3 scheduler.py --simulate --scenario unlucky   # 不顺利走法

所有阈值见下方 CONFIG，可按实际网络状况调档。
"""
import os
import sys
import csv
import json
import time
import argparse
import urllib.request
import urllib.error
from datetime import datetime

# ============================ CONFIG（可调） ============================
TOTAL_BUDGET   = 1080.0   # 槽·分钟总预算 = 3 槽 × 360min（硬天花板）
SLOTS          = 3        # 平台硬上限：同时最多 3 个活跃容器
DEADLINE_MIN   = 360.0    # 6h 硬停
SOFT_DEADLINE  = 350.0    # 软截止：此后不新起长窗，留 10min 优雅收尾
POLL_SEC       = 20       # 轮询周期（真实模式）

# —— 易题阶段 ——
EASY_WALL_CAP  = 95.0     # 墙钟到此强制收口、剩余易题 park、全槽转硬
EASY_SPENT_CAP = 260.0    # 易题累计吃到此槽·分钟同样强制收口
EASY_STALL_MIN = 22.0     # 易题连续无进展 → 干净重启一次
EASY_ABS_KILL  = 30.0     # 易题绝对上限 → park 让槽
EASY_SOFT_BUDGET = 240.0  # 易题软预算，省下的回流给硬题

# —— 硬题阶段 ——
W_MIN            = 70.0   # 赌注题最小有效窗，低于此不新起（转并行/跳过）
RESTART_RESERVE  = 60.0   # 干净重启应急池
HARD_STALL_MIN   = 30.0   # 硬题连续无进展 → 触发看门狗（换思路/放弃）
B_CAP_MULT       = 2.0    # b 族单题窗封顶 = B_CAP_MULT × floor
GAMBLE_HARD_CAP  = 130.0  # 单道赌注题累计封顶（防病态占用）
PROGRESS_SIGNAL_BONUS = 1.6  # 冒出 foothold/半flag 信号的赌注题权重加成

# 9 硬题（用户指定）。b 族按历史耗时+margin 定制 floor；6 赌注题等额起步。
B_FLOOR = {"b-01": 75.0, "b-02": 110.0, "b-03": 88.0}
# hardQ 顺序 = 波1 b族 → 波2 最需长窗的赌注题 → 波3 其余（历史碎片较短的）
HARD_ORDER = ["b-01", "b-02", "b-03",      # 波1：高确定性、早解早释放余量
              "f2-05", "e3-04", "a-18",    # 波2：最需要长连续窗，早起才拿得到
              "a-03", "a-13", "a-16"]      # 波3：历史碎片较短、需求相对短
B_FAMILY  = set(B_FLOOR)
GAMBLES   = set(HARD_ORDER) - B_FAMILY
HARD_SET  = set(HARD_ORDER)

# 家族取题优先级（易题阶段选题用；同族无技术关联，仅作性价比排序）
FAMILY_PRIORITY = ["c", "d", "e1", "a", "e2", "b", "e3", "f1", "f2"]

# ARTEX / 平台接入
ARTEX_API      = os.environ.get("ARTEX_API", "http://127.0.0.1:8787")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "artexbench")
BENCH_TOKEN    = os.environ.get("BENCHMARK_TOKEN", "")
BENCH_BASE     = os.environ.get("BENCHMARK_BASE_URL", "").rstrip("/")
VPN_HEALTH_URL = os.environ.get("VPN_HEALTH_URL", "http://10.0.100.58")
PLAN_PROFILE_ID = os.environ.get("PLAN_PROFILE_ID")  # 可选：给硬题任务 pin 强模型
EVENTS_CSV     = os.environ.get("EVENTS_CSV", "scheduler-events.csv")

# ============================ 日志 ============================
class EventLog:
    """事件 CSV，列对齐历史 run-*.csv 便于回归对比。"""
    COLS = ["时间", "事件类型", "关联题目", "题目编码", "附加信息"]

    def __init__(self, path, clock):
        self.path = path
        self.clock = clock
        self.fh = open(path, "w", newline="", encoding="utf-8-sig")
        self.w = csv.writer(self.fh)
        self.w.writerow(self.COLS)
        self.fh.flush()

    def emit(self, kind, code="", info=""):
        ts = self.clock.wallclock_str()
        self.w.writerow([ts, kind, code, code, info])
        self.fh.flush()
        print(f"[{self.clock.now():6.1f}m] {kind:6s} {code:7s} {info}", file=sys.stderr)

    def close(self):
        self.fh.close()


# ============================ 时钟（真实/模拟统一抽象） ============================
class RealClock:
    def __init__(self):
        self.t0 = time.monotonic()
        self.wall0 = datetime.now()

    def now(self):                      # 距开始的分钟数
        return (time.monotonic() - self.t0) / 60.0

    def sleep(self, sec):
        time.sleep(sec)

    def wallclock_str(self):
        return datetime.now().strftime("%Y/%m/%d %H:%M:%S")


class VirtualClock:
    """模拟模式：虚拟分钟，每 tick 步进 POLL，不真正 sleep。"""
    def __init__(self, step_min):
        self._now = 0.0
        self.step = step_min

    def now(self):
        return self._now

    def sleep(self, sec):
        self._now += self.step        # 虚拟推进

    def wallclock_str(self):
        base = datetime(2026, 1, 1)
        from datetime import timedelta
        return (base + timedelta(minutes=self._now)).strftime("%Y/%m/%d %H:%M:%S")


# ============================ HTTP 助手 ============================
def _http(method, url, headers=None, body=None, timeout=30):
    data = None
    headers = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode()
        headers.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw.strip() else None)
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, {"error": raw}
    except Exception as e:
        return 0, {"error": str(e)}


# ============================ 平台客户端 ============================
class BenchEnded(Exception):
    """跑分任务已结束（token 失效 task_not_found / 总时限到 invalid_state）→ 全局停机。"""


class BenchClient:
    def __init__(self):
        self.h = {"BENCHMARK_TOKEN": BENCH_TOKEN}

    def vpn_ok(self):
        st, body = _http("GET", VPN_HEALTH_URL, timeout=10)
        return st == 200 and isinstance(body, dict) and body.get("status") == "ok"

    def challenges(self):
        st, body = _http("GET", f"{BENCH_BASE}/openapi/v1/challenges", self.h)
        if isinstance(body, list):
            return body
        # 非数组=错误响应：token 失效或总时限到 → 任务结束，抛出让主循环停机
        code = (body or {}).get("code") if isinstance(body, dict) else None
        if code in ("task_not_found", "invalid_state"):
            raise BenchEnded(code)
        return []      # 其它瞬时错误（网络抖动等）：当空处理，下一轮重试

    def start(self, code):
        # 返回 (http_status, error_code|None, container_addr_list)
        st, body = _http("POST", f"{BENCH_BASE}/openapi/v1/challenges/start?unique_code={code}", self.h)
        ecode = (body or {}).get("code") if isinstance(body, dict) else None
        addrs = (body or {}).get("container_addr") if isinstance(body, dict) else None
        return st, ecode, (addrs if isinstance(addrs, list) else [])

    def close(self, code):
        # 返回 (ok, ecode)：ok=True 表示平台确认已关闭
        st, body = _http("POST", f"{BENCH_BASE}/openapi/v1/challenges/close?unique_code={code}", self.h)
        ecode = (body or {}).get("code") if isinstance(body, dict) else None
        # challenge_not_found 说明本就没这个活跃容器，视作已关闭（幂等）
        ok = (st and 200 <= st < 300 and isinstance(body, dict) and body.get("closed")) or ecode == "challenge_not_found"
        return bool(ok), ecode

    def hint(self, code):
        return _http("GET", f"{BENCH_BASE}/openapi/v1/challenges/hint?unique_code={code}", self.h)


# ============================ ARTEX 客户端 ============================
class ArtexClient:
    def __init__(self):
        self.token = None

    def login(self):
        st, body = _http("POST", f"{ARTEX_API}/api/auth/init", body={"password": ADMIN_PASSWORD})
        tok = (body or {}).get("token") if isinstance(body, dict) else None
        if not tok:
            st, body = _http("POST", f"{ARTEX_API}/api/auth/login",
                             body={"username": "ARTEX", "password": ADMIN_PASSWORD})
            tok = (body or {}).get("token") if isinstance(body, dict) else None
        self.token = tok
        return bool(tok)

    def _auth(self):
        return {"Authorization": f"Bearer {self.token}"}

    def _call(self, method, path, body=None):
        """统一出口：401(token 过期) 时自动重登一次再重试，扛住 6h 长跑。"""
        st, resp = _http(method, f"{ARTEX_API}{path}", self._auth(), body)
        if st in (401, 403):
            if self.login():
                st, resp = _http(method, f"{ARTEX_API}{path}", self._auth(), body)
        return st, resp

    def create_task(self, description, goal):
        payload = {"description": description, "goal": goal, "seed_first_intent": True}
        if PLAN_PROFILE_ID:
            payload["llm_profile_id"] = int(PLAN_PROFILE_ID)
        st, body = self._call("POST", "/api/tasks", payload)
        # 只有 2xx + 拿到 id 才算成功；否则返回 None 让调用方回滚容器
        if st and 200 <= st < 300 and isinstance(body, dict) and body.get("id"):
            return body["id"]
        return None

    def control(self, task_id, action):        # action = pause | resume
        return self._call("POST", f"/api/tasks/{task_id}/control", {"action": action})

    def delete_task(self, task_id):
        return self._call("DELETE", f"/api/tasks/{task_id}")

    def last_activity(self):
        """返回 {task_id: last_activity_epoch}，用于进度看门狗。"""
        st, body = self._call("GET", "/api/tasks")
        out = {}
        if isinstance(body, dict):
            for t in body.get("tasks", []):
                la = t.get("last_activity") or t.get("lastActivity") or 0
                out[t.get("id")] = _to_epoch(la)
        return out

    def finding_count(self, task_id):
        st, body = self._call("GET", f"/api/exploration/findings?task={task_id}")
        return len(body) if isinstance(body, list) else 0


def _to_epoch(v):
    if isinstance(v, (int, float)):
        return float(v)
    if isinstance(v, str) and v:
        for fmt in ("%Y-%m-%dT%H:%M:%SZ", "%Y-%m-%dT%H:%M:%S%z"):
            try:
                return datetime.strptime(v, fmt).timestamp()
            except Exception:
                pass
    return 0.0


# ============================ 槽位状态 ============================
class Slot:
    def __init__(self, idx):
        self.idx = idx
        self.reset()

    def reset(self):
        self.code = None
        self.task_id = None
        self.addr = None
        self.kind = None            # easy | b | gamble
        self.started = None         # 起跑墙钟(min)
        self.window = 0.0           # 已分配窗口(min)；easy 不用
        self.banked_flags = 0
        self.last_progress = None   # 上次进展墙钟(min)
        self.last_finding = 0
        self.last_activity = 0.0
        self.restarted = False

    @property
    def busy(self):
        return self.code is not None

    def elapsed(self, now):
        return (now - self.started) if self.started is not None else 0.0


# ============================ 调度器核心 ============================
class Scheduler:
    def __init__(self, bench, artex, clock, simulate=False, sim=None):
        self.b = bench
        self.a = artex
        self.clock = clock
        self.simulate = simulate
        self.sim = sim
        self.log = EventLog(EVENTS_CSV, clock)
        self.slots = [Slot(i) for i in range(SLOTS)]
        self.easyQ = []       # 待跑易题 code
        self.deferQ = []      # 被 park 的易题
        self.blacklist = set()
        self.given_up = set()
        self.solved = set()
        self.hard_started = set()
        self.spent_closed = 0.0    # 已关闭工作累计的真实槽·分钟
        self.easy_spent = 0.0
        self.easy_done = False
        self.flags = {}            # code -> correct_flag_count（用于检测新 flag / b 族里程碑）
        self.flag_total = {}       # code -> flag_count
        self.diff = {}             # code -> difficulty（easy/medium/hard，用于同族内排序）
        self._cmap = {}            # 本轮 challenges 快照
        self.pending_close = {}    # code -> 已重试次数：close 失败/未确认停止的容器，逐轮重试回收
        self.frozen = False        # VPN 断开冻结态
        self.freeze_start = None
        self._poll_seq = 0         # 轮次计数（用于 _activity 每轮缓存）

    # ---------- 预算记账 ----------
    def live_spent(self, now):
        return self.spent_closed + sum(s.elapsed(now) for s in self.slots if s.busy)

    def wall_cap(self, slot, now):
        """该槽从现在到软截止能连续消费的最大窗口（含已跑部分）。防幽灵预算。"""
        return slot.elapsed(now) + max(0.0, SOFT_DEADLINE - now)

    def gamble_target(self, now):
        """赌注题等额目标窗 = 前向自由池 ÷ 剩余赌注题数，动态。"""
        wall_remaining = max(0.0, SOFT_DEADLINE - now)
        forward_cap = wall_remaining * SLOTS
        # 扣除仍未通关 b 族的前向需求
        b_need = 0.0
        for code in B_FAMILY:
            if code in self.solved:
                continue
            done = self._elapsed_of(code, now)
            b_need += max(0.0, B_FLOOR[code] - done)
        gamble_free = max(0.0, forward_cap - b_need - RESTART_RESERVE)
        remaining = [c for c in GAMBLES if c not in self.solved and c not in self.given_up]
        if not remaining:
            return 0.0
        return gamble_free / len(remaining)

    def _elapsed_of(self, code, now):
        for s in self.slots:
            if s.code == code:
                return s.elapsed(now)
        return 0.0

    # ---------- 主循环 ----------
    def run(self):
        self.log.emit("任务开始")
        if not self.simulate and not self.b.vpn_ok():
            self.log.emit("VPN检测", info="VPN 未连通，终止（连上后重启脚本）")
            print("VPN 未连通，终止。", file=sys.stderr)
            return
        self.log.emit("VPN检测", info="通过")

        chs = self._challenges()
        self._classify(chs=chs)
        self.log.emit("分类", info=f"易题 {len(self.easyQ)} 道 / 硬题 {len(HARD_ORDER)} 道")

        while self.clock.now() < DEADLINE_MIN:
            self._poll_seq += 1
            now = self.clock.now()

            # VPN 守卫：断开则冻结各槽预算 + 暂停任务，本轮不做解题处置
            if self._vpn_guard(now):
                self.clock.sleep(POLL_SEC)
                continue

            try:
                chs = self._challenges()
            except BenchEnded as e:
                self.log.emit("任务结束", info=f"平台返回 {e}，总时限到/ token 失效，停机")
                break
            cmap = {c["unique_code"]: c for c in chs}
            self._cmap = cmap                       # 供 _clean_restart 复用，避免 mid-slot 再请求

            self._process_pending_close(cmap)       # 回收 close 失败/未确认的泄漏容器
            self._sync_solved(cmap)                 # 检测得分/通关
            for s in self.slots:                    # 逐槽处置
                if s.busy:
                    self._handle_slot(s, cmap, now)
            self._maybe_close_easy_phase(now)       # 易题聚合收口闸
            try:
                self._fill_free_slots(cmap, now)    # 补名额（一秒不空）
            except BenchEnded as e:
                self.log.emit("任务结束", info=f"start 返回 {e}，停机")
                break
            self._reflow(now)                       # 余量回流：刷新赌注窗口

            if self._all_done():
                break
            self.clock.sleep(POLL_SEC)

        self._shutdown()

    # ---------- 题目获取 / 分类 ----------
    def _challenges(self):
        return self.sim.challenges() if self.simulate else self.b.challenges()

    def _classify(self, chs):
        for c in chs:
            code = c["unique_code"]
            self.flag_total[code] = c.get("flag_count", 1)
            self.flags[code] = c.get("correct_flag_count", 0)
            self.diff[code] = (c.get("difficulty") or "").lower()
            if c.get("is_completed"):
                self.solved.add(code)
                continue
            if code not in HARD_SET:
                self.easyQ.append(code)
        # 易题排序：先按家族性价比，同族内按 difficulty(easy→hard) 把好拿的顶前面
        self.easyQ.sort(key=lambda code: (self._family_rank(code), self._diff_rank(code)))

    def _diff_rank(self, code):
        return {"easy": 0, "medium": 1, "hard": 2}.get(self.diff.get(code, ""), 1)

    def _family_rank(self, code):
        fam = code.split("-")[0]
        try:
            return FAMILY_PRIORITY.index(fam)
        except ValueError:
            return len(FAMILY_PRIORITY)

    # ---------- 得分同步 ----------
    def _sync_solved(self, cmap):
        for code, c in cmap.items():
            cf = c.get("correct_flag_count", 0)
            prev = self.flags.get(code, 0)
            if cf > prev:
                self.log.emit("得分成功", code, f"flag {cf}/{c.get('flag_count',1)}")
                self.flags[code] = cf
                # b 族拿到新 flag = 里程碑，标记该槽已 bank
                for s in self.slots:
                    if s.code == code:
                        s.banked_flags = cf
                        s.last_progress = self.clock.now()
            if c.get("is_completed") and code not in self.solved:
                self.solved.add(code)

    # ---------- 逐槽处置 ----------
    def _handle_slot(self, s, cmap, now):
        c = cmap.get(s.code, {})
        # 1) 通关 → 关靶场、结算、放名额
        if c.get("is_completed") or s.code in self.solved:
            self._close_slot(s, now, reason="solved")
            return
        # 2) 进度检测（看门狗用）
        progressed = self._check_progress(s, c, now)
        if progressed:
            s.last_progress = now

        if s.kind == "easy":
            self._handle_easy(s, now)
        else:
            self._handle_hard(s, c, now)

    def _check_progress(self, s, c, now):
        cf = c.get("correct_flag_count", 0)
        if cf > s.banked_flags:
            return True
        if self.simulate:
            return self.sim.progressing(s.code, now)
        # 真实模式：last_activity 前进 或 finding 数增加 视为进展
        la = self._activity.get(s.task_id, 0.0)
        if la > s.last_activity:
            s.last_activity = la
            return True
        fc = self.a.finding_count(s.task_id)
        if fc > s.last_finding:
            s.last_finding = fc
            return True
        return False

    def _handle_easy(self, s, now):
        el = s.elapsed(now)
        stall = (now - (s.last_progress or s.started))
        if el > EASY_ABS_KILL:                          # 绝对上限 → park
            self.log.emit("放弃", s.code, f"易题超 {EASY_ABS_KILL:.0f}min，park")
            self.deferQ.append(s.code)
            self._close_slot(s, now, reason="park")
        elif stall > EASY_STALL_MIN and not s.restarted:  # 停滞 → 干净重启一次
            self.log.emit("干净重启", s.code, "易题停滞，清上下文重开")
            self._clean_restart(s, now)

    def _handle_hard(self, s, c, now):
        el = s.elapsed(now)
        stall = (now - (s.last_progress or s.started))
        has_unclaimed = c.get("correct_flag_count", 0) < c.get("flag_count", 1)
        # 进度看门狗（独立于窗口，防假死烧槽数小时）
        if stall > HARD_STALL_MIN:
            if s.kind == "b" and s.banked_flags > 0:
                # b 族有 banked flag：势头在就续、势头没了就让位（防落脚点变僵尸占槽）。
                # floor 内不判死（正常全解本就要这么久）；超 floor 仍停滞→释放槽，保留已拿的部分分。
                if s.elapsed(now) < B_FLOOR[s.code]:
                    s.window = min(s.elapsed(now) + HARD_STALL_MIN, self.wall_cap(s, now))
                    return
                self.log.emit("让位", s.code,
                              f"b族超floor仍停滞，保留{s.banked_flags}/{self.flag_total.get(s.code,'?')}flag释放槽")
                self.given_up.add(s.code)      # 退役，不再重取；已 bank 的部分分保留
                self._close_slot(s, now, reason="b_stall_release")
                return
            if not s.restarted and self.wall_cap(s, now) >= W_MIN:
                self.log.emit("干净重启", s.code, f"硬题停滞 {stall:.0f}min，换思路")
                self._clean_restart(s, now)
                return
            self.log.emit("放弃", s.code, "硬题假死且不可重启")
            self.given_up.add(s.code)
            self._close_slot(s, now, reason="stall_giveup")
            return
        # 窗口到期
        if el >= s.window:
            self._handle_exhaust(s, c, now, has_unclaimed)

    def _handle_exhaust(self, s, c, now, has_unclaimed):
        if s.kind == "b" and has_unclaimed:
            # b 族深链：只延不切，优先原地续窗保 foothold
            cap = min(B_CAP_MULT * B_FLOOR[s.code], self.wall_cap(s, now))
            if s.window < cap:
                s.window = min(cap, s.window + 15.0)
                self.log.emit("延窗", s.code, f"b族续窗至 {s.window:.0f}min 保深链")
                return
            self.log.emit("放弃", s.code, "b族达封顶，护带内收尾")
            self.given_up.add(s.code)          # 退役，避免 _all_done 永假 / 被重取
            self._close_slot(s, now, reason="b_cap")
            return
        # 赌注题：near-miss 且可行 → 干净重启一次；否则放弃归池
        near_miss = s.banked_flags > 0 or (s.last_progress and now - s.last_progress < 10)
        if not s.restarted and near_miss and self.wall_cap(s, now) >= W_MIN and s.elapsed(now) < GAMBLE_HARD_CAP:
            self.log.emit("干净重启", s.code, "赌注题窗口到期，换思路一次")
            self._clean_restart(s, now)
            s.window = s.elapsed(now) + min(0.5 * s.window, self.wall_cap(s, now) - s.elapsed(now))
        else:
            self.log.emit("放弃", s.code, f"赌注题窗口到期({s.elapsed(now):.0f}min)，归池")
            self.given_up.add(s.code)
            self._close_slot(s, now, reason="gamble_exhaust")

    # ---------- 易题聚合收口 ----------
    def _maybe_close_easy_phase(self, now):
        if self.easy_done:
            return
        if now >= EASY_WALL_CAP or self.easy_spent >= EASY_SPENT_CAP:
            if self.easyQ:
                self.log.emit("收口", info=f"易题收口闸触发，park {len(self.easyQ)} 道剩余易题")
                self.deferQ.extend(self.easyQ)
                self.easyQ = []
            self.easy_done = True

    # ---------- 补名额 ----------
    def _fill_free_slots(self, cmap, now):
        for s in self.slots:
            if s.busy:
                continue
            code = self._pick_next(now)
            if code is None:
                continue
            self._launch(s, code, cmap, now)

    def _pick_next(self, now):
        # 阶段一：优先耗尽易题（除非已收口）
        if self.easyQ:
            return self.easyQ.pop(0)
        # 阶段二：按 HARD_ORDER 取仍可做且墙钟可行的硬题
        for code in HARD_ORDER:
            if code in self.solved or code in self.given_up or code in self.hard_started:
                continue
            # 墙钟可行性闸：起跑前确认到软截止还够 W_MIN（b 族用其 floor）
            need = W_MIN if code in GAMBLES else min(B_FLOOR[code], W_MIN)
            if (SOFT_DEADLINE - now) >= need:
                return code
        # 兜底：重试 deferQ 里的易题（尾段有余量时）
        if self.deferQ and (SOFT_DEADLINE - now) > 8:
            return self.deferQ.pop(0)
        return None

    def _launch(self, s, code, cmap, now):
        c = cmap.get(code, {})
        # safe_switch 前置：起靶场。container_addr 是【数组】（一题可能多地址）
        if self.simulate:
            addrs = self.sim.start(code, now)
        else:
            st, ecode, addrs = self.b.start(code)
            if st == 409 and ecode == "invalid_state":
                return                          # 达 3 活跃上限(或任务将结束)：下一轮重试，challenges() 会捕获真结束
            if st == 503 or ecode == "resource_unavailable":
                return                          # 靶场资源未就绪：稍后重试
            if st == 404 and ecode == "challenge_not_found":
                self.log.emit("放弃", code, "challenge_not_found，跳过")
                self.given_up.add(code)
                return
            if st == 404 and ecode == "task_not_found":
                raise BenchEnded("task_not_found")
        if not addrs:                           # pending/无地址：下一轮重试
            return
        self.log.emit("启动靶场", code, f"container_addr: {', '.join(addrs)}")
        # 建 ARTEX 任务（把完整原始 description + 全部靶机地址塞进去）
        if self.simulate:
            task_id = self.sim.spawn(code)
        else:
            desc, goal = self._task_text(c, addrs)
            task_id = self.a.create_task(desc, goal)
        # 建任务失败 → 回滚：容器已起但没任务解它，立刻关容器 + 退回队列，绝不落座僵尸槽
        if not task_id:
            self.log.emit("建任务失败", code, "回滚：关容器、退回队列，不占槽")
            self._close_container(code)
            self._requeue(code)
            return
        # 落座（到此才算真正占用，hard_started 也在此登记）
        s.reset()
        s.code, s.task_id, s.addr = code, task_id, addrs
        s.started = now
        s.last_progress = now
        s.banked_flags = self.flags.get(code, 0)
        if code in B_FAMILY:
            s.kind = "b"
            s.window = B_FLOOR[code]
        elif code in GAMBLES:
            s.kind = "gamble"
            s.window = max(W_MIN, min(self.gamble_target(now), self.wall_cap(s, now)))
        else:
            s.kind = "easy"
            s.window = 0.0
        if code in HARD_SET:
            self.hard_started.add(code)
            self.log.emit("分配窗口", code, f"{s.kind} 初始窗 {s.window:.0f}min")

    def _task_text(self, c, addrs):
        code = c.get("unique_code", "")
        addr_s = ", ".join(addrs) if isinstance(addrs, list) else str(addrs)
        desc = (f"unique_code:{code}\n"
                f"题目名称:{code}\n"
                f"题目完整描述原文:{c.get('description','')}\n"
                f"难度:{c.get('difficulty','')} | score={c.get('total_score','')} "
                f"| flag_count={c.get('flag_count',1)}\n"
                f"靶机地址:{addr_s}\n"
                f"请对该目标进行渗透测试，逐个获取 flag 并通过 submit_flag 提交（unique_code={code}）。")
        goal = (f"拿到 {code} 的全部 flag（flag_count={c.get('flag_count',1)}），"
                f"逐个通过 submit_flag 提交（unique_code={code}）。目标地址 {addr_s}。")
        return desc, goal

    # ---------- 余量回流：刷新在跑赌注题窗口（只延不切） ----------
    def _reflow(self, now):
        target = self.gamble_target(now)
        for s in self.slots:
            if s.busy and s.kind == "gamble":
                cap = self.wall_cap(s, now)
                want = target * (PROGRESS_SIGNAL_BONUS if s.banked_flags > 0 else 1.0)
                want = min(want, GAMBLE_HARD_CAP, cap)
                if want > s.window:                 # append-only，绝不砍在跑题
                    s.window = want

    # ---------- 关槽 / 干净重启 / safe_switch ----------
    def _close_slot(self, s, now, reason):
        self.spent_closed += s.elapsed(now)
        if s.kind == "easy" or s.code not in HARD_SET:
            self.easy_spent += s.elapsed(now)
        code = s.code
        if not self.simulate and s.task_id:
            self.a.control(s.task_id, "pause")
        self.log.emit("关闭靶场", code, f"reason={reason} 用时{s.elapsed(now):.0f}min")
        s.reset()                          # 先释放本地槽
        self._close_container(code)        # 再关容器（失败进 pending_close 逐轮重试回收）

    def _close_container(self, code):
        """关平台容器并确认；失败则挂进 pending_close，主循环逐轮重试，防泄漏名额。"""
        ok = self.sim.close(code) if self.simulate else self.b.close(code)[0]
        if ok:
            if code in self.pending_close:
                self.pending_close.pop(code, None)
                self.log.emit("回收容器", code, "泄漏容器已确认关闭")
        else:
            self.pending_close[code] = self.pending_close.get(code, 0) + 1
            self.log.emit("关容器失败", code, f"第{self.pending_close[code]}次，挂起重试防泄漏名额")

    def _process_pending_close(self, cmap):
        """逐轮回收泄漏容器：已 stopped 的清除，仍活跃的重试 close。"""
        for code in list(self.pending_close):
            status = (cmap.get(code) or {}).get("container_status", "stopped")
            if status in ("stopped", "stop_pending"):
                self.pending_close.pop(code, None)
            else:
                self._close_container(code)

    def _requeue(self, code):
        """建任务失败/需重排时把题退回合适队列（硬题不标 hard_started，可被重取）。"""
        if code in HARD_SET:
            self.hard_started.discard(code)        # 允许 _pick_next 再取
        elif code not in self.easyQ and code not in self.deferQ:
            self.easyQ.insert(0, code)

    def _vpn_guard(self, now):
        """VPN 断开检测。返回 True=当前冻结、本轮跳过解题。恢复时把停摆时长从各槽预算补偿掉。"""
        ok = self.sim.vpn_ok() if self.simulate else self.b.vpn_ok()
        if ok:
            if self.frozen:
                outage = now - self.freeze_start
                for s in self.slots:
                    if s.busy:
                        if s.started is not None:
                            s.started += outage           # 停摆不计入该题已跑时长
                        if s.last_progress is not None:
                            s.last_progress += outage     # 也不让停摆触发停滞看门狗
                        if not self.simulate and s.task_id:
                            self.a.control(s.task_id, "resume")
                self.log.emit("VPN恢复", info=f"停摆 {outage:.0f}min：各槽预算已补偿、任务恢复（墙钟仍走）")
                self.frozen = False
            return False
        # VPN 断
        if not self.frozen:
            self.frozen = True
            self.freeze_start = now
            for s in self.slots:
                if s.busy and not self.simulate and s.task_id:
                    self.a.control(s.task_id, "pause")
            self.log.emit("VPN断开", info="冻结各槽预算 + 暂停任务，等待恢复")
        return True

    def _clean_restart(self, s, now):
        """清空上下文换思路：pause+删旧任务 + 重建，靶场不动（保连接）。"""
        s.restarted = True
        if not self.simulate:
            old = s.task_id
            if old:
                self.a.control(old, "pause")
                self.a.delete_task(old)                   # 清掉旧任务上下文，彻底换思路
            # 用本轮已拉到的 challenge 文本重建（不再额外请求，避免 mid-slot 抛 BenchEnded）
            c = self._cmap.get(s.code, {"unique_code": s.code})
            desc, goal = self._task_text(c, s.addr)
            new_id = self.a.create_task(desc, goal)
            if new_id:
                s.task_id = new_id
            else:
                # 重建失败：别留着已删的死 task_id 空跑，直接放弃该题让位
                self.log.emit("放弃", s.code, "干净重启重建任务失败，让位")
                self.given_up.add(s.code)
                self._close_slot(s, now, reason="restart_failed")
                return
        else:
            s.task_id = self.sim.spawn(s.code)
            self.sim.on_restart(s.code, now)
        # 重置进度追踪：新任务 finding/活动从零起，别让旧计数误触发停滞看门狗
        s.last_progress = now
        s.last_finding = 0
        s.last_activity = 0.0

    def _all_done(self):
        if self.easyQ or self.deferQ:
            return False
        pending_hard = [c for c in HARD_ORDER if c not in self.solved and c not in self.given_up]
        if pending_hard:
            return False
        return not any(s.busy for s in self.slots)

    def _shutdown(self):
        now = self.clock.now()
        for s in self.slots:
            if s.busy:
                self._close_slot(s, now, reason="deadline")
        solved_hard = [c for c in HARD_ORDER if c in self.solved]
        self.log.emit("收尾", info=(f"总用时 {now:.0f}min / 消耗 {self.live_spent(now):.0f}槽·分钟 | "
                                     f"解出 {len(self.solved)} 题，其中硬题 {len(solved_hard)}/9: "
                                     f"{','.join(solved_hard)}"))
        self.log.close()

    # 真实模式每轮（按轮次计数缓存）只拉一次 last_activity 映射
    @property
    def _activity(self):
        if self.simulate:
            return {}
        if getattr(self, "_act_seq", -1) != self._poll_seq:
            self._act_cache = self.a.last_activity()
            self._act_seq = self._poll_seq
        return self._act_cache


# ============================ 本地模拟器（不碰靶场/平台/ARTEX） ============================
class Simulator:
    """用历史耗时脚本化题目解出时刻，让调度逻辑在本地端到端跑通。"""
    # (code, flag_count, 解出所需连续窗口min | None=永不解出)
    def __init__(self, scenario="lucky", clock=None, faults=False):
        self.state = {}       # code -> dict(status, addr, cf, opened_at, need, run_started, restarts)
        self.scenario = scenario
        self.clock = clock    # 共享调度器时钟，challenges() 每次读实时虚拟分钟
        self.faults = faults  # 故障注入：建任务失败/关容器失败/VPN 断
        self._task_seq = 1000
        self._spawn_fail_used = set()   # 每题只失败一次，验证回滚后能重来
        self._close_fail_used = set()
        self._build()

    # —— 故障注入（仅 --faults）——
    def vpn_ok(self):
        if self.faults and 100 <= self.clock.now() < 118:   # 模拟第 100~118min VPN 中断
            return False
        return True

    def _inject_spawn_fail(self, code):
        if self.faults and code in ("d-01", "a-18") and code not in self._spawn_fail_used:
            self._spawn_fail_used.add(code)
            return True
        return False

    def _inject_close_fail(self, code):
        if self.faults and code in ("c-03", "f2-05") and code not in self._close_fail_used:
            self._close_fail_used.add(code)
            return True
        return False

    def _build(self):
        # 54 易题：多数 <3min，几道慢题
        easy = {f"c-0{i}": (2, 2) for i in range(1, 10)}
        easy.update({f"d-0{i}": (3, 1) for i in range(1, 7)})
        easy.update({f"e1-0{i}": (3, 1) for i in range(1, 7)})
        easy.update({f"e2-0{i}": (4, 1) for i in range(1, 5)})
        easy.update({f"e3-0{i}": (5, 1) for i in range(1, 4)})   # e3-04 是硬题
        easy.update({f"f1-0{i}": (6, 1) for i in range(1, 6)})
        easy.update({f"f2-0{i}": (8, 1) for i in range(1, 9)})   # f2-05 是硬题
        easy.update({f"a-{i:02d}": (3, 1) for i in range(1, 19)})  # a-* 里 4 道是硬题
        easy["f2-01"] = (51, 1); easy["a-14"] = (17, 1); easy["c-04"] = (16, 1)
        for c in ["a-03", "a-13", "a-16", "a-18", "e3-04", "f2-05"]:
            easy.pop(c, None)
        # b 族历史耗时；顺利下解出，不顺利下 b-02 卡深链
        hard_need_lucky = {"b-01": 64, "b-02": 93, "b-03": 73,
                           "f2-05": 78, "e3-04": 66, "a-18": 85,
                           "a-03": 55, "a-13": 68, "a-16": 80}
        hard_need_unlucky = {"b-01": 70, "b-02": None, "b-03": 85,   # b-02 卡(拿部分flag)
                             "f2-05": None, "e3-04": None, "a-18": None,
                             "a-03": 52, "a-13": None, "a-16": None}
        need = hard_need_lucky if self.scenario == "lucky" else hard_need_unlucky
        for code, (t, fc) in easy.items():
            self.state[code] = self._mk(code, need=t, fc=fc)
        for code in HARD_ORDER:
            fc = 4 if code in ("b-01", "b-03") else (6 if code == "b-02" else 1)
            self.state[code] = self._mk(code, need=need.get(code), fc=fc)
        # 不顺利：易题网络抖动，3 道慢/卡
        if self.scenario == "unlucky":
            for code in ["c-05", "d-03", "a-07"]:
                self.state[code]["need"] = 40    # 会被易题看门狗 park

    def _mk(self, code, need, fc):
        return {"status": "stopped", "addr": None, "cf": 0, "fc": fc,
                "need": need, "run_started": None, "restarts": 0}

    def challenges(self):
        now = self.clock.now()
        out = []
        for code, s in self.state.items():
            # 推进「解出」判定（部分 flag 独立于 need，让 b 族即使不全解也能 bank 落脚点）
            if s["status"] == "available" and s["run_started"] is not None:
                run = now - s["run_started"]
                if s["need"] is not None and run >= s["need"]:
                    s["cf"] = s["fc"]                             # 全解
                elif code == "b-02" and self.scenario == "unlucky":
                    s["cf"] = min(3, int(run / 25))               # b-02 卡在 3/6：foothold 热、深链断
                elif s["fc"] > 1 and s["need"] is not None and run > 0:
                    s["cf"] = min(s["fc"] - 1, int(run / 20))     # 多 flag 题渐进 bank
            out.append({"unique_code": code, "difficulty": "hard",
                        "flag_count": s["fc"], "correct_flag_count": s["cf"],
                        "is_completed": s["cf"] >= s["fc"] and s["fc"] > 0,
                        "container_status": s["status"],
                        "container_addr": [s["addr"]] if s["addr"] else [],
                        "total_score": 500, "description": f"模拟题 {code}"})
        return out

    def start(self, code, now):
        s = self.state[code]
        s["status"] = "available"
        s["addr"] = f"10.0.0.{hash(code) % 250 + 1}:80"
        s["run_started"] = now
        return [s["addr"]]        # 与真实平台一致：container_addr 是数组

    def spawn(self, code):
        if self._inject_spawn_fail(code):     # 建任务失败注入 → 验证 _launch 回滚
            return None
        self._task_seq += 1
        return str(self._task_seq)

    def on_restart(self, code, now):
        s = self.state[code]
        s["restarts"] += 1
        s["run_started"] = now         # 干净重开：连续计时归零
        # 顺利场景：重启有时能撬开原本 need 略高的题
        if self.scenario == "lucky" and s["need"] and s["need"] > 60:
            s["need"] = max(30, s["need"] - 20)

    def close(self, code):
        if self._inject_close_fail(code):     # 关容器失败注入 → 验证 pending_close 重试回收
            return False                       # 容器仍活跃（status 不变），下一轮重试才真关
        s = self.state[code]
        s["status"] = "stopped"; s["addr"] = None; s["run_started"] = None
        return True

    def progressing(self, code, now):
        s = self.state[code]
        if s["run_started"] is None or s["need"] is None:
            return False
        # need 为 None（永不解出）的题在停滞窗后不再进展 → 触发看门狗
        return (now - s["run_started"]) < (s["need"] + 5)


# ============================ main ============================
def main():
    ap = argparse.ArgumentParser(description="CWF-DR benchmark 调度器")
    ap.add_argument("--simulate", action="store_true", help="本地模拟，不碰靶场/平台/ARTEX")
    ap.add_argument("--scenario", choices=["lucky", "unlucky"], default="lucky",
                    help="模拟场景：lucky=顺利走法 / unlucky=不顺利走法")
    ap.add_argument("--poll-min", type=float, default=1.0, help="模拟虚拟步进(分钟)")
    ap.add_argument("--faults", action="store_true",
                    help="模拟故障注入：建任务失败/关容器失败/VPN 中断，验证容错路径")
    args = ap.parse_args()

    if args.simulate:
        clock = VirtualClock(step_min=args.poll_min)
        sim = Simulator(scenario=args.scenario, clock=clock, faults=args.faults)
        sched = Scheduler(bench=None, artex=None, clock=clock, simulate=True, sim=sim)
        sched.run()
        return

    # 真实模式前置校验
    if not BENCH_TOKEN or not BENCH_BASE:
        print("缺少 BENCHMARK_TOKEN / BENCHMARK_BASE_URL 环境变量。", file=sys.stderr)
        sys.exit(1)
    artex = ArtexClient()
    if not artex.login():
        print("ARTEX 登录失败，检查 ADMIN_PASSWORD / ARTEX_API。", file=sys.stderr)
        sys.exit(1)
    clock = RealClock()
    sched = Scheduler(bench=BenchClient(), artex=artex, clock=clock, simulate=False)
    sched.run()


if __name__ == "__main__":
    main()
