package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func randomKey32() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

func TestSealOpenGCM_RoundTrip(t *testing.T) {
	key := randomKey32()
	aad := []byte("SNBKv1backup-id-123")
	plain := []byte("note payload \x00\x01\x02 with binary bytes")

	sealed, err := SealGCM(plain, key, aad)
	if err != nil {
		t.Fatalf("SealGCM: %v", err)
	}
	// 布局：nonce(12) + ciphertext(len(plain)) + tag(16)
	if len(sealed) != nonceLen+len(plain)+16 {
		t.Fatalf("sealed length = %d, want %d", len(sealed), nonceLen+len(plain)+16)
	}
	got, err := OpenGCM(sealed, key, aad)
	if err != nil {
		t.Fatalf("OpenGCM: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: got %q", got)
	}
}

func TestOpenGCM_WrongKey(t *testing.T) {
	key := randomKey32()
	sealed, _ := SealGCM([]byte("secret"), key, nil)
	if _, err := OpenGCM(sealed, randomKey32(), nil); err == nil {
		t.Fatal("expected auth failure with wrong key, got nil")
	}
}

// AAD 篡改必须认证失败（备份信封用 AAD 绑定 backup_id/kid/enc，防伪装）。
func TestOpenGCM_TamperedAAD(t *testing.T) {
	key := randomKey32()
	aad := []byte("backup-A|kid-1|enc-1|size-100")
	sealed, _ := SealGCM([]byte("payload"), key, aad)
	if _, err := OpenGCM(sealed, key, []byte("backup-B|kid-1|enc-1|size-100")); err == nil {
		t.Fatal("expected auth failure when backup_id (AAD) tampered")
	}
	if _, err := OpenGCM(sealed, key, []byte("backup-A|kid-1|enc-2|size-100")); err == nil {
		t.Fatal("expected auth failure when enc (AAD) tampered")
	}
}

func TestOpenGCM_Truncated(t *testing.T) {
	key := randomKey32()
	sealed, _ := SealGCM([]byte("x"), key, nil)
	if _, err := OpenGCM(sealed[:nonceLen+8], key, nil); err == nil {
		t.Fatal("expected error on truncated sealed data")
	}
}

// SealGCM 每次用随机 nonce，同明文两次密文不同（nonce 不复用）。
func TestSealGCM_NonceIsRandom(t *testing.T) {
	key := randomKey32()
	a := mustSeal(t, key)
	b := mustSeal(t, key)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of same plaintext should differ (random nonce)")
	}
}

func mustSeal(t *testing.T, key []byte) []byte {
	t.Helper()
	s, err := SealGCM([]byte("same"), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
