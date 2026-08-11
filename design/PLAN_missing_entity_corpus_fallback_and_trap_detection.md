# PLAN: Missing Entity Recovery & Presupposition Trap Detection

## 1. Executive Summary & Problem Statement

When evaluating queries against `GllamEngine`, a query may reference an entity (e.g., `"Ibrahima"`) that does not exist in the current `semantic_nodes` database table (`gllam_data.db`).

This leads to two distinct failure modes:
1. **Extraction / Ingestion Gap**: `"Ibrahima"` exists in the raw episodic corpus (`corpus_sessions.jsonl` / `episodic_summaries`), but was omitted during node extraction.
2. **Presupposition Trap / Non-Existent Entity**: The question contains a false premise about a character that never existed in the corpus (e.g., *"What did Ibrahima say about X?"*).

This plan defines a **Two-Tier Missing Entity Recovery & Epistemic Trap Detection Engine**:
- **Tier 1 (Corpus Back-Search)**: Dynamically fallback to full-text search over episodic raw transcripts (`episodic_summaries`) to recover un-ingested entities and build on-demand semantic nodes.
- **Tier 2 (Presupposition Trap Handling)**: If corpus back-search confirms zero occurrences of the entity, tag the query as a **Presupposition Trap**, preventing hallucination and enabling explicit refusal/correction.

---

## 2. Architecture & Workflow

```mermaid
flowchart TD
    Query["User Query ('What did Ibrahima say?')"] --> Extract["Extract Referenced Entities: ['Ibrahima']"]
    Extract --> GraphCheck{"Check Graph: Exists in semantic_nodes?"}
    
    GraphCheck -->|Yes| NormalFlow[Normal Retrieval & PDDL Reasoning]
    GraphCheck -->|No| BackSearch["Tier 1: Corpus Back-Search (Full-Text Search on episodic_summaries)"]
    
    BackSearch --> SearchResult{"Matches found in raw transcripts?"}
    
    SearchResult -->|Yes (Hits > 0)| OnDemandIngest["On-Demand Ingestion: Extract Nodes & Links for 'Ibrahima'"]
    OnDemandIngest --> UpdateGraph["Insert missing SemanticNode into gllam_data.db"]
    UpdateGraph --> NormalFlow
    
    SearchResult -->|No (Hits == 0)| TrapDetect["Tier 2: Presupposition Trap Confirmed"]
    TrapDetect --> EpistemicGuard["Inject Epistemic Caveat: Entity 'Ibrahima' does not exist in corpus"]
    EpistemicGuard --> Output["Respond: 'No record of Ibrahima exists in the available memory corpus.'"]
```

---

## 3. Detailed Component Specifications

### 3.1 Tier 1: Corpus Back-Search & On-Demand Node Recovery
When an entity $E$ in the query is missing from `semantic_nodes`:

1. **FTS / Text Search**: Perform SQLite Full-Text Search (`FTS5`) or `LIKE '%Ibrahima%'` over `episodic_summaries` table.
2. **Phonetic / Fuzzy Match Fallback**: If exact string yields 0 hits, run Levenshtein / Damerau-Levenshtein distance ($\le 2$) against existing entity names to detect typos (e.g. `"Ibrahima"` vs `"Ibrahim"`).
3. **On-Demand Node Materialization**:
   - If transcript hits are found, pass the relevant session text to the semantic extractor.
   - Insert the newly created `SemanticNode` (e.g., `id: "agent:ibrahima"`, `taxonomy_path: "/People/Participants/Ibrahima"`) and related `SemanticLink` edges into `gllam_data.db`.
   - Resume query processing with the newly materialized graph context.

### 3.2 Tier 2: Presupposition Trap Detection & Epistemic Refusal
If both graph lookup AND corpus back-search confirm $0$ hits for entity $E$:

1. **Trap Flagging**: Flag entity $E$ with `IsVerifiedNonExistent = true`.
2. **Prompt Injection**: Construct an explicit epistemic boundary in the context prompt:
   ```text
   EPISTEMIC BOUNDARY NOTICE:
   The entity 'Ibrahima' referenced in the question was searched across all 13,344 session transcripts in the corpus and DOES NOT EXIST.
   Rule: Do not hallucinate or assume facts about 'Ibrahima'. State clearly that 'Ibrahima' is not present in the memory corpus.
   ```
3. **Evaluation Scoring Benefit**: Prevents model from making up plausible-sounding answers to trick questions, improving accuracy on benchmark trap dimensions.

---

## 4. Proposed Go Interface (`pkg/engine/entity_recovery.go`)

```go
type EntityVerificationResult struct {
    EntityName            string
    FoundInGraph          bool
    FoundInCorpus         bool
    FuzzyMatchCandidate   string
    MatchingSessionIDs    []string
    IsPresuppositionTrap bool
}

func (e *GllamEngine) VerifyAndRecoverEntity(ctx context.Context, entityName string) (*EntityVerificationResult, error) {
    // 1. Check semantic_nodes
    if nodes, _ := e.GetNodesByNameOrAlias(ctx, entityName); len(nodes) > 0 {
        return &EntityVerificationResult{EntityName: entityName, FoundInGraph: true}, nil
    }

    // 2. Back-search episodic_summaries
    sessions, err := e.SearchEpisodicSummaries(ctx, entityName)
    if err != nil {
        return nil, err
    }

    if len(sessions) > 0 {
        // On-demand recovery
        _ = e.MaterializeMissingEntity(ctx, entityName, sessions)
        return &EntityVerificationResult{
            EntityName:         entityName,
            FoundInGraph:      false,
            FoundInCorpus:     true,
            MatchingSessionIDs: extractSessionIDs(sessions),
        }, nil
    }

    // 3. Check fuzzy match typos
    if candidate := e.FindFuzzyEntityCandidate(ctx, entityName); candidate != "" {
        return &EntityVerificationResult{
            EntityName:          entityName,
            FuzzyMatchCandidate: candidate,
        }, nil
    }

    // 4. Confirmed Presupposition Trap
    return &EntityVerificationResult{
        EntityName:           entityName,
        FoundInGraph:        false,
        FoundInCorpus:       false,
        IsPresuppositionTrap: true,
    }, nil
}
```

---

## 5. Implementation Phases (Post-Benchmark)

1. **Phase 1: Episodic Full-Text Search (`pkg/engine/episodic.go`)**
   - Implement `SearchEpisodicSummaries(ctx, queryText)` to search raw transcripts in `episodic_summaries`.

2. **Phase 2: Entity Recovery & Trap Detector (`pkg/engine/entity_recovery.go`)**
   - Implement `VerifyAndRecoverEntity` and `MaterializeMissingEntity`.
   - Add Levenshtein fuzzy candidate matching for minor spelling variants.

3. **Phase 3: Prompt Integration & Router Guard (`pkg/engine/router.go`)**
   - Integrate `VerifyAndRecoverEntity` into query routing pipeline.
   - Inject `EPISTEMIC BOUNDARY NOTICE` when `IsPresuppositionTrap == true`.

4. **Phase 4: Unit Testing (`pkg/engine/entity_recovery_test.go`)**
   - Test missing entity back-search and on-demand node creation.
   - Test presupposition trap handling and non-existent entity detection.

---

## 6. Verification Criteria

- [ ] Missing entity present in raw corpus transcripts is automatically back-searched and materialized into `semantic_nodes`.
- [ ] Non-existent entity triggers `IsPresuppositionTrap = true` and emits epistemic boundary prompt notice.
- [ ] Evaluation runner output clearly indicates `[Corpus Back-Search] Recovered node: Ibrahima` or `[Presupposition Trap Detected] Entity 'Ibrahima' not in corpus`.
