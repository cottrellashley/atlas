# Using Atlas from an agent

Use Atlas to configure the user's native Claude Code, OpenCode, or Pi setup.
The user works in that harness, and its own storage owns their conversations.
Starting or resuming a session does not require an Atlas session record.

## Inspect the setup

```bash
atlas help agent
atlas config show
atlas doctor --json
atlas agent list
atlas package status --json
```

The project records the selected harness in `.atlas/project.json`. Pass
`--harness claude`, `--harness opencode`, `--harness pi`, or a configured alias
when working outside an initialized project. Use `--cwd /path/to/project` to
target a different directory. `config show` redacts environment values; do not
read credential stores to diagnose a configuration issue.

`package status` reports installed packages, lock state, and drift. A nonzero
exit can mean owned files were edited or packages need installation; read the
reported differences before changing anything.

## Initialize without opening another harness

```bash
atlas init researcher --harness claude --dry-run --json
atlas init researcher --harness claude --no-open --json
```

Use `--no-open` whenever setup should return to the current agent. `--json`,
`--dry-run`, and nonterminal initialization also suppress the native launch.
`--json` alone applies changes; `--dry-run --json` previews them. Review the plan
for conflicts, then apply the requested setup without `--dry-run`.

Find a shared project setup in a configured catalog:

```bash
atlas agent list --catalog company
atlas init @research/company-project@1.0.0 --catalog company \
  --harness opencode --no-open --json
```

Templates can also be local directories or names under `$ATLAS_HOME/templates/`.
Use `--package SOURCE` repeatedly to include additional packages in the same
initialization transaction.

## Add skills and MCP servers

A skill directory contains `SKILL.md` with `name` and `description` frontmatter.
Atlas copies its supporting files and preserves executable modes.

```bash
atlas skill add /path/to/skills/evidence --dry-run --json
atlas skill add /path/to/skills/evidence --json
```

Local skill imports are snapshots. Run `skill add` again when the source changes.
Use a published package for versions shared across machines.

Configure an HTTP MCP server with an environment reference for its credential:

```bash
atlas mcp add company_docs --url https://docs.example.com/mcp \
  --header 'Authorization=Bearer ${COMPANY_DOCS_TOKEN}' --dry-run --json
atlas mcp add company_docs --url https://docs.example.com/mcp \
  --header 'Authorization=Bearer ${COMPANY_DOCS_TOKEN}' --json
```

For a stdio server, supply its executable and arguments separately:

```bash
atlas mcp add local_search --command node \
  --arg /opt/company/search-mcp/server.js --arg=--read-only \
  --env 'SEARCH_TOKEN=${COMPANY_SEARCH_TOKEN}' --json
```

Single quotes preserve environment references for native expansion. Atlas
renders each provider's configuration format. The server executable and native
MCP authentication or trust remain part of the harness setup.

Remove a resource through its owning package:

```bash
atlas skill remove evidence --dry-run --json
atlas mcp remove company_docs --dry-run --json
```

Remove `--dry-run` to apply. The skill package ID comes from its frontmatter;
MCP shortcut packages use `mcp-NAME`.

## Install and maintain shared configuration

```bash
atlas package search research --catalog company --json
atlas package add @research/researcher@1.4.0 --catalog company --dry-run --json
atlas package add @research/researcher@1.4.0 --catalog company --json
atlas package sync --locked --json
atlas package update @research/researcher --version 1.5.0 --dry-run --json
atlas package remove @research/researcher --dry-run --json
```

The default scope is the project. Add `--scope user` only when the requested
change belongs in the user's shared harness configuration. Commit
`.atlas/packages.json` and `.atlas/packages.lock.json` for reproducible team
setups. Local directory sources must be published before another machine can
resolve them.

Atlas preserves unrelated configuration and rejects conflicts or edits to
resources it owns. Preserve those edits, update the appropriate source, and
resolve the conflict instead of replacing the whole native configuration.
After an interrupted package installation, `atlas package recover` uses its
journal to restore the transaction; follow with `atlas package status`.

## Hand back to the user

Describe the configuration changed and any native reload or authentication step
still needed. The user can run `atlas new` to start, or `atlas session list` to
search their existing native conversations. `atlas resume NATIVE_ID --harness ID`
opens a known session. Use `atlas session list --json` for machine-readable
native session groups; `--cwd` optionally filters them by project directory.

Session listing loads up to 100 recent sessions per harness by default, with
`--limit 500` as the maximum. `--search TEXT` and typing in the browser filter
that loaded window. `atlas resume NATIVE_ID` performs an exact lookup independent
of the browse limit. Pi's all-project view reads its global session store;
use `--cwd DIR` for a custom session store configured privately in that project.

Atlas 0.2's managed execution, background-session controls, logs, and session
import/archive commands are retired. Use native sessions directly; configuration,
skill, MCP, and package commands remain available.

See [configuration and templates](configuration.md) for file layouts and custom
harness entries, [packages](packages.md) for catalog authentication and
publishing, and [native packages](native-packages.md) for provider formats.
