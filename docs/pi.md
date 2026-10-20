# Pi harness adapter

The Go adapter runs the installed Pi CLI as a subprocess using JSONL RPC. It was
implemented against **Pi 1.3.0**. Pi loads its own project instructions, settings,
skills, prompt templates, extensions, and MCP configuration in the requested
working directory.

## Configuration

A harness configuration uses adapter `pi`, an argv array for `command`, optional
environment overrides, an optional default model, and these adapter options:

```json
{
  "adapter": "pi",
  "command": ["pi"],
  "model": "provider/model-id",
  "options": {
    "sessionDir": "/absolute/path/to/sessions",
    "offline": false
  }
}
```

| Option | Meaning |
| --- | --- |
| `sessionDir` | Pass `--session-dir`; native files stay in this directory. |
| `approve: true` | Pass Pi's `--approve` for intentional one-run project trust. |
| `approve: false` | Pass `--no-approve`, skipping protected project resources. |
| `approve` omitted | Preserve Pi's saved/native trust decision. |
| `offline: true` | Pass `--offline`, disabling Pi startup network operations; model requests still use their configured endpoint. |

Commands are launched without a shell. Additional native Pi options can be
included as individual `command` array elements. Do not include `--no-session`,
`--print`, or a competing output mode: managed execution requires persistent RPC.
Atlas does not accept unknown adapter options silently.

Pi 1.3's RPC mode cannot show its built-in project-trust prompt. If no existing
trust decision applies, it can skip `.pi` settings/resources. Use the explicit
trust option for a project you intend to load. `AGENTS.md` context discovery is
separate from protected project resources. Project trust is not a per-tool
permission mode.

The CLI exposes this deliberate trust decision as `atlas run --trust-project`.
The managed session retains it for subsequent `atlas send` turns. For example:

```bash
atlas init --harness pi --template researcher
atlas models --harness pi
atlas run --harness pi --trust-project --background \
  '/skill:research Investigate the project architecture.'
atlas wait SESSION_ID
atlas logs SESSION_ID
atlas send SESSION_ID 'Summarize the findings.' --background
```

Pi 1.3 loads MCP servers from `.pi/mcp.json` under `mcpServers`. Atlas renders
portable stdio/HTTP server declarations there. Pi-specific fields can be supplied
through a template's Pi overlay and are preserved by configuration merging.
For example, setting a server's `exposure` to `direct` declares its
`mcp__SERVER__TOOL` tools in the model request. Pi's default `codemode` exposure
uses Pi's code tool instead; the adapter does not change that native default.

## Models and native identity

Catalog reads `get_available_models` and `get_state` from an ephemeral native Pi
runtime in the requested cwd. IDs are returned as `provider/model-id`; model
defaults and context limits come from Pi. The adapter does not advertise a
fabricated universal thinking-level list. A requested thinking level is passed
to Pi for native interpretation.

Run model precedence is explicit request, then configured harness default for a
new conversation, then Pi's own default. Resume retains the native conversation's
model unless the request explicitly overrides it.

The adapter asks for state before submitting the first prompt and immediately
emits a persistence event. Handle version 1 contains:

```json
{
  "sessionId": "native-session-id",
  "sessionFile": "/absolute/path/to/native-session.jsonl"
}
```

The path is required for resume. A missing file or unexpected resumed session ID
is an error; it does not silently start a new conversation. The reported file path
can precede the first durable transcript write by Pi. Failed runs still return any
known handle, so the manager retains identity for diagnosis/recovery.

## Native session discovery

Discovery reads native JSONL session descriptors, not Atlas's managed record
index. It recognizes header identity and cwd, explicit session names, user prompt
previews, model changes, and timestamps. It returns the native session ID and a
versioned handle containing the file path. Cwd comparisons resolve symlinks when
possible.

Session-directory precedence follows:

1. Adapter `sessionDir`, or a native `--session-dir` argument.
2. `PI_CODING_AGENT_SESSION_DIR` from configured environment or inherited env.
3. Project `.pi/settings.json` `sessionDir`.
4. Agent-directory `settings.json` `sessionDir`.
5. `<PI_CODING_AGENT_DIR>/sessions`, defaulting to `~/.pi/agent/sessions`.

Relative configured paths resolve against the requested cwd. Native discovery
reads the newest 500 candidate files, with a 64 KiB header window and 256 KiB tail
window per file. Very old sessions outside that candidate window and unusually
large entries may lack complete discovery metadata. Runtime resume itself uses
Pi's full native session parser. Invalid/in-progress JSONL candidates are skipped.

## Execution and event handling

The adapter reads stdout continuously as LF-delimited JSON records. Unicode line
separators inside a JSON string are not record delimiters. Stderr is kept separate
and bounded for process diagnostics. A single record is limited to 16 MiB.

The runtime maps:

| Pi event | Atlas event |
| --- | --- |
| Assistant text deltas | `text` |
| Thinking deltas | `reasoning` |
| Tool start/update/end | `tool`, preserving native payload |
| Usage reports | `usage`, preserving native payload |
| Model/thinking/session updates | `config` |
| Extension dialogs | `permission` |
| Extension notifications, retries, compaction | `notice` |

The authoritative final assistant message becomes the run result. Assistant
failure/abort messages become errors even when the initial prompt command was
accepted. Automatic retry may replace an earlier failed attempt with success.

**Completion waits for `agent_settled`, not `agent_end`.** In Pi 1.3, `agent_end`
can precede retries, compaction recovery, steering, or follow-up work. A prompt
whose disposition is `handled` can finish without a model run. The adapter checks
final native state and keeps ownership if an extension independently started work.

The current driver owns one foreground turn and its native process. It resumes
the same durable session for the next turn. It does not keep extension-owned
background work resident between separate Atlas runs.

## Permissions and steering

Pi has no general native per-tool approval mode or generic named-agent selector.
The adapter rejects nonempty `PermissionMode` and `NativeAgent` requests rather
than silently ignoring them. Native tools retain Pi's normal behavior.

Supported extension UI methods are `confirm`, `select`, `input`, and `editor`.
Requests preserve native ID, method, title/message, choices, and raw input:

- Allowing `confirm` sends `confirmed: true`.
- Allowing `select` requires text exactly matching one offered choice.
- Allowing `input`/`editor` sends the supplied text.
- Denial sends `cancelled: true`.
- Native dialog timeouts expire the pending Atlas permission too.

Pi's extension UI response has no separate RPC acknowledgement. The adapter emits
`permission_resolved` after writing the response successfully. Unsupported native
TUI-only extension widgets are not converted into interactive Atlas controls;
fire-and-forget UI requests become notices.

Steering sends Pi's native `steer` command. Pi queues it according to its own turn
semantics; prompt acceptance alone is not proof the steering text was consumed.

## Cancellation and cleanup

Caller cancellation first cancels pending dialogs, sends `clear_queue`, then
`abort`. Clearing first prevents queued messages from restarting work after abort.
The adapter waits for native settlement/abort response, closes stdin for orderly
shutdown, and reaps the process. A bounded fallback terminates the process group.
The method returns the caller's cancellation error and any known persistence handle.

The subprocess uses an owned context so caller cancellation can attempt native
abort before force termination. Unknown process exit or malformed protocol data
is an error, not successful completion.

## Verification

Unit checks:

```bash
go test ./internal/adapters/pi
```

Installed-Pi protocol tests:

```bash
ATLAS_PI_E2E=1 go test ./internal/adapters/pi -v -count=1
```

These tests run the **real installed Pi executable**, with isolated settings and
native session files, against a local deterministic OpenAI-compatible HTTP
fixture. They cover catalog, actual native `read` tool execution, streaming,
persistence/discovery, native resume with prior context, cancellation, extension
approval, automatic retry settlement, provider errors, and handled commands.
The CLI tests build a fresh Atlas binary and exercise:

- Template initialization and actual native skill expansion.
- Models, background run, wait, logs, send/resume and managed session listing.
- Native discovery/import, including idempotent repeated import.
- Extension approval/denial through CLI commands and stop during a pending dialog.
- A custom portable template plus Pi overlay that launches a real stdio MCP
  server child, declares its tool to the model, and executes the tool through Pi.

The HTTP fixture is not a real language model and does not prove external-provider
authentication or inference.

Real authenticated model test, making two small model requests:

```bash
ATLAS_PI_LIVE=1 ATLAS_PI_MODEL='provider/model-id' \
  go test ./internal/adapters/pi -run TestInstalledPiLiveModelResume -v -count=1
```

If `ATLAS_PI_MODEL` is omitted, it selects Pi's default available model, falling
back to its first available model. This gate fails when no authenticated model is
available. It never substitutes fixture output. It verifies a model remembers a
fresh marker after the native process is closed and resumed.

The initial environment check found no configured Pi credential providers. Use
Pi's normal authentication flow or provider environment configuration for genuine
inference. Tests never copy credentials from another application.

Actual local model inference, process restart/resume and a native file-read tool
were also verified with the official Qwen3-0.6B Q8_0 model through Atlas CLI; see
[real local model verification](local-model-verification.md) for pinned artifacts,
commands, results and the small model's observed limitations.

## Protocol references

The installed Pi 1.3.0 distribution bundles the version-matched references under
its `docs/` directory. Those local documents were the implementation authority:

- `rpc.md`: framing, command correlation, lifecycle, shutdown.
- `rpc-commands.md`: state, model catalog, prompt, steering, queue and abort.
- `json.md`: events and `agent_settled`.
- `rpc-extension-ui.md`: dialog request/response shapes.
- `session-format.md`: native JSONL metadata.
- `security.md` and `configuration.md`: project trust and native configuration.

Upstream sources: [Pi RPC](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md),
[RPC commands](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md),
[JSON events](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md),
[extension UI](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-extension-ui.md).
The upstream `main` pages may evolve beyond the tested installed version.
