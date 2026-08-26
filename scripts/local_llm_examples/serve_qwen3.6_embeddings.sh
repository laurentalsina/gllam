#!/bin/bash

# this is to serve embeddings for GLLAM, see https://huggingface.co/Qwen/Qwen3-Embedding-0.6B-GGUF

ulimit -l unlimited
export HSA_OVERRIDE_GFX_VERSION=11.5.1

# Bind explicitly to your Tailscale network interface IP
# HOST="100.96.179.19"
# Switched to HTPS over Tailscale IP with Caddy as the proxy
HOST="127.0.0.1"
PORT="8800"

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
    --model /home/laurent/Projects/llm_models/Qwen3-Embedding-0.6B-f16.gguf \
    --embedding --pooling last -ub 8192 \
    --metrics \
    --host "$HOST" \
    --port "$PORT" \
    --device rocm0 \
    --tools all \
    --n-gpu-layers 50 \
    --ctx-size 131070 \
    --threads 16 \
    --parallel 1 \
    --no-mmap \
    --numa distribute \
    --flash-attn on \
    --temp 0.8 \
    --min-p 0.05 \
    --repeat-penalty 1.05 \
    --frequency-penalty 0.04
