package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type IngestionStrategy struct {
	TrackRevisionHistory   bool `json:"track_revision_history"`
	TrackCommentHistory    bool `json:"track_comment_history"`
	TrackStatusTransitions bool `json:"track_status_transitions"`
	TrackBranchMerges      bool `json:"track_branch_merges"`
	TrackThreadReplies     bool `json:"track_thread_replies"`
	MaxRevisionDepth       int  `json:"max_revision_depth,omitempty"`
	CompactAuthorEpochs    bool `json:"compact_author_epochs"`
}

type CustomDocumentTypeRule struct {
	TypeName            string            `json:"type_name"`
	BaselineTrustWeight int               `json:"baseline_trust_weight"`
	IngestionStrategy   IngestionStrategy `json:"ingestion_strategy"`
	Description         string            `json:"description,omitempty"`
}

type RepositoryContextDirective struct {
	RepositoryType     string            `json:"repository_type"`
	ExtractionPrompt   string            `json:"extraction_prompt"`
	MetadataFieldRules map[string]string `json:"metadata_field_rules,omitempty"`
	ContextTemplate    string            `json:"context_template"`
}

type AgenticMemorySystemPrompts struct {
	ChunkSize                      int                                    `json:"chunk_size"`
	ChunkOverlap                   int                                    `json:"chunk_overlap"` 
	SemanticExtraction             string                                 `json:"semantic_extraction"` // Stored as array of strings, but loaded as a concatenated String
	SemanticExtractionTemporal     string                                 `json:"semantic_extraction_temporal"` // Alternate temporal extraction prompt
	AllowUserGrilling              bool                                   `json:"allow_user_grilling"` // Set false in non-interactive benchmark evaluation (e.g. BEAM)
	BitemporalSoftDelete           bool                                   `json:"bitemporal_soft_delete"` // Set true to soft-expire conflicting/superseded facts instead of physical delete
	SemanticDistanceThreshold      float64                                `json:"semantic_distance_threshold"` // Vector similarity cosine distance threshold for entities
	TrustWeightPrompt              string                                 `json:"trust_weight_prompt"`
	SourceReliabilityPrompt        string                                 `json:"source_reliability_prompt"`
	SourceReliabilityHeuristics    map[string]int                         `json:"source_reliability_heuristics,omitempty"` // Individual source trust adjustments (e.g. "alice": +150, "dave": -150)
	IngestionSteeringPrompt        string                                 `json:"ingestion_steering_prompt"`               // Fallback global ingestion steering prompt
	IngestionSteeringPrompts       map[string]string                      `json:"ingestion_steering_prompts,omitempty"`    // Per-content-type targeted ingestion steering prompts (e.g. "jira", "confluence", "git", "slack")
	IngestionSteeringDirectives    map[string]IngestionStrategy           `json:"ingestion_steering_directives,omitempty"` // Per-source type ingestion steering strategies
	CustomDocumentTypeRules        map[string]CustomDocumentTypeRule      `json:"custom_document_type_rules,omitempty"`    // Dynamic custom document types with trust baselines and steering strategies
	RepositoryContextDirectives    map[string]RepositoryContextDirective `json:"repository_context_directives,omitempty"` // Documentation repository-specific context extraction directives
	DirectQAPrompt                 string                                 `json:"direct_qa_prompt"`
	SimpleTemporalRetrieval        string                                 `json:"simple_temporal_retrieval"`
	HistoricalContextPrompt        string                                 `json:"historical_context_prompt"`
	SemanticExtractionPrompt       string                                 `json:"semantic_extraction_prompt"`
	ProceduralGeneralizationPrompt string                                 `json:"procedural_generalization_prompt"`
	SalienceQueryPrompt            string                                 `json:"salience_query_prompt"`
	CustomCategoryPrompts          map[string]string                      `json:"custom_category_prompts,omitempty"`
	ResponseGuidelines             string                                 `json:"response_guidelines"`
	TemporalReasoningGuidelines    string                                 `json:"temporal_reasoning_guidelines"`
	ConflictWarningPrompt          string                                 `json:"conflict_warning_prompt"`
	LineageCitationsPrompt         string                                 `json:"lineage_citations_prompt"`
	PreprocessCompressionPrompt    string                                 `json:"preprocess_compression_prompt"`
}

func (p *AgenticMemorySystemPrompts) UnmarshalJSON(data []byte) error {
	// Alias prevents recursive UnmarshalJSON calls when parsing raw fields
	type Alias AgenticMemorySystemPrompts
	var aux struct {
		SemanticExtraction          json.RawMessage `json:"semantic_extraction"`
		SemanticExtractionTemporal  json.RawMessage `json:"semantic_extraction_temporal"`
		DirectQAPrompt              json.RawMessage `json:"direct_qa_prompt"`
		SimpleTemporalRetrieval     json.RawMessage `json:"simple_temporal_retrieval"`
		ResponseGuidelines          json.RawMessage `json:"response_guidelines"`
		TemporalReasoningGuidelines json.RawMessage `json:"temporal_reasoning_guidelines"`
		ConflictWarningPrompt       json.RawMessage `json:"conflict_warning_prompt"`
		LineageCitationsPrompt      json.RawMessage `json:"lineage_citations_prompt"`
		PreprocessCompressionPrompt json.RawMessage `json:"preprocess_compression_prompt"`
		*Alias
	}
	aux.Alias = (*Alias)(p)

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if len(aux.SemanticExtraction) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.SemanticExtraction, &lines); err == nil {
			p.SemanticExtraction = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.SemanticExtraction, &single); err == nil {
				p.SemanticExtraction = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("semantic_extraction must be a string or array of strings")
			}
		}
	}

	if len(aux.SemanticExtractionTemporal) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.SemanticExtractionTemporal, &lines); err == nil {
			p.SemanticExtractionTemporal = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.SemanticExtractionTemporal, &single); err == nil {
				p.SemanticExtractionTemporal = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("semantic_extraction_temporal must be a string or array of strings")
			}
		}
	}

	if len(aux.DirectQAPrompt) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.DirectQAPrompt, &lines); err == nil {
			p.DirectQAPrompt = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.DirectQAPrompt, &single); err == nil {
				p.DirectQAPrompt = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("direct_qa_prompt must be a string or array of strings")
			}
		}
	}

	if len(aux.SimpleTemporalRetrieval) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.SimpleTemporalRetrieval, &lines); err == nil {
			p.SimpleTemporalRetrieval = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.SimpleTemporalRetrieval, &single); err == nil {
				p.SimpleTemporalRetrieval = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("simple_temporal_retrieval must be a string or array of strings")
			}
		}
	}

	if len(aux.ResponseGuidelines) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.ResponseGuidelines, &lines); err == nil {
			p.ResponseGuidelines = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.ResponseGuidelines, &single); err == nil {
				p.ResponseGuidelines = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("response_guidelines must be a string or array of strings")
			}
		}
	}

	if len(aux.TemporalReasoningGuidelines) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.TemporalReasoningGuidelines, &lines); err == nil {
			p.TemporalReasoningGuidelines = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.TemporalReasoningGuidelines, &single); err == nil {
				p.TemporalReasoningGuidelines = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("temporal_reasoning_guidelines must be a string or array of strings")
			}
		}
	}

	if len(aux.ConflictWarningPrompt) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.ConflictWarningPrompt, &lines); err == nil {
			p.ConflictWarningPrompt = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.ConflictWarningPrompt, &single); err == nil {
				p.ConflictWarningPrompt = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("conflict_warning_prompt must be a string or array of strings")
			}
		}
	}

	if len(aux.LineageCitationsPrompt) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.LineageCitationsPrompt, &lines); err == nil {
			p.LineageCitationsPrompt = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.LineageCitationsPrompt, &single); err == nil {
				p.LineageCitationsPrompt = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("lineage_citations_prompt must be a string or array of strings")
			}
		}
	}

	if len(aux.PreprocessCompressionPrompt) > 0 {
		var lines []string
		if err := json.Unmarshal(aux.PreprocessCompressionPrompt, &lines); err == nil {
			p.PreprocessCompressionPrompt = strings.TrimSpace(strings.Join(lines, "\n"))
		} else {
			var single string
			if err := json.Unmarshal(aux.PreprocessCompressionPrompt, &single); err == nil {
				p.PreprocessCompressionPrompt = strings.TrimSpace(single)
			} else {
				return fmt.Errorf("preprocess_compression_prompt must be a string or array of strings")
			}
		}
	}

	return nil
}

// GetIngestionSteeringPrompt returns the content-type-specific steering prompt for a document type (e.g., "jira", "confluence"),
// falling back to the global IngestionSteeringPrompt if unconfigured.
func (a *AgenticMemorySystemPrompts) GetIngestionSteeringPrompt(docType string) string {
	if a == nil {
		return ""
	}
	if a.IngestionSteeringPrompts != nil {
		if p, ok := a.IngestionSteeringPrompts[strings.ToLower(docType)]; ok && p != "" {
			return p
		}
	}
	return a.IngestionSteeringPrompt
}

// FindDefaultConfigPath searches for config/agentic_memory.json in known relative locations.
func FindDefaultConfigPath() string {
	if envPath := os.Getenv("PROMPTS_CONFIG"); envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	candidates := []string{
		"config/agentic_memory.json",
		"../config/agentic_memory.json",
		"../../config/agentic_memory.json",
		"../../../config/agentic_memory.json",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 5; i++ {
			p := filepath.Join(dir, "config", "agentic_memory.json")
			if _, err := os.Stat(p); err == nil {
				return p
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return ""
}

// DefaultAgenticMemorySystemPrompts loads prompts from config/agentic_memory.json, failing hard if missing.
func DefaultAgenticMemorySystemPrompts() *AgenticMemorySystemPrompts {
	cfgPath := FindDefaultConfigPath()
	if cfgPath == "" {
		panic("FATAL: config/agentic_memory.json not found! Prompts must be loaded from config/agentic_memory.json")
	}
	cfg, err := LoadAgenticMemoryConfig(cfgPath)
	if err != nil {
		panic(fmt.Sprintf("FATAL: failed to load %s: %v", cfgPath, err))
	}
	return cfg
}

// LoadAgenticMemoryConfig loads agentic memory system prompts strictly from a JSON file.
// It fails hard and fast if the file cannot be read or parsed.
func LoadAgenticMemoryConfig(path string) (*AgenticMemorySystemPrompts, error) {
	if path == "" {
		return nil, fmt.Errorf("agentic memory prompts config path cannot be empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file at %s: %w", path, err)
	}

	var cfg AgenticMemorySystemPrompts
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse JSON config at %s: %w", path, err)
	}

	return &cfg, nil
}
