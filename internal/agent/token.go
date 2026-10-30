package agent

import (
	"fmt"
	"strings"

	"gengar-pro/internal/claude"
)

const (
	maxContextTokens  = 200000
	compressThreshold = 0.8
	// keepRecentToolResults 是压缩时保留完整的最近 tool_result 条数——
	// 最新一轮的执行上下文对模型的即时推理最关键，只省略更旧的。
	keepRecentToolResults = 6
)

func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	asciiCount, cjkCount := 0, 0
	for _, r := range text {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjkCount++
		} else {
			asciiCount++
		}
	}
	tokens := asciiCount/4 + cjkCount*2/3
	if tokens == 0 {
		tokens = 1
	}
	return tokens
}

// EstimateConversationTokens 估算整段对话（系统提示词 + 全部消息）的
// token 量。消息内容支持两种形态（与 claude.Message.Content 的实际
// 装配一致）：string（user 文本）与 []claude.ContentBlock（assistant
// 回复 / tool_result）。旧实现只认 string/[]interface{}——ContentBlock
// 切片恒走 default，消息体全部漏算，ShouldCompress 因此恒 false
//（评审 ②-1 修复）。tool_use 块按输入 JSON 长度估算，其余块按文本。
func EstimateConversationTokens(conv *Conversation) int {
	total := EstimateTokens(conv.SystemPrompt())
	for _, msg := range conv.Messages() {
		switch content := msg.Content.(type) {
		case string:
			total += EstimateTokens(content)
		case []claude.ContentBlock:
			for _, b := range content {
				switch b.Type {
				case "tool_use":
					total += 10 + len(b.Input)/4
				case "tool_result":
					if s, ok := b.Content.(string); ok {
						total += EstimateTokens(s)
					} else {
						total += 10
					}
				default:
					total += EstimateTokens(b.Text)
				}
			}
		default:
			total += 10 // 未知形态兜底：宁可高估不可漏算
		}
	}
	return total
}

func ShouldCompress(conv *Conversation, tokenLimit int) bool {
	if tokenLimit <= 0 {
		tokenLimit = maxContextTokens
	}
	return EstimateConversationTokens(conv) > int(float64(tokenLimit)*compressThreshold)
}

// CompressConversation 把最旧的 tool_result 内容替换为占位符
//（保留最近 keepRecentToolResults 条完整）。占位替换是确定性的：
// 不做模型侧摘要（零成本、零额外 API 调用），工具配对结构
//（tool_use ↔ tool_result 消息对）保持完整——这是 API 消息校验的
// 硬约束。user/assistant 文本不动：任务叙述与推理链是模型的语境主脉。
// 返回被省略的总字符数（0 = 无需动作）。同一结果可重复应用
//（幂等：占位符不含原文，再次压缩不会重复省略）。
func CompressConversation(conv *Conversation) int {
	// 收集全部 tool_result 块的（消息下标, 块下标），按消息顺序即时间序
	type loc struct{ msg, blk int }
	var results []loc
	for mi, msg := range conv.Messages() {
		blocks, ok := msg.Content.([]claude.ContentBlock)
		if !ok {
			continue
		}
		for bi, b := range blocks {
			if b.Type == "tool_result" {
				results = append(results, loc{mi, bi})
			}
		}
	}
	if len(results) <= keepRecentToolResults {
		return 0
	}
	// 被省略的是除最近 K 条之外的旧结果
	omit := results[:len(results)-keepRecentToolResults]
	total := 0
	for _, l := range omit {
		blocks := conv.messages[l.msg].Content.([]claude.ContentBlock)
		b := &blocks[l.blk]
		s, _ := b.Content.(string)
		// 幂等：占位符不含原文，再次压缩跳过已省略的块（长度不降反升，
		// len(s) - len(placeholder) 为负，也绝不能替换）
		if s == "" || strings.HasPrefix(s, "[tool result elided:") {
			continue
		}
		firstLine := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			firstLine = s[:i]
		}
		if len(firstLine) > 80 {
			firstLine = firstLine[:80] + "…"
		}
		placeholder := fmt.Sprintf("[tool result elided: %d chars, first line: %s]",
			len(s), firstLine)
		b.Content = placeholder
		total += len(s) - len(placeholder)
	}
	return total
}
