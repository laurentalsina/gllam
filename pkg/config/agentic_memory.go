package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// AgenticMemorySystemPrompts defines configurable system prompts and heuristics
// for epistemic trust weighting, corpus historical context, semantic extraction,
// procedural generalization, and salience scoring.
type AgenticMemorySystemPrompts struct {
	TrustWeightPrompt              string            `json:"trust_weight_prompt"`
	HistoricalContextPrompt        string            `json:"historical_context_prompt"`
	SemanticExtractionPrompt       string            `json:"semantic_extraction_prompt"`
	ProceduralGeneralizationPrompt string            `json:"procedural_generalization_prompt"`
	SalienceQueryPrompt            string            `json:"salience_query_prompt"`
	CustomCategoryPrompts          map[string]string `json:"custom_category_prompts,omitempty"`
}

// DefaultAgenticMemorySystemPrompts returns built-in baseline system prompts.
func DefaultAgenticMemorySystemPrompts() *AgenticMemorySystemPrompts {
	return &AgenticMemorySystemPrompts{
		TrustWeightPrompt: `EVALUATION RULESET FOR SOURCE TRUST WEIGHTING (W in [10, 1000]):
1. Formal resolved ticketing systems (e.g. Jira Resolved, GitHub Merged PRs, Git commits) carry baseline weight 800.
2. Approved architecture and design documents carry baseline weight 700.
3. Open tickets, Slack channels, and incident logs carry baseline weight 500.
4. Unstructured meeting notes, support tickets, and email threads carry baseline weight 400.
5. Drafts and personal scratchpads carry baseline weight 200.
6. Roles: Admin/CI-CD (+150), Tech Lead (+100), Verified Engineer (+50).
7. Penalize incoherent or high-entropy gibberish text (-250).`,

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
