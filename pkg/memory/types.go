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

// Procedural Action Types
const (
	ProceduralActionComposite    = "composite"
	ProceduralActionToolCall     = "tool_call"
	ProceduralActionLLMReasoning = "llm_reasoning"
	ProceduralActionTerminal     = "terminal"
)

// Procedural Link Relation Types
const (
	ProceduralRelationNext              = "next"
	ProceduralRelationSubprocedure      = "subprocedure"
	ProceduralRelationConditionalBranch = "conditional_branch"
	ProceduralRelationOnFailure         = "on_failure"
	ProceduralRelationCompensates       = "compensates"
)

// Procedural Trace Statuses
const (
	ProceduralStatusPending    = "pending"
	ProceduralStatusInProgress = "in_progress"
	ProceduralStatusCompleted  = "completed"
	ProceduralStatusFailed     = "failed"
	ProceduralStatusPaused     = "paused"
)

// Procedural Step Run Statuses
const (
	ProceduralStepSuccess     = "success"
	ProceduralStepFailed      = "failed"
	ProceduralStepSkipped     = "skipped"
	ProceduralStepCompensated = "compensated"
)

// ProceduralNode represents a discrete action or composite workflow container.
type ProceduralNode struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	ActionType           string `json:"action_type"`
	InputSchema          string `json:"input_schema,omitempty"`
	OutputSchema         string `json:"output_schema,omitempty"`
	IsIdempotent         bool   `json:"is_idempotent"`
	Metadata             string `json:"metadata,omitempty"` // Arbitrary JSON
	UserFeedbackModified bool   `json:"user_feedback_modified"`
	UserFeedbackRules    string `json:"user_feedback_rules,omitempty"`
	TimesApplied         int    `json:"times_applied"`
	IsHighlyHelpful      bool   `json:"is_highly_helpful"`
	CreatedAt            int64  `json:"created_at"` // Unix epoch
	UpdatedAt            int64  `json:"updated_at"` // Unix epoch
}

// ProceduralLink represents a directed transition between procedures.
type ProceduralLink struct {
	ID                   string  `json:"id"`
	SourceProcedureID    string  `json:"source_procedure_id"`
	TargetProcedureID    string  `json:"target_procedure_id"`
	RelationType         string  `json:"relation_type"`
	ConditionExpr        string  `json:"condition_expr,omitempty"`
	Weight               float64 `json:"weight"`
	Ordering             int     `json:"ordering"`
	Metadata             string  `json:"metadata,omitempty"` // JSON string for parameter mapping
	UserFeedbackModified bool    `json:"user_feedback_modified"`
	TimesTraversed       int     `json:"times_traversed"`
	CreatedAt            int64   `json:"created_at"`
}

// ProceduralExecutionTrace represents the runtime execution context and state of a procedure instance.
type ProceduralExecutionTrace struct {
	ID              string `json:"id"`
	RootProcedureID string `json:"root_procedure_id"`
	CurrentNodeID   string `json:"current_node_id,omitempty"`
	Status          string `json:"status"`
	ContextState    string `json:"context_state"` // JSON string
	ErrorDetails    string `json:"error_details,omitempty"`
	StartedAt       int64  `json:"started_at"`
	FinishedAt      *int64 `json:"finished_at,omitempty"`
}

// ProceduralStepRun represents an executed step in a procedural trace.
type ProceduralStepRun struct {
	ID            string `json:"id"`
	TraceID       string `json:"trace_id"`
	NodeID        string `json:"node_id"`
	StepNumber    int    `json:"step_number"`
	InputPayload  string `json:"input_payload,omitempty"`
	OutputPayload string `json:"output_payload,omitempty"`
	Status        string `json:"status"`
	ErrorMessage  string `json:"error_message,omitempty"`
	DurationMs    int64  `json:"duration_ms"`
	ExecutedAt    int64  `json:"executed_at"`
}

// ProceduralNextStep represents a resolved reachable next step with its transition edge and target node.
type ProceduralNextStep struct {
	EdgeID            string         `json:"edge_id"`
	SourceProcedureID string         `json:"source_procedure_id"`
	TargetProcedureID string         `json:"target_procedure_id"`
	RelationType      string         `json:"relation_type"`
	ConditionExpr     string         `json:"condition_expr,omitempty"`
	Ordering          int            `json:"ordering"`
	MappingRules      string         `json:"mapping_rules,omitempty"`
	TargetNode        ProceduralNode `json:"target_node"`
	Depth             int            `json:"depth"`
}

// ProceduralCompensationStep represents a compensation step to rollback side effects in reverse Saga order.
type ProceduralCompensationStep struct {
	ExecutedNodeID     string         `json:"executed_node_id"`
	ExecutedStepNumber int            `json:"executed_step_number"`
	ExecutedOutput     string         `json:"executed_output,omitempty"`
	CompensationNode   ProceduralNode `json:"compensation_node"`
	MappingRules       string         `json:"mapping_rules,omitempty"`
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
	ContextSiloID string    `json:"context_silo_id,omitempty"` // Partitioning ID (e.g. corpus conversation or document silo)
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
	ContextSiloID    string `json:"context_silo_id,omitempty"`
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
    ContextSiloID         string  `json:"context_silo_id,omitempty"` // Partitioning ID (e.g. corpus conversation or document silo)
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
	Procedural                  []ProceduralKnowledge
	SemanticNodes               []SemanticNode
	SemanticLinks               []SemanticLink
	Episodic                    []EpisodicSummary
	Lineage                     []DocumentLineage
	PlannerOutput               string
	ResponseGuidelines          string
	TemporalReasoningGuidelines string
	ConflictWarningPrompt       string
	LineageCitationsPrompt      string
	PDDLDomainPath              string
	PDDLProblemPath             string
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
