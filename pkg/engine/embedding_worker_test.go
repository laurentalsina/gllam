package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

type MockAsyncEmbedder struct{}

func (m *MockAsyncEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vec := make([]float32, 1024)
	for i := range vec {
		vec[i] = 0.42
	}
	return vec, nil
}

func (m *MockAsyncEmbedder) ModelVersion() string {
	return "mock-async-v1.0"
}

func TestAsynchronousEmbeddingWorkerPool(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_async_embedding.db")

	embedder := &MockAsyncEmbedder{}
	gllam, err := NewGllamEngine(dbPath, embedder)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Bulk insert 20 semantic nodes synchronously into SQLite relational tables
	for i := 1; i <= 20; i++ {
		node := memory.SemanticNode{
			ID:            fmt.Sprintf("node-async-%d", i),
			Name:          fmt.Sprintf("Enterprise Entity %d", i),
			Type:          memory.NodeTypeEntity,
			ContextPrompt: fmt.Sprintf("High-throughput entity description %d", i),
		}
		if err := gllam.UpsertNode(ctx, node); err != nil {
			t.Fatalf("Failed to upsert node %d: %v", i, err)
		}
	}

	// 2. Verify nodes exist in relational DB but are unindexed in semantic_embeddings
	var unindexedCount int
	err = gllam.DBRO().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM semantic_nodes n
		LEFT JOIN semantic_embeddings v ON n.id = v.node_id
		WHERE v.node_id IS NULL`).Scan(&unindexedCount)
	if err != nil || unindexedCount != 20 {
		t.Fatalf("Expected 20 unindexed vector nodes, got %d, err=%v", unindexedCount, err)
	}

	// 3. Launch background embedding worker pool
	subCtx, cancel := context.WithCancel(ctx)
	gllam.StartEmbeddingWorkerPool(subCtx, 2, 30*time.Millisecond)

	// 4. Wait for background workers to process the queue
	time.Sleep(200 * time.Millisecond)
	cancel()

	// 5. Assert all 20 nodes are now indexed into semantic_embeddings
	err = gllam.DBRO().QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM semantic_nodes n
		LEFT JOIN semantic_embeddings v ON n.id = v.node_id
		WHERE v.node_id IS NULL`).Scan(&unindexedCount)
	if err != nil || unindexedCount != 0 {
		t.Fatalf("Expected 0 unindexed vector nodes after worker pool execution, got %d, err=%v", unindexedCount, err)
	}
}
