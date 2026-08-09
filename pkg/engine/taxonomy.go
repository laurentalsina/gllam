package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)


// DetectTaxonomyCycles uses Kahn's Topological Sort Algorithm to detect cyclic parent-child dependencies
// across all is_a, subclass_of, instance_of, and part_of taxonomy edges.
func (e *GllamEngine) DetectTaxonomyCycles(ctx context.Context) (bool, []string, error) {
	query := `
		SELECT source_id, target_id 
		FROM semantic_links 
		WHERE relationship IN ('is_a', 'subclass_of', 'instance_of', 'part_of') AND valid_until IS NULL`

	rows, err := e.dbRO.QueryContext(ctx, query)
	if err != nil {
		return false, nil, fmt.Errorf("failed to query taxonomy links for cycle detection: %w", err)
	}
	defer rows.Close()

	adj := make(map[string][]string)
	inDegree := make(map[string]int)
	allNodes := make(map[string]bool)

	for rows.Next() {
		var src, tgt string
		if err := rows.Scan(&src, &tgt); err != nil {
			return false, nil, fmt.Errorf("failed to scan taxonomy link: %w", err)
		}
		adj[src] = append(adj[src], tgt) // Directed edge: child (src) -> parent (tgt)
		inDegree[tgt]++
		if _, ok := inDegree[src]; !ok {
			inDegree[src] = 0
		}
		allNodes[src] = true
		allNodes[tgt] = true
	}

	queue := make([]string, 0)
	for node, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, node)
		}
	}

	visitedCount := 0
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		visitedCount++

		for _, neighbor := range adj[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if visitedCount < len(allNodes) {
		// Cycle detected! Collect nodes involved in cycles (inDegree > 0)
		var cyclicNodes []string
		for node, deg := range inDegree {
			if deg > 0 {
				cyclicNodes = append(cyclicNodes, node)
			}
		}
		return true, cyclicNodes, nil
	}

	return false, nil, nil
}

// WouldCreateTaxonomyCycle checks if adding a parent-child relationship (childID -> parentID)
// would introduce a cycle into the taxonomy tree.
func (e *GllamEngine) WouldCreateTaxonomyCycle(ctx context.Context, childID string, parentID string) (bool, error) {
	if childID == parentID {
		return true, nil // Self-loop is an immediate cycle
	}

	// Traversal: Check if childID is reachable starting from parentID via existing parent-child edges
	visited := make(map[string]bool)
	queue := []string{parentID}
	visited[parentID] = true

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr == childID {
			return true, nil // Path exists from parent to child -> adding child -> parent creates cycle!
		}

		query := `
			SELECT target_id 
			FROM semantic_links 
			WHERE source_id = ? AND relationship IN ('is_a', 'subclass_of', 'instance_of', 'part_of') AND valid_until IS NULL`

		rows, err := e.dbRO.QueryContext(ctx, query, curr)
		if err != nil {
			return false, err
		}

		for rows.Next() {
			var tgt string
			if err := rows.Scan(&tgt); err == nil {
				if !visited[tgt] {
					visited[tgt] = true
					queue = append(queue, tgt)
				}
			}
		}
		rows.Close()
	}

	return false, nil
}

// UpdateNodeTaxonomyPath updates the materialized taxonomy path and category flag for a node.
func (e *GllamEngine) UpdateNodeTaxonomyPath(ctx context.Context, nodeID string, taxonomyPath string, isCategory bool) error {

	if taxonomyPath == "" {
		taxonomyPath = "/"
	}
	// Clean taxonomy path ensuring leading slash and trailing consistency
	cleanPath := "/" + strings.Trim(taxonomyPath, "/")

	isCatInt := 0
	if isCategory {
		isCatInt = 1
	}

	query := `UPDATE semantic_nodes SET taxonomy_path = ?, is_category = ? WHERE id = ?`
	res, err := e.db.ExecContext(ctx, query, cleanPath, isCatInt, nodeID)
	if err != nil {
		return fmt.Errorf("failed to update taxonomy path for node %s: %w", nodeID, err)
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("node not found: %s", nodeID)
	}
	return nil
}

// GetNodesByTaxonomyPrefix returns all semantic nodes whose materialized path starts with pathPrefix.
// Enables instantaneous hierarchical filtering (e.g., LIKE '/Engineering/Infrastructure/Databases/%').
func (e *GllamEngine) GetNodesByTaxonomyPrefix(ctx context.Context, pathPrefix string) ([]memory.SemanticNode, error) {
	cleanPrefix := "/" + strings.Trim(pathPrefix, "/")
	pattern := cleanPrefix + "%"

	query := `
		SELECT id, name, type, context_prompt, trust_weight, taxonomy_path, is_category
		FROM semantic_nodes
		WHERE taxonomy_path LIKE ? OR taxonomy_path = ?
		ORDER BY taxonomy_path ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, pattern, cleanPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to query nodes by taxonomy prefix %s: %w", cleanPrefix, err)
	}
	defer rows.Close()

	var nodes []memory.SemanticNode
	for rows.Next() {
		var n memory.SemanticNode
		var isCatInt int
		var ctxPrompt sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &ctxPrompt, &n.TrustWeight, &n.TaxonomyPath, &isCatInt); err != nil {
			return nil, fmt.Errorf("failed to scan taxonomy node: %w", err)
		}
		n.ContextPrompt = ctxPrompt.String
		n.IsCategory = isCatInt == 1
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// GetUncategorizedNodes retrieves semantic nodes that currently have a root or empty taxonomy path ('/').
func (e *GllamEngine) GetUncategorizedNodes(ctx context.Context, limit int) ([]memory.SemanticNode, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, name, type, context_prompt, trust_weight, taxonomy_path, is_category
		FROM semantic_nodes
		WHERE (taxonomy_path IS NULL OR taxonomy_path = '/' OR taxonomy_path = '') AND is_category = 0
		LIMIT ?`

	rows, err := e.dbRO.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query uncategorized nodes: %w", err)
	}
	defer rows.Close()

	var nodes []memory.SemanticNode
	for rows.Next() {
		var n memory.SemanticNode
		var isCatInt int
		var ctxPrompt sql.NullString
		if err := rows.Scan(&n.ID, &n.Name, &n.Type, &ctxPrompt, &n.TrustWeight, &n.TaxonomyPath, &isCatInt); err != nil {
			return nil, fmt.Errorf("failed to scan uncategorized node: %w", err)
		}
		n.ContextPrompt = ctxPrompt.String
		n.IsCategory = isCatInt == 1
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// GetTaxonomyTree returns all active category nodes arranged in a hierarchical tree structure.
func (e *GllamEngine) GetTaxonomyTree(ctx context.Context) ([]*memory.TaxonomyNode, error) {
	query := `
		SELECT id, name, taxonomy_path, is_category
		FROM semantic_nodes
		WHERE is_category = 1 OR type = ?
		ORDER BY taxonomy_path ASC`

	rows, err := e.dbRO.QueryContext(ctx, query, memory.NodeTypeCategory)
	if err != nil {
		return nil, fmt.Errorf("failed to query taxonomy category nodes: %w", err)
	}
	defer rows.Close()

	nodeMap := make(map[string]*memory.TaxonomyNode)
	var rootNodes []*memory.TaxonomyNode

	for rows.Next() {
		var id, name, path string
		var isCatInt int
		if err := rows.Scan(&id, &name, &path, &isCatInt); err != nil {
			return nil, fmt.Errorf("failed to scan taxonomy category node: %w", err)
		}

		cleanPath := "/" + strings.Trim(path, "/")
		parts := strings.Split(strings.Trim(cleanPath, "/"), "/")
		parentPath := "/"
		if len(parts) > 1 {
			parentPath = "/" + strings.Join(parts[:len(parts)-1], "/")
		}

		tn := &memory.TaxonomyNode{
			ID:         id,
			Name:       name,
			Path:       cleanPath,
			IsCategory: true,
			ParentPath: parentPath,
			Children:   make([]*memory.TaxonomyNode, 0),
		}
		nodeMap[cleanPath] = tn

		if parentPath == "/" || parentPath == "" {
			rootNodes = append(rootNodes, tn)
		} else if parent, exists := nodeMap[parentPath]; exists {
			parent.Children = append(parent.Children, tn)
		} else {
			rootNodes = append(rootNodes, tn)
		}
	}
	return rootNodes, nil
}

// ConsolidateTaxonomyBranch merges a duplicate/redundant category branch into a target canonical category branch.
// Performs path rewrites in small, bounded transactions (batchSize = 500) with short sleep intervals
// between commits to prevent exclusive write lock stalls during bulk taxonomy consolidations.
func (e *GllamEngine) ConsolidateTaxonomyBranch(ctx context.Context, sourceCategoryPath string, targetCategoryPath string) error {
	cleanSource := "/" + strings.Trim(sourceCategoryPath, "/")
	cleanTarget := "/" + strings.Trim(targetCategoryPath, "/")

	if cleanSource == cleanTarget {
		return nil // Nothing to merge
	}

	sourcePattern := cleanSource + "%"
	batchSize := 500

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 1. Fetch next batch of node IDs needing path rewrite
		queryBatch := `
			SELECT id
			FROM semantic_nodes
			WHERE taxonomy_path LIKE ? OR taxonomy_path = ?
			LIMIT ?`

		rows, err := e.dbRO.QueryContext(ctx, queryBatch, sourcePattern, cleanSource, batchSize)
		if err != nil {
			return fmt.Errorf("failed to query batch for taxonomy consolidation: %w", err)
		}

		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()

		if len(ids) == 0 {
			break // All nodes rewritten
		}

		// 2. Perform chunked update in a bounded write transaction
		tx, err := e.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to start chunked consolidation transaction: %w", err)
		}

		placeholders := make([]string, len(ids))
		args := make([]interface{}, 0, len(ids)+2)
		args = append(args, cleanTarget, cleanSource)
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}

		rewriteQuery := fmt.Sprintf(`
			UPDATE semantic_nodes
			SET taxonomy_path = ? || SUBSTR(taxonomy_path, LENGTH(?) + 1)
			WHERE id IN (%s)`, strings.Join(placeholders, ","))

		if _, err := tx.ExecContext(ctx, rewriteQuery, args...); err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to execute chunked path rewrite: %w", err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit chunked consolidation: %w", err)
		}

		// Short pause between batches to allow concurrent reads and writes to proceed cleanly
		time.Sleep(10 * time.Millisecond)
	}

	// 3. Redirect ontological semantic_links (is_a, subclass_of, instance_of, part_of) and remove old category node
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start link redirection transaction: %w", err)
	}
	defer tx.Rollback()

	var sourceID string
	err = tx.QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE taxonomy_path = ? AND is_category = 1 LIMIT 1", cleanSource).Scan(&sourceID)
	if err == nil && sourceID != "" {
		var targetID string
		_ = tx.QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE taxonomy_path = ? AND is_category = 1 LIMIT 1", cleanTarget).Scan(&targetID)

		if targetID != "" {
			redirectTargetQuery := `
				UPDATE semantic_links
				SET target_id = ?
				WHERE target_id = ? AND relationship IN ('is_a', 'subclass_of', 'instance_of', 'part_of')`
			_, _ = tx.ExecContext(ctx, redirectTargetQuery, targetID, sourceID)

			deleteOldCategoryQuery := `DELETE FROM semantic_nodes WHERE id = ?`
			_, _ = tx.ExecContext(ctx, deleteOldCategoryQuery, sourceID)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit link redirection during taxonomy consolidation: %w", err)
	}

	return nil
}


// AutoClassifyEntityByRules applies deterministic domain heuristics to categorize an uncategorized entity.
func AutoClassifyEntityByRules(node memory.SemanticNode) (string, string) {
	nameLower := strings.ToLower(node.Name)
	ctxLower := strings.ToLower(node.ContextPrompt)

	switch {
	case strings.Contains(nameLower, "postgres") || strings.Contains(nameLower, "mysql") || strings.Contains(nameLower, "sqlite") || strings.Contains(nameLower, "oracle") || strings.Contains(ctxLower, "relational database"):
		return "/Engineering/Infrastructure/Databases/Relational", "Relational Databases"
	case strings.Contains(nameLower, "redis") || strings.Contains(nameLower, "memcached") || strings.Contains(nameLower, "mongo") || strings.Contains(nameLower, "dynamo") || strings.Contains(ctxLower, "key-value"):
		return "/Engineering/Infrastructure/Databases/NoSQL", "NoSQL Databases"
	case strings.Contains(nameLower, "kubernetes") || strings.Contains(nameLower, "docker") || strings.Contains(nameLower, "caddy") || strings.Contains(nameLower, "nginx") || strings.Contains(ctxLower, "container"):
		return "/Engineering/Infrastructure/Deployment", "Deployment Infrastructure"
	case strings.Contains(nameLower, "jira") || strings.Contains(nameLower, "confluence") || strings.Contains(nameLower, "slack") || strings.Contains(ctxLower, "ticketing"):
		return "/Enterprise/Tools/Communication", "Enterprise Tools"

	default:
		if node.Type == memory.NodeTypeService {
			return "/Engineering/Services", "Engineering Services"
		}
		return "/General/Unclassified", "General Unclassified"
	}
}
