#!/bin/bash
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

echo "Starting MemArena-L Benchmark pipeline..."

echo "[1/3] Waiting for MemArena-L corpus ingestion to finish (if running)..."
while pgrep -f ingest_memarena > /dev/null; do
    sleep 5
done

echo "[2/3] Extracting semantic nodes & links from targeted benchmark sessions (57 evidence sessions)..."
go run ./cmd/extract_semantics/main.go --db ./bench/gllam_data.db --prefix sess_ --qa-file ./bench/d7_qa.jsonl --concurrency 1 --clean --text-server http://100.96.179.19:8888

echo "[3/3] Running MemArena-L Evaluation against GLLAM engine..."
go run ./cmd/eval_d7_qa/main.go --db ./bench/gllam_data.db --qa ./bench/d7_qa.jsonl --out ./bench/d7_qa_results.jsonl

echo "Benchmark complete! Results saved to bench/d7_qa_results.jsonl."
