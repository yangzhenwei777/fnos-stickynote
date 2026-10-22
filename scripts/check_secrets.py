#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Credential and project-sensitive-value gate.

Scans source files for hard-coded passwords/tokens. Optional local denylist
patterns can be supplied with --denylist-file; the file is intentionally kept
outside this repository.

Usage:
    python check_secrets.py [directory]
    python check_secrets.py [directory] --denylist-file <path>
    python check_secrets.py --denylist-selftest
"""
import os
import re
import sys

SCAN_EXTS = (".py", ".sh", ".ps1", ".go", ".js", ".ts", ".vue")

PRUNE_DIRS = {"node_modules", "dist", "build", "release", ".git", "__pycache__",
              "venv", ".venv", "vendor", ".pnpm-store", "coverage", "dist-desktop"}

TEST_PATH_MARKERS = ("__tests__", "__test__", "/testdata/", "test-data", "fixtures")
TEST_NAME_PATTERNS = ("_test.go", "_test.py", ".test.js", ".test.ts", ".spec.js", ".spec.ts")

ALLOWED = {
    "example", "your_", "xxx", "changeme", "placeholder", "test_password",
    "passw0rd!2026", "newpassw0rd!2026", "ci-e2e-test-pass",
    "secret123", "explicit-pwd", "switch-pwd", "abcdef12",
    "notepad_test_password", "your_password", "yourpassword",
}

PATTERNS = [
    re.compile(r"(?<![\w.:])(?:password|passwd|pwd)\s*[:=]\s*[\"'][^\"']{4,}[\"']", re.I),
    re.compile(r"(?<![\w.:])password\s*=\s*[\"'][^\"']{4,}[\"']", re.I),
    re.compile(r"echo\s+[\"']?[0-9A-Za-z@!#$%^&*._-]{4,}[\"']?\s*\|\s*sudo\s+-S", re.I),
    re.compile(r"[\"'](ghp_[0-9A-Za-z]{20,}|github_pat_[0-9A-Za-z_]{20,}|AKIA[0-9A-Z]{16}|xox[baprs]-[0-9A-Za-z-]{10,})[\"']"),
]

COMMENT_PREFIX = ("#", "//", "/*", "*", "<!--", "*/")


def _p(*parts):
    return re.compile("".join(parts))


# Synthetic examples only. Real identifiers belong in the local-only denylist.
DENYLIST = [
    _p(r"\b", "192", r"\.0\.2\.12", "3", r"\b"),
    _p(r"\b", "note", r"\.selftest", r"\.invalid\b"),
    _p("Users", r"[/\\\\]", "Sensitive", "User", r"\b"),
    _p(":", "789", "1", r"\b"),
]

DENYLIST_EXTS = SCAN_EXTS + (".md", ".txt", ".yaml", ".yml", ".json", ".html")
DENYLIST_NAMES = ("manifest",)
SELF_REL = os.path.normpath("scripts/check_secrets.py").lower()


def scan_denylist_file(path):
    hits = []
    try:
        with open(path, "r", encoding="utf-8", errors="ignore") as f:
            for lineno, line in enumerate(f, 1):
                for pat in DENYLIST:
                    if pat.search(line):
                        hits.append((lineno, line.strip()[:120]))
                        break
    except OSError:
        pass
    return hits


def is_test_file(relpath):
    low = relpath.replace("\\", "/").lower()
    if any(m in low for m in TEST_PATH_MARKERS):
        return True
    name = low.rsplit("/", 1)[-1]
    return any(name.endswith(suf) or ("." + suf.lstrip(".") in name) for suf in TEST_NAME_PATTERNS)


def scan_file(path):
    hits = []
    try:
        with open(path, "r", encoding="utf-8", errors="ignore") as f:
            for lineno, line in enumerate(f, 1):
                stripped = line.strip()
                if not stripped:
                    continue
                if stripped.startswith(COMMENT_PREFIX):
                    continue
                low = line.lower()
                if any(a in low for a in ALLOWED):
                    continue
                for pat in PATTERNS:
                    if pat.search(line):
                        hits.append((lineno, line.strip()[:120]))
                        break
    except OSError:
        pass
    return hits


def load_denylist(path):
    patterns = []
    with open(path, "r", encoding="utf-8", errors="ignore") as f:
        for lineno, raw in enumerate(f, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            try:
                patterns.append(re.compile(line))
            except re.error as exc:
                print(f"[check_secrets] 无效 denylist 正则 {path}:{lineno}: {exc}")
                return None
    return patterns


def main():
    args = sys.argv[1:]
    denylist_file = None
    positional = []
    i = 0
    while i < len(args):
        if args[i] == "--denylist-file":
            if i + 1 >= len(args):
                print("[check_secrets] --denylist-file 缺少路径")
                return 2
            denylist_file = args[i + 1]
            i += 2
            continue
        if args[i].startswith("--"):
            i += 1
            continue
        positional.append(args[i])
        i += 1

    default = os.path.abspath(os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
    root = positional[0] if positional else default
    root = os.path.abspath(root)
    if not os.path.isdir(root):
        print(f"[check_secrets] 目录不存在: {root}")
        return 2

    if "--denylist-selftest" in sys.argv:
        import tempfile
        ok = True
        plant_ip = "192" + ".0.2.12" + "3"
        plant_domain = "note" + ".selftest" + ".invalid"
        plant_user = "C:\\Users\\" + "Sensitive" + "User\\x"
        plant_port = ":" + "789" + "1"
        with tempfile.TemporaryDirectory() as td:
            probe = os.path.join(td, "probe.md")
            open(probe, "w", encoding="utf-8").write(
                "# 自测\n服务器 " + plant_ip + " 与 " + plant_domain + "；" + plant_user + "；" + plant_port + "\n干净行\n")
            hits = scan_denylist_file(probe)
            ok &= len(hits) >= 1
            clean = os.path.join(td, "clean.md")
            open(clean, "w", encoding="utf-8").write(
                "# 干净文档\n示例服务器 192.0.2.10 与 note.example.com；C:\\Users\\TestUser\\x;:7890\n")
            ok &= len(scan_denylist_file(clean)) == 0
        print(f"[check_secrets] DENYLIST-SELFTEST 植入命中检出={'YES' if ok else 'NO（门禁失效）'} / 干净误报={'有' if not ok else '无'}")
        return 0 if ok else 1

    global DENYLIST
    if denylist_file:
        extra = load_denylist(denylist_file)
        if extra is None:
            return 2
        DENYLIST = DENYLIST + extra

    total = 0
    scanned = 0
    deny_hits = 0
    deny_scanned = 0
    for dirpath, dirnames, files in os.walk(root):
        dirnames[:] = [d for d in dirnames if d.lower() not in PRUNE_DIRS]
        for name in files:
            p = os.path.join(dirpath, name)
            rel = os.path.normpath(os.path.relpath(p, root)).lower()
            low_name = name.lower()
            is_src = name.endswith(SCAN_EXTS)
            in_deny_scope = name.endswith(DENYLIST_EXTS) or name in DENYLIST_NAMES
            if is_src and not is_test_file(os.path.relpath(p, root)):
                scanned += 1
                for lineno, line in scan_file(p):
                    total += 1
                    print(f"[FAIL] {rel}:{lineno}: {line}")
            if in_deny_scope and rel != SELF_REL:
                deny_scanned += 1
                for lineno, line in scan_denylist_file(p):
                    deny_hits += 1
                    print(f"[FAIL-DENYLIST] {rel}:{lineno}: {line}")

    if total or deny_hits:
        print(f"[check_secrets] 发现 {total} 处疑似硬编码凭据 + {deny_hits} 处项目敏感值命中"
              f"（凭据扫 {scanned} / denylist 扫 {deny_scanned} 个文件）")
        return 1
    print(f"[check_secrets] OK：凭据扫 {scanned} / denylist 扫 {deny_scanned} 个文件，未发现泄露")
    return 0


if __name__ == "__main__":
    sys.exit(main())
