package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

type MockVersionedEmbedder struct {
	Version string
}

func (m *MockVersionedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	// Generate dummy 1024-dim vector
	vec := make([]float32, 1024)
	for i := range vec {
		vec[i] = 0.1
	}
	return vec, nil
}

func (m *MockVersionedEmbedder) ModelVersion() string {
	return m.Version
}

func TestVectorSpaceDriftPreventionAndReembedding(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_reembed.db")

	embedderV1 := &MockVersionedEmbedder{Version: "nomic-embed-text-v1.0"}
	gllam, err := NewGllamEngine(dbPath, embedderV1)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Insert sample semantic nodes under Model V1
	node1 := memory.SemanticNode{ID: "node-v1", Name: "PostgreSQL Database Server", Type: memory.NodeTypeEntity}
	node2 := memory.SemanticNode{ID: "node-v2", Name: "Redis In-Memory Cache", Type: memory.NodeTypeEntity}
	_ = gllam.UpsertNode(ctx, node1)
	_ = gllam.UpsertNode(ctx, node2)

	// Verify initial model version check
	drift, prev, current, err := gllam.CheckEmbeddingModelVersion(ctx)
	if err != nil {
		t.Fatalf("CheckEmbeddingModelVersion failed: %v", err)
	}
	if drift {
		t.Errorf("Expected drift = false on initial setup, got prev=%s, current=%s", prev, current)
	}

	// 2. Simulate model upgrade to Model V2 (e.g. nomic-embed-text-v2.0)
	embedderV2 := &MockVersionedEmbedder{Version: "nomic-embed-text-v2.0"}
	gllam.embedder = embedderV2

	// Check drift again
	drift, prev, current, err = gllam.CheckEmbeddingModelVersion(ctx)
	if err != nil {
		t.Fatalf("CheckEmbeddingModelVersion failed after upgrade: %v", err)
	}
	if !drift || prev != "nomic-embed-text-v1.0" || current != "nomic-embed-text-v2.0" {
		t.Errorf("Expected drift = true (prev=v1.0, current=v2.0), got drift=%v, prev=%s, current=%s", drift, prev, current)
	}

	// 3. Trigger background re-embedding task
	reembeddedCount, err := gllam.ReembedAllSemanticNodes(ctx)
	if err != nil {
		t.Fatalf("ReembedAllSemanticNodes failed: %v", err)
	}
	if reembeddedCount != 2 {
		t.Errorf("Expected 2 re-embedded nodes, got %d", reembeddedCount)
	}

	// 4. Verify model version is now updated and drift is resolved
	drift, prev, current, err = gllam.CheckEmbeddingModelVersion(ctx)
	if err != nil {
		t.Fatalf("Final CheckEmbeddingModelVersion failed: %v", err)
	}
	if drift || current != "nomic-embed-text-v2.0" {
		t.Errorf("Expected drift = false after re-embedding, got drift=%v, current=%s", drift, current)
	}
}
