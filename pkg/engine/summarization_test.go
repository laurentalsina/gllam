package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestSalienceAnchoredSummaryAndProceduralExtraction(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_summarization.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-format", Name: "Table Formatting Rule", Type: memory.NodeTypeRule})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-alice", Name: "Alice", Type: memory.NodeTypeHuman})

	nodes := []memory.SemanticNode{
		{ID: "caddy-service", Name: "Caddy Web Server", Type: memory.NodeTypeService},
		{ID: "port-8080", Name: "Port 8080", Type: memory.NodeTypeEntity},
		{ID: "rule-format", Name: "Table Formatting Rule", Type: memory.NodeTypeRule},
	}
	links := []memory.SemanticLink{
		{SourceID: "caddy-service", TargetID: "port-8080", Relationship: "binds_to", Caveats: "Must use TLS certificate"},
		{SourceID: "user-alice", TargetID: "rule-format", Relationship: "is_preference", RuleContext: "user_preference", ConstraintType: "positive", Caveats: "Always output response tables in Markdown"},
	}
	episodes := []memory.EpisodicSummary{
		{ID: "ep-1", SummaryText: "Configured Caddy web server on port 8080 with TLS cert."},
	}

	// 1. FormatSalienceAnchoredSummary with query focal prompt & SystemPrompts
	summary := FormatSalienceAnchoredSummary(nodes, links, episodes, "What port is Caddy web server running on?", gllam.SystemPrompts)

	// Verify Corpus Historical & Domain Context header is present
	if !strings.Contains(summary, "CORPUS HISTORICAL & DOMAIN CONTEXT") {
		t.Errorf("Summary missing Agentic System Prompts historical context: %s", summary)
	}



	// Verify ground-truth entity IDs and active links are present
	if !strings.Contains(summary, "caddy-service") || !strings.Contains(summary, "port-8080") {
		t.Errorf("Summary missing ground-truth entity IDs: %s", summary)
	}

	// Verify Active since state duration is present
	if !strings.Contains(summary, "Active since:") {
		t.Errorf("Summary missing active state duration ('Active since:'): %s", summary)
	}

	// Verify global preference directive is present
	if !strings.Contains(summary, "Rule (user_preference/positive)") {
		t.Errorf("Summary missing global user preference directive: %s", summary)
	}


	// Verify obsolete link port-8079 is filtered out
	if strings.Contains(summary, "port-8079") {
		t.Errorf("Summary should filter out obsolete state link port-8079: %s", summary)
	}

	// 2. ExtractProceduralWorkflow
	err = gllam.ExtractProceduralWorkflow(ctx, "Caddy Setup Workflow", "caddy_setup", "1. Install 2. Configure port 8080", "Strict TLS")
	if err != nil {
		t.Fatalf("ExtractProceduralWorkflow failed: %v", err)
	}

	var taskType string
	err = gllam.dbRO.QueryRowContext(ctx, "SELECT task_type FROM procedural_knowledge WHERE id = 'proc-caddy-setup-workflow'").Scan(&taskType)
	if err != nil || taskType != "caddy_setup_workflow" {
		t.Errorf("Failed to retrieve inserted procedural workflow: %v, taskType=%s", err, taskType)
	}
}

