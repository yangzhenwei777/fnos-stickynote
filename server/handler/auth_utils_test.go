package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

// v21.96 密码策略测试（用户需求：取消强密码强制执行，仅提示强度偏弱）。
//
// 背景：v21.95 B-6 引入的 validatePasswordStrength 要求「≥12 位 + 四类字符至少三类」，
// 直接导致 123456 这类弱密码在注册时被 400 拒绝、用户无法完成注册。
// v21.96 改为「仅提示、不强制」：唯一硬下限 6 位，复杂度降级为提示信息。
//
// 本文件的作用是**钉死新语义**，防止后续再次引入复杂度拦截：
// 任何把 TestWeakPasswordNotBlocked 改红的改动，都是在回退用户明确要求的行为。

// TestValidatePasswordLength_Boundary 长度下限边界：5 位拒、6 位过（rune 计，非字节）。
func TestValidatePasswordLength_Boundary(t *testing.T) {
	cases := []struct {
		name    string
		pwd     string
		wantErr bool
	}{
		{"空密码", "", true},
		{"1 位", "a", true},
		{"5 位", "ab0!X", true},
		{"6 位下限刚好", "ab0!Xy", false},
		{"7 位", "Short7!", false},
		{"14 位强密码", "Passw0rd!2026", false},
		{"6 位中文（rune 计 6，字节 18）", "密码密码密码", false},
		{"5 位中文（rune 计 5）", "密码密码密", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePasswordLength(tc.pwd)
			if tc.wantErr && err == nil {
				t.Fatalf("%q 应被拒绝（<%d 位），却通过了", tc.pwd, minPasswordLen)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("%q 应放行，却被拒绝：%v", tc.pwd, err)
			}
		})
	}
}

// TestWeakPasswordNotBlocked **核心回归点**：典型弱密码一律放行，但被正确评为 weak。
//
// 这是本次需求的关键断言——弱密码过去会 400，现在必须能通过长度校验。
func TestWeakPasswordNotBlocked(t *testing.T) {
	weakSamples := []string{
		"123456",   // 纯数字 6 位
		"111111",   // 重复数字
		"abcdef",   // 纯小写 6 位
		"password", // 字典词
		"qwerty",   // 键盘序列
		"abc123",   // 小写+数字
		"66666666", // 8 位重复数字（仅 1 类，够不上 medium）
		"密码密码密码",   // 纯中文 6 字（rune 计 6；非 ASCII 全归"其他"类，仅 1 类）
		// 注：a1b2c3d4e5（10 位小写+数字）按规则落在 medium，不列入本组——
		//     本用例只钉死「弱密码必须放行」，不主张所有短密码都评 weak。
	}
	for _, pwd := range weakSamples {
		t.Run(pwd, func(t *testing.T) {
			if err := validatePasswordLength(pwd); err != nil {
				t.Fatalf("弱密码 %q 必须放行（需求：弱密码不拦截），却被拒：%v", pwd, err)
			}
			if got := passwordStrengthLevel(pwd); got != "weak" {
				t.Fatalf("弱密码 %q 应评为 weak，实际 %q", pwd, got)
			}
		})
	}
}

// TestPasswordStrengthLevel 强度分级规则：strong/medium/weak 三档边界。
func TestPasswordStrengthLevel(t *testing.T) {
	cases := []struct {
		pwd  string
		want string
	}{
		// strong：长度 ≥12 且字符类别 ≥3
		{"Passw0rd!2026", "strong"},  // 14 位，大小写+数字+符号 = 4 类
		{"Abcdefgh1234", "strong"},   // 12 位，大小写+数字 = 3 类
		{"AAAAAAAAAAAA", "weak"},     // 12 位但仅 1 类 → 类别不足，降为 weak
		{"abcdefghijkl12", "medium"}, // 14 位，小写+数字仅 2 类 → 够不上 strong，落 medium
		// medium：长度 ≥8 且字符类别 ≥2
		{"abcd1234", "medium"}, // 8 位，小写+数字 = 2 类
		{"Abcdefgh", "medium"}, // 8 位，大小写 = 2 类
		{"12345678", "weak"},   // 8 位但仅 1 类
		{"abcdefgh", "weak"},   // 8 位但仅 1 类
		// weak：长度不足或类别不足
		{"", "weak"},
		{"a", "weak"},
		{"Ab1!", "weak"},    // 4 位，4 类齐全但长度不足 8
		{"Abc1!xy", "weak"}, // 7 位，3 类但长度不足 8
	}
	for _, tc := range cases {
		t.Run(tc.pwd, func(t *testing.T) {
			if got := passwordStrengthLevel(tc.pwd); got != tc.want {
				t.Fatalf("%q 期望 %q，实际 %q", tc.pwd, tc.want, got)
			}
		})
	}
}

// TestPasswordStrengthLevel_MatchesLengthRule 分级与拦截规则不冲突：
// 任何通过长度校验的密码都必须有合法分级值（不会返回空串等异常值）。
func TestPasswordStrengthLevel_MatchesLengthRule(t *testing.T) {
	valid := map[string]bool{"weak": true, "medium": true, "strong": true}
	for _, pwd := range []string{"123456", "abcd1234", "Passw0rd!2026", "密码密码密码", "a b c d e f"} {
		if err := validatePasswordLength(pwd); err != nil {
			t.Fatalf("%q 应通过长度校验：%v", pwd, err)
		}
		if !valid[passwordStrengthLevel(pwd)] {
			t.Fatalf("%q 分级值非法：%q", pwd, passwordStrengthLevel(pwd))
		}
	}
}

// TestBindErrorMinMessageFollowsTag 修复回归：密码长度错误文案必须取自 tag 实际值，
// 不得硬编码（v21.96 前无论 min=8 还是 min=12 都回「密码至少 12 位」）。
func TestBindErrorMinMessageFollowsTag(t *testing.T) {
	type payload struct {
		Password string `json:"password" binding:"required,min=6"`
	}
	validate := validator.New()
	// 关键：项目结构体用的是 gin 的 `binding` tag，而非 validator 默认的 `validate` tag。
	// 不切换 tag 名的话，下面的校验会静默全部通过（tag 被忽略），用例就成了假阳性。
	validate.SetTagName("binding")

	err := validate.Struct(payload{Password: "123"})
	if err == nil {
		t.Fatal("3 位密码应触发校验错误")
	}

	c, w := newNoteContext("POST", "/x", "")
	bindError(c, err)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bindError 应返回 400，实际 %d", w.Code)
	}
	// tag 为 min=6 → 文案必须是「密码至少 6 位」，而不是历史硬编码的 12 位
	if !strings.Contains(w.Body.String(), "密码至少 6 位") {
		t.Fatalf("文案应与 tag 同源（min=6 → 6 位），实际：%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "12") {
		t.Fatalf("文案不得残留 12 位硬编码，实际：%s", w.Body.String())
	}
}
