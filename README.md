# GLLAM: Go Lightweight Local Agentic Memory

A fast single-file agentic-memory engine with vector similarity search:
- Karpathy's LLM-Wiki idea plus some temporal-semantics
- Backed by SQLite in WAL for concurrent access
- sqlite-vec for SIMD-optimized vector similarity search
- In Go for speed of execution

## 🧠 Core Architecture

GLLAM consists of three distinct memory layers managed by a dual-tier cognitive engine:
1. **Episodic Memory:** Raw, chronological interaction logs using `sqlite-vec` for brute-force semantic search.
2. **Semantic Memory:** An abstracted, highly condensed Graph (Nodes & Edges) representing factual entities, relationships, and chronological state timelines (e.g. `valid_from` timestamps).
3. **Procedural Memory:** Executable Markdown routines and strict PDDL `(:action)` blocks that give the agent deterministic workflows and state-space logic.
4. **Neuro-Symbolic PDDL Router (v0.2):** An embedded dual-tier logic engine (Native STRIPS BFS + Fast Downward) that compiles the semantic graph into PDDL and mathematically proves timelines to eliminate LLM logic hallucinations.

### Key Design Decisions

- **Avoid Bloat**: No multi-layered ECL pipelines, no nested async generators, no runtime schema generation
- **Dual-Handle Concurrency**: Write handle (`SetMaxOpenConns(1)`) serializes mutations; read handle (`mode=ro`, `SetMaxOpenConns(8)`) enables concurrent queries without contention
- **Consolidated Storage**: Single SQLite file with `PRAGMA journal_mode = WAL` for concurrent reads
- **Caveat-Qualified Graph**: Semantic relationships carry explicit conditions/constraints/exceptions
- **Explicit Contradictions**: Conflicts are tracked as first-class relationships
- **Temporal Bounds**: Semantic relationships include validity date-time information (`valid_from` / `valid_until`)
- **Tiered Intent Routing**: Heuristic classification and vector similarity search for memory access from Procedural, Semantic, or Episodic stores
- **Vector Search**: sqlite-vec provides SIMD-optimized distance functions for embedding similarity

## Package Structure

```
gllam/
├── cmd/gllam/
│   └── main.go              # CLI / Local daemon entry point
├── pkg/
│   ├── engine/
│   │   ├── engine.go        # SQLite connection, WAL pragmas, schema init
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

sqlite-vec CGO bindings require `sqlite3.h`. Install system-wide or set `CGO_CFLAGS`:

```bash
# Option 1: System-wide (recommended)
sudo dnf install sqlite-devel  # Fedora/RHEL
# sudo apt-get install libsqlite3-dev  # Ubuntu/Debian
# sudo apk add sqlite-dev  # Alpine

# Option 2: Temporary include path
curl -sL https://www.sqlite.org/2024/sqlite-amalgamation-3470200.zip -o /tmp/sqlite.zip
unzip -qo /tmp/sqlite.zip -d /tmp
export CGO_CFLAGS="-I/tmp/sqlite-amalgamation-3470200"
```

Then build with CGO enabled:
```bash
CGO_ENABLED=1 go build ./...
```

## Quick Start

```bash
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

```go
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
```

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

## Contradiction Model

Contradictions that remain stored will be of a temporal nature, eg. only version x of some software supports feature y.

When `AddEdge()` detects an existing active link with the same `source_id` and a mutually exclusive `relationship` (e.g. `has_state`, `located_in`) but a different `target_id`, it automatically shifts to a graph-native contradiction model:
1. It creates a new `SemanticNode` of type `contradiction`.
2. It adds a `has_unresolved_conflict` edge from the source to the contradiction node.
3. It adds `conflicting_claim` edges from the contradiction node to all mutually exclusive targets.

The router detects the presence of `has_unresolved_conflict` edges and surfaces a warning to the LLM to ask the user for clarification.

## Benchmarking Tools

GLLAM provides robust scripts to evaluate retrieval accuracy over large-scale, long-term memory scenarios:
- **MemArena**: Evaluated via `cmd/eval_d7_qa` to test accurate multi-hop retrieval.
- **BEAM 100K**: 
  - **Ingestion**: Handled by `cmd/ingest_beam` to chunk massive 100,000-token multi-session conversations natively using session boundaries.
  - **Evaluation**: Handled by `cmd/eval_beam` which queries the LLM over the full 100K-token episodic context retrieved. Evaluates cognitive memory dimensions like *Contradiction Resolution* and *Abstention*. We use random sampling over the 400 total questions to accelerate test cycles while preserving the extreme context window challenge.

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

## License

(C) Laurent Alsina Blackmore 2026 -  All rights reserved
