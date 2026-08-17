# Build and Test Instructions for GLLAM

To compile Go packages or run tests for this project, you must set the following environment variables to ensure CGO has access to the correct libraries and headers (e.g. `sqlite-vec-go-bindings` and `go-sqlite3`):

```bash
export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"
```

## Running Tests
Run tests with:
```bash
CGO_ENABLED=1 CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include" go test -v ./pkg/...
```

## Building binaries
```bash
CGO_ENABLED=1 CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include" go build ./cmd/...
```
