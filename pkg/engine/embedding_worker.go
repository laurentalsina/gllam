package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// ProcessUnembeddedNodeBatch queries a batch of semantic_nodes that have not yet been indexed
// in semantic_embeddings and asynchronously generates and stores their vector embeddings.
func (e *GllamEngine) ProcessUnembeddedNodeBatch(ctx context.Context, batchSize int) (int, error) {
	if e.embedder == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = 50
	}

	query := `
		SELECT n.id, n.name, n.context_prompt
		FROM semantic_nodes n
		LEFT JOIN semantic_embeddings v ON n.id = v.node_id
		WHERE v.node_id IS NULL
		LIMIT ?`

	rows, err := e.dbRO.QueryContext(ctx, query, batchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to query unembedded nodes: %w", err)
	}
	defer rows.Close()

	type nodeTask struct {
		id   string
		text string
	}
	var tasks []nodeTask

	for rows.Next() {
		var id, name string
		var prompt sql.NullString
		if err := rows.Scan(&id, &name, &prompt); err == nil {
			text := name
			if prompt.Valid && prompt.String != "" {
				text = fmt.Sprintf("%s: %s", name, prompt.String)
			}
			tasks = append(tasks, nodeTask{id: id, text: text})
		}
	}

	if len(tasks) == 0 {
		return 0, nil
	}

	indexedCount := 0
	for _, task := range tasks {
		vec, err := e.embedder.Embed(ctx, task.text)
		if err != nil {
			log.Printf("Embedding worker failed to generate embedding for node %s (%s): %v", task.id, task.text, err)
			continue
		}

		if err := e.IndexNodeVector(ctx, task.id, vec); err == nil {
			indexedCount++
		} else {
			log.Printf("Embedding worker failed to store vector for node %s: %v", task.id, err)
		}
	}

	return indexedCount, nil
}

// StartEmbeddingWorkerPool launches a background worker pool that periodically processes
// unindexed vector embedding batches, decoupling relational graph insertion from vector virtual table mutations.
func (e *GllamEngine) StartEmbeddingWorkerPool(ctx context.Context, numWorkers int, interval time.Duration) {
	if numWorkers <= 0 {
		numWorkers = 2
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}

	for i := 0; i < numWorkers; i++ {
		go func(workerID int) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-e.stopEmbeddingWorkers:
					return
				case <-ticker.C:
					_, _ = e.ProcessUnembeddedNodeBatch(ctx, 50)
				}
			}
		}(i + 1)
	}
}
