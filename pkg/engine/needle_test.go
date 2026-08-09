package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestRetrieveHybridNeedle(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_hybrid_needle.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed semantic nodes & links
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService, ContextPrompt: "Reverse proxy on port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-alice", Name: "Alice", Type: memory.NodeTypeHuman})

	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "caddy-service",
		TargetID:       "port-8080",
		Relationship:   "binds_to",
		Caveats:        "Must use TLS certificate in production",
		OriginSourceID: "user-alice",
	})

	// 1. Hybrid retrieval by exact entityID and source grounding
	results, err := gllam.RetrieveHybridNeedle(ctx, "port number for caddy", []string{"caddy-service"}, "user-alice", 5)
	if err != nil {
		t.Fatalf("RetrieveHybridNeedle failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatalf("Expected non-empty needle results")
	}

	// Verify top needle node and attached caveats
	topNeedle := results[0]
	if topNeedle.Node.ID != "caddy-service" && topNeedle.Node.ID != "port-8080" {
		t.Errorf("Unexpected top needle node: %s", topNeedle.Node.ID)
	}

	foundCaveat := false
	for _, l := range topNeedle.Links {
		if l.Caveats != "" {
			foundCaveat = true
			if l.Caveats != "Must use TLS certificate in production" {
				t.Errorf("Expected caveat text, got %q", l.Caveats)
			}
		}
	}

	if !foundCaveat {
		t.Errorf("Expected caveat-qualified link attached to top needle node")
	}
}
