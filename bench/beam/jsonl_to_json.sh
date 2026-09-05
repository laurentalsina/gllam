#!/usr/bin/env bash
# Convert a .jsonl file (one JSON object per line) into a valid JSON array [{},{},...]

set -euo pipefail

if [ "$#" -lt 1 ]; then
    echo "Usage: $0 <input.jsonl> [output.json]"
    echo "Example: $0 ./bench/beam/run_logs/20260905_022703/beam_100k_results_selective.jsonl"
    exit 1
fi

INPUT_FILE="$1"

if [ ! -f "$INPUT_FILE" ]; then
    echo "Error: Input file '$INPUT_FILE' not found." >&2
    exit 1
fi

# Determine output file path
if [ "$#" -ge 2 ]; then
    OUTPUT_FILE="$2"
else
    if [[ "$INPUT_FILE" == *.jsonl ]]; then
        OUTPUT_FILE="${INPUT_FILE%.jsonl}.json"
    else
        OUTPUT_FILE="${INPUT_FILE}.json"
    fi
fi

echo "Converting '$INPUT_FILE' -> '$OUTPUT_FILE'..."

# Try jq first if available for formatted, validated JSON
if command -v jq >/dev/null 2>&1; then
    jq -s '.' "$INPUT_FILE" > "$OUTPUT_FILE"
# Fallback to python3 if available
elif command -v python3 >/dev/null 2>&1; then
    python3 -c '
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    items = [json.loads(line) for line in f if line.strip()]
with open(sys.argv[2], "w", encoding="utf-8") as f:
    json.dump(items, f, indent=2)
' "$INPUT_FILE" "$OUTPUT_FILE"
# Pure shell fallback: wrap lines in [] and comma-separate objects
else
    (echo "[" && sed -e '/^[[:space:]]*$/d' -e '$!s/$/,/' "$INPUT_FILE" && echo "]") > "$OUTPUT_FILE"
fi

echo "Done! Generated valid JSON array in '$OUTPUT_FILE'."
