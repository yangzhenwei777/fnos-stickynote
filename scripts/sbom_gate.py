#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
scripts/sbom_gate.py —— 依赖清单（SBOM）生成与 I 类门禁校验（唯一实现，CI 与本地共用）

存在理由（177/178 实证）：交付面的 `依赖清单-*.txt` 的 npm 段曾是**手写**，与权威树失真——
列了 6 个树上根本不存在的包、8 项区间矛盾、20 项未具名；而旧门禁只核"清单文件在不在"，
且"npm 42"恰好等于 `npm ls --depth=0` 含根行的输出，于是"逐项一致"长期假绿。
本脚本把清单变成**由命令实跑产出**，并提供 `--check` 按**包名与区间**逐项核对（不按行数）。

三种用法：
    python3 scripts/sbom_gate.py --gen                     # 输出当前树的清单正文（交付面写文件用）
    python3 scripts/sbom_gate.py --check docs/依赖清单.txt  # I 类门禁：仓内基准 ↔ 权威树交叉核对
    python3 scripts/sbom_gate.py --selftest                # 反向验证：5 类定向污染必须全部被检出

CI 位置：.github/workflows/ci.yml backend job（有 go + python3；npm 段只比 `package.json` 声明区间，
不需要 node_modules —— 实装版本列仅由 `--gen` 在本地交付时填）。

口径：
  Go 段 = `cd server && go list -m all` 原样（主模块 + 全部间接依赖）
  npm 段 = `web/package.json` 的 dependencies + devDependencies（直接依赖，逐项）
  与上一版对比须**逐包比名称与区间**；行数相等不构成"零变更"的证据。
"""
import argparse
import datetime
import io
import json
import os
import re
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)          # 源码树根（server/ web/ docs/ 的上一级）
BASELINE = os.path.join(ROOT, "docs", "依赖清单.txt")


def read(p):
    return io.open(p, encoding="utf-8").read()


def find_go():
    """go 可执行文件：PATH 优先，其次本机隔离安装位。找不到即让门禁失败，绝不静默跳过。"""
    c = shutil.which("go")
    if c:
        return c
    for cand in (os.path.expanduser(r"~\.workbuddy\binaries\go\go\bin\go.exe"),
                 "/usr/local/go/bin/go"):
        if os.path.exists(cand):
            return cand
    return None


# ---------------- 权威树实测值 ----------------

def live_go_rows():
    go = find_go()
    if not go:
        return None
    r = subprocess.run([go, "list", "-m", "all"], cwd=os.path.join(ROOT, "server"),
                       capture_output=True, text=True, encoding="utf-8", errors="replace")
    if r.returncode != 0:
        return None
    return [l.strip() for l in r.stdout.splitlines() if l.strip()]


def live_npm_declared():
    """{包名: 声明区间}（直接依赖）。"""
    p = os.path.join(ROOT, "web", "package.json")
    if not os.path.exists(p):
        return None
    j = json.loads(read(p))
    return {**j.get("dependencies", {}), **j.get("devDependencies", {})}


def installed_version(name):
    """node_modules 实装版本（仅 --gen 用；缺失记 '-'，不参与 --check 判定）。"""
    p = os.path.join(ROOT, "web", "node_modules", *name.split("/"), "package.json")
    if not os.path.exists(p):
        return "-"
    try:
        return json.loads(read(p)).get("version", "-")
    except Exception:
        return "-"


def dev_code(ver):
    """开发代号 v21.N 只能查表：取版本变更记录里与本 VERSION 同行的那个。"""
    t = read(os.path.join(ROOT, "docs", "版本变更记录.md")) if os.path.exists(
        os.path.join(ROOT, "docs", "版本变更记录.md")) else ""
    m = re.search(r"##\s*(v\d+\.\d+)\s*/\s*VERSION\s*" + re.escape(ver), t or "")
    return m.group(1) if m else "v?"


# ---------------- 清单正文 ----------------

def build_body(ver, go_rows, npm_rows, label=None, gen_date=None):
    deps, devs = npm_rows["dependencies"], npm_rows["devDependencies"]
    total = len(deps) + len(devs)
    bar = "=" * 80
    o = [bar,
         f"Sticky note 依赖清单（SBOM）— {label or ('StickyNote-' + ver)}（{dev_code(ver)} / VERSION {ver}）",
         bar,
         f"生成日期：{gen_date or datetime.date.today().isoformat()}",
         "生成器：scripts/sbom_gate.py --gen（本文件由脚本生成，请勿手改；依赖有变动请重跑并同步交付面副本）",
         "生成方式：",
         f"  Go ：cd server && go list -m all            → {len(go_rows)} 行（主模块 + 全部间接依赖）",
         f"  npm：web/package.json 声明区间 + node_modules 实装版本 → {total} 项直接依赖"
         f"（{len(deps)} dependencies + {len(devs)} devDependencies）",
         "",
         "【对比口径】与上一版对比须**逐包比名称与区间**，行数相等不等于零变更；"
         "本清单由 `scripts/sbom_gate.py --check` 与权威树交叉核对（CI backend job 常驻）。",
         "【范围】NAS fpk 交付面（server + web）。desktop 模块（Wails/x-crypto 等）依赖树由 CI 单列"
         " `依赖清单-desktop.txt`（artifact），不入本清单。",
         bar, "", "",
         f"一、Go 模块清单（go list -m all，{len(go_rows)} 行）", bar]
    o += go_rows
    o += ["", "", f"二、npm 直接依赖清单（{total} 项；名称 / 声明区间 / 实装版本）", bar,
          f"[dependencies] {len(deps)} 项"]
    o += [f"{n:<44} {r:<12} {v}" for n, r, v in deps]
    o += ["", f"[devDependencies] {len(devs)} 项"]
    o += [f"{n:<44} {r:<12} {v}" for n, r, v in devs]
    o.append("")
    return "\n".join(o)


def gen(label=None, gen_date=None):
    ver = (read(os.path.join(ROOT, "VERSION")) or "").strip()
    if not re.match(r"^\d+\.\d+\.\d+$", ver):
        raise SystemExit(f"VERSION 非法：{ver!r}")
    go = live_go_rows()
    if go is None:
        raise SystemExit("取不到 `go list -m all`（go 未找到或执行失败）——拒绝生成空清单")
    pkg = json.loads(read(os.path.join(ROOT, "web", "package.json")))
    npm = {k: [(n, r, installed_version(n)) for n, r in sorted(pkg.get(k, {}).items())]
           for k in ("dependencies", "devDependencies")}
    return ver, go, npm, build_body(ver, go, npm, label=label, gen_date=gen_date)


# ---------------- 清单解析与核对（I 类断言） ----------------

def parse(text):
    """→ (go_rows, {npm: (区间, 实装)}, {计数声称})；解析不出 Go 段即返回 None 由调用方判门禁失效。"""
    g = re.search(r"^一、Go 模块清单[^\n]*$\n=+\n(.*?)^\s*二、npm", text, re.M | re.S)
    if not g:
        return None, None, None
    go_rows = [l.strip() for l in g.group(1).splitlines() if l.strip() and not l.startswith("=")]
    npm = {}
    for l in text.split("二、npm", 1)[1].splitlines():
        s = l.strip()
        if not s or s.startswith(("=", "[", "二、", "#")) or set(s) <= {"-"}:
            continue
        f = s.split()
        if len(f) in (2, 3) and re.match(r"^[@\w]", f[0]) and re.search(r"\d", f[1]) \
                and (len(f) == 2 or re.match(r"^\d|^-$", f[2])):
            npm[f[0]] = (f[1], f[2] if len(f) == 3 else "-")
    claims = {}
    for key, pat in (("go_header", r"一、Go 模块清单（[^）]*?(\d+)\s*行"),
                     ("npm_header", r"二、npm 直接依赖清单（(\d+)\s*项"),
                     ("go_gen", r"go list -m all\s*→\s*(\d+)\s*行"),
                     ("npm_gen", r"→\s*(\d+)\s*项直接依赖")):
        m = re.search(pat, text)
        if m:
            claims[key] = int(m.group(1))
    return go_rows, npm, claims


def verify(go_rows, npm, claims, live_go, live_npm, problems):
    """逐项核对；不符项 append 到 problems。live_* 为 None 视为门禁失效（不是通过）。"""
    if live_go is None:
        problems.append("取不到 `go list -m all` 实跑结果（go 未找到或执行失败）→ Go 段未核对，视同门禁失效")
    else:
        A, B = set(go_rows), set(live_go)
        d = [f"{x}（清单有/树无）" for x in sorted(A - B)[:6]] + \
            [f"{x}（树有/清单漏）" for x in sorted(B - A)[:6]]
        if d:
            problems.append("Go 段与权威树不符：" + ", ".join(d))
        else:
            claims["go_actual"] = len(live_go)
    if live_npm is None:
        problems.append("读不到 web/package.json → npm 段未核对，视同门禁失效")
    else:
        ghost = sorted(set(npm) - set(live_npm))
        miss = sorted(set(live_npm) - set(npm))
        if ghost:
            problems.append(f"npm 段含权威树未声明的幽灵依赖 {len(ghost)} 项：{ghost[:8]}")
        if miss:
            problems.append(f"npm 段漏列权威树声明的直接依赖 {len(miss)} 项：{miss[:8]}")
        wr = [f"{k}:清单{npm[k][0]}/声明{live_npm[k]}" for k in sorted(set(npm) & set(live_npm))
              if npm[k][0] != live_npm[k]]
        if wr:
            problems.append("npm 声明区间与 package.json 不符：" + ", ".join(wr[:6]))
        claims["npm_actual"] = len(live_npm)
    for key, act in (("go_header", claims.get("go_actual")), ("go_gen", claims.get("go_actual")),
                     ("npm_header", claims.get("npm_actual")), ("npm_gen", claims.get("npm_actual"))):
        if key in claims and act is not None and claims[key] != act:
            problems.append(f"计数声称与实得不符：{key} 声称 {claims[key]} ≠ 实得 {act}")


def do_check(path):
    if not os.path.exists(path):
        print(f"[I 类门禁] FAIL：基准文件不存在 {path}")
        return 1
    text = read(path).replace("\r\n", "\n")
    go_rows, npm, claims = parse(text)
    if go_rows is None:
        print("[I 类门禁] FAIL：基准解析不出「一、Go 模块清单」段（格式漂移？）")
        return 1
    problems = []
    verify(go_rows, npm, claims, live_go_rows(), live_npm_declared(), problems)
    if problems:
        print(f"[I 类门禁] FAIL：SBOM 与权威树不符（{len(problems)} 项）")
        for p in problems:
            print("  - " + p)
        print("处置：重跑 `python3 scripts/sbom_gate.py --gen > docs/依赖清单.txt`"
              "（确认是依赖真变了，而不是清单被手改）")
        return 1
    print(f"[I 类门禁] OK：Go {len(go_rows)} 行逐行相符 ｜ npm {len(npm)} 项逐包相符（名称+区间）")
    return 0


# ---------------- 反向自测（门禁必须能真的红） ----------------

def do_selftest(path):
    """5 类定向污染逐一注入，要求**每类都被检出**，且未污染的原文件零误报。"""
    if not os.path.exists(path):
        print(f"[I 类自测] FAIL：基准文件不存在 {path}")
        return 1
    base = read(path).replace("\r\n", "\n")
    live_go, live_npm = live_go_rows(), live_npm_declared()
    if live_go is None or live_npm is None:
        print("[I 类自测] FAIL：取不到权威树实测值（go 或 package.json）")
        return 1
    pkg_name = sorted(live_npm)[0]

    def polluted(kind):
        t = base
        if kind == "幽灵包":
            return must_change(kind, t.replace("[dependencies] ",
                                               "[dependencies] \nghost-not-real-pkg              ^9.9.9      9.9.9\n[dependencies] ", 1))
        if kind == "漏列":
            rows = [l for l in t.splitlines() if not l.strip().startswith(pkg_name)]
            return must_change(kind, "\n".join(rows))
        if kind == "区间矛盾":
            return must_change(kind, re.sub(r"^(" + re.escape(pkg_name) + r"\s+)\S+",
                                            r"\g<1>^0.0.1", t, count=1, flags=re.M))
        if kind == "Go 行":
            return must_change(kind, t.replace("\n二、npm", "\nghost.example/module v0.0.1\n\n二、npm", 1))
        if kind == "计数谎报":
            # 注意：清单正文用全角逗号「，」——半角写会让替换空转，自测就变成"永远检不出"的空门禁
            return must_change(kind, t.replace("一、Go 模块清单（go list -m all，",
                                               "一、Go 模块清单（go list -m all，谎报 999 行，"))
        raise AssertionError(kind)

    def must_change(kind, t):
        if t == base:
            raise SystemExit(f"[I 类自测] 污染[{kind}] 未改动任何文本 —— 注入式失效，"
                             f"该自测会是永真的空门禁，拒绝继续")
        return t

    classes = ["幽灵包", "漏列", "区间矛盾", "Go 行", "计数谎报"]
    failed = 0
    # 先确认原文件干净（防"门禁逢改必红"这种没法用的实现）
    clean = []
    g, n, c = parse(base)
    verify(g, n, c, live_go, live_npm, clean)
    print(f"  基准原文零误报：{'OK' if not clean else 'FAIL ' + str(clean[:2])}")
    if clean:
        failed += 1
    for k in classes:
        probs = []
        g, n, c = parse(polluted(k))
        if g is None:
            probs = ["解析失败"]
        else:
            verify(g, n, c, live_go, live_npm, probs)
        ok = bool(probs)
        print(f"  污染[{k}] → {'检出 OK' if ok else '漏检 FAIL'}"
              + (f"（{probs[0][:70]}）" if probs else ""))
        if not ok:
            failed += 1
    print(f"[I 类自测] {'OK：5 类污染全部检出且原文件零误报' if failed == 0 else f'FAIL：{failed} 项不达预期'}")
    return 0 if failed == 0 else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--gen", action="store_true", help="输出清单正文到 stdout")
    ap.add_argument("--check", nargs="?", const=BASELINE, metavar="FILE", help="I 类门禁：核对基准")
    ap.add_argument("--selftest", nargs="?", const=BASELINE, metavar="FILE", help="反向自测")
    ap.add_argument("--crlf", action="store_true", help="--gen 时输出 CRLF（交付面历史约定）")
    ap.add_argument("--label", default=None, help="--gen 标题里的交付包名（如 StickyNote-20260921-179.fpk）")
    ap.add_argument("--date", default=None, help="--gen 生成日期（默认今天）")
    a = ap.parse_args()
    if a.gen:
        _, _, _, body = gen(label=a.label, gen_date=a.date)
        sys.stdout.write(body.replace("\n", "\r\n") if a.crlf else body)
        return 0
    if a.check:
        return do_check(a.check)
    if a.selftest:
        return do_selftest(a.selftest)
    ap.print_help()
    return 2


if __name__ == "__main__":
    sys.exit(main())
