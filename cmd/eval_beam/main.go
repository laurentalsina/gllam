package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

type BEAMQAInstance struct {
	InstanceID     string   `json:"instance_id"`
	ConversationID string   `json:"conversation_id"`
	Category       string   `json:"category"`
	Query          string   `json:"query"`
	GroundTruth    string   `json:"ground_truth"`
	Difficulty     string   `json:"difficulty"`
	Rubric         []string `json:"rubric"`
}

type BEAMResult struct {
	Query        string   `json:"query"`
	InstanceID   string   `json:"instance_id"`
	Category     string   `json:"category"`
	ModelAnswer  string   `json:"model_answer"`
	GroundTruth  string   `json:"ground_truth"`
	Rubric       []string `json:"rubric"`
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func main() {
	dbPath := flag.String("db", getEnv("DATABASE_PATH", "./gllam_data.db"), "Path to SQLite database")
	qaPath := flag.String("qa", "/home/laurent/Projects/agentic_benchmarks/beam_100k_qa.jsonl", "Path to beam qa jsonl")
	outPath := flag.String("out", "./beam_100k_results.jsonl", "Output path")
	limit := flag.Int("limit", 0, "Limit number of queries (0 for all)")
	textServer := flag.String("text-server", getEnv("TEXT_SERVER", "http://127.0.0.1:8888"), "LLM text server endpoint")
	embeddingServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", "http://127.0.0.1:8800"), "Embeddings server endpoint")
	promptsPath := flag.String("prompts-config", getEnv("PROMPTS_CONFIG", "config/agentic_memory.json"), "Path to agentic memory config and prompts")
	flag.Parse()

	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder(*embeddingServer)
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.LoadSystemPromptsConfig(*promptsPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load system prompts config: %v\n", err)
		os.Exit(1)
	}

	plannerPath := getEnv("GLLAM_PLANNER_EXECUTABLE_PATH", "")
	if plannerPath != "" {
		gllam.SetPlannerExecutablePath(plannerPath)
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
		var qa BEAMQAInstance
		if err := json.Unmarshal(line, &qa); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse JSON: %v\n", err)
			continue
		}

		fmt.Printf("Evaluating [%s] Category: %s\n", qa.InstanceID, qa.Category)
		
		strongServerEnv := getEnv("STRONG_TEXT_SERVER", "")
		strongModelEnv := getEnv("STRONG_LLM_MODEL", "")
		fastServerEnv := getEnv("FAST_TEXT_SERVER", "")
		fastModelEnv := getEnv("FAST_LLM_MODEL", "")

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
		
		// Optional: prepend conversation ID to query to help disambiguate cross-conversation leakage
		// though vector search should naturally prioritize exact semantic matches.
		promptCtx := fmt.Sprintf("[Conversation %s context] %s", qa.ConversationID, qa.Query)
		
		compiled, err := gllam.RouteAndAssemble(ctx, promptCtx, nil)
		var answer string
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error routing %s: %v\n", qa.InstanceID, err)
			answer = "ERROR"
		} else {
			prompt := engine.FormatSystemPrompt(compiled)
			if gllam.SystemPrompts != nil && gllam.SystemPrompts.CustomCategoryPrompts != nil {
				if catPrompt, ok := gllam.SystemPrompts.CustomCategoryPrompts[qa.Category]; ok && catPrompt != "" {
					prompt = prompt + "\n\n" + catPrompt
				}
			}
			answer, err = getClientForTask("BENCH_RESULT_EVALUATION", "STRONG_TEXT_SERVER", strongClient, fastClient, defaultClient).Generate(ctx, prompt, qa.Query)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error generating answer for %s: %v\n", qa.InstanceID, err)
				answer = "ERROR"
			}
		}

		res := BEAMResult{
			InstanceID:  qa.InstanceID,
			Category:    qa.Category,
			Query:       qa.Query,
			ModelAnswer: answer,
			GroundTruth: qa.GroundTruth,
			Rubric:      qa.Rubric,
		}

		resBytes, _ := json.Marshal(res)
		outFile.Write(resBytes)
		outFile.WriteString("\n")

		count++
	}

	fmt.Printf("Completed %d BEAM evaluations. Results saved to %s\n", count, *outPath)
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
