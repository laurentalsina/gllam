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
	InstanceID   string   `json:"instance_id"`
	Category     string   `json:"category"`
	Query        string   `json:"query"`
	ModelAnswer  string   `json:"model_answer"`
	GroundTruth  string   `json:"ground_truth"`
	Rubric       []string `json:"rubric"`
}

func main() {
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	qaPath := flag.String("qa", "/home/laurent/Projects/agentic_benchmarks/beam_100k_qa.jsonl", "Path to beam qa jsonl")
	outPath := flag.String("out", "./beam_100k_results.jsonl", "Output path")
	limit := flag.Int("limit", 0, "Limit number of queries (0 for all)")
	flag.Parse()

	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

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
		
		llmClient := engine.NewLLMClient("http://100.96.179.19:8888")
		
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
			answer, err = llmClient.Generate(ctx, prompt, qa.Query)
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
