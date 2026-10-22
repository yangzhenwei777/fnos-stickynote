package database

import (
	"testing"
	"time"
)

// v21.108 P2-19：附件 GC 宽限期 env 可配的纯函数测试。
// 规则：UPLOAD_GRACE_HOURS 合法（0<n≤8760）生效；未设置/非法/越界一律回落 24h（fail-closed）。
func TestOrphanGraceDuration(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 24 * time.Hour},
		{"24", 24 * time.Hour},
		{"168", 168 * time.Hour},
		{"1", 1 * time.Hour},
		{"abc", 24 * time.Hour},
		{"0", 24 * time.Hour},
		{"-5", 24 * time.Hour},
		{"8761", 24 * time.Hour}, // 超一年上限
		{" 72 ", 72 * time.Hour}, // 容忍空格
	}
	for _, tc := range cases {
		t.Setenv("UPLOAD_GRACE_HOURS", tc.env)
		if got := orphanGraceDuration(); got != tc.want {
			t.Errorf("UPLOAD_GRACE_HOURS=%q 期望 %v，得到 %v", tc.env, tc.want, got)
		}
	}
}
