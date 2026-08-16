-- PRAGMA configuration enforced at connection time:
-- PRAGMA journal_mode = WAL;
-- PRAGMA foreign_keys = ON;

-- 1. EPISODIC SUMMARIES (Temporal History & Session Summaries)
CREATE TABLE IF NOT EXISTS episodic_summaries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    summary_text TEXT NOT NULL,
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
    updated_at TEXT NOT NULL    -- RFC3339 timestamp
);

-- 3. SEMANTIC NODES (Grounded entities & taxonomy categories)
CREATE TABLE IF NOT EXISTS semantic_nodes (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL,
    context_prompt TEXT,
    trust_weight INTEGER DEFAULT 100,
    taxonomy_path TEXT DEFAULT '/',        -- Materialized path (e.g. /Engineering/Infrastructure/Databases/Relational/Postgres)
    is_category INTEGER DEFAULT 0,          -- Boolean flag indicating if node is a taxonomy category
    caveat_summary TEXT                     -- Compacted historical edge caveat summary string
);


CREATE INDEX IF NOT EXISTS idx_semantic_nodes_taxonomy_path ON semantic_nodes(taxonomy_path);
CREATE INDEX IF NOT EXISTS idx_semantic_nodes_is_category ON semantic_nodes(is_category);



-- 4. SEMANTIC LINKS (Caveat-qualified, temporally bounded relationships with grounded uncertainty support)
CREATE TABLE IF NOT EXISTS semantic_links (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    caveats TEXT NOT NULL,                -- Conditions, constraints, or exceptions
    modality TEXT NOT NULL,               -- Epistemic: default, Alethic: physically/logically necessary, Deontic: obligatory/permitted/prohibited etc...
    valid_from TEXT NOT NULL,             -- Unix timestamp string OR 'temporal_note'
    valid_until TEXT,                     -- Unix timestamp string OR 'temporal_note' (NULL = currently active)
    temporal_anchor_id TEXT,              -- Grounded node ID reference for relative timing (e.g. 'event-db-migration')
    temporal_relation TEXT,               -- Allen Interval Algebra: 'before', 'after', 'equals', 'overlaps', 'during', 'contains', 'starts', 'finishes', 'meets'
    temporal_offset_seconds INTEGER DEFAULT 0, -- Relative offset in seconds (+86400 = +1 day after anchor, -172800 = -2 days before anchor)
    temporal_granularity TEXT DEFAULT 'exact', -- Granularity leniency: 'day' (snap 00:00), 'hour' (snap XX:00), 'exact' (no snap), 'month'
    temporal_note TEXT,                   -- Qualitative phrase describing imprecise timestamp
    origin_source_id TEXT,                -- Origin source node ID (human, agent, system)
    rule_context TEXT DEFAULT 'global',   -- 'user_preference' | 'session' | 'source' | 'global'
    constraint_type TEXT DEFAULT 'positive',-- 'positive' | 'negative'
    rule_rationale TEXT,                  -- Justification / 'because' clause (e.g. 'Security Compliance', 'Accessibility')
    resolution_rationale TEXT,            -- Explanation when resolving a contradiction
    duration_turns INTEGER DEFAULT -1,    -- -1 for infinite, N for N-turn bound constraints
    remaining_turns INTEGER DEFAULT -1,   -- Remaining turns before automatic expiration
    updated_at TEXT NOT NULL,   -- RFC3339 timestamp



    PRIMARY KEY (source_id, target_id, relationship),
    FOREIGN KEY (source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (target_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (temporal_anchor_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (origin_source_id) REFERENCES semantic_nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_semantic_links_target ON semantic_links(target_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_origin ON semantic_links(origin_source_id);
CREATE INDEX IF NOT EXISTS idx_semantic_links_source_rel ON semantic_links(source_id, relationship);




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

-- 9. SYSTEM METADATA (Engine configuration, vector embedding model versions)
CREATE TABLE IF NOT EXISTS system_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

