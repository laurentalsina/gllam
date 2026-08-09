# Implementation Plan — Issue #6: Multi-Hop / Multi-Session Reasoning (MR)

This plan outlines the architecture, data models, trap analysis, and step-by-step implementation for **Issue #6: Multi-Hop Reasoning (MR)** — connecting distinct pieces of information mentioned across different sessions to deduce unstated conclusions (Dietary Restrictions, Spatial Location Transitivity, Quantitative Budgets/Constraints).

---

## Technical Architecture Overview

```mermaid
flowchart TD
    UserQuery[User Question / Implicit Reasoning Request] --> Router[GLLAM Router & Multi-Hop Path Finder]
    
    subgraph Multi-Hop Reasoning Engine (FindMultiHopPath)
        Router --> BFSGraphSearch[Multi-Session Graph BFS Traversal<br/>Finds A -> B -> C transitive paths]
        Router --> SpatialResolver[Hierarchical Spatial Transitivity<br/>ResolveSpatialContainment: Kyoto -> Japan]
        Router --> NumericEvaluator[Quantitative Budget & Capacity Evaluator<br/>EvaluateQuantitativeConstraints: 1000 - 600 = 400 < 500]
    end
    
    BFSGraphSearch & SpatialResolver & NumericEvaluator --> PDDLSynthesizer[Multi-Aspect PDDL Compiler<br/>CompileMultiAspectReasoningDomain]
    PDDLSynthesizer --> Solver[Fast Downward STRIPS Planner]
    Solver --> OutputAssembler[Sanitized Context & Deduced Implicit Conclusions]
```

---

## Detailed Analysis of Traps & Failure Modes

### Trap 1: Cross-Session Path Disconnection
* **Issue:** Fact $A$ is in Session 1 (*"Alice is allergic to peanuts"*), Fact $B$ is in Session 4 (*"Thai class serves peanut sauce"*). Single-session RAG fails to connect them.
* **Solution:** `FindMultiHopPath(ctx, startEntities, endEntities, maxHops=3)` — performs BFS graph traversal across `semantic_links` ignoring session boundaries.

### Trap 2: Quantitative Budget & Inequality Deduction Blindness
* **Issue:** Session 2 (*"Budget is 1000"*), Session 6 (*"Bought laptop for 600"*). User asks *"Can I buy a tablet for 500?"*. Raw retrieval returns 1000 and 600, but fails to evaluate arithmetic ($1000 - 600 = 400 < 500$).
* **Solution:** `EvaluateQuantitativeConstraints(nodes, links, proposedCost)` — computes remaining balance and evaluates inequality constraints.

### Trap 3: Implied Geographical / Spatial Transitivity
* **Issue:** Session 1 (*"Bob lives in Kyoto"*), Session 3 (*"Kyoto is in Japan"*), Session 9 (*"Alice is visiting Bob"*). Query: *"Which country is Alice visiting?"*.
* **Solution:** `ResolveSpatialContainment(ctx, entityID)` — traverses `located_in` / `lives_in` hierarchical edges to resolve multi-hop location inclusion.

### Trap 4: Multi-Aspect Implicit Rule Synthesis
* **Issue:** Combining a dietary restriction (`user_preference`) with an event component (`serves_ingredient`) to deduce safety.
* **Solution:** `CompileMultiAspectReasoningDomain` — merges preference, state, and event links into a unified PDDL domain for STRIPS reasoning.

### Trap 5: High-Order Combinatorial Path Flooding
* **Issue:** BFS in large graphs (1,000+ nodes) produces thousands of irrelevant paths, flooding LLM context windows.
* **Solution:** `ScoreMultiHopPath` — ranks paths using edge relevance, temporal validity, and RRF scoring.

---

## Proposed Implementation Phasing

1. **Phase 1: Multi-Hop Transitive Path Finder (`FindMultiHopPath`)**
   * Implement `FindMultiHopPath(ctx, startEntities, endEntities, maxHops)` in `pkg/engine/semantic.go`.

2. **Phase 2: Quantitative Constraint & Budget Evaluator (`EvaluateQuantitativeConstraints`)**
   * Implement numeric balance tracking and inequality checking in `pkg/engine/semantic.go`.

3. **Phase 3: Hierarchical Spatial Containment Resolver (`ResolveSpatialContainment`)**
   * Implement spatial transitivity resolution (`located_in`, `lives_in`, `part_of`).

4. **Phase 4: Router Integration & Automated Testing**
   * Integrate multi-hop reasoning into `RouteAndAssemble` in `pkg/engine/router.go`.
   * Write unit tests in `pkg/engine/multihop_test.go`.
