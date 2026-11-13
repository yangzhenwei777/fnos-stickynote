#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""release_publish — GitHub Release 创建/资产上传助手。

默认 dry-run，不做网络写入；只有显式 --publish 才会创建 Release 和上传资产。

用法示例：
    python scripts/release_publish.py \
      --repo owner/repo --tag v2.6.6 \
      --assets release/StickyNote.fpk release/StickyNote-desktop.zip \
               release/SHA256SUMS.txt release/StickyNote-source.tar.gz

    python scripts/release_publish.py ... --publish

规则：
1) tag 必须是 vX.Y.Z，且必须与 VERSION 完全一致。
2) 资产文件必须存在、非空、名称 ASCII。
3) 若资产中包含 SHA256SUMS.txt，脚本会反向核对其余资产的 SHA256。
4) 真实发布需要 GITHUB_TOKEN 或 GH_TOKEN；令牌只从环境变量读取，不落日志。
"""
import argparse
import base64
import hashlib
import json
import os
import re
import sys
import urllib.error
import urllib.request
from pathlib import Path

VERSION_RE = re.compile(r"\d+\.\d+\.\d+")


def fail(message):
    print(f"⛔ {message}")
    return 1


def github_request(method, url, token, headers=None, body=None, timeout=300):
    all_headers = {
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
        "User-Agent": "sticky-note-release",
    }
    if headers:
        all_headers.update(headers)
    if token:
        all_headers["Authorization"] = "Basic " + base64.b64encode(token.encode()).decode()
    request = urllib.request.Request(url, data=body, method=method, headers=all_headers)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read()
        return json.loads(raw) if raw and response.headers.get("Content-Type", "").find("json") >= 0 else raw


def parse_sha256sums(path):
    entries = {}
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line:
            continue
        match = re.fullmatch(r"([0-9a-fA-F]{64})  (.+)", line)
        if not match:
            raise ValueError(f"invalid SHA256SUMS line: {line}")
        entries[match.group(2).replace("\\", "/")] = match.group(1).lower()
    return entries


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--title")
    parser.add_argument("--body")
    parser.add_argument("--assets", nargs="+", action="extend", required=True)
    parser.add_argument("--publish", action="store_true", help="actually create release and upload assets")
    args = parser.parse_args()

    problems = []
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repo):
        problems.append(f"invalid repo: {args.repo}")
    if not re.fullmatch("v" + VERSION_RE.pattern, args.tag):
        problems.append(f"tag must be vX.Y.Z: {args.tag}")

    root = Path.cwd()
    version_path = root / "VERSION"
    if version_path.is_file():
        version = version_path.read_text(encoding="utf-8").strip()
        if args.tag.lstrip("v") != version:
            problems.append(f"tag {args.tag} does not match VERSION {version}")
    else:
        problems.append(f"missing VERSION: {version_path}")

    assets = [Path(item) for item in args.assets]
    if not assets:
        problems.append("no release assets")
    for path in assets:
        if not path.is_file() or path.stat().st_size == 0:
            problems.append(f"asset missing or empty: {path}")
            continue
        name = path.name
        if not name.isascii():
            problems.append(f"asset name must be ASCII: {name}")
        if name in {"SHA256SUMS.txt", "SHA256SUMS"}:
            try:
                expected = parse_sha256sums(path)
            except Exception as exc:
                problems.append(f"invalid SHA256SUMS: {exc}")
                continue
            for candidate in assets:
                rel = candidate.name
                if rel in expected:
                    actual = hashlib.sha256(candidate.read_bytes()).hexdigest()
                    if actual != expected[rel]:
                        problems.append(f"SHA mismatch: {rel}")
                elif rel != path.name:
                    problems.append(f"asset not listed in SHA256SUMS: {rel}")
            if path.name not in expected and path.name != "SHA256SUMS.txt":
                problems.append("SHA256SUMS does not list itself")

    if problems:
        for item in problems:
            print(f"  - {item}")
        return fail("release dry-run validation failed")

    print("RELEASE VALIDATION OK")
    for path in assets:
        print(f"  asset: {path.name} ({path.stat().st_size} bytes)")
    if not args.publish:
        print("DRY-RUN ONLY: no network write performed")
        return 0

    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if not token:
        return fail("publish requires GITHUB_TOKEN or GH_TOKEN")

    api = f"https://api.github.com/repos/{args.repo}/releases"
    payload = json.dumps({
        "tag_name": args.tag,
        "target_commitish": "main",
        "name": args.title or f"Sticky Note {args.tag.lstrip('v')}",
        "body": args.body or "",
        "draft": False,
        "prerelease": False,
    }).encode()
    release = github_request("POST", api, token, {"Content-Type": "application/json"}, payload)
    release_id = release["id"]
    print(f"RELEASE_CREATED id={release_id} url={release['html_url']}")

    for path in assets:
        name = path.name
        escaped = urllib.parse.quote(name)
        url = f"https://uploads.github.com/repos/{args.repo}/releases/{release_id}/assets?name={escaped}"
        with path.open("rb") as body:
            github_request(
                "POST", url, token,
                {"Content-Type": "application/octet-stream"},
                body,
                timeout=1800,
            )
        print(f"ASSET_UPLOADED {name}")
    print("RELEASE_PUBLISH_OK")
    return 0


if __name__ == "__main__":
    import urllib.parse
    sys.exit(main())
