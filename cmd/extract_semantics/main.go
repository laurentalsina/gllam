package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type ExtractionResponse struct {
	Nodes []memory.SemanticNode `json:"nodes"`
	Links []memory.SemanticLink `json:"links"`
}

func main() {
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	textEndpoint := flag.String("text-server", "http://100.96.179.19:8888", "LLM text server")
	prefix := flag.String("prefix", "beam-100k-1-", "Prefix of episodic sessions to process")
	flag.Parse()

	ctx := context.Background()
	llmClient := engine.NewLLMClient(*textEndpoint)
	
	// Open engine without embedder for now, we just want to extract and insert
	// Wait, UpsertNode might require embedder if we store embeddings immediately. 
	// We should pass an embedder if StoreNodeEmbedding is called inside UpsertNode.
	// In engine/semantic.go, UpsertNode does not call StoreNodeEmbedding directly, it's called separately.
	// But let's provide one just in case.
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	// 1. Fetch episodic summaries matching prefix
	query := `SELECT id, session_id, summary_text, created_at FROM episodic_summaries WHERE id LIKE ? ORDER BY created_at ASC`
	rows, err := gllam.DBRO().QueryContext(ctx, query, *prefix+"%")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to fetch episodes: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	var episodes []memory.EpisodicSummary
	for rows.Next() {
		var ep memory.EpisodicSummary
		if err := rows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &ep.CreatedAt); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to scan episode: %v\n", err)
			os.Exit(1)
		}
		episodes = append(episodes, ep)
	}
	rows.Close()

	fmt.Printf("Found %d episodes to process for prefix '%s'\n", len(episodes), *prefix)

	systemPrompt := `You are an expert knowledge extractor for an agentic memory graph.
Extract the most critical entities, states, events, and relationships from the provided conversation transcript.

Node Types:
- "event": An occurrence, action, or milestone (e.g. deploy_v1, migration, user_registered)
- "state": An active or past state/status (e.g. state_active, state_deprecated, version_18)
- "entity": A person, physical object, or domain subject
- "service": A software component, database, or network service

Temporal Guidelines for Links:
- If precise timestamps or dates are mentioned (e.g. "May 2024"), extract them into valid_from / valid_until as strings or ISO dates.
- If timing is relative to another event or entity (e.g. "before the database migration"), set valid_from or valid_until to "temporal_note", provide the descriptive phrase in "temporal_note", AND set "temporal_anchor_id" to the node ID of that referenced event/entity (e.g. "event_db_migration") with "temporal_relation" ("before" | "after" | "during" | "co_occurs" | "causes").

You must output ONLY valid JSON matching this exact structure, with no markdown formatting or extra text:
{
  "nodes": [
    {"id": "unique_string", "name": "Display Name", "type": "event|state|entity|service|contradiction", "context_prompt": "Any specific contextual notes"}
  ],
  "links": [
    {
      "source_id": "id1",
      "target_id": "id2",
      "relationship": "happened_before|has_state|depends_on|causes|etc",
      "caveats": "optional conditions",
      "valid_from": "timestamp_or_temporal_note",
      "valid_until": "timestamp_or_temporal_note_or_null",
      "temporal_anchor_id": "node_id_of_referenced_event_or_entity_if_relative",
      "temporal_relation": "before|after|during|co_occurs|causes",
      "temporal_note": "relative or imprecise timing phrase if applicable"
    }
  ]
}

If the chat contains a contradiction (e.g. user changes their mind), extract the conflicting claims as distinct relationships.`

	for _, ep := range episodes {
		fmt.Printf("Processing episode: %s\n", ep.ID)
		
		// Truncate episode text if it's too large to prevent JSON parsing issues with LLM output
		// We'll limit it to first 30,000 chars for safety.
		text := ep.SummaryText
		if len(text) > 30000 {
			text = text[:30000]
		}

		userPrompt := fmt.Sprintf("Transcript:\n%s\n\nExtract JSON:", text)

		response, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err != nil {
			fmt.Printf("LLM extraction failed for %s: %v\n", ep.ID, err)
			continue
		}

		// Clean JSON output (sometimes wrapped in ```json)
		response = strings.TrimSpace(response)
		response = strings.TrimPrefix(response, "```json")
		response = strings.TrimPrefix(response, "```")
		response = strings.TrimSuffix(response, "```")
		response = strings.TrimSpace(response)

		var extraction ExtractionResponse
		if err := json.Unmarshal([]byte(response), &extraction); err != nil {
			fmt.Printf("Failed to parse JSON for %s: %v\n", ep.ID, err)
			// fmt.Println("Raw response:", response)
			continue
		}

		// Insert nodes
		for _, node := range extraction.Nodes {
			if node.ID == "" {
				continue
			}
			if err := gllam.UpsertNode(ctx, node); err != nil {
				fmt.Printf("Failed to upsert node %s: %v\n", node.ID, err)
			} else {
				// Optionally generate embedding for semantic search
				_ = gllam.StoreNodeEmbedding(ctx, node.ID)
			}
		}

		// Insert links
		for _, link := range extraction.Links {
			if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
				continue
			}
			// Default valid_from to episode timestamp if not specified by LLM
			if link.ValidFrom == "" {
				if link.TemporalNote != "" {
					link.ValidFrom = "temporal_note"
				} else {
					link.ValidFrom = fmt.Sprintf("%d", ep.CreatedAt)
				}
			}


			if err := gllam.AddEdge(ctx, link); err != nil {
				if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
					// Self-healing: Check and create missing Source node
					var dummy int
					if errSrc := gllam.DBRO().QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.SourceID).Scan(&dummy); errSrc != nil {
						_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.SourceID, Name: link.SourceID, Type: "inferred"})
						_ = gllam.StoreNodeEmbedding(ctx, link.SourceID)
					}
					// Check and create missing Target node
					if errTgt := gllam.DBRO().QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.TargetID).Scan(&dummy); errTgt != nil {
						_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.TargetID, Name: link.TargetID, Type: "inferred"})
						_ = gllam.StoreNodeEmbedding(ctx, link.TargetID)
					}
					
					// Retry
					if retryErr := gllam.AddEdge(ctx, link); retryErr != nil {
						fmt.Printf("Failed to add edge %s->%s after healing: %v\n", link.SourceID, link.TargetID, retryErr)
					} else {
						fmt.Printf("Self-healed missing node(s) and added edge %s->%s\n", link.SourceID, link.TargetID)
					}
				} else {
					fmt.Printf("Failed to add edge %s->%s: %v\n", link.SourceID, link.TargetID, err)
				}
			}
		}

		fmt.Printf("Successfully ingested %d nodes and %d links from %s\n", len(extraction.Nodes), len(extraction.Links), ep.ID)
	}

	fmt.Println("Semantic extraction complete!")
}
