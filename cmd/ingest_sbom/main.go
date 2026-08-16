package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type SbomItem struct {
	ID               string `json:"id"`
	Category         string `json:"category"`
	RegulationName   string `json:"regulation_name,omitempty"`
	FormatName       string `json:"format_name,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	Description      string `json:"description"`
	SupportedFormats string `json:"supported_formats,omitempty"`
}

func main() {
	dbPath := "./gllam_data.db"
	corpusPath := "./sbom_compliance_en.json"

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

	data, err := os.ReadFile(corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read file: %v\n", err)
		os.Exit(1)
	}

	var items []SbomItem
	if err := json.Unmarshal(data, &items); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Starting ingestion into Semantic Graph...")

	formatMap := make(map[string]string) // name -> id

	for _, item := range items {
		node := memory.SemanticNode{
			ID: item.ID,
			Type:   item.Category,
		}

		if item.Category == "regulation" {
			node.Name = item.RegulationName
		} else if item.Category == "format" {
			node.Name = item.FormatName
			formatMap[strings.ToLower(item.FormatName)] = item.ID
		} else if item.Category == "tool" {
			node.Name = item.ToolName
		} else {
			continue
		}

		if err := gllam.UpsertNode(ctx, node); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save node %s: %v\n", node.ID, err)
		} else {
			if err := gllam.StoreNodeEmbedding(ctx, node.ID); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to embed node %s: %v\n", node.ID, err)
			}
		}
	}

	// Second pass for links
	linkCount := 0
	for _, item := range items {
		if item.Category == "tool" && item.SupportedFormats != "" {
			var formats []string
			if err := json.Unmarshal([]byte(item.SupportedFormats), &formats); err != nil {
				continue
			}

			for _, f := range formats {
				// Naive matching: if format string contains "CycloneDX", link to FMT-SBOM-001
				var targetID string
				fLower := strings.ToLower(f)
				if strings.Contains(fLower, "cyclonedx") {
					targetID = formatMap["cyclonedx"]
				} else if strings.Contains(fLower, "spdx") {
					targetID = formatMap["spdx (software package data exchange)"]
				} else if strings.Contains(fLower, "vex") {
					targetID = formatMap["vex (vulnerability exploitability exchange)"]
				}

				if targetID != "" {
				link := memory.SemanticLink{
						SourceID:     item.ID,
						TargetID:     targetID,
						Relationship: "supports_format",
						Caveats:      fmt.Sprintf("Format variant: %s", f),
					}
					if err := gllam.AddEdge(ctx, link); err != nil {
						// Might already exist
					} else {
						linkCount++
					}
				}
			}
		}
	}

	fmt.Printf("Ingestion complete. Total nodes: %d, Links created: %d\n", len(items), linkCount)
}
