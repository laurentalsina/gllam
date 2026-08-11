#!/bin/bash
# Utility script to run MemArena d7_qa evaluation with model-specific output filenames
# Usage: ./bench/run_d7_qa_eval.sh [optional_text_server_endpoint]

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

MODEL_NAME="${LLM_MODEL:-local_server}"
MODEL_SLUG=$(echo "$MODEL_NAME" | sed 's/[^a-zA-Z0-9]/_/g' | tr '[:upper:]' '[:lower:]')
OUT_FILE="./bench/d7_qa_results_${MODEL_SLUG}.jsonl"

echo "======================================================="
echo "🚀 Running MemArena d7_qa Evaluation"
echo "Endpoint: $TEXT_SERVER"
echo "Model: $MODEL_NAME"
echo "Database: ./bench/gllam_data.db"
echo "QA File: ./bench/d7_qa.jsonl"
echo "Output Results File: $OUT_FILE"
echo "======================================================="

go run ./cmd/eval_d7_qa/main.go \
  --db ./bench/gllam_data.db \
  --qa ./bench/d7_qa.jsonl \
  --out "$OUT_FILE" \
  --text-server "$TEXT_SERVER"
