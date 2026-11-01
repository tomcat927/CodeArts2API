package codearts

import (
	"encoding/json"
	"testing"
)

// TestParseCallerIdentityURN 锁定实测响应结构（2026-11-01，sts.cn-north-4 /v5/caller-identity）。
//
// 回归：早期实现只认 `{"user":{...}}`，而真实响应是 urn 形态，导致解析结果全空
// 且 err 为 nil —— 「补全账号信息」静默失败，浏览器导入的账号全叫
// `CodeArts HSTAxxxx`，用户无法把账号和注册手机号对上（备注列只能靠 caller-identity 补）。
func TestParseCallerIdentityURN(t *testing.T) {
	raw := []byte(`{"account_id":"01a17825504779df8447435613fb1617",` +
		`"principal_id":"01a1782550477a3a80ea7fe808b1eb9c",` +
		`"principal_urn":"iam::01a17825504779df8447435613fb1617:user:hw010922006740496"}`)
	var out callerIdentityResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	id, name, domain := parseCallerIdentity(&out)
	if name != "hw010922006740496" {
		t.Errorf("账号名 = %q, 期望 hw010922006740496", name)
	}
	if domain != "01a17825504779df8447435613fb1617" {
		t.Errorf("域 ID = %q", domain)
	}
	if id != "01a1782550477a3a80ea7fe808b1eb9c" {
		t.Errorf("用户 ID = %q", id)
	}
}

// TestParseCallerIdentityKeepsUserCompat 保留对 user 形态的兼容：
// IAM 的部分形态确实返回 {"user":{...}}，两条路径都要能出名字。
func TestParseCallerIdentityKeepsUserCompat(t *testing.T) {
	raw := []byte(`{"user":{"id":"u-1","name":"gt-admin","domain_id":"d-1"}}`)
	var out callerIdentityResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	id, name, domain := parseCallerIdentity(&out)
	if name != "gt-admin" || id != "u-1" || domain != "d-1" {
		t.Errorf("user 形态解析异常: id=%q name=%q domain=%q", id, name, domain)
	}
}

// TestParseCallerIdentityGroupURN 用户组形态的 urn 末段同样是主体名，不能取错字段。
func TestParseCallerIdentityGroupURN(t *testing.T) {
	raw := []byte(`{"account_id":"d-9","principal_urn":"iam::d-9:group:devs"}`)
	var out callerIdentityResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	_, name, domain := parseCallerIdentity(&out)
	if name != "devs" {
		t.Errorf("group urn 取名 = %q, 期望 devs", name)
	}
	if domain != "d-9" {
		t.Errorf("域 ID = %q, 期望 d-9", domain)
	}
}

// TestParseCallerIdentityEmptyIsDetectable 空响应（上游返回 {}）必须让调用方看出来：
// 三个值全空，CallerIdentity 据此报错，而不是把空账号名写进 AccountLabel。
func TestParseCallerIdentityEmptyIsDetectable(t *testing.T) {
	var out callerIdentityResponse
	if err := json.Unmarshal([]byte(`{}`), &out); err != nil {
		t.Fatal(err)
	}
	id, name, domain := parseCallerIdentity(&out)
	if id != "" || name != "" || domain != "" {
		t.Errorf("空响应不应解析出内容: id=%q name=%q domain=%q", id, name, domain)
	}
}
