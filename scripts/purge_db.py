#!/usr/bin/env python3
"""
purge_db.py - Purge a gllam SQLite database back to a clean state.

Keeps all tables whose names start with "episodic_" (raw imported text chunks).
Clears everything else with DELETE FROM — no DROP, so the vec0 extension is
not required. The vec0 shadow tables (_chunks, _rowids, _info, _vector_chunks00)
hold the actual vector data and are plain tables that can be cleared directly.

Usage:
    python3 scripts/purge_db.py [path/to/gllam_data.db]

Default DB path: bench/gllam_data_ingestion_20260814.db
"""

import sqlite3
import sys
import os

DEFAULT_DB = os.path.join(
    os.path.dirname(__file__), "..", "bench", "gllam_data_ingestion_20260814.db"
)

db_path = sys.argv[1] if len(sys.argv) > 1 else DEFAULT_DB
db_path = os.path.abspath(db_path)

if not os.path.exists(db_path):
    print(f"ERROR: database not found: {db_path}")
    sys.exit(1)

print(f"Connecting to: {db_path}")

con = sqlite3.connect(db_path)
cur = con.cursor()

# All user-visible tables, including vec0 shadow tables (type='shadow' in
# SQLite ≥3.37) and regular tables. Excludes sqlite_* internals.
cur.execute("""
    SELECT name FROM sqlite_master
    WHERE type IN ('table', 'shadow')
      AND name NOT LIKE 'sqlite_%'
    ORDER BY name
""")
all_tables = [row[0] for row in cur.fetchall()]

# The vec0 virtual table rows themselves (e.g. "semantic_embeddings") have
# no actual rows — data lives in their shadow tables. Attempting DELETE FROM
# on them without the extension loaded raises an error, so we skip them.
cur.execute("""
    SELECT name FROM sqlite_master
    WHERE type = 'table'
      AND sql LIKE '%USING vec0%'
""")
virtual_names = {row[0] for row in cur.fetchall()}

print()
cur.execute("PRAGMA foreign_keys = OFF")

for name in all_tables:
    if name.startswith("episodic_"):
        print(f"  KEEP   {name}")
        continue

    if name in virtual_names:
        # Skip the virtual table entry itself — no rows, needs the extension
        print(f"  SKIP   {name}  (vec0 virtual, data is in shadow tables)")
        continue

    print(f"  PURGE  {name}")
    cur.execute(f'DELETE FROM "{name}"')

cur.execute("PRAGMA foreign_keys = ON")

con.commit()

print()
print("Running VACUUM...")
con.execute("VACUUM")
con.close()

print()
print("Done. Re-run gllam InitSchema() to recreate indexes and any missing schema.")

