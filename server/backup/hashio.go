package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// sha256Writer 边写文件边算 sha256（解包时同时落盘 + 校验哈希，避免二次读）。
type sha256Writer struct {
	f io.WriteCloser
	h hash.Hash
}

func newSHA256Writer(f *os.File) *sha256Writer { return &sha256Writer{f: f, h: sha256.New()} }

func (s *sha256Writer) Write(p []byte) (int, error) {
	if _, err := s.h.Write(p); err != nil {
		return 0, err
	}
	return s.f.Write(p)
}

func (s *sha256Writer) hex() string { return hex.EncodeToString(s.h.Sum(nil)) }
