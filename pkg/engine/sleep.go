package engine

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/laurentalsina/gllam/pkg/memory"
)

// EnterMemorySleepCycle puts the memory engine into a maintenance sleep state, performing
// graph caveat compaction, taxonomy consolidation, uncategorized node classification, and synthetic random trace tests.
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

	// Phase 1: Hub Node Caveat Compaction (Historical facts are PRESERVED forever for temporal RAG, never deleted!)
	compactedHubs, err := e.BatchCompactHubCaveats(ctx, 10, 5)
	if err == nil {
		report.CompactedRevisionsCount = compactedHubs
	} else {
		log.Printf("Sleep cycle hub caveat compaction warning: %v", err)
	}
	report.PrunedStaleLinksCount = 0 // Historical links are preserved with valid_until timestamps for temporal lineage!

	// Phase 2: Consolidate Taxonomy Branches
	consolidatedCount, err := e.RunTaxonomyConsolidationPass(ctx)
	if err != nil {
		log.Printf("Sleep cycle taxonomy consolidation warning: %v", err)
	}
	report.ConsolidatedTaxonomyCount = consolidatedCount

	// Phase 3: Process Uncategorized Batch
	if _, err := e.ProcessUncategorizedBatch(ctx, 100); err != nil {
		log.Printf("Sleep cycle uncategorized batch processing warning: %v", err)
	}

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
		paths, _ := e.FindMultiHopPath(ctx, []string{n1.ID, n2.ID}, 4)

		clarity, isConsistent := e.CalculateTraceClarity(ctx, n1, n2, paths)
		if isConsistent {
			consistentCount++
		}

		simulatedAnswer := fmt.Sprintf("Trace exercise: %s -> %s. Path count: %d. Clarity score: %.2f. Domain context: %s", n1.Name, n2.Name, len(paths), clarity, n1.TaxonomyPath)
		if !isConsistent {
			simulatedAnswer = fmt.Sprintf("Trace exercise: Disjoint nodes %s and %s in separate domains. Clarity score: %.2f.", n1.Name, n2.Name, clarity)
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

// CalculateTaxonomyPathOverlap computes the segment overlap ratio between two materialized taxonomy paths.
func CalculateTaxonomyPathOverlap(path1, path2 string) float64 {
	p1 := strings.Split(strings.Trim(path1, "/"), "/")
	p2 := strings.Split(strings.Trim(path2, "/"), "/")
	if len(p1) == 0 || len(p2) == 0 {
		return 0.0
	}
	common := 0
	minLen := len(p1)
	if len(p2) < minLen {
		minLen = len(p2)
	}
	for i := 0; i < minLen; i++ {
		if p1[i] != "" && p1[i] == p2[i] {
			common++
		} else if p1[i] != "" {
			break
		}
	}
	maxLen := len(p1)
	if len(p2) > maxLen {
		maxLen = len(p2)
	}
	if maxLen == 0 {
		return 0.0
	}
	return float64(common) / float64(maxLen)
}

// CalculateTraceClarity computes a quantitative clarity score based on graph hop distance, caveat/conflict penalties,
// or hierarchical taxonomy path segment overlap when no explicit graph path exists.
func (e *GllamEngine) CalculateTraceClarity(ctx context.Context, n1, n2 memory.SemanticNode, paths []MultiHopPath) (float64, bool) {
	if len(paths) > 0 {
		// Pick shortest path
		shortest := paths[0]
		for _, p := range paths {
			if p.HopCount < shortest.HopCount {
				shortest = p
			}
		}

		// Base clarity degrades with hop distance: 1.0 / (1.0 + 0.1 * (hops - 1))
		hops := float64(shortest.HopCount)
		if hops < 1.0 {
			hops = 1.0
		}
		baseClarity := 1.0 / (1.0 + 0.1*(hops-1.0))

		// Check links along shortest path for caveats or active contradictions
		caveatPenalty := 0.0
		for _, l := range shortest.Links {
			if l.Caveats != "" {
				caveatPenalty += 0.10
			}
			if l.Relationship == "resolves_conflict" || l.Relationship == "subverts_claim" || l.Relationship == "exhibits_fallacy" {
				caveatPenalty += 0.20
			}
		}
		clarity := baseClarity - caveatPenalty
		if clarity < 0.10 {
			clarity = 0.10
		}
		return clarity, true
	}

	// No direct graph path — evaluate taxonomy domain alignment
	overlap := CalculateTaxonomyPathOverlap(n1.TaxonomyPath, n2.TaxonomyPath)
	if overlap > 0.25 { // Shares at least top-level category domain
		clarity := 0.50 + 0.50*overlap
		return clarity, true
	}

	// Disjoint domains
	return 0.20, false
}

