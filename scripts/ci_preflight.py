#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""CI preflight — push 前轻量预检，尽量把低级漂移拦在本地。

用法：
    python scripts/ci_preflight.py                # 完整预检
    python scripts/ci_preflight.py --quick       # 快速预检（跳过 SBOM/凭据扫描）
    python scripts/ci_preflight.py --public-root D:/path/to/public-repo

设计目标：
1) 元数据单一事实源：VERSION / manifest / wails / 根 VERSION 不允许漂移。
2) 发布面一致性：Docker 正式 tag 必须是 X.Y.Z；预发布 -R 不允许触发正式 tag。
3) 基础文本卫生：Go/Python/YAML/PowerShell 文件必须有 EOF 换行。
4) WebView2 已登记哈希在 CI 与构建脚本之间一致。
5) 可选运行 SBOM 门禁与凭据扫描，保证 push 前能复现 CI 的低成本门禁。

退出码：0=通过；1=发现阻断项；2=脚本自身无法运行。
"""
import argparse
import json
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
TEXT_SUFFIXES = {".go", ".py", ".yml", ".yaml", ".ps1"}
HEX64 = re.compile(r"[0-9a-fA-F]{64}")


def add_problem(problems, label, detail):
    problems.append(f"{label}: {detail}")
    print(f"  ❌ {label}: {detail}")


def read_text(path):
    return path.read_text(encoding="utf-8-sig", errors="replace")


def check_version_metadata(problems):
    print("== 1. 版本与发布元数据 ==")
    version_path = ROOT / "VERSION"
    if not version_path.is_file():
        add_problem(problems, "metadata", f"missing {version_path}")
        return None
    version = version_path.read_text(encoding="utf-8").strip()
    print(f"  VERSION = {version}")

    manifest_path = ROOT / "fnpack" / "manifest"
    if manifest_path.is_file():
        match = re.search(r"^version\s*=\s*(\S+)", read_text(manifest_path), re.M)
        manifest_version = match.group(1) if match else None
        if manifest_version != version:
            add_problem(problems, "manifest", f"{manifest_version} != VERSION {version}")
        else:
            print(f"  manifest version OK = {manifest_version}")
    else:
        add_problem(problems, "manifest", f"missing {manifest_path}")

    wails_path = ROOT / "desktop" / "wails.json"
    if wails_path.is_file():
        try:
            wails_version = json.loads(read_text(wails_path))["info"]["productVersion"]
        except Exception as exc:
            add_problem(problems, "wails.json", f"cannot parse productVersion: {exc}")
        else:
            if wails_version != version:
                add_problem(problems, "wails.json", f"{wails_version} != VERSION {version}")
            else:
                print(f"  wails productVersion OK = {wails_version}")
    else:
        add_problem(problems, "wails.json", f"missing {wails_path}")

    # 交接包根 VERSION（可选）：本地常有，但公共仓/CI checkout 没有。
    base_root = ROOT.parent.parent
    base_version_path = base_root / "VERSION"
    if base_version_path.is_file():
        base_version = base_version_path.read_text(encoding="utf-8").strip()
        if base_version != version:
            add_problem(problems, "root VERSION", f"{base_version} != source VERSION {version}")
        else:
            print("  handover root VERSION OK")

    if re.fullmatch(r"\d+\.\d+\.\d+", version):
        print("  Docker formal tag format OK")
    elif re.fullmatch(r"\d+\.\d+\.\d+-R\d+", version):
        add_problem(problems, "docker tag", "prerelease -R version must not be published as vX.Y.Z tag")
    else:
        add_problem(problems, "version format", f"invalid VERSION {version!r}")
    return version


def check_public_root(problems, public_root):
    if not public_root:
        return
    print("== 2. 公共导出根 ==")
    public_root = Path(public_root).resolve()
    manifest = public_root / "fnpack" / "manifest"
    if not manifest.is_file():
        add_problem(problems, "public-root", f"missing {manifest}")
        return
    match = re.search(r"^version\s*=\s*(\S+)", read_text(manifest), re.M)
    public_version = match.group(1) if match else None
    source_version = read_text(ROOT / "VERSION").strip()
    if public_version != source_version:
        add_problem(problems, "public manifest", f"{public_version} != source VERSION {source_version}")
    else:
        print(f"  public manifest version OK = {public_version}")
    required = ["VERSION", "Makefile", "Dockerfile", ".github/workflows/ci.yml", ".github/workflows/docker-publish.yml"]
    missing = [name for name in required if not (public_root / name).is_file()]
    if missing:
        add_problem(problems, "public-root", "missing files: " + ", ".join(missing))
    else:
        print("  public export core files OK")


def check_text_hygiene(problems):
    print("== 3. 文本 EOF 换行 ==")
    bad = []
    for suffix in TEXT_SUFFIXES:
        for path in ROOT.rglob(f"*{suffix}"):
            rel = path.relative_to(ROOT).as_posix().lower()
            if any(part in rel for part in ("node_modules/", ".git/", "release/", "dist/", "dist-desktop/", "build/bin/", "__pycache__/")):
                continue
            try:
                if not path.is_file() or path.stat().st_size == 0:
                    continue
                data = path.read_bytes()
            except OSError:
                continue
            if not data.endswith(b"\n"):
                bad.append(path.relative_to(ROOT).as_posix())
    if bad:
        add_problem(problems, "EOF newline", "files missing trailing newline: " + ", ".join(bad[:20]) + (" ..." if len(bad) > 20 else ""))
    else:
        print("  tracked text files end with newline")


def extract_hex_list(text, marker):
    match = re.search(re.escape(marker) + r"\s*=\s*@?\(([\s\S]*?)\)", text)
    if not match:
        return []
    return sorted({item.lower() for item in HEX64.findall(match.group(1))})


def check_webview_hashes(problems):
    print("== 4. WebView2 已登记哈希 ==")
    build_path = ROOT / "desktop" / "scripts" / "build-desktop.ps1"
    ci_path = ROOT / ".github" / "workflows" / "ci.yml"
    if not build_path.is_file() or not ci_path.is_file():
        add_problem(problems, "webview2 hashes", "missing build script or CI workflow")
        return
    build_hashes = extract_hex_list(read_text(build_path), "$WV2_PREPACK_SHA256")
    ci_hashes = extract_hex_list(read_text(ci_path), "$expected")
    if not build_hashes or not ci_hashes:
        add_problem(problems, "webview2 hashes", "cannot parse registered hash list")
        return
    if build_hashes != ci_hashes:
        add_problem(problems, "webview2 hashes", f"build={build_hashes} ci={ci_hashes}")
    else:
        print(f"  registered hash list OK ({len(build_hashes)} entries)")


def run_gate(problems, label, args, cwd=ROOT):
    print(f"== {label} ==")
    try:
        proc = subprocess.run(args, cwd=str(cwd), text=True, encoding="utf-8", errors="replace", timeout=180)
    except (OSError, subprocess.TimeoutExpired) as exc:
        add_problem(problems, label, f"gate failed to run: {exc}")
        return
    if proc.returncode != 0:
        add_problem(problems, label, f"exit code {proc.returncode}")
    else:
        print("  OK")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--quick", action="store_true", help="skip SBOM and secrets scan")
    parser.add_argument("--skip-secrets", action="store_true")
    parser.add_argument("--skip-sbom", action="store_true")
    parser.add_argument("--public-root", help="optional public export root to check")
    args = parser.parse_args()

    problems = []
    if not ROOT.is_dir():
        print(f"⛔ source root not found: {ROOT}")
        return 2

    version = check_version_metadata(problems)
    if version is None:
        return 1
    check_public_root(problems, args.public_root)
    check_text_hygiene(problems)
    check_webview_hashes(problems)

    if not args.quick and not args.skip_sbom:
        run_gate(problems, "SBOM gate", [sys.executable, "scripts/sbom_gate.py", "--check", "docs/依赖清单.txt"])
        run_gate(problems, "SBOM selftest", [sys.executable, "scripts/sbom_gate.py", "--selftest", "docs/依赖清单.txt"])
    if not args.quick and not args.skip_secrets:
        run_gate(problems, "credential scan", [sys.executable, "scripts/check_secrets.py", "."])

    if problems:
        print(f"\nPREFLIGHT FAILED: {len(problems)} problem(s)")
        for item in problems:
            print(f"  - {item}")
        return 1
    print("\nPREFLIGHT OK")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        print("PREFLIGHT interrupted")
        sys.exit(2)
