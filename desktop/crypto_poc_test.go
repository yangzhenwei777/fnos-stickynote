package main

// crypto_poc_test.go — P0-C（v20 §125）：NPD1 桌面↔服务端字节级一致契约冻结。
//
// 桌面与 Web 端同为 stickynote/crypto 实现，本测试的真正价值不是"再测一遍 crypto"，
// 而是把**解密契约**钉成随仓库走的冻结文件 crypto_kat.json：
//   - 新格式（NPD1 magic + iter 600k BE + salt16 + nonce12 + GCM）
//   - legacy 4096 无 magic 兼容路径（v21.52 P2 判别修复的行为锁定）
//   - 错口令 / 密文篡改 / iter 越界(maxIter=2M) 三类拒绝
//   - Encrypt→Decrypt 往返
// 任何一次有人"顺手"改 KDF 参数/信封布局，这里必炸；Android Phase-2 直接复用
// 同一份 json 做跨语言对拍（v20 §65 向量清单的 NPD1 部分）。

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"stickynote/crypto"
)

type katDoc struct {
	GeneratedAt string `json:"generated_at"`
	Purpose     string `json:"purpose"`
	Vectors     []struct {
		Name          string `json:"name"`
		Format        string `json:"format"`
		Iter          int    `json:"iter"`
		Password      string `json:"password"`
		Plaintext     string `json:"plaintext"`
		CiphertextB64 string `json:"ciphertext_b64"`
	} `json:"vectors"`
}

func loadKAT(t *testing.T) katDoc {
	t.Helper()
	// go:embed 仅可用于包级变量且构建期文件必须存在；测试直读同目录文件更简单。
	b, err := os.ReadFile("crypto_kat.json")
	if err != nil {
		t.Fatalf("read crypto_kat.json: %v", err)
	}
	var d katDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Vectors) != 2 {
		t.Fatalf("期望 2 条向量，得 %d", len(d.Vectors))
	}
	return d
}

func TestKATDecryptParity(t *testing.T) {
	d := loadKAT(t)
	for _, v := range d.Vectors {
		got, err := crypto.Decrypt(v.CiphertextB64, v.Password)
		if err != nil {
			t.Fatalf("向量 %s 解密失败（桌面实现与服务端冻结契约漂移？）: %v", v.Name, err)
		}
		if got != v.Plaintext {
			t.Fatalf("向量 %s 明文不符: %q", v.Name, got)
		}
	}
}

func TestKATWrongPassword(t *testing.T) {
	d := loadKAT(t)
	if _, err := crypto.Decrypt(d.Vectors[0].CiphertextB64, "not-the-password"); err == nil {
		t.Fatal("错口令必须解密失败")
	}
}

func TestKATTamperedCiphertext(t *testing.T) {
	d := loadKAT(t)
	raw, err := base64.StdEncoding.DecodeString(d.Vectors[0].CiphertextB64)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0x01 // 翻 GCM tag 一比特
	if _, err := crypto.Decrypt(base64.StdEncoding.EncodeToString(raw), d.Vectors[0].Password); err == nil {
		t.Fatal("篡改密文必须被 AEAD 拒绝")
	}
}

func TestKATMagicDiscipline(t *testing.T) {
	// 判别修复回归（v21.52 P2）：新格式头 iter 越界（>2M）必须拒绝，
	// 绝不允许把 legacy 随机 salt 当前缀解析——构造"看似新格式"的坏 iter。
	d := loadKAT(t)
	raw, _ := base64.StdEncoding.DecodeString(d.Vectors[0].CiphertextB64)
	if string(raw[:4]) != "NPD1" {
		t.Fatal("新格式向量应带 NPD1 magic")
	}
	raw[4] = 0xFF // iter BE 前缀抬高 → 远超 maxIter
	if _, err := crypto.Decrypt(base64.StdEncoding.EncodeToString(raw), d.Vectors[0].Password); err == nil {
		t.Fatal("iter 越界应拒绝（防 CPU 耗尽型伪密文）")
	}
	// legacy 向量必须无 magic（旧格式判别的前提）。
	lraw, _ := base64.StdEncoding.DecodeString(d.Vectors[1].CiphertextB64)
	if string(lraw[:4]) == "NPD1" {
		t.Fatal("legacy 向量不应带 magic，否则兼容路径失去被测面")
	}
}

func TestRoundtripFreshEncrypt(t *testing.T) {
	const pw = "Roundtrip-Pass-2026!"
	plain := "往返向量：中文 🎹 emoji + <script>html</script> + 超长" + string(make([]byte, 4096))
	ct, err := crypto.Encrypt(plain, pw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := crypto.Decrypt(ct, pw)
	if err != nil || got != plain {
		t.Fatalf("往返失败: %v len=%d/%d", err, len(got), len(plain))
	}
}
