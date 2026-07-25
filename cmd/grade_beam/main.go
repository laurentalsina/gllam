package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/laurentalsina/gllam/pkg/engine"
)

type BEAMResult struct {
	InstanceID   string   `json:"instance_id"`
	Category     string   `json:"category"`
	Query        string   `json:"query"`
	ModelAnswer  string   `json:"model_answer"`
	GroundTruth  string   `json:"ground_truth"`
	Rubric       []string `json:"rubric"`
}

func main() {
	resultsPath := flag.String("results", "./beam_100k_results_sample50.jsonl", "Path to results JSONL")
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
	
	categoryStats := make(map[string]struct{ total, correct int })
	total := 0
	correct := 0
	errorsCount := 0

	fmt.Println("Starting BEAM grading...")

	for scanner.Scan() {
		line := scanner.Bytes()
		var res BEAMResult
		if err := json.Unmarshal(line, &res); err != nil {
			fmt.Println("Error parsing JSON:", err)
			continue
		}

		total++
		stats := categoryStats[res.Category]
		stats.total++
		
		if res.ModelAnswer == "ERROR" {
			fmt.Printf("[%s] INCORRECT (Generation Error)\n", res.InstanceID)
			errorsCount++
			categoryStats[res.Category] = stats
			continue
		}

		rubricText := strings.Join(res.Rubric, "\n- ")
		systemPrompt := `You are an expert evaluator for an agentic AI memory benchmark. 
You will be provided with a Question, a Ground Truth answer, a Grading Rubric, and the Model Answer.
Your task is to determine if the Model Answer is correct based on the Ground Truth and Rubric.
If the Model Answer satisfies the rubric requirements, reply with exactly "CORRECT".
If it fails to satisfy the rubric or contradicts the ground truth, reply with exactly "INCORRECT".
Do not provide any explanations, just "CORRECT" or "INCORRECT".`

		userPrompt := fmt.Sprintf("Question: %s\nGround Truth: %s\nRubric:\n- %s\n\nModel Answer: %s\n\nVerdict:", res.Query, res.GroundTruth, rubricText, res.ModelAnswer)

		verdict, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err != nil {
			fmt.Printf("LLM grading failed for %s: %v\n", res.InstanceID, err)
			categoryStats[res.Category] = stats
			continue
		}

		verdict = strings.TrimSpace(verdict)
		// Clean up the verdict (sometimes models say "Verdict: CORRECT" instead of just "CORRECT")
		if strings.Contains(strings.ToUpper(verdict), "CORRECT") && !strings.Contains(strings.ToUpper(verdict), "INCORRECT") {
			correct++
			stats.correct++
			fmt.Printf("[%s] CORRECT\n", res.InstanceID)
		} else {
			fmt.Printf("[%s] INCORRECT (Verdict: %s)\n", res.InstanceID, verdict)
		}
		categoryStats[res.Category] = stats
	}

	if total > 0 {
		accuracy := float64(correct) / float64(total) * 100
		fmt.Printf("\n--- Final BEAM Score ---\nTotal Attempted: %d\nGeneration Errors/Timeouts: %d\nCorrect: %d\nOverall Accuracy: %.2f%%\n\n", total, errorsCount, correct, accuracy)
		
		fmt.Println("--- Accuracy by Category ---")
		for cat, stats := range categoryStats {
			catAcc := float64(stats.correct) / float64(stats.total) * 100
			fmt.Printf("%-30s : %d/%d (%.2f%%)\n", cat, stats.correct, stats.total, catAcc)
		}
	} else {
		fmt.Println("No results found to grade.")
	}
}
