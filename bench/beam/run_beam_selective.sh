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
RUN_TIMESTAMP=$(date +"%Y%m%d_%H%M%S")
RUN_LOG_DIR="./bench/beam/run_log_${RUN_TIMESTAMP}"
mkdir -p "$RUN_LOG_DIR"
OUT_RESULTS="${RUN_LOG_DIR}/beam_100k_results_selective.jsonl"
GRADE_OUT="${RUN_LOG_DIR}/beam_final_grade_selective.txt"

# Parse command line arguments
CATEGORIES=""
DEBUG_FLAG="false"
BYPASS_SEMANTIC="false"
BYPASS_TEMPORAL="false"
PRUNE_CLUE_CHUNKS="true"
DECOMPOSE_QUERY="true"

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
        --debug)
            DEBUG_FLAG="true"
            shift 1
            ;;
        --bypass-semantic)
            BYPASS_SEMANTIC="$2"
            shift 2
            ;;
        --bypass-semantic=*)
            BYPASS_SEMANTIC="${1#*=}"
            shift 1
            ;;
        --bypass-temporal)
            BYPASS_TEMPORAL="$2"
            shift 2
            ;;
        --bypass-temporal=*)
            BYPASS_TEMPORAL="${1#*=}"
            shift 1
            ;;
        --prune-clue-chunks)
            PRUNE_CLUE_CHUNKS="$2"
            shift 2
            ;;
        --prune-clue-chunks=*)
            PRUNE_CLUE_CHUNKS="${1#*=}"
            shift 1
            ;;
        --decompose-query)
            DECOMPOSE_QUERY="$2"
            shift 2
            ;;
        --decompose-query=*)
            DECOMPOSE_QUERY="${1#*=}"
            shift 1
            ;;
        *)
            echo "Unknown parameter: $1"
            echo "Usage: $0 [--cover <category_name>] [--debug] [--bypass-semantic <true|false>] [--bypass-temporal <true|false>] [--prune-clue-chunks <true|false>] [--decompose-query <true|false>]"
            echo "Available categories: all, preference_following, temporal_reasoning, event_ordering, knowledge_update, summarization, instruction_following, information_extraction, contradiction_resolution, multi_session_reasoning, abstention"
            exit 1
            ;;
    esac
done

# Auto-disable bypass-semantic if temporal categories are targeted
if [[ "$CATEGORIES" == *"temporal_reasoning"* || "$CATEGORIES" == *"event_ordering"* ]]; then
    if [ "$BYPASS_SEMANTIC" = "true" ]; then
        BYPASS_SEMANTIC="false"
    fi
fi

echo "======================================================="
echo "🚀 Starting BEAM 100k Dynamic Selective Benchmark"
echo "   ├─ DB: $DB_PATH"
echo "   ├─ Text Server: $TEXT_SERVER"
echo "   ├─ Embeddings Server: $EMBEDDINGS_SERVER"
echo "   ├─ Planner: $GLLAM_PLANNER_EXECUTABLE_PATH"
echo "   ├─ Debug Mode: $DEBUG_FLAG"
echo "   ├─ Bypass Semantic: $BYPASS_SEMANTIC"
echo "   ├─ Bypass Temporal: $BYPASS_TEMPORAL"
echo "   ├─ Prune Clue Chunks: $PRUNE_CLUE_CHUNKS"
echo "   ├─ Decompose Query: $DECOMPOSE_QUERY"
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
  --prompts-config config/beam_prompts.json \
  --categories "$CATEGORIES" \
  --debug="$DEBUG_FLAG" \
  --bypass-semantic="$BYPASS_SEMANTIC" \
  --bypass-temporal="$BYPASS_TEMPORAL" \
  --prune-clue-chunks="$PRUNE_CLUE_CHUNKS" \
  --decompose-query="$DECOMPOSE_QUERY" \
  --run-timestamp "$RUN_TIMESTAMP"

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
