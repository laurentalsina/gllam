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
	dbPath := flag.String("db", "./test_ie_needle.db", "Path to SQLite database")
	query := flag.String("query", "port number for caddy", "Search query for needle extraction")
	entity := flag.String("entity", "caddy-service", "Target entity ID")
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

	// Seed test data if empty
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService, ContextPrompt: "Reverse proxy listening on port 8080"})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to", Caveats: "Must use TLS certificate in production"})

	entities := []string{}
	if *entity != "" {
		entities = append(entities, *entity)
	}

	needleResults, err := gllam.RetrieveHybridNeedle(ctx, *query, entities, "", 5)
	if err != nil {
		log.Fatalf("RetrieveHybridNeedle failed: %v", err)
	}

	fmt.Printf("=== GLLAM Information Extraction (Needle-in-a-Haystack) Evaluation ===\n")
	fmt.Printf("Query: %q | Entities: %v | Top Needles Found: %d\n\n", *query, entities, len(needleResults))

	for i, nr := range needleResults {
		fmt.Printf("[%d] Node: %s (%s) — RRF Score: %.4f (VecRank: %d, GraphRank: %d)\n",
			i+1, nr.Node.Name, nr.Node.ID, nr.RRFScore, nr.VectorRank, nr.GraphRank)
		if nr.Node.ContextPrompt != "" {
			fmt.Printf("    Context: %s\n", nr.Node.ContextPrompt)
		}
		for _, l := range nr.Links {
			caveatStr := ""
			if l.Caveats != "" {
				caveatStr = fmt.Sprintf(" [Caveat: %s]", l.Caveats)
			}
			fmt.Printf("    -> Link: %s --(%s)--> %s%s\n", l.SourceID, l.Relationship, l.TargetID, caveatStr)
		}
		fmt.Println()
	}
}
