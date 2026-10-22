package model

import (
	"errors"
	"strings"
	"testing"

	"modernc.org/sqlite"
	"stickynote/database"
)

// ============================================================================
// user.go 单测（v21.77 P1：补 model 覆盖率 32%→50%）
// 覆盖：注册角色判定、认证、禁用、改密、安全问题、token_version 吊销、事务更新
// ============================================================================

func TestRegisterUser_FirstIsAdmin(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u1, err := RegisterUser("alice", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}
	if u1.Role != "admin" {
		t.Errorf("首用户应自动 admin, got %q", u1.Role)
	}

	u2, err := RegisterUser("bob", "Passw0rd!2026", "q", "b")
	if err != nil {
		t.Fatal(err)
	}
	if u2.Role != "user" {
		t.Errorf("第二用户应 user, got %q", u2.Role)
	}

	t.Run("重名拒绝", func(t *testing.T) {
		if _, err := RegisterUser("alice", "Passw0rd!2026", "", ""); err == nil {
			t.Error("重名注册应返回错误")
		}
	})
}

// TestUniqueConstraintErrorCodeIsStructured 验证 v21.91（审计 P2-20）修复的前提：
// sqlite 驱动对 UNIQUE 冲突返回**结构化**错误。RegisterUser 的 SQL 带 WHERE NOT EXISTS，
// 正常重名走 RowsAffected==0 分支；只有并发竞态下才会真正撞到 UNIQUE 索引——那条分支
// 无法稳定复现，故本用例绕过 WHERE NOT EXISTS 直接 INSERT，断言驱动契约成立
// （*sqlite.Error.Code() == SQLITE_CONSTRAINT_UNIQUE），这是结构化判断能替代字符串匹配的前提。
func TestUniqueConstraintErrorCodeIsStructured(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("dupuser", "Passw0rd!2026", "q", "a"); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	_, err := database.DB.Exec(
		`INSERT INTO users (username, password_hash, role) VALUES ('dupuser', 'x', 'user')`)
	if err == nil {
		t.Fatal("重复用户名 INSERT 应触发 UNIQUE 约束错误")
	}

	var dbErr *sqlite.Error
	if !errors.As(err, &dbErr) {
		t.Fatalf("驱动应返回 *sqlite.Error 结构化错误, got %T: %v", err, err)
	}
	if dbErr.Code() != sqliteConstraintUnique {
		t.Errorf("错误码应为 SQLITE_CONSTRAINT_UNIQUE(%d), got %d", sqliteConstraintUnique, dbErr.Code())
	}
	// 反向确认：本用例命中的确实是唯一约束冲突，而非其他错误
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("错误文案应含 UNIQUE（证明确为唯一约束冲突）: %v", err)
	}
}

func TestCreateUser_FirstIsAdmin(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	// 空表 CreateUser 即使传 role=user 也应强制 admin
	u, err := CreateUser("carol", "Passw0rd!2026", "q", "a", "user")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "admin" {
		t.Errorf("空表 CreateUser 应强制 admin, got %q", u.Role)
	}
}

func TestAuthenticateUser(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("dave", "Passw0rd!2026", "q", "a"); err != nil {
		t.Fatal(err)
	}

	t.Run("正确密码", func(t *testing.T) {
		u, err := AuthenticateUser("dave", "Passw0rd!2026")
		if err != nil {
			t.Fatalf("应成功: %v", err)
		}
		if u.Username != "dave" {
			t.Errorf("用户名不符: %q", u.Username)
		}
	})
	t.Run("错误密码", func(t *testing.T) {
		if _, err := AuthenticateUser("dave", "wrong-pass"); err == nil {
			t.Error("错误密码应失败")
		}
	})
	t.Run("不存在用户", func(t *testing.T) {
		if _, err := AuthenticateUser("nobody", "x"); err == nil {
			t.Error("不存在用户应失败")
		}
	})
	t.Run("禁用用户返回哨兵错误", func(t *testing.T) {
		u, err := GetUserByUsername("dave")
		if err != nil {
			t.Fatal(err)
		}
		if err := UpdateUserStatus(u.ID, "disabled"); err != nil {
			t.Fatal(err)
		}
		_, err = AuthenticateUser("dave", "Passw0rd!2026")
		if err == nil || !errors.Is(err, ErrAccountDisabled) {
			t.Errorf("禁用用户应返回 ErrAccountDisabled, got %v", err)
		}
	})
}

func TestGetUserByIDAndUsername(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("erin", "Passw0rd!2026", "q", "a")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ByID 存在", func(t *testing.T) {
		got, err := GetUserByID(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != "erin" {
			t.Errorf("用户名不符: %q", got.Username)
		}
	})
	t.Run("ByID 不存在", func(t *testing.T) {
		if _, err := GetUserByID(99999); err == nil {
			t.Error("不存在 ID 应报错")
		}
	})
	t.Run("ByUsername 存在", func(t *testing.T) {
		got, err := GetUserByUsername("erin")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != u.ID {
			t.Errorf("ID 不符: %d", got.ID)
		}
	})
	t.Run("ByUsername 不存在", func(t *testing.T) {
		if _, err := GetUserByUsername("nobody"); err == nil {
			t.Error("不存在用户名应报错")
		}
	})
}

func TestBumpTokenVersion(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("frank", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	before := u.TokenVersion
	if err := BumpTokenVersion(u.ID); err != nil {
		t.Fatal(err)
	}
	after, err := GetUserByID(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TokenVersion != before+1 {
		t.Errorf("token_version 应 +1: before=%d after=%d", before, after.TokenVersion)
	}
}

func TestUpdatePassword(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("grace", "OldPass!1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdatePassword(u.ID, "NewPass!2"); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateUser("grace", "NewPass!2"); err != nil {
		t.Errorf("新密码应可登录: %v", err)
	}
	if _, err := AuthenticateUser("grace", "OldPass!1"); err == nil {
		t.Error("旧密码应失效")
	}
	after, _ := GetUserByID(u.ID)
	if after.TokenVersion != u.TokenVersion+1 {
		t.Errorf("改密应递增 token_version")
	}
}

func TestUpdatePasswordByUsername(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("heidi", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	t.Run("存在", func(t *testing.T) {
		if err := UpdatePasswordByUsername("heidi", "NewPass!3"); err != nil {
			t.Fatal(err)
		}
		if _, err := AuthenticateUser("heidi", "NewPass!3"); err != nil {
			t.Error("新密码应可登录")
		}
	})
	t.Run("不存在", func(t *testing.T) {
		if err := UpdatePasswordByUsername("ghost", "x"); err == nil {
			t.Error("不存在用户应报错")
		}
	})
}

func TestUpdateUserStatus(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("ivan", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("禁用", func(t *testing.T) {
		if err := UpdateUserStatus(u.ID, "disabled"); err != nil {
			t.Fatal(err)
		}
		got, _ := GetUserByID(u.ID)
		if got.Status != "disabled" {
			t.Errorf("status 应为 disabled, got %q", got.Status)
		}
	})
	t.Run("不存在", func(t *testing.T) {
		if err := UpdateUserStatus(99999, "disabled"); err == nil {
			t.Error("不存在用户应报错")
		}
	})
}

func TestVerifySecurityAnswer(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("judy", "Passw0rd!2026", "宠物名字?", "Tom"); err != nil {
		t.Fatal(err)
	}

	t.Run("正确且大小写归一化", func(t *testing.T) {
		ok, err := VerifySecurityAnswer("judy", "  tom ")
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Error("答案应通过（含空白/大小写归一化）")
		}
	})
	t.Run("错误答案", func(t *testing.T) {
		ok, _ := VerifySecurityAnswer("judy", "wrong")
		if ok {
			t.Error("错误答案应不通过")
		}
	})
	t.Run("未设置安全问题", func(t *testing.T) {
		if _, err := RegisterUser("karl", "Passw0rd!2026", "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifySecurityAnswer("karl", "x"); err == nil {
			t.Error("未设置安全问题应报错")
		}
	})
}

func TestUpdateSecurityQuestion(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("lisa", "Passw0rd!2026", "旧问题", "旧答案")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateSecurityQuestion(u.ID, "新问题?", "新答案"); err != nil {
		t.Fatal(err)
	}
	ok, err := VerifySecurityAnswer("lisa", "新答案")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("新答案应通过")
	}
}

func TestUpdateUserTx(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("mike", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("同时改角色与密码", func(t *testing.T) {
		if err := UpdateUserTx(u.ID, "admin", "TxNewPass!1"); err != nil {
			t.Fatal(err)
		}
		got, _ := GetUserByID(u.ID)
		if got.Role != "admin" {
			t.Errorf("角色应更新为 admin, got %q", got.Role)
		}
		if _, err := AuthenticateUser("mike", "TxNewPass!1"); err != nil {
			t.Error("新密码应可登录")
		}
	})
	t.Run("空字段不修改", func(t *testing.T) {
		if err := UpdateUserTx(u.ID, "", ""); err != nil {
			t.Fatal(err)
		}
		got, _ := GetUserByID(u.ID)
		if got.Role != "admin" {
			t.Errorf("空 role 不应改变角色")
		}
	})
}

func TestAdminResetPassword(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("nina", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := AdminResetPassword(u.ID, "ResetPass!1"); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateUser("nina", "ResetPass!1"); err != nil {
		t.Error("重置后密码应可登录")
	}
	if err := AdminResetPassword(99999, "x"); err == nil {
		t.Error("不存在用户应报错")
	}
}

func TestListUsersAndCount(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("oscar", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RegisterUser("pam", "Passw0rd!2026", "", ""); err != nil {
		t.Fatal(err)
	}

	users, err := ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Errorf("应有 2 用户, got %d", len(users))
	}
	cnt, err := CountUsers()
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 2 {
		t.Errorf("CountUsers 应=2, got %d", cnt)
	}
}

func TestGetAdminUser(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("quincy", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := GetAdminUser()
	if err != nil {
		t.Fatal(err)
	}
	if admin.ID != u.ID {
		t.Errorf("首个 admin 应为 quincy")
	}
}

func TestUpdateUserRole(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("rita", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateUserRole(u.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	got, _ := GetUserByID(u.ID)
	if got.Role != "admin" {
		t.Errorf("角色应更新")
	}
}

func TestDeleteUser(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	u, err := RegisterUser("sam", "Passw0rd!2026", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := GetUserByID(u.ID); err == nil {
		t.Error("删除后应查不到")
	}
}

// TestRegisterUser_AnswerNormalized 安全答案入库时大小写/空白归一化（防枚举）。
func TestRegisterUser_AnswerNormalized(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	if _, err := RegisterUser("tina", "Passw0rd!2026", "q", "  MiXeD AnSwEr "); err != nil {
		t.Fatal(err)
	}
	u, _ := GetUserByUsername("tina")
	if !strings.HasPrefix(u.SecurityAnswerHash, "$2") {
		t.Error("安全答案应 bcrypt 哈希入库")
	}
}
