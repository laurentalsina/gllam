package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestMultiHopPathFinder(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_multihop.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed multi-session facts:
	// Session 1: Alice has_allergy peanuts
	// Session 4: Thai-Cooking-Class serves peanuts
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "session-1", Name: "Session 1", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "session-4", Name: "Session 4", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-alice", Name: "Alice", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "peanuts", Name: "Peanuts", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "thai-class", Name: "Thai Cooking Class", Type: memory.NodeTypeEvent})


	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "user-alice", TargetID: "peanuts", Relationship: "has_allergy", OriginSourceID: "session-1"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "thai-class", TargetID: "peanuts", Relationship: "serves_ingredient", OriginSourceID: "session-4"})

	// 1. Multi-hop path from user-alice
	paths, err := gllam.FindMultiHopPath(ctx, []string{"user-alice"}, 2)
	if err != nil {
		t.Fatalf("FindMultiHopPath failed: %v", err)
	}

	if len(paths) == 0 || len(paths[0].Nodes) < 3 {
		t.Fatalf("Expected transitive multi-hop path connecting user-alice -> peanuts -> thai-class, got %v", paths)
	}

	// 2. Spatial Containment: Bob lives in Kyoto, Kyoto located_in Japan
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "bob", Name: "Bob", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "kyoto", Name: "Kyoto", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "japan", Name: "Japan", Type: memory.NodeTypeEntity})

	nodes := []memory.SemanticNode{
		{ID: "bob", Name: "Bob"},
		{ID: "kyoto", Name: "Kyoto"},
		{ID: "japan", Name: "Japan"},
	}
	links := []memory.SemanticLink{
		{SourceID: "bob", TargetID: "kyoto", Relationship: "lives_in"},
		{SourceID: "kyoto", TargetID: "japan", Relationship: "located_in"},
	}

	locations := ResolveSpatialContainment(nodes, links, "bob")
	if len(locations) != 2 || locations[0] != "kyoto" || locations[1] != "japan" {
		t.Errorf("Expected spatial containment path [kyoto, japan], got %v", locations)
	}

	// 3. Quantitative Constraints: Budget = 1000, Spent = 600, Proposed = 500 -> Violation
	quantLinks := []memory.SemanticLink{
		{SourceID: "user-alice", TargetID: "budget-1000", Relationship: "has_budget", Caveats: "1000.0"},
		{SourceID: "user-alice", TargetID: "item-laptop", Relationship: "spent", Caveats: "600.0"},
	}

	resViolation := EvaluateQuantitativeConstraints(nodes, quantLinks, 500.0)
	if resViolation.IsAffordable {
		t.Errorf("Expected 500.0 proposed cost to fail (600+500 > 1000)")
	}

	resAffordable := EvaluateQuantitativeConstraints(nodes, quantLinks, 300.0)
	if !resAffordable.IsAffordable {
		t.Errorf("Expected 300.0 proposed cost to pass (600+300 <= 1000)")
	}
}
