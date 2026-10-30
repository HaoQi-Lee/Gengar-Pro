package agent

import "gengar-pro/internal/claude"

type Conversation struct {
	systemPrompt string
	messages     []claude.Message
}

func NewConversation(systemPrompt string) *Conversation {
	return &Conversation{systemPrompt: systemPrompt}
}

// SetSystemPrompt 替换系统提示词。主机清单可能随会话中途增删而变化，
// Agent 在每次 ProcessInput 开始时重建提示词。
func (c *Conversation) SetSystemPrompt(p string) { c.systemPrompt = p }

func (c *Conversation) SystemPrompt() string       { return c.systemPrompt }
func (c *Conversation) Messages() []claude.Message { return c.messages }

func (c *Conversation) AddUserMessage(text string) {
	c.messages = append(c.messages, claude.Message{Role: "user", Content: text})
}

func (c *Conversation) AddAssistantMessage(blocks []claude.ContentBlock) {
	// The API rejects content:null / empty arrays. An empty turn (cancelled
	// before the first block, or a response with no text/tool_use blocks)
	// must still be recorded as a valid assistant message.
	if len(blocks) == 0 {
		blocks = []claude.ContentBlock{{Type: "text", Text: "(no content)"}}
	}
	c.messages = append(c.messages, claude.Message{Role: "assistant", Content: blocks})
}

func (c *Conversation) AddToolResult(toolUseID, content string) {
	c.messages = append(c.messages, claude.Message{
		Role: "user",
		Content: []claude.ContentBlock{
			{Type: "tool_result", ToolUseID: toolUseID, Content: content},
		},
	})
}

func (c *Conversation) Clear() { c.messages = nil }
