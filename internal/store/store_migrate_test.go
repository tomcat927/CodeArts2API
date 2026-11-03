package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenNewFileSetsVersion 新建状态文件时写入当前 schema 版本。
func TestOpenNewFileSetsVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := st.st.Version; got != StateVersion {
		t.Errorf("新文件 Version = %d，期望 %d", got, StateVersion)
	}
}

// TestOpenMigratesLegacyFile 加版本号之前的历史文件（无 version 字段）
// 必须被就地升级，而不是被当作损坏文件或静默丢弃。
func TestOpenMigratesLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	// 注意：故意不写 version 字段。
	legacy := `{"accounts":[],"keys":[],"settings":{},"stats":{},"logs":[]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open 历史文件失败: %v", err)
	}
	if got := st.st.Version; got != StateVersion {
		t.Errorf("迁移后 Version = %d，期望 %d", got, StateVersion)
	}
	// 迁移不应丢掉已有数据 / 默认值。
	if st.Settings().DefaultModel == "" {
		t.Error("迁移后 Settings 默认值未补齐")
	}
}

// TestOpenMigratesV1SetsCreditPriority v1 → v2：CreditPriority 是后加的 bool 开关，
// 老文件缺字段会被 unmarshal 成零值 false（关闭），迁移必须显式置真 ——
// 否则升级一次，所有存量部署的「到期积分优先」都静默变成关闭。
// v2 文件里用户显式关闭的值不能被迁移覆盖。
func TestOpenMigratesV1SetsCreditPriority(t *testing.T) {
	t.Run("v1 文件迁移后默认开启", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v1.json")
		v1 := `{"version":1,"accounts":[],"keys":[],"settings":{},"stats":{},"logs":[]}`
		if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Open(path)
		if err != nil {
			t.Fatalf("Open v1 文件失败: %v", err)
		}
		if !st.Settings().CreditPriority {
			t.Error("v1 迁移后 CreditPriority 应默认开启")
		}
		if st.st.Version != StateVersion {
			t.Errorf("迁移后 Version = %d，期望 %d", st.st.Version, StateVersion)
		}
	})

	t.Run("v2 显式关闭保持关闭", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "v2.json")
		v2 := `{"version":2,"accounts":[],"keys":[],"settings":{"credit_priority":false},"stats":{},"logs":[]}`
		if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
			t.Fatal(err)
		}
		st, err := Open(path)
		if err != nil {
			t.Fatalf("Open v2 文件失败: %v", err)
		}
		if st.Settings().CreditPriority {
			t.Error("v2 显式关闭的 CreditPriority 不应被覆盖")
		}
	})
}

// TestOpenRejectsFutureVersion 版本高于本程序时必须报错：
// 否则旧程序会把不认识的字段静默丢掉，再落盘就造成数据损坏。
func TestOpenRejectsFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.json")
	future := `{"version":99,"accounts":[],"keys":[],"settings":{},"stats":{},"logs":[]}`
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open 未来版本应返回错误")
	}
}

// TestVersionPersistsAcrossSave 版本号必须随状态文件落盘，
// 否则每次启动都会被当成历史文件重新迁移。
func TestVersionPersistsAcrossSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		Version *int `json:"version"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Version == nil {
		t.Fatal("落盘内容缺少 version 字段")
	}
	if *onDisk.Version != StateVersion {
		t.Errorf("落盘 version = %d，期望 %d", *onDisk.Version, StateVersion)
	}

	// 重新打开后版本保持稳定。
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.st.Version; got != StateVersion {
		t.Errorf("重开后 Version = %d，期望 %d", got, StateVersion)
	}
}
