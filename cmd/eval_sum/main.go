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
	dbPath := flag.String("db", "./test_sum.db", "Path to SQLite database")
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

	// Seed test nodes, active links, obsolete links, and global directives
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService, ContextPrompt: "Reverse proxy on port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-format", Name: "Table Formatting Rule", Type: memory.NodeTypeRule})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-alice", Name: "Alice", Type: memory.NodeTypeHuman})

	// Active link
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to", Caveats: "Must use TLS certificate"})

	// Global preference directive
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "user-alice",
		TargetID:       "rule-format",
		Relationship:   "is_preference",
		RuleContext:    "user_preference",
		ConstraintType: "positive",
		Caveats:        "Always output response tables in Markdown",
	})

	// Obsolete link (should be filtered out by FilterActiveSummaryFacts)
	obsoleteUntil := "1500"
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:     "caddy-service",
		TargetID:     "port-8079",
		Relationship: "binds_to",
		ValidFrom:    "1000",
		ValidUntil:   &obsoleteUntil,
	})

	// Extract reusable procedural workflow
	_ = gllam.ExtractProceduralWorkflow(ctx, "Caddy TLS Deployment", "caddy_setup", "1. Install Caddy 2. Bind port 8080 3. Obtain TLS cert", "Never bypass TLS in production")

	nodes := []memory.SemanticNode{
		{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService},
		{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity},
		{ID: "rule-format", Name: "Table Formatting Rule", Type: memory.NodeTypeRule},
	}
	links := []memory.SemanticLink{
		{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to", Caveats: "Must use TLS certificate"},
		{SourceID: "user-alice", TargetID: "rule-format", Relationship: "is_preference", RuleContext: "user_preference", ConstraintType: "positive", Caveats: "Always output response tables in Markdown"},
		{SourceID: "caddy-service", TargetID: "port-8079", Relationship: "binds_to", ValidUntil: &obsoleteUntil},
	}

	episodes := []memory.EpisodicSummary{
		{ID: "ep-1", SummaryText: "Configured Caddy web server on port 8080 with TLS cert."},
	}

	summaryOutput := engine.FormatSalienceAnchoredSummary(nodes, links, episodes, "What port is Caddy web server running on?", nil)



	fmt.Printf("=== GLLAM Summarization Engine (Salience & Procedural Generalization) Evaluation ===\n\n")
	fmt.Println(summaryOutput)
}
