#!/bin/bash
# Shell script to grade an existing BEAM results file.

set -e

# Setup environment variables for compilation & execution
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

TEXT_SERVER="${TEXT_SERVER:-http://100.96.179.19:8888}"
# Resolve the default results input file using the latest run logs directory
DEFAULT_RESULTS="./bench/beam/beam_100k_results_selective.jsonl"
if [ -d "./bench/beam/run_logs" ]; then
    LATEST_RUN_DIR=$(find ./bench/beam/run_logs -mindepth 1 -maxdepth 1 -type d | sed -E 's/(.*\/)(run_log_)?([0-9]{8}_[0-9]{6})/\3 \1\2\3/' | sort -r | awk '{print $2}' | while read -r d; do
        if ls "$d"/*.jsonl >/dev/null 2>&1; then
            echo "$d"
            break
        fi
    done)
    if [ ! -z "$LATEST_RUN_DIR" ]; then
        JSONL_IN_DIR=$(find "$LATEST_RUN_DIR" -maxdepth 1 -name "beam_100k_results_selective.jsonl" -o -name "*.jsonl" | head -n 1)
        if [ -f "$JSONL_IN_DIR" ]; then
            DEFAULT_RESULTS="$JSONL_IN_DIR"
        else
            DEFAULT_RESULTS="${LATEST_RUN_DIR}/beam_100k_results_selective.jsonl"
        fi
    fi
fi
RESULTS_INPUT="${1:-$DEFAULT_RESULTS}"
RESULTS_FILE="$RESULTS_INPUT"

# Auto-resolve directories
if [ -d "$RESULTS_FILE" ]; then
    JSONL_FILE=$(find "$RESULTS_FILE" -maxdepth 1 -name "*.jsonl" | head -n 1)
    if [ -f "$JSONL_FILE" ]; then
        RESULTS_FILE="$JSONL_FILE"
    fi
fi

# Auto-resolve log files in the same directory
if [[ "$RESULTS_FILE" == *".log" ]]; then
    LOG_DIR=$(dirname "$RESULTS_FILE")
    JSONL_FILE=$(find "$LOG_DIR" -maxdepth 1 -name "*.jsonl" | head -n 1)
    if [ -f "$JSONL_FILE" ]; then
        echo "⚠️ Note: You passed a log file. Automatically switching to the JSONL results file in the same directory: $JSONL_FILE"
        RESULTS_FILE="$JSONL_FILE"
    fi
fi

if [ ! -f "$RESULTS_FILE" ]; then
    echo "Error: Results file '$RESULTS_FILE' not found (input was '$RESULTS_INPUT')."
    exit 1
fi

RESULTS_DIR=$(dirname "$RESULTS_FILE")
GRADE_OUT="${2:-${RESULTS_DIR}/beam_final_grade_selective.json}"

echo "======================================================="
echo "📊 Grading BEAM Results"
echo "   ├─ Results: $RESULTS_FILE"
echo "   ├─ Text Server: $TEXT_SERVER"
echo "   ├─ Output: $GRADE_OUT"
echo "======================================================="

# Run Grader
go run ./cmd/grade_beam/main.go \
  --results "$RESULTS_FILE" \
  --text-server "$TEXT_SERVER" \
  --output "$GRADE_OUT"

echo "======================================================="
echo "✅ Grading Complete!"
echo "   └─ Grade report saved to $GRADE_OUT"
echo "======================================================="
cat "$GRADE_OUT"
