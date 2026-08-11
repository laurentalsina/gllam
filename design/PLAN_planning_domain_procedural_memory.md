# PLAN: Domain-Bound Procedural Memory & PDDL Compiler Architecture

> [!IMPORTANT]
> **Implementation Status**: 🟡 **PARTIALLY IMPLEMENTED**
> - ✅ **Implemented**: Dual-Tier PDDL Solver (`planner.go`), Cognitive Trigger (`router.go`), Aspect Projection & Graph Compilation (`pddl_compiler.go`).
> - 📋 **Planned**: Dynamic PDDL `(:action)` procedural template binding & vector association.

## 1. Motivation & Problem Statement
During the evaluation of the BEAM benchmark (100k+ token contextual interactions), GLLAM scored 0% on **Temporal Reasoning** and **Event Ordering**. LLMs natively struggle with formal logic, chronological constraints, and tracking mutually exclusive states across long horizons because they process time as probabilistic token proximity rather than strict mathematical states.

**Solution:** Augment the RAG pipeline by embedding a neuro-symbolic PDDL (Planning Domain Definition Language) solver. Instead of asking the LLM to guess timelines, GLLAM will mathematically *prove* sequences and identify contradictions using a deterministic solver, then pass the proof to the LLM.

---

## 2. The Dual-Tier Architecture ("System 1 vs System 2")

To maintain zero-dependency Go portability while supporting complex temporal reasoning, GLLAM 0.2 will use a unified `PlanningEngine` interface with two solver implementations:

### Tier 1: Native Go STRIPS Solver (System 1)
- **Engine:** Built entirely in pure Go (`pkg/engine/planner.go`).
- **Algorithm:** Breadth-First-Search (BFS) or simple A* forward-chaining.
- **Scope:** Basic propositional logic and timeline validation (e.g., "Did X happen before Y?").
- **Pros:** Zero dependencies, executes in microseconds, completely frictionless.

### Tier 2: Fast Downward Subprocess (System 2)
- **Engine:** Arm's-length execution of advanced C++ planners (e.g., Fast Downward) via `os/exec`.
- **Algorithm:** Highly optimized LAMA heuristics for massive state spaces.
- **Scope:** PDDL 2.1+ features (Durative actions, continuous time, numeric fluents/resources).
- **Pros:** Capable of solving highly complex scheduling, resource allocation, and strict timeline constraints.

---

## 3. Core System Integration

### A. The Cognitive Trigger
Implemented in `pkg/engine/router.go`.
- The router intercepts user prompts and flags `requiresPlanning = true` if chronological keywords (`before`, `after`, `timeline`, `sequence`, `possible`, `plan`) are detected.

### B. Graph-to-PDDL Compilation (The Missing Link)
When triggered, the retrieved SQLite memory graph must be compiled into standard PDDL dynamically:
1. **`(:objects)`**: Mapped 1:1 from `semantic_nodes` (e.g., `user_dev - person`, `flask_231 - technology`).
2. **`(:init)`**: Mapped 1:1 from `semantic_links` based on `valid_from` timestamps (e.g., `(completed user_dev user_auth)`).
3. **`(:goal)`**: The user's query is parsed (likely via a fast LLM extraction call) into a PDDL goal state (e.g., `(and (deployed app) (secured db))`).

### C. Procedural Memory Schema Updates
Currently, the `procedural_knowledge` table stores static Markdown text.
In GLLAM 0.2, a recipe can be defined as a strict PDDL `(:action)` block. 
```lisp
(:action deploy_production
  :parameters (?app - application)
  :precondition (and (has_state ?app analytics_int) (has_state ?app db_optimization))
  :effect (has_state ?app deployed)
)
```
These actions form the `domain.pddl` file passed to the solvers.

### D. Context Injection
Once the `PlanningEngine.Solve()` returns a slice of `PlannerAction` (or an error indicating the timeline is impossible/contradictory), the raw string proof is injected into `CompiledContext.PlannerOutput`. 
The `FormatSystemPrompt` string builder appends this as a `## Mathematical Sequence Verification (PDDL)` block for the LLM to read.

### E. PDDL Domain Persistence, Overlap Reuse & Refinement Caching
Currently, generated `domainStr` and `problemStr` PDDL specs are **ephemeral** in-memory variables.
To leverage domain overlap across benchmark questions and enable continuous domain refinement:
1. **Domain Definition Persistence**: Persist generated PDDL domain definitions in a `pddl_domains` table indexed by aspect projection (`AspectTemporal`, `AspectInstruction`, `AspectStateTransition`) and entity type signatures.
2. **Domain Overlap & Reuse**: When evaluating new queries sharing similar domain aspects, retrieve and merge existing PDDL action definitions and predicates rather than recompiling from scratch.
3. **Domain Definition Refinement**: Incrementally refine PDDL domain action preconditions and effects as new procedural recipes or user constraints are ingested into memory.
4. **Trace Artifact Saving**: Persist generated `domain.pddl` and `problem.pddl` files to `bench/pddl_traces/<instance_id>/` for offline auditing and solver benchmarking.

---

## 4. Current Status (GLLAM 0.2 Alpha)
✅ **Dual-Tier Architecture Defined:** `PlanningEngine` interface created with `NativePlanner` and `FastDownwardPlanner` structs.
✅ **Native STRIPS Solver Implemented:** A <150 line pure Go Propositional BFS solver is fully operational in `planner.go`. It parses initial states, goal states, and action blocks, and mathematically explores the state-space.
✅ **Graph-to-PDDL Compiler:** `pddl_compiler.go` successfully sanitizes SQLite semantic nodes into `(:objects)` and semantic links into `(:init)` states. It dynamically injects `(:action)` blocks retrieved from procedural memory.
✅ **Cognitive Trigger Wired:** `router.go` now detects chronological keywords (`before`, `timeline`, etc.), mocks an LLM Goal Extraction, compiles the graph, runs the planner, and injects the mathematical proof directly into the LLM system prompt.

## 5. Next Steps for Implementation
1. **Un-Mock the LLM Goal Extraction:** Replace the hardcoded `mockedGoalPredicate` in `router.go` with an actual fast LLM inference call using the constructed `goalPrompt`.
2. **Populate Procedural Actions:** Write and insert actual PDDL `(:action)` recipes into the SQLite `procedural_knowledge` table so the BFS planner has operations to execute.
3. **Run the Benchmark:** Execute `eval_beam` against the new logic engine to verify if the 0% scores on Contradiction Resolution and Event Ordering are resolved.
4. ✅ **Fast Downward Integration:** The `FastDownwardPlanner` subprocess is ready to take over if the `NativePlanner` encounters a problem space too large for BFS. (Installed at `~/Projects/downward`).
