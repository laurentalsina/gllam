package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


func TestWALCheckpointingAndReadOnlyHandleEnforcement(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_wal.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Verify read-only handle (dbRO) rejects write mutations under PRAGMA query_only = ON
	_, err = gllam.DBRO().ExecContext(ctx, "CREATE TABLE write_test (id INT)")
	if err == nil || (!strings.Contains(err.Error(), "readonly") && !strings.Contains(err.Error(), "query_only")) {
		t.Errorf("Expected read-only error on dbRO mutation attempt, got %v", err)
	}

	// 2. Insert records using write handle (db) to generate WAL frames
	for i := 0; i < 50; i++ {
		nodeID := fmt.Sprintf("node-wal-%d", i)
		nodeName := fmt.Sprintf("test-node-%d-%d", i, time.Now().UnixNano())
		err = gllam.UpsertNode(ctx, memory.SemanticNode{
			ID:   nodeID,
			Name: nodeName,
			Type: "entity",
		})
		if err != nil {
			t.Fatalf("Failed to insert node %s: %v", nodeID, err)
		}
	}


	// 3. Test explicit PRAGMA wal_checkpoint(RESTART)
	logPages, checkpointedPages, err := gllam.CheckpointWAL(ctx, "RESTART")
	if err != nil {
		t.Fatalf("Failed to execute RESTART WAL checkpoint: %v", err)
	}
	if logPages < 0 || checkpointedPages < 0 {
		t.Errorf("Unexpected checkpoint results: logPages=%d, checkpointedPages=%d", logPages, checkpointedPages)
	}

	// 4. Test explicit PRAGMA wal_checkpoint(TRUNCATE)
	_, _, err = gllam.CheckpointWAL(ctx, "TRUNCATE")
	if err != nil {
		t.Fatalf("Failed to execute TRUNCATE WAL checkpoint: %v", err)
	}

	// 5. Test Background WAL Checkpoint Manager
	subCtx, cancel := context.WithCancel(ctx)
	gllam.StartWALCheckpointManager(subCtx, 50*time.Millisecond)
	time.Sleep(150 * time.Millisecond)
	cancel()
}
