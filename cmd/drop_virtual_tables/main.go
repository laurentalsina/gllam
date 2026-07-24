package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	embedder := engine.NewLlamaEmbedder("http://127.0.0.1:8800")
	gllam, err := engine.NewGllamEngine("./gllam_data.db", embedder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	gllam.DB().ExecContext(context.Background(), "DROP TABLE semantic_embeddings")
	gllam.DB().ExecContext(context.Background(), "DROP TABLE procedural_embeddings")
	gllam.DB().ExecContext(context.Background(), "DROP TABLE episodic_embeddings")
	
	if err := gllam.InitSchema(); err != nil {
		fmt.Println("Init schema failed:", err)
	} else {
		fmt.Println("Successfully dropped and recreated virtual tables")
	}
}
