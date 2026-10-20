# Verification report

Verified on **2026-10-20**, Linux x86_64 (`7.2.5-3-omarchy`), using Go **1.27.3**. The tested machine is ordinary Linux, not a Windows/WSL host. Atlas targets the Linux side of WSL; a company WSL image and company model endpoints were not available for direct validation.

## End-to-end results

| Harness | Version | Actual model inference | Native integration coverage |
| --- | --- | --- | --- |
| Claude Code | 2.1.302 | Passed using existing native authentication and `haiku` | Catalog, project instructions, skills, real local MCP tool, background execution, permissions, cancellation, daemon restart, native resume/discovery/import |
| Pi | 1.3.0 | Passed with actual local Qwen3-0.6B Q8_0 weights | Full CLI lifecycle, native tools, skill expansion, actual stdio MCP, extension approval/denial, cancellation, native discovery/import/resume; retry/settlement and failure cases |
| OpenCode | 2.1.0 | Passed with the same actual local Qwen model | Full CLI lifecycle, instructions/skills, actual MCP through native Code Mode, approvals/denials, descendant permission/question ownership, cancellation, discovery/import/resume |

The local model checks used a checksum-verified temporary llama.cpp CPU server. Both Pi and OpenCode generated the requested codeword, retained it after native process restart, and read previously unseen file contents using their native tools. OpenCode's real model read also exercised an explicit Atlas CLI permission reply. All temporary runtime/model processes and downloads were cleaned up. See [the detailed real-inference report](local-model-verification.md) and [machine-readable evidence](verification/local-model-results.json).

The broader Pi/OpenCode integration suites use deterministic local model API fixtures to exercise retries, cancellation, errors, tool calls and exact protocol assertions reliably. Those fixtures still launch the actual installed harnesses and actual local MCP/tool processes, but their generated text is not genuine inference. Claude's native integration suites use real authenticated inference. These test categories are not counted interchangeably.

## Release checks

The complete opt-in native suite was run with the race detector, alongside the ordinary unit tests:

```bash
ATLAS_PI_E2E=1 \
ATLAS_OPENCODE_REAL=1 ATLAS_OPENCODE_CLI=1 \
ATLAS_TEST_CLAUDE=1 ATLAS_CLAUDE_E2E=1 \
go test -race -json ./... -count=1 -timeout=10m
go vet ./...
```

It passed without race reports. Raw local test output is retained in ignored `test-results/native.jsonl`; a compact committed summary is in [native-suite-summary.json](verification/native-suite-summary.json).

Core regression checks include:

- Custom manifest validation, native null-object rejection, deterministic initialization and reinitialization.
- Additive configuration merge, unchanged files on conflicts, symlink/traversal rejection, changed-file detection and JSONC shadowing refusal.
- Existing private file modes and executable skill assets preserved; MCP environment references translated without resolving secrets.
- Configured executable PATH and literal argument handling; inherited Claude session markers removed.
- Permission responses remain pending until native acknowledgment; multiple resolutions preserve remaining requests.
- Concurrent send exclusion, cancellation settlement, durable handle/sequence ordering and stable managed identity.
- Failed persistence cannot leave a phantom running turn or consume a nonexistent log cursor.
- Partial log-tail repair, complete-record corruption detection, replay after metadata lag and pending permission cleanup on restart.
- Linux direct native child terminates after its owner receives SIGKILL.
- Foreground Ctrl+C during a status request still requests stop and awaits its acknowledgment.

## Limits that remain explicit

- **Company authentication/endpoints:** not validated. OpenCode's listed free model endpoint rejected a test with HTTP 403; no restriction was bypassed. Pi had no configured external credentials. Real inference for those harnesses was therefore verified with an isolated local model. Use normal native login/configuration for your company's endpoints.
- **WSL deployment:** no Windows/WSL host was available. The Go executable and tests run on Linux; run the same native suite inside the company WSL image before rollout. Native Windows execution is not supported.
- **Versions:** OpenCode v1 is not implemented. New harness versions require rerunning conformance tests; v2's experimental endpoints and native RPC/control protocols can change.
- **Native model quality:** the tiny local model sometimes chose text instead of a tool when a previous instruction prohibited tools. A fresh Pi conversation performed the requested read. This is documented rather than hidden as a passing first attempt.
- **Crash/process limits:** Linux parent-death handling covers direct children; arbitrary detached grandchildren created by native tools can require inspection after a hard daemon kill. Interrupted prompts are never automatically replayed.
- **Native features:** configuration changes apply between turns; Atlas does not implement every native interactive slash command, cross-harness conversation conversion, or a universal subagent abstraction. `atlas exec` gives access to each native CLI directly.

## Repeating external inference checks

After configuring native authentication, run:

```bash
ATLAS_PI_LIVE=1 ATLAS_PI_MODEL='<provider/model>' \
  go test -v ./internal/adapters/pi -run '^TestInstalledPiLiveModelResume$' -count=1

ATLAS_OPENCODE_EXTERNAL_MODEL='<provider/model>' \
  go test -v ./internal/adapters/opencode -run '^TestRealOpenCodeExternal$' -count=1
```

These gates perform genuine model calls and must fail when authentication or model execution fails. They are skipped in the deterministic suite; the local Qwen checks above are recorded separately.
