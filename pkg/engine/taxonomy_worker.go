package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

// ProcessUncategorizedBatch queries uncategorized orphaned nodes and updates their taxonomy paths and is_a edges.
func (e *GllamEngine) ProcessUncategorizedBatch(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 50
	}

	nodes, err := e.GetUncategorizedNodes(ctx, batchSize)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch uncategorized nodes: %w", err)
	}

	if len(nodes) == 0 {
		return 0, nil
	}

	processed := 0
	for _, n := range nodes {
		targetPath, categoryName := AutoClassifyEntityByRules(n)

		// 1. Ensure category parent node exists
		catID := fmt.Sprintf("cat-%s", strings.ToLower(strings.ReplaceAll(categoryName, " ", "_")))

		// Cycle Prevention Check: Verify n.ID -> catID will not form a cycle
		if createsCycle, _ := e.WouldCreateTaxonomyCycle(ctx, n.ID, catID); createsCycle {
			log.Printf("Cycle prevented: Attributing node %s to parent %s would create a cyclic taxonomy path! Falling back to root.", n.ID, catID)
			targetPath = "/General/Unclassified"
			categoryName = "General Unclassified"
			catID = "cat-general_unclassified"
		}

		catNode := memory.SemanticNode{
			ID:            catID,
			Name:          categoryName,
			Type:          memory.NodeTypeCategory,
			ContextPrompt: fmt.Sprintf("Taxonomy Category: %s", targetPath),
			TrustWeight:   800,
			TaxonomyPath:  targetPath,
			IsCategory:    true,
		}
		if err := e.UpsertNode(ctx, catNode); err != nil {
			log.Printf("Failed to upsert category node %s: %v", catID, err)
			continue
		}

		// 2. Set node materialized taxonomy path
		nodePath := fmt.Sprintf("%s/%s", strings.TrimRight(targetPath, "/"), strings.ReplaceAll(n.Name, " ", "_"))
		if err := e.UpdateNodeTaxonomyPath(ctx, n.ID, nodePath, false); err != nil {
			log.Printf("Failed to update taxonomy path for %s: %v", n.ID, err)
			continue
		}

		// 3. Create explicit is_a ontological link in semantic_links
		isALink := memory.SemanticLink{
			SourceID:     n.ID,
			TargetID:     catID,
			Relationship: "is_a",
			Caveats:      "Taxonomy categorization",
			
		}
		if err := e.AddEdge(ctx, isALink); err != nil {
			log.Printf("Failed to add is_a link for node %s -> %s: %v", n.ID, catID, err)
		}
		processed++
	}

	return processed, nil
}

// RunTaxonomyConsolidationPass identifies redundant category paths (e.g. /Engineering/DBs vs /Engineering/Databases) and merges them.
func (e *GllamEngine) RunTaxonomyConsolidationPass(ctx context.Context) (int, error) {
	// Alias mappings for self-healing taxonomy consolidation
	consolidations := map[string]string{
		"/Engineering/DBs":                   "/Engineering/Infrastructure/Databases",
		"/Engineering/Database":              "/Engineering/Infrastructure/Databases",
		"/Engineering/Services/Microservices": "/Engineering/Services",
	}

	mergedCount := 0
	for src, tgt := range consolidations {
		if err := e.ConsolidateTaxonomyBranch(ctx, src, tgt); err == nil {
			mergedCount++
		}
	}
	return mergedCount, nil
}

// StartTaxonomyWorker launches a background worker that periodically processes uncategorized node batches
// and executes taxonomy consolidation passes.
func (e *GllamEngine) StartTaxonomyWorker(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stopTaxonomyWorker:
				return
			case <-ticker.C:
				if _, err := e.ProcessUncategorizedBatch(ctx, 50); err != nil {
					log.Printf("Background taxonomy worker ProcessUncategorizedBatch failed: %v", err)
				}
				if _, err := e.RunTaxonomyConsolidationPass(ctx); err != nil {
					log.Printf("Background taxonomy worker RunTaxonomyConsolidationPass failed: %v", err)
				}
			}
		}
	}()
}

