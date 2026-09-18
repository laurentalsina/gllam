Ref:
https://huggingface.co/datasets/Mohammadta/BEAM-10M/tree/main

The BEAM benchmark corpus file is located outside the repository at:
- `/home/laurent/Projects/agentic_benchmarks/beam_100k_conversations.jsonl̀ (13 MB)

The corresponding QA evaluation files are in the same folder:
- Sample 50 QA: `/home/laurent/Projects/agentic_benchmarks/beam_100k_qa_sample50.jsonl` (30 KB)
- Full 100k QA: `/home/laurent/Projects/agentic_benchmarks/beam_100k_qa.jsonl̀` (252 KB)

These paths are configured in the benchmark runner scripts:
- `run_beam_selective.sh:66-67i`
- `run_beam_benchmark.sh:36-37` 

There is a corpus file inside the gllam repo itself under bench/ is `corpus_sessions.jsonl`, which is the 136 MB MemArena/D7 corpus rather than the BEAM 100k corpus.
