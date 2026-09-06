#!/bin/bash
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

echo "Starting MemArena-L Benchmark pipeline..."

echo "[1/3] Waiting for MemArena-L corpus ingestion to finish (if running)..."
while pgrep -f ingest_memarena > /dev/null; do
    sleep 5
done

if [ -z "$FAST_TEXT_SERVER" ] && [ -z "$STRONG_TEXT_SERVER" ]; then
    echo "❌ ERROR: Neither FAST_TEXT_SERVER nor STRONG_TEXT_SERVER is set!" >&2
    echo "Please source your environment configuration (e.g. source scripts/local_llm_examples/source_setup_gllam.sh)." >&2
    exit 1
fi

if [ -z "$EMBEDDINGS_SERVER" ]; then
    echo "❌ ERROR: EMBEDDINGS_SERVER environment variable is not set!" >&2
    exit 1
fi

echo "[2/3] Extracting semantic nodes & links from targeted benchmark sessions (57 evidence sessions)..."
go run ./cmd/extract_semantics/main.go --db ./bench/gllam_data.db --prefix sess_ --qa-file ./bench/d7_qa.jsonl --concurrency 1 --clean --embeddings-server "$EMBEDDINGS_SERVER"

echo "[3/3] Running MemArena-L Evaluation against GLLAM engine..."
go run ./cmd/eval_d7_qa/main.go --db ./bench/gllam_data.db --qa ./bench/d7_qa.jsonl --out ./bench/d7_qa_results.jsonl

echo "Benchmark complete! Results saved to bench/d7_qa_results.jsonl."
