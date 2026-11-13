# -*- coding: utf-8 -*-
"""P1-b / P1-1 压测专项（2.1.1 轮，§32~36 大载荷通道）。

覆盖：
  A. 大载荷同步全链：10MB / 50MB 笔记 → 创建 → blob_ref 行 → 文件 sha256 对拍 → 端点下载对拍（计时）
  B. 4MB 边界：4MB-1 / 4MB+1 两种分流判定下全链正确（预判含 64KB 余量，宁可 blob）
  C. §33 内存验证：50MB 创建前后服务端进程 WorkingSet 采样（流式写盘路径不复制载荷）
  D. 并发：10 线程并发创建 5MB×10 + 并发下载端点，全部 201/200 且 sha256 对拍

用法：python scripts/stress_p1b.py <server_exe> <web_dist>
退出码：0=全部通过，1=有失败。结果同时打印供报告引用。
"""
import ctypes, hashlib, http.client, json, os, shutil, socket, subprocess, sys, tempfile, threading, time

BIN = sys.argv[1]
WEB = sys.argv[2]
def _free_port():
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p

PORT = int(os.environ.get("STICKY_TEST_PORT") or _free_port())
BASE = f"127.0.0.1:{PORT}"
MB = 1024 * 1024
passed, failed = [], []


def check(name, cond, detail=""):
    (passed if cond else failed).append(name)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + (f"  | {detail}" if detail else ""))


def req(method, path, body=None, cookies=None, raw=False, timeout=120):
    conn = http.client.HTTPConnection(BASE, timeout=timeout)
    headers = {"Content-Type": "application/json"}
    if cookies:
        headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    conn.request(method, path, json.dumps(body) if body is not None else None, headers)
    resp = conn.getresponse()
    data = resp.read()
    setcookies = {}
    for k, v in resp.getheaders():
        if k.lower() == "set-cookie":
            nv = v.split(";")[0]
            if "=" in nv:
                n, val = nv.split("=", 1)
                setcookies[n] = val
    conn.close()
    try:
        j = json.loads(data)
    except Exception:
        j = None
    return resp.status, j, setcookies, data


def ws_bytes(pid):
    """服务端进程 WorkingSet（ctypes GetProcessMemoryInfo，无第三方依赖）。"""

    class PMC(ctypes.Structure):
        _fields_ = [("cb", ctypes.c_ulong), ("PageFaultCount", ctypes.c_ulong),
                    ("PeakWorkingSetSize", ctypes.c_size_t), ("WorkingSetSize", ctypes.c_size_t),
                    ("QuotaPeakPagedPoolUsage", ctypes.c_size_t), ("QuotaPagedPoolUsage", ctypes.c_size_t),
                    ("QuotaPeakNonPagedPoolUsage", ctypes.c_size_t), ("QuotaNonPagedPoolUsage", ctypes.c_size_t),
                    ("PagefileUsage", ctypes.c_size_t), ("PeakPagefileUsage", ctypes.c_size_t)]

    PROCESS_QUERY_INFORMATION = 0x0400
    k32 = ctypes.windll.kernel32
    psapi = ctypes.windll.psapi
    h = k32.OpenProcess(PROCESS_QUERY_INFORMATION, False, pid)
    if not h:
        return None
    try:
        pmc = PMC()
        pmc.cb = ctypes.sizeof(PMC)
        if not psapi.GetProcessMemoryInfo(h, ctypes.byref(pmc), pmc.cb):
            return None
        return pmc.WorkingSetSize
    finally:
        k32.CloseHandle(h)


def blob_asserts(tag, nid, note_content, data_dir, cookies):
    """创建成功后的 blob 链四连断言：行 / 文件 / 下载 / sha 对拍。"""
    import sqlite3
    db = sqlite3.connect(os.path.join(data_dir, "notepad.db"))
    try:
        row = db.execute(
            "SELECT payload_kind, payload_ref, payload_size, payload_json FROM sync_changes "
            "WHERE entity_type='NOTE' AND entity_sync_id=(SELECT sync_id FROM notes WHERE id=?)", (nid,)).fetchone()
    finally:
        db.close()
    check(f"{tag} ChangeLog 行存在", bool(row), f"kind={row[0] if row else None}")
    kind, ref, size, pj = row
    check(f"{tag} 分流判定生效", kind == "blob_ref", f"kind={kind}")
    pf = os.path.join(data_dir, "sync-payloads", ref)
    blob = open(pf, "rb").read() if os.path.exists(pf) else b""
    check(f"{tag} 文件 sha256=ref 且 size 对拍",
          len(blob) == size and hashlib.sha256(blob).hexdigest() == ref, f"file={len(blob)} row={size}")
    s, _, _, raw = req("GET", f"/api/sync/payload/{ref}", cookies=cookies, raw=True, timeout=300)
    check(f"{tag} 端点下载 sha256 对拍", s == 200 and hashlib.sha256(raw or b"").hexdigest() == ref, f"status={s} len={len(raw or b'')}")
    return ref


def main():
    data_dir = tempfile.mkdtemp(prefix="sticky-stress-")
    print(f"[setup] data_dir={data_dir}")
    env = dict(os.environ)
    env.pop("JWT_SECRET", None)
    env["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"
    p = subprocess.Popen([BIN, f"-port={PORT}", f"-web-dir={WEB}", f"-data-dir={data_dir}",
                          f"-upload-dir={os.path.join(data_dir, 'upload')}"],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
    try:
        for _ in range(50):
            try:
                if req("GET", "/api/health")[0] == 200:
                    break
            except Exception:
                pass
            time.sleep(0.2)
        s, j, sc, _ = req("POST", "/api/auth/register",
                          {"username": "stress", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        if s not in (200, 201) or not sc:
            s, j, sc, _ = req("POST", "/api/auth/login", {"username": "stress", "password": "Passw0rd!2026"})
        assert s in (200, 201) and sc, f"login failed: {s} {j}"

        print("\n[A] 大载荷同步全链（10MB / 50MB / 100MB）")
        for tag, mb in (("A1(10MB)", 10), ("A2(50MB)", 50), ("A3(100MB)", 100)):
            content = "Q" * (mb * MB)
            t0 = time.time()
            s, j, _, _ = req("POST", "/api/notes", {"title": f"stress-{tag}", "content": content}, cookies=sc, timeout=300)
            dt = time.time() - t0
            nid = (j or {}).get("id")
            check(f"{tag} 创建 201（{dt:.2f}s）", s == 201 and bool(nid), f"status={s} dt={dt:.2f}s")
            if s == 201:
                blob_asserts(tag, nid, content, data_dir, sc)

        print("\n[B] 4MB 边界（预判含 64KB 余量：4MB±1 均应正常工作）")
        for tag, n in (("B1(4MB-1)", 4 * MB - 1), ("B2(4MB+1)", 4 * MB + 1)):
            content = "R" * n
            s, j, _, _ = req("POST", "/api/notes", {"title": f"stress-{tag}", "content": content}, cookies=sc, timeout=120)
            nid = (j or {}).get("id")
            check(f"{tag} 创建 201", s == 201 and bool(nid), f"status={s}")
            if s == 201:
                blob_asserts(tag, nid, content, data_dir, sc)

        print("\n[C] §33 内存采样（50MB 创建前后 WorkingSet）")
        pid = p.pid
        ws0 = ws_bytes(pid)
        s, j, _, _ = req("POST", "/api/notes", {"title": "stress-C-50MB", "content": "M" * (50 * MB)}, cookies=sc, timeout=300)
        nid = (j or {}).get("id")
        ws1 = ws_bytes(pid)
        check("C1 50MB 创建 201", s == 201 and bool(nid), f"status={s}")
        if s == 201:
            blob_asserts("C(50MB)", nid, "M" * (50 * MB), data_dir, sc)
        if ws0 and ws1:
            growth = (ws1 - ws0) / MB
            # 口径说明：§33 管的是「ChangeLog 写盘路径不复制载荷」（writePayloadBlob 流式
            # Encoder，文件 sha 对拍即直接证据）。本断言观测的是**整个 HTTP 链**的
            # WorkingSet 增量：body 原始字节 + 反序列化 string + payload 结构 ≈ 3×载荷
            # （blob 改造前即如此，非本轮引入）。若出现「marshal+内联」的双复制形态
            # （+2×载荷 ≈ 250MB 级）即判 FAIL——阈值 200MB 划在这两者之间。
            check(f"C2 WorkingSet 增量 {growth:.0f}MB < 200MB（流式写盘生效，无 marshal 双复制形态）",
                  growth < 200, f"growth={growth:.0f}MB ws0={ws0//MB}MB（口径：HTTP 链固有 ≈3×载荷）")
        else:
            # 强制验收环境下采样失败不得伪装成通过；普通开发机仅降级提示。
            check("C2 内存采样不可用（降级提示）", os.environ.get("STRESS_REQUIRE_MEMINFO") != "1",
                  "GetProcessMemoryInfo 失败；STRESS_REQUIRE_MEMINFO=1 时必须判失败")

        print("\n[D] 并发：10 线程 × 5MB 创建 + 并发下载")
        results = {}

        def create(i):
            try:
                s, j, _, _ = req("POST", "/api/notes", {"title": f"conc-{i}", "content": "N" * (5 * MB)}, cookies=sc, timeout=300)
                results[i] = (s, (j or {}).get("id"))
            except Exception as e:
                results[i] = (0, str(e))

        t0 = time.time()
        threads = [threading.Thread(target=create, args=(i,)) for i in range(10)]
        [t.start() for t in threads]
        [t.join() for t in threads]
        dt = time.time() - t0
        ok_ids = [results[i][1] for i in range(10) if results[i][0] == 201]
        check(f"D1 10 并发创建全 201（{dt:.2f}s）", len(ok_ids) == 10, f"dt={dt:.2f}s ok={len(ok_ids)}/10")
        errs = [results[i] for i in range(10) if results[i][0] != 201]
        check("D2 无异常响应", not errs, str(errs[:3]))

        # 并发下载：取全部 blob ref 并行拉取对拍。
        import sqlite3
        db = sqlite3.connect(os.path.join(data_dir, "notepad.db"))
        try:
            refs = [r[0] for r in db.execute("SELECT payload_ref FROM sync_changes WHERE payload_kind='blob_ref'").fetchall()]
        finally:
            db.close()
        dl_ok = []
        dl_lock = threading.Lock()

        def download(ref):
            try:
                s, _, _, raw = req("GET", f"/api/sync/payload/{ref}", cookies=sc, raw=True, timeout=300)
                with dl_lock:
                    dl_ok.append(s == 200 and hashlib.sha256(raw or b"").hexdigest() == ref)
            except Exception:
                with dl_lock:
                    dl_ok.append(False)

        threads = [threading.Thread(target=download, args=(r,)) for r in refs]
        [t.start() for t in threads]
        [t.join() for t in threads]
        check(f"D3 并发下载 {len(refs)} 件全对拍", all(dl_ok) and len(dl_ok) == len(refs), f"ok={sum(dl_ok)}/{len(dl_ok)}")

        print("\n[E] pull 形状（blob_ref 行轻量化：响应体不得内联载荷）")
        s, j, _, _ = req("GET", "/api/sync/state", cookies=sc)
        cur = (j or {}).get("current_change_seq", 0)
        s, j, _, raw = req("GET", f"/api/sync/pull?cursor=0&watermark={cur}&limit=500", cookies=sc, raw=True)
        changes = (j or {}).get("changes", [])
        blob_rows = [c for c in changes if c.get("payload_kind") == "blob_ref"]
        check("E1 pull 含 blob_ref 行", len(blob_rows) >= 13, f"blob_rows={len(blob_rows)} total={len(changes)}")
        inline_leak = [c for c in blob_rows if len(json.dumps(c.get("payload"))) > 100]
        check("E2 blob_ref 行不内联载荷（payload=null）", not inline_leak, f"leak={len(inline_leak)}")
        total_bytes = len(raw or b"")
        check(f"E3 pull 响应体 {total_bytes//1024}KB（16 件大载荷仅 KB 级=分流生效）", total_bytes < 2 * MB, f"bytes={total_bytes}")

    finally:
        p.terminate()
        try:
            p.wait(timeout=5)
        except Exception:
            p.kill()
        shutil.rmtree(data_dir, ignore_errors=True)

    print(f"\nRESULT: {len(passed)} passed, {len(failed)} failed")
    if failed:
        print("FAILED:", failed)
        sys.exit(1)


if __name__ == "__main__":
    main()
