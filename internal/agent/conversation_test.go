package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"gengar-pro/internal/claude"
)

const testSystemPrompt = "You are Gengar, a Linux operations AI assistant."

func TestNewConversation(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	if conv == nil {
		t.Fatal("NewConversation returned nil")
	}
	if len(conv.Messages()) != 0 {
		t.Errorf("new conversation should have 0 messages, got %d", len(conv.Messages()))
	}
}

func TestConversation_AddUserMessage(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	conv.AddUserMessage("check nginx status")
	msgs := conv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("Role = %q, want user", msgs[0].Role)
	}
}

func TestConversation_AddAssistantMessage(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	blocks := []claude.ContentBlock{
		{Type: "text", Text: "Let me check."},
		{Type: "tool_use", ID: "toolu_001", Name: "execute_command", Input: []byte(`{"command":"ls"}`)},
	}
	conv.AddAssistantMessage(blocks)
	msgs := conv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("Role = %q, want assistant", msgs[0].Role)
	}
}

func TestConversation_AddAssistantMessage_EmptyBlocksGetPlaceholder(t *testing.T) {
	for name, blocks := range map[string][]claude.ContentBlock{
		"nil slice":   nil,
		"empty slice": {},
	} {
		t.Run(name, func(t *testing.T) {
			conv := NewConversation(testSystemPrompt)
			conv.AddUserMessage("check nginx")
			conv.AddAssistantMessage(blocks)

			raw, err := json.Marshal(conv.Messages())
			if err != nil {
				t.Fatalf("marshal messages: %v", err)
			}
			if bytes.Contains(raw, []byte(`"content":null`)) {
				t.Fatalf("assistant message serialized with content:null: %s", raw)
			}
			content, ok := conv.Messages()[1].Content.([]claude.ContentBlock)
			if !ok || len(content) == 0 {
				t.Fatalf("assistant content should be a non-empty block list, got %#v", conv.Messages()[1].Content)
			}
			if content[0].Type != "text" || content[0].Text == "" {
				t.Errorf("placeholder block should be non-empty text, got %#v", content[0])
			}
		})
	}
}

func TestConversation_AddToolResult(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	conv.AddToolResult("toolu_001", "file1\nfile2\n")
	msgs := conv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("Role = %q, want user", msgs[0].Role)
	}
}

func TestConversation_FullFlow(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	conv.AddUserMessage("check nginx")
	conv.AddAssistantMessage([]claude.ContentBlock{
		{Type: "tool_use", ID: "toolu_001", Name: "systemctl_status", Input: []byte(`{"service":"nginx"}`)},
	})
	conv.AddToolResult("toolu_001", "● nginx.service - active (running)")
	conv.AddAssistantMessage([]claude.ContentBlock{{Type: "text", Text: "Nginx is running normally."}})
	msgs := conv.Messages()
	if len(msgs) != 4 {
		t.Fatalf("Messages count = %d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msgs[0].Role = %q, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msgs[1].Role = %q, want assistant", msgs[1].Role)
	}
	if msgs[2].Role != "user" {
		t.Errorf("msgs[2].Role = %q, want user", msgs[2].Role)
	}
	if msgs[3].Role != "assistant" {
		t.Errorf("msgs[3].Role = %q, want assistant", msgs[3].Role)
	}
}

func TestConversation_Clear(t *testing.T) {
	conv := NewConversation(testSystemPrompt)
	conv.AddUserMessage("test")
	conv.Clear()
	if len(conv.Messages()) != 0 {
		t.Errorf("after Clear, count = %d, want 0", len(conv.Messages()))
	}
}
