package safety

import "testing"

func TestLevelString(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelSafe, "Safe"},
		{LevelRisky, "Risky"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("Level(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

// TestClassifyStatic 用插件声明同值的注入表验证（真实注入方在
// plugin.Registry.SetGate——plugin 包 import safety，测试不能反向 import
// 形成环；生产组装在 appcore.buildAgent）。含历史漂移修复的值对照：
// find/grep/ping/dns_lookup/http_check/query_consensus 现按声明 Safe。
func TestClassifyStatic(t *testing.T) {
	gate := NewGate()
	// 模拟 SetGate 注入（与插件 ToolDefinition 的 RiskLevel 逐项一致）
	for name, level := range map[string]Level{
		"docker_ps": LevelSafe, "docker_logs": LevelSafe,
		"systemctl_status": LevelSafe, "journalctl_logs": LevelSafe,
		"read_file": LevelSafe, "list_directory": LevelSafe,
		"find_files": LevelSafe, "grep_files": LevelSafe,
		"check_port": LevelSafe, "check_process": LevelSafe,
		"check_disk": LevelSafe, "check_memory": LevelSafe,
		"check_network": LevelSafe, "tail_log": LevelSafe,
		"system_info": LevelSafe, "package_search": LevelSafe,
		"package_list": LevelSafe, "ping": LevelSafe,
		"dns_lookup": LevelSafe, "http_check": LevelSafe,
		"query_consensus": LevelSafe, "ask_user": LevelSafe,
		"memory_read": LevelSafe, "memory_write": LevelSafe,
		"execute_command": LevelRisky, "write_file": LevelRisky,
		"docker_exec": LevelRisky, "docker_compose_up": LevelRisky,
		"docker_compose_down": LevelRisky,
		"systemctl_start": LevelRisky, "systemctl_stop": LevelRisky,
		"systemctl_restart": LevelRisky,
		"package_install": LevelRisky, "package_remove": LevelRisky,
		"memory_delete": LevelRisky,
	} {
		SetStaticLevel(name, level)
	}

	tests := []struct {
		toolName string
		want     Level
	}{
		// Read-only tools → Safe（含六工具漂移修复）
		{"docker_ps", LevelSafe},
		{"docker_logs", LevelSafe},
		{"systemctl_status", LevelSafe},
		{"journalctl_logs", LevelSafe},
		{"read_file", LevelSafe},
		{"list_directory", LevelSafe},
		{"find_files", LevelSafe},
		{"grep_files", LevelSafe},
		{"check_port", LevelSafe},
		{"check_disk", LevelSafe},
		{"check_memory", LevelSafe},
		{"check_network", LevelSafe},
		{"check_process", LevelSafe},
		{"tail_log", LevelSafe},
		{"system_info", LevelSafe},
		{"package_search", LevelSafe},
		{"package_list", LevelSafe},
		{"ping", LevelSafe},
		{"dns_lookup", LevelSafe},
		{"http_check", LevelSafe},
		{"query_consensus", LevelSafe},
		// Memory tools → read/write Safe, delete Risky
		{"memory_read", LevelSafe},
		{"memory_write", LevelSafe},
		{"memory_delete", LevelRisky},
		// Side-effect tools → Risky (require confirmation)
		{"docker_compose_up", LevelRisky},
		{"docker_compose_down", LevelRisky},
		{"docker_exec", LevelRisky},
		{"execute_command", LevelRisky},
		{"write_file", LevelRisky},
		{"systemctl_start", LevelRisky},
		{"systemctl_stop", LevelRisky},
		{"systemctl_restart", LevelRisky},
		{"package_install", LevelRisky},
		{"package_remove", LevelRisky},
	}
	for _, tt := range tests {
		got := gate.ClassifyStatic(tt.toolName)
		if got != tt.want {
			t.Errorf("ClassifyStatic(%q) = %v, want %v", tt.toolName, got, tt.want)
		}
	}
}

func TestClassifyStatic_Unknown(t *testing.T) {
	gate := NewGate()
	// Unknown tools default to Risky — require user confirmation.
	if got := gate.ClassifyStatic("unknown_tool"); got != LevelRisky {
		t.Errorf("ClassifyStatic(unknown) = %v, want Risky", got)
	}
}
