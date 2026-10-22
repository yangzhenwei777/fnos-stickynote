#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
edge_test.py — 便签边界 / 死胡同 / 越权测试（v21.75 最终测试补充，纳入长期测试体系）

覆盖：
  A. 边界：空/纯空格/超长标题、超长内容、emoji/中文/特殊字符、配额=1、分页越界
  B. 死胡同：忘记密码找回闭环、删除用户彻底清理
  C. 越权/安全：跨用户访问笔记/附件/回收站、无效/负 ID、非法 JSON

用法：python scripts/edge_test.py <server二进制> <web目录>
"""
import base64
import http.client
import json
import os
import subprocess
import sys
import tempfile
import time

BIN = sys.argv[1]
WEB = sys.argv[2]
def _free_port():
    import socket
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p

# 评审 P1-5a：端口可经 env STICKY_TEST_PORT / argv[3] 覆盖，缺省自动选空闲端口，
# 消除硬编码 18905 与真机/Docker/并行套件的端口冲突。
PORT = int(os.environ.get("STICKY_TEST_PORT") or (sys.argv[3] if len(sys.argv) > 3 else _free_port()))
BASE = f"127.0.0.1:{PORT}"
passed, failed = [], []


def check(name, cond, detail=""):
    (passed if cond else failed).append(name)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + (f"  | {detail}" if detail else ""))


def req(method, path, body=None, cookies=None, raw_body=None, content_type="application/json"):
    conn = http.client.HTTPConnection(BASE, timeout=10)
    headers = {}
    if cookies:
        headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    if raw_body is not None:
        headers["Content-Type"] = content_type
        conn.request(method, path, raw_body, headers)
    else:
        headers["Content-Type"] = "application/json"
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


def upload_png(cookies, png_data):
    boundary = "----edgeboundary99"
    body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"t.png\"\r\n"
            f"Content-Type: application/octet-stream\r\n\r\n").encode() + png_data + f"\r\n--{boundary}--\r\n".encode()
    conn = http.client.HTTPConnection(BASE, timeout=15)
    conn.request("POST", "/api/upload", body, {
        "Content-Type": f"multipart/form-data; boundary={boundary}",
        "Cookie": "; ".join(f"{k}={v}" for k, v in cookies.items()),
    })
    r = conn.getresponse()
    d = r.read()
    conn.close()
    try:
        jj = json.loads(d)
    except Exception:
        jj = None
    return r.status, jj


def start_server(data_dir):
    env = dict(os.environ)
    env.pop("JWT_SECRET", None)
    env["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"
    p = subprocess.Popen(
        [BIN, f"-port={PORT}", f"-web-dir={WEB}", f"-data-dir={data_dir}",
         f"-upload-dir={os.path.join(data_dir, 'upload')}"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env,
    )
    for _ in range(50):
        try:
            s, _, _, _ = req("GET", "/api/health")
            if s == 200:
                return p
        except Exception:
            pass
        time.sleep(0.2)
    raise RuntimeError("server did not start")


def stop(p):
    p.terminate()
    try:
        p.wait(timeout=5)
    except Exception:
        p.kill()


def register(username, answer="a"):
    s, j, sc, _ = req("POST", "/api/auth/register",
                      {"username": username, "password": "Passw0rd!2026",
                       "security_question": "q", "security_answer": answer})
    return s, sc


PNG_1PX = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")


def main():
    data_dir = tempfile.mkdtemp(prefix="sticky-note-edge-")
    print(f"[setup] data_dir={data_dir}")
    srv = start_server(data_dir)
    try:
        # ---- 准备：用户 A（admin）、用户 B（普通）----
        sA, scA = register("edgeA")
        check("注册 A（首用户=管理员）", sA in (200, 201))
        # v21.133 M-04：新装默认关闭自助注册；本套件需 B/C 普通用户做越权/隔离夹具，
        # 由首管 A 显式开启注册（默认关闭的断言已在冒烟 §2.1 覆盖）。
        sEn, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "true"}, cookies=scA)
        check("A 开启自助注册 allow_register=true → 200", sEn == 200, f"status={sEn}")
        sB, scB = register("edgeB")
        check("注册 B（普通用户）", sB in (200, 201))
        sC, scC = register("edgeC")
        check("注册 C（普通用户，越权测试用）", sC in (200, 201))

        print("== A. 边界情况 ==")
        # 空标题 / 纯空格
        s, _, _, _ = req("POST", "/api/notes", {"title": "", "content": "<p>x</p>"}, cookies=scA)
        check("空标题 400", s == 400, f"status={s}")
        s, _, _, _ = req("POST", "/api/notes", {"title": "   ", "content": "<p>x</p>"}, cookies=scA)
        check("纯空格标题 400", s == 400, f"status={s}")
        # 超长标题（201 字）
        s, _, _, _ = req("POST", "/api/notes", {"title": "长" * 201, "content": "<p>x</p>"}, cookies=scA)
        check("超长标题(201) 400", s == 400, f"status={s}")
        # 超长内容（max_note_content_kb=1）
        req("PUT", "/api/configs/max_note_content_kb", {"value": "1"}, cookies=scA)
        big = "<p>" + "啊" * 500 + "</p>"
        s, _, _, _ = req("POST", "/api/notes", {"title": "超限", "content": big}, cookies=scA)
        check("超长内容 413", s == 413, f"status={s}")
        req("PUT", "/api/configs/max_note_content_kb", {"value": "0"}, cookies=scA)  # 还原
        # emoji / 中文 / 特殊字符
        special = "emoji😀中文標題「引號」<>&\"'"
        s, j, _, _ = req("POST", "/api/notes", {"title": special, "content": "<p>正文😀中文</p>"}, cookies=scA)
        nid = (j or {}).get("id")
        check("特殊字符创建成功", s in (200, 201) and bool(nid), f"status={s}")
        s, j, _, _ = req("GET", f"/api/notes/{nid}", cookies=scA)
        got_title = (j or {}).get("title") or ""
        # v21.105 起标题落库保持原文，不再 html.EscapeString 转义——
        # v21.78~v21.104 的转义语义因「前端把已转义标题原样回传」导致每次保存
        # 追加一层转义（Q&A→Q&amp;A→Q&amp;amp;A…）被废除；XSS 防线移至消费端
        # （前端 {{}} 插值自动转义、导出仅 .md 纯文本）。
        # （开发纪律 4.5：断言随产品行为更新，版本依据 v21.105）
        check("特殊字符存取一致(v21.105原文语义)", got_title == special, got_title[:40])
        # 配额 max_notes=1
        req("PUT", "/api/configs/max_notes_per_user", {"value": "1"}, cookies=scA)
        s1, _, _, _ = req("POST", "/api/notes", {"title": "配额1", "content": "<p>a</p>"}, cookies=scB)
        s2, _, _, _ = req("POST", "/api/notes", {"title": "配额2", "content": "<p>b</p>"}, cookies=scB)
        check("配额=1 第二篇被拒", s1 in (200, 201) and s2 in (403, 400), f"s1={s1} s2={s2}")
        req("PUT", "/api/configs/max_notes_per_user", {"value": "0"}, cookies=scA)  # 还原
        # 分页越界
        s, _, _, _ = req("GET", "/api/notes?page=0", cookies=scA)
        c1 = s in (200,)
        s, _, _, _ = req("GET", "/api/notes?page=999999999", cookies=scA)
        c2 = s in (200,)
        s, _, _, _ = req("GET", "/api/notes?page_size=0", cookies=scA)
        c3 = s in (200,)
        s, _, _, _ = req("GET", "/api/notes?page_size=999999", cookies=scA)
        c4 = s in (200,)
        check("分页越界不崩溃", c1 and c2 and c3 and c4, f"{c1}{c2}{c3}{c4}")

        print("== B. 越权 / 安全 ==")
        # B 创建一篇笔记
        s, jB, _, _ = req("POST", "/api/notes", {"title": "B的笔记", "content": "<p>B</p>"}, cookies=scB)
        nidB = (jB or {}).get("id")
        check("B 建笔记成功", bool(nidB))
        # C（普通用户）访问 B 的笔记 → 404
        s, _, _, _ = req("GET", f"/api/notes/{nidB}", cookies=scC)
        check("C 读 B 的笔记 404", s == 404, f"status={s}")
        s, _, _, _ = req("PUT", f"/api/notes/{nidB}", {"title": "篡改", "content": "<p>x</p>", "version": 1}, cookies=scC)
        check("C 改 B 的笔记 404", s == 404, f"status={s}")
        # B 上传附件，C 访问 → 404（C 非管理员且非 owner）
        s, ju = upload_png(scB, PNG_1PX)
        fname = (ju or {}).get("url", "").replace("/uploads/", "")
        check("B 上传附件成功", s == 200 and fname, f"status={s}")
        s, _, _, _ = req("GET", f"/uploads/{fname}", cookies=scC)
        check("C 访问 B 的附件 404", s == 404, f"status={s}")
        # C 永久删除 B 的回收站笔记 → 404（B 先软删进回收站）
        req("DELETE", f"/api/notes/{nidB}", cookies=scB)
        s, _, _, _ = req("DELETE", f"/api/notes/{nidB}/permanent", cookies=scC)
        check("C 永久删除 B 的笔记 404", s == 404, f"status={s}")
        # 无效 / 负 ID
        s, _, _, _ = req("GET", "/api/notes/99999999", cookies=scA)
        check("无效 ID 404", s == 404, f"status={s}")
        s, _, _, _ = req("GET", "/api/notes/-1", cookies=scA)
        check("负 ID 不 panic", s in (404, 400), f"status={s}")
        # 非法 JSON
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("POST", "/api/notes", b"{not valid json", {"Content-Type": "application/json",
                     "Cookie": "; ".join(f"{k}={v}" for k, v in scA.items())})
        r = conn.getresponse()
        r.read()
        conn.close()
        check("非法 JSON 400", r.status == 400, f"status={r.status}")

        print("== C. 死胡同检查 ==")
        # 忘记密码找回闭环：verify-answer → forgot-password → 新密码登录
        s, _, _, _ = req("POST", "/api/auth/verify-answer",
                         {"username": "edgeB", "security_answer": "a"}, cookies=None)
        check("验证安全答案成功", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/forgot-password",
                         {"username": "edgeB", "security_answer": "a", "new_password": "NewPassw0rd!2026"})
        check("忘记密码重置成功", s == 200, f"status={s}")
        s, _, scNew, _ = req("POST", "/api/auth/login", {"username": "edgeB", "password": "NewPassw0rd!2026"})
        check("新密码登录成功（找回闭环）", s == 200, f"status={s}")
        # 错误答案被拒
        s, _, _, _ = req("POST", "/api/auth/forgot-password",
                         {"username": "edgeB", "security_answer": "wrong", "new_password": "Hack12345"})
        check("错误安全答案被拒", s in (400, 401, 403), f"status={s}")
        # 删除用户彻底清理：A 删除 B → B 无法登录
        # 先查 B 的 id（从注册/登录响应或列表）
        s, j, _, _ = req("GET", "/api/users", cookies=scA)
        users = (j or {}).get("users", []) or []
        bid = next((u.get("id") for u in users if u.get("username") == "edgeB"), None)
        check("能查到 B 的用户 id", bid is not None)
        if bid:
            s, _, _, _ = req("DELETE", f"/api/users/{bid}", cookies=scA)
            check("管理员删除 B 成功", s == 200, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/login", {"username": "edgeB", "password": "NewPassw0rd!2026"})
            check("被删用户无法登录", s in (401, 404, 403), f"status={s}")

    finally:
        stop(srv)

    total = len(passed) + len(failed)
    print(f"\nEDGE RESULT: {len(passed)} passed, {len(failed)} failed  (total {total})")
    if failed:
        print("FAILED:", failed)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
