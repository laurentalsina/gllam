package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestAutonomousOntologicalLayer(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_taxonomy.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Insert semantic nodes with materialized paths
	postgresNode := memory.SemanticNode{
		ID:            "node-postgres",
		Name:          "PostgreSQL 15",
		Type:          memory.NodeTypeEntity,
		ContextPrompt: "Relational database server",
		TrustWeight:   800,
		TaxonomyPath:  "/Engineering/Infrastructure/Databases/Relational/Postgres",
		IsCategory:    false,
	}
	if err := gllam.UpsertNode(ctx, postgresNode); err != nil {
		t.Fatalf("Failed to upsert postgres node: %v", err)
	}

	redisNode := memory.SemanticNode{
		ID:            "node-redis",
		Name:          "Redis Cache Cluster",
		Type:          memory.NodeTypeEntity,
		ContextPrompt: "In-memory key-value data store",
		TrustWeight:   800,
		TaxonomyPath:  "/Engineering/Infrastructure/Databases/NoSQL/Redis",
		IsCategory:    false,
	}
	if err := gllam.UpsertNode(ctx, redisNode); err != nil {
		t.Fatalf("Failed to upsert redis node: %v", err)
	}

	caddyNode := memory.SemanticNode{
		ID:            "node-caddy",
		Name:          "Caddy Reverse Proxy",
		Type:          memory.NodeTypeService,
		ContextPrompt: "HTTP/2 web server",
		TrustWeight:   700,
		TaxonomyPath:  "/Engineering/Infrastructure/Deployment/Caddy",
		IsCategory:    false,
	}
	if err := gllam.UpsertNode(ctx, caddyNode); err != nil {
		t.Fatalf("Failed to upsert caddy node: %v", err)
	}

	// 2. Materialized Path Instant Filtering
	dbNodes, err := gllam.GetNodesByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Databases")
	if err != nil {
		t.Fatalf("Failed to get nodes by taxonomy prefix: %v", err)
	}

	if len(dbNodes) != 2 {
		t.Fatalf("Expected 2 database nodes under /Engineering/Infrastructure/Databases, got %d", len(dbNodes))
	}

	hasPostgres, hasRedis := false, false
	for _, n := range dbNodes {
		if n.ID == "node-postgres" {
			hasPostgres = true
		}
		if n.ID == "node-redis" {
			hasRedis = true
		}
	}
	if !hasPostgres || !hasRedis {
		t.Errorf("Expected both Postgres and Redis under Databases prefix filter, got %+v", dbNodes)
	}

	// 3. Asynchronous Categorization Engine Processing
	uncategorizedNode := memory.SemanticNode{
		ID:            "node-docker",
		Name:          "Docker Container Runtime",
		Type:          memory.NodeTypeEntity,
		ContextPrompt: "Containerization tool",
		TrustWeight:   500,
		TaxonomyPath:  "/", // Orphaned
		IsCategory:    false,
	}
	_ = gllam.UpsertNode(ctx, uncategorizedNode)

	uncatNodes, err := gllam.GetUncategorizedNodes(ctx, 10)
	if err != nil || len(uncatNodes) == 0 {
		t.Fatalf("Expected at least 1 uncategorized node, got %d, err=%v", len(uncatNodes), err)
	}

	processed, err := gllam.ProcessUncategorizedBatch(ctx, 10)
	if err != nil || processed == 0 {
		t.Fatalf("Expected batch categorization of orphaned node, got processed=%d, err=%v", processed, err)
	}

	updatedDocker, err := gllam.GetNodesByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Deployment")
	if err != nil || len(updatedDocker) == 0 {
		t.Fatalf("Expected Docker under /Engineering/Infrastructure/Deployment after batch classification, got %+v", updatedDocker)
	}

	// 4. Self-Healing Taxonomy Consolidation Pass
	// Create legacy duplicate category /Engineering/DBs and node under it
	legacyNode := memory.SemanticNode{
		ID:            "node-sqlite-legacy",
		Name:          "SQLite File DB",
		Type:          memory.NodeTypeEntity,
		ContextPrompt: "Embedded database",
		TrustWeight:   700,
		TaxonomyPath:  "/Engineering/DBs/SQLite",
		IsCategory:    false,
	}
	_ = gllam.UpsertNode(ctx, legacyNode)

	// Consolidate /Engineering/DBs -> /Engineering/Infrastructure/Databases
	if err := gllam.ConsolidateTaxonomyBranch(ctx, "/Engineering/DBs", "/Engineering/Infrastructure/Databases"); err != nil {
		t.Fatalf("Failed to consolidate taxonomy branch: %v", err)
	}

	migratedSQLite, err := gllam.GetNodesByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Databases")
	if err != nil || len(migratedSQLite) < 3 {
		t.Fatalf("Expected SQLite node migrated under /Engineering/Infrastructure/Databases (total 3+), got %d nodes", len(migratedSQLite))
	}

	foundSQLite := false
	for _, n := range migratedSQLite {
		if n.ID == "node-sqlite-legacy" {
			foundSQLite = true
			if !strings.HasPrefix(n.TaxonomyPath, "/Engineering/Infrastructure/Databases") {
				t.Errorf("Expected rewritten taxonomy path for SQLite, got %s", n.TaxonomyPath)
			}
		}
	}
	if !foundSQLite {
		t.Errorf("Migrated SQLite node missing from consolidated target branch")
	}

	// 5. Procedural Domain Binding Integration
	dbProcedure := memory.ProceduralKnowledge{
		ID:                "proc-db-migrate",
		TaskType:          "PostgreSQL 15",
		Scope:             "external",
		TriggerContext:    "Schema Migration",
		Instructions:      "1. Run pg_dump\n2. Apply migration scripts\n3. Verify indexes",
		UserFeedbackRules: "Verify port 5432 accessibility",
		TimesApplied:      5,
		IsHighlyHelpful:   true,
		Version:           1,
		UpdatedAt:         time.Now(),
	}
	if err := gllam.UpsertProceduralKnowledge(ctx, dbProcedure); err != nil {
		t.Fatalf("Failed to upsert procedural knowledge: %v", err)
	}

	domainProcedures, err := gllam.GetProceduresByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Databases")
	if err != nil || len(domainProcedures) == 0 {
		t.Fatalf("Expected domain-filtered procedural recipe for Databases, got %d recipes, err=%v", len(domainProcedures), err)
	}
	if domainProcedures[0].ID != "proc-db-migrate" {
		t.Errorf("Expected proc-db-migrate, got %s", domainProcedures[0].ID)
	}
}

func TestTaxonomyCyclePrevention(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_cycle.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Create categories A (/Infrastructure/Storage) and B (/Storage/Databases)
	catA := memory.SemanticNode{ID: "cat-storage", Name: "Storage", Type: memory.NodeTypeCategory, IsCategory: true}
	catB := memory.SemanticNode{ID: "cat-databases", Name: "Databases", Type: memory.NodeTypeCategory, IsCategory: true}
	_ = gllam.UpsertNode(ctx, catA)
	_ = gllam.UpsertNode(ctx, catB)

	// Link A -> B (A is_a B)
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "cat-storage", TargetID: "cat-databases", Relationship: "is_a"})

	// 2. Check if creating reverse link B -> A would cause a cycle
	wouldCycle, err := gllam.WouldCreateTaxonomyCycle(ctx, "cat-databases", "cat-storage")
	if err != nil || !wouldCycle {
		t.Errorf("Expected WouldCreateTaxonomyCycle = true for reverse link B -> A, got %v, err=%v", wouldCycle, err)
	}

	// 3. Insert cyclic edge and verify DetectTaxonomyCycles finds it
	_ = gllam.AddEdge(ctx, memory.SemanticLink{SourceID: "cat-databases", TargetID: "cat-storage", Relationship: "subclass_of"})

	hasCycle, cyclicNodes, err := gllam.DetectTaxonomyCycles(ctx)
	if err != nil || !hasCycle || len(cyclicNodes) == 0 {
		t.Fatalf("Expected DetectTaxonomyCycles = true, got hasCycle=%v, cyclicNodes=%v, err=%v", hasCycle, cyclicNodes, err)
	}
}

func TestStartTaxonomyWorker(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_tax_worker.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Seed orphaned node
	orphanedNode := memory.SemanticNode{
		ID:            "node-postgres-test",
		Name:          "Postgres Server",
		Type:          memory.NodeTypeService,
		ContextPrompt: "Relational Database Engine",
		TaxonomyPath:  "/",
	}
	_ = gllam.UpsertNode(ctx, orphanedNode)

	// Launch background TaxonomyWorker
	subCtx, cancel := context.WithCancel(ctx)
	gllam.StartTaxonomyWorker(subCtx, 30*time.Millisecond)

	time.Sleep(100 * time.Millisecond)
	cancel()

	// Verify orphaned node was categorized by background TaxonomyWorker
	var updatedPath string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT taxonomy_path FROM semantic_nodes WHERE id = ?", "node-postgres-test").Scan(&updatedPath)
	if err != nil || updatedPath == "/" || updatedPath == "" {
		t.Fatalf("Expected node to be categorized by background TaxonomyWorker, got path '%s', err=%v", updatedPath, err)
	}
}


