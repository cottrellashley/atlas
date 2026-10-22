# Configuration and templates

Atlas configures the native harness you work in. `atlas new` opens a session in
the current directory; `atlas init researcher` first installs a reusable setup
and then opens the harness. `atlas session list` discovers native conversation
history and opens a searchable browser. None of these needs an Atlas-managed
session record.

For automated configuration, use `--dry-run --json` to inspect a plan and
`atlas init ... --no-open` to set up files without launching another interactive
harness. See [the agent workflow](agent-workflow.md) for configuration commands.

## Host configuration

Atlas reads `$ATLAS_HOME/config.json`, defaulting to `~/.atlas/config.json`. `ATLAS_HOME` must be a private directory (`0700`). The default harness IDs are `opencode`, `pi`, `omp` (Oh-my-pi), and `claude`; explicit entries override them or add named instances.

```json
{
  "schemaVersion": 1,
  "harnesses": {
    "claude-work": {
      "adapter": "claude",
      "command": ["claude"]
    },
    "pi-research": {
      "adapter": "pi",
      "command": ["pi"],
      "env": {"PI_CODING_AGENT_DIR": "/home/you/.pi-work/agent"},
      "options": {"sessionDir": "/home/you/.pi-work/sessions"}
    },
    "opencode-work": {
      "adapter": "opencode",
      "command": ["/opt/company/bin/opencode"]
    }
  }
}
```

Commands are argument arrays, executed without a shell. Atlas uses the selected
entry's command and environment when opening its native interface. `env`
overrides the inherited launch environment; keep credentials in the harness's
own authentication store or private environment. Do not commit credentials to
project templates.

Use a configured ID with `--harness`, for example
`atlas new --harness claude-work`. An initialized project records its selected
harness; outside a project, interactive startup lets you choose one. Automation
should specify `--harness` when the project does not already select one.

Native settings retain their own managed/user/project precedence. Atlas does
not override company-enforced restrictions. Adapter-specific options reject
unknown keys; consult the individual adapter documents. `atlas config show`
redacts environment values and omits native options; `atlas config path` prints
the file to edit.

## Project metadata and agent setups

`.atlas/project.json` records the configured harness, template version, file
hashes, and optional role defaults. This is configuration metadata, not a
conversation database. The native harness owns session IDs and transcripts.
Role defaults apply to new sessions only; resumed sessions keep their native
settings. Unsupported interactive overrides, such as OpenCode model/role flags
or Pi agent/permission modes, return an explicit error. Use native configuration
or the harness UI for settings that its interactive CLI cannot accept.

`atlas agent list` lists reusable project templates: the built-in researcher and
local templates under `$ATLAS_HOME/templates/`. Add `--catalog NAME` to include
published project-template packages. These setups can supply instructions,
skills, MCP servers, and provider-specific agents. They do not need an Atlas
session wrapper to work.

```bash
atlas agent list
atlas agent list --catalog company
atlas init researcher --harness claude
atlas init ./my-template --harness opencode --no-open
atlas init @research/company-project@1.0.0 --catalog company --harness pi
```

Interactive initialization opens the chosen harness after successful setup.
`--no-open` installs only. `--json`, `--dry-run`, and nonterminal initialization
also suppress launching. `--json` alone still applies the configuration; pair
it with `--dry-run` for a read-only plan. The `--template NAME` form remains
supported; use either it or the positional template, not both.

## Custom templates

Pass a directory to `atlas init`, or install a directory under `$ATLAS_HOME/templates/NAME/`. The built-in `researcher` needs no download. A custom manifest follows this format:

```json
{
  "schemaVersion": 1,
  "id": "company-researcher",
  "version": "1.0.0",
  "instructions": "common/AGENTS.md",
  "skills": ["skills/research"],
  "mcp": "mcp/servers.json",
  "requires": ["skills", "mcp.stdio"],
  "agents": {"researcher": {}, "reviewer": {}},
  "harnesses": {
    "opencode": {"overlay": "overlays/opencode"},
    "claude": {"overlay": "overlays/claude"},
    "pi": {"overlay": "overlays/pi"}
  }
}
```

Omit optional overlay entries when they are unnecessary. Files in an overlay are copied relative to the project root; they cannot write `.atlas` metadata, traverse parent directories, or follow symlinks. Skills are directories containing `SKILL.md`; supporting files are copied too. Portable required capabilities are `skills`, `mcp.stdio`, and `mcp.http`. Runtime resource loading still depends on the tested harness version and native trust.

| Resource | OpenCode | Claude Code | Pi | Oh-my-pi |
| --- | --- | --- | --- | --- |
| Instructions | `AGENTS.md` | `CLAUDE.md` imports `AGENTS.md` | `AGENTS.md` | `AGENTS.md` |
| Skills | `.opencode/skills/` | `.claude/skills/` | `.pi/skills/` | `.omp/skills/` |
| Native configuration | `opencode.json` | `.claude/settings.json` | `.pi/settings.json` | `.omp/config.yml` |
| MCP | `mcp` in `opencode.json` | `.mcp.json` | `.pi/mcp.json` | `.omp/mcp.json` |

## Portable MCP definitions

The template's MCP file is a map of server names to typed server definitions:

```json
{
  "company-search": {
    "transport": "stdio",
    "command": "/opt/company/bin/search-mcp",
    "args": ["--workspace", "."],
    "env": {"SEARCH_TOKEN": "${SEARCH_TOKEN}"}
  },
  "company-docs": {
    "transport": "http",
    "url": "https://docs.example.com/mcp",
    "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}
  }
}
```

Atlas emits native schemas rather than copying one JSON file everywhere. Portable `${NAME}` environment references are retained for Claude/Pi/Oh-my-pi and translated to OpenCode's `{env:NAME}` syntax. Values are not resolved into the template or metadata. Stdio and streamable HTTP are supported; legacy SSE is rejected. Install the referenced server executable separately. Native MCP trust/authentication is still required.

The built-in researcher deliberately has an empty MCP map; no external service, package download, or credential is assumed. Native overlay files can express harness-specific MCP settings beyond the portable subset.

## Safe reinitialization

`atlas init researcher --harness claude --dry-run --json` shows a deterministic file plan without launching a harness. Repeating initialization with unchanged inputs makes no changes. Existing native JSON object keys are retained; incompatible values are reported as conflicts. Existing instruction or skill files are updated only when they still match the previous generated hash. A different harness/template on an initialized project is rejected; use another directory for that migration.

Atlas will not shadow an existing `opencode.jsonc` with a new `opencode.json` or strip its comments. It reports the conflict before writing. Merge that configuration explicitly. JSON configuration overlays are strict JSON. Oh-my-pi initialization leaves existing YAML settings untouched. For field-level YAML configuration, use a `harness-config` package with JSON source values: Atlas merges them into `.omp/config.yml`, preserves unrelated values and comments, and detects edits to owned keys. YAML aliases, anchors, merge keys, duplicate keys, custom tags, and multi-document files are rejected before writes.

Oh-my-pi has a separate adapter and session store; Pi handles and package extensions are not interchangeable with it. Named Oh-my-pi profiles are supported for native session discovery and launch. Installing user-scope packages with custom native directories or profiles is rejected; use project scope or the native installer. See [Oh-my-pi](omp.md) for paths and [enterprise configuration](enterprise.md) for Claude's native policy boundaries.

All conflicts are checked before writes. Changed files use temporary-file, fsync, and rename; an `.atlas/init-recovery.json` journal stores prior bytes for rollback after an interrupted installation. A caught write error rolls back applied files. If the process is killed, inspect this journal and restore its `Data` (base64) for entries with `Existed: true`, or remove newly created entries with `Existed: false`; remove the journal and stale `.atlas/init.lock` only after confirming no initializer is running. Multi-file initialization is not a single filesystem transaction.

Native configuration references: [OpenCode](https://opencode.ai/docs/config/), [Claude MCP](https://code.claude.com/docs/en/mcp), [Pi MCP](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md).
