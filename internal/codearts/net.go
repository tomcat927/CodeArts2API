package codearts

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
)

// IsTransientNetwork 报告错误是否属于「网络层抖动」，而不是账号或请求本身的问题。
//
// 背景（2026-11-03 线上事故）：三个账号因为 `unexpected EOF` / `context deadline exceeded`
// 被 MarkAccountResult(false) 标成 error，而这两种错误既不是上游业务错误
// （IsTransientUpstream 只认 429 / 5xx / transientCodes），也不是请求非法
// （IsClientRequestError 只认 400 / 406 / 413 / 422）。它们落到「其余失败」分支，
// 一次抖动就把健康账号踢出轮转。
//
// 判定为网络层意味着：换账号或稍后重试即可，**不能**计入账号故障。
//
// 注意排除 context.Canceled：那是客户端主动断开，由调用方单独处理；
// 也排除 UpstreamError：上游已经给出了业务错误码，应走各自的分类。
func IsTransientNetwork(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return false
	}

	// 连接中途断开 / 对端关闭 / 底层 errno 类错误。
	for _, target := range []error{
		context.DeadlineExceeded,
		io.ErrUnexpectedEOF,
		io.EOF,
		net.ErrClosed,
		syscall.ECONNRESET,
		syscall.ECONNABORTED,
		syscall.EPIPE,
		syscall.ECONNREFUSED,
	} {
		if errors.Is(err, target) {
			return true
		}
	}

	// 超时、DNS、拨号、TLS 握手等结构化网络错误。
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var tlsErr *tls.RecordHeaderError
	if errors.As(err, &tlsErr) {
		return true
	}
	// url.Error 是 http.Client 对传输层失败的统一包装；业务错误不会走到这里
	// （业务错误在 UpstreamError 里，上面已排除）。
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}
