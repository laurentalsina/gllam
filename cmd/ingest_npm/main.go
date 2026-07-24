package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type NpmResponse struct {
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Dependencies map[string]string `json:"dependencies"`
}

func fetchPackage(pkg string) (*NpmResponse, error) {
	url := fmt.Sprintf("https://registry.npmjs.org/%s/latest", pkg)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var data NpmResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func main() {
	rootPkg := flag.String("pkg", "react", "Root NPM package to crawl")
	maxDepth := flag.Int("depth", 2, "Maximum depth to crawl")
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	flag.Parse()

	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Schema init error: %v\n", err)
	}

	visited := make(map[string]bool)
	var crawl func(pkg string, depth int)

	crawl = func(pkg string, depth int) {
		if depth > *maxDepth || visited[pkg] {
			return
		}
		visited[pkg] = true
		fmt.Printf("Crawling %s (depth %d)...\n", pkg, depth)

		data, err := fetchPackage(pkg)
		if err != nil {
			fmt.Printf("Error fetching %s: %v\n", pkg, err)
			return
		}

		// 1. Insert Node
		nodeID := "npm:" + pkg
		node := memory.SemanticNode{
			ID:   nodeID,
			Name: pkg,
			Type: "npm_package",
		}
		
		if err := gllam.UpsertNode(ctx, node); err != nil {
			fmt.Printf("Failed to upsert node %s: %v\n", nodeID, err)
		} else {
			// Embed node description if available, else just the name
			// textToEmbed := pkg
			// if data.Description != "" {
			// 	textToEmbed = pkg + ": " + data.Description
			// }
			// wait, StoreNodeEmbedding does a lookup by ID to embed the name+type
			// let's just use the built-in function which grabs the name
			if err := gllam.StoreNodeEmbedding(ctx, nodeID); err != nil {
				fmt.Printf("Failed to embed %s: %v\n", nodeID, err)
			}
		}

		// 2. Process Dependencies
		for dep, version := range data.Dependencies {
			depID := "npm:" + dep
			// Optimistically upsert the dependency node so the foreign key constraint passes
			depNode := memory.SemanticNode{
				ID:   depID,
				Name: dep,
				Type: "npm_package",
			}
			_ = gllam.UpsertNode(ctx, depNode)
			_ = gllam.StoreNodeEmbedding(ctx, depID)

			link := memory.SemanticLink{
				SourceID:     nodeID,
				TargetID:     depID,
				Relationship: "depends_on",
				Caveats:      fmt.Sprintf("Version constraint: %s", version),
				ValidFrom:    time.Now().Unix(),
				UpdatedAt:    time.Now().Unix(),
			}

			if err := gllam.AddEdge(ctx, link); err != nil && !strings.Contains(err.Error(), "UNIQUE constraint") {
				fmt.Printf("Failed to add edge %s -> %s: %v\n", nodeID, depID, err)
			}

			// Crawl deeper
			crawl(dep, depth+1)
		}
	}

	crawl(*rootPkg, 0)
	fmt.Printf("Crawled %d unique packages!\n", len(visited))
}
