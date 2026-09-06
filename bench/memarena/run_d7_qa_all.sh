#!/bin/bash
# Complete end-to-end MemArena d7_qa benchmark pipeline: Extraction -> Audit -> Evaluation -> Grading
# Usage: ./bench/memarena/run_d7_qa_all.sh

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "Starting full MemArena d7_qa Benchmark Pipeline..."

"$SCRIPT_DIR/run_d7_qa_extract_semantics.sh"
"$SCRIPT_DIR/run_d7_qa_audit.sh"
"$SCRIPT_DIR/run_d7_qa_eval.sh"
"$SCRIPT_DIR/run_d7_qa_grade_results.sh"

echo "🎉 Full MemArena d7_qa benchmark pipeline complete!"
