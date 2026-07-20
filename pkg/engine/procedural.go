package engine

import (
    "context"
    "fmt"
    "time"

    "github.com/laurentalsina/gllam/pkg/memory"
)

// UpsertProceduralKnowledge inserts or updates a procedural knowledge entry
func (e *GllamEngine) UpsertProceduralKnowledge(ctx context.Context, pk memory.ProceduralKnowledge) error {
    query := `
        INSERT INTO procedural_knowledge (id, task_type, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(task_type) DO UPDATE SET
            instructions = excluded.instructions,
            user_feedback_rules = excluded.user_feedback_rules,
            version = version + 1,
            updated_at = excluded.updated_at`

    _, err := e.db.ExecContext(ctx, query, pk.ID, pk.TaskType, pk.Instructions, pk.UserFeedbackRules, pk.TimesApplied, pk.IsHighlyHelpful, pk.Version, pk.SupersededBy, pk.UpdatedAt)
    if err != nil {
        return fmt.Errorf("failed to upsert procedural knowledge: %w", err)
    }
    return nil
}

// MarkProcedureHelpful toggles the is_highly_helpful flag for a procedure
func (e *GllamEngine) MarkProcedureHelpful(ctx context.Context, taskType string, helpful bool) error {
    query := `UPDATE procedural_knowledge SET is_highly_helpful = ?, updated_at = ? WHERE task_type = ?`
    now := time.Now().Unix()
    _, err := e.db.ExecContext(ctx, query, helpful, now, taskType)
    if err != nil {
        return fmt.Errorf("failed to mark procedure helpful: %w", err)
    }
    return nil
}

// RetrieveProcedure fetches a procedure by task type and increments times_applied
func (e *GllamEngine) RetrieveProcedure(ctx context.Context, taskType string) (*memory.ProceduralKnowledge, error) {
    var pk memory.ProceduralKnowledge
    query := `
        SELECT id, task_type, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at
        FROM procedural_knowledge
        WHERE task_type = ?`

    err := e.db.QueryRowContext(ctx, query, taskType).Scan(
        &pk.ID, &pk.TaskType, &pk.Instructions, &pk.UserFeedbackRules,
        &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, &pk.UpdatedAt)
    if err != nil {
        return nil, fmt.Errorf("failed to retrieve procedure: %w", err)
    }

    // Increment times_applied
    _, err = e.db.ExecContext(ctx, `UPDATE procedural_knowledge SET times_applied = times_applied + 1 WHERE task_type = ?`, taskType)
    if err != nil {
        return nil, fmt.Errorf("failed to increment times_applied: %w", err)
    }

    return &pk, nil
}

// GetTopProcedures retrieves procedures ordered by helpfulness and usage
func (e *GllamEngine) GetTopProcedures(ctx context.Context, limit int) ([]memory.ProceduralKnowledge, error) {
    query := `
        SELECT id, task_type, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at
        FROM procedural_knowledge
        ORDER BY is_highly_helpful DESC, times_applied DESC
        LIMIT ?`

    rows, err := e.db.QueryContext(ctx, query, limit)
    if err != nil {
        return nil, fmt.Errorf("failed to get top procedures: %w", err)
    }
    defer rows.Close()

    var procedures []memory.ProceduralKnowledge
    for rows.Next() {
        var pk memory.ProceduralKnowledge
        if err := rows.Scan(
            &pk.ID, &pk.TaskType, &pk.Instructions, &pk.UserFeedbackRules,
            &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, &pk.UpdatedAt); err != nil {
            return nil, fmt.Errorf("failed to scan procedure: %w", err)
        }
        procedures = append(procedures, pk)
    }

    return procedures, rows.Err()
}
