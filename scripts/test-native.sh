#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Uses installed harness binaries. Claude tests perform actual model inference
# with existing native authentication. Pi/OpenCode use local model fixtures.
ATLAS_PI_E2E=1 \
ATLAS_OPENCODE_REAL=1 \
ATLAS_OPENCODE_CLI=1 \
ATLAS_TEST_CLAUDE=1 \
ATLAS_CLAUDE_E2E=1 \
go test -race ./... -count=1 -timeout=10m
