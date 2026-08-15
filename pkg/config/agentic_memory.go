package config

import (
	"encoding/json"
	"fmt"
	"os"
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
	AllowUserGrilling              bool                                   `json:"allow_user_grilling"` // Set false in non-interactive benchmark evaluation (e.g. BEAM)
	TrustWeightPrompt              string                                 `json:"trust_weight_prompt"`
	SourceReliabilityPrompt        string                                 `json:"source_reliability_prompt"`
	SourceReliabilityHeuristics    map[string]int                         `json:"source_reliability_heuristics,omitempty"` // Individual source trust adjustments (e.g. "alice": +150, "dave": -150)
	IngestionSteeringPrompt        string                                 `json:"ingestion_steering_prompt"`               // Fallback global ingestion steering prompt
	IngestionSteeringPrompts       map[string]string                      `json:"ingestion_steering_prompts,omitempty"`    // Per-content-type targeted ingestion steering prompts (e.g. "jira", "confluence", "git", "slack")
	IngestionSteeringDirectives    map[string]IngestionStrategy           `json:"ingestion_steering_directives,omitempty"` // Per-source type ingestion steering strategies
	CustomDocumentTypeRules        map[string]CustomDocumentTypeRule      `json:"custom_document_type_rules,omitempty"`    // Dynamic custom document types with trust baselines and steering strategies
	RepositoryContextDirectives    map[string]RepositoryContextDirective `json:"repository_context_directives,omitempty"` // Documentation repository-specific context extraction directives
	HistoricalContextPrompt        string                                 `json:"historical_context_prompt"`
	SemanticExtractionPrompt       string                                 `json:"semantic_extraction_prompt"`
	ProceduralGeneralizationPrompt string                                 `json:"procedural_generalization_prompt"`
	SalienceQueryPrompt            string                                 `json:"salience_query_prompt"`
	CustomCategoryPrompts          map[string]string                      `json:"custom_category_prompts,omitempty"`
}

func (p *AgenticMemorySystemPrompts) UnmarshalJSON(data []byte) error {
	// Alias prevents recursive UnmarshalJSON calls when parsing raw fields
	type Alias AgenticMemorySystemPrompts
	var aux struct {
		SemanticExtraction json.RawMessage `json:"semantic_extraction"`
		*Alias
	}
	aux.Alias = (*Alias)(p)

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if len(aux.SemanticExtraction) > 0 {
		// 1. Attempt to parse as an array of string lines
		var lines []string
		if err := json.Unmarshal(aux.SemanticExtraction, &lines); err == nil {
			p.SemanticExtraction = strings.TrimSpace(strings.Join(lines, "\n"))
			return nil
		}

		// 2. Fall back to parsing as a single string
		var single string
		if err := json.Unmarshal(aux.SemanticExtraction, &single); err == nil {
			p.SemanticExtraction = strings.TrimSpace(single)
			return nil
		}

		return fmt.Errorf("semantic_extraction must be a string or array of strings")
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

// DefaultAgenticMemorySystemPrompts returns built-in baseline system prompts.
func DefaultAgenticMemorySystemPrompts() *AgenticMemorySystemPrompts {
	return &AgenticMemorySystemPrompts{
		AllowUserGrilling: true,
		IngestionSteeringPrompt: `INGESTION STEERING DIRECTIVES FOR MULTI-AUTHOR & VERSION HISTORY:
1. Confluence / Wiki: Parse page revision history and author edit epochs into CompactedRevisionEpochs.
2. Jira / Issue Trackers: Parse comment history, author provenance, and status transitions (Open -> Resolved).
3. Git Repositories / PRs: Parse branch merge history, commit signatures, and PR review comments.
4. Chat / Slack: Parse thread reply chains and author message timestamps.`,

		IngestionSteeringPrompts: map[string]string{
			"confluence":   "Confluence / Wiki Directives: Parse page revision history and author edit epochs into CompactedRevisionEpochs.",
			"jira":         "Jira / Issue Tracker Directives: Parse comment history, author provenance, and status transitions (Open -> Resolved).",
			"git":          "Git Repositories / PR Directives: Parse branch merge history, commit signatures, and PR review comments.",
			"pull_request": "Pull Request Directives: Parse branch merge history, commit signatures, and PR review comments.",
			"slack":        "Chat / Slack Directives: Parse thread reply chains and author message timestamps.",
		},

		IngestionSteeringDirectives: map[string]IngestionStrategy{
			"confluence":   {TrackRevisionHistory: true, MaxRevisionDepth: 10, CompactAuthorEpochs: true},
			"jira":         {TrackCommentHistory: true, TrackStatusTransitions: true, CompactAuthorEpochs: true},
			"git":          {TrackBranchMerges: true, TrackRevisionHistory: true, CompactAuthorEpochs: true},
			"pull_request": {TrackBranchMerges: true, TrackCommentHistory: true, CompactAuthorEpochs: true},
			"slack":        {TrackThreadReplies: true, CompactAuthorEpochs: true},
		},

		RepositoryContextDirectives: map[string]RepositoryContextDirective{
			"jira": {
				RepositoryType:   "jira",
				ExtractionPrompt: "Extract Jira issue key, status transitions, resolution, priority, and epic linkage into entity context profiles.",
				ContextTemplate:  "Jira Issue: {{key}}\nType: {{type}}\nStatus: {{status}}\nResolution: {{resolution}}",
			},
			"confluence": {
				RepositoryType:   "confluence",
				ExtractionPrompt: "Extract space name, page hierarchy, parent page, and approval status into entity context profiles.",
				ContextTemplate:  "Confluence Space: {{space}}\nParent Page: {{parent}}\nApproval Status: {{status}}",
			},
			"git": {
				RepositoryType:   "git",
				ExtractionPrompt: "Extract branch name, commit hash, pull request ID, and review approval state into entity context profiles.",
				ContextTemplate:  "Git Repo: {{repo}}\nBranch: {{branch}}\nPR: #{{pr_id}}",
			},
		},


		TrustWeightPrompt: `EVALUATION RULESET FOR SOURCE TRUST WEIGHTING (W in [10, 1000]):

1. Formal resolved ticketing systems (e.g. Jira Resolved, GitHub Merged PRs, Git commits) carry baseline weight 800.
2. Approved architecture and design documents carry baseline weight 700.
3. Open tickets, Slack channels, and incident logs carry baseline weight 500.
4. Unstructured meeting notes, support tickets, and email threads carry baseline weight 400.
5. Drafts and personal scratchpads carry baseline weight 200.
6. Individual source reliability heuristics adjust scores per source/person (e.g. Alice +150, Dave -150).
7. Penalize incoherent or high-entropy gibberish text (-250).`,

		SourceReliabilityPrompt: `INDIVIDUAL SOURCE RELIABILITY HEURISTICS:
Evaluate individual source track records based on past documentation completeness. Specific sources/individuals who consistently deliver verified, complete implementations receive positive trust adjustments (+100 to +200). Sources with histories of incomplete drafts, unverified claims, or abandoned proposals receive negative trust adjustments (-100 to -200).`,

		SourceReliabilityHeuristics: map[string]int{
			"alice":          150,
			"carol_lead":     200,
			"bob_contractor": -100,
			"dave_drafts":    -150,
		},


		HistoricalContextPrompt: `CORPUS HISTORICAL & DOMAIN CONTEXT:
The target document corpus contains multi-session transcripts, enterprise ticketing exports, and architectural specifications spanning system evolution. When evaluating temporal ordering or conflicting claims, prioritize verified post-migration architecture state over historical pre-migration discussion notes.`,

		SemanticExtractionPrompt: `SEMANTIC EXTRACTION DIRECTIVES:
Extract grounded entities, caveated relationships, Allen temporal interval relations, turn-bound constraints, and epistemic origin nodes. Maintain strict entity ID fidelity.`,

		ProceduralGeneralizationPrompt: `PROCEDURAL KNOWLEDGE GENERALIZATION DIRECTIVES:
Identify repeated operational workflows and terminal command sequences across episodes. Abstract them into reusable procedural recipes with explicit trigger contexts and feedback rules.`,

		SalienceQueryPrompt: `SALIENCE SCORING DIRECTIVES:
Prioritize focal entities mentioned directly in the user query, their 1-hop semantic neighbors, and active temporal bounds relevant to the query window.`,

		CustomCategoryPrompts: make(map[string]string),
	}
}

// LoadAgenticMemoryConfig loads agentic memory system prompts from a JSON file, falling back to defaults if unreadable.
func LoadAgenticMemoryConfig(path string) (*AgenticMemorySystemPrompts, error) {
	if path == "" {
		return DefaultAgenticMemorySystemPrompts(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return DefaultAgenticMemorySystemPrompts(), fmt.Errorf("failed to read config file at %s, using defaults: %w", path, err)
	}

	cfg := DefaultAgenticMemorySystemPrompts()
	if err := json.Unmarshal(data, cfg); err != nil {
		return DefaultAgenticMemorySystemPrompts(), fmt.Errorf("failed to parse JSON config at %s: %w", path, err)
	}

	return cfg, nil
}
