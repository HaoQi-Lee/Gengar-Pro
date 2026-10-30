package config

import (
	"os"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.APIBaseURL != "https://api.anthropic.com" {
		t.Errorf("APIBaseURL = %q, want %q", cfg.APIBaseURL, "https://api.anthropic.com")
	}
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-20250514")
	}
	if cfg.MaxSteps != 20 {
		t.Errorf("MaxSteps = %d, want %d", cfg.MaxSteps, 20)
	}
	if cfg.CommandTimeout != "30s" {
		t.Errorf("CommandTimeout = %q, want %q", cfg.CommandTimeout, "30s")
	}
}

func TestLoad_EnvVars(t *testing.T) {
	os.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	os.Setenv("ANTHROPIC_BASE_URL", "https://custom-api.example.com")
	os.Setenv("ANTHROPIC_MODEL", "claude-opus-4-20250514")
	defer os.Unsetenv("ANTHROPIC_API_KEY")
	defer os.Unsetenv("ANTHROPIC_BASE_URL")
	defer os.Unsetenv("ANTHROPIC_MODEL")

	cfg := Load()
	if cfg.APIKey != "sk-test-key" {
		t.Errorf("APIKey = %q, want %q", cfg.APIKey, "sk-test-key")
	}
	if cfg.APIBaseURL != "https://custom-api.example.com" {
		t.Errorf("APIBaseURL = %q, want %q", cfg.APIBaseURL, "https://custom-api.example.com")
	}
	if cfg.Model != "claude-opus-4-20250514" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-opus-4-20250514")
	}
}

func TestLoad_DefaultsWhenNoEnv(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("ANTHROPIC_MODEL")

	cfg := Load()
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", cfg.APIKey)
	}
	if cfg.APIBaseURL != "https://api.anthropic.com" {
		t.Errorf("APIBaseURL = %q, want default", cfg.APIBaseURL)
	}
	if cfg.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want default", cfg.Model)
	}
}

func TestLoadEnv_OnlyExplicit(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	t.Setenv("ANTHROPIC_MODEL", "m-env")

	cfg := LoadEnv()
	if want := (Config{Model: "m-env"}); *cfg != want {
		t.Errorf("LoadEnv() = %+v, want %+v", *cfg, want)
	}
}
