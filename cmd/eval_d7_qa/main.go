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

func main() {
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	qaPath := flag.String("qa", "./d7_qa.jsonl", "Path to d7_qa.jsonl")
	outPath := flag.String("out", "./d7_qa_results.jsonl", "Output path")
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
		var qa QAInstance
		if err := json.Unmarshal(line, &qa); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse JSON: %v\n", err)
			continue
		}

		fmt.Printf("Evaluating [%s]: %s\n", qa.InstanceID, qa.Query)
		
		llmClient := engine.NewLLMClient("http://100.96.179.19:8888")
		compiled, err := gllam.RouteAndAssemble(ctx, qa.Query, nil)
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
