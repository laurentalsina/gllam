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

    // Relationships that are strictly 1:1 (mutually exclusive targets)
    isMutuallyExclusive := map[string]bool{
        "has_state":  true,
        "located_in": true,
    }

    // If an existing active link was found, it points to a different target, and the relationship is mutually exclusive, create a contradiction node
    if err == nil && existingTargetID != link.TargetID && isMutuallyExclusive[link.Relationship] {
        conflictID := fmt.Sprintf("conflict-%s-%s", link.SourceID, link.Relationship)
        conflictNode := memory.SemanticNode{
            ID:   conflictID,
            Name: fmt.Sprintf("Conflict regarding %s for %s", link.Relationship, link.SourceID),
            Type: "contradiction",
        }
        _ = e.UpsertNode(ctx, conflictNode)
        _ = e.StoreNodeEmbedding(ctx, conflictNode.ID)

        // Add edge from source to conflict
        now := time.Now().Unix()
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     link.SourceID,
            TargetID:     conflictID,
            Relationship: "has_unresolved_conflict",
            ValidFrom:    now,
        })

        // Add edges from conflict to targets
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     conflictID,
            TargetID:     existingTargetID,
            Relationship: "conflicting_claim",
            ValidFrom:    now,
        })
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     conflictID,
            TargetID:     link.TargetID,
            Relationship: "conflicting_claim",
            ValidFrom:    now,
        })
    }

    // Insert the new link (idempotent update if it already exists)
    insertQuery := `
        INSERT INTO semantic_links (source_id, target_id, relationship, caveats, valid_from, valid_until, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(source_id, target_id, relationship) DO UPDATE SET 
            caveats = excluded.caveats,
            valid_from = excluded.valid_from,
            valid_until = excluded.valid_until,
            updated_at = excluded.updated_at`

    now := time.Now().Unix()
    _, err = e.db.ExecContext(ctx, insertQuery,
        link.SourceID, link.TargetID, link.Relationship, link.Caveats,
        link.ValidFrom, link.ValidUntil, now)
    if err != nil {
        return fmt.Errorf("failed to add edge: %w", err)
    }

    return nil
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

// StoreNodeEmbedding generates and stores an embedding vector for a semantic node.
// The embedding is generated from the node's name using the configured embedder.
func (e *GllamEngine) StoreNodeEmbedding(ctx context.Context, nodeID string) error {
    if e.embedder == nil {
        return fmt.Errorf("no embedder configured")
    }

    // Fetch the node to get its name
    var name string
    err := e.db.QueryRowContext(ctx, "SELECT name FROM semantic_nodes WHERE id = ?", nodeID).Scan(&name)
    if err != nil {
        return fmt.Errorf("failed to fetch node %s: %w", nodeID, err)
    }

    // Generate embedding
    embedding, err := e.embedder.Embed(ctx, name)
    if err != nil {
        return fmt.Errorf("failed to generate embedding for %s: %w", nodeID, err)
    }

    // Serialize embedding to blob
    embeddingBlob, err := serializeEmbedding(embedding)
    if err != nil {
        return fmt.Errorf("failed to serialize embedding: %w", err)
    }

    // Upsert into vec0 virtual table
    // sqlite-vec does not support ON CONFLICT (UPSERT), so we DELETE then INSERT
    _, err = e.db.ExecContext(ctx, "DELETE FROM semantic_embeddings WHERE node_id = ?", nodeID)
    if err != nil {
        return fmt.Errorf("failed to delete old embedding for node %s: %w", nodeID, err)
    }

    _, err = e.db.ExecContext(ctx, `
        INSERT INTO semantic_embeddings (node_id, embedding)
        VALUES (?, vec_f32(?))
    `, nodeID, embeddingBlob)
    if err != nil {
        return fmt.Errorf("failed to store embedding for node %s: %w", nodeID, err)
    }

    return nil
}

// SearchSimilarNodes finds nodes with similar embeddings to the given query text.
func (e *GllamEngine) SearchSimilarNodes(ctx context.Context, queryText string, limit int) ([]struct {
    NodeID   string
    Distance float32
}, error) {
    if e.embedder == nil {
        return nil, fmt.Errorf("no embedder configured")
    }

    // Generate query embedding
    queryEmbedding, err := e.embedder.Embed(ctx, queryText)
    if err != nil {
        return nil, fmt.Errorf("failed to generate query embedding: %w", err)
    }

    // Serialize query embedding
    queryBlob, err := serializeEmbedding(queryEmbedding)
    if err != nil {
        return nil, fmt.Errorf("failed to serialize query embedding: %w", err)
    }

    // Search using vec0 MATCH
    query := `
        SELECT node_id, distance
        FROM semantic_embeddings
        WHERE embedding MATCH vec_f32(?) AND k = ?
        ORDER BY distance`

    rows, err := e.dbRO.QueryContext(ctx, query, queryBlob, limit)
    if err != nil {
        fmt.Printf("SQL Error in SearchSimilarNodes: %v\n", err)
        return nil, fmt.Errorf("failed to search similar nodes: %w", err)
    }
    defer rows.Close()

    var results []struct {
        NodeID   string
        Distance float32
    }
    for rows.Next() {
        var r struct {
            NodeID   string
            Distance float32
        }
        if err := rows.Scan(&r.NodeID, &r.Distance); err != nil {
            return nil, fmt.Errorf("failed to scan result: %w", err)
        }
        results = append(results, r)
    }

    fmt.Printf("SearchSimilarNodes found %d nodes\n", len(results))
    return results, rows.Err()
}
