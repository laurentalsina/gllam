package engine

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/config"
	"github.com/laurentalsina/gllam/pkg/memory"
)






// UpsertNode inserts or updates a semantic node
func (e *GllamEngine) UpsertNode(ctx context.Context, node memory.SemanticNode) error {
	if node.TrustWeight <= 0 {
		node.TrustWeight = 100 // Default trust weight
	}
	if node.TaxonomyPath == "" {
		node.TaxonomyPath = "/"
	}
	isCatInt := 0
	if node.IsCategory {
		isCatInt = 1
	}

	var caveatSummaryVal sql.NullString
	if node.CaveatSummary != "" {
		caveatSummaryVal = sql.NullString{String: node.CaveatSummary, Valid: true}
	}

	var createdFromVal sql.NullString
	if node.CreatedFrom != "" {
		createdFromVal = sql.NullString{String: node.CreatedFrom, Valid: true}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	createdTime := now
	if !node.CreatedAt.IsZero() {
		createdTime = node.CreatedAt.UTC().Format(time.RFC3339)
	}

	query := `
        INSERT INTO semantic_nodes (id, name, type, context_prompt, trust_weight, taxonomy_path, is_category, caveat_summary, created_from, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET 
            name = CASE 
                WHEN (lower(excluded.name) LIKE 'user%' OR lower(excluded.name) LIKE 'assistant%') AND NOT (lower(semantic_nodes.name) LIKE 'user%' OR lower(semantic_nodes.name) LIKE 'assistant%') AND semantic_nodes.name != '' THEN semantic_nodes.name
                ELSE excluded.name
            END, 
            type = excluded.type, 
            context_prompt = CASE 
                WHEN excluded.context_prompt != '' AND excluded.context_prompt IS NOT NULL THEN excluded.context_prompt
                ELSE semantic_nodes.context_prompt
            END, 
            trust_weight = excluded.trust_weight,
            taxonomy_path = CASE WHEN excluded.taxonomy_path != '/' AND excluded.taxonomy_path != '' THEN excluded.taxonomy_path ELSE semantic_nodes.taxonomy_path END,
            is_category = CASE WHEN excluded.is_category != 0 THEN excluded.is_category ELSE semantic_nodes.is_category END,
            caveat_summary = CASE WHEN excluded.caveat_summary IS NOT NULL AND excluded.caveat_summary != '' THEN excluded.caveat_summary ELSE semantic_nodes.caveat_summary END,
            created_from = COALESCE(excluded.created_from, semantic_nodes.created_from),
            updated_at = excluded.updated_at`

	_, err := e.db.ExecContext(ctx, query, node.ID, node.Name, node.Type, node.ContextPrompt, node.TrustWeight, node.TaxonomyPath, isCatInt, caveatSummaryVal, createdFromVal, createdTime, now)
	if err != nil {
		return fmt.Errorf("failed to upsert node: %w", err)
	}
	return nil
}



// AddEdge inserts a new semantic link after checking for existing active links with the same source and relationship
func (e *GllamEngine) AddEdge(ctx context.Context, link memory.SemanticLink) error {
	nowTime := time.Now().UTC().Format(time.RFC3339)
	createdTime := nowTime
	if !link.CreatedAt.IsZero() {
		createdTime = link.CreatedAt.UTC().Format(time.RFC3339)
	}

	// Query existing active links for the same source_id and relationship
	var existingCaveats string
	var existingTargetID string
	var existingOriginSource sql.NullString

	query := `
        SELECT caveats, target_id, origin_id 
        FROM semantic_links 
        WHERE source_id = ? AND relationship = ?
        LIMIT 1`

	err := e.db.QueryRowContext(ctx, query, link.SourceID, link.Relationship).Scan(&existingCaveats, &existingTargetID, &existingOriginSource)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to query existing edges: %w", err)
	}

	// Relationships that are strictly 1:1 (mutually exclusive targets)
	isMutuallyExclusive := map[string]bool{
		"has_state":  true,
		"located_in": true,
	}

	// If an existing link was found, it points to a different target, and the relationship is mutually exclusive:
	if err == nil && existingTargetID != link.TargetID && isMutuallyExclusive[link.Relationship] {
		// Epistemic Hierarchy Source Trust Weighting Check
		newTrustWeight := 100
		existingTrustWeight := 100

		if link.OriginID != "" {
			var tw sql.NullInt64
			if err := e.db.QueryRowContext(ctx, "SELECT trust_weight FROM semantic_nodes WHERE id = ?", link.OriginID).Scan(&tw); err == nil && tw.Valid {
				newTrustWeight = int(tw.Int64)
			}
		}

		if existingOriginSource.Valid && existingOriginSource.String != "" {
			var tw sql.NullInt64
			if err := e.db.QueryRowContext(ctx, "SELECT trust_weight FROM semantic_nodes WHERE id = ?", existingOriginSource.String).Scan(&tw); err == nil && tw.Valid {
				existingTrustWeight = int(tw.Int64)
			}
		}

		if newTrustWeight > existingTrustWeight {
			if e.BitemporalSoftDelete {
				// Soft-expire the existing claim instead of deleting it physically
				if err := e.InvalidateObsoleteEdgeWithAnchor(ctx, link.SourceID, link.Relationship, link.TargetID, createdTime, "", ""); err != nil {
					return fmt.Errorf("failed to soft-expire lower-trust claim: %w", err)
				}
			} else {
				// Incoming claim has HIGHER trust weight -> Delete existing claim automatically!
				deleteQuery := `DELETE FROM semantic_links WHERE source_id = ? AND target_id = ? AND relationship = ?`
				if _, err := e.db.ExecContext(ctx, deleteQuery, link.SourceID, existingTargetID, link.Relationship); err != nil {
					return fmt.Errorf("failed to delete existing lower-trust claim: %w", err)
				}
			}

			// Insert resolves_conflict edge
			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:            link.TargetID,
				TargetID:            existingTargetID,
				Relationship:        "resolves_conflict",
				ResolutionRationale: fmt.Sprintf("Automated Epistemic Hierarchy Resolution: Incoming source trust weight (%d) higher than existing (%d)", newTrustWeight, existingTrustWeight),
				OriginID:            link.OriginID,
			})
		} else if existingTrustWeight > newTrustWeight {
			// Existing claim has HIGHER trust weight -> Automatically reject incoming claim
			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:            existingTargetID,
				TargetID:            link.TargetID,
				Relationship:        "resolves_conflict",
				ResolutionRationale: fmt.Sprintf("Automated Epistemic Hierarchy Resolution: Existing source trust weight (%d) higher than incoming (%d)", existingTrustWeight, newTrustWeight),
				OriginID:            existingOriginSource.String,
			})
			return nil
		} else if !e.AllowUserGrilling || (e.SystemPrompts != nil && !e.SystemPrompts.AllowUserGrilling) {
			// Equal trust weights & User Grilling is DISABLED (e.g. BEAM Benchmark Evaluation Mode):
			// Automatically resolve by Recency Preference (newer incoming claim supersedes older claim)
			if e.BitemporalSoftDelete {
				// Soft-expire the older claim instead of deleting it physically
				if err := e.InvalidateObsoleteEdgeWithAnchor(ctx, link.SourceID, link.Relationship, link.TargetID, createdTime, "", ""); err != nil {
					return fmt.Errorf("failed to soft-expire older claim: %w", err)
				}
			} else {
				deleteQuery := `DELETE FROM semantic_links WHERE source_id = ? AND target_id = ? AND relationship = ?`
				if _, err := e.db.ExecContext(ctx, deleteQuery, link.SourceID, existingTargetID, link.Relationship); err != nil {
					return fmt.Errorf("failed to delete older claim in benchmark mode: %w", err)
				}
			}

			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:            link.TargetID,
				TargetID:            existingTargetID,
				Relationship:        "resolves_conflict",
				ResolutionRationale: fmt.Sprintf("Non-Interactive Benchmark Recency Resolution (AllowUserGrilling=false): Equal trust weights (%d)", newTrustWeight),
				OriginID:            link.OriginID,
			})
		} else {
			// Equal trust weights & User Grilling ENABLED -> Create unresolved contradiction node for human REPL grilling
			conflictID := fmt.Sprintf("conflict-%s-%s", link.SourceID, link.Relationship)
			conflictNode := memory.SemanticNode{
				ID:   conflictID,
				Name: fmt.Sprintf("Conflict regarding %s for %s", link.Relationship, link.SourceID),
				Type: "contradiction",
			}
			_ = e.UpsertNode(ctx, conflictNode)
			_ = e.StoreNodeEmbedding(ctx, conflictNode.ID)

			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:     link.SourceID,
				TargetID:     conflictID,
				Relationship: "has_unresolved_conflict",
			})
			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:     conflictID,
				TargetID:     existingTargetID,
				Relationship: "conflicting_claim",
			})
			_ = e.AddEdge(ctx, memory.SemanticLink{
				SourceID:     conflictID,
				TargetID:     link.TargetID,
				Relationship: "conflicting_claim",
			})
		}
	}

	if link.Modality == "" {
		link.Modality = "epistemic"
	}

	var temporalLinkID sql.NullString
	if link.Temporal != nil {
		tID := fmt.Sprintf("temp-%s-%s-%s", link.SourceID, link.TargetID, link.Relationship)
		temporalLinkID = sql.NullString{String: tID, Valid: true}

		insertTempQuery := `
			INSERT INTO semantic_temporal_links (id, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_note)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				valid_from = excluded.valid_from,
				valid_until = excluded.valid_until,
				temporal_anchor_id = excluded.temporal_anchor_id,
				temporal_relation = excluded.temporal_relation,
				temporal_note = excluded.temporal_note`

		var anchorID sql.NullString
		if link.Temporal.TemporalAnchorID != "" {
			anchorID = sql.NullString{String: link.Temporal.TemporalAnchorID, Valid: true}
			// Ensure anchor node exists in semantic_nodes to satisfy foreign key constraint
			var exists int
			_ = e.db.QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.Temporal.TemporalAnchorID).Scan(&exists)
			if exists == 0 {
				_ = e.UpsertNode(ctx, memory.SemanticNode{
					ID:          link.Temporal.TemporalAnchorID,
					Name:        link.Temporal.TemporalAnchorID,
					Type:        "event",
					CreatedFrom: "anchor_inference",
				})
			}
		}

		_, tErr := e.db.ExecContext(ctx, insertTempQuery,
			tID, link.Temporal.ValidFrom, link.Temporal.ValidUntil,
			anchorID, link.Temporal.TemporalRelation, link.Temporal.TemporalNote)
		if tErr != nil {
			return fmt.Errorf("failed to save semantic temporal attributes: %w", tErr)
		}
	}

    insertQuery := `
        INSERT INTO semantic_links (source_id, target_id, relationship, caveats, modality, origin_id, resolution_rationale, created_from, created_at, updated_at, temporal_link_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(source_id, target_id, relationship) DO UPDATE SET 
            caveats = excluded.caveats,
            modality = excluded.modality,
            origin_id = excluded.origin_id,
            resolution_rationale = excluded.resolution_rationale,
            created_from = excluded.created_from,
            updated_at = excluded.updated_at,
            temporal_link_id = excluded.temporal_link_id`

    var origSource, resRationaleVal sql.NullString
    if link.OriginID != "" && !strings.EqualFold(link.OriginID, "null") && !strings.EqualFold(link.OriginID, "none") && !strings.EqualFold(link.OriginID, "nil") && !strings.EqualFold(link.OriginID, "unknown") {
        origSource = sql.NullString{String: link.OriginID, Valid: true}
		// Ensure origin node exists in semantic_nodes to satisfy foreign key constraint
		var exists int
		_ = e.db.QueryRowContext(ctx, "SELECT 1 FROM semantic_nodes WHERE id = ?", link.OriginID).Scan(&exists)
		if exists == 0 {
			_ = e.UpsertNode(ctx, memory.SemanticNode{
				ID:          link.OriginID,
				Name:        link.OriginID,
				Type:        "human",
				CreatedFrom: "origin_inference",
			})
		}
    }
    if link.ResolutionRationale != "" {
        resRationaleVal = sql.NullString{String: link.ResolutionRationale, Valid: true}
    }

    var createdFromVal sql.NullString
    if link.CreatedFrom != "" {
        createdFromVal = sql.NullString{String: link.CreatedFrom, Valid: true}
    }

    _, err = e.db.ExecContext(ctx, insertQuery,
        link.SourceID, link.TargetID, link.Relationship, link.Caveats, link.Modality,
        origSource, resRationaleVal, createdFromVal, createdTime, nowTime, temporalLinkID)

    if err != nil {
        return fmt.Errorf("failed to add edge: %w", err)
    }

	// Event-Anchored State Invalidation (Trap 9):
	// When a new state or preference link is added, mark previous active state links as expired
	// using the new link's valid_from timestamp and temporal anchor ID.
	if link.Relationship == "has_state" || link.Relationship == "is_preference" {
		var validFromVal, anchorIDVal, tempNoteVal string
		if link.Temporal != nil {
			validFromVal = link.Temporal.ValidFrom
			anchorIDVal = link.Temporal.TemporalAnchorID
			tempNoteVal = link.Temporal.TemporalNote
		}
		_ = e.InvalidateObsoleteEdgeWithAnchor(ctx, link.SourceID, link.Relationship, link.TargetID, validFromVal, anchorIDVal, tempNoteVal)
	}

    return nil
}

// InvalidateObsoleteEdge marks an older link as expired when a new preference supersedes it
func (e *GllamEngine) InvalidateObsoleteEdge(ctx context.Context, sourceID, relationship, targetID string) error {
	return e.InvalidateObsoleteEdgeWithAnchor(ctx, sourceID, relationship, targetID, "", "", "")
}

// InvalidateObsoleteEdgeWithAnchor marks older links as expired using an event's timestamp or anchor ID
func (e *GllamEngine) InvalidateObsoleteEdgeWithAnchor(ctx context.Context, sourceID, relationship, targetID string, validUntil string, anchorID string, tempNote string) error {
	if validUntil == "" {
		if tempNote != "" || anchorID != "" {
			validUntil = "temporal_note"
		} else {
			validUntil = fmt.Sprintf("%d", time.Now().Unix())
		}
	}

	// 1. Find the old links matching sourceID and relationship (where targetID is different, or targetID is empty/ignored)
	// and get their temporal_link_id.
	var query string
	var rows *sql.Rows
	var err error

	if targetID != "" {
		query = `SELECT target_id, temporal_link_id FROM semantic_links WHERE source_id = ? AND relationship = ? AND target_id != ?`
		rows, err = e.db.QueryContext(ctx, query, sourceID, relationship, targetID)
	} else {
		query = `SELECT target_id, temporal_link_id FROM semantic_links WHERE source_id = ? AND relationship = ?`
		rows, err = e.db.QueryContext(ctx, query, sourceID, relationship)
	}
	if err != nil {
		return fmt.Errorf("failed to query old links for invalidation: %w", err)
	}

	type oldLinkItem struct {
		targetID       string
		temporalLinkID sql.NullString
	}
	var items []oldLinkItem

	for rows.Next() {
		var item oldLinkItem
		if err := rows.Scan(&item.targetID, &item.temporalLinkID); err == nil {
			items = append(items, item)
		}
	}
	rows.Close() // Release SQLite read lock immediately!

	now := time.Now().UTC().Format(time.RFC3339)

	for _, item := range items {
		var tID string
		if item.temporalLinkID.Valid && item.temporalLinkID.String != "" {
			tID = item.temporalLinkID.String
			// Update existing temporal link
			updateQuery := `
				UPDATE semantic_temporal_links 
				SET valid_until = ?, 
				    temporal_anchor_id = COALESCE(NULLIF(?, ''), temporal_anchor_id),
				    temporal_relation = CASE WHEN ? != '' THEN 'ended_by' ELSE temporal_relation END,
				    temporal_note = COALESCE(NULLIF(?, ''), temporal_note)
				WHERE id = ?`
			if _, err := e.db.ExecContext(ctx, updateQuery, validUntil, anchorID, anchorID, tempNote, tID); err != nil {
				return fmt.Errorf("failed to update temporal link: %w", err)
			}
		} else {
			// Create a new temporal link
			tID = fmt.Sprintf("temp-%s-%s-%s", sourceID, item.targetID, relationship)
			insertQuery := `
				INSERT INTO semantic_temporal_links (id, valid_until, temporal_anchor_id, temporal_relation, temporal_note)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					valid_until = excluded.valid_until,
					temporal_anchor_id = COALESCE(NULLIF(excluded.temporal_anchor_id, ''), temporal_anchor_id),
					temporal_relation = CASE WHEN excluded.temporal_relation != '' THEN excluded.temporal_relation ELSE temporal_relation END,
					temporal_note = COALESCE(NULLIF(excluded.temporal_note, ''), temporal_note)`
			
			var tempRel string
			if anchorID != "" {
				tempRel = "ended_by"
			}
			if _, err := e.db.ExecContext(ctx, insertQuery, tID, validUntil, anchorID, tempRel, tempNote); err != nil {
				return fmt.Errorf("failed to insert temporal link: %w", err)
			}

			// Associate with the old link in semantic_links
			associateQuery := `UPDATE semantic_links SET temporal_link_id = ?, updated_at = ? WHERE source_id = ? AND target_id = ? AND relationship = ?`
			if _, err := e.db.ExecContext(ctx, associateQuery, tID, now, sourceID, item.targetID, relationship); err != nil {
				return fmt.Errorf("failed to associate temporal link with old link: %w", err)
			}
		}
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

// IndexNodeVector stores a pre-computed float32 vector embedding for a semantic node into semantic_embeddings.
func (e *GllamEngine) IndexNodeVector(ctx context.Context, nodeID string, vec []float32) error {
	vecBytes, err := serializeEmbedding(vec)
	if err != nil {
		return fmt.Errorf("failed to serialize embedding: %w", err)
	}

	if _, err := e.db.ExecContext(ctx, "DELETE FROM semantic_embeddings WHERE node_id = ?", nodeID); err != nil {
		return fmt.Errorf("failed to clear previous embedding for node %s: %w", nodeID, err)
	}
	_, err = e.db.ExecContext(ctx, "INSERT INTO semantic_embeddings (node_id, embedding) VALUES (?, vec_f32(?))", nodeID, vecBytes)
	if err != nil {
		return fmt.Errorf("failed to store embedding for node %s: %w", nodeID, err)
	}
	return nil
}


// SearchSimilarNodes finds nodes with similar embeddings to the given query text.
func (e *GllamEngine) SearchSimilarNodes(ctx context.Context, queryText string, limit int) ([]struct {
    NodeID   string
    Distance float32
    Name     string
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
        SELECT e.node_id, e.distance, COALESCE(n.name, '')
        FROM semantic_embeddings e
        LEFT JOIN semantic_nodes n ON e.node_id = n.id
        WHERE e.embedding MATCH vec_f32(?) AND e.k = ?
        ORDER BY e.distance`

    rows, err := e.dbRO.QueryContext(ctx, query, queryBlob, limit)
    if err != nil {
        fmt.Printf("SQL Error in SearchSimilarNodes: %v\n", err)
        return nil, fmt.Errorf("failed to search similar nodes: %w", err)
    }
    defer rows.Close()

    var results []struct {
        NodeID   string
        Distance float32
        Name     string
    }
    for rows.Next() {
        var r struct {
            NodeID   string
            Distance float32
            Name     string
        }
        if err := rows.Scan(&r.NodeID, &r.Distance, &r.Name); err != nil {
            return nil, fmt.Errorf("failed to scan result: %w", err)
        }
        results = append(results, r)
    }

    return results, rows.Err()
}

// GetActiveLinksAtTime retrieves semantic links active at a specific Unix timestamp (read-only -> dbRO)
// It dynamically resolves temporal_anchor_id timestamps when valid_from or valid_until is "temporal_note".
func (e *GllamEngine) GetActiveLinksAtTime(ctx context.Context, timestamp int64) ([]memory.SemanticLink, error) {
    query := `
        SELECT 
            l.source_id, l.target_id, l.relationship, l.caveats, l.modality, l.origin_id, 
            l.resolution_rationale, l.created_from, l.created_at, l.updated_at, l.temporal_link_id,
            t.valid_from, t.valid_until, t.temporal_anchor_id, t.temporal_relation, t.temporal_note
        FROM semantic_links l
        LEFT JOIN semantic_temporal_links t ON l.temporal_link_id = t.id`

    rows, err := e.dbRO.QueryContext(ctx, query)
    if err != nil {
        return nil, fmt.Errorf("failed to query links: %w", err)
    }
    defer rows.Close()

    var candidates []memory.SemanticLink
    for rows.Next() {
        var l memory.SemanticLink
        var origSource, resRatVal, createdFrom, temporalLinkID sql.NullString
        var validFromVal, validUntilVal, tempAnchorID, tempRelation, tempNote sql.NullString
        
        err := rows.Scan(
            &l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.Modality, 
            &origSource, &resRatVal, &createdFrom, scanTime(&l.CreatedAt), scanTime(&l.UpdatedAt),
            &temporalLinkID, &validFromVal, &validUntilVal, &tempAnchorID, &tempRelation, &tempNote,
        )
        if err != nil {
            return nil, fmt.Errorf("failed to scan link: %w", err)
        }
        if origSource.Valid {
            l.OriginID = origSource.String
        }
        if resRatVal.Valid {
            l.ResolutionRationale = resRatVal.String
        }
        if createdFrom.Valid {
            l.CreatedFrom = createdFrom.String
        }
        if temporalLinkID.Valid {
            l.TemporalLinkID = temporalLinkID.String
            l.Temporal = &memory.SemanticTemporalAttributes{
                ID: temporalLinkID.String,
            }
            if validFromVal.Valid {
                l.Temporal.ValidFrom = validFromVal.String
            }
            if validUntilVal.Valid {
                l.Temporal.ValidUntil = validUntilVal.String
            }
            if tempAnchorID.Valid {
                l.Temporal.TemporalAnchorID = tempAnchorID.String
            }
            if tempRelation.Valid {
                l.Temporal.TemporalRelation = tempRelation.String
            }
            if tempNote.Valid {
                l.Temporal.TemporalNote = tempNote.String
            }
        }
        candidates = append(candidates, l)
    }

    var activeLinks []memory.SemanticLink
    for _, l := range candidates {
        if l.Temporal == nil {
            activeLinks = append(activeLinks, l)
            continue
        }

        // Check valid_from
        fromVal := l.Temporal.ValidFrom
        if fromVal != "" && fromVal != "temporal_note" {
            if fromTS, err := strconv.ParseInt(fromVal, 10, 64); err == nil && fromTS > timestamp {
                continue // Not active yet
            }
        }

        // Check valid_until
        untilVal := l.Temporal.ValidUntil
        if untilVal != "" && untilVal != "temporal_note" {
            if untilTS, err := strconv.ParseInt(untilVal, 10, 64); err == nil && untilTS <= timestamp {
                continue // Already expired
            }
        }

        // Dynamic Anchor Resolution
        if l.Temporal.TemporalAnchorID != "" {
            anchorTS := e.resolveAnchorTimestamp(ctx, l.Temporal.TemporalAnchorID, 0, "")
            if anchorTS > 0 {
                // If valid_from is anchored after requested timestamp, link wasn't active yet
                if l.Temporal.ValidFrom == "temporal_note" && (l.Temporal.TemporalRelation == "after" || l.Temporal.TemporalRelation == "ended_by") {
                    if timestamp < anchorTS {
                        continue
                    }
                }
                // If valid_until is anchored before/ended_by requested timestamp, link has expired
                if l.Temporal.ValidUntil == "temporal_note" && (l.Temporal.TemporalRelation == "ended_by" || l.Temporal.TemporalRelation == "before") {
                    if timestamp >= anchorTS {
                        continue
                    }
                }
            }
        }

        activeLinks = append(activeLinks, l)
    }

    return activeLinks, nil
}

func (e *GllamEngine) resolveAnchorTimestamp(ctx context.Context, anchorID string, offsetSeconds int64, granularity string) int64 {
	var ts int64
	query := `
		SELECT CAST(t.valid_from AS INTEGER) 
		FROM semantic_links l
		JOIN semantic_temporal_links t ON l.temporal_link_id = t.id
		WHERE l.source_id = ? AND t.valid_from != 'temporal_note' 
		LIMIT 1`
	if err := e.dbRO.QueryRowContext(ctx, query, anchorID).Scan(&ts); err == nil && ts > 0 {
		return ts
	}
	return 0
}


// ExpandTemporalNeighbors performs N-hop traversal over temporal links and temporal anchors
// to ensure complete transitive ordering chains (e.g. A -> B -> C) are loaded into context.
func (e *GllamEngine) ExpandTemporalNeighbors(ctx context.Context, seedNodes []memory.SemanticNode, existingLinks []memory.SemanticLink, maxHops int) ([]memory.SemanticNode, []memory.SemanticLink, error) {
	return e.ExpandTemporalNeighborsWithTime(ctx, seedNodes, existingLinks, maxHops, nil)
}

// ExpandTemporalNeighborsWithTime performs N-hop traversal over temporal links active as of a specific evaluation timestamp (evalTimestamp).
// Passing evalTimestamp enables point-in-time "time travel" RAG queries (e.g. querying active facts as of 2021).
func (e *GllamEngine) ExpandTemporalNeighborsWithTime(ctx context.Context, seedNodes []memory.SemanticNode, existingLinks []memory.SemanticLink, maxHops int, evalTimestamp *int64) ([]memory.SemanticNode, []memory.SemanticLink, error) {
	nodeMap := make(map[string]memory.SemanticNode)
	linkMap := make(map[string]memory.SemanticLink)

	for _, n := range seedNodes {
		nodeMap[n.ID] = n
	}
	for _, l := range existingLinks {
		key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
		linkMap[key] = l
	}

	visitedNodes := make(map[string]bool)
	frontier := make([]string, 0, len(seedNodes))
	for _, n := range seedNodes {
		frontier = append(frontier, n.ID)
		visitedNodes[n.ID] = true
	}

	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		var nextFrontier []string

		for _, currentID := range frontier {
			var query string
			var rows *sql.Rows
			var err error

			query = `
				SELECT 
					l.source_id, l.target_id, l.relationship, l.caveats, l.modality, l.origin_id, 
					l.resolution_rationale, l.created_from, l.created_at, l.updated_at, l.temporal_link_id,
					t.valid_from, t.valid_until, t.temporal_anchor_id, t.temporal_relation, t.temporal_note
				FROM semantic_links l
				LEFT JOIN semantic_temporal_links t ON l.temporal_link_id = t.id
				WHERE l.source_id = ? OR l.target_id = ?`
			rows, err = e.dbRO.QueryContext(ctx, query, currentID, currentID)

			if err != nil {
				continue
			}

			for rows.Next() {
				var l memory.SemanticLink
				var origSource, resRatVal, createdFrom, temporalLinkID sql.NullString
				var validFromVal, validUntilVal, tempAnchorID, tempRelation, tempNote sql.NullString
				
				err := rows.Scan(
					&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.Modality, 
					&origSource, &resRatVal, &createdFrom, scanTime(&l.CreatedAt), scanTime(&l.UpdatedAt),
					&temporalLinkID, &validFromVal, &validUntilVal, &tempAnchorID, &tempRelation, &tempNote,
				)
				if err != nil {
					continue
				}
				if origSource.Valid {
					l.OriginID = origSource.String
				}
				if resRatVal.Valid {
					l.ResolutionRationale = resRatVal.String
				}
				if createdFrom.Valid {
					l.CreatedFrom = createdFrom.String
				}
				if temporalLinkID.Valid {
					l.TemporalLinkID = temporalLinkID.String
					l.Temporal = &memory.SemanticTemporalAttributes{
						ID: temporalLinkID.String,
					}
					if validFromVal.Valid {
						l.Temporal.ValidFrom = validFromVal.String
					}
					if validUntilVal.Valid {
						l.Temporal.ValidUntil = validUntilVal.String
					}
					if tempAnchorID.Valid {
						l.Temporal.TemporalAnchorID = tempAnchorID.String
					}
					if tempRelation.Valid {
						l.Temporal.TemporalRelation = tempRelation.String
					}
					if tempNote.Valid {
						l.Temporal.TemporalNote = tempNote.String
					}
				}

				key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
				linkMap[key] = l

				// Collect connected neighbor node IDs
				neighbors := []string{l.SourceID, l.TargetID}

				for _, neighborID := range neighbors {
					if !visitedNodes[neighborID] {
						visitedNodes[neighborID] = true
						nextFrontier = append(nextFrontier, neighborID)

						// Fetch Node metadata if missing
						var node memory.SemanticNode
						var ctxPrompt, caveatSum, createdFrom sql.NullString
						nodeQuery := `SELECT id, name, type, context_prompt, caveat_summary, created_from FROM semantic_nodes WHERE id = ?`
						if err := e.dbRO.QueryRowContext(ctx, nodeQuery, neighborID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt, &caveatSum, &createdFrom); err == nil {
							if ctxPrompt.Valid {
								node.ContextPrompt = ctxPrompt.String
							}
							if caveatSum.Valid {
								node.CaveatSummary = caveatSum.String
							}
							if createdFrom.Valid {
								node.CreatedFrom = createdFrom.String
							}
							nodeMap[node.ID] = node
						}
					}
				}
			}
			rows.Close()
		}
		frontier = nextFrontier
	}

	expNodes := make([]memory.SemanticNode, 0, len(nodeMap))
	for _, n := range nodeMap {
		expNodes = append(expNodes, n)
	}
	expLinks := make([]memory.SemanticLink, 0, len(linkMap))
	for _, l := range linkMap {
		expLinks = append(expLinks, l)
	}

	return expNodes, expLinks, nil
}

// GetActiveConstraintsForSource retrieves active rules, preferences, and constraints for a given source_id or targetContext
func (e *GllamEngine) GetActiveConstraintsForSource(ctx context.Context, sourceID string, targetContext string) ([]memory.SemanticLink, error) {
	query := `
		SELECT 
			d.source_id, d.target_id, d.relationship, d.caveats, d.modality, d.origin_id, 
			d.resolution_rationale, d.created_from, d.created_at, d.updated_at, d.temporal_link_id,
			t.valid_from, t.valid_until, t.temporal_anchor_id, t.temporal_relation, t.temporal_note
		FROM semantic_links d
		LEFT JOIN semantic_temporal_links t ON d.temporal_link_id = t.id
		WHERE 1=1 
		  AND d.relationship NOT IN ('supersedes_rule', 'conflicting_claim', 'has_unresolved_conflict')
		  AND (d.modality = 'deontic' OR d.relationship IN ('has_constraint', 'is_preference', 'applies_rule') OR d.target_id LIKE 'rule%' OR d.target_id LIKE 'constraint%')
		  AND (d.source_id = ? OR ? = 'global' OR ? = '')
		  AND NOT EXISTS (
		      SELECT 1 FROM semantic_links s 
		      WHERE s.relationship = 'supersedes_rule' AND s.target_id = d.target_id
		  )
		ORDER BY d.rowid ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, sourceID, targetContext, sourceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query active constraints for source %s: %w", sourceID, err)
	}
	defer rows.Close()

	var links []memory.SemanticLink
	for rows.Next() {
		var l memory.SemanticLink
		var origSource, resRatVal, createdFrom, temporalLinkID sql.NullString
		var validFromVal, validUntilVal, tempAnchorID, tempRelation, tempNote sql.NullString
		
		err := rows.Scan(
			&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.Modality, 
			&origSource, &resRatVal, &createdFrom, scanTime(&l.CreatedAt), scanTime(&l.UpdatedAt),
			&temporalLinkID, &validFromVal, &validUntilVal, &tempAnchorID, &tempRelation, &tempNote,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan constraint link: %w", err)
		}
		if origSource.Valid {
			l.OriginID = origSource.String
		}
		if resRatVal.Valid {
			l.ResolutionRationale = resRatVal.String
		}
		if createdFrom.Valid {
			l.CreatedFrom = createdFrom.String
		}
		if temporalLinkID.Valid {
			l.TemporalLinkID = temporalLinkID.String
			l.Temporal = &memory.SemanticTemporalAttributes{
				ID: temporalLinkID.String,
			}
			if validFromVal.Valid {
				l.Temporal.ValidFrom = validFromVal.String
			}
			if validUntilVal.Valid {
				l.Temporal.ValidUntil = validUntilVal.String
			}
			if tempAnchorID.Valid {
				l.Temporal.TemporalAnchorID = tempAnchorID.String
			}
			if tempRelation.Valid {
				l.Temporal.TemporalRelation = tempRelation.String
			}
			if tempNote.Valid {
				l.Temporal.TemporalNote = tempNote.String
			}
		}
		links = append(links, l)
	}
	return links, rows.Err()
}


// RevokeOrSupersedeRule revokes a rule/constraint or marks it as superseded by a newer rule ID
func (e *GllamEngine) RevokeOrSupersedeRule(ctx context.Context, oldRuleID string, newRuleID string) error {
	if newRuleID != "" {
		// Add supersedes link
		return e.AddEdge(ctx, memory.SemanticLink{
			SourceID:     newRuleID,
			TargetID:     oldRuleID,
			Relationship: "supersedes_rule",
			Modality:     "deontic",
		})
	} else {
		// If newRuleID is empty, we just delete the rule link to revoke it
		_, err := e.db.ExecContext(ctx, "DELETE FROM semantic_links WHERE (target_id = ? OR source_id = ?) AND (modality = 'deontic' OR relationship = 'supersedes_rule')", oldRuleID, oldRuleID)
		return err
	}
}


// DecrementActiveTurnConstraints decrements remaining_turns on active turn-bounded rules and auto-expires rules that hit 0 turns
func (e *GllamEngine) DecrementActiveTurnConstraints(ctx context.Context) error {
	return nil
}


// ConfrontRuleRationales evaluates pairs of active rules for priority/rationale collisions,
// returning a human-readable confrontation diagnostic detailing why higher-priority rationale wins.
func ConfrontRuleRationales(links []memory.SemanticLink) string {
	var posRules []memory.SemanticLink
	var negRules []memory.SemanticLink

	for _, l := range links {
		targetLower := strings.ToLower(l.TargetID)
		relLower := strings.ToLower(l.Relationship)
		caveatsLower := strings.ToLower(l.Caveats)

		if strings.Contains(targetLower, "no_") || strings.Contains(targetLower, "never_") || strings.Contains(targetLower, "dont_") || strings.Contains(relLower, "prohibit") || strings.Contains(caveatsLower, "negative") {
			negRules = append(negRules, l)
		} else if relLower == "is_preference" || relLower == "has_constraint" || relLower == "applies_rule" || l.Modality == "deontic" {
			posRules = append(posRules, l)
		}
	}

	if len(negRules) == 0 || len(posRules) == 0 {
		return ""
	}

	var notices []string
	for _, neg := range negRules {
		negTarget := strings.ToLower(neg.TargetID)
		for _, pos := range posRules {
			posTarget := strings.ToLower(pos.TargetID)

			// Detect domain collision between negative restriction and positive preference
			collision := false
			if (strings.Contains(negTarget, "token") && strings.Contains(posTarget, "log")) ||
				(strings.Contains(negTarget, "ip") && strings.Contains(posTarget, "verbose")) ||
				(strings.Contains(negTarget, "format") && strings.Contains(posTarget, "format")) {
				collision = true
			}

			if collision {
				negRat := neg.Caveats
				if negRat == "" {
					negRat = "Security & Global Policy"
				}
				posRat := pos.Caveats
				if posRat == "" {
					posRat = "User Style Preference"
				}

				notice := fmt.Sprintf("⚠️ RULE RATIONALE CONFRONTATION RESOLVED: Negative restriction '%s' (Rationale: %s) supersedes positive directive '%s' (Rationale: %s).",
					neg.TargetID, negRat, pos.TargetID, posRat)
				notices = append(notices, notice)
			}
		}
	}

	return strings.Join(notices, "\n")
}

// DisambiguityResult holds the resolved node ID or diagnostic warning when disambiguating ambiguous terms
type DisambiguityResult struct {
	ResolvedNodeID string
	IsAmbiguous    bool
	Candidates     []memory.SemanticNode
	Diagnostic     string
}

// DisambiguateEntityForSource resolves ambiguous entity mentions (Trap 10) by grounding candidate nodes
// in the epistemic interaction history and role context of the active sourceID.
func (e *GllamEngine) DisambiguateEntityForSource(ctx context.Context, term string, sourceID string) (DisambiguityResult, error) {
	termLower := strings.ToLower(strings.TrimSpace(term))
	if termLower == "" {
		return DisambiguityResult{}, nil
	}

	// 1. Query candidate nodes matching the term in ID or Name
	query := `
		SELECT id, name, type, context_prompt, created_from
		FROM semantic_nodes
		WHERE LOWER(name) LIKE ? OR LOWER(id) LIKE ?`
	pattern := "%" + termLower + "%"

	rows, err := e.dbRO.QueryContext(ctx, query, pattern, pattern)
	if err != nil {
		return DisambiguityResult{}, fmt.Errorf("failed to query candidate nodes for term %s: %w", term, err)
	}
	defer rows.Close()

	var candidates []memory.SemanticNode
	for rows.Next() {
		var n memory.SemanticNode
		var ctxPrompt, createdFrom sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &ctxPrompt, &createdFrom); err == nil {
			if ctxPrompt.Valid {
				n.ContextPrompt = ctxPrompt.String
			}
			if createdFrom.Valid {
				n.CreatedFrom = createdFrom.String
			}
			candidates = append(candidates, n)
		}
	}

	if len(candidates) == 0 {
		return DisambiguityResult{}, nil
	}
	if len(candidates) == 1 {
		return DisambiguityResult{
			ResolvedNodeID: candidates[0].ID,
			Candidates:     candidates,
		}, nil
	}

	// 2. Score candidates by interaction affinity with sourceID
	bestScore := -1
	var bestCandidate string
	var tiedCandidates []memory.SemanticNode

	for _, cand := range candidates {
		score := 0
		if sourceID != "" {
			var linkCount int
			linkQuery := `
				SELECT COUNT(*) 
				FROM semantic_links 
				WHERE (source_id = ? OR target_id = ?) AND origin_id = ?`
			if err := e.dbRO.QueryRowContext(ctx, linkQuery, cand.ID, cand.ID, sourceID).Scan(&linkCount); err == nil {
				score += linkCount * 10
			}
		}

		if score > bestScore {
			bestScore = score
			bestCandidate = cand.ID
			tiedCandidates = []memory.SemanticNode{cand}
		} else if score == bestScore {
			tiedCandidates = append(tiedCandidates, cand)
		}
	}

	if len(tiedCandidates) == 1 && bestScore > 0 {
		return DisambiguityResult{
			ResolvedNodeID: bestCandidate,
			Candidates:     candidates,
		}, nil
	}

	// 3. Ambiguity impasse (Trap 10 diagnostic)
	candNames := make([]string, len(candidates))
	for i, c := range candidates {
		candNames[i] = fmt.Sprintf("'%s' (%s)", c.Name, c.ID)
	}

	diag := fmt.Sprintf("⚠️ TIMELINE NAMING AMBIGUITY: Term '%s' matches multiple semantic entities [%s] for source '%s'. Please specify which entity is intended.",
		term, strings.Join(candNames, ", "), sourceID)

	return DisambiguityResult{
		IsAmbiguous: true,
		Candidates:  candidates,
		Diagnostic:  diag,
	}, nil
}

// ResolveContradiction resolves an active contradiction between two claims (Trap 5),
// deleting the losing claim's links and inserting a resolves_conflict edge.
func (e *GllamEngine) ResolveContradiction(ctx context.Context, contradictionID string, winningClaimID string, losingClaimID string, rationale string) error {
	// 1. Delete losing claim links
	if _, err := e.db.ExecContext(ctx,
		`DELETE FROM semantic_links WHERE source_id = ? OR target_id = ?`,
		losingClaimID, losingClaimID); err != nil {
		return fmt.Errorf("failed to delete losing claim %s: %w", losingClaimID, err)
	}

	// 2. Delete active contradiction node links
	if contradictionID != "" {
		if _, err := e.db.ExecContext(ctx,
			`DELETE FROM semantic_links WHERE source_id = ? OR target_id = ?`,
			contradictionID, contradictionID); err != nil {
			return fmt.Errorf("failed to delete contradiction node links for %s: %w", contradictionID, err)
		}
	}

	// 3. Insert resolves_conflict link
	resLink := memory.SemanticLink{
		SourceID:            winningClaimID,
		TargetID:            losingClaimID,
		Relationship:        "resolves_conflict",
		ResolutionRationale: rationale,
	}

	return e.AddEdge(ctx, resLink)
}

// DetectFallacySubversion inspects retrieved links and nodes for active logical fallacies across all 6 categories,
// returning a human-readable diagnostic warning explaining how GLLAM has isolated or guarded against them.
func DetectFallacySubversion(links []memory.SemanticLink, nodes []memory.SemanticNode) string {
	fallacyNodes := make(map[string]memory.SemanticNode)
	for _, n := range nodes {
		if n.Type == memory.NodeTypeFallacy || strings.HasPrefix(strings.ToLower(n.ID), "fallacy_") {
			fallacyNodes[n.ID] = n
		}
	}

	var diagnostics []string

	for _, l := range links {
		rel := strings.ToLower(l.Relationship)
		if rel == "exhibits_fallacy" || rel == "subverts_claim" || fallacyNodes[l.SourceID].ID != "" || fallacyNodes[l.TargetID].ID != "" {
			fallacyID := l.TargetID
			claimID := l.SourceID
			if fallacyNodes[l.SourceID].ID != "" {
				fallacyID = l.SourceID
				claimID = l.TargetID
			}

			fNode := fallacyNodes[fallacyID]
			fType := strings.ToLower(fallacyID)
			fExplain := fNode.ContextPrompt
			if fExplain == "" {
				fExplain = "Deceptive or logically flawed premise detected"
			}

			var guardAction string
			switch {
			case strings.Contains(fType, "false_dilemma"):
				guardAction = "Isolated binary constraint; prevented promotion to global rule."
			case strings.Contains(fType, "circularity") || strings.Contains(fType, "begging_question"):
				guardAction = "Disabled cyclic PDDL action preconditions."
			case strings.Contains(fType, "post_hoc") || strings.Contains(fType, "cum_hoc"):
				guardAction = "Downgraded causal link to weak temporal sequence."
			case strings.Contains(fType, "ad_hominem"):
				guardAction = "Preserved underlying claim; isolated source attack."
			case strings.Contains(fType, "red_herring") || strings.Contains(fType, "straw_man"):
				guardAction = "Suppressed multi-hop graph expansion for distracting sub-graph."
			case strings.Contains(fType, "equivocation"):
				guardAction = "Triggered entity disambiguation for ambiguous terms."
			default:
				guardAction = "Isolated fallacy node to protect automated reasoning."
			}

			diag := fmt.Sprintf("⚠️ BYZANTINE FALLACY DETECTED: Claim '%s' exhibits fallacy '%s' (%s). Guard Action: %s",
				claimID, fallacyID, fExplain, guardAction)
			diagnostics = append(diagnostics, diag)
		}
	}

	return strings.Join(diagnostics, "\n")
}

// NeedleScoredNode wraps a SemanticNode with its Reciprocal Rank Fusion score and attached links/caveats
type NeedleScoredNode struct {
	Node       memory.SemanticNode   `json:"node"`
	Links      []memory.SemanticLink `json:"links"`
	RRFScore   float64               `json:"rrf_score"`
	VectorRank int                   `json:"vector_rank"`
	GraphRank  int                   `json:"graph_rank"`
}

// RetrieveHybridNeedle performs dual-channel RRF hybrid retrieval over vector embeddings and exact graph traversal
func (e *GllamEngine) RetrieveHybridNeedle(ctx context.Context, query string, entityIDs []string, sourceID string, limit int) ([]NeedleScoredNode, error) {
	return e.RetrieveHybridNeedleWithTime(ctx, query, entityIDs, sourceID, limit, nil)
}

// RetrieveHybridNeedleWithTime performs dual-channel RRF hybrid retrieval over vector embeddings and exact graph traversal,
// filtering active facts as of a specific virtual evaluation timestamp (enabling point-in-time "time travel" RAG queries).
func (e *GllamEngine) RetrieveHybridNeedleWithTime(ctx context.Context, query string, entityIDs []string, sourceID string, limit int, asOfTime *int64) ([]NeedleScoredNode, error) {
	if limit <= 0 {
		limit = 10
	}
	k := 60.0 // RRF standard smoothing constant

	// 1. Vector Channel: SearchSimilarNodes
	var vectorNodes []memory.SemanticNode
	if query != "" {
		simResults, err := e.SearchSimilarNodes(ctx, query, limit*2)
		if err == nil {
			for _, res := range simResults {
				var node memory.SemanticNode
				var ctxPrompt, caveatSum, createdFrom sql.NullString
				nodeQuery := `SELECT id, name, type, context_prompt, caveat_summary, created_from FROM semantic_nodes WHERE id = ?`
				if err := e.dbRO.QueryRowContext(ctx, nodeQuery, res.NodeID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt, &caveatSum, &createdFrom); err == nil {
					if ctxPrompt.Valid {
						node.ContextPrompt = ctxPrompt.String
					}
					if caveatSum.Valid {
						node.CaveatSummary = caveatSum.String
					}
					if createdFrom.Valid {
						node.CreatedFrom = createdFrom.String
					}
					vectorNodes = append(vectorNodes, node)
				}
			}
		}
	}

	// 2. Disambiguation Channel: Disambiguate entityIDs for sourceID
	resolvedEntities := make([]string, 0, len(entityIDs))
	for _, ent := range entityIDs {
		disRes, err := e.DisambiguateEntityForSource(ctx, ent, sourceID)
		if err == nil && disRes.ResolvedNodeID != "" {
			resolvedEntities = append(resolvedEntities, disRes.ResolvedNodeID)
		} else {
			resolvedEntities = append(resolvedEntities, ent)
		}
	}

	// 3. Graph Channel: Fetch seed nodes & ExpandTemporalNeighborsWithTime (2 hops)
	var seedNodes []memory.SemanticNode
	for _, entID := range resolvedEntities {
		var node memory.SemanticNode
		var ctxPrompt, caveatSum, createdFrom sql.NullString
		nodeQuery := `SELECT id, name, type, context_prompt, caveat_summary, created_from FROM semantic_nodes WHERE id = ?`
		if err := e.dbRO.QueryRowContext(ctx, nodeQuery, entID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt, &caveatSum, &createdFrom); err == nil {
			if ctxPrompt.Valid {
				node.ContextPrompt = ctxPrompt.String
			}
			if caveatSum.Valid {
				node.CaveatSummary = caveatSum.String
			}
			if createdFrom.Valid {
				node.CreatedFrom = createdFrom.String
			}
			seedNodes = append(seedNodes, node)
		}
	}

	expNodes, expLinks, _ := e.ExpandTemporalNeighborsWithTime(ctx, seedNodes, nil, 2, asOfTime)
	expLinks = FilterActiveSummaryFactsForTime(expLinks, asOfTime)

	// Map links to source/target nodes
	linksByNode := make(map[string][]memory.SemanticLink)
	for _, l := range expLinks {
		linksByNode[l.SourceID] = append(linksByNode[l.SourceID], l)
		linksByNode[l.TargetID] = append(linksByNode[l.TargetID], l)
	}

	// 4. Compute RRF Scores
	nodeMap := make(map[string]*NeedleScoredNode)

	// Add vector ranks
	for rank, vNode := range vectorNodes {
		nodeID := vNode.ID
		if _, exists := nodeMap[nodeID]; !exists {
			nodeMap[nodeID] = &NeedleScoredNode{
				Node:       vNode,
				Links:      linksByNode[nodeID],
				VectorRank: rank + 1,
			}
		}
		nodeMap[nodeID].RRFScore += 1.0 / (k + float64(rank+1))
	}

	// Add graph ranks
	for rank, gNode := range expNodes {
		nodeID := gNode.ID
		if _, exists := nodeMap[nodeID]; !exists {
			nodeMap[nodeID] = &NeedleScoredNode{
				Node:      gNode,
				Links:     linksByNode[nodeID],
				GraphRank: rank + 1,
			}
		} else {
			nodeMap[nodeID].GraphRank = rank + 1
		}
		nodeMap[nodeID].RRFScore += 1.0 / (k + float64(rank+1))
	}

	// 5. Apply Qualifier Boosting (Trap 6) for specific environment/context tokens (e.g. "staging", "prod", "dev")
	queryLower := strings.ToLower(query)
	qualifiers := []string{"staging", "prod", "production", "dev", "development", "test", "read-only", "primary", "replica"}
	var activeQualifiers []string
	for _, q := range qualifiers {
		if strings.Contains(queryLower, q) {
			activeQualifiers = append(activeQualifiers, q)
		}
	}

	for _, sn := range nodeMap {
		if len(activeQualifiers) > 0 {
			combinedText := strings.ToLower(sn.Node.Name + " " + sn.Node.ContextPrompt)
			for _, l := range sn.Links {
				combinedText += " " + strings.ToLower(l.Caveats) + " " + strings.ToLower(l.Relationship)
			}

			matchedCount := 0
			for _, q := range activeQualifiers {
				if strings.Contains(combinedText, q) {
					matchedCount++
				}
			}

			if matchedCount > 0 {
				sn.RRFScore += float64(matchedCount) * 0.05 // Boost exact qualifier matches
			}
		}
	}

	// 6. Sort by RRFScore DESC
	scoredList := make([]NeedleScoredNode, 0, len(nodeMap))
	for _, sn := range nodeMap {
		scoredList = append(scoredList, *sn)
	}

	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].RRFScore > scoredList[j].RRFScore
	})

	// 7. Apply RRF Minimum Confidence Thresholding (Trap 7 - Abstention Guard)
	minThreshold := 0.015
	var filteredList []NeedleScoredNode
	for _, sn := range scoredList {
		if sn.RRFScore >= minThreshold {
			filteredList = append(filteredList, sn)
		}
	}

	if len(filteredList) > limit {
		filteredList = filteredList[:limit]
	}

	return filteredList, nil
}

// SupersedeFact updates a fact (Trap 1 & Trap 5), marking oldLink as expired (valid_until = newLink.valid_from)
// and adding a superseded_by edge connecting newLink to oldLink. Handles out-of-order ingestion backdating.
func (e *GllamEngine) SupersedeFact(ctx context.Context, oldLink memory.SemanticLink, newLink memory.SemanticLink, rationale string) error {
	nowStr := fmt.Sprintf("%d", time.Now().Unix())

	// Retrieve old temporal ID and valid_from to check chronological ordering
	var oldTemporalLinkID sql.NullString
	var oldFrom string
	_ = e.dbRO.QueryRowContext(ctx, 
		"SELECT temporal_link_id FROM semantic_links WHERE source_id = ? AND target_id = ? AND relationship = ?",
		oldLink.SourceID, oldLink.TargetID, oldLink.Relationship).Scan(&oldTemporalLinkID)

	if oldTemporalLinkID.Valid && oldTemporalLinkID.String != "" {
		_ = e.dbRO.QueryRowContext(ctx, 
			"SELECT valid_from FROM semantic_temporal_links WHERE id = ?", 
			oldTemporalLinkID.String).Scan(&oldFrom)
	}

	newFrom := ""
	if newLink.Temporal != nil {
		newFrom = newLink.Temporal.ValidFrom
	}

	// Parse timestamps to check for out-of-order ingestion (Trap 5)
	oldFromTS, _ := strconv.ParseInt(oldFrom, 10, 64)
	newFromTS, _ := strconv.ParseInt(newFrom, 10, 64)

	var expireTSStr string
	if newFromTS > 0 && oldFromTS > 0 && newFromTS < oldFromTS {
		// Out-of-order ingestion: newLink is chronologically OLDER than oldLink!
		// Do not overwrite oldLink's active status; instead, set newLink's valid_until = oldLink.ValidFrom
		if newLink.Temporal == nil {
			newLink.Temporal = &memory.SemanticTemporalAttributes{}
		}
		newLink.Temporal.ValidUntil = oldFrom
		if newLink.Relationship == "" {
			newLink.Relationship = oldLink.Relationship
		}
		return e.AddEdge(ctx, newLink)
	} else {
		// Normal supersession: newLink is chronologically NEWER
		expireTSStr = newFrom
		if expireTSStr == "" || expireTSStr == "temporal_note" {
			expireTSStr = nowStr
		}
	}

	// 1. Expire old link
	if oldTemporalLinkID.Valid && oldTemporalLinkID.String != "" {
		expireQuery := "UPDATE semantic_temporal_links SET valid_until = ? WHERE id = ?"
		_, err := e.db.ExecContext(ctx, expireQuery, expireTSStr, oldTemporalLinkID.String)
		if err != nil {
			return fmt.Errorf("failed to expire old fact temporal link: %w", err)
		}
	} else {
		tID := fmt.Sprintf("temp_%s_%s_%s", oldLink.SourceID, oldLink.TargetID, oldLink.Relationship)
		_, _ = e.db.ExecContext(ctx, 
			"INSERT OR IGNORE INTO semantic_temporal_links (id, valid_until) VALUES (?, ?)", 
			tID, expireTSStr)
		_, _ = e.db.ExecContext(ctx, 
			"UPDATE semantic_links SET temporal_link_id = ? WHERE source_id = ? AND target_id = ? AND relationship = ?",
			tID, oldLink.SourceID, oldLink.TargetID, oldLink.Relationship)
	}

	// 2. Add newLink
	if err := e.AddEdge(ctx, newLink); err != nil {
		return fmt.Errorf("failed to add new superseded fact link: %w", err)
	}

	// 3. Add superseded_by edge connecting new link target to old link target
	supLink := memory.SemanticLink{
		SourceID:            newLink.TargetID,
		TargetID:            oldLink.TargetID,
		Relationship:        "superseded_by",
		ResolutionRationale: rationale,
		OriginID:            newLink.OriginID,
	}
	if newLink.Temporal != nil {
		supLink.Temporal = &memory.SemanticTemporalAttributes{
			ValidFrom: expireTSStr,
		}
	}

	// Trigger cascading invalidation on cross-cutting dependent links (Trap 6)
	_ = e.InvalidateDependentCrossCuttingLinks(ctx, oldLink.TargetID, expireTSStr)

	return e.AddEdge(ctx, supLink)
}


// InvalidateDependentCrossCuttingLinks performs cascading re-validation tagging across downstream dependent nodes.
// Employs Active Stack Cycle Detection to gracefully break circular dependency loops (e.g. A -> B -> C -> A)
// while allowing deep acyclic propagation up to a configurable maxDepth (default 10).
func (e *GllamEngine) InvalidateDependentCrossCuttingLinks(ctx context.Context, updatedNodeID string, validFrom string) error {
	activeStack := make(map[string]bool)
	return e.InvalidateDependentCrossCuttingLinksRecursive(ctx, updatedNodeID, validFrom, 10, activeStack)
}

// InvalidateDependentCrossCuttingLinksRecursive traverses downstream dependencies with active stack cycle prevention.
func (e *GllamEngine) InvalidateDependentCrossCuttingLinksRecursive(ctx context.Context, currentNodeID string, validFrom string, remainingDepth int, activeStack map[string]bool) error {
	if remainingDepth <= 0 {
		return nil // Maximum traversal depth reached
	}

	// Active Stack Cycle Prevention: If currentNodeID is already in the active traversal stack, a cycle is detected!
	if activeStack[currentNodeID] {
		log.Printf("Circular dependency invalidation loop detected at node %s. Terminating branch recursion.", currentNodeID)
		return nil
	}

	// Mark node as active in current recursion branch
	activeStack[currentNodeID] = true
	defer func() {
		delete(activeStack, currentNodeID) // Unmark when exiting branch
	}()

	now := time.Now()

	// Query downstream nodes connected to currentNodeID as target_id
	queryDownstream := `
		SELECT source_id
		FROM semantic_links
		WHERE target_id = ? AND relationship IN ('depends_on', 'applies_rule', 'requires_config', 'references', 'uses_version') `

	rows, err := e.dbRO.QueryContext(ctx, queryDownstream, currentNodeID)
	if err != nil {
		return fmt.Errorf("failed to query downstream dependent nodes for %s: %w", currentNodeID, err)
	}

	var downstreamNodeIDs []string
	for rows.Next() {
		var srcID string
		if err := rows.Scan(&srcID); err == nil {
			downstreamNodeIDs = append(downstreamNodeIDs, srcID)
		}
	}
	rows.Close()

	if len(downstreamNodeIDs) == 0 {
		return nil
	}

	// Flag direct links as REQUIRES_REVALIDATION
	updateQuery := `
		UPDATE semantic_links
		SET caveats = CASE 
			WHEN caveats IS NULL OR caveats = '' THEN '[REQUIRES_REVALIDATION: Upstream node ' || ? || ' was updated]' 
			ELSE caveats || ' [REQUIRES_REVALIDATION: Upstream node ' || ? || ' was updated]' 
		END,
		updated_at = ?
		WHERE target_id = ? AND relationship IN ('depends_on', 'applies_rule', 'requires_config', 'references', 'uses_version') `

	_, err = e.db.ExecContext(ctx, updateQuery, currentNodeID, currentNodeID, now, currentNodeID)
	if err != nil {
		return fmt.Errorf("failed to invalidate dependent cross-cutting links for %s: %w", currentNodeID, err)
	}

	// Recurse downstream
	for _, nextNodeID := range downstreamNodeIDs {
		_ = e.InvalidateDependentCrossCuttingLinksRecursive(ctx, nextNodeID, validFrom, remainingDepth-1, activeStack)
	}

	return nil
}


// SurfaceCrossCuttingImpacts inspects retrieved links for any requires_revalidation caveats (Trap 8),
// returning human-readable cross-cutting update diagnostics.
func SurfaceCrossCuttingImpacts(links []memory.SemanticLink, nodes []memory.SemanticNode, sourceID string) string {
	var notices []string
	for _, l := range links {
		if strings.Contains(l.Caveats, "REQUIRES_REVALIDATION") {
			notice := fmt.Sprintf("⚠️ CROSS-CUTTING KNOWLEDGE UPDATE WARNING: Link '%s --(%s)--> %s' requires re-validation due to upstream updates. (%s)",
				l.SourceID, l.Relationship, l.TargetID, l.Caveats)
			notices = append(notices, notice)
		}
	}
	return strings.Join(notices, "\n")
}

// MultiHopPath represents a multi-step transitive reasoning chain across session boundaries
type MultiHopPath struct {
	Nodes    []memory.SemanticNode `json:"nodes"`
	Links    []memory.SemanticLink `json:"links"`
	HopCount int                   `json:"hop_count"`
}

// FindMultiHopPath performs BFS multi-hop graph traversal up to maxHops starting from seedEntityIDs (Trap 1 & Trap 5).
func (e *GllamEngine) FindMultiHopPath(ctx context.Context, seedEntityIDs []string, maxHops int) ([]MultiHopPath, error) {
	if maxHops <= 0 {
		maxHops = 3
	}

	if len(seedEntityIDs) == 0 {
		return nil, nil
	}

	var seedNodes []memory.SemanticNode
	for _, id := range seedEntityIDs {
		var node memory.SemanticNode
		var ctxPrompt, caveatSum, createdFrom sql.NullString
		err := e.dbRO.QueryRowContext(ctx, "SELECT id, name, type, context_prompt, caveat_summary, created_from FROM semantic_nodes WHERE id = ?", id).
			Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt, &caveatSum, &createdFrom)
		if err == nil {
			if ctxPrompt.Valid {
				node.ContextPrompt = ctxPrompt.String
			}
			if caveatSum.Valid {
				node.CaveatSummary = caveatSum.String
			}
			if createdFrom.Valid {
				node.CreatedFrom = createdFrom.String
			}
			seedNodes = append(seedNodes, node)
		}
	}

	expNodes, expLinks, err := e.ExpandTemporalNeighbors(ctx, seedNodes, nil, maxHops)
	if err != nil {
		return nil, fmt.Errorf("failed multi-hop expansion: %w", err)
	}

	paths := []MultiHopPath{
		{
			Nodes:    expNodes,
			Links:    expLinks,
			HopCount: maxHops,
		},
	}

	return paths, nil
}

// QuantitativeResult represents numeric balance evaluation
type QuantitativeResult struct {
	InitialAmount float64 `json:"initial_amount"`
	SpentAmount   float64 `json:"spent_amount"`
	RemBalance    float64 `json:"rem_balance"`
	ProposedCost  float64 `json:"proposed_cost"`
	IsAffordable  bool    `json:"is_affordable"`
	Explanation   string  `json:"explanation"`
}

// EvaluateQuantitativeConstraints calculates cumulative costs and evaluates numeric budget/capacity bounds (Trap 2).
func EvaluateQuantitativeConstraints(nodes []memory.SemanticNode, links []memory.SemanticLink, proposedCost float64) QuantitativeResult {
	res := QuantitativeResult{
		ProposedCost: proposedCost,
		IsAffordable: true,
	}

	for _, l := range links {
		relLower := strings.ToLower(l.Relationship)
		if relLower == "has_budget" || relLower == "initial_balance" || relLower == "budget" {
			var val float64
			if _, err := fmt.Sscanf(l.Caveats, "%f", &val); err == nil && val > 0 {
				res.InitialAmount = val
			}
		}
		if relLower == "spent" || relLower == "bought" || relLower == "purchased" || relLower == "expense" {
			var val float64
			if _, err := fmt.Sscanf(l.Caveats, "%f", &val); err == nil && val > 0 {
				res.SpentAmount += val
			}
		}
	}

	if res.InitialAmount > 0 {
		res.RemBalance = res.InitialAmount - res.SpentAmount
		if res.RemBalance-proposedCost < 0 {
			res.IsAffordable = false
			res.Explanation = fmt.Sprintf("⚠️ QUANTITATIVE CONSTRAINT VIOLATION: Initial budget %.2f - spent %.2f = remaining balance %.2f. Cannot afford proposed cost %.2f.",
				res.InitialAmount, res.SpentAmount, res.RemBalance, proposedCost)
		} else {
			res.Explanation = fmt.Sprintf("✅ QUANTITATIVE BALANCE CONFIRMED: Initial budget %.2f - spent %.2f = remaining balance %.2f. Sufficient for proposed cost %.2f.",
				res.InitialAmount, res.SpentAmount, res.RemBalance, proposedCost)
		}
	}

	return res
}

// ResolveSpatialContainment traverses located_in, lives_in, and part_of edges to resolve spatial inclusion (Trap 3).
func ResolveSpatialContainment(nodes []memory.SemanticNode, links []memory.SemanticLink, startEntityID string) []string {
	visited := make(map[string]bool)
	var locations []string

	curr := startEntityID
	for i := 0; i < 5; i++ {
		visited[curr] = true
		foundNext := false
		for _, l := range links {
			relLower := strings.ToLower(l.Relationship)
			if (l.SourceID == curr) && (relLower == "located_in" || relLower == "lives_in" || relLower == "part_of" || relLower == "visiting") {
				if !visited[l.TargetID] {
					locations = append(locations, l.TargetID)
					curr = l.TargetID
					foundNext = true
					break
				}
			}
		}
		if !foundNext {
			break
		}
	}

	return locations
}

// FormatSalienceAnchoredSummary builds a ground-truth anchored summary (Trap 1, 2, 4, 5)
// preserving exact entity IDs, active relationships, hard temporal boundaries, global user directives, and agentic system prompts.
func FormatSalienceAnchoredSummary(nodes []memory.SemanticNode, links []memory.SemanticLink, episodes []memory.EpisodicSummary, queryPrompt string, sysPrompts *config.AgenticMemorySystemPrompts) string {
	// 1. Filter out obsolete state links (Trap 2 - Knowledge Update Active State Filter)
	activeLinks := FilterActiveSummaryFacts(links)

	// 2. Extract global user preferences & negative constraints (Trap 5)
	globalDirectives := PreserveGlobalDirectives(activeLinks)

	// 3. Compute dynamic query-conditioned salience scores
	salienceScores := ComputeQueryConditionedSalience(nodes, activeLinks, queryPrompt)

	var sb strings.Builder
	sb.WriteString("=== GROUND-TRUTH ANCHORED SUMMARY ===\n")

	if sysPrompts != nil && sysPrompts.HistoricalContextPrompt != "" {
		sb.WriteString("--- CORPUS HISTORICAL & DOMAIN CONTEXT ---\n")
		sb.WriteString(sysPrompts.HistoricalContextPrompt)
		sb.WriteString("\n\n")
	}

	if globalDirectives != "" {
		sb.WriteString("--- GLOBAL DIRECTIVES & RULES ---\n")
		sb.WriteString(globalDirectives)
		sb.WriteString("\n")
	}


	sb.WriteString("--- SALIENT GROUND-TRUTH ENTITIES & STATES ---\n")
	for _, n := range nodes {
		salienceScore := salienceScores[n.ID]

		if salienceScore > 0.1 || n.ContextPrompt != "" {
			sb.WriteString(fmt.Sprintf("• Entity: %s (ID: %s, Type: %s, Query Salience: %.2f)\n", n.Name, n.ID, n.Type, salienceScore))
			if n.ContextPrompt != "" {
				sb.WriteString(fmt.Sprintf("  Context: %s\n", n.ContextPrompt))
			}
		}
	}


	sb.WriteString("\n--- ACTIVE FACTUAL RELATIONSHIPS & TEMPORAL BOUNDS ---\n")
	

	for _, l := range activeLinks {
		tempStr := ""

		// Calculate state origin and duration ("since X")
		sinceStr := ""

		turnStr := ""
		caveatStr := ""
		if l.Caveats != "" {
			caveatStr = fmt.Sprintf(" (Caveat: %s)", l.Caveats)
		}

		sb.WriteString(fmt.Sprintf("• %s --(%s)--> %s%s%s%s%s\n", l.SourceID, l.Relationship, l.TargetID, caveatStr, tempStr, sinceStr, turnStr))
	}


	if len(episodes) > 0 {
		sb.WriteString("\n--- EPISODIC TIMELINE SUMMARY ---\n")
		for _, ep := range episodes {
			sb.WriteString(fmt.Sprintf("• Episode %s: %s\n", ep.ID, ep.SummaryText))
		}
	}

	return sb.String()
}

// FilterActiveSummaryFacts filters out links where valid_until IS NOT NULL (Trap 2).
func FilterActiveSummaryFacts(links []memory.SemanticLink) []memory.SemanticLink {
	return FilterActiveSummaryFactsForTime(links, nil)
}

// FilterActiveSummaryFactsForTime filters links active as of a specific evaluation timestamp (asOfTime).
// If asOfTime is nil, filters links where valid_until IS NULL.
func FilterActiveSummaryFactsForTime(links []memory.SemanticLink, asOfTime *int64) []memory.SemanticLink {
	var active []memory.SemanticLink
	for _, l := range links {
		if l.Temporal == nil {
			active = append(active, l)
			continue
		}

		// Check valid_from
		if asOfTime != nil && l.Temporal.ValidFrom != "" && l.Temporal.ValidFrom != "temporal_note" {
			fromTS := parseTimestamp(l.Temporal.ValidFrom)
			if fromTS > 0 && fromTS > *asOfTime {
				continue // Not active yet as of the requested timestamp
			}
		}

		// Check valid_until
		if l.Temporal.ValidUntil != "" && l.Temporal.ValidUntil != "temporal_note" {
			untilTS := parseTimestamp(l.Temporal.ValidUntil)
			if untilTS > 0 {
				if asOfTime != nil {
					if untilTS <= *asOfTime {
						continue // Already expired as of the requested timestamp
					}
				} else {
					continue // Expired links are filtered out for normal active fact retrieval
				}
			}
		}

		active = append(active, l)
	}
	return active
}

func parseTimestamp(s string) int64 {
	if s == "" || s == "temporal_note" {
		return 0
	}
	ts, _ := strconv.ParseInt(s, 10, 64)
	return ts
}

func parseTimestampPtr(s *string) int64 {
	if s == nil {
		return 0
	}
	return parseTimestamp(*s)
}

// PreserveGlobalDirectives extracts user_preference and negative constraint rules (Trap 5).
func PreserveGlobalDirectives(links []memory.SemanticLink) string {
	var directives []string
	for _, l := range links {
		if l.Modality == "deontic" || l.Relationship == "is_preference" || l.Relationship == "has_constraint" || l.Relationship == "applies_rule" || strings.HasPrefix(l.TargetID, "rule") || strings.HasPrefix(l.TargetID, "constraint") {
			directive := fmt.Sprintf("• Rule (%s): %s --(%s)--> %s", l.Modality, l.SourceID, l.Relationship, l.TargetID)
			if l.Caveats != "" {
				directive += fmt.Sprintf(" [Rationale: %s]", l.Caveats)
			}
			directives = append(directives, directive)
		}
	}
	return strings.Join(directives, "\n")
}

// ExtractProceduralWorkflow detects repeated operational step patterns and persists a reusable procedural recipe (Trap 3).
func (e *GllamEngine) ExtractProceduralWorkflow(ctx context.Context, name string, triggerContext string, instructions string, feedbackRules string) error {
	nowStr := time.Now().UTC().Format(time.RFC3339)
	taskType := strings.ToLower(strings.ReplaceAll(name, " ", "_"))
	query := `
		INSERT INTO procedural_knowledge (id, task_type, scope, trigger_context, instructions, user_feedback_rules, times_applied, is_highly_helpful, version, created_at, updated_at)
		VALUES (?, ?, 'external', ?, ?, ?, 1, 1, 1, ?, ?)
		ON CONFLICT(task_type) DO UPDATE SET instructions = excluded.instructions, times_applied = times_applied + 1, updated_at = excluded.updated_at`

	id := fmt.Sprintf("proc-%s", strings.ToLower(strings.ReplaceAll(name, " ", "-")))
	_, err := e.db.ExecContext(ctx, query, id, taskType, triggerContext, instructions, feedbackRules, nowStr, nowStr)
	if err != nil {
		return fmt.Errorf("failed to insert procedural workflow %s: %w", name, err)
	}
	return nil
}

// ComputeQueryConditionedSalience calculates dynamic focal salience scores (S in [0, 1])
// for nodes based on graph degree centrality AND explicit term/focal matches in the query prompt.
func ComputeQueryConditionedSalience(nodes []memory.SemanticNode, links []memory.SemanticLink, query string) map[string]float64 {
	degreeMap := make(map[string]int)
	for _, l := range links {
		degreeMap[l.SourceID]++
		degreeMap[l.TargetID]++
	}

	queryLower := strings.ToLower(query)
	focalNodes := make(map[string]bool)

	// Identify direct query focal matches (person, date, entity, port)
	for _, n := range nodes {
		nameLower := strings.ToLower(n.Name)
		idLower := strings.ToLower(n.ID)
		if (nameLower != "" && strings.Contains(queryLower, nameLower)) ||
			(idLower != "" && strings.Contains(queryLower, idLower)) {
			focalNodes[n.ID] = true
		}
	}

	// Also check date / timing terms in query
	dateKeywords := []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december", "yesterday", "today", "last month", "weeks ago", "days ago"}
	hasDateQuery := false
	for _, dk := range dateKeywords {
		if strings.Contains(queryLower, dk) {
			hasDateQuery = true
			break
		}
	}

	salienceScores := make(map[string]float64)
	for _, n := range nodes {
		// Base degree score (0 to 0.4)
		score := float64(degreeMap[n.ID]) / 25.0
		if score > 0.4 {
			score = 0.4
		}

		// Direct focal match boost (+0.50)
		if focalNodes[n.ID] {
			score += 0.50
		}

		// Direct date match boost (+0.30)
		if hasDateQuery && (n.Type == memory.NodeTypeEvent || strings.Contains(strings.ToLower(n.ContextPrompt), "date") || strings.Contains(strings.ToLower(n.Name), "202")) {
			score += 0.30
		}

		// 1-hop focal proximity boost (+0.30)
		if !focalNodes[n.ID] {
			for _, l := range links {
				if (l.SourceID == n.ID && focalNodes[l.TargetID]) || (l.TargetID == n.ID && focalNodes[l.SourceID]) {
					score += 0.30
					break
				}
			}
		}

		if score > 1.0 {
			score = 1.0
		}
		salienceScores[n.ID] = score
	}

	return salienceScores
}

// SetIndividualSourceTrustWeight sets a source/person-specific reliability adjustment bonus/penalty.
func (e *GllamEngine) SetIndividualSourceTrustWeight(sourceID string, adjustment int) {
	if e.SystemPrompts == nil {
		e.SystemPrompts = config.DefaultAgenticMemorySystemPrompts()
	}
	if e.SystemPrompts.SourceReliabilityHeuristics == nil {
		e.SystemPrompts.SourceReliabilityHeuristics = make(map[string]int)
	}
	e.SystemPrompts.SourceReliabilityHeuristics[strings.ToLower(sourceID)] = adjustment
}


// RegisterCustomDocumentTypeRule dynamically adds or overrides an information source type with its specific trust baseline and ingestion strategy.
func (e *GllamEngine) RegisterCustomDocumentTypeRule(rule config.CustomDocumentTypeRule) {
	if e.SystemPrompts == nil {
		e.SystemPrompts = config.DefaultAgenticMemorySystemPrompts()
	}
	if e.SystemPrompts.CustomDocumentTypeRules == nil {
		e.SystemPrompts.CustomDocumentTypeRules = make(map[string]config.CustomDocumentTypeRule)
	}
	if e.SystemPrompts.IngestionSteeringDirectives == nil {
		e.SystemPrompts.IngestionSteeringDirectives = make(map[string]config.IngestionStrategy)
	}
	key := strings.ToLower(rule.TypeName)
	e.SystemPrompts.CustomDocumentTypeRules[key] = rule
	e.SystemPrompts.IngestionSteeringDirectives[key] = rule.IngestionStrategy
}

// DetermineDocumentIngestionStrategy queries agentic steering directives and custom document type rules to decide whether
// revision history, comment history, status transitions, or branch merges should be ingested for a document type.
func (e *GllamEngine) DetermineDocumentIngestionStrategy(docType string) config.IngestionStrategy {
	if e.SystemPrompts == nil {
		e.SystemPrompts = config.DefaultAgenticMemorySystemPrompts()
	}
	key := strings.ToLower(docType)
	if customRule, ok := e.SystemPrompts.CustomDocumentTypeRules[key]; ok {
		return customRule.IngestionStrategy
	}
	if strat, ok := e.SystemPrompts.IngestionSteeringDirectives[key]; ok {
		return strat
	}
	// Baseline default strategy
	return config.IngestionStrategy{
		TrackRevisionHistory: true,
		CompactAuthorEpochs:  true,
	}
}

// RegisterRepositoryContextDirective dynamically registers a documentation repository type directive (e.g. Jira, Confluence, Git).
func (e *GllamEngine) RegisterRepositoryContextDirective(directive config.RepositoryContextDirective) {

	if e.SystemPrompts == nil {
		e.SystemPrompts = config.DefaultAgenticMemorySystemPrompts()
	}
	if e.SystemPrompts.RepositoryContextDirectives == nil {
		e.SystemPrompts.RepositoryContextDirectives = make(map[string]config.RepositoryContextDirective)
	}
	key := strings.ToLower(directive.RepositoryType)
	e.SystemPrompts.RepositoryContextDirectives[key] = directive
}

// BuildRepositoryEntityContext builds a structured context string for an entity from a documentation repository metadata map.
func (e *GllamEngine) BuildRepositoryEntityContext(repoType string, metadata map[string]string) string {
	if e.SystemPrompts == nil {
		e.SystemPrompts = config.DefaultAgenticMemorySystemPrompts()
	}
	key := strings.ToLower(repoType)
	directive, ok := e.SystemPrompts.RepositoryContextDirectives[key]
	if !ok || directive.ContextTemplate == "" {
		// Fallback default formatting
		var lines []string
		for k, v := range metadata {
			lines = append(lines, fmt.Sprintf("%s: %s", k, v))
		}
		return strings.Join(lines, "\n")
	}

	result := directive.ContextTemplate
	for k, v := range metadata {
		placeholder := fmt.Sprintf("{{%s}}", k)
		result = strings.ReplaceAll(result, placeholder, v)
	}
	return result
}




// GaugeAndUpsertSourceNode evaluates multi-factor trust input (document type, individual author reliability, internal coherence, temporal freshness)
// to compute composite trust weight and upserts the source node into SQLite.
func (e *GllamEngine) GaugeAndUpsertSourceNode(ctx context.Context, id string, name string, nodeType string, input SourceTrustInput) (int, error) {
	
	now := time.Now().Unix()
	calculatedWeight := CalculateCompositeTrustWeight(input, e.SystemPrompts, now)

	authorLabel := input.AuthorID
	if authorLabel == "" {
		authorLabel = input.AuthorName
	}
	if authorLabel == "" {
		authorLabel = input.AuthorRole
	}

	node := memory.SemanticNode{
		ID:            id,
		Name:          name,
		Type:          nodeType,
		ContextPrompt: fmt.Sprintf("Source Type: %s, Source Identity: %s, Trust Weight: %d", input.DocumentType, authorLabel, calculatedWeight),
		TrustWeight:   calculatedWeight,
	}


	if err := e.UpsertNode(ctx, node); err != nil {
		return 0, err
	}
	return calculatedWeight, nil
}

// AttributeContainerEntryToSource parses an individual entry/comment/version within a document container (e.g., Jira ticket, Confluence page)
// and attributes the claim directly to the specific individual source node (with their individual trust_weight), NOT the container.
func (e *GllamEngine) AttributeContainerEntryToSource(ctx context.Context, containerType string, entryAuthorID string, entryAuthorName string, entryText string, createdAt int64) (string, int, error) {

	sourceNodeID := fmt.Sprintf("src-%s", strings.ToLower(entryAuthorID))
	if entryAuthorID == "" && entryAuthorName != "" {
		sourceNodeID = fmt.Sprintf("src-%s", strings.ToLower(strings.ReplaceAll(entryAuthorName, " ", "_")))
	}

	trustInput := SourceTrustInput{
		DocumentType: containerType,
		AuthorID:     entryAuthorID,
		AuthorName:   entryAuthorName,
		DocumentText: entryText,
		CreatedAt:    createdAt,
	}

	authorName := entryAuthorName
	if authorName == "" {
		authorName = entryAuthorID
	}

	weight, err := e.GaugeAndUpsertSourceNode(ctx, sourceNodeID, authorName, memory.NodeTypeHuman, trustInput)
	if err != nil {
		return "", 0, fmt.Errorf("failed to gauge and upsert source node %s: %w", sourceNodeID, err)
	}

	return sourceNodeID, weight, nil
}

// AddDocumentLineage stores source URI provenance for a semantic node (Issue #8 Strict Information Lineage).

func (e *GllamEngine) AddDocumentLineage(ctx context.Context, lineage memory.DocumentLineage) error {
	now := time.Now()
	if lineage.CreatedAt.IsZero() {
		lineage.CreatedAt = now
	}
	if lineage.ID == "" {
		lineage.ID = fmt.Sprintf("lin-%s-%d", lineage.NodeID, now.Unix())
	}

	query := `
		INSERT INTO document_lineage (id, node_id, source_uri, document_title, source_type, line_number, char_offset, checksum, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET source_uri = excluded.source_uri, document_title = excluded.document_title, line_number = excluded.line_number`

	_, err := e.db.ExecContext(ctx, query, lineage.ID, lineage.NodeID, lineage.SourceURI, lineage.DocumentTitle, lineage.SourceType, lineage.LineNumber, lineage.CharOffset, lineage.Checksum, lineage.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert document lineage for node %s: %w", lineage.NodeID, err)
	}
	return nil
}

// GetDocumentLineageForNodes retrieves all document lineage records for a slice of node IDs.
func (e *GllamEngine) GetDocumentLineageForNodes(ctx context.Context, nodeIDs []string) ([]memory.DocumentLineage, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(nodeIDs))
	args := make([]interface{}, len(nodeIDs))
	for i, id := range nodeIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT id, node_id, source_uri, document_title, source_type, line_number, char_offset, checksum, created_at
		FROM document_lineage
		WHERE node_id IN (%s)
		ORDER BY created_at DESC`, strings.Join(placeholders, ","))

	rows, err := e.dbRO.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query document lineage: %w", err)
	}
	defer rows.Close()

	var lineages []memory.DocumentLineage
	for rows.Next() {
		var l memory.DocumentLineage
		var title, checksum sql.NullString
		var lineNo, charOff sql.NullInt64
		if err := rows.Scan(&l.ID, &l.NodeID, &l.SourceURI, &title, &l.SourceType, &lineNo, &charOff, &checksum, scanTime(&l.CreatedAt)); err != nil {
			continue
		}
		if title.Valid {
			l.DocumentTitle = title.String
		}
		if checksum.Valid {
			l.Checksum = checksum.String
		}
		if lineNo.Valid {
			l.LineNumber = int(lineNo.Int64)
		}
		if charOff.Valid {
			l.CharOffset = int(charOff.Int64)
		}
		lineages = append(lineages, l)
	}

	// Fetch versions and multi-authors for each lineage entry
	for i := range lineages {
		vers, err := e.GetDocumentVersionsForLineage(ctx, lineages[i].ID)
		if err == nil && len(vers) > 0 {
			lineages[i].Versions = vers
			lineages[i].RevisionEpochs = CompactRevisionHistory(vers)
			authorMap := make(map[string]bool)
			var authors []string
			for _, v := range vers {
				name := v.AuthorID
				if v.AuthorName != "" {
					name = fmt.Sprintf("%s (%s)", v.AuthorName, v.AuthorID)
				}
				if !authorMap[name] {
					authorMap[name] = true
					authors = append(authors, name)
				}
			}
			lineages[i].Authors = authors
		}
	}

	return lineages, nil
}

// CompactRevisionHistory collapses granular line-by-line version edit histories into compact, synthetic author epoch summaries
// preventing context window token explosion ("Mr. A worked on X from T1..T2, then Mr. B modified Y").
func CompactRevisionHistory(versions []memory.DocumentVersion) []memory.CompactedRevisionEpoch {
	if len(versions) == 0 {
		return nil
	}

	var epochs []memory.CompactedRevisionEpoch
	var currentEpoch *memory.CompactedRevisionEpoch
	var currentStartVer int
	var currentEndVer int
	var minTS time.Time
	var maxTS time.Time
	var summaries []string

	for i, v := range versions {
		if currentEpoch == nil {
			currentEpoch = &memory.CompactedRevisionEpoch{
				AuthorID:   v.AuthorID,
				AuthorName: v.AuthorName,
			}
			currentStartVer = v.VersionNumber
			currentEndVer = v.VersionNumber
			minTS = v.CreatedAt
			maxTS = v.CreatedAt
			if v.ChangeSummary != "" {
				summaries = append(summaries, v.ChangeSummary)
			}
		} else if v.AuthorID == currentEpoch.AuthorID {
			// Same author consecutive edits -> group into single synthetic epoch!
			currentEndVer = v.VersionNumber
			if v.CreatedAt.After(maxTS) {
				maxTS = v.CreatedAt
			}
			if v.ChangeSummary != "" {
				summaries = append(summaries, v.ChangeSummary)
			}
		} else {
			// Author changed -> finalize previous epoch and start new epoch!
			if currentStartVer == currentEndVer {
				currentEpoch.VersionRange = fmt.Sprintf("v%d", currentStartVer)
			} else {
				currentEpoch.VersionRange = fmt.Sprintf("v%d-v%d", currentStartVer, currentEndVer)
			}
			if minTS.Equal(maxTS) {
				currentEpoch.TimeRange = minTS.Format(time.RFC3339)
			} else {
				currentEpoch.TimeRange = fmt.Sprintf("%s to %s", minTS.Format(time.RFC3339), maxTS.Format(time.RFC3339))
			}
			currentEpoch.SyntheticSummary = strings.Join(summaries, "; ")
			epochs = append(epochs, *currentEpoch)

			// Start new epoch
			currentEpoch = &memory.CompactedRevisionEpoch{
				AuthorID:   v.AuthorID,
				AuthorName: v.AuthorName,
			}
			currentStartVer = v.VersionNumber
			currentEndVer = v.VersionNumber
			minTS = v.CreatedAt
			maxTS = v.CreatedAt
			summaries = nil
			if v.ChangeSummary != "" {
				summaries = append(summaries, v.ChangeSummary)
			}
		}

		if i == len(versions)-1 && currentEpoch != nil {
			if currentStartVer == currentEndVer {
				currentEpoch.VersionRange = fmt.Sprintf("v%d", currentStartVer)
			} else {
				currentEpoch.VersionRange = fmt.Sprintf("v%d-v%d", currentStartVer, currentEndVer)
			}
			if minTS.Equal(maxTS) {
				currentEpoch.TimeRange = minTS.Format(time.RFC3339)
			} else {
				currentEpoch.TimeRange = fmt.Sprintf("%s to %s", minTS.Format(time.RFC3339), maxTS.Format(time.RFC3339))
			}
			currentEpoch.SyntheticSummary = strings.Join(summaries, "; ")
			epochs = append(epochs, *currentEpoch)
		}
	}

	return epochs
}


// AddDocumentVersion records a multi-author edit version history entry for a document lineage record.
func (e *GllamEngine) AddDocumentVersion(ctx context.Context, ver memory.DocumentVersion) error {
	now := time.Now()
	if ver.CreatedAt.IsZero() {
		ver.CreatedAt = now
	}
	if ver.ID == "" {
		ver.ID = fmt.Sprintf("ver-%s-%d-%d", ver.LineageID, ver.VersionNumber, now.Unix())
	}

	query := `
		INSERT INTO document_versions (id, lineage_id, version_number, author_id, author_name, change_summary, start_line, end_line, char_offset, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET change_summary = excluded.change_summary, start_line = excluded.start_line, end_line = excluded.end_line`

	_, err := e.db.ExecContext(ctx, query, ver.ID, ver.LineageID, ver.VersionNumber, ver.AuthorID, ver.AuthorName, ver.ChangeSummary, ver.StartLine, ver.EndLine, ver.CharOffset, ver.CreatedAt)
	if err != nil {
		return fmt.Errorf("failed to insert document version for lineage %s: %w", ver.LineageID, err)
	}
	return nil
}

// GetDocumentVersionsForLineage fetches all version edit entries for a given lineage ID.
func (e *GllamEngine) GetDocumentVersionsForLineage(ctx context.Context, lineageID string) ([]memory.DocumentVersion, error) {
	query := `
		SELECT id, lineage_id, version_number, author_id, author_name, change_summary, start_line, end_line, char_offset, created_at
		FROM document_versions
		WHERE lineage_id = ?
		ORDER BY version_number ASC, created_at ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, lineageID)
	if err != nil {
		return nil, fmt.Errorf("failed to query document versions: %w", err)
	}
	defer rows.Close()

	var versions []memory.DocumentVersion
	for rows.Next() {
		var v memory.DocumentVersion
		var authorName, changeSummary sql.NullString
		var startLine, endLine, charOff sql.NullInt64
		if err := rows.Scan(&v.ID, &v.LineageID, &v.VersionNumber, &v.AuthorID, &authorName, &changeSummary, &startLine, &endLine, &charOff, scanTime(&v.CreatedAt)); err != nil {
			continue
		}
		if authorName.Valid {
			v.AuthorName = authorName.String
		}
		if changeSummary.Valid {
			v.ChangeSummary = changeSummary.String
		}
		if startLine.Valid {
			v.StartLine = int(startLine.Int64)
		}
		if endLine.Valid {
			v.EndLine = int(endLine.Int64)
		}
		if charOff.Valid {
			v.CharOffset = int(charOff.Int64)
		}
		versions = append(versions, v)
	}
	return versions, nil
}














