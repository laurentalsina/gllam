# Walkthrough — Issue #1: Event Ordering & Temporal Reasoning

We have completed the implementation of **Issue #1: Event Ordering & Temporal Reasoning** in GLLAM, relying on temporally-bounded links (`valid_from` / `valid_until`) with explicit support for **temporal uncertainty** (`temporal_note`) and typed PDDL domain compilation (`(:types event state entity service contradiction - object)`).

---

## Key Changes Made

### 1. Schema & Data Model
* **[`pkg/schema/schema.sql`](file:///home/laurent/gllam/pkg/schema/schema.sql#L39-L50)**: Added `temporal_note TEXT` column to `semantic_links` and updated `valid_from` & `valid_until` to `TEXT` to support numeric timestamps as well as sentinel values (`'temporal_note'`).
* **[`pkg/memory/types.go`](file:///home/laurent/gllam/pkg/memory/types.go#L3-L49)**: Defined `NodeType` constants (`NodeTypeEvent`, `NodeTypeState`, `NodeTypeEntity`, etc.) and updated `SemanticLink` struct with `TemporalNote string`.

### 2. Temporal Engine & Querying
* **[`pkg/engine/semantic.go`](file:///home/laurent/gllam/pkg/engine/semantic.go#L60-L260)**:
  * Updated `AddEdge()` to populate `temporal_note` and handle stringified `valid_from` / `valid_until`.
  * Added `GetActiveLinksAtTime(ctx, timestamp)` to query graph state snapshots at any given timestamp $T$ (supporting both exact timestamps and uncertain `temporal_note` links).
  * Updated `InvalidateObsoleteEdge()` for timestamp strings.

### 3. Typed PDDL Compiler & Goal Extraction
* **[`pkg/engine/pddl_compiler.go`](file:///home/laurent/gllam/pkg/engine/pddl_compiler.go#L20-L160)**:
  * Grouped objects by node type in PDDL problem output (`e1 e2 - event`, `s1 s2 - state`).
  * Emitted `(:types event state entity service contradiction - object)` in PDDL domain definition.
  * Added `ExtractPDDLGoal(userPrompt, nodes, links)` to dynamically derive PDDL goal expressions (`(and (verified_sequence event_a event_b))`) for temporal queries.
* **[`pkg/engine/router.go`](file:///home/laurent/gllam/pkg/engine/router.go#L148-L175)**: Integrated `ExtractPDDLGoal` into the dual-tier solver router.

### 4. Codebase Alignment
* Updated `cmd/` entrypoints ([`cmd/gllam`](file:///home/laurent/gllam/cmd/gllam/main.go), [`cmd/extract_semantics`](file:///home/laurent/gllam/cmd/extract_semantics/main.go), [`cmd/ingest_npm`](file:///home/laurent/gllam/cmd/ingest_npm/main.go), [`cmd/ingest_sbom`](file:///home/laurent/gllam/cmd/ingest_sbom/main.go), [`cmd/ingest_synthetic_packages`](file:///home/laurent/gllam/cmd/ingest_synthetic_packages/main.go), [`cmd/test_conflict`](file:///home/laurent/gllam/cmd/test_conflict/main.go)) to pass string timestamps into `SemanticLink`.

---

## Verification Results

### Automated Tests (`go test -v ./pkg/engine`)
Executed and passed all 4 test suites:

```bash
=== RUN   TestCompileGraphToPDDLTyped
--- PASS: TestCompileGraphToPDDLTyped (0.00s)
=== RUN   TestExtractPDDLGoal
--- PASS: TestExtractPDDLGoal (0.00s)
=== RUN   TestTemporalUncertaintyLink
--- PASS: TestTemporalUncertaintyLink (0.00s)
=== RUN   TestFastDownwardPlanner
    planner_test.go:37: Successfully solved plan with 1 actions: [{move [loc1 loc2]}]
--- PASS: TestFastDownwardPlanner (0.22s)
PASS
ok  	github.com/laurentalsina/gllam/pkg/engine	0.226s
```

### Build Verification
* Built [`cmd/gllam`](file:///home/laurent/gllam/cmd/gllam/main.go) binary with 0 compilation errors.
