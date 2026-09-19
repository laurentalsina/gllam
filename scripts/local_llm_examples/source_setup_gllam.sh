# ==============================================================================
# GLLAM Multi-Provider Configuration
# ==============================================================================

export EMBEDDINGS_SERVER="http://127.0.0.1:8800"
export DATABASE_PATH="/home/laurent/Projects/gllam/bench/gllam_data.db"

# ------------------------------------------------------------------------------
# 1. STRUCT-Only Endpoints (System One / Decision / Extraction / Scoring)
#    - Native schema: POST /v1/systemone (state + questions)
#    - Primitives: choice, score, noul (calibrated probabilities, no prose)
#    - Note: Endpoints restricted to structured evaluation must be marked with _KIND="STRUCT".
# ------------------------------------------------------------------------------
export TYPESAFE="jev-latest,https://api.typesafe.ai/v1/systemone,apikey42424242424242424242424242424242424242424242424242424242424"
export TYPESAFE_KIND="STRUCT"
export TYPESAFE_PARAMS="65535"

# ------------------------------------------------------------------------------
# 2. General / TEXT Endpoints (Default: OpenAI-compatible Chat & Completion)
#    - Native schema: POST /chat/completions (messages -> text generation)
#    - Note: Endpoints default to general text generation; no _KIND needed.
# ------------------------------------------------------------------------------
# Cerebras: Ultra-low latency, high-throughput text generation
export CEREBRAS="qwen-3.8-27b,https://api.cerebras.ai/v1/chat/completions,csk-42424242424242424242424242424242424242424242424242424242"
export CEREBRAS_PARAMS="131072,timeout:360,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3"

# OpenRouter: Gemini workhorse for deep reasoning & fallback synthesis
export OPENROUTER="google/gemini-3.7-flash,https://openrouter.ai/api/v1,sk4242424242424242424242424242424242424242424242424242424242"
export OPENROUTER_PARAMS="262144,timeout:180,temperature:1.0,minp:0.05,topp:0.95,repeat:1.0,presence:0.3,frequency:0.3,reasoning:medium"

# Lemond / llama.cpp: Local high-context execution
export LEMOND="Qwen3.8-27B-UD-Q8_K_XL,http://127.0.0.1:13305,"
export LEMOND_PARAMS="262144,timeout:960,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3"

# Optional remote llama.cpp instance
# export LLAMACPP="Qwen3.8-27B-UD-Q8_K_XL,http://100.96.179.19:8888,"
# export LLAMACPP_PARAMS="131072"

# ------------------------------------------------------------------------------
# 3. Task-to-Provider Routing
# ------------------------------------------------------------------------------

# --- STRUCT Tasks (Decision, Candidate Filtering, Semantic Classification) ---
#     can be assigned to any LLM
export SEMANTIC_EXTRACTION=TYPESAFE
export SEARCH_CANDIDATES=TYPESAFE
export QUERY_DECOMPOSITION=TYPESAFE

# --- TEXT Tasks (Generative Synthesis, Open-Ended QA, Evaluation) ---
#      cannot be assigned to "STRUCT"-kind LLMs
export ZERO_SHOT_ANSWER=CEREBRAS
export FINAL_ANSWER=CEREBRAS
export FALLBACK_ANSWER=OPENROUTER
export BENCH_RESULT_EVALUATION=LEMOND
export TURN_COMPRESSION=CEREBRAS

# ------------------------------------------------------------------------------
# 4. Pipeline Parameters & Legacy Aliases
# ------------------------------------------------------------------------------
export TARGET_COMPRESSION=60

# Legacy dual-tier aliases for backward compatibility with older benchmark scripts
export FAST_TEXT_SERVER="${CEREBRAS}"
export STRONG_TEXT_SERVER="${OPENROUTER}"
