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
	Query        string   `json:"query"`
	InstanceID   string   `json:"instance_id"`
	Category     string   `json:"category"`
	ModelAnswer  string   `json:"model_answer"`
	GroundTruth  string   `json:"ground_truth"`
	Rubric       []string `json:"rubric"`
}

type EvaluationDetail struct {
	Query       string   `json:"query"`
	InstanceID  string   `json:"instance_id"`
	Category    string   `json:"category"`
	ModelAnswer string   `json:"model_answer"`
	GroundTruth string   `json:"ground_truth"`
	Rubric      []string `json:"rubric"`
	Verdict     string   `json:"verdict"` // "CORRECT", "INCORRECT", "ERROR"
	Explanation string   `json:"explanation,omitempty"`
}

type CategorySummary struct {
	Total           int     `json:"total"`
	Correct         int     `json:"correct"`
	AccuracyPercent float64 `json:"accuracy_percent"`
}

type GradingSummary struct {
	TotalAttempted   int     `json:"total_attempted"`
	ErrorsCount      int     `json:"errors_count"`
	CorrectCount     int     `json:"correct_count"`
	AccuracyPercent  float64 `json:"accuracy_percent"`
}

type GradingReport struct {
	Summary     GradingSummary             `json:"summary"`
	Categories  map[string]CategorySummary `json:"categories"`
	Evaluations []EvaluationDetail         `json:"evaluations"`
}

func main() {
	resultsPath := flag.String("results", "./beam_100k_results_sample50.jsonl", "Path to results JSONL")
	textEndpoint := flag.String("text-server", "http://100.96.179.19:8888", "LLM text server")
	outputPath := flag.String("output", "", "Path to output JSON file (prints to stdout if empty)")
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
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var evaluations []EvaluationDetail
	categoryStats := make(map[string]struct{ total, correct int })
	total := 0
	correct := 0
	errorsCount := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		var res BEAMResult
		if err := json.Unmarshal(line, &res); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing JSON line: %v\n", err)
			continue
		}

		total++
		stats := categoryStats[res.Category]
		stats.total++

		rubricText := strings.Join(res.Rubric, "\n- ")

		if res.ModelAnswer == "ERROR" {
			evaluations = append(evaluations, EvaluationDetail{
				InstanceID:  res.InstanceID,
				Category:    res.Category,
				Query:       res.Query,
				ModelAnswer: res.ModelAnswer,
				GroundTruth: res.GroundTruth,
				Rubric:      res.Rubric,
				Verdict:     "INCORRECT",
				Explanation: "Model generation failed or timed out.",
			})
			errorsCount++
			categoryStats[res.Category] = stats
			continue
		}

		systemPrompt := `You are an expert evaluator for an agentic AI memory benchmark. 
You will be provided with a Question, a Ground Truth answer, a Grading Rubric, and the Model Answer.
Your task is to determine if the Model Answer is correct based on the Ground Truth and Rubric.
First, analyze the model's response against the rubric and explain why it is correct or incorrect.
Then, conclude your evaluation with a final line starting with "Verdict: " followed by either "CORRECT" or "INCORRECT".`

		userPrompt := fmt.Sprintf("Question: %s\nGround Truth: %s\nRubric:\n- %s\n\nModel Answer: %s\n\nEvaluation:", res.Query, res.GroundTruth, rubricText, res.ModelAnswer)

		verdict, err := llmClient.Generate(ctx, systemPrompt, userPrompt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "LLM grading failed for %s: %v\n", res.InstanceID, err)
			evaluations = append(evaluations, EvaluationDetail{
				InstanceID:  res.InstanceID,
				Category:    res.Category,
				Query:       res.Query,
				ModelAnswer: res.ModelAnswer,
				GroundTruth: res.GroundTruth,
				Rubric:      res.Rubric,
				Verdict:     "ERROR",
				Explanation: fmt.Sprintf("Grading failed: %v", err),
			})
			categoryStats[res.Category] = stats
			continue
		}

		verdict = strings.TrimSpace(verdict)
		isCorrect := false
		lines := strings.Split(verdict, "\n")
		var explanationLines []string
		for _, line := range lines {
			trimmedLine := strings.TrimSpace(line)
			if strings.HasPrefix(strings.ToUpper(trimmedLine), "VERDICT:") {
				if strings.Contains(strings.ToUpper(trimmedLine), "CORRECT") && !strings.Contains(strings.ToUpper(trimmedLine), "INCORRECT") {
					isCorrect = true
				}
			} else {
				explanationLines = append(explanationLines, line)
			}
		}

		explanationText := strings.TrimSpace(strings.Join(explanationLines, "\n"))

		verdictStr := "INCORRECT"
		if isCorrect {
			correct++
			stats.correct++
			verdictStr = "CORRECT"
		}
		categoryStats[res.Category] = stats

		evaluations = append(evaluations, EvaluationDetail{
			InstanceID:  res.InstanceID,
			Category:    res.Category,
			Query:       res.Query,
			ModelAnswer: res.ModelAnswer,
			GroundTruth: res.GroundTruth,
			Rubric:      res.Rubric,
			Verdict:     verdictStr,
			Explanation: explanationText,
		})
	}

	var report GradingReport
	if total > 0 {
		report.Summary = GradingSummary{
			TotalAttempted:  total,
			ErrorsCount:     errorsCount,
			CorrectCount:    correct,
			AccuracyPercent: float64(correct) / float64(total) * 100,
		}

		report.Categories = make(map[string]CategorySummary)
		for cat, stats := range categoryStats {
			catAcc := 0.0
			if stats.total > 0 {
				catAcc = float64(stats.correct) / float64(stats.total) * 100
			}
			report.Categories[cat] = CategorySummary{
				Total:           stats.total,
				Correct:         stats.correct,
				AccuracyPercent: catAcc,
			}
		}
	}
	report.Evaluations = evaluations

	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal grading report: %v\n", err)
		os.Exit(1)
	}

	if *outputPath != "" {
		if err := os.WriteFile(*outputPath, jsonBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write output file: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Grading completed successfully! Report saved to: %s\n", *outputPath)
	} else {
		fmt.Println(string(jsonBytes))
	}
}
