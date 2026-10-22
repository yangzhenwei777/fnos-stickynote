package main

// remote_login_validation_test.go —— 登录入口的参数校验（批次 8-Alt 刀 6）。
// 钉的是"不该打的请求不要打"：空值与畸形地址必须在触网前被拒，否则既污染服务端的
// 登录防爆破计数（用户填错一次就吃 429），也要让用户等满 15s 超时才看到一句"失败"。
// 只测非法入参——合法入参会走到 EnsureTables/真登录，那面由真机走查覆盖。

import (
	"strings"
	"testing"
)

func TestRemoteLoginRejectsBeforeTouchingNetwork(t *testing.T) {
	l := &LocalService{}
	cases := []struct {
		name, base, user, pass, want string
	}{
		{"空服务器", "", "admin", "pw123456", "都要填"},
		{"空用户名", "http://127.0.0.1:13389", "", "pw123456", "都要填"},
		{"空密码", "http://127.0.0.1:13389", "admin", "", "都要填"},
		{"只有空格的用户名", "http://127.0.0.1:13389", "   ", "pw123456", "都要填"},
		{"缺协议头", "192.0.2.10:13389", "admin", "pw123456", "http:// 或 https://"},
		{"错误协议", "ftp://nas.local", "admin", "pw123456", "http:// 或 https://"},
		{"只有协议没有主机", "https://", "admin", "pw123456", "http:// 或 https://"},
		{"垃圾串", "not a url at all", "admin", "pw123456", "http:// 或 https://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.RemoteLogin(tc.base, tc.user, tc.pass)
			if err == nil {
				t.Fatalf("非法入参必须被拒，却返回了 %+v", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案要能指导用户改什么，得 %q，期望含 %q", err.Error(), tc.want)
			}
			// 中文可读文案直接上屏：不允许出现 Go 内部术语/堆栈
			for _, bad := range []string{"url.Parse", "nil pointer", "runtime error", "exit status"} {
				if strings.Contains(err.Error(), bad) {
					t.Fatalf("错误里漏出内部术语 %q：%v", bad, err)
				}
			}
		})
	}
}

// TestRemoteLoginTOTPRejectsIncompleteParams v21.151：二因子第二步的入参校验
// （空临时票/空验证码在触网前被拒；畸形地址文案与第一步一致）。
func TestRemoteLoginTOTPRejectsIncompleteParams(t *testing.T) {
	l := &LocalService{}
	cases := []struct {
		name, base, user, token, code, want string
	}{
		{"空临时票", "http://127.0.0.1:13389", "admin", "", "123456", "不完整"},
		{"空验证码", "http://127.0.0.1:13389", "admin", "T-1", "", "不完整"},
		{"缺协议头", "192.0.2.10:13389", "admin", "T-1", "123456", "http:// 或 https://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.RemoteLoginTOTP(tc.base, tc.user, tc.token, tc.code)
			if err == nil {
				t.Fatalf("非法入参必须被拒，却返回了 %+v", got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误文案要能指导用户改什么，得 %q，期望含 %q", err.Error(), tc.want)
			}
		})
	}
}
