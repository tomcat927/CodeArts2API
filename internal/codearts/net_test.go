package codearts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"testing"
)

// TestIsTransientNetwork 锁定「网络层抖动」的判定边界。
//
// 回归背景（2026-11-03）：`unexpected EOF` 与 `context deadline exceeded` 既不被
// IsTransientUpstream 命中（不是 UpstreamError），也不被 IsClientRequestError 命中，
// 于是落到 MarkAccountResult(false)，一次抖动就把账号永久踢出轮转。
func TestIsTransientNetwork(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"EOF", io.EOF, true},
		{"单次请求超时", context.DeadlineExceeded, true},
		{"连接被重置", syscall.ECONNRESET, true},
		{"管道断开", syscall.EPIPE, true},
		{"net.OpError", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("broken")}, true},
		{"DNS 失败", &net.DNSError{Err: "no such host", Name: "x"}, true},
		{"os time 包装", &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}, true},
		{"url.Error 包装超时", &url.Error{Op: "Post", URL: "https://x", Err: context.DeadlineExceeded}, true},
		{"包装后仍识别", fmt.Errorf("上游调用失败: %w", io.ErrUnexpectedEOF), true},

		// 以下都不是网络层：不能吞掉各自应有的处理。
		{"客户端主动断开", context.Canceled, false},
		{"上游业务错误", &UpstreamError{Status: http.StatusBadGateway, Message: "502: bad"}, false},
		{"上游 400", &UpstreamError{Status: http.StatusBadRequest, Message: "400: bad"}, false},
		{"普通错误", errors.New("凭据无效"), false},
	}
	for _, c := range cases {
		if got := IsTransientNetwork(c.err); got != c.want {
			t.Errorf("%s: IsTransientNetwork = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestIsTransientNetworkIgnoresUpstreamError 上游业务错误即使包装了网络字样，
// 也必须走上游自己的分类（状态码 / 业务码），不能被当成链路抖动。
func TestIsTransientNetworkIgnoresUpstreamError(t *testing.T) {
	ue := &UpstreamError{Status: http.StatusBadGateway, Message: "unexpected EOF"}
	if IsTransientNetwork(ue) {
		t.Error("UpstreamError 不应被判为网络层错误")
	}
	if !IsTransientUpstream(ue) {
		t.Error("502 仍应被判为上游临时故障")
	}
}
