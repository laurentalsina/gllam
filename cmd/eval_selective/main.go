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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/laurentalsina/gllam/pkg/engine"
	"github.com/laurentalsina/gllam/pkg/memory"
)

type QAInstance struct {
	InstanceID     string      `json:"instance_id"`
	ConversationID string      `json:"conversation_id"`
	Category       string      `json:"category"`
	Query          string      `json:"query"`
	GroundTruth    interface{} `json:"ground_truth"`
	Rubric         []string    `json:"rubric"`
}

type Result struct {
	InstanceID   string   `json:"instance_id"`
	Category     string   `json:"category"`
	Query        string   `json:"query"`
	ModelAnswer  string   `json:"model_answer"`
	GroundTruth  string   `json:"ground_truth"`
	Rubric       []string `json:"rubric"`
}

type CandidateInfo struct {
	UtteranceID string `json:"utterance_id"`
	Speaker     string `json:"speaker"`
	Text        string `json:"text"`
	SessionID   string `json:"session_id"`
}

type ChunkPruningEvent struct {
	ChunkIndex   int    `json:"chunk_index"`
	Text         string `json:"text"`
	SystemPrompt string `json:"system_prompt"`
	UserPrompt   string `json:"user_prompt"`
	LlmDecision  string `json:"llm_decision"`
}

type ChunkPruningInfo struct {
	PruningEnabled bool                `json:"pruning_enabled"`
	Chunks         []ChunkPruningEvent `json:"chunks"`
}

type FirstPassDirectQAInfo struct {
	Attempted    bool   `json:"attempted"`
	SystemPrompt string `json:"system_prompt"`
	UserPrompt   string `json:"user_prompt"`
	Response     string `json:"response"`
	IsTemporal   bool   `json:"is_temporal"`
	IsNotFound   bool   `json:"is_not_found"`
}

type JITExtractionEvent struct {
	ChunkIndex           int      `json:"chunk_index"`
	SystemPrompt         string   `json:"system_prompt"`
	UserPrompt           string   `json:"user_prompt"`
	RawResponse          string   `json:"raw_response"`
	SanitizedJSON        string   `json:"sanitized_json"`
	NodesExtracted       int      `json:"nodes_extracted"`
	LinksExtracted       int      `json:"links_extracted"`
	CanonicalizationLogs []string `json:"canonicalization_logs"`
}

type FinalQAInfo struct {
	CompiledContext   interface{} `json:"compiled_context"`
	SystemPrompt      string      `json:"system_prompt"`
	UserPrompt        string      `json:"user_prompt"`
	Response          string      `json:"response"`
	FallbackTriggered bool        `json:"fallback_triggered"`
	FallbackResponse  string      `json:"fallback_response"`
	PDDLDomainPath    string      `json:"pddl_domain_path,omitempty"`
	PDDLProblemPath   string      `json:"pddl_problem_path,omitempty"`
}

type StructuredDetailsLog struct {
	InstanceID          string                 `json:"instance_id"`
	Query               string                 `json:"query"`
	DecomposedQueries   []string               `json:"decomposed_queries"`
	SearchTerms         []string               `json:"search_terms"`
	RetrievedCandidates []CandidateInfo        `json:"retrieved_candidates"`
	ChunkPruning        ChunkPruningInfo       `json:"chunk_pruning"`
	FirstPassDirectQA   FirstPassDirectQAInfo  `json:"first_pass_direct_qa"`
	JITExtractions      []JITExtractionEvent   `json:"jit_extractions"`
	FinalQA             FinalQAInfo            `json:"final_qa"`
}


func getGroundTruthAnswer(gt interface{}) string {
	if gt == nil {
		return ""
	}
	if s, ok := gt.(string); ok {
		return s
	}
	if m, ok := gt.(map[string]interface{}); ok {
		if ans, ok := m["answer"].(string); ok {
			return ans
		}
	}
	return ""
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
	"my": true, "myself": true, "your": true, "yours": true, "our": true, "ours": true, "their": true, "theirs": true, "its": true,
}

var nonExpandableTerms = map[string]bool{
    "you": true, "i": true, "he": true, "she": true, "they": true, "we": true, "me": true,
    "him": true, "her": true, "us": true, "them": true, "myself": true, "your": true, "my": true,
    "our": true, "their": true, "any": true, "some": true, "there": true, "here": true,
    "this": true, "that": true, "these": true, "those": true,
    "many": true, "both": true, "either": true, "about": true, "for": true, "to": true,
    "with": true, "and": true, "or": true, "of": true, "it": true, "as": true, "an": true,
    "a": true, "the": true, "who": true, "what": true, "where": true, "how": true, "why": true,
    "which": true, "do": true, "does": true, "done": true,
    "key": true, "points": true, "conversation": true,
}

func clearSemanticTables(ctx context.Context, db *sql.DB) {
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_links")
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_nodes")
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_embeddings")
	_, _ = db.ExecContext(ctx, "DELETE FROM semantic_temporal_links")
	_, _ = db.ExecContext(ctx, "DELETE FROM document_lineage")
	_, _ = db.ExecContext(ctx, "DELETE FROM document_versions")
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
	categories := flag.String("categories", "", "Comma-separated list of categories to evaluate (all, preference_following, temporal_reasoning, event_ordering, knowledge_update, summarization, instruction_following, information_extraction, contradiction_resolution, multi_session_reasoning, abstention)")
	debug := flag.Bool("debug", false, "Print verbose debugging information about JIT processing steps")
	pruneClueChunks := flag.Bool("prune-clue-chunks", false, "Prune irrelevant transcript chunks using a fast LLM YES/NO classifier pass")
	bypassTemporal := flag.Bool("bypass-temporal", false, "Bypass JIT semantic extraction for temporal questions and answer directly from transcript")
	bypassSemantic := flag.Bool("bypass-semantic", false, "Bypass JIT semantic extraction completely and answer directly from transcript")
	useUtterancesVectors := flag.Bool("use-utterances-vectors", false, "Use turn-level vector embedding similarity search for paragraph/context retrieval")
	useTermsVectors := flag.Bool("use-terms-vectors", false, "Use semantic query expansion via term vocabulary embeddings")
	decomposeQueryFlag := flag.Bool("decompose-query", false, "Decompose complex questions into sub-queries for multi-hop retrieval")
	runTimestampFlag := flag.String("run-timestamp", "", "Override run timestamp for log directory naming")

	flag.Parse()
	_ = debug
	_ = decomposeQueryFlag

	runTimestamp := time.Now().Format("20060102_150405")
	if *runTimestampFlag != "" {
		runTimestamp = *runTimestampFlag
	}
	runLogDir := fmt.Sprintf("./bench/beam/run_log_%s", runTimestamp)
	if err := os.MkdirAll(runLogDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create run log directory: %v\n", err)
		os.Exit(1)
	}

	mainLogPath := filepath.Join(runLogDir, "eval_selective.log")
	mainLogFile, err := os.OpenFile(mainLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open main log file: %v\n", err)
		os.Exit(1)
	}
	defer mainLogFile.Close()

	logMain := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		fmt.Print(msg)
		_, _ = mainLogFile.WriteString(msg)
	}

	logMain("DEBUG: use-utterances-vectors=%v, use-terms-vectors=%v, bypass-temporal=%v, bypass-semantic=%v, top-k=%d\n", *useUtterancesVectors, *useTermsVectors, *bypassTemporal, *bypassSemantic, *topKMatches)

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
		logMain("⚠️ Could not load prompts config from %s: %v\n", *promptsPath, err)
	}

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
		os.Exit(1)
	}

	plannerPath := os.Getenv("GLLAM_PLANNER_EXECUTABLE_PATH")
	if plannerPath != "" {
		gllam.SetPlannerExecutablePath(plannerPath)
		logMain("   ├─ External PDDL Planner set to: %s\n", plannerPath)
	}

	logMain("Building postings index from corpus...\n")
	startIdx := time.Now()
	idx, err := engine.BuildInvertedIndex(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to build postings index: %v\n", err)
		os.Exit(1)
	}
	logMain("Index built in %v. Utterances: %d, Terms: %d\n", time.Since(startIdx).Round(time.Millisecond), len(idx.Utterances), len(idx.Postings))

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

		if *categories != "" {
			matched := false
			allowedCats := strings.Split(*categories, ",")
			for _, cat := range allowedCats {
				catTrimmed := strings.TrimSpace(cat)
				if catTrimmed == "all" || catTrimmed == qa.Category {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}



		structuredLog := StructuredDetailsLog{
			InstanceID:     qa.InstanceID,
			Query:          qa.Query,
			SearchTerms:    []string{},
			JITExtractions: []JITExtractionEvent{},
		}

		logMain("%s\n", strings.Repeat("=", 100))
		logMain("Processing [%s]: %s\n", qa.InstanceID, qa.Query)
		logMain("   ├─ Processing Details Log: %s/processing_details_%s.log\n", runLogDir, qa.InstanceID)

		// 1. Clear semantic database tables for fresh query
		clearSemanticTables(ctx, gllam.DB())

		targetSpeakers := extractTargetSpeakers(qa.Query, idx)
		if len(targetSpeakers) > 0 {
			logMain("   ├─ Target speakers detected in question: %v\n", targetSpeakers)
		}

		// 2. Retrieve top matching utterances
		var subQueries []string
		if *decomposeQueryFlag {
			subQueries = decomposeQuery(ctx, llmClient, qa.Query)
			logMain("   ├─ Decomposed query into sub-queries: %v\n", subQueries)
			structuredLog.DecomposedQueries = subQueries
		} else {
			subQueries = []string{qa.Query}
		}

		var allCandidates []string
		seenCand := make(map[string]bool)

		for _, sq := range subQueries {
			if *useUtterancesVectors {
				logMain("   ├─ Retrieving top-%d matching paragraphs via Hybrid Search (TF-IDF + Vector RRF) for: %q...\n", *topKMatches, sq)
			} else {
				logMain("   ├─ Retrieving top-%d matching paragraphs via TF-IDF for: %q...\n", *topKMatches, sq)
			}
			sqCandidates, sqTerms := retrieveCandidatesForQuery(ctx, sq, targetSpeakers, idx, embedder, gllam, *topKMatches, *useUtterancesVectors, *useTermsVectors, qa.ConversationID, llmClient)
			
			for _, term := range sqTerms {
				termSeen := false
				for _, t := range structuredLog.SearchTerms {
					if t == term {
						termSeen = true
						break
					}
				}
				if !termSeen {
					structuredLog.SearchTerms = append(structuredLog.SearchTerms, term)
				}
			}

			for _, c := range sqCandidates {
				if !seenCand[c] {
					seenCand[c] = true
					allCandidates = append(allCandidates, c)
					
					if u, ok := idx.Utterances[c]; ok {
						structuredLog.RetrievedCandidates = append(structuredLog.RetrievedCandidates, CandidateInfo{
							UtteranceID: c,
							Speaker:     u.SpeakerID,
							Text:        u.Text,
							SessionID:   u.SessionID,
						})
					}
				}
			}
		}

		var answer string

		for pass := 0; pass < 3; pass++ {
			startIndex := pass * *topKMatches
			if startIndex >= len(allCandidates) {
				if pass > 0 {
					break
				}
				answer = "ANSWER_NOT_FOUND"
				break
			}
			endIndex := startIndex + *topKMatches
			if endIndex > len(allCandidates) {
				endIndex = len(allCandidates)
			}

			selectedUtteranceIDs := allCandidates[startIndex:endIndex]
			if len(selectedUtteranceIDs) == 0 {
				break
			}

			if pass > 0 {
				logMain("   ├─ 🔄 Pass %d: Retrying with next set of %d utterances (rank %d to %d)...\n", pass+1, len(selectedUtteranceIDs), startIndex+1, endIndex)
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

				matchIdx := -1
				for j, id := range sessUtteranceIDs {
					if id == matchID {
						matchIdx = j
						break
					}
				}

				if matchIdx != -1 {
					start := matchIdx - 10
					if start < 0 {
						start = 0
					}
					end := matchIdx + 15
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

			structuredLog.ChunkPruning.PruningEnabled = *pruneClueChunks
			if *pruneClueChunks {
				logMain("   ├─ Pruning irrelevant chunks from transcript using LLM YES/NO checks...\n")
				transcriptText = pruneIrrelevantChunks(ctx, llmClient, transcriptText, qa.Query, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap, &structuredLog.ChunkPruning.Chunks)
				logMain("   ├─ Transcript size after pruning: %d characters\n", len(transcriptText))
			}

			logMain("   ├─ Retrieved & expanded context size: %d turns (%d characters)\n", len(expandedUtterances), len(transcriptText))

			// 5. Try Direct QA First Pass
			logMain("   ├─ Attempting Direct QA first-pass...\n")
			directSystemPrompt := gllam.SystemPrompts.DirectQAPrompt

			directAnswer, err := tryDirectQA(ctx, llmClient, directSystemPrompt, transcriptText, qa.Query)
			
			isTemporal := strings.HasPrefix(strings.ToUpper(directAnswer), "TEMPORAL") || (qa.Category == "temporal_reasoning" || qa.Category == "event_ordering")
			isNotFound := strings.ToUpper(directAnswer) == "ANSWER_NOT_FOUND"

			structuredLog.FirstPassDirectQA = FirstPassDirectQAInfo{
				Attempted:    true,
				SystemPrompt: directSystemPrompt,
				UserPrompt:   fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query),
				Response:     directAnswer,
				IsTemporal:   isTemporal,
				IsNotFound:   isNotFound,
			}

			if err == nil && !isTemporal && !isNotFound && directAnswer != "" {
				answer = directAnswer
				logMain("   ├─ ✅ First-pass Direct QA succeeded.\n")
				structuredLog.FinalQA = FinalQAInfo{
					CompiledContext:   nil,
					SystemPrompt:      directSystemPrompt,
					UserPrompt:        fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query),
					Response:          directAnswer,
					FallbackTriggered: false,
					FallbackResponse:  "",
				}
			} else if *bypassSemantic || (isTemporal && *bypassTemporal) {
				reason := "TEMPORAL with --bypass-temporal"
				if *bypassSemantic {
					reason = "--bypass-semantic"
				}
				logMain("   ├─ ⚠️ Bypassing JIT semantic extraction (%s). Answering directly from transcript...\n", reason)
				directPrompt := gllam.SystemPrompts.SimpleTemporalRetrieval
				if directPrompt == "" {
					directPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript. Pay absolute attention to the chronological sequence of lines in the transcript. Determine who speaks first and who speaks second, and trace the sequence of statements. Answer the temporal ordering question precisely and directly."
				}
				userPrompt := fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query)

				answer, err = llmClient.Generate(ctx, directPrompt, userPrompt)
				if err != nil {
					answer = "ERROR"
				}

				structuredLog.FinalQA = FinalQAInfo{
					CompiledContext:   nil,
					SystemPrompt:      directPrompt,
					UserPrompt:        userPrompt,
					Response:          answer,
					FallbackTriggered: false,
					FallbackResponse:  "",
				}
			} else {
				logMain("   ├─ ❌ Direct QA returned %s. Falling back to JIT semantic extraction...\n", directAnswer)

				// 6. Extract semantics just-in-time
				extractionPrompt := gllam.SystemPrompts.SemanticExtraction
				schemaToUse := extractionJSONSchema
				if isTemporal && gllam.SystemPrompts.SemanticExtractionTemporal != "" {
					extractionPrompt = gllam.SystemPrompts.SemanticExtractionTemporal
					logMain("   ├─ Using alternate temporal-ready extraction prompts for JIT extraction.\n")

					temporalSchemaPath := "./config/semantic_extraction_temporal_schema.json"
					tempData, err := os.ReadFile(temporalSchemaPath)
					if err == nil {
						var tempSchema map[string]interface{}
						if uErr := json.Unmarshal(tempData, &tempSchema); uErr == nil {
							schemaToUse = tempSchema
							logMain("   ├─ Loaded temporal schema for JSON validation.\n")
						}
					}
				}
				nodes, links, err := extractSemanticsForText(ctx, gllam, embedder, llmClient, transcriptText, extractionPrompt, schemaToUse, qa.ConversationID, &structuredLog.JITExtractions)
				if err != nil {
					logMain("   ❌ Semantic extraction failed: %v\n", err)
				} else {
					logMain("   ├─ Extracted JIT: %d nodes, %d links\n", nodes, links)
				}

				// 7. Route and Assemble semantic context & answer query
				compiled, err := gllam.RouteAndAssemble(ctx, qa.Query, nil)
				if err != nil {
					logMain("   ❌ Error routing query: %v\n", err)
					answer = "ERROR"
				} else {
					prompt := engine.FormatSystemPrompt(compiled)
					if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
						if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
							prompt = prompt + "\n\n" + catPrompt
							logMain("   ├─ Appended category-specific guidelines for: %s\n", qa.Category)
						}
					}
					userQuery := fmt.Sprintf("Discussion Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query)

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

					var fallbackTriggered bool
					var fallbackResponse string
					// Fallback to direct transcript generation if temporal engine returned 'not found'
					if isNotFoundResponse(stripThinkingTags(answer)) {
						logMain("   ├─ ⚠️ Temporal engine returned 'not found'. Falling back to direct transcript generation...\n")
						fallbackPrompt := gllam.SystemPrompts.SimpleTemporalRetrieval
						if fallbackPrompt == "" {
							fallbackPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript. Pay absolute attention to the chronological sequence of lines in the transcript. Determine who speaks first and who speaks second, and trace the sequence of statements. Answer the temporal ordering question precisely and directly."
						}
						fallbackUserQuery := fmt.Sprintf("Transcript:\n%s\n\nQuestion: %s", transcriptText, qa.Query)
						
						fallbackAnswer, fErr := llmClient.Generate(ctx, fallbackPrompt, fallbackUserQuery)
						if fErr == nil && !isNotFoundResponse(stripThinkingTags(fallbackAnswer)) && fallbackAnswer != "" {
							answer = fallbackAnswer
							fallbackTriggered = true
							fallbackResponse = fallbackAnswer
							logMain("   ├─ ✅ Fallback direct generation succeeded.\n")
						}
					}

					structuredLog.FinalQA = FinalQAInfo{
						CompiledContext:   compiled,
						SystemPrompt:      prompt,
						UserPrompt:        userQuery,
						Response:          answer,
						FallbackTriggered: fallbackTriggered,
						FallbackResponse:  fallbackResponse,
						PDDLDomainPath:    compiled.PDDLDomainPath,
						PDDLProblemPath:   compiled.PDDLProblemPath,
					}
				}
			}

			cleanedAnswer := stripThinkingTags(answer)
			if !isNotFoundResponse(cleanedAnswer) {
				answer = cleanedAnswer
				break
			} else {
				logMain("   ├─ ❌ LLM returned 'not found' response in this pass: %q\n", cleanedAnswer)
				answer = cleanedAnswer
			}
		}

		// Write the consolidated details log file
		detailLogPath := filepath.Join(runLogDir, fmt.Sprintf("processing_details_%s.log", qa.InstanceID))
		logBytes, _ := json.MarshalIndent(structuredLog, "", "  ")
		_ = os.WriteFile(detailLogPath, logBytes, 0644)
		logMain("   ├─ Logged processing details to %s\n", detailLogPath)

		res := Result{
			InstanceID:  qa.InstanceID,
			Category:    qa.Category,
			Query:       qa.Query,
			ModelAnswer: answer,
			GroundTruth: getGroundTruthAnswer(qa.GroundTruth),
			Rubric:      qa.Rubric,
		}

		resBytes, _ := json.Marshal(res)
		outFile.Write(resBytes)
		outFile.WriteString("\n")

		count++
		logMain("   └─ Answer: %s\n", strings.ReplaceAll(strings.Split(answer, "\n")[0], "\r", ""))
	}

	logMain("\nCompleted %d evaluations. Results saved to %s\n", count, *outPath)
}

func extractSemanticsForText(ctx context.Context, gllam *engine.GllamEngine, embedder engine.Embedder, llmClient *engine.LLMClient, text string, systemPrompt string, extractionJSONSchema map[string]interface{}, sourceName string, events *[]JITExtractionEvent) (int, int, error) {
	chunks := engine.ChunkTranscript(text, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap)

	var nodesCount, linksCount int
	for cIdx, chunk := range chunks {
		if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
			continue
		}

		userPrompt := fmt.Sprintf("Transcript Chunk (%d/%d):\n%s\n\nExtract JSON:", cIdx+1, len(chunks), chunk.Text)

		response, err := llmClient.GenerateWithFormat(ctx, systemPrompt, userPrompt, extractionJSONSchema)
		if err != nil {
			return 0, 0, err
		}

		sanitized := SanitizeLLMJSON(response)

		var extraction struct {
			Nodes []memory.SemanticNode `json:"nodes"`
			Links []memory.SemanticLink `json:"links"`
		}
		if err := json.Unmarshal([]byte(sanitized), &extraction); err != nil {
			logFile := "./bench/beam/beam_selective_extraction_error.log"
			logContent := fmt.Sprintf("=== ERROR AT %s ===\nError: %v\n[RAW RESPONSE]:\n%s\n[SANITIZED RESPONSE]:\n%s\n=================================\n\n", time.Now().Format(time.RFC3339), err, response, sanitized)
			_ = os.WriteFile(logFile, []byte(logContent), 0644)
			continue
		}

		var canonicalizationLogs []string

		// Build ID mapping to canonicalize nodes and resolve duplicates
		nodeIDMapping := make(map[string]string)
		for _, node := range extraction.Nodes {
			if node.ID == "" {
				continue
			}

			// 1. Check if ID already exists in DB
			var dbID string
			err := gllam.DB().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE id = ?", node.ID).Scan(&dbID)
			if err == nil {
				nodeIDMapping[node.ID] = dbID
				continue
			}

			// 2. Check if name already exists (exact case-insensitive match)
			var dbIDByName string
			err = gllam.DB().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE LOWER(name) = LOWER(?) LIMIT 1", node.Name).Scan(&dbIDByName)
			if err == nil {
				nodeIDMapping[node.ID] = dbIDByName
				canonicalizationLogs = append(canonicalizationLogs, fmt.Sprintf("🔄 Canonicalized Node ID: '%s' -> '%s' (exact Name match: '%s')", node.ID, dbIDByName, node.Name))
				continue
			}

			// 3. Check for vector similarity match
			if embedder != nil {
				similar, err := gllam.SearchSimilarNodes(ctx, node.Name, 1)
				if err == nil && len(similar) > 0 {
					// Cosine distance threshold: < 0.12 (highly similar)
					if similar[0].Distance < 0.12 {
						nodeIDMapping[node.ID] = similar[0].NodeID
						canonicalizationLogs = append(canonicalizationLogs, fmt.Sprintf("🔄 Canonicalized Node ID: '%s' -> '%s' (vector similarity match: Distance %f)", node.ID, similar[0].NodeID, similar[0].Distance))
						continue
					}
				}
			}

			// Keep original ID if no match
			nodeIDMapping[node.ID] = node.ID
		}

		// Apply mapping to Nodes and filter duplicates
		var canonicalNodes []memory.SemanticNode
		seenNodeIDs := make(map[string]bool)
		for _, node := range extraction.Nodes {
			if node.ID == "" {
				continue
			}
			mappedID := nodeIDMapping[node.ID]
			if mappedID == "" {
				mappedID = node.ID
			}
			if seenNodeIDs[mappedID] {
				continue
			}
			seenNodeIDs[mappedID] = true

			node.ID = mappedID
			canonicalNodes = append(canonicalNodes, node)
		}

		// Apply mapping to Links and filter self-loops
		var canonicalLinks []memory.SemanticLink
		for _, link := range extraction.Links {
			if link.SourceID == "" || link.TargetID == "" || link.Relationship == "" {
				continue
			}

			if mSrc, ok := nodeIDMapping[link.SourceID]; ok {
				link.SourceID = mSrc
			}
			if mTgt, ok := nodeIDMapping[link.TargetID]; ok {
				link.TargetID = mTgt
			}
			if link.OriginID != "" {
				if mOrig, ok := nodeIDMapping[link.OriginID]; ok {
					link.OriginID = mOrig
				}
			}
			if link.Temporal != nil && link.Temporal.TemporalAnchorID != "" {
				if mAnchor, ok := nodeIDMapping[link.Temporal.TemporalAnchorID]; ok {
					link.Temporal.TemporalAnchorID = mAnchor
				}
			}

			if link.SourceID == link.TargetID {
				continue
			}
			canonicalLinks = append(canonicalLinks, link)
		}

		// Ingest into SQLite
		_, _ = gllam.DB().ExecContext(ctx, "BEGIN IMMEDIATE")

		type nodeVector struct {
			id  string
			vec []float32
		}
		var nodeVecs []nodeVector

		nodeSource := fmt.Sprintf("conversation_%s_chunk_%d", sourceName, cIdx+1)
		addLineage := func(nodeID string) {
			lineage := memory.DocumentLineage{
				NodeID:        nodeID,
				SourceURI:     fmt.Sprintf("conversation://%s", sourceName),
				DocumentTitle: fmt.Sprintf("Conversation Session %s", sourceName),
				SourceType:    "conversation",
				LineNumber:    cIdx + 1,
				CharOffset:    0,
			}
			_ = gllam.AddDocumentLineage(ctx, lineage)
		}

		for _, node := range canonicalNodes {
			node.CreatedFrom = nodeSource
			if err := gllam.UpsertNode(ctx, node); err == nil {
				nodesCount++
				addLineage(node.ID)
				if vec, err := embedder.Embed(ctx, node.Name); err == nil && len(vec) > 0 {
					nodeVecs = append(nodeVecs, nodeVector{id: node.ID, vec: vec})
				}
			}
		}

		for _, link := range canonicalLinks {
			link.CreatedFrom = nodeSource
			if err := gllam.AddEdge(ctx, link); err != nil {
				canonicalizationLogs = append(canonicalizationLogs, fmt.Sprintf("⚠️ AddEdge first pass failed for link %s -> %s (%s): %v", link.SourceID, link.TargetID, link.Relationship, err))
				_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.SourceID, Name: link.SourceID, Type: "inferred", CreatedFrom: link.CreatedFrom})
				addLineage(link.SourceID)
				_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: link.TargetID, Name: link.TargetID, Type: "inferred", CreatedFrom: link.CreatedFrom})
				addLineage(link.TargetID)
				if retryErr := gllam.AddEdge(ctx, link); retryErr == nil {
					linksCount++
				} else {
					canonicalizationLogs = append(canonicalizationLogs, fmt.Sprintf("❌ AddEdge retry failed: %v", retryErr))
				}
			} else {
				linksCount++
			}
		}

		for _, nv := range nodeVecs {
			_ = gllam.IndexNodeVector(ctx, nv.id, nv.vec)
		}

		_, _ = gllam.DB().ExecContext(ctx, "COMMIT")

		if events != nil {
			*events = append(*events, JITExtractionEvent{
				ChunkIndex:           cIdx + 1,
				SystemPrompt:         systemPrompt,
				UserPrompt:           userPrompt,
				RawResponse:          response,
				SanitizedJSON:        sanitized,
				NodesExtracted:       len(canonicalNodes),
				LinksExtracted:       len(canonicalLinks),
				CanonicalizationLogs: canonicalizationLogs,
			})
		}
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

	indexedIDs := make(map[string]bool)
	rows, err := gllam.DBRO().QueryContext(ctx, "SELECT utterance_id FROM utterance_embeddings")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr == nil {
				indexedIDs[id] = true
			}
		}
	}

	fmt.Printf("   ├─ Found %d utterance embeddings pre-indexed in DB.\n", len(indexedIDs))

	var missing []engine.CorpusUtterance
	for id, u := range idx.Utterances {
		if !indexedIDs[id] {
			missing = append(missing, u)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	fmt.Printf("   ├─ ⚙️ Embedding and indexing %d missing utterances into DB...\n", len(missing))
	
	type task struct {
		id   string
		text string
	}
	
	tasks := make(chan task, len(missing))
	for _, u := range missing {
		tasks <- task{id: u.ID, text: u.Text}
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
				if curr%100 == 0 || curr == int64(len(missing)) {
					fmt.Printf("      -> Embedded %d/%d missing utterances...\n", curr, len(missing))
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

func ensureTermEmbeddingsIndexed(ctx context.Context, gllam *engine.GllamEngine, embedder engine.Embedder, idx *engine.InvertedIndex) error {
	var count int
	err := gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='term_embeddings'").Scan(&count)
	if err != nil || count == 0 {
		return fmt.Errorf("term_embeddings table does not exist or schema not initialized: %v", err)
	}

	indexedTerms := make(map[string]bool)
	rows, err := gllam.DBRO().QueryContext(ctx, "SELECT term FROM term_embeddings")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var term string
			if scanErr := rows.Scan(&term); scanErr == nil {
				indexedTerms[term] = true
			}
		}
	}

	fmt.Printf("   ├─ Found %d vocabulary term embeddings pre-indexed in DB.\n", len(indexedTerms))

	var missing []string
	for term := range idx.Postings {
		if strings.Contains(term, " ") {
			continue // Skip bigram phrases during pre-indexing to avoid database vocabulary explosion
		}
		if !indexedTerms[term] {
			missing = append(missing, term)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	fmt.Printf("   ├─ ⚙️ Embedding and indexing %d missing vocabulary terms into DB...\n", len(missing))
	
	type task struct {
		term string
	}
	
	tasks := make(chan task, len(missing))
	for _, term := range missing {
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
				if curr%500 == 0 || curr == int64(len(missing)) {
					fmt.Printf("      -> Embedded %d/%d missing terms...\n", curr, len(missing))
				}
			}
		}()
	}
	wg.Wait()
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

func isNotFoundResponse(answer string) bool {
	upper := strings.ToUpper(answer)
	if upper == "ANSWER_NOT_FOUND" || upper == "ERROR" {
		return true
	}
	lower := strings.ToLower(answer)
	if strings.Contains(lower, "not explicitly mentioned") ||
		strings.Contains(lower, "not mentioned") ||
		strings.Contains(lower, "no direct mention") ||
		strings.Contains(lower, "no mention of") ||
		strings.Contains(lower, "is not mentioned") ||
		strings.Contains(lower, "not found in the provided transcript") {
		return true
	}
	return false
}

func pruneIrrelevantChunks(ctx context.Context, llmClient *engine.LLMClient, text string, query string, chunkSize, chunkOverlap int, events *[]ChunkPruningEvent) string {
	chunks := engine.ChunkTranscript(text, chunkSize, chunkOverlap)
	var keptChunks []string
	
	for i, chunk := range chunks {
		if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
			continue
		}
		systemPrompt := "You are a helpful assistant. Determine if a given transcript chunk contains ANY information, clues, dates, or mentions of the entities/events referred to in the user's question. The question may be a multi-step or multi-hop comparison, so a chunk is relevant if it mentions even ONE of the events, topics, or dates referred to in the question. The first word of your response must strictly be YES or NO, you can add a brief explanation of the why after."
		userPrompt := fmt.Sprintf("Question: %q\n\nTranscript Chunk:\n%s\n\nDoes this chunk contain information that helps answer the question? (YES/NO):", query, chunk.Text)
		
		var answer string
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			answer, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
			if err == nil && strings.TrimSpace(answer) != "" {
				break
			}
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		
		cleaned := strings.TrimSpace(strings.ToUpper(answer))
		if events != nil {
			*events = append(*events, ChunkPruningEvent{
				ChunkIndex:   i + 1,
				Text:         chunk.Text,
				SystemPrompt: systemPrompt,
				UserPrompt:   userPrompt,
				LlmDecision:  answer,
			})
		}

		if strings.HasPrefix(cleaned, "YES") {
			keptChunks = append(keptChunks, chunk.Text)
		}
	}
	return strings.Join(keptChunks, "\n\n")
}

func decomposeQuery(ctx context.Context, llmClient *engine.LLMClient, query string) []string {
	systemPrompt := `You are an information retrieval expert. Your task is to decompose a complex, multi-hop user question into 2 or 3 distinct, simple search queries or points of focus (phrases or keywords) that must be retrieved from a dialogue database. 
Focus strictly on the specific entities, events, dates, or actions mentioned. Do not include question words like "how many", "what", "when", or structural logic.
Output each query on a new line. Do not add numbering or explanations.`

	userPrompt := fmt.Sprintf("Question: %q\n\nSearch Queries:", query)
	
	var response string
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		response, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err == nil && strings.TrimSpace(response) != "" {
			break
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		return []string{query} // Fallback to original query
	}

	var subQueries []string
	lines := strings.Split(response, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		if idx := strings.Index(trimmed, ". "); idx != -1 && idx < 3 {
			trimmed = trimmed[idx+2:]
		}
		trimmed = strings.TrimSpace(trimmed)
		if len(trimmed) > 3 {
			subQueries = append(subQueries, trimmed)
		}
	}
	
	if len(subQueries) == 0 {
		return []string{query}
	}
	return subQueries
}

func retrieveCandidatesForQuery(ctx context.Context, query string, targetSpeakers []string, idx *engine.InvertedIndex, embedder engine.Embedder, gllam *engine.GllamEngine, topK int, useUtterancesVectors, useTermsVectors bool, conversationID string, llmClient *engine.LLMClient) ([]string, []string) {
	var searchTerms []string
	queryTokens := engine.Tokenize(query)
	seenTerms := make(map[string]bool)
	for _, tok := range queryTokens {
		if !stopWords[tok] && !seenTerms[tok] {
			seenTerms[tok] = true
			searchTerms = append(searchTerms, tok)
		}
	}

	// Add adjacent non-stopword bigrams (e.g. "cover letter", "zoom call")
	for i := 0; i < len(queryTokens)-1; i++ {
		tok1 := queryTokens[i]
		tok2 := queryTokens[i+1]
		if !stopWords[tok1] && !stopWords[tok2] {
			bigram := tok1 + " " + tok2
			if !seenTerms[bigram] {
				seenTerms[bigram] = true
				searchTerms = append(searchTerms, bigram)
			}
		}
	}

	if useTermsVectors {
		var expandedTerms []string
		for _, term := range searchTerms {
			if nonExpandableTerms[term] {
				continue
			}
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
		for _, t := range expandedTerms {
			if !seenTerms[t] {
				seenTerms[t] = true
				searchTerms = append(searchTerms, t)
			}
		}
	}

	if llmClient != nil {
		searchTerms = filterTermsWithLLM(ctx, llmClient, query, searchTerms)
	}

	// Build map of allowed session IDs belonging to this conversationID
	allowedSessions := make(map[string]bool)
	targetPrefix := "beam-100k-" + conversationID + "-session"
	for sessID := range idx.Sessions {
		if strings.HasPrefix(sessID, targetPrefix) || sessID == conversationID {
			allowedSessions[sessID] = true
		}
	}

	if useUtterancesVectors {
		// 1. TF-IDF scoring
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
				u, ok := idx.Utterances[p.UtteranceID]
				if ok && allowedSessions[u.SessionID] {
					uttScores[p.UtteranceID] += float64(p.Frequency) * idf
				}
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

		// 2. Vector search filtered by session
		qEmb, err := embedder.Embed(ctx, query)
		var vecMatches []struct {
			UtteranceID string
			Distance    float32
		}
		if err == nil {
			rawMatches, _ := gllam.SearchSimilarUtterances(ctx, qEmb, 500)
			for _, match := range rawMatches {
				u, ok := idx.Utterances[match.UtteranceID]
				if ok && allowedSessions[u.SessionID] {
					vecMatches = append(vecMatches, match)
				}
			}
		}

		// 3. RRF Combination
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

		// Speaker boost
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

		var candidates []string
		for i := 0; i < len(rrfList) && i < topK; i++ {
			candidates = append(candidates, rrfList[i].id)
		}
		return candidates, searchTerms
	} else {
		// Only TF-IDF
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
				u, ok := idx.Utterances[p.UtteranceID]
				if ok && allowedSessions[u.SessionID] {
					score := float64(p.Frequency) * idf
					if matchesAnySpeaker(u.SpeakerID, targetSpeakers) {
						score *= 10.0
					}
					uttScores[p.UtteranceID] += score
				}
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

		var candidates []string
		for i := 0; i < len(scoredList) && i < topK; i++ {
			candidates = append(candidates, scoredList[i].id)
		}
		return candidates, searchTerms
	}
}

func filterTermsWithLLM(ctx context.Context, llmClient *engine.LLMClient, query string, terms []string) []string {
	if len(terms) == 0 {
		return terms
	}
	
	systemPrompt := `You are an information retrieval expert. Review the candidate search terms/phrases for the given question. 
Filter out terms that are too generic (such as "call", "planned", "finish", "day", "many", "user", "assistant", "what", "how", "when", "days", "there", "between", "finish revising") and keep only the terms, bigrams, or concepts that are highly specific to retrieving the relevant dialogue chunks.
Output the filtered terms as a JSON array of strings. Do not include any explanations, bullet points, or markdown formatting (like code blocks). Just output the JSON array.`

	userPrompt := fmt.Sprintf("Question: %q\nCandidate Search Terms: %v\nFiltered Search Terms:", query, terms)
	
	var response string
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		response, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err == nil && strings.TrimSpace(response) != "" {
			break
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	
	if err != nil {
		return terms // Fallback to unfiltered terms on error
	}
	
	// Parse JSON array
	var filtered []string
	cleanedResponse := strings.TrimSpace(response)
	// Remove markdown code block wraps if present
	cleanedResponse = strings.TrimPrefix(cleanedResponse, "```json")
	cleanedResponse = strings.TrimPrefix(cleanedResponse, "```")
	cleanedResponse = strings.TrimSuffix(cleanedResponse, "```")
	cleanedResponse = strings.TrimSpace(cleanedResponse)
	
	if err := json.Unmarshal([]byte(cleanedResponse), &filtered); err == nil && len(filtered) > 0 {
		fmt.Printf("   ├─ LLM filtered search terms: original %d -> filtered %d: %v\n", len(terms), len(filtered), filtered)
		return filtered
	}
	
	return terms
}
