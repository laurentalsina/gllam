package engine

import (
    "database/sql"
    "fmt"
    "os"

    sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
    _ "github.com/mattn/go-sqlite3"
)

type GllamEngine struct {
    db       *sql.DB   // Write handle: single connection, serializes all mutations
    dbRO     *sql.DB   // Read handle: connection pool for concurrent read-only queries
    embedder Embedder  // Pluggable embedding generator (e.g., llama.cpp)
}

// NewGllamEngine opens two SQLite handles with sqlite-vec support:
//   - db:   a single-connection handle for all write mutations (prevents SQLite write serialization)
//   - dbRO: a read-only handle (mode=ro) with a larger pool for concurrent reads
//
// sqlite-vec is registered globally via Auto() for vector operations.
// This achieves true read concurrency: WAL-mode readers don't block each other,
// and read-only connections can never contend with the writer.
func NewGllamEngine(dbPath string, embedder Embedder) (*GllamEngine, error) {
    // Register sqlite-vec virtual tables and SIMD distance functions globally
    sqlite_vec.Auto()

    // --- Write handle: exactly one connection ---
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open sqlite database: %w", err)
    }
    db.SetMaxOpenConns(1) // Single-writer model prevents table-lock contention

    // --- Read handle: read-only, pooled connections ---
    dbRO, err := sql.Open("sqlite3", dbPath+"?mode=ro")
    if err != nil {
        db.Close()
        return nil, fmt.Errorf("failed to open read-only sqlite database: %w", err)
    }
    dbRO.SetMaxOpenConns(8) // Multiple concurrent readers

    // Apply pragmas to the write handle (journal_mode & synchronous are DB-level, persist for readers)
    writePragmas := `
        PRAGMA journal_mode = WAL;
        PRAGMA synchronous = NORMAL;
        PRAGMA foreign_keys = ON;
        PRAGMA busy_timeout = 5000;
    `
    if _, err := db.Exec(writePragmas); err != nil {
        db.Close()
        dbRO.Close()
        return nil, fmt.Errorf("failed to apply sqlite pragmas: %w", err)
    }

    // Connection-level pragmas that readers also need
    _, err = dbRO.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;")
    if err != nil {
        db.Close()
        dbRO.Close()
        return nil, fmt.Errorf("failed to apply read-only sqlite pragmas: %w", err)
    }

    return &GllamEngine{db: db, dbRO: dbRO, embedder: embedder}, nil
}

func (e *GllamEngine) Close() error {
    var errs []error
    if err := e.db.Close(); err != nil {
        errs = append(errs, err)
    }
    if err := e.dbRO.Close(); err != nil {
        errs = append(errs, err)
    }
    if len(errs) > 0 {
        return fmt.Errorf("errors closing databases: %v", errs)
    }
    return nil
}

// DB returns the write handle for direct SQL access (e.g., vector operations)
func (e *GllamEngine) DB() *sql.DB {
    return e.db
}

// DBRO returns the read-only handle for concurrent read queries
func (e *GllamEngine) DBRO() *sql.DB {
    return e.dbRO
}

// InitSchema reads and executes schema.sql to create all tables and indexes
func (e *GllamEngine) InitSchema() error {
    schemaPaths := []string{
        "pkg/schema/schema.sql",
        "../pkg/schema/schema.sql",
        "../../pkg/schema/schema.sql",
    }

    var schemaBytes []byte
    var lastErr error
    for _, path := range schemaPaths {
        schemaBytes, lastErr = os.ReadFile(path)
        if lastErr == nil {
            break
        }
    }
    if lastErr != nil {
        return fmt.Errorf("failed to read schema.sql from any known path: %w", lastErr)
    }

    if _, err := e.db.Exec(string(schemaBytes)); err != nil {
        return fmt.Errorf("failed to execute schema.sql: %w", err)
    }

    return nil
}
