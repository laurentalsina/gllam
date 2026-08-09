-- PRAGMA configuration enforced at connection time:
-- PRAGMA journal_mode = WAL;
-- PRAGMA foreign_keys = ON;

-- 1. EPISODIC SUMMARIES (Temporal History & Session Summaries)
CREATE TABLE IF NOT EXISTS episodic_summaries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    summary_text TEXT NOT NULL,
    created_at INTEGER NOT NULL -- Unix timestamp
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
    updated_at INTEGER NOT NULL
);

-- 3. SEMANTIC NODES
CREATE TABLE IF NOT EXISTS semantic_nodes (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL,
    context_prompt TEXT
);

-- 4. SEMANTIC LINKS (Caveat-qualified, temporally bounded relationships with grounded uncertainty support)
CREATE TABLE IF NOT EXISTS semantic_links (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    caveats TEXT NOT NULL,                -- Conditions, constraints, or exceptions
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
    updated_at INTEGER NOT NULL,



    PRIMARY KEY (source_id, target_id, relationship),
    FOREIGN KEY (source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (target_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (temporal_anchor_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (origin_source_id) REFERENCES semantic_nodes(id)
);




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
