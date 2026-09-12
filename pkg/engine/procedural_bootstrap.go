package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/config"
	"github.com/laurentalsina/gllam/pkg/memory"
)

// ProceduralNodeFixture represents a fixture entry for procedural_nodes in JSON.
type ProceduralNodeFixture struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	ActionType   string      `json:"action_type"`
	InputSchema  interface{} `json:"input_schema,omitempty"`
	OutputSchema interface{} `json:"output_schema,omitempty"`
	IsIdempotent bool        `json:"is_idempotent"`
	Metadata     interface{} `json:"metadata,omitempty"`
	CreatedAt    int64       `json:"created_at,omitempty"`
	UpdatedAt    int64       `json:"updated_at,omitempty"`
}

// ProceduralLinkFixture represents a fixture entry for procedural_links in JSON.
type ProceduralLinkFixture struct {
	ID                string      `json:"id"`
	SourceProcedureID string      `json:"source_procedure_id"`
	TargetProcedureID string      `json:"target_procedure_id"`
	RelationType      string      `json:"relation_type"`
	ConditionExpr     string      `json:"condition_expr,omitempty"`
	Weight            float64     `json:"weight,omitempty"`
	Ordering          int         `json:"ordering"`
	Metadata          interface{} `json:"metadata,omitempty"`
	CreatedAt         int64       `json:"created_at,omitempty"`
}

// ProceduralFixtures represents the complete JSON fixture document.
type ProceduralFixtures struct {
	Version     string                  `json:"version"`
	Description string                  `json:"description"`
	Nodes       []ProceduralNodeFixture `json:"nodes"`
	Links       []ProceduralLinkFixture `json:"links"`
}

// toJSONString converts an interface{} (string, map, etc.) to a serialized JSON string.
func toJSONString(val interface{}) string {
	if val == nil {
		return "{}"
	}
	switch v := val.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "{}"
		}
		if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			return trimmed
		}
		b, _ := json.Marshal(v)
		return string(b)
	default:
		b, err := json.Marshal(val)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

// findFixturesPath searches known relative paths for procedural_fixtures.json.
func findFixturesPath(customPath string) (string, error) {
	if customPath != "" {
		if _, err := os.Stat(customPath); err == nil {
			return customPath, nil
		}
	}

	candidates := []string{
		"config/procedural_fixtures.json",
		"../config/procedural_fixtures.json",
		"../../config/procedural_fixtures.json",
		"../../../config/procedural_fixtures.json",
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	// Try relative to executable or working dir
	if wd, err := os.Getwd(); err == nil {
		p := filepath.Join(wd, "config", "procedural_fixtures.json")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("procedural_fixtures.json not found in any standard path")
}

// BootstrapProceduralMemory reads the fixtures file and non-destructively seeds or updates procedural_nodes and procedural_links.
// Any node or link modified by user feedback or reinforced through usage is strictly preserved.
func (e *GllamEngine) BootstrapProceduralMemory(ctx context.Context, fixturesPath string) error {
	resolvedPath, err := findFixturesPath(fixturesPath)
	if err != nil {
		return fmt.Errorf("failed to locate procedural fixtures: %w", err)
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return fmt.Errorf("failed to read fixtures file %s: %w", resolvedPath, err)
	}

	var fixtures ProceduralFixtures
	if err := json.Unmarshal(data, &fixtures); err != nil {
		return fmt.Errorf("failed to parse procedural fixtures: %w", err)
	}

	// 1. Non-destructively bridge legacy procedural_knowledge entries if any exist
	_ = e.MigrateLegacyProceduralKnowledge(ctx)

	// 2. Non-destructively Seed Nodes (preserves user_feedback_modified and feedback rules)
	for _, n := range fixtures.Nodes {
		node := memory.ProceduralNode{
			ID:           n.ID,
			Name:         n.Name,
			Description:  n.Description,
			ActionType:   n.ActionType,
			InputSchema:  toJSONString(n.InputSchema),
			OutputSchema: toJSONString(n.OutputSchema),
			IsIdempotent: n.IsIdempotent,
			Metadata:     toJSONString(n.Metadata),
			CreatedAt:    n.CreatedAt,
			UpdatedAt:    n.UpdatedAt,
		}
		if err := e.SeedProceduralNode(ctx, node); err != nil {
			return fmt.Errorf("failed to seed procedural node %s: %w", n.ID, err)
		}
	}

	// 3. Non-destructively Seed Links (preserves user_feedback_modified and traversed weights)
	for _, l := range fixtures.Links {
		weight := l.Weight
		if weight == 0 {
			weight = 1.0
		}
		link := memory.ProceduralLink{
			ID:                l.ID,
			SourceProcedureID: l.SourceProcedureID,
			TargetProcedureID: l.TargetProcedureID,
			RelationType:      l.RelationType,
			ConditionExpr:     l.ConditionExpr,
			Weight:            weight,
			Ordering:          l.Ordering,
			Metadata:          toJSONString(l.Metadata),
			CreatedAt:         l.CreatedAt,
		}
		if err := e.SeedProceduralLink(ctx, link); err != nil {
			return fmt.Errorf("failed to seed procedural link %s: %w", l.ID, err)
		}
	}

	return nil
}

// MigrateLegacyProceduralKnowledge safely bridges any legacy procedural_knowledge records
// into procedural_nodes without destroying existing nodes or overwriting adaptations.
func (e *GllamEngine) MigrateLegacyProceduralKnowledge(ctx context.Context) error {
	var tableExists int
	_ = e.dbRO.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='procedural_knowledge'").Scan(&tableExists)
	if tableExists == 0 {
		return nil
	}

	type legacyRecord struct {
		id             string
		taskType       string
		scope          string
		triggerContext string
		instructions   string
		feedbackRules  string
		timesApplied   int
		isHelpful      bool
	}

	rows, err := e.dbRO.QueryContext(ctx, `
		SELECT id, task_type, scope, COALESCE(trigger_context, ''), instructions,
		       COALESCE(user_feedback_rules, ''), times_applied, COALESCE(is_highly_helpful, 0)
		FROM procedural_knowledge
	`)
	if err != nil {
		return fmt.Errorf("failed to query legacy procedural_knowledge: %w", err)
	}

	var records []legacyRecord
	for rows.Next() {
		var r legacyRecord
		var isHelpfulVal interface{}
		if err := rows.Scan(&r.id, &r.taskType, &r.scope, &r.triggerContext, &r.instructions, &r.feedbackRules, &r.timesApplied, &isHelpfulVal); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan legacy procedural_knowledge row: %w", err)
		}
		switch v := isHelpfulVal.(type) {
		case bool:
			r.isHelpful = v
		case int64:
			r.isHelpful = (v != 0)
		case int:
			r.isHelpful = (v != 0)
		}
		records = append(records, r)
	}
	rows.Close()

	for _, r := range records {
		hasFeedback := (r.feedbackRules != "" || r.timesApplied > 0 || r.isHelpful)
		feedbackMod := 0
		if hasFeedback {
			feedbackMod = 1
		}
		isHelpfulInt := 0
		if r.isHelpful {
			isHelpfulInt = 1
		}

		var existingID, existingFeedbackRules string
		_ = e.dbRO.QueryRowContext(ctx, "SELECT id, user_feedback_rules FROM procedural_nodes WHERE id = ? OR id = ?", r.id, r.taskType).Scan(&existingID, &existingFeedbackRules)

		if existingID == "" {
			metaMap := map[string]interface{}{
				"legacy_scope":           r.scope,
				"legacy_trigger_context": r.triggerContext,
			}
			metaBytes, _ := json.Marshal(metaMap)

			_, err := e.db.ExecContext(ctx, `
				INSERT INTO procedural_nodes (
					id, name, description, action_type, input_schema, output_schema,
					is_idempotent, metadata, user_feedback_modified, user_feedback_rules,
					times_applied, is_highly_helpful, created_at, updated_at
				) VALUES (?, ?, ?, 'composite', NULL, NULL, 0, ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now'))
			`, r.id, r.taskType, r.instructions, string(metaBytes), feedbackMod, r.feedbackRules, r.timesApplied, isHelpfulInt)
			if err != nil {
				return fmt.Errorf("failed to migrate legacy procedural node %s: %w", r.id, err)
			}
		} else if hasFeedback && existingFeedbackRules == "" {
			_, err := e.db.ExecContext(ctx, `
				UPDATE procedural_nodes
				SET user_feedback_rules = ?,
				    user_feedback_modified = 1,
				    times_applied = MAX(times_applied, ?),
				    is_highly_helpful = MAX(is_highly_helpful, ?),
				    updated_at = strftime('%s', 'now')
				WHERE id = ?
			`, r.feedbackRules, r.timesApplied, isHelpfulInt, existingID)
			if err != nil {
				return fmt.Errorf("failed to update procedural node with legacy feedback: %w", err)
			}
		}
	}
	return nil
}

// EnsureProceduralMemoryBootstrapped verifies and seeds procedural fixtures non-destructively,
// guaranteeing that any existing user feedback or adaptations are strictly preserved.
func (e *GllamEngine) EnsureProceduralMemoryBootstrapped(ctx context.Context) error {
	return e.BootstrapProceduralMemory(ctx, "")
}

// UpdateProceduralPrompt updates a prompt stored in a node's metadata, stamps user_feedback_modified = true,
// and synchronizes the change to e.SystemPrompts.
func (e *GllamEngine) UpdateProceduralPrompt(ctx context.Context, nodeID string, promptKey string, newPrompt string) error {
	node, err := e.GetProceduralNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeID, err)
	}

	var meta map[string]interface{}
	if node.Metadata == "" || node.Metadata == "{}" {
		meta = make(map[string]interface{})
	} else {
		if err := json.Unmarshal([]byte(node.Metadata), &meta); err != nil {
			meta = make(map[string]interface{})
		}
	}

	if promptKey == "" {
		promptKey = "system_prompt"
	}
	meta[promptKey] = newPrompt
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}

	node.Metadata = string(metaBytes)
	node.UserFeedbackModified = true
	node.UpdatedAt = time.Now().Unix()

	if err := e.UpsertProceduralNode(ctx, *node); err != nil {
		return fmt.Errorf("failed to update node %s: %w", nodeID, err)
	}

	return e.SyncSystemPromptsFromProceduralMemory(ctx)
}

// GetProceduralPrompt extracts a prompt template or guideline string stored in a procedural node's metadata.
func (e *GllamEngine) GetProceduralPrompt(ctx context.Context, nodeID string, promptKey string) (string, error) {
	node, err := e.GetProceduralNode(ctx, nodeID)
	if err != nil {
		return "", err
	}

	if node.Metadata == "" || node.Metadata == "{}" {
		return node.Description, nil
	}

	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(node.Metadata), &meta); err != nil {
		return node.Description, nil
	}

	if promptKey != "" {
		if val, ok := meta[promptKey]; ok {
			return fmt.Sprintf("%v", val), nil
		}
	}

	// Fallback sequence: system_prompt -> guidelines -> description
	if val, ok := meta["system_prompt"]; ok {
		return fmt.Sprintf("%v", val), nil
	}
	if val, ok := meta["guidelines"]; ok {
		return fmt.Sprintf("%v", val), nil
	}

	return node.Description, nil
}

// SyncSystemPromptsFromProceduralMemory populates e.SystemPrompts from the database's procedural_nodes.
func (e *GllamEngine) SyncSystemPromptsFromProceduralMemory(ctx context.Context) error {
	if e.SystemPrompts == nil {
		e.SystemPrompts = &config.AgenticMemorySystemPrompts{
			ChunkSize:             5000,
			ChunkOverlap:          500,
			CustomCategoryPrompts: make(map[string]string),
		}
	}
	if e.SystemPrompts.CustomCategoryPrompts == nil {
		e.SystemPrompts.CustomCategoryPrompts = make(map[string]string)
	}

	nodes, err := e.ListProceduralNodes(ctx)
	if err != nil {
		return err
	}

	for _, n := range nodes {
		var meta map[string]interface{}
		if err := json.Unmarshal([]byte(n.Metadata), &meta); err != nil {
			continue
		}

		getMetaStr := func(k string) string {
			if v, ok := meta[k]; ok {
				return fmt.Sprintf("%v", v)
			}
			return ""
		}

		switch n.ID {
		case "proc_first_pass_direct_qa":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.DirectQAPrompt = p
			}
		case "proc_simple_temporal_retrieval":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.SimpleTemporalRetrieval = p
			}
		case "proc_jit_turn_compression":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.PreprocessCompressionPrompt = p
			}
		case "proc_jit_semantic_extraction":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.SemanticExtraction = p
			}
			if p := getMetaStr("system_prompt_temporal"); p != "" {
				e.SystemPrompts.SemanticExtractionTemporal = p
			}
		case "proc_final_qa":
			if p := getMetaStr("response_guidelines"); p != "" {
				e.SystemPrompts.ResponseGuidelines = p
			}
			if p := getMetaStr("temporal_guidelines"); p != "" {
				e.SystemPrompts.TemporalReasoningGuidelines = p
			}
		case "proc_trust_weight_evaluator":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.TrustWeightPrompt = p
			}
		case "proc_source_reliability_analyzer":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.SourceReliabilityPrompt = p
			}
			if h, ok := meta["heuristics"].(map[string]interface{}); ok {
				if e.SystemPrompts.SourceReliabilityHeuristics == nil {
					e.SystemPrompts.SourceReliabilityHeuristics = make(map[string]int)
				}
				for k, v := range h {
					if intVal, ok := v.(float64); ok {
						e.SystemPrompts.SourceReliabilityHeuristics[k] = int(intVal)
					}
				}
			}
		case "proc_ingestion_steering":
			if p := getMetaStr("system_prompt"); p != "" {
				e.SystemPrompts.IngestionSteeringPrompt = p
			}
		default:
			if strings.HasPrefix(n.ID, "proc_category_") {
				cat := strings.TrimPrefix(n.ID, "proc_category_")
				if g := getMetaStr("guidelines"); g != "" {
					e.SystemPrompts.CustomCategoryPrompts[cat] = g
				}
			}
		}
	}

	return nil
}
