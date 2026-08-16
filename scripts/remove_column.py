import sqlite3


def remove_column(db_path: str):
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()

    try:
        # Check existing columns in the semantic_links table
        cursor.execute("PRAGMA table_info(semantic_links);")
        columns = [row[1] for row in cursor.fetchall()]
        print("got these columns")
        print(columns) 

        # temporal validity of links should be stored in its own table
        # most likely we need to post-train model for performing specific correct extractions fast 
        # removed already:
        #    valid_from, valid_until 

        columns_to_drop = [
        "temporal_anchor_id",
        "temporal_relation",
        "temporal_offset_seconds",
        "temporal_granularity",
        "temporal_note",
        "duration_turns",
        "remaining_turns",
         ]

        for col in columns_to_drop:
            cursor.execute(f"ALTER TABLE semantic_links DROP COLUMN {col};")

        conn.commit()

        cursor.execute(
          """ALTER TABLE semantic_links DROP COLUMN valid_from;
             ALTER TABLE semantic_links DROP COLUMN temporal_anchor_id;
             ALTER TABLE semantic_links DROP COLUMN temporal_relation;
             ALTER TABLE semantic_links DROP COLUMN temporal_offset_seconds;
             ALTER TABLE semantic_links DROP COLUMN temporal_granularity;
             ALTER TABLE semantic_links DROP COLUMN temporal_note;
             ALTER TABLE semantic_links DROP COLUMN duration_turns;
             ALTER TABLE semantic_links DROP COLUMN remaining_turns;"""
        )
        conn.commit()
        print("Removed successfully.")

    except sqlite3.OperationalError as e:
        print(f"Database error: {e}")
    finally:
        conn.close()


if __name__ == "__main__":
    DATABASE_PATH = "bench/gllam_data.db"
    remove_column(DATABASE_PATH)
