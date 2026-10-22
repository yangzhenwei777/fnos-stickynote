//go:build windows

package main

// bootstrap_bindings_windows_test.go —— 批次 5 · 刀 3 绑定集成（进程外真服务端）：
// 登录态 + epoch 陈旧 → SyncNow 报 resync_required → BootstrapNow 换代 →
// 本地未推送编辑搬运重放收敛、Outbox 清空、服务端 HTTP 实锤
// （红线 1/6/9/17/18 绑定层半段；§89/§140 触发态）。
//
// 服务端必须进程外（bootptest）：同进程 httptest 会让 syncsrv 读到桌面全局库，
// epoch 校验被"同一份数据"自证通过——假绿温床（刀 3 开发中实际踩过后改架构）。

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"stickynote/database"

	"stickynote-desktop/internal/bootptest"
	"stickynote-desktop/localauth"
	"stickynote-desktop/synclocal"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestBootstrapNowResyncChain(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "server-note")

	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db := leftoverOldGen.Swap(nil); db != nil {
			db.Close() // v21.217c：句柄不再自动关，TempDir 清理前必须手动关
		}
		database.GetDB().Close()
	})
	ls := NewLocalService(dir)

	tok := ts.Token(t) // 真 JWT（服务端签发）；假票会被 401 降级路径正确拒绝
	sess, err := localauth.SignIn(database.DB, ts.URL, "u1", func() (string, string, error) {
		return tok, "REF-BOOT", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	setWorker(synclocal.NewWorker(database.DB, synclocal.NewClient(sess.ServerURL, sess.AccessToken, sess.DeviceID)))
	workerPtr().AttachDir = ls.uploadDir
	t.Cleanup(func() { setWorker(nil) })

	if _, err := ls.CreateNote("local-edit", "keep me", "", nil); err != nil {
		t.Fatal(err)
	}
	// 模拟服务端恢复换代（§89）：本地 epoch 锚陈旧 → SyncNow 必须停摆并报 resync_required。
	// UPSERT：全新档可能尚无锚行（锚由首轮 RunOnce 建立；无锚行=首跑基线语义不报错，
	// 故必须先造锚再改脏）。
	if _, err := database.DB.Exec(`INSERT INTO sync_state(key,value) VALUES('server_epoch','99')
		ON CONFLICT(key) DO UPDATE SET value='99'`); err != nil {
		t.Fatal(err)
	}
	resp, err := ls.SyncNow()
	if err == nil {
		t.Fatal("epoch mismatch must block sync")
	}
	if resp["resync_required"] != true {
		t.Fatalf("want resync_required=true, got %v", resp)
	}

	// 换代前钉死记账事实（红线 1/6 前提）：本地编辑行 + PENDING op 都在旧代。
	var pending int
	if err := database.DB.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE operation='CREATE' AND status='PENDING'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("precondition: local edit must be queued in old gen: %d %v", pending, err)
	}
	oldGen := database.GetDB()
	res, err := ls.BootstrapNow()
	if err != nil {
		t.Fatalf("BootstrapNow: %v (resp=%v)", err, res)
	}
	newDB := database.GetDB()
	if res["generation"] == "" || newDB == oldGen {
		t.Fatalf("must land on new generation & swap global: %v", res)
	}
	t.Cleanup(func() { newDB.Close() })
	var n int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM notes WHERE title='server-note'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("snapshot content: %d %v", n, err)
	}
	// 换代后同步：搬运的 CREATE 重放成功、笔记在场、Outbox 清空。
	if _, err := ls.SyncNow(); err != nil {
		t.Fatalf("post-bootstrap sync: %v", err)
	}
	if err := database.GetDB().QueryRow(`SELECT COUNT(*) FROM notes WHERE title='local-edit'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("local edit must converge: %d %v", n, err)
	}
	st, err := ls.SyncStatus()
	if err != nil {
		t.Fatal(err)
	}
	if st["pending_ops"].(int) != 0 {
		t.Fatalf("outbox must drain: %v", st)
	}
	// 服务端实锤（HTTP 探针，不碰其进程全局）。
	resp2, err := http.Get(ts.URL + "/api/test/has-note?title=local-edit")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	b, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(b), `"count":1`) {
		t.Fatalf("server must hold pushed edit: %s", b)
	}
}

// TestBootstrapNowCarriesIdentityAndReturns v21.152 生产根因回归（app 同构）：
// 调度器在跑（登录后 wake 即刻一轮）+ BootstrapNow 并发 → 必须①返回 ②换代后
// client_profile 随代迁移。真机观测：激活完成（磁盘出现新代）但调用方永不返回、
// 新代无身份表 → 「账号与设备」显示未登录 + 数据不同步。
func TestBootstrapNowCarriesIdentityAndReturns(t *testing.T) {
	ts := bootptest.Start(t)
	ts.Seed(t, "carry-note")

	dir := t.TempDir()
	if _, err := bootLocalstore(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db := leftoverOldGen.Swap(nil); db != nil {
			db.Close() // v21.217c：句柄不再自动关，TempDir 清理前必须手动关
		}
		database.GetDB().Close()
	})
	ls := NewLocalService(dir)
	// 全局态卫生：本用例启动调度器/挂 worker，收工必须还原（否则污染同包后续用例）
	oldWorker, oldSched := workerPtr(), syncScheduler
	t.Cleanup(func() {
		stopScheduler()
		syncScheduler = oldSched
		setWorker(oldWorker)
	})

	tok := ts.Token(t)
	sess, err := localauth.SignIn(database.DB, ts.URL, "u1", func() (string, string, error) {
		return tok, "REF-CARRY", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	setWorker(synclocal.NewWorker(database.DB, synclocal.NewClient(sess.ServerURL, sess.AccessToken, sess.DeviceID)))
	workerPtr().AttachDir = ls.uploadDir

	res, err := ls.BootstrapNow()
	if err != nil {
		t.Fatalf("BootstrapNow: %v (resp=%v)", err, res)
	}
	if res["generation"] == "" {
		t.Fatalf("must land on new generation: %v", res)
	}
	p, err := localauth.Load(database.GetDB())
	if err != nil {
		t.Fatalf("v21.152：换代后 client_profile 必须在场（否则 UI 未登录/NO_REFRESH）: %v", err)
	}
	if p.ServerURL != ts.URL || p.Username != "u1" || p.DeviceID != sess.DeviceID {
		t.Fatalf("身份迁移内容不符: %+v", p)
	}
	if len(p.RefreshSealed) == 0 {
		t.Fatal("refresh 封存件必须随代迁移（否则会话不可恢复）")
	}
}

// TestBootstrapNowSingleFlight v21.153 R-1 取证收口：三条入口（登录 goroutine / 调度轮
// auto-bootstrap / 用户显式动作）可并发进入 BootstrapNow——并发 Run = 双代激活抢写
// pointer + carry 源库被对方 Close。单飞后后到者必须立即得 bootstrap_busy 语义返回。
func TestBootstrapNowSingleFlight(t *testing.T) {
	ls := NewLocalService(t.TempDir())
	bootstrapMu.Lock() // 模拟另一入口正在换代
	defer bootstrapMu.Unlock()
	res, err := ls.BootstrapNow()
	if err == nil || res["bootstrap_busy"] != true {
		t.Fatalf("并发进入必须立即得 bootstrap_busy 语义: err=%v res=%v", err, res)
	}
}
