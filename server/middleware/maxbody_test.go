package middleware

import (
	"testing"
	"time"

	"stickynote/database"
	"stickynote/model"
)

// 动态请求体上限测试（v21.55 P2：跟随前台【单篇笔记上限】配置）
// 覆盖：X=0 兜底 100MB / 有效 X 带 1.2 倍余量 / 下限 16MB / 封顶 128MB（v21.90 N-04 由 256MB 收敛）/ 缓存刷新。

func setupBodyDB(t *testing.T) func() {
	t.Helper()
	if err := database.Init(tempDBPath(t), "99.0.0"); err != nil {
		t.Fatalf("db init: %v", err)
	}
	return func() {
		database.DB.Close()
		database.DB = nil
	}
}

func resetBodyCache() {
	dynamicBodyCache.mu.Lock()
	dynamicBodyCache.expires = time.Time{}
	dynamicBodyCache.limit = 0
	dynamicBodyCache.mu.Unlock()
}

// TestDynamicLimit_ConfigZero 配置 0（不限）→ 100MB 兜底。
func TestDynamicLimit_ConfigZero(t *testing.T) {
	cleanup := setupBodyDB(t)
	defer cleanup()
	resetBodyCache()

	model.SetConfig("max_note_content_kb", "0")
	limit := DynamicMaxBodyLimit()
	if limit != 100<<20 {
		t.Fatalf("X=0 应 100MB 兜底, got %d", limit)
	}
}

// TestDynamicLimit_EffectiveX 配置 50MB → 上限 = max(50×1.2, 50+1)MB = 60MB。
func TestDynamicLimit_EffectiveX(t *testing.T) {
	cleanup := setupBodyDB(t)
	defer cleanup()
	resetBodyCache()

	model.SetConfig("max_note_content_kb", "51200") // 50MB
	limit := DynamicMaxBodyLimit()
	want := int64(51200*1024 + 51200*1024/5) // 60MB (1.2x)
	if limit != want {
		t.Fatalf("50MB 配置应 60MB 上限, got %d want %d", limit, want)
	}
}

// TestDynamicLimit_MinFloor 配置过小（1KB）→ 下限 16MB。
func TestDynamicLimit_MinFloor(t *testing.T) {
	cleanup := setupBodyDB(t)
	defer cleanup()
	resetBodyCache()

	model.SetConfig("max_note_content_kb", "1")
	limit := DynamicMaxBodyLimit()
	if limit != 16<<20 {
		t.Fatalf("1KB 配置应 16MB 下限, got %d", limit)
	}
}

// TestDynamicLimit_MaxCap 配置超大（1GB）→ 封顶 128MB（v21.90 N-04 收敛）。
func TestDynamicLimit_MaxCap(t *testing.T) {
	cleanup := setupBodyDB(t)
	defer cleanup()
	resetBodyCache()

	model.SetConfig("max_note_content_kb", "1048576") // 1GB
	limit := DynamicMaxBodyLimit()
	if limit != 128<<20 {
		t.Fatalf("1GB 配置应 128MB 封顶, got %d", limit)
	}
}

// TestDynamicLimit_CacheRefresh 改配置后缓存刷新（5s 内旧值，超时后新值）。
func TestDynamicLimit_CacheRefresh(t *testing.T) {
	cleanup := setupBodyDB(t)
	defer cleanup()
	resetBodyCache()

	model.SetConfig("max_note_content_kb", "10240") // 10MB
	first := DynamicMaxBodyLimit()
	if first < 16<<20 {
		t.Fatalf("10MB 配置应 ≥16MB 下限, got %d", first)
	}

	// 改配置后立即读（缓存内旧值）
	model.SetConfig("max_note_content_kb", "51200") // 50MB
	stillOld := DynamicMaxBodyLimit()
	if stillOld != first {
		t.Fatalf("缓存期内应保持旧值, got %d want %d", stillOld, first)
	}

	// 强制过期后读新值
	dynamicBodyCache.mu.Lock()
	dynamicBodyCache.expires = time.Time{}
	dynamicBodyCache.mu.Unlock()
	after := DynamicMaxBodyLimit()
	if after == first {
		t.Fatal("缓存过期后应读取新配置值")
	}
}
