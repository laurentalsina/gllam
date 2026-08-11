# PLAN: Predictive World Model & Causal Physics Simulation Engine

> [!IMPORTANT]
> **Implementation Status**: 📋 **PLANNED / AWAITING IMPLEMENTATION** *(Post-Benchmark)*

## 1. Executive Summary & Design Vision

Current Memory RAG architectures (including standard Knowledge Graphs) operate at the level of **Concept Interrelations** (e.g., `(stove, has_property, hot)`). They can answer static recall queries, but lack **Predictive Power**—the ability to simulate action-effect trajectories, evaluate counterfactual "what-if" scenarios, and enforce physical laws (e.g., *"If I touch an active stove, my hand will suffer a thermal burn"*).

This plan outlines the evolution of GLLAM from a **Semantic Memory Graph** into a **Neuro-Symbolic Predictive World Model**. By unifying:
1. **Causal & Affordance Graph Links** (`can_be_acted_upon`, `causal_effect`, `state_precondition`)
2. **PDDL / PDDL+ Numeric Fluent Forward Simulation** (Action-Effect State Dynamics)
3. **Counterfactual "What-If" Sandbox Rollouts** (Mental Simulation before execution)

GLLAM gains zero-shot predictive physical reasoning and safety boundary enforcement.

---

## 2. The 3-Tier Spectrum: From Concept Graph to World Model

```mermaid
flowchart TD
    subgraph Tier 1: Semantic Recall (Current)
        Node1[Stove] -->|has_property| Node2[Hot]
        Node1 -->|associated_with| Node3[Kitchen]
    end

    subgraph Tier 2: Causal Affordance Graph (Proposed)
        StoveNode[Object: Stove] -->|has_state| ActiveState[State: Active/Hot]
        StoveNode -->|affords_action| TouchAction[Action: Touch]
        TouchAction -->|requires_precondition| ActiveState
        TouchAction -->|causal_effect| BurnEffect[Effect: Thermal Burn]
    end

    subgraph Tier 3: PDDL Numeric Forward Simulator (Proposed)
        InitState["State(t0): temp(stove) = 220°C, health(skin) = 100%"]
        Simulate["Simulate Action: touch(hand, stove)"]
        NextState["State(t1): health(skin) = 0%, state(hand) = burned"]
        InitState --> Simulate --> NextState
    end
```

---

## 3. Core Architectural Extensions

### 3.1 Causal & Affordance Graph Ontology

We extend `semantic_links` with 4 physical & causal edge types:

| Edge Type | Source Node | Target Node | Purpose |
| :--- | :--- | :--- | :--- |
| `affords_action` | Physical Entity (`stove`) | Action (`touch`, `turn_knob`) | Defines what physical operations an object permits. |
| `state_precondition` | Action (`touch`) | Required State (`state_active_hot`) | Condition that must hold for an effect to trigger. |
| `causal_effect` | Action (`touch`) | Consequence (`thermal_burn`) | Direct deterministic or probabilistic outcome. |
| `mitigates_effect` | Protective Item (`oven_mitt`) | Consequence (`thermal_burn`) | Counter-causal rule that neutralizes negative effects. |

### 3.2 PDDL Action-Effect Physics Compiler

When GLLAM evaluates a predictive query (e.g. *"What happens if I touch the stove?"*), the compiler generates PDDL `(:action)` blocks representing physical laws:

```lisp
(:action touch_object
  :parameters (?actor - human ?obj - physical_object)
  :precondition (and 
    (has_state ?obj state_active_hot)
    (not (wearing ?actor protective_mitt))
  )
  :effect (and 
    (has_state ?actor state_burned)
    (not (has_state ?actor state_unharmed))
  )
)
```

### 3.3 Counterfactual "What-If" Sandbox Rollout

Before taking an action or answering a safety question, the engine forks a temporary in-memory state:

1. **State Snapshot**: Clones current `semantic_nodes` and `semantic_links`.
2. **Action Injection**: Applies hypothetical action (e.g., `touch(hand, stove)`).
3. **Forward Chain Execution**: `NewNativePlanner()` or `FastDownwardPlanner()` steps forward $N$ transitions.
4. **Safety Verification**: Checks if any `negative_constraint` or damage state (`state_burned`) is reached in $t+1$.
5. **LLM Context Injection**: Passes the simulated state trajectory to the LLM as verified causal reality.

### 3.4 Domain Grounding & Bootstrapping Configuration (`--domain-config`)

To allow instant zero-shot predictive power on specific domains of interest (e.g., industrial safety, robotics, thermal dynamics, software infrastructure) without waiting for experience ingestion, GLLAM supports a **Domain Grounding Configuration File** (`--domain-config`).

#### A. Bootstrap YAML Specification Example (`config/domain_physics.yaml`)
```yaml
domain_name: thermal_and_kinetic_physics
taxonomy_root: /Physics/Thermal

category_nodes:
  - id: cat_thermal_source
    name: Thermal Source
    taxonomy_path: /Physics/Thermal/HeatSource
    is_category: 1
  - id: cat_protective_gear
    name: Protective Apparel
    taxonomy_path: /Physics/Thermal/Protection
    is_category: 1

causal_rules:
  - name: thermal_contact_burn
    object_category: cat_thermal_source
    afforded_action: touch
    preconditions:
      - state: active_hot
      - min_temperature_celsius: 60
    causal_effects:
      - target_state: thermal_burn
        energy_transfer_type: thermal
        damage_severity: high
    mitigating_factors:
      - apparel_category: cat_protective_gear

pddl_action_template: |
  (:action touch_hot_object
    :parameters (?actor - agent ?obj - cat_thermal_source)
    :precondition (and (has_state ?obj active_hot) (not (equipped ?actor cat_protective_gear)))
    :effect (and (has_state ?actor thermal_burn) (not (has_state ?actor unharmed)))
  )
```

#### B. Engine Bootstrapping Pipeline (`pkg/engine/bootstrap.go`)
- On engine initialization (`NewGllamEngine`), if `--domain-config` is provided:
  1. Parses category taxonomy nodes and inserts them into `semantic_nodes` with `is_category = 1`.
  2. Parses causal rules and indexes them as `affords_action` and `causal_effect` links in `semantic_links`.
  3. Ingests PDDL action templates directly into `procedural_knowledge`.
  4. Provides instant, zero-shot predictive simulation for the target domain on Day 1.

---

## 4. Required Schema & Engine Code Additions

### A. Schema Update (`pkg/schema/schema.sql`)
```sql
ALTER TABLE semantic_links ADD COLUMN causal_probability REAL DEFAULT 1.0;
ALTER TABLE semantic_links ADD COLUMN energy_transfer_type TEXT; -- 'thermal', 'mechanical', 'chemical', 'electrical'
```

### B. Forward Simulation Function (`pkg/engine/world_model.go`)
```go
type PredictiveSimulationResult struct {
    ActionSequence   []string `json:"action_sequence"`
    PredictedStates  []string `json:"predicted_states"`
    HasSafetyViolation bool   `json:"has_safety_violation"`
    ViolationDetail  string   `json:"violation_detail"`
}

func (e *GllamEngine) SimulateActionTrajectory(ctx context.Context, actionPrompt string) (*PredictiveSimulationResult, error)
```

---

## 5. Verification & Safety Test Suite

- [ ] **TestPhysicalPrecondition**: Verify `touch(stove)` predicts `state_burned` when `stove` has state `hot`.
- [ ] **TestMitigationRule**: Verify `wear(oven_mitt)` + `touch(stove)` predicts `state_unharmed`.
- [ ] **TestCounterfactualRollout**: Forward simulation returns $t+1$ state trajectory without modifying persistent `gllam_data.db`.
