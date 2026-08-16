package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


// UpsertProceduralKnowledge inserts or updates a procedural knowledge entry
func (e *GllamEngine) UpsertProceduralKnowledge(ctx context.Context, pk memory.ProceduralKnowledge) error {
    query := `
        INSERT INTO procedural_knowledge (id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(task_type) DO UPDATE SET
            scope = excluded.scope,
            trigger_context = excluded.trigger_context,
            instructions = excluded.instructions,
            user_feedback_rules = excluded.user_feedback_rules,
            version = version + 1,
            updated_at = excluded.updated_at`

    _, err := e.db.ExecContext(ctx, query, pk.ID, pk.TaskType, pk.Scope, pk.TriggerContext, pk.Instructions, pk.UserFeedbackRules, pk.TimesApplied, pk.IsHighlyHelpful, pk.Version, pk.SupersededBy, pk.UpdatedAt)
    if err != nil {
        return fmt.Errorf("failed to upsert procedural knowledge: %w", err)
    }
    return nil
}

// MarkProcedureHelpful toggles the is_highly_helpful flag for a procedure
func (e *GllamEngine) MarkProcedureHelpful(ctx context.Context, taskType string, helpful bool) error {
    query := `UPDATE procedural_knowledge SET is_highly_helpful = ?, updated_at = ? WHERE task_type = ?`
    now := time.Now()
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
        SELECT id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at
        FROM procedural_knowledge
        WHERE task_type = ?`

    err := e.db.QueryRowContext(ctx, query, taskType).Scan(
        &pk.ID, &pk.TaskType, &pk.Scope, &pk.TriggerContext, &pk.Instructions, &pk.UserFeedbackRules,
        &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, scanTime(&pk.UpdatedAt))
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

// GetTopProcedures retrieves procedures ordered by helpfulness and usage (read-only → dbRO)
func (e *GllamEngine) GetTopProcedures(ctx context.Context, limit int) ([]memory.ProceduralKnowledge, error) {
    query := `
        SELECT id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at
        FROM procedural_knowledge
        WHERE scope = 'external'
        ORDER BY is_highly_helpful DESC, times_applied DESC
        LIMIT ?`

    rows, err := e.dbRO.QueryContext(ctx, query, limit)
    if err != nil {
        return nil, fmt.Errorf("failed to get top procedures: %w", err)
    }
    defer rows.Close()

    var procedures []memory.ProceduralKnowledge
    for rows.Next() {
        var pk memory.ProceduralKnowledge
        if err := rows.Scan(
            &pk.ID, &pk.TaskType, &pk.Scope, &pk.TriggerContext, &pk.Instructions, &pk.UserFeedbackRules,
            &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, scanTime(&pk.UpdatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan procedure: %w", err)
        }
        procedures = append(procedures, pk)
    }

    return procedures, rows.Err()
}

// StoreProcedureEmbedding generates and stores an embedding vector for a procedural knowledge entry.
func (e *GllamEngine) StoreProcedureEmbedding(ctx context.Context, id string) error {
    if e.embedder == nil {
        return fmt.Errorf("no embedder configured")
    }

    var taskType, instructions string
    err := e.db.QueryRowContext(ctx, "SELECT task_type, instructions FROM procedural_knowledge WHERE id = ?", id).Scan(&taskType, &instructions)
    if err != nil {
        return fmt.Errorf("failed to fetch procedure %s: %w", id, err)
    }

    textToEmbed := fmt.Sprintf("Task: %s\n%s", taskType, instructions)
    embedding, err := e.embedder.Embed(ctx, textToEmbed)
    if err != nil {
        return fmt.Errorf("failed to generate embedding for %s: %w", id, err)
    }

    embeddingBlob, err := serializeEmbedding(embedding)
    if err != nil {
        return fmt.Errorf("failed to serialize embedding: %w", err)
    }

    _, err = e.db.ExecContext(ctx, "DELETE FROM procedural_embeddings WHERE id = ?", id)
    if err != nil {
        return fmt.Errorf("failed to delete old procedural embedding: %w", err)
    }

    _, err = e.db.ExecContext(ctx, `
        INSERT INTO procedural_embeddings (id, embedding)
        VALUES (?, vec_f32(?))
    `, id, embeddingBlob)
    if err != nil {
        return fmt.Errorf("failed to store embedding for procedure %s: %w", id, err)
    }
    return nil
}

// SearchSimilarProcedures finds procedures with similar embeddings to the given query text.
func (e *GllamEngine) SearchSimilarProcedures(ctx context.Context, queryText string, limit int) ([]memory.ProceduralKnowledge, error) {
    if e.embedder == nil {
        return nil, fmt.Errorf("no embedder configured")
    }

    queryEmbedding, err := e.embedder.Embed(ctx, queryText)
    if err != nil {
        return nil, fmt.Errorf("failed to generate query embedding: %w", err)
    }

    queryBlob, err := serializeEmbedding(queryEmbedding)
    if err != nil {
        return nil, fmt.Errorf("failed to serialize query embedding: %w", err)
    }

    query := `
        SELECT pk.id, pk.task_type, pk.scope, pk.trigger_context, pk.instructions, pk.user_feedback_rules, pk.times_applied, pk.is_highly_helpful, pk.version, pk.superseded_by, pk.updated_at
        FROM (
            SELECT id, distance
            FROM procedural_embeddings
            WHERE embedding MATCH vec_f32(?) AND k = ?
        ) pe
        JOIN procedural_knowledge pk ON +pe.id = pk.id
        WHERE pk.scope = 'external'
        ORDER BY pe.distance`

    rows, err := e.dbRO.QueryContext(ctx, query, queryBlob, limit)
    if err != nil {
        return nil, fmt.Errorf("failed to search similar procedures: %w", err)
    }
    defer rows.Close()

    var procedures []memory.ProceduralKnowledge
    for rows.Next() {
        var pk memory.ProceduralKnowledge
        if err := rows.Scan(
            &pk.ID, &pk.TaskType, &pk.Scope, &pk.TriggerContext, &pk.Instructions, &pk.UserFeedbackRules,
            &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, scanTime(&pk.UpdatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan procedure: %w", err)
        }
        procedures = append(procedures, pk)
    }

    return procedures, rows.Err()
}

// GetInternalProceduresByTrigger retrieves cognitive procedures for a specific internal scope and trigger context
func (e *GllamEngine) GetInternalProceduresByTrigger(ctx context.Context, scope string, triggerContext string) ([]memory.ProceduralKnowledge, error) {
    query := `
        SELECT id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, updated_at
        FROM procedural_knowledge
        WHERE scope = ? AND trigger_context = ?
        ORDER BY is_highly_helpful DESC, times_applied DESC`

    rows, err := e.dbRO.QueryContext(ctx, query, scope, triggerContext)
    if err != nil {
        return nil, fmt.Errorf("failed to get internal procedures: %w", err)
    }
    defer rows.Close()

    var procedures []memory.ProceduralKnowledge
    for rows.Next() {
        var pk memory.ProceduralKnowledge
        var tc sql.NullString
        if err := rows.Scan(
            &pk.ID, &pk.TaskType, &pk.Scope, &tc, &pk.Instructions, &pk.UserFeedbackRules,
            &pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, scanTime(&pk.UpdatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan internal procedure: %w", err)
        }
        if tc.Valid {
            pk.TriggerContext = tc.String
        }
        procedures = append(procedures, pk)
    }

    return procedures, rows.Err()
}

// GetProceduresByTaxonomyPrefix retrieves procedural knowledge bound to a specific taxonomy domain path prefix.
func (e *GllamEngine) GetProceduresByTaxonomyPrefix(ctx context.Context, taxonomyPrefix string) ([]memory.ProceduralKnowledge, error) {
	cleanPrefix := "/" + strings.Trim(taxonomyPrefix, "/")
	pattern := cleanPrefix + "%"

	query := `
		SELECT pk.id, pk.task_type, pk.scope, pk.trigger_context, pk.instructions, pk.user_feedback_rules, pk.times_applied, pk.is_highly_helpful, pk.version, pk.superseded_by, pk.updated_at
		FROM procedural_knowledge pk
		JOIN semantic_nodes sn ON sn.name = pk.task_type OR sn.id = pk.id
		WHERE sn.taxonomy_path LIKE ? OR sn.taxonomy_path = ?
		ORDER BY pk.is_highly_helpful DESC, pk.times_applied DESC`

	rows, err := e.dbRO.QueryContext(ctx, query, pattern, cleanPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to query procedures by taxonomy prefix %s: %w", cleanPrefix, err)
	}
	defer rows.Close()

	var procedures []memory.ProceduralKnowledge
	for rows.Next() {
		var pk memory.ProceduralKnowledge
		var tc sql.NullString
		if err := rows.Scan(
			&pk.ID, &pk.TaskType, &pk.Scope, &tc, &pk.Instructions, &pk.UserFeedbackRules,
			&pk.TimesApplied, &pk.IsHighlyHelpful, &pk.Version, &pk.SupersededBy, scanTime(&pk.UpdatedAt)); err != nil {
			return nil, fmt.Errorf("failed to scan procedure: %w", err)
		}
		if tc.Valid {
			pk.TriggerContext = tc.String
		}
		procedures = append(procedures, pk)
	}

	return procedures, rows.Err()
}
