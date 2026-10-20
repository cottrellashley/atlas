# Claude Code adapter

Atlas runs the installed Claude Code executable directly from Go. It uses Claude Code's newline-delimited `stream-json` input/output and SDK control messages; it does not embed Node, the TypeScript SDK, or an Anthropic inference client.

## Tested version

The adapter was verified with **Claude Code 2.1.302** on Linux, using an existing authenticated Claude subscription. The native `haiku` alias resolved to `claude-haiku-5-5` during the test. Alias resolutions and catalog entries are supplied by Claude Code and can change.

The control protocol follows the installed Agent SDK transport, including `initialize`, `can_use_tool`, control responses, and `interrupt`. This part of the native CLI is version-sensitive. The adapter rejects initialization/protocol failures and does not fall back to scraping terminal output. Repeat the opt-in tests when updating the supported Claude version.

Relevant native documentation:

- [Programmatic Claude Code](https://code.claude.com/docs/en/headless)
- [Streaming output](https://code.claude.com/docs/en/agent-sdk/streaming-output)
- [Approval callbacks and user input](https://code.claude.com/docs/en/agent-sdk/user-input)
- [Native project memory](https://code.claude.com/docs/en/memory)

## Execution and project configuration

Every Atlas turn starts a process in the requested project directory. The command prefix comes from the configured harness; Atlas appends its protocol flags. A new native UUID is passed with `--session-id`. A continued Atlas conversation passes its opaque native ID to `--resume`, preserving native conversation history.

The adapter uses normal Claude project discovery. It does not pass `--bare`, `--safe-mode`, `--restricted`, or `--strict-mcp-config`. Consequently Claude loads native project instructions, skills, MCP configuration, hooks, and applicable settings according to its own rules. Atlas's Claude template uses `CLAUDE.md` to import the shared `AGENTS.md` instructions.

Authentication is inherited from Claude's normal environment/configuration. Run `claude auth login` through the native CLI if authentication is missing. Atlas does not copy credentials into session metadata.

The runtime starts with `--input-format stream-json`, `--output-format stream-json`, `--verbose`, `--include-partial-messages`, and `--permission-prompt-tool stdio`. It initializes the control connection before sending the user message. Merely accepting the prompt is not treated as completion: `Run` finishes only after a successful terminal result, or returns an error/cancellation.

An early persistence event stores the session ID when native output confirms it. Model information comes from native initialization/assistant messages. A failed turn can still return the known resume handle. Streamed text is emitted once; the corresponding complete assistant message is not duplicated.

Each turn releases its process after the native terminal result. Close allows a short native shutdown/persistence window, then kills the owned process group if needed and waits for the process to be reaped. Cancellation requests a native interrupt, closes input, and follows the same bounded cleanup. A new turn is not admitted by this driver before the previous `Run` returns.

## Models, effort, and permissions

`Catalog` starts a nonpersistent native control connection and reads the `initialize` response. No model prompt is sent. Returned model IDs are the native `value` fields, including aliases such as `haiku` and `default`; effort choices come from `supportedEffortLevels`. Catalog visibility is not a guarantee of entitlement to every model.

Leaving the model unset preserves Claude's own default unless the configured Atlas harness provides a model. An explicit model is passed unchanged. Thinking effort accepts `low`, `medium`, `high`, `xhigh`, or `max`; the native CLI validates support for the chosen model.

Native permission modes exposed are `manual`, `plan`, `acceptEdits`, `dontAsk`, `auto`, and `bypassPermissions`. Atlas also accepts `default` as an alias for `manual` for this tested version. `bypassPermissions` is enabled only when explicitly requested; it is not the adapter default. Native availability and managed settings may further restrict modes.

For a `can_use_tool` request, Atlas emits a permission containing the native request ID, tool name, description, and input. A reply is sent back with that exact request ID:

- Allow preserves the native tool input.
- Deny sends a denial and optional user message.
- A native cancellation resolves the pending permission.
- `AskUserQuestion` requires an answer. One question accepts plain text in the control reply. Multiple questions accept a JSON object keyed by the exact question text. Missing answers leave the request pending and emit a notice.

Unknown native control-request types are rejected explicitly. Active-turn steering is not supported by this adapter; send another turn after completion, or stop the current turn before replacing it. Changing model, effort, native agent, or permission mode applies on the next turn through its launch configuration, rather than through a resident session's live mutation API.

## Adapter options

Unknown option keys fail validation. These values belong to the configured harness's `options` object:

| Option | Type | Native mapping |
| --- | --- | --- |
| `allowedTools` | String array | `--allowedTools` |
| `disallowedTools` | String array | `--disallowedTools` |
| `additionalDirectories` | String array | Repeated `--add-dir` |
| `settingSources` | Array containing `user`, `project`, and/or `local` | `--setting-sources`; omission keeps native behavior |
| `appendSystemPrompt` | String | `--append-system-prompt` |
| `settings` | JSON object | `--settings` |
| `mcpConfig` | Native JSON object, normally containing `mcpServers` | `--mcp-config`; native discovery remains enabled |
| `maxTurns` | Nonnegative integer | `--max-turns` when positive |
| `maxBudgetUSD` | Nonnegative number | `--max-budget-usd` when positive |
| `startupTimeoutMs` | Nonnegative integer | Initialization/catalog deadline; zero uses 30 seconds |

Settings and MCP objects are native Claude configuration, not a portable schema. Do not place resolved credential values in shared project templates.

## Native session discovery

Discovery reads top-level session JSONL files beneath `$CLAUDE_CONFIG_DIR/projects`, or `~/.claude/projects` when unset. Configured harness environment overrides affect this location. It ignores subagent directories, reads at most the first and last 256 KiB of a transcript, and inspects at most the newest 500 session candidates. Results can therefore omit very old conversations on a host with extensive history. Working-directory filtering uses the native recorded absolute cwd.

The resulting handle contains the native UUID and cwd. Atlas never edits the native transcript. Import/resume delegates to `claude --resume`; a missing or invalid native session is an error, not a silently created replacement.

Discovery is not a full transcript-history API. Sessions with unreadable, unsupported, or incomplete native metadata are skipped. Native transcript layouts can change with Claude versions.

## Verification

Ordinary adapter tests do not send model requests:

```bash
go test ./internal/adapters/claude
```

The opt-in native tests use isolated temporary projects and the developer's existing authentication:

```bash
ATLAS_TEST_CLAUDE=1 go test -v ./internal/adapters/claude -run '^TestRealClaude' -count=1 -timeout=4m
```

Verified scenarios include native catalog discovery, project `CLAUDE.md` loading, incremental text output, early persistence, native session enumeration, a second turn remembering a nonce from the first turn, Bash approval and acknowledgement, and cancellation with subprocess cleanup. Unit tests cover invalid options/handles, duplicate text suppression, native error results, question-answer translation, and cwd filtering.

The native tests make small real model requests. Temporary project files are removed by the test framework; the native Claude history remains in the user's normal Claude session store.

The full CLI/daemon subprocess test has a separate opt-in:

```bash
ATLAS_CLAUDE_E2E=1 go test -v ./tests -run '^TestClaudeCLI$' -count=1 -timeout=5m
```

It verifies `init --template researcher`, models, background run/wait/logs, a real project skill, a local deterministic MCP tool, CLI permission responses, daemon restart with stable IDs, continued native conversation, discovery/import deduplication, and stop while awaiting a permission. It builds its own temporary Atlas binary and uses an isolated `ATLAS_HOME` while preserving native Claude authentication. The MCP fixture is a local test subprocess and does not contact an external API; the Claude agent still makes real model requests.
