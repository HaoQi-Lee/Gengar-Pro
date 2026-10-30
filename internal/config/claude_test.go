package config

import "testing"

func TestParseClaudeSettings(t *testing.T) {
	cases := []struct {
		name string
		json string
		want Config
	}{
		{
			name: "APIKey 优先 ANTHROPIC_API_KEY",
			json: `{"env":{"ANTHROPIC_API_KEY":"sk-a","ANTHROPIC_AUTH_TOKEN":"tok-b"}}`,
			want: Config{APIKey: "sk-a"},
		},
		{
			name: "APIKey 回退 ANTHROPIC_AUTH_TOKEN",
			json: `{"env":{"ANTHROPIC_AUTH_TOKEN":"tok-b"}}`,
			want: Config{APIKey: "tok-b"},
		},
		{
			name: "BaseURL 取 ANTHROPIC_BASE_URL",
			json: `{"env":{"ANTHROPIC_BASE_URL":"https://gw.example.com"}}`,
			want: Config{APIBaseURL: "https://gw.example.com"},
		},
		{
			name: "Model 优先 ANTHROPIC_MODEL",
			json: `{"env":{"ANTHROPIC_MODEL":"m0","ANTHROPIC_DEFAULT_SONNET_MODEL":"m1"}}`,
			want: Config{Model: "m0"},
		},
		{
			name: "Model 回退 SONNET 先于 HAIKU",
			json: `{"env":{"ANTHROPIC_DEFAULT_SONNET_MODEL":"ms","ANTHROPIC_DEFAULT_HAIKU_MODEL":"mh"}}`,
			want: Config{Model: "ms"},
		},
		{
			name: "Model 回退 HAIKU 先于 OPUS",
			json: `{"env":{"ANTHROPIC_DEFAULT_HAIKU_MODEL":"mh","ANTHROPIC_DEFAULT_OPUS_MODEL":"mo"}}`,
			want: Config{Model: "mh"},
		},
		{
			name: "Model 仅 OPUS 也取",
			json: `{"env":{"ANTHROPIC_DEFAULT_OPUS_MODEL":"mo"}}`,
			want: Config{Model: "mo"},
		},
		{
			name: "坏 JSON 返回空",
			json: `{not-json`,
			want: Config{},
		},
		{
			name: "无 env 键返回空",
			json: `{"permissions":{}}`,
			want: Config{},
		},
		{
			name: "空值视为未设置",
			json: `{"env":{"ANTHROPIC_AUTH_TOKEN":"","ANTHROPIC_BASE_URL":""}}`,
			want: Config{},
		},
		{
			name: "非字符串值容忍且不影响其他键",
			json: `{"env":{"MAX_TOKENS":4096,"ANTHROPIC_AUTH_TOKEN":"tok-x"}}`,
			want: Config{APIKey: "tok-x"},
		},
		{
			name: "env 为 null 返回空",
			json: `{"env":null}`,
			want: Config{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseClaudeSettings([]byte(tc.json))
			if *got != tc.want {
				t.Fatalf("ParseClaudeSettings(%s) = %+v, want %+v", tc.json, *got, tc.want)
			}
		})
	}
}
