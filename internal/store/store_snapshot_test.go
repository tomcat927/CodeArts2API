package store

import (
	"path/filepath"
	"sync"
	"testing"

	"codearts2api/internal/codearts"
)

// TestAccountSnapshotsAreIsolated 验证对外只暴露快照：
// 改快照不会影响 store，改 store 也不会影响已发出的快照。
//
// 回归背景：Accounts/Account/NextAccount 曾经直接返回内部指针，
// 控制台的 toggle / clear-errors 与请求路径的 MarkAccountResult
// 会在没有锁保护的情况下读写同一块内存。
func TestAccountSnapshotsAreIsolated(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "n1", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "ak1", SecretKey: "sk1"},
	})

	// 1) 改快照不影响 store
	snap := st.Account("a1")
	snap.Enabled = false
	snap.Failures = 99
	snap.Status = "hacked"
	fresh := st.Account("a1")
	if !fresh.Enabled || fresh.Failures != 0 || fresh.Status != "ok" {
		t.Errorf("修改快照污染了 store: enabled=%v failures=%d status=%q",
			fresh.Enabled, fresh.Failures, fresh.Status)
	}

	// 2) 改 store 不影响已发出的快照
	snap2 := st.Account("a1")
	st.SetAccountEnabled("a1", false)
	st.MarkAccountResult("a1", false, "boom")
	if !snap2.Enabled || snap2.Status != "ok" || snap2.Failures != 0 {
		t.Errorf("store 的变更穿透到了旧快照: enabled=%v status=%q failures=%d",
			snap2.Enabled, snap2.Status, snap2.Failures)
	}
	if fresh2 := st.Account("a1"); fresh2.Enabled || fresh2.Status != "error" || fresh2.Failures != 1 {
		t.Errorf("store 自身的变更没生效: enabled=%v status=%q failures=%d",
			fresh2.Enabled, fresh2.Status, fresh2.Failures)
	}

	// 3) 入库时拷贝：调用方之后改入参不应影响 store
	acct := &codearts.Account{ID: "a2", Name: "n2", Enabled: true, Session: &codearts.Session{AccessKey: "ak2"}}
	st.UpsertAccount(acct)
	acct.Enabled = false
	if got := st.Account("a2"); !got.Enabled {
		t.Error("UpsertAccount 后修改入参影响了 store")
	}
}

// TestAccountNoteSurvivesMarkAccountResult 备注（如手机号）由用户维护，
// 调用结果只能写 LastErr / Failures，不能覆盖 Note。
//
// 回归背景：MarkAccountResult 成功分支曾经在 note 非空时写 a.Note，
// 把用户备注换成上游返回的提示语。
func TestAccountNoteSurvivesMarkAccountResult(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "n1", Enabled: true, Status: "ok", Note: "13800000000",
		Session: &codearts.Session{AccessKey: "ak1", SecretKey: "sk1"},
	})

	st.MarkAccountResult("a1", true, "conversation expired")
	if got := st.Account("a1"); got.Note != "13800000000" {
		t.Errorf("成功调用覆盖了备注: %q", got.Note)
	}

	st.MarkAccountResult("a1", false, "boom")
	got := st.Account("a1")
	if got.Note != "13800000000" {
		t.Errorf("失败调用覆盖了备注: %q", got.Note)
	}
	if got.LastErr != "boom" {
		t.Errorf("错误信息应写 LastErr，实际 %q", got.LastErr)
	}
}

// TestSetAccountNoteAndRename 校验备注 / 改名的基本语义。
func TestSetAccountNoteAndRename(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{ID: "a1", Name: "old", Enabled: true, Status: "ok"})

	if !st.SetAccountNote("a1", "13900000000") {
		t.Fatal("SetAccountNote 应返回 true")
	}
	if got := st.Account("a1"); got.Note != "13900000000" {
		t.Errorf("备注 = %q，期望 13900000000", got.Note)
	}
	if st.SetAccountNote("missing", "x") {
		t.Error("不存在的账号应返回 false")
	}

	if !st.RenameAccount("a1", " 新名字 ") {
		t.Fatal("RenameAccount 应返回 true")
	}
	if got := st.Account("a1"); got.Name != "新名字" {
		t.Errorf("名称 = %q，期望「新名字」（应去掉首尾空白）", got.Name)
	}
	if st.RenameAccount("a1", "   ") {
		t.Error("空名应被拒绝")
	}
	if got := st.Account("a1"); got.Name != "新名字" {
		t.Errorf("空名被拒后名称不该变化，实际 %q", got.Name)
	}
}

// TestUpsertAccountPreservesNote 重新导入同一账号的登录态时，
// 入参没带备注应保留原备注；显式带备注则覆盖。
func TestUpsertAccountPreservesNote(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpsertAccount(&codearts.Account{
		ID: "a1", Name: "n", Enabled: true, Status: "ok", Note: "13800000000",
		Session: &codearts.Session{AccessKey: "AK1", SecretKey: "SK1"},
	})

	// 同一 AK 再次导入（模拟「导入本机登录态」），没带备注。
	st.UpsertAccount(&codearts.Account{
		ID: "a2", Name: "n", Enabled: true, Status: "ok",
		Session: &codearts.Session{AccessKey: "AK1", SecretKey: "SK2"},
	})
	if got := st.Account("a1"); got == nil || got.Note != "13800000000" {
		t.Errorf("重新导入清空了备注: %+v", got)
	}

	// 显式带备注时覆盖。
	st.UpsertAccount(&codearts.Account{
		ID: "a3", Name: "n", Enabled: true, Status: "ok", Note: "13900000000",
		Session: &codearts.Session{AccessKey: "AK1", SecretKey: "SK3"},
	})
	if got := st.Account("a1"); got == nil || got.Note != "13900000000" {
		t.Errorf("显式备注未覆盖: %+v", got)
	}
}

// TestConcurrentAccountAccessIsSafe 并发跑「控制台读 + 请求路径写」，
// 配合 -race 即可发现锁缺失（无 -race 时至少保证不死锁、不 panic）。
func TestConcurrentAccountAccessIsSafe(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		st.UpsertAccount(&codearts.Account{
			ID: string(rune('a' + i)), Enabled: true, Status: "ok",
			Session: &codearts.Session{AccessKey: "ak"},
		})
	}

	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				for _, a := range st.Accounts() {
					_ = a.DisplayName()
					_ = a.Status
					_ = a.Session.AccessKey
				}
			}
		}()
	}
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if a := st.NextAccount(); a != nil {
					st.MarkAccountResult(a.ID, i%2 == 0, "")
				}
				st.SetAccountEnabled("a", i%2 == 0)
				st.ClearAccountErrors("a")
			}
		}()
	}
	wg.Wait()
}
