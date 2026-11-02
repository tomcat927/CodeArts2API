package apierr

import (
	"context"
	"errors"

	"codearts2api/internal/codearts"
)

// Classify 把内部错误归为一个简短标签，用于请求日志的分类统计与控制台展示。
//
// 标签含义：
//   - moderation      内容审核拒绝（上游 403 ModelArts.81011，请求侧，不换号）
//   - client_request  请求本身非法（参数越界 / 模态不符 / tools 超长，不换号）
//   - credentials     账号凭据失效（需重新登录）
//   - no_account      账号池为空
//   - transient       上游临时故障（并发上限 / 429 / 5xx，退避重试）
//   - rate_limited    网关侧限流（本机密钥打太快）
//   - upstream        其他上游错误
//   - client_abort    客户端主动断开
//   - internal        网关内部错误
//
// 顺序从具体到宽泛，关键是「子集先于父集」：
//   - moderation 是 client_request 的子集（403 审核也被 IsClientRequestError 命中），必须先判；
//   - transient 里的 TM.00001041（并发上限）用 400 表达，同样被 IsClientRequestError 命中，
//     必须先于 client_request 判，否则并发上限会被记成「参数错误」，与 withAccountRetry
//     里「退避重试」的实际行为不符；
//   - credentials / no_account 不属于上游错误，先判掉避免走到 UpstreamError 兜底。
//
// 纯函数，不依赖 HTTP 上下文，可被单元测试完整覆盖（与 From 同风格）。
func Classify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "client_abort"
	}
	if errors.Is(err, NoAccount) {
		return "no_account"
	}
	if codearts.IsCredentialsError(err) {
		return "credentials"
	}
	if codearts.IsContentModerationError(err) {
		return "moderation"
	}
	if codearts.IsTransientUpstream(err) {
		return "transient"
	}
	if codearts.IsClientRequestError(err) {
		return "client_request"
	}
	var rl *RateLimited
	if errors.As(err, &rl) {
		return "rate_limited"
	}
	var ue *codearts.UpstreamError
	if errors.As(err, &ue) {
		return "upstream"
	}
	return "internal"
}
