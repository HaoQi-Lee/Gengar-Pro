package consensus

import (
	"testing"
)

func TestUnmarshal_BasicShape(t *testing.T) {
	// Test removed - yaml.Unmarshal not used directly in production code
	t.Skip("yaml unmarshaling tested via Load()")
}

func TestLoad_DefaultDev(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if c.Profile != "dev" {
		t.Errorf("default profile = %q, want dev", c.Profile)
	}
}

func TestLoad_ExplicitProd(t *testing.T) {
	c, err := Load("prod")
	if err != nil {
		t.Fatalf("Load(prod) error: %v", err)
	}
	if c.Profile != "prod" {
		t.Errorf("profile = %q, want prod", c.Profile)
	}
	if len(c.Guidelines) == 0 {
		t.Errorf("expected guidelines, got 0")
	}
}

func TestLoad_UnknownEnv(t *testing.T) {
	_, err := Load("nosuch")
	if err == nil {
		t.Fatalf("expected error for unknown env")
	}
	// 错误信息应包含可用 profile 列表
	msg := err.Error()
	for _, want := range []string{"dev", "prod"} {
		if !contains(msg, want) {
			t.Errorf("error message %q missing %q", msg, want)
		}
	}
}

func TestLoad_ProfileMatchesFilename(t *testing.T) {
	for _, env := range []string{"dev", "prod"} {
		c, err := Load(env)
		if err != nil {
			t.Fatalf("Load(%s) error: %v", env, err)
		}
		if c.Profile != env {
			t.Errorf("Load(%s).Profile = %q, want %q", env, c.Profile, env)
		}
	}
}

func TestLoad_UniqueGuidelineIDs(t *testing.T) {
	c, err := Load("dev")
	if err != nil {
		t.Fatalf("Load(dev) error: %v", err)
	}
	seen := map[string]bool{}
	for _, g := range c.Guidelines {
		if seen[g.ID] {
			t.Errorf("duplicate guideline id: %q", g.ID)
		}
		seen[g.ID] = true
	}
}

func TestCatalog_Format(t *testing.T) {
	c := &Consensus{
		Profile: "dev",
		Guidelines: []Guideline{
			{ID: "yum-repo", Title: "YUM 源配置", Summary: "开发环境 YUM 源地址", Content: "..."},
			{ID: "backup-policy", Title: "备份策略", Summary: "修改前必须备份", Content: "..."},
		},
	}
	out := c.Catalog()

	// 必含 profile 名
	if !contains(out, "profile: dev") {
		t.Errorf("Catalog 缺少 profile 名: %q", out)
	}
	// 必含每条 id 与 summary
	if !contains(out, "yum-repo: 开发环境 YUM 源地址") {
		t.Errorf("Catalog 缺少 yum-repo 行: %q", out)
	}
	if !contains(out, "backup-policy: 修改前必须备份") {
		t.Errorf("Catalog 缺少 backup-policy 行: %q", out)
	}
}

func TestCatalog_EmptyGuidelines(t *testing.T) {
	c := &Consensus{Profile: "dev"}
	out := c.Catalog()
	if out == "" {
		t.Errorf("Catalog should still render header for empty guidelines")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && findSubstring(s, sub)
}

func findSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
