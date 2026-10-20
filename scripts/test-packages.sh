#!/usr/bin/env bash
set -euo pipefail
# Requires the native harness binaries on PATH. No inference requests in these
# gates; isolated homes and local MCP servers are created and removed by tests.
ATLAS_ASSETS_NATIVE=1 ATLAS_PACKAGE_NATIVE=1 ATLAS_PACKAGE_CLI_NATIVE=1 \
  go test -race -v ./internal/artifact ./internal/catalog ./internal/resolve \
  ./internal/install ./internal/assets ./internal/cli -count=1 -timeout=5m
# Optional read-only check against an existing GitHub demo catalog:
# ATLAS_PACKAGE_GITHUB_NATIVE=1 ATLAS_PACKAGE_GITHUB_REPO=OWNER/REPO \
#   go test -race ./internal/assets -run TestGitHub -v -count=1
