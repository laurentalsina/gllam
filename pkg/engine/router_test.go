package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestExpandTemporalNeighborsMultiHop(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_router.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()
	nowStr := time.Now().String()

	// Setup 3-hop temporal chain: Event A -> Event B -> Event C
	nodes := []memory.SemanticNode{
		{ID: "event-a", Name: "Database Migration", Type: memory.NodeTypeEvent},
		{ID: "event-b", Name: "Service Deployment", Type: memory.NodeTypeEvent},
		{ID: "event-c", Name: "User Onboarding", Type: memory.NodeTypeEvent},
	}
	for _, n := range nodes {
		_ = gllam.UpsertNode(ctx, n)
	}

	// Link A -> B and B -> C
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:     "event-a",
		TargetID:     "event-b",
		Relationship: "happened_before",
		ValidFrom:    nowStr,
	})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:         "event-b",
		TargetID:         "event-c",
		Relationship:     "happened_before",
		TemporalAnchorID: "event-c",
		TemporalRelation: "before",
		ValidFrom:        nowStr,
	})

	// Seed only Event A
	seeds := []memory.SemanticNode{{ID: "event-a", Name: "Database Migration", Type: memory.NodeTypeEvent}}
	expNodes, expLinks, err := gllam.ExpandTemporalNeighbors(ctx, seeds, nil, 2)
	if err != nil {
		t.Fatalf("ExpandTemporalNeighbors failed: %v", err)
	}

	if len(expNodes) < 3 {
		t.Errorf("Expected at least 3 expanded nodes across 2 hops, got %d", len(expNodes))
	}
	if len(expLinks) < 2 {
		t.Errorf("Expected at least 2 expanded links across 2 hops, got %d", len(expLinks))
	}
}

func TestExtractPDDLGoalEntityDisambiguation(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "event-db-migration-v2", Name: "database migration", Type: memory.NodeTypeEvent},
		{ID: "rel-caddy-deploy-prod", Name: "deployment", Type: memory.NodeTypeEvent},
	}

	prompt := "Did the database migration happen before the deployment?"
	goal := ExtractPDDLGoal(prompt, nodes, nil)

	expected := "(and (verified_sequence event_db_migration_v2 rel_caddy_deploy_prod))"
	if goal != expected {
		t.Errorf("Expected goal %q, got %q", expected, goal)
	}
}

func TestDetectTemporalCycles(t *testing.T) {
	cyclicLinks := []memory.SemanticLink{
		{SourceID: "event-postgres", TargetID: "event-redis", Relationship: "happened_before"},
		{SourceID: "event-redis", TargetID: "event-postgres", Relationship: "happened_before"},
	}

	res := DetectTemporalCycles(cyclicLinks)
	if !res.HasCycle {
		t.Errorf("Expected cycle detection to return true for cyclic links")
	}
	if len(res.CycleNodes) < 2 {
		t.Errorf("Expected cycle nodes to be populated, got %v", res.CycleNodes)
	}

	acyclicLinks := []memory.SemanticLink{
		{SourceID: "event-a", TargetID: "event-b", Relationship: "happened_before"},
		{SourceID: "event-b", TargetID: "event-c", Relationship: "happened_before"},
	}
	resAcyclic := DetectTemporalCycles(acyclicLinks)
	if resAcyclic.HasCycle {
		t.Errorf("Expected cycle detection to return false for acyclic links")
	}
}

