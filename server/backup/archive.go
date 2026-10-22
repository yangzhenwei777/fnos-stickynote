package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func openRO(p string) (io.ReadCloser, error) { return os.Open(p) }

// ArchiveInput 打包入参。ManifestBytes 由 engine 在算完文件哈希后生成（含 file_hashes）。
type ArchiveInput struct {
	ManifestBytes []byte      // 内部 manifest.json（其 file_hashes 已填 db/jwt/upload 哈希）
	DBPath        string      // 快照 notepad.db 文件
	JWTSecretEnc  []byte      // jwt_secret.enc 内容（EncryptRaw 输出字符串的字节）
	Uploads       []FileEntry // 待打包附件（Name=basename, AbsPath=绝对路径）
}

// FileEntry 一个附件文件的归档名与其磁盘绝对路径。
type FileEntry struct {
	Name    string // basename，如 "1f2e….png"
	AbsPath string
}

const (
	arcManifest = "manifest.json"
	arcDB       = "notepad.db"
	arcJWT      = "jwt_secret.enc"
	arcUploads  = "uploads/"
)

// BuildArchive 把输入打成 tar.gz 明文字节（供 EnvelopeSeal 加密）。返回 payload。
// 附件名严格校验为 basename（禁路径穿越）。
func BuildArchive(in ArchiveInput) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	writeFile := func(name string, content []byte) error {
		hdr := &tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(content)
		return err
	}
	writeFileFromDisk := func(arcName, diskPath string) error {
		f, err := os.Open(diskPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", diskPath, err)
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: arcName, Mode: 0600, Size: st.Size(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		return err
	}

	if err := writeFile(arcManifest, in.ManifestBytes); err != nil {
		return nil, fmt.Errorf("archive manifest: %w", err)
	}
	if err := writeFileFromDisk(arcDB, in.DBPath); err != nil {
		return nil, fmt.Errorf("archive db: %w", err)
	}
	if len(in.JWTSecretEnc) > 0 {
		if err := writeFile(arcJWT, in.JWTSecretEnc); err != nil {
			return nil, fmt.Errorf("archive jwt: %w", err)
		}
	}
	for _, u := range in.Uploads {
		if err := writeFileFromDisk(arcUploads+u.Name, u.AbsPath); err != nil {
			return nil, fmt.Errorf("archive upload %s: %w", u.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// 解压资源上限（审计 GPT-08：防 gzip 炸弹/磁盘耗尽的纵深防御）。归档虽经 AES-GCM 认证加密
// （无密钥不可伪造），但管理员可能校验/恢复外部来源包，故解压层仍设硬上限。
const (
	maxExtractFileBytes  = 1 << 30 // 单文件解压上限 1 GiB
	maxExtractTotalBytes = 8 << 30 // 总解压字节上限 8 GiB
	maxExtractFiles      = 500000  // 最大文件条目数
)

// ExtractArchive 把 tar.gz 明文解到 destDir（仅恢复/验证用），逐文件返回计算出的 sha256 供比对。
// 严格防 zip-slip：只接受受控路径名（manifest.json/notepad.db/jwt_secret.enc/uploads/<basename>）。
func ExtractArchive(payload []byte, destDir string) (map[string]string, error) {
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return nil, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("extract gunzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	hashes := make(map[string]string)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("extract tar next: %w", err)
		}
		if len(hashes) >= maxExtractFiles {
			return nil, fmt.Errorf("extract: too many entries (> %d)", maxExtractFiles)
		}
		if hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("extract: unexpected entry type %q", hdr.Name)
		}
		clean, err := sanitizeArcName(hdr.Name)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(destDir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		// 边写边算 sha256；io.LimitReader 兜单文件解压上限（超限即判 gzip 炸弹/异常）
		hashWriter := newSHA256Writer(f)
		n, err := io.Copy(hashWriter, io.LimitReader(tr, maxExtractFileBytes+1))
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("extract write %s: %w", clean, err)
		}
		if n > maxExtractFileBytes {
			f.Close()
			return nil, fmt.Errorf("extract: %s exceeds per-file decompress limit (%d bytes)", clean, maxExtractFileBytes)
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		total += n
		if total > maxExtractTotalBytes {
			return nil, fmt.Errorf("extract: total decompressed size exceeds limit (%d bytes)", maxExtractTotalBytes)
		}
		hashes[clean] = hashWriter.hex()
	}
	return hashes, nil
}

// sanitizeArcName 白名单校验归档成员名，杜绝 .. / 绝对路径 / 反斜杠穿越。
func sanitizeArcName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\\') || path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("extract: unsafe entry name %q", name)
	}
	clean := path.Clean(name)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("extract: path traversal %q", name)
	}
	switch {
	case clean == arcManifest || clean == arcDB || clean == arcJWT:
		return clean, nil
	case strings.HasPrefix(clean, arcUploads):
		base := strings.TrimPrefix(clean, arcUploads)
		if base == "" || strings.ContainsRune(base, '/') {
			return "", fmt.Errorf("extract: unsafe uploads entry %q", name)
		}
		return arcUploads + base, nil
	default:
		return "", fmt.Errorf("extract: unexpected entry %q", name)
	}
}
