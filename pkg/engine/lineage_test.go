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
	if !strings.Contains(prompt, "(Line 42)") || !strings.Contains(prompt, "(Line 12)") {
		t.Errorf("Formatted prompt missing line numbers: %s", prompt)
	}
}
