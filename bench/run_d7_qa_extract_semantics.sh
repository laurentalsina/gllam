#!/bin/bash
# Utility script to extract semantic nodes & links for MemArena d7_qa benchmark
# Usage: ./bench/run_d7_qa_extract_semantics.sh [optional_text_server_endpoint]

export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

TEXT_SERVER="${1}"
if [ -z "$TEXT_SERVER" ]; then
    if [ -n "$OPENROUTER_API_KEY" ]; then
        TEXT_SERVER="https://openrouter.ai/api/v1"
    else
        TEXT_SERVER="http://100.96.179.19:8888"
    fi
fi

EXTRA_FLAGS=()
if [ "$CLEAN" = "true" ]; then
    EXTRA_FLAGS+=("--clean")
fi

echo "======================================================="
echo "🧩 Extracting Semantics for MemArena d7_qa Benchmark"
echo "Endpoint: $TEXT_SERVER"
echo "Database: ./bench/gllam_data.db"
echo "Mode: Resumable (checkpointing active; set CLEAN=true to purge)"
echo "======================================================="

go run ./cmd/extract_semantics/main.go \
  --db ./bench/gllam_data.db \
  --prefix sess_ \
  --concurrency 4 \
  "${EXTRA_FLAGS[@]}" \
  --text-server "$TEXT_SERVER"
