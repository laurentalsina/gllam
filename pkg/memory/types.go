package memory

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
)

type EpisodicSummary struct {

    ID          string `json:"id"`
    SessionID   string `json:"session_id"`
    SummaryText string `json:"summary_text"`
    CreatedAt   int64  `json:"created_at"`
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
    UpdatedAt         int64  `json:"updated_at"`
}

type SemanticNode struct {
    ID            string `json:"id"`
    Name          string `json:"name"`
    Type          string `json:"type"`
    ContextPrompt string `json:"context_prompt"`
}

type SemanticLink struct {
    SourceID              string  `json:"source_id"`
    TargetID              string  `json:"target_id"`
    Relationship          string  `json:"relationship"`
    Caveats               string  `json:"caveats"`
    ValidFrom             string  `json:"valid_from"`              // Unix timestamp string OR "temporal_note"
    ValidUntil            *string `json:"valid_until"`             // Unix timestamp string OR "temporal_note" OR nil
    TemporalAnchorID      string  `json:"temporal_anchor_id"`     // Grounded node ID reference for relative timing
    TemporalRelation      string  `json:"temporal_relation"`     // Allen Interval Algebra: "before"|"after"|"equals"|...
    TemporalOffsetSeconds int64   `json:"temporal_offset_seconds"`// Relative offset in seconds
    TemporalGranularity   string  `json:"temporal_granularity"`   // "day" (snap 00:00:00) | "hour" | "exact" | "month"
    TemporalNote          string  `json:"temporal_note"`           // Qualitative phrase describing imprecise timestamp
    OriginSourceID        string  `json:"origin_source_id"`       // FK to semantic_nodes(id) for human/agent/system origin
    RuleContext           string  `json:"rule_context"`           // "user_preference" | "session" | "source" | "global"
    ConstraintType        string  `json:"constraint_type"`         // "positive" | "negative"
    UpdatedAt             int64   `json:"updated_at"`
}




type CompiledContext struct {
	Procedural    []ProceduralKnowledge
	SemanticNodes []SemanticNode
	SemanticLinks []SemanticLink
	Episodic      []EpisodicSummary
	PlannerOutput string
}
