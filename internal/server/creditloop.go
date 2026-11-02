package server

import (
	"context"
	"log"
	"time"
)

// 后台积分快照刷新。
//
// 「到期积分优先」调度需要每个账号的即将到期额度（CreditInfo.Expiring），
// 而额度查询是一次真实的上游请求（snap-manager 统计插件，只读、不消耗积分），
// 不能进请求路径。这里周期性批量刷新并写回账号快照；调度侧只信任 24h 内的
// 快照，所以刷新失败只是让该账号暂时退回普通轮询，不会把调度带坏。

// creditRefreshInterval 是积分快照的刷新周期。
const creditRefreshInterval = 10 * time.Minute

// StartCreditLoop 启动后台积分快照刷新。
func (s *Server) StartCreditLoop(ctx context.Context) {
	go func() {
		// 启动后先等一会儿，让账号导入与首轮凭据解析完成
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
		s.RefreshAccountCredits(ctx)

		ticker := time.NewTicker(creditRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.RefreshAccountCredits(ctx)
			}
		}
	}()
}

// RefreshAccountCredits 对所有启用账号各刷新一次积分快照，有变更时落盘。
// 返回成功刷新的账号数。
func (s *Server) RefreshAccountCredits(ctx context.Context) int {
	ok := 0
	for _, a := range s.Store.Accounts() {
		if ctx.Err() != nil {
			return ok
		}
		if !a.Enabled {
			continue
		}
		// 走 creditFor 的 60 秒短缓存：控制台刚查过的账号不再打上游
		info, err := s.creditFor(ctx, a, false)
		if err != nil {
			log.Printf("账号 %s 积分快照刷新失败: %v", a.DisplayName(), err)
			continue
		}
		if s.Store.SetAccountCredit(a.ID, info) {
			ok++
		}
	}
	if ok > 0 {
		s.persist()
	}
	return ok
}
