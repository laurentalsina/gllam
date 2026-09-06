#!/bin/bash
# Utility script to extract semantic nodes & links for MemArena d7_qa benchmark
# Usage: ./bench/run_d7_qa_extract_semantics.sh [optional_text_server_endpoint]

export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

if [ -z "$FAST_TEXT_SERVER" ] && [ -z "$STRONG_TEXT_SERVER" ]; then
    echo "❌ ERROR: Neither FAST_TEXT_SERVER nor STRONG_TEXT_SERVER is set in the environment!" >&2
    echo "Please source your environment configuration (e.g. source scripts/local_llm_examples/source_setup_gllam.sh)." >&2
    exit 1
fi

if [ -z "$EMBEDDINGS_SERVER" ]; then
    echo "❌ ERROR: EMBEDDINGS_SERVER environment variable is not set!" >&2
    exit 1
fi

EXTRA_FLAGS=()
if [ "$CLEAN" = "true" ]; then
    EXTRA_FLAGS+=("--clean")
fi

CONCURRENCY="${CONCURRENCY:-1}"

echo "======================================================="
echo "🧩 Extracting Semantics for MemArena d7_qa Benchmark"
echo "Fast Server: ${FAST_TEXT_SERVER:-<not set>}"
echo "Strong Server: ${STRONG_TEXT_SERVER:-<not set>}"
echo "Embeddings Server: $EMBEDDINGS_SERVER"
echo "Database: ./bench/gllam_data.db"
echo "Mode: Resumable (checkpointing active; set CLEAN=true to purge)"
echo "======================================================="

go run ./cmd/extract_semantics/main.go \
  --dbpath ./bench/gllam_data.db \
  --prefix sess_ \
  --concurrency "$CONCURRENCY" \
  --embeddings-server "$EMBEDDINGS_SERVER" \
  "${EXTRA_FLAGS[@]}"
