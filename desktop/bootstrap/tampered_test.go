package bootstrap

// tampered_test.go —— 假服务端：READY 元数据指向与账本 sha 不符的 zip（传输/存储篡改模型）。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newTamperedServer(t *testing.T, upstream string) string {
	t.Helper()
	// 造一个合法结构的 zip（含 meta/notes），但账本 sha 报假值。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("meta.json")
	m := Meta{SnapshotID: "snap-tamper", Watermark: 1, ServerEpoch: 1}
	b, _ := jsonMarshal(m)
	_, _ = w.Write(b)
	n, _ := zw.Create("notes.db")
	_, _ = n.Write([]byte("sqlite-tampered-payload"))
	zw.Close()
	zipBytes := buf.Bytes()
	_ = sha256.Sum256
	_ = hex.EncodeToString
	_ = filepath.Base
	_ = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sync/snapshot":
			_, _ = w.Write([]byte(`{"snapshot_id":"snap-tamper","status":"PENDING"}`))
			w.WriteHeader(http.StatusAccepted)
		case r.URL.Path == "/api/sync/snapshot/snap-tamper":
			_, _ = w.Write([]byte(`{"snapshot_id":"snap-tamper","status":"READY","sha256":"0000000000000000000000000000000000000000000000000000000000000000","watermark":1,"server_epoch":1}`))
		case filepath.Base(r.URL.Path) == "download":
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
