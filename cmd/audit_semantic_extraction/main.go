package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
)

type ExtractionAudit struct {
	Timestamp      string         `json:"timestamp"`
	DBPath         string         `json:"db_path"`
	Model          string         `json:"model"`
	TotalNodes     int            `json:"total_nodes"`
	TotalLinks     int            `json:"total_links"`
	NodeTypeCounts map[string]int `json:"node_type_counts"`
	LinkTypeCounts map[string]int `json:"link_type_counts"`
}

func main() {
	dbPath := flag.String("db", "./bench/gllam_data.db", "Path to SQLite database")
	savePath := flag.String("save", "./bench/extraction_snapshot.json", "Save snapshot JSON path")
	comparePath := flag.String("compare", "", "Path to previous snapshot JSON to compare against")
	modelFlag := flag.String("model", "", "LLM model used for extraction")
	flag.Parse()

	modelName := *modelFlag
	if modelName == "" {
		modelName = os.Getenv("LLM_MODEL")
	}
	if modelName == "" {
		if os.Getenv("OPENROUTER_API_KEY") != "" {
			modelName = "meta-llama/llama-3.3-70b-instruct (default)"
		} else {
			modelName = "local-server (Ornith-1.0 / Llama)"
		}
	}

	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to DB: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	audit := ExtractionAudit{
		Timestamp:      time.Now().Format(time.RFC3339),
		DBPath:         *dbPath,
		Model:          modelName,
		NodeTypeCounts: make(map[string]int),
		LinkTypeCounts: make(map[string]int),
	}

	// 1. Count nodes by type
	nodeRows, err := gllam.DBRO().QueryContext(ctx, "SELECT type, count(*) FROM semantic_nodes GROUP BY type")
	if err == nil {
		for nodeRows.Next() {
			var nType string
			var count int
			if err := nodeRows.Scan(&nType, &count); err == nil {
				audit.NodeTypeCounts[nType] = count
				audit.TotalNodes += count
			}
		}
		nodeRows.Close()
	}

	// 2. Count links by relationship
	linkRows, err := gllam.DBRO().QueryContext(ctx, "SELECT relationship, count(*) FROM semantic_links GROUP BY relationship")
	if err == nil {
		for linkRows.Next() {
			var rel string
			var count int
			if err := linkRows.Scan(&rel, &count); err == nil {
				audit.LinkTypeCounts[rel] = count
				audit.TotalLinks += count
			}
		}
		linkRows.Close()
	}

	// Print Summary
	fmt.Printf("=======================================================\n")
	fmt.Printf("📊 Semantic Extraction Audit Snapshot\n")
	fmt.Printf("   ├─ Database: %s\n", *dbPath)
	fmt.Printf("   ├─ LLM Model Used: %s\n", audit.Model)
	fmt.Printf("   ├─ Total Semantic Nodes: %d\n", audit.TotalNodes)
	fmt.Printf("   └─ Total Semantic Links: %d\n", audit.TotalLinks)
	fmt.Printf("=======================================================\n\n")

	fmt.Printf("--- Node Distribution by Type ---\n")
	for nType, count := range audit.NodeTypeCounts {
		fmt.Printf("  • %-15s : %d nodes\n", nType, count)
	}

	fmt.Printf("\n--- Link Distribution by Relationship ---\n")
	for rel, count := range audit.LinkTypeCounts {
		fmt.Printf("  • %-25s : %d links\n", rel, count)
	}

	// Save Snapshot
	data, _ := json.MarshalIndent(audit, "", "  ")
	if err := os.WriteFile(*savePath, data, 0644); err == nil {
		fmt.Printf("\n✅ Snapshot saved to: %s\n", *savePath)
	}

	// Compare with previous snapshot if provided
	if *comparePath != "" {
		prevData, err := os.ReadFile(*comparePath)
		if err == nil {
			var prev AuditSnapshot
			if err := json.Unmarshal(prevData, &prev); err == nil {
				fmt.Printf("\n=======================================================\n")
				fmt.Printf("🔍 Extraction Comparison: Current (%s) vs Previous (%s)\n", audit.Model, prev.Model)
				fmt.Printf("   Snapshot Timestamp: %s vs %s\n", audit.Timestamp, prev.Timestamp)
				fmt.Printf("=======================================================\n")
				fmt.Printf("  Nodes: Current %d vs Previous %d (Diff: %+d)\n", audit.TotalNodes, prev.TotalNodes, audit.TotalNodes-prev.TotalNodes)
				fmt.Printf("  Links: Current %d vs Previous %d (Diff: %+d)\n", audit.TotalLinks, prev.TotalLinks, audit.TotalLinks-prev.TotalLinks)
			}
		}
	}
}

type AuditSnapshot struct {
	Timestamp  string `json:"timestamp"`
	Model      string `json:"model"`
	TotalNodes int    `json:"total_nodes"`
	TotalLinks int    `json:"total_links"`
}
