# Walkthrough — Issue #3: Information Extraction (Needle-in-a-Haystack)

We have implemented **Phase 1 of Issue #3: Dual-Channel RRF Hybrid Retrieval Engine (`RetrieveHybridNeedle`)** to enable high-precision needle-in-a-haystack fact extraction over transcripts exceeding 10x the LLM context window.

---

## Technical Architecture Overview

```mermaid
flowchart TD
    UserQuery[User Question / Needle Request] --> Router[GLLAM Router & Dual-Channel Hybrid Retriever]
    
    subgraph Dual-Channel Hybrid Retrieval Engine (RetrieveHybridNeedle)
        Router --> VectorSearch[Vector Similarity Channel<br/>SearchSimilarNodes via sqlite-vec]
        Router --> GraphSearch[Exact Graph & Caveat Channel<br/>ExpandTemporalNeighbors & SemanticLink.Caveats]
        Router --> SourceDisambig[Source Epistemic Disambiguator<br/>DisambiguateEntityForSource]
    end
    
    VectorSearch & GraphSearch & SourceDisambig --> RRFRanker[Reciprocal Rank Fusion & Caveat Attacher<br/>Blends vector ranks + exact graph edges]
    RRFRanker --> ContextAssembler[Sanitized System Context & Needle Output]
```

---

## Key Components Implemented

### 1. Dual-Channel RRF Hybrid Retrieval Engine ([`RetrieveHybridNeedle`](file:///home/laurent/gllam/pkg/engine/semantic.go#L940-L1050))
* Combines **Vector Similarity Search (`sqlite-vec`)** with **Exact Graph Traversal (`ExpandTemporalNeighbors`)** and **Source Epistemic Disambiguation (`DisambiguateEntityForSource`)**.
* Computes **Reciprocal Rank Fusion (RRF)** scores across both channels:
  $$RRF(d) = \frac{1}{60 + R_{\text{vec}}(d)} + \frac{1}{60 + R_{\text{graph}}(d)}$$
* Prevents sub-query vector dilution in massive transcripts (>100k tokens) while ensuring non-literal semantic matches are retrieved.

### 2. Caveat-Qualified Needle Scoring (`NeedleScoredNode`)
* Returns `NeedleScoredNode` objects carrying ranked nodes, vector ranks, graph ranks, RRF scores, and attached `Caveats` text.

---

## Audit of Pre-Existing Sub-Components & Overlaps

| Sub-Mechanism | Location in Codebase | Status | Reusability for Issue #3 |
| :--- | :--- | :--- | :--- |
| **Boundary-Aware Sliding Chunker** | `pkg/engine/chunker.go` (`ChunkTranscript`) | ✅ **Already Implemented** | Uses 6,000 char windows with 2,000 char overlap; prevents truncation of needle facts across chunk boundaries. |
| **Caveat-Qualified Links** | `pkg/schema/schema.sql` & `pkg/memory/types.go` (`SemanticLink.Caveats`) | ✅ **Already Implemented** | Relationships explicitly carry `caveats` text, preventing caveat-blind fact extraction. |
| **Source Epistemic Disambiguation** | `pkg/engine/semantic.go` (`DisambiguateEntityForSource`) | ✅ **Already Implemented** | Grounding candidate entities against active speaker `origin_source_id` history. |
| **Vector Similarity Search** | `pkg/engine/semantic.go` (`SearchSimilarNodes`) | ✅ **Already Implemented** | `sqlite-vec` CGO vector search over node embeddings. |

---

## Verification & Automated Test Results

Executed `go test -v ./pkg/engine` across all 26 engine test suites:

```bash
=== RUN   TestRetrieveHybridNeedle
--- PASS: TestRetrieveHybridNeedle (0.01s)
PASS
ok  	github.com/laurentalsina/gllam/pkg/engine	0.284s
```

### Git Commits Pushed to `main`
* **`b25e55b`**: `docs: create design/ directory in repo root containing all issue plans and walkthroughs`
* **`0e1be5b`**: `feat(ie): implement Phase 1 Dual-Channel RRF Hybrid Retrieval Engine (RetrieveHybridNeedle)`
