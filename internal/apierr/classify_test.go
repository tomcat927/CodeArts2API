package apierr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"codearts2api/internal/codearts"
)

// TestClassify 锁定内部错误到日志标签的映射顺序。
//
// 关键不变量：moderation 是 client_request 的子集，必须先于它判定，
// 否则审核拒绝会被记成普通「参数错误」，控制台上看不到独立的审核计数。
func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"内容审核（403 带码）", &codearts.UpstreamError{
			Status:  http.StatusForbidden,
			Message: `403: {"error_code":"ModelArts.81011","error":{"message":"Input image May contain sensitive information"}}`,
		}, "moderation"},
		{"内容审核（纯模板话术）", &codearts.UpstreamError{
			Status:  http.StatusForbidden,
			Message: "403: 抱歉，我无法提供关于您提问内容的建议。",
		}, "moderation"},
		{"参数越界（400）", &codearts.UpstreamError{
			Status:  http.StatusBadRequest,
			Message: "400: The request param is invalid",
		}, "client_request"},
		{"模态不符（406）", &codearts.UpstreamError{
			Status:  http.StatusNotAcceptable,
			Message: "406: InferHub.001001020.406 The request model is not multimodal",
		}, "client_request"},
		{"凭据失效", &codearts.CredentialsError{Account: "a", Reason: "已过期"}, "credentials"},
		{"账号池为空", NoAccount, "no_account"},
		{"上游 429", &codearts.UpstreamError{Status: http.StatusTooManyRequests, Message: "slow down"}, "transient"},
		{"上游 503", &codearts.UpstreamError{Status: http.StatusServiceUnavailable}, "transient"},
		{"并发上限（400 + TM.00001041）", &codearts.UpstreamError{
			Status:  http.StatusBadRequest,
			Message: "TM.00001041 concurrent limit",
		}, "transient"},
		{"上游 402（其他业务错误）", &codearts.UpstreamError{Status: 402, Message: "quota"}, "upstream"},
		{"客户端断开", context.Canceled, "client_abort"},
		{"网关内部错误", errors.New("json marshal failed"), "internal"},
	}
	for _, c := range cases {
		got := Classify(c.err)
		if got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestClassifyUnwrapsWrapped 包装后的错误仍要按根因分类。
func TestClassifyUnwrapsWrapped(t *testing.T) {
	mod := &codearts.UpstreamError{
		Status:  http.StatusForbidden,
		Message: "403: ModelArts.81011 Input image May contain sensitive information",
	}
	if got := Classify(fmt.Errorf("chat 失败: %w", mod)); got != "moderation" {
		t.Errorf("包装后的审核拒绝 = %q, want moderation", got)
	}
	creds := &codearts.CredentialsError{Account: "a", Reason: "r"}
	if got := Classify(fmt.Errorf("上层: %w", creds)); got != "credentials" {
		t.Errorf("包装后的凭据失效 = %q, want credentials", got)
	}
}

// TestClassifyModerationBeforeClientRequest 顺序回归：
// moderation 必须先于 client_request 判定，否则被吞成 client_request。
func TestClassifyModerationBeforeClientRequest(t *testing.T) {
	e := &codearts.UpstreamError{
		Status:  http.StatusForbidden,
		Message: "403: ModelArts.81011 我无法提供关于您提问内容的建议",
	}
	if Classify(e) != "moderation" {
		t.Error("审核拒绝必须分类为 moderation，不能被 client_request 吞掉")
	}
}
