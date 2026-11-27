package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestRefreshModelsRecordsProbeResult 回归 2026-11-27：
// RefreshModels 会先选中「冷却到期的失败账号」做半开探测；如果失败不记账
// （failures++ + 冷却），下一轮还会立刻选中同一个坏账号，模型清单会一直
// 停在兜底版（线上表现：deepseek-v4.1-flash-1017 等上游模型集体"消失"）。
func TestRefreshModelsRecordsProbeResult(t *testing.T) {
	s := newAdminTestServer(t, "a", "b")

	// 账号 a 失败一次进入冷却，再手动启用清掉冷却（failures 保留）——
	// 即「冷却到期、等待半开探测」的状态；b 始终健康。
	s.Store.MarkAccountResult("a", false, "boom")
	s.Store.SetAccountEnabled("a", false)
	s.Store.SetAccountEnabled("a", true)

	// 上游伪造：a（AK=aka）的清单请求 500，b 返回含 -1017 的真实清单。
	s.Up.HTTP = &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.Header.Get("Authorization"), "Access=aka") {
			return probeFakeResponse(r, http.StatusInternalServerError, "boom"), nil
		}
		body := `{"count":1,"builtinModels":[{"model_id":"deepseek-v4.1-flash-1017","model_name":"DeepSeek-V4.1-Flash-1017","enable":true}]}`
		return probeFakeResponse(r, http.StatusOK, body), nil
	})}

	// 第一次刷新：探测命中坏账号 a，应失败并记账（failures 1→2，新冷却）。
	if err := s.RefreshModels(context.Background()); err == nil {
		t.Fatal("探测坏账号应失败")
	}
	a := s.Store.Account("a")
	if a.Failures != 2 || a.CooldownUntil == 0 {
		t.Fatalf("探测失败未记账：failures=%d cooldown=%d", a.Failures, a.CooldownUntil)
	}
	for _, m := range s.Reg.List(nil) {
		if m.ID == "deepseek-v4.1-flash-1017" {
			t.Fatal("清单刷新失败后不应出现上游模型，应仍是兜底清单")
		}
	}

	// 第二次刷新：a 在冷却中被跳过，换健康账号 b 拉到真实清单。
	if err := s.RefreshModels(context.Background()); err != nil {
		t.Fatalf("第二次刷新应成功，实际 %v", err)
	}
	found := false
	for _, m := range s.Reg.List(nil) {
		if m.ID == "deepseek-v4.1-flash-1017" {
			found = true
		}
	}
	if !found {
		t.Fatal("刷新成功后注册表应包含上游清单里的 deepseek-v4.1-flash-1017")
	}
	if b := s.Store.Account("b"); b.Status != "ok" || b.Failures != 0 {
		t.Fatalf("成功账号状态应恢复 ok：status=%q failures=%d", b.Status, b.Failures)
	}
}
