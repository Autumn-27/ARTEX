#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""chains-20260701 反问思维链语料 ETL(CHAINS-INTEGRATION-DESIGN.md §6 步骤 1)。

解析 chains-20260701/chains/ 下 62 个 md(三级结构:`# 领域 · 主题` → `### 场景句`
→ 单行 `**Qn**:`/`**An**:` 对,编号节内重号),产出结构化 JSON:

  chains-20260701/dist/chains.json          全量记录 + stats
  chains-20260701/dist/<category>.json      按类分文件(web/ad/app/priv/pwn/recon/rev/pivot/meta)
  chains-20260701/dist/check_report.txt     校验报告(--check 时只打印不落盘)

记录字段:{id, category, file, section, section_idx, q_idx, q, a[, platform_specific]}
id 为稳定复合键:<文件名>#s<节序号>#q<节内序号>。

用法:
  python scripts/chains_etl.py            # 解析 + 校验 + 写 dist/
  python scripts/chains_etl.py --check    # 仅校验,打印报告;发现问题时退出码 1
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SRC = ROOT / "chains-20260701" / "chains"
DIST = ROOT / "chains-20260701" / "dist"

H1_RE = re.compile(r"^# (.+?)\s*$")
SECTION_RE = re.compile(r"^### (.+?)\s*$")
QA_RE = re.compile(r"^\*\*([QA])(\d+)\*\*:\s?(.*)$")
BOLD_QA_LIKE_RE = re.compile(r"^\*\*[QA]")  # 形如上式但不匹配的异常行

# 人工标记:耦合其它平台机制(黑板 remember/recall、Ralph 压缩)的条目。
# (file, section_idx 1-based) -> 该节全部 QA 打 platform_specific: true
PLATFORM_SPECIFIC = {
    # 整节围绕"把入场券 remember 到黑板 / recall 取回 / Ralph 压缩重置",
    # 机制绑定外部记忆平台,非通用判据。
    ("meta-close-the-kill.md", 2): "黑板 remember/recall + Ralph 压缩耦合",
}


def parse_file(path: Path):
    """返回 (title, sections, anomalies)。sections: [{title, lines:[(kind,num,text,line_no)]}]"""
    title = ""
    sections = []
    anomalies = []  # (line_no, kind, text)
    cur = None
    for no, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.rstrip()
        if not line.strip():
            continue
        m = H1_RE.match(line)
        if m and not line.startswith("##"):
            if not title:
                title = m.group(1)
                continue
        m = SECTION_RE.match(line)
        if m:
            cur = {"title": m.group(1), "lines": []}
            sections.append(cur)
            continue
        if line.startswith(">"):
            continue  # tags 行等元信息
        if re.fullmatch(r"-{3,}\s*", line):
            continue  # 分隔线
        m = QA_RE.match(line)
        if m:
            if cur is None:
                anomalies.append((no, "qa_outside_section", line[:80]))
                continue
            cur["lines"].append((m.group(1), int(m.group(2)), m.group(3), no))
            continue
        if BOLD_QA_LIKE_RE.match(line):
            anomalies.append((no, "malformed_qa_line", line[:80]))
            continue
        # 其余行(## 标题、普通段落等)视为异常候选记录,便于人工确认
        anomalies.append((no, "unrecognized_line", line[:80]))
    return title, sections, anomalies


def build_records(files):
    records, report = [], []
    for path in files:
        fname = path.name
        category = fname.split("-", 1)[0]
        title, sections, anomalies = parse_file(path)
        for no, kind, text in anomalies:
            report.append(f"[异常行] {fname}:{no} {kind}: {text}")
        if not title:
            report.append(f"[文件] {fname}: 缺少 H1 标题")
        if not sections:
            report.append(f"[文件] {fname}: 无任何 ### 场景节")
        for s_idx, sec in enumerate(sections, 1):
            if not sec["lines"]:
                report.append(f"[空节] {fname}#s{s_idx} 「{sec['title']}」 无 Q/A")
                continue
            qs = [(n, t) for k, n, t, _ in sec["lines"] if k == "Q"]
            ans = {n: t for k, n, t, _ in sec["lines"] if k == "A"}
            # 编号连续性:节内重号,Q 编号应为 1..len(qs)
            q_nums = [n for n, _ in qs]
            if q_nums != list(range(1, len(qs) + 1)):
                report.append(
                    f"[编号不连续] {fname}#s{s_idx} 「{sec['title']}」 Q 编号序列 {q_nums}"
                )
            a_nums = sorted(ans)
            if a_nums != list(range(1, len(a_nums) + 1)):
                report.append(
                    f"[编号不连续] {fname}#s{s_idx} 「{sec['title']}」 A 编号序列 {a_nums}"
                )
            # 成对校验
            for n, qtext in qs:
                if n not in ans:
                    report.append(
                        f"[不成对] {fname}#s{s_idx}#q{n}: 有 Q 无 A 「{qtext[:40]}」"
                    )
            for n in a_nums:
                if n not in q_nums:
                    report.append(
                        f"[不成对] {fname}#s{s_idx}: 有 A{n} 无对应 Q"
                    )
            ps_key = (fname, s_idx)
            for q_idx, (n, qtext) in enumerate(qs, 1):
                rec = {
                    "id": f"{fname}#s{s_idx}#q{q_idx}",
                    "category": category,
                    "file": fname,
                    "section": sec["title"],
                    "section_idx": s_idx,
                    "q_idx": q_idx,
                    "q": qtext,
                    "a": ans.get(n, ""),
                }
                if ps_key in PLATFORM_SPECIFIC:
                    rec["platform_specific"] = True
                    rec["platform_specific_note"] = PLATFORM_SPECIFIC[ps_key]
                records.append(rec)
    return records, report


def make_stats(records, files):
    cats = {}
    for f in files:
        c = f.name.split("-", 1)[0]
        cats.setdefault(c, {"files": 0, "sections": 0, "qas": 0})
        cats[c]["files"] += 1
    seen_sections = set()
    for r in records:
        c = cats[r["category"]]
        key = (r["file"], r["section_idx"])
        if key not in seen_sections:
            seen_sections.add(key)
            c["sections"] += 1
        c["qas"] += 1
    total = {
        "files": sum(c["files"] for c in cats.values()),
        "sections": sum(c["sections"] for c in cats.values()),
        "qas": sum(c["qas"] for c in cats.values()),
    }
    return {"by_category": cats, "total": total}


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--check", action="store_true", help="仅校验并打印报告,不写 dist/")
    ap.add_argument("--src", type=Path, default=SRC)
    ap.add_argument("--dist", type=Path, default=DIST)
    args = ap.parse_args()

    files = sorted(args.src.glob("*.md"))
    if not files:
        print(f"未找到语料文件: {args.src}", file=sys.stderr)
        return 2

    records, report = build_records(files)
    stats = make_stats(records, files)

    lines = ["== chains ETL 校验报告 ==", ""]
    for cat in sorted(stats["by_category"]):
        c = stats["by_category"][cat]
        lines.append(f"{cat:6s} 文件 {c['files']:2d}  节 {c['sections']:4d}  QA {c['qas']:5d}")
    t = stats["total"]
    lines.append(f"{'合计':6s} 文件 {t['files']:2d}  节 {t['sections']:4d}  QA {t['qas']:5d}")
    lines.append("")
    ps = sum(1 for r in records if r.get("platform_specific"))
    lines.append(f"platform_specific 标记: {ps} 条")
    lines.append("")
    if report:
        lines.append(f"发现 {len(report)} 项问题:")
        lines.extend("  " + r for r in report)
    else:
        lines.append("未发现问题:Q/A 全部成对、节内编号连续、无空节、无异常行。")
    output = "\n".join(lines)
    print(output)

    if args.check:
        return 1 if report else 0

    args.dist.mkdir(parents=True, exist_ok=True)
    payload = {"stats": stats, "records": records}
    (args.dist / "chains.json").write_text(
        json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8"
    )
    for cat in sorted(stats["by_category"]):
        recs = [r for r in records if r["category"] == cat]
        sub = {
            "category": cat,
            "stats": stats["by_category"][cat],
            "records": recs,
        }
        (args.dist / f"{cat}.json").write_text(
            json.dumps(sub, ensure_ascii=False, indent=1), encoding="utf-8"
        )
    (args.dist / "check_report.txt").write_text(output + "\n", encoding="utf-8")
    print(f"\n已写出: {args.dist / 'chains.json'} + {len(stats['by_category'])} 个分类文件 + check_report.txt")
    return 1 if report else 0


if __name__ == "__main__":
    sys.exit(main())
