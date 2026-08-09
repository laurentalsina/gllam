# Asynchronous Embedding Worker Pool Architecture Plan

## Overview

When scaling past 100,000 extracted `semantic_nodes`, executing embedding model inference and updating virtual vector tables (`sqlite-vec` `vec0`) inside the primary write transaction drastically increases commit latency.

The **Asynchronous Embedding Worker Pool** decouples relational graph insertion from vector virtual table mutations. Relational nodes and links are committed to SQLite immediately (sub-millisecond latency), while vector embeddings are generated and indexed by background worker goroutines.

---

## Architectural Workflow

```mermaid
flowchart TD
    RelationalWrite[UpsertNode Ingestion] -->|Immediate Commit| RelationalDB[(semantic_nodes SQLite Table)]
    
    RelationalDB -->|Query Unembedded Nodes v.node_id IS NULL| Queue[ProcessUnembeddedNodeBatch]
    
    subgraph EmbeddingWorkerPool[Asynchronous Embedding Worker Pool]
        Worker1[Embedding Worker 1]
        Worker2[Embedding Worker 2]
        Worker3[Embedding Worker N]
    end
    
    Queue --> EmbeddingWorkerPool
    EmbeddingWorkerPool -->|Embed Text via Embedder| GenVec[Generate Vector Embedding]
    GenVec -->|Async Write| VecTable[(vec0 Virtual Table - semantic_embeddings)]
```

### 1. Decoupled Ingestion
* `UpsertNode` synchronously writes `semantic_nodes` relational records without blocking on embedding model calls or vector virtual table updates.

### 2. Unindexed Vector Discovery (`ProcessUnembeddedNodeBatch`)
* Efficiently queries unindexed nodes using an outer join:
  ```sql
  SELECT n.id, n.name, n.context_prompt
  FROM semantic_nodes n
  LEFT JOIN semantic_embeddings v ON n.id = v.node_id
  WHERE v.node_id IS NULL
  LIMIT 50;
  ```

### 3. Asynchronous Worker Pool (`StartEmbeddingWorkerPool`)
* Launches $N$ concurrent worker goroutines that periodically fetch batches of unindexed nodes.
* Computes vector embeddings via `e.embedder.Embed(ctx, text)`.
* Writes vector blobs into `semantic_embeddings` via `IndexNodeVector`.

---

## Verification Strategy

* **Unit Tests (`pkg/engine/embedding_worker_test.go`):**
  * `TestAsynchronousEmbeddingWorkerPool`:
    * Bulk inserts 20 `semantic_nodes` without synchronous vector embedding.
    * Asserts relational nodes exist in SQLite immediately while vector index count is initially 0.
    * Launches worker pool (`StartEmbeddingWorkerPool`).
    * Asserts all 20 nodes are indexed into `semantic_embeddings` asynchronously.
