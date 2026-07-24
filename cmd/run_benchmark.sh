#!/bin/bash
export CGO_CFLAGS="-I/tmp/sqlite-amalgamation-3470200"
export CGO_ENABLED=1

echo "Starting MemArena-L Benchmark pipeline..."

echo "[1/2] Waiting for MemArena-L corpus ingestion to finish..."
# Wait for the ingest_memarena process to finish if it's running
while pgrep -f ingest_memarena > /dev/null; do
    sleep 5
done

echo "[2/2] Running MemArena-L Evaluation against GLLAM engine..."
go run ./cmd/eval_d7_qa/main.go --out ./d7_qa_results.jsonl

echo "Benchmark complete! Results saved to d7_qa_results.jsonl."
