export EMBEDDINGS_SERVER="http://127.0.0.1:8800"

export DATABASE_PATH="/home/laurent/Projects/gllam/bench/gllam_data.db"

# Jev by typesafe.ai is a very different beast, only structured choice or boolean answers, super fast
export TYPESAFE="Jev,https://api.typesafe.ai/v1/systemone,apikey42424242424242424242424242424242424242424242424242424242424"
# gemini workhorse text model on openrouter
export OPENROUTER="google/gemini-3.7-flash,https://openrouter.ai/api/v1,sk4242424242424242424242424242424242424242424242424242424242"-
# very fast and often good enough text model
export CEREBRAS="qwen-3.8-27b,https://api.cerebras.ai/v1/chat/completions,csk-42424242424242424242424242424242424242424242424242424242"
# lemonade's llama.cpp, lemond to bypass lemonade's 10 minutes hardcoded timeout on llama.cpp
export LEMOND="Qwen3.8-27B-UD-Q8_K_XL,http://127.0.0.1:8001,"
#export LLAMACPP="Qwen3.8-27B-UD-Q8_K_XL,http://100.96.179.19:8888," (slower build of llama.cpp than AMD's lemond)

export TYPESAFE_PARAMS="65535"
export OPENROUTER_PARAMS="262144,timeout:180,temperature:1.0,minp:0.05,topp=0.95,repeat:1.0,presence:0.3,frequency:0.3"
export CEREBRAS_PARAMS="131072,timeout:360,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3"
export LEMOND_PARAMS="262144,timeout:960,temperature:0.3,minp:0.05,topp:0.95,repeat:1.1,presence:0.3,frequency:0.3"

export SEMANTIC_EXTRACTION=TYPESAFE
export SEARCH_CANDIDATES=TYPESAFE
export QUERY_DECOMPOSITION=TYPESAFE
export ZERO_SHOT_ANSWER=TYPESAFE
export FINAL_ANSWER=TYPESAFE
export FALLBACK_ANSWER=TYPESAFE
export BENCH_RESULT_EVALUATION=TYPESAFE

export TARGET_COMPRESSION=60
