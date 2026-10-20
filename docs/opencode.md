# OpenCode adapter

## Supported runtime

This adapter targets the **OpenCode v2 HTTP API** and was exercised with the
installed **OpenCode 2.1.0** binary on Linux, on 2026-10-20. It does not implement
the legacy v1 server API. It checks the server major version; other v2 releases
still need conformance testing because several native endpoints are experimental.

The API definitions used for implementation came from the installed server's
`/openapi.json`. The public [server guide](https://opencode.ai/docs/server/) still
documents several v1 routes and authentication variable names. Do not copy those
routes into this adapter without checking the installed server schema.

The adapter is ordinary Go HTTP/SSE code. No Node bridge or OpenCode SDK package
is required by Atlas. Install and authenticate OpenCode separately:

```sh
opencode --version
atlas exec --harness opencode -- auth login
atlas init --harness opencode --template researcher --cwd ./my-project
atlas models --harness opencode --cwd ./my-project
atlas run --cwd ./my-project --background 'Inspect the project and summarize it.'
```

`atlas exec` hands the terminal to the native executable. Authentication remains
in native OpenCode storage; Atlas does not copy credentials into a project.

## Process and API lifecycle

Each catalog lookup, native discovery operation, or agent turn starts an owned
helper with `opencode serve --hostname 127.0.0.1 --port 0`. Its generated password
is passed through `OPENCODE_PASSWORD`. The helper's stdout is scanned only for
its loopback URL; arbitrary stdout/stderr, which can include credentials, is not
relayed into Atlas logs. HTTP clients do not use environment HTTP proxies for
these loopback requests. The process is closed and reaped when the operation
finishes.

The helper starts in a temporary directory. The actual project is passed with
`location.directory` or `location[directory]`. This keeps native configuration
loading tied to the selected project, independently of the CLI's directory.
Native plugin initialization is awaited. Pending MCP connections are allowed
to settle before a prompt; native failed, disabled, and authentication-required
MCP states do not disable unrelated tools.

| Atlas operation | Native v2 operation |
| --- | --- |
| Models | `GET /api/model`, `/api/model/default`, `/api/agent` with project location |
| New session | `POST /api/session` with project, optional model/agent/permissions |
| Prompt | `POST /api/session/{id}/prompt` |
| Live output | `GET /api/event` SSE, filtered by native session ID |
| Recovery and completion | Paginated native messages, durable execution log, `/api/session/active` |
| Native discovery | Paginated `GET /api/session`, root sessions, unarchived, project directory |
| Resume | Read existing native session, optionally update settings, submit another prompt |
| Permissions/questions | Location request lists, verified session ancestry, reply to owning session |
| Cancellation | Native interrupt request, then helper process group cleanup |

A prompt acknowledgment only means admission. Atlas finishes the turn after a
native execution succeeded/failed/interrupted event and the session is no
longer active. A dropped SSE connection triggers recovery from native messages
and execution logs. EOF cannot become a successful turn.

Text and reasoning are tracked separately by native message/part identity.
Previously persisted history is seeded before a resumed turn, so old responses
are not emitted again. Tool state changes are emitted as raw native tool data.

## Models and native session identity

Model IDs remain `provider/model`; additional slashes in the model portion are
preserved. Native variants are exposed as thinking choices. `--thinking` maps
to the native model variant. With no explicit model, a new session uses the
native default; a resumed session keeps its saved model. A standalone thinking
override applies to that model.

The catalog contains native enabled models, which is **not proof of inference
authorization**. Provider quota, account restrictions, unavailable local model
servers, and provider errors are reported by the native execution failure.

A versioned opaque handle stores `sessionId`, `cwd`, and `apiMajor: 2`. It is
emitted immediately after native identity is known, including on a later failed
turn. Resume requires the saved project directory. Native discovery currently
returns at most 1,000 root sessions and follows native pagination cursors.
OpenCode continues to own its SQLite/history storage; Atlas does not recreate
that storage format or flatten the native conversation into a new prompt.

```sh
atlas sessions discover --harness opencode --cwd ./my-project --json
atlas sessions import --harness opencode --cwd ./my-project --handle ses_...
atlas send ATLAS_SESSION_ID 'Continue the investigation.' --background
atlas wait ATLAS_SESSION_ID --timeout 10m
atlas logs ATLAS_SESSION_ID --json
```

## Native project configuration

Atlas writes the `researcher` template into native locations:

| File | Owner/purpose |
| --- | --- |
| `AGENTS.md` | Native project instructions |
| `.opencode/skills/research/SKILL.md` | Native discoverable research skill |
| `opencode.json` | Native model/provider/MCP and other OpenCode settings |
| `.atlas/project.json` | Atlas project role/default-harness metadata |

The built-in template leaves the model unset and starts with an empty MCP map.
It does not install a provider SDK, MCP server, or credential. Custom templates
can populate these native files. OpenCode handles its normal global/project
configuration merging and instruction discovery. See the primary
[configuration guide](https://opencode.ai/docs/config/),
[rules guide](https://opencode.ai/docs/rules/),
[skills guide](https://opencode.ai/docs/skills/), and
[MCP guide](https://opencode.ai/docs/mcp-servers/).

In the tested v2 runtime, MCP tools can be exposed through native Code Mode's
`execute` tool and searchable tool catalog. The connected MCP server's tool may
not appear as a separate top-level model tool. Atlas delegates this behavior to
OpenCode. Skills are discovered natively and loaded by native skill mechanisms;
Atlas does not paste every skill body into every request.

### Harness profile options

Add named profiles in `$ATLAS_HOME/config.json`:

```json
{
  "schemaVersion": 1,
  "harnesses": {
    "opencode-work": {
      "adapter": "opencode",
      "command": ["/absolute/path/to/opencode"],
      "env": {"OPENCODE_CONFIG": "/absolute/path/to/work-opencode.json"},
      "options": {
        "startupTimeoutSeconds": 30,
        "permissions": [{"action": "*", "resource": "*", "effect": "ask"}]
      }
    }
  }
}
```

The command is an argv prefix, not a shell fragment. Native environment
variables such as `OPENCODE_CONFIG`, `OPENCODE_CONFIG_CONTENT`, and XDG directory
overrides pass to the helper. A profile can optionally specify `model`.
Unknown adapter option keys fail explicitly. `startupTimeoutSeconds` accepts
1–300 and controls helper launch. Native API calls/readiness have their own
bounded deadlines.

Permissions use **v2** native `action`, `resource`, and `effect` rules. CLI
`--permission-mode ask|allow|deny` sets a wildcard rule for the session;
`default` leaves native/profile behavior in place. These are not interchangeable
with the legacy v1 permission configuration object.

Permission allow replies grant a single request. Denial can cause OpenCode to
interrupt execution; Atlas then reports an unsuccessful turn while retaining
the resume handle. Native questions appear as permissions with `tool: question`
and raw field definitions. Answer with a JSON object keyed by native field key:

```sh
atlas permissions list ATLAS_SESSION_ID
atlas permissions allow ATLAS_SESSION_ID REQUEST_ID --text '{"answer":"yes"}'
atlas permissions deny ATLAS_SESSION_ID REQUEST_ID
```

Requests from native child/grandchild sessions in the project are routed to
their actual owning session. Parent links are checked before exposing a
request; another root session in the same project is excluded. Child transcript
text is not merged into the root transcript. Root tool events retain native
subagent tool information.

## Reproducible validation

Fast tests require only Go:

```sh
go test ./internal/adapters/opencode
go test -race ./internal/adapters/opencode
```

Real installed-runtime tests use temporary projects and isolated XDG data.
They create and remove only their own Atlas daemon home. Their model backend
is deterministic local HTTP fixture code: these validate transport and tool
execution, not model intelligence or external provider access.

```sh
ATLAS_OPENCODE_REAL=1 ATLAS_OPENCODE_CLI=1 \
  go test -v ./internal/adapters/opencode -count=1
```

Coverage includes streamed response, native session persistence/listing,
restart/resume history, shell tool permissions, active cancellation, native
instructions/skill/MCP configuration, and descendant permission/question
ownership. The CLI portion builds a fresh binary and exercises init, models,
background run, wait, log cursors, daemon restart/send, native import, permission
allow/deny, stop, and session listing.

Actual local model inference was also verified separately with Qwen3-0.6B
Q8_0 served by llama.cpp b11761: a fresh Atlas CLI run and a follow-up after
process restart returned the requested marker while preserving the native
session handle. See [local model verification](local-model-verification.md)
for the configuration and retained evidence. This does not establish access to
an external company provider.

An opt-in provider inference test is separate:

```sh
ATLAS_OPENCODE_EXTERNAL_MODEL=your-provider/your-model \
  go test -v ./internal/adapters/opencode -run TestRealOpenCodeExternal -count=1
```

On this development machine, OpenCode reported no authenticated integrations.
The attempted free-tier server model returned HTTP 403 with a provider
restriction; it was not counted as successful inference and no bypass was
attempted. Use an authorized provider or a local model server for that test.
