#!/bin/bash
ulimit -l unlimited
export HSA_OVERRIDE_GFX_VERSION=11.5.1

# Bind explicitly to your Tailscale network interface IP
# HTPS over Tailscale IP with Caddy as the proxy
# HOST="127.0.0.1"
HOST="100.96.179.19"
PORT="8888"

# 1. Target your active development workspace
cd /home/laurent/Projects/phurba_lora

# 2. Tell the dynamic linker where the companion libraries live
export LD_LIBRARY_PATH="/home/laurent/Projects/llama.cpp/build/bin:$LD_LIBRARY_PATH"

# 3. Execute Firejail using the localized profile constraints
firejail \
  --profile=/home/laurent/Projects/phurba_lora/sandbox.profile \
  --read-only=/home/laurent/Projects/llm_models \
  --allow-debuggers \
  /home/laurent/Projects/llama.cpp/build/bin/llama-server \
    --model /home/laurent/Projects/llm_models/Ornith-1.5-35B-BF16.gguf \
    --cache-type-k q8_0 --cache-type-v q8_0 \
    --spec-ngram-mod-n-max 8 \
    --spec-ngram-mod-n-min 1 \
    --metrics \
    --host "$HOST" \
    --port "$PORT" \
    --device rocm0 \
    --n-gpu-layers 99 \
    --ctx-size 131072 \
    --threads 16 \
    --parallel 1 \
    --temp 0.7 \
    --repeat-penalty 1.5 \
    --top-k 20 \
    --jinja \
    --batch-size 2048 --ubatch-size 512 \
    --log-verbosity 3 \
