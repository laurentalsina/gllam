#!/bin/bash
# Audit tool to inspect & compare semantic extraction graph metrics with model-specific snapshot filenames
# Usage: ./bench/run_d7_qa_audit.sh [optional_previous_snapshot_file]

export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

PREV_SNAPSHOT="${1}"
MODEL_NAME="${LLM_MODEL:-local_server}"
MODEL_SLUG=$(echo "$MODEL_NAME" | sed 's/[^a-zA-Z0-9]/_/g' | tr '[:upper:]' '[:lower:]')

SAVE_FILE="./bench/extraction_snapshot_${MODEL_SLUG}.json"

echo "======================================================="
echo "📊 Running Semantic Extraction Audit for Model: $MODEL_NAME"
echo "Snapshot File: $SAVE_FILE"
echo "======================================================="

if [ -n "$PREV_SNAPSHOT" ] && [ -f "$PREV_SNAPSHOT" ]; then
    go run ./cmd/audit_semantic_extraction/main.go \
      --db ./bench/gllam_data.db \
      --model "$MODEL_NAME" \
      --save "$SAVE_FILE" \
      --compare "$PREV_SNAPSHOT"
else
    go run ./cmd/audit_semantic_extraction/main.go \
      --db ./bench/gllam_data.db \
      --model "$MODEL_NAME" \
      --save "$SAVE_FILE"
fi
