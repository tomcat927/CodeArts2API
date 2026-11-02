package store

import (
	"path/filepath"
	"testing"
	"time"

	"codearts2api/internal/codearts"
)

// newScheduleStore 建一个带 n 个启用账号的空状态 store。
func newScheduleStore(t *testing.T, n int) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		st.UpsertAccount(&codearts.Account{
			ID: string(rune('a' + i)), Name: string(rune('a' + i)),
			Enabled: true, Status: "ok",
			Session: &codearts.Session{AccessKey: "ak", SecretKey: "sk"},
		})
	}
	return st
}

// setCredit 直接改 store 内部账号的积分快照（测试专用）。
func setCredit(t *testing.T, st *Store, id string, expiring float64, refreshedAt int64) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.st.Accounts {
		if a.ID == id {
			a.CreditExpiring = expiring
			a.CreditResetDate = "2026-12-31"
			a.CreditRefreshedAt = refreshedAt
			return
		}
	}
	t.Fatalf("账号 %s 不存在", id)
}

// TestNextAccountPrefersExpiringCredits 有账号带着未过期的「即将到期」额度时，
// 调度应只命中它，把额度在作废前用掉；没数据的账号照旧排在其后。
func TestNextAccountPrefersExpiringCredits(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Unix())

	for i := 0; i < 4; i++ {
		if got := st.NextAccount(); got.ID != "a" {
			t.Fatalf("第 %d 次选号 = %q，期望优先命中到期积分账号 a", i+1, got.ID)
		}
	}
}

// TestNextAccountRotatesExpiringPool 到期池内多个账号按额度降序轮询，
// 而不是把流量压死在额度最大的一个上（单账号并发上限会被打满）。
func TestNextAccountRotatesExpiringPool(t *testing.T) {
	st := newScheduleStore(t, 3)
	setCredit(t, st, "a", 3000, time.Now().Unix())
	setCredit(t, st, "b", 1000, time.Now().Unix())

	want := []string{"a", "b", "a", "b"}
	for i, id := range want {
		if got := st.NextAccount(); got.ID != id {
			t.Fatalf("第 %d 次选号 = %q，期望 %q（到期池内轮询）", i+1, got.ID, id)
		}
	}
}

// TestNextAccountIgnoresStaleCreditSnapshot 快照超过 24h 没刷新成功就不再作为
// 调度依据 —— 数据可能早就变了（额度用光 / 到期已过）。
func TestNextAccountIgnoresStaleCreditSnapshot(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Add(-25*time.Hour).Unix())
	setCredit(t, st, "b", 3000, time.Now().Unix())

	for i := 0; i < 3; i++ {
		if got := st.NextAccount(); got.ID != "b" {
			t.Fatalf("第 %d 次选号 = %q，期望只命中快照新鲜的 b", i+1, got.ID)
		}
	}
}

// TestNextAccountRespectsCreditPrioritySetting 关闭开关后即使快照新鲜也不改轮询。
func TestNextAccountRespectsCreditPrioritySetting(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Unix())
	st.UpdateSettings(func(s *Settings) { s.CreditPriority = false })

	got := map[string]int{}
	for i := 0; i < 4; i++ {
		got[st.NextAccount().ID]++
	}
	if got["a"] != 2 || got["b"] != 2 {
		t.Errorf("关闭到期优先后应恢复普通轮询 a/b 各半，实际 %v", got)
	}
}

// TestNextAccountSkipsFailingExpiringAccount 失败中的账号不进到期池，
// 否则「优先」会变成「优先撞墙」。
func TestNextAccountSkipsFailingExpiringAccount(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Unix())
	st.MarkAccountResult("a", false, "boom")

	for i := 0; i < 2; i++ {
		if got := st.NextAccount(); got.ID != "b" {
			t.Fatalf("第 %d 次选号 = %q，期望跳过失败中的到期账号 a", i+1, got.ID)
		}
	}
}

// TestNextAccountSkipsBusyExpiringAccount 到期账号在飞请求数打满单账号并发上限时
// 让溢出请求落到其他账号，而不是反复触发上游 TM.00001041 退避；
// 普通轮询路径同样跳过打满的账号（溢出请求不能原路撞回到期账号）。
func TestNextAccountSkipsBusyExpiringAccount(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Unix())

	for i := 0; i < perAccountInFlight; i++ {
		st.AcquireAccount("a")
	}
	if got := st.NextAccount(); got.ID != "b" {
		t.Fatalf("在飞打满后选号 = %q，期望落到 b", got.ID)
	}
	st.ReleaseAccount("a")
	if got := st.NextAccount(); got.ID != "a" {
		t.Fatalf("腾出并发空位后选号 = %q，期望恢复优先 a", got.ID)
	}
	st.ReleaseAccount("a")
	st.ReleaseAccount("a")
}

// TestNextAccountFallsBackWhenExpiringAccountDisabled 停用 / 删除后普通池照常工作。
func TestNextAccountFallsBackWhenExpiringAccountDisabled(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "a", 3000, time.Now().Unix())
	st.SetAccountEnabled("a", false)

	for i := 0; i < 2; i++ {
		if got := st.NextAccount(); got.ID != "b" {
			t.Fatalf("第 %d 次选号 = %q，期望落到 b", i+1, got.ID)
		}
	}
}

// TestSetAccountCredit 写回快照的三元组；查询失败（info=nil）不动已有数据。
func TestSetAccountCredit(t *testing.T) {
	st := newScheduleStore(t, 1)
	setCredit(t, st, "a", 3000, time.Now().Unix())

	if st.SetAccountCredit("a", nil) {
		t.Error("info=nil 应返回 false")
	}
	if got := st.Account("a"); got.CreditExpiring != 3000 {
		t.Errorf("查询失败不应改动已有快照，实际 expiring=%v", got.CreditExpiring)
	}

	before := time.Now().Unix()
	if !st.SetAccountCredit("a", &codearts.CreditInfo{Expiring: 800, ResetDate: "2026-11-30"}) {
		t.Fatal("SetAccountCredit 应返回 true")
	}
	got := st.Account("a")
	if got.CreditExpiring != 800 || got.CreditResetDate != "2026-11-30" || got.CreditRefreshedAt < before {
		t.Errorf("快照未正确写回: %+v", got)
	}
	if st.SetAccountCredit("missing", &codearts.CreditInfo{}) {
		t.Error("不存在的账号应返回 false")
	}
}
