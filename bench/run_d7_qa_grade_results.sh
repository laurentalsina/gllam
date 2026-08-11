#!/bin/bash
# Utility script to grade MemArena d7_qa benchmark results with model-specific filenames & fail reports
# Usage: ./bench/run_d7_qa_grade_results.sh [optional_text_server_endpoint]

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

RESULTS_FILE="./bench/d7_qa_results_${MODEL_SLUG}.jsonl"
if [ ! -f "$RESULTS_FILE" ]; then
    RESULTS_FILE="./bench/d7_qa_results.jsonl"
fi

GRADE_SUMMARY_FILE="./bench/d7_qa_grade_${MODEL_SLUG}.txt"
FAIL_REPORT_FILE="./bench/d7_qa_failures_${MODEL_SLUG}.md"

echo "======================================================="
echo "📊 Grading MemArena d7_qa Results for Model: $MODEL_NAME"
echo "Endpoint: $TEXT_SERVER"
echo "Results Input File: $RESULTS_FILE"
echo "Grade Summary Output: $GRADE_SUMMARY_FILE"
echo "Failure Markdown Report: $FAIL_REPORT_FILE"
echo "======================================================="

go run ./cmd/grade_results/main.go \
  --results "$RESULTS_FILE" \
  --fail-report "$FAIL_REPORT_FILE" \
  --text-server "$TEXT_SERVER" | tee "$GRADE_SUMMARY_FILE"
