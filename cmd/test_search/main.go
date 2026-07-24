package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine("./gllam_data.db", embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	nodes, err := gllam.SearchSimilarNodes(context.Background(), "react", 5)
	if err != nil {
		fmt.Printf("Search error: %v\n", err)
	} else {
		fmt.Printf("Found %d nodes.\n", len(nodes))
		for _, n := range nodes {
			fmt.Printf("- %s (dist: %f)\n", n.NodeID, n.Distance)
		}
	}
}
