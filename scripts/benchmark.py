#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
benchmark.py — 便签性能基准（v21.73 P2-09）

用法：
    python scripts/benchmark.py <server二进制> <web目录> [--notes 1000]

压测项：批量创建笔记 → 列表分页查询 → 全文搜索 → 单篇详情 → 批量删除，
产出各阶段耗时报告。用于建立 1k/10k/100k 规模基线（家庭 NAS 建议 1k 起步）。

示例：
    python scripts/benchmark.py server/sticky-note-linux-amd64 web/dist --notes 1000
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
import urllib.request

BASE = "http://127.0.0.1:18906"
PORT = 18906


def req(path, method="GET", body=None, cookies=None, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(BASE + path, data=data, method=method)
    r.add_header("Content-Type", "application/json")
    if cookies:
        r.add_header("Cookie", "; ".join(f"{k}={v}" for k, v in cookies.items()))
    try:
        resp = urllib.request.urlopen(r, timeout=30)
        ct = resp.headers.get("content-type", "")
        b = resp.read()
        if raw:
            return resp.status, b, resp.headers
        if "json" in ct:
            return resp.status, json.loads(b), resp.headers, None
        return resp.status, None, resp.headers, None
    except urllib.error.HTTPError as e:
        return e.code, None, None, None


def start_server(binary, webdir, datadir):
    env = dict(os.environ)
    env.pop("JWT_SECRET", None)
    env["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"
    proc = subprocess.Popen(
        [os.path.abspath(binary), f"-port={PORT}", f"-web-dir={webdir}",
         f"-data-dir={datadir}", f"-upload-dir={os.path.join(datadir, 'upload')}"],
        env=env, cwd=os.path.dirname(os.path.abspath(binary)),
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    for _ in range(100):
        try:
            if req("/api/health")[0] in (200, 404):
                return proc
        except Exception:
            pass
        time.sleep(0.1)
    proc.terminate()
    raise RuntimeError("server not reachable")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("binary")
    ap.add_argument("webdir")
    ap.add_argument("--notes", type=int, default=1000)
    args = ap.parse_args()

    datadir = tempfile.mkdtemp(prefix="sticky-note-bench-")
    srv = start_server(args.binary, args.webdir, datadir)
    try:
        s, j, _, _ = req("/api/auth/register", "POST",
                         {"username": "bench", "password": "Passw0rd!2026",
                          "security_question": "q", "security_answer": "a"})
        if s not in (200, 201):
            print(f"注册失败: {s}")
            return 1
        # 注册接口可能已返回 cookie，需重新登录拿 token
        s, j, h, _ = req("/api/auth/login", "POST", {"username": "bench", "password": "Passw0rd!2026"})
        sc = {}
        for k, v in h.items() if h else []:
            if k.lower() == "set-cookie":
                sc[v.split(";")[0].split("=")[0]] = v.split(";")[0].split("=", 1)[1]
        # 兜底：从登录响应体拿 token
        if not sc and j and "access_token" in j:
            sc["access_token"] = j["access_token"]

        n = args.notes
        print(f"== 便签性能基准：{n} 篇笔记 ==")

        # 1. 批量创建
        t0 = time.time()
        for i in range(n):
            req("/api/notes", "POST", {"title": f"笔记{i}", "content": f"<p>内容{i} 量子 拓扑 测试词</p>", "tags": "基准"}, cookies=sc)
        t_create = time.time() - t0
        print(f"批量创建 {n} 篇: {t_create:.2f}s ({n/t_create:.0f} 篇/s)")

        # 2. 列表分页
        t0 = time.time()
        for p in range(1, 6):
            req(f"/api/notes?page={p}&page_size=50", cookies=sc)
        t_list = time.time() - t0
        print(f"列表分页 5 页: {t_list:.3f}s")

        # 3. 全文搜索
        t0 = time.time()
        for _ in range(10):
            req("/api/notes?q=" + urllib.parse.quote("量子"), cookies=sc)
        t_search = time.time() - t0
        print(f"全文搜索 10 次: {t_search:.3f}s ({t_search/10*1000:.0f} ms/次)")

        # 4. 单篇详情
        s, j, _, _ = req("/api/notes?page=1&page_size=1", cookies=sc)
        nid = (j or {}).get("notes", [{}])[0].get("id") if j else None
        t0 = time.time()
        for _ in range(20):
            req(f"/api/notes/{nid}", cookies=sc)
        t_detail = time.time() - t0
        print(f"单篇详情 20 次: {t_detail:.3f}s ({t_detail/20*1000:.0f} ms/次)")

        print("== 完成（建议另跑 --notes 10000 / 100000 建完整基线）==")
        return 0
    finally:
        srv.terminate()
        srv.wait()


if __name__ == "__main__":
    sys.exit(main())
