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
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	Error        string   `json:"error,omitempty"`
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
	Error        string `json:"error,omitempty"`
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
	Error                string   `json:"error,omitempty"`
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
	Error             string      `json:"error,omitempty"`
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
	Error               string                 `json:"error,omitempty"`
}

type MainLogInstanceEvent struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
}

type MainLogInstance struct {
	InstanceID  string                 `json:"instance_id"`
	Category    string                 `json:"category"`
	Query       string                 `json:"query"`
	Timestamp   string                 `json:"timestamp"`
	Events      []MainLogInstanceEvent `json:"events"`
	ModelAnswer string                 `json:"model_answer"`
	GroundTruth string                 `json:"ground_truth"`
}

type MainLogConfig struct {
	UseUtterancesVectors bool `json:"use_utterances_vectors"`
	UseTermsVectors      bool `json:"use_terms_vectors"`
	BypassTemporal       bool `json:"bypass_temporal"`
	BypassSemantic       bool `json:"bypass_semantic"`
	TopKMatches          int  `json:"top_k_matches"`
}

type MainLogPhaseDurations struct {
	DatabaseCleanupsMs      int64 `json:"database_cleanups_ms"`
	QueryDecompositionMs    int64 `json:"query_decomposition_ms"`
	CandidateRetrievalMs    int64 `json:"candidate_retrieval_ms"`
	ChunkPruningMs          int64 `json:"chunk_pruning_ms"`
	FirstPassDirectQAMs     int64 `json:"first_pass_direct_qa_ms"`
	JITSemanticExtractionMs int64 `json:"jit_semantic_extraction_ms"`
	RouteAssembleMs         int64 `json:"route_assemble_ms"`
	FinalQAGenerationMs     int64 `json:"final_qa_generation_ms"`
}

type MainLogSummary struct {
	TotalAttempted int     `json:"total_attempted"`
	Correct        int     `json:"correct"`
	Accuracy       float64 `json:"accuracy"`
}

type MainLogStructure struct {
	Timestamp            string                 `json:"timestamp"`
	Config               MainLogConfig          `json:"config"`
	IndexBuildDurationMs int64                  `json:"index_build_duration_ms"`
	IndexUtteranceCount  int                    `json:"index_utterance_count"`
	IndexTermCount       int                    `json:"index_term_count"`
	Evaluations          []MainLogInstance      `json:"evaluations"`
	Summary              MainLogSummary         `json:"summary"`
	PhaseDurationsMs     MainLogPhaseDurations  `json:"phase_durations_ms"`
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
	"down": true, "up": true, "out": true, "off": true, "over": true, "under": true,
	"can": true, "cant": true, "cannot": true, "could": true, "would": true, "should": true, "will": true, "wont": true,
	"shall": true, "may": true, "might": true, "must": true,
	"put": true, "get": true, "got": true, "take": true, "took": true, "make": true, "made": true,
	"go": true, "went": true, "come": true, "came": true, "see": true, "saw": true, "say": true, "said": true,
	"let": true, "set": true, "also": true, "just": true, "now": true, "then": true, "even": true,
	"more": true, "most": true, "less": true, "least": true, "back": true, "away": true, "again": true, "still": true, "yet": true, "already": true, "well": true,
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
	"put": true, "down": true, "up": true, "out": true, "off": true, "back": true, "away": true, "over": true, "under": true,
	"get": true, "got": true, "take": true, "took": true, "make": true, "made": true,
	"go": true, "went": true, "come": true, "came": true, "see": true, "saw": true, "say": true, "said": true,
	"let": true, "set": true, "use": true, "used": true, "want": true, "wanted": true, "like": true, "liked": true,
	"need": true, "needed": true, "give": true, "given": true, "gave": true, "find": true, "found": true,
	"look": true, "looked": true, "keep": true, "kept": true, "turn": true, "turned": true,
	"call": true, "called": true, "ask": true, "asked": true, "seem": true, "seemed": true,
	"feel": true, "felt": true, "try": true, "tried": true, "start": true, "started": true, "stop": true, "stopped": true,
	"work": true, "worked": true, "run": true, "ran": true,
	"can": true, "cant": true, "cannot": true, "could": true, "would": true, "should": true, "will": true, "wont": true,
	"may": true, "might": true, "must": true,
	"also": true, "just": true, "now": true, "then": true, "even": true, "more": true, "most": true, "less": true, "least": true,
	"again": true, "still": true, "yet": true, "already": true, "well": true,
}

var metaRequestWords = map[string]bool{
	// Recommendations & suggestions
	"recommendation": true, "recommendations": true, "recommend": true, "recommended": true, "recommending": true, "recommenders": true,
	"suggestion": true, "suggestions": true, "suggest": true, "suggested": true, "suggesting": true, "suggests": true,
	// Advice & guidance
	"advice": true, "advise": true, "advised": true, "advises": true, "advising": true,
	"guidance": true, "guideline": true, "guidelines": true, "guide": true, "guides": true,
	"tip": true, "tips": true, "feedback": true, "opinion": true, "opinions": true,
	// Conversational request / prompt wrappers
	"tell": true, "mention": true, "mentioned": true, "mentions": true, "mentioning": true,
	"explain": true, "explained": true, "explaining": true, "explanation": true,
	"discuss": true, "discussed": true, "discusses": true, "discussing": true, "discussion": true,
	"walk": true, "help": true, "helpful": true, "helping": true, "helps": true,
	"check": true, "checkout": true, "look": true, "looking": true, "find": true, "finding": true,
	"give": true, "gives": true, "given": true, "giving": true, "know": true,
	// Generic qualifiers & fillers
	"specific": true, "specifically": true, "particular": true, "particularly": true,
	"detail": true, "details": true, "detailed": true, "info": true, "information": true,
	"good": true, "great": true, "best": true, "better": true,
	"some": true, "any": true, "all": true, "few": true, "many": true,
	"should": true, "would": true, "could": true, "can": true, "please": true,
	"out": true, "up": true, "down": true, "about": true,
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
	runLogDir := fmt.Sprintf("./bench/beam/run_logs/%s", runTimestamp)
	if err := os.MkdirAll(runLogDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create run log directory: %v\n", err)
		os.Exit(1)
	}

	mainLogPath := filepath.Join(runLogDir, "eval_selective.log")

	logMain := func(format string, args ...interface{}) {
		msg := fmt.Sprintf(format, args...)
		fmt.Print(msg)
	}

	var mainLog MainLogStructure
	mainLog.Timestamp = time.Now().Format(time.RFC3339)
	mainLog.Config = MainLogConfig{
		UseUtterancesVectors: *useUtterancesVectors,
		UseTermsVectors:      *useTermsVectors,
		BypassTemporal:       *bypassTemporal,
		BypassSemantic:       *bypassSemantic,
		TopKMatches:          *topKMatches,
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
	mainLog.IndexBuildDurationMs = time.Since(startIdx).Milliseconds()
	mainLog.IndexUtteranceCount = len(idx.Utterances)
	mainLog.IndexTermCount = len(idx.Postings)

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

	var (
		totalClearTablesTime   time.Duration
		totalDecomposeTime     time.Duration
		totalRetrieveTime      time.Duration
		totalPruneTime         time.Duration
		totalDirectQATime      time.Duration
		totalExtractionTime    time.Duration
		totalRouteAssembleTime time.Duration
		totalGenerationTime    time.Duration
	)



	scanner := bufio.NewScanner(qaFile)
	count := 0
	strongServerEnv := os.Getenv("STRONG_TEXT_SERVER")
	strongModelEnv := os.Getenv("STRONG_LLM_MODEL")
	fastServerEnv := os.Getenv("FAST_TEXT_SERVER")
	fastModelEnv := os.Getenv("FAST_LLM_MODEL")

	var strongClient *engine.LLMClient
	if strongServerEnv != "" {
		strongClient = engine.NewLLMClientWithKey(strongServerEnv, "", strongModelEnv)
		strongClient.Tier = "strong"
	}

	var fastClient *engine.LLMClient
	if fastServerEnv != "" {
		fastClient = engine.NewLLMClientWithKey(fastServerEnv, "", fastModelEnv)
		fastClient.Tier = "fast"
	}

	var defaultClient *engine.LLMClient
	if strongClient == nil && fastClient == nil {
		defaultClient = engine.NewLLMClient(*textServer)
		defaultClient.Tier = "default"
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logMain("\n⚠️ Interrupted by user! Saving progress and exiting gracefully...\n")

		if outFile != nil {
			_ = outFile.Sync()
			_ = outFile.Close()
		}

		mainLog.Summary = MainLogSummary{
			TotalAttempted: count,
		}
		mainLog.PhaseDurationsMs = MainLogPhaseDurations{
			DatabaseCleanupsMs:      totalClearTablesTime.Milliseconds(),
			QueryDecompositionMs:    totalDecomposeTime.Milliseconds(),
			CandidateRetrievalMs:    totalRetrieveTime.Milliseconds(),
			ChunkPruningMs:          totalPruneTime.Milliseconds(),
			FirstPassDirectQAMs:     totalDirectQATime.Milliseconds(),
			JITSemanticExtractionMs: totalExtractionTime.Milliseconds(),
			RouteAssembleMs:         totalRouteAssembleTime.Milliseconds(),
			FinalQAGenerationMs:     totalGenerationTime.Milliseconds(),
		}

		mainLogData, _ := json.MarshalIndent(mainLog, "", "  ")
		_ = os.WriteFile(mainLogPath, mainLogData, 0644)

		os.Exit(0)
	}()

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



		logInstance := MainLogInstance{
			InstanceID:  qa.InstanceID,
			Category:    qa.Category,
			Query:       qa.Query,
			Timestamp:   time.Now().Format(time.RFC3339),
			Events:      []MainLogInstanceEvent{},
			ModelAnswer: "",
			GroundTruth: getGroundTruthAnswer(qa.GroundTruth),
		}

		addEvent := func(msg string) {
			logInstance.Events = append(logInstance.Events, MainLogInstanceEvent{
				Timestamp: time.Now().Format(time.RFC3339),
				Event:     msg,
			})
		}

		logTimestamp := func(stepName string) {
			logMain("   ├─ [%s] Starting %s...\n", time.Now().Format("2006-01-02 15:04:05"), stepName)
			addEvent("Starting " + stepName)
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
		tClear0 := time.Now()
		clearSemanticTables(ctx, gllam.DB())
		totalClearTablesTime += time.Since(tClear0)
		addEvent("Semantic tables cleared")

		targetSpeakers := extractTargetSpeakers(qa.Query, idx)
		if len(targetSpeakers) > 0 {
			logMain("   ├─ Target speakers detected in question: %v\n", targetSpeakers)
		}

		// 2. Retrieve top matching utterances
		tDecompose0 := time.Now()
		var subQueries []string
		if *decomposeQueryFlag {
			logTimestamp("query decomposition")
			subQueries = decomposeQuery(ctx, getClientForTask("QUERY_DECOMPOSITION", "STRONG_TEXT_SERVER", strongClient, fastClient, defaultClient), qa.Query)
			logMain("   ├─ Decomposed query into sub-queries: %q\n", subQueries)
			structuredLog.DecomposedQueries = subQueries
		} else {
			subQueries = []string{qa.Query}
		}
		totalDecomposeTime += time.Since(tDecompose0)
		if *decomposeQueryFlag {
			addEvent(fmt.Sprintf("Query decomposition completed: %v sub-queries", len(subQueries)))
		}

		tRetrieve0 := time.Now()
		logTimestamp("candidate retrieval")
		var allCandidates []string
		seenCand := make(map[string]bool)

		for _, sq := range subQueries {
			if *useUtterancesVectors {
				logMain("   ├─ Retrieving top-%d matching paragraphs via Hybrid Search (TF-IDF + Vector RRF) for: %q...\n", *topKMatches, sq)
			} else {
				logMain("   ├─ Retrieving top-%d matching paragraphs via TF-IDF for: %q...\n", *topKMatches, sq)
			}
			sqCandidates, sqTerms := retrieveCandidatesForQuery(ctx, sq, targetSpeakers, idx, embedder, gllam, *topKMatches, *useUtterancesVectors, *useTermsVectors, qa.ConversationID, getClientForTask("SEARCH_CANDIDATES", "FAST_TEXT_SERVER", strongClient, fastClient, defaultClient))
			
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
		retrieveDur := time.Since(tRetrieve0)
		totalRetrieveTime += retrieveDur
		logMain("   ├─ [%s] [%v] Candidate retrieval completed.\n", time.Now().Format("2006-01-02 15:04:05"), retrieveDur.Round(time.Millisecond))
		addEvent(fmt.Sprintf("Candidate retrieval completed in %v", retrieveDur.Round(time.Millisecond)))

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

			tPrune0 := time.Now()
			structuredLog.ChunkPruning.PruningEnabled = *pruneClueChunks
			if *pruneClueChunks {
				logTimestamp("chunk pruning")
				logMain("   ├─ Pruning irrelevant chunks from transcript using keyword overlap scoring...\n")
				transcriptText = pruneIrrelevantChunks(ctx, transcriptText, qa.Query, structuredLog.SearchTerms, gllam.SystemPrompts.ChunkSize, gllam.SystemPrompts.ChunkOverlap, &structuredLog.ChunkPruning.Chunks)
				pruneDur := time.Since(tPrune0)
				logMain("   ├─ [%s] [%v] Chunk pruning completed.\n", time.Now().Format("2006-01-02 15:04:05"), pruneDur.Round(time.Millisecond))
				addEvent(fmt.Sprintf("Chunk pruning completed in %v", pruneDur.Round(time.Millisecond)))
				logMain("   ├─ Transcript size after pruning: %d characters\n", len(transcriptText))
			}
			totalPruneTime += time.Since(tPrune0)

			logMain("   ├─ Retrieved & expanded context size: %d turns (%d characters)\n", len(expandedUtterances), len(transcriptText))

			// 5. Try Direct QA First Pass
			tDirectQA0 := time.Now()
			logTimestamp("first-pass Direct QA")
			logMain("   ├─ Attempting Direct QA first-pass...\n")
			directSystemPrompt := gllam.SystemPrompts.DirectQAPrompt
			if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
				if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
					directSystemPrompt = directSystemPrompt + "\n\n" + catPrompt
					logMain("   ├─ Appended category-specific guidelines to Direct QA for: %s\n", qa.Category)
				}
			}

			directAnswer, err := tryDirectQA(ctx, getClientForTask("ZERO_SHOT_ANSWER", "STRONG_TEXT_SERVER", strongClient, fastClient, defaultClient), directSystemPrompt, transcriptText, qa.Query)
			directQADur := time.Since(tDirectQA0)
			totalDirectQATime += directQADur
			logMain("   ├─ [%s] [%v] First-pass Direct QA completed.\n", time.Now().Format("2006-01-02 15:04:05"), directQADur.Round(time.Millisecond))
			
			var errStr string
			if err != nil {
				errStr = err.Error()
				logMain("   ├─ ❌ First-pass Direct QA failed: %v\n", err)
				addEvent(fmt.Sprintf("First-pass Direct QA failed with error: %v", err))
			} else {
				addEvent(fmt.Sprintf("First-pass Direct QA completed in %v. Result: %s", directQADur.Round(time.Millisecond), directAnswer))
			}
			
			cleanedDirect := stripThinkingTags(directAnswer)
			isTemporal := strings.HasPrefix(strings.ToUpper(cleanedDirect), "TEMPORAL") || (qa.Category == "temporal_reasoning" || qa.Category == "event_ordering")
			isNotFound := strings.ToUpper(cleanedDirect) == "ANSWER_NOT_FOUND"

			structuredLog.FirstPassDirectQA = FirstPassDirectQAInfo{
				Attempted:    true,
				SystemPrompt: directSystemPrompt,
				UserPrompt:   fmt.Sprintf("Question: %s\n\nTranscript:\n%s", qa.Query, transcriptText),
				Response:     directAnswer,
				IsTemporal:   isTemporal,
				IsNotFound:   isNotFound,
				Error:        errStr,
			}

			if err == nil && !isTemporal && !isNotFound && cleanedDirect != "" {
				answer = cleanedDirect
				logMain("   ├─ ✅ First-pass Direct QA succeeded.\n")
				structuredLog.FinalQA = FinalQAInfo{
					CompiledContext:   nil,
					SystemPrompt:      directSystemPrompt,
					UserPrompt:        fmt.Sprintf("Question: %s\n\nTranscript:\n%s", qa.Query, transcriptText),
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
				if !isTemporal {
					directPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript."
				} else if directPrompt == "" {
					directPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript. Pay absolute attention to the chronological sequence of lines in the transcript. Determine who speaks first and who speaks second, and trace the sequence of statements. Answer the temporal ordering question precisely and directly."
				}

				if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
					if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
						directPrompt = directPrompt + "\n\n" + catPrompt
						logMain("   ├─ Appended category-specific guidelines for: %s\n", qa.Category)
					}
				}
				userPrompt := fmt.Sprintf("Question: %s\n\nTranscript:\n%s", qa.Query, transcriptText)

				answer, err = getClientForTask("FINAL_ANSWER", "STRONG_TEXT_SERVER", strongClient, fastClient, defaultClient).Generate(ctx, directPrompt, userPrompt)
				var finalErrStr string
				if err != nil {
					answer = "ERROR"
					finalErrStr = err.Error()
					logMain("   ├─ ❌ Direct QA bypass generation failed: %v\n", err)
				}

				structuredLog.FinalQA = FinalQAInfo{
					CompiledContext:   nil,
					SystemPrompt:      directPrompt,
					UserPrompt:        userPrompt,
					Response:          answer,
					FallbackTriggered: false,
					FallbackResponse:  "",
					Error:             finalErrStr,
				}
				structuredLog.Error = finalErrStr
			} else {
				logMain("   ├─ ❌ Direct QA returned %s. Falling back to JIT semantic extraction...\n", directAnswer)

				// 6. Extract semantics just-in-time
				tExtract0 := time.Now()
				logTimestamp("JIT semantic extraction")
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
				nodes, links, err := extractSemanticsForText(ctx, gllam, embedder, getClientForTask("SEMANTIC_EXTRACTION", "FAST_TEXT_SERVER", strongClient, fastClient, defaultClient), transcriptText, extractionPrompt, schemaToUse, qa.ConversationID, &structuredLog.JITExtractions)
				extractDur := time.Since(tExtract0)
				totalExtractionTime += extractDur
				logMain("   ├─ [%s] [%v] JIT semantic extraction completed.\n", time.Now().Format("2006-01-02 15:04:05"), extractDur.Round(time.Millisecond))
				addEvent(fmt.Sprintf("JIT semantic extraction completed in %v. Extracted %d nodes, %d links", extractDur.Round(time.Millisecond), nodes, links))
				if err != nil {
					logMain("   ❌ Semantic extraction failed: %v\n", err)
					structuredLog.Error = err.Error()
				} else {
					logMain("   ├─ Extracted JIT: %d nodes, %d links\n", nodes, links)
				}

				// 7. Route and Assemble semantic context & answer query
				tRoute0 := time.Now()
				logTimestamp("Route & Assemble")
				compiled, err := gllam.RouteAndAssemble(ctx, qa.Query, nil)
				routeDur := time.Since(tRoute0)
				totalRouteAssembleTime += routeDur
				logMain("   ├─ [%s] [%v] Route & Assemble completed.\n", time.Now().Format("2006-01-02 15:04:05"), routeDur.Round(time.Millisecond))
				addEvent(fmt.Sprintf("Route & Assemble completed in %v", routeDur.Round(time.Millisecond)))
				if err != nil {
					logMain("   ❌ Error routing query: %v\n", err)
					answer = "ERROR"
					structuredLog.Error = err.Error()
				} else {
					prompt := engine.FormatSystemPrompt(compiled)
					if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
						if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
							prompt = prompt + "\n\n" + catPrompt
							logMain("   ├─ Appended category-specific guidelines for: %s\n", qa.Category)
						}
					}
					userQuery := fmt.Sprintf("Question: %s\n\nDiscussion Transcript:\n%s", qa.Query, transcriptText)

					var genErr error
					var finalErrStr string
					tGen0 := time.Now()
					logTimestamp("Final QA Generation")
					for attempt := 1; attempt <= 3; attempt++ {
						answer, genErr = getClientForTask("FINAL_ANSWER", "STRONG_TEXT_SERVER", strongClient, fastClient, defaultClient).Generate(ctx, prompt, userQuery)
						if genErr == nil && strings.TrimSpace(answer) != "" {
							break
						}
						time.Sleep(time.Duration(attempt) * time.Second)
					}
					if genErr != nil {
						answer = "ERROR"
						finalErrStr = genErr.Error()
						logMain("   ├─ ❌ Final QA generation failed: %v\n", genErr)
					}

					var fallbackTriggered bool
					var fallbackResponse string
					// Fallback to direct transcript generation if temporal engine returned 'not found'
					if isNotFoundResponse(stripThinkingTags(answer)) {
						logMain("   ├─ ⚠️ Temporal engine returned 'not found'. Falling back to direct transcript generation...\n")
						fallbackPrompt := gllam.SystemPrompts.SimpleTemporalRetrieval
						if !isTemporal {
							fallbackPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript."
						} else if fallbackPrompt == "" {
							fallbackPrompt = "You are a helpful assistant. Answer the question strictly using facts directly stated in the transcript. Pay absolute attention to the chronological sequence of lines in the transcript. Determine who speaks first and who speaks second, and trace the sequence of statements. Answer the temporal ordering question precisely and directly."
						}

						if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
							if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
								fallbackPrompt = fallbackPrompt + "\n\n" + catPrompt
								logMain("   ├─ Appended category-specific guidelines for: %s\n", qa.Category)
							}
						}
						fallbackUserQuery := fmt.Sprintf("Question: %s\n\nTranscript:\n%s", qa.Query, transcriptText)
						
						fallbackAnswer, fErr := getClientForTask("FALLBACK_ANSWER", "FAST_TEXT_SERVER", strongClient, fastClient, defaultClient).Generate(ctx, fallbackPrompt, fallbackUserQuery)
						if fErr != nil {
							finalErrStr = fErr.Error()
							logMain("   ├─ ❌ Fallback direct generation failed: %v\n", fErr)
						} else if !isNotFoundResponse(stripThinkingTags(fallbackAnswer)) && fallbackAnswer != "" {
							answer = fallbackAnswer
							fallbackTriggered = true
							fallbackResponse = fallbackAnswer
							logMain("   ├─ ✅ Fallback direct generation succeeded.\n")
						}
					}
					genDur := time.Since(tGen0)
					totalGenerationTime += genDur
					logMain("   ├─ [%s] [%v] Final QA Generation completed.\n", time.Now().Format("2006-01-02 15:04:05"), genDur.Round(time.Millisecond))
					addEvent(fmt.Sprintf("Final QA Generation completed in %v", genDur.Round(time.Millisecond)))

					structuredLog.FinalQA = FinalQAInfo{
						CompiledContext:   compiled,
						SystemPrompt:      prompt,
						UserPrompt:        userQuery,
						Response:          answer,
						FallbackTriggered: fallbackTriggered,
						FallbackResponse:  fallbackResponse,
						PDDLDomainPath:    compiled.PDDLDomainPath,
						PDDLProblemPath:   compiled.PDDLProblemPath,
						Error:             finalErrStr,
					}
					structuredLog.Error = finalErrStr
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
			Error:       structuredLog.Error,
		}

		resBytes, _ := json.Marshal(res)
		outFile.Write(resBytes)
		outFile.WriteString("\n")

		logInstance.ModelAnswer = answer
		mainLog.Evaluations = append(mainLog.Evaluations, logInstance)

		count++
		if answer == "ERROR" {
			logMain("   ├─ [%s] Status: ❌ ERROR\n", time.Now().Format("2006-01-02 15:04:05"))
			logMain("   └─ Answer: None\n")
		} else {
			logMain("   ├─ [%s] Status: ✅ SUCCESS\n", time.Now().Format("2006-01-02 15:04:05"))
			logMain("   └─ Answer: %s\n", answer)
		}
	}

	logMain("\nCompleted %d evaluations. Results saved to %s\n", count, *outPath)

	totalOverallTime := totalClearTablesTime + totalDecomposeTime + totalRetrieveTime + totalPruneTime + totalDirectQATime + totalExtractionTime + totalRouteAssembleTime + totalGenerationTime
	
	percentage := func(part time.Duration, total time.Duration) float64 {
		if total <= 0 {
			return 0
		}
		return float64(part) / float64(total) * 100
	}

	logMain("\n====================================================================================================\n")
	logMain("--- Phase Execution Time Summary ---\n")
	logMain("Total Accumulated Phase Time: %v\n", totalOverallTime.Round(time.Millisecond))
	logMain("  - Database Cleanups        : %-8v (%5.1f%%)\n", totalClearTablesTime.Round(time.Millisecond), percentage(totalClearTablesTime, totalOverallTime))
	logMain("  - Query Decomposition      : %-8v (%5.1f%%)\n", totalDecomposeTime.Round(time.Millisecond), percentage(totalDecomposeTime, totalOverallTime))
	logMain("  - Candidate Retrieval      : %-8v (%5.1f%%)\n", totalRetrieveTime.Round(time.Millisecond), percentage(totalRetrieveTime, totalOverallTime))
	logMain("  - Chunk Pruning            : %-8v (%5.1f%%)\n", totalPruneTime.Round(time.Millisecond), percentage(totalPruneTime, totalOverallTime))
	logMain("  - First-Pass Direct QA     : %-8v (%5.1f%%)\n", totalDirectQATime.Round(time.Millisecond), percentage(totalDirectQATime, totalOverallTime))
	logMain("  - JIT Semantic Extraction  : %-8v (%5.1f%%)\n", totalExtractionTime.Round(time.Millisecond), percentage(totalExtractionTime, totalOverallTime))
	logMain("  - Route & Assemble         : %-8v (%5.1f%%)\n", totalRouteAssembleTime.Round(time.Millisecond), percentage(totalRouteAssembleTime, totalOverallTime))
	logMain("  - Final QA Generation      : %-8v (%5.1f%%)\n", totalGenerationTime.Round(time.Millisecond), percentage(totalGenerationTime, totalOverallTime))
	logMain("====================================================================================================\n")

	mainLog.Summary = MainLogSummary{
		TotalAttempted: count,
	}
	mainLog.PhaseDurationsMs = MainLogPhaseDurations{
		DatabaseCleanupsMs:      totalClearTablesTime.Milliseconds(),
		QueryDecompositionMs:    totalDecomposeTime.Milliseconds(),
		CandidateRetrievalMs:    totalRetrieveTime.Milliseconds(),
		ChunkPruningMs:          totalPruneTime.Milliseconds(),
		FirstPassDirectQAMs:     totalDirectQATime.Milliseconds(),
		JITSemanticExtractionMs: totalExtractionTime.Milliseconds(),
		RouteAssembleMs:         totalRouteAssembleTime.Milliseconds(),
		FinalQAGenerationMs:     totalGenerationTime.Milliseconds(),
	}

	mainLogData, _ := json.MarshalIndent(mainLog, "", "  ")
	_ = os.WriteFile(mainLogPath, mainLogData, 0644)
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
	s = stripThinkingTags(string(raw))

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
	userPrompt := fmt.Sprintf("Question: %s\n\nTranscript:\n%s", query, transcript)
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
	tags := []struct{ open, close string }{
		{"<think>", "</think>"},
		{"<thinking>", "</thinking>"},
		{"<THINK>", "</THINK>"},
		{"<THINKING>", "</THINKING>"},
	}
	for _, t := range tags {
		for {
			start := strings.Index(s, t.open)
			if start == -1 {
				break
			}
			end := strings.Index(s[start:], t.close)
			if end != -1 {
				s = s[:start] + s[start+end+len(t.close):]
			} else {
				// Unclosed opening tag: remove everything from start onwards
				s = s[:start]
				break
			}
		}
	}
	// Clean up any orphan closing tags
	for _, closeTag := range []string{"</think>", "</thinking>", "</THINK>", "</THINKING>"} {
		s = strings.ReplaceAll(s, closeTag, "")
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

func pruneIrrelevantChunks(ctx context.Context, text string, query string, searchTerms []string, chunkSize, chunkOverlap int, events *[]ChunkPruningEvent) string {
	chunks := engine.ChunkTranscript(text, chunkSize, chunkOverlap)
	var keptChunks []string

	// Build search keywords from query as fallback/expansion
	words := strings.Fields(query)
	stopWords := map[string]struct{}{
		"what": {}, "does": {}, "did": {}, "before": {}, "after": {}, "is": {},
		"the": {}, "a": {}, "an": {}, "where": {}, "why": {}, "who": {}, "how": {},
		"mentioned": {}, "say": {}, "said": {}, "about": {}, "with": {}, "to": {},
		"mention": {}, "type": {}, "first": {}, "second": {}, "between": {}, "from": {},
		"and": {}, "of": {}, "in": {}, "on": {}, "at": {}, "for": {}, "or": {},
		"many": {}, "days": {}, "passed": {}, "when": {}, "had": {}, "have": {},
		"some": {}, "good": {}, "should": {}, "check": {}, "out": {}, "could": {},
		"would": {}, "will": {}, "can": {}, "then": {}, "than": {}, "more": {},
		"better": {}, "best": {}, "are": {}, "was": {}, "were": {}, "been": {},
		"has": {}, "into": {}, "onto": {}, "your": {}, "my": {}, "their": {},
		"his": {}, "her": {}, "our": {}, "they": {}, "them": {}, "him": {},
		"she": {}, "you": {}, "me": {}, "this": {}, "that": {}, "these": {},
		"those": {}, "there": {}, "here": {}, "any": {}, "all": {},
		"please": {}, "help": {}, "sure": {}, "yes": {}, "no": {}, "not": {},
		"but": {}, "so": {}, "very": {}, "just": {}, "like": {},
	}

	allTerms := make([]string, 0, len(searchTerms))
	seenTerm := make(map[string]bool)
	for _, term := range searchTerms {
		tLower := strings.TrimSpace(strings.ToLower(term))
		_, isStop := stopWords[tLower]
		if len(tLower) >= 3 && !isStop && !seenTerm[tLower] {
			seenTerm[tLower] = true
			allTerms = append(allTerms, tLower)
		}
	}

	// Fallback to query words ONLY if we have no filtered search terms
	if len(allTerms) == 0 {
		for _, w := range words {
			wClean := strings.Trim(strings.ToLower(w), "?!.,'\":;")
			_, isStop := stopWords[wClean]
			if len(wClean) >= 3 && !isStop && !seenTerm[wClean] {
				seenTerm[wClean] = true
				allTerms = append(allTerms, wClean)
			}
		}
	}

	type chunkScore struct {
		text  string
		score float64
		index int
	}
	var scoredChunks []chunkScore

	for i, chunk := range chunks {
		if !engine.ValidateTranscriptSemanticCoherence(chunk.Text) {
			continue
		}

		score := 0.0
		chunkLower := strings.ToLower(chunk.Text)
		for _, term := range allTerms {
			count := strings.Count(chunkLower, term)
			score += float64(count)
		}

		scoredChunks = append(scoredChunks, chunkScore{
			text:  chunk.Text,
			score: score,
			index: i,
		})
	}

	// Keep chunks with score > 0, up to a character limit to prevent context window bloat
	maxChars := 65000
	currentChars := 0
	keptIndices := make(map[int]bool)

	type chunkIndexScore struct {
		index int
		score float64
		text  string
	}
	var cisList []chunkIndexScore
	for _, sc := range scoredChunks {
		if sc.score > 0 {
			cisList = append(cisList, chunkIndexScore{
				index: sc.index,
				score: sc.score,
				text:  sc.text,
			})
		}
	}

	sort.Slice(cisList, func(i, j int) bool {
		return cisList[i].score > cisList[j].score
	})

	for _, cis := range cisList {
		keptIndices[cis.index] = true
		currentChars += len(cis.text)
		if currentChars > maxChars {
			break
		}
	}

	// Fallback if no chunks had any overlap: keep all of them within budget (including first overstep)
	if len(keptIndices) == 0 {
		for _, sc := range scoredChunks {
			keptIndices[sc.index] = true
			currentChars += len(sc.text)
			if currentChars > maxChars {
				break
			}
		}
	}

	// Reconstruct kept chunks in chronological order
	for i, chunk := range chunks {
		decision := "NO (Score: 0.0)"
		var score float64
		for _, sc := range scoredChunks {
			if sc.index == i {
				score = sc.score
				break
			}
		}

		if keptIndices[i] {
			keptChunks = append(keptChunks, chunk.Text)
			decision = fmt.Sprintf("YES (Score: %.1f)", score)
		} else if score > 0 {
			decision = fmt.Sprintf("NO (Score: %.1f, Exceeded Budget)", score)
		}

		if events != nil {
			*events = append(*events, ChunkPruningEvent{
				ChunkIndex:   i + 1,
				Text:         chunk.Text,
				SystemPrompt: "Keyword overlap pruning",
				UserPrompt:   strings.Join(allTerms, ", "),
				LlmDecision:  decision,
			})
		}
	}

	return strings.Join(keptChunks, "\n\n")
}

func decomposeQuery(ctx context.Context, llmClient *engine.LLMClient, query string) []string {
	systemPrompt := `You are an expert search specialist for conversational dialogue databases.
Your task is to identify 1 to 3 distinct search focus queries targeting the ACTUAL DIALOGUE PASSAGES THAT CONTAIN THE ANSWER.

Key Search Directives:
1. Target Evidence, Not Request Framing:
   - Identify the concrete subject matter, entities, events, dates, and actions that speakers in the conversation will use when discussing the answer.
   - Strictly DO NOT include request meta-words such as "recommendations", "suggestions", "advice", "guidance", "feedback", "opinions", "check out", "tell me", "explain". People in dialogue do not label their speech with these request words!
   - Examples:
     - Question: "What are some good books I should check out?" -> Focus: "books reading novel author", "favorite book series" (NOT "book recommendations").
     - Question: "What specific advice did Jake give me about documenting prototype tests beyond the April 15 deadline?" -> Focus: "Jake prototype testing logbook", "April 15 deadline prototype" (NOT "Jake advice").
2. Multi-hop decomposition: If the question compares two events, milestones, or people, create a separate focus line for each distinct event/entity.
3. Output each search focus query on a new line. Do not add numbering, bullet points, thinking tags, or introductory commentary.`

	userPrompt := fmt.Sprintf("Question: %q\n\nSearch Focus Queries:", query)
	
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

	cleaned := stripThinkingTags(response)

	var subQueries []string
	lines := strings.Split(cleaned, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "- ")
		trimmed = strings.TrimPrefix(trimmed, "* ")
		if idx := strings.Index(trimmed, ". "); idx != -1 && idx < 3 {
			trimmed = trimmed[idx+2:]
		}
		trimmed = strings.TrimSpace(trimmed)
		lower := strings.ToLower(trimmed)
		if len(trimmed) > 3 &&
			!strings.HasPrefix(lower, "<think") &&
			!strings.HasPrefix(lower, "</think") &&
			!strings.HasPrefix(lower, "<thinking") &&
			!strings.HasPrefix(lower, "</thinking") &&
			!strings.HasPrefix(trimmed, "```") &&
			!strings.HasPrefix(lower, "search queries:") &&
			!strings.HasPrefix(lower, "queries:") &&
			!strings.HasPrefix(lower, "here are") {
			subQueries = append(subQueries, trimmed)
		}
	}
	
	if len(subQueries) == 0 {
		return []string{query}
	}
	if len(subQueries) > 4 {
	subQueries = subQueries[:4]
	}
	return subQueries
}

func isCleanNaturalWord(w string) bool {
	if len(w) < 3 || len(w) > 20 {
		return false
	}
	for _, r := range w {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	// Filter out common code suffixes/substrings
	codeSuffixes := []string{"file", "list", "func", "test", "null", "elem", "var", "pkg", "const", "spec", "stmt"}
	for _, suf := range codeSuffixes {
		if strings.HasSuffix(w, suf) && len(w) > len(suf)+2 {
			return false
		}
	}
	return true
}

func isCleanPhrase(phrase string) bool {
	parts := strings.Fields(phrase)
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	for _, p := range parts {
		if !isCleanNaturalWord(p) || metaRequestWords[p] || stopWords[p] {
			return false
		}
	}
	return true
}

func retrieveCandidatesForQuery(ctx context.Context, query string, targetSpeakers []string, idx *engine.InvertedIndex, embedder engine.Embedder, gllam *engine.GllamEngine, topK int, useUtterancesVectors, useTermsVectors bool, conversationID string, llmClient *engine.LLMClient) ([]string, []string) {
	var unigramTerms []string
	var bigramPhrases []string

	queryTokens := engine.Tokenize(query)
	seenUnigrams := make(map[string]bool)
	for _, tok := range queryTokens {
		if len(tok) > 1 && !stopWords[tok] && !metaRequestWords[tok] && !seenUnigrams[tok] {
			seenUnigrams[tok] = true
			unigramTerms = append(unigramTerms, tok)
		}
	}

	// Add adjacent non-stopword bigrams (e.g. "cover letter", "zoom call", "prototype tests")
	seenBigrams := make(map[string]bool)
	for i := 0; i < len(queryTokens)-1; i++ {
		tok1 := queryTokens[i]
		tok2 := queryTokens[i+1]
		if len(tok1) > 1 && len(tok2) > 1 && !stopWords[tok1] && !metaRequestWords[tok1] && !stopWords[tok2] && !metaRequestWords[tok2] {
			bigram := tok1 + " " + tok2
			if !seenBigrams[bigram] {
				seenBigrams[bigram] = true
				bigramPhrases = append(bigramPhrases, bigram)
			}
		}
	}

	if useTermsVectors {
		var expandedTerms []string
		for _, term := range unigramTerms {
			if nonExpandableTerms[term] || metaRequestWords[term] {
				continue
			}
			tEmb, err := embedder.Embed(ctx, term)
			if err != nil {
				continue
			}
			similar, err := gllam.SearchSimilarTerms(ctx, tEmb, 4)
			if err == nil {
				for _, sim := range similar {
					if isCleanNaturalWord(sim.Term) && !metaRequestWords[sim.Term] && !stopWords[sim.Term] && len(idx.Postings[sim.Term]) >= 2 {
						expandedTerms = append(expandedTerms, sim.Term)
					}
				}
			}
		}
		for _, t := range expandedTerms {
			if !seenUnigrams[t] {
				seenUnigrams[t] = true
				unigramTerms = append(unigramTerms, t)
			}
		}
	}

	if llmClient != nil {
		var combinedCandidates []string
		combinedCandidates = append(combinedCandidates, unigramTerms...)
		combinedCandidates = append(combinedCandidates, bigramPhrases...)
		llmUnigrams, llmPhrases := filterTermsWithLLM(ctx, llmClient, query, combinedCandidates)
		if len(llmUnigrams) > 0 || len(llmPhrases) > 0 {
			unigramTerms = llmUnigrams
			bigramPhrases = llmPhrases
		}
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
		// 1. TF-IDF scoring on unigrams
		uttScores := make(map[string]float64)
		totalUtterances := float64(len(idx.Utterances))
		for _, term := range unigramTerms {
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

		// Exact phrase matching boost for bigrams / multi-word phrases
		for _, phrase := range bigramPhrases {
			phraseMatches := idx.PhraseSearch(phrase)
			for _, uttID := range phraseMatches {
				u, ok := idx.Utterances[uttID]
				if ok && allowedSessions[u.SessionID] {
					uttScores[uttID] += 15.0
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

		var combinedTerms []string
		combinedTerms = append(combinedTerms, unigramTerms...)
		combinedTerms = append(combinedTerms, bigramPhrases...)
		return candidates, combinedTerms
	} else {
		// Only TF-IDF
		uttScores := make(map[string]float64)
		totalUtterances := float64(len(idx.Utterances))
		for _, term := range unigramTerms {
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

		// Exact phrase matching boost for bigrams / multi-word phrases
		for _, phrase := range bigramPhrases {
			phraseMatches := idx.PhraseSearch(phrase)
			for _, uttID := range phraseMatches {
				u, ok := idx.Utterances[uttID]
				if ok && allowedSessions[u.SessionID] {
					score := 15.0
					if matchesAnySpeaker(u.SpeakerID, targetSpeakers) {
						score *= 10.0
					}
					uttScores[uttID] += score
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

		var combinedTerms []string
		combinedTerms = append(combinedTerms, unigramTerms...)
		combinedTerms = append(combinedTerms, bigramPhrases...)
		return candidates, combinedTerms
	}
}

func filterTermsWithLLM(ctx context.Context, llmClient *engine.LLMClient, query string, candidateTerms []string) ([]string, []string) {
	if len(candidateTerms) == 0 && strings.TrimSpace(query) == "" {
		return candidateTerms, nil
	}
	
	systemPrompt := `You are an expert search specialist for conversational dialogue retrieval.
Your objective is to produce search terms (keywords and 2-word phrases) that will match dialogue utterances containing the ACTUAL FACTS, OPINIONS, or ANSWERS, rather than turns that merely echo or re-ask the user question.

CRITICAL RETRIEVAL PRINCIPLES:
1. FILTER OUT ALL REQUEST & META-INTENT VOCABULARY:
   Speakers answering in dialogue discuss facts, entities, and opinions—they do NOT label their speech with request meta-words.
   Strictly EXCLUDE:
   - Request & meta-intent words: "recommendation", "recommendations", "recommend", "suggest", "suggestions", "suggestion", "advice", "advise", "tips", "feedback", "opinion", "guidance".
   - Conversational wrappers: "check", "check out", "tell", "mention", "walk", "explain", "discuss", "help".
   - Generic filler: "good", "best", "some", "specific", "details", "information".
   - Programming artifacts or typos (e.g. "readfile", "suggestionlist", "ngood", "testdata").

2. BROAD RETENTION OF RELEVANT EVIDENCE VOCABULARY:
   DO NOT aggressively prune valid morphological variations, inflections, or related domain concepts.
   Retain all relevant terms and inflections from the candidate list (e.g. read, reads, reading, reader; novel, novels, novella; book, books; love, loved, loving, beloved; series, sequence; genre, genres; library, bookstore).
   Only prune terms that are genuinely off-topic, noisy/irrelevant stopwords, code artifacts, or request meta-words.

3. COMPREHENSIVE ANSWER VOCABULARY EXPANSION:
   Include both the valid candidate terms and additional likely answer vocabulary (e.g. domain entities, actions, synonyms, and attributes that answering speakers would use).
   Aim for a rich, inclusive set of 15 to 30 relevant search terms and key 2-word phrases.
   Output ONLY a JSON array of strings (e.g. ["term1", "term2", ...]). Do not include explanations, thinking tags, or markdown blocks.`

	candJSON, _ := json.Marshal(candidateTerms)
	userPrompt := fmt.Sprintf("User Question: %q\nCandidate Terms: %s\nTarget Answer Search Terms:", query, string(candJSON))
	
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
		var fallbackUnigrams []string
		var fallbackPhrases []string
		for _, t := range candidateTerms {
			if strings.Contains(t, " ") {
				fallbackPhrases = append(fallbackPhrases, t)
			} else {
				fallbackUnigrams = append(fallbackUnigrams, t)
			}
		}
		return fallbackUnigrams, fallbackPhrases
	}
	
	var filtered []string
	cleanedResponse := stripThinkingTags(response)
	if idx := strings.Index(cleanedResponse, "```json"); idx != -1 {
		cleanedResponse = cleanedResponse[idx+7:]
	} else if idx := strings.Index(cleanedResponse, "```"); idx != -1 {
		cleanedResponse = cleanedResponse[idx+3:]
	}
	if idx := strings.LastIndex(cleanedResponse, "```"); idx != -1 {
		cleanedResponse = cleanedResponse[:idx]
	}
	cleanedResponse = strings.TrimSpace(cleanedResponse)
	if start := strings.Index(cleanedResponse, "["); start != -1 {
		if end := strings.LastIndex(cleanedResponse, "]"); end != -1 && end > start {
			cleanedResponse = cleanedResponse[start : end+1]
		}
	}
	
	var unigrams []string
	var phrases []string
	seenU := make(map[string]bool)
	seenP := make(map[string]bool)

	if err := json.Unmarshal([]byte(cleanedResponse), &filtered); err == nil && len(filtered) > 0 {
		for _, ft := range filtered {
			ft = strings.ToLower(strings.TrimSpace(ft))
			if strings.Contains(ft, " ") {
				if isCleanPhrase(ft) && !seenP[ft] {
					seenP[ft] = true
					phrases = append(phrases, ft)
				}
			} else {
				if isCleanNaturalWord(ft) && !metaRequestWords[ft] && !stopWords[ft] && !seenU[ft] {
					seenU[ft] = true
					unigrams = append(unigrams, ft)
				}
			}
		}
	}
	
	// Allow valid candidate terms to filter through as well (preserving domain vocabulary, inflections, and relevant bigrams)
	for _, ct := range candidateTerms {
		ct = strings.ToLower(strings.TrimSpace(ct))
		if strings.Contains(ct, " ") {
			if isCleanPhrase(ct) && !seenP[ct] {
				seenP[ct] = true
				phrases = append(phrases, ct)
			}
		} else {
			if isCleanNaturalWord(ct) && !metaRequestWords[ct] && !stopWords[ct] && !nonExpandableTerms[ct] && len(ct) > 1 && !seenU[ct] {
				seenU[ct] = true
				unigrams = append(unigrams, ct)
			}
		}
	}

	if len(unigrams) > 0 || len(phrases) > 0 {
		var allFilt []string
		allFilt = append(allFilt, unigrams...)
		allFilt = append(allFilt, phrases...)
		origJSON, _ := json.Marshal(candidateTerms)
		filtJSON, _ := json.Marshal(allFilt)
		fmt.Printf("   ├─ LLM identified answer search terms: original %d %s -> answer terms %d %s\n", len(candidateTerms), string(origJSON), len(allFilt), string(filtJSON))
		return unigrams, phrases
	}
	
	var fallbackUnigrams []string
	var fallbackPhrases []string
	for _, t := range candidateTerms {
		if strings.Contains(t, " ") {
			fallbackPhrases = append(fallbackPhrases, t)
		} else {
			fallbackUnigrams = append(fallbackUnigrams, t)
		}
	}
	return fallbackUnigrams, fallbackPhrases
}

func getClientForTask(taskName string, defaultTier string, strongClient, fastClient, defaultClient *engine.LLMClient) *engine.LLMClient {
	tier := getEnv(taskName, defaultTier)
	if tier == "STRONG_TEXT_SERVER" && strongClient != nil {
		return strongClient
	}
	if tier == "FAST_TEXT_SERVER" && fastClient != nil {
		return fastClient
	}
	
	// Fallback to whichever client is configured
	if strongClient != nil {
		return strongClient
	}
	if fastClient != nil {
		return fastClient
	}
	return defaultClient
}
