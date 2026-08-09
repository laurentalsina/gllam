package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

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
}



