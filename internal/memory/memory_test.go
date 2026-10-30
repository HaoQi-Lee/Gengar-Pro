package memory

import (
	"os"
	"testing"
	"time"
)

func TestStoreGet_FileNotExist(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStoreDir() error = %v", err)
	}

	m := s.Get("ghost")
	if m.SystemInfo != nil {
		t.Errorf("SystemInfo = %v, want nil", m.SystemInfo)
	}
	if m.History != nil {
		t.Errorf("History = %v, want nil", m.History)
	}

	// Get 不应创建文件
	if _, err := os.Stat(s.hostPath("ghost")); !os.IsNotExist(err) {
		t.Errorf("memory file should not exist after Get() on empty state")
	}
}

func TestSaveAndLoad_Roundtrip(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStoreDir() error = %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	m := s.Get("web-01")
	m.SystemInfo = &Category{
		UpdatedAt: now,
		Data: map[string]interface{}{
			"os":        "CentOS 7",
			"cpu_cores": float64(8), // JSON 数字默认 float64
		},
	}

	if err := m.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded := s.Get("web-01")
	if loaded.SystemInfo == nil {
		t.Fatal("loaded.SystemInfo is nil")
	}
	if !loaded.SystemInfo.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", loaded.SystemInfo.UpdatedAt, now)
	}
	data, ok := loaded.SystemInfo.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Data type = %T, want map", loaded.SystemInfo.Data)
	}
	if data["os"] != "CentOS 7" {
		t.Errorf("os = %v, want CentOS 7", data["os"])
	}
}

func TestSave_WithoutPath(t *testing.T) {
	// 直接构造的 Memory 没有落盘路径，Save 必须报错而不是写全局位置。
	// 隔离 HOME 以防实现错误时污染真实用户目录。
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	if err := (&Memory{}).Save(); err == nil {
		t.Fatal("Save() on Memory without backing path should error")
	}
}

func TestWrite_SystemInfo(t *testing.T) {
	m := &Memory{}
	before := time.Now()

	err := m.Write("system_info", map[string]interface{}{"os": "Ubuntu 22.04"})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	after := time.Now()

	if m.SystemInfo == nil {
		t.Fatal("SystemInfo is nil after Write")
	}
	if m.SystemInfo.UpdatedAt.Before(before) || m.SystemInfo.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt out of range: %v", m.SystemInfo.UpdatedAt)
	}
	data, _ := m.SystemInfo.Data.(map[string]interface{})
	if data["os"] != "Ubuntu 22.04" {
		t.Errorf("os = %v, want Ubuntu 22.04", data["os"])
	}
}

func TestWrite_History(t *testing.T) {
	m := &Memory{}
	err := m.Write("history", []interface{}{
		map[string]interface{}{"action": "installed nginx"},
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if m.History == nil {
		t.Fatal("History is nil after Write")
	}
	records, ok := m.History.Data.([]interface{})
	if !ok {
		t.Fatalf("History.Data type = %T, want []interface{}", m.History.Data)
	}
	if len(records) != 1 {
		t.Errorf("len(records) = %d, want 1", len(records))
	}
}

func TestWrite_UnknownCategory(t *testing.T) {
	m := &Memory{}
	err := m.Write("unknown_cat", map[string]interface{}{})
	if err == nil {
		t.Error("Write(unknown_cat) should return error")
	}
}

func TestWrite_OverwritesUpdatesTimestamp(t *testing.T) {
	m := &Memory{}
	if err := m.Write("system_info", map[string]interface{}{"os": "v1"}); err != nil {
		t.Fatal(err)
	}
	first := m.SystemInfo.UpdatedAt

	time.Sleep(10 * time.Millisecond) // 保证时间戳能区分
	if err := m.Write("system_info", map[string]interface{}{"os": "v2"}); err != nil {
		t.Fatal(err)
	}
	if !m.SystemInfo.UpdatedAt.After(first) {
		t.Errorf("UpdatedAt did not advance: first=%v, second=%v", first, m.SystemInfo.UpdatedAt)
	}
}

func TestRead_Hit(t *testing.T) {
	m := &Memory{}
	_ = m.Write("system_info", map[string]interface{}{"os": "CentOS 7"})

	result, err := m.Read("system_info")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	res, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Read result type = %T, want map", result)
	}
	if res["category"] != "system_info" {
		t.Errorf("category = %v, want system_info", res["category"])
	}
	if _, ok := res["updated_at"]; !ok {
		t.Error("missing updated_at in Read result")
	}
	data, _ := res["data"].(map[string]interface{})
	if data["os"] != "CentOS 7" {
		t.Errorf("data.os = %v, want CentOS 7", data["os"])
	}
}

func TestRead_Miss(t *testing.T) {
	m := &Memory{}
	_, err := m.Read("system_info")
	if err == nil {
		t.Error("Read on empty Memory should return error")
	}
}

func TestRead_All_Empty(t *testing.T) {
	m := &Memory{}
	result, err := m.Read("all")
	if err != nil {
		t.Fatalf("Read(all) on empty Memory error: %v", err)
	}
	res, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Read(all) result type = %T, want map", result)
	}
	if len(res) != 0 {
		t.Errorf("Read(all) on empty Memory = %v, want empty map", res)
	}
}

func TestRead_All_WithData(t *testing.T) {
	m := &Memory{}
	_ = m.Write("system_info", map[string]interface{}{"os": "CentOS 7"})
	_ = m.Write("history", []interface{}{
		map[string]interface{}{"action": "test"},
	})

	result, err := m.Read("all")
	if err != nil {
		t.Fatalf("Read(all) error: %v", err)
	}
	res := result.(map[string]interface{})
	if _, ok := res["system_info"]; !ok {
		t.Error("Read(all) missing system_info")
	}
	if _, ok := res["history"]; !ok {
		t.Error("Read(all) missing history")
	}
}

func TestRead_UnknownCategory(t *testing.T) {
	m := &Memory{}
	_, err := m.Read("unknown_cat")
	if err == nil {
		t.Error("Read(unknown_cat) should return error")
	}
}

func TestDelete_SingleCategory(t *testing.T) {
	m := &Memory{}
	_ = m.Write("system_info", map[string]interface{}{"os": "CentOS 7"})
	_ = m.Write("history", []interface{}{
		map[string]interface{}{"action": "test"},
	})

	if err := m.Delete("system_info"); err != nil {
		t.Fatalf("Delete(system_info) error = %v", err)
	}
	if m.SystemInfo != nil {
		t.Error("SystemInfo should be nil after Delete")
	}
	if m.History == nil {
		t.Error("History should remain after deleting only system_info")
	}
}

func TestDelete_All_RemovesFile(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStoreDir() error = %v", err)
	}

	m := s.Get("web-01")
	_ = m.Write("system_info", map[string]interface{}{"os": "CentOS 7"})
	if err := m.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// 确认文件存在
	path := s.hostPath("web-01")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("memory file should exist before Delete(all): %v", err)
	}

	if err := m.Delete("all"); err != nil {
		t.Fatalf("Delete(all) error = %v", err)
	}
	if m.SystemInfo != nil || m.History != nil {
		t.Error("Memory should be fully cleared after Delete(all)")
	}

	// 文件应被删除
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("memory file should be removed, stat err = %v", err)
	}
}

func TestDelete_All_FileMissing_NoError(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStoreDir() error = %v", err)
	}

	m := s.Get("never-written")
	if err := m.Delete("all"); err != nil {
		t.Errorf("Delete(all) on missing file should not error, got: %v", err)
	}
}

func TestDelete_UnknownCategory(t *testing.T) {
	m := &Memory{}
	if err := m.Delete("unknown_cat"); err == nil {
		t.Error("Delete(unknown_cat) should return error")
	}
}
