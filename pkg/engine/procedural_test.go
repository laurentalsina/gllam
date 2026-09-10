package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/laurentalsina/gllam/pkg/memory"
)

func TestEvaluateCondition(t *testing.T) {
	ctxMap := map[string]interface{}{
		"status":      "success",
		"retry_count": 2,
		"score":       0.85,
		"is_active":   true,
		"has_errors":  false,
		"empty_str":   "",
		"user": map[string]interface{}{
			"role":  "admin",
			"level": 5,
		},
	}

	tests := []struct {
		name     string
		expr     string
		expected bool
	}{
		{"empty expr is true", "", true},
		{"whitespace is true", "   ", true},
		{"string equality match single quotes", "status == 'success'", true},
		{"string equality match double quotes", `status == "success"`, true},
		{"string equality mismatch", "status == 'failed'", false},
		{"string inequality match", "status != 'failed'", true},
		{"number equality match", "retry_count == 2", true},
		{"number less than match", "retry_count < 3", true},
		{"number less than fail", "retry_count < 2", false},
		{"number less equal match", "retry_count <= 2", true},
		{"number greater than match", "score > 0.8", true},
		{"number greater than fail", "score > 0.9", false},
		{"boolean equality match", "is_active == true", true},
		{"boolean equality mismatch", "is_active == false", false},
		{"boolean inequality match", "has_errors != true", true},
		{"nested dot path string match", "user.role == 'admin'", true},
		{"nested dot path number match", "user.level >= 5", true},
		{"jsonpath prefix string match", "$.user.role == 'admin'", true},
		{"missing field equality null", "missing == null", true},
		{"missing field inequality null", "missing != null", false},
		{"missing field equality value", "missing == 'foo'", false},
		{"unary boolean truthy", "is_active", true},
		{"unary boolean falsy", "has_errors", false},
		{"unary boolean negated", "!has_errors", true},
		{"unary string empty is falsy", "empty_str", false},
		{"unary string empty negated is truthy", "!empty_str", true},
		{"unary missing field is falsy", "not_exist", false},
		{"unary missing field negated is truthy", "!not_exist", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := EvaluateCondition(tc.expr, ctxMap)
			if err != nil {
				t.Fatalf("Unexpected error evaluating '%s': %v", tc.expr, err)
			}
			if res != tc.expected {
				t.Errorf("EvaluateCondition('%s') = %v, expected %v", tc.expr, res, tc.expected)
			}
		})
	}
}

func TestApplyParameterMapping(t *testing.T) {
	ctxMap := map[string]interface{}{
		"auth": map[string]interface{}{
			"token": "tok_xyz_123",
		},
		"user": map[string]interface{}{
			"id": 99,
		},
		"flag": true,
	}

	mappingJSON := `{"access_token": "$.auth.token", "user_id": "user.id", "is_valid": "flag"}`
	res, err := ApplyParameterMapping(mappingJSON, ctxMap)
	if err != nil {
		t.Fatalf("ApplyParameterMapping failed: %v", err)
	}

	if res["access_token"] != "tok_xyz_123" {
		t.Errorf("Expected access_token 'tok_xyz_123', got %v", res["access_token"])
	}
	if res["user_id"] != 99 {
		t.Errorf("Expected user_id 99, got %v", res["user_id"])
	}
	if res["is_valid"] != true {
		t.Errorf("Expected is_valid true, got %v", res["is_valid"])
	}
}

func TestProceduralNodeAndLinkCRUD(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_proc_crud.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// 1. Create nodes
	node1 := memory.ProceduralNode{
		ID:           "node_check_auth",
		Name:         "Check Authorization",
		Description:  "Verify user credentials and bearer token validity",
		ActionType:   memory.ProceduralActionToolCall,
		InputSchema:  `{"type":"object","properties":{"token":{"type":"string"}}}`,
		OutputSchema: `{"type":"object","properties":{"valid":{"type":"boolean"}}}`,
		IsIdempotent: true,
	}
	if err := gllam.UpsertProceduralNode(ctx, node1); err != nil {
		t.Fatalf("Failed to upsert node1: %v", err)
	}

	node2 := memory.ProceduralNode{
		ID:           "node_refresh_token",
		Name:         "Refresh Token",
		Description:  "Exchange refresh token for new access token",
		ActionType:   memory.ProceduralActionToolCall,
		IsIdempotent: false,
	}
	if err := gllam.UpsertProceduralNode(ctx, node2); err != nil {
		t.Fatalf("Failed to upsert node2: %v", err)
	}

	// 2. Retrieve node
	fetched, err := gllam.GetProceduralNode(ctx, "node_check_auth")
	if err != nil {
		t.Fatalf("Failed to get node: %v", err)
	}
	if fetched.Name != "Check Authorization" || !fetched.IsIdempotent {
		t.Errorf("Unexpected node contents: %+v", fetched)
	}

	// 3. List nodes
	nodes, err := gllam.ListProceduralNodes(ctx)
	if err != nil {
		t.Fatalf("Failed to list nodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("Expected 2 nodes, got %d", len(nodes))
	}

	// 4. Create link
	link := memory.ProceduralLink{
		ID:                "link_auth_refresh",
		SourceProcedureID: "node_check_auth",
		TargetProcedureID: "node_refresh_token",
		RelationType:      memory.ProceduralRelationConditionalBranch,
		ConditionExpr:     "valid == false",
		Weight:            1.0,
		Ordering:          0,
		Metadata:          `{"refresh_token":"$.credentials.refresh_token"}`,
	}
	if err := gllam.UpsertProceduralLink(ctx, link); err != nil {
		t.Fatalf("Failed to upsert link: %v", err)
	}

	// 5. Get links for node
	links, err := gllam.GetProceduralLinksForNode(ctx, "node_check_auth")
	if err != nil {
		t.Fatalf("Failed to get links: %v", err)
	}
	if len(links) != 1 || links[0].ConditionExpr != "valid == false" {
		t.Errorf("Unexpected links returned: %+v", links)
	}

	// 6. Delete link and node
	if err := gllam.DeleteProceduralLink(ctx, "link_auth_refresh"); err != nil {
		t.Fatalf("Failed to delete link: %v", err)
	}
	if err := gllam.DeleteProceduralNode(ctx, "node_refresh_token"); err != nil {
		t.Fatalf("Failed to delete node: %v", err)
	}
}

func TestProceduralWorkflowExecutionAndTransitions(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_proc_exec.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Build Workflow:
	// Composite root: "workflow_deploy"
	// Subprocedures inside workflow_deploy:
	//   0: "step_run_tests"
	//   1: "step_build_artifact"
	//   2: "step_publish" (terminal)
	// Transitions:
	//   step_run_tests -> conditional_branch -> step_build_artifact (tests_passed == true)
	//   step_run_tests -> conditional_branch -> step_notify_failure (tests_passed == false)
	//   step_build_artifact -> next -> step_publish

	nodes := []memory.ProceduralNode{
		{ID: "workflow_deploy", Name: "Deploy Workflow", Description: "Full deployment pipeline", ActionType: memory.ProceduralActionComposite},
		{ID: "step_run_tests", Name: "Run Tests", Description: "Execute automated unit and integration tests", ActionType: memory.ProceduralActionToolCall, IsIdempotent: true},
		{ID: "step_build_artifact", Name: "Build Artifact", Description: "Compile and package binaries", ActionType: memory.ProceduralActionToolCall},
		{ID: "step_notify_failure", Name: "Notify Failure", Description: "Send alert on test failure", ActionType: memory.ProceduralActionTerminal},
		{ID: "step_publish", Name: "Publish Release", Description: "Push release to production", ActionType: memory.ProceduralActionTerminal},
	}
	for _, n := range nodes {
		if err := gllam.UpsertProceduralNode(ctx, n); err != nil {
			t.Fatalf("Failed to upsert node %s: %v", n.ID, err)
		}
	}

	links := []memory.ProceduralLink{
		// Hierarchical subprocedures for composite workflow
		{ID: "w_sub_0", SourceProcedureID: "workflow_deploy", TargetProcedureID: "step_run_tests", RelationType: memory.ProceduralRelationSubprocedure, Ordering: 0},
		{ID: "w_sub_1", SourceProcedureID: "workflow_deploy", TargetProcedureID: "step_build_artifact", RelationType: memory.ProceduralRelationSubprocedure, Ordering: 1},
		{ID: "w_sub_2", SourceProcedureID: "workflow_deploy", TargetProcedureID: "step_publish", RelationType: memory.ProceduralRelationSubprocedure, Ordering: 2},

		// Transitions
		{ID: "edge_tests_pass", SourceProcedureID: "step_run_tests", TargetProcedureID: "step_build_artifact", RelationType: memory.ProceduralRelationConditionalBranch, ConditionExpr: "tests_passed == true"},
		{ID: "edge_tests_fail", SourceProcedureID: "step_run_tests", TargetProcedureID: "step_notify_failure", RelationType: memory.ProceduralRelationConditionalBranch, ConditionExpr: "tests_passed == false"},
		{ID: "edge_build_to_pub", SourceProcedureID: "step_build_artifact", TargetProcedureID: "step_publish", RelationType: memory.ProceduralRelationNext},
	}
	for _, l := range links {
		if err := gllam.UpsertProceduralLink(ctx, l); err != nil {
			t.Fatalf("Failed to upsert link %s: %v", l.ID, err)
		}
	}

	// 1. Start Trace
	trace, err := gllam.StartProceduralTrace(ctx, "workflow_deploy", map[string]interface{}{"git_ref": "v1.2.0"})
	if err != nil {
		t.Fatalf("Failed to start trace: %v", err)
	}
	if trace.Status != memory.ProceduralStatusInProgress {
		t.Fatalf("Expected in_progress, got %s", trace.Status)
	}

	// 2. Resolve initial entry step (hierarchical expansion into composite)
	nextSteps, err := gllam.ResolveNextSteps(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to resolve next steps: %v", err)
	}
	if len(nextSteps) != 1 || nextSteps[0].TargetNode.ID != "step_run_tests" {
		t.Fatalf("Expected entry step 'step_run_tests', got: %+v", nextSteps)
	}

	// 3. Record step run 1: tests pass
	step1Output := map[string]interface{}{
		"tests_passed": true,
		"tests_count":  142,
	}
	err = gllam.RecordStepExecution(ctx, trace.ID, "step_run_tests", map[string]interface{}{"ref": "v1.2.0"}, step1Output, memory.ProceduralStepSuccess, "", 1500)
	if err != nil {
		t.Fatalf("Failed to record step 1 execution: %v", err)
	}

	// 4. Resolve next step after tests passed
	nextSteps, err = gllam.ResolveNextSteps(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to resolve next steps after step 1: %v", err)
	}
	if len(nextSteps) != 1 || nextSteps[0].TargetNode.ID != "step_build_artifact" {
		t.Fatalf("Expected 'step_build_artifact', got: %+v", nextSteps)
	}

	// 5. Record step run 2: build artifact
	step2Output := map[string]interface{}{
		"artifact_id": "sha256:abc123456",
		"size_bytes":  41943040,
	}
	err = gllam.RecordStepExecution(ctx, trace.ID, "step_build_artifact", map[string]interface{}{"target": "prod"}, step2Output, memory.ProceduralStepSuccess, "", 3200)
	if err != nil {
		t.Fatalf("Failed to record step 2 execution: %v", err)
	}

	// 6. Resolve next step after build
	nextSteps, err = gllam.ResolveNextSteps(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to resolve next steps after step 2: %v", err)
	}
	if len(nextSteps) != 1 || nextSteps[0].TargetNode.ID != "step_publish" {
		t.Fatalf("Expected 'step_publish', got: %+v", nextSteps)
	}

	// 7. Record step run 3: publish (terminal node)
	step3Output := map[string]interface{}{
		"published_url": "https://cdn.example.com/v1.2.0.tar.gz",
	}
	err = gllam.RecordStepExecution(ctx, trace.ID, "step_publish", map[string]interface{}{"env": "prod"}, step3Output, memory.ProceduralStepSuccess, "", 400)
	if err != nil {
		t.Fatalf("Failed to record step 3 execution: %v", err)
	}

	// 8. Verify Trace status is completed
	completedTrace, err := gllam.GetProceduralTrace(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to fetch completed trace: %v", err)
	}
	if completedTrace.Status != memory.ProceduralStatusCompleted {
		t.Errorf("Expected status completed, got %s", completedTrace.Status)
	}
	if completedTrace.FinishedAt == nil {
		t.Errorf("Expected finished_at timestamp to be set")
	}

	// 9. Inspect execution history & memoization
	history, err := gllam.GetStepRunHistory(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to get step history: %v", err)
	}
	if len(history) != 3 {
		t.Fatalf("Expected 3 step runs in history, got %d", len(history))
	}
	if history[0].StepNumber != 1 || history[1].StepNumber != 2 || history[2].StepNumber != 3 {
		t.Errorf("Step numbers are not monotonic: %+v", history)
	}

	// Test memoization lookup for step_run_tests
	testOutput, found, err := gllam.FindStepOutputInTrace(ctx, trace.ID, "step_run_tests")
	if err != nil || !found {
		t.Fatalf("Expected to find step_run_tests output, found=%v err=%v", found, err)
	}
	if testOutput["tests_count"] != float64(142) && testOutput["tests_count"] != 142 {
		t.Errorf("Unexpected memoized test output: %+v", testOutput)
	}

	// Test unexecuted step returns found = false
	_, foundUnexecuted, _ := gllam.FindStepOutputInTrace(ctx, trace.ID, "step_notify_failure")
	if foundUnexecuted {
		t.Errorf("Expected unexecuted step to return found=false")
	}
}

func TestProceduralSagaCompensations(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_proc_saga.db")

	gllam, err := NewGllamEngine(dbPath, nil)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer gllam.Close()

	if err := gllam.InitSchema(); err != nil {
		t.Fatalf("Failed to init schema: %v", err)
	}

	ctx := context.Background()

	// Nodes:
	// 1. proc_create_vpc
	// 2. proc_destroy_vpc (compensates proc_create_vpc)
	// 3. proc_allocate_ip
	// 4. proc_release_ip (compensates proc_allocate_ip)
	// 5. proc_provision_vm (fails)
	nodes := []memory.ProceduralNode{
		{ID: "proc_create_vpc", Name: "Create VPC", Description: "Network VPC", ActionType: memory.ProceduralActionToolCall},
		{ID: "proc_destroy_vpc", Name: "Destroy VPC", Description: "Rollback VPC", ActionType: memory.ProceduralActionToolCall, IsIdempotent: true},
		{ID: "proc_allocate_ip", Name: "Allocate Elastic IP", Description: "Allocate static IP", ActionType: memory.ProceduralActionToolCall},
		{ID: "proc_release_ip", Name: "Release Elastic IP", Description: "Rollback IP", ActionType: memory.ProceduralActionToolCall, IsIdempotent: true},
		{ID: "proc_provision_vm", Name: "Provision VM", Description: "Launch instance", ActionType: memory.ProceduralActionToolCall},
	}
	for _, n := range nodes {
		if err := gllam.UpsertProceduralNode(ctx, n); err != nil {
			t.Fatalf("Failed to upsert node: %v", err)
		}
	}

	links := []memory.ProceduralLink{
		{ID: "link_comp_vpc", SourceProcedureID: "proc_create_vpc", TargetProcedureID: "proc_destroy_vpc", RelationType: memory.ProceduralRelationCompensates},
		{ID: "link_comp_ip", SourceProcedureID: "proc_allocate_ip", TargetProcedureID: "proc_release_ip", RelationType: memory.ProceduralRelationCompensates},
	}
	for _, l := range links {
		if err := gllam.UpsertProceduralLink(ctx, l); err != nil {
			t.Fatalf("Failed to upsert link: %v", err)
		}
	}

	trace, err := gllam.StartProceduralTrace(ctx, "proc_create_vpc", nil)
	if err != nil {
		t.Fatalf("Failed to start trace: %v", err)
	}

	// Step 1: create vpc succeeds
	_ = gllam.RecordStepExecution(ctx, trace.ID, "proc_create_vpc", nil, map[string]interface{}{"vpc_id": "vpc-001"}, memory.ProceduralStepSuccess, "", 100)

	// Step 2: allocate IP succeeds
	_ = gllam.RecordStepExecution(ctx, trace.ID, "proc_allocate_ip", nil, map[string]interface{}{"ip_id": "eipalloc-002"}, memory.ProceduralStepSuccess, "", 100)

	// Step 3: provision VM fails
	_ = gllam.RecordStepExecution(ctx, trace.ID, "proc_provision_vm", nil, nil, memory.ProceduralStepFailed, "Quota exceeded in region", 50)

	// Query saga compensations
	compensations, err := gllam.ResolveCompensations(ctx, trace.ID)
	if err != nil {
		t.Fatalf("Failed to resolve compensations: %v", err)
	}

	// Must be in reverse execution order:
	// 1. proc_release_ip (for proc_allocate_ip)
	// 2. proc_destroy_vpc (for proc_create_vpc)
	if len(compensations) != 2 {
		t.Fatalf("Expected 2 compensation steps, got %d", len(compensations))
	}
	if compensations[0].CompensationNode.ID != "proc_release_ip" {
		t.Errorf("Expected first compensation to be proc_release_ip, got %s", compensations[0].CompensationNode.ID)
	}
	if compensations[1].CompensationNode.ID != "proc_destroy_vpc" {
		t.Errorf("Expected second compensation to be proc_destroy_vpc, got %s", compensations[1].CompensationNode.ID)
	}
}
