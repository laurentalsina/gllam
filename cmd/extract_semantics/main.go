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
	resumeExtraction := flag.Bool("resume", true, "Skip sessions that have already been extracted in previous runs")
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

	// Create checkpoint table for resumable extraction
	_, _ = gllam.DB().ExecContext(ctx, `CREATE TABLE IF NOT EXISTS extracted_sessions (
		session_id TEXT PRIMARY KEY,
		extracted_at DATETIME,
		node_count INTEGER,
		link_count INTEGER
	)`)

	if *cleanSemantics {
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

	if *resumeExtraction && !*cleanSemantics {
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

CRITICAL JSON RULE: Never put unescaped double quotes inside string property values (such as context_prompt, caveats, name, temporal_note). Use single quotes or plain text for quotes inside values.

CRITICAL GRAPH CONSTRAINT: You MUST extract semantic relationships (links) connecting every extracted node! Do not output floating disconnected nodes; every extracted entity, event, state, human, or preference must be connected by at least one link.

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

			var epLLMTime time.Duration
			var epDBTime time.Duration
			var epEmbedTime time.Duration

			for cIdx, chunk := range chunks {
				if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
					continue
				}

				userPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nExtract JSON:", cIdx+1, len(chunks), chunk.Text)
				var response string
				var err error
				llmStart := time.Now()
				for attempt := 1; attempt <= 3; attempt++ {
					response, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
					if err == nil && strings.TrimSpace(response) != "" {
						break
					}
					if err == nil {
						err = fmt.Errorf("empty response from LLM")
					}
					dbMutex.Lock()
					fmt.Printf("⚠️ OpenRouter error for %s chunk %d (attempt %d/3): %v. Retrying in %ds...\n", episode.ID, cIdx+1, attempt, err, attempt*2)
					dbMutex.Unlock()
					time.Sleep(time.Duration(attempt*2) * time.Second)
				}
				epLLMTime += time.Since(llmStart)

				if err != nil {
					dbMutex.Lock()
					fmt.Printf("❌ LLM extraction failed for %s chunk %d after 3 attempts: %v\n", episode.ID, cIdx+1, err)
					dbMutex.Unlock()
					continue
				}

				response = SanitizeLLMJSON(response)

				var extraction ExtractionResponse
				if err := json.Unmarshal([]byte(response), &extraction); err != nil {
					repaired := RepairTruncatedJSON(response)
					if err2 := json.Unmarshal([]byte(repaired), &extraction); err2 == nil {
						response = repaired
					} else {
						dbMutex.Lock()
						errToReport := err
						if err2 != nil {
							errToReport = err2
						}
						offset := 0
						if syntaxErr, ok := errToReport.(*json.SyntaxError); ok {
							offset = int(syntaxErr.Offset)
						}
						sample := response
						if offset > 0 && offset < len(response) {
							subStart := offset - 40
							if subStart < 0 {
								subStart = 0
							}
							subEnd := offset + 40
							if subEnd > len(response) {
								subEnd = len(response)
							}
							sample = fmt.Sprintf("...%s...", response[subStart:subEnd])
						} else if len(sample) > 200 {
							sample = sample[:200] + "..."
						}
						fmt.Printf("⚠️ JSON parse error for %s chunk %d at offset %d: %v | Context: %q\n", episode.ID, cIdx+1, offset, errToReport, sample)
						dbMutex.Unlock()
						continue
					}
				}

				var insertedNodeIDs []string
				dbStart := time.Now()
				dbMutex.Lock()
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
								if errNode := gllam.DBRO().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE id = ? OR name = ?", *nodeID, *nodeID).Scan(&existingID); errNode == nil {
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
								fmt.Printf("⚠️ AddEdge retry failed for %s link %s->%s: %v\n", episode.ID, link.SourceID, link.TargetID, retryErr)
							}
						} else {
							fmt.Printf("⚠️ AddEdge failed for %s link %s->%s: %v\n", episode.ID, link.SourceID, link.TargetID, err)
						}
					} else {
						epLinks++
					}
				}
				dbMutex.Unlock()
				epDBTime += time.Since(dbStart)

				// Store embeddings outside of dbMutex to prevent worker serialization (parallelized across 5 workers)
				embedStart := time.Now()
				if len(insertedNodeIDs) > 0 {
					var embWg sync.WaitGroup
					embSem := make(chan struct{}, 5)
					for _, nid := range insertedNodeIDs {
						embWg.Add(1)
						embSem <- struct{}{}
						go func(id string) {
							defer embWg.Done()
							defer func() { <-embSem }()
							_ = gllam.StoreNodeEmbedding(ctx, id)
						}(nid)
					}
					embWg.Wait()
				}
				epEmbedTime += time.Since(embedStart)
			}

			atomic.AddInt64(&grandTotalNodes, epNodes)
			atomic.AddInt64(&grandTotalLinks, epLinks)

			dbMutex.Lock()
			if epNodes > 0 || epLinks > 0 {
				_, _ = gllam.DB().ExecContext(ctx, "INSERT OR REPLACE INTO extracted_sessions (session_id, extracted_at, node_count, link_count) VALUES (?, ?, ?, ?)", episode.SessionID, time.Now(), epNodes, epLinks)
				fmt.Printf("✅ Finished %s (%d nodes, %d links) | LLM: %v, DB: %v, Embed: %v\n", episode.ID, epNodes, epLinks, epLLMTime.Round(time.Millisecond), epDBTime.Round(time.Millisecond), epEmbedTime.Round(time.Millisecond))
			} else {
				fmt.Printf("⚠️ Finished %s with 0 nodes and 0 links (will be retried on next resume run)\n", episode.ID)
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
