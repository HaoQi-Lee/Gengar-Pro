package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gengar-pro/internal/memory"
)

// newTestMemoryManager 在临时记忆目录中创建一个干净的 MemoryManager（按主机分域）。
func newTestMemoryManager(t *testing.T) *MemoryManager {
	t.Helper()
	return NewMemoryManager(memory.OpenStoreForTest(t.TempDir()))
}

func TestMemoryManager_Tools(t *testing.T) {
	mm := newTestMemoryManager(t)
	tools := mm.Tools()
	if len(tools) != 3 {
		t.Fatalf("Tools() count = %d, want 3", len(tools))
	}

	wantNames := map[string]int{
		"memory_read":   0, // RiskLevel
		"memory_write":  0,
		"memory_delete": 1,
	}
	for _, td := range tools {
		wantRisk, ok := wantNames[td.Name]
		if !ok {
			t.Errorf("unexpected tool %q", td.Name)
			continue
		}
		if int(td.RiskLevel) != wantRisk {
			t.Errorf("%s RiskLevel = %d, want %d", td.Name, td.RiskLevel, wantRisk)
		}
		if td.Targeting {
			t.Errorf("%s Targeting = true, want false (control-plane)", td.Name)
		}
		if !strings.Contains(string(td.InputSchema), "category") {
			t.Errorf("%s InputSchema missing 'category': %s", td.Name, td.InputSchema)
		}
		if !strings.Contains(string(td.InputSchema), `"host"`) {
			t.Errorf("%s InputSchema missing 'host': %s", td.Name, td.InputSchema)
		}
		delete(wantNames, td.Name)
	}
	if len(wantNames) > 0 {
		t.Errorf("missing tools: %v", wantNames)
	}
}

func TestMemoryManager_UnknownTool(t *testing.T) {
	mm := newTestMemoryManager(t)
	_, err := mm.Execute(context.Background(), "not_a_tool", json.RawMessage(`{}`), nil)
	if err == nil {
		t.Error("Execute(unknown) should return error")
	}
}

func TestMemoryReadRequiresHost(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, _ := mm.Execute(context.Background(), "memory_read",
		json.RawMessage(`{"category":"all"}`), nil)
	if !res.IsError || !strings.Contains(res.Content, "host") {
		t.Fatalf("missing host must error: %+v", res)
	}
}

func TestMemoryWriteReadRoundtrip(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, err := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"web-01","category":"system_info","data":{"os":"ubuntu"}}`), nil)
	if err != nil || res.IsError {
		t.Fatalf("write: %+v err=%v", res, err)
	}
	res, _ = mm.Execute(context.Background(), "memory_read",
		json.RawMessage(`{"host":"web-01","category":"system_info"}`), nil)
	if res.IsError || !strings.Contains(res.Content, "ubuntu") {
		t.Fatalf("read: %+v", res)
	}
}

func TestMemoryIsolationBetweenHosts(t *testing.T) {
	mm := newTestMemoryManager(t)
	_, _ = mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"a","category":"system_info","data":{"x":1}}`), nil)
	res, _ := mm.Execute(context.Background(), "memory_read",
		json.RawMessage(`{"host":"b","category":"system_info"}`), nil)
	if !res.IsError {
		t.Fatal("host b must not see host a's memory")
	}
}

func TestMemoryManager_Read_Miss(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, err := mm.Execute(context.Background(), "memory_read", json.RawMessage(`{"host":"h1","category":"system_info"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true for empty memory, got content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "system_info") {
		t.Errorf("error content should mention category, got: %q", res.Content)
	}
}

func TestMemoryManager_Read_Hit(t *testing.T) {
	mm := newTestMemoryManager(t)
	// 先写一条
	_, err := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info","data":{"os":"CentOS 7","cpu_cores":8}}`), nil)
	if err != nil {
		t.Fatalf("seed write error: %v", err)
	}

	res, err := mm.Execute(context.Background(), "memory_read", json.RawMessage(`{"host":"h1","category":"system_info"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError=true; content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "CentOS 7") {
		t.Errorf("content missing 'CentOS 7': %q", res.Content)
	}
	if !strings.Contains(res.Content, "updated_at") {
		t.Errorf("content missing 'updated_at': %q", res.Content)
	}
}

func TestMemoryManager_Read_All(t *testing.T) {
	mm := newTestMemoryManager(t)
	_, _ = mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info","data":{"os":"Ubuntu"}}`), nil)

	res, err := mm.Execute(context.Background(), "memory_read", json.RawMessage(`{"host":"h1","category":"all"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError=true for Read(all); content: %q", res.Content)
	}
	if !strings.Contains(res.Content, "system_info") {
		t.Errorf("Read(all) content missing system_info: %q", res.Content)
	}
}

func TestMemoryManager_Read_BadInput(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, err := mm.Execute(context.Background(), "memory_read", json.RawMessage(`{"host":"h1","category":""}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !res.IsError {
		t.Errorf("empty category should be error")
	}
}

func TestMemoryManager_Write_Persists(t *testing.T) {
	store := memory.OpenStoreForTest(t.TempDir())
	mm := NewMemoryManager(store)
	res, err := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info","data":{"os":"Debian 12"}}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("Write IsError=true; content: %q", res.Content)
	}

	// 验证落盘：从 Store 重新 Get（新实例，从磁盘读）验证持久化
	mem2 := store.Get("h1")
	if mem2.SystemInfo == nil {
		t.Fatal("SystemInfo not persisted")
	}
	data, _ := mem2.SystemInfo.Data.(map[string]interface{})
	if data["os"] != "Debian 12" {
		t.Errorf("persisted os = %v, want Debian 12", data["os"])
	}
}

func TestMemoryManager_Write_History_Array(t *testing.T) {
	store := memory.OpenStoreForTest(t.TempDir())
	mm := NewMemoryManager(store)
	res, err := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"history","data":[{"action":"installed nginx","timestamp":"2026-05-30T10:00:00Z"}]}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("Write history IsError=true; content: %q", res.Content)
	}

	mem2 := store.Get("h1")
	if mem2.History == nil {
		t.Fatal("History not persisted")
	}
	arr, ok := mem2.History.Data.([]interface{})
	if !ok {
		t.Fatalf("History.Data type = %T, want []interface{}", mem2.History.Data)
	}
	if len(arr) != 1 {
		t.Errorf("history len = %d, want 1", len(arr))
	}
}

func TestMemoryManager_Write_UnknownCategory(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, _ := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"bogus","data":{}}`), nil)
	if !res.IsError {
		t.Error("unknown category should be error")
	}
}

func TestMemoryManager_Write_MissingData(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, _ := mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info"}`), nil)
	if !res.IsError {
		t.Error("missing data should be error")
	}
}

func TestMemoryManager_Delete_Category(t *testing.T) {
	store := memory.OpenStoreForTest(t.TempDir())
	mm := NewMemoryManager(store)
	_, _ = mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info","data":{"os":"x"}}`), nil)
	_, _ = mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"history","data":[{"action":"y"}]}`), nil)

	res, err := mm.Execute(context.Background(), "memory_delete", json.RawMessage(`{"host":"h1","category":"system_info"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("Delete IsError=true; content: %q", res.Content)
	}

	mem2 := store.Get("h1")
	if mem2.SystemInfo != nil {
		t.Error("system_info should be gone")
	}
	if mem2.History == nil {
		t.Error("history should remain")
	}
}

func TestMemoryManager_Delete_All_RemovesFile(t *testing.T) {
	store := memory.OpenStoreForTest(t.TempDir())
	mm := NewMemoryManager(store)
	_, _ = mm.Execute(context.Background(), "memory_write",
		json.RawMessage(`{"host":"h1","category":"system_info","data":{"os":"x"}}`), nil)

	res, err := mm.Execute(context.Background(), "memory_delete", json.RawMessage(`{"host":"h1","category":"all"}`), nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.IsError {
		t.Errorf("Delete(all) IsError=true; content: %q", res.Content)
	}

	path := filepath.Join(store.Dir(), "h1.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s should be removed, stat err = %v", path, err)
	}
}

func TestMemoryManager_Delete_UnknownCategory(t *testing.T) {
	mm := newTestMemoryManager(t)
	res, _ := mm.Execute(context.Background(), "memory_delete", json.RawMessage(`{"host":"h1","category":"bogus"}`), nil)
	if !res.IsError {
		t.Error("unknown category should be error")
	}
}
