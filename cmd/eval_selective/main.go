package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type QAInstance struct {
	InstanceID  string `json:"instance_id"`
	Query       string `json:"query"`
	GroundTruth struct {
		Answer string `json:"answer"`
	} `json:"ground_truth"`
}

type Result struct {
	InstanceID  string `json:"instance_id"`
	Query       string `json:"query"`
	ModelAnswer string `json:"model_answer"`
	GroundTruth string `json:"ground_truth"`
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

var stopWords = map[string]bool{
	"who": true, "did": true, "as": true, "an": true, "a": true, "the": true, "is": true, "was": true,
	"for": true, "to": true, "in": true, "on": true, "at": true, "by": true, "with": true, "and": true,
	"or": true, "of": true, "it": true, "that": true, "this": true, "these": true, "those": true,
	"what": true, "where": true, "when": true, "how": true, "why": true, "which": true, "are": true,
	"were": true, "been": true, "have": true, "has": true, "had": true, "do": true, "does": true,
	"done": true, "about": true, "key": true, "points": true, "talked": true, "remember": true,
	"revisit": true, "earlier": true, "conversation": true, "you": true, "i": true, "he": true,
	"she": true, "they": true, "we": true, "me": true, "him": true, "her": true, "us": true, "them": true,
}

func clearSemanticTables(ctx context.Context, db *sql.DB) {
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_links")
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_nodes")
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_embeddings")
}

func main() {
	dbPath := flag.String("dbpath", getEnv("DATABASE_PATH", "./bench/gllam_data.db"), "Path to SQLite database")
	textServer := flag.String("text-server", getEnv("TEXT_SERVER", "http://127.0.0.1:8888"), "LLM text server endpoint (llama.cpp)")
	embeddingsServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", "http://127.0.0.1:8800"), "Embeddings server endpoint")
	corpusPath := flag.String("corpus", getEnv("CORPUS_PATH", "./bench/memarena/corpus_sessions.jsonl"), "Path to corpus file")
	qaPath := flag.String("qa", getEnv("QA_PATH", "./bench/memarena/d7_qa.jsonl"), "Path to QA jsonl file")
	outPath := flag.String("out", getEnv("OUT_PATH", "./d7_qa_results_selective.jsonl"), "Output path")
	promptsPath := flag.String("prompts-config", getEnv("PROMPTS_CONFIG", "config/agentic_memory.json"), "Path to agentic memory config")
	schemaPath := flag.String("schema-file", getEnv("EXTRACTION_SCHEMA_PATH", "./config/semantic_extraction_schema.json"), "Path to JSON schema")
	topKMatches := flag.Int("top-k", 2, "Number of top matching utterances to expand context for")
	limit := flag.Int("limit", 0, "Limit number of queries (0 for all)")
	bypassTemporal := flag.Bool("bypass-temporal", false, "Bypass JIT semantic extraction for temporal questions and answer directly from transcript")
	bypassSemantic := flag.Bool("bypass-semantic", false, "Bypass JIT semantic extraction completely and answer directly from transcript")
	useUtterancesVectors := flag.Bool("use-utterances-vectors", false, "Use turn-level vector embedding similarity search for paragraph/context retrieval")
	useTermsVectors := flag.Bool("use-terms-vectors", false, "Use semantic query expansion via term vocabulary embeddings")

	flag.Parse()

	fmt.Printf("DEBUG: use-utterances-vectors=%v, use-terms-vectors=%v, bypass-temporal=%v, bypass-semantic=%v, top-k=%d\n", *useUtterancesVectors, *useTermsVectors, *bypassTemporal, *bypassSemantic, *topKMatches)

	ctx := context.Background()

	var extractionJSONSchema map[string]interface{}
	if *schemaPath != "" {
		data, err := os.ReadFile(*schemaPath)
		if err == nil {
			_ = json.Unmarshal(data, &extractionJSONSchema)
		}
	}

	embedder := engine.NewLlamaEmbedder(*embeddingsServer)
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.LoadSystemPromptsConfig(*promptsPath); err != nil {
		fmt.Printf("⚠️ Could not load prompts config from %s: %v\n", *promptsPath, err)
	}

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Building postings index from corpus...")
	startIdx := time.Now()
	idx, err := engine.BuildInvertedIndex(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build postings index: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Index built in %v. Utterances: %d, Terms: %d\n", time.Since(startIdx).Round(time.Millisecond), len(idx.Utterances), len(idx.Postings))

	if *useUtterancesVectors {
		if err := ensureUtteranceEmbeddingsIndexed(ctx, gllam, embedder, idx); err != nil {
			fmt.Fprintf(os.Stderr, "Utterance vector indexing failed: %v\n", err)
			os.Exit(1)
		}
	}

	if *useTermsVectors {
		if err := ensureTermEmbeddingsIndexed(ctx, gllam, embedder, idx); err != nil {
			fmt.Fprintf(os.Stderr, "Term vector indexing failed: %v\n", err)
			os.Exit(1)
		}
	}

	qaFile, err := os.Open(*qaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open QA file: %v\n", err)
		os.Exit(1)
	}
	defer qaFile.Close()

	outFile, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	scanner := bufio.NewScanner(qaFile)
	count := 0
	llmClient := engine.NewLLMClient(*textServer)

	for scanner.Scan() {
		if *limit > 0 && count >= *limit {
			break
		}

		line := scanner.Bytes()
		var qa QAInstance
		if err := json.Unmarshal(line, &qa); err != nil {
			continue
		}

		fmt.Println(strings.Repeat("=", 100))
		fmt.Printf("Processing [%s]: %s\n", qa.InstanceID, qa.Query)

		// 1. Clear semantic database tables for fresh query
		clearSemanticTables(ctx, gllam.DB())

		targetSpeakers := extractTargetSpeakers(qa.Query, idx)
		if len(targetSpeakers) > 0 {
			fmt.Printf("   ├─ Target speakers detected in question: %v\n", targetSpeakers)
		}

		// 2. Retrieve top matching utterances
		var selectedUtteranceIDs []string
		if *useUtterancesVectors {
			fmt.Printf("   ├─ Retrieving top-%d matching paragraphs via Hybrid Search (TF-IDF + Vector RRF)...\n", *topKMatches)

			// 2a. Run TF-IDF search to get ranked list (up to 100)
			queryTokens := engine.Tokenize(qa.Query)
			var searchTerms []string
			for _, tok := range queryTokens {
				if !stopWords[tok] {
					searchTerms = append(searchTerms, tok)
				}
			}

			if *useTermsVectors {
				fmt.Printf("   ├─ Expanding query vocabulary via term embeddings...\n")
				var expandedTerms []string
				for _, term := range searchTerms {
					tEmb, err := embedder.Embed(ctx, term)
					if err != nil {
						continue
					}
					similar, err := gllam.SearchSimilarTerms(ctx, tEmb, 4)
					if err == nil {
						for _, sim := range similar {
							expandedTerms = append(expandedTerms, sim.Term)
						}
					}
				}
				termSet := make(map[string]bool)
				for _, t := range searchTerms {
					termSet[t] = true
				}
				for _, t := range expandedTerms {
					if !termSet[t] {
						termSet[t] = true
						searchTerms = append(searchTerms, t)
					}
				}
				fmt.Printf("   ├─ Expanded search terms: %v\n", searchTerms)
			}

			uttScores := make(map[string]float64)
			totalUtterances := float64(len(idx.Utterances))
			for _, term := range searchTerms {
				postings, ok := idx.Postings[term]
				if !ok {
					continue
				}
				df := float64(len(postings))
				if df == 0 {
					continue
				}
				idf := math.Log(totalUtterances / df)
				for _, p := range postings {
					uttScores[p.UtteranceID] += float64(p.Frequency) * idf
				}
			}

			type scoreEntry struct {
				id    string
				score float64
			}
			var tfidfList []scoreEntry
			for id, sc := range uttScores {
				tfidfList = append(tfidfList, scoreEntry{id: id, score: sc})
			}
			sort.Slice(tfidfList, func(i, j int) bool {
				return tfidfList[i].score > tfidfList[j].score
			})

			// 2b. Run Vector search to get ranked list (up to 100)
			qEmb, err := embedder.Embed(ctx, qa.Query)
			if err != nil {
				fmt.Fprintf(os.Stderr, "   ❌ Failed to embed query: %v\n", err)
				os.Exit(1)
			}
			vecMatches, err := gllam.SearchSimilarUtterances(ctx, qEmb, 100)
			if err != nil {
				fmt.Fprintf(os.Stderr, "   ❌ Vector search failed: %v\n", err)
				os.Exit(1)
			}

			// 2c. Reciprocal Rank Fusion (RRF)
			rrfScores := make(map[string]float64)
			const kRRF = 60.0

			for rank, entry := range tfidfList {
				if rank >= 100 {
					break
				}
				rrfScores[entry.id] += 1.0 / (kRRF + float64(rank+1))
			}

			for rank, entry := range vecMatches {
				rrfScores[entry.UtteranceID] += 1.0 / (kRRF + float64(rank+1))
			}

			// Apply speaker focus boost on RRF scores
			if len(targetSpeakers) > 0 {
				for id, score := range rrfScores {
					u, ok := idx.Utterances[id]
					if ok && matchesAnySpeaker(u.SpeakerID, targetSpeakers) {
						rrfScores[id] = score * 10.0
					}
				}
			}

			type rrfEntry struct {
				id    string
				score float64
			}
			var rrfList []rrfEntry
			for id, sc := range rrfScores {
				rrfList = append(rrfList, rrfEntry{id: id, score: sc})
			}
			sort.Slice(rrfList, func(i, j int) bool {
				return rrfList[i].score > rrfList[j].score
			})

			limit := *topKMatches
			if len(rrfList) < limit {
				limit = len(rrfList)
			}
			for i := 0; i < limit; i++ {
				selectedUtteranceIDs = append(selectedUtteranceIDs, rrfList[i].id)
			}
		} else {
			// Tokenize query and filter stop words
			queryTokens := engine.Tokenize(qa.Query)
			var searchTerms []string
			for _, tok := range queryTokens {
				if !stopWords[tok] {
					searchTerms = append(searchTerms, tok)
				}
			}

			if *useTermsVectors {
				fmt.Printf("   ├─ Expanding query vocabulary via term embeddings...\n")
				var expandedTerms []string
				for _, term := range searchTerms {
					tEmb, err := embedder.Embed(ctx, term)
					if err != nil {
						continue
					}
					similar, err := gllam.SearchSimilarTerms(ctx, tEmb, 4)
					if err == nil {
						for _, sim := range similar {
							expandedTerms = append(expandedTerms, sim.Term)
						}
					}
				}
				termSet := make(map[string]bool)
				for _, t := range searchTerms {
					termSet[t] = true
				}
				for _, t := range expandedTerms {
					if !termSet[t] {
						termSet[t] = true
						searchTerms = append(searchTerms, t)
					}
				}
				fmt.Printf("   ├─ Expanded search terms: %v\n", searchTerms)
			}

			// Score utterances based on TF-IDF to prioritize rare terms like names and entities
			uttScores := make(map[string]float64)
			totalUtterances := float64(len(idx.Utterances))
			for _, term := range searchTerms {
				postings, ok := idx.Postings[term]
				if !ok {
					continue
				}
				df := float64(len(postings))
				if df == 0 {
					continue
				}
				// Compute IDF
				idf := math.Log(totalUtterances / df)
				for _, p := range postings {
					score := float64(p.Frequency) * idf
					u, ok := idx.Utterances[p.UtteranceID]
					if ok && matchesAnySpeaker(u.SpeakerID, targetSpeakers) {
						score *= 10.0
					}
					uttScores[p.UtteranceID] += score
				}
			}

			type scoreEntry struct {
				id    string
				score float64
			}
			var scoredList []scoreEntry
			for id, sc := range uttScores {
				scoredList = append(scoredList, scoreEntry{id: id, score: sc})
			}
			sort.Slice(scoredList, func(i, j int) bool {
				return scoredList[i].score > scoredList[j].score
			})

			limit := *topKMatches
			if len(scoredList) < limit {
				limit = len(scoredList)
			}
			for i := 0; i < limit; i++ {
				selectedUtteranceIDs = append(selectedUtteranceIDs, scoredList[i].id)
			}
		}

		// Gather context expanded utterances
		expandedSet := make(map[string]bool)
		var expandedUtterances []engine.CorpusUtterance

		for _, matchID := range selectedUtteranceIDs {
			matchUtt, ok := idx.Utterances[matchID]
			if !ok {
				continue
			}
			sessUtteranceIDs, ok := idx.Sessions[matchUtt.SessionID]
			if !ok {
				continue
			}

			// Find index of matchID in session list
			matchIdx := -1
			for j, id := range sessUtteranceIDs {
				if id == matchID {
					matchIdx = j
					break
				}
			}

			if matchIdx != -1 {
				start := matchIdx - 2
				if start < 0 {
					start = 0
				}
				end := matchIdx + 5
				if end >= len(sessUtteranceIDs) {
					end = len(sessUtteranceIDs) - 1
				}

				for j := start; j <= end; j++ {
					id := sessUtteranceIDs[j]
					if !expandedSet[id] {
						expandedSet[id] = true
						expandedUtterances = append(expandedUtterances, idx.Utterances[id])
					}
				}
			}
		}

		// Sort expanded utterances by line number and byte offsets to maintain dialogue order
		sort.Slice(expandedUtterances, func(i, j int) bool {
			if expandedUtterances[i].LineNumber == expandedUtterances[j].LineNumber {
				return expandedUtterances[i].StartByte < expandedUtterances[j].StartByte
			}
			return expandedUtterances[i].LineNumber < expandedUtterances[j].LineNumber
		})

		// Rebuild dialogue transcript text
		var transcriptBuilder strings.Builder
		for _, u := range expandedUtterances {
			transcriptBuilder.WriteString(fmt.Sprintf("%s: %s\n", u.SpeakerID, u.Text))
		}
		transcriptText := cleanTranscriptSAYArtifacts(transcriptBuilder.String())

		fmt.Printf("   ├─ Retrieved & expanded context size: %d turns (%d characters)\n", len(expandedUtterances), len(transcriptText))

		// 5. Try Direct QA First Pass
		fmt.Printf("   ├─ Attempting Direct QA first-pass...\n")
		directSystemPrompt := gllam.SystemPrompts.DirectQAPrompt

		fmt.Println("--- LLM Direct QA System Prompt ---")
		fmt.Println(directSystemPrompt)
		fmt.Println("--- LLM Direct QA User Prompt ---")
		fmt.Printf("Transcript:\n%s\n\nQuestion: %s\n", transcriptText, qa.Query)
		fmt.Println("----------------------------------")

		directAnswer, err := tryDirectQA(ctx, llmClient, directSystemPrompt, transcriptText, qa.Query)
		var answer string
		isTemporal := strings.HasPrefix(strings.ToUpper(directAnswer), "TEMPORAL")
		isNotFound := strings.ToUpper(directAnswer) == "ANSWER_NOT_FOUND"

		if err == nil && !isTemporal && !isNotFound && directAnswer != "" {
			answer = directAnswer
			fmt.Printf("   ├─ ✅ First-pass Direct QA succeeded.\n")
		} else if *bypassSemantic || (isTemporal && *bypassTemporal) {
			reason := "TEMPORAL with --bypass-temporal"
			if *bypassSemantic {
				reason = "--bypass-semantic"
			}
			fmt.Printf("   ├─ ⚠️ Bypassing JIT semantic extraction (%s). Answering directly from transcript...\n", reason)
			directPrompt := gllam.SystemPrompts.SimpleTemporalRetrieval
			if directPrompt == "" {
				directPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript. Pay absolute attention to the chronological sequence of lines in the transcript. Determine who speaks first and who speaks second, and trace the sequence of statements. Answer the temporal ordering question precisely and directly."
			}
			userPrompt := fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query)
			
			fmt.Println("--- LLM Direct QA Bypassed Prompt ---")
			fmt.Println(directPrompt)
			fmt.Printf("User:\n%s\n", userPrompt)
			fmt.Println("------------------------------------")
			
			answer, err = llmClient.Generate(ctx, directPrompt, userPrompt)
			if err != nil {
				answer = "ERROR"
			}
		} else {
			fmt.Printf("   ├─ ❌ Direct QA returned %s. Falling back to JIT semantic extraction...\n", directAnswer)

			// 6. Extract semantics just-in-time
			nodes, links, err := extractSemanticsForText(ctx, gllam, embedder, llmClient, transcriptText, gllam.SystemPrompts.SemanticExtraction, extractionJSONSchema)
			if err != nil {
				fmt.Printf("   ❌ Semantic extraction failed: %v\n", err)
			} else {
				fmt.Printf("   ├─ Extracted JIT: %d nodes, %d links\n", nodes, links)
			}

			// 7. Route and Assemble semantic context & answer query
			compiled, err := gllam.RouteAndAssemble(ctx, qa.Query, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "   ❌ Error routing query: %v\n", err)
				answer = "ERROR"
			} else {
				prompt := engine.FormatSystemPrompt(compiled)
				userQuery := fmt.Sprintf("Discussion Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query)

				fmt.Println("--- LLM Final Q&A System Prompt ---")
				fmt.Println(prompt)
				fmt.Println("--- LLM Final Q&A User Prompt ---")
				fmt.Println(userQuery)
				fmt.Println("---------------------------------")

				var genErr error
				for attempt := 1; attempt <= 3; attempt++ {
					answer, genErr = llmClient.Generate(ctx, prompt, userQuery)
					if genErr == nil && strings.TrimSpace(answer) != "" {
						break
					}
					time.Sleep(time.Duration(attempt) * time.Second)
				}
				if genErr != nil {
					answer = "ERROR"
				}
			}
		}

		answer = stripThinkingTags(answer)
		res := Result{
			InstanceID:  qa.InstanceID,
			Query:       qa.Query,
			ModelAnswer: answer,
			GroundTruth: qa.GroundTruth.Answer,
		}

		resBytes, _ := json.Marshal(res)
		outFile.Write(resBytes)
		outFile.WriteString("\n")

		count++
		fmt.Printf("   └─ Answer: %s\n", strings.ReplaceAll(strings.Split(answer, "\n")[0], "\r", ""))
	}

	fmt.Printf("\nCompleted %d evaluations. Results saved to %s\n", count, *outPath)
}

func extractSemanticsForText(ctx context.Context, gllam *engine.GllamEngine, embedder engine.Embedder, llmClient *engine.LLMClient, text string, systemPrompt string, extractionJSONSchema map[string]interface{}) (int, int, error) {
	chunks := engine.ChunkTranscript(text, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap)

	var nodesCount, linksCount int
	for cIdx, chunk := range chunks {
		if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
			continue
		}

		userPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nExtract JSON:", cIdx+1, len(chunks), chunk.Text)

		fmt.Println("--- LLM JIT Semantic Extraction System Prompt ---")
		fmt.Println(systemPrompt)
		fmt.Println("--- LLM JIT Semantic Extraction User Prompt ---")
		fmt.Println(userPrompt)
		fmt.Println("-------------------------------------------------")

		response, err := llmClient.GenerateWithFormat(ctx, systemPrompt, userPrompt, extractionJSONSchema)
		if err != nil {
			fmt.Printf("   ❌ LLM GenerateWithFormat error: %v\n", err)
			return 0, 0, err
		}

		sanitized := SanitizeLLMJSON(response)
		var extraction struct {
			Nodes []memory.SemanticNode `json:"nodes"`
			Links []memory.SemanticLink `json:"links"`
		}
		if err := json.Unmarshal([]byte(sanitized), &extraction); err != nil {
			fmt.Printf("   ❌ Failed to parse JSON from LLM: %v\n", err)
			fmt.Printf("   [RAW RESPONSE]:\n%s\n", response)
			fmt.Printf("   [SANITIZED RESPONSE]:\n%s\n", sanitized)
			continue
		}

		fmt.Printf("   ├─ Chunk %d/%d: Extracted %d nodes, %d links from this chunk.\n", cIdx+1, len(chunks), len(extraction.Nodes), len(extraction.Links))
		if len(extraction.Nodes) == 0 && len(extraction.Links) == 0 {
			fmt.Printf("   [RAW RESPONSE]:\n%s\n", response)
		}

		// Ingest into SQLite
		_, _ = gllam.DB().ExecContext(ctx, "BEGIN IMMEDIATE")

		type nodeVector struct {
			id  string
			vec []float32
		}
		var nodeVecs []nodeVector

		for _, node := range extraction.Nodes {
			if node.ID == "" {
				continue
			}
			node.CreatedFrom = fmt.Sprintf("selective_chunk-%d", cIdx+1)
			if err := gllam.UpsertNode(ctx, node); err == nil {
				nodesCount++
				if vec, err := embedder.Embed(ctx, node.Name); err == nil && len(vec) > 0 {
					nodeVecs = append(nodeVecs, nodeVector{id: node.ID, vec: vec})
				}
			}
		}

		for _, link := range extraction.Links {
			if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
				continue
			}
			link.CreatedFrom = fmt.Sprintf("selective_chunk-%d", cIdx+1)
			if err := gllam.AddEdge(ctx, link); err != nil {
				_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.SourceID, Name: link.SourceID, Type: "inferred", CreatedFrom: link.CreatedFrom})
				_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.TargetID, Name: link.TargetID, Type: "inferred", CreatedFrom: link.CreatedFrom})
				if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
					linksCount++
				}
			} else {
				linksCount++
			}
		}

		for _, nv := range nodeVecs {
			_ = gllam.IndexNodeVector(ctx, nv.id, nv.vec)
		}

		_, _ = gllam.DB().ExecContext(ctx, "COMMIT")
	}

	return nodesCount, linksCount, nil
}

func SanitizeLLMJSON(s string) string {
	raw := []byte(s)
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
	end := strings.LastIndex(s, "}")
	if start != -1 && end != -1 && end > start {
		s = s[start : end+1]
	}
	return strings.TrimSpace(s)
}

func cleanTranscriptSAYArtifacts(text string) string {
	lines := strings.Split(text, "\n")
	lastSpeaker := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "say:") || strings.HasPrefix(lower, "say :") {
			colonIdx := strings.Index(line, ":")
			if colonIdx != -1 && lastSpeaker != "" {
				lines[i] = lastSpeaker + ":" + line[colonIdx+1:]
			}
		} else {
			colonIdx := strings.Index(line, ":")
			if colonIdx != -1 {
				potentialSpeaker := strings.TrimSpace(line[:colonIdx])
				if !strings.Contains(potentialSpeaker, " ") {
					lastSpeaker = potentialSpeaker
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func tryDirectQA(ctx context.Context, llmClient *engine.LLMClient, systemPrompt string, transcript string, query string) (string, error) {
	userPrompt := fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcript, query)
	answer, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(answer), nil
}

func ensureUtteranceEmbeddingsIndexed(ctx context.Context, gllam *engine.GllamEngine, embedder engine.Embedder, idx *engine.InvertedIndex) error {
	var count int
	err := gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='utterance_embeddings'").Scan(&count)
	if err != nil || count == 0 {
		return fmt.Errorf("utterance_embeddings table does not exist or schema not initialized: %v", err)
	}

	err = gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM utterance_embeddings").Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		fmt.Printf("   ├─ Found %d utterance embeddings pre-indexed in DB.\n", count)
		return nil
	}

	fmt.Printf("   ├─ ⚙️ Embedding and indexing %d utterances into DB (one-time setup)...\n", len(idx.Utterances))
	
	type task struct {
		id   string
		text string
	}
	
	tasks := make(chan task, len(idx.Utterances))
	for id, u := range idx.Utterances {
		tasks <- task{id: id, text: u.Text}
	}
	close(tasks)

	numWorkers := 40
	var wg sync.WaitGroup
	var progress int64
	var mu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				vec, err := embedder.Embed(ctx, t.text)
				if err != nil {
					continue
				}
				mu.Lock()
				_ = gllam.IndexUtteranceVector(ctx, t.id, vec)
				mu.Unlock()

				curr := atomic.AddInt64(&progress, 1)
				if curr%1000 == 0 {
					fmt.Printf("      -> Embedded %d/%d utterances...\n", curr, len(idx.Utterances))
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("   ├─ Finished indexing %d utterance embeddings!\n", progress)
	return nil
}

func ensureTermEmbeddingsIndexed(ctx context.Context, gllam *engine.GllamEngine, embedder engine.Embedder, idx *engine.InvertedIndex) error {
	var count int
	err := gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='term_embeddings'").Scan(&count)
	if err != nil || count == 0 {
		return fmt.Errorf("term_embeddings table does not exist or schema not initialized: %v", err)
	}

	err = gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM term_embeddings").Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		fmt.Printf("   ├─ Found %d vocabulary term embeddings pre-indexed in DB.\n", count)
		return nil
	}

	fmt.Printf("   ├─ ⚙️ Embedding and indexing %d unique vocabulary terms into DB (one-time setup)...\n", len(idx.Postings))
	
	var termsList []string
	for term := range idx.Postings {
		termsList = append(termsList, term)
	}

	type task struct {
		term string
	}
	
	tasks := make(chan task, len(termsList))
	for _, term := range termsList {
		tasks <- task{term: term}
	}
	close(tasks)

	numWorkers := 40
	var wg sync.WaitGroup
	var progress int64
	var mu sync.Mutex

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range tasks {
				vec, err := embedder.Embed(ctx, t.term)
				if err != nil {
					continue
				}
				mu.Lock()
				_ = gllam.IndexTermVector(ctx, t.term, vec)
				mu.Unlock()

				curr := atomic.AddInt64(&progress, 1)
				if curr%2000 == 0 {
					fmt.Printf("      -> Embedded %d/%d terms...\n", curr, len(termsList))
				}
			}
		}()
	}
	wg.Wait()
	fmt.Printf("   ├─ Finished indexing %d term embeddings!\n", progress)
	return nil
}

func extractTargetSpeakers(query string, idx *engine.InvertedIndex) []string {
	speakers := make(map[string]bool)
	for _, u := range idx.Utterances {
		name := strings.ToLower(u.SpeakerID)
		if name == "" || name == "unknown" || name == "system" {
			continue
		}
		parts := strings.Split(name, "_")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if len(trimmed) > 2 {
				speakers[trimmed] = true
			}
		}
		speakers[name] = true
	}

	lowerQuery := strings.ToLower(query)
	var found []string
	for name := range speakers {
		if strings.Contains(lowerQuery, name) {
			found = append(found, name)
		}
	}
	return found
}

func matchesAnySpeaker(speakerID string, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	lowerSpeaker := strings.ToLower(speakerID)
	for _, t := range targets {
		if strings.Contains(lowerSpeaker, t) {
			return true
		}
	}
	return false
}

func stripThinkingTags(s string) string {
	for {
		start := strings.Index(s, "<thinking>")
		end := strings.Index(s, "</thinking>")
		if start != -1 && end != -1 && end > start {
			s = s[:start] + s[end+11:]
		} else {
			break
		}
	}
	return strings.TrimSpace(s)
}
