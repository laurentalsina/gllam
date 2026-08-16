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
	"github.com/laurentalsina/gllam/pkg/memory"
)

type Turn struct {
	SpeakerID string `json:"speaker_id"`
	Text      string `json:"text"`
}

type Session struct {
	SessionID string `json:"session_id"`
	Turns     []Turn `json:"turns"`
}

func getEnv(key, fallback string) string {
        if value, exists := os.LookupEnv(key); exists {
                return value
        }
        return fallback
}

func main() {
        // Command Line Flag (has prio over)  Environment Variable (has prio over)  Hardcoded Default
        dbPath := flag.String("dbpath", getEnv("DATABASE_PATH", "./bench/ gllam_data.db"), "Path to SQLite database (env: DATABASE_PATH_PATH)")
        embeddingsServer := flag.String("embeddings-server", getEnv("EMBEDDINGS_SERVER", "http://127.0.0.1:8800"), "Embeddings server endpoint (env: EMBEDDINGS_SERVER)")

	corpusPath := flag.String("corpus", "./corpus_sessions.jsonl", "Path to corpus_sessions.jsonl")
	flag.Parse()

	ctx := context.Background()

	embedder := engine.NewLlamaEmbedder(*embeddingsServer)
	gllam, err := engine.NewGllamEngine(*dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
		os.Exit(1)
	}

	file, err := os.Open(*corpusPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open corpus: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	fmt.Println("Starting ingestion...")
	scanner := bufio.NewScanner(file)
	
	// Sessions contain lots of text, increase the scanner buffer size
	const maxCapacity = 10 * 1024 * 1024
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	count := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		var s Session
		if err := json.Unmarshal(line, &s); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse session JSON: %v\n", err)
			continue
		}

		var transcriptBuilder strings.Builder
		for _, turn := range s.Turns {
			transcriptBuilder.WriteString(fmt.Sprintf("%s: %s\n", turn.SpeakerID, turn.Text))
		}

		summary := memory.EpisodicSummary{
			ID:          s.SessionID,
			SessionID:   s.SessionID,
			SummaryText: transcriptBuilder.String(),
			CreatedAt:   time.Now(),
		}

		if err := gllam.SaveEpisodicSummary(ctx, summary); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to save session %s: %v\n", s.SessionID, err)
		} else {
			count++
			if count%100 == 0 {
				fmt.Printf("Ingested %d sessions...\n", count)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Scanner error: %v\n", err)
	}

	fmt.Printf("Ingestion complete. Total sessions ingested: %d\n", count)
}
