package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

func main() {
	dbPath := flag.String("db", "./test_lineage.db", "Path to SQLite database")
	flag.Parse()

	absDBPath, err := filepath.Abs(*dbPath)
	if err != nil {
		log.Fatalf("Invalid DB path: %v", err)
	}

	gllam, err := engine.NewGllamEngine(absDBPath, nil)
	if err != nil {
		log.Fatalf("Failed to initialize GLLAM Engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		log.Fatalf("Failed to initialize schema: %v", err)
	}

	ctx := context.Background()

	// 1. Seed semantic nodes
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService, ContextPrompt: "Reverse proxy service on port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})

	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to"})

	// 2. Attach strict document lineage URIs
	lin1 := memory.DocumentLineage{
		ID:            "lin-caddy-jira-101",
		NodeID:        "caddy-service",
		SourceURI:     "https://jira.internal.company.com/browse/PROD-101",
		DocumentTitle: "Jira PROD-101: Configure Caddy Web Server",
		SourceType:    "jira",
		LineNumber:    42,
		Checksum:      "a1b2c3d4e5f67890",
	}
	lin2 := memory.DocumentLineage{
		ID:            "lin-port-pr-55",
		NodeID:        "port-8080",
		SourceURI:     "https://github.company.com/infra/config/pull/55",
		DocumentTitle: "PR #55: Bind Caddy to Port 8080",
		SourceType:    "pull_request",
		LineNumber:    12,
		Checksum:      "f6e5d4c3b2a10987",
	}

	if err := gllam.AddDocumentLineage(ctx, lin1); err != nil {
		log.Fatalf("Failed to add lineage 1: %v", err)
	}
	if err := gllam.AddDocumentLineage(ctx, lin2); err != nil {
		log.Fatalf("Failed to add lineage 2: %v", err)
	}

	// 3. Route and assemble context
	compiledCtx, err := gllam.RouteAndAssemble(ctx, "where is caddy web server running?", []string{"caddy-service"})
	if err != nil {
		log.Fatalf("RouteAndAssemble failed: %v", err)
	}

	prompt := engine.FormatSystemPrompt(compiledCtx)

	fmt.Printf("=== GLLAM Strict Information Lineage Evaluation ===\n\n")
	fmt.Println(prompt)
}
