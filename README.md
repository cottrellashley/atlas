# Atlas

A Go CLI for using **OpenCode, Pi, and Claude Code** through one project and session workflow. Atlas creates native project resources and delegates execution to your installed harness. It does not replace the harness's tools, model authentication, or conversation storage.

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
atlas exec --harness opencode
atlas exec --harness pi
atlas exec --harness claude
```

Native arguments follow `--`, for example `atlas exec --harness opencode -- auth login`. `exec` hands the terminal directly to the harness; it does not create an Atlas-managed session.

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

- Built-in researcher template and local/custom template directories.
- Native instruction, skill, MCP, and overlay generation for the three harnesses.
- Additive JSON configuration merging, conflict checks, dry runs, file hashes, symlink/traversal protection, and recovery metadata.
- Configured harness aliases with argv, environment, validated native options, and optional model defaults.
- Runtime model catalogs; model IDs remain opaque, including their slashes.
- Streaming text, reasoning/tool/usage events, native approvals, durable logs, native resume handles, and session archive/unarchive.
- Background execution through a private Unix socket daemon; no network-facing Atlas server and no Windows executable required.
- Real harness integration tests plus CLI end-to-end tests. See the [verification report](docs/verification.md) for what was actually run and which external model tests require credentials.

This implementation targets the installed **OpenCode 2.1.0, Pi 1.3.0, and Claude Code 2.1.302** protocols. OpenCode v1 is explicitly unsupported. Harness protocols can change: run the integration suite when upgrading them. Native Windows execution is not supported; run Atlas inside WSL.

## Build and test

Go 1.25 or newer is required. No third-party Go modules are needed.

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
```

See adapter documentation for external model test flags.

## Documentation

- [Company configuration and templates](docs/configuration.md)
- [Architecture and extension boundaries](docs/architecture.md)
- [Verification, supported versions, and remaining checks](docs/verification.md)
- [Claude adapter](docs/claude.md), [OpenCode adapter](docs/opencode.md), [Pi adapter](docs/pi.md)
- [Example company template](examples/templates/company-researcher/template.json)

The design was informed by a source audit of [Paseo at `068f272`](https://github.com/getpaseo/paseo/tree/068f272a00ffef3f00ec30ec4cfa3a631be97624). The implementation uses the installed native harness protocols directly; Paseo and Node bridges are not runtime dependencies of Atlas.
