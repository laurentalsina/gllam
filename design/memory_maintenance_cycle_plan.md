# Memory Maintenance Cycle & Synthetic Random Trace Tests Architecture Plan

## Overview

High-scale memory systems require a periodic offline **Memory Maintenance Cycle** to prevent memory degradation, prune stale temporal links, compact revision histories, and exercise graph clarity and consistency through **Synthetic Random Trace Tests**.

---

## Phases of the Memory Maintenance Cycle

```mermaid
flowchart TD
    SleepTrigger[EnterMemorySleepCycle] --> Phase1[Phase 1: Compaction & Pruning]
    Phase1 --> PruneLinks[Prune Expired Links valid_until <= now]
    Phase1 --> TaxonomyConsolidation[Run Taxonomy Branch Consolidation]
    Phase1 --> ProcessOrphans[Process Uncategorized Node Queue]

    SleepTrigger --> Phase2[Phase 2: Synthetic Random Trace Tests]
    Phase2 --> PickNodes[Pick Random Entity Pairs]
    Phase2 --> TraceMultiHop[Exercise Graph Multi-Hop Traversal]
    Phase2 --> ScoreMetrics[Calculate MemoryClarity & Consistency Scores]

    Phase1 & Phase2 --> Phase3[Phase 3: Diagnostic Reporting]
    Phase3 --> SleepReport[Return MemorySleepReport]
```

### 1. Phase 1: Maintenance Compaction & Cleaning
* **Stale Link Pruning:** Deletes expired edges from `semantic_links` (`valid_until <= now`).
* **Taxonomy Consolidation:** Merges duplicate taxonomy categories in an atomic SQLite transaction.
* **Orphan Classification:** Assigns uncategorized nodes to taxonomy paths.

### 2. Phase 2: Synthetic Random Trace Tests & Memory Exercise
* Picks pairs/triples of nodes from `semantic_nodes`.
* Generates synthetic question/answer scenario traces.
* Exercises graph retrieval (`FindMultiHopPath`).
* Calculates quantitative metrics:
  * $\text{MemoryClarityScore} \in [0.0, 1.0]$: Measure of uncontradicted, clear graph paths.
  * $\text{MemoryConsistencyScore} \in [0.0, 1.0]$: Ratio of consistent simulated answers across domains.

### 3. Phase 3: Diagnostic Reporting
Returns a structured `MemorySleepReport` containing pruned link counts, consolidated branch counts, simulated trace test scenarios, and clarity/consistency metrics.

---

## Verification Strategy

* **Unit Tests (`pkg/engine/sleep_test.go`):**
  * `TestMemoryMaintenanceCycleAndRandomTraceTests`: Verifies stale link pruning, synthetic trace test scenario generation, and score calculations.
