# -*- coding: utf-8 -*-
"""便签冒烟测试：认证链路 + XSS 消毒 + JWT 持久化 + 双通道 + gzip/缓存头 + 回收站 + FTS5 搜索与导出（v17）+ v18 安全加固 + v18.4 审查修复（12.8/13 节）+ v18.5 删用户清理上传文件（14 节）+ v18.6 认证闭环（15 节）+ v18.7 P1 八项（16 节）+ v18.8 P2 日常项（17 节）+ v18.9 审计日志（18）+ v19.0 MD导出（19）+ v19.1 配额可视化（20）+ v19.3 P3基建（21）+ v20.0 笔记加密（22）+ v21.86 文件夹（23）+ v21.87 笔记移动/复制（24）"""
import os
import http.client, io, json, os, shutil, signal, socket, subprocess, sys, tempfile, time, zipfile, urllib.parse

BIN = sys.argv[1]                      # Windows 测试二进制路径
WEB = sys.argv[2]                      # web dist 目录
PORT = 18904
BASE = f"127.0.0.1:{PORT}"
passed, failed = [], []

def check(name, cond, detail=""):
    (passed if cond else failed).append(name)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + (f"  | {detail}" if detail else ""))

def req(method, path, body=None, cookies=None, raw=False):
    conn = http.client.HTTPConnection(BASE, timeout=10)
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
            if raw:
                setcookies.setdefault("__raw__", []).append(v) if "__raw__" in setcookies else setcookies.update({"__raw__": [v]})
    conn.close()
    try: j = json.loads(data)
    except Exception: j = None
    return resp.status, j, setcookies, data

def start_server(data_dir):
    env = dict(os.environ)
    env.pop("JWT_SECRET", None)  # 确保走持久化文件路径
    env["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"  # v21.19: 冒烟高密度请求会打满单IP配额，关闭全局限流（特性级loginGuard仍测）
    p = subprocess.Popen([BIN, f"-port={PORT}", f"-web-dir={WEB}", f"-data-dir={data_dir}", f"-upload-dir={os.path.join(data_dir,'upload')}"],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env)
    for _ in range(50):
        try:
            s, _, _, _ = req("GET", "/api/health")
            if s == 200: return p
        except Exception: pass
        time.sleep(0.2)
    raise RuntimeError("server did not start")

def stop(p):
    p.terminate()
    try: p.wait(timeout=5)
    except Exception: p.kill()

def _unix_probe_health(sock_path):
    """经 unix socket 发 HTTP GET /api/health（仅 POSIX；网关转发最后一跳等价验证）。"""
    class UnixHTTPConnection(http.client.HTTPConnection):
        def __init__(self, sp):
            super().__init__("localhost", timeout=5)
            self._sp = sp
        def connect(self):
            s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            s.settimeout(5)
            s.connect(self._sp)
            self.sock = s
    conn = UnixHTTPConnection(sock_path)
    try:
        conn.request("GET", "/api/health")
        r = conn.getresponse()
        r.read()
        return r.status
    finally:
        conn.close()

def main():
    data_dir = tempfile.mkdtemp(prefix="sticky-note-smoke-")
    print(f"[setup] data_dir={data_dir}")
    srv = start_server(data_dir)
    try:
        print("== 1. 健康检查 ==")
        s, j, _, _ = req("GET", "/api/health")
        check("health 200", s == 200)
        check("health 含 db 探活字段", isinstance(j, dict) and ("version" in j or "uptime" in j or "database" in j or "status" in j), str(j)[:120])

        print("== 2. 注册与 cookie ==")
        s, j, sc, raw = req("POST", "/api/auth/register", {"username": "smoketest", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        check("注册成功（首个管理员，count==0 恒放行）", s in (200, 201), f"status={s} {raw[:100]}")
        check("下发 access_token cookie", "access_token" in sc)
        check("下发 refresh_token cookie", "refresh_token" in sc)

        # 2.1 v21.133 M-04（审计 Grok-M-04）：新装实例默认不开放自助注册。
        #     首管注册不受开关限制（count==0 恒放行），但其后局域网自助注册默认关闭，
        #     管理员可显式重新开启。此处验证默认值 + 拒绝 + 开启后放行三步闭环。
        s, jpc, _, _ = req("GET", "/api/public-config")
        check("新装默认 allow_register=false（public-config）", (jpc or {}).get("allow_register") == "false", f"j={jpc}")
        s, _, _, _ = req("POST", "/api/auth/register", {"username": "smokeuser_lan", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        check("首管后自助注册默认被拒（403）", s == 403, f"status={s}")
        s, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "true"}, cookies=sc)
        check("管理员显式开启自助注册 allow_register=true → 200", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/register", {"username": "smokeuser_lan", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        check("开启后自助注册恢复放行（201）", s in (200, 201), f"status={s}")

        print("== 3. 认证访问 ==")
        s, j, _, _ = req("GET", "/api/notes", cookies=sc)
        check("带 cookie 访问 200", s == 200)
        s, _, _, _ = req("GET", "/api/notes")
        check("无 cookie 401", s == 401)

        print("== 4. XSS 消毒 ==")
        payload = '<span style="color:red">字色</span><p style="text-align:center">对齐</p><script>alert(1)</script><img src="x" onerror="alert(2)">'
        s, j, _, _ = req("POST", "/api/notes", {"title": "xss", "content": payload}, cookies=sc)
        check("创建笔记成功", s in (200, 201), f"status={s}")
        nid = (j or {}).get("id") or (j or {}).get("note", {}).get("id")
        s, j, _, _ = req("GET", f"/api/notes/{nid}", cookies=sc)
        content = ((j or {}).get("content") or (j or {}).get("note", {}).get("content") or "")
        compact = content.replace(" ", "")
        check("script 被剥除", "<script>" not in content.lower(), content[:80])
        check("onerror 被剥除", "onerror" not in content.lower())
        check("span 字色 style 保留", 'style="color:red"' in compact, content[:80])
        check("p 对齐 style 保留", 'style="text-align:center"' in compact, content[:80])

        print("== 5. JWT 持久化（核心新增）==")
        secret_file = os.path.join(data_dir, ".jwt_secret")
        check(".jwt_secret 已生成", os.path.isfile(secret_file))
        stop(srv)
        srv = start_server(data_dir)  # 重启，同数据目录
        s, j, _, _ = req("GET", "/api/notes", cookies=sc)
        check("重启后旧 cookie 仍有效", s == 200, f"status={s}")

        print("== 6. refresh 与 logout ==")
        s, j, sc2, _ = req("POST", "/api/auth/refresh", cookies={"refresh_token": sc["refresh_token"]})
        check("refresh 换新 access", s == 200 and "access_token" in sc2, f"status={s}")
        s, _, sc3, _ = req("POST", "/api/auth/logout", cookies=sc2 if sc2 else sc)
        check("logout 200", s == 200)
        # v18.2：logout 吊销该用户全部 refresh token（token_version +1）——
        # 即使 cookie 清除在反代/iframe 下失效，残留 refresh 也无法再换出新会话。
        # 注意：这里必须用「登出时仍然有效」的那张票（sc2 的 refresh）来断言，
        # 否则 401 会来自一次性轮转的重放检测，而非登出吊销（语义漂移）。
        s, j, _, _ = req("POST", "/api/auth/refresh", cookies={"refresh_token": sc2["refresh_token"]})
        check("logout 后当前有效 refresh 已吊销（401）", s == 401, f"status={s}")
        # v18.6：登出后旧 access token 也立即失效（中间件比对 token_version），
        # 不再能用到自然过期——登出双保险在 access 层闭环
        s, _, _, _ = req("GET", "/api/notes", cookies=sc)
        check("logout 后旧 access token 立即失效（401，v18.6）", s == 401, f"status={s}")
        s, j, sc6, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("logout 后重新登录正常（吊销不影响后续登录）", s == 200, f"status={s}")
        sc.update(sc6)  # 后续小节使用新会话（旧 access 已随登出失效）

        print("== 6a. refresh 一次性轮转（v21.93 P1 → v21.217 重放宽限期语义）==")
        # v21.90 只是「换新票」（replacement）：旧票在过期前可反复换发。
        # v21.93 起旧票 jti 与库中记录不符即判定重放 → 401 + BumpTokenVersion 全局吊销。
        # v21.217（生产实证·桌面弱网反复掉登录的根治）：已吊销旧票在 **60s 宽限期**内重现
        # 且 family 仍活跃 → 不撤族、续签同族新票返回 200（RFC 9700 / Okta / Auth0 生产
        # 默认同款，救「服务端已轮转但新票未送达客户端」的窗口）；family 已死（登出/改密
        # BumpTokenVersion 全局吊销）→ 维持 401。断言同步该语义：
        #   ① 轮转后立即重放（毫秒级，必在 60s 窗内）→ 200 且续签下发新 refresh；
        #   ② 登出（BumpTokenVersion 撤全端）→ 旧票重放 → 401（安全语义保持）。
        #
        # 必须用**独立登录会话**验证并放在 logout 之后：若混在上一段里，
        # 会连带把 logout 打成 401，断言语义失真。
        s, j, sc_rr, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("轮转用例：独立登录 200", s == 200, f"status={s}")
        s, j, sc_rr2, _ = req("POST", "/api/auth/refresh", cookies={"refresh_token": sc_rr["refresh_token"]})
        check("轮转用例：首次换票 200 且下发新 refresh",
              s == 200 and bool((sc_rr2 or {}).get("refresh_token")), f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/refresh", cookies={"refresh_token": sc_rr["refresh_token"]})
        check("一次性轮转：旧 refresh 60s 宽限期内重放续签（200，v21.217）", s == 200, f"status={s}")
        # 登出（带本会话 cookie → BumpTokenVersion 全局吊销）→ 旧票重放必拒
        s, _, _, _ = req("POST", "/api/auth/logout", cookies=sc_rr)
        check("轮转用例：登出 200", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/refresh", cookies={"refresh_token": sc_rr["refresh_token"]})
        check("撤族后旧 refresh 重放被拒（401）", s == 401, f"status={s}")
        # 登出已全局吊销，重新登录供后续小节使用
        s, j, sc7, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("撤族后仍可重新登录 200", s == 200, f"status={s}")
        sc.update(sc7)

        print("== 6b. 头通道（v13 核心新增）==")
        # 重新登录拿响应体里的双 token
        s, j, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026", "remember": True})
        check("登录响应体含 access_token", s == 200 and isinstance(j, dict) and j.get("access_token"))
        check("登录响应体含 refresh_token", s == 200 and isinstance(j, dict) and j.get("refresh_token"))
        at, rt = j.get("access_token", ""), j.get("refresh_token", "")
        # 完全不用 cookie，只用 Authorization 头
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("GET", "/api/notes", headers={"Authorization": f"Bearer {at}"})
        r = conn.getresponse(); r.read(); conn.close()
        check("仅头通道访问 200（无 cookie）", r.status == 200, f"status={r.status}")
        # body 带 refresh_token 换新（无 cookie）
        s, j2, _, _ = req("POST", "/api/auth/refresh", {"refresh_token": rt})
        check("body refresh 换新（无 cookie）", s == 200 and isinstance(j2, dict) and j2.get("access_token"), f"status={s}")
        new_at = j2.get("access_token", "")
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("GET", "/api/notes", headers={"Authorization": f"Bearer {new_at}"})
        r = conn.getresponse(); r.read(); conn.close()
        check("新 access 头通道可用", r.status == 200)
        # 错误 token 仍拒绝
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("GET", "/api/notes", headers={"Authorization": "Bearer garbage.token.x"})
        r = conn.getresponse(); r.read(); conn.close()
        check("伪造 token 401", r.status == 401)

        print("== 7. SPA 页面 ==")
        s, _, _, body = req("GET", "/")
        text = body.decode("utf-8", errors="replace")
        # v21.96 改名：应用展示名由「便签」改为「Sticky note」（index.html 构建时写入）
        check("首页 200 且标题为 Sticky note", s == 200 and "Sticky note" in text)
        import re as _re
        m = _re.search(r'http-equiv="Content-Security-Policy" content="([^"]+)"', text)
        csp = m.group(1) if m else ""
        check("CSP meta 已注入", bool(csp))
        check("CSP 不含 frame-ancestors/XFO（保 fnOS iframe）",
              "frame-ancestors" not in csp and "X-Frame-Options" not in csp, csp[:60])

        print("== 8. gzip 与缓存头（v15 核心新增）==")
        import glob, gzip as _gz
        # 找体积最大的 JS 主包（小 chunk 只有百余字节，gzip 开销会大于收益，不能作为判据）
        js_files = glob.glob(os.path.join(WEB, "assets", "*.js"))
        js_files.sort(key=lambda f: os.path.getsize(f), reverse=True)
        asset_path = "/assets/" + os.path.basename(js_files[0]) if js_files else None
        check("dist 存在 assets JS 资源", asset_path is not None)

        def raw_get(path, extra_headers=None):
            conn = http.client.HTTPConnection(BASE, timeout=10)
            conn.request("GET", path, headers=extra_headers or {})
            r = conn.getresponse()
            d = r.read()
            hs = {k.lower(): v for k, v in r.getheaders()}
            conn.close()
            return r.status, hs, d

        if asset_path:
            # 8.1 协商 gzip：带 Accept-Encoding 应返回 Content-Encoding: gzip 且体积显著变小
            st1, h1, d1 = raw_get(asset_path, {"Accept-Encoding": "gzip"})
            st2, h2, d2 = raw_get(asset_path)
            check("assets JS 带 Accept-Encoding 返回 gzip", st1 == 200 and h1.get("content-encoding") == "gzip",
                  f"ce={h1.get('content-encoding')}")
            check("gzip 体积显著小于原始", len(d1) < len(d2) * 0.7, f"{len(d1)}B vs {len(d2)}B")
            try:
                plain = _gz.decompress(d2 if h2.get("content-encoding") == "gzip" else d1 if h1.get("content-encoding") == "gzip" else b"")
                check("gzip 内容可正常解压", len(plain) > 1000)
            except Exception as e:
                check("gzip 内容可正常解压", False, str(e))
            # 8.2 缓存头：assets 发一年 immutable
            check("assets 缓存头 immutable 一年", "immutable" in h2.get("cache-control", "") and "31536000" in h2.get("cache-control", ""),
                  h2.get("cache-control", ""))
        # 8.3 index.html / 根路径发 no-cache（根治升级后旧缓存白屏）
        st3, h3, _ = raw_get("/")
        check("根路径 no-cache", "no-cache" in h3.get("cache-control", ""), h3.get("cache-control", ""))

        print("== 9. 回收站（v16 核心新增）==")
        # 建一篇带唯一标签的笔记
        s, j, _, _ = req("POST", "/api/notes", {"title": "回收站测试", "content": "<p>trash flow</p>", "tags": "trashsmoketag"}, cookies=sc)
        check("创建待删笔记", s in (200, 201), f"status={s}")
        tid = (j or {}).get("id")
        # 软删除
        s, _, _, _ = req("DELETE", f"/api/notes/{tid}", cookies=sc)
        check("删除（软删）返回 200", s == 200)
        # 正常列表不可见
        s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("删除后正常列表不可见", tid not in ids)
        # 详情 404
        s, _, _, _ = req("GET", f"/api/notes/{tid}", cookies=sc)
        check("删除后详情 404", s == 404, f"status={s}")
        # 标签列表不再含该唯一标签
        s, j, _, _ = req("GET", "/api/notes/tags", cookies=sc)
        check("删除后标签列表已过滤", "trashsmoketag" not in ((j or {}).get("tags") or []))
        # 回收站可见且带删除时间
        s, j, _, _ = req("GET", "/api/trash/notes", cookies=sc)
        trash = (j or {}).get("notes", [])
        hit = [n for n in trash if n.get("id") == tid]
        check("回收站列表可见", len(hit) == 1)
        check("回收站条目带 deleted_at", bool(hit and hit[0].get("deleted_at")))
        # 恢复
        s, _, _, _ = req("POST", f"/api/notes/{tid}/restore", cookies=sc)
        check("恢复返回 200", s == 200)
        s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("恢复后正常列表可见", tid in ids)
        s, j, _, _ = req("GET", "/api/trash/notes", cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("恢复后回收站不再可见", tid not in ids)
        # 再删一次后彻底删除
        req("DELETE", f"/api/notes/{tid}", cookies=sc)
        s, _, _, _ = req("DELETE", f"/api/notes/{tid}/permanent", cookies=sc)
        check("彻底删除返回 200", s == 200)
        s, j, _, _ = req("GET", "/api/trash/notes", cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("彻底删除后回收站清空", tid not in ids)
        # 对正常笔记执行彻底删除应被拒绝（防误伤）
        s, j, _, _ = req("POST", "/api/notes", {"title": "保护测试", "content": "<p>keep</p>"}, cookies=sc)
        kid = (j or {}).get("id")
        s, _, _, _ = req("DELETE", f"/api/notes/{kid}/permanent", cookies=sc)
        check("正常笔记不可被彻底删除（404）", s == 404, f"status={s}")
        s, j, _, _ = req("GET", f"/api/notes/{kid}", cookies=sc)
        check("正常笔记仍在", s == 200)

        print("== 10. 图片宽度属性（v16.5 缩放功能新增）==")
        # 图片缩放后序列化为 <img width="N">，后端消毒链必须保留该属性
        s, j, _, _ = req("POST", "/api/notes", {"title": "图片宽度", "content": '<p>图</p><img src="/uploads/test.png" width="320" alt="图片" />'}, cookies=sc)
        check("创建含 width 图片笔记", s in (200, 201), f"status={s}")
        wid = (j or {}).get("id")
        s, j, _, _ = req("GET", f"/api/notes/{wid}", cookies=sc)
        wcontent = ((j or {}).get("content") or "").replace(" ", "")
        check("img width 入库保留（后端消毒不剥）", 'width="320"' in wcontent, wcontent[:100])

        print("== 11. FTS5 全文搜索与笔记导出（v17 核心新增）==")
        # 11.1 基础设施：虚表 / 触发器 / content_text 列
        import sqlite3 as _sq
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")]
        trigs = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='trigger'")]
        check("notes_fts 虚表已建", "notes_fts" in tables, str(tables))
        check("FTS5 三个同步触发器已建", sorted(trigs) == ["notes_fts_ad", "notes_fts_ai", "notes_fts_au"], str(trigs))
        cols = [r[1] for r in db.execute("PRAGMA table_info(notes)")]
        check("notes.content_text 列存在", "content_text" in cols)
        db.close()

        # 11.2 造两篇含独特术语的笔记
        from urllib.parse import quote as _q
        s, j, _, _ = req("POST", "/api/notes", {"title": "量子计算机原理笔记", "content": "<p>量子纠缠与叠加态是核心概念</p>", "tags": "科技"}, cookies=sc)
        f1 = (j or {}).get("id")
        check("建 FTS 笔记1", s in (200, 201) and bool(f1), f"status={s}")
        s, j, _, _ = req("POST", "/api/notes", {"title": "红烧狮子头食谱", "content": "<p>五花肉剁馅加马蹄，文火慢炖两小时</p>", "tags": "美食"}, cookies=sc)
        f2 = (j or {}).get("id")
        check("建 FTS 笔记2", s in (200, 201) and bool(f2), f"status={s}")

        # 11.3 ≥3 字走 FTS5：正文命中唯一篇 / 标题命中不串篇
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("量子纠缠"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("FTS5 正文 4 字命中唯一篇", ids == [f1], f"ids={ids}")
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("红烧狮子头"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("FTS5 标题命中且不串篇", f2 in ids and f1 not in ids, f"ids={ids}")
        # 11.4 <3 字回退 LIKE（trigram 固有限制，非 bug）
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("纠缠"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("<3 字回退 LIKE 可命中", f1 in ids, f"ids={ids}")
        # 11.5 更新后索引同步（AFTER UPDATE 触发器）
        s, _, _, _ = req("PUT", f"/api/notes/{f1}", {"title": "量子计算机原理笔记", "content": "<p>已改写：拓扑量子比特与纠错</p>", "tags": "科技", "version": 1}, cookies=sc)
        check("更新 FTS 笔记1", s == 200, f"status={s}")
        # v21.75：连续二次更新同笔记（version=2 应成功，锁定「前端 version 未同步导致 409」回归）
        s, _, _, _ = req("PUT", f"/api/notes/{f1}", {"title": "量子计算机原理笔记", "content": "<p>已改写：拓扑量子比特与纠错（二次）</p>", "tags": "科技", "version": 2}, cookies=sc)
        check("连续二次更新同笔记（version=2 成功）", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("拓扑量子"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("更新后新词可命中", f1 in ids, f"ids={ids}")
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("量子纠缠"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("更新后旧词不再命中", f1 not in ids, f"ids={ids}")
        # 11.6 软删除不进搜索结果，恢复后可见
        req("DELETE", f"/api/notes/{f1}", cookies=sc)
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("拓扑量子"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("软删后搜索不可见", f1 not in ids, f"ids={ids}")
        req("POST", f"/api/notes/{f1}/restore", cookies=sc)
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("拓扑量子"), cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("恢复后搜索可见", f1 in ids, f"ids={ids}")

        # 11.7 导出单篇：纯文本 + 中文文件名头
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("GET", f"/api/notes/{f2}/export", headers={"Cookie": "; ".join(f"{k}={v}" for k, v in sc.items())})
        r = conn.getresponse()
        raw = r.read()
        hs = {k.lower(): v for k, v in r.getheaders()}
        conn.close()
        check("导出单篇 200 且 text/markdown（v19.4 仅 MD）", r.status == 200 and "text/markdown" in hs.get("content-type", ""), f"status={r.status}")
        cd = hs.get("content-disposition", "")
        check("导出头含 filename* 中文名", "filename*=UTF-8''" in cd and "%E7%BA%A2%E7%83%A7" in cd, cd[:90])
        text = raw.decode("utf-8-sig")
        check("导出为纯文本无 HTML 标签", "五花肉" in text and "<p>" not in text, text[:60])

        # 11.8 导出全部 zip：条数与正常笔记一致（回收站不导出）
        s, j, _, raw = req("GET", "/api/export/notes", cookies=sc)
        check("导出全部 200", s == 200, f"status={s}")
        import io as _io, zipfile as _zf
        zf = _zf.ZipFile(_io.BytesIO(raw))
        names = zf.namelist()
        check("zip 条目全为 .md", len(names) > 0 and all(n.endswith(".md") for n in names), str(names)[:110])
        s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
        total_normal = (j or {}).get("total")
        check("zip 条数与正常笔记数一致", len(names) == total_normal, f"{len(names)} vs {total_normal}")
        # zip 内容抽查：食谱条目为纯文本
        hit = [n for n in names if "红烧狮子头食谱" in n]
        body = zf.read(hit[0]).decode("utf-8-sig") if hit else ""
        check("zip 内条目内容纯文本", "五花肉" in body and "<p>" not in body, (hit or ["<none>"])[0])

        # 11.9 zip 内重名加序号
        req("POST", "/api/notes", {"title": "重名笔记", "content": "<p>A</p>"}, cookies=sc)
        req("POST", "/api/notes", {"title": "重名笔记", "content": "<p>B</p>"}, cookies=sc)
        s, j, _, raw = req("GET", "/api/export/notes", cookies=sc)
        check("导出全部(重名) 200", s == 200, f"status={s}")
        if s != 200 or not raw[:2] == b'PK':
            check("zip 内重名加序号", False, f"非 zip 响应: status={s} raw[:20]={raw[:20]}")
        else:
            zf = _zf.ZipFile(_io.BytesIO(raw))
            dup = [n for n in zf.namelist() if n.startswith("重名笔记")]
            check("zip 内重名加序号", sorted(dup) == ["重名笔记(2).md", "重名笔记.md"], str(dup))

        # 11.10 未认证导出被拒绝
        s, _, _, _ = req("GET", f"/api/notes/{f2}/export")
        check("未认证导出单篇 401", s == 401, f"status={s}")
        s, _, _, _ = req("GET", "/api/export/notes")
        check("未认证导出全部 401", s == 401, f"status={s}")
        # 11.11 跨用户导出他人笔记 404（数据隔离）
        s, j, sc_b, _ = req("POST", "/api/auth/register", {"username": "smokeuserb", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        s, _, _, _ = req("GET", f"/api/notes/{f2}/export", cookies=sc_b)
        check("跨用户导出他人笔记 404", s == 404, f"status={s}")

        print("== 12. v18 安全加固（防爆破/回收站永久/cookie 自动/上传/CORS）==")
        # 12.1 cookie Secure 自动模式：未设 COOKIE_SECURE 时 HTTP 登录 cookie 不带 Secure
        s, j, sc_raw, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"}, raw=True)
        at_raw = [c for c in sc_raw.get("__raw__", []) if c.startswith("access_token=")]
        check("自动模式 HTTP 登录 cookie 无 Secure 属性", s == 200 and at_raw and "; secure" not in at_raw[0].lower(), at_raw[0][:80] if at_raw else "no cookie")

        # 12.2 上传加固：magic number 交叉校验
        import base64 as _b64
        def upload_raw(filename, data, cookies):
            import threading as _th
            boundary = "----smokeboundary42"
            body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\nContent-Type: application/octet-stream\r\n\r\n").encode() + data + f"\r\n--{boundary}--\r\n".encode()
            conn = http.client.HTTPConnection(BASE, timeout=15)
            conn.connect()
            conn.putrequest("POST", "/api/upload")
            conn.putheader("Content-Type", f"multipart/form-data; boundary={boundary}")
            conn.putheader("Content-Length", str(len(body)))
            conn.putheader("Cookie", "; ".join(f"{k}={v}" for k, v in cookies.items()))
            conn.endheaders()
            # v21.126 CI §27 flaky 根治：reader 线程并发读响应。
            # 服务端对超限 body 合法地"回错误响应后带未读数据关连接"→ RST 会冲掉客户端
            # 接收缓冲（Windows 必现、Linux 竞态），顺序整写/select 停写都赢不了该竞态
            # （CI 偶发 BrokenPipe traceback 红即此）。reader 线程在响应到达瞬间读取
            # （全双工、先于 RST），写线程见到结果即停写；服务端未发响应即关闭时
            # status=0，断言以 FAIL 呈现而非 traceback。产品代码零改动。
            result = {}
            def _reader():
                try:
                    r = conn.getresponse()
                    result["status"], result["body"] = r.status, r.read()
                except Exception as e:
                    result["err"] = e
            sock = conn.sock
            th = _th.Thread(target=_reader, daemon=True)
            th.start()
            mv = memoryview(body)
            off, CHUNK = 0, 1024 * 1024
            while off < len(body):
                if "status" in result or "err" in result:
                    break
                try:
                    n = sock.send(mv[off:off + CHUNK])
                except (BrokenPipeError, ConnectionResetError, OSError):
                    break
                off += n
            th.join(timeout=15)
            status, d = result.get("status", 0), result.get("body", b"")
            conn.close()
            try:
                jj = json.loads(d)
            except Exception:
                jj = None
            return status, jj
        png_1px = _b64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")
        s, j = upload_raw("evil.jpg", b"<script>alert(1)</script>", sc)
        check("伪装图片的脚本文件被拒（magic number）", s == 400, f"status={s} {j}")
        s, j = upload_raw("real.png", png_1px, sc)
        check("真实 PNG 上传成功", s == 200 and isinstance(j, dict) and str(j.get("url", "")).startswith("/uploads/"), f"status={s} {j}")

        # 12.3 CORS（v2.1.5 范式修正）：跨域不再 403，改为不下发 ACAO（浏览器拦读取）；
        # 凭据边界由 SameSite=Lax + Bearer 保证。同源正常。
        st, h, _ = raw_get("/api/health", {"Origin": "https://evil.example.com"})
        check("跨域简单请求不下发 ACAO", "access-control-allow-origin" not in h, str(h.get("access-control-allow-origin")))
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("OPTIONS", "/api/notes", headers={"Origin": "https://evil.example.com", "Access-Control-Request-Method": "POST"})
        r = conn.getresponse(); r.read(); conn.close()
        # v2.1.5：预检不再 403（过度防御会误杀 Host 被网关改写的同源请求）；
        # 非白名单预检无路由时 404、无 ACAO——浏览器仍拦读取，跨站语义不变。
        check("跨域预检不再 403 且不下发 ACAO（v2.1.5）", r.status != 403, f"status={r.status}")
        st, h, _ = raw_get("/api/health", {"Origin": f"http://{BASE}"})
        check("同源请求正常", st == 200, f"status={st}")

        # 12.4 CORS 白名单（后台配置驱动，5s 缓存需等待过期）
        s, _, _, _ = req("PUT", "/api/configs/cors_origins", {"value": "https://evil.example.com"}, cookies=sc)
        check("后台配置 cors_origins", s == 200, f"status={s}")
        time.sleep(5.5)
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("OPTIONS", "/api/notes", headers={"Origin": "https://evil.example.com", "Access-Control-Request-Method": "POST"})
        r = conn.getresponse(); r.read()
        hs = {k.lower(): v for k, v in r.getheaders()}
        conn.close()
        check("白名单来源预检 204 且反射 Origin", r.status == 204 and hs.get("access-control-allow-origin") == "https://evil.example.com", f"status={r.status} acao={hs.get('access-control-allow-origin')}")
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("OPTIONS", "/api/notes", headers={"Origin": "https://other.example.com", "Access-Control-Request-Method": "POST"})
        r = conn.getresponse(); r.read()
        hs2 = {k.lower(): v for k, v in r.getheaders()}
        conn.close()
        # v2.1.5：非白名单不再 403，但不下发 ACAO——跨站读取仍被浏览器拦截。
        check("非白名单来源不再 403 且不下发 ACAO（v2.1.5）",
              r.status != 403 and "access-control-allow-origin" not in hs2,
              f"status={r.status} acao={hs2.get('access-control-allow-origin')}")
        req("PUT", "/api/configs/cors_origins", {"value": "*"}, cookies=sc)
        time.sleep(5.5)
        conn = http.client.HTTPConnection(BASE, timeout=10)
        conn.request("OPTIONS", "/api/notes", headers={"Origin": "https://any.example.com", "Access-Control-Request-Method": "POST"})
        r = conn.getresponse(); r.read()
        hs = {k.lower(): v for k, v in r.getheaders()}
        conn.close()
        # v21.52 P10：cors_origins=* 不生效（防"任意来源+凭据"）；v2.1.5 起表现为
        # 不反射 ACAO（而非 403）——跨站读取语义不变。
        check("cors_origins=* 不生效（不反射 ACAO，v2.1.5 口径）",
              "access-control-allow-origin" not in hs,
              f"status={r.status} acao={hs.get('access-control-allow-origin')}")
        req("PUT", "/api/configs/cors_origins", {"value": ""}, cookies=sc)  # 还原默认

        # 12.4a 注册开关（v20.8 曾因白名单幽灵键 register_enabled 导致更新失败）
        s, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "false"}, cookies=sc)
        check("更新 allow_register=false → 200", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/public-config", cookies=sc)
        check("public-config 反映 allow_register=false", (j or {}).get("allow_register") == "false", f"j={j}")
        s, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "true"}, cookies=sc)
        check("更新 allow_register=true → 200", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/public-config", cookies=sc)
        check("public-config 反映 allow_register=true", (j or {}).get("allow_register") == "true", f"j={j}")

        # 12.5 回收站永久：篡改删除时间为 40 天前，重启后仍保留
        s, j, _, _ = req("POST", "/api/notes", {"title": "永久保留测试", "content": "<p>keep forever</p>"}, cookies=sc)
        pid = (j or {}).get("id")
        req("DELETE", f"/api/notes/{pid}", cookies=sc)
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        db.execute("UPDATE notes SET deleted_at = datetime('now', '-40 days') WHERE id = ?", (pid,))
        db.commit(); db.close()
        stop(srv)
        srv = start_server(data_dir)  # v18 起重启不再自动清理回收站
        s, j, _, _ = req("GET", "/api/trash/notes", cookies=sc)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("40 天前删除的笔记重启后仍在回收站（永久保留）", pid in ids, f"ids={ids}")

        # 12.6 env 组合重启：显式 COOKIE_SECURE / 短锁定 / 小上传上限
        stop(srv)
        os.environ["COOKIE_SECURE"] = "true"
        os.environ["LOGIN_LOCK_SECONDS"] = "2"
        os.environ["UPLOAD_MAX_MB"] = "1"
        srv = start_server(data_dir)
        try:
            s, j, sc_raw, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"}, raw=True)
            check("显式 secure + HTTP 登录响应带 warning", s == 200 and isinstance(j, dict) and bool(j.get("warning")), str(j)[:100])
            at_raw = [c for c in sc_raw.get("__raw__", []) if c.startswith("access_token=")]
            check("显式 secure 时 cookie 带 Secure 属性", bool(at_raw) and "; secure" in at_raw[0].lower(), at_raw[0][:80] if at_raw else "no cookie")
            big = b"\xff\xd8\xff\xe0" + b"A" * (1100 * 1024)
            s, j = upload_raw("big.jpg", big, sc)
            check("超限文件被大小上限拒绝", s == 400, f"status={s} {j}")
            for _ in range(5):
                s, _, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "wrong-pass-xx"})
            check("连续失败 5 次均 401", s == 401, f"status={s}")
            s, j, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
            check("锁定中正确密码也 429", s == 429, f"status={s}")
            time.sleep(2.5)
            s, j, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
            check("锁定期满自动解锁可登录", s == 200, f"status={s}")

            # 12.7 找回密码防爆破（v18.3）：verify-answer / forgot-password / security-question
            # 与登录共用同一 guard。注意上个用例刚成功登录已清零用户维度记录，此处重新累计。
            for _ in range(5):
                s, _, _, _ = req("POST", "/api/auth/verify-answer", {"username": "smoketest", "security_answer": "wrong-answer"})
            check("找回密码：连续错误答案 5 次均 400", s == 400, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/verify-answer", {"username": "smoketest", "security_answer": "a"})
            check("找回密码：锁定中正确答案也 429", s == 429, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/forgot-password", {"username": "smoketest", "security_answer": "a", "new_password": "NewPassw0rd!2026"})
            check("找回密码：锁定中重置接口也 429", s == 429, f"status={s}")
            s, _, _, _ = req("GET", "/api/auth/security-question?username=smoketest")
            check("找回密码：锁定中查安全问题也 429", s == 429, f"status={s}")
            time.sleep(2.5)
            s, _, _, _ = req("POST", "/api/auth/verify-answer", {"username": "smoketest", "security_answer": "a"})
            check("找回密码：解锁后正确答案通过", s == 200, f"status={s}")

            # 12.8 注册防爆破（v18.4）：注册接入同一 loginGuard，用户名冲突（400）计失败
            for _ in range(5):
                s, _, _, _ = req("POST", "/api/auth/register", {"username": "smokeuserb", "password": "Passw0rd!2026"})
            check("注册：重复占用名 5 次均 400", s == 400, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/register", {"username": "smokeuserb", "password": "Passw0rd!2026"})
            check("注册：第 6 次被限流 429", s == 429, f"status={s}")
            s, j, _, _ = req("POST", "/api/auth/register", {"username": "smokeuserc", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
            check("注册：锁定期间新用户名不受影响", s in (200, 201), f"status={s}")
            time.sleep(2.5)
            s, _, _, _ = req("POST", "/api/auth/register", {"username": "smokeuserb", "password": "Passw0rd!2026"})
            check("注册：解锁后恢复 400（非 429）", s == 400, f"status={s}")
        finally:
            for k in ("COOKIE_SECURE", "LOGIN_LOCK_SECONDS", "UPLOAD_MAX_MB"):
                os.environ.pop(k, None)

        print("== 13. v18.4 审查修复（配额/唯一管理员/role 校验/搜索正文/404 语义）==")
        # 13.1 每用户笔记配额落地：configs.max_notes_per_user 真正生效，回收站不占配额
        s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
        cur_total = (j or {}).get("total", 0)
        s, _, _, _ = req("PUT", "/api/configs/max_notes_per_user", {"value": str(cur_total + 1)}, cookies=sc)
        check("后台配置笔记配额", s == 200, f"status={s}")
        s, j, _, _ = req("POST", "/api/notes", {"title": "配额边界笔记", "content": "<p>quota</p>"}, cookies=sc)
        check("限额内创建成功（回收站已有笔记，不占配额）", s in (200, 201), f"status={s}")
        quota_nid = (j or {}).get("id")
        s, j, _, _ = req("POST", "/api/notes", {"title": "超额笔记", "content": "<p>over</p>"}, cookies=sc)
        check("超额创建被 403 拒绝", s == 403, f"status={s} {j}")
        req("PUT", "/api/configs/max_notes_per_user", {"value": "1000"}, cookies=sc)  # 还原默认
        req("DELETE", f"/api/notes/{quota_nid}", cookies=sc)  # 清场

        # 13.2 唯一管理员约束两个入口统一：UpdateUser 不可提升第二 admin（v18.4 堵上）
        s, j, _, _ = req("GET", "/api/users", cookies=sc)
        users = (j or {}).get("users", [])
        uid_b = next((u.get("id") for u in users if u.get("username") == "smokeuserb"), None)
        check("用户列表可查到 smokeuserb", bool(uid_b))
        s, j, _, _ = req("PUT", f"/api/users/{uid_b}", {"role": "admin"}, cookies=sc)
        check("提升普通用户为第二管理员被拒（400）", s == 400, f"status={s} {j}")
        uid_admin = next((u.get("id") for u in users if u.get("username") == "smoketest"), None)
        s, _, _, _ = req("PUT", f"/api/users/{uid_admin}", {"role": "admin"}, cookies=sc)
        check("现有管理员保持 admin（幂等 200）", s == 200, f"status={s}")

        # 13.3 createUser 脏 role 校验（v18.4）
        s, j, _, _ = req("POST", "/api/users", {"username": "dirtyrole1", "password": "Passw0rd!2026", "role": "superroot"}, cookies=sc)
        check("脏角色串建用户被 400 拒绝", s == 400, f"status={s} {j}")

        # 13.4 短关键词搜索只扫正文文本（v18.4）：HTML 标签/属性不再误命中
        s, j, _, _ = req("POST", "/api/notes", {"title": "搜索边界笔记", "content": '<p>纯文本内容</p><img src="/uploads/boundary.png" width="100" />'}, cookies=sc)
        search_nid = (j or {}).get("id")
        check("建含图搜索测试笔记", s in (200, 201), f"status={s}")
        s, j, _, _ = req("GET", "/api/notes?q=im", cookies=sc)  # 2 字走 LIKE 分支
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("<3 字 LIKE 不命中 HTML 标签（<img 的 im）", search_nid not in ids and len(ids) == 0, f"ids={ids}")
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("纯文本"), cookies=sc)  # 3 字走 FTS5
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("FTS5 正文命中正常", search_nid in ids, f"ids={ids}")
        # P2-2 搜索高亮：FTS 命中返回 highlight 片段（<mark> 包裹命中词）
        hl = next((n.get("highlight", "") for n in (j or {}).get("notes", []) if n.get("id") == search_nid), "")
        check("FTS5 搜索返回高亮片段", "<mark>纯文本</mark>" in hl, f"highlight={hl!r}")
        s, j, _, _ = req("GET", "/api/notes?q=" + _q("纯文"), cookies=sc)  # 2 字 LIKE 正向命中
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("LIKE 回退命中 content_text 正常", search_nid in ids, f"ids={ids}")
        # P2-2：LIKE 回退分支不返回 highlight（无 FTS snippet）
        hl = next((n.get("highlight", "") for n in (j or {}).get("notes", []) if n.get("id") == search_nid), None)
        check("LIKE 回退分支无 highlight", hl is None or hl == "", f"highlight={hl!r}")

        # 13.5 404 语义（v18.4）：更新/删除不存在或已在回收站的笔记不再假 200
        s, _, _, _ = req("PUT", "/api/notes/99999999", {"title": "x", "content": "<p>x</p>"}, cookies=sc)
        check("更新不存在笔记 404", s == 404, f"status={s}")
        s, _, _, _ = req("DELETE", "/api/notes/99999999", cookies=sc)
        check("删除不存在笔记 404", s == 404, f"status={s}")
        s, _, _, _ = req("DELETE", f"/api/notes/{pid}", cookies=sc)
        check("重复删除回收站笔记 404", s == 404, f"status={s}")
        s, _, _, _ = req("PUT", f"/api/notes/{pid}", {"title": "x", "content": "<p>x</p>"}, cookies=sc)
        check("更新回收站笔记 404", s == 404, f"status={s}")

        print("== 14. v18.5 删用户清理上传文件（uploads 跟踪表 + 启动回填/激进清理）==")
        # 14.1 用户X上传两个文件：一个插入自己笔记（且被管理员笔记跨用户引用），一个仅上传
        s, j, sc_x, _ = req("POST", "/api/auth/register", {"username": "smokeuserx", "password": "Passw0rd!2026", "security_question": "q", "security_answer": "a"})
        check("注册用户X", s in (200, 201), f"status={s}")
        s, j = upload_raw("x1.png", png_1px, sc_x)
        fx1 = (j or {}).get("url", "").replace("/uploads/", "")
        check("用户X上传文件1", s == 200 and fx1, f"status={s}")
        s, j = upload_raw("x2.png", png_1px, sc_x)
        fx2 = (j or {}).get("url", "").replace("/uploads/", "")
        check("用户X上传文件2", s == 200 and fx2, f"status={s}")
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        cnt = db.execute("SELECT COUNT(*) FROM uploads WHERE filename IN (?, ?)", (fx1, fx2)).fetchone()[0]
        check("上传归属实时落库（2 条）", cnt == 2, f"cnt={cnt}")
        db.close()
        st0, _, h0, _ = req("GET", f"/uploads/{fx1}", cookies=sc_x, raw=True)
        check("上传后文件受保护可访问（需登录，200 且为图片）", st0 == 200, f"status={st0}")
        st0a, _, _ = raw_get(f"/uploads/{fx1}")
        check("上传文件未登录拒绝访问（非200）", st0a == 401, f"status={st0a}")
        s, j, _, _ = req("POST", "/api/notes", {"title": "X的含图笔记", "content": f'<p>x</p><img src="/uploads/{fx1}" />'}, cookies=sc_x)
        check("用户X建含图笔记", s in (200, 201), f"status={s}")
        s, j, _, _ = req("POST", "/api/notes", {"title": "管理员引用X的图", "content": f'<p>admin ref</p><img src="/uploads/{fx1}" />'}, cookies=sc)
        check("管理员笔记跨用户引用X的文件", s in (200, 201), f"status={s}")

        # 14.2 删除用户X：全部上传文件无条件物理删除（不保护跨用户引用，用户已确认语义）
        s, j, _, _ = req("GET", "/api/users", cookies=sc)
        uid_x = next((u.get("id") for u in (j or {}).get("users", []) if u.get("username") == "smokeuserx"), None)
        s, j, _, _ = req("DELETE", f"/api/users/{uid_x}", cookies=sc)
        check("删除用户X返回清理计数 uploads_cleaned=2", s == 200 and (j or {}).get("uploads_cleaned") == 2, f"status={s} {j}")
        st1, _, _, _ = req("GET", f"/uploads/{fx1}", cookies=sc, raw=True)
        check("被他人引用文件仍随属主删除（不保护，404）", st1 == 404, f"status={st1}")
        st2, _, _, _ = req("GET", f"/uploads/{fx2}", cookies=sc, raw=True)
        check("仅上传未引用文件随属主删除（404）", st2 == 404, f"status={st2}")
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        cnt = db.execute("SELECT COUNT(*) FROM uploads WHERE user_id = ?", (uid_x,)).fetchone()[0]
        check("uploads 记录随用户 CASCADE 自清", cnt == 0, f"cnt={cnt}")
        cnt = db.execute("SELECT COUNT(*) FROM notes WHERE user_id = ?", (uid_x,)).fetchone()[0]
        check("用户X笔记随 CASCADE 删除", cnt == 0, f"cnt={cnt}")
        db.close()

        # 14.3 启动回填 + 激进清理（模拟 v18.5 之前的存量库：重置旗标触发重启执行）
        stop(srv)
        updir = os.path.join(data_dir, "upload")
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        db.execute("INSERT INTO notes (user_id, title, content, content_text) VALUES (1, '存量笔记', '<p>old</p><img src=\"/uploads/legacy-keep.png\" />', 'old')")
        db.execute("DELETE FROM configs WHERE key = 'uploads_backfilled'")
        db.commit(); db.close()
        for name in ("legacy-keep.png", "legacy-orphan.png", "fresh-inflight.png"):
            with open(os.path.join(updir, name), "wb") as f:
                f.write(png_1px)
        old_ts = time.time() - 48 * 3600  # 超 24h 宽限期
        os.utime(os.path.join(updir, "legacy-keep.png"), (old_ts, old_ts))
        os.utime(os.path.join(updir, "legacy-orphan.png"), (old_ts, old_ts))
        # fresh-inflight.png 保持当前 mtime（宽限期内，模拟刚上传未保存的在途文件）
        srv = start_server(data_dir)
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        row = db.execute("SELECT user_id FROM uploads WHERE filename = 'legacy-keep.png'").fetchone()
        check("启动回填：被引用旧文件按笔记属主落库", row is not None and row[0] == 1, str(row))
        check("激进清理：无归属超期旧文件被删除", not os.path.exists(os.path.join(updir, "legacy-orphan.png")))
        check("宽限期内新文件保留（防误删在途上传）", os.path.exists(os.path.join(updir, "fresh-inflight.png")))
        flag = db.execute("SELECT value FROM configs WHERE key='uploads_backfilled'").fetchone()
        check("回填旗标已写入（一次性）", flag is not None and flag[0] == "true", str(flag))
        db.close()

        print("== 15. v18.6 认证闭环（XFF 伪造收敛 / access 版本校验 / 算法限定）==")
        # 15.1 改密后旧 access 立即失效（用普通用户 smokeuserc，不污染主账号流程）
        s, j, sc_c, _ = req("POST", "/api/auth/login", {"username": "smokeuserc", "password": "Passw0rd!2026"})
        check("用户C 登录", s == 200, f"status={s}")
        s, _, _, _ = req("GET", "/api/notes", cookies=sc_c)
        check("改密前 access 可用", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/change-password", {"current_password": "Passw0rd!2026", "new_password": "NewPassw0rd!y"}, cookies=sc_c)
        check("修改密码 200", s == 200, f"status={s}")
        s, _, _, _ = req("GET", "/api/notes", cookies=sc_c)
        check("改密后旧 access 立即失效（401）", s == 401, f"status={s}")
        s, j, sc_c2, _ = req("POST", "/api/auth/login", {"username": "smokeuserc", "password": "NewPassw0rd!y"})
        check("新密码可登录", s == 200, f"status={s}")
        req("POST", "/api/auth/change-password", {"current_password": "NewPassw0rd!y", "new_password": "Passw0rd!2026"}, cookies=sc_c2)  # 还原

        # 15.2 JWT 算法限定 HS256：用持久化密钥自签 HS384 token 必须被拒
        import hmac as _hmac, hashlib as _hl, base64 as _b642
        def b64u(b): return _b642.urlsafe_b64encode(b).rstrip(b"=")
        with open(os.path.join(data_dir, ".jwt_secret")) as f:
            secret = f.read().strip().encode()
        db = _sq.connect(os.path.join(data_dir, "notepad.db"))
        cur_ver = db.execute("SELECT token_version FROM users WHERE username='smoketest'").fetchone()[0]
        db.close()
        hdr = b64u(json.dumps({"alg": "HS384", "typ": "JWT"}).encode())
        pld = b64u(json.dumps({"user_id": 1, "username": "smoketest", "role": "admin", "type": "access", "ver": cur_ver, "exp": int(time.time()) + 3600}).encode())
        sig = b64u(_hmac.new(secret, hdr + b"." + pld, _hl.sha384).digest())
        tok384 = (hdr + b"." + pld + b"." + sig).decode()
        s, _, _, _ = req("GET", "/api/notes", cookies={"access_token": tok384})
        check("HS384 异构算法 token 被拒（401）", s == 401, f"status={s}")

        # 15.3 XFF 伪造收敛（v18.6 核心）：TRUSTED_PROXIES=none 时伪造 XFF 不再改变限流维度
        def login_xff(user, pwd, xff):
            conn = http.client.HTTPConnection(BASE, timeout=10)
            conn.request("POST", "/api/auth/login", json.dumps({"username": user, "password": pwd}),
                         {"Content-Type": "application/json", "X-Forwarded-For": xff})
            r = conn.getresponse(); r.read(); conn.close()
            return r.status
        stop(srv)
        os.environ["TRUSTED_PROXIES"] = "none"
        os.environ["LOGIN_LOCK_SECONDS"] = "2"
        srv = start_server(data_dir)
        try:
            for i in range(5):
                s = login_xff("smoketest", "wrong-pass-xx", f"203.0.113.{i + 1}")  # 每次伪造不同 XFF
            check("收紧模式：伪造 XFF 连续失败 5 次均 401", s == 401, f"status={s}")
            s = login_xff("smoketest", "Passw0rd!2026", "203.0.113.99")
            check("收紧模式：换 XFF 也绕过不了（RemoteAddr 维度锁定 429）", s == 429, f"status={s}")
            time.sleep(2.5)
            s = login_xff("smoketest", "Passw0rd!2026", "203.0.113.99")
            check("收紧模式：解锁后正常登录", s == 200, f"status={s}")
        finally:
            stop(srv)
            os.environ.pop("TRUSTED_PROXIES", None)

        # 15.4 默认模式（信任本机回环）：经本机代理时按 XFF 客户端 IP 分桶
        srv = start_server(data_dir)
        for _ in range(5):
            s = login_xff("smoketest", "wrong-pass-xx", "198.51.100.7")  # 同一 XFF
        check("默认模式：同一 XFF 客户端 5 次失败均 401", s == 401, f"status={s}")
        s = login_xff("smoketest", "Passw0rd!2026", "198.51.100.7")
        check("默认模式：该 XFF 客户端被锁定（429）", s == 429, f"status={s}")
        s = login_xff("smoketest", "Passw0rd!2026", "198.51.100.8")
        check("默认模式：换 XFF 的新客户端不受影响（按 XFF 分桶）", s == 200, f"status={s}")
        time.sleep(2.5)
        login_xff("smoketest", "Passw0rd!2026", "198.51.100.7")  # 解锁并清零

        print("== 16. v18.7 安全加固（安全问题统一话术防枚举/存储配额/内容上限/禁外链图/标签精确/回收站分页/CSP 头/注册竞态）==")
        # 16.1 找回密码响应差异根除：真实/幽灵用户不可区分
        # v21.93 P3 变更：原「真实问题 vs 由用户名确定性派生的假问题」方案本身即枚举预言机
        # ——攻击者可本地复算 fakeSecurityQuestion(u) 与响应比对，不一致即说明账号真实存在。
        # 现改为统一话术：响应不含任何与账号相关的信息，存在与不存在逐字节一致。
        s, j_real_q, _, _ = req("GET", "/api/auth/security-question?username=smoketest")
        check("真实用户返回统一话术（不泄露真实问题「q」）",
              s == 200 and bool((j_real_q or {}).get("security_question"))
              and (j_real_q or {}).get("security_question") != "q", f"status={s} {j_real_q}")
        s, j_ghost_q, _, _ = req("GET", "/api/auth/security-question?username=no-such-user-xyz")
        check("不存在用户同样 200 + 统一话术（不再 404）",
              s == 200 and bool((j_ghost_q or {}).get("security_question")), f"status={s} {j_ghost_q}")
        check("存在/不存在用户响应完全一致（枚举 oracle 已消除）",
              j_real_q == j_ghost_q, f"{j_real_q} vs {j_ghost_q}")
        s, j_real, _, _ = req("POST", "/api/auth/verify-answer", {"username": "smoketest", "security_answer": "wrong-x"})
        s2, j_ghost, _, _ = req("POST", "/api/auth/verify-answer", {"username": "no-such-user-xyz", "security_answer": "any"})
        check("答案错误与不存在用户同状态同话术（枚举预言机消除）", s == 400 and s2 == 400 and j_real == j_ghost, f"{s}/{s2} {j_real} vs {j_ghost}")

        # 16.2 每用户存储总配额（uploads 表 SUM(size)）
        s, _, _, _ = req("PUT", "/api/configs/max_storage_mb_per_user", {"value": "1"}, cookies=sc)
        check("后台配置存储配额 1MB", s == 200, f"status={s}")
        big = b"\xff\xd8\xff\xe0" + b"A" * (1100 * 1024)
        s, j = upload_raw("quota.jpg", big, sc)
        check("超配额上传被 403 拒绝", s == 403, f"status={s} {j}")
        req("PUT", "/api/configs/max_storage_mb_per_user", {"value": "1024"}, cookies=sc)  # 还原
        s, j = upload_raw("quota-ok.png", png_1px, sc)
        check("还原后上传正常", s == 200, f"status={s} {j}")

        # 16.3 笔记内容字节上限
        s, _, _, _ = req("PUT", "/api/configs/max_note_content_kb", {"value": "1"}, cookies=sc)
        check("后台配置内容上限 1KB", s == 200, f"status={s}")
        big_content = "<p>" + "啊" * 500 + "</p>"  # 约 1.5KB
        s, j, _, _ = req("POST", "/api/notes", {"title": "超限内容", "content": big_content}, cookies=sc)
        check("创建超限内容被 413 拒绝", s == 413, f"status={s} {j}")
        s, j, _, _ = req("PUT", f"/api/notes/{wid}", {"title": "图片宽度", "content": big_content}, cookies=sc)
        check("更新超限内容被 413 拒绝", s == 413, f"status={s} {j}")
        req("PUT", "/api/configs/max_note_content_kb", {"value": "0"}, cookies=sc)  # 还原

        # 16.4 图片禁外链（v21.68 P2-05：外链图片功能已废弃，永久关闭，无后悔药开关）
        s, j, _, _ = req("POST", "/api/notes", {"title": "外链图测试", "content": '<p>本</p><img src="/uploads/local.png" /><img src="https://evil.example.com/x.png" /><img src="data:image/png;base64,iVBOR" />'}, cookies=sc)
        ext_nid = (j or {}).get("id")
        s, j, _, _ = req("GET", f"/api/notes/{ext_nid}", cookies=sc)
        c16 = (j or {}).get("content", "")
        check("本地上传图保留", "/uploads/local.png" in c16, c16[:100])
        check("外链图被剥除", "evil.example.com" not in c16, c16[:100])
        check("data: 内嵌图同样被剥（与历史行为一致，粘贴一律先走上传）", "data:image" not in c16, c16[:120])
        # 确认外链图片开关已废弃：更新笔记仍剥除外链图
        s, _, _, _ = req("PUT", f"/api/notes/{ext_nid}", {"title": "外链图测试", "content": '<img src="https://evil.example.com/x.png" />', "version": 1}, cookies=sc)
        s, j, _, _ = req("GET", f"/api/notes/{ext_nid}", cookies=sc)
        check("外链图始终被剥除（无开关）", "evil.example.com" not in ((j or {}).get("content") or ""))
        req("DELETE", f"/api/notes/{ext_nid}", cookies=sc)  # 清场（进回收站，供 16.6 分页用）

        # 16.5 标签精确匹配：'工作' 不误中 '工作任务'
        s, j, _, _ = req("POST", "/api/notes", {"title": "标签匹配A", "content": "<p>a</p>", "tags": "工作任务, 效率"}, cookies=sc)
        tag_a = (j or {}).get("id")
        s, j, _, _ = req("POST", "/api/notes", {"title": "标签匹配B", "content": "<p>b</p>", "tags": "工作"}, cookies=sc)
        tag_b = (j or {}).get("id")
        s, j, _, _ = req("GET", "/api/notes?tag=" + _q("工作"), cookies=sc)
        titles = [n.get("title") for n in (j or {}).get("notes", [])]
        check("标签精确匹配：'工作' 不误中 '工作任务'", titles == ["标签匹配B"], str(titles))
        s, j, _, _ = req("GET", "/api/notes?tag=" + _q("工作任务"), cookies=sc)
        titles = [n.get("title") for n in (j or {}).get("notes", [])]
        check("标签精确匹配：完整标签正常命中", titles == ["标签匹配A"], str(titles))
        req("DELETE", f"/api/notes/{tag_a}", cookies=sc)  # 清场（进回收站）
        req("DELETE", f"/api/notes/{tag_b}", cookies=sc)

        # 16.6 回收站分页
        s, j, _, _ = req("GET", "/api/trash/notes?page=1&page_size=2", cookies=sc)
        t_total = (j or {}).get("total", 0)
        p1 = (j or {}).get("notes", [])
        check("回收站分页：total 正确且首页按 page_size 截断", t_total >= 3 and len(p1) == 2, f"total={t_total} n1={len(p1)}")
        s, j, _, _ = req("GET", "/api/trash/notes?page=2&page_size=2", cookies=sc)
        p2 = (j or {}).get("notes", [])
        p1_ids = [m.get("id") for m in p1]
        check("回收站分页：第二页不重叠且 total 一致", len(p2) >= 1 and all(n.get("id") not in p1_ids for n in p2) and (j or {}).get("total") == t_total, f"n2={len(p2)}")

        # 16.7 CSP 响应头（img-src 收紧）
        # v2.1.2：frame-ancestors 口径回退——web/index.html 一直写着"刻意不包含
        # frame-ancestors / X-Frame-Options：fnOS 桌面通过 iframe 嵌入应用，设置后
        # 桌面图标将无法打开"，v20.8 却在响应头单方面加了 'self'，两处矛盾；而
        # 'self' 在跨端口（5666 桌面 / 13389 应用）时不同源，经 iframe 包装的手机
        # App 会被整页拦截。现统一为"两边都不含"，并强化为正向断言 + 兜底 hash 在场。
        st, h, _ = raw_get("/")
        csp_h = h.get("content-security-policy", "")
        check("CSP 响应头已下发", "default-src 'self'" in csp_h, csp_h[:60])
        check("CSP 头 img-src 收紧", "img-src 'self' data: blob:;" in csp_h, csp_h[:130])
        check("CSP 头不含 frame-ancestors（与 meta 口径一致 · 保 fnOS iframe）",
              "frame-ancestors" not in csp_h and "X-Frame-Options" not in (h.get("x-frame-options") or ""),
              csp_h[:160])
        check("CSP 头含启动兜底内联脚本 hash（v2.1.2）",
              "script-src 'self' 'sha256-" in csp_h, csp_h[:200])

        # 16.7b v2.1.2 子路径挂载（fnOS 网关 /app/<appname>/）
        # 「移动端启动卡死」根因的服务端侧防回归钉：
        #   ① 子路径必须能取到 index.html 且注入了挂载基址
        #   ② 子路径必须能取到静态资源（MIME 正确——MIME 错就是本次事故的直接死因）
        #   ③ 缺失资源仍 404，绝不 SPA 回退成 HTML
        import re as _re
        st_s, _h_s, d_s = raw_get("/app/stickynote/")
        body_s = d_s.decode("utf-8", "replace")
        check("16.7b.1 子路径首页 200", st_s == 200, st_s)
        # v2.1.2 口径：服务端**只注入 meta、绝不注入 <base>**——网关剥前缀时应用
        # 也只看到 /，若钉死 <base href="/"> 会把 ./assets/… 打到宿主根（实测真坑）。
        # 真正的 <base> 由 index.html 的内联脚本按 location.pathname 自举。
        check("16.7b.2 服务端不注入 <base>（Vite 入口 script 在 head 最前，base 来不及生效）",
              "<base href=" not in body_s, body_s[:200])
        check("16.7b.3 可判定前缀时注入 sn-app-base",
              '<meta name="sn-app-base" content="/app/stickynote">' in body_s, body_s[:240])
        # 可判定前缀 ⇒ 相对引用被重写成带前缀的绝对路径（深路由刷新也正确）
        check("16.7b.4 资源重写为带前缀绝对路径",
              'src="/app/stickynote/assets/' in body_s, body_s[:300])
        check("16.7b.4b 内联基址自举脚本在场", 'id="sn-boot-inline"' in body_s, body_s[:200])
        _m = _re.search(r'src="(/app/stickynote/assets/[^"]+)"', body_s)
        if _m:
            st_a, h_a, _ = raw_get(_m.group(1))
            check("16.7b.5 子路径 JS 可达且 MIME 正确",
                  st_a == 200 and "javascript" in (h_a.get("content-type") or ""),
                  "%s %s" % (st_a, h_a.get("content-type")))
        else:
            check("16.7b.5 子路径 JS 可达且 MIME 正确", False, "产物中找不到重写后的资源引用")
        st_m, _, _ = raw_get("/app/stickynote/assets/definitely-missing.js")
        check("16.7b.6 子路径缺失资源 404（不得 SPA 回退成 HTML）", st_m == 404, st_m)
        st_r, _, d_r = raw_get("/app/stickynote/notes-list")
        check("16.7b.7 子路径前端路由回退 index.html 且带 selftest 脚本",
              st_r == 200 and b'id="sn-boot-inline"' in d_r, st_r)
        st_u, _, _ = raw_get("/app/stickynote/api/health")
        check("16.7b.8 子路径 API 可达（前缀被正确剥离）", st_u == 200, st_u)

        # 16.7c v2.1.3 路由扁平化 + 旧链接兼容。
        # 注意分层：服务端只在**前缀可判定**时 302；站点根（前缀不可判定）必须回
        # SPA 首页，由前端 redirect 完成跳转——否则拼出的 Location 是根相对的
        # /admin-users，会把子路径部署的用户送出挂载点（实测踩过）。
        st_old, h_old, d_old = raw_get("/admin/users?a=1")
        check("16.7c.1 站点根旧路径回 SPA 首页（不在服务端跳）",
              st_old == 200 and b'id="app"' in d_old and not h_old.get("location"),
              "%s %s" % (st_old, h_old.get("location")))
        st_new, _, d_new = raw_get("/admin-users")
        check("16.7c.2 新 /admin-users 直接回退 SPA 首页",
              st_new == 200 and b'id="app"' in d_new, st_new)
        st_sub_old, h_sub_old, _ = raw_get("/app/stickynote/admin/backup")
        check("16.7c.3 子路径旧链接服务端 302 且带回挂载前缀",
              st_sub_old == 302 and h_sub_old.get("location") == "/app/stickynote/admin-backup",
              "%s %s" % (st_sub_old, h_sub_old.get("location")))

        # 16.8 首用户注册竞态（独立新库收尾）：并发 10 个首注册应只有 1 个 admin
        stop(srv)
        race_dir = tempfile.mkdtemp(prefix="sticky-note-race-")
        srv = start_server(race_dir)
        import threading
        race_status = []
        def race_register(i):
            s, _, _, _ = req("POST", "/api/auth/register", {"username": f"racer{i}", "password": "Passw0rd!2026"})
            race_status.append(s)
        threads = [threading.Thread(target=race_register, args=(i,)) for i in range(10)]
        for t in threads: t.start()
        for t in threads: t.join()
        db = _sq.connect(os.path.join(race_dir, "notepad.db"))
        admins = db.execute("SELECT COUNT(*) FROM users WHERE role='admin'").fetchone()[0]
        total_u = db.execute("SELECT COUNT(*) FROM users").fetchone()[0]
        db.close()
        check("并发首注册仅产生 1 个 admin（竞态窗口已堵）", admins == 1, f"admins={admins}")
        # v1.0.175 P0-D CI 后修订契约：bootstrap 门禁 CountUsers 判定在事务外（设计内 TOCTOU，
        # 历史 201×10 依赖 bcrypt 拖后首提交的时序巧合，CI 负载下 5×201+5×403 即碎）。
        # 本用例的原始防线是"并发注册不得被限流/锁定/枚举干扰"（只允许 201 或 403 两种结果），
        # 而非要求全部抢到 bootstrap 窗口；DB 行数必须等于 201 数（无幽灵用户）。
        ok_codes = set(race_status) <= {201, 403}
        n201 = sum(1 for x in race_status if x == 201)
        check("并发首注册仅 201/403 且库行数=201 数（无限流/锁定/幽灵用户）",
              ok_codes and n201 >= 1 and total_u == n201,
              f"users={total_u} n201={n201} statuses={sorted(set(race_status))}")
        stop(srv)
        shutil.rmtree(race_dir, ignore_errors=True)
        srv = start_server(data_dir)  # 恢复主库（外层 finally 统一 stop）

        print("== 17. v18.8 P2 日常项（回收站可视化/唯一 admin 兜底/recover-admin 二次确认/前端构建产物）==")
        # 17.1 回收站可视化（优-10）：条数 + 回收站笔记引用图片的占用字节数
        s, j, _, _ = req("GET", "/api/trash/stats", cookies=sc)
        base_cnt = (j or {}).get("count", -1)
        base_bytes = (j or {}).get("upload_bytes", -1)
        check("回收站统计接口 200 且字段齐全", s == 200 and base_cnt >= 0 and base_bytes >= 0, f"status={s} {j}")
        s, j = upload_raw("trashstat.png", png_1px, sc)
        stat_url = (j or {}).get("url", "")
        s, j, _, _ = req("POST", "/api/notes", {"title": "统计用", "content": f'<p>x</p><img src="{stat_url}" />'}, cookies=sc)
        stat_nid = (j or {}).get("id")
        req("DELETE", f"/api/notes/{stat_nid}", cookies=sc)  # 进回收站
        s, j, _, _ = req("GET", "/api/trash/stats", cookies=sc)
        check("回收站统计：条数+1 且图片占用覆盖引用文件", (j or {}).get("count") == base_cnt + 1 and (j or {}).get("upload_bytes") >= base_bytes + len(png_1px), f"{j}")
        req("DELETE", f"/api/notes/{stat_nid}/permanent", cookies=sc)  # 彻底删除
        s, j, _, _ = req("GET", "/api/trash/stats", cookies=sc)
        check("彻底删除后统计回落", (j or {}).get("count") == base_cnt and (j or {}).get("upload_bytes") < base_bytes + len(png_1px), f"{j}")

        # 17.2 唯一 admin 删除兜底（P1-3 冗余保险）：不变量链未破坏——
        # 唯一 admin 删自己仍被"不能删除自己"先行拦截（显式兜底在其后，API 层不可达，纯防御）
        s, j, _, _ = req("GET", "/api/users", cookies=sc)
        admin_id = next((u.get("id") for u in (j or {}).get("users", []) if u.get("username") == "smoketest"), None)
        s, j, _, _ = req("DELETE", f"/api/users/{admin_id}", cookies=sc)
        check("唯一 admin 删除仍被拦截（400，不变量链完整）", s == 400, f"status={s} {j}")

        # 17.3 recover-admin 二次确认（P1-8）：确认不匹配即中止；-yes 跳确认可脚本化。
        # 改密会 BumpTokenVersion 吊销全部 token，必须放在所有用旧凭据的小节之后。
        cli_env = dict(os.environ)
        cli_env["DATA_DIR"] = data_dir  # CLI 无 -data-dir 参数，走 DATA_DIR env
        r = subprocess.run([BIN, "recover-admin"], input="not-the-admin\nNewPassw0rd!y\n",
                           capture_output=True, text=True, encoding="utf-8", errors="replace", env=cli_env, timeout=30)
        check("recover-admin 用户名确认不匹配即中止", "aborted" in (r.stdout + r.stderr).lower(), (r.stdout or "")[-60:])
        r = subprocess.run([BIN, "recover-admin", "-yes"], input="NewPassw0rd!y\n",
                           capture_output=True, text=True, encoding="utf-8", errors="replace", env=cli_env, timeout=30)
        check("recover-admin -yes 跳确认重置成功", "reset successfully" in (r.stdout + r.stderr).lower(), (r.stdout or "")[-60:])
        s, _, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("CLI 重置后旧密码失效（401）", s == 401, f"status={s}")
        s, _, sc17, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "NewPassw0rd!y"})
        check("CLI 重置后新密码可登录", s == 200, f"status={s}")
        # v21.91 测试质量改进：此处此前**未还原密码**，导致 §17.3 之后 smoketest 的真实口令
        # 一直是 NewPassw0rd!y——一个没人记录、只靠口口相传的隐藏状态依赖；
        # 任何后续新增"用 smoketest 登录"的用例都会莫名其妙 401（本轮 §25 即踩中）。
        # 现显式还原为 Passw0rd!2026 并断言，让后续小节可以无顾虑地重新登录。
        s, _, _, _ = req("POST", "/api/auth/change-password",
                         {"current_password": "NewPassw0rd!y", "new_password": "Passw0rd!2026"}, cookies=sc17)
        check("17.3 收尾还原管理员密码 → 200（消除隐藏状态依赖）", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("17.3 还原后原密码可登录 → 200", s == 200, f"status={s}")

        # v21.106：前端构建产物须含保存链路整改文案（防"改了源码忘了 build/cp embed"老坑）
        _dist_js = ""
        _assets = os.path.join(WEB, "assets")
        for _fn in os.listdir(_assets):
            if _fn.endswith(".js"):
                with open(os.path.join(_assets, _fn), "r", encoding="utf-8", errors="replace") as _f:
                    _dist_js += _f.read()
        check("构建产物含切换保存失败分类对话框(v21.106)",
              "切换前保存失败" in _dist_js and "重新加载服务器版本" in _dist_js)
        check("构建产物含笔记已删除回收站提示(v21.106)", "可在回收站恢复" in _dist_js)

        # 17.4 前端构建产物含 v18.8 新 UI 代码（防"改了源码忘了 build/cp embed"的老坑）
        dist_js = ""
        assets_dir = os.path.join(WEB, "assets")
        for fn in os.listdir(assets_dir):
            if fn.endswith(".js"):
                with open(os.path.join(assets_dir, fn), "r", encoding="utf-8", errors="replace") as f:
                    dist_js += f.read()
        check("构建产物含搜索短关键词提示（A6）", "少于 3 个字" in dist_js)
        check("构建产物含密码强度条组件（A4）", "pwd-strength" in dist_js)
        check("构建产物含 HTTP 强提示横幅（A5）", "HTTP 明文连接" in dist_js)
        check("构建产物 stripHtml 已改 DOMParser（A1）", "DOMParser" in dist_js)

        print("== 18. v18.9 审计日志结构化（JSON Lines，优-6）==")
        # 上面的登录/注册/改密动作已产生审计事件，直接读当日 audit 日志验证格式
        import glob as _glob
        audit_files = _glob.glob(os.path.join(data_dir, "logs", "audit-*.log"))
        check("audit 日志文件已生成", len(audit_files) == 1, str(audit_files))
        audit_lines = []
        if audit_files:
            with open(audit_files[0], "r", encoding="utf-8", errors="replace") as f:
                audit_lines = [l for l in f.read().splitlines() if "[AUDIT]" in l]
        login_entries = []
        for l in audit_lines:
            payload = l.split("[AUDIT] ", 1)[-1]
            try:
                e = json.loads(payload)
                if e.get("action") == "login":
                    login_entries.append(e)
            except Exception:
                pass
        check("audit 全部为合法 JSON Lines 且含 login 事件", len(login_entries) >= 2, f"lines={len(audit_lines)} login={len(login_entries)}")
        e0 = login_entries[0] if login_entries else {}
        check("audit 固定字段齐全（time/action/actor/ip/result）", all(k in e0 for k in ("time", "action", "actor", "ip", "result")), str(e0)[:120])
        check("login 事件含成功与失败两种 result", {"success", "failed"} <= {e.get("result") for e in login_entries}, str({e.get('result') for e in login_entries}))

        print("== 19. v19.0 Markdown 导出（单篇+批量，?format=md）==")
        # CLI 改密后旧 token 已失效——重新登录拿有效会话
        # v21.91：§17.3 已显式还原密码，此处同步改回 Passw0rd!2026——
        # 此前这里硬编码 NewPassw0rd!y，正是"§17.3 不还原"这处隐藏状态依赖的下游受害者。
        s, _, sc19, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("§19 重新登录成功", s == 200, f"status={s}")
        sc19x = {k: v for k, v in sc19.items() if v} if sc19 else sc
        # cookie 头用于 raw_get（raw_get 不走 req 的 cookie 拼合逻辑）
        def cookie_hdr(cookies):
            return {"Cookie": "; ".join(f"{k}={v}" for k, v in cookies.items() if v)}

        # 单篇 MD 导出：含标题/链接/图片的笔记
        s, j, _, _ = req("POST", "/api/notes",
            {"title": "MD导出测试", "content": '<h1>大标题</h1><p>文字<strong>粗</strong></p><p><a href="https://example.com">链接</a><img src="/uploads/a.png" alt="插图" /></p>'},
            cookies=sc19x)
        md_nid = (j or {}).get("id")
        check("MD 测试笔记创建成功", bool(md_nid), f"id={md_nid}")

        # 单篇 MD（blob 响应，v19.4 起默认 Markdown 无需 format=md 参数）
        s19, md_headers, md_blob = raw_get(f"/api/notes/{md_nid}/export", cookie_hdr(sc19x))
        md_text = md_blob.decode("utf-8", errors="replace")
        check("单篇 MD：标题→# 前置", "# 大标题" in md_text or "MD导出测试" in md_text, md_text[:80])
        check("单篇 MD：粗体→**", "**粗**" in md_text, md_text[:120])
        check("单篇 MD：链接→[text](url)", "[链接](https://example.com)" in md_text, md_text[:150])
        check("单篇 MD：图片→![alt](url)", "![插图](/uploads/a.png)" in md_text, md_text[:200])
        check("单篇 MD：Content-Disposition 含 .md", "md" in md_headers.get("content-disposition", "").lower() and ".txt" not in md_headers.get("content-disposition", "").lower(), md_headers.get("content-disposition", ""))

        # 批量 MD 导出（zip 内 .md 文件）
        s19b, _, zip_blob = raw_get("/api/export/notes", cookie_hdr(sc19x))
        with zipfile.ZipFile(io.BytesIO(zip_blob)) as zf:
            names = zf.namelist()
            check("批量 MD zip 含 .md 文件（不含 .txt）", any(".md" in n for n in names) and not any(".txt" in n for n in names), str(names))
            md_name = next((n for n in names if n.endswith(".md")), "")
            if md_name:
                with zf.open(md_name) as mdf:
                    mdt = mdf.read().decode("utf-8", errors="replace")
                check("批量 MD zip 内文件含 Markdown 标题", "# " in mdt, mdt[:80])

        req("DELETE", f"/api/notes/{md_nid}", cookies=sc19x)  # 清场

        print("== 20. v19.1 配额可视化（用量查询接口 /api/quota）==")
        s, j, _, _ = req("GET", "/api/quota", cookies=sc19x)
        check("配额接口 200 且字段齐全", s == 200 and all(k in (j or {}) for k in ("notes_count", "notes_limit", "storage_bytes", "storage_limit_mb")), f"status={s} {j}")
        base_quot = j or {}
        # 建一条笔记 → notes_count +1
        s, j, _, _ = req("POST", "/api/notes", {"title": "配额测试", "content": "<p>x</p>"}, cookies=sc19x)
        q_nid = (j or {}).get("id")
        s, j, _, _ = req("GET", "/api/quota", cookies=sc19x)
        check("建一条笔记后 notes_count +1", (j or {}).get("notes_count") == base_quot.get("notes_count") + 1, f"base={base_quot.get('notes_count')} now={j.get('notes_count')}")
        # 上传图片 → storage_bytes 增加
        s, ju = upload_raw("quota-test.png", png_1px, sc19x)
        q_img_url = (ju or {}).get("url", "")
        s, j, _, _ = req("GET", "/api/quota", cookies=sc19x)
        check("上传图片后 storage_bytes 增加", (j or {}).get("storage_bytes", 0) >= base_quot.get("storage_bytes", 0) + len(png_1px), f"base={base_quot.get('storage_bytes')} now={j.get('storage_bytes')}")
        check("配置上限读取正确（notes_limit/storage_limit_mb 均为正整数）", isinstance((j or {}).get("notes_limit"), int) and (j or {}).get("notes_limit") > 0 and isinstance((j or {}).get("storage_limit_mb"), int) and (j or {}).get("storage_limit_mb") > 0, f"{j}")
        # 删笔记 → notes_count 恢复
        req("DELETE", f"/api/notes/{q_nid}", cookies=sc19x)
        s, j, _, _ = req("GET", "/api/quota", cookies=sc19x)
        check("删笔记后 notes_count 恢复", (j or {}).get("notes_count") == base_quot.get("notes_count"), f"base={base_quot.get('notes_count')} now={j.get('notes_count')}")

        print("== 21. v19.3 P3 基础设施建设（健康检查/请求日志/流式导出/连接池/slog）==")
        # health 端点（不鉴权）
        s, j, _, _ = req("GET", "/health")
        check("/health 返回 200 且 status=ok/healthy", s == 200 and (j or {}).get("status", "") in ("ok", "healthy"), f"status={s} {j}")
        # v21.90 L-04：/health 收敛为只回 status，不再泄露 version/uptime
        check("/health 只回 status（无 version/uptime）", (j or {}).get("status", "") in ("ok", "healthy") and "version" not in (j or {}) and "uptime" not in (j or {}), f"{j}")
        # v21.91 P2-26：/api/version 从未认证组移入需登录组——该端点回 build_time/git_commit
        # 构建指纹且前端零调用，未认证暴露无收益；运维探活统一走 /health。
        s_v, j_v, _, _ = req("GET", "/api/version")
        check("未认证 /api/version → 401（v21.91 收敛）", s_v == 401, f"status={s_v} {j_v}")
        s_v2, j_v2, _, _ = req("GET", "/api/version", cookies=sc19x)
        check("已登录 /api/version → 200 且回版本号", s_v2 == 200 and isinstance((j_v2 or {}).get("version"), str) and (j_v2 or {}).get("version") != "", f"status={s_v2} {j_v2}")

        # 请求日志中间件 → info 日志文件含结构化行（msg=request completed）
        info_files = _glob.glob(os.path.join(data_dir, "logs", "info-*.log"))
        if info_files:
            with open(info_files[0], "r", encoding="utf-8", errors="replace") as f:
                info_lines = f.read().splitlines()
            req_lines = [l for l in info_lines if "request completed" in l]
            check("请求日志中间件已产生结构化日志行", len(req_lines) >= 1, f"total={len(info_lines)}")
            if req_lines:
                check("请求日志含 method/path/status/duration_ms/ip 字段", all(k in req_lines[0] for k in ("method", "path", "status", "duration_ms", "ip")), req_lines[0][:150])
        print("== 22. v20.0 笔记加密（AES-256-GCM+PBKDF2+防爆破）==")
        # --- 22.1~22.3 加密与验证 ---
        s, j, _, _ = req("POST", "/api/notes", {"title": "加密测试笔记", "content": "<p>秘密内容</p>"}, cookies=sc19x)
        enc_nid = (j or {}).get("id")
        check("22.1 创建笔记成功", isinstance(enc_nid, int) and enc_nid > 0, f"id={enc_nid}")

        s, j, _, _ = req("PUT", f"/api/notes/{enc_nid}/encrypt",
                         {"password": "mypassword", "confirm_password": "mypassword"}, cookies=sc19x)
        check("22.2 加密成功 → 200 is_encrypted:true", s == 200 and (j or {}).get("is_encrypted") is True, f"status={s} {j}")

        s, j, _, _ = req("PUT", f"/api/notes/{enc_nid}/encrypt",
                         {"password": "againagain", "confirm_password": "againagain"}, cookies=sc19x)
        check("22.3 已加密笔记再次加密被拒", s == 400 and "已加密" in (j or {}).get("error", ""), f"status={s} {j}")

        # --- 22.4~22.7 密码验证与防爆破 ---
        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/verify-encrypt",
                         {"password": "mypassword"}, cookies=sc19x)
        check("22.4 正确密码验证 → 200 返回解密内容", s == 200 and isinstance(j, dict) and "秘密内容" in (j or {}).get("content", ""),
              f"status={s} content={(j or {}).get('content','')[:40]}")

        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/verify-encrypt",
                         {"password": "wrongpass"}, cookies=sc19x)
        check("22.5 错误密码验证 → 403", s == 403, f"status={s} {j}")

        os.environ["LOGIN_LOCK_SECONDS"] = "2"
        for i in range(4):
            req("POST", f"/api/notes/{enc_nid}/verify-encrypt", {"password": "wrongpass"}, cookies=sc19x)
        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/verify-encrypt",
                         {"password": "wrongpass"}, cookies=sc19x)
        check("22.6 错误密码×5 → 第6次 429", s == 429, f"status={s} {j}")

        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/verify-encrypt",
                         {"password": "mypassword"}, cookies=sc19x)
        check("22.7 锁定中正确密码也 429", s == 429, f"status={s} {j}")

        time.sleep(3)
        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/verify-encrypt",
                         {"password": "mypassword"}, cookies=sc19x)
        check("22.8 锁过期后正确密码恢复 200", s == 200 and "秘密内容" in (j or {}).get("content", ""),
              f"status={s} content={(j or {}).get('content','')[:40]}")
        os.environ.pop("LOGIN_LOCK_SECONDS", None)

        # --- 22.9~22.10 解密 ---
        s, j, _, _ = req("DELETE", f"/api/notes/{enc_nid}/encrypt",
                         {"password": "wrongpass"}, cookies=sc19x)
        check("22.9 解密：错误密码 → 403", s == 403, f"status={s} {j}")

        s, j, _, _ = req("DELETE", f"/api/notes/{enc_nid}/encrypt",
                         {"password": "mypassword"}, cookies=sc19x)
        check("22.10 解密成功 → 200 is_encrypted:false", s == 200 and (j or {}).get("is_encrypted") is False,
              f"status={s} {j}")

        # --- 22.10b P2 批次1（§60 门禁）：加密三件同事务产事件且 version++ ---
        # 直查 SQLite：加密 2 次 + 解密 1 次 + 重复加密被拒零事件 → 事件链 UPDATE 序列
        import sqlite3 as _sq2
        _db2 = _sq2.connect(os.path.join(data_dir, "notepad.db"))
        _esid = _db2.execute("SELECT sync_id FROM notes WHERE id=?", (enc_nid,)).fetchone()[0]
        def _ev2():
            return _db2.execute("SELECT operation, server_version FROM sync_changes WHERE entity_sync_id=? ORDER BY change_seq", (_esid,)).fetchall()
        _chain = [(r[0], r[1]) for r in _ev2()]
        # 加密(22.2, v2) → 再加密被拒零事件 → 解密(22.10, v3)；其后 22.11 再加密为 v4
        check("22.10b 加密三件事件链 version 递增", _chain == [("CREATE", 1), ("UPDATE", 2), ("UPDATE", 3)], str(_chain))
        _enc_row = _db2.execute("SELECT is_encrypted, version, CASE WHEN encrypted_content != '' THEN 1 ELSE 0 END FROM notes WHERE id=?", (enc_nid,)).fetchone()
        check("22.10b 事件与行状态一致（解密态 v3 明文内容）", _enc_row == (0, 3, 0), str(_enc_row))
        _db2.close()

        # --- 22.11~22.12 导出跳过加密笔记 ---
        # 加密这条笔记
        s, j, _, _ = req("PUT", f"/api/notes/{enc_nid}/encrypt",
                         {"password": "exportpw", "confirm_password": "exportpw"}, cookies=sc19x)
        check("22.11 重新加密成功", s == 200, f"status={s}")

        # --- 22.11a/b 加密笔记单篇导出（v20.6 POST body 密码；此前路由漏注册 POST 致 404）---
        s, _, _, raw = req("POST", f"/api/notes/{enc_nid}/export",
                           {"password": "exportpw"}, cookies=sc19x, raw=True)
        check("22.11a 加密笔记导出 POST 正确密码 → 200 且含明文",
              s == 200 and "秘密内容".encode("utf-8") in raw, f"status={s} body={raw[:80]}")
        s, j, _, _ = req("POST", f"/api/notes/{enc_nid}/export",
                         {"password": "badpassword"}, cookies=sc19x)
        check("22.11b 加密笔记导出 POST 错误密码 → 403", s == 403, f"status={s} {j}")

        # 再创建一条普通笔记，确保 zip 导出能区分
        s, j2, _, _ = req("POST", "/api/notes", {"title": "普通笔记", "content": "<p>公开</p>"}, cookies=sc19x)
        plain_nid = (j2 or {}).get("id")
        _, headers, zip_blob = raw_get("/api/export/notes", cookie_hdr(sc19x))
        enc_skipped = headers.get("x-encrypted-skipped", "0")
        check("22.12 导出全部 → X-Encrypted-Skipped 含跳过计数",
              int(enc_skipped) >= 1, f"skipped={enc_skipped}")

        with zipfile.ZipFile(io.BytesIO(zip_blob)) as zf:
            zip_names = zf.namelist()
            plain_only = [n for n in zip_names if "普通" in n or "加密" not in n]
            check("22.13 zip 内不含加密笔记内容", not any("加密测试笔记" in n for n in zip_names),
                  str(zip_names[:5]))

        req("DELETE", f"/api/notes/{plain_nid}", cookies=sc19x)

        # --- 22.14 FTS5 不索引加密内容 ---
        s, j, _, _ = req("GET", "/api/notes?q=" + urllib.parse.quote("秘密内容"), cookies=sc19x)
        enc_ids = [n.get("id") for n in (j or []) if isinstance(n, dict)]
        check("22.14 FTS5 搜索不到加密笔记内容", enc_nid not in enc_ids,
              f"found ids={enc_ids}")

        # --- 22.15~22.16 加密笔记列表字段 + 回收站字段 ---
        s, j, _, _ = req("GET", "/api/notes", cookies=sc19x)
        notes = (j or []) if isinstance(j, list) else (j or {}).get("notes", [])
        enc_note = next((n for n in notes if (n.get("id") if isinstance(n, dict) else None) == enc_nid), {})
        check("22.15 列表返回 is_encrypted:true", (enc_note or {}).get("is_encrypted") is True,
              f"is_encrypted={(enc_note or {}).get('is_encrypted')}")

        req("DELETE", f"/api/notes/{enc_nid}", cookies=sc19x)
        s, j, _, _ = req("GET", "/api/trash/notes", cookies=sc19x)
        trash = (j or []) if isinstance(j, list) else (j or {}).get("notes", [])
        trash_enc = next((n for n in trash if (n.get("id") if isinstance(n, dict) else None) == enc_nid), {})
        check("22.16 回收站中加密笔记 is_encrypted:true 且无 content",
              (trash_enc or {}).get("is_encrypted") is True and not (trash_enc or {}).get("content"),
              f"is_encrypted={(trash_enc or {}).get('is_encrypted')} content={(trash_enc or {}).get('content','')[:30]}")
        # 清场：彻底删除
        req("DELETE", f"/api/notes/{enc_nid}/permanent", cookies=sc19x)

        print("== 23. v21.86 文件夹（CRUD + 非空删除保护 + folder_id 过滤）==")
        # --- 23.1 默认文件夹存在（GET /api/folders 返回 is_default 项）---
        s, j, _, _ = req("GET", "/api/folders", cookies=sc19x)
        check("23.1 默认文件夹存在", s == 200 and any(f.get("is_default") for f in (j or {}).get("folders", [])), f"status={s} {j}")
        # --- 23.2~23.3 创建 + 重名/与默认重名拒绝 ---
        s, j, _, _ = req("POST", "/api/folders", {"name": "工作"}, cookies=sc19x)
        fid = (j or {}).get("id")
        check("23.2 创建文件夹 → 201", s == 201 and fid, f"status={s} {j}")
        s, j, _, _ = req("POST", "/api/folders", {"name": "工作"}, cookies=sc19x)
        check("23.3 重名 → 409", s == 409, f"status={s} {j}")
        # --- 23.4 重命名 ---
        s, j, _, _ = req("PUT", f"/api/folders/{fid}", {"name": "日常工作"}, cookies=sc19x)
        check("23.4 重命名 → 200", s == 200, f"status={s} {j}")
        # --- 23.5 创建带 folder_id 笔记 ---
        s, j, _, _ = req("POST", "/api/notes", {"title": "F笔记", "content": "<p>x</p>", "tags": "", "folder_id": fid}, cookies=sc19x)
        fnid = (j or {}).get("id")
        check("23.5 创建带 folder_id 笔记 → 201", s == 201 and fnid, f"status={s} {j}")
        # --- 23.6 非空文件夹删除保护 ---
        s, j, _, _ = req("DELETE", f"/api/folders/{fid}", cookies=sc19x)
        check("23.6 非空文件夹删除 → 409 FOLDER_NOT_EMPTY", s == 409 and (j or {}).get("code") == "FOLDER_NOT_EMPTY", f"status={s} {j}")
        # --- 23.7~23.8 folder_id 过滤（指定 / default / 全部）---
        s, j, _, _ = req("GET", f"/api/notes?folder_id={fid}", cookies=sc19x)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("23.7 folder_id 过滤只返回该文件夹笔记", fnid in ids, f"ids={ids}")
        s, j, _, _ = req("GET", "/api/notes?folder_id=default", cookies=sc19x)
        ids_def = [n.get("id") for n in (j or {}).get("notes", [])]
        check("23.8 default 过滤排除自定义文件夹笔记", fnid not in ids_def, f"ids={ids_def}")
        # --- 23.8b P1 批次2（§129 门禁）：文件夹 mutation 事务内产事件（§18/§55）---
        import sqlite3 as _sqf
        _dbf = _sqf.connect(os.path.join(data_dir, "notepad.db"))
        def _fev(sid):
            return [(r[0], r[1]) for r in _dbf.execute(
                "SELECT operation, server_version FROM sync_changes WHERE entity_type='FOLDER' AND entity_sync_id=? ORDER BY change_seq", (sid,)).fetchall()]
        _fs = _dbf.execute("SELECT sync_id FROM note_folders WHERE id=?", (fid,)).fetchone()[0]
        check("23.8b 建夹/重命名事件链 CREATE1+UPDATE2", _fev(_fs) == [("CREATE", 1), ("UPDATE", 2)], str(_fev(_fs)))
        s, j, _, _ = req("POST", "/api/folders", {"name": "排序夹"}, cookies=sc19x)
        fid2 = (j or {}).get("id")
        s, j, _, _ = req("PUT", f"/api/folders/{fid2}", {"position": "top"}, cookies=sc19x)
        check("23.8b 文件夹移动排序 → 200", s == 200, f"status={s} {j}")
        _orderN = _dbf.execute("SELECT COUNT(*) FROM sync_changes WHERE entity_type='FOLDER_ORDER'").fetchone()[0]
        check("23.8b 一次移动产恰 1 条 FOLDER_ORDER 集合级事件（§55 单一 seq）", _orderN == 1, str(_orderN))
        _fs2 = _dbf.execute("SELECT sync_id FROM note_folders WHERE id=?", (fid2,)).fetchone()[0]
        # 移动不产逐行 FOLDER 事件（版本推进只体现在 note_folders.version 与 FOLDER_ORDER 载荷内）
        _mv = _dbf.execute("SELECT sync_id, name, version FROM note_folders WHERE id IN (?,?)", (fid, fid2)).fetchall()
        _mvv = {r[0]: (r[1], r[2]) for r in _mv}
        check("23.8b 移动推进全序内各 folder version（库内权威）", _mvv.get(_fs) == ("日常工作", 3) or _mvv.get(_fs) == ("工作", 3), str(_mvv.get(_fs)))
        check("23.8b 被移动夹版本同步推进", _mvv.get(_fs2) == ("排序夹", 2), str(_mvv.get(_fs2)))
        _opay = _dbf.execute("SELECT payload_json FROM sync_changes WHERE entity_type='FOLDER_ORDER' ORDER BY change_seq DESC LIMIT 1").fetchone()[0]
        check("23.8b FOLDER_ORDER 载荷含双夹 sync_id 全序", _fs in _opay and _fs2 in _opay and _opay.index(_fs2) < _opay.index(_fs), _opay[:160])

        # --- 23.9 清空后可删 ---
        req("DELETE", f"/api/notes/{fnid}", cookies=sc19x)
        s, j, _, _ = req("DELETE", f"/api/folders/{fid}", cookies=sc19x)
        check("23.9 空文件夹删除 → 200", s == 200, f"status={s} {j}")
        # --- 23.10 P1 批次2（§56/§143）：DELETE 墓碑 + 回收站悬空引用逐行 UPDATE（保墓碑态）---
        _fdel = _fev(_fs)
        check("23.10 文件夹删除墓碑事件 DELETE v3", _fdel[-1] == ("DELETE", 3), str(_fdel))
        _ns = _dbf.execute("SELECT sync_id FROM notes WHERE id=?", (fnid,)).fetchone()[0]
        _rept = _dbf.execute(
            "SELECT operation, server_version, payload_json LIKE ? FROM sync_changes WHERE entity_type='NOTE' AND entity_sync_id=? ORDER BY change_seq DESC LIMIT 1",
            ('%"deleted_at":"20%', _ns)).fetchone()
        check("23.10 回收站笔记重指向 UPDATE v3 且 payload 保墓碑态",
              _rept[0] == "UPDATE" and _rept[1] == 3 and _rept[2] == 1, str(_rept))
        _dbf.close()

        print("== 24. v21.87 笔记移动到/复制到文件夹 ==")
        # --- 24.1 建默认笔记 + 目标文件夹 ---
        s, j, _, _ = req("POST", "/api/notes", {"title": "移动源笔记", "content": "<p>move me</p>", "tags": "movetag"}, cookies=sc19x)
        mv_id = (j or {}).get("id")
        check("24.1 建源笔记 → 201", s == 201 and mv_id, f"status={s} {j}")
        s, j, _, _ = req("POST", "/api/folders", {"name": "移动目标"}, cookies=sc19x)
        mv_fid = (j or {}).get("id")
        check("24.1 建目标文件夹 → 201", s == 201 and mv_fid, f"status={s} {j}")
        # --- 24.2 移动到自定义文件夹 ---
        s, j, _, _ = req("PUT", f"/api/notes/{mv_id}/folder", {"folder_id": mv_fid}, cookies=sc19x)
        check("24.2 移动到自定义文件夹 → 200", s == 200, f"status={s} {j}")
        s, j, _, _ = req("GET", f"/api/notes?folder_id={mv_fid}", cookies=sc19x)
        ids = [n.get("id") for n in (j or {}).get("notes", [])]
        check("24.2 移动后 folder_id 过滤可见", mv_id in ids, f"ids={ids}")
        s, j, _, _ = req("GET", "/api/notes?folder_id=default", cookies=sc19x)
        ids_def = [n.get("id") for n in (j or {}).get("notes", [])]
        check("24.2 移动后默认文件夹不可见", mv_id not in ids_def, f"ids={ids_def}")
        # --- 24.3 移回默认（folder_id=null）---
        s, j, _, _ = req("PUT", f"/api/notes/{mv_id}/folder", {"folder_id": None}, cookies=sc19x)
        check("24.3 移动回默认 → 200", s == 200, f"status={s} {j}")
        s, j, _, _ = req("GET", "/api/notes?folder_id=default", cookies=sc19x)
        ids_def = [n.get("id") for n in (j or {}).get("notes", [])]
        check("24.3 移动后默认文件夹可见", mv_id in ids_def, f"ids={ids_def}")
        # --- 24.4 复制到自定义文件夹：新笔记带(副本)后缀 + 源笔记不动 ---
        s, j, _, _ = req("POST", f"/api/notes/{mv_id}/copy", {"folder_id": mv_fid}, cookies=sc19x)
        cp_id = (j or {}).get("id")
        check("24.4 复制到文件夹 → 201", s == 201 and cp_id and cp_id != mv_id, f"status={s} {j}")
        check("24.4 副本标题带 (副本)", "副本" in (j or {}).get("title", ""), f"title={(j or {}).get('title')}")
        s, j, _, _ = req("GET", f"/api/notes/{cp_id}", cookies=sc19x)
        check("24.4 副本内容与标签一致", ((j or {}).get("content") or "") == "<p>move me</p>" and (j or {}).get("tags") == "movetag", f"{j}")
        s, j, _, _ = req("GET", "/api/notes?folder_id=default", cookies=sc19x)
        ids_def = [n.get("id") for n in (j or {}).get("notes", [])]
        check("24.4 源笔记仍在默认文件夹", mv_id in ids_def, f"ids={ids_def}")
        # --- 24.5 复制到默认（folder_id=null）---
        s, j, _, _ = req("POST", f"/api/notes/{mv_id}/copy", {"folder_id": None}, cookies=sc19x)
        check("24.5 复制到默认 → 201 且 folder_id 为空", s == 201 and not (j or {}).get("folder_id"), f"status={s} {j}")
        # --- 24.6 错误路径：非法文件夹 / 笔记不存在 / 未认证 ---
        s, j, _, _ = req("PUT", f"/api/notes/{mv_id}/folder", {"folder_id": 99999999}, cookies=sc19x)
        check("24.6 移动到不存在文件夹 → 400", s == 400, f"status={s} {j}")
        s, j, _, _ = req("POST", "/api/notes/99999999/copy", {"folder_id": None}, cookies=sc19x)
        check("24.6 复制不存在笔记 → 404", s == 404, f"status={s} {j}")
        s, j, _, _ = req("PUT", f"/api/notes/{mv_id}/folder", {"folder_id": mv_fid})
        check("24.6 未认证移动 → 401", s == 401, f"status={s}")
        # --- 24.7 P2 批次1（§129 门禁）：Web 写路径同事务产 ChangeLog 事件 ---
        # 建目录直查 SQLite：事件行 (user, entity_sync_id, operation, server_version) 与
        # API 行为一一对应；PermanentDelete 属 ADR-004 R1 豁免清单（零新增事件）。
        import sqlite3 as _sq1
        _db1 = _sq1.connect(os.path.join(data_dir, "notepad.db"))
        _uid = _db1.execute("SELECT user_id FROM notes WHERE id=?", (mv_id,)).fetchone()[0]
        _csid = _db1.execute("SELECT sync_id FROM notes WHERE id=?", (mv_id,)).fetchone()[0]
        def _ev(sync_id, op=None):
            q = "SELECT operation, server_version FROM sync_changes WHERE entity_sync_id=?"
            args = [sync_id]
            if op:
                q += " AND operation=?"
                args.append(op)
            return _db1.execute(q, args).fetchall()
        _evs = _ev(_csid)
        check("24.7 移动源笔记事件链 CREATE+2xUPDATE", [(e[0], e[1]) for e in _evs] == [("CREATE", 1), ("UPDATE", 2), ("UPDATE", 3)], str(_evs))
        _cpsid = _db1.execute("SELECT sync_id FROM notes WHERE id=?", (cp_id,)).fetchone()[0]
        _cevs = _ev(_cpsid)
        check("24.7 副本事件 CREATE version=1", [(e[0], e[1]) for e in _cevs] == [("CREATE", 1)], str(_cevs))
        # 删除→恢复→彻底删除：DELETE/RESTORE 各 +1；permanent 豁免零新增（ADR-004 R1）
        s, j, _, _ = req("DELETE", f"/api/notes/{cp_id}", cookies=sc19x)
        check("24.7 删除副本 → 200", s == 200, f"status={s}")
        _devs = _ev(_cpsid)
        check("24.7 DELETE 墓碑事件 version=2", [(e[0], e[1]) for e in _devs][-1] == ("DELETE", 2), str(_devs))
        s, j, _, _ = req("POST", f"/api/notes/{cp_id}/restore", cookies=sc19x)
        check("24.7 恢复副本 → 200", s == 200, f"status={s}")
        _revs = _ev(_cpsid)
        check("24.7 RESTORE 事件 version=3", [(e[0], e[1]) for e in _revs][-1] == ("RESTORE", 3), str(_revs))
        # 再次软删（彻底删除仅对回收站笔记生效），其后 permanent 不得再产事件
        s, j, _, _ = req("DELETE", f"/api/notes/{cp_id}", cookies=sc19x)
        check("24.7 二次软删副本 → 200", s == 200, f"status={s}")
        _devs2 = _ev(_cpsid)
        check("24.7 二次 DELETE 事件 version=4", [(e[0], e[1]) for e in _devs2][-1] == ("DELETE", 4), str(_devs2))
        _seq0 = _db1.execute("SELECT MAX(change_seq) FROM sync_changes").fetchone()[0]
        s, j, _, _ = req("DELETE", f"/api/notes/{cp_id}/permanent", cookies=sc19x)
        check("24.7 彻底删除副本 → 200", s == 200, f"status={s}")
        _seq1 = _db1.execute("SELECT MAX(change_seq) FROM sync_changes").fetchone()[0]
        # E8（2026-09-23 用户产品决策，**推翻原「purge 不发事件」设计**）：永久删产 PURGE 事件
        # （客户端据此物理清理本地行；不发事件会留僵尸行 + 恢复必撞 ENTITY_GONE，真机实锤）。
        check("24.7 PermanentDelete 产 PURGE 事件（change_seq +1）", _seq1 == _seq0 + 1, f"{_seq0}→{_seq1}")
        check("24.7 PURGE 事件已落链", len(_ev(_cpsid, op="PURGE")) == 1, str(_ev(_cpsid, op="PURGE")))
        _db1.close()
        # --- 24.8 移动推进 version（P2 批次1 · v20 §57 冻结决策，定向推翻 v21.87
        #     "移动不动 version"：移动变更经 ChangeLog 事件传播，跨端下次保存按 409 冲突处理）---
        s, j, _, _ = req("GET", f"/api/notes/{mv_id}", cookies=sc19x)
        ver_before = (j or {}).get("version")
        s, _, _, _ = req("PUT", f"/api/notes/{mv_id}/folder", {"folder_id": mv_fid}, cookies=sc19x)
        s, j, _, _ = req("GET", f"/api/notes/{mv_id}", cookies=sc19x)
        check("24.8 移动后 version +1（§57，事件携带新版本）", (j or {}).get("version") == ver_before + 1, f"before={ver_before} after={j.get('version')}")
        # 清场
        req("DELETE", f"/api/notes/{mv_id}", cookies=sc19x)
        req("DELETE", f"/api/notes/{cp_id}", cookies=sc19x)
        # --- 24.9 P2 批次1：批量删除单事务逐行事件 + EmptyTrash 豁免（ADR-004 R1）---
        s, j, _, _ = req("POST", "/api/notes", {"title": "批删1", "content": "<p>b</p>"}, cookies=sc19x)
        b1 = (j or {}).get("id")
        s, j, _, _ = req("POST", "/api/notes", {"title": "批删2", "content": "<p>b</p>"}, cookies=sc19x)
        b2 = (j or {}).get("id")
        s, j, _, _ = req("POST", "/api/notes/batch-delete", {"ids": [b1, b2, 999999]}, cookies=sc19x)
        check("24.9 批量删除 2 成功 1 跳过", s == 200 and (j or {}).get("deleted") == 2 and (j or {}).get("total") == 3, f"{j}")
        import sqlite3 as _sq3
        _db3 = _sq3.connect(os.path.join(data_dir, "notepad.db"))
        def _chain3(nid):
            _sid = _db3.execute("SELECT sync_id FROM notes WHERE id=?", (nid,)).fetchone()[0]
            return [(r[0], r[1]) for r in _db3.execute("SELECT operation, server_version FROM sync_changes WHERE entity_sync_id=? ORDER BY change_seq", (_sid,)).fetchall()]
        check("24.9 批删行事件 CREATE+DELETE v2", _chain3(b1) == [("CREATE", 1), ("DELETE", 2)], str(_chain3(b1)))
        check("24.9 批删行事件一致", _chain3(b2) == [("CREATE", 1), ("DELETE", 2)], str(_chain3(b2)))
        _seq0 = _db3.execute("SELECT MAX(change_seq) FROM sync_changes").fetchone()[0]
        _trash_n = _db3.execute("SELECT COUNT(*) FROM notes WHERE deleted_at IS NOT NULL").fetchone()[0]
        s, j, _, _ = req("DELETE", "/api/trash/notes", cookies=sc19x)
        check("24.9 清空回收站 → 200", s == 200, f"status={s}")
        _seq1 = _db3.execute("SELECT MAX(change_seq) FROM sync_changes").fetchone()[0]
        # E8（2026-09-23，同上推翻原豁免设计）：清空回收站按条产 PURGE 事件（逐条传播永久删）。
        check("24.9 EmptyTrash 逐条产 PURGE 事件（change_seq +N）",
              _seq1 == _seq0 + _trash_n, f"{_seq0}→{_seq1} trash={_trash_n}")
        _db3.close()
        # --- 24.10 P1 批次3（ADR-003 R2）：Web 上传落盘即算服务端权威 sha256 入库 ---
        s, j = upload_raw("hashchk.png", bytes([0x89]) + b"PNG" + bytes([13, 10, 26, 10]) + b"smoke-hash-bytes" * 8, sc19x)
        up_name = ((j or {}).get("url") or "").split("/")[-1]
        check("24.10 图片上传成功", s == 200 and up_name, f"status={s} {j}")
        import sqlite3 as _sqh
        _dbh = _sqh.connect(os.path.join(data_dir, "notepad.db"))
        _rowh = _dbh.execute("SELECT length(sha256), size FROM uploads WHERE filename = ?", (up_name,)).fetchone()
        check("24.10 uploads.sha256 落库为 64 位 hex", _rowh is not None and _rowh[0] == 64, str(_rowh))
        _dbh.close()
        s, j, _, _ = req("GET", "/api/system/uploads-hash", cookies=sc19x)
        check("24.10 管理端回填进度端点 200", s == 200 and isinstance((j or {}).get("pending"), int) and (j or {}).get("pending") >= 0, f"status={s} {j}")
        req("DELETE", f"/api/folders/{mv_fid}", cookies=sc19x)

        print("== 25. v21.91 管理后台闭环（审计日志 /api/logs + 用户管理 + 权限边界）==")
        # 背景：覆盖率矩阵扫描发现 GET /api/logs 是唯一无任何测试命中的端点，
        # 且管理后台 4 个页面零 UI 覆盖——本节点补 API 层闭环（UI 层见真机脚本）。
        # --- 25.0 准备：两组独立会话 ---
        # 坑（本轮踩到）：不能直接复用 sc / sc19x ——
        #   sc   在 §21 的 token_version 吊销测试后已失效（返回"登录已过期"）；
        #   sc19x 派生自 smoketest（首个注册用户 = admin），拿来测"普通用户越权"会恒 200。
        # 故本节点各自重新登录，确保身份与有效期都是干净的。
        s, j, scAdm, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("25.0 管理员重新登录（前置）", s == 200, f"status={s} body={str(j)[:120]}")
        req("POST", "/api/auth/register", {"username": "nor25", "password": "Nor25@Pass2026",
                                           "security_question": "q", "security_answer": "a"})
        s, j, scNor, _ = req("POST", "/api/auth/login", {"username": "nor25", "password": "Nor25@Pass2026"})
        check("25.0 普通用户登录（前置）", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/quota", cookies=scNor)
        is_admin = bool(j and isinstance(j, dict) and (j.get("role") == "admin"))
        check("25.0 确认 nor25 非管理员（前置）", not is_admin, f"quota={str(j)[:70]}")

        # --- 25.1 权限边界三级 ---
        s, j, _, _ = req("GET", "/api/logs")
        check("25.1 未认证 /api/logs → 401", s == 401, f"status={s}")
        s, j, _, _ = req("GET", "/api/logs", cookies=scNor)
        check("25.1 普通用户 /api/logs → 403", s == 403, f"status={s}")
        s, j, _, _ = req("GET", "/api/users", cookies=scNor)
        check("25.1 普通用户 /api/users → 403", s == 403, f"status={s}")

        # --- 25.2 admin 查日志：响应结构 ---
        s, j, _, _ = req("GET", "/api/logs", cookies=scAdm)
        ok_struct = (s == 200 and isinstance(j, dict) and "logs" in j and "total" in j
                     and j.get("page") == 1 and j.get("page_size") == 20)
        check("25.2 admin /api/logs → 200 且含 logs/total/page/page_size", ok_struct, f"status={s} {str(j)[:110]}")

        # --- 25.3 管理操作日志真实落库（非仅查结构）---
        # 设计要点（易混淆，特此注明）：项目有**两套**日志体系——
        #   A. 认证审计日志（login/register/forgot_password…）→ logger.AuditEvent 写 audit-YYYY-MM-DD.log 文件，
        #      由 §18 覆盖；**不进 /api/logs**。
        #   B. 管理员操作日志（修改配置/创建用户/重置密码/切换状态/删除用户/关闭TOTP）→ model.LogAdminAction
        #      写 admin_logs 表，由 /api/logs 暴露，即本节对象。
        s, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "false"}, cookies=scAdm)
        check("25.3 admin 改配置（触发操作日志）→ 200", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/logs?action=" + urllib.parse.quote("修改配置"), cookies=scAdm)
        logs = (j or {}).get("logs") or []
        hit_cfg = any((lg or {}).get("target") == "allow_register" for lg in logs)
        check("25.3 /api/logs?action=修改配置 能查到刚做的 allow_register 变更",
              hit_cfg, f"条数={len(logs)} 样本={str(logs[:1])[:150]}")
        s, _, _, _ = req("PUT", "/api/configs/allow_register", {"value": "true"}, cookies=scAdm)
        check("25.3 还原 allow_register → 200（保持注册开放）", s == 200, f"status={s}")

        # --- 25.4 分页参数钳制（handler 规则：page<1→1；pageSize<1 或 >100→20）---
        s, j, _, _ = req("GET", "/api/logs?page=0&page_size=500", cookies=scAdm)
        check("25.4 page=0→1 且 page_size=500 被钳制到 20（handler 规则）",
              (j or {}).get("page") == 1 and (j or {}).get("page_size") == 20,
              f"page={(j or {}).get('page')} page_size={(j or {}).get('page_size')}")
        s, j, _, _ = req("GET", "/api/logs?page=2&page_size=5", cookies=scAdm)
        check("25.4 合法分页参数原样生效（page=2/page_size=5）",
              (j or {}).get("page") == 2 and (j or {}).get("page_size") == 5,
              f"page={(j or {}).get('page')} page_size={(j or {}).get('page_size')}")
        s, j, _, _ = req("GET", "/api/logs?page=99999", cookies=scAdm)
        # Go nil slice 序列化为 null，故同时接受 null 与空数组
        logs_p = (j or {}).get("logs")
        check("25.4 超大页码 → 200 且空结果（null 或 []，不 5xx）",
              s == 200 and (logs_p is None or logs_p == []), f"status={s} logs={str(logs_p)[:60]}")

        # --- 25.5 用户管理闭环：建 → 禁用 → 登录被拒 → 启用 → 可登录 ---
        au, apw = "adm_target", "AdmTarget@2026"
        req("POST", "/api/auth/register", {"username": au, "password": apw,
                                           "security_question": "q", "security_answer": "a"})
        s, j, _, _ = req("GET", "/api/users", cookies=scAdm)
        tid = next((u.get("id") for u in ((j or {}).get("users") or []) if u.get("username") == au), None)
        check("25.5 admin 能在用户列表中找到目标用户", tid is not None, f"id={tid}")

        if tid:
            s, _, _, _ = req("PUT", f"/api/users/{tid}/status", {"status": "disabled"}, cookies=scAdm)
            check("25.5 admin 禁用用户 → 200", s == 200, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/login", {"username": au, "password": apw})
            check("25.5 被禁用用户登录 → 401", s == 401, f"status={s}")
            s, _, _, _ = req("PUT", f"/api/users/{tid}/status", {"status": "active"}, cookies=scAdm)
            check("25.5 admin 重新启用 → 200", s == 200, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/login", {"username": au, "password": apw})
            check("25.5 重新启用后可登录 → 200", s == 200, f"status={s}")

            # --- 25.6 重置他人密码：新密码可用；重置自己被拒 ---
            newpw = "AdmReset@2026"
            s, _, _, _ = req("PUT", f"/api/users/{tid}/reset-password", {"password": newpw}, cookies=scAdm)
            check("25.6 admin 重置他人密码 → 200", s == 200, f"status={s}")
            s, _, sc_new, _ = req("POST", "/api/auth/login", {"username": au, "password": newpw})
            check("25.6 新密码可登录 → 200", s == 200, f"status={s}")
            s, _, _, _ = req("POST", "/api/auth/login", {"username": au, "password": apw})
            check("25.6 旧密码已失效 → 400/401", s in (400, 401), f"status={s}")
            # v21.94 P3-8：遗忘兜底闭环——被重置用户用新密码登录后可重设安全问题
            # （忘记安全答案的兜底链路：admin 重置密码 → 登录 → 重设问题/密码）
            s, _, _, _ = req("PUT", "/api/auth/security-question",
                {"current_password": newpw, "security_question": "新问题?", "security_answer": "新答案abcdefgh"},
                cookies=sc_new if sc_new else None)
            check("25.6 重置后重设安全问题 → 200（遗忘兜底闭环）", s == 200, f"status={s}")

        s, _, _, _ = req("GET", "/api/users", cookies=scAdm)
        # 管理员自身 ID（smoketest 是首个注册用户 = admin）
        s, j, _, _ = req("GET", "/api/users", cookies=scAdm)
        my_id = next((u.get("id") for u in ((j or {}).get("users") or []) if u.get("username") == "smoketest"), None)
        if my_id:
            s, _, _, _ = req("PUT", f"/api/users/{my_id}/reset-password", {"password": "SelfReset@2026"}, cookies=scAdm)
            check("25.6 admin 重置自己密码 → 400（须走个人中心）", s == 400, f"status={s}")
            s, _, _, _ = req("PUT", f"/api/users/{my_id}/status", {"status": "disabled"}, cookies=scAdm)
            check("25.6 admin 禁用自己 → 400（防自锁）", s == 400, f"status={s}")

        # --- 25.7 唯一管理员约束（回归 v18.4）---
        s, j, _, _ = req("POST", "/api/users", {"username": "adm_two", "password": "Passw0rd!2026", "role": "admin"}, cookies=scAdm)
        check("25.7 创建第二个管理员被拒（唯一 admin 约束）", s in (400, 409), f"status={s} {str(j)[:80]}")

        print("== 26. v21.105 操作逻辑修复（标题原文存取 / 改密不踢会话 / 当前密码错误 400）==")
        # 独立会话（§25 各小节口令状态多变，重新登录消除隐藏状态依赖——附录B 教训）
        s, _, sc26, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("26.0 重新登录成功", s == 200, f"status={s}")

        # --- 26.1 P0-1：标题含 & < > 时原文存取，多次保存不累积转义 ---
        # v21.79~v21.104 缺陷：入库 EscapeString + 前端原样回传 = 每次保存追加一层
        # 转义（"Q&A"→"Q&amp;A"→"Q&amp;amp;A"…）。v21.105 起落库保持原文。
        title105 = 'Q&A <测试> & "引号"'
        s, j, _, _ = req("POST", "/api/notes", {"title": title105, "content": "<p>v1</p>"}, cookies=sc26)
        nid105 = (j or {}).get("id")
        check("26.1 特殊字符标题创建成功", s in (200, 201) and bool(nid105), f"status={s}")
        check("26.1 标题落库保持原文（不再转义）", (j or {}).get("title") == title105, repr((j or {}).get("title")))
        # 模拟前端行为：读回标题原样带回再保存两轮，验证无累积
        for rnd in (2, 3):
            s, j, _, _ = req("GET", f"/api/notes/{nid105}", cookies=sc26)
            cur_title, cur_ver = (j or {}).get("title"), (j or {}).get("version")
            s, j, _, _ = req("PUT", f"/api/notes/{nid105}",
                             {"title": cur_title, "content": f"<p>v{rnd}</p>", "version": cur_ver}, cookies=sc26)
            check(f"26.1 第{rnd}轮保存成功", s == 200, f"status={s}")
        s, j, _, _ = req("GET", f"/api/notes/{nid105}", cookies=sc26)
        check("26.1 多次保存后标题仍为原文（无累积转义）", (j or {}).get("title") == title105, repr((j or {}).get("title")))
        req("DELETE", f"/api/notes/{nid105}", cookies=sc26)  # 清理

        # --- 26.2 P0-3：改密时当前密码错误 → 400（不再 401 触发前端会话过期链路） ---
        s, j, _, _ = req("POST", "/api/auth/change-password",
                         {"current_password": "WrongPass!1", "new_password": "Passw0rd!2027"}, cookies=sc26)
        check("26.2 当前密码错误返回 400（P0-3，非 401）",
              s == 400 and (j or {}).get("error") == "当前密码错误", f"status={s} {j}")
        s, j, _, _ = req("GET", "/api/notes", cookies=sc26)
        check("26.2 密码错误后会话不受影响（未被踢）", s == 200, f"status={s}")

        # --- 26.3 P0-2：改密成功响应体携带新 token 对，新会话立即可用 ---
        s, j, sc26n, _ = req("POST", "/api/auth/change-password",
                             {"current_password": "Passw0rd!2026", "new_password": "Passw0rd!2027"}, cookies=sc26)
        check("26.3 改密成功 200", s == 200, f"status={s}")
        check("26.3 响应体含 access_token（P0-2）", bool((j or {}).get("access_token")), str(j)[:80])
        check("26.3 响应体含 refresh_token（P0-2）", bool((j or {}).get("refresh_token")), str(j)[:80])
        # 模拟前端同步新 token 后的行为：用响应下发的新 cookie 直接请求业务接口
        sc26.update(sc26n or {})
        s, j, _, _ = req("GET", "/api/notes", cookies=sc26)
        check("26.3 新会话立即可用（不被踢登录页）", s == 200, f"status={s}")
        # 全端下线语义保留：改密前的旧 access 已被 token_version 吊销
        s2, _, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2027"})
        check("26.3 新密码可登录", s2 == 200, f"status={s2}")

        # --- 26.4 还原管理员密码（消除隐藏状态依赖，附录B v21.91 教训） ---
        s, _, _, _ = req("POST", "/api/auth/change-password",
                         {"current_password": "Passw0rd!2027", "new_password": "Passw0rd!2026"}, cookies=sc26)
        check("26.4 还原管理员密码 → 200", s == 200, f"status={s}")
        s, _, _, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("26.4 还原后原密码可登录", s == 200, f"status={s}")

        print("== 27. v21.107 多入口一致性与文案透传（上传链路分级/恢复去向/public-config）==")
        s, _, sc27, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("27.0 登录成功", s == 200, f"status={s}")
        png_head = b"\x89PNG\r\n\x1a\n"

        # --- 27.1 P1-13 上传网关独立组：16~30MB 合法尺寸放行、>30MB 业务文案可达、超网关 413 有体 ---
        s27a, _ = upload_raw("big20.png", png_head + b"A" * (20 * 1024 * 1024), sc27)
        check("27.1 20MB 图片上传成功(网关放宽至 UPLOAD_MAX_MB+4MB)", s27a == 200, f"status={s27a}")
        s27b, jb = upload_raw("big33.png", png_head + b"A" * (33 * 1024 * 1024), sc27)
        check("27.1 33MB → 400 业务大小限制文案可达", s27b == 400 and "超过大小限制" in str((jb or {}).get("error", "")), f"status={s27b} {str(jb)[:60]}")
        s27c, jc = upload_raw("big36.png", png_head + b"A" * (36 * 1024 * 1024), sc27)
        check("27.1 36MB → 413 网关兜底明确文案", s27c == 413 and "接收上限" in str((jc or {}).get("error", "")), f"status={s27c} {str(jc)[:60]}")

        # --- 27.2 P2-9 恢复响应携带去向文件夹 ---
        s, j, _, _ = req("POST", "/api/notes", {"title": "恢复去向27", "content": "<p>x</p>"}, cookies=sc27)
        nid27 = (j or {}).get("id")
        req("DELETE", f"/api/notes/{nid27}", cookies=sc27)
        s, j, _, _ = req("POST", f"/api/notes/{nid27}/restore", {}, cookies=sc27)
        check("27.2 恢复响应含 folder 去向字段", s == 200 and "folder" in (j or {}), f"status={s} {str(j)[:60]}")

        # --- 27.3 P2-8 public-config 对普通用户下发回收站保留天数 ---
        s, j, _, _ = req("GET", "/api/public-config")
        check("27.3 public-config 含 trash_retention_days", s == 200 and "trash_retention_days" in (j or {}), f"status={s} {str(j)[:80]}")

        print("== 28. v21.108 锁定剩余时长 / remember 轮转语义 / 标签保存链路 ==")
        # --- 28.1 P1-11：登录连续失败锁定后，提示携带剩余分钟（专用用户；本小节位于末段，锁定不影响他项）---
        for _i in range(5):
            req("POST", "/api/auth/login", {"username": "lockprobe", "password": "WrongPass!9"})
        s, j, _, _ = req("POST", "/api/auth/login", {"username": "lockprobe", "password": "WrongPass!9"})
        err28 = str((j or {}).get("error", ""))
        check("28.1 第6次被锁 429", s == 429, f"status={s}")
        check("28.1 锁定提示含剩余分钟（请在 N 分钟后再试）", "请在" in err28 and "分钟后再试" in err28, err28[:60])

        # --- 28.2 P1-9：remember=false 登录与 refresh 轮转，refresh cookie 全程会话级（无 Max-Age）---
        s, j, ck28, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026", "remember": False}, raw=True)
        raws = ck28.get("__raw__", []) if ck28 else []
        ref_cookies = [r for r in raws if r.startswith("refresh_token=")]
        check("28.2a 登录(remember=false) refresh cookie 会话级", s == 200 and bool(ref_cookies) and all("max-age" not in r.lower() for r in ref_cookies), str(ref_cookies)[:80])
        refval = ref_cookies[0].split(";")[0].split("=", 1)[1] if ref_cookies else ""
        s, j2, ck28b, _ = req("POST", "/api/auth/refresh", {"refresh_token": refval}, raw=True)
        raws2 = ck28b.get("__raw__", []) if ck28b else []
        ref2 = [r for r in raws2 if r.startswith("refresh_token=")]
        check("28.2b 轮转继承 remember=false（旧实现会被升级为 30 天持久 cookie）",
              s == 200 and bool(ref2) and all("max-age" not in r.lower() for r in ref2), str(ref2)[:80])

        # --- 28.3 P1-14：标签保存链路（前端新入口对应的后端能力回归）---
        s, _, sc28, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        s, j, _, _ = req("POST", "/api/notes", {"title": "标签链路28", "content": "<p>t</p>", "tags": ""}, cookies=sc28)
        nid28 = (j or {}).get("id")
        s, j, _, _ = req("GET", f"/api/notes/{nid28}", cookies=sc28)
        v28 = (j or {}).get("version")
        s, j, _, _ = req("PUT", f"/api/notes/{nid28}", {"title": "标签链路28", "content": "<p>t</p>", "tags": "v21108标签,第二个", "version": v28}, cookies=sc28)
        check("28.3a 保存携带 tags → 200", s == 200, f"status={s}")
        s, j, _, _ = req("GET", "/api/notes/tags", cookies=sc28)
        tags28 = (j or {}).get("tags") or []
        check("28.3b 标签聚合接口可见新标签", "v21108标签" in tags28 and "第二个" in tags28, str(tags28)[:80])
        req("DELETE", f"/api/notes/{nid28}", cookies=sc28)

        # --- 29 v21.111：GFM 任务清单导入直构 + 导出回环（gomarkdown 无 task-list 扩展的挂账修复）---
        def import_md(filename, text, cookies):
            boundary = "----smoketask151"
            body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
                    f"Content-Type: text/markdown\r\n\r\n").encode() + text.encode("utf-8") + f"\r\n--{boundary}--\r\n".encode()
            conn = http.client.HTTPConnection(BASE, timeout=15)
            conn.request("POST", "/api/notes/import", body, {
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

        def export_md(nid, cookies):
            conn = http.client.HTTPConnection(BASE, timeout=15)
            conn.request("POST", f"/api/notes/{nid}/export", json.dumps({}), {
                "Content-Type": "application/json",
                "Cookie": "; ".join(f"{k}={v}" for k, v in cookies.items()),
            })
            r = conn.getresponse()
            d = r.read().decode("utf-8", "replace")
            conn.close()
            return r.status, d

        s, j = import_md("task151.md", "# 周报\n\n- [x] 完成评审\n  - [ ] 补文档\n- [ ] 写周报\n", sc28)
        nid29 = (j or {}).get("id")
        c29 = ""
        if nid29:
            _, j29, _, _ = req("GET", f"/api/notes/{nid29}", cookies=sc28)
            c29 = (j29 or {}).get("content", "")
        check("29.1 导入 md 任务清单直构 taskList+data-checked（含嵌套）",
              s == 201 and nid29 and 'data-type="taskList"' in c29 and 'data-checked="true"' in c29 and 'data-checked="false"' in c29,
              f"status={s} len={len(c29)}")
        s29e, md29 = export_md(nid29, sc28)
        check("29.2 导出回环 md 含 [x]/[ ] 与嵌套文本", s29e == 200 and "- [x]" in md29 and "- [ ]" in md29 and "补文档" in md29, f"status={s29e}")
        s, j = import_md("plain151.md", "- 普通项一\n- 普通项二\n", sc28)
        c29p = ""
        if (j or {}).get("id"):
            _, jp, _, _ = req("GET", f"/api/notes/{(j or {}).get('id')}", cookies=sc28)
            c29p = (jp or {}).get("content", "")
            req("DELETE", f"/api/notes/{(j or {}).get('id')}", cookies=sc28)
        check("29.3 普通列表导入不误转 taskList", s == 201 and 'data-type="taskList"' not in c29p and "普通项一" in c29p, f"status={s}")
        req("DELETE", f"/api/notes/{nid29}", cookies=sc28)

        # == 30. v21.112 backup-db CLI 无副作用（P1 回归：不得经 config.Load 建 ./data）==
        # 用空 cwd 探针（模拟 fnOS 钩子 cwd 语义）：回归时旧实现会在空 cwd 下 mkdir ./data
        # 注意：BIN 可能是相对路径（CI 传 server/sticky-note）——chdir 前先转绝对
        print("\n== 30. backup-db CLI（P1 回归）==")
        bin_abs = os.path.abspath(BIN)
        bk_target = os.path.join(tempfile.gettempdir(), f"sn152_backup_{os.getpid()}.db")
        src_db = os.path.join(data_dir, "notepad.db")
        cwd0 = os.getcwd()
        probe_dir = tempfile.mkdtemp(prefix="sn152_cwd_probe_")
        os.chdir(probe_dir)
        p30 = subprocess.run([bin_abs, "backup-db", bk_target, src_db], capture_output=True, text=True, timeout=60)
        os.chdir(cwd0)
        check("30.1 backup-db rc=0 且 Backup created（WAL 活跃库可在线快照）",
              p30.returncode == 0 and "Backup created" in (p30.stdout or ""),
              f"rc={p30.returncode} out={(p30.stdout or '').strip()[:60]} err={(p30.stderr or '').strip()[:80]}")
        check("30.2 快照文件生成非空", os.path.exists(bk_target) and os.path.getsize(bk_target) > 0)
        check("30.3 空 cwd 探针无 ./data 产生（P1 锁：不调用 config.Load）",
              not os.path.exists(os.path.join(probe_dir, "data")),
              f"probe_dir={probe_dir}")
        shutil.rmtree(probe_dir, ignore_errors=True)
        try:
            os.remove(bk_target)
        except OSError:
            pass

        # == 31. v1.0.167 系统备份 API（WebDAV，切片 2）==
        print("\n== 31. 系统备份 API ==")
        # 早期 logout 已 bump token_version 使旧 sc 失效；此处现取一个全新管理员会话。
        s, j, sca, _ = req("POST", "/api/auth/login", {"username": "smoketest", "password": "Passw0rd!2026"})
        check("31.0 管理员重新登录取会话", s == 200 and sca, f"status={s}")
        # 31.1 未认证 → 401（备份 API 全在 RequireAdmin 组）
        s, j, _, _ = req("GET", "/api/backup/status")
        check("31.1 未认证访问备份 API → 401", s == 401, f"status={s}")
        # 31.2 管理员 status 200 且含 bool running
        s, j, _, _ = req("GET", "/api/backup/status", cookies=sca)
        check("31.2 管理员 status 200 含 running(bool)",
              s == 200 and isinstance((j or {}).get("running"), bool), f"status={s} {j}")
        # 31.3 创建带明文密码的目标 → 201 + id
        s, j, _, _ = req("POST", "/api/backup/targets", {
            "name": "坚果云-冒烟", "type": "webdav", "base_url": "https://dav.jianguoyun.com/dav/",
            "username": "me", "password": "SuperSecret123!", "remote_path": "/snbk/",
            "enabled": False, "retention_count": 7, "schedule_interval": "24h"}, cookies=sca)
        tid31 = (j or {}).get("id")
        check("31.3 创建备份目标 201", s == 201 and tid31, f"status={s} {j}")
        # 31.4 列表脱敏：不回显明文密码/密文/CA；has_secret=true
        s, j, _, _ = req("GET", "/api/backup/targets", cookies=sca)
        body_txt = json.dumps(j or {}, ensure_ascii=False)
        tgt31 = next((t for t in (j or {}).get("targets", []) if t.get("id") == tid31), {})
        check("31.4 目标列表脱敏（无明文密码/secret_encrypted/custom_ca_pem，has_secret=true）",
              s == 200 and "SuperSecret123!" not in body_txt and "secret_encrypted" not in body_txt
              and "custom_ca_pem" not in body_txt and tgt31.get("has_secret") is True, body_txt[:80])
        # 31.5 测试连接：无网/错凭据均结构化 ok:false，不得 500
        s, j, _, _ = req("POST", f"/api/backup/targets/{tid31}/test", cookies=sca)
        check("31.5 测试连接结构化 ok:false（不抛 500）",
              s == 200 and (j or {}).get("ok") is False, f"status={s} {j}")
        # 31.6 SSRF：云元数据地址被护栏拒（ok:false，非 500）
        s, j, _, _ = req("POST", "/api/backup/targets", {
            "name": "meta", "type": "webdav", "base_url": "http://169.254.169.254/",
            "username": "u", "password": "p", "remote_path": "/", "enabled": False,
            "retention_count": 7, "schedule_interval": "24h"}, cookies=sca)
        meta_id = (j or {}).get("id")
        s, j, _, _ = req("POST", f"/api/backup/targets/{meta_id}/test", cookies=sca)
        check("31.6 SSRF 元数据地址测试被拒（ok:false）",
              s == 200 and (j or {}).get("ok") is False, f"status={s} {j}")
        # 31.7 主密钥导出：错口令 → 403（绝不回显密钥）
        s, j, _, _ = req("POST", "/api/backup/key/export", {"password": "definitely-wrong"}, cookies=sca)
        check("31.7 主密钥导出错口令 → 403", s == 403, f"status={s}")
        # 31.8 非管理员访问备份 API → 403
        req("POST", "/api/users", {"username": "bkuser31", "password": "Passw0rd!2026", "role": "user"}, cookies=sca)
        s, _, scu, _ = req("POST", "/api/auth/login", {"username": "bkuser31", "password": "Passw0rd!2026"})
        s, _, _, _ = req("GET", "/api/backup/status", cookies=scu)
        check("31.8 非管理员访问备份 API → 403", s == 403, f"status={s}")
        # 31.9 删除备份目标（不删远端）→ 200
        s, _, _, _ = req("DELETE", f"/api/backup/targets/{meta_id}", cookies=sca)
        check("31.9 删除备份目标 200", s == 200, f"status={s}")
        req("DELETE", f"/api/backup/targets/{tid31}", cookies=sca)
        # 31.10 快照列表端点契约：200 + 含 snapshots 键（Go nil 切片序列化为 null，前端 `|| []` 兜底；
        #        has_remote 字段由 Go 测 + E2E §34 覆盖）
        s, j, _, _ = req("GET", "/api/backup/snapshots", cookies=sca)
        check("31.10 快照列表 200 且含 snapshots 键",
              s == 200 and isinstance(j, dict) and "snapshots" in j, f"status={s}")

        # == 32. v1.0.168 在线恢复 API 守卫（非破坏性；成功恢复的破坏性 e2e 由 Go 测试覆盖）==
        print("\n== 32. 在线恢复 API ==")
        # 32.1 未认证 → 401
        s, j, _, _ = req("POST", "/api/backup/restore", {"backup_id": "x", "password": "y", "confirm": True})
        check("32.1 未认证触发恢复 → 401", s == 401, f"status={s}")
        # 32.2 管理员缺 confirm → 400
        s, j, _, _ = req("POST", "/api/backup/restore", {"backup_id": "x", "password": "Passw0rd!2026"}, cookies=sca)
        check("32.2 恢复缺显式确认 → 400", s == 400, f"status={s} {j}")
        # 32.3 错口令 + confirm → 403（口令二次校验先于任何取档/破坏）
        s, j, _, _ = req("POST", "/api/backup/restore", {"backup_id": "x", "password": "wrong-pw-zzz", "confirm": True}, cookies=sca)
        check("32.3 恢复口令错误 → 403", s == 403, f"status={s}")
        # 32.4 恢复进度端点：200 且含 phase 字段
        s, j, _, _ = req("GET", "/api/backup/restore/progress", cookies=sca)
        check("32.4 恢复进度端点含 phase", s == 200 and "phase" in (j or {}), f"status={s} {j}")
        # 32.5 正常期 /health healthy（恢复安全窗口未激活，RestorationGate 放行）
        s, j, _, _ = req("GET", "/health")
        check("32.5 正常期 /health healthy", s == 200 and (j or {}).get("status") == "healthy", f"status={s} {j}")

        # == 33. 批次5 P5 Bootstrap 快照 API（非破坏；投影/搬运正确性由 Go 集成测试钉）==
        print("\n== 33. Bootstrap 快照 API ==")
        # 33.1 未认证 → 401
        s, _, _, _ = req("POST", "/api/sync/snapshot")
        check("33.1 快照创建未认证 401", s == 401, f"status={s}")
        # 33.2 本人创建 → 202 + snapshot_id
        s, j, _, _ = req("POST", "/api/sync/snapshot", {}, cookies=sca)
        sid33 = (j or {}).get("snapshot_id")
        check("33.2 快照创建 202+snapshot_id", s == 202 and bool(sid33), f"status={s} {j}")
        # 33.3 轮询至 READY（≤30s）且元数据齐备
        import time as _t33
        st33, ready33 = None, False
        for _ in range(60):
            s, st33, _, _ = req("GET", f"/api/sync/snapshot/{sid33}", cookies=sca)
            if (st33 or {}).get("status") in ("READY", "FAILED"):
                ready33 = (st33 or {}).get("status") == "READY"
                break
            _t33.sleep(0.5)
        check("33.3 快照轮询至 READY", ready33, f"st={st33}")
        check("33.3b READY 含 sha256/watermark", bool((st33 or {}).get("sha256")) and "watermark" in (st33 or {}), f"{st33}")
        # 33.4 下载 → 200 + zip 魔数（PK）+ X-Snapshot-Sha256 头
        s, _, _, raw33 = req("GET", f"/api/sync/snapshot/{sid33}/download", cookies=sca, raw=True)
        check("33.4 快照下载 zip 魔数", s == 200 and (raw33 or b"")[:2] == b"PK" and len(raw33 or b"") > 100,
              f"status={s} len={len(raw33 or b'')}")
        # 33.5 未知 id → 404
        s, _, _, _ = req("GET", "/api/sync/snapshot/snap-deadbeef00000000", cookies=sca)
        check("33.5 未知快照 404", s == 404, f"status={s}")

        # ================= §34 大载荷 blob_ref 通道（P1-b，§32~36）=================
        # 覆盖：>4MB 笔记 ChangeLog 分流（blob_ref 行 + 文件 sha256=ref）→ 下载端点
        # 授权面（§35：属主 200/未引用 404/非 hex 404）→ ≤4MB 保持内联。
        import hashlib as _h34
        import sqlite3 as _sq34
        print("[34] 大载荷 blob_ref 通道")
        big34 = "P" * (4 * 1024 * 1024 + 1024)
        s, j34, _, _ = req("POST", "/api/notes", {"title": "p1b-big", "content": big34}, cookies=sca)
        nid34 = (j34 or {}).get("id")
        check("34.1 >4MB 笔记创建 201", s == 201 and bool(nid34), f"status={s}")
        _db34 = _sq34.connect(os.path.join(data_dir, "notepad.db"))
        try:
            _row34 = _db34.execute(
                "SELECT payload_kind, payload_ref, payload_size, payload_json FROM sync_changes "
                "WHERE entity_type='NOTE' AND entity_sync_id=(SELECT sync_id FROM notes WHERE id=?)", (nid34,)).fetchone()
            check("34.2 ChangeLog 落 blob_ref 行", bool(_row34) and _row34[0] == "blob_ref" and bool(_row34[1]) and _row34[2] > 0 and _row34[3] == "",
                  f"kind={_row34[0] if _row34 else None} ref={(_row34[1][:12] + '…') if _row34 and _row34[1] else None} size={_row34[2] if _row34 else None}")
            ref34 = _row34[1] if _row34 else ""
            size34 = _row34[2] if _row34 else 0
            check("34.3 payload_ref 形状 hex64", len(ref34) == 64 and all(ch in "0123456789abcdef" for ch in ref34), ref34[:16])
            _pfile = os.path.join(data_dir, "sync-payloads", ref34)
            _pbytes = b""
            if os.path.exists(_pfile):
                with open(_pfile, "rb") as _pf:
                    _pbytes = _pf.read()
            check("34.4 blob 文件在位且 sha256=ref、size 对拍",
                  len(_pbytes) == size34 and _h34.sha256(_pbytes).hexdigest() == ref34,
                  f"file={len(_pbytes)} row={size34}")
            # 下载端点：属主 200 + 内容逐字节同源（§34 唯一读取通道）。
            s, _, _, raw34 = req("GET", f"/api/sync/payload/{ref34}", cookies=sca, raw=True)
            check("34.5 端点下载 200 且内容 sha256=ref",
                  s == 200 and _h34.sha256(raw34 or b"").hexdigest() == ref34, f"status={s} len={len(raw34 or b'')}")
            # §35 守卫：未引用 hash / 非 hex 形状一律 404（不向探测者泄露区分度）。
            s, _, _, _ = req("GET", "/api/sync/payload/" + "cd" * 32, cookies=sca)
            check("34.6 未引用 hash 404（不能只因知道 hash 就读取）", s == 404, f"status={s}")
            s, _, _, _ = req("GET", "/api/sync/payload/not-a-hash-shape", cookies=sca)
            check("34.7 非 hex 形状 404", s == 404, f"status={s}")
        finally:
            _db34.close()
        # ≤4MB 小载荷保持内联（回归对照：分流不误伤常规事件）。
        s, j34b, _, _ = req("POST", "/api/notes", {"title": "p1b-small", "content": "<p>small-34</p>"}, cookies=sca)
        nid34b = (j34b or {}).get("id")
        _db34b = _sq34.connect(os.path.join(data_dir, "notepad.db"))
        try:
            _row34b = _db34b.execute(
                "SELECT payload_kind FROM sync_changes WHERE entity_type='NOTE' AND entity_sync_id=(SELECT sync_id FROM notes WHERE id=?)", (nid34b,)).fetchone()
            check("34.8 小载荷保持内联 json", bool(_row34b) and _row34b[0] == "json", f"kind={_row34b[0] if _row34b else None}")
        finally:
            _db34b.close()

        # ------------------------------------------------------------------
        # §35 统一网关模式双监听（v2.2.0，方案 §3.1）：-socket 进程级生效 + TCP 双轨并存 +
        # socket 文件产生 + 残留自愈。socket→handler 全链由 Go 单测 TestSocketHTTPRoundTrip
        # 钉死；本节钉「双监听并存不互相破坏 + 启动/退出 socket 文件生命周期」。
        # ------------------------------------------------------------------
        print("== 35. 统一网关模式双监听（v2.2.0）==")
        sock_path = os.path.join(data_dir, "gw.sock")
        dd35 = tempfile.mkdtemp(prefix="sticky-note-smoke-gw-")
        env35 = dict(os.environ)
        env35.pop("JWT_SECRET", None)
        env35["NOTEPAD_DISABLE_RATE_LIMIT"] = "1"
        args35 = [BIN, "-port=18905", "-socket=" + sock_path, "-web-dir=" + WEB,
                  "-data-dir=" + dd35, "-upload-dir=" + os.path.join(dd35, "upload")]

        def _spawn35():
            return subprocess.Popen(args35, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env35)

        def _health35():
            c = http.client.HTTPConnection("127.0.0.1:18905", timeout=5)
            try:
                c.request("GET", "/api/health")
                r = c.getresponse()
                r.read()
                return r.status
            finally:
                c.close()

        p35 = _spawn35()
        try:
            up35 = False
            for _ in range(50):
                try:
                    if _health35() == 200:
                        up35 = True
                        break
                except Exception:
                    pass
                time.sleep(0.2)
            check("35.1 socket 模式实例 TCP /api/health 200（双轨并存）", up35)
            check("35.2 socket 文件已创建", os.path.exists(sock_path), sock_path)
            if hasattr(socket, "AF_UNIX") and sys.platform != "win32":
                try:
                    s35 = _unix_probe_health(sock_path)
                    check("35.3 经 socket 直探 /api/health 200", s35 == 200, f"status={s35}")
                except Exception as e35:
                    check("35.3 经 socket 直探 /api/health 200", False, repr(e35))
            else:
                print("  SKIP  35.3 socket 直探（本平台 Python 无 AF_UNIX；Go 单测 TestSocketHTTPRoundTrip 已覆盖全链）")
            if sys.platform != "win32":
                p35.terminate()
                try:
                    p35.wait(timeout=10)
                except Exception:
                    p35.kill()
                check("35.4 SIGTERM 优雅退出后 socket 文件被清理", not os.path.exists(sock_path))
                open(sock_path, "wb").close()  # 预置假残留（异常退出同型占位）
                p35 = _spawn35()
                up35b = False
                for _ in range(50):
                    try:
                        if _health35() == 200:
                            up35b = True
                            break
                    except Exception:
                        pass
                    time.sleep(0.2)
                check("35.5 残留 socket 文件启动自愈（同路径重启 TCP 200）", up35b)
                p35.terminate()
                try:
                    p35.wait(timeout=10)
                except Exception:
                    p35.kill()
                check("35.6 二次优雅退出 socket 清理", not os.path.exists(sock_path))
            else:
                p35.kill()
                p35.wait(timeout=10)
                check("35.4 强杀后 socket 文件残留（TerminateProcess 无优雅路径，预期）", os.path.exists(sock_path))
                p35 = _spawn35()
                up35b = False
                for _ in range(50):
                    try:
                        if _health35() == 200:
                            up35b = True
                            break
                    except Exception:
                        pass
                    time.sleep(0.2)
                check("35.5 残留 socket 文件启动自愈（同路径重启 TCP 200）", up35b)
        finally:
            if p35.poll() is None:
                p35.kill()
                try:
                    p35.wait(timeout=5)
                except Exception:
                    pass
            shutil.rmtree(dd35, ignore_errors=True)

    finally:
        for k in ("LOGIN_LOCK_SECONDS",):
            os.environ.pop(k, None)
        stop(srv)
        shutil.rmtree(data_dir, ignore_errors=True)

    print(f"\nRESULT: {len(passed)} passed, {len(failed)} failed")
    if failed:
        print("FAILED:", failed)
        sys.exit(1)

if __name__ == "__main__":
    main()
