# GLLAM: Go Lightweight Local Agentic Memory

A high-performance, single-file local memory engine for AI agents, backed by SQLite with WAL mode for concurrent access.

## Architecture

GLLAM replaces abstract, latency-heavy Python pipelines (such as `cognee` or LangChain memory modules) with a streamlined, concurrent Go architecture. All data is stored in a single local SQLite file with three memory types:

- **Episodic** — Session summaries with temporal rolling-window queries
- **Procedural** — Reusable step-by-step workflow recipes with helpfulness scoring
- **Semantic** — Caveat-qualified entity graph with explicit contradiction tracking

### Key Design Decisions

- **Zero Bloat**: No multi-layered ECL pipelines, nested async generators, or runtime schema generation
- **Consolidated Storage**: Single SQLite file with `PRAGMA journal_mode = WAL` for concurrent reads
- **Caveat-Qualified Graph**: Every relationship carries explicit conditions/constraints/exceptions
- **Explicit Contradictions**: Conflicts are tracked as first-class relationships, not boolean flags
- **Temporal Bounds**: Knowledge has `valid_from` / `valid_until` for automatic expiration
- **Tiered Intent Routing**: Heuristic classification into Procedural / Semantic / Episodic retrieval paths

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
```

## Quick Start

```bash
# Seed sample data
go run ./cmd/gllam -seed

# Query with auto-routing
go run ./cmd/gllam -query "how to deploy caddy" -entity "caddy"

# Interactive REPL
go run ./cmd/gllam
```

## Usage as a Library

```go
import "github.com/laurentalsina/gllam/pkg/engine"

// Initialize
gllam, err := engine.NewGllamEngine("./data.db")
if err != nil { log.Fatal(err) }
defer gllam.Close()

if err := gllam.InitSchema(); err != nil { log.Fatal(err) }

// Store a semantic node
gllam.UpsertNode(ctx, memory.SemanticNode{ID: "caddy", Name: "Caddy", Type: "service"})

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
```

## Database Schema

| Table | Go Type | Purpose |
|-------|---------|---------|
| `semantic_nodes` | `memory.SemanticNode` | Entities (services, IPs, configs...) |
| `semantic_links` | `memory.SemanticLink` | Caveat-qualified, temporally bounded relationships |
| `contradictions` | `memory.Contradiction` | Explicit conflict relationships between links |
| `procedural_knowledge` | `memory.ProceduralKnowledge` | Reusable workflow recipes |
| `episodic_summaries` | `memory.EpisodicSummary` | Session summaries with timestamps |

### SQLite Configuration

Enforced at connection time:
```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
```

Connection pool: `SetMaxOpenConns(1)` (single-writer model prevents SQLite table-lock contention).

## API Reference

### Engine

| Method | Description |
|--------|-------------|
| `NewGllamEngine(dbPath)` | Open/configure SQLite connection |
| `InitSchema()` | Execute `schema.sql` DDL |
| `Close()` | Close database connection |

### Semantic (Graph)

| Method | Description |
|--------|-------------|
| `UpsertNode(ctx, node)` | Insert or update a node |
| `AddEdge(ctx, link)` | Insert link; auto-creates contradiction on conflict |
| `CreateContradiction(ctx, ...)` | Record a contradiction between two links |
| `ResolveContradiction(ctx, id, notes)` | Mark contradiction resolved |
| `GetUnresolvedContradictions(ctx)` | List all active conflicts |
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

## Contradiction Model

Unlike boolean flags, contradictions are first-class relationships:

```
contradictions
├── id: "contradiction-caddy-0.0.0.0-100.64.0.1"
├── link1: (caddy) -[binds_to]-> (0.0.0.0)
├── link2: (caddy) -[binds_to]-> (100.64.0.1)
├── detected_at: 1784567140
├── resolved: false
├── resolved_at: NULL
└── resolution_notes: NULL
```

When `AddEdge()` detects an existing active link with the same `source_id` and `relationship` but a different `target_id`, it automatically creates a contradiction entry. The router surfaces unresolved contradictions in the grilling prompt for user clarification.

## License

MIT
