package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	dbPath := "./gllam_data.db"
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine(dbPath, embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	_, err = gllam.DB().ExecContext(context.Background(), "DELETE FROM semantic_embeddings")
	if err != nil {
		fmt.Println("Error deleting semantic_embeddings:", err)
	} else {
		fmt.Println("Successfully deleted semantic_embeddings")
	}
}
