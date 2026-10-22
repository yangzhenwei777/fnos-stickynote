//go:build windows

package localauth

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"stickynote-desktop/dpapi"
)

func TestSignInSealAndRestore(t *testing.T) {
	db := openTestDB(t)
	access := "ACCESSTOKEN.plain-must-not-hit-disk"
	refresh := "REFRESHTOKEN.plain-must-not-hit-disk"

	sess, err := SignIn(db, "http://192.0.2.10:13389/", "alice", func() (string, string, error) {
		return access, refresh, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.AccessToken != access || sess.DeviceID == "" || len(sess.DeviceSecret) != 32 {
		t.Fatalf("session shape wrong: %+v", sess)
	}
	if sess.ServerURL != "http://192.0.2.10:13389" {
		t.Fatalf("trailing slash must be trimmed: %q", sess.ServerURL)
	}

	// 盘上（列内）必须是封存件：无明文字节泄漏 + 可解。
	var rf, sc []byte
	if err := db.QueryRow(`SELECT refresh_sealed, secret_sealed FROM client_profile WHERE id=1`).Scan(&rf, &sc); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rf, []byte(refresh)) || bytes.Contains(sc, sess.DeviceSecret) {
		t.Fatal("sealed columns must not contain plaintext")
	}
	opened, err := dpapi.Unprotect(rf)
	if err != nil || string(opened) != refresh {
		t.Fatalf("refresh unseal: %v %q", err, opened)
	}

	// 冷启动恢复路径：OpenSession 解出同一 device_secret 与 refresh。
	s2, rt, err := OpenSession(db)
	if err != nil {
		t.Fatal(err)
	}
	if rt != refresh || !bytes.Equal(s2.DeviceSecret, sess.DeviceSecret) || !s2.FromRestore {
		t.Fatalf("restore mismatch: sameSecret=%v fromRestore=%v",
			bytes.Equal(s2.DeviceSecret, sess.DeviceSecret), s2.FromRestore)
	}

	// 二次登录沿用 device_secret（设备身份跨登录稳定），refresh 覆盖为新轮转值。
	sess2, err := SignIn(db, "http://h2:1/", "alice", func() (string, string, error) {
		return "A2", "R2-rotated", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sess2.DeviceSecret, sess.DeviceSecret) {
		t.Fatal("device_secret must survive re-login")
	}
	p, err := Load(db)
	if err != nil || p.ServerURL != "http://h2:1" {
		t.Fatalf("profile must follow latest login: %+v %v", p, err)
	}
	if _, rt2, err := OpenSession(db); err != nil || rt2 != "R2-rotated" {
		t.Fatalf("rotated refresh must be the sealed one: %q %v", rt2, err)
	}

	// TouchRevalidate 写点 + 解析回路。
	if err := TouchRevalidate(db, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	p2, err := Load(db)
	if err != nil || p2.LastRevalAt.IsZero() {
		t.Fatalf("last_revalidate_at must parse: %+v %v", p2, err)
	}
}

func TestOpenSessionTamperedSecretIsFailClosed(t *testing.T) {
	db := openTestDB(t)
	if _, err := SignIn(db, "http://s/", "u", func() (string, string, error) { return "a", "r", nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE client_profile SET secret_sealed = X'00' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	_, _, err := OpenSession(db)
	if !errors.Is(err, dpapi.ErrCorrupted) {
		t.Fatalf("tampered secret must fail closed with ErrCorrupted, got %v", err)
	}
}
