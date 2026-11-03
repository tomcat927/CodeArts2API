package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"codearts2api/internal/apierr"
	"codearts2api/internal/codearts"
	"codearts2api/internal/ratelimit"
	"codearts2api/internal/registry"
	"codearts2api/internal/store"
)

// newTestServer 构造一个只有内存状态的最小 Server（不触碰网络）。
func newTestServer(t *testing.T, accounts int, retries int) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.UpdateSettings(func(s *store.Settings) {
		s.MaxRetries = retries
		// 这个 helper 的用例关心的是重试序列，不是限流；默认关掉以免
		// 「连发多个请求」的用例被 429 干扰。限流自己的测试单独建服务。
		s.RateLimitEnabled = false
	})
	for i := 0; i < accounts; i++ {
		// AccessKey 必须唯一：UpsertAccount 按 AK 去重，全用 "ak" 会被合并成一个账号。
		id := string(rune('a' + i))
		st.UpsertAccount(&codearts.Account{
			ID:      id,
			Name:    "acct-" + id,
			Enabled: true,
			Status:  "ok",
			Session: &codearts.Session{AccessKey: "ak-" + id, SecretKey: "sk-" + id},
		})
	}
	return &Server{Store: st, Up: codearts.NewClient(), Reg: registry.New(),
		Limits: ratelimit.New(rateLimitConfig(st.Settings()), nil)}
}

// transientErr 模拟上游「每账号 3 个并发会话」打满。
func transientErr() error {
	return &codearts.UpstreamError{Status: 400, Message: `{"error_code":"TM.00001041"}`}
}

// TestWithAccountRetrySingleAccount 回归：只有 1 个账号时也必须对临时性故障重试。
//
// 回归背景：attempts 曾经被 `if maxTries > len(accts)` 钳成账号数量，
// 单账号场景下 MaxRetries=3 只尝试 1 次 —— 上游并发上限会直接变成客户端 502。
func TestWithAccountRetrySingleAccount(t *testing.T) {
	s := newTestServer(t, 1, 2)

	var attempts int32
	_, _, err := s.withAccountRetry(context.Background(), 5*time.Second, func(context.Context, *codearts.Account) error {
		atomic.AddInt32(&attempts, 1)
		return transientErr()
	})
	if err == nil {
		t.Fatal("全部尝试失败时应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Errorf("临时性故障没有重试：attempts=%d，期望 >= 2", got)
	}
}

// TestWithAccountRetryTransientKeepsAccountHealthy 临时性故障不应把账号标记为故障。
func TestWithAccountRetryTransientKeepsAccountHealthy(t *testing.T) {
	s := newTestServer(t, 1, 2)
	_, _, _ = s.withAccountRetry(context.Background(), 5*time.Second, func(context.Context, *codearts.Account) error {
		return transientErr()
	})
	acct := s.Store.Account("a")
	if acct.Failures != 0 {
		t.Errorf("临时性故障被计入账号失败次数: %d", acct.Failures)
	}
	if acct.Status != "ok" {
		t.Errorf("临时性故障把账号标记为 %q", acct.Status)
	}
}

// TestWithAccountRetryMarksHardFailure 非临时性故障要计一次失败，并换下一个账号。
func TestWithAccountRetryMarksHardFailure(t *testing.T) {
	s := newTestServer(t, 2, 2)
	hard := errors.New("凭据无效")

	var seen []string
	_, _, err := s.withAccountRetry(context.Background(), 5*time.Second, func(_ context.Context, a *codearts.Account) error {
		seen = append(seen, a.ID)
		return hard
	})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if len(seen) != 2 {
		t.Fatalf("尝试了 %d 次，期望 2 次（两个账号各一次）", len(seen))
	}
	// 多账号用例必须真的用到两个不同账号。
	// 回归背景：newTestServer 曾给所有账号同一个 AccessKey，UpsertAccount 按 AK 去重
	// 把 N 个账号合并成 1 个 —— 于是这个断言「过了」，但其实只测了单账号。
	if seen[0] == seen[1] {
		t.Fatalf("两次尝试都落在账号 %s，账号轮换没生效", seen[0])
	}
	for _, id := range seen {
		if acct := s.Store.Account(id); acct.Failures == 0 {
			t.Errorf("账号 %s 的硬失败没有计数", id)
		}
	}
}

// TestWithAccountRetryNetworkErrorKeepsAccountHealthy 回归 2026-11-03 线上事故：
// `unexpected EOF` / `context deadline exceeded` 属于网络层抖动，必须换号重试，
// **不能**计入账号故障 —— 否则一次抖动就把健康账号永久踢出轮转。
func TestWithAccountRetryNetworkErrorKeepsAccountHealthy(t *testing.T) {
	s := newTestServer(t, 2, 2)

	restore := waitRetry
	waitRetry = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { waitRetry = restore })

	var seen []string
	_, _, err := s.withAccountRetry(context.Background(), 5*time.Second, func(_ context.Context, a *codearts.Account) error {
		seen = append(seen, a.ID)
		return fmt.Errorf("Post upstream: %w", io.ErrUnexpectedEOF)
	})
	if err == nil {
		t.Fatal("全部尝试失败时应返回错误")
	}
	if len(seen) != 2 {
		t.Fatalf("网络抖动尝试了 %d 次，期望 2 次（换号重试）", len(seen))
	}
	if seen[0] == seen[1] {
		t.Fatalf("两次尝试都落在账号 %s，没有换号", seen[0])
	}
	for _, id := range seen {
		acct := s.Store.Account(id)
		if acct.Failures != 0 {
			t.Errorf("账号 %s 的网络抖动被计入失败次数: %d", id, acct.Failures)
		}
		if acct.Status != "ok" {
			t.Errorf("账号 %s 因网络抖动被标记为 %q", id, acct.Status)
		}
		if acct.CooldownUntil != 0 {
			t.Errorf("账号 %s 因网络抖动进入冷却: %d", id, acct.CooldownUntil)
		}
	}
}

// TestWithAccountRetryTimeoutKeepsAccountHealthy 单次请求超时同理：父 ctx 还活着，
// 不能当成客户端断开，也不能当成账号故障。
func TestWithAccountRetryTimeoutKeepsAccountHealthy(t *testing.T) {
	s := newTestServer(t, 1, 2)

	restore := waitRetry
	waitRetry = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { waitRetry = restore })

	var attempts int32
	_, _, err := s.withAccountRetry(context.Background(), 5*time.Second, func(context.Context, *codearts.Account) error {
		atomic.AddInt32(&attempts, 1)
		return context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got < 2 {
		t.Errorf("超时没有重试：attempts=%d", got)
	}
	acct := s.Store.Account("a")
	if acct.Failures != 0 || acct.Status != "ok" {
		t.Errorf("超时污染了账号: failures=%d status=%q", acct.Failures, acct.Status)
	}
}

// TestWithAccountRetryNoAccounts 账号池为空时返回明确错误。
func TestWithAccountRetryNoAccounts(t *testing.T) {
	s := newTestServer(t, 0, 3)
	_, _, err := s.withAccountRetry(context.Background(), time.Second, func(context.Context, *codearts.Account) error {
		t.Fatal("没有账号时不应调用 fn")
		return nil
	})
	if !errors.Is(err, apierr.NoAccount) {
		t.Errorf("err = %v，期望 apierr.NoAccount", err)
	}
}

// TestWithAccountRetryClientCancelStops 客户端断开后不应继续重试。
func TestWithAccountRetryClientCancelStops(t *testing.T) {
	s := newTestServer(t, 1, 4)
	ctx, cancel := context.WithCancel(context.Background())

	var attempts int32
	_, _, err := s.withAccountRetry(ctx, 5*time.Second, func(context.Context, *codearts.Account) error {
		atomic.AddInt32(&attempts, 1)
		cancel() // 模拟客户端断开
		return transientErr()
	})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("客户端已断开却继续重试：attempts=%d", got)
	}
}
