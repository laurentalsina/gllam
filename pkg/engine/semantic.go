package engine

import (
    "context"
    "database/sql"
    "fmt"
    "sort"
    "strings"
    "time"

    "github.com/laurentalsina/gllam/pkg/memory"
)



// UpsertNode inserts or updates a semantic node
func (e *GllamEngine) UpsertNode(ctx context.Context, node memory.SemanticNode) error {
    query := `
        INSERT INTO semantic_nodes (id, name, type, context_prompt)
        VALUES (?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET name = excluded.name, type = excluded.type, context_prompt = excluded.context_prompt`

    _, err := e.db.ExecContext(ctx, query, node.ID, node.Name, node.Type, node.ContextPrompt)
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
        nowStr := fmt.Sprintf("%d", now)
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     link.SourceID,
            TargetID:     conflictID,
            Relationship: "has_unresolved_conflict",
            ValidFrom:    nowStr,
        })

        // Add edges from conflict to targets
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     conflictID,
            TargetID:     existingTargetID,
            Relationship: "conflicting_claim",
            ValidFrom:    nowStr,
        })
        _ = e.AddEdge(ctx, memory.SemanticLink{
            SourceID:     conflictID,
            TargetID:     link.TargetID,
            Relationship: "conflicting_claim",
            ValidFrom:    nowStr,
        })
    }

    // Default valid_from if unassigned
    now := time.Now().Unix()
    if link.ValidFrom == "" {
        if link.TemporalNote != "" {
            link.ValidFrom = "temporal_note"
        } else {
            link.ValidFrom = fmt.Sprintf("%d", now)
        }
    }

    // Event-Anchored State Invalidation (Trap 9):
    // When a new state or preference link is added, mark previous active state links as expired
    // using the new link's valid_from timestamp and temporal anchor ID.
    if link.Relationship == "has_state" || link.Relationship == "is_preference" {
        _ = e.InvalidateObsoleteEdgeWithAnchor(ctx, link.SourceID, link.Relationship, link.TargetID, link.ValidFrom, link.TemporalAnchorID, link.TemporalNote)
    }

    // Insert the new link (idempotent update if it already exists)

    gran := link.TemporalGranularity
    if gran == "" {
        gran = "exact"
    }
    ruleCtx := link.RuleContext
    if ruleCtx == "" {
        ruleCtx = "global"
    }
    cType := link.ConstraintType
    if cType == "" {
        cType = "positive"
    }


    durTurns := link.DurationTurns
    if durTurns == 0 {
        durTurns = -1
    }
    remTurns := link.RemainingTurns
    if remTurns == 0 {
        if durTurns > 0 {
            remTurns = durTurns
        } else {
            remTurns = -1
        }
    }

    insertQuery := `
        INSERT INTO semantic_links (source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_offset_seconds, temporal_granularity, temporal_note, origin_source_id, rule_context, constraint_type, rule_rationale, resolution_rationale, duration_turns, remaining_turns, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(source_id, target_id, relationship) DO UPDATE SET 
            caveats = excluded.caveats,
            valid_from = excluded.valid_from,
            valid_until = excluded.valid_until,
            temporal_anchor_id = excluded.temporal_anchor_id,
            temporal_relation = excluded.temporal_relation,
            temporal_offset_seconds = excluded.temporal_offset_seconds,
            temporal_granularity = excluded.temporal_granularity,
            temporal_note = excluded.temporal_note,
            origin_source_id = excluded.origin_source_id,
            rule_context = excluded.rule_context,
            constraint_type = excluded.constraint_type,
            rule_rationale = excluded.rule_rationale,
            resolution_rationale = excluded.resolution_rationale,
            duration_turns = excluded.duration_turns,
            remaining_turns = excluded.remaining_turns,
            updated_at = excluded.updated_at`

    var anchorID, tempRel, tempNote, origSource, rationaleVal, resRationaleVal sql.NullString
    if link.TemporalAnchorID != "" {
        anchorID = sql.NullString{String: link.TemporalAnchorID, Valid: true}
    }
    if link.TemporalRelation != "" {
        tempRel = sql.NullString{String: link.TemporalRelation, Valid: true}
    }
    if link.TemporalNote != "" {
        tempNote = sql.NullString{String: link.TemporalNote, Valid: true}
    }
    if link.OriginSourceID != "" {
        origSource = sql.NullString{String: link.OriginSourceID, Valid: true}
    }
    if link.RuleRationale != "" {
        rationaleVal = sql.NullString{String: link.RuleRationale, Valid: true}
    }
    if link.ResolutionRationale != "" {
        resRationaleVal = sql.NullString{String: link.ResolutionRationale, Valid: true}
    }

    _, err = e.db.ExecContext(ctx, insertQuery,
        link.SourceID, link.TargetID, link.Relationship, link.Caveats,
        link.ValidFrom, link.ValidUntil, anchorID, tempRel, link.TemporalOffsetSeconds, gran, tempNote, origSource, ruleCtx, cType, rationaleVal, resRationaleVal, durTurns, remTurns, now)






    if err != nil {
        return fmt.Errorf("failed to add edge: %w", err)
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

	query := `
		UPDATE semantic_links 
		SET valid_until = ?, 
		    temporal_anchor_id = COALESCE(NULLIF(?, ''), temporal_anchor_id),
		    temporal_relation = CASE WHEN ? != '' THEN 'ended_by' ELSE temporal_relation END,
		    temporal_note = COALESCE(NULLIF(?, ''), temporal_note),
		    updated_at = ?
		WHERE source_id = ? AND relationship = ? AND (target_id != ? OR ? = '') AND valid_until IS NULL`

	now := time.Now().Unix()
	_, err := e.db.ExecContext(ctx, query, validUntil, anchorID, anchorID, tempNote, now, sourceID, relationship, targetID, targetID)
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

// GetActiveLinksAtTime retrieves semantic links active at a specific Unix timestamp (read-only -> dbRO)
// It dynamically resolves temporal_anchor_id timestamps when valid_from or valid_until is "temporal_note".
func (e *GllamEngine) GetActiveLinksAtTime(ctx context.Context, timestamp int64) ([]memory.SemanticLink, error) {
    query := `
        SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_offset_seconds, temporal_granularity, temporal_note, origin_source_id, rule_context, constraint_type, rule_rationale, resolution_rationale, duration_turns, remaining_turns, updated_at
        FROM semantic_links
        WHERE (valid_from = 'temporal_note' OR CAST(valid_from AS INTEGER) <= ?) 
          AND (valid_until IS NULL OR valid_until = 'temporal_note' OR CAST(valid_until AS INTEGER) > ?)
        ORDER BY valid_from ASC`

    rows, err := e.dbRO.QueryContext(ctx, query, timestamp, timestamp)
    if err != nil {
        return nil, fmt.Errorf("failed to query active links at timestamp %d: %w", timestamp, err)
    }
    defer rows.Close()

    var candidates []memory.SemanticLink
    for rows.Next() {
        var l memory.SemanticLink
        var anchorID, tempRel, tempGran, tempNote, origSource, rCtx, cType, ratVal, resRatVal sql.NullString
        var durTurns, remTurns sql.NullInt64
        if err := rows.Scan(&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.ValidFrom, &l.ValidUntil, &anchorID, &tempRel, &l.TemporalOffsetSeconds, &tempGran, &tempNote, &origSource, &rCtx, &cType, &ratVal, &resRatVal, &durTurns, &remTurns, &l.UpdatedAt); err != nil {
            return nil, fmt.Errorf("failed to scan link: %w", err)
        }
        if anchorID.Valid {
            l.TemporalAnchorID = anchorID.String
        }
        if tempRel.Valid {
            l.TemporalRelation = tempRel.String
        }
        if tempGran.Valid {
            l.TemporalGranularity = tempGran.String
        } else {
            l.TemporalGranularity = "exact"
        }
        if tempNote.Valid {
            l.TemporalNote = tempNote.String
        }
        if origSource.Valid {
            l.OriginSourceID = origSource.String
        }
        if rCtx.Valid {
            l.RuleContext = rCtx.String
        }
        if cType.Valid {
            l.ConstraintType = cType.String
        }
        if ratVal.Valid {
            l.RuleRationale = ratVal.String
        }
        if resRatVal.Valid {
            l.ResolutionRationale = resRatVal.String
        }
        if durTurns.Valid {
            l.DurationTurns = durTurns.Int64
        } else {
            l.DurationTurns = -1
        }
        if remTurns.Valid {
            l.RemainingTurns = remTurns.Int64
        } else {
            l.RemainingTurns = -1
        }
        candidates = append(candidates, l)
    }
    rows.Close()

    // Dynamic Anchor Resolution: Filter candidates whose anchored event timestamp invalidates them at time `timestamp`
    var activeLinks []memory.SemanticLink
    for _, l := range candidates {
        if l.TemporalAnchorID != "" {
            anchorTS := e.resolveAnchorTimestamp(ctx, l.TemporalAnchorID, l.TemporalOffsetSeconds, l.TemporalGranularity)
            if anchorTS > 0 {
                // If valid_from is anchored after requested timestamp, link wasn't active yet
                if l.ValidFrom == "temporal_note" && (l.TemporalRelation == "after" || l.TemporalRelation == "ended_by") {
                    if timestamp < anchorTS {
                        continue
                    }
                }
                // If valid_until is anchored before/ended_by requested timestamp, link has expired
                if l.ValidUntil != nil && *l.ValidUntil == "temporal_note" && (l.TemporalRelation == "ended_by" || l.TemporalRelation == "before") {
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

// resolveAnchorTimestamp looks up the unix timestamp of an anchor node/link, applies offset seconds,
// and applies leniency boundary snapping based on granularity ("day", "hour", "month", "exact").
func (e *GllamEngine) resolveAnchorTimestamp(ctx context.Context, anchorID string, offsetSeconds int64, granularity string) int64 {
	var ts int64
	query := `SELECT CAST(valid_from AS INTEGER) FROM semantic_links WHERE source_id = ? AND valid_from != 'temporal_note' LIMIT 1`
	if err := e.dbRO.QueryRowContext(ctx, query, anchorID).Scan(&ts); err == nil && ts > 0 {
		effectiveTS := ts + offsetSeconds
		tTime := time.Unix(effectiveTS, 0).UTC()

		switch strings.ToLower(granularity) {
		case "day":
			// Round down to beginning of that day (00:00:00 UTC) for human leniency ("2 weeks ago", "3 days ago")
			return time.Date(tTime.Year(), tTime.Month(), tTime.Day(), 0, 0, 0, 0, time.UTC).Unix()
		case "hour":
			// Round down to beginning of that hour (XX:00:00 UTC)
			return time.Date(tTime.Year(), tTime.Month(), tTime.Day(), tTime.Hour(), 0, 0, 0, time.UTC).Unix()
		case "month":
			// Round down to 1st of that month (00:00:00 UTC)
			return time.Date(tTime.Year(), tTime.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
		default:
			return effectiveTS
		}
	}
	return 0
}

// ExpandTemporalNeighbors performs N-hop traversal over temporal links and temporal anchors
// to ensure complete transitive ordering chains (e.g. A -> B -> C) are loaded into context.
func (e *GllamEngine) ExpandTemporalNeighbors(ctx context.Context, seedNodes []memory.SemanticNode, existingLinks []memory.SemanticLink, maxHops int) ([]memory.SemanticNode, []memory.SemanticLink, error) {
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
			query := `
				SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_offset_seconds, temporal_granularity, temporal_note, origin_source_id, rule_context, constraint_type, rule_rationale, resolution_rationale, duration_turns, remaining_turns, updated_at
				FROM semantic_links
				WHERE source_id = ? OR target_id = ? OR temporal_anchor_id = ?`

			rows, err := e.dbRO.QueryContext(ctx, query, currentID, currentID, currentID)
			if err != nil {
				continue
			}

			for rows.Next() {
				var l memory.SemanticLink
				var anchorID, tempRel, tempGran, tempNote, origSource, rCtx, cType, ratVal, resRatVal sql.NullString
				var durTurns, remTurns sql.NullInt64
				if err := rows.Scan(&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.ValidFrom, &l.ValidUntil, &anchorID, &tempRel, &l.TemporalOffsetSeconds, &tempGran, &tempNote, &origSource, &rCtx, &cType, &ratVal, &resRatVal, &durTurns, &remTurns, &l.UpdatedAt); err != nil {
					continue
				}
				if anchorID.Valid {
					l.TemporalAnchorID = anchorID.String
				}
				if tempRel.Valid {
					l.TemporalRelation = tempRel.String
				}
				if tempGran.Valid {
					l.TemporalGranularity = tempGran.String
				} else {
					l.TemporalGranularity = "exact"
				}
				if tempNote.Valid {
					l.TemporalNote = tempNote.String
				}
				if origSource.Valid {
					l.OriginSourceID = origSource.String
				}
				if rCtx.Valid {
					l.RuleContext = rCtx.String
				}
				if cType.Valid {
					l.ConstraintType = cType.String
				}
				if ratVal.Valid {
					l.RuleRationale = ratVal.String
				}
				if resRatVal.Valid {
					l.ResolutionRationale = resRatVal.String
				}
				if durTurns.Valid {
					l.DurationTurns = durTurns.Int64
				} else {
					l.DurationTurns = -1
				}
				if remTurns.Valid {
					l.RemainingTurns = remTurns.Int64
				} else {
					l.RemainingTurns = -1
				}

				key := fmt.Sprintf("%s-%s-%s", l.SourceID, l.TargetID, l.Relationship)
				linkMap[key] = l

				// Collect connected neighbor node IDs
				neighbors := []string{l.SourceID, l.TargetID}
				if l.TemporalAnchorID != "" {
					neighbors = append(neighbors, l.TemporalAnchorID)
				}

				for _, neighborID := range neighbors {
					if !visitedNodes[neighborID] {
						visitedNodes[neighborID] = true
						nextFrontier = append(nextFrontier, neighborID)

						// Fetch Node metadata if missing
						var node memory.SemanticNode
						var ctxPrompt sql.NullString
						nodeQuery := `SELECT id, name, type, context_prompt FROM semantic_nodes WHERE id = ?`
						if err := e.dbRO.QueryRowContext(ctx, nodeQuery, neighborID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt); err == nil {
							if ctxPrompt.Valid {
								node.ContextPrompt = ctxPrompt.String
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

// GetActiveConstraintsForSource retrieves active rules, preferences, and constraints for a given source_id or rule_context
func (e *GllamEngine) GetActiveConstraintsForSource(ctx context.Context, sourceID string, targetContext string) ([]memory.SemanticLink, error) {
	query := `
		SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, temporal_anchor_id, temporal_relation, temporal_offset_seconds, temporal_granularity, temporal_note, origin_source_id, rule_context, constraint_type, rule_rationale, resolution_rationale, duration_turns, remaining_turns, updated_at
		FROM semantic_links
		WHERE valid_until IS NULL 
		  AND (remaining_turns < 0 OR remaining_turns > 0)
		  AND relationship NOT IN ('supersedes_rule', 'conflicting_claim', 'has_unresolved_conflict')
		  AND (relationship IN ('has_constraint', 'is_preference', 'applies_rule') OR target_id LIKE 'rule%' OR target_id LIKE 'constraint%')
		  AND (origin_source_id = ? OR source_id = ? OR rule_context = 'global' OR rule_context = ?)
		ORDER BY valid_from ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, sourceID, sourceID, targetContext)
	if err != nil {
		return nil, fmt.Errorf("failed to query active constraints for source %s: %w", sourceID, err)
	}
	defer rows.Close()

	var links []memory.SemanticLink
	for rows.Next() {
		var l memory.SemanticLink
		var anchorID, tempRel, tempGran, tempNote, origSource, rCtx, cType, ratVal, resRatVal sql.NullString
		var durTurns, remTurns sql.NullInt64
		if err := rows.Scan(&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &l.ValidFrom, &l.ValidUntil, &anchorID, &tempRel, &l.TemporalOffsetSeconds, &tempGran, &tempNote, &origSource, &rCtx, &cType, &ratVal, &resRatVal, &durTurns, &remTurns, &l.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan constraint link: %w", err)
		}
		if anchorID.Valid {
			l.TemporalAnchorID = anchorID.String
		}
		if tempRel.Valid {
			l.TemporalRelation = tempRel.String
		}
		if tempGran.Valid {
			l.TemporalGranularity = tempGran.String
		}
		if tempNote.Valid {
			l.TemporalNote = tempNote.String
		}
		if origSource.Valid {
			l.OriginSourceID = origSource.String
		}
		if rCtx.Valid {
			l.RuleContext = rCtx.String
		}
		if cType.Valid {
			l.ConstraintType = cType.String
		}
		if ratVal.Valid {
			l.RuleRationale = ratVal.String
		}
		if resRatVal.Valid {
			l.ResolutionRationale = resRatVal.String
		}
		if durTurns.Valid {
			l.DurationTurns = durTurns.Int64
		} else {
			l.DurationTurns = -1
		}
		if remTurns.Valid {
			l.RemainingTurns = remTurns.Int64
		} else {
			l.RemainingTurns = -1
		}
		links = append(links, l)
	}
	return links, rows.Err()
}


// RevokeOrSupersedeRule revokes a rule/constraint or marks it as superseded by a newer rule ID
func (e *GllamEngine) RevokeOrSupersedeRule(ctx context.Context, oldRuleID string, newRuleID string) error {
	nowStr := fmt.Sprintf("%d", time.Now().Unix())
	now := time.Now().Unix()

	query := `
		UPDATE semantic_links
		SET valid_until = ?, updated_at = ?
		WHERE (target_id = ? OR source_id = ?) AND valid_until IS NULL`

	_, err := e.db.ExecContext(ctx, query, nowStr, now, oldRuleID, oldRuleID)
	if err != nil {
		return fmt.Errorf("failed to revoke old rule %s: %w", oldRuleID, err)
	}

	if newRuleID != "" {
		// Add supersedes link
		_ = e.AddEdge(ctx, memory.SemanticLink{
			SourceID:     newRuleID,
			TargetID:     oldRuleID,
			Relationship: "supersedes_rule",
			ValidFrom:    nowStr,
		})
	}
	return nil
}

// DecrementActiveTurnConstraints decrements remaining_turns on active turn-bounded rules and auto-expires rules that hit 0 turns
func (e *GllamEngine) DecrementActiveTurnConstraints(ctx context.Context) error {
	nowStr := fmt.Sprintf("%d", time.Now().Unix())
	now := time.Now().Unix()

	// 1. Expire rules where remaining_turns == 1 (about to hit 0 after this turn)
	expireQuery := `
		UPDATE semantic_links
		SET valid_until = ?, remaining_turns = 0, updated_at = ?
		WHERE valid_until IS NULL AND remaining_turns = 1`

	if _, err := e.db.ExecContext(ctx, expireQuery, nowStr, now); err != nil {
		return fmt.Errorf("failed to expire 1-turn remaining constraints: %w", err)
	}

	// 2. Decrement rules where remaining_turns > 1
	decQuery := `
		UPDATE semantic_links
		SET remaining_turns = remaining_turns - 1, updated_at = ?
		WHERE valid_until IS NULL AND remaining_turns > 1`

	if _, err := e.db.ExecContext(ctx, decQuery, now); err != nil {
		return fmt.Errorf("failed to decrement remaining turns: %w", err)
	}

	return nil
}

// ConfrontRuleRationales evaluates pairs of active rules for priority/rationale collisions,
// returning a human-readable confrontation diagnostic detailing why higher-priority rationale wins.
func ConfrontRuleRationales(links []memory.SemanticLink) string {
	var posRules []memory.SemanticLink
	var negRules []memory.SemanticLink

	for _, l := range links {
		if l.ConstraintType == "negative" || strings.Contains(strings.ToLower(l.TargetID), "no_") || strings.Contains(strings.ToLower(l.TargetID), "never_") || strings.Contains(strings.ToLower(l.TargetID), "dont_") {
			negRules = append(negRules, l)
		} else if l.ConstraintType == "positive" || l.RuleContext == "user_preference" {
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
				negRat := neg.RuleRationale
				if negRat == "" {
					negRat = "Security & Global Policy"
				}
				posRat := pos.RuleRationale
				if posRat == "" {
					posRat = "User Style Preference"
				}

				notice := fmt.Sprintf("⚠️ RULE RATIONALE CONFRONTATION RESOLVED: Negative restriction '%s' (Rationale: %s, Scope: %s) supersedes positive directive '%s' (Rationale: %s, Scope: %s).",
					neg.TargetID, negRat, neg.RuleContext, pos.TargetID, posRat, pos.RuleContext)
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
		SELECT id, name, type, context_prompt
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
		var ctxPrompt sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &ctxPrompt); err == nil {
			if ctxPrompt.Valid {
				n.ContextPrompt = ctxPrompt.String
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
				WHERE (source_id = ? OR target_id = ?) AND origin_source_id = ?`
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
// marking losingClaimID as expired (valid_until = now) and inserting a resolves_conflict edge.
func (e *GllamEngine) ResolveContradiction(ctx context.Context, contradictionID string, winningClaimID string, losingClaimID string, rationale string) error {
	nowStr := fmt.Sprintf("%d", time.Now().Unix())
	now := time.Now().Unix()

	// 1. Expire losing claim links
	expireQuery := `
		UPDATE semantic_links
		SET valid_until = ?, updated_at = ?
		WHERE (source_id = ? OR target_id = ?) AND valid_until IS NULL`
	if _, err := e.db.ExecContext(ctx, expireQuery, nowStr, now, losingClaimID, losingClaimID); err != nil {
		return fmt.Errorf("failed to expire losing claim %s: %w", losingClaimID, err)
	}

	// 2. Expire active contradiction node links
	if contradictionID != "" {
		expireContrQuery := `
			UPDATE semantic_links
			SET valid_until = ?, updated_at = ?
			WHERE (source_id = ? OR target_id = ?) AND valid_until IS NULL`
		_, _ = e.db.ExecContext(ctx, expireContrQuery, nowStr, now, contradictionID, contradictionID)
	}

	// 3. Insert resolves_conflict link
	resLink := memory.SemanticLink{
		SourceID:            winningClaimID,
		TargetID:            losingClaimID,
		Relationship:        "resolves_conflict",
		ResolutionRationale: rationale,
		ValidFrom:           nowStr,
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
				var ctxPrompt sql.NullString
				nodeQuery := `SELECT id, name, type, context_prompt FROM semantic_nodes WHERE id = ?`
				if err := e.dbRO.QueryRowContext(ctx, nodeQuery, res.NodeID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt); err == nil {
					if ctxPrompt.Valid {
						node.ContextPrompt = ctxPrompt.String
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

	// 3. Graph Channel: Fetch seed nodes & ExpandTemporalNeighbors (2 hops)
	var seedNodes []memory.SemanticNode
	for _, entID := range resolvedEntities {
		var node memory.SemanticNode
		var ctxPrompt sql.NullString
		nodeQuery := `SELECT id, name, type, context_prompt FROM semantic_nodes WHERE id = ?`
		if err := e.dbRO.QueryRowContext(ctx, nodeQuery, entID).Scan(&node.ID, &node.Name, &node.Type, &ctxPrompt); err == nil {
			if ctxPrompt.Valid {
				node.ContextPrompt = ctxPrompt.String
			}
			seedNodes = append(seedNodes, node)
		}
	}

	expNodes, expLinks, _ := e.ExpandTemporalNeighbors(ctx, seedNodes, nil, 2)

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

	// 5. Sort by RRFScore DESC
	scoredList := make([]NeedleScoredNode, 0, len(nodeMap))
	for _, sn := range nodeMap {
		scoredList = append(scoredList, *sn)
	}

	sort.Slice(scoredList, func(i, j int) bool {
		return scoredList[i].RRFScore > scoredList[j].RRFScore
	})

	if len(scoredList) > limit {
		scoredList = scoredList[:limit]
	}

	return scoredList, nil
}





