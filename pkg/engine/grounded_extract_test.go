package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestGroundedEpisodicExtract(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_grounded.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Create an episodic transcript that spans across multiple chunks (>5000 chars)
	chunk1Text := "Turn 1 (User): We have scheduled the initial product review on April 10 at 10 AM. Everything is set.\n\n" + strings.Repeat("Padding dialogue text for turn 2.\n", 160)
	chunk2Text := "Turn 40 (User): I've accepted Leslie's introduction offer and have a Zoom call with the creative director on April 21 at 3 PM.\n\nTurn 42 (User): I was thinking about moving to April 22 but there is a schedule conflict.\n\n" + strings.Repeat("Padding dialogue text for turn 43.\n", 80)
	fullTranscript := chunk1Text + "\n\n" + chunk2Text

	sessID := "beam-100k-8-session1"
	ep := memory.EpisodicSummary{
		ID:          sessID,
		SessionID:   sessID,
		SummaryText: fullTranscript,
		CreatedAt:   time.Now().UTC(),
	}
	if err := gllam.SaveEpisodicSummary(ctx, ep); err != nil {
		t.Fatalf("SaveEpisodicSummary failed: %v", err)
	}

	// Insert semantic node and link referencing chunk-2
	node := memory.SemanticNode{
		ID:          "beam-100k-8-zoom_call_director",
		Name:        "Zoom Call Director",
		Type:        "event",
		CreatedFrom: "file://corpus.jsonl beam-100k-8-session1#chunk-2",
	}
	if err := gllam.UpsertNode(ctx, node); err != nil {
		t.Fatalf("UpsertNode failed: %v", err)
	}

	link := memory.SemanticLink{
		SourceID:     "beam-100k-8-user",
		TargetID:     "beam-100k-8-zoom_call_director",
		Relationship: "scheduled_on_april_21",
		Modality:     "epistemic",
		OriginID:     "beam-100k-8-user",
		CreatedFrom:  "beam-100k-8-session1#chunk-2",
	}
	if err := gllam.AddEdge(ctx, link); err != nil {
		t.Fatalf("AddEdge failed: %v", err)
	}

	// Route and Assemble
	promptCtx := "[Conversation 8 context] What date is the Zoom call with the creative director?"
	compiled, err := gllam.RouteAndAssemble(ctx, promptCtx, []string{"beam-100k-8-zoom_call_director"})
	if err != nil {
		t.Fatalf("RouteAndAssemble failed: %v", err)
	}

	if len(compiled.Episodic) == 0 {
		t.Fatalf("Expected grounded episodic evidence, got 0 episodes")
	}

	groundedSnippet := compiled.Episodic[0]
	if !strings.Contains(groundedSnippet.ID, "chunk-2") {
		t.Errorf("Expected reference ID to contain chunk-2, got %s", groundedSnippet.ID)
	}
	if !strings.Contains(groundedSnippet.SummaryText, "April 21 at 3 PM") {
		t.Errorf("Expected grounded snippet to contain 'April 21 at 3 PM', got: %s", groundedSnippet.SummaryText)
	}

	// Check FormatSystemPrompt
	formatted := FormatSystemPrompt(compiled)
	if !strings.Contains(formatted, "## Grounded Dialogue & Episodic Evidence") {
		t.Errorf("Expected 'Grounded Dialogue & Episodic Evidence' header in prompt, got:\n%s", formatted)
	}
	if !strings.Contains(formatted, "April 21 at 3 PM") {
		t.Errorf("Expected prompt to contain grounded dialogue with April 21 at 3 PM, got:\n%s", formatted)
	}
}
