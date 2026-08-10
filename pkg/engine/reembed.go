package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// CheckEmbeddingModelVersion queries system_metadata for the active embedding model version.
// If a model version change is detected (or uninitialized), it updates the stored version and returns drift status.
func (e *GllamEngine) CheckEmbeddingModelVersion(ctx context.Context) (bool, string, string, error) {
	if e.embedder == nil {
		return false, "", "", nil
	}

	activeVersion := e.embedder.ModelVersion()
	if activeVersion == "" {
		activeVersion = "nomic-embed-text-v1.5"
	}

	var storedVersion string
	query := `SELECT value FROM system_metadata WHERE key = 'embedding_model_version'`
	err := e.dbRO.QueryRowContext(ctx, query).Scan(&storedVersion)

	if err == sql.ErrNoRows {
		// First initialization: store current model version
		insertQuery := `INSERT INTO system_metadata (key, value, updated_at) VALUES ('embedding_model_version', ?, ?)`
		if _, err := e.db.ExecContext(ctx, insertQuery, activeVersion, time.Now().Unix()); err != nil {
			log.Printf("Failed to store initial embedding_model_version: %v", err)
		}
		return false, "", activeVersion, nil
	} else if err != nil {
		return false, "", "", fmt.Errorf("failed to query embedding_model_version: %w", err)
	}

	if storedVersion != activeVersion {
		log.Printf("Vector space drift detected: stored model '%s' vs active model '%s'. Initiating background re-embedding task.", storedVersion, activeVersion)
		return true, storedVersion, activeVersion, nil
	}

	return false, storedVersion, activeVersion, nil
}

// ReembedAllSemanticNodes iterates through all semantic_nodes and updates their vector representations in
// semantic_embeddings to eliminate vector space drift after embedding model upgrades.
func (e *GllamEngine) ReembedAllSemanticNodes(ctx context.Context) (int, error) {
	if e.embedder == nil {
		return 0, fmt.Errorf("no embedder configured")
	}

	query := `SELECT id, name, context_prompt FROM semantic_nodes`
	rows, err := e.dbRO.QueryContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch nodes for re-embedding: %w", err)
	}
	defer rows.Close()

	type nodeInfo struct {
		id   string
		text string
	}
	var nodes []nodeInfo

	for rows.Next() {
		var id, name string
		var prompt sql.NullString
		if err := rows.Scan(&id, &name, &prompt); err == nil {
			text := name
			if prompt.Valid && prompt.String != "" {
				text = fmt.Sprintf("%s: %s", name, prompt.String)
			}
			nodes = append(nodes, nodeInfo{id: id, text: text})
		}
	}

	reembeddedCount := 0
	for _, n := range nodes {
		vec, err := e.embedder.Embed(ctx, n.text)
		if err != nil {
			log.Printf("Failed to generate embedding for node %s (%s): %v", n.id, n.text, err)
			continue
		}

		vecBytes, err := serializeEmbedding(vec)
		if err != nil {
			log.Printf("Failed to serialize vector for node %s: %v", n.id, err)
			continue
		}

		// Update or insert vector into semantic_embeddings
		deleteQuery := `DELETE FROM semantic_embeddings WHERE node_id = ?`
		if _, err := e.db.ExecContext(ctx, deleteQuery, n.id); err != nil {
			log.Printf("Failed to delete old embedding for node %s: %v", n.id, err)
		}

		insertQuery := `INSERT INTO semantic_embeddings (node_id, embedding) VALUES (?, ?)`
		if _, err := e.db.ExecContext(ctx, insertQuery, n.id, vecBytes); err == nil {
			reembeddedCount++
		}
	}

	// Update stored model version metadata to active model
	activeVersion := e.embedder.ModelVersion()
	updateMeta := `INSERT INTO system_metadata (key, value, updated_at) VALUES ('embedding_model_version', ?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`
	if _, err := e.db.ExecContext(ctx, updateMeta, activeVersion, time.Now().Unix()); err != nil {
		return reembeddedCount, fmt.Errorf("failed to update embedding_model_version in system_metadata: %w", err)
	}

	return reembeddedCount, nil
}
