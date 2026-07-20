package memory

type EpisodicSummary struct {
    ID          string `json:"id"`
    SessionID   string `json:"session_id"`
    SummaryText string `json:"summary_text"`
    CreatedAt   int64  `json:"created_at"`
}

type ProceduralKnowledge struct {
    ID                string `json:"id"`
    TaskType          string `json:"task_type"`
    Instructions      string `json:"instructions"`
    UserFeedbackRules string `json:"user_feedback_rules"`
    TimesApplied      int    `json:"times_applied"`
    IsHighlyHelpful   bool   `json:"is_highly_helpful"`
    Version           int    `json:"version"`
    SupersededBy      string `json:"superseded_by"`
    UpdatedAt         int64  `json:"updated_at"`
}

type SemanticNode struct {
    ID   string `json:"id"`
    Name string `json:"name"`
    Type string `json:"type"`
}

type SemanticLink struct {
    SourceID     string `json:"source_id"`
    TargetID     string `json:"target_id"`
    Relationship string `json:"relationship"`
    Caveats      string `json:"caveats"`
    ValidFrom    int64  `json:"valid_from"`
    ValidUntil   *int64 `json:"valid_until"` // nil means currently active
    UpdatedAt    int64  `json:"updated_at"`
}

type Contradiction struct {
    ID                 string  `json:"id"`
    Link1SourceID      string  `json:"link1_source_id"`
    Link1TargetID      string  `json:"link1_target_id"`
    Link1Relationship  string  `json:"link1_relationship"`
    Link2SourceID      string  `json:"link2_source_id"`
    Link2TargetID      string  `json:"link2_target_id"`
    Link2Relationship  string  `json:"link2_relationship"`
    DetectedAt         int64   `json:"detected_at"`
    Resolved           bool    `json:"resolved"`
    ResolvedAt         *int64  `json:"resolved_at"`
    ResolutionNotes    string  `json:"resolution_notes"`
}

type CompiledContext struct {
    Procedural     []ProceduralKnowledge
    Semantic       []SemanticLink
    Episodic       []EpisodicSummary
    Contradictions []Contradiction
    HasConflicts   bool
    GrillingPrompt string
}
