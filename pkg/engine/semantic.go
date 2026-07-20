package engine

import (
    "context"
    "database/sql"
    "fmt"
    "time"

    "github.com/laurentalsina/gllam/pkg/memory"
)

// UpsertNode inserts or updates a semantic node
func (e *GllamEngine) UpsertNode(ctx context.Context, node memory.SemanticNode) error {
    query := `
        INSERT INTO semantic_nodes (id, name, type)
        VALUES (?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type`

    _, err := e.db.ExecContext(ctx, query, node.ID, node.Name, node.Type)
    if err != nil {
        return fmt.Errorf("failed to upsert node: %w", err)
    }
    return nil
}

// AddEdge inserts a new semantic link after checking for existing active links with the same source and relationship
func (e *GllamEngine) AddEdge(ctx context.Context, link memory.SemanticLink) error {
    // Query existing active links for the same source_id and relationship
    var existingCaveats string
    var existingTargetID string
    query := `
        SELECT caveats, target_id 
        FROM semantic_links 
        WHERE source_id = ? AND relationship = ? AND valid_until IS NULL
        LIMIT 1`

    err := e.db.QueryRowContext(ctx, query, link.SourceID, link.Relationship).Scan(&existingCaveats, &existingTargetID)
    if err != nil && err != sql.ErrNoRows {
        return fmt.Errorf("failed to query existing edges: %w", err)
    }

    // If an existing active link was found and it points to a different target, create a contradiction
    if err == nil && existingTargetID != link.TargetID {
        if err := e.CreateContradiction(ctx, existingTargetID, link.TargetID, link.SourceID, link.Relationship); err != nil {
            return fmt.Errorf("failed to create contradiction: %w", err)
        }
    }

    // Insert the new link
    insertQuery := `
        INSERT INTO semantic_links (source_id, target_id, relationship, caveats, valid_from, valid_until, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`

    now := time.Now().Unix()
    _, err = e.db.ExecContext(ctx, insertQuery,
        link.SourceID, link.TargetID, link.Relationship, link.Caveats,
        link.ValidFrom, link.ValidUntil, now)
    if err != nil {
        return fmt.Errorf("failed to add edge: %w", err)
    }

    return nil
}

// CreateContradiction records a contradiction between two semantic links
func (e *GllamEngine) CreateContradiction(ctx context.Context, targetID1, targetID2, sourceID, relationship string) error {
    query := `
        INSERT INTO contradictions (id, link1_source_id, link1_target_id, link1_relationship,
                                    link2_source_id, link2_target_id, link2_relationship, detected_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

    id := fmt.Sprintf("contradiction-%s-%s-%s", sourceID, targetID1, targetID2)
    now := time.Now().Unix()
    _, err := e.db.ExecContext(ctx, query, id, sourceID, targetID1, relationship,
        sourceID, targetID2, relationship, now)
    if err != nil {
        return fmt.Errorf("failed to create contradiction: %w", err)
    }
    return nil
}

// ResolveContradiction marks a contradiction as resolved with optional notes
func (e *GllamEngine) ResolveContradiction(ctx context.Context, contradictionID, notes string) error {
    query := `
        UPDATE contradictions 
        SET resolved = 1, resolved_at = ?, resolution_notes = ?
        WHERE id = ?`

    now := time.Now().Unix()
    _, err := e.db.ExecContext(ctx, query, now, notes, contradictionID)
    if err != nil {
        return fmt.Errorf("failed to resolve contradiction: %w", err)
    }
    return nil
}

// GetUnresolvedContradictions retrieves all unresolved contradictions
func (e *GllamEngine) GetUnresolvedContradictions(ctx context.Context) ([]memory.Contradiction, error) {
    query := `
        SELECT id, link1_source_id, link1_target_id, link1_relationship,
               link2_source_id, link2_target_id, link2_relationship,
               detected_at, resolved, resolved_at, resolution_notes
        FROM contradictions
        WHERE resolved = 0
        ORDER BY detected_at DESC`

    rows, err := e.db.QueryContext(ctx, query)
    if err != nil {
        return nil, fmt.Errorf("failed to get unresolved contradictions: %w", err)
    }
    defer rows.Close()

    var contradictions []memory.Contradiction
    for rows.Next() {
        var c memory.Contradiction
        if err := rows.Scan(&c.ID, &c.Link1SourceID, &c.Link1TargetID, &c.Link1Relationship,
            &c.Link2SourceID, &c.Link2TargetID, &c.Link2Relationship,
            &c.DetectedAt, &c.Resolved, &c.ResolvedAt, &c.ResolutionNotes); err != nil {
            return nil, fmt.Errorf("failed to scan contradiction: %w", err)
        }
        contradictions = append(contradictions, c)
    }

    return contradictions, rows.Err()
}

// InvalidateObsoleteEdge marks an older link as expired when a new preference supersedes it
func (e *GllamEngine) InvalidateObsoleteEdge(ctx context.Context, sourceID, relationship, targetID string) error {
    query := `
        UPDATE semantic_links 
        SET valid_until = ?, updated_at = ?
        WHERE source_id = ? AND relationship = ? AND target_id = ? AND valid_until IS NULL`

    now := time.Now().Unix()
    _, err := e.db.ExecContext(ctx, query, now, now, sourceID, relationship, targetID)
    if err != nil {
        return fmt.Errorf("failed to invalidate obsolete edge: %w", err)
    }

    return nil
}
