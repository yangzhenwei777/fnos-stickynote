package model

import (
	"errors"
	"slices"
	"testing"

	"stickynote/database"
)

// v21.86 文件夹单测：CRUD / 重命名去重 / 排序移动 / 空文件夹删除保护 / 越权隔离。
// 复用 note_quota_test 的 DB setup 与 note_encrypt_test 的建用户助手。

func setupFolderDB(t *testing.T) func() {
	return setupQuotaDB(t)
}

// TestFolderCRUD 基本 CRUD：创建→列表（默认项在前）→重命名→删除。
func TestFolderCRUD(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_user")

	// 新用户：仅默认文件夹
	folders, err := ListFolders(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 || !folders[0].IsDefault || folders[0].ID != nil || folders[0].Name != DefaultFolderName {
		t.Fatalf("新用户应只有默认文件夹, got %+v", folders[0])
	}

	// 创建两个自定义文件夹
	f1, err := CreateFolder(uid, "工作")
	if err != nil {
		t.Fatal(err)
	}
	f2, err := CreateFolder(uid, "生活")
	if err != nil {
		t.Fatal(err)
	}
	if f1.SortOrder != 1 || f2.SortOrder != 2 {
		t.Fatalf("sort_order 应为 1,2 got %d,%d", f1.SortOrder, f2.SortOrder)
	}

	// 列表：默认 + 工作 + 生活
	folders, _ = ListFolders(uid)
	if len(folders) != 3 {
		t.Fatalf("应 3 个文件夹 got %d", len(folders))
	}
	if !folders[0].IsDefault || folders[1].Name != "工作" || folders[2].Name != "生活" {
		t.Fatalf("顺序错: %+v", folders)
	}

	// 重命名
	if err := RenameFolder(*f2.ID, uid, "日常生活"); err != nil {
		t.Fatal(err)
	}
	if _, err := GetFolder(*f2.ID, uid); err != nil {
		t.Fatal(err)
	}

	// 删除空文件夹
	if err := DeleteFolder(*f2.ID, uid); err != nil {
		t.Fatal(err)
	}
	folders, _ = ListFolders(uid)
	if len(folders) != 2 {
		t.Fatalf("删除后应 2 个 got %d", len(folders))
	}
}

// TestFolderNameValidation 名称校验：空名/超长/与默认重名/重名。
func TestFolderNameValidation(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_name_user")

	if _, err := CreateFolder(uid, "   "); err == nil {
		t.Fatal("空白名应拒绝")
	}
	if _, err := CreateFolder(uid, DefaultFolderName); err == nil {
		t.Fatal("与默认文件夹重名应拒绝")
	}
	longName := ""
	for i := 0; i < 33; i++ {
		longName += "字"
	}
	if _, err := CreateFolder(uid, longName); err == nil {
		t.Fatal("超长名应拒绝")
	}

	// 重名拒绝（含 trim 后重名）
	if _, err := CreateFolder(uid, "项目"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateFolder(uid, "项目"); !errors.Is(err, ErrFolderNameExists) {
		t.Fatalf("重名应 ErrFolderNameExists got %v", err)
	}
	if _, err := CreateFolder(uid, "  项目  "); !errors.Is(err, ErrFolderNameExists) {
		t.Fatalf("trim 后重名应拒绝 got %v", err)
	}

	// 重命名为已有名称
	f2, _ := CreateFolder(uid, "其他")
	if err := RenameFolder(*f2.ID, uid, "项目"); !errors.Is(err, ErrFolderNameExists) {
		t.Fatalf("重命名重名应拒绝 got %v", err)
	}
}

// TestFolderDeleteNotEmpty 非空文件夹删除保护；清空后可删。
func TestFolderDeleteNotEmpty(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_del_user")
	f, _ := CreateFolder(uid, "有笔记")
	fid := *f.ID

	// 建一篇归属该文件夹的笔记
	if _, err := CreateNoteInFolder(uid, "t", "<p>x</p>", "", 0, &fid); err != nil {
		t.Fatal(err)
	}

	// 非空 → 拒绝
	if err := DeleteFolder(fid, uid); !errors.Is(err, ErrFolderNotEmpty) {
		t.Fatalf("非空应 ErrFolderNotEmpty got %v", err)
	}

	// note_count 统计
	folders, _ := ListFolders(uid)
	var count int
	for _, fo := range folders {
		if fo.ID != nil && *fo.ID == fid {
			count = fo.NoteCount
		}
	}
	if count != 1 {
		t.Fatalf("note_count 应 1 got %d", count)
	}

	// 软删笔记后（回收站）→ 允许删文件夹（active=0）
	notes, _ := ListNotes(uid, 1, 10, "", "", "updated_at", nil)
	if err := DeleteNote(notes.Notes[0].ID, uid); err != nil {
		t.Fatal(err)
	}
	if err := DeleteFolder(fid, uid); err != nil {
		t.Fatalf("回收站清空后应可删, got %v", err)
	}
}

// TestFolderMove 排序移动：up / down / top + 幂等边界。
func TestFolderMove(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_move_user")
	fa, _ := CreateFolder(uid, "A")
	fb, _ := CreateFolder(uid, "B")
	fc, _ := CreateFolder(uid, "C")

	// C 上移 → 与 B 交换 → A C B
	if err := MoveFolder(*fc.ID, uid, "up"); err != nil {
		t.Fatal(err)
	}
	folders, _ := ListFolders(uid)
	if folders[1].Name != "A" || folders[2].Name != "C" || folders[3].Name != "B" {
		t.Fatalf("up 后应 A C B got %s %s %s", folders[1].Name, folders[2].Name, folders[3].Name)
	}

	// A 下移 → 与 C 交换 → C A B
	if err := MoveFolder(*fa.ID, uid, "down"); err != nil {
		t.Fatal(err)
	}
	folders, _ = ListFolders(uid)
	if folders[1].Name != "C" || folders[2].Name != "A" || folders[3].Name != "B" {
		t.Fatalf("down 后应 C A B got %s %s %s", folders[1].Name, folders[2].Name, folders[3].Name)
	}

	// B 置顶 → B C A
	if err := MoveFolder(*fb.ID, uid, "top"); err != nil {
		t.Fatal(err)
	}
	folders, _ = ListFolders(uid)
	if folders[1].Name != "B" || folders[2].Name != "C" || folders[3].Name != "A" {
		t.Fatalf("top 后应 B C A got %s %s %s", folders[1].Name, folders[2].Name, folders[3].Name)
	}

	// 顶部再上移 → 幂等成功
	if err := MoveFolder(*fb.ID, uid, "up"); err != nil {
		t.Fatalf("顶部 up 应幂等成功 got %v", err)
	}
	// 底部再下移 → 幂等成功
	if err := MoveFolder(*fa.ID, uid, "down"); err != nil {
		t.Fatalf("底部 down 应幂等成功 got %v", err)
	}
	// 非法方向
	if err := MoveFolder(*fa.ID, uid, "left"); err == nil {
		t.Fatal("非法方向应拒绝")
	}
}

// ---- v21.101 回归：top/bottom + sort_order 自愈 ----

func mustCreateFolder(t *testing.T, uid int64, name string) *Folder {
	t.Helper()
	f, err := CreateFolder(uid, name)
	if err != nil {
		t.Fatalf("创建文件夹 %s 失败: %v", name, err)
	}
	return f
}

func folderNamesOf(fs []Folder) []string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Name)
	}
	return names
}

// assertFolderOrder 断言自定义文件夹（跳过默认项）的显示顺序。
func assertFolderOrder(t *testing.T, uid int64, want []string) {
	t.Helper()
	folders, err := ListFolders(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != len(want)+1 {
		t.Fatalf("文件夹总数应 %d got %d", len(want)+1, len(folders))
	}
	got := folderNamesOf(folders[1:])
	if !slices.Equal(got, want) {
		t.Fatalf("顺序不符 want=%v got=%v", want, got)
	}
}

// assertSortOrderUnique 直接查库断言 sort_order 严格为 1..n 且无重复——
// 历史缺陷表现为「目标行 sort_order 从未被重写」，仅看列表顺序无法稳定捕获。
func assertSortOrderUnique(t *testing.T, uid int64, n int) {
	t.Helper()
	rows, err := database.GetDB().Query(
		"SELECT id, sort_order FROM note_folders WHERE user_id = ? ORDER BY sort_order ASC, id ASC", uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[int]bool{}
	count := 0
	for rows.Next() {
		var id int64
		var so int
		if err := rows.Scan(&id, &so); err != nil {
			t.Fatal(err)
		}
		count++
		if so < 1 || so > n {
			t.Fatalf("sort_order 越界 id=%d sort_order=%d (n=%d)", id, so, n)
		}
		if seen[so] {
			t.Fatalf("sort_order 重复: %d (id=%d)", so, id)
		}
		seen[so] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("自定义文件夹数应 %d got %d", n, count)
	}
	for i := 1; i <= n; i++ {
		if !seen[i] {
			t.Fatalf("sort_order 缺失 %d（应为连续 1..%d）", i, n)
		}
	}
}

// TestFolderMoveTopFromMiddle 回归：原 top 分支因 append 共享底层数组，
// 对「非末尾」元素置顶时目标元素丢失 → 顺序不变。原用例只覆盖末尾元素，成为盲区。
func TestFolderMoveTopFromMiddle(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_top_mid_user")
	fa := mustCreateFolder(t, uid, "A")
	fb := mustCreateFolder(t, uid, "B")
	fc := mustCreateFolder(t, uid, "C")
	fd := mustCreateFolder(t, uid, "D")
	assertFolderOrder(t, uid, []string{"A", "B", "C", "D"})

	// C(idx=2) 置顶 → C A B D
	if err := MoveFolder(*fc.ID, uid, "top"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"C", "A", "B", "D"})
	assertSortOrderUnique(t, uid, 4)

	// B(idx=2) 置顶 → B C A D
	if err := MoveFolder(*fb.ID, uid, "top"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"B", "C", "A", "D"})
	assertSortOrderUnique(t, uid, 4)

	// D(idx=3，末尾) 置顶 → D B C A
	if err := MoveFolder(*fd.ID, uid, "top"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"D", "B", "C", "A"})
	assertSortOrderUnique(t, uid, 4)

	// 首位再置顶 → 幂等，顺序不变
	if err := MoveFolder(*fd.ID, uid, "top"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"D", "B", "C", "A"})
	assertSortOrderUnique(t, uid, 4)

	if err := MoveFolder(*fa.ID, uid, "left"); err == nil {
		t.Fatal("非法方向应拒绝")
	}
}

// TestFolderMoveBottom v21.101 新增 position=bottom。
func TestFolderMoveBottom(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_bottom_user")
	fa := mustCreateFolder(t, uid, "A")
	fb := mustCreateFolder(t, uid, "B")
	mustCreateFolder(t, uid, "C")

	// B(idx=1) 移至底部 → A C B
	if err := MoveFolder(*fb.ID, uid, "bottom"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"A", "C", "B"})
	assertSortOrderUnique(t, uid, 3)

	// 末位再移底部 → 幂等
	if err := MoveFolder(*fb.ID, uid, "bottom"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"A", "C", "B"})

	// A(idx=0，首位) 移至底部 → C B A
	if err := MoveFolder(*fa.ID, uid, "bottom"); err != nil {
		t.Fatal(err)
	}
	assertFolderOrder(t, uid, []string{"C", "B", "A"})
	assertSortOrderUnique(t, uid, 3)
}

// TestFolderSortOrderNormalize 自愈：人为制造重复 sort_order 后规范化为 1..n。
func TestFolderSortOrderNormalize(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_normalize_user")
	mustCreateFolder(t, uid, "A")
	fb := mustCreateFolder(t, uid, "B")
	fc := mustCreateFolder(t, uid, "C")
	fd := mustCreateFolder(t, uid, "D")

	// 脏数据：B 的 sort_order 改成 3，与 C(3) 重复（模拟历史缺陷残留）
	if _, err := database.GetDB().Exec(
		"UPDATE note_folders SET sort_order = 3 WHERE id = ? AND user_id = ?", *fb.ID, uid); err != nil {
		t.Fatal(err)
	}

	fixed, err := NormalizeFolderSortOrder(uid)
	if err != nil {
		t.Fatal(err)
	}
	if fixed != 1 {
		t.Fatalf("应修正 1 行(B: 3→2) got %d", fixed)
	}
	// 次序按 (sort_order, id)：A(1) B(3) C(3) D(4) → 重排为 A1 B2 C3 D4
	assertFolderOrder(t, uid, []string{"A", "B", "C", "D"})
	assertSortOrderUnique(t, uid, 4)

	// 幂等：再跑一次零修正
	if fixed, err := NormalizeFolderSortOrder(uid); err != nil || fixed != 0 {
		t.Fatalf("规范化后应幂等零修正 got fixed=%d err=%v", fixed, err)
	}

	// 全量自愈入口可用
	total, err := NormalizeAllFolderSortOrders()
	if err != nil {
		t.Fatal(err)
	}
	if total < 0 {
		t.Fatalf("total 不应为负 got %d", total)
	}
	_ = fc
	_ = fd
}

// TestReorderIDsPure 纯函数单测：覆盖所有 position × idx 组合，并校验不修改入参。
func TestReorderIDsPure(t *testing.T) {
	base := []int64{1, 2, 3, 4}
	cases := []struct {
		pos  string
		idx  int
		want []int64
	}{
		{"up", 0, []int64{1, 2, 3, 4}},
		{"up", 1, []int64{2, 1, 3, 4}},
		{"up", 2, []int64{1, 3, 2, 4}},
		{"up", 3, []int64{1, 2, 4, 3}},
		{"down", 0, []int64{2, 1, 3, 4}},
		{"down", 1, []int64{1, 3, 2, 4}},
		{"down", 2, []int64{1, 2, 4, 3}},
		{"down", 3, []int64{1, 2, 3, 4}},
		{"top", 0, []int64{1, 2, 3, 4}},
		{"top", 1, []int64{2, 1, 3, 4}},
		{"top", 2, []int64{3, 1, 2, 4}},
		{"top", 3, []int64{4, 1, 2, 3}},
		{"bottom", 0, []int64{2, 3, 4, 1}},
		{"bottom", 1, []int64{1, 3, 4, 2}},
		{"bottom", 2, []int64{1, 2, 4, 3}},
		{"bottom", 3, []int64{1, 2, 3, 4}},
		{"unknown", 2, []int64{1, 2, 3, 4}},
	}
	for _, c := range cases {
		in := slices.Clone(base)
		got := reorderIDs(in, c.idx, c.pos)
		if !slices.Equal(got, c.want) {
			t.Errorf("reorderIDs(pos=%s idx=%d) = %v, want %v", c.pos, c.idx, got, c.want)
		}
		if !slices.Equal(in, base) {
			t.Errorf("reorderIDs 不应修改入参: pos=%s idx=%d in=%v", c.pos, c.idx, in)
		}
	}
	// 越界索引：原样返回
	if got := reorderIDs(base, 9, "top"); !slices.Equal(got, base) {
		t.Errorf("越界 idx 应原样返回 got %v", got)
	}
	if got := reorderIDs(nil, 0, "top"); len(got) != 0 {
		t.Errorf("nil 切片应返回空 got %v", got)
	}
}

// TestFolderOwnershipIsolation 越权隔离：用户 2 操作用户 1 的文件夹 → NotFound。
func TestFolderOwnershipIsolation(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid1 := createUserForTest(t, "owner1")
	uid2 := createUserForTest(t, "owner2")

	f, _ := CreateFolder(uid1, "私有")
	fid := *f.ID

	if _, err := GetFolder(fid, uid2); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("越权读应 NotFound got %v", err)
	}
	if err := RenameFolder(fid, uid2, "抢注"); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("越权重命名应 NotFound got %v", err)
	}
	if err := DeleteFolder(fid, uid2); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("越权删应 NotFound got %v", err)
	}
	if err := MoveFolder(fid, uid2, "up"); !errors.Is(err, ErrFolderNotFound) {
		t.Fatalf("越权移动应 NotFound got %v", err)
	}

	// 越权建笔记进他人文件夹 → 拒绝
	if _, err := CreateNoteInFolder(uid2, "t", "<p>x</p>", "", 0, &fid); err == nil {
		t.Fatal("越权挂笔记应拒绝")
	}
	// 用户 1 正常操作不受影响
	if err := RenameFolder(fid, uid1, "改名"); err != nil {
		t.Fatal(err)
	}
}

// TestListNotesFolderFilter ListNotes 的 folder_id 过滤：全部/默认(NULL)/指定。
func TestListNotesFolderFilter(t *testing.T) {
	cleanup := setupFolderDB(t)
	defer cleanup()

	uid := createUserForTest(t, "folder_filter_user")
	f, _ := CreateFolder(uid, "工作")
	fid := *f.ID

	// 2 篇默认 + 1 篇工作
	if _, err := CreateNoteInFolder(uid, "默认1", "<p>a</p>", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNoteInFolder(uid, "默认2", "<p>b</p>", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateNoteInFolder(uid, "工作1", "<p>c</p>", "", 0, &fid); err != nil {
		t.Fatal(err)
	}

	// 不传 → 全部 3 篇
	all, _ := ListNotes(uid, 1, 10, "", "", "updated_at", nil)
	if len(all.Notes) != 3 {
		t.Fatalf("全部应 3 got %d", len(all.Notes))
	}

	// folder_id=0 → 默认文件夹 2 篇
	zero := int64(0)
	def, _ := ListNotes(uid, 1, 10, "", "", "updated_at", &zero)
	if len(def.Notes) != 2 {
		t.Fatalf("默认应 2 got %d", len(def.Notes))
	}
	for _, n := range def.Notes {
		if n.FolderID != nil {
			t.Fatalf("默认文件夹笔记 folder_id 应 nil got %v", n.FolderID)
		}
	}

	// folder_id=工作 → 1 篇
	work, _ := ListNotes(uid, 1, 10, "", "", "updated_at", &fid)
	if len(work.Notes) != 1 {
		t.Fatalf("工作应 1 got %d", len(work.Notes))
	}
	if work.Notes[0].FolderID == nil || *work.Notes[0].FolderID != fid {
		t.Fatal("工作笔记 folder_id 应指向工作文件夹")
	}

	// GetNote 返回 folder_id
	got, _ := GetNote(work.Notes[0].ID, uid)
	if got.FolderID == nil || *got.FolderID != fid {
		t.Fatal("GetNote 应带 folder_id")
	}
}
