# Implementation Plan — Issue #3: Information Extraction (Needle-in-a-Haystack)

This plan outlines the architecture, data models, trap analysis, and step-by-step implementation for **Issue #3: Information Extraction (IE)** — high-precision baseline retrieval of isolated facts buried deep within transcripts that exceed 10x the LLM context size.

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

## Audit of Pre-Existing Sub-Components & Overlaps

Before building new features, we audited our existing codebase and design docs for overlapping capabilities:

| Cognitive Sub-Mechanism | Existing Location in GLLAM | Status | Reusability in Issue #3 |
| :--- | :--- | :--- | :--- |
| **Boundary-Aware Sliding Chunker** | `pkg/engine/chunker.go` (`ChunkTranscript`) | ✅ **Already Implemented** | Uses 6,000 char windows with 2,000 char overlap; prevents truncation of needle facts across chunk boundaries. |
| **Caveat-Qualified Semantic Graph** | `pkg/schema/schema.sql` & `pkg/memory/types.go` (`SemanticLink.Caveats`) | ✅ **Already Implemented** | Relationships explicitly carry `caveats` text, preventing caveat-blind fact extraction. |
| **Source Epistemic Disambiguation** | `pkg/engine/semantic.go` (`DisambiguateEntityForSource`) | ✅ **Already Implemented** | Grounding candidate entities against active speaker `origin_source_id` history. |
| **Vector Similarity Search** | `pkg/engine/semantic.go` (`SearchSimilarNodes`) | ✅ **Already Implemented** | `sqlite-vec` CGO vector search over node embeddings. |

---

## The Remaining Core Challenge for Issue #3

The critical missing component for Issue #3 is **Dual-Channel Hybrid Retrieval (`RetrieveHybridNeedle`)**:
* Vector search alone suffers from **sub-query dilution** in 100k+ token transcripts.
* Exact graph lookup alone misses non-literal semantic matches.
* **Solution:** Build `RetrieveHybridNeedle(ctx, query, entityIDs, sourceID)` using **Reciprocal Rank Fusion (RRF)** to combine vector similarity scores ($R_{\text{vector}}$) with graph-traversal proximity scores ($R_{\text{graph}}$) into a unified rank score:
  $$RRF(d) = \frac{1}{k + R_{\text{vector}}(d)} + \frac{1}{k + R_{\text{graph}}(d)}$$

---

## Detailed Analysis of Traps & Failure Modes

### Trap 1: Vector Sub-Query Dilution
* **Issue:** Vector embeddings of broad prompts get diluted across 100k+ tokens.
* **Solution:** **Dual-Channel Hybrid Retrieval (`RetrieveHybridNeedle`)** — combine `sqlite-vec` vector similarity search with exact entity graph indexing (`semantic_nodes` & `semantic_links`).

### Trap 2: Chunk Boundary Truncation
* **Status:** ✅ **Solved by `ChunkTranscript`** (6,000 char windows with 2,000 char overlap).

### Trap 3: Caveat-Qualified Information Blindness
* **Status:** ✅ **Solved by `SemanticLink.Caveats`** (automatically attached during RAG context assembly).

### Trap 4: Large-Corpus Entity Ambiguity
* **Status:** ✅ **Solved by `DisambiguateEntityForSource`** (grounds entity references to `origin_source_id` / `session_id`).

### Trap 5: High Latency & Memory Pressure on >100k Token Corpora
* **Issue:** Loading full graphs into RAM during context assembly.
* **Solution:** Bounded N-hop expansion (`maxHops = 2`), strict SQL LIMITs, and WAL-mode concurrent read handles (`dbRO`).

---

## Proposed Implementation Phasing

1. **Phase 1: Dual-Channel Hybrid Retrieval Engine (`RetrieveHybridNeedle`)**
   * Combine `SearchSimilarNodes` (vector search) with `ExpandTemporalNeighbors` (exact graph traversal).
   * Implement Reciprocal Rank Fusion (RRF) scoring to merge vector and graph ranks.

2. **Phase 2: Router Integration & Context Formatting**
   * Wire `RetrieveHybridNeedle` into `RouteAndAssemble` in `pkg/engine/router.go`.
   * Ensure caveats, temporal bounds, and source attributions are automatically formatted in system prompts.

3. **Phase 3: Automated Unit Testing & Benchmarking Command (`cmd/eval_ie_needle`)**
   * Write unit tests `TestRetrieveHybridNeedle` in `pkg/engine/needle_test.go`.
   * Add CLI tool `cmd/eval_ie_needle` to benchmark needle extraction accuracy over 100k+ token transcripts.
