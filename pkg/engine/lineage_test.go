package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/config"
	"github.com/laurentalsina/gllam/pkg/memory"
)



func TestStrictInformationLineage(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_lineage.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService, ContextPrompt: "Reverse proxy on port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to"})

	lin1 := memory.DocumentLineage{
		ID:            "lin-jira-101",
		NodeID:        "caddy-service",
		SourceURI:     "https://jira.internal.company.com/browse/PROD-101",
		DocumentTitle: "Jira PROD-101: Configure Caddy Web Server",
		SourceType:    "jira",
		LineNumber:    42,
	}
	lin2 := memory.DocumentLineage{
		ID:            "lin-pr-55",
		NodeID:        "port-8080",
		SourceURI:     "https://github.company.com/infra/config/pull/55",
		DocumentTitle: "PR #55: Bind Caddy to Port 8080",
		SourceType:    "pull_request",
		LineNumber:    12,
	}

	if err := gllam.AddDocumentLineage(ctx, lin1); err != nil {
		t.Fatalf("AddDocumentLineage lin1 failed: %v", err)
	}
	if err := gllam.AddDocumentLineage(ctx, lin2); err != nil {
		t.Fatalf("AddDocumentLineage lin2 failed: %v", err)
	}

	// Add multi-author version edit history
	ver1 := memory.DocumentVersion{
		ID:            "ver-lin1-v1",
		LineageID:     "lin-jira-101",
		VersionNumber: 1,
		AuthorID:      "alice",
		AuthorName:    "Alice Smith",
		ChangeSummary: "Initial draft specification",
		StartLine:     1,
		EndLine:       30,
	}
	ver2 := memory.DocumentVersion{
		ID:            "ver-lin1-v2",
		LineageID:     "lin-jira-101",
		VersionNumber: 2,
		AuthorID:      "carol_lead",
		AuthorName:    "Carol Tech Lead",
		ChangeSummary: "Approved production auth config on port 8080",
		StartLine:     31,
		EndLine:       50,
	}

	if err := gllam.AddDocumentVersion(ctx, ver1); err != nil {
		t.Fatalf("AddDocumentVersion ver1 failed: %v", err)
	}
	if err := gllam.AddDocumentVersion(ctx, ver2); err != nil {
		t.Fatalf("AddDocumentVersion ver2 failed: %v", err)
	}

	// Fetch lineage for node IDs
	lineages, err := gllam.GetDocumentLineageForNodes(ctx, []string{"caddy-service", "port-8080"})
	if err != nil || len(lineages) != 2 {
		t.Fatalf("GetDocumentLineageForNodes expected 2 records, got len=%d, err=%v", len(lineages), err)
	}

	// RouteAndAssemble and FormatSystemPrompt
	compiledCtx, err := gllam.RouteAndAssemble(ctx, "caddy web server port", []string{"caddy-service"})
	if err != nil {
		t.Fatalf("RouteAndAssemble failed: %v", err)
	}

	prompt := FormatSystemPrompt(compiledCtx)

	if !strings.Contains(prompt, "Strict Source Lineage Citations") {
		t.Errorf("Formatted prompt missing Strict Source Lineage Citations header: %s", prompt)
	}
	if !strings.Contains(prompt, "https://jira.internal.company.com/browse/PROD-101") || !strings.Contains(prompt, "https://github.company.com/infra/config/pull/55") {
		t.Errorf("Formatted prompt missing source URIs: %s", prompt)
	}
	if !strings.Contains(prompt, "Synthetic Revision Timeline") {
		t.Errorf("Formatted prompt missing Synthetic Revision Timeline: %s", prompt)
	}
	if !strings.Contains(prompt, "Alice Smith (alice)") || !strings.Contains(prompt, "Carol Tech Lead (carol_lead)") {
		t.Errorf("Formatted prompt missing multi-author version handles: %s", prompt)
	}
}

func TestIngestionSteeringDirectives(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_steering.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}


	// 1. Confluence strategy
	confStrat := gllam.DetermineDocumentIngestionStrategy("confluence")
	if !confStrat.TrackRevisionHistory || !confStrat.CompactAuthorEpochs {
		t.Errorf("Expected Confluence strategy to track revision history and compact author epochs, got %+v", confStrat)
	}

	// 2. Jira strategy
	jiraStrat := gllam.DetermineDocumentIngestionStrategy("jira")
	if !jiraStrat.TrackCommentHistory || !jiraStrat.TrackStatusTransitions {
		t.Errorf("Expected Jira strategy to track comments and status transitions, got %+v", jiraStrat)
	}

	// 3. Git strategy
	gitStrat := gllam.DetermineDocumentIngestionStrategy("git")
	if !gitStrat.TrackBranchMerges || !gitStrat.TrackRevisionHistory {
		t.Errorf("Expected Git strategy to track branch merges and revision history, got %+v", gitStrat)
	}

	// 4. Register custom document type at runtime (e.g. "notion_workspace" with 650 trust baseline)
	gllam.RegisterCustomDocumentTypeRule(config.CustomDocumentTypeRule{
		TypeName:            "notion_workspace",
		BaselineTrustWeight: 650,
		IngestionStrategy: config.IngestionStrategy{
			TrackRevisionHistory: true,
			CompactAuthorEpochs:  true,
		},
	})

	notionStrat := gllam.DetermineDocumentIngestionStrategy("notion_workspace")
	if !notionStrat.TrackRevisionHistory {
		t.Errorf("Expected custom notion_workspace strategy to track revision history, got %+v", notionStrat)
	}

	ctx := context.Background()
	weight, err := gllam.GaugeAndUpsertSourceNode(ctx, "notion-node-1", "Notion Spec", memory.NodeTypeEntity, SourceTrustInput{
		DocumentType: "notion_workspace",
	})
	if err != nil || weight < 600 {
		t.Errorf("Expected custom notion_workspace trust weight (>=600), got %d, err=%v", weight, err)
	}
}

func TestAttributeContainerEntryToSource(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_container_attr.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	// Register high trust for Alice (+150) and low trust for Dave (-150)
	gllam.SetIndividualSourceTrustWeight("alice", 150)
	gllam.SetIndividualSourceTrustWeight("dave", -150)

	ctx := context.Background()
	now := time.Now().Unix()

	// Parse Comment 1 by Alice in Jira Ticket PROD-101
	aliceSourceID, aliceWeight, err := gllam.AttributeContainerEntryToSource(ctx, "jira", "alice", "Alice Smith", "Comment 1: Database is PostgreSQL 15 on port 5432.", now)
	if err != nil || aliceWeight < 800 {
		t.Errorf("Expected high trust for Alice comment (>=800), got weight=%d, err=%v", aliceWeight, err)
	}


	// Parse Comment 2 by Dave in same Jira Ticket PROD-101
	daveSourceID, daveWeight, err := gllam.AttributeContainerEntryToSource(ctx, "jira", "dave", "Dave Miller", "Comment 2: DB is MySQL on port 3306.", now)
	if err != nil || daveWeight > 650 {
		t.Errorf("Expected lower trust for Dave comment (<=650), got weight=%d, err=%v", daveWeight, err)
	}

	if aliceWeight <= daveWeight {
		t.Errorf("Alice's comment trust weight (%d) must exceed Dave's comment trust weight (%d) within the same Jira container", aliceWeight, daveWeight)
	}

	if aliceSourceID != "src-alice" || daveSourceID != "src-dave" {
		t.Errorf("Unexpected source IDs: alice=%s, dave=%s", aliceSourceID, daveSourceID)
	}
}

func TestRepositoryContextDirectives(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_repo_directives.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	// 1. Built-in Jira repository context building
	jiraCtx := gllam.BuildRepositoryEntityContext("jira", map[string]string{
		"key":        "PROD-101",
		"type":       "Bug",
		"status":     "Resolved",
		"resolution": "Fixed",
	})

	if !strings.Contains(jiraCtx, "Jira Issue: PROD-101") || !strings.Contains(jiraCtx, "Status: Resolved") {
		t.Errorf("Unexpected Jira entity context: %s", jiraCtx)
	}

	// 2. Register custom repository context directive (e.g. "sharepoint")
	gllam.RegisterRepositoryContextDirective(config.RepositoryContextDirective{
		RepositoryType:   "sharepoint",
		ExtractionPrompt: "Extract SharePoint site URL, document library, and version label into entity context profiles.",
		ContextTemplate:  "SharePoint Doc: {{doc_name}}\nLibrary: {{library}}\nSite: {{site}}",
	})

	spCtx := gllam.BuildRepositoryEntityContext("sharepoint", map[string]string{
		"doc_name": "Architecture_V2.pdf",
		"library":  "SystemDocs",
		"site":     "https://sharepoint.company.com/sites/engineering",
	})

	if !strings.Contains(spCtx, "SharePoint Doc: Architecture_V2.pdf") || !strings.Contains(spCtx, "Library: SystemDocs") {
		t.Errorf("Unexpected SharePoint entity context: %s", spCtx)
	}
}






