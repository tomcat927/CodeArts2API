package store

import (
	"path/filepath"
	"testing"
	"time"

	"codearts2api/internal/codearts"
)

func newCooldownStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		st.UpsertAccount(&codearts.Account{
			ID: id, Name: id, Enabled: true, Status: "ok",
			Session: &codearts.Session{AccessKey: "ak-" + id},
		})
	}
	return st
}

// TestNextAccountCooldownAndHalfOpen 回归 2026-11-03 的容量事故：
// 失败账号进入冷却后应被跳过，冷却到期必须重新参与轮转（半开探测），
// 而不是被「优先 Failures==0」的规则永久排除。
func TestNextAccountCooldownAndHalfOpen(t *testing.T) {
	st := newCooldownStore(t)

	st.MarkAccountResult("a", false, "unexpected EOF")
	a := st.Account("a")
	if a.Status != "error" || a.Failures != 1 {
		t.Fatalf("失败未计入: status=%q failures=%d", a.Status, a.Failures)
	}
	if a.CooldownUntil <= time.Now().Unix() {
		t.Fatalf("失败后应进入冷却，cooldown_until=%d", a.CooldownUntil)
	}

	// 冷却期内 a 不参与轮转。
	for i := 0; i < 4; i++ {
		got := st.NextAccount()
		if got == nil || got.ID != "b" {
			t.Fatalf("冷却期选中了 %v，期望全部是 b", got)
		}
	}

	// 冷却到期：a 重新进入轮转，且不能把健康账号挤掉。
	st.mu.Lock()
	for _, acc := range st.st.Accounts {
		if acc.ID == "a" {
			acc.CooldownUntil = time.Now().Add(-time.Second).Unix()
		}
	}
	st.mu.Unlock()

	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		got := st.NextAccount()
		if got == nil {
			t.Fatal("NextAccount 返回 nil")
		}
		seen[got.ID]++
	}
	if seen["a"] == 0 {
		t.Fatalf("冷却到期后 a 仍未参与轮转: %v", seen)
	}
	if seen["b"] == 0 {
		t.Fatalf("健康账号 b 被挤掉: %v", seen)
	}
}

// TestMarkAccountResultSuccessClearsCooldown 一次成功必须完全清掉冷却与失败计数。
func TestMarkAccountResultSuccessClearsCooldown(t *testing.T) {
	st := newCooldownStore(t)
	st.MarkAccountResult("a", false, "boom")
	st.MarkAccountResult("a", true, "")

	a := st.Account("a")
	if a.Status != "ok" || a.Failures != 0 || a.CooldownUntil != 0 || a.LastErr != "" {
		t.Fatalf("成功后状态未复位: status=%q failures=%d cooldown=%d last_err=%q",
			a.Status, a.Failures, a.CooldownUntil, a.LastErr)
	}
}

// TestClearAccountErrorsClearsCooldown 控制台「清错」必须同时解除冷却，
// 否则按钮按了但账号仍被跳过，用户会以为没生效。
func TestClearAccountErrorsClearsCooldown(t *testing.T) {
	st := newCooldownStore(t)
	st.MarkAccountResult("a", false, "boom")
	if !st.ClearAccountErrors("a") {
		t.Fatal("ClearAccountErrors 未找到账号")
	}
	a := st.Account("a")
	if a.Status != "ok" || a.Failures != 0 || a.CooldownUntil != 0 {
		t.Fatalf("清错后仍有残留: status=%q failures=%d cooldown=%d",
			a.Status, a.Failures, a.CooldownUntil)
	}
}

// TestNextAccountFallsBackWhenAllCooling 全部账号都在冷却时必须兜底，
// 不能因为「都在冷却」就把请求硬性拒绝。
func TestNextAccountFallsBackWhenAllCooling(t *testing.T) {
	st := newCooldownStore(t)
	st.MarkAccountResult("a", false, "x")
	st.MarkAccountResult("b", false, "y")
	if got := st.NextAccount(); got == nil {
		t.Fatal("全部账号冷却时应兜底返回一个账号")
	}
}

// TestFailureCooldownBacksOff 冷却时长随连续失败递增并封顶。
func TestFailureCooldownBacksOff(t *testing.T) {
	prev := time.Duration(0)
	for n := 1; n <= 8; n++ {
		d := failureCooldown(n)
		if d < prev {
			t.Fatalf("第 %d 次失败的冷却 %v 小于前一次 %v", n, d, prev)
		}
		prev = d
	}
	if failureCooldown(100) > time.Hour {
		t.Fatalf("冷却时长未封顶: %v", failureCooldown(100))
	}
	if failureCooldown(1) != 2*time.Minute {
		t.Fatalf("首次失败冷却 = %v，期望 2m（保证一次抖动后很快能半开重试）", failureCooldown(1))
	}
}
