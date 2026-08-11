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

	"github.com/laurentalsina/gllam/pkg/engine"
)

type Result struct {
	InstanceID  string `json:"instance_id"`
	Query       string `json:"query"`
	ModelAnswer string `json:"model_answer"`
	GroundTruth string `json:"ground_truth"`
}

func main() {
	resultsPath := flag.String("results", "./d7_qa_results.jsonl", "Path to results JSONL")
	textEndpoint := flag.String("text-server", "http://100.96.179.19:8888", "LLM text server")
	flag.Parse()

	file, err := os.Open(*resultsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open results file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	llmClient := engine.NewLLMClient(*textEndpoint)
	ctx := context.Background()

	scanner := bufio.NewScanner(file)
	total := 0
	correct := 0

	fmt.Println("Starting grading...")

	for scanner.Scan() {
		line := scanner.Bytes()
		var res Result
		if err := json.Unmarshal(line, &res); err != nil {
			fmt.Println("Error parsing JSON:", err)
			continue
		}

		total++
		
		// If the model had an error or explicitly states it's missing, it's incorrect
		if res.ModelAnswer == "ERROR" || strings.Contains(strings.ToLower(res.ModelAnswer), "there is no mention") {
			fmt.Printf("[%s] INCORRECT (Missing/Error)\n", res.InstanceID)
			continue
		}

		systemPrompt := `You are a strict evaluation judge for AI memory systems. You will be provided with a Question, a Ground Truth answer, and a Model Answer.
Your task is to determine if the Model Answer is strictly correct based on the Ground Truth.

Evaluation Rules:
1. Reply "INCORRECT" if the Model Answer contains internal self-contradictions (e.g. asserting an event happened in the past before a conversation, but concluding it happened after).
2. Reply "INCORRECT" if the Model Answer reaches a different temporal ordering conclusion than the Ground Truth.
3. Reply "CORRECT" ONLY if the Model Answer is factually equivalent to the Ground Truth without internal contradictions.
Do not provide any explanations, just "CORRECT" or "INCORRECT".`

		userPrompt := fmt.Sprintf("Question: %s\nGround Truth: %s\nModel Answer: %s\n\nVerdict:", res.Query, res.GroundTruth, res.ModelAnswer)

		verdict, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err != nil {
			time.Sleep(2 * time.Second)
			verdict, err = llmClient.Generate(ctx, systemPrompt, userPrompt)
		}
		if err != nil {
			fmt.Printf("LLM grading failed for %s: %v\n", res.InstanceID, err)
			continue
		}

		verdict = strings.TrimSpace(verdict)
		if strings.Contains(strings.ToUpper(verdict), "CORRECT") && !strings.Contains(strings.ToUpper(verdict), "INCORRECT") {
			correct++
			fmt.Printf("[%s] CORRECT\n", res.InstanceID)
		} else {
			fmt.Printf("[%s] INCORRECT (Verdict: %s)\n", res.InstanceID, verdict)
		}
	}

	if total > 0 {
		accuracy := float64(correct) / float64(total) * 100
		fmt.Printf("\n--- Final Score ---\nTotal: %d\nCorrect: %d\nAccuracy: %.2f%%\n", total, correct, accuracy)
	} else {
		fmt.Println("No results found to grade.")
	}
}
