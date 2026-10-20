# Configuration and templates

## Host configuration

Atlas reads `$ATLAS_HOME/config.json`, defaulting to `~/.atlas/config.json`. `ATLAS_HOME` must be a private directory (`0700`). The default harness IDs are `opencode`, `pi`, and `claude`; explicit entries override them or add named instances.

```json
{
  "schemaVersion": 1,
  "harnesses": {
    "claude-work": {
      "adapter": "claude",
      "command": ["claude"],
      "options": {"maxBudgetUSD": 2}
    },
    "pi-research": {
      "adapter": "pi",
      "command": ["pi"],
      "env": {"PI_CODING_AGENT_DIR": "/home/you/.pi-work/agent"},
      "options": {"sessionDir": "/home/you/.pi-work/sessions"}
    },
    "opencode-work": {
      "adapter": "opencode",
      "command": ["/opt/company/bin/opencode"],
      "options": {"startupTimeoutSeconds": 30}
    }
  }
}
```

Commands are argument arrays, executed without a shell. The adapter appends protocol arguments. `env` overrides the inherited launch environment; keep credentials in the harness's own authentication store or private environment. Do not commit credentials to project templates. Host configuration is read for each new turn. The daemon inherits its environment when started: restart it if you change shell environment variables that it needs.

`model` on a configured harness sets its initial default. Project agent settings override that default, and explicit CLI settings override project defaults. Without an Atlas override, the harness chooses its native model. Catalog ordering does not choose a model. A resumed native session keeps its own selection unless an explicit managed override is present. `atlas configure ID --model ''` clears the managed override for subsequent turns.

Native settings retain their own managed/user/project precedence. Atlas does not override company-enforced restrictions. Adapter-specific options reject unknown keys; consult the individual adapter documents. `atlas config show` redacts environment values and omits native options; `atlas config path` prints the file to edit.

## Project metadata and roles

`.atlas/project.json` records the configured harness, template version, file hashes, default Atlas agent, and role defaults. An Atlas agent is a reusable launch policy. It is separate from a native OpenCode agent or Claude subagent.

```json
{
  "agents": {
    "researcher": {},
    "reviewer": {"model": "<native-model-id>", "thinking": "<supported-level>"}
  },
  "defaultAgent": "researcher"
}
```

Edit those fields in the generated metadata rather than replacing the whole file. Role settings are `model`, `thinking`, `nativeAgent`, and `permissionMode`. Native agent selection uses `--native-agent`; tool policy uses `--permission-mode`. Supported values differ between harnesses and are validated by their adapters.

## Custom templates

Pass a directory to `--template`, or install a directory under `$ATLAS_HOME/templates/NAME/`. The built-in `researcher` needs no download. A custom manifest follows this format:

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

| Resource | OpenCode | Claude Code | Pi |
| --- | --- | --- | --- |
| Instructions | `AGENTS.md` | `CLAUDE.md` imports `AGENTS.md` | `AGENTS.md` |
| Skills | `.opencode/skills/` | `.claude/skills/` | `.pi/skills/` |
| Native configuration | `opencode.json` | `.claude/settings.json` | `.pi/settings.json` |
| MCP | `mcp` in `opencode.json` | `.mcp.json` | `.pi/mcp.json` |

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

Atlas emits native schemas rather than copying one JSON file everywhere. Portable `${NAME}` environment references are retained for Claude/Pi and translated to OpenCode's `{env:NAME}` syntax. Values are not resolved into the template or metadata. Stdio and streamable HTTP are supported; legacy SSE is rejected. Install the referenced server executable separately. Native MCP trust/authentication is still required.

The built-in researcher deliberately has an empty MCP map; no external service, package download, or credential is assumed. Native overlay files can express harness-specific MCP settings beyond the portable subset.

## Safe reinitialization

`atlas init --dry-run --json` shows a deterministic file plan. Repeating initialization with unchanged inputs makes no changes. Existing native JSON object keys are retained; incompatible values are reported as conflicts. Existing instruction or skill files are updated only when they still match the previous generated hash. A different harness/template on an initialized project is rejected; use another directory for that migration.

Atlas will not shadow an existing `opencode.jsonc` with a new `opencode.json` or strip its comments. It reports the conflict before writing. Merge that configuration explicitly. Native overlays/configuration are strict JSON in this version.

All conflicts are checked before writes. Changed files use temporary-file, fsync, and rename; an `.atlas/init-recovery.json` journal stores prior bytes for rollback after an interrupted installation. A caught write error rolls back applied files. If the process is killed, inspect this journal and restore its `Data` (base64) for entries with `Existed: true`, or remove newly created entries with `Existed: false`; remove the journal and stale `.atlas/init.lock` only after confirming no initializer is running. Multi-file initialization is not a single filesystem transaction.

Native configuration references: [OpenCode](https://opencode.ai/docs/config/), [Claude MCP](https://code.claude.com/docs/en/mcp), [Pi MCP](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/mcp.md).
