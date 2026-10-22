# Atlas architecture

## Current native-session workflow

The public CLI now starts and resumes sessions in the native harness. `atlas`
and bare `atlas tui` open the terminal menu; `atlas new` opens the selected
harness; `atlas init` installs configuration and opens the harness when running
interactively. `internal/picker` owns the searchable menu, and `internal/native`
coordinates native history discovery and provider-specific launch arguments.
`atlas session list` reads native histories, and `atlas resume` hands the
terminal to the selected harness in its saved working directory.

These flows do not start an Atlas daemon, import conversations, or create
Atlas-managed session records. Configuration metadata, package locks, ownership,
and recovery remain Atlas responsibilities. The native harness owns inference,
permissions, authentication, and transcript persistence. Project defaults apply
only to new sessions; unsupported interactive overrides are rejected explicitly.

Oh-my-pi has its own adapter (`omp`), native paths, profile selection, and title
records. Fresh launches explicitly select the native session directory to avoid
implicit auto-resume; continuations validate the native handle before passing
`--resume`. It does not use Pi's legacy RPC execution driver. YAML configuration
packages use field ownership over a validated syntax tree, preserving unrelated
settings and comments.

The managed execution sections below describe the earlier **Atlas 0.2**
implementation retained internally. Their references to public `run`, `send`,
`logs`, `wait`, `stop`, `permissions`, `configure`, `daemon`, and session import or
archive commands are historical: those public commands are retired. Existing
Atlas records are left untouched. Package and configuration boundaries remain
in use by the current workflow.

## Current boundaries

```text
cmd/atlas           Process entry and signals; retained hidden daemon entry
internal/cli        Menus, setup, native-session routing, configuration commands
internal/native     Native history discovery, exact ID lookup, launch arguments
internal/picker     Searchable grouped terminal selection and terminal restoration
internal/project    Template plans, native rendering, safe installation
internal/config     Host harness registry and aliases
internal/harness    Common catalogs, opaque native handles, retained protocol types
internal/adapters   Provider discovery and native launch; retained protocol drivers
internal/process    Argv/environment construction and process ownership
internal/daemon     Legacy managed execution, approvals, and socket API
internal/store      Legacy managed records, event logs, and recovery
tests               Compiled CLI fixtures with isolated native history and handoff
```

## Legacy managed execution (Atlas 0.2)

The following lifecycle and storage description applies to retained internal
managed execution. Current public startup and resume use the native workflow
above; they do not use the daemon client, socket, or Atlas session store.

The implementation uses the Go standard library. No daemon service installation is required: the CLI automatically starts its own executable in `__serve` mode. A private Unix socket and a daemon flock provide one owner per `ATLAS_HOME`. The socket is `0600`; the home is required to be `0700`. There is no Atlas TCP listener. OpenCode's adapter starts its own loopback server with a random per-process password.

## Legacy identity and state

The configured harness ID, adapter kind, model ID, Atlas session ID, native persistence handle, and turn ID are separate. Native handles have a version and adapter-owned JSON. Claude and OpenCode use native session IDs; Pi also needs its absolute session file path. Core storage never guesses a model vendor or parses slash-separated model IDs.

`sessions list` reads Atlas-owned metadata. `sessions discover` uses the native adapter. Import accepts a session found by native discovery and deduplicates the same handle/adapter within this Atlas home. Native clients outside Atlas are not controlled by its locking. Multiple independent `ATLAS_HOME` directories are also independent owners; do not import the same conversation into them concurrently.

Each turn obtains one driver runtime. The runtime is closed after completion; the next `send` resumes the durable native conversation. This trades some launch latency for clear process ownership and configuration isolation. The daemon remains resident for background ownership, approvals, logs, and subsequent turns; it does not keep every native harness process idle indefinitely.

Session state is `running`, `waiting_permission`, `idle`, `failed`, `stopped`, or `interrupted`. Archival is a separate boolean. Requested and observed model fields are separate. Configuration updates are accepted only between turns and are validated by the native harness when the next turn starts. They do not claim immediate effective state.

## Retained protocol execution contract

`Driver.Run(ctx, request, emit, controls)` owns one native turn and its processes. Returning nil means native completion, not request acceptance. A persistence event is emitted as soon as native identity is known, including before model success. Failed turns retain their handle. A command handled without model inference is allowed to complete normally.

The manager serializes event writes and assigns sequence numbers. Native tools, reasoning, usage, permission requests/resolutions and configuration are retained in JSON event logs; plain terminal output prints assistant text. `logs --after SEQ --json` supports cursor-based consumers. Logs contain native tool inputs/results and prompts; they are private files, not a redacted telemetry stream.

Permission responses are enqueued, then remain pending until the adapter emits the native resolution. An incomplete question response therefore can be corrected. Each resolution removes only its matching ID. Terminal completion clears pending approvals. A tool denial can terminate a native turn with an error; Atlas reports that outcome instead of inventing a successful answer.

Cancellation waits for the driver's native abort/termination and process reaping before releasing turn ownership. Context cancellation cannot admit a replacement turn until the previous driver has returned. Process launches use argv without a shell and process groups on Linux; Linux children also receive a parent-death kill signal. A forcibly killed daemon cannot guarantee cleanup of arbitrary detached grandchildren created by a native tool, so interrupted work requires inspection before retrying side-effecting prompts.

## Legacy session persistence and recovery

Each session has `record.json` plus `events.jsonl` under `$ATLAS_HOME/sessions/ID/`. Metadata uses fsync+rename; events are appended and synced. At daemon startup, event records newer than metadata restore persistence, model, turn, and terminal state. An incomplete final log write is truncated; corruption in a complete log entry fails loudly. A session left active is marked interrupted, retaining native identity; prompts are never automatically replayed after a crash.

Native harness transcripts remain the source for model conversation continuation. Atlas logs capture activity since management/import and are not a full backfill of pre-import native history. Native histories are never deleted by Atlas archive/unarchive. Event logs are currently scanned from disk for cursor reads; very large histories may need an indexed log store in a later revision.

## Extending a retained protocol adapter

1. Implement `harness.Driver`: live catalog, native discovery, and one owned turn.
2. Validate native options; reject unsupported settings explicitly.
3. Keep native model IDs and persistence opaque to common code.
4. Emit early persistence, observed model, approvals, tool/text activity; keep protocol stdout separate from diagnostic stderr.
5. Prove completion, restart/resume, denial, cancellation and process cleanup against the installed harness.
6. Register the factory and add native template rendering only if that harness requires it.

No generic ACP implementation, plugin loader, graphical UI, cross-harness conversation conversion, or universal live model switch is claimed. The requested three harnesses share a tested CLI workflow, while their native capabilities remain explicit. Additional providers can implement the same contract without duplicating the daemon or template installer.

## Package distribution and native installation

Distribution is independent of inference/session `harness.Driver` adapters:

- `internal/catalog`: Go GitHub SDK client, authenticated catalogs, immutable
  release identities, bounded digest-verified artifact downloads.
- `internal/artifact`: strict package manifests, declared resource snapshots,
  safe archives, content-addressed cache and disposable extraction views.
- `internal/resolve`: per-harness bundles, exact graph locks, aliases, cold sync,
  cycle/version conflict checks and locked dependency validation.
- `internal/assets`: native resource layouts, MCP/config projection, native
  plugin payloads and read-only installed-version preflight.
- `internal/install`: complete desired contributions, ownership of files/JSON
  pointers/array entries, comments-preserving JSONC and YAML edits, shared owners,
  optimistic transactions, durable journal, rollback/recovery and drift checks.

Project declarations and locks are shareable; installation receipts and journal
backups are private. Receipts retain installed version/source/digest provenance,
so changing a lock alone does not cause `status` to report it installed.
`init --package` combines template files and package contributions in one
transaction before any project mutation. No provider cache/database is edited.

Native runtime loading remains native: `atlas new` and selected sessions use
the terminal handoff, and direct provider commands read the same installed files.
Bare `atlas tui` opens the new/continue menu.
See [package distribution](packages.md) and [native payloads](native-packages.md).
