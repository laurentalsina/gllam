package engine

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)


func TestEdgeCaveatCompactionAndHubWindowing(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_caveats.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Create enterprise hub node: "Auth Service"
	authService := memory.SemanticNode{
		ID:          "node-auth-service",
		Name:        "Auth Service",
		Type:        memory.NodeTypeService,
		TrustWeight: 900,
	}
	_ = gllam.UpsertNode(ctx, authService)

	// 2. Add 12 links with caveats connected to Auth Service
	for i := 1; i <= 12; i++ {
		targetID := fmt.Sprintf("node-dep-%d", i)
		targetNode := memory.SemanticNode{ID: targetID, Name: fmt.Sprintf("Dependency %d", i), Type: memory.NodeTypeService}
		_ = gllam.UpsertNode(ctx, targetNode)

		caveatText := fmt.Sprintf("Jira ticket AUTH-%d: Service maintenance window required for schema migration v%d", 100+i, i)
		link := memory.SemanticLink{
			SourceID:     "node-auth-service",
			TargetID:     targetID,
			Relationship: "depends_on",
			Caveats:      caveatText,
		}
		_ = gllam.AddEdge(ctx, link)
	}

	// 3. Run CompactNodeEdgeCaveats with maxInline = 5
	summaryText, retainedCount, prunedCount, err := gllam.CompactNodeEdgeCaveats(ctx, "node-auth-service", 5)
	if err != nil {
		t.Fatalf("CompactNodeEdgeCaveats failed: %v", err)
	}

	if retainedCount != 5 {
		t.Errorf("Expected 5 retained inline caveats, got %d", retainedCount)
	}
	if prunedCount != 7 {
		t.Errorf("Expected 7 pruned/compacted caveats, got %d", prunedCount)
	}
	if summaryText == "" {
		t.Errorf("Expected non-empty summaryText for compacted edge caveats")
	}

	// 4. Verify node now contains caveat_summary
	var fetched memory.SemanticNode
	var caveatSum sql.NullString
	err = gllam.DBRO().QueryRowContext(ctx, "SELECT id, caveat_summary FROM semantic_nodes WHERE id = ?", "node-auth-service").Scan(&fetched.ID, &caveatSum)
	if err != nil || !caveatSum.Valid || caveatSum.String == "" {
		t.Fatalf("Expected valid caveat_summary in DB, got err=%v, valid=%v", err, caveatSum.Valid)
	}

	// 5. Test BatchCompactHubCaveats
	compactedHubs, err := gllam.BatchCompactHubCaveats(ctx, 4, 3)
	if err != nil {
		t.Fatalf("BatchCompactHubCaveats failed: %v", err)
	}
	if compactedHubs < 1 {
		t.Errorf("Expected at least 1 hub node compacted during batch run, got %d", compactedHubs)
	}
}
