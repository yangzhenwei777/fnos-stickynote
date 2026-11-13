#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""Sticky Note 可重复性能与持续负载验证（纯标准库）。

示例：
  python scripts/benchmark.py server/sticky-note.exe web/dist --notes 1000 --concurrency 4
  python scripts/benchmark.py server/sticky-note.exe web/dist --notes 10000 --duration 120 --json bench.json

所有请求均校验状态和业务形状；非预期响应立即失败。临时进程和数据库始终清理。
"""
import argparse
import concurrent.futures
import json
import os
import shutil
import socket
import statistics
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


class Metrics:
    def __init__(self):
        self.values = []
        self.lock = threading.Lock()

    def add(self, seconds):
        with self.lock:
            self.values.append(seconds * 1000)

    def summary(self):
        if not self.values:
            raise RuntimeError("性能样本为空")
        data = sorted(self.values)
        def pct(p):
            return round(data[min(len(data) - 1, int((len(data) - 1) * p))], 3)
        return {
            "count": len(data), "p50_ms": pct(0.50), "p95_ms": pct(0.95),
            "p99_ms": pct(0.99), "mean_ms": round(statistics.fmean(data), 3),
            "max_ms": round(data[-1], 3),
        }


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Client:
    def __init__(self, base, token):
        self.base = base
        self.token = token

    def req(self, path, method="GET", body=None, expected=(200,), raw=False):
        payload = json.dumps(body).encode() if body is not None else None
        request = urllib.request.Request(self.base + path, data=payload, method=method)
        request.add_header("Content-Type", "application/json")
        request.add_header("Cookie", "access_token=" + self.token)
        started = time.perf_counter()
        try:
            with urllib.request.urlopen(request, timeout=120) as response:
                status = response.status
                data = response.read()
        except urllib.error.HTTPError as exc:
            status = exc.code
            data = exc.read()
        elapsed = time.perf_counter() - started
        if status not in expected:
            raise RuntimeError(f"{method} {path}: status={status}, want={expected}, body={data[:160]!r}")
        if raw:
            return elapsed, data
        content_type = response.headers.get("Content-Type", "")
        parsed = json.loads(data) if "json" in content_type and data else None
        return elapsed, parsed


def start_server(binary, webdir, datadir, port):
    env = dict(os.environ)
    env.pop("JWT_SECRET", None)
    env["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"
    proc = subprocess.Popen(
        [os.path.abspath(binary), f"-port={port}", f"-web-dir={webdir}",
         f"-data-dir={datadir}", f"-upload-dir={os.path.join(datadir, 'upload')}"],
        env=env, cwd=os.path.dirname(os.path.abspath(binary)),
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    base = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            with urllib.request.urlopen(base + "/api/health", timeout=1) as response:
                if response.status == 200:
                    return proc
        except (urllib.error.URLError, OSError):
            time.sleep(0.1)
    proc.terminate()
    raise RuntimeError("server not reachable")


def login_client(port):
    base = f"http://127.0.0.1:{port}"
    body = {"username": "bench", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"}
    request = urllib.request.Request(base + "/api/auth/register", data=json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"}, method="POST")
    with urllib.request.urlopen(request, timeout=30) as response:
        payload = json.loads(response.read())
    token = payload.get("access_token")
    if not token:
        raise RuntimeError("注册响应缺少 access_token")
    return Client(base, token)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("binary")
    parser.add_argument("webdir")
    parser.add_argument("--notes", type=int, default=1000)
    parser.add_argument("--concurrency", type=int, default=4)
    parser.add_argument("--duration", type=float, default=0, help="额外混合读负载秒数")
    parser.add_argument("--port", type=int, default=0)
    parser.add_argument("--json", dest="json_path")
    args = parser.parse_args()
    if args.notes <= 0 or args.concurrency <= 0 or args.duration < 0:
        parser.error("notes/concurrency 必须为正数，duration 不得为负")

    port = args.port or free_port()
    datadir = tempfile.mkdtemp(prefix="sticky-note-bench-")
    proc = start_server(args.binary, args.webdir, datadir, port)
    report = {
        "binary": os.path.abspath(args.binary), "notes": args.notes,
        "concurrency": args.concurrency, "duration_sec": args.duration,
        "port": port, "metrics": {},
    }
    try:
        client = login_client(port)
        # 基准库为一次性环境；按目标规模显式调整默认 1000 篇配额，避免大数据量
        # 在创建阶段被业务 403 正确拒绝而未进入持续负载。
        client.req("/api/configs/max_notes_per_user", "PUT",
                   {"value": str(max(1000, args.notes + 100))})
        create_metrics = Metrics()
        ids = []
        ids_lock = threading.Lock()

        def create(i):
            elapsed, note = client.req("/api/notes", "POST",
                                       {"title": f"笔记{i}", "content": f"<p>内容{i} 量子 拓扑 测试词</p>", "tags": "基准"},
                                       expected=(201,))
            if not isinstance(note, dict) or not note.get("id"):
                raise RuntimeError(f"创建响应缺 id: {note!r}")
            create_metrics.add(elapsed)
            with ids_lock:
                ids.append(note["id"])

        with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
            list(pool.map(create, range(args.notes)))
        report["metrics"]["create"] = create_metrics.summary()

        for name, path, expected in (
            ("list", "/api/notes?page=1&page_size=50", "notes"),
            ("search", "/api/notes?q=" + urllib.parse.quote("量子"), "notes"),
            ("detail", f"/api/notes/{ids[0]}", "id"),
        ):
            metric = Metrics()
            def read_once(path=path, expected=expected, metric=metric):
                elapsed, data = client.req(path)
                if not isinstance(data, dict) or expected not in data:
                    raise RuntimeError(f"响应字段 {expected} 缺失: {data!r}")
                metric.add(elapsed)
            for _ in range(20):
                read_once()
            report["metrics"][name] = metric.summary()

        if args.duration:
            mixed = Metrics()
            stop_at = time.monotonic() + args.duration
            def mixed_worker():
                while time.monotonic() < stop_at:
                    elapsed, data = client.req("/api/notes?page=1&page_size=20")
                    if not isinstance(data, dict) or "notes" not in data:
                        raise RuntimeError("持续负载列表响应异常")
                    mixed.add(elapsed)
                    elapsed, data = client.req("/api/notes?q=" + urllib.parse.quote("拓扑"))
                    if not isinstance(data, dict) or "notes" not in data:
                        raise RuntimeError("持续负载搜索响应异常")
                    mixed.add(elapsed)
            with concurrent.futures.ThreadPoolExecutor(max_workers=args.concurrency) as pool:
                list(pool.map(lambda _: mixed_worker(), range(args.concurrency)))
            report["metrics"]["mixed_read"] = mixed.summary()

        report["status"] = "PASS"
        return report
    except Exception as exc:
        report["status"] = "FAIL"
        report["error"] = str(exc)
        raise
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)
        shutil.rmtree(datadir, ignore_errors=True)
        if args.json_path:
            with open(args.json_path, "w", encoding="utf-8") as f:
                json.dump(report, f, ensure_ascii=False, indent=2)
        print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"BENCHMARK FAILED: {exc}", file=sys.stderr)
        sys.exit(1)
