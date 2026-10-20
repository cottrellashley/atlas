# Atlas

A Go CLI for using **OpenCode, Pi, and Claude Code** through one project, package, and session workflow. Atlas 0.2 creates native project resources, shares versioned skills/MCP configurations/plugins through GitHub, and delegates execution to your installed harness. Native tools, model authentication, trust, and conversation storage remain with the provider.

## Quick start

The development copy is installed at `~/.local/bin/atlas`. Run it **inside WSL/Linux**, where your native harnesses are installed and authenticated.

```bash
mkdir my-research && cd my-research
atlas init --harness claude --template researcher
atlas models
atlas run --agent researcher "Inspect this project and propose a research plan"
```

Choose `opencode`, `pi`, or `claude` for `--harness`. For Pi, review the generated resources, then use `atlas run --trust-project ...` to approve loading them, or approve the directory in Pi's own interface. Atlas leaves native trust and tool permissions in effect.

Run the native terminal interface whenever needed:

```bash
atlas tui --harness opencode
atlas tui --harness pi
atlas tui --harness claude
```

Native arguments follow `--`, for example `atlas exec --harness opencode -- auth login`. `tui` and `exec` hand the terminal directly to the harness; they do not create an Atlas-managed session. Running `opencode`, `pi`, or `claude` directly from the same project loads the same native resources.

## Share skills, MCP servers, and plugins

Use local resources immediately:

```bash
atlas skill add /path/to/skills/evidence
atlas mcp add company_docs --url https://docs.example.com/mcp \
  --header 'Authorization=Bearer ${COMPANY_DOCS_TOKEN}'
atlas package status
atlas tui
```

For company distribution, configure a GitHub.com or GitHub Enterprise repository:

```bash
# Supply GH_TOKEN/GITHUB_TOKEN for GitHub.com, or an Enterprise token for GHE.
atlas catalog add company platform/agent-packages \
  --host github.company.example --default

atlas package search research
atlas package add @research/researcher@1.4.0 --harness opencode --dry-run
atlas package add @research/researcher@1.4.0 --harness opencode
atlas package sync --locked
atlas package update @research/researcher --version 1.5.0
atlas package remove @research/researcher
```

GitHub operations use the native Go SDK. No internal API gateway, package server,
or `gh` executable is required when credentials are supplied through the supported
environment variables or Atlas login. An existing `gh` login can be used as an
optional credential bridge. `atlas catalog create` creates a private repository
by default.

Authors publish ordinary package directories:

```bash
# Paths below are relative to this repository.
atlas package validate ./examples/packages/research-skill
atlas package publish ./examples/packages/research-skill --dry-run
atlas package publish ./examples/packages/research-skill --catalog company
```

An initialization can include a published template and bundle in one transaction:

```bash
atlas init --harness claude \
  --template @research/company-project@1.0.0 \
  --package @research/researcher@1.4.0
```

Commit the package declaration and lockfile; teammates run `atlas package sync
--locked` to install the same verified release bytes. Atlas tracks ownership by
file and configuration key, preserves unrelated settings and JSONC comments,
detects user edits, and keeps shared dependencies until their last owner is
removed. Native workspace trust and MCP approval still apply.

Read [Sharing packages](docs/packages.md) for authentication, Enterprise URLs,
publishing, scopes, locks, updates, and recovery. Read [Native packages](docs/native-packages.md)
for each provider's plugin format and supported loading behavior.

## Managed work

```bash
# Foreground: stream the answer and return a nonzero exit code on failure.
atlas run --model '<exact-model-id>' "Investigate the API"

# Background: print the managed session ID; the Atlas daemon owns the turn.
atlas run --background "Write a research report"
atlas sessions list
atlas logs SESSION_ID --follow
atlas wait SESSION_ID --timeout 10m
atlas send SESSION_ID "Focus on authentication"
atlas stop SESSION_ID

# Configuration changes apply to subsequent turns; actual observed model is
# recorded separately from the requested model.
atlas configure SESSION_ID --model '<exact-model-id>'
atlas send SESSION_ID "Reconsider the tradeoffs"

# Respond to native tool approvals or extension questions.
atlas permissions list SESSION_ID
atlas permissions allow SESSION_ID PERMISSION_ID
atlas permissions deny SESSION_ID PERMISSION_ID

# Native discovery is separate from Atlas's own session index.
atlas sessions discover --harness pi --cwd . --json
atlas sessions import --harness pi --handle '<native-id-or-path>' --cwd .

atlas daemon status
atlas daemon stop
```

`resume ID "prompt"` is an alias for `send`. A wait timeout leaves work running. Ctrl+C during a foreground `run`/`send` requests cancellation and waits for cleanup; Ctrl+C while following logs only detaches. `stop` is idempotent for an already idle session. You can stop/restart the daemon and continue sessions using the same Atlas IDs.

Do not write to a native conversation from its own CLI while Atlas is running it. Atlas prevents competing writers among its managed sessions, but cannot lock an unrelated native client. Stop the native client before importing its conversation.

## What's included

- Built-in researcher template, local/custom template directories, and GitHub-distributed project templates.
- Native instruction, skill, MCP, and overlay generation for the three harnesses.
- GitHub Releases catalogs through `go-github/v82`, exact release digests, dependency bundles, and native Claude/Pi/OpenCode packages.
- Package ownership for files, JSON keys, and resource-list entries; JSONC comment preservation; conflict checks, dry runs, and journaled recovery.
- Atomic project initialization with packages, executable resource preservation, and symlink/traversal protection.
- Configured harness aliases with argv, environment, validated native options, and optional model defaults.
- Runtime model catalogs; model IDs remain opaque, including their slashes.
- Streaming text, reasoning/tool/usage events, native approvals, durable logs, native resume handles, and session archive/unarchive.
- Background execution through a private Unix socket daemon; no network-facing Atlas server and no Windows executable required.
- Real harness integration tests plus CLI end-to-end tests. See the [verification report](docs/verification.md) for what was actually run and which external model tests require credentials.

This implementation targets the installed **OpenCode 2.1.0, Pi 1.3.0, and Claude Code 2.1.302** protocols. OpenCode v1 is explicitly unsupported. Harness protocols can change: run the integration suite when upgrading them. Native Windows execution is not supported; run Atlas inside WSL.

## Build and test

Go 1.25 or newer is required. Go modules include `google/go-github/v82` for the
GitHub API and `yaml.v3` for skill frontmatter. Atlas is built as a Go binary;
the native harness executables must be installed separately.

```bash
go build -trimpath -o bin/atlas ./cmd/atlas
go test ./...
go vet ./...
go test -race ./...
install -m 755 bin/atlas ~/.local/bin/atlas
```

On this machine Go 1.27.3 was installed using mise. Without changing your global tool selection, you can build with `mise exec go@1.27.3 -- go build -o bin/atlas ./cmd/atlas`.

Native integration tests are opt-in because they launch harnesses, execute harmless tools in temporary projects, and the Claude tests use authenticated model inference. The other fixtures use local deterministic model responses; that distinction is preserved in the report.

```bash
ATLAS_PI_E2E=1 go test -v ./internal/adapters/pi -count=1
ATLAS_OPENCODE_REAL=1 ATLAS_OPENCODE_CLI=1 go test -v ./internal/adapters/opencode -count=1
ATLAS_TEST_CLAUDE=1 ATLAS_CLAUDE_E2E=1 go test -v ./internal/adapters/claude ./tests -count=1

# Installed native resource/plugin loading, MCP connections, and CLI handoff.
# These package tests use isolated homes and make no model inference requests.
ATLAS_PACKAGE_NATIVE=1 ATLAS_ASSETS_NATIVE=1 ATLAS_PACKAGE_CLI_NATIVE=1 \
  go test -v ./internal/assets -count=1 -timeout=5m
```

See adapter documentation for external model test flags.

## Documentation

- [Company configuration and templates](docs/configuration.md)
- [Sharing and publishing packages through GitHub](docs/packages.md)
- [Native Claude, Pi, and OpenCode packages](docs/native-packages.md)
- [Architecture and extension boundaries](docs/architecture.md)
- [Verification, supported versions, and remaining checks](docs/verification.md)
- [Claude adapter](docs/claude.md), [OpenCode adapter](docs/opencode.md), [Pi adapter](docs/pi.md)
- [Example company template](examples/templates/company-researcher/template.json)
- [Example distributable packages](examples/packages/README.md)

The design was informed by a source audit of [Paseo at `068f272`](https://github.com/getpaseo/paseo/tree/068f272a00ffef3f00ec30ec4cfa3a631be97624). The implementation uses the installed native harness protocols directly; Paseo and Node bridges are not runtime dependencies of Atlas.
