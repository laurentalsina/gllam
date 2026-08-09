package engine

import (
	"context"
	"path/filepath"
	"strings"
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

func TestGetActiveConstraintsForSourceAndRevocation(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_revoke.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-bob", Name: "Bob", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-json", Name: "Output JSON", Type: memory.NodeTypeRule})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-yaml", Name: "Output YAML", Type: memory.NodeTypeRule})

	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "user-bob",
		TargetID:       "rule-json",
		Relationship:   "is_preference",
		ValidFrom:      "1000",
		OriginSourceID: "user-bob",
		RuleContext:    "user_preference",
	})

	// 1. Check active constraints for user-bob
	bobConstraints, err := gllam.GetActiveConstraintsForSource(ctx, "user-bob", "user_preference")
	if err != nil {
		t.Fatalf("GetActiveConstraintsForSource failed: %v", err)
	}
	if len(bobConstraints) != 1 || bobConstraints[0].TargetID != "rule-json" {
		t.Errorf("Expected rule-json constraint for user-bob, got %v", bobConstraints)
	}

	// 2. Revoke rule-json and replace with rule-yaml
	if err := gllam.RevokeOrSupersedeRule(ctx, "rule-json", "rule-yaml"); err != nil {
		t.Fatalf("RevokeOrSupersedeRule failed: %v", err)
	}

	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "user-bob",
		TargetID:       "rule-yaml",
		Relationship:   "is_preference",
		ValidFrom:      "2000",
		OriginSourceID: "user-bob",
		RuleContext:    "user_preference",
	})

	// 3. Verify rule-json is expired and rule-yaml is active
	newConstraints, err := gllam.GetActiveConstraintsForSource(ctx, "user-bob", "user_preference")
	if err != nil {
		t.Fatalf("GetActiveConstraintsForSource after revoke failed: %v", err)
	}
	hasJson := false
	hasYaml := false
	for _, c := range newConstraints {
		if c.TargetID == "rule-json" {
			hasJson = true
		}
		if c.TargetID == "rule-yaml" {
			hasYaml = true
		}
	}
	if hasJson {
		t.Errorf("Expected rule-json to be revoked/expired")
	}
	if !hasYaml {
		t.Errorf("Expected rule-yaml to be active")
	}
}

func TestPDDLRuleVerification(t *testing.T) {
	nodes := []memory.SemanticNode{
		{ID: "rule-format-table", Name: "Format Table", Type: memory.NodeTypeRule},
	}
	links := []memory.SemanticLink{
		{SourceID: "user-alice", TargetID: "rule-format-table", Relationship: "is_preference", RuleContext: "user_preference"},
	}

	goal := ExtractPDDLGoal("Did we follow the rule for table format?", nodes, links)
	expectedGoal := "(and (rule_satisfied rule_format_table))"
	if goal != expectedGoal {
		t.Errorf("Expected goal %q, got %q", expectedGoal, goal)
	}

	domain, problem := CompileGraphToPDDL(nodes, links, goal, nil)
	if !strings.Contains(domain, "must_follow_rule") || !strings.Contains(domain, "verify_instruction_rule") {
		t.Errorf("Domain missing rule predicates/action")
	}
	if !strings.Contains(problem, "rule_format_table") {
		t.Errorf("Problem missing rule_format_table object")
	}
}

func TestNegativeConstraintRedaction(t *testing.T) {
	links := []memory.SemanticLink{
		{
			SourceID:       "sys-security",
			TargetID:       "constraint-no-internal-ip",
			Relationship:   "has_constraint",
			ConstraintType: "negative",
		},
		{
			SourceID:       "sys-security",
			TargetID:       "constraint-no-token",
			Relationship:   "has_constraint",
			ConstraintType: "negative",
		},
	}

	nodes := []memory.SemanticNode{
		{ID: "constraint-no-internal-ip", Name: "Internal Server 192.168.1.100", Type: memory.NodeTypeConstraint},
		{ID: "constraint-no-token", Name: "Master Token", Type: memory.NodeTypeConstraint},
	}

	inputPrompt := "Deploying to 192.168.1.100 with bearer token sk-12345678901234567890."
	redacted := RedactProhibitedContent(inputPrompt, links, nodes)

	if strings.Contains(redacted, "192.168.1.100") {
		t.Errorf("Failed to redact IP address: %s", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED_INTERNAL_IP]") {
		t.Errorf("Expected [REDACTED_INTERNAL_IP] in output: %s", redacted)
	}

	if strings.Contains(redacted, "sk-12345678901234567890") {
		t.Errorf("Failed to redact secret token: %s", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED_SECRET]") {
		t.Errorf("Expected [REDACTED_SECRET] in output: %s", redacted)
	}
}

func TestTurnCountBoundConstraints(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_turn_bound.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "user-alice", Name: "Alice", Type: memory.NodeTypeHuman})
	_ = gllam.UpsertNode(ctx, memory.SemanticNode{ID: "rule-yes-no", Name: "Answer YES/NO only", Type: memory.NodeTypeRule})

	// Add a rule active for 2 turns
	_ = gllam.AddEdge(ctx, memory.SemanticLink{
		SourceID:       "user-alice",
		TargetID:       "rule-yes-no",
		Relationship:   "is_preference",
		ValidFrom:      "1000",
		OriginSourceID: "user-alice",
		RuleContext:    "session",
		DurationTurns:  2,
		RemainingTurns: 2,
	})

	// 1. Initial state: rule is active with 2 remaining turns
	rules1, err := gllam.GetActiveConstraintsForSource(ctx, "user-alice", "session")
	if err != nil || len(rules1) != 1 {
		t.Fatalf("Expected 1 active rule initially, got %d", len(rules1))
	}
	if rules1[0].RemainingTurns != 2 {
		t.Errorf("Expected 2 remaining turns, got %d", rules1[0].RemainingTurns)
	}

	// 2. Turn 1 (decrement)
	if err := gllam.DecrementActiveTurnConstraints(ctx); err != nil {
		t.Fatalf("Turn 1 decrement failed: %v", err)
	}
	rules2, err := gllam.GetActiveConstraintsForSource(ctx, "user-alice", "session")
	if err != nil || len(rules2) != 1 {
		t.Fatalf("Expected 1 active rule after Turn 1, got %d", len(rules2))
	}
	if rules2[0].RemainingTurns != 1 {
		t.Errorf("Expected 1 remaining turn after Turn 1, got %d", rules2[0].RemainingTurns)
	}

	// 3. Turn 2 (decrement & auto-expire)
	if err := gllam.DecrementActiveTurnConstraints(ctx); err != nil {
		t.Fatalf("Turn 2 decrement failed: %v", err)
	}
	rules3, err := gllam.GetActiveConstraintsForSource(ctx, "user-alice", "session")
	if err != nil {
		t.Fatalf("Query after Turn 2 failed: %v", err)
	}
	if len(rules3) != 0 {
		t.Errorf("Expected rule to expire after Turn 2, but got %d active rules: %v", len(rules3), rules3)
	}
}

func TestRuleRationaleConfrontation(t *testing.T) {
	links := []memory.SemanticLink{
		{
			SourceID:       "sys-security",
			TargetID:       "constraint-no-token",
			Relationship:   "has_constraint",
			ConstraintType: "negative",
			RuleContext:    "global",
			RuleRationale:  "Security & Access Governance",
		},
		{
			SourceID:       "user-bob",
			TargetID:       "rule-verbose-logging",
			Relationship:   "is_preference",
			ConstraintType: "positive",
			RuleContext:    "user_preference",
			RuleRationale:  "API Endpoint Debugging",
		},
	}

	diag := ConfrontRuleRationales(links)
	if diag == "" {
		t.Fatalf("Expected confrontation diagnostic, got empty string")
	}

	if !strings.Contains(diag, "constraint-no-token") || !strings.Contains(diag, "rule-verbose-logging") {
		t.Errorf("Diagnostic missing rule IDs: %s", diag)
	}
	if !strings.Contains(diag, "Security & Access Governance") || !strings.Contains(diag, "API Endpoint Debugging") {
		t.Errorf("Diagnostic missing rationales: %s", diag)
	}
}




