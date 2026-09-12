package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


// UpsertProceduralKnowledge inserts or updates a procedural knowledge entry
func (e *GllamEngine) UpsertProceduralKnowledge(ctx context.Context, pk memory.ProceduralKnowledge) error {
    now := time.Now().UTC().Format(time.RFC3339)
    createdTime := now
    if !pk.CreatedAt.IsZero() {
        createdTime = pk.CreatedAt.UTC().Format(time.RFC3339)
    }

    query := `
        INSERT INTO procedural_knowledge (id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, superseded_by, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(task_type) DO UPDATE SET
            scope = excluded.scope,
            trigger_context = excluded.trigger_context,
            instructions = excluded.instructions,
            user_feedback_rules = excluded.user_feedback_rules,
            version = version + 1,
            updated_at = excluded.updated_at`

    _, err := e.db.ExecContext(ctx, query, pk.ID, pk.TaskType, pk.Scope, pk.TriggerContext, pk.Instructions, pk.UserFeedbackRules, pk.TimesApplied, pk.IsHighlyHelpful, pk.Version, pk.SupersededBy, createdTime, now)
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

// ============================================================================
// GRAPH-BASED PROCEDURAL MEMORY ENGINE (Nodes, Links, Traces & Step Runs)
// ============================================================================

func nullIfEmpty(s string) sql.NullString {
	if strings.TrimSpace(s) == "" {
		return sql.NullString{Valid: false}
	}
	return sql.NullString{String: s, Valid: true}
}

// UpsertProceduralNode creates or updates a discrete procedural node.
func (e *GllamEngine) UpsertProceduralNode(ctx context.Context, node memory.ProceduralNode) error {
	now := time.Now().Unix()
	createdAt := node.CreatedAt
	if createdAt == 0 {
		createdAt = now
	}
	updatedAt := now
	metadata := node.Metadata
	if metadata == "" {
		metadata = "{}"
	}

	query := `
		INSERT INTO procedural_nodes (id, name, description, action_type, input_schema, output_schema, is_idempotent, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			action_type = excluded.action_type,
			input_schema = excluded.input_schema,
			output_schema = excluded.output_schema,
			is_idempotent = excluded.is_idempotent,
			metadata = excluded.metadata,
			updated_at = excluded.updated_at`

	isIdempotentInt := 0
	if node.IsIdempotent {
		isIdempotentInt = 1
	}

	_, err := e.db.ExecContext(ctx, query,
		node.ID, node.Name, node.Description, node.ActionType,
		nullIfEmpty(node.InputSchema), nullIfEmpty(node.OutputSchema),
		isIdempotentInt, metadata, createdAt, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert procedural node %s: %w", node.ID, err)
	}
	return nil
}

// GetProceduralNode fetches a procedural node by its primary ID.
func (e *GllamEngine) GetProceduralNode(ctx context.Context, id string) (*memory.ProceduralNode, error) {
	query := `
		SELECT id, name, description, action_type, COALESCE(input_schema, ''), COALESCE(output_schema, ''), is_idempotent, COALESCE(metadata, '{}'), created_at, updated_at
		FROM procedural_nodes
		WHERE id = ?`

	var node memory.ProceduralNode
	var isIdempotentInt int
	err := e.dbRO.QueryRowContext(ctx, query, id).Scan(
		&node.ID, &node.Name, &node.Description, &node.ActionType,
		&node.InputSchema, &node.OutputSchema, &isIdempotentInt,
		&node.Metadata, &node.CreatedAt, &node.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get procedural node %s: %w", id, err)
	}
	node.IsIdempotent = (isIdempotentInt == 1)
	return &node, nil
}

// DeleteProceduralNode removes a procedural node and cascades across foreign keys.
func (e *GllamEngine) DeleteProceduralNode(ctx context.Context, id string) error {
	_, err := e.db.ExecContext(ctx, "DELETE FROM procedural_nodes WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete procedural node %s: %w", id, err)
	}
	return nil
}

// ListProceduralNodes retrieves all registered procedural nodes.
func (e *GllamEngine) ListProceduralNodes(ctx context.Context) ([]memory.ProceduralNode, error) {
	query := `
		SELECT id, name, description, action_type, COALESCE(input_schema, ''), COALESCE(output_schema, ''), is_idempotent, COALESCE(metadata, '{}'), created_at, updated_at
		FROM procedural_nodes
		ORDER BY name ASC`

	rows, err := e.dbRO.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list procedural nodes: %w", err)
	}
	defer rows.Close()

	var nodes []memory.ProceduralNode
	for rows.Next() {
		var n memory.ProceduralNode
		var isIdempotentInt int
		if err := rows.Scan(
			&n.ID, &n.Name, &n.Description, &n.ActionType,
			&n.InputSchema, &n.OutputSchema, &isIdempotentInt,
			&n.Metadata, &n.CreatedAt, &n.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan procedural node: %w", err)
		}
		n.IsIdempotent = (isIdempotentInt == 1)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// UpsertProceduralLink inserts or updates a directed transition edge between procedural nodes.
func (e *GllamEngine) UpsertProceduralLink(ctx context.Context, link memory.ProceduralLink) error {
	now := time.Now().Unix()
	createdAt := link.CreatedAt
	if createdAt == 0 {
		createdAt = now
	}
	metadata := link.Metadata
	if metadata == "" {
		metadata = "{}"
	}
	weight := link.Weight
	if weight == 0 {
		weight = 1.0
	}

	query := `
		INSERT INTO procedural_links (id, source_procedure_id, target_procedure_id, relation_type, condition_expr, weight, ordering, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			source_procedure_id = excluded.source_procedure_id,
			target_procedure_id = excluded.target_procedure_id,
			relation_type = excluded.relation_type,
			condition_expr = excluded.condition_expr,
			weight = excluded.weight,
			ordering = excluded.ordering,
			metadata = excluded.metadata`

	_, err := e.db.ExecContext(ctx, query,
		link.ID, link.SourceProcedureID, link.TargetProcedureID, link.RelationType,
		nullIfEmpty(link.ConditionExpr), weight, link.Ordering, metadata, createdAt,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert procedural link %s: %w", link.ID, err)
	}
	return nil
}

// DeleteProceduralLink deletes a link by ID.
func (e *GllamEngine) DeleteProceduralLink(ctx context.Context, id string) error {
	_, err := e.db.ExecContext(ctx, "DELETE FROM procedural_links WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete procedural link %s: %w", id, err)
	}
	return nil
}

// GetProceduralLinksForNode returns all incoming or outgoing links for a given procedure node.
func (e *GllamEngine) GetProceduralLinksForNode(ctx context.Context, nodeID string) ([]memory.ProceduralLink, error) {
	query := `
		SELECT id, source_procedure_id, target_procedure_id, relation_type, COALESCE(condition_expr, ''), weight, ordering, COALESCE(metadata, '{}'), created_at
		FROM procedural_links
		WHERE source_procedure_id = ? OR target_procedure_id = ?
		ORDER BY ordering ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, nodeID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get procedural links for node: %w", err)
	}
	defer rows.Close()

	var links []memory.ProceduralLink
	for rows.Next() {
		var l memory.ProceduralLink
		if err := rows.Scan(
			&l.ID, &l.SourceProcedureID, &l.TargetProcedureID, &l.RelationType,
			&l.ConditionExpr, &l.Weight, &l.Ordering, &l.Metadata, &l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan procedural link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// StartProceduralTrace initializes a new runtime trace instance for executing a root procedure workflow.
func (e *GllamEngine) StartProceduralTrace(ctx context.Context, rootProcedureID string, initialContext map[string]interface{}) (*memory.ProceduralExecutionTrace, error) {
	// Verify root exists
	if _, err := e.GetProceduralNode(ctx, rootProcedureID); err != nil {
		return nil, fmt.Errorf("invalid root procedure %s: %w", rootProcedureID, err)
	}

	traceID := fmt.Sprintf("trace_%d_%s", time.Now().UnixNano(), rootProcedureID)
	now := time.Now().Unix()

	contextBytes, err := json.Marshal(initialContext)
	if err != nil || len(initialContext) == 0 {
		contextBytes = []byte("{}")
	}

	trace := memory.ProceduralExecutionTrace{
		ID:              traceID,
		RootProcedureID: rootProcedureID,
		CurrentNodeID:   rootProcedureID,
		Status:          memory.ProceduralStatusInProgress,
		ContextState:    string(contextBytes),
		StartedAt:       now,
	}

	query := `
		INSERT INTO procedural_execution_traces (id, root_procedure_id, current_node_id, status, context_state, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`

	_, err = e.db.ExecContext(ctx, query, trace.ID, trace.RootProcedureID, trace.CurrentNodeID, trace.Status, trace.ContextState, trace.StartedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to start procedural trace: %w", err)
	}
	return &trace, nil
}

// GetProceduralTrace fetches an execution trace by ID.
func (e *GllamEngine) GetProceduralTrace(ctx context.Context, traceID string) (*memory.ProceduralExecutionTrace, error) {
	query := `
		SELECT id, root_procedure_id, COALESCE(current_node_id, ''), status, context_state, COALESCE(error_details, ''), started_at, finished_at
		FROM procedural_execution_traces
		WHERE id = ?`

	var trace memory.ProceduralExecutionTrace
	var currentNodeID, errorDetails sql.NullString
	var finishedAt sql.NullInt64

	err := e.dbRO.QueryRowContext(ctx, query, traceID).Scan(
		&trace.ID, &trace.RootProcedureID, &currentNodeID, &trace.Status, &trace.ContextState,
		&errorDetails, &trace.StartedAt, &finishedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get procedural trace %s: %w", traceID, err)
	}
	if currentNodeID.Valid {
		trace.CurrentNodeID = currentNodeID.String
	}
	if errorDetails.Valid {
		trace.ErrorDetails = errorDetails.String
	}
	if finishedAt.Valid {
		val := finishedAt.Int64
		trace.FinishedAt = &val
	}
	return &trace, nil
}

// UpdateProceduralTraceStatus explicitly sets the status or error details of an execution trace.
func (e *GllamEngine) UpdateProceduralTraceStatus(ctx context.Context, traceID string, status string, errorDetails string) error {
	now := time.Now().Unix()
	var finishedAt sql.NullInt64
	if status == memory.ProceduralStatusCompleted || status == memory.ProceduralStatusFailed {
		finishedAt = sql.NullInt64{Int64: now, Valid: true}
	}

	query := `
		UPDATE procedural_execution_traces
		SET status = ?, error_details = ?, finished_at = ?
		WHERE id = ?`

	_, err := e.db.ExecContext(ctx, query, status, nullIfEmpty(errorDetails), finishedAt, traceID)
	if err != nil {
		return fmt.Errorf("failed to update trace status %s: %w", traceID, err)
	}
	return nil
}

// resolveContextValue extracts a nested property from a JSON context map using dot notation or JSONPath syntax.
func resolveContextValue(path string, ctx map[string]interface{}) (interface{}, bool) {
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimSpace(path)
	if path == "" {
		return ctx, true
	}
	parts := strings.Split(path, ".")
	var curr interface{} = ctx
	for _, part := range parts {
		m, ok := curr.(map[string]interface{})
		if !ok {
			return nil, false
		}
		curr, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return curr, true
}

// EvaluateCondition evaluates an edge condition_expr string against a runtime context map.
func EvaluateCondition(conditionExpr string, contextMap map[string]interface{}) (bool, error) {
	conditionExpr = strings.TrimSpace(conditionExpr)
	if conditionExpr == "" {
		return true, nil
	}

	// Support compound conjunctions (expr1 && expr2)
	if strings.Contains(conditionExpr, "&&") {
		parts := strings.Split(conditionExpr, "&&")
		for _, part := range parts {
			ok, err := EvaluateCondition(part, contextMap)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}

	// Support compound disjunctions (expr1 || expr2)
	if strings.Contains(conditionExpr, "||") {
		parts := strings.Split(conditionExpr, "||")
		for _, part := range parts {
			ok, err := EvaluateCondition(part, contextMap)
			if err == nil && ok {
				return true, nil
			}
		}
		return false, nil
	}

	// Operators to check in order
	operators := []string{"==", "!=", ">=", "<=", ">", "<"}
	var op string
	var leftPath, rightStr string
	for _, candidate := range operators {
		if idx := strings.Index(conditionExpr, candidate); idx != -1 {
			op = candidate
			leftPath = strings.TrimSpace(conditionExpr[:idx])
			rightStr = strings.TrimSpace(conditionExpr[idx+len(candidate):])
			break
		}
	}

	if op != "" {
		leftVal, found := resolveContextValue(leftPath, contextMap)
		// Clean quotes from right value if string
		cleanRight := rightStr
		isQuoted := false
		if (strings.HasPrefix(rightStr, "'") && strings.HasSuffix(rightStr, "'")) ||
			(strings.HasPrefix(rightStr, "\"") && strings.HasSuffix(rightStr, "\"")) {
			if len(rightStr) >= 2 {
				cleanRight = rightStr[1 : len(rightStr)-1]
				isQuoted = true
			}
		}

		if !found {
			if cleanRight == "null" || cleanRight == "nil" {
				if op == "==" {
					return true, nil
				}
				if op == "!=" {
					return false, nil
				}
			}
			if op == "!=" {
				return true, nil
			}
			return false, nil
		}

		if cleanRight == "null" || cleanRight == "nil" {
			if op == "==" {
				return leftVal == nil, nil
			}
			if op == "!=" {
				return leftVal != nil, nil
			}
			return false, nil
		}

		// Boolean comparison
		if cleanRight == "true" || cleanRight == "false" {
			targetBool := (cleanRight == "true")
			leftBool, ok := leftVal.(bool)
			if !ok {
				leftBool = fmt.Sprintf("%v", leftVal) == "true"
			}
			if op == "==" {
				return leftBool == targetBool, nil
			}
			if op == "!=" {
				return leftBool != targetBool, nil
			}
			return false, nil
		}

		// Numeric comparison if not quoted
		if !isQuoted {
			if targetNum, err := strconv.ParseFloat(cleanRight, 64); err == nil {
				var leftNum float64
				switch v := leftVal.(type) {
				case float64:
					leftNum = v
				case float32:
					leftNum = float64(v)
				case int:
					leftNum = float64(v)
				case int64:
					leftNum = float64(v)
				case string:
					if parsed, err := strconv.ParseFloat(v, 64); err == nil {
						leftNum = parsed
					}
				default:
					leftNum = 0
				}
				switch op {
				case "==":
					return leftNum == targetNum, nil
				case "!=":
					return leftNum != targetNum, nil
				case ">":
					return leftNum > targetNum, nil
				case ">=":
					return leftNum >= targetNum, nil
				case "<":
					return leftNum < targetNum, nil
				case "<=":
					return leftNum <= targetNum, nil
				}
			}
		}

		// String comparison fallback
		leftStr := fmt.Sprintf("%v", leftVal)
		switch op {
		case "==":
			return leftStr == cleanRight, nil
		case "!=":
			return leftStr != cleanRight, nil
		}
		return false, nil
	}

	// Unary truthiness test (e.g. "flag" or "!flag")
	isNegated := strings.HasPrefix(conditionExpr, "!")
	fieldPath := strings.TrimPrefix(conditionExpr, "!")
	val, found := resolveContextValue(fieldPath, contextMap)
	if !found || val == nil {
		if isNegated {
			return true, nil
		}
		return false, nil
	}

	isTruthy := false
	switch v := val.(type) {
	case bool:
		isTruthy = v
	case string:
		isTruthy = (v != "" && v != "false" && v != "0")
	case float64:
		isTruthy = (v != 0)
	case int:
		isTruthy = (v != 0)
	default:
		isTruthy = true
	}

	if isNegated {
		return !isTruthy, nil
	}
	return isTruthy, nil
}

// queryReachableSteps executes the Recursive CTE to resolve downstream next steps and decompose subprocedures.
func (e *GllamEngine) queryReachableSteps(ctx context.Context, sourceID string, relTypeFilter string) ([]memory.ProceduralNextStep, error) {
	query := `
		WITH RECURSIVE next_steps AS (
			SELECT 
				e.id AS edge_id,
				e.source_procedure_id,
				e.target_procedure_id,
				e.relation_type,
				e.condition_expr,
				e.ordering,
				e.metadata AS mapping_rules,
				p.id AS target_id,
				p.name AS target_name,
				p.description AS target_description,
				p.action_type AS target_action_type,
				COALESCE(p.input_schema, '') AS target_input_schema,
				COALESCE(p.output_schema, '') AS target_output_schema,
				p.is_idempotent AS target_is_idempotent,
				COALESCE(p.metadata, '{}') AS target_metadata,
				p.created_at AS target_created_at,
				p.updated_at AS target_updated_at,
				1 AS depth
			FROM procedural_links e
			JOIN procedural_nodes p ON e.target_procedure_id = p.id
			WHERE e.source_procedure_id = ?
			  AND (
				  (? = 'subprocedure' AND e.relation_type = 'subprocedure' AND e.ordering = 0)
				  OR (? != 'subprocedure' AND e.relation_type IN ('next', 'conditional_branch'))
			  )

			UNION ALL

			SELECT 
				child_e.id,
				child_e.source_procedure_id,
				child_e.target_procedure_id,
				child_e.relation_type,
				child_e.condition_expr,
				child_e.ordering,
				child_e.metadata,
				p2.id,
				p2.name,
				p2.description,
				p2.action_type,
				COALESCE(p2.input_schema, ''),
				COALESCE(p2.output_schema, ''),
				p2.is_idempotent,
				COALESCE(p2.metadata, '{}'),
				p2.created_at,
				p2.updated_at,
				ns.depth + 1
			FROM procedural_links child_e
			JOIN next_steps ns ON child_e.source_procedure_id = ns.target_id
			JOIN procedural_nodes p2 ON child_e.target_procedure_id = p2.id
			WHERE ns.target_action_type = 'composite'
			  AND child_e.relation_type = 'subprocedure'
			  AND child_e.ordering = 0
		)
		SELECT edge_id, source_procedure_id, target_procedure_id, relation_type,
		       COALESCE(condition_expr, ''), ordering, COALESCE(mapping_rules, '{}'),
		       target_id, target_name, target_description, target_action_type,
		       target_input_schema, target_output_schema, target_is_idempotent,
		       target_metadata, target_created_at, target_updated_at, depth
		FROM next_steps
		ORDER BY depth ASC, ordering ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, sourceID, relTypeFilter, relTypeFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query reachable steps from %s: %w", sourceID, err)
	}
	defer rows.Close()

	var steps []memory.ProceduralNextStep
	for rows.Next() {
		var s memory.ProceduralNextStep
		var isIdempotentInt int
		if err := rows.Scan(
			&s.EdgeID, &s.SourceProcedureID, &s.TargetProcedureID, &s.RelationType,
			&s.ConditionExpr, &s.Ordering, &s.MappingRules,
			&s.TargetNode.ID, &s.TargetNode.Name, &s.TargetNode.Description, &s.TargetNode.ActionType,
			&s.TargetNode.InputSchema, &s.TargetNode.OutputSchema, &isIdempotentInt,
			&s.TargetNode.Metadata, &s.TargetNode.CreatedAt, &s.TargetNode.UpdatedAt, &s.Depth,
		); err != nil {
			return nil, fmt.Errorf("failed to scan reachable step: %w", err)
		}
		s.TargetNode.IsIdempotent = (isIdempotentInt == 1)
		steps = append(steps, s)
	}
	return steps, rows.Err()
}

func filterRunnableSteps(steps []memory.ProceduralNextStep) []memory.ProceduralNextStep {
	hasLeaves := false
	for _, s := range steps {
		if s.TargetNode.ActionType != memory.ProceduralActionComposite {
			hasLeaves = true
			break
		}
	}
	if !hasLeaves {
		return steps
	}
	var runnable []memory.ProceduralNextStep
	for _, s := range steps {
		if s.TargetNode.ActionType != memory.ProceduralActionComposite {
			runnable = append(runnable, s)
		}
	}
	return runnable
}

func (e *GllamEngine) findParentComposite(ctx context.Context, childNodeID string) (string, bool, error) {
	var parentID string
	err := e.dbRO.QueryRowContext(ctx, `
		SELECT source_procedure_id
		FROM procedural_links
		WHERE target_procedure_id = ? AND relation_type = 'subprocedure'
		LIMIT 1`, childNodeID).Scan(&parentID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return parentID, true, nil
}

// ResolveNextSteps determines all valid reachable next steps for an active trace, evaluating transitions and condition rules.
func (e *GllamEngine) ResolveNextSteps(ctx context.Context, traceID string) ([]memory.ProceduralNextStep, error) {
	trace, err := e.GetProceduralTrace(ctx, traceID)
	if err != nil {
		return nil, fmt.Errorf("trace not found: %w", err)
	}

	if trace.Status == memory.ProceduralStatusCompleted || trace.Status == memory.ProceduralStatusFailed {
		return nil, nil
	}

	var contextMap map[string]interface{}
	if err := json.Unmarshal([]byte(trace.ContextState), &contextMap); err != nil {
		contextMap = make(map[string]interface{})
	}

	var stepRunCount int
	err = e.dbRO.QueryRowContext(ctx, "SELECT count(*) FROM procedural_step_runs WHERE trace_id = ?", traceID).Scan(&stepRunCount)
	if err != nil {
		return nil, fmt.Errorf("failed to count step runs: %w", err)
	}

	var candidateSteps []memory.ProceduralNextStep

	if stepRunCount == 0 {
		// Starting execution: if root is composite, drill down into its entry subprocedure
		rootNode, err := e.GetProceduralNode(ctx, trace.RootProcedureID)
		if err != nil {
			return nil, fmt.Errorf("root procedure node not found: %w", err)
		}

		if rootNode.ActionType == memory.ProceduralActionComposite {
			rawSteps, err := e.queryReachableSteps(ctx, rootNode.ID, "subprocedure")
			if err != nil {
				return nil, err
			}
			candidateSteps = filterRunnableSteps(rawSteps)
		} else {
			// Root itself is an atomic action to execute
			candidateSteps = []memory.ProceduralNextStep{
				{
					EdgeID:            "",
					SourceProcedureID: "",
					TargetProcedureID: rootNode.ID,
					RelationType:      "start",
					Ordering:          0,
					TargetNode:        *rootNode,
					Depth:             0,
				},
			}
		}
	} else {
		// Intermediate step: search transitions starting from current node
		searchNodeID := trace.CurrentNodeID
		for searchNodeID != "" {
			rawSteps, err := e.queryReachableSteps(ctx, searchNodeID, "next")
			if err != nil {
				return nil, err
			}
			runnable := filterRunnableSteps(rawSteps)
			if len(runnable) > 0 {
				candidateSteps = runnable
				break
			}

			// If current step has no direct next links, check if it's nested inside a composite parent
			parentID, found, pErr := e.findParentComposite(ctx, searchNodeID)
			if pErr != nil {
				return nil, pErr
			}
			if !found {
				break
			}
			searchNodeID = parentID
		}
	}

	// Filter by condition expressions
	var validSteps []memory.ProceduralNextStep
	for _, step := range candidateSteps {
		if step.ConditionExpr != "" {
			matched, err := EvaluateCondition(step.ConditionExpr, contextMap)
			if err != nil || !matched {
				continue
			}
		}
		validSteps = append(validSteps, step)
	}

	return validSteps, nil
}

// RecordStepExecution records an executed step in procedural_step_runs and updates the trace state.
func (e *GllamEngine) RecordStepExecution(ctx context.Context, traceID string, nodeID string, inputPayload, outputPayload map[string]interface{}, status string, errorMsg string, durationMs int64) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// 1. Get next monotonic step number
	var nextStepNum int
	err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(step_number), 0) + 1 FROM procedural_step_runs WHERE trace_id = ?", traceID).Scan(&nextStepNum)
	if err != nil {
		return fmt.Errorf("failed to get next step number: %w", err)
	}

	inputBytes, _ := json.Marshal(inputPayload)
	outputBytes, _ := json.Marshal(outputPayload)
	if len(inputPayload) == 0 {
		inputBytes = []byte("{}")
	}
	if len(outputPayload) == 0 {
		outputBytes = []byte("{}")
	}

	now := time.Now().Unix()
	stepRunID := fmt.Sprintf("step_%s_%d", traceID, nextStepNum)

	insertStepQuery := `
		INSERT INTO procedural_step_runs (id, trace_id, node_id, step_number, input_payload, output_payload, status, error_message, duration_ms, executed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = tx.ExecContext(ctx, insertStepQuery,
		stepRunID, traceID, nodeID, nextStepNum,
		string(inputBytes), string(outputBytes), status,
		nullIfEmpty(errorMsg), durationMs, now,
	)
	if err != nil {
		return fmt.Errorf("failed to insert step run: %w", err)
	}

	// Fetch node to see if terminal
	var actionType string
	_ = tx.QueryRowContext(ctx, "SELECT action_type FROM procedural_nodes WHERE id = ?", nodeID).Scan(&actionType)

	// Fetch trace context
	var currentContextStr string
	err = tx.QueryRowContext(ctx, "SELECT context_state FROM procedural_execution_traces WHERE id = ?", traceID).Scan(&currentContextStr)
	if err != nil {
		return fmt.Errorf("failed to fetch trace context state: %w", err)
	}

	var currentContext map[string]interface{}
	if err := json.Unmarshal([]byte(currentContextStr), &currentContext); err != nil {
		currentContext = make(map[string]interface{})
	}

	// Merge output payload into context state
	for k, v := range outputPayload {
		currentContext[k] = v
	}
	mergedContextBytes, _ := json.Marshal(currentContext)

	var newStatus string
	var finishedAt sql.NullInt64
	var errDetails sql.NullString

	if status == memory.ProceduralStepFailed {
		newStatus = memory.ProceduralStatusFailed
		errDetails = sql.NullString{String: errorMsg, Valid: errorMsg != ""}
		finishedAt = sql.NullInt64{Int64: now, Valid: true}
	} else if actionType == memory.ProceduralActionTerminal {
		newStatus = memory.ProceduralStatusCompleted
		finishedAt = sql.NullInt64{Int64: now, Valid: true}
	} else {
		newStatus = memory.ProceduralStatusInProgress
	}

	updateTraceQuery := `
		UPDATE procedural_execution_traces
		SET current_node_id = ?,
		    status = ?,
		    context_state = ?,
		    error_details = ?,
		    finished_at = ?
		WHERE id = ?`

	_, err = tx.ExecContext(ctx, updateTraceQuery,
		nodeID, newStatus, string(mergedContextBytes), errDetails, finishedAt, traceID,
	)
	if err != nil {
		return fmt.Errorf("failed to update procedural execution trace: %w", err)
	}

	return tx.Commit()
}

// GetStepRunHistory returns the chronological execution history of all steps in a trace.
func (e *GllamEngine) GetStepRunHistory(ctx context.Context, traceID string) ([]memory.ProceduralStepRun, error) {
	query := `
		SELECT id, trace_id, node_id, step_number, input_payload, output_payload, status, COALESCE(error_message, ''), duration_ms, executed_at
		FROM procedural_step_runs
		WHERE trace_id = ?
		ORDER BY step_number ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, traceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get step run history: %w", err)
	}
	defer rows.Close()

	var history []memory.ProceduralStepRun
	for rows.Next() {
		var sr memory.ProceduralStepRun
		if err := rows.Scan(
			&sr.ID, &sr.TraceID, &sr.NodeID, &sr.StepNumber,
			&sr.InputPayload, &sr.OutputPayload, &sr.Status,
			&sr.ErrorMessage, &sr.DurationMs, &sr.ExecutedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan step run: %w", err)
		}
		history = append(history, sr)
	}
	return history, rows.Err()
}

// FindStepOutputInTrace inspects the trace history to see if a given step was already executed successfully (memoization).
func (e *GllamEngine) FindStepOutputInTrace(ctx context.Context, traceID string, nodeID string) (map[string]interface{}, bool, error) {
	var rawOutput string
	err := e.dbRO.QueryRowContext(ctx, `
		SELECT output_payload
		FROM procedural_step_runs
		WHERE trace_id = ? AND node_id = ? AND status = 'success'
		ORDER BY step_number DESC
		LIMIT 1`, traceID, nodeID).Scan(&rawOutput)

	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to find step output: %w", err)
	}

	var output map[string]interface{}
	if err := json.Unmarshal([]byte(rawOutput), &output); err != nil {
		return nil, true, nil
	}
	return output, true, nil
}

// GetLastSuccessfulStepOutput returns the raw JSON string output of the most recent successful run of a node.
func (e *GllamEngine) GetLastSuccessfulStepOutput(ctx context.Context, traceID string, nodeID string) (string, bool, error) {
	var rawOutput string
	err := e.dbRO.QueryRowContext(ctx, `
		SELECT output_payload
		FROM procedural_step_runs
		WHERE trace_id = ? AND node_id = ? AND status = 'success'
		ORDER BY step_number DESC
		LIMIT 1`, traceID, nodeID).Scan(&rawOutput)

	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to query last successful step output: %w", err)
	}
	return rawOutput, true, nil
}

// ResolveCompensations resolves rollback actions in reverse Saga order for all executed steps with 'compensates' transitions.
func (e *GllamEngine) ResolveCompensations(ctx context.Context, traceID string) ([]memory.ProceduralCompensationStep, error) {
	query := `
		SELECT sr.node_id, sr.step_number, sr.output_payload,
		       COALESCE(l.metadata, '{}'),
		       p.id, p.name, p.description, p.action_type,
		       COALESCE(p.input_schema, ''), COALESCE(p.output_schema, ''),
		       p.is_idempotent, COALESCE(p.metadata, '{}'), p.created_at, p.updated_at
		FROM procedural_step_runs sr
		JOIN procedural_links l ON l.source_procedure_id = sr.node_id AND l.relation_type = 'compensates'
		JOIN procedural_nodes p ON l.target_procedure_id = p.id
		WHERE sr.trace_id = ? AND sr.status = 'success'
		ORDER BY sr.step_number DESC`

	rows, err := e.dbRO.QueryContext(ctx, query, traceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query compensation steps: %w", err)
	}
	defer rows.Close()

	var compensations []memory.ProceduralCompensationStep
	for rows.Next() {
		var step memory.ProceduralCompensationStep
		var isIdempotentInt int
		if err := rows.Scan(
			&step.ExecutedNodeID, &step.ExecutedStepNumber, &step.ExecutedOutput,
			&step.MappingRules,
			&step.CompensationNode.ID, &step.CompensationNode.Name, &step.CompensationNode.Description,
			&step.CompensationNode.ActionType, &step.CompensationNode.InputSchema, &step.CompensationNode.OutputSchema,
			&isIdempotentInt, &step.CompensationNode.Metadata, &step.CompensationNode.CreatedAt, &step.CompensationNode.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan compensation step: %w", err)
		}
		step.CompensationNode.IsIdempotent = (isIdempotentInt == 1)
		compensations = append(compensations, step)
	}
	return compensations, rows.Err()
}

// ApplyParameterMapping maps variables from context_state into target input parameters using JSON path mapping rules.
func ApplyParameterMapping(mappingRulesJSON string, contextState map[string]interface{}) (map[string]interface{}, error) {
	mappingRulesJSON = strings.TrimSpace(mappingRulesJSON)
	if mappingRulesJSON == "" || mappingRulesJSON == "{}" {
		return make(map[string]interface{}), nil
	}
	var rules map[string]string
	if err := json.Unmarshal([]byte(mappingRulesJSON), &rules); err != nil {
		return nil, fmt.Errorf("invalid parameter mapping rules JSON: %w", err)
	}

	result := make(map[string]interface{})
	for targetKey, sourcePath := range rules {
		if val, found := resolveContextValue(sourcePath, contextState); found {
			result[targetKey] = val
		}
	}
	return result, nil
}
