package webdav

import "errors"

// ErrAuth 鉴权失败（401/403）。上层据此把目标标记为 AUTH_FAILED，
// 与网络/SSRF/存在性等错误区分（不同的用户提示与重试策略）。
var ErrAuth = errors.New("WEBDAV_AUTH_FAILED")
