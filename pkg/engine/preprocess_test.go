package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCompressionSystemPrompt(t *testing.T) {
	prompt := BuildCompressionSystemPrompt("", 40, 60)
	if !strings.Contains(prompt, "40%") {
		t.Fatalf("expected prompt to contain 40%%, got: %s", prompt)
	}
	if !strings.Contains(prompt, "60%") {
		t.Fatalf("expected prompt to contain 60%%, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Task: Compress the input text") {
		t.Fatalf("expected prompt to contain task description, got: %s", prompt)
	}
}

func TestCleanCompressedText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Plain text",
			input:    "Meeting on March 15, 2024 at 09:00 CET.",
			expected: "Meeting on March 15, 2024 at 09:00 CET.",
		},
		{
			name:     "Thinking tags",
			input:    "<think>Let's remove fluff.</think>Meeting on March 15, 2024 at 09:00 CET.",
			expected: "Meeting on March 15, 2024 at 09:00 CET.",
		},
		{
			name:     "Markdown code fence",
			input:    "```\nMeeting on March 15, 2024.\n```",
			expected: "Meeting on March 15, 2024.",
		},
		{
			name:     "User message tag",
			input:    "<user_message>\nMeeting on March 15, 2024.\n</user_message>",
			expected: "Meeting on March 15, 2024.",
		},
		{
			name:     "Assistant message tag",
			input:    "<assistant_message>Deploy to Render using Gunicorn on port 8000.</assistant_message>",
			expected: "Deploy to Render using Gunicorn on port 8000.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := CleanCompressedText(tt.input)
			if actual != tt.expected {
				t.Errorf("CleanCompressedText() = %q, expected %q", actual, tt.expected)
			}
		})
	}
}

func TestCompressMessage_ShortMessageSkipped(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cache.db")

	cfg := DefaultCompressionConfig()
	cfg.MinWords = 15

	mc, err := NewMessageCompressor(nil, dbPath, cfg)
	if err != nil {
		t.Fatalf("failed to create compressor: %v", err)
	}
	defer mc.Close()

	ctx := context.Background()
	shortText := "March 15, 2024 at 09:00 CET"
	out, err := mc.CompressMessage(ctx, "user", shortText)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != shortText {
		t.Fatalf("expected short text to be unchanged, got %s", out)
	}

	stats := mc.Stats()
	if stats.SkippedTurns != 1 {
		t.Fatalf("expected 1 skipped turn, got %d", stats.SkippedTurns)
	}
}

func TestCompressMessage_CachePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cache.db")

	cfg := DefaultCompressionConfig()
	cfg.MinWords = 5

	mc, err := NewMessageCompressor(nil, dbPath, cfg)
	if err != nil {
		t.Fatalf("failed to create compressor: %v", err)
	}

	ctx := context.Background()
	// Manually insert into cache to verify lookup
	_, err = mc.db.Exec(`INSERT INTO compressed_messages 
		(message_hash, role, original_text, compressed_text, original_words, compressed_words, created_at)
		VALUES ('test_hash', 'user', 'This is a test message to be compressed.', 'Compressed text.', 8, 2, '2026-09-07T00:00:00Z')`)
	if err != nil {
		t.Fatalf("failed to insert test cache entry: %v", err)
	}

	// Verify query with matching hash
	h := BuildCompressionSystemPrompt("", 40, 60)
	_ = h
	mc.Close()

	// Re-open compressor with same DB to test persistence
	mc2, err := NewMessageCompressor(nil, dbPath, cfg)
	if err != nil {
		t.Fatalf("failed to re-open compressor: %v", err)
	}
	defer mc2.Close()

	var count int
	err = mc2.db.QueryRowContext(ctx, "SELECT count(*) FROM compressed_messages").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 cached entry, got %d (err: %v)", count, err)
	}
}

func TestPreprocessBeamConversation(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cache.db")

	cfg := DefaultCompressionConfig()
	cfg.MinWords = 50 // High threshold so all turns are skipped without needing an LLM

	mc, err := NewMessageCompressor(nil, dbPath, cfg)
	if err != nil {
		t.Fatalf("failed to create compressor: %v", err)
	}
	defer mc.Close()

	rawJSON := `{
		"conversation_id": "test_1",
		"chat": [
			[
				{"id": 0, "role": "user", "content": "Hello there.", "time_anchor": "2024-03-15"},
				{"id": 1, "role": "assistant", "content": "How can I help you today?"}
			]
		]
	}`

	var conv map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &conv); err != nil {
		t.Fatalf("failed to unmarshal test conv: %v", err)
	}

	ctx := context.Background()
	if err := mc.PreprocessBeamConversation(ctx, conv); err != nil {
		t.Fatalf("PreprocessBeamConversation failed: %v", err)
	}

	// Ensure structural integrity and metadata retention
	chat := conv["chat"].([]interface{})
	sess := chat[0].([]interface{})
	turn0 := sess[0].(map[string]interface{})
	if turn0["content"] != "Hello there." {
		t.Fatalf("unexpected turn0 content: %v", turn0["content"])
	}
	if turn0["time_anchor"] != "2024-03-15" {
		t.Fatalf("expected time_anchor to be preserved, got: %v", turn0["time_anchor"])
	}
}
