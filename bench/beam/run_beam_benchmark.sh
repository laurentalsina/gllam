#!/bin/bash
# Shell script to run the BEAM benchmark pipeline
# Ingests, extracts, evaluates, and grades the BEAM 100k dataset sample.

set -e

# Setup environment variables for compilation & execution
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"
export GLLAM_PLANNER_EXECUTABLE_PATH="/home/laurent/Projects/downward/fast-downward.py"

# Model Context Window & Timeout Environment Variables
export STRONG_MODEL_CONTEXT="${STRONG_MODEL_CONTEXT:-131072}"
export FAST_MODEL_CONTEXT="${FAST_MODEL_CONTEXT:-65536}"
export STRONG_MODEL_TIMEOUT="${STRONG_MODEL_TIMEOUT:-300}"
export FAST_MODEL_TIMEOUT="${FAST_MODEL_TIMEOUT:-300}"
CONCURRENCY="${CONCURRENCY:-1}"

# Validate required server endpoints
if [ -z "$FAST_TEXT_SERVER" ] && [ -z "$STRONG_TEXT_SERVER" ]; then
    echo "❌ ERROR: Neither FAST_TEXT_SERVER nor STRONG_TEXT_SERVER is set!" >&2
    echo "Please source your environment configuration (e.g. source scripts/local_llm_examples/source_setup_gllam.sh)." >&2
    exit 1
fi

if [ -z "$EMBEDDINGS_SERVER" ]; then
    echo "❌ ERROR: EMBEDDINGS_SERVER environment variable is not set!" >&2
    echo "Please export EMBEDDINGS_SERVER before running this benchmark." >&2
    exit 1
fi
DB_PATH="./bench/gllam_data_beam_test.db"

BEAM_CONVERSATIONS="/home/laurent/Projects/agentic_benchmarks/beam_100k_conversations.jsonl"
BEAM_QA="/home/laurent/Projects/agentic_benchmarks/beam_100k_qa_sample50.jsonl"
OUT_RESULTS="./bench/beam/beam_100k_results_test.jsonl"
GRADE_OUT="./bench/beam/beam_final_grade_test.txt"

echo "======================================================="
echo "🚀 Starting BEAM 100k Benchmark Pipeline"
echo "   ├─ DB: $DB_PATH"
echo "   ├─ Fast Text Server: ${FAST_TEXT_SERVER:-<not set>}"
echo "   ├─ Strong Text Server: ${STRONG_TEXT_SERVER:-<not set>}"
echo "   ├─ Embeddings Server: $EMBEDDINGS_SERVER"
echo "   ├─ Planner: $GLLAM_PLANNER_EXECUTABLE_PATH"
echo "   ├─ Concurrency: $CONCURRENCY"
echo "   ├─ Fast Timeout: $FAST_MODEL_TIMEOUT"
echo "   └─ Strong Timeout: $STRONG_MODEL_TIMEOUT"
echo "======================================================="

# Step 1: Ingestion
echo -e "\n📥 Step 1: Ingesting BEAM Conversations..."
go run ./cmd/ingest_beam/main.go \
  --db "$DB_PATH" \
  --jsonl "$BEAM_CONVERSATIONS" \
  --embeddings-server "$EMBEDDINGS_SERVER"

# Step 2: Semantic Extraction
echo -e "\n🧩 Step 2: Extracting Graph Semantics..."
go run ./cmd/extract_semantics/main.go \
  --dbpath "$DB_PATH" \
  --prefix beam-100k- \
  --embeddings-server "$EMBEDDINGS_SERVER" \
  --concurrency "$CONCURRENCY" \
  --temporal \
  --clean

# Step 3: Evaluation
echo -e "\n🏎️  Step 3: Running Evaluation..."
go run ./cmd/eval_beam/main.go \
  --db "$DB_PATH" \
  --qa "$BEAM_QA" \
  --out "$OUT_RESULTS" \
  --embeddings-server "$EMBEDDINGS_SERVER"

# Step 4: Grading
echo -e "\n📊 Step 4: Grading Results..."
go run ./cmd/grade_beam/main.go \
  --results "$OUT_RESULTS" > "$GRADE_OUT" 2>&1

echo "======================================================="
echo "✅ Pipeline Complete!"
echo "   └─ Grade report saved to $GRADE_OUT"
echo "======================================================="
cat "$GRADE_OUT"
