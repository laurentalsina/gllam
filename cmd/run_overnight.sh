#!/bin/bash
# Wait for the background extraction task to complete
LOG_FILE="/home/laurent/.gemini/antigravity-cli/brain/56ea6821-6911-4c71-9952-1c37f9cad637/.system_generated/tasks/task-1491.log"

echo "Monitoring $LOG_FILE for completion..."
while ! grep -q "Semantic extraction complete!" "$LOG_FILE"; do
    sleep 30
done

echo "Extraction complete! Launching evaluation..."
export CGO_CFLAGS="-I/tmp/sqlite-amalgamation-3470200"
export CGO_ENABLED=1
cd /home/laurent/Projects/gllam

# Run evaluation and overwrite the results file
go run cmd/eval_beam/main.go -qa /home/laurent/Projects/agentic_benchmarks/beam_100k_qa_sample50.jsonl -out ./beam_100k_results_sample50.jsonl > eval_output.log 2>&1

echo "Evaluation complete! Launching grading..."
# Run grading and save final score to a text file
go run cmd/grade_beam/main.go > ./beam_final_grade.txt 2>&1

echo "Overnight pipeline complete! Ready for review."
