#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

# Uses installed harness binaries. Claude tests perform actual model inference
# with existing native authentication. Pi/OpenCode use local model fixtures.
# OMP and enterprise-policy gates make no inference requests. Supply the actual
# OMP executable to avoid wrappers which update tools or global configuration.
: "${ATLAS_OMP_BINARY:?Set ATLAS_OMP_BINARY to the absolute installed omp executable}"
ATLAS_PI_E2E=1 \
ATLAS_OPENCODE_REAL=1 \
ATLAS_OPENCODE_CLI=1 \
ATLAS_TEST_CLAUDE=1 \
ATLAS_TEST_OMP=1 \
ATLAS_CLAUDE_ENTERPRISE=1 \
ATLAS_ASSETS_NATIVE=1 \
ATLAS_PACKAGE_NATIVE=1 \
ATLAS_PACKAGE_CLI_NATIVE=1 \
go test -race ./... -count=1 -timeout=10m
