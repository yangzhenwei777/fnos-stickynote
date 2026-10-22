// Package dpapi 封装 Windows DPAPI（CryptProtectData，用户域）——批次 4 · P3 LocalAuth
// 的机器绑定层（离线方案 v20 P3 / 红线 29 无监听下的"设备会话"凭据落盘）。
//
// 纪律：
//   - 密文自描述：magic "SDP1"(4) + version(1) + nonce(8) + 密文变长；nonce 参与 AAD 绑定
//     （经 optionalEntropy 拼入），使同一段明文的两次加密产生不同密文（防"相同凭据→相同密文"
//     的存在性探测），且密文挪位/换 nonce 即解密失败（fail-closed）。
//   - 应用熵 = 包内常量。注意口径：DPAPI 用户域对**同用户任意进程**可解（微软官方语义），
//     应用熵只挡跨应用/跨机拷贝，**不防同用户恶意进程**——威胁模型与 §87 一致（防拷文件走，
//     不防提权/木马），不吹牛更强保证。
//   - 非 Windows 恒 ErrUnsupported（明确失败，禁静默降级成明文存储；CI Linux 上相关测试
//     显式 SKIP 并注明"Windows-only"，防"假绿"）。
package dpapi

import "errors"

// ErrUnsupported 本平台无 DPAPI（非 Windows）。
var ErrUnsupported = errors.New("dpapi: not supported on this platform (windows-only)")

// ErrCorrupted 解封失败：篡改/换机/跨用户/nonce 不符统一归此哨兵（不区分细节，防 oracle）。
var ErrCorrupted = errors.New("dpapi: protected blob failed to open")
