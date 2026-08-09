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

// CompactNodeEdgeCaveats ranks and compacts edge caveats for a specific node when total caveats exceed maxInline.
// High-priority active caveats remain inline, while older/lower-trust caveats are synthesized into a node-level CaveatSummary.
func (e *GllamEngine) CompactNodeEdgeCaveats(ctx context.Context, nodeID string, maxInline int) (string, int, int, error) {
	if maxInline <= 0 {
		maxInline = 5
	}

	// Fetch all links connected to nodeID (as source or target)
	query := `
		SELECT l.source_id, l.target_id, l.relationship, l.caveats, l.valid_from, l.valid_until, l.origin_source_id,
		       COALESCE(n_src.trust_weight, 500) as src_trust
		FROM semantic_links l
		LEFT JOIN semantic_nodes n_src ON l.origin_source_id = n_src.id
		WHERE (l.source_id = ? OR l.target_id = ?) AND l.caveats IS NOT NULL AND l.caveats != ''`

	rows, err := e.dbRO.QueryContext(ctx, query, nodeID, nodeID)
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to fetch link caveats for node %s: %w", nodeID, err)
	}
	defer rows.Close()

	type caveatItem struct {
		link        memory.SemanticLink
		srcTrust    int
		isActive    bool
		validFromTS int64
	}
	var items []caveatItem

	nowTS := time.Now().Unix()
	for rows.Next() {
		var l memory.SemanticLink
		var srcTrust int
		var validUntil sql.NullString
		var validFrom sql.NullString
		var originSrc sql.NullString

		if err := rows.Scan(&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &validFrom, &validUntil, &originSrc, &srcTrust); err == nil {
			isActive := true
			if validUntil.Valid && validUntil.String != "" {
				l.ValidUntil = &validUntil.String
				isActive = false
			}
			if validFrom.Valid {
				l.ValidFrom = validFrom.String
			}
			if originSrc.Valid {
				l.OriginSourceID = originSrc.String
			}

			items = append(items, caveatItem{
				link:        l,
				srcTrust:    srcTrust,
				isActive:    isActive,
				validFromTS: nowTS,
			})
		}
	}

	if len(items) <= maxInline {
		return "", len(items), 0, nil
	}

	// Sort items by priority: Active > Source Trust > Recency
	sort.Slice(items, func(i, j int) bool {
		if items[i].isActive != items[j].isActive {
			return items[i].isActive // Active first
		}
		if items[i].srcTrust != items[j].srcTrust {
			return items[i].srcTrust > items[j].srcTrust // Higher trust weight first
		}
		return items[i].validFromTS > items[j].validFromTS // Recency
	})

	retained := items[:maxInline]
	compacted := items[maxInline:]

	// Build synthetic caveat summary string for historical/lower-priority caveats
	var caveatTexts []string
	for _, c := range compacted {
		caveatTexts = append(caveatTexts, fmt.Sprintf("[%s -> %s (%s)]: %s", c.link.SourceID, c.link.TargetID, c.link.Relationship, c.link.Caveats))
	}

	summaryText := fmt.Sprintf("Compacted Edge Caveat Epoch (%d historical items): %s", len(compacted), strings.Join(caveatTexts, "; "))

	// Update semantic_nodes caveat_summary
	updateQuery := `UPDATE semantic_nodes SET caveat_summary = ? WHERE id = ?`
	if _, err := e.db.ExecContext(ctx, updateQuery, summaryText, nodeID); err != nil {
		return "", len(retained), len(compacted), fmt.Errorf("failed to update node caveat_summary: %w", err)
	}

	return summaryText, len(retained), len(compacted), nil
}

// BatchCompactHubCaveats scans for hub entity nodes exceeding caveatThreshold and runs CompactNodeEdgeCaveats.
func (e *GllamEngine) BatchCompactHubCaveats(ctx context.Context, caveatThreshold int, maxInline int) (int, error) {
	if caveatThreshold <= 0 {
		caveatThreshold = 10
	}
	if maxInline <= 0 {
		maxInline = 5
	}

	query := `
		SELECT n.id
		FROM semantic_nodes n
		JOIN semantic_links l ON (l.source_id = n.id OR l.target_id = n.id)
		WHERE l.caveats IS NOT NULL AND l.caveats != ''
		GROUP BY n.id
		HAVING COUNT(l.caveats) > ?`

	rows, err := e.dbRO.QueryContext(ctx, query, caveatThreshold)
	if err != nil {
		return 0, fmt.Errorf("failed to query hub nodes for caveat compaction: %w", err)
	}
	defer rows.Close()

	var hubIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			hubIDs = append(hubIDs, id)
		}
	}

	compactedHubs := 0
	for _, id := range hubIDs {
		if _, _, pruned, err := e.CompactNodeEdgeCaveats(ctx, id, maxInline); err == nil && pruned > 0 {
			compactedHubs++
		}
	}

	return compactedHubs, nil
}
