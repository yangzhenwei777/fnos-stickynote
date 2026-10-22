//go:build windows

package dpapi

import (
	"bytes"
	"errors"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	secret := []byte("device-secret-32bytes-aaaaaaaaaaa")
	blob, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, secret) {
		t.Fatal("blob must not contain plaintext")
	}
	if string(blob[:4]) != blobMagic {
		t.Fatalf("self-describing header magic missing: %q", blob[:4])
	}
	out, err := Unprotect(blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, secret) {
		t.Fatal("roundtrip mismatch")
	}
}

func TestRandomizedPerCall(t *testing.T) {
	secret := []byte("same-input")
	a, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce randomization broken: identical ciphertexts leak equality")
	}
	for _, bl := range [][]byte{a, b} {
		if o, err := Unprotect(bl); err != nil || !bytes.Equal(o, secret) {
			t.Fatalf("both blobs must open to same plaintext: %v", err)
		}
	}
}

func TestTamperFailClosed(t *testing.T) {
	secret := []byte("payload-under-tamper")
	blob, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}

	flipCipher := bytes.Clone(blob)
	flipCipher[len(flipCipher)-1] ^= 0x01

	flipNonce := bytes.Clone(blob)
	flipNonce[5] ^= 0x01

	trunc := bytes.Clone(blob[:headerLen-1])

	foreign := append([]byte("XXXX"), blob[4:]...)

	badVer := bytes.Clone(blob)
	badVer[4] = 9

	// 双 blob 拼接（A 头 + B 密文段）：nonce 与密文解绑必须失败。
	spliced := append(bytes.Clone(blob[:headerLen]), second[headerLen:]...)

	cases := map[string][]byte{
		"flip-cipher-byte": flipCipher,
		"flip-nonce-byte":  flipNonce,
		"truncate-header":  trunc,
		"foreign-magic":    foreign,
		"unknown-version":  badVer,
		"splice-headers":   spliced,
	}
	for name, b := range cases {
		if _, err := Unprotect(b); !errors.Is(err, ErrCorrupted) {
			t.Fatalf("%s: want ErrCorrupted, got %v", name, err)
		}
	}
}

// TestMachineScopeGuard 机器级/跨用户不可解属真机验收面（第二 Windows 账户实测归刀 2
// 收口走查，见批次 4 验证门），此处钉桩提醒：勿把"本机能解"当"迁移安全"。
func TestMachineScopeGuard(t *testing.T) {
	t.Log("cross-user / cross-machine rejection is verified manually on a second Windows account (batch-4 gate)")
}
