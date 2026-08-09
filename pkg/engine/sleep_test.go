package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


func TestMemoryMaintenanceCycleAndRandomTraceTests(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_sleep.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()
	now := time.Now().Unix()

	// 1. Insert active & stale nodes and links
	node1 := memory.SemanticNode{ID: "node-1", Name: "PostgreSQL 15", Type: memory.NodeTypeEntity, TaxonomyPath: "/Engineering/Infrastructure/Databases/Relational"}
	node2 := memory.SemanticNode{ID: "node-2", Name: "Redis Cache", Type: memory.NodeTypeEntity, TaxonomyPath: "/Engineering/Infrastructure/Databases/NoSQL"}
	node3 := memory.SemanticNode{ID: "node-3", Name: "Caddy Server", Type: memory.NodeTypeService, TaxonomyPath: "/Engineering/Infrastructure/Deployment"}
	_ = gllam.UpsertNode(ctx, node1)
	_ = gllam.UpsertNode(ctx, node2)
	_ = gllam.UpsertNode(ctx, node3)

	// Active link
	activeLink := memory.SemanticLink{SourceID: "node-1", TargetID: "node-2", Relationship: "depends_on", Caveats: "Production replication"}
	_ = gllam.AddEdge(ctx, activeLink)

	// Stale link (expired 1000s ago)
	staleUntil := fmt.Sprintf("%d", now-1000)
	staleLink := memory.SemanticLink{SourceID: "node-1", TargetID: "node-3", Relationship: "temporary_test", Caveats: "Deprecated test", ValidUntil: &staleUntil}
	_ = gllam.AddEdge(ctx, staleLink)

	// 2. Trigger Memory Sleep Cycle
	sleepReport, err := gllam.EnterMemorySleepCycle(ctx, 5)
	if err != nil {
		t.Fatalf("Failed to enter memory sleep cycle: %v", err)
	}

	if sleepReport.PrunedStaleLinksCount != 1 {
		t.Errorf("Expected 1 pruned stale link, got %d", sleepReport.PrunedStaleLinksCount)
	}

	if len(sleepReport.SimulatedTraceTests) != 5 {
		t.Errorf("Expected 5 synthetic random trace test scenarios, got %d", len(sleepReport.SimulatedTraceTests))
	}

	if sleepReport.MemoryClarityScore <= 0 || sleepReport.MemoryConsistencyScore <= 0 {
		t.Errorf("Expected positive clarity and consistency scores, got clarity=%f, consistency=%f", sleepReport.MemoryClarityScore, sleepReport.MemoryConsistencyScore)
	}


	// 3. Verify stale link was completely pruned from database
	var count int
	_ = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE relationship = 'temporary_test'").Scan(&count)
	if count != 0 {
		t.Errorf("Expected 0 remaining stale links in SQLite, got %d", count)
	}
}
