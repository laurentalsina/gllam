package engine

import (
    "context"
    "fmt"

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
    return nil
}

// GetRecentEpisodes retrieves the most recent episodic summaries
func (e *GllamEngine) GetRecentEpisodes(ctx context.Context, limit int) ([]memory.EpisodicSummary, error) {
    query := `
        SELECT id, session_id, summary_text, created_at
        FROM episodic_summaries
        ORDER BY created_at DESC
        LIMIT ?`

    rows, err := e.db.QueryContext(ctx, query, limit)
    if err != nil {
        return nil, fmt.Errorf("failed to get recent episodes: %w", err)
    }
    defer rows.Close()

    var episodes []memory.EpisodicSummary
    for rows.Next() {
        var es memory.EpisodicSummary
        if err := rows.Scan(&es.ID, &es.SessionID, &es.SummaryText, &es.CreatedAt); err != nil {
            return nil, fmt.Errorf("failed to scan episode: %w", err)
        }
        episodes = append(episodes, es)
    }

    return episodes, rows.Err()
}

// GetEpisodesInWindow retrieves episodic summaries within a temporal window
func (e *GllamEngine) GetEpisodesInWindow(ctx context.Context, startTime, endTime int64) ([]memory.EpisodicSummary, error) {
    query := `
        SELECT id, session_id, summary_text, created_at
        FROM episodic_summaries
        WHERE created_at >= ? AND created_at <= ?
        ORDER BY created_at DESC`

    rows, err := e.db.QueryContext(ctx, query, startTime, endTime)
    if err != nil {
        return nil, fmt.Errorf("failed to get episodes in window: %w", err)
    }
    defer rows.Close()

    var episodes []memory.EpisodicSummary
    for rows.Next() {
        var es memory.EpisodicSummary
        if err := rows.Scan(&es.ID, &es.SessionID, &es.SummaryText, &es.CreatedAt); err != nil {
            return nil, fmt.Errorf("failed to scan episode: %w", err)
        }
        episodes = append(episodes, es)
    }

    return episodes, rows.Err()
}
