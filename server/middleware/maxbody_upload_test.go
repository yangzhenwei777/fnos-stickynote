package middleware

import (
	"testing"
)

// v21.107 P1-13：/upload 网关限额纯函数测试。
// 规则：UPLOAD_MAX_MB + 4MB multipart 编码余量，下限 16MB（只放宽不收紧）。
func TestUploadBodyLimit(t *testing.T) {
	cases := []struct {
		env  string
		want int64
	}{
		{"", 34 << 20},     // 未设置 → 默认 30MB + 4MB
		{"30", 34 << 20},   // 显式 30
		{"2", 16 << 20},    // 小阈值 → 钳到 16MB 下限（业务层 400 仍先于网关生效）
		{"100", 104 << 20}, // 放宽场景
		{"abc", 34 << 20},  // 非法值 → 回落默认 30
		{"0", 34 << 20},    // 0/负数视为无效 → 默认
		{"-5", 34 << 20},
	}
	for _, tc := range cases {
		t.Setenv("UPLOAD_MAX_MB", tc.env)
		if got := UploadBodyLimit(); got != tc.want {
			t.Errorf("UPLOAD_MAX_MB=%q 期望 %d, 得到 %d", tc.env, tc.want, got)
		}
	}
}
