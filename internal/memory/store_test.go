package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePersistsPerHost(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	m1 := s.Get("web-01")
	if err := m1.Write("system_info", map[string]string{"os": "ubuntu"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m1.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 新 Store 实例重新加载，验证落盘按主机隔离
	s2, _ := OpenStoreDir(s.Dir())
	again := s2.Get("web-01")
	v, err := again.Read("system_info")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// Read 返回 categoryView 结构 {category, updated_at, data}，
	// 断言实际内容而不是永假的 fmt.Sprint(v) == ""
	view, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("read back type = %T, want categoryView map", v)
	}
	data, _ := view["data"].(map[string]interface{})
	if data["os"] != "ubuntu" {
		t.Fatalf("read back data.os = %v, want ubuntu (view: %v)", data["os"], view)
	}
	if _, err := s2.Get("web-02").Read("system_info"); err == nil {
		t.Fatal("web-02 must have its own empty memory")
	}
}

func TestStoreSanitizesHostKey(t *testing.T) {
	s, _ := OpenStoreDir(t.TempDir())
	m := s.Get("bad/../host name!")
	if err := m.Write("system_info", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// 落盘文件名必须是清洗后的（不含路径分隔符与空格）——目录穿越不可能
	if _, err := s.Get("bad/../host name!").Read("system_info"); err != nil {
		t.Fatalf("read back sanitized: %v", err)
	}
}

// TestHostPathNeverEscapesDir 直接锁定穿越屏障：hostPath 的产物必须始终
// 落在 Store 目录内。TestStoreSanitizesHostKey 的往返断言不够——
// filepath.Clean 恰好把 "bad/../host name!.json" 清成合法文件名，
// 删掉 strings.Map 清洗该测试仍过。
func TestHostPathNeverEscapesDir(t *testing.T) {
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, h := range []string{"../..", "..\\..", "../../etc/passwd", "evil/", "", ".", "..", "a b/c", "CON", "NUL"} {
		if got := filepath.Dir(s.hostPath(h)); got != s.Dir() {
			t.Errorf("hostPath(%q) escapes dir: %s", h, got)
		}
	}
}

// TestStoreGetCorruptFileReturnsEmpty: 损坏 JSON 降级为空 Memory 而非报错，
// 调用方随后 Write+Save 即整体恢复该主机文件。
func TestStoreGetCorruptFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	s, err := OpenStoreDir(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	m := s.Get("bad")
	if m.SystemInfo != nil || m.History != nil {
		t.Fatalf("corrupt file must degrade to empty memory: %+v", m)
	}
}

// TestHostPathManglesWindowsReservedNames: 实测（Windows 10 19045）对
// CON.json 调 WriteFile 报成功但落盘命中设备别名——文件根本不存在，
// 记忆静默丢失；NUL.json 则全部写入被丢弃。设备别名在 Win10 及更早
// 对带扩展名的形式仍然生效（Win11 22H2+ 才放宽）。hostPath 必须把
// 保留名改写成普通文件名；跨平台统一规则，文件名可预期。
func TestHostPathManglesWindowsReservedNames(t *testing.T) {
	// 独立于实现的保留名清单：实现里的清单被误删时测试仍能抓住
	reserved := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
		"COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
		"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
	s, err := OpenStoreDir(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// 设备别名按首个 '.' 前的词匹配（CON.x、con.example.com 同样命中），
	// 因此断言落在 stems 上
	for _, h := range []string{"CON", "con", "Nul", "PRN", "AUX", "COM1", "LPT9", "com3", "CON.x"} {
		base := strings.TrimSuffix(filepath.Base(s.hostPath(h)), ".json")
		stem := strings.ToUpper(strings.SplitN(base, ".", 2)[0])
		if reserved[stem] {
			t.Errorf("hostPath(%q) = %q still resolves to a reserved device name", h, base)
		}
	}
}
