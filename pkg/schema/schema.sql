-- PRAGMA configuration enforced at connection time:
-- PRAGMA journal_mode = WAL;
-- PRAGMA foreign_keys = ON;

-- 1. EPISODIC SUMMARIES (Temporal History & Session Summaries)
CREATE TABLE IF NOT EXISTS episodic_summaries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    summary_text TEXT NOT NULL,
    source_uri TEXT,
    created_at TEXT NOT NULL    -- RFC3339 timestamp
);

CREATE INDEX IF NOT EXISTS idx_episodic_created ON episodic_summaries(created_at DESC);

-- 2. PROCEDURAL KNOWLEDGE (Validated, Reusable Step-by-Step Recipes)
CREATE TABLE IF NOT EXISTS procedural_knowledge (
    id TEXT PRIMARY KEY,
    task_type TEXT NOT NULL UNIQUE,       -- e.g., "deploy_caddy_reverse_proxy" or "handle_contradiction"
    scope TEXT NOT NULL DEFAULT 'external', -- 'external', 'internal_semantic', 'internal_episodic'
    trigger_context TEXT,                 -- specific trigger string, e.g. "contradiction", "bug_report"
    instructions TEXT NOT NULL,           -- Markdown/Text step-by-step method
    user_feedback_rules TEXT,             -- Specific constraints/preferences
    times_applied INTEGER DEFAULT 0,
    is_highly_helpful BOOLEAN DEFAULT 0,  -- Explicitly flagged as golden standard
    version INTEGER DEFAULT 1,
    superseded_by TEXT,                   -- Self-reference to newer procedural ID
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL    -- RFC3339 timestamp
);

-- 2b. PROCEDURAL GRAPH (State-Machine Workflows, Subprocedures & Traces)
CREATE TABLE IF NOT EXISTS procedural_nodes (
    id                  TEXT PRIMARY KEY,            -- e.g. "proc_auth_refresh", UUID, or semantic slug
    name                TEXT NOT NULL,
    description         TEXT NOT NULL,
    action_type         TEXT NOT NULL,               -- 'composite', 'tool_call', 'llm_reasoning', 'terminal'
    input_schema        TEXT,                        -- JSON Schema defining expected parameters
    output_schema       TEXT,                        -- JSON Schema defining return payload
    is_idempotent       INTEGER NOT NULL DEFAULT 0,  -- 0 = False, 1 = True (safety/retry policy)
    metadata            TEXT DEFAULT '{}',           -- Arbitrary JSON for engine-specific flags
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    updated_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

CREATE TABLE IF NOT EXISTS procedural_links (
    id                  TEXT PRIMARY KEY,
    source_procedure_id TEXT NOT NULL,
    target_procedure_id TEXT NOT NULL,
    relation_type       TEXT NOT NULL,               -- 'next', 'subprocedure', 'conditional_branch', 'on_failure', 'compensates'
    condition_expr      TEXT,                        -- Evaluation logic against runtime context
    weight              REAL NOT NULL DEFAULT 1.0,   -- Priority/likelihood weight if probabilistic
    ordering            INTEGER NOT NULL DEFAULT 0,  -- Sort order when multiple child/next edges exist
    metadata            TEXT DEFAULT '{}',           -- JSON for parameter mapping (source.output -> target.input)
    created_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),

    FOREIGN KEY (source_procedure_id) REFERENCES procedural_nodes(id) ON DELETE CASCADE,
    FOREIGN KEY (target_procedure_id) REFERENCES procedural_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_proc_links_source ON procedural_links(source_procedure_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_proc_links_target ON procedural_links(target_procedure_id, relation_type);

CREATE TABLE IF NOT EXISTS procedural_execution_traces (
    id                  TEXT PRIMARY KEY,
    root_procedure_id   TEXT NOT NULL,
    current_node_id     TEXT,
    status              TEXT NOT NULL,               -- 'pending', 'in_progress', 'completed', 'failed', 'paused'
    context_state       TEXT NOT NULL DEFAULT '{}',  -- JSON object storing runtime state and outputs
    error_details       TEXT,
    started_at          INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    finished_at         INTEGER,

    FOREIGN KEY (root_procedure_id) REFERENCES procedural_nodes(id),
    FOREIGN KEY (current_node_id) REFERENCES procedural_nodes(id)
);

CREATE TABLE IF NOT EXISTS procedural_step_runs (
    id                  TEXT PRIMARY KEY,
    trace_id            TEXT NOT NULL,
    node_id             TEXT NOT NULL,
    step_number         INTEGER NOT NULL,
    input_payload       TEXT DEFAULT '{}',
    output_payload      TEXT DEFAULT '{}',
    status              TEXT NOT NULL,               -- 'success', 'failed', 'skipped', 'compensated'
    error_message       TEXT,
    duration_ms         INTEGER DEFAULT 0,
    executed_at         INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),

    FOREIGN KEY (trace_id) REFERENCES procedural_execution_traces(id) ON DELETE CASCADE,
    FOREIGN KEY (node_id) REFERENCES procedural_nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_proc_step_runs_trace ON procedural_step_runs(trace_id, step_number);
CREATE INDEX IF NOT EXISTS idx_proc_step_runs_node ON procedural_step_runs(trace_id, node_id, status);

-- 3. SEMANTIC NODES (Grounded entities & taxonomy categories)
CREATE TABLE IF NOT EXISTS semantic_nodes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL, 
    context_silo_id TEXT NOT NULL DEFAULT '', -- Partitioning ID (e.g. corpus conversation or document silo)
    context_prompt TEXT,
    trust_weight INTEGER DEFAULT 100,
    taxonomy_path TEXT DEFAULT '/',        -- Materialized path (e.g. /Engineering/Infrastructure/Databases/Relational/Postgres)
    is_category INTEGER DEFAULT 0,         -- Boolean flag indicating if node is a taxonomy category
    caveat_summary TEXT,                   -- Compacted historical node caveat summary string
    created_from TEXT,                     -- Reference to raw data that led to the creation of the node
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_semantic_nodes_id ON semantic_nodes(id);
CREATE INDEX IF NOT EXISTS idx_semantic_nodes_silo ON semantic_nodes(context_silo_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_semantic_nodes_silo_name ON semantic_nodes(context_silo_id, name);
CREATE INDEX IF NOT EXISTS idx_semantic_nodes_taxonomy_path ON semantic_nodes(taxonomy_path);
CREATE INDEX IF NOT EXISTS idx_semantic_nodes_is_category ON semantic_nodes(is_category);

-- 4a. SEMANTIC TEMPORAL ATTRIBUTES
CREATE TABLE IF NOT EXISTS semantic_temporal_links (
    id TEXT PRIMARY KEY,
    context_silo_id TEXT NOT NULL DEFAULT '', -- Partitioning ID (e.g. corpus conversation or document silo)
    valid_from TEXT,                -- Unix timestamp string OR qualitative date/time
    valid_until TEXT,               -- Unix timestamp string OR qualitative date/time
    temporal_anchor_id TEXT,        -- Grounded node ID reference for relative timing
    temporal_relation TEXT,         -- "before" | "after" | "during" | "co_occurs" | "causes"
    temporal_note TEXT,             -- Qualitative phrase describing imprecise timestamp / duration
    FOREIGN KEY (temporal_anchor_id) REFERENCES semantic_nodes(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_semantic_temporal_links_silo ON semantic_temporal_links(context_silo_id);

-- 4b. SEMANTIC LINKS (Caveat-qualified relationships with grounded uncertainty support)
CREATE TABLE IF NOT EXISTS semantic_links (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    context_silo_id TEXT NOT NULL DEFAULT '', -- Partitioning ID (e.g. corpus conversation or document silo)
    caveats TEXT NOT NULL,                -- Conditions, constraints, or exceptions
    modality TEXT NOT NULL,               -- Epistemic: default, Alethic: physically/logically necessary, Deontic: obligatory/permitted/prohibited etc...
    origin_id TEXT,                       -- node ID (human, agent, system) that provided information about the link
    resolution_rationale TEXT,            -- Explanation when resolving a contradiction
    created_from TEXT,                    -- Reference to raw data that led to the creation of the link
    created_at TEXT NOT NULL,             -- RFC3339 timestamp
    updated_at TEXT NOT NULL,             -- RFC3339 timestamp
    temporal_link_id TEXT,
    PRIMARY KEY (source_id, target_id, relationship),
    FOREIGN KEY (source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (target_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (origin_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (temporal_link_id) REFERENCES semantic_temporal_links(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_semantic_links_silo ON semantic_links(context_silo_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_target ON semantic_links(target_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_source ON semantic_links(source_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_origin ON semantic_links(origin_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_source_rel ON semantic_links(source_id, relationship);
CREATE INDEX IF NOT EXISTS idx_semantic_links_modality ON semantic_links(modality);

-- 5. DOCUMENT LINEAGE (Strict information source URI traceability)
CREATE TABLE IF NOT EXISTS document_lineage (
    id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    source_uri TEXT NOT NULL,
    document_title TEXT,
    source_type TEXT NOT NULL,
    line_number INTEGER DEFAULT 0,
    char_offset INTEGER DEFAULT 0,
    checksum TEXT,
    created_at TEXT NOT NULL,   -- RFC3339 timestamp
    FOREIGN KEY (node_id) REFERENCES semantic_nodes(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_lineage_node ON document_lineage(node_id);
CREATE INDEX IF NOT EXISTS idx_lineage_uri ON document_lineage(source_uri);

-- 5b. DOCUMENT VERSIONS (Multi-author edit history and version granularity)
CREATE TABLE IF NOT EXISTS document_versions (
    id TEXT PRIMARY KEY,
    lineage_id TEXT NOT NULL,
    version_number INTEGER NOT NULL,
    author_id TEXT NOT NULL,
    author_name TEXT,
    change_summary TEXT,
    start_line INTEGER DEFAULT 0,
    end_line INTEGER DEFAULT 0,
    char_offset INTEGER DEFAULT 0,
    created_at TEXT NOT NULL,   -- RFC3339 timestamp
    FOREIGN KEY (lineage_id) REFERENCES document_lineage(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_doc_version_lineage ON document_versions(lineage_id);
CREATE INDEX IF NOT EXISTS idx_doc_version_author ON document_versions(author_id);

CREATE INDEX IF NOT EXISTS idx_links_lookup ON semantic_links(source_id, target_id);





-- 6. SEMANTIC EMBEDDINGS (sqlite-vec virtual table for SIMD-optimized vector search)
CREATE VIRTUAL TABLE IF NOT EXISTS semantic_embeddings USING vec0(
    +node_id TEXT,
    embedding float[1024]
);

-- 7. PROCEDURAL EMBEDDINGS (sqlite-vec virtual table for intent routing)
CREATE VIRTUAL TABLE IF NOT EXISTS procedural_embeddings USING vec0(
    +id TEXT,
    embedding float[1024]
);

-- 8. EPISODIC EMBEDDINGS (sqlite-vec virtual table for semantic retrieval of memory sessions)
CREATE VIRTUAL TABLE IF NOT EXISTS episodic_embeddings USING vec0(
    +session_id TEXT,
    embedding float[1024]
);

-- 8b. UTTERANCE EMBEDDINGS (sqlite-vec virtual table for turn-level semantic search)
CREATE VIRTUAL TABLE IF NOT EXISTS utterance_embeddings USING vec0(
    +utterance_id TEXT,
    embedding float[1024]
);

-- 8c. TERM EMBEDDINGS (sqlite-vec virtual table for vocabulary term-level semantic search)
CREATE VIRTUAL TABLE IF NOT EXISTS term_embeddings USING vec0(
    +term TEXT,
    embedding float[1024]
);

-- 9. SYSTEM METADATA (Engine configuration, vector embedding model versions)
CREATE TABLE IF NOT EXISTS system_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

