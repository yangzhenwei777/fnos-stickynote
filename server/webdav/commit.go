package webdav

import (
	"fmt"
	"os"
	"strings"
)

// 提交标记 / 可靠落盘模型（v4.3-final §0.3、§3）。
//
// 备份归档 = 单个不可切分的 .snbak 对象。WebDAV 没有原子 rename 保证、
// 也不保证断线后 PUT 是否落全，因此用"先立后破 + 尺寸核对"三段式：
//
//  1. 把本地文件 PUT 到 <final>.part-<nonce>（临时名，绝不与正式对象撞名）；
//  2. 尝试 MOVE part→final（多数服务端秒级、不占额外流量）；
//     MOVE 不被支持/歧义 → 回退：对 final 直接整文件 PUT（重开 reader，
//     不依赖第 1 步的流），代价是再传一份，但正确性优先；
//  3. HEAD final 核对存在且 size == 本地源大小。
//     HEAD 也歧义（超时/网络错）→ 不谎报成功，返回 ErrAmbiguousCommit，
//     交由上层据"备份已存在同尺寸即幂等成功"的规则复核，避免半途假绿。
//
// 注意：这里只保证"单个归档对象完整落盘"。跨对象的可见性提交（索引最后写、
// 索引内容核对、ORPHAN/CORRUPT 判定）属于 manager 的编排职责，不在本层。

// ErrAmbiguousCommit 表示写入结果无法判定（连接歧义 / HEAD 失败）。
// 上层必须先 HEAD/GET 复核，禁止据此直接标记失败或成功。
var ErrAmbiguousCommit = fmt.Errorf("WEBDAV_COMMIT_AMBIGUOUS")

// localSize 取本地文件大小（避免把整份读进内存）。
func localSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if fi.IsDir() {
		return 0, fmt.Errorf("%s 不是普通文件", path)
	}
	return fi.Size(), nil
}

// PutLocalVerified 把本地文件可靠落到 remote，返回核对后的远端尺寸。
// partPath 为临时对象名（调用方拼 nonce）；move 是否可用由本函数探测，
// 失败自动回退整文件 PUT。remote 与 partPath 均已含目录前缀。
func (c *Client) PutLocalVerified(localPath, remote, partPath string) (int64, error) {
	want, err := localSize(localPath)
	if err != nil {
		return 0, err
	}

	// ① 上传到临时名
	if err := c.putFile(localPath, partPath, want); err != nil {
		return 0, err
	}

	// ② 提交：先 MOVE，失败/歧义回退整文件 PUT
	if mvErr := c.Move(partPath, remote); mvErr == nil {
		return c.verifySize(remote, want)
	}
	// MOVE 不可用 → 直接 PUT 正式名（重开本地文件）
	if putErr := c.putFile(localPath, remote, want); putErr != nil {
		// PUT 歧义时，先探一次远端是否已到位（可能上一次已写成）
		if s, err := c.verifySize(remote, want); err == nil {
			return s, nil
		}
		return 0, fmt.Errorf("%w: %v", ErrAmbiguousCommit, putErr)
	}
	// 成功则清理临时对象（尽力而为，失败不影响已落地的正式对象）
	_ = c.Delete(partPath)
	return c.verifySize(remote, want)
}

// putFile 以已知长度 PUT 本地文件到 remote。
// 优先流式（省内存）；但部分 WebDAV 实现（实测 fnOS 自带 WebDAV）对带 Content-Length 的
// 流式 PUT 直接回 405 Method Not Allowed（而同一路径的普通缓冲 PUT 可以）——真机才暴露，
// x/net 假服务端不报。故流式失败即回退整文件缓冲 PUT（≤归档帽 256MB，内存可接受）。
func (c *Client) putFile(localPath, remote string, size int64) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	streamErr := c.PutStream(remote, f, size)
	_ = f.Close()
	if streamErr == nil {
		return nil
	}
	b, rerr := os.ReadFile(localPath)
	if rerr != nil {
		return streamErr // 读不出原文则保留流式错误
	}
	return c.PutBytes(remote, b)
}

// verifySize HEAD 核对远端对象存在且尺寸==want。
// HEAD 报网络类错误（非明确 404/其它 HTTP）→ 视为歧义。
func (c *Client) verifySize(remote string, want int64) (int64, error) {
	size, exists, err := c.Head(remote)
	if err != nil {
		return 0, fmt.Errorf("%w: HEAD %s: %v", ErrAmbiguousCommit, remote, err)
	}
	if !exists {
		return 0, fmt.Errorf("WEBDAV_COMMIT_MISSING: %s 未落地", remote)
	}
	if size != want {
		return size, fmt.Errorf("WEBDAV_COMMIT_SIZE_MISMATCH: %s 远端 %d != 本地 %d", remote, size, want)
	}
	return size, nil
}

// ProviderCapability 探测到的服务端能力，供 manager 决定提交策略与限额。
type ProviderCapability struct {
	SupportsMove  bool  // 支持服务端 MOVE（秒级提交，不二次占流量）
	MaxObjectSize int64 // 单对象上限（0=未知，用调用侧默认；坚果云=500MB）
}

// ProbeMove 用真实自测判定服务端是否支持 MOVE：在 dir 下写探针对象→MOVE→
// 确认落地→清理。比读 OPTIONS 头可靠（很多实现不报 MS-* 扩展头）。
// 任何失败都判为"不支持 MOVE"（回退整文件 PUT 永远可用），不返回错误、不阻断。
func (c *Client) ProbeMove(dir string) ProviderCapability {
	cap := ProviderCapability{MaxObjectSize: c.opt.MaxObjectSize}
	base := strings.TrimSuffix(dir, "/")
	probe := base + "/.stickynote-move-probe.tmp"
	target := base + "/.stickynote-move-probe.moved"
	if err := c.PutBytes(probe, []byte("ping")); err != nil {
		return cap
	}
	defer func() { _ = c.Delete(probe); _ = c.Delete(target) }()
	if err := c.Move(probe, target); err != nil {
		return cap
	}
	if _, exists, err := c.Head(target); err == nil && exists {
		cap.SupportsMove = true
	}
	return cap
}
