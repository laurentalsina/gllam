package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

func main() {
	dbPath := "./gllam_data.db"
	ctx := context.Background()

	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	// Insert nodes
	nodes := []memory.SemanticNode{
		{ID: "state-active", Name: "active_state", Type: "state"},
		{ID: "state-deprecated", Name: "deprecated_state", Type: "state"},
	}

	for _, n := range nodes {
		_ = gllam.UpsertNode(ctx, n)
		_ = gllam.StoreNodeEmbedding(ctx, n.ID)
	}

	// Insert conflicting edges
	// Edge 1
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:     "pkg-react",
		TargetID:     "state-active",
		Relationship: "has_state",
	})

	// Edge 2 (triggers conflict)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:     "pkg-react",
		TargetID:     "state-deprecated",
		Relationship: "has_state",
	})

	fmt.Println("Inserted conflicting edges successfully.")
}
