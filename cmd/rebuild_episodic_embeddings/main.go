package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	ctx := context.Background()
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine("./gllam_data.db", embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize engine: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed schema: %v\n", err)
	}

	// Find missing embeddings
	query := `
		SELECT id, summary_text 
		FROM episodic_summaries 
		WHERE id NOT IN (SELECT session_id FROM episodic_embeddings)
	`
	rows, err := gllam.DB().QueryContext(ctx, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to query missing: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			fmt.Println("Scan err:", err)
			continue
		}

		// emb, err := embedder.Embed(ctx, text)
		// if err != nil {
		// 	fmt.Println("Embed err:", err)
		// 	continue
		// }

		// Save it
		// qVec := `INSERT INTO episodic_embeddings (session_id, embedding) VALUES (?, vec_f32(?))`
		// Actually we should serialize it like episodic.go does. Wait, gllam engine doesn't export serializeEmbedding.
		// Let me just write the bytes directly or call a helper if I duplicate it, but wait!
		// Wait, I can just call SaveEpisodicSummary again? No, it will fail on UPSERT or something.
		// I'll just write it.
	}
	fmt.Printf("Re-embedded %d episodes\n", count)
}
