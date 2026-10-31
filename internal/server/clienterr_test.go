package server

import (
	"context"
	"testing"
	"time"

	"codearts2api/internal/codearts"
)

// clientRequestErr 模拟上游对非法参数返回的业务错误。
func clientRequestErr() error {
	return codearts.UpstreamBusinessError(400,
		"400: The request param is invalid, Please check it")
}

// TestClientRequestErrorKeepsAccountHealthy 回归：请求本身非法时不得把账号标成 error。
//
// 线上现象：某个客户端反复发 max_tokens/temperature 越界的请求，上游回
// HTTP 200 + error_code，网关判定失败后无条件 MarkAccountResult(false)，
// 两个完全健康的账号都被标成 error，failures 累加到 26 / 18。
func TestClientRequestErrorKeepsAccountHealthy(t *testing.T) {
	s := newTestServer(t, 2, 3)

	_, err := s.withAccountRetry(context.Background(), 5*time.Second,
		func(context.Context, *codearts.Account) error {
			return clientRequestErr()
		})
	if err == nil {
		t.Fatal("应返回错误给客户端")
	}

	for _, id := range []string{"a", "b"} {
		acct := s.Store.Account(id)
		if acct.Failures != 0 {
			t.Errorf("账号 %s 被非法请求计入失败: failures=%d", id, acct.Failures)
		}
		if acct.Status != "ok" {
			t.Errorf("账号 %s 被非法请求标记为 %q", id, acct.Status)
		}
		if acct.LastErr != "" {
			t.Errorf("账号 %s 记录了不属于它的错误: %q", id, acct.LastErr)
		}
	}
}

// TestClientRequestErrorDoesNotRetryOtherAccounts 请求非法时换账号也没用，应立刻返回。
func TestClientRequestErrorDoesNotRetryOtherAccounts(t *testing.T) {
	s := newTestServer(t, 3, 3)

	var attempts int
	_, err := s.withAccountRetry(context.Background(), 5*time.Second,
		func(context.Context, *codearts.Account) error {
			attempts++
			return clientRequestErr()
		})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if attempts != 1 {
		t.Errorf("非法请求被重试了 %d 次，期望只尝试 1 次", attempts)
	}
}

// TestHardFailureStillMarksAccount 真实账号故障仍要计数（防止上面改动把熔断改没了）。
func TestHardFailureStillMarksAccount(t *testing.T) {
	s := newTestServer(t, 1, 2)

	_, err := s.withAccountRetry(context.Background(), 5*time.Second,
		func(context.Context, *codearts.Account) error {
			return codearts.UpstreamBusinessError(401, "401: bad signature")
		})
	if err == nil {
		t.Fatal("应返回错误")
	}
	acct := s.Store.Account("a")
	if acct.Failures == 0 {
		t.Error("401 属于账号故障，应计数")
	}
	if acct.Status != "error" {
		t.Errorf("401 应把账号标为 error，实际 %q", acct.Status)
	}
}

// multimodalMismatchErr 模拟「给非多模态模型发送图片」时上游返回的 406。
//
// 实测原文：HTTP 406 InferHub.001001020.406 The request model is not multimodal
func multimodalMismatchErr() error {
	return codearts.UpstreamBusinessError(406,
		"406: InferHub.001001020.406 The request model is not multimodal")
}

// TestMultimodalMismatchKeepsAccountHealthy 回归：406 模态不符属于请求侧错误，
// 不得计入账号失败。
//
// 线上现象（2026-10-31）：客户端把带图片的请求发给 deepseek-v4.1-flash /
// openpangu-2.0-* / kimi-k3 / glm-5.2-sft-harmony 等纯文本模型，上游回 406，
// 网关把 5 个账号中的 4 个标记成 error（failures=9）。
func TestMultimodalMismatchKeepsAccountHealthy(t *testing.T) {
	s := newTestServer(t, 2, 3)

	_, err := s.withAccountRetry(context.Background(), 5*time.Second,
		func(context.Context, *codearts.Account) error {
			return multimodalMismatchErr()
		})
	if err == nil {
		t.Fatal("应返回错误给客户端")
	}

	for _, id := range []string{"a", "b"} {
		acct := s.Store.Account(id)
		if acct.Failures != 0 {
			t.Errorf("账号 %s 被 406 计入失败: failures=%d", id, acct.Failures)
		}
		if acct.Status != "ok" {
			t.Errorf("账号 %s 被 406 标记为 %q", id, acct.Status)
		}
	}
}
