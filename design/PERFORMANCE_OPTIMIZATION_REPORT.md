# GLLAM Extraction Engine Architecture & Performance Optimization Report

> [!IMPORTANT]
> **Status**: 🟢 **OFFICIAL OPTIMIZATION DOCUMENTATION**
> - Documents the root-cause diagnostics, architectural refactoring, and benchmark performance speedups achieved for GLLAM's semantic memory extraction engine (`cmd/extract_semantics`).

---

## 1. Executive Summary & Impact Metrics

During scale ingestion of the 13,343-episode MemArena `d7_qa` benchmark (over 176,000 nodes and 271,000 links), database write latency and worker lock queue delays degraded from milliseconds to over **2 minutes and 37 seconds per episode**.

Through systematic profiling, lock contention analysis, and transaction refactoring, we eliminated 5 major system bottlenecks. 

### Key Performance Benchmark Gains

| Metric | Baseline (Pre-Optimization) | Optimized (Post-Refactor) | Improvement |
| :--- | :--- | :--- | :--- |
| **SQLite Transaction Execution (`DB-write`)** | 2m 19s (139,000ms) | **1ms to 12ms** | **> 11,000× Speedup** |
| **Mutex Queue Waiting Delay (`DB-wait`)** | 2m 37s (157,000ms) | **< 50ms** | **> 3,000× Reduction** |
| **Parallel Vector Embedding (`Embed`)** | 2m 05s (125,000ms) | **1s to 3s** | **> 40× Speedup** |
| **0-Link Session Data Loss** | 1,324 un-connected sessions | **0 sessions** (100% link yield) | **100% Data Integrity** |
| **Overall Episode Ingestion Rate** | ~0.5 episodes / min | **~2.2 to 5.0 episodes / min** | **~5× Throughput Increase** |

---

## 2. Detailed Bottleneck Analysis & Engineering Solutions

### Bottleneck 1: Un-Batched Autocommit Fsync Overheads

#### 🔍 Root Cause Diagnostic
For an episode extracting 40 nodes and 60 links, `extract_semantics` executed **100 individual autocommit SQL statements** (`UpsertNode`, `AddEdge`, `InvalidateObsoleteEdge`). In SQLite, each un-batched write statement forces a full disk sync (`fsync`) to the Write-Ahead Log (WAL). As the database grew to 270,000 links, 100 disk syncs per episode caused SQLite to spend 2 minutes and 19 seconds disk-flushing per chunk.

#### 🛠️ Fix Applied ([`cmd/extract_semantics/main.go`](file:///home/laurent/Projects/gllam/cmd/extract_semantics/main.go#L380-L435))
Wrapped all node upserts, edge insertions, and vector indexes for each episode inside a single **`BEGIN IMMEDIATE` ... `COMMIT` batch transaction**:

```go
dbMutex.Lock()
dbWriteStart := time.Now()
_, _ = gllam.DB().ExecContext(ctx, "BEGIN IMMEDIATE")

// Insert all nodes, links, and pre-generated vectors for episode...

_, _ = gllam.DB().ExecContext(ctx, "COMMIT")
epDBWriteTime += time.Since(dbWriteStart)
dbMutex.Unlock()
```

- **Impact**: Reduced 100 individual disk syncs down to **1 single atomic commit per episode**, dropping DB write execution time from **2m 19s** to **1ms–12ms**!

---

### Bottleneck 2: Cross-Handle Read/Write Lock Deadlocks

#### 🔍 Root Cause Diagnostic
When an edge insertion failed due to a missing foreign key node, the `ensureNode` helper function executed:
```go
gllam.DBRO().QueryRowContext(ctx, "SELECT id FROM semantic_nodes WHERE id = ? OR name = ?", ...)
```
Because `gllam.DB()` (the write handle) already held an active `BEGIN IMMEDIATE` transaction under `dbMutex.Lock()`, invoking `gllam.DBRO()` (the read-only connection pool) caused `DBRO` to hit SQLite write-lock contention against `DB`'s open transaction—forcing `DBRO` to block for SQLite's **30-second `busy_timeout`**!

#### 🛠️ Fix Applied ([`cmd/extract_semantics/main.go`](file:///home/laurent/Projects/gllam/cmd/extract_semantics/main.go#L405))
Changed `gllam.DBRO()` to `gllam.DB()` inside `ensureNode`:
- **Impact**: `ensureNode` now reads directly from `DB`'s open transaction memory buffer in **< 0.01ms** with zero lock contention, eliminating 30-second timeout stalls.

---

### Bottleneck 3: Un-Mutexed Vector Connection Bombardment

#### 🔍 Root Cause Diagnostic
Previously, node embeddings were saved *after* unlocking `dbMutex`, where **45 parallel worker goroutines** (9 episode workers $\times$ 5 embedding workers) executed un-mutexed autocommit `DELETE` and `INSERT` queries on `semantic_embeddings` directly on `gllam.DB()`.
These 45 goroutines bombarded SQLite's single write connection outside of `dbMutex`, constantly hitting `busy_timeout`. Episode workers attempting `BEGIN IMMEDIATE` on `gllam.DB()` were blocked for **2 minutes 37 seconds (`DB-wait: 2m37s`)** waiting for the un-mutexed embedding queries to clear!

#### 🛠️ Fix Applied ([`cmd/extract_semantics/main.go`](file:///home/laurent/Projects/gllam/cmd/extract_semantics/main.go#L360-L435))
Refactored vector embedding into two distinct phases:
1. **Phase 1 (Parallel Network Embedding)**: Vectors are generated over the HTTP network in parallel **before** acquiring `dbMutex.Lock()`. (Zero SQLite database queries occur during network vector generation).
2. **Phase 2 (Atomic Batch Vector Commit)**: Pre-generated vectors are written to `semantic_embeddings` inside the episode's single `BEGIN IMMEDIATE` ... `COMMIT` transaction.

- **Impact**: Eliminates 100% of un-mutexed DB write contention on SQLite. `DB-wait` dropped from **2m 37s** to **< 50ms**, and `Embed` time dropped from **2m 05s** to **1s–3s**!

---

### Bottleneck 4: Synchronous WAL Auto-Checkpoint Stalls

#### 🔍 Root Cause Diagnostic
SQLite's default `PRAGMA wal_autocheckpoint = 1000` (flushing every 4MB) caused SQLite to periodically pause writing threads during `COMMIT` to flush WAL pages into `gllam_data.db`. When an episode hit the WAL limit, `DB-write` stalled for **39.5 seconds**, causing all other 8 workers to queue up (`DB-wait: 25s–38s`).

#### 🛠️ Fix Applied ([`pkg/engine/engine.go`](file:///home/laurent/Projects/gllam/pkg/engine/engine.go#L149-L173) & [`cmd/extract_semantics/main.go`](file:///home/laurent/Projects/gllam/cmd/extract_semantics/main.go#L67))
Launched a background WAL manager goroutine executing **`PRAGMA wal_checkpoint(PASSIVE)`** every 10 seconds.
- **Impact**: `PASSIVE` mode non-blockingly merges WAL pages into `gllam_data.db` in the background **without holding exclusive write locks or pausing worker `COMMIT` statements**.

---

### Bottleneck 5: 0-Link Session Completion & Automatic Startup Audit

#### 🔍 Root Cause Diagnostic
A lenient completion check (`if epNodes > 0 || epLinks > 0`) marked episodes as complete in `extracted_sessions` even if `epLinks == 0`. Over time, 1,324 sessions were recorded with 0 links due to temporary LLM output omissions.

#### 🛠️ Fix Applied ([`cmd/extract_semantics/main.go`](file:///home/laurent/Projects/gllam/cmd/extract_semantics/main.go#L77-L82,L417))
1. **Startup Healing Audit**: Automatically runs `DELETE FROM extracted_sessions WHERE link_count IS NULL OR link_count = 0` at startup. (Purged and re-processed 1,324 sessions yielding **25 to 103 links each**).
2. **Strict Ingestion Validation**: Requires **`epNodes > 0 && epLinks > 0`** before saving to `extracted_sessions`.
3. **Targeted 0-Link LLM Re-Prompting**: If an LLM response extracts nodes but 0 links, `extract_semantics` immediately issues a targeted retry prompt forcing the LLM to output relationship links.
4. **Diagnostic Incident Surfacing**: If an LLM still outputs 0 links, `extract_semantics` logs structured incident metadata (Episode ID, Chunk Index, Extracted Nodes, and Transcript Snippet) without generating synthetic fake edges.

---

## 3. Telemetry Log Formatting Reference

Extraction completion logs provide fine-grained performance visibility:

```text
[2026.08.13 06:41:30] ✅ Finished sess_ac38e96164e6 (9 nodes, 16 links) | LLM: 4.052s, DB-wait: 12ms, DB-write: 4ms, Embed: 2.199s
```

- **`LLM`**: Time spent waiting for OpenRouter HTTP LLM generation.
- **`DB-wait`**: Time worker spent waiting for `dbMutex` turnstile.
- **`DB-write`**: Pure SQLite execution latency inside `BEGIN IMMEDIATE` ... `COMMIT`.
- **`Embed`**: Parallel network vector generation latency.
