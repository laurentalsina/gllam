package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// BootstrapProceduralMemory reads the fixtures file and seeds or updates procedural_nodes and procedural_links.
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

	// 1. Upsert Nodes
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
		if err := e.UpsertProceduralNode(ctx, node); err != nil {
			return fmt.Errorf("failed to bootstrap node %s: %w", n.ID, err)
		}
	}

	// 2. Upsert Links
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
		if err := e.UpsertProceduralLink(ctx, link); err != nil {
			return fmt.Errorf("failed to bootstrap link %s: %w", l.ID, err)
		}
	}

	return nil
}

// EnsureProceduralMemoryBootstrapped verifies if procedural_nodes contains records; if empty, it triggers bootstrapping.
func (e *GllamEngine) EnsureProceduralMemoryBootstrapped(ctx context.Context) error {
	var count int
	err := e.dbRO.QueryRowContext(ctx, "SELECT count(*) FROM procedural_nodes").Scan(&count)
	if err != nil {
		// Table might not exist yet if schema was not run
		return nil
	}
	if count == 0 {
		return e.BootstrapProceduralMemory(ctx, "")
	}
	return nil
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
