package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestEventAnchoredStateInvalidationAndDynamicResolution(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_temporal.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Insert nodes first for FK satisfaction
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy", Name: "Caddy Service", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "state-v2-7", Name: "Version 2.7", Type: memory.NodeTypeState})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "state-v2-8", Name: "Version 2.8", Type: memory.NodeTypeState})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "event-deploy-v2-8", Name: "Deploy v2.8", Type: memory.NodeTypeEvent})

	// 1. Add Caddy v2.7 state at T=1000
	link1 := memory.SemanticLink{
		SourceID:     "caddy",
		TargetID:     "state-v2-7",
		Relationship: "has_state",
		ValidFrom:    "1000",
	}
	if err := gllam.AddEdge(ctx, link1); err != nil {
		t.Fatalf("Failed to add link1: %v", err)
	}


	// 2. Add Caddy v2.8 state at T=2000 anchored to event-deploy-v2-8
	link2 := memory.SemanticLink{
		SourceID:         "caddy",
		TargetID:         "state-v2-8",
		Relationship:     "has_state",
		ValidFrom:        "2000",
		TemporalAnchorID: "event-deploy-v2-8",
		TemporalRelation: "after",
	}
	if err := gllam.AddEdge(ctx, link2); err != nil {
		t.Fatalf("Failed to add link2: %v", err)
	}

	// Also insert anchor event link with timestamp 2000
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:     "event-deploy-v2-8",
		TargetID:     "state-v2-8",
		Relationship: "introduced_state",
		ValidFrom:    "2000",
	})

	// Check Trap 9: State v2.7 should be invalidated with valid_until = "2000" and anchor = event-deploy-v2-8
	activeAt1500, err := gllam.GetActiveLinksAtTime(ctx, 1500)
	if err != nil {
		t.Fatalf("GetActiveLinksAtTime(1500) failed: %v", err)
	}
	hasV27 := false
	hasV28 := false
	for _, l := range activeAt1500 {
		if l.TargetID == "state-v2-7" {
			hasV27 = true
		}
		if l.TargetID == "state-v2-8" {
			hasV28 = true
		}
	}

	if !hasV27 {
		t.Errorf("Expected state-v2-7 to be active at T=1500")
	}
	if hasV28 {
		t.Errorf("Did not expect state-v2-8 to be active at T=1500")
	}

	// Check Trap 6: At T=2500, state-v2-7 must be expired and state-v2-8 active
	activeAt2500, err := gllam.GetActiveLinksAtTime(ctx, 2500)
	if err != nil {
		t.Fatalf("GetActiveLinksAtTime(2500) failed: %v", err)
	}
	hasV27At2500 := false
	hasV28At2500 := false
	for _, l := range activeAt2500 {
		if l.TargetID == "state-v2-7" {
			hasV27At2500 = true
		}
		if l.TargetID == "state-v2-8" {
			hasV28At2500 = true
		}
	}

	if hasV27At2500 {
		t.Errorf("Expected state-v2-7 to be expired at T=2500")
	}
	if !hasV28At2500 {
		t.Errorf("Expected state-v2-8 to be active at T=2500")
	}
}
