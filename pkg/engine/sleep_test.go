package engine

import (
	"context"
	"path/filepath"
	"testing"

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

	// 1. Insert active nodes and links
	node1 := memory.SemanticNode{ID: "node-1", Name: "PostgreSQL 15", Type: memory.NodeTypeEntity, TaxonomyPath: "/Engineering/Infrastructure/Databases/Relational"}
	node2 := memory.SemanticNode{ID: "node-2", Name: "Redis Cache", Type: memory.NodeTypeEntity, TaxonomyPath: "/Engineering/Infrastructure/Databases/NoSQL"}
	node3 := memory.SemanticNode{ID: "node-3", Name: "Caddy Server", Type: memory.NodeTypeService, TaxonomyPath: "/Engineering/Infrastructure/Deployment"}
	_ = gllam.UpsertNode(ctx, node1)
	_ = gllam.UpsertNode(ctx, node2)
	_ = gllam.UpsertNode(ctx, node3)

	// Active link
	activeLink := memory.SemanticLink{SourceID: "node-1", TargetID: "node-2", Relationship: "depends_on", Caveats: "Production replication"}
	_ = gllam.AddEdge(ctx, activeLink)

	// Previously-stale link (temporal fields removed; stored as a regular link)
	testLink := memory.SemanticLink{SourceID: "node-1", TargetID: "node-3", Relationship: "temporary_test", Caveats: "Deprecated test"}
	_ = gllam.AddEdge(ctx, testLink)

	// 2. Trigger Memory Sleep Cycle
	sleepReport, err := gllam.EnterMemorySleepCycle(ctx, 5)
	if err != nil {
		t.Fatalf("Failed to enter memory sleep cycle: %v", err)
	}

	if sleepReport.PrunedStaleLinksCount != 0 {
		t.Errorf("Expected 0 deleted stale links (historical facts preserved forever), got %d", sleepReport.PrunedStaleLinksCount)
	}

	if len(sleepReport.SimulatedTraceTests) != 5 {
		t.Errorf("Expected 5 synthetic random trace test scenarios, got %d", len(sleepReport.SimulatedTraceTests))
	}

	if sleepReport.MemoryClarityScore <= 0 || sleepReport.MemoryConsistencyScore <= 0 {
		t.Errorf("Expected positive clarity and consistency scores, got clarity=%f, consistency=%f", sleepReport.MemoryClarityScore, sleepReport.MemoryConsistencyScore)
	}

	// 3. Verify link is present in database
	var count int
	_ = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE relationship = 'temporary_test'").Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 link with relationship 'temporary_test', got %d", count)
	}
}

func TestCalculateTraceClarityMetrics(t *testing.T) {
	// 1. Taxonomy path overlap tests
	overlapSame := CalculateTaxonomyPathOverlap("/Engineering/Infrastructure/Databases", "/Engineering/Infrastructure/Databases")
	if overlapSame != 1.0 {
		t.Errorf("Expected 1.0 overlap for identical taxonomy paths, got %f", overlapSame)
	}

	overlapPartial := CalculateTaxonomyPathOverlap("/Engineering/Infrastructure/Databases", "/Engineering/Infrastructure/Deployment")
	if overlapPartial < 0.50 || overlapPartial > 0.70 {
		t.Errorf("Expected ~0.66 overlap for shared /Engineering/Infrastructure branch, got %f", overlapPartial)
	}

	overlapDisjoint := CalculateTaxonomyPathOverlap("/Engineering/Services", "/Sales/Tools")
	if overlapDisjoint != 0.0 {
		t.Errorf("Expected 0.0 overlap for disjoint taxonomy branches, got %f", overlapDisjoint)
	}

	// 2. Trace clarity calculations
	n1 := memory.SemanticNode{ID: "n1", Name: "Postgres", TaxonomyPath: "/Engineering/Infrastructure/Databases"}
	n2 := memory.SemanticNode{ID: "n2", Name: "Redis", TaxonomyPath: "/Engineering/Infrastructure/Databases"}
	n3 := memory.SemanticNode{ID: "n3", Name: "Salesforce", TaxonomyPath: "/Sales/Tools"}

	gllam, _ := NewGllamEngine(filepath.Join(t.TempDir(), "clarity.db"), nil)
	defer gllam.Close()

	// Path with 1 hop and 0 caveats
	singleHopPaths := []MultiHopPath{
		{
			HopCount: 1,
			Links: []memory.SemanticLink{
				{SourceID: "n1", TargetID: "n2", Relationship: "connects_to"},
			},
		},
	}
	clarityDirect, isConsistent := gllam.CalculateTraceClarity(context.Background(), n1, n2, singleHopPaths)
	if !isConsistent || clarityDirect != 1.0 {
		t.Errorf("Expected clarity 1.0 for direct uncaveated link, got clarity=%f, consistent=%v", clarityDirect, isConsistent)
	}

	// Path with caveat & conflict penalty
	caveatPaths := []MultiHopPath{
		{
			HopCount: 1,
			Links: []memory.SemanticLink{
				{SourceID: "n1", TargetID: "n2", Relationship: "resolves_conflict", Caveats: "High latency spike"},
			},
		},
	}
	clarityCaveat, _ := gllam.CalculateTraceClarity(context.Background(), n1, n2, caveatPaths)
	if clarityCaveat >= clarityDirect {
		t.Errorf("Expected lower clarity for path with caveats and conflict resolution, got %f vs %f", clarityCaveat, clarityDirect)
	}

	// Disjoint taxonomy clarity
	clarityDisjoint, isConsistentDisjoint := gllam.CalculateTraceClarity(context.Background(), n1, n3, nil)
	if isConsistentDisjoint || clarityDisjoint != 0.20 {
		t.Errorf("Expected clarity 0.20 and inconsistent for disjoint domains, got clarity=%f, consistent=%v", clarityDisjoint, isConsistentDisjoint)
	}
}
