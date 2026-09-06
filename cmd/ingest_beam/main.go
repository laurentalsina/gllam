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

type ChatMessage struct {
	ID      int    `json:"id"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Conversation struct {
	ConversationID string          `json:"conversation_id"`
	Chat           [][]ChatMessage `json:"chat"`
}

func main() {
	dbPath := flag.String("db", "./gllam_data.db", "Path to SQLite database")
	jsonlPath := flag.String("jsonl", "/home/laurent/Projects/agentic_benchmarks/beam_100k_conversations.jsonl", "Path to the exported BEAM jsonl file")
	defaultEmbeddingServer := os.Getenv("EMBEDDINGS_SERVER")
	embeddingsServer := flag.String("embeddings-server", defaultEmbeddingServer, "Embeddings server URL")
	forceIngest := flag.Bool("force", false, "Force re-ingestion and re-embedding of already ingested sessions")
	flag.Parse()

	if *embeddingsServer == "" {
		fmt.Fprintf(os.Stderr, "❌ Error: Embeddings server not specified. Pass --embeddings-server or export EMBEDDINGS_SERVER\n")
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Failed to initialize schema: %v\n", err)
		os.Exit(1)
	}

	existingSessions := make(map[string]bool)
	rows, err := gllam.DB().QueryContext(ctx, "SELECT id FROM episodic_summaries")
	if err == nil {
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				existingSessions[id] = true
			}
		}
		rows.Close()
	}
	if len(existingSessions) > 0 && !*forceIngest {
		fmt.Printf("ℹ️ Found %d existing sessions in database. Existing sessions will be skipped (use --force to re-embed).\n", len(existingSessions))
	}

	file, err := os.Open(*jsonlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open jsonl file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	fmt.Println("Starting BEAM ingestion using natural session boundaries...")
	scanner := bufio.NewScanner(file)
	
	const maxCapacity = 50 * 1024 * 1024 // 50MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	convCount := 0
	sessionCount := 0
	skippedCount := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		var conv Conversation
		if err := json.Unmarshal(line, &conv); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse conversation JSON: %v\n", err)
			continue
		}

		convIngested := 0
		convSkipped := 0

		for sessionIdx, session := range conv.Chat {
			sessionID := fmt.Sprintf("beam-100k-%s-session%d", conv.ConversationID, sessionIdx)
			if !*forceIngest && existingSessions[sessionID] {
				skippedCount++
				convSkipped++
				continue
			}

			var currentSession strings.Builder
			for _, msg := range session {
				turnText := fmt.Sprintf("%s (id %d): %s\n\n", msg.Role, msg.ID, msg.Content)
				currentSession.WriteString(turnText)
			}

			summary := memory.EpisodicSummary{
				ID:          sessionID,
				SessionID:   sessionID,
				SummaryText: currentSession.String(),
				CreatedAt:   time.Now().Add(time.Duration(sessionIdx) * time.Second), // sequential timestamps
			}

			if err := gllam.SaveEpisodicSummary(ctx, summary); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to save session %s: %v\n", sessionID, err)
			} else {
				sessionCount++
				convIngested++
			}
		}

		convCount++
		if convIngested > 0 {
			fmt.Printf("Ingested conversation %s (new sessions: %d, cached: %d)...\n", conv.ConversationID, convIngested, convSkipped)
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Scanner error: %v\n", err)
	}

	fmt.Printf("Ingestion complete. Total conversations: %d, Newly ingested: %d, Skipped (already cached): %d\n", convCount, sessionCount, skippedCount)
}
