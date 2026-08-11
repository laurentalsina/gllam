package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type QAInstanceRef struct {
	EvidenceSessionIDs []string `json:"evidence_session_ids"`
	GroundTruth        struct {
		SourceSession string `json:"source_session"`
	} `json:"ground_truth"`
}

type ExtractionResponse struct {
	Nodes []memory.SemanticNode `json:"nodes"`
	Links []memory.SemanticLink `json:"links"`
}

func main() {
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	defaultTextServer := "http://100.96.179.19:8888"
	if os.Getenv("OPENROUTER_API_KEY") != "" {
		defaultTextServer = "https://openrouter.ai/api/v1"
	}
	textEndpoint := flag.String("text-server", defaultTextServer, "LLM text server")
	prefix := flag.String("prefix", "sess_", "Prefix of episodic sessions to process")
	qaPath := flag.String("qa-file", "", "Optional path to QA jsonl (e.g. ./bench/d7_qa.jsonl) to extract only target benchmark sessions")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent LLM extraction workers")
	cleanSemantics := flag.Bool("clean", false, "Purge existing semantic_nodes, semantic_links, and semantic_embeddings before extraction")
	flag.Parse()

	if os.Getenv("OPENROUTER_API_KEY") != "" && (*textEndpoint == "http://100.96.179.19:8888" || *textEndpoint == defaultTextServer) {
		*textEndpoint = "https://openrouter.ai/api/v1"
	}

	ctx := context.Background()
	llmClient := engine.NewLLMClient(*textEndpoint)

	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema & migrations: %v\n", err)
		os.Exit(1)
	}

	if *cleanSemantics {
		fmt.Println("Purging existing semantic nodes, links, and embeddings...")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_links")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_nodes")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_embeddings")
	}

	// 1. Optional target session filtering via QA file
	targetSessions := make(map[string]bool)
	if *qaPath != "" {
		qaFile, err := os.Open(*qaPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open QA file %s: %v\n", *qaPath, err)
			os.Exit(1)
		}
		scanner := bufio.NewScanner(qaFile)
		for scanner.Scan() {
			var ref QAInstanceRef
			if err := json.Unmarshal(scanner.Bytes(), &ref); err == nil {
				if ref.GroundTruth.SourceSession != "" {
					targetSessions[ref.GroundTruth.SourceSession] = true
				}
				for _, sid := range ref.EvidenceSessionIDs {
					if sid != "" {
						targetSessions[sid] = true
					}
				}
			}
		}
		_ = qaFile.Close()
		fmt.Printf("Loaded QA target sessions: %d unique evidence sessions referenced in %s\n", len(targetSessions), *qaPath)
	}

	// 2. Fetch episodic summaries matching prefix
	query := `SELECT id, session_id, summary_text, created_at FROM episodic_summaries WHERE id LIKE ? ORDER BY created_at ASC`
	rows, err := gllam.DBRO().QueryContext(ctx, query, *prefix+"%")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to fetch episodes: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	var allEpisodes []memory.EpisodicSummary
	for rows.Next() {
		var ep memory.EpisodicSummary
		if err := rows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &ep.CreatedAt); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to scan episode: %v\n", err)
			os.Exit(1)
		}
		allEpisodes = append(allEpisodes, ep)
	}
	rows.Close()

	var episodes []memory.EpisodicSummary
	if len(targetSessions) > 0 {
		for _, ep := range allEpisodes {
			if targetSessions[ep.SessionID] || targetSessions[ep.ID] {
				episodes = append(episodes, ep)
			}
		}
		fmt.Printf("Targeted extraction: Filtered from %d total episodes down to %d benchmark evidence sessions!\n", len(allEpisodes), len(episodes))
	} else {
		episodes = allEpisodes
		fmt.Printf("Found %d episodes to process for prefix '%s'\n", len(episodes), *prefix)
	}

	systemPrompt := `You are an expert knowledge extractor for an agentic memory graph.
Extract the most critical entities, states, events, and relationships from the provided conversation transcript.

Node Types:
- "event": An occurrence, action, or milestone (e.g. deploy_v1, migration, user_registered)
- "state": An active or past state/status (e.g. state_active, state_deprecated, version_18)
- "entity": A person, physical object, or domain subject
- "service": A software component, database, or network service
- "rule": A general behavioral, output, or formatting rule (e.g. rule_format_markdown_table)
- "constraint": An explicit restriction or negative boundary (e.g. constraint_no_internal_ip)
- "human": A human user or speaker origin (e.g. user_alice)
- "agent": An LLM agent or autonomous worker origin (e.g. agent_planner)
- "system": An external system or API origin (e.g. sys_github)
- "contradiction": An active or past unresolved contradiction between two claims (e.g. contradiction_db_engine)
- "fallacy": A logical fallacy or deceptive premise from the 6 major categories.

You must output ONLY valid JSON matching this exact structure, with no markdown formatting or extra text:
{
  "nodes": [
    {"id": "unique_string", "name": "Display Name", "type": "event|state|entity|service|rule|constraint|human|agent|system|contradiction|fallacy", "context_prompt": "Any specific contextual notes"}
  ],
  "links": [
    {
      "source_id": "id1",
      "target_id": "id2",
      "relationship": "happened_before|has_state|depends_on|has_constraint|is_preference|has_unresolved_conflict|exhibits_fallacy|subverts_claim|resolves_conflict|etc",
      "caveats": "optional conditions",
      "valid_from": "timestamp_or_temporal_note",
      "valid_until": "timestamp_or_temporal_note_or_null",
      "temporal_anchor_id": "node_id_of_referenced_event_or_entity_if_relative",
      "temporal_relation": "before|after|equals|overlaps|during|contains|starts|finishes|meets",
      "temporal_offset_seconds": 0,
      "temporal_note": "relative or imprecise timing phrase if applicable",
      "origin_source_id": "source_node_id_if_issued_by_human_agent_system"
    }
  ]
}`

	var dbMutex sync.Mutex
	var grandTotalNodes int64
	var grandTotalLinks int64

	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup

	startTime := time.Now()

	for i, ep := range episodes {
		wg.Add(1)
		sem <- struct{}{}

		go func(index int, episode memory.EpisodicSummary) {
			defer wg.Done()
			defer func() { <-sem }()

			chunks := engine.ChunkTranscript(episode.SummaryText, 3500, 1000)
			dbMutex.Lock()
			fmt.Printf("[%d/%d] Processing episode %s (%d chars, %d chunks)...\n", index+1, len(episodes), episode.ID, len(episode.SummaryText), len(chunks))
			dbMutex.Unlock()

			var epNodes int64
			var epLinks int64

			for cIdx, chunk := range chunks {
				if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
					continue
				}

				userPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nExtract JSON:", cIdx+1, len(chunks), chunk.Text)
				response, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
				if err != nil {
					dbMutex.Lock()
					fmt.Printf("❌ LLM extraction failed for %s chunk %d: %v\n", episode.ID, cIdx+1, err)
					dbMutex.Unlock()
					continue
				}

				response = strings.TrimSpace(response)
				response = strings.TrimPrefix(response, "```json")
				response = strings.TrimPrefix(response, "```")
				response = strings.TrimSuffix(response, "```")
				response = strings.TrimSpace(response)

				var extraction ExtractionResponse
				if err := json.Unmarshal([]byte(response), &extraction); err != nil {
					repaired := RepairTruncatedJSON(response)
					if err2 := json.Unmarshal([]byte(repaired), &extraction); err2 == nil {
						response = repaired
					} else {
						dbMutex.Lock()
						fmt.Printf("⚠️ JSON parse error for %s chunk %d: %v\n", episode.ID, cIdx+1, err)
						dbMutex.Unlock()
						continue
					}
				}

				dbMutex.Lock()
				// Insert nodes
				for _, node := range extraction.Nodes {
					if node.ID == "" {
						continue
					}
					if err := gllam.UpsertNode(ctx, node); err == nil {
						_ = gllam.StoreNodeEmbedding(ctx, node.ID)
						epNodes++
					}
				}

				// Insert links
				for _, link := range extraction.Links {
					if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
						continue
					}
					if link.ValidFrom == "" {
						if link.TemporalNote != "" {
							link.ValidFrom = "temporal_note"
						} else {
							link.ValidFrom = fmt.Sprintf("%d", episode.CreatedAt)
						}
					}

					if err := gllam.AddEdge(ctx, link); err != nil {
						if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
							var dummy int
							if errSrc := gllam.DBRO().QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.SourceID).Scan(&dummy); errSrc != nil {
								_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.SourceID, Name: link.SourceID, Type: "inferred"})
								_ = gllam.StoreNodeEmbedding(ctx, link.SourceID)
							}
							if errTgt := gllam.DBRO().QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.TargetID).Scan(&dummy); errTgt != nil {
								_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.TargetID, Name: link.TargetID, Type: "inferred"})
								_ = gllam.StoreNodeEmbedding(ctx, link.TargetID)
							}
							if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
								epLinks++
							}
						}
					} else {
						epLinks++
					}
				}
				dbMutex.Unlock()
			}

			atomic.AddInt64(&grandTotalNodes, epNodes)
			atomic.AddInt64(&grandTotalLinks, epLinks)

			dbMutex.Lock()
			fmt.Printf("✅ Finished episode %s: Extracted %d nodes and %d links\n", episode.ID, epNodes, epLinks)
			dbMutex.Unlock()
		}(i, ep)
	}

	wg.Wait()
	elapsed := time.Since(startTime)
	fmt.Printf("\n🎉 Semantic extraction complete in %v! Ingested %d total nodes and %d total links across %d episodes.\n",
		elapsed.Round(time.Second), grandTotalNodes, grandTotalLinks, len(episodes))
}

// RepairTruncatedJSON attempts to auto-close unclosed brackets and objects if an LLM stream cuts off
func RepairTruncatedJSON(jsonStr string) string {
	jsonStr = strings.TrimSpace(jsonStr)
	if jsonStr == "" {
		return jsonStr
	}

	var js map[string]interface{}
	if json.Unmarshal([]byte(jsonStr), &js) == nil {
		return jsonStr
	}

	lastBracket := strings.LastIndex(jsonStr, "}")
	if lastBracket > 0 {
		trimmed := jsonStr[:lastBracket+1]
		openBraces := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		openSquare := strings.Count(trimmed, "[") - strings.Count(trimmed, "]")
		for openSquare > 0 {
			trimmed += "]"
			openSquare--
		}
		for openBraces > 0 {
			trimmed += "}"
			openBraces--
		}
		if json.Unmarshal([]byte(trimmed), &js) == nil {
			return trimmed
		}
	}

	lastSquare := strings.LastIndex(jsonStr, "]")
	if lastSquare > 0 {
		trimmed := jsonStr[:lastSquare+1]
		openBraces := strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		openSquare := strings.Count(trimmed, "[") - strings.Count(trimmed, "]")
		for openSquare > 0 {
			trimmed += "]"
			openSquare--
		}
		for openBraces > 0 {
			trimmed += "}"
			openBraces--
		}
		if json.Unmarshal([]byte(trimmed), &js) == nil {
			return trimmed
		}
	}

	return jsonStr
}
