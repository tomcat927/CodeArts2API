package store

import (
	"testing"
	"time"
)

// expireCooldown 把账号的冷却直接改到过去（测试专用）。
func expireCooldown(t *testing.T, st *Store, id string) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.st.Accounts {
		if a.ID == id {
			a.CooldownUntil = time.Now().Add(-time.Second).Unix()
			return
		}
	}
	t.Fatalf("账号 %s 不存在", id)
}

// TestNextAccountProbesDueFailedAccountUnderCreditPriority 回归 2026-11-27 线上问题：
// credit_priority 开启且到期积分池一直非空时，普通池整体被饿着，导致冷却到期的
// 失败账号永远拿不到半开探测 —— 控制台显示「待探测」，但状态会永远卡在 error。
func TestNextAccountProbesDueFailedAccountUnderCreditPriority(t *testing.T) {
	st := newScheduleStore(t, 2)
	// b 一直有即将到期的额度：到期池始终非空，这正是触发饿死的前提。
	setCredit(t, st, "b", 3000, time.Now().Unix())

	st.MarkAccountResult("a", false, "boom")
	expireCooldown(t, st, "a")

	got := st.NextAccount()
	if got == nil || got.ID != "a" {
		t.Fatalf("冷却到期后选号 = %v，期望先给失败账号 a 一次半开探测", got)
	}

	// 探测名额已用掉（结果还没写回），紧跟的请求必须回到到期池，不能连续撞 a。
	for i := 0; i < 3; i++ {
		if got := st.NextAccount(); got == nil || got.ID != "b" {
			t.Fatalf("探测请求进行中选号 = %v，期望回到到期池账号 b", got)
		}
	}

	// 探测失败：重新进入更长的冷却，后续请求继续走到期池。
	st.MarkAccountResult("a", false, "still boom")
	for i := 0; i < 3; i++ {
		if got := st.NextAccount(); got == nil || got.ID != "b" {
			t.Fatalf("探测失败后选号 = %v，期望跳过冷却中的 a", got)
		}
	}
	if a := st.Account("a"); a.Failures != 2 {
		t.Fatalf("探测失败后 failures = %d，期望 2", a.Failures)
	}
}

// TestNextAccountProbeWaitsForCooldown 冷却期内不做半开探测，避免「优先撞墙」。
func TestNextAccountProbeWaitsForCooldown(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "b", 3000, time.Now().Unix())

	st.MarkAccountResult("a", false, "boom")
	for i := 0; i < 3; i++ {
		if got := st.NextAccount(); got == nil || got.ID != "b" {
			t.Fatalf("冷却期内选号 = %v，期望跳过 a", got)
		}
	}
}

// TestNextAccountDoesNotProbeDisabledAccount 停用的账号不参与探测。
func TestNextAccountDoesNotProbeDisabledAccount(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "b", 3000, time.Now().Unix())

	st.MarkAccountResult("a", false, "boom")
	expireCooldown(t, st, "a")
	st.SetAccountEnabled("a", false)

	for i := 0; i < 3; i++ {
		if got := st.NextAccount(); got == nil || got.ID != "b" {
			t.Fatalf("停用账号被选号 = %v，期望只落到 b", got)
		}
	}
}

// TestNextAccountProbeAgainAfterGap 探测间隔过后，仍未恢复的失败账号可以再探一次；
// 间隔本身只防并发重复选中，不会把账号永久排除。
func TestNextAccountProbeAgainAfterGap(t *testing.T) {
	st := newScheduleStore(t, 2)
	setCredit(t, st, "b", 3000, time.Now().Unix())

	st.MarkAccountResult("a", false, "boom")
	expireCooldown(t, st, "a")
	if got := st.NextAccount(); got == nil || got.ID != "a" {
		t.Fatalf("首次探测选号 = %v，期望 a", got)
	}

	st.mu.Lock()
	st.probeAt["a"] = time.Now().Add(-2 * probeGap).Unix()
	st.mu.Unlock()

	if got := st.NextAccount(); got == nil || got.ID != "a" {
		t.Fatalf("间隔过后选号 = %v，期望允许再次探测 a", got)
	}
}
