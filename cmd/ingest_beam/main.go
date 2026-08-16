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
	embeddingsServer := flag.String("embeddings-server", "http://127.0.0.1:8800", "Embeddings server URL")
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

	for scanner.Scan() {
		line := scanner.Bytes()
		var conv Conversation
		if err := json.Unmarshal(line, &conv); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to parse conversation JSON: %v\n", err)
			continue
		}

		for sessionIdx, session := range conv.Chat {
			var currentSession strings.Builder

			for _, msg := range session {
				turnText := fmt.Sprintf("%s (id %d): %s\n\n", msg.Role, msg.ID, msg.Content)
				currentSession.WriteString(turnText)
			}

			// Save this session
			sessionID := fmt.Sprintf("beam-100k-%s-session%d", conv.ConversationID, sessionIdx)
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
			}
		}

		convCount++
		fmt.Printf("Ingested conversation %s containing %d sessions...\n", conv.ConversationID, len(conv.Chat))
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Scanner error: %v\n", err)
	}

	fmt.Printf("Ingestion complete. Total conversations: %d, Total sessions ingested: %d\n", convCount, sessionCount)
}
