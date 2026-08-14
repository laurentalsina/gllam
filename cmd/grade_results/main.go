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
	defaultTextServer := "http://100.96.179.19:8888"
	if os.Getenv("OPENROUTER_API_KEY") != "" {
		defaultTextServer = "https://openrouter.ai/api/v1"
	}
	textEndpoint := flag.String("text-server", defaultTextServer, "LLM text server")
	verbose := flag.Bool("verbose", true, "Print detailed Query, Ground Truth, and Model Answer breakdown for failed items")
	failReportPath := flag.String("fail-report", "", "Optional path to save failures markdown report")
	flag.Parse()

	if os.Getenv("OPENROUTER_API_KEY") != "" && (*textEndpoint == "http://100.96.179.19:8888" || *textEndpoint == defaultTextServer) {
		*textEndpoint = "https://openrouter.ai/api/v1"
	}

	file, err := os.Open(*resultsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open results file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	llmClient := engine.NewLLMClient(*textEndpoint)
	ctx := context.Background()

	scanner := bufio.NewScanner(file)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB max token size buffer
	total := 0
	correct := 0
	var failedResults []Result

	fmt.Println("Starting grading...")

	for scanner.Scan() {
		line := scanner.Bytes()
		var res Result
		if err := json.Unmarshal(line, &res); err != nil {
			fmt.Println("Error parsing JSON:", err)
			continue
		}

		total++
		
		// If the model had an error or explicitly states it's missing, it's a FAIL
		if res.ModelAnswer == "ERROR" || strings.Contains(strings.ToLower(res.ModelAnswer), "there is no mention") {
			failedResults = append(failedResults, res)
			fmt.Printf("[%s] FAIL (Missing/Error)\n", res.InstanceID)
			if *verbose {
				fmt.Printf("   ├─ Query: %s\n", res.Query)
				fmt.Printf("   ├─ Ground Truth: %s\n", res.GroundTruth)
				fmt.Printf("   └─ Model Answer: %s\n\n", res.ModelAnswer)
			}
			continue
		}

		systemPrompt := `You are a strict evaluation judge for AI memory systems. You will be provided with a Question, a Ground Truth answer, and a Model Answer.
Your task is to determine if the Model Answer is strictly correct based on the Ground Truth.

Evaluation Rules:
1. Reply "FAIL" if the Model Answer contains internal self-contradictions (e.g. asserting an event happened in the past before a conversation, but concluding it happened after).
2. Reply "FAIL" if the Model Answer reaches a different temporal ordering conclusion than the Ground Truth.
3. Reply "PASS" ONLY if the Model Answer is factually equivalent to the Ground Truth without internal contradictions.
Do not provide any explanations, just "PASS" or "FAIL".`

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
		if strings.Contains(strings.ToUpper(verdict), "PASS") && !strings.Contains(strings.ToUpper(verdict), "FAIL") {
			correct++
			fmt.Printf("[%s] PASS\n", res.InstanceID)
		} else {
			failedResults = append(failedResults, res)
			cleanVerdict := strings.TrimSpace(strings.ReplaceAll(verdict, "FAIL", ""))
			cleanVerdict = strings.Trim(cleanVerdict, " :-.\n")
			if cleanVerdict != "" {
				fmt.Printf("[%s] FAIL (%s)\n", res.InstanceID, cleanVerdict)
			} else {
				fmt.Printf("[%s] FAIL\n", res.InstanceID)
			}
			if *verbose {
				fmt.Printf("   ├─ Query: %s\n", res.Query)
				fmt.Printf("   ├─ Ground Truth: %s\n", res.GroundTruth)
				fmt.Printf("   └─ Model Answer: %s\n\n", res.ModelAnswer)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning results file: %v\n", err)
	}

	if total > 0 {
		accuracy := float64(correct) / float64(total) * 100
		fmt.Printf("\n--- Final Evaluation Score ---\nTotal Evaluated: %d\nPassed: %d\nFailed: %d\nAccuracy Score: %.2f%%\n", total, correct, total-correct, accuracy)
	} else {
		fmt.Println("No results found to grade.")
	}

	if *failReportPath != "" && len(failedResults) > 0 {
		var report strings.Builder
		report.WriteString(fmt.Sprintf("# Benchmark Failure Diagnostic Report\n\nTotal Failed Items: %d\n\n", len(failedResults)))
		for _, f := range failedResults {
			report.WriteString(fmt.Sprintf("## [%s] %s\n", f.InstanceID, f.Query))
			report.WriteString(fmt.Sprintf("- **Ground Truth**: %s\n", f.GroundTruth))
			report.WriteString(fmt.Sprintf("- **Model Answer**: %s\n\n---\n\n", f.ModelAnswer))
		}
		_ = os.WriteFile(*failReportPath, []byte(report.String()), 0644)
		fmt.Printf("📝 Saved failure diagnostic report to: %s\n", *failReportPath)
	}
}
