package model

import "testing"

// ============================================================================
// config.go 单测（v21.77 P1：补 model 覆盖率）
// ============================================================================

func TestConfigCRUD(t *testing.T) {
	cleanup := setupEncDB(t)
	defer cleanup()

	t.Run("Set 后 Get 一致", func(t *testing.T) {
		if err := SetConfig("site_title", "便签"); err != nil {
			t.Fatal(err)
		}
		v, err := GetConfig("site_title")
		if err != nil {
			t.Fatal(err)
		}
		if v != "便签" {
			t.Errorf("value 不符: %q", v)
		}
	})

	t.Run("Set 覆盖更新", func(t *testing.T) {
		if err := SetConfig("site_title", "便签 v2"); err != nil {
			t.Fatal(err)
		}
		v, _ := GetConfig("site_title")
		if v != "便签 v2" {
			t.Errorf("应覆盖更新: %q", v)
		}
	})

	t.Run("不存在的 key 报错", func(t *testing.T) {
		if _, err := GetConfig("no_such_key"); err == nil {
			t.Error("不存在的 key 应报错")
		}
	})

	t.Run("GetAllConfigs 返回全部", func(t *testing.T) {
		if err := SetConfig("allow_register", "true"); err != nil {
			t.Fatal(err)
		}
		all, err := GetAllConfigs()
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, c := range all {
			found[c.Key] = true
		}
		if !found["site_title"] || !found["allow_register"] {
			t.Errorf("GetAllConfigs 应包含已设配置: %v", found)
		}
	})

	t.Run("GetAllConfigs 隐藏内部标记旗标", func(t *testing.T) {
		// 2.6.4：内部旗标 DB 行保留（迁移幂等依赖），但列表接口不下发
		if err := SetConfig("title_entities_unescaped", "true"); err != nil {
			t.Fatal(err)
		}
		all, err := GetAllConfigs()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range all {
			if IsInternalConfigKey(c.Key) {
				t.Errorf("列表不应含内部旗标: %q", c.Key)
			}
		}
		v, err := GetConfig("title_entities_unescaped")
		if err != nil || v != "true" {
			t.Errorf("内部旗标 DB 行必须保留: v=%q err=%v", v, err)
		}
	})

	t.Run("GetPublicConfigs 只含公开项", func(t *testing.T) {
		if err := SetConfig("allow_register", "false"); err != nil {
			t.Fatal(err)
		}
		if err := SetConfig("max_note_content_kb", "100"); err != nil {
			t.Fatal(err)
		}
		pub, err := GetPublicConfigs()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := pub["allow_register"]; !ok {
			t.Error("应含 allow_register")
		}
		if _, ok := pub["max_note_content_kb"]; ok {
			t.Error("不应含非公开配置 max_note_content_kb")
		}
	})
}
