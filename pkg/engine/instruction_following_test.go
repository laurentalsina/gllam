package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestInstructionFollowingDataModelAndSourceNodes(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_instruction.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Create Source Nodes (Human, Agent, System)
	humanUser := memory.SemanticNode{ID: "user-alice", Name: "Alice (Lead Dev)", Type: memory.NodeTypeHuman, ContextPrompt: "Frontend Team Lead"}
	agentWorker := memory.SemanticNode{ID: "agent-planner", Name: "Planner Agent", Type: memory.NodeTypeAgent, ContextPrompt: "Automated PDDL Planner"}
	systemMcp := memory.SemanticNode{ID: "sys-github", Name: "GitHub MCP Server", Type: memory.NodeTypeSystem, ContextPrompt: "GitHub Integration"}

	for _, n := range []memory.SemanticNode{humanUser, agentWorker, systemMcp} {
		if err := gllam.UpsertNode(ctx, n); err != nil {
			t.Fatalf("Failed to upsert source node %s: %v", n.ID, err)
		}
	}

	// 2. Create Rule & Constraint Nodes
	ruleMarkdown := memory.SemanticNode{ID: "rule-format-table", Name: "Format as Markdown Table", Type: memory.NodeTypeRule}
	constraintNoIp := memory.SemanticNode{ID: "constraint-no-internal-ip", Name: "Do Not Expose Internal IPs", Type: memory.NodeTypeConstraint}

	for _, r := range []memory.SemanticNode{ruleMarkdown, constraintNoIp} {
		if err := gllam.UpsertNode(ctx, r); err != nil {
			t.Fatalf("Failed to upsert rule node %s: %v", r.ID, err)
		}
	}

	// 3. Create Links tied to OriginSourceID, RuleContext, and ConstraintType
	link1 := memory.SemanticLink{
		SourceID:       "user-alice",
		TargetID:       "rule-format-table",
		Relationship:   "is_preference",
		ValidFrom:      "1000",
		OriginSourceID: "user-alice",
		RuleContext:    "user_preference",
		ConstraintType: "positive",
	}
	if err := gllam.AddEdge(ctx, link1); err != nil {
		t.Fatalf("Failed to add link1: %v", err)
	}

	link2 := memory.SemanticLink{
		SourceID:       "sys-github",
		TargetID:       "constraint-no-internal-ip",
		Relationship:   "has_constraint",
		ValidFrom:      "1000",
		OriginSourceID: "sys-github",
		RuleContext:    "global",
		ConstraintType: "negative",
	}
	if err := gllam.AddEdge(ctx, link2); err != nil {
		t.Fatalf("Failed to add link2: %v", err)
	}

	// 4. Retrieve links and verify fields
	links, err := gllam.GetActiveLinksAtTime(ctx, 1500)
	if err != nil {
		t.Fatalf("GetActiveLinksAtTime failed: %v", err)
	}

	if len(links) < 2 {
		t.Fatalf("Expected at least 2 active links, got %d", len(links))
	}

	foundLink1 := false
	foundLink2 := false
	for _, l := range links {
		if l.TargetID == "rule-format-table" {
			foundLink1 = true
			if l.OriginSourceID != "user-alice" {
				t.Errorf("Expected OriginSourceID 'user-alice', got %q", l.OriginSourceID)
			}
			if l.RuleContext != "user_preference" {
				t.Errorf("Expected RuleContext 'user_preference', got %q", l.RuleContext)
			}
			if l.ConstraintType != "positive" {
				t.Errorf("Expected ConstraintType 'positive', got %q", l.ConstraintType)
			}
		}
		if l.TargetID == "constraint-no-internal-ip" {
			foundLink2 = true
			if l.OriginSourceID != "sys-github" {
				t.Errorf("Expected OriginSourceID 'sys-github', got %q", l.OriginSourceID)
			}
			if l.RuleContext != "global" {
				t.Errorf("Expected RuleContext 'global', got %q", l.RuleContext)
			}
			if l.ConstraintType != "negative" {
				t.Errorf("Expected ConstraintType 'negative', got %q", l.ConstraintType)
			}
		}
	}

	if !foundLink1 || !foundLink2 {
		t.Errorf("Did not find both expected links in active links query")
	}
}
