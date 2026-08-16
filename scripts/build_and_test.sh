#!/bin/bash
# Build and test verification script for GLLAM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$ROOT_DIR"

export CGO_ENABLED=1
export CGO_CFLAGS="-I/home/laurent/vllm/.venv/lib/python3.13/site-packages/_rocm_sdk_devel/lib/rocm_sysdeps/include"

echo ""
echo "=== Compiling Go Executables in ./cmd/... ==="

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

FAILED_PACKAGES=()

for cmd_dir in ./cmd/*/; do
    if [ -d "$cmd_dir" ]; then
        bin_name=$(basename "$cmd_dir")
        echo "Building cmd/$bin_name..."
        if ! go build -o "$BUILD_DIR/$bin_name" "$cmd_dir"; then
            echo "ERROR: Failed to compile cmd/$bin_name"
            FAILED_PACKAGES+=("cmd/$bin_name")
        fi
    fi
done

echo ""
if [ ${#FAILED_PACKAGES[@]} -gt 0 ]; then
    echo "COMPILATION FAILED for the following packages:"
    for pkg in "${FAILED_PACKAGES[@]}"; do
        echo "  - $pkg"
    done
    exit 1
fi

echo "=== Running Go Package Unit Tests ==="
go test -v ./pkg/...

echo "ALL TESTS PASSED AND ALL EXECUTABLES COMPILED CLEANLY."
