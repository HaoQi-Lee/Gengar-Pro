package plugin

import (
	"context"
	"encoding/json"
	"fmt"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/safety"
)

type ClarifyPlugin struct{}

func NewClarifyPlugin() *ClarifyPlugin {
	return &ClarifyPlugin{}
}

func (p *ClarifyPlugin) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "ask_user",
			Description: "Ask the user a clarifying multiple-choice question. Handled directly by the agent loop, not via shell.\nUse when: intent is ambiguous (multiple valid interpretations); destructive operation needs target confirmation; choice affects business logic. Ask one question at a time, options must be mutually exclusive.\nReturns: the user's chosen option (or a free-form answer).",
			InputSchema: json.RawMessage(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["question", "options"],
  "additionalProperties": false,
  "properties": {
    "question": {
      "type": "string",
      "description": "要向用户提出的完整问题，应以问号结尾"
    },
    "header": {
      "type": "string",
      "description": "问题的短标签，最多 12 个字符，用于终端显示"
    },
    "options": {
      "type": "array",
      "minItems": 2,
      "maxItems": 4,
      "description": "供用户选择的选项列表",
      "items": {
        "type": "object",
        "required": ["label", "description"],
        "additionalProperties": false,
        "properties": {
          "label": {
            "type": "string",
            "description": "选项的显示文本，1-5 个词"
          },
          "description": {
            "type": "string",
            "description": "选项的详细说明，帮助用户理解该选项的含义"
          }
        }
      }
    }
  }
}`),
			RiskLevel: safety.LevelSafe,
		},
	}
}

// Execute 永不远端执行：ask_user 由 Agent 循环在 Registry 之前拦截。
func (p *ClarifyPlugin) Execute(ctx context.Context, toolName string, input json.RawMessage, _ executor.Executor) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("ask_user should be handled by agent loop, not registry")
}
