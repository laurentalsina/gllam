package main

import (
	"context"
	"fmt"
	"os"

	"github.com/laurentalsina/gllam/pkg/engine"
)

func main() {
	gllam, err := engine.NewGllamEngine("./gllam_data.db", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed: %v\n", err)
		os.Exit(1)
	}
	defer gllam.Close()

	rows, err := gllam.DB().QueryContext(context.Background(), "SELECT node_id FROM semantic_embeddings")
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			fmt.Println("Scan error:", err)
			return
		}
		fmt.Printf("node_id=%s\n", nodeID)
	}
}
