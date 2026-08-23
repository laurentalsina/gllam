package memory

import "time"

const (
	NodeTypeEvent         = "event"
	NodeTypeState         = "state"
	NodeTypeEntity        = "entity"
	NodeTypeService       = "service"
	NodeTypeContradiction = "contradiction"
	NodeTypeRule          = "rule"
	NodeTypeConstraint    = "constraint"
	NodeTypeHuman         = "human"
	NodeTypeAgent         = "agent"
	NodeTypeSystem        = "system"
	NodeTypeFallacy       = "fallacy"
	NodeTypeCategory      = "category"
)


type EpisodicSummary struct {
    ID          string `json:"id"`
    SessionID   string `json:"session_id"`
    SummaryText string `json:"summary_text"`
    SourceURI   string `json:"source_uri"`
    CreatedAt   time.Time `json:"created_at"`
}


type ProceduralKnowledge struct {
    ID                string `json:"id"`
    TaskType          string `json:"task_type"`
    Scope             string `json:"scope"`
    TriggerContext    string `json:"trigger_context"`
    Instructions      string `json:"instructions"`
    UserFeedbackRules string `json:"user_feedback_rules"`
    TimesApplied      int    `json:"times_applied"`
    IsHighlyHelpful   bool   `json:"is_highly_helpful"`
    Version           int    `json:"version"`
    SupersededBy      string `json:"superseded_by"`
    CreatedAt         time.Time `json:"created_at"`
    UpdatedAt         time.Time `json:"updated_at"`
}


type SemanticNode struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	ContextPrompt string `json:"context_prompt"`
	TrustWeight   int    `json:"trust_weight"` // Epistemic trust weight (e.g. 900 for Jira Resolved/PR Merged, 100 for draft)
	TaxonomyPath  string `json:"taxonomy_path"` // Materialized path (e.g. /Engineering/Infrastructure/Databases/Relational/Postgres)
	IsCategory    bool   `json:"is_category"`   // Flag indicating if node represents a taxonomy category
	CaveatSummary string    `json:"caveat_summary,omitempty"` // Compacted historical node caveat summary
	CreatedFrom   string    `json:"created_from"`          // Reference to raw data that led to creation (e.g. filename + chunk number)
        CreatedAt     time.Time `json:"created_at"`
        UpdatedAt     time.Time `json:"updated_at"`
}


type TaxonomyNode struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Path         string          `json:"path"`
	IsCategory   bool            `json:"is_category"`// Flag indicating if node represents a taxonomy category
	ParentPath   string          `json:"parent_path,omitempty"`
	Children     []*TaxonomyNode `json:"children,omitempty"`
	DirectMemberCount int        `json:"direct_member_count,omitempty"`
        CreatedAt     time.Time      `json:"created_at"`
        UpdatedAt     time.Time      `json:"updated_at"`
}

type SemanticTemporalAttributes struct {
	ID               string `json:"id,omitempty"`
	ValidFrom        string `json:"valid_from,omitempty"`
	ValidUntil       string `json:"valid_until,omitempty"`
	TemporalAnchorID string `json:"temporal_anchor_id,omitempty"`
	TemporalRelation string `json:"temporal_relation,omitempty"`
	TemporalNote     string `json:"temporal_note,omitempty"`
}

// This covers Epistemic (thought/known by someone), Alethic (possible/impossible/necessary), and Deontic (obligations, permissions, prohibitions) modalities
type SemanticLink struct {
    SourceID              string  `json:"source_id"`
    TargetID              string  `json:"target_id"`
    Relationship          string  `json:"relationship"`
    Caveats               string  `json:"caveats"`                // certainty, applicability, justification...
    Modality              string  `json:"modality"`
    OriginID              string  `json:"origin_id"`              // node (id) for human/agent/system that provided information about the link
    ResolutionRationale   string  `json:"resolution_rationale"`   // Explanation when resolving a contradiction
    CreatedFrom           string  `json:"created_from"`           // Reference to raw data that led to creation (e.g. filename + chunk number)
    CreatedAt             time.Time `json:"created_at"`
    UpdatedAt             time.Time `json:"updated_at"`
    TemporalLinkID        string                      `json:"temporal_link_id,omitempty"`
    Temporal              *SemanticTemporalAttributes `json:"temporal,omitempty"`
}

type DocumentVersion struct {
	ID            string `json:"id"`
	LineageID     string `json:"lineage_id"`
	VersionNumber int    `json:"version_number"`
	AuthorID      string `json:"author_id"`
	AuthorName    string `json:"author_name"`
	ChangeSummary string `json:"change_summary"`
	StartLine     int    `json:"start_line,omitempty"`
	EndLine       int    `json:"end_line,omitempty"`
	CharOffset    int    `json:"char_offset,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}


type CompactedRevisionEpoch struct {
	AuthorID         string `json:"author_id"`
	AuthorName       string `json:"author_name"`
	VersionRange     string `json:"version_range"`
	TimeRange        string `json:"time_range"`
	SyntheticSummary string `json:"synthetic_summary"`
}


type DocumentLineage struct {
	ID               string                   `json:"id"`
	NodeID           string                   `json:"node_id"`
	SourceURI        string                   `json:"source_uri"`
	DocumentTitle    string                   `json:"document_title"`
	SourceType       string                   `json:"source_type"`
	LineNumber       int                      `json:"line_number,omitempty"`
	CharOffset       int                      `json:"char_offset,omitempty"`
	Checksum         string                   `json:"checksum,omitempty"`
	Authors          []string                 `json:"authors,omitempty"`
	Versions         []DocumentVersion        `json:"versions,omitempty"`
	RevisionEpochs   []CompactedRevisionEpoch `json:"revision_epochs,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
}


type CompiledContext struct {
	Procedural         []ProceduralKnowledge
	SemanticNodes      []SemanticNode
	SemanticLinks      []SemanticLink
	Episodic           []EpisodicSummary
	Lineage            []DocumentLineage
	PlannerOutput      string
	ResponseGuidelines string
}


type SyntheticTraceTestScenario struct {
	ID               string   `json:"id"`
	PromptQuery      string   `json:"prompt_query"`
	SimulatedAnswer  string   `json:"simulated_answer"`
	RetrievedNodeIDs []string `json:"retrieved_node_ids"`
	IsConsistent     bool     `json:"is_consistent"`
	ClarityScore     float64  `json:"clarity_score"`
}


type MemorySleepReport struct {
	SleepCycleID              string                      `json:"sleep_cycle_id"`
	DurationSeconds           float64                     `json:"duration_seconds"`
	PrunedStaleLinksCount     int                         `json:"pruned_stale_links_count"`
	CompactedRevisionsCount   int                         `json:"compacted_revisions_count"`
	ConsolidatedTaxonomyCount int                         `json:"consolidated_taxonomy_count"`
	SimulatedTraceTests       []SyntheticTraceTestScenario `json:"simulated_trace_tests"`
	MemoryClarityScore        float64                     `json:"memory_clarity_score"`
	MemoryConsistencyScore    float64                     `json:"memory_consistency_score"`
}
