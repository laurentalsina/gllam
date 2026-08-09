package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


func TestResolveContradiction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_resolve_contr.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "claim-mysql", Name: "DB is MySQL", Type: memory.NodeTypeState})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "claim-postgres", Name: "DB is Postgres", Type: memory.NodeTypeState})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "contr-db", Name: "DB Engine Contradiction", Type: memory.NodeTypeContradiction})

	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "claim-mysql", Relationship: "has_state", ValidFrom: "1000"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "claim-postgres", Relationship: "has_state", ValidFrom: "2000"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "contr-db", Relationship: "has_unresolved_conflict", ValidFrom: "2000"})

	// Resolve contradiction: Postgres wins over MySQL
	rationale := "User confirmed Postgres is active DB engine; MySQL was legacy"
	if err := gllam.ResolveContradiction(ctx, "contr-db", "claim-postgres", "claim-mysql", rationale); err != nil {
		t.Fatalf("ResolveContradiction failed: %v", err)
	}

	// Verify losing claim links expired and resolves_conflict link created
	queryTS := time.Now().Unix() + 10
	links, err := gllam.GetActiveLinksAtTime(ctx, queryTS)
	if err != nil {
		t.Fatalf("GetActiveLinksAtTime failed: %v", err)
	}


	foundLosing := false
	foundResolvesLink := false
	for _, l := range links {
		if l.TargetID == "claim-mysql" && l.Relationship == "has_state" {
			foundLosing = true
		}
		if l.SourceID == "claim-postgres" && l.TargetID == "claim-mysql" && l.Relationship == "resolves_conflict" {
			foundResolvesLink = true
			if l.ResolutionRationale != rationale {
				t.Errorf("Expected rationale %q, got %q", rationale, l.ResolutionRationale)
			}
		}
	}

	if foundLosing {
		t.Errorf("Expected claim-mysql link to be expired/inactive")
	}
	if !foundResolvesLink {
		t.Errorf("Expected resolves_conflict link between claim-postgres and claim-mysql")
	}
}

func TestDetectFallacySubversion(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "claim-delete-db", Name: "Must delete DB", Type: memory.NodeTypeConstraint},
		{ID: "fallacy_false_dilemma_1", Name: "False Dichotomy", Type: memory.NodeTypeFallacy, ContextPrompt: "Asserts binary choice between DB deletion and deploy failure"},
	}

	links := []memory.SemanticLink{
		{
			SourceID:     "claim-delete-db",
			TargetID:     "fallacy_false_dilemma_1",
			Relationship: "exhibits_fallacy",
		},
	}

	diag := DetectFallacySubversion(links, nodes)
	if diag == "" {
		t.Fatalf("Expected fallacy diagnostic, got empty string")
	}

	if !strings.Contains(diag, "BYZANTINE FALLACY DETECTED") {
		t.Errorf("Diagnostic missing header: %s", diag)
	}
	if !strings.Contains(diag, "claim-delete-db") || !strings.Contains(diag, "fallacy_false_dilemma_1") {
		t.Errorf("Diagnostic missing node IDs: %s", diag)
	}
	if !strings.Contains(diag, "Isolated binary constraint") {
		t.Errorf("Diagnostic missing guard action: %s", diag)
	}
}

func TestEpistemicHierarchySourceTrustWeighting(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_trust_weight.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Source nodes with different trust weights
	// High trust: Jira Resolved Ticket (TrustWeight = 900)
	// Low trust: Email Draft (TrustWeight = 100)
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "source-jira-101", Name: "Jira PROD-101 (Resolved)", Type: memory.NodeTypeSystem, TrustWeight: 900})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "source-email-draft", Name: "Email Draft", Type: memory.NodeTypeHuman, TrustWeight: 100})

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "service-auth", Name: "Auth Service", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-9090", Name: "Port 9090", Type: memory.NodeTypeEntity})

	// Low-trust claim first: Auth Service located_in port-8080 (from Email Draft)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "service-auth",
		TargetID:       "port-8080",
		Relationship:   "located_in",
		OriginSourceID: "source-email-draft",
	})

	// High-trust claim second: Auth Service located_in port-9090 (from Jira PROD-101)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "service-auth",
		TargetID:       "port-9090",
		Relationship:   "located_in",
		OriginSourceID: "source-jira-101",
	})

	// Verify low-trust claim (port-8080) was automatically expired without user grilling!
	var expiredUntil string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT valid_until FROM semantic_links WHERE source_id = 'service-auth' AND target_id = 'port-8080' AND relationship = 'located_in'").Scan(&expiredUntil)
	if err != nil || expiredUntil == "" {
		t.Errorf("Low-trust claim should be automatically expired by Epistemic Hierarchy, got err=%v, valid_until=%s", err, expiredUntil)
	}

	// Verify high-trust claim (port-9090) is active
	var activeTarget string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT target_id FROM semantic_links WHERE source_id = 'service-auth' AND relationship = 'located_in' AND valid_until IS NULL").Scan(&activeTarget)
	if err != nil || activeTarget != "port-9090" {
		t.Errorf("High-trust claim port-9090 should be active, got target=%s", activeTarget)
	}

	// Verify resolves_conflict rationale
	var rationale string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT resolution_rationale FROM semantic_links WHERE source_id = 'port-9090' AND target_id = 'port-8080' AND relationship = 'resolves_conflict'").Scan(&rationale)
	if err != nil || !strings.Contains(rationale, "Automated Epistemic Hierarchy Resolution") {
		t.Errorf("Missing resolution rationale link: %v, rationale=%s", err, rationale)
	}
}

