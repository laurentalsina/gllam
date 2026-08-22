#!/bin/bash
# Shell script to run the BEAM benchmark using the dynamic, selective JIT extraction runner.
clear
set -e

# Setup environment variables for compilation & execution
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"
export GLLAM_PLANNER_EXECUTABLE_PATH="/home/laurent/Projects/downward/fast-downward.py"

# Configurable endpoints and files
TEXT_SERVER="${TEXT_SERVER:-http://100.96.179.19:8888}"
EMBEDDINGS_SERVER="${EMBEDDINGS_SERVER:-http://127.0.0.1:8800}"
DB_PATH="./bench/gllam_data_selective_beam.db"

BEAM_CORPUS="/home/laurent/Projects/agentic_benchmarks/beam_100k_conversations.jsonl"
BEAM_QA="/home/laurent/Projects/agentic_benchmarks/beam_100k_qa_sample50.jsonl"
OUT_RESULTS="./bench/beam/beam_100k_results_selective.jsonl"
GRADE_OUT="./bench/beam/beam_final_grade_selective.txt"

# Parse command line arguments
CATEGORIES=""
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --cover)
            if [ -z "$CATEGORIES" ]; then
                CATEGORIES="$2"
            else
                CATEGORIES="$CATEGORIES,$2"
            fi
            shift 2
            ;;
        *)
            echo "Unknown parameter: $1"
            echo "Usage: $0 [--cover <category_name>] [--cover <category_name2>] ..."
            echo "Available categories: all, preference_following, temporal_reasoning, event_ordering, knowledge_update, summarization, instruction_following, information_extraction, contradiction_resolution, multi_session_reasoning, abstention"
            exit 1
            ;;
    esac
done

echo "======================================================="
echo "🚀 Starting BEAM 100k Dynamic Selective Benchmark"
echo "   ├─ DB: $DB_PATH"
echo "   ├─ Text Server: $TEXT_SERVER"
echo "   ├─ Embeddings Server: $EMBEDDINGS_SERVER"
echo "   ├─ Planner: $GLLAM_PLANNER_EXECUTABLE_PATH"
if [ ! -z "$CATEGORIES" ]; then
echo "   ├─ Target Categories: $CATEGORIES"
fi
echo "======================================================="

# Run Selective Evaluation
go run ./cmd/eval_selective/main.go \
  --dbpath "$DB_PATH" \
  --corpus "$BEAM_CORPUS" \
  --qa "$BEAM_QA" \
  --out "$OUT_RESULTS" \
  --text-server "$TEXT_SERVER" \
  --embeddings-server "$EMBEDDINGS_SERVER" \
  --top-k 10 \
  --use-utterances-vectors=true \
  --use-terms-vectors=true \
  --bypass-semantic=true \
  --prompts-config config/beam_prompts.json \
  --categories "$CATEGORIES"

# Grade Results
echo -e "\n📊 Grading Results..."
go run ./cmd/grade_beam/main.go \
  --results "$OUT_RESULTS" \
  --text-server "$TEXT_SERVER" > "$GRADE_OUT" 2>&1

echo "======================================================="
echo "✅ Pipeline Complete!"
echo "   └─ Grade report saved to $GRADE_OUT"
echo "======================================================="
cat "$GRADE_OUT"
