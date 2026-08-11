# PLAN: Anti-LLM Benchmark Cheating & Integrity Verification Architecture

> [!IMPORTANT]
> **Implementation Status**: 🟡 **PARTIALLY IMPLEMENTED**
> - ✅ **Implemented**: Double-blind LLM grading without context leakage (`grade_results`), Field isolation between QA targets and memory DB.
> - 📋 **Planned**: Automated pre-execution DB auditor script.

## 1. Executive Summary & Objective

In AI memory system evaluation (such as `eval_d7_qa` and `eval_beam`), benchmark integrity depends on strict isolation between **Ground Truth Target Answers** and **Memory Retrieval Context**. 

Any accidental exposure of ground truth text, reference labels, or evaluation instance metadata to the retrieval pipeline (`RouteAndAssemble`, `SearchSimilarNodes`) or LLM context prompt constitutes **benchmark contamination / cheating**.

This plan outlines a formal **Anti-Cheating & Leakage Prevention Architecture** featuring:
1. **Strict Input Sanitization & Field Masking**: Enforcing structural isolation of query strings prior to context assembly.
2. **Pre-Execution Database Contamination Auditing**: Automated verification that memory databases (`gllam_data.db`) contain zero QA ground truth substrings.
3. **Cryptographic Provenance & Blind Grading**: SHA-256 dataset hashing and double-blind grading protocols.

---

## 2. Threat Model & Leakage Vectors

```mermaid
flowchart TD
    QAFile["Evaluation Dataset (d7_qa.jsonl)"] --> Loader[Evaluation Runner]
    
    subgraph Isolated Pipeline
        Loader -->|Query String ONLY| Route["GllamEngine.RouteAndAssemble(query)"]
        Route -->|Retrieved Nodes & Links| PromptFormatter["FormatSystemPrompt()"]
        PromptFormatter -->|System + User Prompt| LLM["LLM Inference Engine"]
        LLM -->|Model Answer| Collector[Result Collector]
    end
    
    subgraph Anti-Leakage Guards
        Auditor["DB Contamination Auditor"] -->|Scans| DB["Memory Database (gllam_data.db)"]
        Auditor -->|Verifies| Clean["No Ground Truth Substrings Found"]
    end
    
    QAFile -->|Ground Truth (Post-Inference Only)| Collector
```

| Threat Vector | Description | Prevention Guard |
| :--- | :--- | :--- |
| **T1: Pre-Ingestion DB Contamination** | QA test sets accidentally ingested into `semantic_nodes` or `episodic_summaries`. | Pre-bench DB Contamination Auditor (`AuditDatabaseCleanliness`). |
| **T2: Struct Field Leakage** | Unmarshaling full QA objects and passing fields beyond `query` to `RouteAndAssemble`. | Explicit `QueryOnlyRequest` struct stripping all `ground_truth` fields. |
| **T3: Context Prompt Injection** | Memory nodes containing target answers retrieved via broad vector matches. | Semantic taxonomy separation and strict node origin auditing. |
| **T4: Grader Bias / Favoritism** | LLM grader receiving ground truth prior to scoring or using non-deterministic rubrics. | Double-Blind LLM Grader with randomized option ordering. |

---

## 3. Anti-Cheating Architecture Specifications

### 3.1 Strict Input Sanitization (`QueryOnlyRequest`)
In `pkg/engine/eval_guard.go`, evaluation runners MUST strip all non-query metadata before passing requests to `GllamEngine`:

```go
// Isolated request struct containing ONLY the question
type QueryOnlyRequest struct {
    QueryText string `json:"query_text"`
}

func SanitizeQueryForEval(rawJSON []byte) (QueryOnlyRequest, error) {
    var req QueryOnlyRequest
    if err := json.Unmarshal(rawJSON, &req); err != nil {
        return req, fmt.Errorf("failed to extract query: %w", err)
    }
    return req, nil
}
```

### 3.2 Automated Database Contamination Auditor (`AuditDatabaseCleanliness`)
Before launching benchmark runs, an automated auditor executes FTS5 and exact substring queries over `gllam_data.db` using ground truth strings from `d7_qa.jsonl`:

```go
func (e *GllamEngine) AuditDatabaseCleanliness(ctx context.Context, groundTruthAnswers []string) (int, error) {
    contaminationHits := 0
    for _, gt := range groundTruthAnswers {
        if len(gt) < 10 { // Skip short trivial words
            continue
        }
        var count int
        err := e.dbRO.QueryRowContext(ctx, 
            "SELECT count(*) FROM episodic_summaries WHERE summary_text LIKE ?", "%"+gt+"%").Scan(&count)
        if err == nil && count > 0 {
            contaminationHits++
            log.Printf("[CONTAMINATION WARNING] Ground truth substring found in episodic memory: %s", gt)
        }
    }
    return contaminationHits, nil
}
```

### 3.3 Double-Blind LLM Grading Protocol
When grading model answers against ground truths in `cmd/grade_results/main.go`:
- The evaluator prompt randomly swaps candidate answer order to prevent positional bias.
- The grader uses strict JSON schema validation (`{ "score": 0.0 - 1.0, "reasoning": "..." }`).

---

## 4. Implementation Phases (Post-Benchmark)

1. **Phase 1: Pre-Execution DB Auditor (`cmd/audit_benchmark_db/main.go`)**
   - Implement CLI tool to scan `gllam_data.db` against `d7_qa.jsonl` and `beam_100k_qa.jsonl`.
   - Output contamination report verifying zero test set leakage.

2. **Phase 2: Evaluation Runner Guardrails (`pkg/engine/eval_guard.go`)**
   - Refactor `cmd/eval_d7_qa/main.go` and `cmd/eval_beam/main.go` to use `QueryOnlyRequest`.
   - Enforce compile-time type separation between `QAInstance` (used only by runner output writer) and `GllamEngine` inputs.

3. **Phase 3: Dataset Hash Provenance (`bench/PROVENANCE.json`)**
   - Generate SHA-256 hashes for `d7_qa.jsonl`, `corpus_sessions.jsonl`, and `gllam_data.db`.
   - Store immutable hash manifests to guarantee evaluation dataset versions match published baselines.

---

## 5. Verification Criteria

- [ ] `AuditDatabaseCleanliness` returns `0` contamination hits when run on `gllam_data.db` against `d7_qa.jsonl`.
- [ ] Evaluation runner `eval_d7_qa` passes only `QueryOnlyRequest` to `RouteAndAssemble`.
- [ ] Ground truth strings are read strictly after model inference completes for output writing.
- [ ] `PROVENANCE.json` SHA-256 dataset hashes match repo release tags.
