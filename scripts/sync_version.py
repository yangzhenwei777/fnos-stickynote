#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""sync_version — 版本元数据单一事实源同步。

用法：
    python scripts/sync_version.py 2.6.6 --dry-run
    python scripts/sync_version.py 2.6.6

只同步纯元数据（VERSION / fnpack manifest / desktop wails productVersion /
交接包根 VERSION / 公共 overlay manifest）。changelog 必须人工编写，避免自动
生成发布文案造成事实漂移。
"""
import argparse
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r"^\d+\.\d+\.\d+(?:-R\d+)?$")
MANIFEST_RE = re.compile(r"^(version\s*=\s*)(\S+)\s*$", re.M)
WAILS_RE = re.compile(r"(\"productVersion\"\s*:\s*\")([^\"]+)(\")")


def replace(path, pattern, repl, label, changes, dry_run):
    if not path.is_file():
        print(f"  skip missing {label}: {path}")
        return
    text = path.read_text(encoding="utf-8-sig")
    new_text, count = pattern.subn(repl, text, count=1)
    if count == 0:
        raise SystemExit(f"⛔ {label}: pattern not found in {path}")
    if new_text == text:
        print(f"  unchanged {label}: {path}")
        return
    changes.append((path, new_text))
    print(f"  {'would update' if dry_run else 'updated'} {label}: {path}")


def write(path, content, dry_run):
    if not dry_run:
        if path.suffix == ".json":
            path.write_text(content, encoding="utf-8", newline="\n")
        else:
            path.write_text(content, encoding="utf-8", newline="\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("version")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--public-overlay", default=ROOT.parent.parent / "devwalk" / "public-overlay" / "fnpack" / "manifest")
    args = parser.parse_args()

    version = args.version.strip()
    if not VERSION_RE.fullmatch(version):
        raise SystemExit(f"⛔ invalid version: {version!r} (expected X.Y.Z or X.Y.Z-R<n>)")

    changes = []

    # 源码树 VERSION。
    version_path = ROOT / "VERSION"
    if not version_path.is_file():
        raise SystemExit(f"⛔ missing {version_path}")
    old = version_path.read_text(encoding="utf-8")
    if old != version + "\n":
        changes.append((version_path, version + "\n"))
        print(f"  {'would update' if args.dry_run else 'updated'} VERSION: {version_path}")

    # fnpack manifest。
    manifest_path = ROOT / "fnpack" / "manifest"
    if manifest_path.is_file():
        replace(manifest_path, MANIFEST_RE, rf"\g<1>{version}", "fnpack manifest", changes, args.dry_run)
    else:
        raise SystemExit(f"⛔ missing {manifest_path}")

    # desktop wails.json：保留格式，只替换 productVersion 字面量。
    wails_path = ROOT / "desktop" / "wails.json"
    if not wails_path.is_file():
        raise SystemExit(f"⛔ missing {wails_path}")
    text = wails_path.read_text(encoding="utf-8-sig")
    new_text, count = WAILS_RE.subn(rf"\g<1>{version}\g<3>", text, count=1)
    if count == 0:
        raise SystemExit(f"⛔ wails.json productVersion not found")
    if new_text != text:
        changes.append((wails_path, new_text))
        print(f"  {'would update' if args.dry_run else 'updated'} wails.json: {wails_path}")

    # 交接包根 VERSION（本地存在才同步）。
    base_root = ROOT.parent.parent
    handover_version = base_root / "VERSION"
    if handover_version.is_file() and handover_version.read_text(encoding="utf-8") != version + "\n":
        changes.append((handover_version, version + "\n"))
        print(f"  {'would update' if args.dry_run else 'updated'} handover VERSION: {handover_version}")

    # 公共 overlay manifest（默认本地 devwalk 路径，不存在则跳过）。
    overlay_path = Path(args.public_overlay)
    if overlay_path.is_file():
        replace(overlay_path, MANIFEST_RE, rf"\g<1>{version}", "public overlay manifest", changes, args.dry_run)

    if not changes:
        print("SYNC_VERSION OK: already consistent")
        return 0

    if not args.dry_run:
        for path, content in changes:
            write(path, content)
        # JSON 重新解析校验，防止写坏 wails.json。
        parsed = json.loads(wails_path.read_text(encoding="utf-8-sig"))
        if parsed["info"]["productVersion"] != version:
            raise SystemExit("⛔ wails.json productVersion mismatch after write")

    print("SYNC_VERSION OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
