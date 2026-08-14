package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
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

func getEnv(key, fallback string) string {
        if value, exists := os.LookupEnv(key); exists {
                return value
        }
        return fallback
}

func main() {
        // Command Line Flag (has prio over)  Environment Variable (has prio over)  Hardcoded Default
        dbPath := flag.String("dbpath", getEnv("DATABASE_PATH", "./bench/ gllam_data.db"), "Path to SQLite database (env: DATABASE_PATH_PATH)")
        textServer := flag.String("text-server", getEnv("TEXT_SERVER", "https://openrouter.ai/api/v1"), "LLM text server endpoint (env: TEXT_SERVER)")
        embeddingServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", "http://127.0.0.1:8800"), "Embeddings server endpoint (env: EMBEDDINGS_SERVER)")

	prefix := flag.String("prefix", "sess_", "Prefix of episodic sessions to process")
	qaPath := flag.String("qa-file", "", "Optional path to QA jsonl (e.g. ./bench/d7_qa.jsonl) to extract only target benchmark sessions")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent LLM extraction workers")
	cleanSemantics := flag.Bool("clean", false, "Purge existing semantic_nodes, semantic_links, and semantic_embeddings before extraction")
	resumeExtraction := flag.Bool("resume", true, "Skip sessions that have already been extracted in previous runs")
	trialMode := flag.Bool("trial", false, "Trial run on 1 chunk without database modifications, printing full raw text and full semantic data")
	trialChunk := flag.Int("trial-chunk", 1, "Chunk index (1-based) to use in trial mode")
	flag.Parse()

	ctx := context.Background()
	llmClient := engine.NewLLMClient(*textServer)

	embedder := engine.NewLlamaEmbedder(*embeddingServer)
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

	// Launch background WAL manager to non-blockingly flush WAL pages every 10s
	gllam.StartWALCheckpointManager(ctx, 10*time.Second)

	// Create checkpoint table for resumable extraction
	_, _ = gllam.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extracted_sessions (
		session_id TEXT PRIMARY KEY,
		extracted_at DATETIME,
		node_count INTEGER,
		link_count INTEGER
	)`)

	// Auto-purge stale 0-link session checkpoints at startup so they are re-extracted with connected links
	if res, err := gllam.DB().ExecContext(ctx, "DELETE FROM extracted_sessions WHERE link_count IS NULL OR link_count = 0"); err == nil {
		if rows, rErr := res.RowsAffected(); rErr == nil && rows > 0 {
			fmt.Printf("🔄 Startup Checkpoint Audit: Purged %d zero-link session checkpoints for reprocessing.\n", rows)
		}
	}

	if *cleanSemantics && !*trialMode {
		fmt.Println("Purging existing semantic nodes, links, embeddings, and extraction checkpoints...")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_links")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_nodes")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_embeddings")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM extracted_sessions")
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
	var allEpisodes []memory.EpisodicSummary
	query := `SELECT id, session_id, summary_text, created_at FROM episodic_summaries WHERE id LIKE ? ORDER BY created_at ASC`
	rows, err := gllam.DBRO().QueryContext(ctx, query, *prefix+"%")
	if err == nil {
		for rows.Next() {
			var ep memory.EpisodicSummary
			if err := rows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &ep.CreatedAt); err == nil {
				allEpisodes = append(allEpisodes, ep)
			}
		}
		rows.Close()
	}

	// Fallback to fetch all episodes if prefix filter yielded 0 results (e.g. session IDs start with d7_ instead of sess_)
	if len(allEpisodes) == 0 {
		fallbackRows, fErr := gllam.DBRO().QueryContext(ctx, `SELECT id, session_id, summary_text, created_at FROM episodic_summaries ORDER BY created_at ASC`)
		if fErr == nil {
			for fallbackRows.Next() {
				var ep memory.EpisodicSummary
				if err := fallbackRows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &ep.CreatedAt); err == nil {
					allEpisodes = append(allEpisodes, ep)
				}
			}
			fallbackRows.Close()
		}
	}

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

	if *resumeExtraction && !*cleanSemantics && !*trialMode {
		// Clean up any 0-node/0-link failure checkpoints from previous aborted runs
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM extracted_sessions WHERE node_count = 0 AND link_count = 0")

		completedSessions := make(map[string]bool)
		rowsDone, err := gllam.DBRO().QueryContext(ctx, "SELECT session_id FROM extracted_sessions WHERE node_count > 0 OR link_count > 0")
		if err == nil {
			for rowsDone.Next() {
				var sid string
				if err := rowsDone.Scan(&sid); err == nil {
					completedSessions[sid] = true
				}
			}
			rowsDone.Close()
		}

		var uncompletedEpisodes []memory.EpisodicSummary
		for _, ep := range episodes {
			if !completedSessions[ep.SessionID] && !completedSessions[ep.ID] {
				uncompletedEpisodes = append(uncompletedEpisodes, ep)
			}
		}
		if len(completedSessions) > 0 {
			fmt.Printf("🔄 Resuming extraction: %d valid episodes already completed. Re-processing 0-yield failures and remaining %d episodes...\n", len(completedSessions), len(uncompletedEpisodes))
		}
		episodes = uncompletedEpisodes
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

COMPACT FIELD FORMAT RULES:
- Keep all string property values (context_prompt, caveats, temporal_note, rule_rationale) short and concise (1 short sentence maximum).
- Keep display "name" short (1-4 words maximum).
- DO NOT output long paragraphs or explanations inside JSON fields or outside JSON blocks. Output ONLY raw compact JSON.

CRITICAL JSON RULE: Never put unescaped double quotes inside string property values (such as context_prompt, caveats, name, temporal_note). Use single quotes or plain text for quotes inside values.

CRITICAL GRAPH CONSTRAINT: You MUST extract semantic relationships (links) connecting every extracted node! Do not output floating disconnected nodes; every extracted entity, event, state, human, or preference must be connected by at least one link.

SPEECH ACT & STATEMENT RELATIONSHIP RULES:
- When speakers converse, connect human/speaker nodes to the states, events, or entities they discuss using speech act relationships:
  * "asked_about": Speaker asking a question (e.g. user_alice -> asked_about -> topic_weather)
  * "reported_state": Speaker asserting a fact or state (e.g. user_bob -> reported_state -> state_wind_picking_up)
  * "observed_event": Speaker sharing an observation (e.g. user_bob -> observed_event -> event_radar_glitching)
  * "expressed_concern": Speaker noting a risk or hesitation (e.g. user_bob -> expressed_concern -> state_hand_tingling)
  * "replied_to": Speaker responding to another speaker (e.g. user_bob -> replied_to -> user_alice)
- Set "caveats" to the exact short statement phrase or quote (e.g. "Wind's picking up from the SW, as usual").
- Set "temporal_note" to the conversation sequence context (e.g. "Episode [N/13343] conversation").

You must output ONLY valid JSON matching this exact structure, with no markdown formatting or extra text:
{
  "nodes": [
    {"id": "unique_string", "name": "Display Name", "type": "event|state|entity|service|rule|constraint|human|agent|system|contradiction|fallacy", "context_prompt": "Any specific contextual notes"}
  ],
  "links": [
    {
      "source_id": "id1",
      "target_id": "id2",
      "relationship": "happened_before|has_state|depends_on|has_constraint|is_preference|asked_about|reported_state|observed_event|expressed_concern|replied_to|has_unresolved_conflict|exhibits_fallacy|subverts_claim|resolves_conflict|etc",
      "caveats": "optional conditions or statement quote",
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

	if *trialMode {
		if len(episodes) == 0 {
			fmt.Fprintf(os.Stderr, "❌ No episodes found to run trial extraction!\n")
			os.Exit(1)
		}
		ep := episodes[0]
		chunks := engine.ChunkTranscript(ep.SummaryText, 5000, 600)
		if len(chunks) == 0 {
			fmt.Fprintf(os.Stderr, "❌ Episode %s has an empty transcript!\n", ep.ID)
			os.Exit(1)
		}
		cIdx := *trialChunk - 1
		if cIdx < 0 || cIdx >= len(chunks) {
			cIdx = 0
		}
		chunk := chunks[cIdx]

		userPrompt := fmt.Sprintf("Conversation Episode [1 / %d] (Session ID: %s | Created At: %d):\nTranscript Chunk (%d/%d):\n%s\n\nExtract JSON:", len(episodes), ep.ID, ep.CreatedAt, cIdx+1, len(chunks), chunk.Text)

		fmt.Println("================================================================================")
		fmt.Printf("🔬 TRIAL EXTRACTION MODE (Model: %s)\n", llmClient.Model)
		fmt.Printf("   ├─ Endpoint: %s\n", llmClient.BaseURL)
		fmt.Printf("   ├─ Target Episode ID: %s\n", ep.ID)
		fmt.Printf("   └─ Chunk %d of %d (%d characters)\n", cIdx+1, len(chunks), len(chunk.Text))
		fmt.Println("================================================================================")

		startTime := time.Now()
		response, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
		duration := time.Since(startTime)

		if err != nil {
			fmt.Fprintf(os.Stderr, "❌ LLM Extraction Failed: %v\n", err)
			os.Exit(1)
		}

		cleanJSON := SanitizeLLMJSON(response)
		var extData ExtractionResponse
		parseErr := json.Unmarshal([]byte(cleanJSON), &extData)
		prettyParsedJSON, _ := json.MarshalIndent(extData, "", "  ")

		// Graph Connectivity Audit
		nodeMap := make(map[string]bool)
		for _, n := range extData.Nodes {
			nodeMap[n.ID] = true
		}
		linkedNodes := make(map[string]bool)
		for _, l := range extData.Links {
			linkedNodes[l.SourceID] = true
			linkedNodes[l.TargetID] = true
		}
		var unlinkedNodes []string
		for nID := range nodeMap {
			if !linkedNodes[nID] {
				unlinkedNodes = append(unlinkedNodes, nID)
			}
		}

		fmt.Println("\n--- 1. FULL RAW TRANSCRIPT CHUNK ---")
		fmt.Println(chunk.Text)

		fmt.Println("\n--- 2. RAW LLM MODEL RESPONSE ---")
		fmt.Println(response)

		fmt.Println("\n--- 3. PARSED SEMANTIC DATA (FULL JSON) ---")
		fmt.Println(string(prettyParsedJSON))

		fmt.Println("\n--- 4. EXTRACTION QUALITY METRICS ---")
		fmt.Printf("├─ Total Extracted Nodes: %d\n", len(extData.Nodes))
		fmt.Printf("├─ Total Extracted Links: %d\n", len(extData.Links))
		fmt.Printf("├─ LLM Inference Duration: %.2f seconds\n", duration.Seconds())
		if parseErr != nil {
			fmt.Printf("├─ ⚠️ JSON Parsing Status: FAILED (%v)\n", parseErr)
		} else {
			fmt.Printf("├─ ✅ JSON Parsing Status: SUCCESS (Valid JSON)\n")
		}
		if len(unlinkedNodes) > 0 {
			fmt.Printf("└─ ⚠️ Disconnected Floating Nodes (%d): %s\n", len(unlinkedNodes), strings.Join(unlinkedNodes, ", "))
		} else {
			fmt.Printf("└─ ✅ Graph Connectivity: All nodes are fully connected by at least one link!\n")
		}
		fmt.Println("================================================================================")
		return
	}

	sem := make(chan struct{}, *concurrency)
	var wg sync.WaitGroup

	startTime := time.Now()

	for i, ep := range episodes {
		wg.Add(1)
		sem <- struct{}{}

		go func(index int, episode memory.EpisodicSummary) {
			defer wg.Done()
			defer func() { <-sem }()

			chunks := engine.ChunkTranscript(episode.SummaryText, 5000, 600)
			dbMutex.Lock()
			fmt.Printf("[%s] [%d/%d] Processing episode %s (%d chars, %d chunks)...\n", time.Now().Format("2006.01.02 15:04:05"), index+1, len(episodes), episode.ID, len(episode.SummaryText), len(chunks))
			dbMutex.Unlock()

			var epNodes int64
			var epLinks int64
			var epLLMTime time.Duration
			var epDBWaitTime time.Duration
			var epDBWriteTime time.Duration
			var epEmbedTime time.Duration

			for cIdx, chunk := range chunks {
				if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
					continue
				}

				userPrompt := fmt.Sprintf("Conversation Episode [%d / %d] (Session ID: %s | Created At: %d):\nTranscript Chunk (%d/%d):\n%s\n\nExtract JSON:", index+1, len(episodes), episode.ID, episode.CreatedAt, cIdx+1, len(chunks), chunk.Text)
				var response string
				var err error
				llmStart := time.Now()
				for attempt := 1; attempt <= 5; attempt++ {
					response, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
					if err == nil && strings.TrimSpace(response) != "" {
						break
					}
					if err == nil {
						err = fmt.Errorf("empty response from LLM")
					}
					sleepSec := 1 << attempt
					if sleepSec > 30 {
						sleepSec = 30
					}
					dbMutex.Lock()
					fmt.Printf("[%s] ⚠️ OpenRouter error for %s chunk %d (attempt %d/5): %v. Retrying in %ds...\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, attempt, err, sleepSec)
					dbMutex.Unlock()
					time.Sleep(time.Duration(sleepSec) * time.Second)
				}
				epLLMTime += time.Since(llmStart)

				if err != nil {
					dbMutex.Lock()
					fmt.Printf("[%s] ❌ LLM extraction failed for %s chunk %d after 5 attempts: %v\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, err)
					dbMutex.Unlock()
					continue
				}

				response = SanitizeLLMJSON(response)

				var extraction ExtractionResponse
				if err := json.Unmarshal([]byte(response), &extraction); err != nil {
					repairedJSON := RepairTruncatedJSON(response)
					if err2 := json.Unmarshal([]byte(repairedJSON), &extraction); err2 == nil {
						response = repairedJSON
					} else {
						offset := int64(0)
						errToReport := err
						if syntaxErr, ok := err.(*json.SyntaxError); ok {
							offset = syntaxErr.Offset
							errToReport = fmt.Errorf("invalid character at offset %d", syntaxErr.Offset)
						}
						sample := response
						if offset > 0 && offset < int64(len(response)) {
							start := offset - 20
							if start < 0 {
								start = 0
							}
							end := offset + 20
							if end > int64(len(response)) {
								end = int64(len(response))
							}
							sample = response[start:end]
						}
						dbMutex.Lock()
						fmt.Printf("[%s] ⚠️ JSON parse error for %s chunk %d at offset %d: %v | Context: %q\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, offset, errToReport, sample)
						dbMutex.Unlock()
						continue
					}
				}

				// Active Link Recovery: If nodes were extracted but 0 links, perform an immediate targeted LLM retry prompt
				if len(extraction.Nodes) > 0 && len(extraction.Links) == 0 {
					var nodeNames []string
					for _, n := range extraction.Nodes {
						if n.Name != "" {
							nodeNames = append(nodeNames, n.Name)
						}
					}
					retryPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nYour previous response extracted nodes %v but 0 links. You MUST output relationship links connecting these nodes (e.g. participated_in, has_state, occurred_during, relates_to). Extract JSON with links:", cIdx+1, len(chunks), chunk.Text, nodeNames)
					if retryResp, rErr := llmClient.Generate(ctx, systemPrompt, retryPrompt); rErr == nil {
						retryResp = SanitizeLLMJSON(retryResp)
						var retryExt ExtractionResponse
						if err := json.Unmarshal([]byte(retryResp), &retryExt); err == nil && len(retryExt.Links) > 0 {
							extraction = retryExt
						} else if repaired := RepairTruncatedJSON(retryResp); json.Unmarshal([]byte(repaired), &retryExt) == nil && len(retryExt.Links) > 0 {
							extraction = retryExt
						}
					}
				}

				// Diagnostic Surfacing: If links remain 0 despite retry prompt, log complete incident metadata for analysis
				if len(extraction.Nodes) > 0 && len(extraction.Links) == 0 {
					var nodeSummary []string
					for _, n := range extraction.Nodes {
						nodeSummary = append(nodeSummary, fmt.Sprintf("%s (%s)", n.ID, n.Type))
					}
					snippet := chunk.Text
					if len(snippet) > 150 {
						snippet = snippet[:150] + "..."
					}
					dbMutex.Lock()
					fmt.Printf("[%s] ⚠️ 0-Link Yield Incident for %s (chunk %d/%d):\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, len(chunks))
					fmt.Printf("   ├─ Extracted Nodes: %v\n", nodeSummary)
					fmt.Printf("   └─ Transcript Snippet: %q\n", snippet)
					dbMutex.Unlock()
				}

				// Parallel Vector Generation: Generate vector embeddings over network in parallel BEFORE acquiring DB lock
				type nodeVector struct {
					id  string
					vec []float32
				}
				var nodeVecs []nodeVector
				var vecMutex sync.Mutex
				embedStart := time.Now()

				if len(extraction.Nodes) > 0 {
					var embWg sync.WaitGroup
					embSem := make(chan struct{}, 5)
					for _, node := range extraction.Nodes {
						if node.ID == "" || node.Name == "" {
							continue
						}
						embWg.Add(1)
						embSem <- struct{}{}
						go func(n memory.SemanticNode) {
							defer embWg.Done()
							defer func() { <-embSem }()
							if vec, err := embedder.Embed(ctx, n.Name); err == nil && len(vec) > 0 {
								vecMutex.Lock()
								nodeVecs = append(nodeVecs, nodeVector{id: n.ID, vec: vec})
								vecMutex.Unlock()
							}
						}(node)
					}
					embWg.Wait()
				}
				epEmbedTime += time.Since(embedStart)

				var insertedNodeIDs []string
				dbWaitStart := time.Now()
				dbMutex.Lock()
				epDBWaitTime += time.Since(dbWaitStart)

				dbWriteStart := time.Now()
				_, _ = gllam.DB().ExecContext(ctx, "BEGIN IMMEDIATE")

				// Insert nodes
				for _, node := range extraction.Nodes {
					if node.ID == "" {
						continue
					}
					if err := gllam.UpsertNode(ctx, node); err == nil {
						insertedNodeIDs = append(insertedNodeIDs, node.ID)
						epNodes++
					}
				}

				// Insert links
				for _, link := range extraction.Links {
					if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
						continue
					}
					if strings.EqualFold(link.TemporalAnchorID, "null") || strings.EqualFold(link.TemporalAnchorID, "none") || strings.EqualFold(link.TemporalAnchorID, "nil") {
						link.TemporalAnchorID = ""
					}
					if strings.EqualFold(link.OriginSourceID, "null") || strings.EqualFold(link.OriginSourceID, "none") || strings.EqualFold(link.OriginSourceID, "nil") {
						link.OriginSourceID = ""
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
							ensureNode := func(nodeID *string) {
								if *nodeID == "" {
									return
								}
								var existingID string
								if errNode := gllam.DB().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE id = ? OR name = ?", *nodeID, *nodeID).Scan(&existingID); errNode == nil {
									*nodeID = existingID
									return
								}
								nodeName := *nodeID
								if errUp := gllam.UpsertNode(ctx, memory.SemanticNode{ID: *nodeID, Name: nodeName, Type: "inferred"}); errUp != nil {
									nodeName = fmt.Sprintf("%s_inf_%d", *nodeID, time.Now().UnixNano()%10000)
									_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: *nodeID, Name: nodeName, Type: "inferred"})
								}
								insertedNodeIDs = append(insertedNodeIDs, *nodeID)
							}
							ensureNode(&link.SourceID)
							ensureNode(&link.TargetID)
							ensureNode(&link.OriginSourceID)
							ensureNode(&link.TemporalAnchorID)

							if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
								epLinks++
							} else {
								fmt.Printf("[%s] ⚠️ AddEdge retry failed for %s link %s->%s: %v\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, link.SourceID, link.TargetID, retryErr)
							}
						} else {
							fmt.Printf("[%s] ⚠️ AddEdge failed for %s link %s->%s: %v\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, link.SourceID, link.TargetID, err)
						}
					} else {
						epLinks++
					}
				}

				// Batch insert pre-generated node vectors inside the single atomic transaction
				for _, nv := range nodeVecs {
					_ = gllam.IndexNodeVector(ctx, nv.id, nv.vec)
				}

				_, _ = gllam.DB().ExecContext(ctx, "COMMIT")
				epDBWriteTime += time.Since(dbWriteStart)
				dbMutex.Unlock()
			}

			atomic.AddInt64(&grandTotalNodes, epNodes)
			atomic.AddInt64(&grandTotalLinks, epLinks)

			dbMutex.Lock()
			tsStr := time.Now().Format("2006.01.02 15:04:05")
			if epNodes > 0 && epLinks > 0 {
				_, _ = gllam.DB().ExecContext(ctx, "INSERT OR REPLACE INTO extracted_sessions (session_id, extracted_at, node_count, link_count) VALUES (?, ?, ?, ?)", episode.SessionID, time.Now(), epNodes, epLinks)
				fmt.Printf("[%s] ✅ Finished %s (%d nodes, %d links) | LLM: %v, DB-wait: %v, DB-write: %v, Embed: %v\n", tsStr, episode.ID, epNodes, epLinks, epLLMTime.Round(time.Millisecond), epDBWaitTime.Round(time.Millisecond), epDBWriteTime.Round(time.Millisecond), epEmbedTime.Round(time.Millisecond))
			} else {
				fmt.Printf("[%s] ⚠️ Finished %s (%d nodes, 0 links - will be retried on next resume run)\n", tsStr, episode.ID, epNodes)
			}
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
	jsonStr = SanitizeLLMJSON(jsonStr)
	if jsonStr == "" {
		return jsonStr
	}

	var ext ExtractionResponse
	if json.Unmarshal([]byte(jsonStr), &ext) == nil {
		return jsonStr
	}

	lastBracket := strings.LastIndex(jsonStr, "}")
	for lastBracket > 0 {
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
		if json.Unmarshal([]byte(trimmed), &ext) == nil {
			return trimmed
		}
		lastBracket = strings.LastIndex(jsonStr[:lastBracket], "}")
	}

	lastSquare := strings.LastIndex(jsonStr, "]")
	for lastSquare > 0 {
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
		if json.Unmarshal([]byte(trimmed), &ext) == nil {
			return trimmed
		}
		lastSquare = strings.LastIndex(jsonStr[:lastSquare], "]")
	}

	lastComma := strings.LastIndex(jsonStr, ",")
	for lastComma > 0 {
		trimmed := jsonStr[:lastComma]
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
		if json.Unmarshal([]byte(trimmed), &ext) == nil {
			return trimmed
		}
		lastComma = strings.LastIndex(jsonStr[:lastComma], ",")
	}

	return jsonStr
}

// SanitizeLLMJSON cleans common LLM markdown artifacts (asterisks, trailing commas, non-JSON preambles)
func SanitizeLLMJSON(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.Index(s, "```json"); idx != -1 {
		s = s[idx+7:]
	} else if idx := strings.Index(s, "```"); idx != -1 {
		s = s[idx+3:]
	}
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	s = strings.TrimSpace(s)

	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	} else if start != -1 {
		s = s[start:]
	} else {
		return ""
	}

	// Escape raw unescaped newlines inside JSON string literals FIRST
	s = EscapeRawNewlinesInStrings(s)

	asteriskRegex := regexp.MustCompile(`(?m)\*\*([a-zA-Z0-9_]+)\*\*\s*:`)
	s = asteriskRegex.ReplaceAllString(s, `"$1":`)

	// Clean up stray unquoted bare words output after comma/brace before newline (e.g. "},eville\"},\n" -> "},\n")
	bareWordRegex := regexp.MustCompile(`(?m)([,\}\]])\s*[a-zA-Z_][a-zA-Z0-9_]*[^,\{\}\[\]\n]*([,\}\]])?\s*([\r\n]+)`)
	s = bareWordRegex.ReplaceAllString(s, "$1\n")

	// Clean up unquoted stray key fragments after brace before colon (e.g. "},eville\":" -> "},\n\"context_prompt\":")
	strayKeyRegex := regexp.MustCompile(`(?m)\}\s*,?\s*([a-zA-Z0-9_]+)":`)
	s = strayKeyRegex.ReplaceAllString(s, "},\n\"context_prompt\":")

	// Clean up non-JSON CJK/multilingual words typed right after comma, brace, or bracket (e.g. ",一起\n" -> ",\n" or "},一起\n" -> "},\n")
	cjkCommentRegex := regexp.MustCompile(`(?m)([,\}\]])\s*[\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}\x{f900}-\x{faff}\x{3000}-\x{303f}\x{3040}-\x{30ff}\x{ac00}-\x{d7af}]+\s*`)
	s = cjkCommentRegex.ReplaceAllString(s, "$1\n")

	// Clean up any non-ASCII / non-JSON characters typed between JSON tokens (e.g. "},一致\n" -> "},\n")
	nonJsonCharRegex := regexp.MustCompile(`(?m)([,\}\]])\s*[^,\{\}\[\]"a-zA-Z0-9_\s]+\s*`)
	s = nonJsonCharRegex.ReplaceAllString(s, "$1\n")

	// Clean up adjacent unseparated strings before brace or comma (e.g. "\"flair\"eville\"\n    }" -> "\"flair eville\"\n    }")
	adjacentStringRegex := regexp.MustCompile(`(?m)"([^"\\]*(?:\\.[^"\\]*)*)"\s*"([a-zA-Z0-9_]+)"\s*([\r\n\s]*[\},\]])`)
	s = adjacentStringRegex.ReplaceAllString(s, `"$1 $2"$3`)

	// Fix keys followed by comma instead of colon (e.g. "\"context_patchy_data\", \"context_prompt\":" -> "\"context_prompt\": \"patchy_data\",")
	keyWithoutColonRegex := regexp.MustCompile(`(?m)"(context_[a-zA-Z0-9_]+)"\s*,`)
	s = keyWithoutColonRegex.ReplaceAllString(s, `"context_prompt": "$1",`)

	// Clean up double-escaped structural newlines outside quotes (e.g. "},\\nn" -> "},\n")
	escapedStructuralNewlineRegex := regexp.MustCompile(`(?m)\}\s*,\s*\\+[nN]+\s*`)
	s = escapedStructuralNewlineRegex.ReplaceAllString(s, "},\n")

	// Auto-close property string values missing closing quotes before structural braces/brackets (e.g. "\"context_prompt\": \"text\n  ]," -> "\"context_prompt\": \"text\"\n  ],")
	unclosedStringRegex := regexp.MustCompile(`(?m)(:\s*"[^"]*?)(\s*[\r\n]+\s*[\}\]\,])`)
	s = unclosedStringRegex.ReplaceAllString(s, `$1"$2`)

	trailingCommaRegex := regexp.MustCompile(`,(\s*[\}\]])`)
	s = trailingCommaRegex.ReplaceAllString(s, "$1")

	// Escape any raw unescaped newlines inside JSON string literals as the final safety pass
	s = EscapeRawNewlinesInStrings(s)

	return strings.TrimSpace(s)
}

// EscapeRawNewlinesInStrings escapes illegal raw unescaped newlines and control characters inside JSON string literals
func EscapeRawNewlinesInStrings(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				out.WriteByte(ch)
			} else if ch == '\\' {
				escaped = true
				out.WriteByte(ch)
			} else if ch == '"' {
				inString = false
				out.WriteByte(ch)
			} else if ch == '\n' {
				out.WriteString(`\n`)
			} else if ch == '\r' {
				// skip raw carriage return
			} else if ch == '\t' {
				out.WriteString(`\t`)
			} else {
				out.WriteByte(ch)
			}
		} else {
			if ch == '"' {
				inString = true
			}
			out.WriteByte(ch)
		}
	}
	return out.String()
}
