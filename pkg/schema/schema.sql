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
    task_type TEXT NOT NULL UNIQUE,       -- e.g., "deploy_caddy_reverse_proxy"
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
    type TEXT NOT NULL
);

-- 4. SEMANTIC LINKS (Caveat-qualified, temporally bounded relationships)
CREATE TABLE IF NOT EXISTS semantic_links (
    source_id TEXT NOT NULL,
    target_id TEXT NOT NULL,
    relationship TEXT NOT NULL,
    caveats TEXT NOT NULL,                -- Conditions, constraints, or exceptions
    valid_from INTEGER NOT NULL,          -- Unix timestamp
    valid_until INTEGER,                  -- Unix timestamp (NULL = currently active)
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (source_id, target_id, relationship),
    FOREIGN KEY (source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (target_id) REFERENCES semantic_nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_links_lookup ON semantic_links(source_id, target_id);

-- 5. CONTRADICTIONS (Explicit relationships between conflicting SemanticLinks)
CREATE TABLE IF NOT EXISTS contradictions (
    id TEXT PRIMARY KEY,
    link1_source_id TEXT NOT NULL,
    link1_target_id TEXT NOT NULL,
    link1_relationship TEXT NOT NULL,
    link2_source_id TEXT NOT NULL,
    link2_target_id TEXT NOT NULL,
    link2_relationship TEXT NOT NULL,
    detected_at INTEGER NOT NULL,         -- Unix timestamp
    resolved BOOLEAN DEFAULT 0,           -- 1 if user resolved the conflict
    resolved_at INTEGER,                  -- Unix timestamp when resolved
    resolution_notes TEXT,                -- User's clarification
    FOREIGN KEY (link1_source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (link1_target_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (link2_source_id) REFERENCES semantic_nodes(id),
    FOREIGN KEY (link2_target_id) REFERENCES semantic_nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_contradictions_unresolved ON contradictions(resolved, detected_at DESC);
