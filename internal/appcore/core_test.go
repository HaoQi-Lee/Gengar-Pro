package appcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gengar-pro/internal/config"
	"gengar-pro/internal/host"
)

func newTestCore(t *testing.T) *Core {
	t.Helper()
	c, err := OpenAt(t.TempDir(), host.MemoryStore{}, 5*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestPinnedDefaults(t *testing.T) {
	c := newTestCore(t)
	_ = c.Store().Save(&host.Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root"})
	_ = c.Store().Save(&host.Host{ID: "h2", Name: "db-01", Addr: "10.0.0.2", Port: 22, User: "root"})
	c.SetPinned([]string{"h2"})

	names, unresolved := c.Names(nil)
	if len(unresolved) != 0 || len(names) != 1 || names[0] != "db-01" {
		t.Fatalf("empty refs must fall back to pinned: %v %v", names, unresolved)
	}
}

func TestResolveReportsConnectFailure(t *testing.T) {
	c := newTestCore(t)
	_ = c.Store().Save(&host.Host{ID: "h1", Name: "web-01", Addr: "127.0.0.1", Port: 1, User: "root"})
	_, _, err := c.Resolve([]string{"web-01"})
	if err == nil || !strings.Contains(err.Error(), "connect web-01") {
		t.Fatalf("expected connect error, got %v", err)
	}
}

func TestResolveUnresolvedRefsSurface(t *testing.T) {
	c := newTestCore(t)
	_, unresolved, err := c.Resolve([]string{"ghost"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(unresolved) != 1 || unresolved[0] != "ghost" {
		t.Fatalf("unresolved=%v", unresolved)
	}
}

func TestCatalogTextListsHosts(t *testing.T) {
	c := newTestCore(t)
	_ = c.Store().Save(&host.Host{ID: "h1", Name: "web-01", Addr: "10.0.0.1", Port: 22, User: "root",
		Tags: []string{"web"}, OS: "ubuntu 22.04"})
	c.SetPinned([]string{"h1"})
	text := c.CatalogText()
	for _, want := range []string{"web-01", "web", "ubuntu 22.04", "🎯", "不要把范围扩大到目标之外"} {
		if !strings.Contains(text, want) {
			t.Fatalf("catalog missing %q:\n%s", want, text)
		}
	}
	// 04-T4 审查移交：未钉住时明确提示，模型才知道先确认目标
	c.SetPinned(nil)
	if text2 := c.CatalogText(); !strings.Contains(text2, "未设置目标主机") {
		t.Fatalf("empty pin must be explicit:\n%s", text2)
	}
}

func TestNamesAndResolveSymmetry(t *testing.T) {
	// 04-T3 定下的承重不变量：Names 是 Resolve 的免连接投影
	c := newTestCore(t)
	_ = c.Store().Save(&host.Host{ID: "h1", Name: "web-01", Addr: "127.0.0.1", Port: 1, User: "root"})
	names, _ := c.Names([]string{"all"})
	if len(names) != 1 || names[0] != "web-01" {
		t.Fatalf("names=%v", names)
	}
	// Resolve 会因连接失败报错——对称性在"名字集合"层面验证：
	// 解析成功的主机名序列（失败前）与 Names 一致。这里验证单主机场景
	// Names 正确即可；连接成功的对称性由 05-T3 集成测试覆盖。
}

func TestLoadConfigSettingsOverride(t *testing.T) {
	c := newTestCore(t)
	_ = c.Store().SetSetting("model", "claude-opus-4-8")
	cfg := LoadConfig(c.Store(), c.Secrets())
	if cfg.Model != "claude-opus-4-8" {
		t.Fatalf("settings must override env default: %q", cfg.Model)
	}
}

func TestBootstrapFromClaudeImportsAndPersists(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	err := os.WriteFile(settings, []byte(`{"env":{
		"ANTHROPIC_AUTH_TOKEN":"tok-x",
		"ANTHROPIC_BASE_URL":"https://gw.example.com",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":"m-sonnet"}}`), 0o600)
	if err != nil {
		t.Fatalf("write settings: %v", err)
	}
	secrets := host.MemoryStore{}
	c, err := OpenAt(dir, host.MemoryStore{}, 5*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	cfg := &config.Config{}
	bootstrapFromClaude(c.Store(), secrets, cfg, settings)
	if cfg.APIKey != "tok-x" || cfg.APIBaseURL != "https://gw.example.com" || cfg.Model != "m-sonnet" {
		t.Fatalf("import failed: %+v", cfg)
	}
	// api_key 落 secrets 密文通道；base_url/model 落 settings 表
	if v, err := secrets.Get("api_key"); err != nil || v != "tok-x" {
		t.Fatalf("api_key 未入 secrets: %q (%v)", v, err)
	}
	// 重开 store 验证一次性初始化确实落库
	reopened, err := host.OpenStore(filepath.Join(dir, "gengar.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	for key, want := range map[string]string{
		"base_url": "https://gw.example.com",
		"model":    "m-sonnet",
	} {
		if v, err := reopened.GetSetting(key); err != nil || v != want {
			t.Fatalf("persisted %s = %q (%v), want %q", key, v, err, want)
		}
	}
}

func TestBootstrapFromClaudeNoOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"tok-x","ANTHROPIC_BASE_URL":"https://gw.example.com"}}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	c, err := OpenAt(dir, host.MemoryStore{}, 5*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()

	// cfg 已有 APIKey（模拟 env/store 已提供）与 Model → 引导只补 base_url
	cfg := &config.Config{APIKey: "sk-env", Model: "m-store"}
	bootstrapFromClaude(c.Store(), c.Secrets(), cfg, settings)
	if cfg.APIKey != "sk-env" || cfg.Model != "m-store" {
		t.Fatalf("must not override existing: %+v", cfg)
	}
	if cfg.APIBaseURL != "https://gw.example.com" {
		t.Fatalf("missing field must be imported: %+v", cfg)
	}
	if v, _ := c.Store().GetSetting("api_key"); v != "" {
		t.Fatalf("api_key must not persist when already configured: %q", v)
	}
}

func TestBootstrapFromClaudeMissingFileNoOp(t *testing.T) {
	c := newTestCore(t)
	cfg := &config.Config{APIKey: "sk-env"}
	bootstrapFromClaude(c.Store(), c.Secrets(), cfg, filepath.Join(t.TempDir(), "nope.json"))
	if cfg.APIKey != "sk-env" || cfg.APIBaseURL != "" || cfg.Model != "" {
		t.Fatalf("missing file must be a no-op: %+v", cfg)
	}
}

// TestLoadConfigBootstrapsFromClaudeSettings 验证 LoadConfig 端到端引导：
// env 与 store 均缺失时从真实 ~/.claude/settings.json 导入并落库。
// 路径不可注入（设计决定），故用跳过守卫保证确定性：本机无可引导
// settings.json 时跳过而非失败。
func TestLoadConfigBootstrapsFromClaudeSettings(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("ANTHROPIC_MODEL")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Skip("no ~/.claude/settings.json on this machine")
	}
	src := config.ParseClaudeSettings(data)
	if src.APIKey == "" {
		t.Skip("settings.json has no bootstrappable api key")
	}

	c := newTestCore(t)
	cfg := LoadConfig(c.Store(), c.Secrets())
	if cfg.APIKey != src.APIKey {
		t.Fatalf("APIKey 未按引导导入（只打印长度防泄漏）: got len=%d, want len=%d",
			len(cfg.APIKey), len(src.APIKey))
	}
	if cfg.MaxSteps != 20 || cfg.CommandTimeout != "30s" {
		t.Fatalf("defaults lost: MaxSteps=%d CommandTimeout=%q", cfg.MaxSteps, cfg.CommandTimeout)
	}
	if v, err := c.Secrets().Get("api_key"); err != nil || v != src.APIKey {
		t.Fatalf("引导的 api_key 未入 secrets（只打印长度防泄漏）: got len=%d (%v), want len=%d",
			len(v), err, len(src.APIKey))
	}
}
