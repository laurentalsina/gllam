#!/bin/bash
# Complete end-to-end MemArena d7_qa benchmark pipeline: Extraction -> Audit -> Evaluation -> Grading
# Usage: ./bench/run_d7_qa_all.sh [optional_text_server_endpoint]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Starting full MemArena d7_qa Benchmark Pipeline..."

"$SCRIPT_DIR/run_d7_qa_extract_semantics.sh" ${1:+"$1"}
"$SCRIPT_DIR/run_d7_qa_audit.sh"
"$SCRIPT_DIR/run_d7_qa_eval.sh" ${1:+"$1"}
"$SCRIPT_DIR/run_d7_qa_grade_results.sh" ${1:+"$1"}

echo "🎉 Full MemArena d7_qa benchmark pipeline complete!"
