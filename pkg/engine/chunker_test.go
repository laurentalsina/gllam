package engine

import (
	"strings"
	"testing"
)

func TestChunkTranscriptBasic(t *testing.T) {
	shortText := "User (id 1): Hello world!\n\nAssistant (id 2): How can I help you today?"
	chunks := ChunkTranscript(shortText, 6000, 2000)

	if len(chunks) != 1 {
		t.Fatalf("Expected 1 chunk for short text, got %d", len(chunks))
	}
	if chunks[0].Text != shortText {
		t.Errorf("Expected chunk text %q, got %q", shortText, chunks[0].Text)
	}
}

func TestChunkTranscriptOverlappingBoundaries(t *testing.T) {
	// Create a long text with distinct sentences
	var sb strings.Builder
	for i := 1; i <= 100; i++ {
		sb.WriteString(strings.Repeat("Word ", 15))
		sb.WriteString("End of sentence. ")
		if i%5 == 0 {
			sb.WriteString("\n\nUser (turn): New conversation turn starts here.\n\n")
		}
	}

	longText := sb.String()
	chunkSize := 1000
	overlapSize := 300

	chunks := ChunkTranscript(longText, chunkSize, overlapSize)

	if len(chunks) <= 1 {
		t.Fatalf("Expected multiple chunks for long text, got %d", len(chunks))
	}

	for i, chunk := range chunks {
		t.Logf("Chunk %d (len %d): %s...", i+1, len(chunk.Text), chunk.Text[:min(50, len(chunk.Text))])
		
		// Verify no mid-word cuts at boundaries where possible
		if strings.HasPrefix(chunk.Text, "ord ") {
			t.Errorf("Chunk %d started mid-word: %s", i+1, chunk.Text[:20])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
