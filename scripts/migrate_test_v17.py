# -*- coding: utf-8 -*-
"""v17+ 老库迁移验证：v16.6 schema 老库（无 content_text/notes_fts/uploads）→ 启动应
自动补列、回填纯文本、建 FTS5 索引、建 uploads 表（v18.5）并写存量处理旗标，
且老数据无损、老用户可登录、搜索/导出可用。

流程：
  A. 新二进制起临时库注册一个用户，取其 bcrypt 哈希（移植到老库，实现老用户登录）
  B. Python sqlite3 手工造 v16.6 schema 老库 + 两条 HTML 笔记
  C. 新二进制启动 → SQL 层断言迁移结果 + API 层断言老用户全链路可用
"""
import os
import http.client, io, json, os, shutil, sqlite3, subprocess, sys, tempfile, time, zipfile
from urllib.parse import quote

BIN = sys.argv[1] if len(sys.argv) > 1 else os.environ.get("STICKY_BIN")
WEB = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("STICKY_WEB")
def _free_port():
    import socket
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p
# 评审 P1-5a：端口 env/argv 可覆盖，缺省自动选空闲端口（可并行、免冲突）
PORT = int(os.environ.get("STICKY_TEST_PORT") or (sys.argv[3] if len(sys.argv) > 3 else _free_port()))
BASE = f"127.0.0.1:{PORT}"
if not BIN or not WEB:
    print("用法: python migrate_test_v17.py <server二进制> <web目录> [端口]  （或设 STICKY_BIN/STICKY_WEB/STICKY_TEST_PORT）")
    sys.exit(2)
passed, failed = [], []

def check(name, cond, detail=""):
    (passed if cond else failed).append(name)
    print(f"  {'PASS' if cond else 'FAIL'}  {name}" + (f"  | {detail}" if detail else ""))

def req(method, path, body=None, cookies=None):
    conn = http.client.HTTPConnection(BASE, timeout=10)
    headers = {"Content-Type": "application/json"}
    if cookies:
        headers["Cookie"] = "; ".join(f"{k}={v}" for k, v in cookies.items())
    conn.request(method, path, json.dumps(body) if body is not None else None, headers)
    resp = conn.getresponse()
    data = resp.read()
    sc = {}
    for k, v in resp.getheaders():
        if k.lower() == "set-cookie":
            nv = v.split(";")[0]
            if "=" in nv:
                n, val = nv.split("=", 1)
                sc[n] = val
    conn.close()
    try:
        j = json.loads(data)
    except Exception:
        j = None
    return resp.status, j, sc, data

def start(data_dir):
    p = subprocess.Popen([BIN, f"-port={PORT}", f"-web-dir={WEB}", f"-data-dir={data_dir}",
                          f"-upload-dir={os.path.join(data_dir, 'upload')}"],
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(80):
        try:
            s, _, _, _ = req("GET", "/api/health")
            if s == 200:
                return p
        except Exception:
            pass
        time.sleep(0.25)
    raise RuntimeError("server did not start")

def stop(p):
    p.terminate()
    try:
        p.wait(timeout=5)
    except Exception:
        p.kill()

# ---------- 阶段 A：取一个真实 bcrypt 哈希 ----------
print("== A. 准备老用户凭据 ==")
dir_a = tempfile.mkdtemp(prefix="sticky-note-mig-a-")
srv = start(dir_a)
s, j, sc_a, _ = req("POST", "/api/auth/register", {"username": "migrateold", "password": "Passw0rd!2026",
                                                   "security_question": "q", "security_answer": "a"})
check("阶段A 注册用户", s in (200, 201), f"status={s}")
stop(srv)
db = sqlite3.connect(os.path.join(dir_a, "notepad.db"))
pwd_hash = db.execute("SELECT password_hash FROM users WHERE username='migrateold'").fetchone()[0]
db.close()
check("取到 bcrypt 哈希", pwd_hash.startswith("$2"), pwd_hash[:12] + "...")
shutil.rmtree(dir_a, ignore_errors=True)

# ---------- 阶段 B：手工造 v16.6 schema 老库 ----------
print("== B. 造 v16.6 老库（无 content_text / 无 notes_fts）==")
dir_b = tempfile.mkdtemp(prefix="sticky-note-mig-b-")
db_path = os.path.join(dir_b, "notepad.db")
db = sqlite3.connect(db_path)
db.executescript("""
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    security_question TEXT DEFAULT '',
    security_answer_hash TEXT DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    token_version INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    title TEXT NOT NULL DEFAULT 'Untitled',
    content TEXT NOT NULL DEFAULT '',
    tags TEXT DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME DEFAULT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT NOT NULL UNIQUE,
    value TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE upgrade_records (
    version TEXT PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO upgrade_records (version) VALUES ('1.0.0');
""")
db.execute("INSERT INTO users (username, password_hash, security_question, security_answer_hash, role) VALUES (?, ?, '', '', 'admin')",
           ("migrateold", pwd_hash))
db.execute("INSERT INTO notes (user_id, title, content, tags) VALUES (1, ?, ?, ?)",
           ("老库·宋代点茶笔记", "<p>宋人点茶以<span style=\"color:red\">末茶</span>为贵，碾茶成粉，注汤击拂</p>", "历史,茶"))
db.execute("INSERT INTO notes (user_id, title, content, tags) VALUES (1, ?, ?, ?)",
           ("老库·装修清单", "<p>客厅刷墙，卧室铺地板，阳台封窗</p><ul><li>预算二十万</li></ul>", "生活"))
db.commit()
cols = [r[1] for r in db.execute("PRAGMA table_info(notes)")]
check("老库 notes 无 content_text 列", "content_text" not in cols, str(cols))
check("老库无 notes_fts", db.execute("SELECT COUNT(*) FROM sqlite_master WHERE name='notes_fts'").fetchone()[0] == 0)
db.close()

# ---------- 阶段 C：v17 启动，自动迁移 ----------
print("== C. v17 启动 → 自动迁移验证 ==")
srv = start(dir_b)
try:
    db = sqlite3.connect(db_path)
    cols = [r[1] for r in db.execute("PRAGMA table_info(notes)")]
    check("迁移后 notes 有 content_text 列", "content_text" in cols)
    tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")]
    check("迁移后 notes_fts 已建", "notes_fts" in tables)
    check("迁移后 uploads 表已建（v18.5）", "uploads" in tables, str(tables))
    trigs = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='trigger'")]
    check("迁移后三个触发器已建", sorted(trigs) == ["notes_fts_ad", "notes_fts_ai", "notes_fts_au"], str(trigs))
    rows = db.execute("SELECT id, title, content_text FROM notes ORDER BY id").fetchall()
    t1 = dict((r[0], r) for r in rows)
    ct1, ct2 = t1[1][2], t1[2][2]
    check("老笔记1 content_text 已回填且无 HTML", "末茶" in ct1 and "<p>" not in ct1 and "<span>" not in ct1, ct1[:40])
    check("老笔记2 content_text 已回填（列表转 - 前缀）", "预算二十万" in ct2, ct2[:60].replace("\n", " "))
    # SQL 层直接 MATCH 验证索引含存量
    cnt = db.execute("SELECT COUNT(*) FROM notes_fts WHERE notes_fts MATCH '\"宋代点茶\"'").fetchone()[0]
    check("FTS5 索引覆盖存量行（SQL MATCH 命中）", cnt == 1, f"cnt={cnt}")
    # v18.5：存量回填/清理在升级后首次启动执行一次（旗标落库）
    flag = db.execute("SELECT value FROM configs WHERE key='uploads_backfilled'").fetchone()
    check("uploads 存量处理旗标已写入（v18.5）", flag is not None and flag[0] == "true", str(flag))
    # v18.7：配额/上限配置键幂等补入（v21.68 P2-05：外链开关 allow_external_images 已废弃移除）
    cfg_keys = [r[0] for r in db.execute("SELECT key FROM configs")]
    check("v18.7 配置键已补入（存储配额/内容上限）",
          all(k in cfg_keys for k in ("max_storage_mb_per_user", "max_note_content_kb")), str(cfg_keys))
    db.close()

    # API 层：老用户登录 + 数据无损 + 搜索 + 导出
    s, j, sc, _ = req("POST", "/api/auth/login", {"username": "migrateold", "password": "Passw0rd!2026"})
    check("老用户登录成功", s == 200 and "access_token" in sc, f"status={s}")
    s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
    titles = [n.get("title") for n in (j or {}).get("notes", [])]
    check("老笔记全部可见", "老库·宋代点茶笔记" in titles and "老库·装修清单" in titles, str(titles))
    s, j, _, _ = req("GET", "/api/notes?q=" + quote("宋代点茶"), cookies=sc)
    ids = [n.get("id") for n in (j or {}).get("notes", [])]
    check("API 搜索命中老笔记（FTS5 ≥3 字）", len(ids) == 1, f"ids={ids}")
    s, j, _, _ = req("GET", "/api/notes?q=" + quote("点茶"), cookies=sc)
    ids = [n.get("id") for n in (j or {}).get("notes", [])]
    check("API 搜索命中老笔记（LIKE <3 字）", len(ids) == 1, f"ids={ids}")
    s, j, _, raw = req("GET", "/api/export/notes", cookies=sc)
    zf = zipfile.ZipFile(io.BytesIO(raw))
    names = zf.namelist()
    check("老库导出 zip 两条", sorted(names) == ["老库·宋代点茶笔记.md", "老库·装修清单.md"], str(names))
    body = zf.read("老库·宋代点茶笔记.md").decode("utf-8")
    check("导出内容为回填同款纯文本", "末茶" in body and "<p>" not in body)
    # 老库上新建笔记 → 触发器即时入索引
    s, j, _, _ = req("POST", "/api/notes", {"title": "迁移后新笔记", "content": "<p>鲲鹏处理器的指令流水线</p>", "tags": ""}, cookies=sc)
    check("老库上新建笔记", s in (200, 201), f"status={s}")
    s, j, _, _ = req("GET", "/api/notes?q=" + quote("鲲鹏处理器"), cookies=sc)
    check("新笔记即刻可被 FTS5 命中", (j or {}).get("total") == 1)
    # 重启一次，确认幂等（不重复建表/不报错）
    stop(srv)
    srv = start(dir_b)
    s, j, _, _ = req("GET", "/api/notes?page_size=100", cookies=sc)
    check("迁移后再次重启正常", s == 200 and (j or {}).get("total") == 3, f"total={(j or {}).get('total')}")
finally:
    stop(srv)
    shutil.rmtree(dir_b, ignore_errors=True)

print(f"\nRESULT: {len(passed)} passed, {len(failed)} failed")
if failed:
    print("FAILED:", failed)
    sys.exit(1)
print("== v17 老库迁移验证通过 ==")
