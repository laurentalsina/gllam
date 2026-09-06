if [ -z "$EMBEDDINGS_SERVER" ]; then
    echo "❌ ERROR: EMBEDDINGS_SERVER environment variable is not set!" >&2
    exit 1
fi

CGO_ENABLED=1 CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include" \
    go run ./cmd/ingest_memarena/main.go \
      --dbpath ./bench/gllam_data.db \
      --corpus ./bench/memarena/corpus_sessions.jsonl \
      --embeddings-server "$EMBEDDINGS_SERVER"
