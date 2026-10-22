#!/usr/bin/env python3
# vuln_gate.py — CI 漏洞门禁：govulncheck / npm audit 基线 diff 只拦新增（纪律 3.2）
# 用法：govulncheck -json ./... | python3 scripts/vuln_gate.py --mode go --baseline docs/漏洞扫描基线-xxx.md
#       npm audit --json --registry=https://registry.npmjs.org | python3 scripts/vuln_gate.py --mode npm --baseline docs/漏洞扫描基线-xxx.md
# 退出码 0=无新增，1=发现新增漏洞
#
# v21.52 审计修复（豁免机制）：
#   基线条目支持三种格式：
#     1. 纯 ID（历史遗留，仅记录 ID）
#     2. "ID | 影响版本 | 当前版本 | 豁免理由"（推荐，附上下文）
#     3. "ID | ... | DUE:YYYY-MM-DD"（带复核到期日）
#   到期日已过 = 视为新增（强制复核），避免"历史漏洞永久忽略"。

import os
import json, re, sys, argparse, datetime

def parse_baseline_entry(line):
    """解析基线条目 → (id, due_date)。返回 id 与可选到期日。"""
    line = line.strip()
    if not line or line.startswith("#"):
        return None, None
    # 取第一段为 ID（GO-XXXX 或 GHSA-XXXX 或包名，含 @scope/name）。
    # v21.107 修复：正则此前不支持 @ 开头——@tiptap/core 基线条目被静默丢弃，
    # gate 输出「无新增」为**假绿**（CI 基线恢复生效后才暴露 37 条真未覆盖）。
    m = re.match(r"^(GO-\d+-\d+|GHSA-[A-Za-z0-9-]+|@?[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)?)", line)
    if not m:
        # 机器不可读的基线行必须显式告警——静默丢弃会让豁免悄悄失效（纪律：门禁反向验证同源教训）
        import sys as _sys
        print(f"[vuln_gate WARN] 基线行无法解析（该豁免不生效）: {line[:60]}", file=_sys.stderr)
        return None, None
    vid = m.group(1)
    due = None
    dm = re.search(r"DUE:(\d{4}-\d{2}-\d{2})", line)
    if dm:
        try:
            due = datetime.date.fromisoformat(dm.group(1))
        except ValueError:
            due = None
    return vid, due

def load_go_baseline(baseline_path):
    """从基线文件读取在案的 GO-XXXX-XXXX ID（每行一个）→ {id: due_date}"""
    entries = {}
    with open(baseline_path, "r", encoding="utf-8") as f:
        for line in f:
            vid, due = parse_baseline_entry(line)
            if vid and vid.startswith("GO-"):
                entries[vid] = due
    return entries

def load_npm_baseline(baseline_path):
    """从基线文件读取在案的 GHSA-XXXX 或包名（每行一个条目）→ {id: due_date}"""
    entries = {}
    with open(baseline_path, "r", encoding="utf-8") as f:
        for line in f:
            vid, due = parse_baseline_entry(line)
            if vid:
                entries[vid.upper()] = due
    return entries

def check_go(data, baseline_ids):
    """govulncheck -json 输出为 NDJSON（可能有格式化的多行对象），按行解析提取 GO IDs

    govulncheck v1.6.0 JSON 协议（NDJSON，顶层对象五类）：
      - config / SBOM / progress : 元数据，无漏洞判定
      - osv     : 漏洞数据库全量信息（含"扫描到但判定不受影响"的记录）
      - finding : 实际受影响（可达调用链）的漏洞，finding.osv 为漏洞 ID
    修复（v21.79，GitHub CI 误报根因）：
      _walk_go_ids 此前无差别收集所有 GO- ID（含 osv 段不受影响记录），
      2026-08-13 Go 官方 6 条 stdlib 公告（修复版 go1.26.6，本项目已用）被误判为
      "新增漏洞"阻断 CI。现只收集 finding 段的 ID；无 finding 段（旧版工具）
      时降级收集 osv 段 ID，保证向后兼容。
    """
    finding_ids = set()
    osv_ids = set()
    decoder = json.JSONDecoder()
    pos = 0
    s = data.strip()
    while pos < len(s):
        # 跳过空白
        while pos < len(s) and s[pos] in ' \t\n\r':
            pos += 1
        if pos >= len(s):
            break
        try:
            obj, end = decoder.raw_decode(s, pos)
            pos = end
            if 'finding' in obj:
                f = obj['finding']
                if isinstance(f, dict) and isinstance(f.get('osv'), str) and f['osv'].startswith('GO-'):
                    finding_ids.add(f['osv'])
            elif 'osv' in obj:
                v = obj['osv']
                if isinstance(v, dict) and isinstance(v.get('id'), str) and v['id'].startswith('GO-'):
                    osv_ids.add(v['id'])
        except json.JSONDecodeError:
            # 跳过无法解析的行（govulncheck 可能输出非 JSON 内容）
            pos += 1
    # 优先用 finding 段（真实受影响）；无 finding 段（旧版协议）降级用 osv 段
    ids_found = finding_ids if finding_ids else osv_ids
    new = []
    today = datetime.date.today()
    for vid in sorted(ids_found):
        due = baseline_ids.get(vid)
        if due is None:
            new.append(f"{vid} (不在基线)")
        elif due < today:
            new.append(f"{vid} (基线豁免已到期 {due}，强制复核)")
    return new

def _walk_go_ids(obj, ids):
    """保留的通用递归提取（向后兼容路径，正常不触发；见 check_go 注释）"""
    if isinstance(obj, dict):
        if 'id' in obj and isinstance(obj['id'], str) and obj['id'].startswith('GO-'):
            ids.add(obj['id'])
        for v in obj.values():
            _walk_go_ids(v, ids)
    elif isinstance(obj, list):
        for item in obj:
            _walk_go_ids(item, ids)

def _iter_npm_findings(findings):
    """统一迭代 npm audit 报告条目，产出 {'label':..., 'ids': set()}，兼容两代报告格式。

    auditReportVersion 2（npm 7+，**当前实际输出**）：顶层 vulnerabilities = {包名: {...}}
      - GHSA ID 藏在 via 列表里：via 元素为 dict 时 url 末段即 GHSA ID；
        为字符串时代表上游包名（间接依赖链）。
    auditReportVersion 1（npm 6，历史格式）：顶层 advisories = {ID: {...}}
      - 直接含 id / module_name / title 字段。

    v21.91 修复（实测发现）：此前只读 advisories——npm 7+ 输出中该键**恒为空 dict**，
    导致本门禁永远返回"无新增"（实测：npm audit 报 5 条 high，门禁仍输出 ✅ 全绿），
    纪律 3.2 的 npm audit 基线 diff 形同虚设。现同时兼容两种格式。
    """
    if findings.get("vulnerabilities"):
        for pkg, v in findings["vulnerabilities"].items():
            ids = {pkg.upper()}
            for via in v.get("via", []):
                if isinstance(via, dict):
                    if via.get("name"):
                        ids.add(via["name"].upper())
                    url = via.get("url") or ""
                    if url:
                        ids.add(url.rstrip("/").rsplit("/", 1)[-1].upper())
                elif isinstance(via, str):
                    ids.add(via.upper())
            title = ""
            for via in v.get("via", []):
                if isinstance(via, dict) and via.get("title"):
                    title = via["title"]
                    break
            yield {"label": f"{pkg}（{v.get('severity', '?')}）{title}".strip(), "ids": ids}
        return

    for id_, adv in findings.get("advisories", {}).items():
        ids = {id_.upper()}
        if adv.get("module_name"):
            ids.add(adv["module_name"].upper())
        yield {"label": f"{id_} ({adv.get('module_name', '')}) {adv.get('title', '')}".strip(),
               "ids": ids}


def check_npm(data, baseline_entries):
    findings = json.loads(data) if isinstance(data, str) else data
    new = []
    today = datetime.date.today()
    for item in _iter_npm_findings(findings):
        matched = [i for i in item["ids"] if i in baseline_entries]
        if not matched:
            new.append(f"{item['label']} (不在基线)")
            continue
        # 命中基线：任一条目有未过期的豁免即放行；全部命中项都过期才强制复核
        dues = [baseline_entries[i] for i in matched if baseline_entries[i]]
        if dues and max(dues) < today:
            new.append(f"{item['label']} 基线豁免已到期 {max(dues)}，强制复核")
    return new

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--mode", choices=["go", "npm"], required=True)
    p.add_argument("--baseline", required=True)
    args = p.parse_args()
    data = sys.stdin.read()

    # v21.112 修复（P3，2026-09-07 深度测试实测）：空输入 = 扫描工具链故障
    # （govulncheck 未运行 / npm audit 失败 / 管道断裂），此前会被判为
    # "✅ 无新增漏洞" 假绿。门禁必须 fail-closed：无输入即失败（exit 2）。
    if not data.strip():
        print("⛔ 门禁输入为空：扫描工具未产出任何输出（工具链故障？）")
        print("   可能原因：govulncheck 不在 PATH / go 工具链缺失 / npm audit 网络失败 / 管道断裂。")
        print("   按 fail-closed 判为失败；修复工具链后重跑（不得以空输入视为通过）。")
        sys.exit(2)

    if args.mode == "go":
        baseline_ids = load_go_baseline(args.baseline)
        new = check_go(data, baseline_ids)
        for vid in new:
            print(f"  NEW: {vid}")
        bl_size = len(baseline_ids)
    else:
        baseline_entries = load_npm_baseline(args.baseline)
        new = check_npm(data, baseline_entries)
        for item in new:
            print(f"  NEW: {item}")
        bl_size = len(baseline_entries)

    if new:
        print(f"\n⛔ {len(new)} 条新增/到期漏洞（超出基线），阻断交付。")
        print("   若须豁免：更新基线文件并标注原因 + DUE:YYYY-MM-DD 复核期限（纪律 6.2），PR 内做完再重跑。")
        sys.exit(1)
    print(f"✅ 无新增漏洞（基线 {bl_size} 条在案）")
    sys.exit(0)

if __name__ == "__main__":
    main()
