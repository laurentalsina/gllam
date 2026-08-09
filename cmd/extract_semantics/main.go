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
- "rule": A general behavioral, output, or formatting rule (e.g. rule_format_markdown_table)
- "constraint": An explicit restriction or negative boundary (e.g. constraint_no_internal_ip)
- "human": A human user or speaker origin (e.g. user_alice)
- "agent": An LLM agent or autonomous worker origin (e.g. agent_planner)
- "system": An external system or API origin (e.g. sys_github)
- "contradiction": An active or past unresolved contradiction between two claims (e.g. contradiction_db_engine)
- "fallacy": A logical fallacy or deceptive premise from the 6 major categories:
   1. Formal Logic Error (fallacy_affirming_consequent, fallacy_denying_antecedent, fallacy_circularity)
   2. Improper Premise (fallacy_begging_question, fallacy_false_dilemma, fallacy_false_equivalence)
   3. Faulty Generalization (fallacy_hasty_generalization, fallacy_cherry_picking, fallacy_anecdotal)
   4. Questionable Cause (fallacy_post_hoc, fallacy_cum_hoc, fallacy_single_cause)
   5. Relevance & Poisoning (fallacy_ad_hominem, fallacy_straw_man, fallacy_red_herring, fallacy_appeal_to_authority)
   6. Ambiguity (fallacy_equivocation, fallacy_amphiboly, fallacy_composition_division)

Temporal, Rule, Contradiction, & Fallacy Guidelines for Links:
- If precise timestamps or dates are mentioned (e.g. "May 2024"), extract them into valid_from / valid_until as strings or ISO dates.
- If timing is relative to another event or entity (e.g. "3 days after the migration"), set valid_from or valid_until to "temporal_note", provide the descriptive phrase in "temporal_note", set "temporal_anchor_id" to the referenced node ID, "temporal_relation" using Allen's Interval Algebra ("before" | "after" | "equals" | "overlaps" | "during" | "contains" | "starts" | "finishes" | "meets"), AND "temporal_offset_seconds" (e.g. +259200 for 3 days after, -172800 for 2 days before).
- If a link expresses a rule, constraint, or preference, set "rule_context" ("user_preference" | "session" | "source" | "global"), "constraint_type" ("positive" | "negative"), "rule_rationale" (justification / "because" clause), set "origin_source_id" to the node ID of the human/agent/system that issued it, AND if it is turn-bounded (e.g. "for the next 3 questions"), set "duration_turns" (e.g. 3) and "remaining_turns" (e.g. 3). Otherwise set -1.
- Contradiction & Fallacy Detection:
  * If two claims directly conflict (e.g. DB engine is Postgres vs DB engine is MySQL), extract a "contradiction" node and link both claims using relationship "has_unresolved_conflict". If one claim resolved the conflict, set "resolution_rationale" explaining why.
  * If a premise exhibits any of the 6 logical fallacy categories, extract a "fallacy" node (e.g. fallacy_false_dilemma_1) and link the claim using relationship "exhibits_fallacy" or "subverts_claim".

You must output ONLY valid JSON matching this exact structure, with no markdown formatting or extra text:
{
  "nodes": [
    {"id": "unique_string", "name": "Display Name", "type": "event|state|entity|service|rule|constraint|human|agent|system|contradiction|fallacy", "context_prompt": "Any specific contextual notes or fallacy explanations"}
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
      "origin_source_id": "source_node_id_if_issued_by_human_agent_system",
      "rule_context": "user_preference|session|source|global",
      "constraint_type": "positive|negative",
      "rule_rationale": "justification clause if applicable",
      "resolution_rationale": "explanation when resolving a contradiction",
      "duration_turns": -1,
      "remaining_turns": -1
    }
  ]
}






If the chat contains a contradiction (e.g. user changes their mind), extract the conflicting claims as distinct relationships.`

	for _, ep := range episodes {
		fmt.Printf("Processing episode: %s (length: %d chars)\n", ep.ID, len(ep.SummaryText))
		
		// Split episode text into boundary-aware overlapping chunks (6,000 chars with 2,000 overlap)
		chunks := engine.ChunkTranscript(ep.SummaryText, 6000, 2000)
		fmt.Printf("Split episode %s into %d semantic chunks for extraction\n", ep.ID, len(chunks))

		totalNodesExtracted := 0
		totalLinksExtracted := 0

		for _, chunk := range chunks {
			// Trap 9 Guard: Pre-filter adversarial nonsense / DoS gibberish before LLM extraction
			if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
				fmt.Printf("⚠️ BYZANTINE SEMANTIC POISONING GUARD: Chunk (%d/%d) of episode %s failed semantic coherence check (Adversarial Nonsense/DoS text). Skipping extraction.\n",
					chunk.ChunkIndex+1, len(chunks), ep.ID)
				continue
			}

			userPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nExtract JSON:", chunk.ChunkIndex+1, len(chunks), chunk.Text)


			response, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
			if err != nil {
				fmt.Printf("LLM extraction failed for %s chunk %d: %v\n", ep.ID, chunk.ChunkIndex+1, err)
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
				fmt.Printf("Failed to parse JSON for %s chunk %d: %v\n", ep.ID, chunk.ChunkIndex+1, err)
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
					_ = gllam.StoreNodeEmbedding(ctx, node.ID)
					totalNodesExtracted++
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
						if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
							totalLinksExtracted++
						}
					}
				} else {
					totalLinksExtracted++
				}
			}
		}

		fmt.Printf("Successfully ingested %d nodes and %d links from %s across %d chunks\n", totalNodesExtracted, totalLinksExtracted, ep.ID, len(chunks))
	}


	fmt.Println("Semantic extraction complete!")
}
