import sqlite3


def add_modality_column(db_path: str):
    conn = sqlite3.connect(db_path)
    cursor = conn.cursor()

    try:
        # Check existing columns in the semantic_links table
        cursor.execute("PRAGMA table_info(semantic_links);")
        columns = [row[1] for row in cursor.fetchall()]

        if "modality" not in columns:
            cursor.execute(
                "ALTER TABLE semantic_links ADD COLUMN modality TEXT;"
            )
            conn.commit()
            print("Column 'modality' added successfully.")
        else:
            print("Column 'modality' already exists.")

    except sqlite3.OperationalError as e:
        print(f"Database error: {e}")
    finally:
        conn.close()


if __name__ == "__main__":
    DATABASE_PATH = "gllam_data.db"
    add_modality_column(DATABASE_PATH)
