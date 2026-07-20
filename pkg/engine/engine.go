package engine

import (
    "database/sql"
    "fmt"
    "os"

    _ "github.com/mattn/go-sqlite3"
)

type GllamEngine struct {
    db *sql.DB
}

func NewGllamEngine(dbPath string) (*GllamEngine, error) {
    db, err := sql.Open("sqlite3", dbPath)
    if err != nil {
        return nil, fmt.Errorf("failed to open sqlite database: %w", err)
    }

    // Optimize SQLite execution parameters for local agent concurrency
    db.SetMaxOpenConns(1) // Single writer model prevents table lock conflicts
    
    pragmas := `
        PRAGMA journal_mode = WAL;
        PRAGMA synchronous = NORMAL;
        PRAGMA foreign_keys = ON;
        PRAGMA busy_timeout = 5000;
    `
    if _, err := db.Exec(pragmas); err != nil {
        return nil, fmt.Errorf("failed to apply sqlite pragmas: %w", err)
    }

    return &GllamEngine{db: db}, nil
}

func (e *GllamEngine) Close() error {
    return e.db.Close()
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
