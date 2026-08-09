# GLLAM: Go Lightweight Local Agentic Memory

A fast agentic-memory engine:
- In Go for speed of execution
- SQLite + sqlite-vec for vector similarity search
- Opinionated procedural memory: use PDDL (Plan Domain Definition Language) for event sequences
- A semantic-graph augmented for validity-time and information-contradictions tracking 

## Architecture

Memory layers:
1. **Episodic** Interaction logs using sqlite-vec for semantic search.
2. **Semantic** Entities & Relationships graph with added timeline info.
3. **Procedural** Executable markdown, plus an embedded PDDL router with two logic engines: 1-Native STRIPS BFS, 2-delegation to Fast-Downward. Compiles the semantic graph into PDDL to remember timelines, preventing LLM's planning-logic errors.

### Design Decisions

- **Avoid Bloat**: No multi-layered data-extraction pipeline, no nested async generators, no runtime schema generation
- **Consolidate Storage**: Single SQLite file with `PRAGMA journal_mode = WAL` for concurrent reads
- **Caveat-Qualified Graph**: Semantic relationships carry explicit conditions/constraints/exceptions
- **Explicit Contradictions**: Conflicts are tracked as first-class relationships
- **Temporal Bounds**: Semantic relationships include validity date-time information
- **Vector Search**: sqlite-vec distance functions for episodic-memory retrieval

## Package Structure

```bash
gllam
├── cmd/gllam/
│   └── main.go              # CLI Local daemon entry point
├── pkg/
│   ├── engine
│   │   ├── engine.go        # SQLite connection, WAL, schema init
│   │   ├── semantic.go      # Node/link CRUD, contradiction management
│   │   ├── procedural.go    # Workflow recipes, helpfulness scoring
│   │   ├── episodic.go      # Session summaries, temporal windows
│   │   └── router.go        # Intent classifier & context assembler
│   ├── memory/
│   │   └── types.go         # All Go type definitions
│   └── schema/
│       └── schema.sql       # Complete SQLite DDL
├── go.mod
└── go.sum
```

## Installation

```bash
go mod init github.com/laurentalsina/gllam
go get github.com/mattn/go-sqlite3
go get github.com/asg017/sqlite-vec-go-bindings/cgo
```

### Build Requirements

sqlite-vec CGO bindings:

# Option 1: System-wide
```
sudo dnf install sqlite-devel  # Fedora/RedHat
sudo apt-get install libsqlite3-dev  # Ubuntu/Debian
```

# Option 2: Temporary include path
```
curl -sL https://www.sqlite.org/2024/sqlite-amalgamation-3470200.zip -o /tmp/sqlite.zip
unzip -qo /tmp/sqlite.zip -d /tmp
export CGO_CFLAGS="-I/tmp/sqlite-amalgamation-3470200"
```

Then build with CGO enabled:
```
CGO_ENABLED=1 go build ./...
```

## Startup

# Start embeddings server (separate process)
llama-server -m your-embedding-model.gguf --port 8080

# Seed sample data
go run ./cmd/gllam -seed

# Recall with auto-routing
go run ./cmd/gllam --recall "how to deploy caddy" --entity "caddy"

# Recall with embeddings
go run ./cmd/gllam --embeddings-server http://localhost:8080 --recall "web server" --entity "caddy"

# Interactive REPL
go run ./cmd/gllam --embeddings-server http://localhost:8080
```

## Usage as a Library

import (
    "context"
    "log"
    "time"

    "github.com/laurentalsina/gllam/pkg/engine"
    "github.com/laurentalsina/gllam/pkg/memory"
)

// Initialize with llama.cpp embedder
embedder := engine.NewLlamaEmbedder("http://localhost:8080")
gllam, err := engine.NewGllamEngine("./data.db", embedder)
if err != nil { log.Fatal(err) }
defer gllam.Close()

if err := gllam.InitSchema(); err != nil { log.Fatal(err) }

// Store a semantic node
gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy", Name: "Caddy", Type: "service"})

// Generate and store embedding for the node
if err := gllam.StoreNodeEmbedding(ctx, "caddy"); err != nil {
    log.Fatal(err)
}

// Store a caveat-qualified link
gllam.AddEdge(ctx, memory.SemanticLink{
    SourceID: "caddy", TargetID: "tailscale",
    Relationship: "binds_to",
    Caveats: "Must use Tailscale FQDN",
    ValidFrom: time.Now().Unix(),
})

// Route a user prompt and assemble context
ctxResult, err := gllam.RouteAndAssemble(ctx, "how to deploy caddy", []string{"caddy"})
if err != nil { log.Fatal(err) }

// Format for LLM consumption
prompt := engine.FormatSystemPrompt(ctxResult)
fmt.Print(prompt)

// Semantic similarity search
results, err := gllam.SearchSimilarNodes(ctx, "web server", 5)
if err != nil { log.Fatal(err) }
for _, r := range results {
    fmt.Printf("Node: %s (distance: %.4f)\n", r.NodeID, r.Distance)
}

## Database Schema

| Table | Go Type | Purpose |
|-------|---------|---------|
| `semantic_nodes` | `memory.SemanticNode` | Entities (services, IPs, configs...), includes `context_prompt` |
| `semantic_links` | `memory.SemanticLink` | Caveat-qualified, temporally bounded relationships |
| `procedural_knowledge` | `memory.ProceduralKnowledge` | Reusable workflow recipes |
| `episodic_summaries` | `memory.EpisodicSummary` | Session summaries with timestamps |
| `semantic_embeddings` | *vec0 virtual table* | Embedding vectors for similarity search (sqlite-vec) |

### SQLite Configuration

Enforced at connection time:
```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

Connection architecture: dual-handle design for true read concurrency:
- **Write handle**: `SetMaxOpenConns(1)` — serializes all mutations through a single connection
- **Read handle**: opened with `mode=ro` DSN flag, `SetMaxOpenConns(8)` — concurrent read-only queries never contend with the writer

WAL-mode readers don't block each other, and the read-only file descriptor is completely independent of the writer.

## API Reference

### Engine

| Method | Description |
|--------|-------------|
| `NewGllamEngine(dbPath, embedder)` | Open dual-handle SQLite connection with sqlite-vec and embedder |
| `InitSchema()` | Execute `schema.sql` DDL |
| `Close()` | Close both database handles |
| `DB()` | Return write handle for direct SQL access |
| `DBRO()` | Return read-only handle for concurrent queries |

### Embedding

| Method | Description |
|--------|-------------|
| `StoreNodeEmbedding(ctx, nodeID)` | Generate and store embedding for a node |
| `SearchSimilarNodes(ctx, query, limit)` | Find nodes similar to query text |
| `NewLlamaEmbedder(baseURL)` | Create embedder for llama.cpp server |

### Semantic (Graph)

| Method | Description |
|--------|-------------|
| `UpsertNode(ctx, node)` | Insert or update a node (including its `context_prompt`) |
| `AddEdge(ctx, link)` | Insert link; auto-creates contradiction nodes/edges on conflict |
| `InvalidateObsoleteEdge(ctx, ...)` | Set `valid_until` for temporal expiration |

### Procedural

| Method | Description |
|--------|-------------|
| `UpsertProceduralKnowledge(ctx, pk)` | Insert/update recipe (version auto-increments) |
| `MarkProcedureHelpful(ctx, taskType, helpful)` | Toggle golden-standard flag |
| `RetrieveProcedure(ctx, taskType)` | Fetch recipe + increment `times_applied` |
| `GetTopProcedures(ctx, limit)` | Ordered by helpfulness then usage |

### Episodic

| Method | Description |
|--------|-------------|
| `SaveEpisodicSummary(ctx, summary)` | Store session summary |
| `GetRecentEpisodes(ctx, limit)` | Top N by `created_at DESC` |
| `GetEpisodesInWindow(ctx, start, end)` | Temporal range query |

### Router

| Method | Description |
|--------|-------------|
| `RouteAndAssemble(ctx, prompt, entities)` | Classify intent, retrieve relevant data |
| `FormatSystemPrompt(ctx)` | Format compiled context as Markdown |

### Vector Search

sqlite-vec is registered globally via `sqlite_vec.Auto()` and provides SIMD-optimized vector operations. Use directly in SQL queries:

```sql
-- Create a vector virtual table
CREATE VIRTUAL TABLE semantic_embeddings USING vec0(
    node_id INTEGER PRIMARY KEY,
    embedding float[1536]
);

-- Insert embeddings
INSERT INTO semantic_embeddings(node_id, embedding)
VALUES (?, vec_f32(?));

-- Similarity search (lower distance = more similar)
SELECT node_id, distance
FROM semantic_embeddings
WHERE embedding MATCH vec_f32(?)
ORDER BY distance
LIMIT 10;
```

## Contradiction Model & Byzantine Fallacy Handling

Contradictions that remain stored will be of a temporal nature, eg. only version x of some software supports feature y.

When `AddEdge()` detects an existing active link with the same `source_id` and a mutually exclusive `relationship` (e.g. `has_state`, `located_in`) but a different `target_id`:

### Epistemic Hierarchy (Source Trust Weighting)

1. It compares the `trust_weight` (integer in `[10, 1000]`) of both origin sources (`OriginSourceID`).
2. If a highly trusted source (e.g. `Jira Resolved` or `Merged Pull Request`, $W = 900$) conflicts with a low-trust source (e.g. `Email Draft`, $W = 100$), the low-trust claim is **automatically expired** (`valid_until = now`) and superseded with a `resolves_conflict` edge.
3. This completely bypasses the need for manual user grilling or unresolved contradiction nodes!
4. If trust weights are equal, GLLAM falls back to creating an explicit `NodeTypeContradiction` node for planner or user resolution (or auto-resolves by recency if `AllowUserGrilling = false`).

### Multi-Factor Composite Trust Weight Calculation

$$W_{\text{composite}} = \text{Clamp}\Big( W_{\text{doc\_type}} + W_{\text{source}} + \Delta W_{\text{coherence}} + \Delta W_{\text{temporal\_freshness}},\, 10,\, 1000 \Big)$$

* **Document Type Base ($W_{\text{doc\_type}}$):** Jira Resolved / Merged PR (800), Approved Architecture Doc (700), Jira Open / Slack / Incident Log (600), Email Thread / Support Ticket (400), Draft / Scratchpad (200).
* **Source Identity ($W_{\text{source}}$):** Person / source handle adjustments from `SourceReliabilityHeuristics` (e.g. Alice `+150`, Carol `+200`, Dave `-150`), falling back to roles (System CI/CD `+150`, Tech Lead `+100`, Verified Eng `+50`).
* **Internal Semantic Coherence ($\Delta W_{\text{coherence}}$):** Shannon character entropy & non-lexical noise check (+50 boost for valid prose, -250 penalty for DoS / gibberish).
* **Temporal Freshness ($\Delta W_{\text{temporal\_freshness}}$):** < 30 days old (+50), > 6 months old (-50), > 1 year old (-150).

## Agentic Memory System Prompting & Configuration

GLLAM provides a dedicated JSON configuration system (`config/agentic_memory_prompts.json` & `pkg/config/agentic_memory.go`) to define system prompts, repository directives, and source trust heuristics without re-compiling code:

```json
{
  "allow_user_grilling": true,
  "ingestion_steering_directives": {
    "confluence": { "track_revision_history": true, "max_revision_depth": 10, "compact_author_epochs": true },
    "jira": { "track_comment_history": true, "track_status_transitions": true, "compact_author_epochs": true },
    "git": { "track_branch_merges": true, "track_revision_history": true, "compact_author_epochs": true }
  },
  "custom_document_type_rules": {
    "notion_workspace": {
      "type_name": "notion_workspace",
      "baseline_trust_weight": 600,
      "ingestion_strategy": { "track_revision_history": true, "compact_author_epochs": true }
    }
  },
  "repository_context_directives": {
    "jira": {
      "repository_type": "jira",
      "extraction_prompt": "Extract Jira issue key, status transitions, resolution, priority, and epic linkage into entity context profiles.",
      "context_template": "Jira Issue: {{key}}\nType: {{type}}\nStatus: {{status}}\nResolution: {{resolution}}"
    }
  },
  "trust_weight_prompt": "EVALUATION RULESET FOR SOURCE TRUST WEIGHTING (W in [10, 1000])...",
  "source_reliability_prompt": "INDIVIDUAL SOURCE RELIABILITY HEURISTICS...",
  "source_reliability_heuristics": {
    "alice": 150,
    "carol_lead": 200,
    "dave_drafts": -150
  }
}
```

```go
// Load custom agentic memory prompts
err := gllam.LoadSystemPromptsConfig("./config/agentic_memory_prompts.json")

// Register dynamic custom document type
gllam.RegisterCustomDocumentTypeRule(config.CustomDocumentTypeRule{
    TypeName:            "notion_workspace",
    BaselineTrustWeight: 650,
    IngestionStrategy:   config.IngestionStrategy{TrackRevisionHistory: true, CompactAuthorEpochs: true},
})

// Register documentation repository context directive
gllam.RegisterRepositoryContextDirective(config.RepositoryContextDirective{
    RepositoryType:   "sharepoint",
    ExtractionPrompt: "Extract SharePoint site URL, document library, and version label into entity context profiles.",
    ContextTemplate:  "SharePoint Doc: {{doc_name}}\nLibrary: {{library}}\nSite: {{site}}",
})

// Attribute comment inside Jira container directly to individual source node
sourceID, trustWeight, err := gllam.AttributeContainerEntryToSource(ctx, "jira", "alice", "Alice Smith", "Comment 1: DB is PostgreSQL 15.", time.Now().Unix())
```

### Autonomous Ontological Layer (Materialized Paths & Self-Healing Taxonomy)

To prevent a flat semantic graph from devolving into an unsearchable hairball across 15,000+ Jira issues and 10,000+ Confluence pages, GLLAM features an **Autonomous Ontological Layer**:

1. **Materialized Path SQLite Indexing:**
   * Nodes store hierarchical paths in `taxonomy_path TEXT DEFAULT '/'` (e.g. `/Engineering/Infrastructure/Databases/Relational/Postgres`) and boolean `is_category INTEGER DEFAULT 0`.
   * Enables instantaneous hierarchical filtering via `taxonomy_path LIKE '/Engineering/Infrastructure/Databases/%'`.
2. **Asynchronous Batch Categorization (`ProcessUncategorizedBatch`):**
   * Decoupled from bulk ingestion pipeline; pulls orphaned nodes (`taxonomy_path = '/'`) and categorizes them into category nodes (`NodeTypeCategory = "category"`) with explicit `is_a` links.
3. **Cyclic Path Prevention (`DetectTaxonomyCycles` & `WouldCreateTaxonomyCycle`):**
   * Uses **Kahn's Topological Sort Algorithm** to detect and prevent circular parent-child relationships (e.g. `/Infrastructure/Storage` $\rightarrow$ `/Storage/Databases` $\rightarrow$ `/Infrastructure/Storage`).
4. **Self-Healing Taxonomy Consolidation (`ConsolidateTaxonomyBranch`):**
   * Merges redundant categories (e.g. `/Engineering/DBs` into `/Engineering/Infrastructure/Databases`) in an atomic transaction that rewrites child node paths and redirects `is_a` edges.
5. **Procedural Domain Binding (`GetProceduresByTaxonomyPrefix`):**
   * Supercharges procedural knowledge retrieval by isolating operational recipes bound to target taxonomy sub-trees.

```go
// Instantaneous hierarchical filtering
nodes, err := gllam.GetNodesByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Databases")

// Asynchronous batch classification of orphaned nodes
processedCount, err := gllam.ProcessUncategorizedBatch(ctx, 50)

// Detect circular taxonomy paths across is_a/subclass_of edges
hasCycle, cyclicNodes, err := gllam.DetectTaxonomyCycles(ctx)

// Self-healing taxonomy branch consolidation
err := gllam.ConsolidateTaxonomyBranch(ctx, "/Engineering/DBs", "/Engineering/Infrastructure/Databases")

// Domain-isolated procedural recipe retrieval
recipes, err := gllam.GetProceduresByTaxonomyPrefix(ctx, "/Engineering/Infrastructure/Databases")
```


### Memory Maintenance Cycle & Synthetic Random Trace Tests

High-scale memory systems require a periodic offline **Memory Maintenance Cycle** (`EnterMemorySleepCycle`) to prevent memory degradation and measure graph consistency:

1. **Maintenance Compaction & Pruning:**
   * Automatically purges expired temporal links (`valid_until <= now`).
   * Runs self-healing taxonomy branch consolidation (`ConsolidateTaxonomyBranch`).
   * Processes orphaned entity nodes (`ProcessUncategorizedBatch`).
2. **Synthetic Random Trace Tests & Memory Exercise (`SimulateRandomTraceTests`):**
   * Generates synthetic question/answer trace scenarios across randomly sampled entity pairs.
   * Exercises multi-hop graph retrieval (`FindMultiHopPath`).
   * Measures quantitative metrics:
     * **Memory Clarity Score ($\text{Clarity} \in [0.0, 1.0]$):** Proportion of unambiguous, uncontradicted graph paths.
     * **Memory Consistency Score ($\text{Consistency} \in [0.0, 1.0]$):** Ratio of consistent simulated answers across domains.

```go
// Trigger offline memory maintenance cycle with 10 synthetic random trace tests
report, err := gllam.EnterMemorySleepCycle(ctx, 10)

fmt.Printf("Pruned Stale Links: %d\n", report.PrunedStaleLinksCount)
fmt.Printf("Memory Clarity Score: %.2f\n", report.MemoryClarityScore)
fmt.Printf("Memory Consistency Score: %.2f\n", report.MemoryConsistencyScore)
```

### WAL Checkpoint Management & Read-Only Handle Enforcement

To prevent WAL file swelling and checkpoint stalls (`SQLITE_BUSY`) during high-frequency bulk ingestion (e.g. 15,000+ Jira issues and 10,000+ Confluence pages), GLLAM implements explicit WAL checkpointing and strict read-only handle isolation:

1. **Dedicated Connection Pragmas:**
   * **Write Handle (`db`):** `MaxOpenConns = 1`, `PRAGMA journal_mode = WAL`, `PRAGMA wal_autocheckpoint = 1000`, `PRAGMA busy_timeout = 5000`.
   * **Read Handle (`dbRO`):** `MaxOpenConns = 8`, `PRAGMA query_only = ON;`, `PRAGMA busy_timeout = 5000;`. Read operations cannot lock the writer or attempt invalid mutations.
2. **Explicit WAL Checkpoint API (`CheckpointWAL`):**
   * Executes explicit `PRAGMA wal_checkpoint(RESTART)` or `PRAGMA wal_checkpoint(TRUNCATE)` flushes.
3. **Background Checkpoint Manager (`StartWALCheckpointManager`):**
   * Asynchronous background goroutine that manages WAL size during idle ingestion windows.

```go
// Launch background WAL Checkpoint Manager running every 5 seconds
gllam.StartWALCheckpointManager(ctx, 5*time.Second)

// Manually trigger a WAL restart or truncation checkpoint
logPages, checkpointedPages, err := gllam.CheckpointWAL(ctx, "RESTART")
```

### Vector Space Drift Prevention & Re-Embedding

Swapping or upgrading the local embedding model midway through ingesting a dataset makes stored vector embeddings in `sqlite-vec` mathematically incompatible with new embeddings, degrading Reciprocal Rank Fusion (`RetrieveHybridNeedle`). GLLAM prevents vector space drift through automated version metadata tracking and background re-indexing:

1. **Model Version Metadata:** Tracks `embedding_model_version` in `system_metadata`.
2. **Drift Detection (`CheckEmbeddingModelVersion`):** Compares stored model version with active `embedder.ModelVersion()`.
3. **Automated Re-Embedding (`ReembedAllSemanticNodes`):** Background worker re-computes vector embeddings across all `semantic_nodes` and updates `semantic_embeddings` virtual table.

```go
// Check for vector space drift on engine initialization
drift, prevModel, activeModel, err := gllam.CheckEmbeddingModelVersion(ctx)
if drift {
    log.Printf("Vector space drift detected (%s -> %s). Re-embedding nodes...", prevModel, activeModel)
    reembeddedCount, err := gllam.ReembedAllSemanticNodes(ctx)
}
```

### Node Caveat Compaction & Salience Windowing

Core enterprise hub entities (e.g. *"Auth Service"* or *"Production Database"*) accumulate hundreds of caveats across years of Jira tickets, causing context window bloat and confusing LLM reasoning. GLLAM implements salience windowing and node caveat compaction:

1. **Caveat Ranking:** Orders caveats by Active Validity (`valid_until IS NULL`), Source Trust Weight ($W_{\text{trust}}$), and Recency.
2. **Inline Windowing (`maxInline`):** Retains Top-K (default 5) active high-trust caveats inline.
3. **Historical Epoch Compaction (`CompactNodeCaveats`):** Synthesizes older/lower-trust caveats into a node-level `caveat_summary` string stored on `semantic_nodes`.

```go
// Compact caveats for a hub entity node, retaining Top 5 inline caveats
summaryText, retainedCount, prunedCount, err := gllam.CompactNodeCaveats(ctx, "node-auth-service", 5)

// Run batch compaction across all hub entities with > 10 caveats
compactedHubs, err := gllam.BatchCompactHubCaveats(ctx, 10, 5)

```

### Asynchronous Vector Index Worker Pool

When scaling past 100,000 extracted `semantic_nodes`, executing embedding model calls and updating virtual vector tables (`sqlite-vec` `vec0`) inside the primary write transaction drastically increases commit latency. GLLAM decouples relational graph insertion from vector virtual table creation:

1. **Sub-Millisecond Relational Commit:** `UpsertNode` commits relational graph entities to SQLite immediately.
2. **Background Unembedded Queue (`ProcessUnembeddedNodeVectorBatch`):** Queries nodes where `v.node_id IS NULL`.
3. **Worker Pool (`StartVectorEmbeddingWorkerPool`):** Launches background worker goroutines that generate embeddings and populate `semantic_embeddings` asynchronously.

```go
// Launch background vector worker pool (2 workers polling every 2 seconds)
gllam.StartVectorEmbeddingWorkerPool(ctx, 2, 2*time.Second)

// Manually process a batch of unindexed vector embeddings
indexedCount, err := gllam.ProcessUnembeddedNodeVectorBatch(ctx, 50)
```

### Active Stack Cycle Detection & Cascading Invalidation

In enterprise datasets with circular component dependencies (e.g. `Service A` $\rightarrow$ `Spec B` $\rightarrow$ `Rule C` $\rightarrow$ `Service A`), upstream state invalidation risks entering infinite recursive loops. GLLAM implements active stack cycle prevention:

1. **Active Call Stack Tracking (`activeStack`):** Tracks nodes in the current recursion branch. If `activeStack[currentNodeID] == true`, branch recursion terminates with a diagnostic log.
2. **Adaptive Propagation Depth:** Allows deep dependency propagation (default 10 hops) without getting trapped in circular graph loops.

```go
// Trigger cascading cross-cutting invalidation across downstream dependencies
err := gllam.InvalidateDependentCrossCuttingLinks(ctx, "service-a", "2000")
```











### Logical Fallacy Taxonomy & Terminology Guide


GLLAM treats logical fallacies in user or agent input as first-class cognitive nodes (`NodeTypeFallacy`) to prevent deceptive or flawed premises from corrupting automated reasoning.

Fallacies are classified across **6 major categories** (referencing the [Wikipedia List of Fallacies](https://en.wikipedia.org/wiki/List_of_fallacies)):

| Fallacy Key | Plain English Meaning & Example | Engine Impact |
| :--- | :--- | :--- |
| **`post_hoc`** *(Post Hoc Ergo Propter Hoc)* | *"After this, therefore because of this"* — Blindly assuming Event A caused Event B simply because B occurred after A *(e.g. "We deployed Caddy, then the server rebooted, so Caddy crashed the server")*. | Downgrades `causes` link to a weak `happened_before` temporal observation. |
| **`cum_hoc`** *(Cum Hoc Ergo Propter Hoc)* | *"With this, therefore because of this"* — Confusing correlation with causation *(e.g. "CPU usage rose whenever user logins increased")*. | Prevents inserting hard `depends_on` dependencies without explicit proof. |
| **`false_dilemma`** | *"False Dichotomy"* — Forcing a fake binary choice when middle options exist *(e.g. "Either we delete the database or the migration fails")*. | Prevents promoting either choice to a `global` or `must_follow_rule` constraint. |
| **`begging_question`** *(Circularity)* | Premise assumes the unproven conclusion *(e.g. "Postgres is reliable because Postgres never fails")*. | Disables cyclic PDDL action preconditions. |
| **`equivocation`** | Using an ambiguous term in two different senses within the same premise *(e.g. using "service" to mean both systemd service and cloud API)*. | Triggers `DisambiguateEntityForSource` to split ambiguous nodes. |
| **`ad_hominem`** | Attacking the person or agent issuing the claim rather than addressing the claim's substance. | Preserves the underlying claim, flags source attack edge. |
| **`straw_man`** | Misrepresenting a rule or claim to make it easy to refute or override. | Prevents overriding established rules without matching rationale. |
| **`red_herring`** | Introducing an irrelevant topic to distract from an active contradiction. | Suppresses multi-hop graph expansion for that sub-graph during retrieval. |


## Benchmarking Tools

to evaluate retrieval accuracy on  memory benchmarks:
- **MemArena**: Use cmd/eval_d7_qa to test multi-hop retrieval accuracy.
- **BEAM 100K**: 
  - **Ingestion**: Use cmd/ingest_beam to chunk 100,000-token conversations.
  - **Evaluation**: Use cmd/eval_beam to query the session LLM over the  episodic context retrieved. Evaluates cognitive memory dimensions like *Contradiction Resolution* and *Abstention*. We use random sampling over the 400 total..

## Embedding Architecture

GLLAM uses a pluggable embedder interface for generating vector embeddings:

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
}
```

### Default: Embeddings Server

The `LlamaEmbedder` connects to a running embeddings server (e.g., `llama-server`):

```bash
# Start embeddings server with an embedding model
llama-server -m nomic-embed-text.gguf --port 8080
```

```go
embedder := engine.NewLlamaEmbedder("http://localhost:8080")
gllam, err := engine.NewGllamEngine("./data.db", embedder)
```

**Behavior:**
- Hard fail if server is unreachable (no fallback)
- 30-second timeout per request
- Embeddings generated on-demand via `StoreNodeEmbedding()` or `SearchSimilarNodes()`

### Custom Embedders

Implement the `Embedder` interface for other sources:

```go
type MyEmbedder struct { /* ... */ }

func (m *MyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
    // Your embedding logic
    return vector, nil
}
```

## Local Dependencies

When running the full GLLAM locally, it requires an LLM, an embedding model, and the Fast Downward solver. 

### AI Models
Start the models, for example:
```bash
cd ~/your_llm_servers_folder/
./serve_Ornith-1.0.sh
./serve_qwen3.6_embeddings.sh
```

### Fast Downward (Full PDDL Planner)
Requires the Fast Downward C++ binary:
```bash
cd ~/Projects
git clone https://github.com/aibasel/downward.git
cd downward
./build.py
```
*(GLLAM's `FastDownwardPlanner` defaults to executing `~/Projects/downward/fast-downward.py`)*

## License

(C) Laurent Alsina Blackmore 2026 -  All rights reserved
