# Validation & Audit of `time.Now()` Call Sites

## Executive Summary

A comprehensive audit was performed across all packages in GLLAM to validate that `time.Now()` is used strictly for valid operational, profiling, and write-path default assignment purposes, and **never for point-in-time temporal query evaluation**.

One bug in `caveat_compaction.go` was identified and fixed. All other `time.Now()` call sites were validated as architecturally sound.

---

## Audit Findings & Categorization

### Category 1: Fixed Bi-Temporal Bug (Corrected)

| File & Line | Function | Usage | Assessment & Fix |
|:---|:---|:---|:---|
| [`pkg/engine/caveat_compaction.go:68`](file:///home/laurent/gllam/pkg/engine/caveat_compaction.go#L68) | `CompactNodeCaveats` | Assigned wall-clock `nowTS := time.Now().Unix()` to `validFromTS` during caveat recency sorting. | ❌ **BUG FIXED**: Updated to parse `validFrom.String` (`strconv.ParseInt`) so recency ranking reflects historical fact origin timestamps rather than wall-clock execution time. |

---

### Category 2: Write-Path Ingestion & Event Defaulting (VALID)

When entity nodes, links, contradictions, or lineage records are inserted or updated without an explicit timestamp, `time.Now().Unix()` provides the default transaction write timestamp. When explicit timestamps are provided (e.g. historical Jira ticket dates), the explicit timestamps are honored.

| File & Line | Function | Purpose / Justification | Status |
|:---|:---|:---|:---|
| [`pkg/engine/semantic.go:105`](file:///home/laurent/gllam/pkg/engine/semantic.go#L105) | `AddEdge` | Default `valid_from` for incoming edge resolution. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/semantic.go:186`](file:///home/laurent/gllam/pkg/engine/semantic.go#L186) | `UpsertNode` | Default `created_at`/`updated_at` node metadata. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/semantic.go:300,313`](file:///home/laurent/gllam/pkg/engine/semantic.go#L300) | `SupersedeFact` | Stamp revocation `valid_until` timestamp when fact is superseded. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:777,778`](file:///home/laurent/gllam/pkg/engine/semantic.go#L777) | `UpdateNodeTaxonomyPath` | Stamp `updated_at` node metadata. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:804,805`](file:///home/laurent/gllam/pkg/engine/semantic.go#L804) | `SetNodeCategoryFlag` | Stamp `updated_at` category metadata. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:986,987`](file:///home/laurent/gllam/pkg/engine/semantic.go#L986) | `ResolveContradiction` | Stamp resolution `valid_until` timestamp on losing claim. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:1249,1250`](file:///home/laurent/gllam/pkg/engine/semantic.go#L1249) | `InvalidateDependentCrossCuttingLinksRecursive` | Stamp `valid_until` on revoked cross-cutting dependencies. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:1330`](file:///home/laurent/gllam/pkg/engine/semantic.go#L1330) | `RegisterContainerSource` | Default `created_at` for source provenance container. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/semantic.go:1676`](file:///home/laurent/gllam/pkg/engine/semantic.go#L1676) | `ExtractProceduralWorkflow` | Stamp `updated_at` on procedural recipe insertion. | ✅ VALID (Mutation Write Path) |
| [`pkg/engine/semantic.go:1851`](file:///home/laurent/gllam/pkg/engine/semantic.go#L1851) | `GaugeAndUpsertSourceNode` | Evaluates temporal recency component during initial source node creation. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/semantic.go:1910`](file:///home/laurent/gllam/pkg/engine/semantic.go#L1910) | `AddDocumentLineage` | Default `created_at` timestamp if `lineage.CreatedAt == 0`. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/semantic.go:2092`](file:///home/laurent/gllam/pkg/engine/semantic.go#L2092) | `AddDocumentVersion` | Default `created_at` timestamp if `ver.CreatedAt == 0`. | ✅ VALID (Ingestion Write Path) |
| [`pkg/engine/taxonomy_worker.go:70`](file:///home/laurent/gllam/pkg/engine/taxonomy_worker.go#L70) | `ProcessUncategorizedBatch` | Default `valid_from` for auto-generated `is_a` taxonomy links. | ✅ VALID (Ingestion Write Path) |

---

### Category 3: System Operational Metadata (VALID)

| File & Line | Function | Purpose / Justification | Status |
|:---|:---|:---|:---|
| [`pkg/engine/reembed.go:30,108`](file:///home/laurent/gllam/pkg/engine/reembed.go#L30) | `CheckEmbeddingModelVersion`, `ReembedAllSemanticNodes` | Stamp `updated_at` in `system_metadata` when embedding model version changes. | ✅ VALID (System Metadata) |
| [`pkg/engine/procedural.go:37`](file:///home/laurent/gllam/pkg/engine/procedural.go#L37) | `MarkProcedureHelpful` | Stamp `updated_at` in `procedural_knowledge` table. | ✅ VALID (System Metadata) |

---

### Category 4: Performance Profiling & Unique ID Generation (VALID)

| File & Line | Function | Purpose / Justification | Status |
|:---|:---|:---|:---|
| [`pkg/engine/sleep.go:17`](file:///home/laurent/gllam/pkg/engine/sleep.go#L17) | `EnterMemorySleepCycle` | Profile sleep cycle duration (`time.Since(start)`). | ✅ VALID (Profiling) |
| [`pkg/engine/sleep.go:93`](file:///home/laurent/gllam/pkg/engine/sleep.go#L93) | `SimulateRandomTraceTests` | Seed pseudo-random number generator (`rand.NewSource`). | ✅ VALID (RNG Seeding) |
| [`pkg/engine/sleep.go:108`](file:///home/laurent/gllam/pkg/engine/sleep.go#L108) | `SimulateRandomTraceTests` | Unique scenario ID generation (`fmt.Sprintf("trace-test-%d-%d", ...)`). | ✅ VALID (ID Generation) |

---

### Category 5: Point-In-Time Query Path Verification (VERIFIED 100% CLEAN)

Bi-temporal point-in-time retrieval functions (`RetrieveHybridNeedleWithTime`, `ExpandTemporalNeighborsWithTime`, `FilterActiveSummaryFactsForTime`) accept an explicit `asOfTime` parameter and **do not invoke `time.Now()`**:

```go
// RetrieveHybridNeedleWithTime filters active facts relative to asOfTime (Point-in-Time RAG):
func (e *GllamEngine) RetrieveHybridNeedleWithTime(ctx context.Context, query string, topK int, asOfTime string) ([]memory.SemanticNode, []memory.SemanticLink, error)

// FilterActiveSummaryFactsForTime evaluates validity relative to evaluationTimestamp (Point-in-Time):
func FilterActiveSummaryFactsForTime(links []memory.SemanticLink, evaluationTimestamp int64) []memory.SemanticLink
```

Bi-temporal RAG queries can accurately ask *"What did the system know as of 2021?"* without interference from wall-clock time.
