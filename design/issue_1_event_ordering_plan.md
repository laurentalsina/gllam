# Implementation Plan: Issue #1 — Event Ordering & Temporal Reasoning

## Goal Description
Implement end-to-end temporal reasoning and event-ordering question answering in GLLAM. This includes adding explicit semantic node types (`event`, `state`, `entity`) to distinguish temporal events from static states, updating the PDDL domain compiler to output typed PDDL (`(:types event state entity)`), implementing natural language PDDL goal extraction, and verifying event sequence queries via the dual-tier solver engine.

---

## Data Model Fitness Review & Node Types

### Schema & Node Type Specification
GLLAM's `semantic_nodes` table stores `type TEXT NOT NULL`. We will establish explicit Go constants and PDDL type mappings:

```go
const (
    NodeTypeEvent         = "event"
    NodeTypeState         = "state"
    NodeTypeEntity        = "entity"
    NodeTypeService       = "service"
    NodeTypeContradiction = "contradiction"
)
```

| Component | Schema State | Fitness for Event Ordering | Required Enhancements |
|---|---|---|---|
| `semantic_nodes` | `id`, `name`, `type`, `context_prompt` | 🟢 Excellent | Standardize `NodeTypeEvent` (`"event"`) and `NodeTypeState` (`"state"`). |
| `semantic_links` | `source_id`, `target_id`, `relationship`, `caveats`, `valid_from`, `valid_until` | 🟢 Strong | `valid_from` and `valid_until` allow interval-based validity. Add `GetActiveLinksAtTime(ctx, timestamp)`. |
| `CompileGraphToPDDL` | Compiles generic `(:types entity)` | 🟢 Upgraded | Compile specific `(:types event state entity - object)` to prune solver search space. |
| PDDL Goal Translator | Hardcoded mock `"(and (resolved user_conflict))"` in `router.go` | 🔴 Deficient | Build LLM Goal Extractor to translate natural language questions into PDDL goal predicates. |

---

## User Review Required

> [!IMPORTANT]
> **Typed PDDL Compilation:**
> Objects in the generated PDDL problem file will now be strictly typed according to `SemanticNode.Type` (e.g. `e1 e2 - event`, `s1 s2 - state`). This significantly accelerates Fast Downward and Native BFS search performance by eliminating invalid action bindings.

---

## Open Questions

> [!NOTE]
> 1. Should `valid_from` / `valid_until` timestamps on `semantic_links` be exposed as explicit PDDL numerical fluents, or compiled into discrete relational orderings (`happened_before`)?
>    * *Recommendation:* Discrete relational orderings (`happened_before`) compile into standard STRIPS/PDDL 2.1 without requiring complex numeric fluents in Fast Downward.

---

## Proposed Changes

### Component 1: Node Type Constants & Temporal Helpers
Files: `pkg/memory/types.go`, `pkg/engine/semantic.go`

#### [MODIFY] `pkg/memory/types.go`
Add `NodeType` constants:

```go
const (
    NodeTypeEvent         = "event"
    NodeTypeState         = "state"
    NodeTypeEntity        = "entity"
    NodeTypeService       = "service"
    NodeTypeContradiction = "contradiction"
)
```

#### [MODIFY] `pkg/engine/semantic.go`
Add `GetActiveLinksAtTime(ctx, timestamp)` to query valid graph snapshots at a specific Unix timestamp:

```go
// GetActiveLinksAtTime retrieves semantic links that were active at a specific Unix timestamp
func (e *GllamEngine) GetActiveLinksAtTime(ctx context.Context, timestamp int64) ([]memory.SemanticLink, error) {
    query := `
        SELECT source_id, target_id, relationship, caveats, valid_from, valid_until, updated_at
        FROM semantic_links
        WHERE valid_from <= ? AND (valid_until IS NULL OR valid_until > ?)`
    // ... query execution and scanning ...
}
```

---

### Component 2: PDDL Compiler with Typed Objects & Actions
File: `pkg/engine/pddl_compiler.go`

#### [MODIFY] `pkg/engine/pddl_compiler.go`
Update `CompileGraphToPDDL` to emit typed PDDL declarations (`(:types event state entity - object)`) and automatic state transition actions:

```go
// Output typed objects in PDDL problem file:
// (:objects
//    event1 event2 - event
//    state1 state2 - state
//    server1 - entity
// )
```

---

### Component 3: Goal Translator & Router Integration
File: `pkg/engine/router.go`

#### [MODIFY] `pkg/engine/router.go`
Replace hardcoded mocked goal predicate with dynamic goal extraction:

```go
// 1. LLM Goal Extraction
goalPredicate := e.ExtractPDDLGoal(ctx, userPrompt, ctxResult.SemanticNodes, ctxResult.SemanticLinks)

// 2. Compile and Solve
domainStr, problemStr := CompileGraphToPDDL(ctxResult.SemanticNodes, ctxResult.SemanticLinks, goalPredicate, ctxResult.Procedural)
```

---

## Verification Plan

### Automated Tests
1. **Unit Test for Typed PDDL Compiler & Temporal Action Generation:**
   ```bash
   go test -v ./pkg/engine -run TestCompileGraphToPDDL
   ```
2. **End-to-End Solver Test (Native BFS & Fast Downward):**
   ```bash
   go test -v ./pkg/engine -run TestEventOrderingSolver
   ```

### Manual Verification
1. Run GLLAM CLI with an event-ordering query:
   ```bash
   go run ./cmd/gllam -config config.yaml -recall "Did event A happen before event B?"
   ```
2. Verify that `PlannerOutput` in `# GLLAM Context` reports a mathematically verified plan sequence.
