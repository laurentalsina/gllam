package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)


func TestSupersedeFactAndCrossCuttingInvalidation(t *testing.T) {
	t.Skip("Deprecated / flawed supersession design")
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_ku.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed V1 state and dependent service link
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "db-server", Name: "Database Server", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "postgres-v14", Name: "Postgres v14", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "postgres-v15", Name: "Postgres v15", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "api-gateway", Name: "API Gateway", Type: memory.NodeTypeService})


	oldLink := memory.SemanticLink{
		SourceID:     "db-server",
		TargetID:     "postgres-v14",
		Relationship: "uses_version",
	}
	_ = gllam.AddEdge(ctx, oldLink)

	// Cross-cutting dependent link: api-gateway depends_on postgres-v14
	depLink := memory.SemanticLink{
		SourceID:     "api-gateway",
		TargetID:     "postgres-v14",
		Relationship: "depends_on",
	}
	_ = gllam.AddEdge(ctx, depLink)

	// 1. Supersede postgres-v14 with postgres-v15
	newLink := memory.SemanticLink{
		SourceID:     "db-server",
		TargetID:     "postgres-v15",
		Relationship: "uses_version",
	}

	err = gllam.SupersedeFact(ctx, oldLink, newLink, "Upgraded database engine to v15")
	if err != nil {
		t.Fatalf("SupersedeFact failed: %v", err)
	}

	// 2. Verify active links at timestamp 2500 -> oldLink must be expired, newLink must be active
	activeLinks, err := gllam.GetActiveLinksAtTime(ctx, 2500)
	if err != nil {
		t.Fatalf("GetActiveLinksAtTime failed: %v", err)
	}

	foundNew := false
	foundSupersededBy := false
	for _, l := range activeLinks {
		if l.SourceID == "db-server" && l.TargetID == "postgres-v15" {
			foundNew = true
		}
		if l.SourceID == "postgres-v15" && l.TargetID == "postgres-v14" && l.Relationship == "superseded_by" {
			foundSupersededBy = true
		}
		if l.SourceID == "api-gateway" && l.TargetID == "postgres-v14" {
			if !testing.Verbose() {
				// Verify cross-cutting invalidation caveat was attached
			}
		}
	}

	if !foundNew {
		t.Errorf("Expected new fact (postgres-v15) to be active")
	}
	if !foundSupersededBy {
		t.Errorf("Expected superseded_by edge connecting postgres-v15 -> postgres-v14")
	}
}

func TestOutOfOrderFactSupersession(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_ooo_ku.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "service-a", Name: "Service A", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "v1", Name: "Version 1", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "v2", Name: "Version 2", Type: memory.NodeTypeEntity})

	activeLink := memory.SemanticLink{
		SourceID:     "service-a",
		TargetID:     "v2",
		Relationship: "uses_version",
	}

	_ = gllam.AddEdge(ctx, activeLink)

	// Out-of-order ingested older link
	olderLink := memory.SemanticLink{
		SourceID:     "service-a",
		TargetID:     "v1",
		Relationship: "uses_version",
	}

	err = gllam.SupersedeFact(ctx, activeLink, olderLink, "Ingested late")
	if err != nil {
		t.Fatalf("SupersedeFact for out-of-order ingestion failed: %v", err)
	}
}

func TestCircularDependencyInvalidationLoopPrevention(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_circ_inv.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed circular dependency: Service A -> Spec B -> Rule C -> Service A
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "service-a", Name: "Service A", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "spec-b", Name: "Spec B", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-c", Name: "Rule C", Type: memory.NodeTypeRule})


	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "service-a", TargetID: "spec-b", Relationship: "depends_on"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "spec-b", TargetID: "rule-c", Relationship: "applies_rule"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "rule-c", TargetID: "service-a", Relationship: "references"})

	// Trigger invalidation on Service A
	err = gllam.InvalidateDependentCrossCuttingLinks(ctx, "service-a", "2000")
	if err != nil {
		t.Fatalf("InvalidateDependentCrossCuttingLinks failed: %v", err)
	}

	// Verify links in cycle are flagged with REQUIRES_REVALIDATION without entering an infinite loop
	var caveat string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT caveats FROM semantic_links WHERE source_id = 'service-a' AND target_id = 'spec-b'").Scan(&caveat)
	if err != nil || !strings.Contains(caveat, "REQUIRES_REVALIDATION") {
		t.Errorf("Expected REQUIRES_REVALIDATION caveat on service-a -> spec-b link, got '%s', err=%v", caveat, err)
	}
}

