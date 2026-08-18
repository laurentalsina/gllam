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

	flag.Parse()

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

		fmt.Printf("\nProcessing [%s]: %s\n", qa.InstanceID, qa.Query)

		// 1. Clear semantic database tables for fresh query
		clearSemanticTables(ctx, gllam.DB())

		// 2. Tokenize query and filter stop words
		queryTokens := engine.Tokenize(qa.Query)
		var searchTerms []string
		for _, tok := range queryTokens {
			if !stopWords[tok] {
				searchTerms = append(searchTerms, tok)
			}
		}

		// 3. Score utterances based on TF-IDF to prioritize rare terms like names and entities
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
				uttScores[p.UtteranceID] += float64(p.Frequency) * idf
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

		// 4. Retrieve top matches and expand context (2 before, 5 after)
		var selectedUtteranceIDs []string
		limit := *topKMatches
		if len(scoredList) < limit {
			limit = len(scoredList)
		}
		for i := 0; i < limit; i++ {
			selectedUtteranceIDs = append(selectedUtteranceIDs, scoredList[i].id)
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
		fmt.Print(transcriptText)

		// 5. Try Direct QA First Pass
		fmt.Printf("   ├─ Attempting Direct QA first-pass...\n")
		directSystemPrompt := `You are a helpful assistant. You will be provided with a conversation transcript and a question.
Your task is to:
1. First, analyze if the question has a temporal sequencing component (e.g. asking whether an event happened "before" or "after" another, or requesting a specific chronological order/sequence of events).
   - If the question contains a temporal ordering/sequencing component, reply exactly "NOT_FOUND" and do not output anything else.
2. If there is no temporal sequencing component, try to answer the question strictly using facts directly stated in the transcript.
   - If the transcript contains the answer, write a concise 1-sentence answer.
   - If the transcript does NOT contain the answer, reply exactly "NOT_FOUND". Do not explain or output anything else.`

		fmt.Println("--- LLM Direct QA System Prompt ---")
		fmt.Println(directSystemPrompt)
		fmt.Println("--- LLM Direct QA User Prompt ---")
		fmt.Printf("Transcript:\n%s\n\nQuestion: %s\n", transcriptText, qa.Query)
		fmt.Println("----------------------------------")

		directAnswer, err := tryDirectQA(ctx, llmClient, directSystemPrompt, transcriptText, qa.Query)
		var answer string
		if err == nil && directAnswer != "NOT_FOUND" && directAnswer != "" {
			answer = directAnswer
			fmt.Printf("   ├─ ✅ First-pass Direct QA succeeded.\n")
		} else {
			fmt.Printf("   ├─ ❌ Direct QA returned NOT_FOUND/error. Falling back to JIT semantic extraction...\n")

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
			return 0, 0, err
		}

		response = SanitizeLLMJSON(response)
		var extraction struct {
			Nodes []memory.SemanticNode `json:"nodes"`
			Links []memory.SemanticLink `json:"links"`
		}
		if err := json.Unmarshal([]byte(response), &extraction); err != nil {
			continue
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
