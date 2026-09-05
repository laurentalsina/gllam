package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"database/sql"

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

func resolvePath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func main() {
	dbPath := flag.String("dbpath", getEnv("DATABASE_PATH", "./bench/gllam_data.db"), "Path to SQLite database")
	textServer := flag.String("text-server", getEnv("TEXT_SERVER", "http://127.0.0.1:8888"), "LLM text server endpoint (llama.cpp)")
	embeddingServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", "http://127.0.0.1:8800"), "Embeddings server endpoint")
        promptsPath := flag.String("prompts-config", getEnv("PROMPTS_CONFIG", "config/agentic_memory.json"), "Path to agentic memory config and prompts")
        schemaPath := flag.String("schema-file", getEnv("EXTRACTION_SCHEMA_PATH", "./config/semantic_extraction_schema.json"), "Path to JSON schema file for constrained decoding")

	prefix := flag.String("prefix", "sess_", "Prefix of episodic sessions to process")
	qaPath := flag.String("qa-file", "", "Optional path to QA jsonl to extract target benchmark sessions")
	sourceURI := flag.String("source-uri", "file://corpus_sessions.jsonl", "Base URI of the raw source data (e.g. file://corpus_sessions.jsonl or dataset://memarena)")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent LLM extraction workers")
	cleanSemantics := flag.Bool("clean", false, "Purge existing semantic data before extraction")
	resumeExtraction := flag.Bool("resume", true, "Skip sessions that have already been extracted")
	trialMode := flag.Bool("trial", false, "Trial run on 1 chunk without database modifications")
	trialChunk := flag.Int("trial-chunk", 1, "Chunk index (1-based) to use in trial mode")
	useTemporal := flag.Bool("temporal", false, "Use temporal-ready extraction prompts instead of default")
	flag.Parse()

        var extractionJSONSchema map[string]interface{}
        if *schemaPath != "" {
                targetSchema := *schemaPath
                if *useTemporal {
                        targetSchema = "./config/semantic_extraction_temporal_schema.json"
                        fmt.Printf("ℹ️ Overriding extraction schema with temporal: %s\n", targetSchema)
                }
                data, err := os.ReadFile(targetSchema)
                if err != nil {
                        fmt.Fprintf(os.Stderr, "⚠️ Failed to read schema file %s: %v. Running without schema constraint.\n", targetSchema, err)
                } else {
                        if err := json.Unmarshal(data, &extractionJSONSchema); err != nil {
                                fmt.Fprintf(os.Stderr, "❌ Failed to parse schema JSON %s: %v\n", targetSchema, err)
                                os.Exit(1)
                        }
                }
        }

	ctx := context.Background()
	llmClient := engine.NewLLMClient(*textServer)
	llmClient.Tier = "fast"

	embedder := engine.NewLlamaEmbedder(*embeddingServer)
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

        // Load custom prompts if the path is provided
	if *promptsPath != "" {
		if err := gllam.LoadSystemPromptsConfig(*promptsPath); err != nil {
			fmt.Printf("⚠️ Could not load prompts config from %s (%v). Using engine default prompts.\n", *promptsPath, err)
		} else {
			fmt.Printf("✅ Loaded system prompts config from: %s\n", *promptsPath)
		}
	}

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema & migrations: %v\n", err)
		os.Exit(1)
	}
	
	gllam.StartWALCheckpointManager(ctx, 10*time.Second)

	_, _ = gllam.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extracted_sessions (
		session_id TEXT PRIMARY KEY,
		extracted_at DATETIME,
		node_count INTEGER,
		link_count INTEGER
	)`)

	if res, err := gllam.DB().ExecContext(ctx, "DELETE FROM extracted_sessions WHERE link_count IS NULL OR link_count = 0"); err == nil {
		if rows, rErr := res.RowsAffected(); rErr == nil && rows > 0 {
			fmt.Printf("🔄 Startup Checkpoint Audit: Purged %d zero-link session checkpoints for reprocessing.\n", rows)
		}
	}

	if *cleanSemantics && !*trialMode {
		fmt.Println("Purging existing semantic nodes, links, embeddings, and checkpoints...")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_links")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_nodes")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM semantic_embeddings")
		_, _ = gllam.DB().ExecContext(ctx, "DELETE FROM extracted_sessions")
	}

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

	var allEpisodes []memory.EpisodicSummary
	query := `SELECT id, session_id, summary_text, source_uri, created_at FROM episodic_summaries WHERE id LIKE ? ORDER BY created_at ASC`
	rows, err := gllam.DBRO().QueryContext(ctx, query, *prefix+"%")
	if err == nil {
		for rows.Next() {
			var ep memory.EpisodicSummary
			var srcURI sql.NullString
			if err := rows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &srcURI, engine.ScanTime(&ep.CreatedAt)); err == nil {
				if srcURI.Valid {
					ep.SourceURI = srcURI.String
				}
				allEpisodes = append(allEpisodes, ep)
			}
		}
		rows.Close()
	}

	if len(allEpisodes) == 0 {
		fallbackRows, fErr := gllam.DBRO().QueryContext(ctx, `SELECT id, session_id, summary_text, source_uri, created_at FROM episodic_summaries ORDER BY created_at ASC`)
		if fErr == nil {
			for fallbackRows.Next() {
				var ep memory.EpisodicSummary
				var srcURI sql.NullString
				if err := fallbackRows.Scan(&ep.ID, &ep.SessionID, &ep.SummaryText, &srcURI, engine.ScanTime(&ep.CreatedAt)); err == nil {
					if srcURI.Valid {
						ep.SourceURI = srcURI.String
					}
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
		fmt.Printf("Targeted extraction: Filtered down to %d benchmark sessions.\n", len(episodes))
	} else {
		episodes = allEpisodes
		fmt.Printf("Found %d episodes to process for prefix '%s'\n", len(episodes), *prefix)
	}

	if *resumeExtraction && !*cleanSemantics && !*trialMode {
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
			fmt.Printf("🔄 Resuming extraction: %d valid episodes already completed. %d remaining.\n", len(completedSessions), len(uncompletedEpisodes))
		}
		episodes = uncompletedEpisodes
	}

        systemPrompt := gllam.SystemPrompts.SemanticExtraction
        if *useTemporal {
                if gllam.SystemPrompts.SemanticExtractionTemporal != "" {
                        systemPrompt = gllam.SystemPrompts.SemanticExtractionTemporal
                        fmt.Println("ℹ️ Using alternate temporal-ready extraction prompts.")
                } else {
                        fmt.Println("⚠️ Temporal extraction prompt requested but empty/not loaded. Falling back to default.")
                }
        }

	var dbMutex sync.Mutex
	var grandTotalNodes int64
	var grandTotalLinks int64

	// Helper function that executes extraction with JSON Schema constraint
	extractWithSchema := func(uPrompt string) (string, error) {
		// If pkg/engine/llm_client supports passing options/schema, pass extractionJSONSchema.
		// If using standard chat completion, ensure llmClient forwards response_format in the payload.
		return llmClient.GenerateWithFormat(ctx, systemPrompt, uPrompt, extractionJSONSchema)
	}

	if *trialMode {
		if len(episodes) == 0 {
			fmt.Fprintf(os.Stderr, "❌ No episodes found to run trial extraction!\n")
			os.Exit(1)
		}
		ep := episodes[0]
		chunks := engine.ChunkTranscript(ep.SummaryText, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap)
		if len(chunks) == 0 {
			fmt.Fprintf(os.Stderr, "❌ Episode %s has an empty transcript!\n", ep.ID)
			os.Exit(1)
		}
		cIdx := *trialChunk - 1
		if cIdx < 0 || cIdx >= len(chunks) {
			cIdx = 0
		}
		chunk := chunks[cIdx]

		userPrompt := fmt.Sprintf("Conversation Episode [1 / %d] (Session ID: %s | Created At: %s):\nTranscript Chunk (%d/%d):\n%s\n\nExtract semantic nodes and links:", len(episodes), ep.ID, ep.CreatedAt.Format(time.RFC3339), cIdx+1, len(chunks), chunk.Text)

		fmt.Println("================================================================================")
		fmt.Printf("🔬 TRIAL EXTRACTION MODE (Model: %s)\n", llmClient.Model)
		fmt.Printf("   ├─ Endpoint: %s\n", llmClient.BaseURL)
		fmt.Printf("   ├─ Target Episode ID: %s\n", ep.ID)
		fmt.Printf("   └─ Chunk %d of %d (%d characters)\n", cIdx+1, len(chunks), len(chunk.Text))
		fmt.Println("================================================================================")


                fmt.Println("--- SYSTEM PROMPT ---")
                fmt.Println(systemPrompt)

                fmt.Println("--- USER PROMPT ---")
                fmt.Println(userPrompt)
                
		startTime := time.Now()
		response, err := extractWithSchema(userPrompt)
		duration := time.Since(startTime)

		if err != nil {
			fmt.Printf("❌ LLM Extraction Failed: %v\n", err)
			os.Exit(1)
		}

		cleanJSON := SanitizeLLMJSON(response)
		var extData ExtractionResponse
		parseErr := json.Unmarshal([]byte(cleanJSON), &extData)

		// Populate CreatedAt and UpdatedAt timestamps for trial mode stdout printout
		now := time.Now()
		baseURI := *sourceURI
		if ep.SourceURI != "" {
			baseURI = ep.SourceURI
		}
		createdFromRef := fmt.Sprintf("%s %s#chunk-%d", strings.TrimRight(baseURI, "/"), ep.ID, cIdx+1)
		for i := range extData.Nodes {
			extData.Nodes[i].CreatedAt = now
			extData.Nodes[i].UpdatedAt = now
		}
		for i := range extData.Links {
			extData.Links[i].CreatedAt = now
			extData.Links[i].UpdatedAt = now
			extData.Links[i].CreatedFrom = createdFromRef
		}

		prettyParsedJSON, _ := json.MarshalIndent(extData, "", "  ")

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

		fmt.Println("\n--- RAW MODEL RESPONSE ---")
		fmt.Println(response)

		fmt.Println("\n--- PARSED SEMANTIC DATA (FULL JSON) ---")
		fmt.Println(string(prettyParsedJSON))

		fmt.Println("\n--- EXTRACTION QUALITY METRICS ---")
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

			chunks := engine.ChunkTranscript(episode.SummaryText, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap)
			dbMutex.Lock()
			fmt.Printf("[%s] [%d/%d] Start processing episode %s (%d chars, %d chunks)...\n", time.Now().Format("2006.01.02 15:04:05"), index+1, len(episodes), episode.ID, len(episode.SummaryText), len(chunks))
			dbMutex.Unlock()

			var epNodes int64
			var epLinks int64
			var epLLMTime, epDBWaitTime, epDBWriteTime, epEmbedTime time.Duration

			for cIdx, chunk := range chunks {
				if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
					continue
				}

				userPrompt := fmt.Sprintf("Conversation Episode [%d / %d] (Session ID: %s | Created At: %s):\nTranscript Chunk (%d/%d):\n%s\n\nExtract JSON:", index+1, len(episodes), episode.ID, episode.CreatedAt.Format(time.RFC3339), cIdx+1, len(chunks), chunk.Text)
				var extraction ExtractionResponse
				var response string
				var err error
				llmStart := time.Now()

				for attempt := 1; attempt <= 5; attempt++ {
					response, err = extractWithSchema(userPrompt)
					if err != nil {
						sleepSec := 1 << attempt
						if sleepSec > 15 {
							sleepSec = 15
						}
						time.Sleep(time.Duration(sleepSec) * time.Second)
						continue
					}

					sanitizedResponse := SanitizeLLMJSON(response)
					err = json.Unmarshal([]byte(sanitizedResponse), &extraction)
					if err == nil {
						response = sanitizedResponse
						dbMutex.Lock()
						fmt.Printf("[%s] ✅ Successfully extracted %s chunk %d (%d nodes, %d links)\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, len(extraction.Nodes), len(extraction.Links))
						fmt.Printf("--- EXTRACTED JSON START ---\n%s\n--- EXTRACTED JSON END ---\n", response)
						dbMutex.Unlock()
						break
					}

					// If unmarshaling failed, we log and retry the generation
					dbMutex.Lock()
					fmt.Printf("[%s] ⚠️ JSON unmarshal failed for %s chunk %d (attempt %d/5): %v\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, attempt, err)
					fmt.Printf("--- RAW RESPONSE ATTEMPT %d START ---\n%s\n--- RAW RESPONSE ATTEMPT %d END ---\n", attempt, response, attempt)
					dbMutex.Unlock()

					sleepSec := 1 << attempt
					if sleepSec > 15 {
						sleepSec = 15
					}
					time.Sleep(time.Duration(sleepSec) * time.Second)
				}
				epLLMTime += time.Since(llmStart)

				if err != nil {
					dbMutex.Lock()
					fmt.Printf("[%s] ❌ LLM extraction/parsing failed for %s chunk %d after 5 attempts: %v\n", time.Now().Format("2006.01.02 15:04:05"), episode.ID, cIdx+1, err)
					dbMutex.Unlock()
					continue
				}

				// Parallel Vector Generation
				type nodeVector struct {
					id  string
					vec []float32
				}
				convPrefix := ""
				if idx := strings.Index(episode.ID, "-session"); idx != -1 {
					convPrefix = episode.ID[:idx] + "-"
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
								nodeID := n.ID
								if convPrefix != "" && !strings.HasPrefix(nodeID, convPrefix) {
									nodeID = convPrefix + nodeID
								}
								nodeVecs = append(nodeVecs, nodeVector{id: nodeID, vec: vec})
								vecMutex.Unlock()
							}
						}(node)
					}
					embWg.Wait()
				}
				epEmbedTime += time.Since(embedStart)

				// Ingest into SQLite
				dbWaitStart := time.Now()
				dbMutex.Lock()
				epDBWaitTime += time.Since(dbWaitStart)

				dbWriteStart := time.Now()
				_, _ = gllam.DB().ExecContext(ctx, "BEGIN IMMEDIATE")

				for _, node := range extraction.Nodes {
					if node.ID == "" {
						continue
					}
					if convPrefix != "" && !strings.HasPrefix(node.ID, convPrefix) {
						node.ID = convPrefix + node.ID
					}
					baseURI := *sourceURI
					if episode.SourceURI != "" {
						baseURI = episode.SourceURI
					}
					node.CreatedFrom = fmt.Sprintf("%s %s#chunk-%d", strings.TrimRight(baseURI, "/"), episode.ID, cIdx+1)
					if err := gllam.UpsertNode(ctx, node); err == nil {
						epNodes++
					}
				}

				for _, link := range extraction.Links {
					if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
						continue
					}
					if convPrefix != "" {
						if !strings.HasPrefix(link.SourceID, convPrefix) {
							link.SourceID = convPrefix + link.SourceID
						}
						if !strings.HasPrefix(link.TargetID, convPrefix) {
							link.TargetID = convPrefix + link.TargetID
						}
						if link.OriginID != "" && link.OriginID != "unknown" && !strings.HasPrefix(link.OriginID, convPrefix) {
							link.OriginID = convPrefix + link.OriginID
						}
					}
					baseURI := *sourceURI
					if episode.SourceURI != "" {
						baseURI = episode.SourceURI
					}
					link.CreatedFrom = fmt.Sprintf("%s %s#chunk-%d", strings.TrimRight(baseURI, "/"), episode.ID, cIdx+1)

					if err := gllam.AddEdge(ctx, link); err != nil {
						if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
							ensureNode := func(nodeID *string) {
								if *nodeID == "" {
									return
								}
								if convPrefix != "" && !strings.HasPrefix(*nodeID, convPrefix) {
									*nodeID = convPrefix + *nodeID
								}
								var existingID string
								if errNode := gllam.DB().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE id = ? OR name = ?", *nodeID, *nodeID).Scan(&existingID); errNode == nil {
									*nodeID = existingID
									return
								}
								_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: *nodeID, Name: *nodeID, Type: "inferred", CreatedFrom: link.CreatedFrom})
							}
							ensureNode(&link.SourceID)
							ensureNode(&link.TargetID)

							if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
								epLinks++
							}
						}
					} else {
						epLinks++
					}
				}

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
				fmt.Printf("[%s] ⚠️ Finished %s (%d nodes, 0 links)\n", tsStr, episode.ID, epNodes)
			}
			dbMutex.Unlock()
		}(i, ep)
	}

	wg.Wait()
	elapsed := time.Since(startTime)
	fmt.Printf("\n🎉 Semantic extraction complete in %v! Ingested %d total nodes and %d total links across %d episodes.\n",
		elapsed.Round(time.Second), grandTotalNodes, grandTotalLinks, len(episodes))
}

// SanitizeLLMJSON removes non-breaking spaces, extracts JSON block, and auto-repairs truncated JSON
func SanitizeLLMJSON(s string) string {
	raw := []byte(s)
	// Replace non-breaking spaces (\u00a0) with regular ASCII space
	raw = bytes.ReplaceAll(raw, []byte{0xc2, 0xa0}, []byte(" "))
	s = string(raw)

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
	if start == -1 {
		return s
	}
	end := strings.LastIndex(s, "}")
	if end != -1 && end > start {
		candidate := s[start : end+1]
		var dummy interface{}
		if json.Unmarshal([]byte(candidate), &dummy) == nil {
			return candidate
		}
	}

	// Truncated JSON recovery: attempt auto-repair by closing open strings and brackets
	return repairTruncatedJSON(s[start:])
}

func repairTruncatedJSON(s string) string {
	var dummy interface{}
	if json.Unmarshal([]byte(s), &dummy) == nil {
		return s
	}

	for i := len(s) - 1; i >= 0; i-- {
		ch := s[i]
		if ch == '}' || ch == ']' || ch == ',' {
			candidate := s[:i]
			if ch != ',' {
				candidate = s[:i+1]
			}

			var stack []rune
			inString := false
			escaped := false
			for _, r := range candidate {
				if escaped {
					escaped = false
					continue
				}
				if r == '\\' {
					escaped = true
					continue
				}
				if r == '"' {
					inString = !inString
					continue
				}
				if inString {
					continue
				}
				if r == '{' || r == '[' {
					stack = append(stack, r)
				} else if r == '}' {
					if len(stack) > 0 && stack[len(stack)-1] == '{' {
						stack = stack[:len(stack)-1]
					}
				} else if r == ']' {
					if len(stack) > 0 && stack[len(stack)-1] == '[' {
						stack = stack[:len(stack)-1]
					}
				}
			}

			if inString {
				candidate += "\""
			}

			for j := len(stack) - 1; j >= 0; j-- {
				if stack[j] == '{' {
					candidate += "}"
				} else if stack[j] == '[' {
					candidate += "]"
				}
			}

			if json.Unmarshal([]byte(candidate), &dummy) == nil {
				return candidate
			}
		}
	}

	return s
}
