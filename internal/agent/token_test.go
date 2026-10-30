package agent

import (
	"strings"
	"testing"

	"gengar-pro/internal/claude"
)

func TestEstimateTokens_ASCII(t *testing.T) {
	text := "Hello, this is a test message."
	tokens := EstimateTokens(text)
	expected := len(text) / 4
	if tokens != expected {
		t.Errorf("EstimateTokens = %d, want %d", tokens, expected)
	}
}

func TestEstimateTokens_Chinese(t *testing.T) {
	text := "你好世界测试消息"
	tokens := EstimateTokens(text)
	if tokens < 4 || tokens > 8 {
		t.Errorf("EstimateTokens for Chinese = %d, want 4-8", tokens)
	}
}

func TestEstimateTokens_Empty(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Errorf("EstimateTokens('') should be 0")
	}
}

func TestShouldCompress_BelowThreshold(t *testing.T) {
	conv := NewConversation("system")
	for i := 0; i < 5; i++ {
		conv.AddUserMessage("short message")
	}
	if ShouldCompress(conv, 100000) {
		t.Error("ShouldCompress should be false when well below limit")
	}
}

func TestShouldCompress_AboveThreshold(t *testing.T) {
	conv := NewConversation("system")
	for i := 0; i < 100; i++ {
		conv.AddUserMessage("This is a longer message that adds more tokens to the conversation history for testing purposes.")
	}
	if !ShouldCompress(conv, 1000) {
		t.Error("ShouldCompress should be true when above 80% of limit")
	}
}

// TestEstimateConversationTokens_ContentBlocks: 评审 ②-1 的核心回归——
// assistant 消息（[]claude.ContentBlock）在旧实现的类型 switch 里走
// default 恒漏算，ShouldCompress 因此恒 false。现在 ContentBlock 切片
// 必须计入。
func TestEstimateConversationTokens_ContentBlocks(t *testing.T) {
	conv := NewConversation("short system")
	conv.AddUserMessage("hi")
	conv.AddAssistantMessage([]claude.ContentBlock{
		{Type: "text", Text: strings.Repeat("a", 4000)},
	})
	conv.AddToolResult("t1", strings.Repeat("b", 4000))
	conv.AddAssistantMessage([]claude.ContentBlock{
		{Type: "tool_use", ID: "t1", Name: "check_disk", Input: []byte(`{"path":"/"}`)},
	})
	// 系统提示词 12 + "hi" 1 + 4000/4 + 4000/4 + ~10+4 ≈ 2030 — 明显 > 0
	if got := EstimateConversationTokens(conv); got < 2000 {
		t.Errorf("EstimateConversationTokens = %d; content blocks were skipped (old bug)", got)
	}
}

// TestCompressConversation: 占位替换只动旧 tool_result（保留最近 6 条完整），
// user/assistant 文本与工具配对结构不变；幂等可重复应用。
func TestCompressConversation(t *testing.T) {
	conv := NewConversation("sys")
	conv.AddUserMessage("task")
	// 10 条 tool_result：前 4 条应被省略，最近 6 条保留
	for i := 0; i < 10; i++ {
		conv.AddToolResult(string(rune('A'+i)), strings.Repeat("x", 1000))
	}

	omitted := CompressConversation(conv)
	if omitted <= 0 {
		t.Fatalf("CompressConversation = %d; want > 0", omitted)
	}
	blocks := 0
	elided := 0
	for _, msg := range conv.Messages() {
		bs, ok := msg.Content.([]claude.ContentBlock)
		if !ok {
			continue
		}
		for _, b := range bs {
			if b.Type != "tool_result" {
				continue
			}
			blocks++
			if s, _ := b.Content.(string); strings.Contains(s, "[tool result elided:") {
				elided++
			} else if s, _ := b.Content.(string); !strings.HasPrefix(s, "xxxx") {
				t.Fatalf("unexpected tool_result content: %q", s[:30])
			}
		}
	}
	if blocks != 10 {
		t.Fatalf("tool_result count changed: %d; want 10 (structure must be intact)", blocks)
	}
	if elided != 4 {
		t.Fatalf("elided = %d; want 4 (10 - keepRecent 6)", elided)
	}
	// 幂等：再次压缩无新省略（占位符已不含原文）
	if again := CompressConversation(conv); again != 0 {
		t.Fatalf("second CompressConversation = %d; want 0 (idempotent)", again)
	}
}

// TestCompressConversation_KeepsRecentAndBoundary: 条数 ≤ 6 时不动任何内容。
func TestCompressConversation_KeepsRecentAndBoundary(t *testing.T) {
	conv := NewConversation("sys")
	for i := 0; i < 6; i++ {
		conv.AddToolResult(string(rune('A'+i)), strings.Repeat("y", 100))
	}
	if omitted := CompressConversation(conv); omitted != 0 {
		t.Fatalf("CompressConversation = %d; want 0 when <= keepRecent", omitted)
	}
}

// TestCompressConversation_ToolUseBlockUntouched: tool_use 块（assistant
// 消息内）不得被压缩触碰——模型需要它的 Input 来理解后续 tool_result 语境。
func TestCompressConversation_ToolUseBlockUntouched(t *testing.T) {
	conv := NewConversation("sys")
	input := []byte(`{"path":"/var/log"}`)
	for i := 0; i < 10; i++ {
		conv.AddAssistantMessage([]claude.ContentBlock{
			{Type: "tool_use", ID: "t" + string(rune('0'+i)), Name: "read_file", Input: input},
		})
		conv.AddToolResult("t"+string(rune('0'+i)), strings.Repeat("z", 800))
	}
	CompressConversation(conv)
	for _, msg := range conv.Messages() {
		bs, ok := msg.Content.([]claude.ContentBlock)
		if !ok {
			continue
		}
		for _, b := range bs {
			if b.Type == "tool_use" {
				if string(b.Input) != string(input) {
					t.Fatalf("tool_use Input mutated: %s", b.Input)
				}
			}
		}
	}
}
