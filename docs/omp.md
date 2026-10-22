# Oh My Pi

Atlas supports [Oh My Pi (`omp`)](https://github.com/can1357/oh-my-pi) as a
separate harness from Pi. Start a native conversation, install a reusable setup,
or browse native history:

```bash
atlas new --harness omp
atlas init researcher --harness omp
atlas session list --harness omp
atlas resume NATIVE_SESSION_ID --harness omp
```

Initialization installs project instructions and skills before opening OMP.
Use `--no-open` for agent-driven setup. Native OMP owns conversation IDs,
transcripts, tools, authentication, and inference. These commands create no
Atlas session database or daemon. The legacy managed `Run` interface is not
implemented for OMP; its RPC protocol differs from Pi's.

## Native configuration and resources

| Resource | Project | Default user scope |
| --- | --- | --- |
| Instructions | `AGENTS.md` or `.omp/AGENTS.md` | `~/.omp/agent/AGENTS.md` |
| Skills | `.omp/skills/NAME/SKILL.md` | `~/.omp/agent/skills/NAME/SKILL.md` |
| Settings | `.omp/config.yml` | `~/.omp/agent/config.yml` |
| MCP | `.omp/mcp.json` | `~/.omp/agent/mcp.json` |
| Task subagents | `.omp/agents/NAME.md` | `~/.omp/agent/agents/NAME.md` |

Atlas's built-in researcher uses root `AGENTS.md`, `.omp/skills/research`, and
native `.omp/mcp.json`. It leaves existing YAML settings untouched. Config
packages can contribute individual YAML settings through Atlas's ownership
tracking. Native plugin-package installation is not claimed compatible with
Pi extensions; use OMP's plugin workflow for that feature.

For settings packages, supply a JSON object through the manifest's `config.omp`
entry, or an explicit canonical `config.yml` file target. Atlas merges owned
fields into native YAML. In user scope, it preserves an existing `config.yaml`
fallback instead of creating a higher-priority `config.yml`. Project settings
use `config.yml` only. Conflicting user YAML files or unmigrated legacy settings
require native OMP migration or consolidation before installing settings;
unrelated skills and MCP packages remain available. Package file targets cannot
write alternate or legacy main settings files directly.

OMP supports stdio and HTTP MCP directly. Portable Atlas MCP packages render to
the native `mcpServers` mapping. OMP expands `${VAR}` and `${VAR:-default}` in
native configuration, including headers and process environment values. Keep
credentials in native authentication or private environment variables.

```json
{
  "mcpServers": {
    "research": {
      "type": "http",
      "url": "https://mcp.example.com/research",
      "headers": {"Authorization": "Bearer ${RESEARCH_TOKEN}"}
    }
  }
}
```

OMP permission modes accepted by Atlas are `always-ask`, `always-ask-cached`,
`write`, and `yolo`; these map to `--approval-mode`. Thinking levels map to
`--thinking`: `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, or
`auto`. Native model selectors remain opaque and pass through `--model` for
new sessions. Resuming retains the saved native settings. OMP task subagents
are available inside OMP; there is no supported top-level `nativeAgent`
selector.

## Profiles and session stores

Named harnesses can select isolated OMP profiles:

```json
{
  "schemaVersion": 1,
  "harnesses": {
    "omp-work": {
      "adapter": "omp",
      "command": ["omp", "--profile", "work"]
    }
  }
}
```

Profile selection follows OMP's command, `OMP_PROFILE`, and legacy `PI_PROFILE`
precedence. An explicitly empty `OMP_PROFILE` selects the default profile.
Adapter options support `profile` and `sessionDir`; unknown options fail.
Conflicting command and option values fail instead of selecting two stores.

Default history is under `~/.omp/agent/sessions`. Profiles use
`~/.omp/profiles/NAME/agent/sessions`. `PI_CODING_AGENT_DIR` overrides the
default profile's agent directory; named profiles ignore that override.
`PI_CONFIG_DIR` changes the home-relative `.omp` root. On Linux and macOS,
OMP uses `$XDG_DATA_HOME/omp/sessions` when its app directory exists, or the
corresponding `omp/profiles/NAME/sessions` when that profile directory exists.
A nondefault agent-directory override disables this XDG relocation.

Explicit `--session-dir`, the adapter's `sessionDir` option, or
`PI_CODING_AGENT_SESSION_DIR` selects a flat native session directory. Global
discovery reads the selected profile's native project buckets; it does not
mix other profiles or recursively list task-agent transcripts. Exact native
ID lookup is independent of the recent-history display limit. Workspace
filtering happens before the result limit.

For a new conversation, Atlas passes OMP's native project session directory
explicitly, preventing the user's optional `autoResume` setting from reopening
an existing conversation. Resume validates the native file's ID and working
directory, then passes its absolute path to `--resume`. Atlas never rewrites
the transcript. Metadata reads are bounded to head/tail windows; very large
records are skipped. Current title slots, legacy session headers, and title
audit entries are supported.

Project resource installation works with these native profiles. Atlas's
user-scope package workflow explicitly rejects custom profile/config-directory
overrides rather than writing into the wrong user directory.

## Verification and upstream contract

The adapter targets the installed OMP **18.12.6** format. Its source contract is
pinned to [the v18.12.6 session format](https://github.com/can1357/oh-my-pi/blob/v18.12.6/docs/session.md),
[native directory resolver](https://github.com/can1357/oh-my-pi/blob/v18.12.6/packages/utils/src/dirs.ts),
[session startup behavior](https://github.com/can1357/oh-my-pi/blob/v18.12.6/packages/coding-agent/src/main.ts),
and [MCP configuration](https://github.com/can1357/oh-my-pi/blob/v18.12.6/docs/mcp-config.md).

Unit tests cover native metadata, oversized records, more than 500 histories,
profile and XDG precedence, exact lookup, argv construction, and identity
validation. An opt-in installed-harness gate initializes an Atlas template in
an isolated home, checks native instructions and skill discovery, observes
stdio and authenticated local HTTP MCP handshakes, and verifies fresh and
resumed native session identities through OMP's inspection RPC. It submits no
model prompts and provides no evidence of genuine model inference or Windows
execution.

A compiled Atlas terminal smoke check also opened OMP for a new session and
resumed an existing fixture session, with clean native exits and no Atlas
session or daemon state. It disabled only disposable first-run onboarding,
splash, and update checks; native `autoResume` remained enabled. No model prompt
was submitted.

```bash
go test -race ./internal/adapters/omp
ATLAS_TEST_OMP=1 ATLAS_OMP_BINARY=/absolute/path/to/installed/omp \
  go test -race ./internal/adapters/omp -run TestInstalledOMP -v
```

Use the actual native executable for the installed test. Shell wrappers that
automatically update or select global tool-manager versions are rejected.
