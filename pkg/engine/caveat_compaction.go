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

// CompactNodeCaveats ranks and compacts node caveats for a specific entity node when total caveats exceed maxInline.
// High-priority caveats (by origin trust weight and recency) remain inline, while lower-priority caveats are
// synthesized into a node-level CaveatSummary.
func (e *GllamEngine) CompactNodeCaveats(ctx context.Context, nodeID string, maxInline int) (string, int, int, error) {
	if maxInline <= 0 {
		maxInline = 5
	}

	// Fetch all links connected to nodeID (as source or target), joining the origin node for trust weight
	query := `
		SELECT l.source_id, l.target_id, l.relationship, l.caveats, l.origin_id, l.created_at,
		       COALESCE(n_origin.trust_weight, 100) as origin_trust
		FROM semantic_links l
		LEFT JOIN semantic_nodes n_origin ON l.origin_id = n_origin.id
		WHERE (l.source_id = ? OR l.target_id = ?) AND l.caveats IS NOT NULL AND l.caveats != ''`

	rows, err := e.dbRO.QueryContext(ctx, query, nodeID, nodeID)
	if err != nil {
		return "", 0, 0, fmt.Errorf("failed to fetch link caveats for node %s: %w", nodeID, err)
	}
	defer rows.Close()

	type caveatItem struct {
		link       memory.SemanticLink
		originTrust int
		createdAt  string
	}
	var items []caveatItem

	for rows.Next() {
		var l memory.SemanticLink
		var originTrust int
		var createdAt string
		var originID sql.NullString

		if err := rows.Scan(&l.SourceID, &l.TargetID, &l.Relationship, &l.Caveats, &originID, &createdAt, &originTrust); err == nil {
			if originID.Valid {
				l.OriginID = originID.String
			}
			l.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

			items = append(items, caveatItem{
				link:        l,
				originTrust: originTrust,
				createdAt:   createdAt,
			})
		}
	}

	if len(items) <= maxInline {
		return "", len(items), 0, nil
	}

	// Sort items by priority: Higher origin trust weight first, then recency
	sort.Slice(items, func(i, j int) bool {
		if items[i].originTrust != items[j].originTrust {
			return items[i].originTrust > items[j].originTrust
		}
		return items[i].createdAt > items[j].createdAt
	})

	retained := items[:maxInline]
	compacted := items[maxInline:]

	// Build synthetic caveat summary string for lower-priority caveats
	var caveatTexts []string
	for _, c := range compacted {
		caveatTexts = append(caveatTexts, fmt.Sprintf("[%s -> %s (%s)]: %s", c.link.SourceID, c.link.TargetID, c.link.Relationship, c.link.Caveats))
	}

	summaryText := fmt.Sprintf("Compacted Node Caveat Epoch (%d lower-priority items): %s", len(compacted), strings.Join(caveatTexts, "; "))

	// Update semantic_nodes caveat_summary
	updateQuery := `UPDATE semantic_nodes SET caveat_summary = ? WHERE id = ?`
	if _, err := e.db.ExecContext(ctx, updateQuery, summaryText, nodeID); err != nil {
		return "", len(retained), len(compacted), fmt.Errorf("failed to update node caveat_summary: %w", err)
	}

	return summaryText, len(retained), len(compacted), nil
}

// BatchCompactHubCaveats scans for hub entity nodes exceeding caveatThreshold and runs CompactNodeCaveats.
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
		if _, _, pruned, err := e.CompactNodeCaveats(ctx, id, maxInline); err == nil && pruned > 0 {
			compactedHubs++
		}
	}


	return compactedHubs, nil
}
