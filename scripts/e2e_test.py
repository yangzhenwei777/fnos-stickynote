#!/usr/bin/env python3
"""
e2e_test.py — 便签关键链路浏览器端到端测试（Playwright）
使用方法:
    python e2e_test.py <sticky-note_exe> <dist_dir>
示例:
    python scripts/e2e_test.py source/notepad-src/server/sticky-note-test.exe source/notepad-src/web/dist
"""

import sys
import os
import time
import subprocess
import urllib.request
import urllib.error
import urllib.parse
import shutil
import tempfile
import atexit
import json
import base64
import hmac
import hashlib
import struct
import sqlite3

# ---------------------------------------------------------------------------
# Playwright
# ---------------------------------------------------------------------------
try:
    from playwright.sync_api import sync_playwright
except ImportError:
    print("[SKIP] playwright not installed. Run: pip install playwright && playwright install chromium")
    sys.exit(0)


def totp_code(secret, interval=30, digits=6):
    """标准 TOTP（RFC 6238）生成器，用于 E2E 开启两步验证（v21.58 A-2）。"""
    key = base64.b32decode(secret.upper() + "=" * (-len(secret) % 8))
    counter = int(time.time()) // interval
    msg = struct.pack(">Q", counter)
    digest = hmac.new(key, msg, hashlib.sha1).digest()
    offset = digest[-1] & 0x0F
    code = (struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7FFFFFFF) % (10 ** digits)
    return str(code).zfill(digits)


def main():
    if len(sys.argv) < 3:
        print(f"Usage: python {sys.argv[0]} <sticky-note_exe> <dist_dir>")
        sys.exit(1)

    exe = os.path.abspath(sys.argv[1])
    dist = os.path.abspath(sys.argv[2])
    # Find a free port
    import socket
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    http_base = f"http://127.0.0.1:{port}"

    if not os.path.exists(exe):
        print(f"ERROR: exe not found: {exe}")
        sys.exit(2)
    if not os.path.isdir(dist):
        print(f"ERROR: dist dir not found: {dist}")
        sys.exit(2)

    # Start server
    data_dir = tempfile.mkdtemp(prefix="e2e_sticky_note_")
    env = os.environ.copy()
    env["PORT"] = str(port)
    env["DATA_DIR"] = data_dir
    env["GIN_MODE"] = "release"

    # Start server with cwd set to the binary's directory
    # v21.79 修复（CI E2E 首次执行暴露）：必须显式传 -web-dir 指向真实前端 dist。
    # 否则 server 走 config 默认 webDir="./www"（不存在）→ 回退 go:embed 的占位 dist
    # （backend job 编译时 prepare embed placeholder 生成的空 index.html）→
    # 页面无输入框 → wait_for_selector 30s 超时。与 smoke_test.py 传参方式保持一致。
    exe_dir = os.path.dirname(exe)
    print(f"[SERVER] Starting {exe}")
    proc = subprocess.Popen(
        [exe, f"-web-dir={dist}"], env=env, cwd=exe_dir,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    def _cleanup():
        proc.terminate()
        try: proc.wait(timeout=10)
        except subprocess.TimeoutExpired: proc.kill()
        shutil.rmtree(data_dir, ignore_errors=True)
    atexit.register(_cleanup)

    # Wait for server
    print("[WAIT] Waiting for server...")
    for _ in range(60):
        try:
            urllib.request.urlopen(f"{http_base}/health", timeout=1)
            break
        except (urllib.error.URLError, OSError):
            time.sleep(0.5)
    else:
        print("ERROR: Server did not start")
        proc.terminate()
        sys.exit(3)

    passed = 0
    failed = 0
    total = 0
    skipped = 0

    def t(desc, condition, detail=None):
        nonlocal passed, failed, total
        total += 1
        if condition:
            passed += 1
            print(f"  PASS  {desc}")
        else:
            failed += 1
            if detail:
                print(f"  FAIL  {desc}  << {detail}")
            print(f"  FAIL  {desc}")

    def skip(desc):
        """显式跳过：不计入 total/passed/failed（不污染通过率）。
        T-2 审计整改：原实现用 t(..., True) 虚记 PASS，使被跳过用例
        计入通过率、掩盖真实覆盖缺口。改为显式 SKIP。"""
        nonlocal skipped
        skipped += 1
        print(f"  SKIP  {desc}")

    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)

        def wait(ms=600):
            time.sleep(ms / 1000)

        # -------------------------------------------------------------------
        # 1. First-admin setup (no users → auto redirect to setup page)
        # -------------------------------------------------------------------
        print("\n[E2E] 注册管理员（首次设置）...")
        page = browser.new_page()
        page.set_default_timeout(30000)
        page.goto(f"{http_base}/register?setup=true", wait_until="networkidle")
        page.wait_for_selector('input[placeholder*="管理员用户名"]')
        page.fill('input[placeholder*="管理员用户名"]', "admin")
        page.fill('input[placeholder*="管理员密码"]', "Admin@123456")
        page.fill('input[placeholder*="请再次输入密码"]', "Admin@123456")
        page.fill('input[placeholder*="安全问题"]', "我的宠物叫什么")
        page.fill('input[placeholder="安全答案"]', "旺财")
        page.click('button:has-text("设置管理员")')
        # After registration, app redirects to / (authenticated → notes page)
        page.wait_for_url(f"{http_base}/", timeout=15000)
        t("管理员设置成功 → 进入主页", "新建笔记" in page.content())
        page.close()

        # -------------------------------------------------------------------
        # 2. Login
        # -------------------------------------------------------------------
        print("\n[E2E] 登录...")
        page = browser.new_page()
        page.set_default_timeout(30000)
        page.goto(f"{http_base}/login")
        page.wait_for_selector('input[placeholder="请输入用户名"]')
        page.fill('input[placeholder="请输入用户名"]', "admin")
        page.fill('input[placeholder="请输入密码"]', "Admin@123456")
        page.click('button:has-text("登录")')
        page.wait_for_url(f"{http_base}/", timeout=15000)
        t("登录成功 → 进入主页", "新建笔记" in page.content())

        # -------------------------------------------------------------------
        # 3. Create note
        # -------------------------------------------------------------------
        print("\n[E2E] 创建笔记...")
        page.click('button:has-text("新建笔记")')
        wait(1000)
        # Wait for Tiptap editor
        page.wait_for_selector('.tiptap')
        editor = page.query_selector('.tiptap')
        editor.click()
        page.keyboard.type("这是一篇端到端测试笔记的内容")
        # Set title (Element Plus puts class on wrapper div, not the input)
        title_el = page.query_selector('input[placeholder="笔记标题"]')
        if title_el:
            title_el.fill("E2E测试笔记")
        wait(3000)  # Wait for auto-save (longer patience)

        # Verify in sidebar
        page.goto(f"{http_base}/")
        page.wait_for_selector('button:has-text("新建笔记")')
        wait(1000)
        t("笔记已创建（侧栏可见）", "E2E测试笔记" in page.content())

        # -------------------------------------------------------------------
        # 4. Encrypt note
        # -------------------------------------------------------------------
        print("\n[E2E] 加密笔记...")
        # Click note in sidebar
        note_el = page.query_selector('text=E2E测试笔记')
        if note_el:
            note_el.click()
        wait(800)

        # Click encrypt item in【更多】dropdown（v21.81 预览重设计：加密/解密/删除/下载移入
        # 标题行右侧【更多】下拉菜单，原独立按钮取消——断言随拍板交互同步更新，纪律 4.5）
        def open_more_menu():
            btn = page.query_selector('.view-actions .more-btn')
            if not btn:
                return False
            btn.click()
            wait(600)
            return True

        encrypt_btn = None
        if open_more_menu():
            encrypt_btn = page.query_selector('.el-dropdown-menu__item:has-text("加密")')
        if not encrypt_btn:
            t("加密按钮存在", False)
        else:
            encrypt_btn.click()
            wait(800)
            # 加密弹框含「密码 + 确认密码」两个输入框
            pw_fields = page.query_selector_all('.el-overlay input[type="password"], .el-dialog input[type="password"]')
            if len(pw_fields) < 2:
                t("加密密码+确认密码输入框存在", False)
            else:
                pw_fields[0].fill("encrypt123")
                pw_fields[1].fill("encrypt123")
                page.click('.el-overlay button:has-text("确认"), .el-dialog button:has-text("确认")')
                wait(1000)
                # 加密成功 → 更多菜单出现「解密」项（is_encrypted=true）
                ok = open_more_menu() and page.query_selector('.el-dropdown-menu__item:has-text("解密")') is not None
                t("加密后出现解密按钮（is_encrypted=true）", ok)

        # -------------------------------------------------------------------
        # 5. Decrypt note
        # -------------------------------------------------------------------
        print("\n[E2E] 解密验证...")
        page.goto(f"{http_base}/")
        page.wait_for_selector('button:has-text("新建笔记")', timeout=5000)
        note_el = page.query_selector('text=E2E测试笔记')
        if note_el:
            note_el.click()
        wait(800)

        decrypt_btn = None
        if open_more_menu():
            decrypt_btn = page.query_selector('.el-dropdown-menu__item:has-text("解密")')
        if not decrypt_btn:
            t("解密按钮存在", False)
        else:
            decrypt_btn.click()
            wait(800)
            pw_field = page.wait_for_selector('.el-overlay input[type="password"], .el-dialog input[type="password"]')
            if not pw_field:
                t("解密密码输入框存在", False)
            else:
                pw_field.fill("encrypt123")
                page.click('.el-overlay button:has-text("确认"), .el-dialog button:has-text("确认")')
                wait(1000)
                # 解密成功 → 更多菜单出现「加密」项（is_encrypted=false）
                ok = open_more_menu() and page.query_selector('.el-dropdown-menu__item:has-text("加密")') is not None
                t("解密后出现加密按钮（is_encrypted=false）", ok)

        # -------------------------------------------------------------------
        # 6. Search
        # -------------------------------------------------------------------
        print("\n[E2E] 搜索测试...")
        page.goto(f"{http_base}/notes-list")
        page.wait_for_selector('input[placeholder*="搜索"]', timeout=5000)
        page.fill('input[placeholder*="搜索"]', "端到端测试")
        wait(2500)  # Wait for FTS5 search

        content = page.content()
        t("搜索结果包含目标笔记", "E2E测试笔记" in content)

        # -------------------------------------------------------------------
        # 7. 工具栏图标存在性（v21.50/51 SVG 回归点）
        # -------------------------------------------------------------------
        print("\n[E2E] 工具栏图标...")
        page.goto(f"{http_base}/")
        page.wait_for_selector('button:has-text("新建笔记")', timeout=5000)
        page.click('button:has-text("新建笔记")')
        page.wait_for_selector('.tiptap', timeout=5000)
        wait(800)
        page_html = page.content()
        # 图片按钮 SVG（viewBox 24 线框）与链接按钮（Feather link 双链环）
        t("图片按钮为 SVG 图标", '<svg' in page_html and '插入图片' in page_html)
        t("链接按钮为 SVG 图标", '插入链接' in page_html and 'svg' in page_html)
        # 颜色按钮存在
        t("颜色按钮存在", '文字颜色' in page_html)

        # -------------------------------------------------------------------
        # 8. Markdown 导入（v20.3）
        # -------------------------------------------------------------------
        print("\n[E2E] Markdown 导入...")
        page.goto(f"{http_base}/")
        page.wait_for_selector('button:has-text("新建笔记")', timeout=5000)
        # 通过 API multipart 上传 .md（带登录 token）
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            boundary = "----e2e" + str(int(time.time()))
            md_bytes = "# 导入测试笔记\n\n这是通过 Markdown 导入的内容".encode("utf-8")
            body = (
                f"--{boundary}\r\n"
                'Content-Disposition: form-data; name="file"; filename="import.md"\r\n'
                "Content-Type: text/markdown\r\n\r\n"
            ).encode() + md_bytes + f"\r\n--{boundary}--\r\n".encode()
            req3 = urllib.request.Request(
                f"{http_base}/api/notes/import", data=body,
                headers={
                    "Content-Type": f"multipart/form-data; boundary={boundary}",
                    "Authorization": "Bearer " + token
                })
            try:
                resp = urllib.request.urlopen(req3, timeout=15)
                t("Markdown 导入 API（multipart）", resp.status in (200, 201))
            except urllib.error.HTTPError as e:
                t(f"Markdown 导入 API（multipart, status={e.code}）", e.code in (200, 201))
            except Exception as e:
                t(f"Markdown 导入 API（异常 {e}）", False)
        else:
            skip("Markdown 导入 API（无 token 跳过）")

        # -------------------------------------------------------------------
        # 9. 批量导出 API（zip 流式）
        # -------------------------------------------------------------------
        print("\n[E2E] 批量导出 API...")
        # 获取当前页面的 token（从 localStorage 读取）
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            req2 = urllib.request.Request(
                f"{http_base}/api/notes/batch-export",
                data=json.dumps({"ids": []}).encode(),
                headers={"Content-Type": "application/json", "Authorization": "Bearer " + token},
                method="POST")
            try:
                resp = urllib.request.urlopen(req2, timeout=10)
                t("批量导出 API 返回", resp.status == 200)
            except urllib.error.HTTPError as e:
                t("批量导出 API 返回", e.code in (200, 400, 401))  # 空 ID 列表 400 也可接受
            except Exception:
                t("批量导出 API 返回", False)
        else:
            skip("批量导出 API 返回（无 token 跳过）")

        # -------------------------------------------------------------------
        # 10. 移动端视口（PWA 响应式）
        # -------------------------------------------------------------------
        print("\n[E2E] 移动端视口...")
        page2 = browser.new_page(viewport={"width": 390, "height": 844})  # iPhone 12 尺寸
        page2.set_default_timeout(15000)
        page2.goto(f"{http_base}/login")
        page2.wait_for_selector('input[placeholder="请输入用户名"]', timeout=10000)
        t("移动端登录页可渲染", "登录" in page2.content())
        page2.close()

        # -------------------------------------------------------------------
        # 11. 注册新用户（多用户隔离）——已有 admin 后 /register 重定向到 /login，
        #     故用 API 验证注册端点（冒烟 §12 已覆盖浏览器侧注册流程）
        #     v21.133 M-04：新装默认关闭自助注册；§11/§13 需注册额外用户，先由已登录 admin 开启。
        # -------------------------------------------------------------------
        print("\n[E2E] 开启自助注册（§11/§13 前置）...")
        en_flag = page.evaluate("""async () => {
            const token = localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || '';
            const resp = await fetch('/api/configs/allow_register', {
                method: 'PUT',
                headers: {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token},
                body: JSON.stringify({value: 'true'})
            });
            return resp.status;
        }""")
        t(f"admin 开启自助注册 allow_register=true（status={en_flag}）", en_flag == 200)
        print("\n[E2E] 注册新用户 API...")
        import random as _r
        uname = "e2euser" + str(_r.randint(1000, 9999))
        reg_data = json.dumps({
            "username": uname, "password": "User@1234567",
            "security_question": "q", "security_answer": "a"
        }).encode()
        reg_req = urllib.request.Request(
            f"{http_base}/api/auth/register", data=reg_data,
            headers={"Content-Type": "application/json"})
        try:
            resp = urllib.request.urlopen(reg_req, timeout=10)
            t("新用户可注册（API）", resp.status in (200, 201))
        except urllib.error.HTTPError as e:
            t(f"新用户可注册（API, status={e.code}）", e.code in (200, 201))

        # -------------------------------------------------------------------
        # 12. 大笔记保存（v21.53 100MB 分级上限，用 20MB 验证放宽）
        # -------------------------------------------------------------------
        print("\n[E2E] 大笔记保存（20MB）...")
        big_content = "x" * (20 * 1024 * 1024)
        big_ok = page.evaluate("""async (content) => {
            const token = localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || '';
            try {
                const resp = await fetch('/api/notes', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + token},
                    body: JSON.stringify({title: '大笔记测试', content, tags: ''})
                });
                return resp.status;
            } catch (e) { return 0; }
        }""", big_content)
        t(f"20MB 笔记保存（status={big_ok}）", big_ok in (200, 201))

        # -------------------------------------------------------------------
        # 13. TOTP 取消登录（v21.58 F-03 回归：首因子通过不建立会话）
        # -------------------------------------------------------------------
        print("\n[E2E] TOTP 取消登录...")
        totp_user = "e2etotp" + str(_r.randint(1000, 9999))
        # 2026-08-28 审计整改（H-01）：测试账号密码必须走环境变量。
        # 2026-09-08 修复（评审 P0-1）：缺口令时仅 SKIP 本用例，绝不 return 终止 main()
        # （原 `return` 会连带跳过 §14–§17 与末尾汇总，制造 CI 半绿假象）。
        totp_pwd = os.environ.get("NOTEPAD_TEST_PASSWORD")
        if not totp_pwd:
            skip("§13 TOTP 取消登录（缺 NOTEPAD_TEST_PASSWORD，跳过；不影响后续用例）")
        else:
            reg_data = json.dumps({
                "username": totp_user, "password": totp_pwd,
                "security_question": "q", "security_answer": "a"
            }).encode()
            reg_req = urllib.request.Request(
                f"{http_base}/api/auth/register", data=reg_data,
                headers={"Content-Type": "application/json"})
            totp_token = ""
            try:
                resp = urllib.request.urlopen(reg_req, timeout=10)
                reg_resp = json.loads(resp.read())
                totp_token = reg_resp.get("access_token", "")
                t("TOTP 测试用户注册成功", bool(totp_token))
            except Exception:
                t("TOTP 测试用户注册成功", False)

            if totp_token:
                en_req = urllib.request.Request(
                    f"{http_base}/api/auth/enable-totp", data=b"",
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + totp_token},
                    method="POST")
                secret = ""
                try:
                    resp = urllib.request.urlopen(en_req, timeout=10)
                    secret = json.loads(resp.read()).get("secret", "")
                    t("TOTP 密钥生成", bool(secret))
                except Exception:
                    t("TOTP 密钥生成", False)

                if secret:
                    code = totp_code(secret)
                    conf_data = json.dumps({"code": code}).encode()
                    conf_req = urllib.request.Request(
                        f"{http_base}/api/auth/confirm-totp", data=conf_data,
                        headers={"Content-Type": "application/json", "Authorization": "Bearer " + totp_token})
                    try:
                        resp = urllib.request.urlopen(conf_req, timeout=10)
                        t("TOTP 开启确认", resp.status == 200)
                    except Exception:
                        t("TOTP 开启确认", False)

                    # UI 登录：密码正确后应弹两步验证框，取消后停留登录页且无会话
                    page3 = browser.new_page()
                    page3.set_default_timeout(20000)
                    page3.goto(f"{http_base}/login")
                    page3.wait_for_selector('input[placeholder="请输入用户名"]')
                    page3.fill('input[placeholder="请输入用户名"]', totp_user)
                    page3.fill('input[placeholder="请输入密码"]', totp_pwd)
                    page3.click('button:has-text("登录")')
                    wait(1200)
                    t("密码正确后弹出两步验证框", page3.query_selector('.el-message-box') is not None)
                    cancel_btn = page3.query_selector('.el-message-box button:has-text("取消")')
                    if cancel_btn:
                        cancel_btn.click()
                        wait(800)
                    t("取消 TOTP 后停留登录页", "/login" in page3.url)
                    has_user = page3.evaluate("localStorage.getItem('user') !== null")
                    t("取消 TOTP 后未建立会话（无 user）", not has_user)
                    page3.close()


        # -------------------------------------------------------------------
        # 14. 加密笔记附件生命周期（v21.58 F-02 回归：永久删除清理附件）
        # -------------------------------------------------------------------
        print("\n[E2E] 加密笔记附件删除...")
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            # 上传 1x1 PNG
            png = base64.b64decode(
                "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
            up_boundary = "----e2eupload" + str(int(time.time()))
            up_body = (
                f"--{up_boundary}\r\n"
                'Content-Disposition: form-data; name="file"; filename="e2e.png"\r\n'
                "Content-Type: image/png\r\n\r\n"
            ).encode() + png + f"\r\n--{up_boundary}--\r\n".encode()
            up_req = urllib.request.Request(
                f"{http_base}/api/upload", data=up_body,
                headers={"Content-Type": f"multipart/form-data; boundary={up_boundary}", "Authorization": "Bearer " + token})
            up_name = ""
            try:
                resp = urllib.request.urlopen(up_req, timeout=15)
                up_url = json.loads(resp.read()).get("url", "")
                up_name = up_url.rsplit("/", 1)[-1]
                t("图片上传成功", bool(up_name))
            except Exception:
                t("图片上传成功", False)

            if up_name:
                create_data = json.dumps({
                    "title": "含图加密笔记", "content": f'<p><img src="/uploads/{up_name}"></p>', "tags": ""
                }).encode()
                create_req = urllib.request.Request(
                    f"{http_base}/api/notes", data=create_data,
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
                note_id = None
                try:
                    resp = urllib.request.urlopen(create_req, timeout=15)
                    note_id = json.loads(resp.read()).get("id")
                    t("含图笔记创建成功", bool(note_id))
                except Exception:
                    t("含图笔记创建成功", False)

                if note_id:
                    enc_data = json.dumps({"password": "enc123", "confirm_password": "enc123"}).encode()
                    enc_req = urllib.request.Request(
                        f"{http_base}/api/notes/{note_id}/encrypt", data=enc_data,
                        headers={"Content-Type": "application/json", "Authorization": "Bearer " + token},
                        method="PUT")
                    try:
                        resp = urllib.request.urlopen(enc_req, timeout=15)
                        t("含图笔记加密成功", resp.status == 200)
                    except Exception:
                        t("含图笔记加密成功", False)

                    for label, path in [("软删到回收站", f"/api/notes/{note_id}"), ("永久删除", f"/api/notes/{note_id}/permanent")]:
                        d = urllib.request.Request(
                            f"{http_base}{path}", headers={"Authorization": "Bearer " + token}, method="DELETE")
                        try:
                            resp = urllib.request.urlopen(d, timeout=15)
                            t(label, resp.status == 200)
                        except Exception:
                            t(label, False)

                    upload_dir = os.path.join(data_dir, "upload")
                    up_path = os.path.join(upload_dir, up_name)
                    t("加密笔记附件已随永久删除清理", not os.path.exists(up_path))

        # -------------------------------------------------------------------
        # 15. 批量导出加密跳过提示（v21.58 F-12 回归）
        # -------------------------------------------------------------------
        print("\n[E2E] 批量导出加密跳过...")
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            def _api_create_note(title):
                d = json.dumps({"title": title, "content": "<p>x</p>", "tags": ""}).encode()
                r = urllib.request.Request(f"{http_base}/api/notes", data=d,
                                           headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
                try:
                    resp = urllib.request.urlopen(r, timeout=15)
                    return json.loads(resp.read()).get("id")
                except Exception:
                    return None

            nid_normal = _api_create_note("批量导出普通")
            nid_enc = _api_create_note("批量导出加密")
            if nid_enc:
                enc_data = json.dumps({"password": "enc123", "confirm_password": "enc123"}).encode()
                enc_req = urllib.request.Request(
                    f"{http_base}/api/notes/{nid_enc}/encrypt", data=enc_data,
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + token},
                    method="PUT")
                try:
                    urllib.request.urlopen(enc_req, timeout=15)
                except Exception:
                    pass

            if nid_normal and nid_enc:
                exp_data = json.dumps({"ids": [nid_normal, nid_enc]}).encode()
                exp_req = urllib.request.Request(
                    f"{http_base}/api/notes/batch-export", data=exp_data,
                    headers={"Content-Type": "application/json", "Authorization": "Bearer " + token},
                    method="POST")
                try:
                    resp = urllib.request.urlopen(exp_req, timeout=15)
                    skipped = resp.headers.get("X-Encrypted-Skipped")
                    exported = resp.headers.get("X-Exported-Count")
                    t("批量导出返回跳过头", skipped == "1" and exported == "1")
                except Exception:
                    t("批量导出返回跳过头", False)

        # -------------------------------------------------------------------
        # 16. 回收站恢复/永久删除
        # -------------------------------------------------------------------
        print("\n[E2E] 回收站恢复/永久删除...")
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            d = json.dumps({"title": "回收站测试笔记", "content": "<p>回收站</p>", "tags": ""}).encode()
            r = urllib.request.Request(f"{http_base}/api/notes", data=d,
                                       headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
            tid = None
            try:
                resp = urllib.request.urlopen(r, timeout=15)
                tid = json.loads(resp.read()).get("id")
            except Exception:
                pass
            if tid:
                # 软删
                dd = urllib.request.Request(f"{http_base}/api/notes/{tid}", headers={"Authorization": "Bearer " + token}, method="DELETE")
                try:
                    urllib.request.urlopen(dd, timeout=15)
                except Exception:
                    pass
                # 回收站列表应含该笔记
                tl = urllib.request.urlopen(urllib.request.Request(f"{http_base}/api/trash/notes", headers={"Authorization": "Bearer " + token}), timeout=10)
                trash_content = tl.read().decode("utf-8", "ignore")
                t("回收站包含已删笔记", str(tid) in trash_content)
                # 恢复
                rs = urllib.request.Request(f"{http_base}/api/notes/{tid}/restore", data=b"",
                                            headers={"Content-Type": "application/json", "Authorization": "Bearer " + token}, method="POST")
                try:
                    resp = urllib.request.urlopen(rs, timeout=15)
                    t("恢复成功", resp.status == 200)
                except Exception:
                    t("恢复成功", False)
                # 再删 + 永久删除
                dd2 = urllib.request.Request(f"{http_base}/api/notes/{tid}", headers={"Authorization": "Bearer " + token}, method="DELETE")
                try:
                    urllib.request.urlopen(dd2, timeout=15)
                except Exception:
                    pass
                pd = urllib.request.Request(f"{http_base}/api/notes/{tid}/permanent", headers={"Authorization": "Bearer " + token}, method="DELETE")
                try:
                    resp = urllib.request.urlopen(pd, timeout=15)
                    t("永久删除成功", resp.status == 200)
                except Exception:
                    t("永久删除成功", False)
                # 回收站应不再包含
                tl2 = urllib.request.urlopen(urllib.request.Request(f"{http_base}/api/trash/notes", headers={"Authorization": "Bearer " + token}), timeout=10)
                trash_content2 = tl2.read().decode("utf-8", "ignore")
                t("永久删除后回收站不再包含", str(tid) not in trash_content2)

        # -------------------------------------------------------------------
        # 17. 草稿恢复（IndexedDB 注入，v21.58 F-11 相关）
        # -------------------------------------------------------------------
        print("\n[E2E] 草稿恢复...")
        token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
        if token:
            d = json.dumps({"title": "草稿测试笔记", "content": "<p>原始</p>", "tags": ""}).encode()
            r = urllib.request.Request(f"{http_base}/api/notes", data=d,
                                       headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
            did = None
            try:
                resp = urllib.request.urlopen(r, timeout=15)
                did = json.loads(resp.read()).get("id")
            except Exception:
                pass
            if did:
                # 向 IndexedDB 注入"未来草稿"（模拟保存失败遗留的本地草稿）
                inject_ok = page.evaluate("""async (key) => {
                    const db = await new Promise((res, rej) => {
                        const r = indexedDB.open('keyval-store');
                        r.onsuccess = () => res(r.result);
                        r.onerror = () => rej(r.error);
                    });
                    const tx = db.transaction('keyval', 'readwrite');
                    tx.objectStore('keyval').put({title: '草稿标题', content: '<p>草稿内容</p>', tags: '', saved_at: Date.now() + 60000}, key);
                    await new Promise(r => tx.oncomplete = r);
                    db.close();
                    return true;
                }""", f"draft_1_{did}")
                t("草稿注入 IndexedDB", inject_ok is True)
                # 刷新并进入该笔记，应弹草稿恢复提示
                # v21.94 真机（192.0.2.15）偶发 FAIL 修复：固定 wait 在慢速环境不足，
                # 改用显式等待（wait_for_selector），消除时序脆弱导致的误报。
                page.goto(f"{http_base}/")
                page.wait_for_selector('button:has-text("新建笔记")', timeout=5000)
                wait(800)
                try:
                    page.wait_for_selector('text=草稿测试笔记', timeout=5000)
                    page.click('text=草稿测试笔记')
                except Exception:
                    pass
                try:
                    page.wait_for_selector('.el-message-box', timeout=5000)
                    t("出现草稿恢复提示", "草稿" in page.content())
                except Exception:
                    t("出现草稿恢复提示", False)
                # 丢弃草稿，避免残留
                discard_btn = page.query_selector('.el-message-box button:has-text("丢弃草稿")')
                if discard_btn:
                    discard_btn.click()
                    wait(500)

        # -------------------------------------------------------------------
        # N. 管理后台 4 页面 UI 走查
        #    背景：v21.91 覆盖率矩阵分析显示 admin/{users,configs,limits,logs} 四条路由
        #    在此前所有测试中零覆盖（后端 57 端点已 100% 覆盖，缺口全在管理后台 UI）。
        # -------------------------------------------------------------------
        print("\n[E2E] 管理后台页面走查...")
        try:
            apage = browser.new_page()
            apage.set_default_timeout(30000)
            apage.goto(f"{http_base}/login")
            apage.wait_for_selector('input[placeholder="请输入用户名"]')
            apage.fill('input[placeholder="请输入用户名"]', "admin")
            apage.fill('input[placeholder="请输入密码"]', "Admin@123456")
            apage.click('button:has-text("登录")')
            apage.wait_for_url(f"{http_base}/", timeout=15000)

            # v2.1.3 路由扁平化：管理后台一律单段（admin-users 而非 admin/users）。
            # 多段路由在「网关剥前缀 + 整页刷新」下会被多吃一层相对基址，故压平。
            for path, kw in (("/admin-users", "用户管理"),
                             ("/admin-configs", "系统配置"),
                             ("/admin-limits", "系统配置"),
                             ("/admin-logs", "操作日志")):
                apage.goto(f"{http_base}{path}", wait_until="networkidle")
                wait(1200)
                t(f"{path} 页面渲染（含「{kw}」）", kw in apage.content())

            # 用户管理：表格应至少含 admin 自己
            apage.goto(f"{http_base}/admin-users", wait_until="networkidle")
            wait(1500)
            t("用户管理表格含 admin 账号", "admin" in apage.content())

            # 操作日志：表格/空态任一呈现即可（不应白屏）
            apage.goto(f"{http_base}/admin-logs", wait_until="networkidle")
            wait(1500)
            c = apage.content()
            t("操作日志页面非白屏（含筛选或表格）",
              ("操作类型" in c or "暂无数据" in c or "el-table" in c))

            # 限制页：应呈现配额卡片（存储/笔记数等）
            apage.goto(f"{http_base}/admin-limits", wait_until="networkidle")
            wait(1500)
            t("限制页含配额卡片", ("存储配额" in apage.content() or "limit-card" in apage.content()))

            # v2.1.3 兼容层：旧深链接 /admin/users 必须重定向到 /admin-users 且仍可渲染。
            # 反向验证：把 redirect 表删掉这条就会红（说明门禁真的在起作用）。
            apage.goto(f"{http_base}/admin/users?probe=1", wait_until="networkidle")
            wait(1500)
            t("旧深链接 /admin/users 重定向到 /admin-users",
              apage.url.rstrip("/").endswith("/admin-users?probe=1")
              or apage.url.rstrip("/").endswith("/admin-users"),
              apage.url)
            t("旧深链接重定向后页面仍渲染", "用户管理" in apage.content())

            # 注意（本轮发现，未改产品，仅记录）：侧栏"系统配置"菜单跳 /admin/limits，
            # 而 /admin/configs 无菜单入口；且两页 h2 标题同为"系统配置"。
            # 此处直接 goto URL 验证，绕开导航问题，先保证页面本身可测。
            apage.close()
        except Exception as e:
            t(f"管理后台走查异常: {e}", False)

        # -------------------------------------------------------------------
        # 30. 视觉/计算样式门禁（v21.125 新增）
        #     背景：v21.121~123 将工具栏模板迁入 EditorToolbar.vue 子组件，但样式留在
        #     TiptapEditor.vue 的 <style scoped>——父 scoped 选择器对子组件内部元素失效
        #     （仅子组件根节点继承父 scope；teleport 弹层 scope 跟随渲染组件），工具栏
        #     视觉损毁而功能断言全绿。功能性用例发现不了这类问题，故以计算样式断言兜底。
        # -------------------------------------------------------------------
        print("\n[E2E] §30 视觉/计算样式门禁 ...")
        try:
            r = page.request.post(f"{http_base}/api/notes",
                                  data={"title": "§30 视觉门禁探针", "content": "<p>probe</p>"})
            t("§30 探针笔记创建成功", r.ok)
            page.goto(f"{http_base}/notes-list", wait_until="networkidle")
            wait(1200)
            page.locator(".note-card").first.click()
            page.wait_for_selector('[aria-label="更多操作"]', timeout=15000)
            page.click('button:has-text("编辑")')
            page.wait_for_selector(".editor-toolbar", timeout=15000)
            wait(400)
            st = page.evaluate("""() => { const g=(s)=>{const e=document.querySelector(s); if(!e) return null;
                const c=getComputedStyle(e); const r=e.getBoundingClientRect();
                return {h:+r.height.toFixed(1), w:+r.width.toFixed(1), display:c.display, border:c.borderTopStyle};};
                return {btn:g('.tb-btn'), ic:g('.tb-ic svg'), caret:g('.tb-caret svg'), sep:g('.tb-sep')}; }""")
            t("§30 工具栏按钮 30px 高且无边框（scoped 生效）",
              bool(st.get("btn")) and st["btn"]["h"] == 30 and st["btn"]["border"] == "none")
            t("§30 工具栏图标 svg 17x17",
              bool(st.get("ic")) and st["ic"]["w"] == 17 and st["ic"]["h"] == 17)
            t("§30 下拉 caret svg 12px（非原生尺寸）",
              bool(st.get("caret")) and st["caret"]["w"] == 12)
            t("§30 分组分隔符 1x18",
              bool(st.get("sep")) and st["sep"]["w"] == 1 and st["sep"]["h"] == 18)
            page.locator(".tb-select").first.click()
            wait(500)
            dd = page.evaluate("""() => { const d=[...document.querySelectorAll('.tb-toolbar-dd')]
                .find(x=>x.getBoundingClientRect().width>0); if(!d) return null;
                const c=d.querySelector('.tb-check'); const i=d.querySelector('.el-dropdown-menu__item');
                return {check: c? +c.getBoundingClientRect().width.toFixed(1):0,
                        item: i? +i.getBoundingClientRect().height.toFixed(1):0}; }""")
            t("§30 下拉弹层 check 列 16px（teleport 弹层 scoped 生效）",
              bool(dd) and dd["check"] == 16)
            t("§30 下拉弹层 item 行高正常（EP 样式在位）",
              bool(dd) and 28 <= dd["item"] <= 36)
            page.keyboard.press("Escape")
            wait(300)
            # toast 单行三成员光学居中（图标/标题/关闭符中心差 ≤1.5px）
            page.goto(f"{http_base}/notes-list", wait_until="networkidle")
            wait(1000)
            page.locator(".note-card").first.click()
            page.wait_for_selector('[aria-label="更多操作"]', timeout=15000)
            page.click('[aria-label="更多操作"]')
            wait(300)
            page.click('.el-dropdown-menu__item:has-text("删除")')
            wait(300)
            page.click('.el-message-box button:has-text("删除")')
            page.wait_for_selector("#toast-container > div", timeout=5000)
            wait(400)
            tz = page.evaluate("""() => { const t=document.querySelector('#toast-container > div'); if(!t) return null;
                const icon=t.querySelector('span'); const title=t.querySelectorAll('div div')[0]; const close=t.querySelector('button');
                const c=e=>{const r=e.getBoundingClientRect(); return r.top+r.height/2;};
                return [c(icon), c(title), c(close)]; }""")
            spread = (max(tz) - min(tz)) if tz else 99
            t(f"§30 toast 三成员光学中心差 ≤1.5px（实测 {spread:.1f}px）", spread <= 1.5)
        except Exception as e:
            t(f"§30 视觉门禁异常: {e}", False)

        # -------------------------------------------------------------------
        # 33/34. 备份页布局 + 按钮态门禁（v21.131 新增）
        #   背景：v21.129/130 快照页列宽换行/锁死、备份ID 截断、孤儿快照行下载失败
        #   均漏到人工复核——§30 视觉门禁只覆盖编辑器，admin 表格零布局/态断言。
        #   此处以真实注入数据驱动 admin 表格"表头同排 + 操作按钮同排 + 备份ID全显 +
        #   无副本行禁用"断言。关键：el-tabs v-show 保留所有面板，查询限定可见面板。
        # -------------------------------------------------------------------
        print("\n[E2E] §33/§34 备份页布局与按钮态门禁 ...")
        try:
            db_path = os.path.join(data_dir, "notepad.db")
            con = sqlite3.connect(db_path, timeout=15)
            con.execute("PRAGMA busy_timeout=15000")
            cu = con.cursor()
            cu.execute("DELETE FROM backup_snapshots WHERE backup_id LIKE 'e2e%'")
            cu.execute("DELETE FROM backup_targets WHERE name='e2e-target'")
            cu.execute(
                "INSERT INTO backup_targets(name,type,base_url,username,secret_encrypted,remote_path,"
                "enabled,retention_count,schedule_interval,allow_loopback,allow_insecure_tls,custom_ca_pem,"
                "consecutive_failures,last_error) VALUES('e2e-target','webdav','https://dav.example.com/dav/',"
                "'u','enc','/bk/',1,7,'24h',0,0,'',0,'')")
            _tid = cu.lastrowid
            for _bid, _trig in (("e2elive0001manual", "manual"), ("e2elive0002sched", "scheduled")):
                cu.execute(
                    "INSERT INTO backup_snapshots(backup_id,created_at,trigger_kind,archive_sha256,size_bytes,"
                    "app_version,schema_version,note_count,user_count,attachment_count,status,incomplete) "
                    "VALUES(?,?,?,?,?,'1.0.171',1,10,1,1,'LOCAL_VERIFIED',0)",
                    (_bid, "2026-09-15T10:00:00Z", _trig, "sha-" + _bid, 1048576))
                cu.execute("INSERT INTO backup_placements(backup_id,target_id,status,remote_path,error) "
                           "VALUES(?,?,'VERIFIED',?,'')", (_bid, _tid, "/bk/" + _bid + ".snbak"))
            # 孤儿：有快照行、零放置（模拟历史 prune 遗留死记录）
            cu.execute(
                "INSERT INTO backup_snapshots(backup_id,created_at,trigger_kind,archive_sha256,size_bytes,"
                "app_version,schema_version,note_count,user_count,attachment_count,status,incomplete) "
                "VALUES('e2eorphan0000000','2026-09-15T08:00:00Z','discovered','sha-o',524288,'1.0.171',1,1,1,0,'LOCAL_VERIFIED',0)")
            con.commit()
            con.close()

            bpage = browser.new_page()
            bpage.set_default_timeout(30000)
            bpage.goto(f"{http_base}/login", wait_until="networkidle")
            bpage.wait_for_selector('input[placeholder="请输入用户名"]')
            bpage.fill('input[placeholder="请输入用户名"]', "admin")
            bpage.fill('input[placeholder="请输入密码"]', "Admin@123456")
            bpage.click('button:has-text("登录")')
            bpage.wait_for_url(f"{http_base}/", timeout=15000)
            bpage.goto(f"{http_base}/admin-backup", wait_until="networkidle")
            wait(800)
            bpage.click('.el-tabs__item:has-text("快照与下载")')
            wait(1500)

            VT_JS = """() => {
              const panes=[...document.querySelectorAll('.el-tab-pane')]
                .filter(p=>getComputedStyle(p).display!=='none' && p.offsetParent!==null);
              const pane=panes[0]; if(!pane) return {error:'no visible pane'};
              const headers=[...pane.querySelectorAll('.el-table__header-wrapper th')]
                .map(th=>({text:(th.innerText||'').trim(), top:th.getBoundingClientRect().top}));
              const rows=[...pane.querySelectorAll('.el-table__body-wrapper tbody tr')].map(tr=>{
                const cells=tr.querySelectorAll('td');
                const bid=cells.length?(cells[0].innerText||'').trim():'';
                const btns=[...tr.querySelectorAll('.el-button')].map(b=>({
                  text:(b.innerText||'').trim(),
                  disabled:b.disabled||b.classList.contains('is-disabled'),
                  top:b.getBoundingClientRect().top}));
                const rt=tr.innerText||'';
                return {bid, btns, noCopy: rt.includes('无可用副本')};
              });
              return {headers, rows};
            }"""
            d = bpage.evaluate(VT_JS)
            if d.get("error"):
                t(f"§33/§34 可见面板取数（{d['error']}）", False)
            else:
                tops = [round(h["top"], 1) for h in d["headers"]]
                t("§33 快照表头同一横排（不换行）", bool(tops) and (max(tops) - min(tops)) <= 2.0)
                rows = d["rows"]
                t(f"§34 快照表有数据行（注入 3 份，rows={len(rows)}）", len(rows) >= 3)
                live = [r for r in rows if not r["noCopy"]]
                orph = [r for r in rows if r["noCopy"]]
                if live:
                    t(f"§33 备份ID 完整显示（无截断省略号，bid={live[0]['bid']!r}）",
                      "…" not in live[0]["bid"] and "..." not in live[0]["bid"])
                    bts = [round(b["top"], 1) for b in live[0]["btns"]]
                    t("§33 操作列 4 按钮同一横排（不换行）",
                      bool(bts) and len(live[0]["btns"]) == 4 and (max(bts) - min(bts)) <= 2.0)
                    dl = next((x for x in live[0]["btns"] if x["text"] == "下载"), None)
                    rv = next((x for x in live[0]["btns"] if x["text"] == "恢复整站"), None)
                    vf = next((x for x in live[0]["btns"] if x["text"] == "校验完整性"), None)
                    t("§34 活快照 下载可用", bool(dl) and not dl["disabled"])
                    t("§34 活快照 恢复可用", bool(rv) and not rv["disabled"])
                    t("§34 活快照 校验可用", bool(vf) and not vf["disabled"])
                    t("§34 活快照 无『无可用副本』标记", not live[0]["noCopy"])
                t(f"§34 存在零放置孤儿行（orphans={len(orph)}）", len(orph) >= 1)
                if orph:
                    o = orph[0]
                    dl = next((x for x in o["btns"] if x["text"] == "下载"), None)
                    rv = next((x for x in o["btns"] if x["text"] == "恢复整站"), None)
                    vf = next((x for x in o["btns"] if x["text"] == "校验完整性"), None)
                    de = next((x for x in o["btns"] if x["text"] == "删除备份"), None)
                    t("§34 孤儿行 下载禁用", bool(dl) and dl["disabled"])
                    t("§34 孤儿行 恢复禁用", bool(rv) and rv["disabled"])
                    t("§34 孤儿行 校验禁用", bool(vf) and vf["disabled"])
                    t("§34 孤儿行 删除仍可用", bool(de) and not de["disabled"])
                    t("§34 孤儿行 有『无可用副本』标记", o["noCopy"])
            bpage.close()
        except Exception as e:
            t(f"§33/§34 备份页门禁异常: {e}", False)

        # -------------------------------------------------------------------
        # 35. 管理菜单顺序 + PC/移动一致性（v21.131 新增）
        #   背景：管理菜单有两份（PC AppSidebar + 移动 MainLayout），"两处必须同改"此前
        #   靠人记；v21.129 菜单顺序调整无自动断言。此处锁死顺序 + PC/移动项集合一致。
        # -------------------------------------------------------------------
        print("\n[E2E] §35 管理菜单顺序 + PC/移动一致 ...")
        try:
            def order_ok(items):
                need = ["系统配置", "系统备份", "操作日志"]
                if not all(n in items for n in need):
                    return False
                return items.index("系统配置") < items.index("系统备份") < items.index("操作日志")

            ADMIN_SET = ("用户管理", "系统配置", "系统备份", "操作日志")

            # PC：主 page 已登录，侧栏 .sb-user-item hover 展开
            page.goto(f"{http_base}/", wait_until="networkidle")
            wait(800)
            sb = page.query_selector(".sb-user-item")
            if sb:
                sb.hover()
                wait(600)
            pc_items = [(it.inner_text() or "").strip() for it in page.query_selector_all(".el-dropdown-menu__item")]
            page.keyboard.press("Escape")
            wait(300)
            t(f"§35 PC 管理菜单顺序正确（{pc_items}）", order_ok(pc_items))
            admin_pc = [x for x in pc_items if x in ADMIN_SET]

            # 移动端：新页 390 视口，登录，点 .user-info 展开顶栏下拉
            mpage = browser.new_page(viewport={"width": 390, "height": 844})
            mpage.set_default_timeout(30000)
            mpage.goto(f"{http_base}/login", wait_until="networkidle")
            mpage.wait_for_selector('input[placeholder="请输入用户名"]')
            mpage.fill('input[placeholder="请输入用户名"]', "admin")
            mpage.fill('input[placeholder="请输入密码"]', "Admin@123456")
            mpage.click('button:has-text("登录")')
            mpage.wait_for_url(f"{http_base}/", timeout=15000)
            wait(800)
            ui = mpage.query_selector(".user-info") or mpage.query_selector(".user-dropdown")
            if ui:
                ui.click()
                wait(600)
            mob_items = [(it.inner_text() or "").strip() for it in mpage.query_selector_all(".el-dropdown-menu__item")]
            admin_mob = [x for x in mob_items if x in ADMIN_SET]
            t(f"§35 移动 管理菜单顺序正确（{mob_items}）", order_ok(mob_items))
            t(f"§35 PC/移动 管理菜单项集合一致（pc={admin_pc} mob={admin_mob}）", admin_pc == admin_mob and len(admin_pc) == 4)
            mpage.close()
        except Exception as e:
            t(f"§35 菜单门禁异常: {e}", False)

        # -------------------------------------------------------------------
        # 36. 暗色模式计算样式门禁（v21.131 新增）
        #   背景：v21.100/118/120 多次漏网——暗色下硬编码亮色值致元素与背景同色不可见。
        #   功能断言查不出，须计算样式（luminance）兜底。切暗色靠 localStorage theme=dark。
        # -------------------------------------------------------------------
        print("\n[E2E] §36 暗色模式计算样式门禁 ...")
        try:
            page.goto(f"{http_base}/", wait_until="networkidle")
            wait(600)
            SAMPLE_JS = """() => {
                const lum = (c) => { const m = (c||'').match(/\\d+/g); if (!m || m.length<3) return -1;
                    const [r,g,b] = m.map(Number); return 0.2126*r + 0.7152*g + 0.0722*b; };
                const sb = document.querySelector('.app-sidebar') || document.body;
                const tx = document.querySelector('.sb-item-text') || document.body;
                const sbcs = getComputedStyle(sb), tcs = getComputedStyle(tx);
                return { theme: document.documentElement.getAttribute('data-theme'),
                         bg: sbcs.backgroundColor, bgLum: lum(sbcs.backgroundColor),
                         textLum: lum(tcs.color) };
            }"""
            light = page.evaluate(SAMPLE_JS)
            page.evaluate("localStorage.setItem('theme','dark')")
            page.reload(wait_until="networkidle")
            wait(800)
            dark = page.evaluate(SAMPLE_JS)
            t(f"§36 暗色切换生效（data-theme={dark.get('theme')}）", dark.get("theme") == "dark")
            t(f"§36 侧栏背景随主题变化（{light.get('bg')}→{dark.get('bg')}）",
              light.get("bg") != dark.get("bg"))
            t(f"§36 暗色下侧栏文字与背景有足够对比（textLum={dark.get('textLum'):.0f} > bgLum={dark.get('bgLum'):.0f}+40，防同色不可见）",
              dark.get("textLum", 0) > dark.get("bgLum", 999) + 40)
            t(f"§36 暗色下侧栏背景为暗色（bgLum={dark.get('bgLum'):.0f}<128）",
              0 <= dark.get("bgLum", -1) < 128)
            # 复位亮色，避免影响后续用例
            page.evaluate("localStorage.setItem('theme','light')")
            page.reload(wait_until="networkidle")
            wait(500)
        except Exception as e:
            t(f"§36 暗色门禁异常: {e}", False)

        # -------------------------------------------------------------------
        # 37. 表格/任务清单 导出↔导入回环门禁（v21.131 新增）
        #   背景：v21.110 表格 GFM 导出缺失、v21.111 GFM 任务清单导入缺口——smoke §19
        #   仅测标题/粗体/链接/图片导出，未覆盖表格与任务清单。此处补端到端回环。
        # -------------------------------------------------------------------
        print("\n[E2E] §37 表格/任务清单 导出↔导入回环门禁 ...")
        try:
            token = page.evaluate("localStorage.getItem('access_token') || sessionStorage.getItem('access_token') || ''")
            hdr = {"Content-Type": "application/json", "Authorization": "Bearer " + token}
            rt_html = ('<table><tbody><tr><td><p>甲</p></td><td><p>乙</p></td></tr>'
                       '<tr><td><p>1</p></td><td><p>2</p></td></tr></tbody></table>'
                       '<ul data-type="taskList"><li data-type="taskItem" data-checked="false">'
                       '<label><input type="checkbox"><span></span></label><div><p>待办项</p></div></li></ul>')
            d = json.dumps({"title": "§37 回环", "content": rt_html, "tags": ""}).encode()
            nid = None
            try:
                nid = json.loads(urllib.request.urlopen(
                    urllib.request.Request(f"{http_base}/api/notes", data=d, headers=hdr), timeout=15).read()).get("id")
            except Exception:
                pass
            t(f"§37 建含表格+任务清单笔记（id={nid}）", bool(nid))
            if nid:
                exp = urllib.request.urlopen(urllib.request.Request(
                    f"{http_base}/api/notes/{nid}/export",
                    headers={"Authorization": "Bearer " + token}), timeout=15).read().decode("utf-8", "replace")
                t(f"§37 导出 MD 含表格语法 |（v21.110 回归）md={exp[:80]!r}", "|" in exp)
                t(f"§37 导出 MD 含任务清单 - [（v21.111 回归）md={exp[:100]!r}", "- [" in exp)
                boundary = "----e2eimport" + str(int(time.time()))
                body = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"rt.md\"\r\n"
                        f"Content-Type: text/markdown\r\n\r\n{exp}\r\n--{boundary}--\r\n").encode()
                imp = urllib.request.Request(f"{http_base}/api/notes/import", data=body,
                                             headers={"Content-Type": f"multipart/form-data; boundary={boundary}",
                                                      "Authorization": "Bearer " + token})
                imported = None
                try:
                    imported = json.loads(urllib.request.urlopen(imp, timeout=15).read())
                except Exception:
                    pass
                ic = ((imported or {}).get("content", "") or "").lower()
                t("§37 导入回环成功（返回新笔记 id）", bool(imported and imported.get("id")))
                t(f"§37 导入回环保留表格 ic={ic[:100]!r}", "<table" in ic)
                t(f"§37 导入回环保留任务清单 ic={ic[:140]!r}", ("taskitem" in ic or "checkbox" in ic))
        except Exception as e:
            t(f"§37 回环门禁异常: {e}", False)

        # -------------------------------------------------------------------
        # §38 网关模拟（v2.2.0 统一网关模式）：nginx-style 前缀保留转发——
        # 飞牛统一网关经 gatewayPrefix=/app/stickynote 转发时保留前缀（应用自行剥，
        # 复用 2.1.2 的 appbase/appBaseHandler 适配）。本节直打 /app/stickynote/*
        # 模拟「网关转发保留前缀」形态，钉死：API 剥前缀、认证边界、子路径浏览器登录。
        # -------------------------------------------------------------------
        print("\n[E2E] §38 网关模拟（/app/stickynote 前缀保留转发）...")
        gw_prefix = "/app/stickynote"
        import http.client as _hc38

        def _gw_get(path):
            c = _hc38.HTTPConnection(f"127.0.0.1:{port}", timeout=10)
            try:
                c.request("GET", path)
                r = c.getresponse()
                b = r.read()
                return r.status, b
            finally:
                c.close()

        try:
            s38, _ = _gw_get(f"{gw_prefix}/api/health")
            t("§38.1 经网关前缀 API health 200（appbase 剥前缀）", s38 == 200, f"status={s38}")
        except Exception as e:
            t(f"§38.1 经网关前缀 API health 200（appbase 剥前缀）: {e}", False)
        try:
            s38b, _ = _gw_get(f"{gw_prefix}/api/notes")
            t("§38.2 网关前缀下认证边界保持（未登录 401）", s38b == 401, f"status={s38b}")
        except Exception as e:
            t(f"§38.2 网关前缀下认证边界保持（未登录 401）: {e}", False)
        # 浏览器级：子路径下登录页加载 + 登录 + 落回挂载点内主页
        # （断言路由落点读 location.pathname——vue-router replaceState 下 page.url 可能旧值）
        gpage = browser.new_page()
        gpage.set_default_timeout(30000)
        try:
            gpage.goto(f"{http_base}{gw_prefix}/login")
            gpage.wait_for_selector('input[placeholder="请输入用户名"]')
            gpage.fill('input[placeholder="请输入用户名"]', "admin")
            gpage.fill('input[placeholder="请输入密码"]', "Admin@123456")
            gpage.click('button:has-text("登录")')
            pn38 = ""
            ok38 = False
            end38 = time.time() + 12
            while time.time() < end38:
                try:
                    pn38 = gpage.evaluate("location.pathname")
                    if pn38 == f"{gw_prefix}/":
                        ok38 = True
                        break
                except Exception:
                    pass
                time.sleep(0.25)
            t("§38.3 子路径登录成功 → 落回挂载点内主页", ok38, f"pathname={pn38!r}")
            t("§38.4 子路径主页可用（新建笔记入口在位）", "新建笔记" in gpage.content())
        except Exception as e:
            t(f"§38.3 子路径浏览器登录链: {e}", False)
            t("§38.4 子路径主页可用（新建笔记入口在位）", False)
        finally:
            gpage.close()

        page.close()
        browser.close()

    # Stop server
    atexit.unregister(_cleanup)
    _cleanup()

    # Report
    print(f"\n{'='*60}")
    print(f" E2E RESULT: {passed} passed, {failed} failed, {skipped} skipped  (total {total})")
    print(f"{'='*60}")
    if failed > 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
