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

	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "claim-mysql", Relationship: "has_state"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "claim-postgres", Relationship: "has_state"})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "db-main", TargetID: "contr-db", Relationship: "has_unresolved_conflict"})

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
	gllam.SetBitemporalSoftDelete(false)

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
		OriginID:       "source-email-draft",
	})

	// High-trust claim second: Auth Service located_in port-9090 (from Jira PROD-101)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "service-auth",
		TargetID:       "port-9090",
		Relationship:   "located_in",
		OriginID:       "source-jira-101",
	})

	// Verify low-trust claim (port-8080) was automatically deleted!
	var count int
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE source_id = 'service-auth' AND target_id = 'port-8080' AND relationship = 'located_in'").Scan(&count)
	if err != nil || count != 0 {
		t.Errorf("Low-trust claim should be automatically deleted by Epistemic Hierarchy, got err=%v, count=%d", err, count)
	}

	// Verify high-trust claim (port-9090) is active
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE source_id = 'service-auth' AND target_id = 'port-9090' AND relationship = 'located_in'").Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("High-trust claim port-9090 should be active, got count=%d", count)
	}

	// Verify resolves_conflict rationale
	var rationale string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT resolution_rationale FROM semantic_links WHERE source_id = 'port-9090' AND target_id = 'port-8080' AND relationship = 'resolves_conflict'").Scan(&rationale)
	if err != nil || !strings.Contains(rationale, "Automated Epistemic Hierarchy Resolution") {
		t.Errorf("Missing resolution rationale link: %v, rationale=%s", err, rationale)
	}
}

func TestGaugeCompositeTrustWeight(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_gauge_trust.db")

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

	// High-trust individual author: "alice" (+150 individual bonus) + Jira Resolved + Fresh text
	highInput := SourceTrustInput{
		DocumentType: "jira_resolved",
		AuthorID:     "alice",
		AuthorName:   "Alice Smith",
		DocumentText: "Resolved issue PROD-101: Updated service auth to port 9090 following security audit.",
		CreatedAt:    now - 86400, // 1 day ago
	}
	highWeight, err := gllam.GaugeAndUpsertSourceNode(ctx, "source-jira-high", "Jira High Trust", memory.NodeTypeSystem, highInput)
	if err != nil || highWeight < 900 {
		t.Errorf("Expected high trust weight (>=900), got %d, err=%v", highWeight, err)
	}

	// Low-trust individual author: "dave_drafts" (-150 individual penalty) + Draft + Gibberish text + Old (400 days old)
	lowInput := SourceTrustInput{
		DocumentType: "confluence_draft",
		AuthorID:     "dave_drafts",
		AuthorName:   "Dave Miller",
		DocumentText: "asdf kjhgf qwert zxcvb poiuy lkjhg mnbvc 12345 67890 !@#$%^&*()_+",
		CreatedAt:    now - (400 * 86400), // 400 days ago
	}
	lowWeight, err := gllam.GaugeAndUpsertSourceNode(ctx, "source-draft-low", "Draft Low Trust", memory.NodeTypeHuman, lowInput)
	if err != nil || lowWeight > 100 {
		t.Errorf("Expected low trust weight (<=100 due to individual dave_drafts penalty & age), got %d, err=%v", lowWeight, err)
	}

	if highWeight <= lowWeight {
		t.Errorf("High trust weight (%d) must exceed low trust weight (%d)", highWeight, lowWeight)
	}
}


func TestAllowUserGrillingDisabledBenchmarkMode(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_disallow_grilling.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()
	gllam.SetBitemporalSoftDelete(false)

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// Disable user grilling (BEAM benchmark mode)
	gllam.SetAllowUserGrilling(false)

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "source-a", Name: "Source A", Type: memory.NodeTypeHuman, TrustWeight: 500})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "source-b", Name: "Source B", Type: memory.NodeTypeHuman, TrustWeight: 500})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "server-1", Name: "Server 1", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "state-stopped", Name: "Stopped", Type: memory.NodeTypeState})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "state-running", Name: "Running", Type: memory.NodeTypeState})

	// Equal trust weight claims:
	// Claim 1: Server 1 has_state stopped (Source A)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "server-1",
		TargetID:       "state-stopped",
		Relationship:   "has_state",
		OriginID:       "source-a",
	})

	// Claim 2: Server 1 has_state running (Source B)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "server-1",
		TargetID:       "state-running",
		Relationship:   "has_state",
		OriginID:       "source-b",
	})

	// Since AllowUserGrilling = false, Claim 1 (stopped) must be automatically deleted by recency preference,
	// and Claim 2 (running) must be active WITHOUT creating a contradiction node!
	var count int
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE source_id = 'server-1' AND target_id = 'state-stopped' AND relationship = 'has_state'").Scan(&count)
	if err != nil || count != 0 {
		t.Errorf("Older equal-trust claim should be automatically deleted when AllowUserGrilling=false, got err=%v, count=%d", err, count)
	}

	err = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_links WHERE source_id = 'server-1' AND target_id = 'state-running' AND relationship = 'has_state'").Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("Newer equal-trust claim state-running should be active, got count=%d", count)
	}

	// Verify no contradiction node was created
	_ = gllam.dbRO.QueryRowContext(ctx, "SELECT COUNT(*) FROM semantic_nodes WHERE type = 'contradiction'").Scan(&count)
	if count != 0 {
		t.Errorf("No contradiction nodes should be created when AllowUserGrilling=false, got count=%d", count)
	}
}



