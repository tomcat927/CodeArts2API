package store

import (
	"path/filepath"
	"testing"
	"time"

	"codearts2api/internal/codearts"
)

// TestUpsertAccountMergesByDomainID 回归 v0.1.7 的真实故障：
// 浏览器登录创建的账号 UserData 为空，STS 的 AccessKey 每次登录都会轮换，
// 旧逻辑按 AK 去重必然匹配不上，于是同一个华为账号每重登一次就多一条记录。
// 现在 DomainID 相同就必须合并，且保留原 ID 与用户备注。
func TestUpsertAccountMergesByDomainID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "hw71755092", Enabled: true, Status: "ok", Note: "13800000000",
		Session: &codearts.Session{
			AccessKey: "AK-old", SecretKey: "SK-old", DomainID: "dom-1",
			ExpiresAt: time.Now().Add(-48 * time.Hour),
		},
	})

	isNew := st.UpsertAccount(&codearts.Account{
		ID: "a2", Name: "hw71755092", Enabled: true, Status: "ok",
		Session: &codearts.Session{
			AccessKey: "AK-new", SecretKey: "SK-new", DomainID: "dom-1",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	})
	if isNew {
		t.Fatal("同一 DomainID 重新登录不应新增账号")
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("账号数 = %d，期望 1", got)
	}
	got := st.Account("a1")
	if got == nil {
		t.Fatal("合并后未保留原 ID a1")
	}
	if got.Session == nil || got.Session.AccessKey != "AK-new" {
		t.Errorf("凭据未更新为最新: %+v", got.Session)
	}
	if got.Note != "13800000000" {
		t.Errorf("备注被清空: %q", got.Note)
	}
}

// TestUpsertAccountMergesByUserData 桌面端导入按 profile 目录去重，
// 同样不受 AK 轮换影响。
func TestUpsertAccountMergesByUserData(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("C:", "Users", "me", "AppData", "Roaming", "codearts-agent")
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "desktop", Enabled: true, Status: "ok", UserData: dir,
		Session: &codearts.Session{AccessKey: "AK1", SecretKey: "SK1", ExpiresAt: time.Now().Add(-time.Hour)},
	})
	isNew := st.UpsertAccount(&codearts.Account{
		ID: "a2", Name: "desktop", Enabled: true, Status: "ok", UserData: dir + string(filepath.Separator),
		Session: &codearts.Session{AccessKey: "AK2", SecretKey: "SK2", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if isNew {
		t.Fatal("同一 UserData 目录不应新增账号")
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("账号数 = %d，期望 1", got)
	}
}

// TestUpsertAccountDoesNotMergeSameNameWithoutIdentity 没有 DomainID / UserData
// 的账号无法安全判定是否同一账号，宁可保留两条，也不能按名字猜着合并。
func TestUpsertAccountDoesNotMergeSameNameWithoutIdentity(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "same-name", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK1", SecretKey: "SK1"},
	})
	st.UpsertAccount(&codearts.Account{
		ID: "a2", Name: "same-name", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK2", SecretKey: "SK2"},
	})
	if got := len(st.Accounts()); got != 2 {
		t.Fatalf("账号数 = %d，期望 2（不能按重名合并）", got)
	}
}

// TestUpsertAccountKeepsNewerCredential 合并时更旧的凭据不能覆盖现有有效凭据。
func TestUpsertAccountKeepsNewerCredential(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "hw", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK-new", SecretKey: "SK", DomainID: "d", ExpiresAt: time.Now().Add(2 * time.Hour)},
	})
	st.UpsertAccount(&codearts.Account{
		ID: "a2", Name: "hw", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK-stale", SecretKey: "SK", DomainID: "d", ExpiresAt: time.Now().Add(-2 * time.Hour)},
	})
	got := st.Account("a1")
	if got.Session.AccessKey != "AK-new" {
		t.Errorf("旧凭据覆盖了新凭据: %s", got.Session.AccessKey)
	}
}

// TestMergeDuplicatesCollapsesDomainGroups 存量收敛：
// 线上同一个华为账号曾有 3 条记录（1 条过期 error + 2 条新登录），
// 启动后应合并成 1 条，ID 沿用最早的一条，凭据取过期最晚的一份。
func TestMergeDuplicatesCollapsesDomainGroups(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.st.Accounts = []*codearts.Account{
		{
			ID: "old", Name: "hw71755092", Source: "manual", Enabled: true,
			Status: "error", Failures: 16, Note: "主号",
			Session: &codearts.Session{AccessKey: "AK1", DomainID: "dom-1", ExpiresAt: time.Now().Add(-72 * time.Hour)},
		},
		{
			ID: "mid", Name: "hw71755092", Source: "browser", Enabled: true, Status: "ok",
			Session: &codearts.Session{AccessKey: "AK2", DomainID: "dom-1", ExpiresAt: time.Now().Add(time.Hour)},
		},
		{
			ID: "new", Name: "hw71755092", Source: "browser", Enabled: true, Status: "ok",
			LastClaimDate: "2026-11-19",
			Session:       &codearts.Session{AccessKey: "AK3", DomainID: "dom-1", ExpiresAt: time.Now().Add(2 * time.Hour)},
		},
		{
			ID: "other", Name: "another", Source: "manual", Enabled: true, Status: "ok",
			Session: &codearts.Session{AccessKey: "AK9", DomainID: "dom-2", ExpiresAt: time.Now().Add(time.Hour)},
		},
	}
	st.mu.Unlock()

	groups, removed := st.MergeDuplicates()
	if groups != 1 || removed != 2 {
		t.Fatalf("groups,removed = %d,%d，期望 1,2", groups, removed)
	}
	accts := st.Accounts()
	if len(accts) != 2 {
		t.Fatalf("合并后账号数 = %d，期望 2", len(accts))
	}
	got := st.Account("old")
	if got == nil {
		t.Fatal("未保留最早记录的 ID")
	}
	if got.Session == nil || got.Session.AccessKey != "AK3" {
		t.Errorf("未采用过期时间最晚的凭据: %+v", got.Session)
	}
	if got.Note != "主号" {
		t.Errorf("备注未合并: %q", got.Note)
	}
	if got.LastClaimDate != "2026-11-19" {
		t.Errorf("签到记录未合并: %q", got.LastClaimDate)
	}
	if st.Account("dom-2") != nil {
		t.Error("不应保留已被合并的 ID")
	}
}

// TestUpsertAccountMergesDeadRecordWithoutDomainID 回归 2026-11-27：
// 早期导入的记录没有 domain_id（当时登录流程还没抓这个字段），重登后 AK 轮换
// 导致身份键对不上，旧死号成了孤儿、新登录另起一条（线上：重登
// hw026318765993194 后出现两条）。现在：新登录带 DomainID 时，对
// 「Status==error 且同名 / 备注含华为用户名」的无 domain 死号做兜底合并。
func TestUpsertAccountMergesDeadRecordWithoutDomainID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "old", Name: "CodeArts HSTAUQ8U", Enabled: true, Status: "error", Failures: 10,
		Note:    "13080025897 · hw026318765993194",
		Session: &codearts.Session{AccessKey: "AK-old", ExpiresAt: time.Now().Add(-48 * time.Hour)},
	})

	isNew := st.UpsertAccount(&codearts.Account{
		ID: "new", Name: "hw026318765993194", Enabled: true, Status: "ok",
		Session: &codearts.Session{
			AccessKey: "AK-new", SecretKey: "SK-new", DomainID: "dom-1",
			ExpiresAt: time.Now().Add(time.Hour),
		},
	})
	if isNew {
		t.Fatal("死号备注含同一华为用户名，重登应合并而不是新增")
	}
	if got := len(st.Accounts()); got != 1 {
		t.Fatalf("账号数 = %d，期望 1", got)
	}
	got := st.Account("old")
	if got == nil {
		t.Fatal("合并后未保留原 ID old")
	}
	if got.Session == nil || got.Session.AccessKey != "AK-new" || got.Session.DomainID != "dom-1" {
		t.Errorf("凭据未更新为新登录: %+v", got.Session)
	}
	if got.Status != "ok" || got.Failures != 0 {
		t.Errorf("合并后应恢复 ok: status=%q failures=%d", got.Status, got.Failures)
	}
	if got.Note != "13080025897 · hw026318765993194" {
		t.Errorf("备注被清空: %q", got.Note)
	}
}

// TestUpsertAccountSkipsAmbiguousDeadMatch 两条死号都可能匹配时视为歧义，
// 宁可新增一条也不能猜着并错。
func TestUpsertAccountSkipsAmbiguousDeadMatch(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d1", "d2"} {
		st.UpsertAccount(&codearts.Account{
			ID: id, Name: "CodeArts " + id, Enabled: true, Status: "error", Failures: 3,
			Note:    "手机号 · hw026318765993194",
			Session: &codearts.Session{AccessKey: "AK-" + id, ExpiresAt: time.Now().Add(-time.Hour)},
		})
	}
	isNew := st.UpsertAccount(&codearts.Account{
		ID: "new", Name: "hw026318765993194", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK-new", DomainID: "dom-1", ExpiresAt: time.Now().Add(time.Hour)},
	})
	if !isNew {
		t.Fatal("多条死号同时命中应视为歧义，不合并")
	}
	if got := len(st.Accounts()); got != 3 {
		t.Fatalf("账号数 = %d，期望 3", got)
	}
}

// TestUpsertAccountDoesNotMergeHealthyRecordWithoutDomainID 活号不参与兜底合并：
// 名字/备注撞上也不能把两条都活着的记录猜成同一条。
func TestUpsertAccountDoesNotMergeHealthyRecordWithoutDomainID(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "live", Name: "hw026318765993194", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK-live", ExpiresAt: time.Now().Add(time.Hour)},
	})
	isNew := st.UpsertAccount(&codearts.Account{
		ID: "new", Name: "hw026318765993194", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK-new", DomainID: "dom-1", ExpiresAt: time.Now().Add(2 * time.Hour)},
	})
	if !isNew {
		t.Fatal("活号不是死号，不应被兜底合并")
	}
	if got := len(st.Accounts()); got != 2 {
		t.Fatalf("账号数 = %d，期望 2", got)
	}
}
