package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
	"strconv"

	"github.com/laurentalsina/gllam/pkg/engine"
)

type QAInstance struct {
	InstanceID         string `json:"instance_id"`
	Query              string `json:"query"`
	GroundTruth        struct {
		Answer string `json:"answer"`
	} `json:"ground_truth"`
}

type Result struct {
	InstanceID   string `json:"instance_id"`
	Query        string `json:"query"`
	ModelAnswer  string `json:"model_answer"`
	GroundTruth  string `json:"ground_truth"`
}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if valueStr, exists := os.LookupEnv(key); exists {
		if value, err := strconv.Atoi(valueStr); err == nil {
			return value
		}
	}
	return fallback
}

func main() {
	dbPath := flag.String("dbpath", getEnv("DATABASE_PATH", "./bench/gllam_data.db"), "Path to SQLite database (env: DATABASE_PATH)")
	embeddingsServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", ""), "Embeddings server endpoint (env: EMBEDDINGS_SERVER)")

	qaPath := flag.String("qa", getEnv("QA_PATH", "./d7_qa.jsonl"), "Path to d7_qa.jsonl (env: QA_PATH)")
	outPath := flag.String("out", getEnv("OUT_PATH", "./d7_qa_results.jsonl"), "Output path (env: OUT_PATH)")
	limit := flag.Int("limit", getEnvInt("LIMIT", 0), "Limit number of queries (0 for all) (env: LIMIT)")
	
	flag.Parse()

	strongServerEnv := getEnv("STRONG_TEXT_SERVER", "")
	strongModelEnv := getEnv("STRONG_LLM_MODEL", "")
	fastServerEnv := getEnv("FAST_TEXT_SERVER", "")
	fastModelEnv := getEnv("FAST_LLM_MODEL", "")

	if strongServerEnv == "" && fastServerEnv == "" {
		fmt.Fprintf(os.Stderr, "❌ Error: Neither STRONG_TEXT_SERVER nor FAST_TEXT_SERVER is set in the environment!\n")
		os.Exit(1)
	}
	if *embeddingsServer == "" {
		fmt.Fprintf(os.Stderr, "❌ Error: Embeddings server not specified. Pass --embeddings-server or export EMBEDDINGS_SERVER\n")
		os.Exit(1)
	}

	taskTier := getEnv("BENCH_RESULT_EVALUATION", "STRONG_TEXT_SERVER")
	var llmClient *engine.LLMClient
	if taskTier == "STRONG_TEXT_SERVER" && strongServerEnv != "" {
		llmClient = engine.NewLLMClientWithKey(strongServerEnv, os.Getenv("OPENROUTER_API_KEY"), strongModelEnv)
		llmClient.Tier = "strong"
	} else if fastServerEnv != "" {
		llmClient = engine.NewLLMClientWithKey(fastServerEnv, "", fastModelEnv)
		llmClient.Tier = "fast"
	} else {
		llmClient = engine.NewLLMClientWithKey(strongServerEnv, os.Getenv("OPENROUTER_API_KEY"), strongModelEnv)
		llmClient.Tier = "strong"
	}

	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder(*embeddingsServer)
	
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

	modelName := os.Getenv("LLM_MODEL")
	if modelName == "" {
		modelName = "local-server"
	}

	fmt.Printf("=======================================================\n")
	fmt.Printf("🚀 Starting Evaluation Engine\n")
	fmt.Printf("   ├─ Endpoint: %s\n", llmClient.BaseURL)
	fmt.Printf("   └─ Target Model: %s\n", llmClient.Model)
	fmt.Printf("=======================================================\n\n")

	var nodeCount int
	if err := gllam.DBRO().QueryRowContext(ctx, "SELECT count(*) FROM semantic_nodes").Scan(&nodeCount); err == nil {
		fmt.Printf("Loaded GllamEngine with %d semantic nodes in database.\n", nodeCount)
		if nodeCount < 50 {
			fmt.Printf("⚠️ WARNING: Only %d semantic nodes found. Run 'extract_semantics --prefix sess_' to populate semantic_nodes!\n", nodeCount)
		}
	}

	file, err := os.Open(*qaPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open qa file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	outFile, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create output file: %v\n", err)
		os.Exit(1)
	}
	defer outFile.Close()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)
	count := 0
	for scanner.Scan() {
		if *limit > 0 && count >= *limit {
			break
		}
		
		line := scanner.Bytes()
		var qa QAInstance
		if err := json.Unmarshal(line, &qa); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse JSON: %v\n", err)
			continue
		}

		fmt.Printf("Evaluating [%s]: %s\n", qa.InstanceID, qa.Query)
		
		compiled, err := gllam.RouteAndAssemble(ctx, qa.Query, nil)
		var answer string
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error routing %s: %v\n", qa.InstanceID, err)
			answer = "ERROR"
		} else {
			prompt := engine.FormatSystemPrompt(compiled)
			fmt.Printf("   ├─ Context Assembled: %d semantic nodes, %d links, %d episodic summaries\n",
				len(compiled.SemanticNodes), len(compiled.SemanticLinks), len(compiled.Episodic))
			fmt.Printf("   └─ Prompt Size: %d chars (~%d tokens). Submitting to LLM...\n", len(prompt), len(prompt)/4)

			var genErr error
			for attempt := 1; attempt <= 3; attempt++ {
				answer, genErr = llmClient.Generate(ctx, prompt, qa.Query)
				if genErr == nil && strings.TrimSpace(answer) != "" {
					break
				}
				if genErr == nil {
					genErr = fmt.Errorf("empty response received from LLM")
				}
				fmt.Fprintf(os.Stderr, "⚠️ Stream / empty error for %s (attempt %d/3): %v. Retrying in %ds...\n", qa.InstanceID, attempt, genErr, attempt*2)
				time.Sleep(time.Duration(attempt*2) * time.Second)
			}
			if genErr != nil {
				fmt.Fprintf(os.Stderr, "❌ Error generating answer for %s after 3 attempts: %v\n", qa.InstanceID, genErr)
				answer = "ERROR"
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
	}

	fmt.Printf("Completed %d evaluations. Results saved to %s\n", count, *outPath)
}
