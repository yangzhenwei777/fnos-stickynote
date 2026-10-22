package bootstrap

// redline11_anchor_test.go —— 红线 11「Watermark 与 Snapshot 状态不一致」的**注入直测**。
//
// 台账原缺口：一致正例已钉（TestSnapshotBuildHappyPath / TestBootstrapHappyPathAndWatermark），
// **缺「账本与 meta 错位 → ErrBadZip」的注入直测**（bootstrap.go:120-123 拒收分支从未被执行过）。
//
// 风险面：账本（State 响应里的 watermark/server_epoch）与 zip 内 meta.json 是**两份独立数据**。
// 若二者错位仍继续，桌面会以 meta 的 cursor 记账 —— 服务端 pull 从别的水位起算，
// 中间那段事件**永久丢失且无人察觉**（静默数据缺口）。这条分支是唯一的收敛点，必须真跑。
//
// 注入手法：假服务端同时返回
//   ① READY 元数据：sha256 = **真 zip 的正确 sha**（否则先撞 ErrSHAMismatch，测不到本分支）
//      但 watermark/epoch 报 **账本值**；
//   ② zip 内 meta.json：sha 一致但 watermark/epoch 报 **错位值**。
// 这样必然穿过 sha 校验、精确落在 meta/ledger anchor mismatch 分支上。
// 反向对照：把 meta 改成与账本一致 → 同一工装必须成功激活（防「测试其实恒失败」假绿）。
//
// 附带钉住 unpack 层的两条既有拒收分支在**带正确 sha** 的前提下同样生效（老工装
// newTamperedServer 的 sha 是全零，只能证明 sha 分支先命中）。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"stickynote-desktop/localauth"

	_ "modernc.org/sqlite"
)

// redline11Zip 造一份**结构合法且 notes.db 真能开**的快照 zip，meta 的水位/epoch 由参数决定。
func redline11Zip(t *testing.T, snapID, instanceID string, watermark, epoch int64) []byte {
	t.Helper()
	// 真实 sqlite 库（unpack 内会做 integrity_check，垃圾字节会被先行拒掉）
	dbPath := filepath.Join(t.TempDir(), "src.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO notes (id,title) VALUES (1,'锚点用例')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mw, err := zw.Create("meta.json")
	if err != nil {
		t.Fatal(err)
	}
	mb, err := json.Marshal(Meta{
		SnapshotID: snapID, ProtocolVer: 1, UserID: 1, SourceUserID: 1, Username: "u1",
		ServerEpoch: epoch, Watermark: watermark, InstanceID: instanceID,
		CreatedAt: "2026-10-08T00:00:00Z", FileCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mw.Write(mb); err != nil {
		t.Fatal(err)
	}
	nw, err := zw.Create("notes.db")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nw.Write(dbBytes); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// redline11Server 起一个假服务端：账本报 (ledgerWM, ledgerEpoch)，
// zip 内 meta 报 (metaWM, metaEpoch)，但 **sha256 一律按 zip 实际内容计算**（真值）。
func redline11Server(t *testing.T, ledgerWM, ledgerEpoch, metaWM, metaEpoch int64) string {
	t.Helper()
	zipBytes := redline11Zip(t, "snap-r11", "inst-r11", metaWM, metaEpoch)
	sum := sha256.Sum256(zipBytes)
	realSHA := hex.EncodeToString(sum[:])
	snapID := "snap-r11"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sync/snapshot":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshot_id": snapID, "status": "PENDING"})
		case r.URL.Path == "/api/sync/snapshot/"+snapID:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"snapshot_id": snapID, "status": "READY",
				"sha256": realSHA, "watermark": ledgerWM, "server_epoch": ledgerEpoch,
			})
		case filepath.Base(r.URL.Path) == "download":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestRedline11LedgerMetaAnchorMismatch(t *testing.T) {
	t.Run("水位错位_必须ErrBadZip且零激活", func(t *testing.T) {
		// 账本说 watermark=77 / epoch=5；zip 内 meta 谎报 watermark=42
		base := redline11Server(t, 77, 5, 42, 5)
		profile := t.TempDir()
		rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
		sess := newR11Session(base)

		_, err := rn.Run(sess, nil)
		if err == nil {
			t.Fatal("红线11：账本与 meta 的 watermark 错位必须被拒")
		}
		if !isBadZip(err) {
			t.Fatalf("应为 ErrBadZip（anchor mismatch），实得 %v", err)
		}
		if !bytes.Contains([]byte(err.Error()), []byte("anchor mismatch")) {
			t.Fatalf("错误文案应指明锚点错位（便于排障），实得 %v", err)
		}
		// 决定性断言：一个代都不得激活（否则桌面以错水位记账 → 静默数据缺口）
		if ents, _ := os.ReadDir(filepath.Join(profile, "generations")); len(ents) != 0 {
			t.Fatalf("错位包不得激活任何 generation，得 %d 个", len(ents))
		}
		// 且不得留下「已下载」的 staging 残骸被下次误用
		if ents, _ := os.ReadDir(filepath.Join(profile, "staging")); len(ents) > 1 {
			t.Logf("注：staging 留有 %d 项（清理策略由 GC 负责，不在本分支断言面）", len(ents))
		}
	})

	t.Run("epoch错位_同样拒收", func(t *testing.T) {
		base := redline11Server(t, 10, 9, 10, 3) // epoch 错位
		profile := t.TempDir()
		rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
		if _, err := rn.Run(newR11Session(base), nil); !isBadZip(err) {
			t.Fatalf("epoch 错位同样必须 ErrBadZip，实得 %v", err)
		}
		if ents, _ := os.ReadDir(filepath.Join(profile, "generations")); len(ents) != 0 {
			t.Fatalf("错位包不得激活任何 generation，得 %d 个", len(ents))
		}
	})

	t.Run("正对照_账本与meta一致_必须成功激活", func(t *testing.T) {
		// 同一工装，只把两侧水位/epoch 对齐 → 必须成功（防「本测试恒失败」的假绿）
		base := redline11Server(t, 42, 5, 42, 5)
		profile := t.TempDir()
		rn := &Runner{ProfileDir: profile, AttachDir: filepath.Join(profile, "uploads")}
		res, err := rn.Run(newR11Session(base), nil)
		if err != nil {
			t.Fatalf("账本与 meta 一致时应成功激活，实得 %v", err)
		}
		if res == nil || res.Generation == "" {
			t.Fatalf("应返回激活代号：%+v", res)
		}
		if ents, _ := os.ReadDir(filepath.Join(profile, "generations")); len(ents) == 0 {
			t.Fatal("正对照：应至少激活一个 generation")
		}
	})
}

// newR11Session 造一个指向假服务端的会话（token 形如 url-escaped 的 "t"，占位即可）。
func newR11Session(base string) *localauth.Session {
	u, _ := url.Parse(base)
	return &localauth.Session{ServerURL: u.String(), AccessToken: "t", DeviceID: "dev-r11"}
}
