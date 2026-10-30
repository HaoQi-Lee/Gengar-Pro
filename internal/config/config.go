package config

import "os"

type Config struct {
	APIKey         string
	APIBaseURL     string
	Model          string
	MaxSteps       int
	CommandTimeout string
}

func Default() *Config {
	return &Config{
		APIBaseURL:     "https://api.anthropic.com",
		Model:          "claude-sonnet-4-20250514",
		MaxSteps:       20,
		CommandTimeout: "30s",
	}
}

// LoadEnv 返回环境变量显式设置的 API 配置，未设置字段留空（无默认值打底）。
// 供需要区分「显式配置」与「内置默认」的调用方（appcore.LoadConfig 的
// settings.json 引导层）使用；Load 仍保留默认值打底的便捷语义（当前仅自身测试在用）。
func LoadEnv() *Config {
	cfg := &Config{}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.APIKey = key
	}
	if url := os.Getenv("ANTHROPIC_BASE_URL"); url != "" {
		cfg.APIBaseURL = url
	}
	if model := os.Getenv("ANTHROPIC_MODEL"); model != "" {
		cfg.Model = model
	}
	return cfg
}

func Load() *Config {
	cfg := Default()
	env := LoadEnv()
	if env.APIKey != "" {
		cfg.APIKey = env.APIKey
	}
	if env.APIBaseURL != "" {
		cfg.APIBaseURL = env.APIBaseURL
	}
	if env.Model != "" {
		cfg.Model = env.Model
	}
	return cfg
}
