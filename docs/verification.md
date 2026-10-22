# Verification report

## Final compatibility checks — 2026-10-22

Tested on Linux x86_64 with Go 1.27.3. The combined installed-harness suite
passed **180 top-level tests**, with **no failures or race reports**. Eleven
top-level skips were eight subprocess helpers and three opt-in checks for a
live GitHub catalog or external Pi/OpenCode model credentials. Those skips are
not counted as successful native checks. `go vet ./...` and the CLI build passed.
Local detailed output is in ignored `test-results/final-native-20261022.jsonl`.
After the settings-filename review, the affected configuration/package suite
passed another **73 top-level tests**, with two expected optional/helper skips
and no failures or races (`test-results/final-config-20261022.jsonl`). This
includes four new OMP regressions for active user `config.yaml`, ambiguous or
legacy settings, project filename semantics, and package target restrictions.
These counts overlap the combined suite; they are not additive. A compact
[machine-readable report](verification/compatibility-20261022.json) records
the run boundaries.

| Harness | Installed version | Verified behavior |
| --- | --- | --- |
| Claude Code | 2.1.302 | Genuine authenticated model calls, persisted conversation continuation, approvals/cancellation, native resources, and isolated enterprise-policy checks |
| OpenCode | 2.1.0 | Actual installed process with deterministic local model responses; native discovery/continuation, MCP and plugin loading, cancellation and approvals |
| Pi | 1.3.0 | Actual installed process with deterministic local model responses; native discovery/continuation, MCP and extension loading, cancellation and approvals |
| Oh-my-pi | 18.12.6 | Dedicated adapter; actual native instruction/skill loading, stdio and authenticated HTTP MCP handshakes, native-created history discovery and exact resume, fresh startup with `autoResume: true`; no model prompts |

The compiled CLI tests additionally verify native process replacement, saved
working directories, literal argument and environment forwarding, setup-only
operation, and absence of Atlas session/daemon records. Unit cases cover profile
and account isolation, large transcripts, histories beyond the browse limit,
invalid handles, partial discovery failures, terminal cleanup, and package drift.
Oh-my-pi configuration packages preserve YAML comments and unrelated settings;
unchanged syncs are byte-identical, and edits to owned fields block removal.

Actual terminal smoke checks passed new, resume, and searchable-picker handoff
for Pi, and for OpenCode in both default native service mode and `--standalone`
mode. Oh-my-pi's compiled Atlas
new/resume handoff rendered its real UI, retained saved history only on resume,
and exited cleanly with `autoResume: true`. These checks used disposable homes
and no external model calls; Pi/OpenCode histories were seeded by local fixtures.
The default OpenCode test assigned an available native service port inside its
disposable home: the native default port was already occupied on the host.
The initial port collision also reproduced without Atlas; after isolating the
port, all three default UI flows passed and the fixture service was stopped.
The existing host listener was not changed.

The enterprise gate ran all installed-Claude cases, including the private Linux
mount/network namespace test: managed MCP policy remains effective against
lower-priority settings, malformed managed policy fails closed, unapproved
project MCP does not connect, and a local TLS MCP connection requires its CA.
Provider/network environment forwarding uses synthetic credentials. **Actual
enterprise SSO, tenant authorization, corporate proxy/mTLS negotiation, live
Bedrock/Vertex/Foundry inference, and Windows/WSL deployment remain unverified.**
See [the enterprise evidence and repeatable checks](enterprise.md).

Repeat the complete gate using installed harnesses and existing Claude login:

```bash
ATLAS_OMP_BINARY=/absolute/path/to/installed/omp scripts/test-native.sh
go vet ./...
go build -trimpath -o bin/atlas ./cmd/atlas
```

This suite makes real Claude model calls. Pi/OpenCode responses are local
fixtures; Oh-my-pi inspection and enterprise-policy checks make no model calls.
The OMP path must be the actual executable, not an auto-updating shell wrapper.
Tests use disposable native homes/settings except the explicitly authenticated
Claude inference gate; no login or global native configuration is replaced.

## Native-session CLI migration

The current public interface uses native harness sessions. The Atlas 0.2
results below are historical evidence for package installation and the earlier
managed execution paths; their test counts and managed CLI examples do not
verify the new menu or session browser. Retired commands include `run`, `send`,
`logs`, `wait`, `stop`, `permissions`, `configure`, `daemon`, and managed session
import/archive operations. Existing records are preserved.

`tests/native_cli_test.go` replaces the old managed Claude CLI suite with a
compiled CLI fixture test. It builds Atlas once and verifies setup-only
initialization, configuration preservation, native Claude history listing,
actual process replacement for resume/new/exec, native working directory and
argument/environment forwarding, retired-command guidance, and the absence of
Atlas session or daemon state. Native history and all homes are isolated; the
fake native executable makes no inference requests and uses no real credentials.
This checks the handoff contract, not a real harness's interactive UI.

The following checks passed on 2026-10-21:

```bash
go test ./tests -run TestNativeCLI -count=1 -v
go test -race ./...
go vet ./...
```


The CLI build also passed. Read-only discovery of the installed harnesses'
existing stores found 27 Claude Code sessions, 2 OpenCode sessions, and 0 Pi
sessions. Only these counts are recorded here; transcripts were not modified.

Separate installed-harness checks passed for Pi CLI/MCP and OpenCode lifecycle
and CLI behavior using local deterministic model responses. Those checks launch
real harness executables, but do not demonstrate genuine model inference.
The full race/vet and fixture results above are current; the Atlas 0.2 reports
below remain historical evidence for their original managed execution paths.

## Archived Atlas 0.2 report

Verified on **2026-10-20**, Linux x86_64 (`7.2.5-3-omarchy`), using Go **1.27.3**. The tested machine is ordinary Linux, not a Windows/WSL host. Atlas targets the Linux side of WSL; a company WSL image and company model endpoints were not available for direct validation.

## Package distribution verification (Atlas 0.2)

The new package subsystem was tested with the real installed Claude Code
2.1.302, Pi 1.3.0 and OpenCode 2.1.0 executables, alongside the existing
session/inference integration suite. The final gate passed **130 top-level tests**
with no failures or race reports; `go vet ./...` also passed. Six skips were
four subprocess helper entrypoints and two optional external-model checks.
See [package-suite results](verification/packages-suite-summary.json)
and [live GitHub publication results](verification/github-packages.json).

Verified package paths:

- Local skill imports, executable skill assets, native configuration and stdio
  plus authenticated HTTP MCP connections for all three providers.
- Native Claude plugin/skill discovery, Pi extension registration, and OpenCode
  plugin execution, including TypeScript entrypoints and forwarded options.
- New public CLI commands: init, skill/MCP/package add, locked sync, status,
  update, remove and `atlas tui` terminal handoff.
- Atomic initialization with packages: template/package conflicts and conflicts
  between packages leave the target untouched.
- JSONC comment preservation, file/key/array ownership, shared dependencies,
  drift, stale plans, interrupted-operation recovery, alias and scope retention.
- Release provenance in local receipts is checked against the lock; changing a
  lock alone cannot report a different release as already installed.
- Invalid archives, digest/cache tampering, gzip corruption, path traversal,
  symlinks, cycles, conflicting releases and invalid locked dependency graphs.
- GitHub SDK authentication, pagination, publication, asset downloads, immutable
  release identity, credential isolation on redirects and explicit token login.

### Real GitHub account verification

Created the private test catalog
[cottrellashley/atlas-harness-packages](https://github.com/cottrellashley/atlas-harness-packages)
using Atlas's Go GitHub SDK integration. Published a skill, three native plugin
variants, a cross-harness bundle, a project template, and a second skill version.
Verified duplicate publication rejection, update preview, update, explicit
downgrade, edited-file protection, removal and shared-template initialization.

The read-only `TestGitHubNativeBundleTwoConsumers` gate installs the published
bundle for **each of the three providers** using two independent Atlas caches
and native homes. The second consumer receives only project/declaration/lock
records. Cold sync recreates the exact native resources; lockfile bytes remain
identical, the shared skill is discoverable and the native plugin is active.
Both consumers authenticate as the authorized test GitHub account; this is not
a test of separate company users' GitHub permissions.

Ready-to-launch examples are recorded in [demo-projects.json](verification/demo-projects.json)
and live under `~/atlas-demo/{claude,pi,opencode}`. Each uses the published
researcher bundle and has a verified clean package status.

A separate live SDK request also succeeded with an empty executable `PATH`
and a supplied token. GitHub CLI is optional credential discovery, not a
repository/API implementation dependency.

### Repeating package checks

```bash
scripts/test-packages.sh

# Read-only: uses the demo releases already published to an authorized catalog.
ATLAS_PACKAGE_GITHUB_NATIVE=1 ATLAS_PACKAGE_GITHUB_REPO=OWNER/REPO \
  go test -race ./internal/assets -run TestGitHubNativeBundleTwoConsumers -v

# Mutating: publishes a NEW version to an authorized test catalog.
# Pick a version which does not already exist; existing releases are preserved.
python3 scripts/verify-github-packages.py OWNER/REPO 1.2.0
```

The native resource tests make no inference requests. Existing adapter gates
retain their original distinction between real Claude inference and deterministic
Pi/OpenCode model fixtures. Earlier genuine local-model checks remain documented
below. No Windows/WSL machine or company GitHub Enterprise instance was available;
Enterprise URL/auth behavior was covered by configuration/SDK tests, not a live
company deployment. The sample GitHub Actions workflow is provided but has not
been run on a company runner or configured with organization approval rules.

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
