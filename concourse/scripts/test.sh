#!/usr/bin/env bash
# Mirror .github/workflows/go.yml test job — single Go version (1.25).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "${ROOT}"

git submodule update --init --recursive

go run build/ci.go test -p 8

echo "Tests passed"
