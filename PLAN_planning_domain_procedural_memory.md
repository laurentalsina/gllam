# GLLAM 0.2: Dual-Tier PDDL Planning Engine Architecture

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

---

## 4. Next Steps for Implementation
1. **Build the PDDL Compiler:** Write a function `CompileGraphToPDDL(nodes, links)` that safely formats the SQLite objects into LISP-like strings.
2. **Implement Native STRIPS BFS:** Finish the stubbed `NativePlanner.Solve()` method in `pkg/engine/planner.go` with a <200 line Go state-space search algorithm.
3. **LLM Goal Translation:** Write a prompt template to convert user questions (e.g., "Could I have deployed?") into strict PDDL goals.
4. **Testing:** Write unit tests feeding known contradictions to the `NativePlanner` to verify it correctly returns "No valid plan found."
