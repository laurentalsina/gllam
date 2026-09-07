package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/laurentalsina/gllam/pkg/config"
	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	corpusPath := flag.String("corpus", "", "Path to input beam conversations JSONL")
	outputPath := flag.String("out", "", "Path to output compressed JSONL")
	qaPath := flag.String("qa", "", "Path to QA JSONL (for filtering target conversations)")
	categories := flag.String("categories", "", "Comma-separated target categories (e.g. temporal_reasoning)")
	conversations := flag.String("conversations", "", "Comma-separated target conversation IDs (e.g. 8,13)")
	cacheDB := flag.String("cache-db", "./bench/beam/beam_preprocess_cache.db", "Path to SQLite cache DB")
	llmServer := flag.String("llm-server", "", "LLM server endpoint (defaults to FAST_TEXT_SERVER or STRONG_TEXT_SERVER)")
	promptsConfig := flag.String("prompts-config", "config/beam_prompts.json", "Path to prompts config JSON")
	targetCompression := flag.Int("target-compression", 40, "Target word count as percent of original (default 40)")
	reductionPercent := flag.Int("reduction-percent", 0, "Target reduction percent (defaults to 100 - target-compression)")
	minWords := flag.Int("min-words", 15, "Minimum word count to trigger compression (default 15)")
	concurrency := flag.Int("concurrency", 4, "Number of concurrent workers (default 4)")
	force := flag.Bool("force", false, "Force re-compression even if cached")
	flag.Parse()

	if *corpusPath == "" || *outputPath == "" {
		fmt.Println("Usage: preprocess_beam --corpus <input.jsonl> --out <output.jsonl> [options]")
		flag.PrintDefaults()
		os.Exit(1)
	}

	if *reductionPercent <= 0 {
		*reductionPercent = 100 - *targetCompression
	}

	serverURL := *llmServer
	if serverURL == "" {
		serverURL = os.Getenv("FAST_TEXT_SERVER")
		if serverURL == "" {
			serverURL = os.Getenv("STRONG_TEXT_SERVER")
		}
		if serverURL == "" {
			serverURL = "http://127.0.0.1:13305"
		}
	}

	// Load prompt configuration
	promptTemplate := config.DefaultPreprocessCompressionPrompt
	if *promptsConfig != "" {
		if cfg, err := config.LoadAgenticMemoryConfig(*promptsConfig); err == nil && cfg.PreprocessCompressionPrompt != "" {
			promptTemplate = cfg.PreprocessCompressionPrompt
		}
	}

	// Filter target conversations
	targetConvIDs := make(map[string]bool)
	if *conversations != "" {
		for _, id := range strings.Split(*conversations, ",") {
			clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(id), "BEAM_100K_Conversation_"), "Conversation_")
			if clean != "" {
				targetConvIDs[clean] = true
			}
		}
	} else if *qaPath != "" {
		catFilter := make(map[string]bool)
		if *categories != "" && strings.ToLower(*categories) != "all" {
			for _, c := range strings.Split(*categories, ",") {
				c = strings.TrimSpace(strings.ToLower(c))
				if c != "" {
					catFilter[c] = true
				}
			}
		}

		qaFile, err := os.Open(*qaPath)
		if err == nil {
			defer qaFile.Close()
			scanner := bufio.NewScanner(qaFile)
			for scanner.Scan() {
				var q struct {
					ConversationID string `json:"conversation_id"`
					Category       string `json:"category"`
				}
				if err := json.Unmarshal(scanner.Bytes(), &q); err == nil {
					if len(catFilter) == 0 || catFilter[strings.ToLower(q.Category)] {
						clean := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(q.ConversationID), "BEAM_100K_Conversation_"), "Conversation_")
						if clean != "" {
							targetConvIDs[clean] = true
						}
					}
				}
			}
		}
	}

	fmt.Println("=======================================================")
	fmt.Println("🧹 Starting BEAM Pre-processing & Turn Compression")
	fmt.Printf("   ├─ Input Corpus: %s\n", *corpusPath)
	fmt.Printf("   ├─ Output Corpus: %s\n", *outputPath)
	fmt.Printf("   ├─ Cache DB: %s\n", *cacheDB)
	fmt.Printf("   ├─ LLM Server: %s\n", serverURL)
	fmt.Printf("   ├─ Target Compression: %d%% (Target Reduction: %d%%)\n", *targetCompression, *reductionPercent)
	fmt.Printf("   ├─ Concurrency: %d workers\n", *concurrency)
	fmt.Printf("   ├─ Min Words to Compress: %d\n", *minWords)
	if len(targetConvIDs) > 0 {
		var targetList []string
		for id := range targetConvIDs {
			targetList = append(targetList, id)
		}
		fmt.Printf("   ├─ Filtered Target Conversations (%d): %s\n", len(targetList), strings.Join(targetList, ", "))
	} else {
		fmt.Printf("   ├─ Target: All conversations in corpus\n")
	}
	fmt.Println("=======================================================")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	llmClient := engine.NewLLMClient(serverURL)
	llmClient.Tier = "fast"

	compConfig := engine.CompressionConfig{
		TargetCompressionPercent: *targetCompression,
		ReductionPercent:         *reductionPercent,
		MinWords:                 *minWords,
		Concurrency:              *concurrency,
		PromptTemplate:           promptTemplate,
		Timeout:                  120 * time.Second,
		Force:                    *force,
	}

	compressor, err := engine.NewMessageCompressor(llmClient, *cacheDB, compConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Failed to initialize compressor: %v\n", err)
		os.Exit(1)
	}
	defer compressor.Close()

	t0 := time.Now()
	err = compressor.PreprocessBeamCorpus(ctx, *corpusPath, *outputPath, targetConvIDs, func(convID string, isTarget bool, convStats engine.CompressionStats) {
		if !isTarget {
			fmt.Printf("   ├─ Conversation %-4s (passed through unchanged, not in active filter)\n", convID)
			return
		}
		fmt.Printf("   ├─ Conversation %-4s [%v] %d turns (%d compressed, %d cached, %d short skipped, %d errs) | Words: %d -> %d (%.1f%% of orig, %.1f%% reduction)\n",
			convID,
			convStats.Duration.Round(time.Millisecond),
			convStats.TotalTurns,
			convStats.ProcessedTurns,
			convStats.CachedTurns,
			convStats.SkippedTurns,
			convStats.ErrorTurns,
			convStats.OriginalWords,
			convStats.CompressedWords,
			convStats.CompressionPercentage(),
			convStats.ReductionPercentage(),
		)
	})

	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Pre-processing failed: %v\n", err)
		os.Exit(1)
	}

	totalStats := compressor.Stats()
	elapsed := time.Since(t0)

	fmt.Println("=======================================================")
	fmt.Println("✅ Pre-processing Completed Successfully!")
	fmt.Printf("   ├─ Total Duration: %v\n", elapsed.Round(time.Millisecond))
	fmt.Printf("   ├─ Total Turns Evaluated: %d\n", totalStats.TotalTurns)
	fmt.Printf("   ├─ Compressed with LLM: %d\n", totalStats.ProcessedTurns)
	fmt.Printf("   ├─ Retrieved from Cache: %d\n", totalStats.CachedTurns)
	fmt.Printf("   ├─ Short Turns Preserved: %d\n", totalStats.SkippedTurns)
	if totalStats.ErrorTurns > 0 {
		fmt.Printf("   ├─ Errors Fallback to Original: %d\n", totalStats.ErrorTurns)
	}
	fmt.Printf("   ├─ Total Words: %d -> %d\n", totalStats.OriginalWords, totalStats.CompressedWords)
	fmt.Printf("   └─ Overall Compression: %.1f%% of original (%.1f%% reduction)\n",
		totalStats.CompressionPercentage(), totalStats.ReductionPercentage())
	fmt.Println("=======================================================")
}
