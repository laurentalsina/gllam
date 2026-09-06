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
		OriginID: "user-alice",
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

func TestRetrieveHybridNeedleQualifierBoostingAndAbstention(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_qualifier_needle.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-dev", Name: "Caddy Dev Server", Type: memory.NodeTypeService, ContextPrompt: "Dev port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-staging", Name: "Caddy Staging Server", Type: memory.NodeTypeService, ContextPrompt: "Staging port 8081"})

	// 1. Query with "staging" qualifier -> caddy-staging must win due to qualifier boosting
	resultsStaging, err := gllam.RetrieveHybridNeedle(ctx, "port for caddy in staging", []string{"caddy-dev", "caddy-staging"}, "", 5)
	if err != nil {
		t.Fatalf("RetrieveHybridNeedle for staging failed: %v", err)
	}
	if len(resultsStaging) == 0 {
		t.Fatalf("Expected results for staging query")
	}
	if resultsStaging[0].Node.ID != "caddy-staging" {
		t.Errorf("Expected caddy-staging to rank top for staging query, got %s", resultsStaging[0].Node.ID)
	}

	// 2. Query for absent fact -> must return empty list (abstention trigger)
	resultsAbsent, err := gllam.RetrieveHybridNeedle(ctx, "quantum key rotation password", []string{"nonexistent-node"}, "", 5)
	if err != nil {
		t.Fatalf("RetrieveHybridNeedle for absent query failed: %v", err)
	}
	if len(resultsAbsent) != 0 {
		t.Errorf("Expected 0 results for absent needle, got %d results", len(resultsAbsent))
	}
}

func TestContextSiloIsolation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_silo_isolation.db")

	mockEmbedder := &MockVersionedEmbedder{Version: "mock-v1"}
	gllam, err := NewGllamEngine(dbPath, mockEmbedder)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed Silo 13 facts
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{
		ID:            "silo_13_alice",
		Name:          "Alice in Silo 13",
		Type:          "human",
		ContextPrompt: "Alice loves green tea",
		ContextSiloID: "13",
	})
	_ = gllam.StoreNodeEmbedding(ctx, "silo_13_alice")

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{
		ID:            "silo_13_bob",
		Name:          "Bob in Silo 13",
		Type:          "human",
		ContextPrompt: "Bob is a software architect",
		ContextSiloID: "13",
	})
	_ = gllam.StoreNodeEmbedding(ctx, "silo_13_bob")

	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:      "silo_13_alice",
		TargetID:      "silo_13_bob",
		Relationship:  "friends_with",
		ContextSiloID: "13",
	})

	// Seed Silo 3 facts
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{
		ID:            "silo_3_alice",
		Name:          "Alice in Silo 3",
		Type:          "human",
		ContextPrompt: "Alice loves black coffee",
		ContextSiloID: "3",
	})
	_ = gllam.StoreNodeEmbedding(ctx, "silo_3_alice")

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{
		ID:            "silo_3_charlie",
		Name:          "Charlie in Silo 3",
		Type:          "human",
		ContextPrompt: "Charlie is a devops engineer",
		ContextSiloID: "3",
	})
	_ = gllam.StoreNodeEmbedding(ctx, "silo_3_charlie")

	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:      "silo_3_alice",
		TargetID:      "silo_3_charlie",
		Relationship:  "works_with",
		ContextSiloID: "3",
	})

	// 0. Vector Search isolation in SearchSimilarNodesInSilo
	sim13, err := gllam.SearchSimilarNodesInSilo(ctx, "Alice", "13", 5)
	if err != nil {
		t.Fatalf("SearchSimilarNodesInSilo 13 failed: %v", err)
	}
	for _, res := range sim13 {
		if res.NodeID != "silo_13_alice" && res.NodeID != "silo_13_bob" {
			t.Fatalf("Vector search contamination! Found node %s in Silo 13 vector search", res.NodeID)
		}
	}

	// 1. RetrieveHybridNeedle with Silo 13 -> Must NOT contain any Silo 3 nodes
	results13, err := gllam.RetrieveHybridNeedleWithSilo(ctx, "Alice", []string{"silo_13_alice", "silo_3_alice"}, "", "13", 10)
	if err != nil {
		t.Fatalf("RetrieveHybridNeedleWithSilo 13 failed: %v", err)
	}
	for _, res := range results13 {
		if res.Node.ContextSiloID != "" && res.Node.ContextSiloID != "13" {
			t.Fatalf("Knowledge contamination! Found node from silo %s in query for silo 13: %s", res.Node.ContextSiloID, res.Node.ID)
		}
	}

	// 2. RetrieveHybridNeedle with Silo 3 -> Must NOT contain any Silo 13 nodes
	results3, err := gllam.RetrieveHybridNeedleWithSilo(ctx, "Alice", []string{"silo_13_alice", "silo_3_alice"}, "", "3", 10)
	if err != nil {
		t.Fatalf("RetrieveHybridNeedleWithSilo 3 failed: %v", err)
	}
	for _, res := range results3 {
		if res.Node.ContextSiloID != "" && res.Node.ContextSiloID != "3" {
			t.Fatalf("Knowledge contamination! Found node from silo %s in query for silo 3: %s", res.Node.ContextSiloID, res.Node.ID)
		}
	}

	// 3. RouteAndAssembleWithSilo for Silo 13
	compiled13, err := gllam.RouteAndAssembleWithSilo(ctx, "Who is friends with Alice?", []string{"silo_13_alice"}, "13")
	if err != nil {
		t.Fatalf("RouteAndAssembleWithSilo 13 failed: %v", err)
	}
	for _, n := range compiled13.SemanticNodes {
		if n.ContextSiloID != "" && n.ContextSiloID != "13" {
			t.Fatalf("RouteAndAssemble contamination! Found node from silo %s in silo 13 compiled context: %s", n.ContextSiloID, n.ID)
		}
	}
	for _, l := range compiled13.SemanticLinks {
		if l.ContextSiloID != "" && l.ContextSiloID != "13" {
			t.Fatalf("RouteAndAssemble contamination! Found link from silo %s in silo 13 compiled context", l.ContextSiloID)
		}
	}
}


