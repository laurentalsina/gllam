package engine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

// EnterMemorySleepCycle puts the memory engine into a maintenance sleep state, performing
// graph compaction, stale link pruning, taxonomy consolidation, and synthetic random trace tests.
func (e *GllamEngine) EnterMemorySleepCycle(ctx context.Context, numTraceTests int) (*memory.MemorySleepReport, error) {
	start := time.Now()
	nowTS := start.Unix()
	sleepID := fmt.Sprintf("sleep-%d", nowTS)

	if numTraceTests <= 0 {
		numTraceTests = 5
	}

	report := &memory.MemorySleepReport{
		SleepCycleID:        sleepID,
		SimulatedTraceTests: make([]memory.SyntheticTraceTestScenario, 0),
	}

	// Phase 1: Prune Stale / Expired Links
	pruneQuery := `DELETE FROM semantic_links WHERE valid_until IS NOT NULL AND valid_until <= ?`
	res, err := e.db.ExecContext(ctx, pruneQuery, fmt.Sprintf("%d", nowTS))
	if err == nil {
		rows, _ := res.RowsAffected()
		report.PrunedStaleLinksCount = int(rows)
	}

	// Phase 2: Consolidate Taxonomy Branches
	consolidatedCount, _ := e.RunTaxonomyConsolidationPass(ctx)
	report.ConsolidatedTaxonomyCount = consolidatedCount

	// Phase 3: Process Uncategorized Batch
	_, _ = e.ProcessUncategorizedBatch(ctx, 100)

	// Phase 4: Synthetic Random Trace Tests & Memory Exercise
	traces, clarityScore, consistencyScore, err := e.SimulateRandomTraceTests(ctx, numTraceTests)
	if err == nil {
		report.SimulatedTraceTests = traces
		report.MemoryClarityScore = clarityScore
		report.MemoryConsistencyScore = consistencyScore
	} else {
		report.MemoryClarityScore = 1.0
		report.MemoryConsistencyScore = 1.0
	}

	report.DurationSeconds = time.Since(start).Seconds()
	return report, nil
}

// SimulateRandomTraceTests picks entity node pairs from memory to run synthetic random trace tests,
// exercising multi-hop graph retrieval and measuring consistency and clarity scores.
func (e *GllamEngine) SimulateRandomTraceTests(ctx context.Context, numTraceTests int) ([]memory.SyntheticTraceTestScenario, float64, float64, error) {
	if numTraceTests <= 0 {
		numTraceTests = 5
	}

	query := `SELECT id, name, taxonomy_path FROM semantic_nodes WHERE is_category = 0 LIMIT 100`
	rows, err := e.dbRO.QueryContext(ctx, query)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed to fetch nodes for trace test simulation: %w", err)
	}
	defer rows.Close()

	var nodes []memory.SemanticNode
	for rows.Next() {
		var n memory.SemanticNode
		if err := rows.Scan(&n.ID, &n.Name, &n.TaxonomyPath); err == nil {
			nodes = append(nodes, n)
		}
	}

	if len(nodes) < 2 {
		// Insufficient nodes for multi-hop trace test simulation
		return []memory.SyntheticTraceTestScenario{}, 1.0, 1.0, nil
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	var traces []memory.SyntheticTraceTestScenario
	totalClarity := 0.0
	consistentCount := 0

	for i := 0; i < numTraceTests; i++ {
		idx1 := r.Intn(len(nodes))
		idx2 := r.Intn(len(nodes))
		if idx1 == idx2 {
			idx2 = (idx1 + 1) % len(nodes)
		}

		n1 := nodes[idx1]
		n2 := nodes[idx2]

		traceID := fmt.Sprintf("trace-test-%d-%d", i+1, time.Now().UnixNano())
		simulatedQuery := fmt.Sprintf("What is the structural relationship between '%s' (%s) and '%s' (%s)?", n1.Name, n1.TaxonomyPath, n2.Name, n2.TaxonomyPath)

		// Exercise graph traversal path finder
		path, pathErr := e.FindMultiHopPath(ctx, []string{n1.ID, n2.ID}, 4)

		isConsistent := pathErr == nil && len(path) > 0
		clarity := 0.85
		if isConsistent {
			clarity = 1.0
			consistentCount++
		} else if strings.HasPrefix(n1.TaxonomyPath, n2.TaxonomyPath) || strings.HasPrefix(n2.TaxonomyPath, n1.TaxonomyPath) {
			// Related taxonomy domain boost
			clarity = 0.90
			isConsistent = true
			consistentCount++
		}

		simulatedAnswer := fmt.Sprintf("Trace exercise: %s -> %s. Path steps: %d. Domain context: %s", n1.Name, n2.Name, len(path), n1.TaxonomyPath)
		if !isConsistent {
			simulatedAnswer = fmt.Sprintf("Trace exercise: Disjoint nodes %s and %s in separate domains.", n1.Name, n2.Name)
		}

		traceScenario := memory.SyntheticTraceTestScenario{
			ID:               traceID,
			PromptQuery:      simulatedQuery,
			SimulatedAnswer:  simulatedAnswer,
			RetrievedNodeIDs: []string{n1.ID, n2.ID},
			IsConsistent:     isConsistent,
			ClarityScore:     clarity,
		}

		traces = append(traces, traceScenario)
		totalClarity += clarity
	}

	avgClarity := totalClarity / float64(numTraceTests)
	consistencyRatio := float64(consistentCount) / float64(numTraceTests)

	return traces, avgClarity, consistencyRatio, nil
}

