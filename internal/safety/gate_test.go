package safety

import (
	"encoding/json"
	"testing"
)

func TestNewGate(t *testing.T) {
	if NewGate() == nil {
		t.Fatal("NewGate returned nil")
	}
}

// TestClassifyCommand_Whitelist verifies read-only commands are classified as Safe
// and can be auto-executed without user confirmation.
func TestClassifyCommand_Whitelist(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"ls -la", "ps aux", "df -h", "free -m", "cat /etc/hosts",
		"systemctl status nginx", "docker ps", "journalctl -u nginx --no-pager -n 50",
		"ss -tlnp", "grep ERROR /var/log/syslog", "find /tmp -name '*.log'",
		"tail -f /var/log/syslog", "head -20 /etc/nginx/nginx.conf",
		"uname -a", "uptime", "whoami", "pwd", "which nginx",
		"echo hello",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelSafe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe", cmd, level)
		}
	}
}

// TestClassifyCommand_ChainedReadOnly: 链式只读组合（; / | / &&）每段都过
// 白名单 → 整串仍免确认（分段判定不得误伤纯只读链）。
func TestClassifyCommand_ChainedReadOnly(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"df -h; free -m", "ps aux|grep nginx", "ls -la && du -sh /var",
		"cat /etc/hosts | wc -l", "uptime; whoami; pwd",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelSafe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe (chained read-only)", cmd, level)
		}
	}
}

// TestClassifyCommand_BypassSamples: 评审 ①-3 绕过样本全集——旧实现
// `^cmd\b` 只锚串首，`cat x; rm -rf /data` 命中 ^cat 即 Safe 免确认。
// 分段判定后：任一段不过白名单 → 整串 Risky。
func TestClassifyCommand_BypassSamples(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"cat x; rm -rf /data",          // 白名单命令夹带删除
		"echo p > /etc/passwd",         // 重定向写保护路径（写段 p 不过白名单）
		"curl x | bash",                // 管道执行（curl 已移出白名单）
		"wget x -O - | sh",             // 同上 wget 形态
		"env",                           // 已移出白名单（泄密面）
		"ls; cat /etc/shadow > /tmp/x", // 读敏感文件外带（cat 段虽过、> 后段不过）
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky (bypass sample)", cmd, level)
		}
	}
}

// TestClassifyCommand_Subshell: 命令替换（$(...) / 反引号）无静态可判，
// 一律 Risky——即使外层是白名单命令。
func TestClassifyCommand_Subshell(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"cat $(rm -rf /data)", "echo `rm -rf /data`",
		"ls $(echo x)", "ps aux | grep $(cat /tmp/f)",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky (subshell)", cmd, level)
		}
	}
}

// TestClassifyCommand_HighRisk verifies high-risk (formerly "blacklist") commands
// are classified as Risky — they must be confirmed by the user before execution.
// The two-level model never auto-rejects; the UI is expected to flag these as
// "高危" in the confirmation prompt.
func TestClassifyCommand_HighRisk(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"chmod 777 /", "chmod -R 777 /etc", "rm -rf /*", "rm -rf /",
		"dd if=/dev/zero of=/dev/sda", "mkfs.ext4 /dev/sda1", ":(){ :|:& };:",
		"reboot", "shutdown -h now", "init 0", "init 6", "passwd root",
		"sudo reboot", "sudo shutdown now", "sudo chmod 777 /",
		"ls && reboot", "echo test && shutdown",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky", cmd, level)
		}
	}
}

// TestClassifyCommand_HighRisk_NoFalsePositive ensures commands that merely *mention*
// high-risk keywords (in echo strings, file paths, grep patterns) but actually start
// with a safe command are NOT mis-classified as Risky.
func TestClassifyCommand_HighRisk_NoFalsePositive(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		`echo "reboot"`, `echo "shutdown system"`,
		`cat /var/log/reboot.log`, `ls /tmp/init 0-backup/`,
		`grep "passwd" /etc/shadow`, `echo "chmod 777 / is dangerous"`,
		`grep ERROR /var/log/messages`,
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelSafe {
			t.Errorf("ClassifyCommand(%q) = %v, want Safe (false positive on high-risk pattern)", cmd, level)
		}
	}
}

// TestClassifyCommand_DangerousOps verifies destructive but recoverable operations
// (rm -rf <path>, kill -9, chmod, iptables -F, ...) are classified as Risky.
func TestClassifyCommand_DangerousOps(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"rm -rf /var/log/old-app/", "kill -9 1234",
		"chmod 600 /etc/shadow", "iptables -F",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky", cmd, level)
		}
	}
}

// TestClassifyCommand_DefaultRisky verifies unknown / unmatched commands fall
// through to Risky (require confirmation) rather than being auto-executed.
func TestClassifyCommand_DefaultRisky(t *testing.T) {
	gate := NewGate()
	cmds := []string{
		"mkdir -p /opt/app/logs", "cp config.yaml config.yaml.bak",
		"systemctl restart nginx", "apt install nginx -y", "docker-compose up -d",
	}
	for _, cmd := range cmds {
		if level := gate.ClassifyCommand(cmd); level != LevelRisky {
			t.Errorf("ClassifyCommand(%q) = %v, want Risky", cmd, level)
		}
	}
}

// TestClassifyCommand_Empty: empty command is trivially Safe (nothing to execute).
func TestClassifyCommand_Empty(t *testing.T) {
	gate := NewGate()
	if level := gate.ClassifyCommand(""); level != LevelSafe {
		t.Errorf("ClassifyCommand(\"\") = %v, want Safe", level)
	}
}

func TestClassify_UsesStaticForKnownTool(t *testing.T) {
	gate := NewGate()
	// 分级单源（评审 ①-4）：Classify 静态路由读取插件声明注入的表
	// （SetGate 生产组装在 appcore；此处直接注入代表同一来源）。
	SetStaticLevel("docker_ps", LevelSafe)
	if level := gate.Classify("docker_ps", json.RawMessage(`{}`)); level != LevelSafe {
		t.Errorf("Classify(docker_ps) = %v, want Safe", level)
	}
}

func TestClassify_UsesDynamicForExecuteCommand(t *testing.T) {
	gate := NewGate()
	if level := gate.Classify("execute_command", json.RawMessage(`{"command":"rm -rf /var/log"}`)); level != LevelRisky {
		t.Errorf("Classify(execute_command, rm) = %v, want Risky", level)
	}
}
