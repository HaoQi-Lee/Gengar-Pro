package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gengar-pro/internal/executor"
)

// PackageManager 提供 apt/yum/dnf 包管理工具。执行类子命令（install/
// remove/search/list）用 argv 形式：模型可控的包名/查询不经 shell 重新
// 解释（评审 ①-4）。包管理器按调用探测：插件不持有跨调用状态，同一实例
// 被 fan-out 到多台主机时各自独立探测，不会把 A 主机的探测结果泄漏给 B
// 主机。探测（cat /etc/os-release、command -v）本身命令固定无注入面，
// 仍走 RunShell。
type PackageManager struct{}

func NewPackageManager() *PackageManager { return &PackageManager{} }

func (pm *PackageManager) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "package_install",
			Description: "Install a system package using the host's package manager (apt/yum/dnf auto-detected from /etc/os-release).\nUse when: deploying a service that needs an apt/yum package; installing build deps. Don't construct yum/apt commands yourself.\nReturns: installer output. Prefer over execute_command + yum/apt install.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Package name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["name"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "package_remove",
			Description: "Uninstall a system package using the host's package manager.\nUse when: cleaning up an obsolete service; freeing disk.\nReturns: removal output. Prefer over execute_command + yum/apt remove.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Package name"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["name"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "package_search",
			Description: "Search the package index for available packages matching a query.\nUse when: confirming a package name exists before install; finding alternative versions.\nReturns: matching package names and short descriptions. Prefer over execute_command + yum/apt search.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["query"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "package_list",
			Description: "List installed packages on the host.\nUse when: checking whether a package is already installed; auditing versions before upgrade.\nReturns: installed package names with versions (from rpm/dpkg). Prefer over execute_command + rpm -qa / dpkg -l.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
	}
}

func (pm *PackageManager) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	mgr, err := pm.detectPackageManager(ctx, exec)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v", err), IsError: true}, nil
	}
	switch toolName {
	case "package_install":
		var p struct {
			Name string `json:"name"`
		}
		json.Unmarshal(input, &p)
		// argv 形式：包名不经 shell 重新解释（评审 ①-4），`--` 阻断
		// 以 - 开头的「包名」被当 flag 解析。
		return pm.exec(ctx, exec, mgr, "install", "-y", "--", p.Name)
	case "package_remove":
		var p struct {
			Name string `json:"name"`
		}
		json.Unmarshal(input, &p)
		return pm.exec(ctx, exec, mgr, "remove", "-y", "--", p.Name)
	case "package_search":
		var p struct {
			Query string `json:"query"`
		}
		json.Unmarshal(input, &p)
		if mgr == "apt" {
			// apt-cache 无子命令层，模式当独立 cli 调
			return pm.exec(ctx, exec, "apt-cache", "search", "--", p.Query)
		}
		return pm.exec(ctx, exec, mgr, "search", "--", p.Query)
	case "package_list":
		if mgr == "apt" {
			return pm.exec(ctx, exec, "dpkg", "-l")
		}
		return pm.exec(ctx, exec, mgr, "list", "installed")
	default:
		return ToolResult{}, fmt.Errorf("PackageManager: unknown tool %q", toolName)
	}
}

// detectPackageManager identifies the target host's package manager
// deterministically, per call (the plugin is stateless; each Execute may
// be bound to a different host).
//
// Strategy: read /etc/os-release (ID / ID_LIKE) to learn the distro family, then
// confirm the actual binary exists via `command -v` — a shell builtin that does
// NOT depend on the external `which` binary. This replaces the old approach of
// probing `which apt|yum|dnf`, which failed silently on minimal images that lack
// `which` and fell back to a hardcoded (and wrong-for-yum-hosts) default of apt.
//
// Returns an error when no manager is detectable, so the caller surfaces a clear
// failure instead of guessing apt and running a doomed `apt install`.
func (pm *PackageManager) detectPackageManager(ctx context.Context, exec executor.Executor) (string, error) {
	switch pm.familyFromOSRelease(ctx, exec) {
	case "apt":
		if pm.binaryExists(ctx, exec, "apt") {
			return "apt", nil
		}
	case "rpm":
		// Prefer dnf (modern Fedora/RHEL/Rocky/Alma/Kylin/openEuler...);
		// fall back to yum for legacy distros like CentOS 7.
		if pm.binaryExists(ctx, exec, "dnf") {
			return "dnf", nil
		}
		if pm.binaryExists(ctx, exec, "yum") {
			return "yum", nil
		}
	default:
		// Unknown distro: probe whatever is installed, preferring dnf > yum > apt.
		for _, m := range []string{"dnf", "yum", "apt"} {
			if pm.binaryExists(ctx, exec, m) {
				return m, nil
			}
		}
	}
	return "", fmt.Errorf("could not detect package manager: /etc/os-release did not identify the distro and no apt/yum/dnf binary is installed")
}

// familyFromOSRelease maps the distro's /etc/os-release ID/ID_LIKE tokens to a
// package-manager family: "apt", "rpm", or "" (unknown).
func (pm *PackageManager) familyFromOSRelease(ctx context.Context, exec executor.Executor) string {
	out, err := exec.RunShell(ctx, "cat /etc/os-release")
	if err != nil {
		return ""
	}
	for _, id := range parseOSReleaseIDs(string(out)) {
		switch id {
		case "debian", "ubuntu", "linuxmint", "kali", "raspbian", "pop", "neon", "deepin", "zorin", "aosc":
			return "apt"
		case "rhel", "centos", "fedora", "rocky", "almalinux", "ol", "oraclelinux",
			"scientific", "amzn", "kylin", "openeuler", "uos", "anolis", "nobara":
			return "rpm"
		}
	}
	return ""
}

// binaryExists reports whether the given binary resolves on PATH, using the
// `command -v` shell builtin (always available in bash) rather than `which`
// (a separate package that minimal images often omit).
func (pm *PackageManager) binaryExists(ctx context.Context, exec executor.Executor, name string) bool {
	out, err := exec.RunShell(ctx, "command -v "+name)
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// parseOSReleaseIDs extracts lowercased tokens from the ID= and ID_LIKE= lines
// of /etc/os-release. ID_LIKE is space-separated and may carry several ancestors
// (e.g. centos: ID_LIKE="rhel fedora"), so all of them are collected.
func parseOSReleaseIDs(content string) []string {
	var ids []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		var raw string
		switch {
		case strings.HasPrefix(line, "ID="):
			raw = strings.TrimPrefix(line, "ID=")
		case strings.HasPrefix(line, "ID_LIKE="):
			raw = strings.TrimPrefix(line, "ID_LIKE=")
		default:
			continue
		}
		raw = strings.Trim(raw, `"'`)
		for _, tok := range strings.Fields(raw) {
			ids = append(ids, strings.ToLower(strings.Trim(tok, `"'`)))
		}
	}
	return ids
}

func (pm *PackageManager) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}
