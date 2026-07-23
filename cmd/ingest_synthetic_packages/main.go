package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

func main() {
	dbPath := "./gllam_data.db"
	ctx := context.Background()

	// Initialize engine with embedder for node search
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Ingesting Synthetic Software Package Dataset...")

	now := time.Now().Unix()

	// 1. Define Nodes
	nodes := []memory.SemanticNode{
		// Packages
		{ID: "pkg-react", Name: "react", Type: "package"},
		{ID: "pkg-react-dom", Name: "react-dom", Type: "package"},
		{ID: "pkg-loose-envify", Name: "loose-envify", Type: "package"},
		{ID: "pkg-js-tokens", Name: "js-tokens", Type: "package"},
		
		// Releases
		{ID: "rel-react-18.0.0", Name: "react-18.0.0", Type: "release"},
		{ID: "rel-react-17.0.2", Name: "react-17.0.2", Type: "release"},
		{ID: "rel-loose-envify-1.4.0", Name: "loose-envify-1.4.0", Type: "release"},
		{ID: "rel-loose-envify-1.3.1", Name: "loose-envify-1.3.1", Type: "release"},
		
		// Features & Vulnerabilities
		{ID: "feat-concurrent", Name: "concurrent_rendering", Type: "feature"},
		{ID: "cve-2023-12345", Name: "CVE-2023-12345", Type: "vulnerability"},
	}

	for _, n := range nodes {
		if err := gllam.UpsertNode(ctx, n); err != nil {
			fmt.Printf("Error saving node %s: %v\n", n.ID, err)
		} else {
			if err := gllam.StoreNodeEmbedding(ctx, n.ID); err != nil {
				fmt.Printf("Error storing embedding for node %s: %v\n", n.ID, err)
			}
		}
	}

	// 2. Define Links
	links := []memory.SemanticLink{
		// Release to Package mapping
		{SourceID: "rel-react-18.0.0", TargetID: "pkg-react", Relationship: "is_release_of", ValidFrom: now, UpdatedAt: now},
		{SourceID: "rel-react-17.0.2", TargetID: "pkg-react", Relationship: "is_release_of", ValidFrom: now, UpdatedAt: now},
		
		// Dependencies for React 18.0.0
		{
			SourceID: "rel-react-18.0.0", TargetID: "rel-loose-envify-1.4.0", 
			Relationship: "depends_on", Caveats: "Required for production builds", 
			ValidFrom: now, UpdatedAt: now,
		},
		
		// Dependencies for React 17.0.2
		{
			SourceID: "rel-react-17.0.2", TargetID: "rel-loose-envify-1.3.1", 
			Relationship: "depends_on", Caveats: "Required for production builds", 
			ValidFrom: now, UpdatedAt: now,
		},

		// Features
		{
			SourceID: "rel-react-18.0.0", TargetID: "feat-concurrent", 
			Relationship: "introduces_feature", Caveats: "Requires opt-in via createRoot API", 
			ValidFrom: now, UpdatedAt: now,
		},

		// Vulnerabilities
		{
			SourceID: "cve-2023-12345", TargetID: "rel-loose-envify-1.4.0", 
			Relationship: "affects", Caveats: "Critical RCE if parsing untrusted environment variables. Fixed in 1.4.1", 
			ValidFrom: now, UpdatedAt: now,
		},
	}

	for _, l := range links {
		if err := gllam.AddEdge(ctx, l); err != nil {
			fmt.Printf("Error saving link from %s to %s: %v\n", l.SourceID, l.TargetID, err)
		}
	}

	fmt.Printf("Ingestion complete. Added %d nodes and %d links.\n", len(nodes), len(links))
}
