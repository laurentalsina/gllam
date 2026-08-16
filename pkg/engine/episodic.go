package engine

import (
    "context"
    "fmt"
    "time"

    "github.com/laurentalsina/gllam/pkg/memory"
)

// SaveEpisodicSummary inserts a new episodic summary
func (e *GllamEngine) SaveEpisodicSummary(ctx context.Context, summary memory.EpisodicSummary) error {
    query := `
        INSERT INTO episodic_summaries (id, session_id, summary_text, created_at)
        VALUES (?, ?, ?, ?)`

    _, err := e.db.ExecContext(ctx, query, summary.ID, summary.SessionID, summary.SummaryText, summary.CreatedAt)
    if err != nil {
        return fmt.Errorf("failed to save episodic summary: %w", err)
    }

    if e.embedder != nil {
        emb, err := e.embedder.Embed(ctx, summary.SummaryText)
        if err == nil {
            embBytes, err := serializeEmbedding(emb)
            if err == nil {
                e.db.ExecContext(ctx, "DELETE FROM episodic_embeddings WHERE session_id = ?", summary.ID)
                qVec := `INSERT INTO episodic_embeddings (session_id, embedding) VALUES (?, vec_f32(?))`
                e.db.ExecContext(ctx, qVec, summary.ID, embBytes)
            }
        }
    }

    return nil
}

// GetRecentEpisodes retrieves the most recent episodic summaries (read-only → dbRO)
func (e *GllamEngine) GetRecentEpisodes(ctx context.Context, limit int) ([]memory.EpisodicSummary, error) {
    query := `
        SELECT id, session_id, summary_text, created_at
        FROM episodic_summaries
        ORDER BY created_at DESC
        LIMIT ?`

    rows, err := e.dbRO.QueryContext(ctx, query, limit)
    if err != nil {
        return nil, fmt.Errorf("failed to get recent episodes: %w", err)
    }
    defer rows.Close()

    var episodes []memory.EpisodicSummary
    for rows.Next() {
        var es memory.EpisodicSummary
        if err := rows.Scan(&es.ID, &es.SessionID, &es.SummaryText, scanTime(&es.CreatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan episode: %w", err)
        }
        episodes = append(episodes, es)
    }

    return episodes, rows.Err()
}

// GetEpisodesInWindow retrieves episodic summaries within a temporal window (read-only → dbRO)
func (e *GllamEngine) GetEpisodesInWindow(ctx context.Context, startTime, endTime time.Time) ([]memory.EpisodicSummary, error) {
    query := `
        SELECT id, session_id, summary_text, created_at
        FROM episodic_summaries
        WHERE created_at >= ? AND created_at <= ?
        ORDER BY created_at DESC`

    rows, err := e.dbRO.QueryContext(ctx, query, startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))
    if err != nil {
        return nil, fmt.Errorf("failed to get episodes in window: %w", err)
    }
    defer rows.Close()

    var episodes []memory.EpisodicSummary
    for rows.Next() {
        var es memory.EpisodicSummary
        if err := rows.Scan(&es.ID, &es.SessionID, &es.SummaryText, scanTime(&es.CreatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan episode: %w", err)
        }
        episodes = append(episodes, es)
    }

    return episodes, rows.Err()
}

// SearchSimilarEpisodes retrieves the top K most semantically similar episodes
func (e *GllamEngine) SearchSimilarEpisodes(ctx context.Context, queryText string, topK int) ([]memory.EpisodicSummary, error) {
    if e.embedder == nil {
        return nil, fmt.Errorf("embedder is not configured")
    }

    emb, err := e.embedder.Embed(ctx, queryText)
    if err != nil {
        return nil, fmt.Errorf("failed to embed query: %w", err)
    }

    query := `
        SELECT es.id, es.session_id, es.summary_text, es.created_at
        FROM (
            SELECT session_id, distance
            FROM episodic_embeddings
            WHERE embedding MATCH vec_f32(?) AND k = ?
        ) ee
        JOIN episodic_summaries es ON +ee.session_id = es.id
        ORDER BY ee.distance ASC`

    embBytes, err := serializeEmbedding(emb)
    if err != nil {
        return nil, fmt.Errorf("failed to serialize embedding: %w", err)
    }

    rows, err := e.dbRO.QueryContext(ctx, query, embBytes, topK)
    if err != nil {
        return nil, fmt.Errorf("failed to search similar episodes: %w", err)
    }
    defer rows.Close()

    var episodes []memory.EpisodicSummary
    for rows.Next() {
        var es memory.EpisodicSummary
        if err := rows.Scan(&es.ID, &es.SessionID, &es.SummaryText, scanTime(&es.CreatedAt)); err != nil {
            return nil, fmt.Errorf("failed to scan episode: %w", err)
        }
        episodes = append(episodes, es)
    }

    return episodes, rows.Err()
}
