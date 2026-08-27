#!/usr/bin/env bash
# Mirror .github/workflows/go.yml lint job — build/ci.go linters.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "${ROOT}"

go run build/ci.go lint
go run build/ci.go check_generate
go run build/ci.go check_baddeps

echo "Lint passed"
