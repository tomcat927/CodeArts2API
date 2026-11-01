package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// --- 手工录入：STS 与永久 AK/SK 的形态区分 ---

// TestAdminAccountsManualAKSKCreatesPermanentCredential 回归：录入永久 AK/SK 后
// 账号必须不过期、不续期、不带 security_token。
//
// 历史 bug：Valid() 把「过期时间为零值」判为不可用，永久凭据在 110 分钟 TTL
// 用尽后永久失效，且 HasRefresh 为 false、Manager 也救不回来。
func TestAdminAccountsManualAKSKCreatesPermanentCredential(t *testing.T) {
	s := newAdminTestServer(t)

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"manual","name":"perm","access_key":"AKP","secret_key":"SKP","credential_kind":"aksk"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}

	accts := s.Store.Accounts()
	if len(accts) != 1 {
		t.Fatalf("应有 1 个账号，实际 %d", len(accts))
	}
	a := accts[0]
	if a.Session == nil {
		t.Fatal("账号没有 Session")
	}
	if !a.Session.IsPermanent() {
		t.Error("credential_kind=aksk 的账号应标记为永久凭据")
	}
	if a.Session.HasRefresh() {
		t.Error("永久 AK/SK 不应声称可续期")
	}
	if !a.Session.Valid(5 * time.Minute) {
		t.Error("永久 AK/SK 必须始终有效")
	}
	if a.Session.SecurityToken != "" {
		t.Errorf("永久 AK/SK 不应带 security_token，实际 %q", a.Session.SecurityToken)
	}
	if !a.Session.ExpiresAt.IsZero() {
		t.Errorf("永久 AK/SK 的过期时间应为零值，实际 %v", a.Session.ExpiresAt)
	}
}

// TestAdminAccountsManualSTSStaysTemporary 未声明 credential_kind 时仍走 STS 临时凭据，
// 保持向后兼容。
func TestAdminAccountsManualSTSStaysTemporary(t *testing.T) {
	s := newAdminTestServer(t)

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"manual","name":"sts","access_key":"AKS","secret_key":"SKS"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}

	a := s.Store.Accounts()[0]
	if a.Session.IsPermanent() {
		t.Error("未声明 credential_kind 的账号不应被当成永久凭据")
	}
	if a.Session.ExpiresAt.IsZero() {
		t.Error("STS 临时凭据应有过期时间（默认 TTL）")
	}
}

// TestAdminAccountsManualAKSKRequiresKeys 缺 AK/SK 时明确报错，不能落一个空账号。
func TestAdminAccountsManualAKSKRequiresKeys(t *testing.T) {
	s := newAdminTestServer(t)

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"manual","name":"x","access_key":"","secret_key":"","credential_kind":"aksk"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（body: %s）", rec.Code, rec.Body.String())
	}
	if n := len(s.Store.Accounts()); n != 0 {
		t.Errorf("失败的手工录入不应写入账号，实际 %d 个", n)
	}
}

// --- 首屏状态：永久凭据的展示字段 ---

// TestAdminStateReportsPermanentCredential 永久 AK/SK 的账号行必须带 permanent=true、
// expires_in=0，控制台据此显示「永不过期」而不是 1970。
func TestAdminStateReportsPermanentCredential(t *testing.T) {
	s := newAdminTestServer(t, "a")

	rec := adminProbeRequest(t, s, http.MethodGet, "/admin/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(resp.Accounts) != 1 {
		t.Fatalf("应有 1 个账号，实际 %d", len(resp.Accounts))
	}
	row := resp.Accounts[0]
	if perm, _ := row["permanent"].(bool); !perm {
		t.Errorf("permanent 应为 true，实际 %v", row["permanent"])
	}
	if exp, _ := row["expires_in"].(float64); exp != 0 {
		t.Errorf("expires_in 应为 0，实际 %v", row["expires_in"])
	}
	if hr, _ := row["has_refresh"].(bool); hr {
		t.Errorf("永久凭据 has_refresh 应为 false，实际 %v", row["has_refresh"])
	}
}

// --- 活动福利：按账号查询 ---

// TestAdminWelfareSelectsAccountByID 活动清单是按账号查的：?id= 必须精确选中该账号，
// 响应带全量账号下拉与可领数量，避免用户误以为只有某个号能领。
func TestAdminWelfareSelectsAccountByID(t *testing.T) {
	s := newAdminTestServer(t, "a", "b")
	s.Up.HTTP = &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return probeFakeResponse(r, http.StatusOK,
			`{"code":0,"message":"ok","data":{"items":[`+
				`{"campaignId":1,"title":"可领","claimable":true,"type":"DAILY"},`+
				`{"campaignId":2,"title":"不可领","claimable":false,"type":"INVITE_USER"}]}}`), nil
	})}

	rec := adminProbeRequest(t, s, http.MethodGet, "/admin/api/welfare?id=b", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}

	var resp struct {
		ID             string           `json:"id"`
		Account        string           `json:"account"`
		ClaimableCount int              `json:"claimable_count"`
		Accounts       []map[string]any `json:"accounts"`
		Campaigns      []map[string]any `json:"campaigns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if resp.ID != "b" {
		t.Errorf("应选中账号 b，实际 %q", resp.ID)
	}
	if resp.Account != "acct-b" {
		t.Errorf("账号名 = %q，期望 acct-b", resp.Account)
	}
	if resp.ClaimableCount != 1 {
		t.Errorf("claimable_count = %d，期望 1", resp.ClaimableCount)
	}
	if len(resp.Accounts) != 2 {
		t.Errorf("下拉框应列出全部 2 个账号，实际 %d", len(resp.Accounts))
	}
	if len(resp.Campaigns) != 2 {
		t.Errorf("应返回 2 个活动，实际 %d", len(resp.Campaigns))
	}
}

// TestAdminWelfareFallsBackToNextAccount 未指定 id（或 id 不存在）时回退到调度池，
// 不能直接报「没有账号」。
func TestAdminWelfareFallsBackToNextAccount(t *testing.T) {
	s := newAdminTestServer(t, "a")
	s.Up.HTTP = &http.Client{Transport: probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return probeFakeResponse(r, http.StatusOK, `{"code":0,"message":"ok","data":{"items":[]}}`), nil
	})}

	rec := adminProbeRequest(t, s, http.MethodGet, "/admin/api/welfare?id=does-not-exist", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != "a" {
		t.Errorf("应回退到账号 a，实际 %q", resp.ID)
	}
}

// --- 账号备注 / 改名 ---

// TestAdminAccountsSetNote 备注（如注册手机号）可写可改，并能在
// /admin/api/state 里回读 —— 控制台靠它区分账号归属。
func TestAdminAccountsSetNote(t *testing.T) {
	s := newAdminTestServer(t, "a")

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"set-note","id":"a","note":" 13800000000 "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}
	if got := s.Store.Account("a"); got == nil || got.Note != "13800000000" {
		t.Fatalf("备注未写入（应去掉首尾空白），实际 %+v", got)
	}

	rec = adminProbeRequest(t, s, http.MethodGet, "/admin/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("state 状态码 = %d，期望 200", rec.Code)
	}
	var state struct {
		Accounts []struct {
			ID   string `json:"id"`
			Note string `json:"note"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Accounts) != 1 || state.Accounts[0].Note != "13800000000" {
		t.Errorf("state 未返回备注: %+v", state.Accounts)
	}

	// 清空备注也要生效（手机号换绑后需要抹掉旧值）。
	rec = adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"set-note","id":"a","note":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("清空备注状态码 = %d，期望 200", rec.Code)
	}
	if got := s.Store.Account("a"); got == nil || got.Note != "" {
		t.Errorf("备注未清空: %+v", got)
	}
}

// TestAdminAccountsSetNoteUnknownID 不存在的账号要报 400，而不是静默成功。
func TestAdminAccountsSetNoteUnknownID(t *testing.T) {
	s := newAdminTestServer(t, "a")
	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"set-note","id":"nope","note":"138"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400（body: %s）", rec.Code, rec.Body.String())
	}
}

// TestAdminAccountsRename 改名生效，且空名被拒绝（否则列表会退化成 ID）。
func TestAdminAccountsRename(t *testing.T) {
	s := newAdminTestServer(t, "a")

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"rename","id":"a","name":" 手机号 138 "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}
	if got := s.Store.Account("a"); got == nil || got.Name != "手机号 138" {
		t.Fatalf("改名未生效，实际 %+v", got)
	}

	rec = adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"rename","id":"a","name":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空名应返回 400，实际 %d", rec.Code)
	}
}

// TestAdminAccountsManualAcceptsNote 录入 / 导入账号时可一次填好备注。
func TestAdminAccountsManualAcceptsNote(t *testing.T) {
	s := newAdminTestServer(t)

	rec := adminProbeRequest(t, s, http.MethodPost, "/admin/api/accounts",
		`{"action":"manual","name":"m","access_key":"AKN","secret_key":"SKN",
		  "credential_kind":"aksk","note":"13900000000"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200（body: %s）", rec.Code, rec.Body.String())
	}
	accts := s.Store.Accounts()
	if len(accts) != 1 || accts[0].Note != "13900000000" {
		t.Errorf("录入时带的备注丢了: %+v", accts)
	}
}
