// claude.go 从 Claude Code 的 ~/.claude/settings.json（env 对象）解析
// API 配置候选值，供 appcore 启动引导使用。
package config

import "encoding/json"

// ParseClaudeSettings 解析 settings.json 内容，返回仅含命中字段的 Config
// （未命中字段留空）。JSON 损坏或无 env 键时返回空 Config 而非报错，
// 个别值为非字符串时容忍并取回已解码键——引导是尽力而为的兜底，
// 失败应保持现状而非阻断启动。
func ParseClaudeSettings(data []byte) *Config {
	var doc struct {
		Env map[string]string `json:"env"`
	}
	// 语法错误时 Env 保持为空（checkValid 先于解码）；个别值为非字符串的
	// 类型错误时已解码的键仍会填充——尽力而为，容忍部分损坏，取回能用的。
	_ = json.Unmarshal(data, &doc)
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := doc.Env[k]; v != "" {
				return v
			}
		}
		return ""
	}
	return &Config{
		APIKey:     get("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"),
		APIBaseURL: get("ANTHROPIC_BASE_URL"),
		Model: get("ANTHROPIC_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL"),
	}
}
