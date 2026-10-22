# Atlas

Atlas sets up your **Claude Code, OpenCode, Pi, and Oh-my-pi** configuration and gets you
into a native session. Work in the harness you already use; Atlas manages the
instructions, skills, MCP servers, and shared packages around it.

## Start or continue

Run Atlas inside Linux or WSL with your native harnesses installed and authenticated:

```bash
atlas
```

Choose **New session** or **Continue session**. Start in the current directory,
or browse your existing native sessions grouped by harness. Type to search,
use the arrow keys to select a session, and press Enter to open it.

The same actions have direct commands:

```bash
atlas new                        # Start a native session here
atlas new --harness claude        # Choose Claude Code explicitly
atlas session list               # Find and continue a native session
atlas resume                     # Open the session browser
```

No template or Atlas project is required to start. Session history belongs to
Claude Code, OpenCode, Pi, or Oh-my-pi: Atlas discovers it directly, including sessions
started outside Atlas. Browsing and resuming require no import or Atlas-managed
session records.

To resume a known native session or narrow the list:

```bash
atlas resume NATIVE_SESSION_ID --harness opencode
atlas session list --harness claude --cwd /path/to/project
atlas session list --plain        # Print the list without opening the browser
atlas session list --json         # Machine-readable groups
```

Use `--harness` to disambiguate native IDs shared by different harnesses.
`atlas sessions` is an alias for `atlas session`. Without a terminal, session
listing prints the results. Bare `atlas tui` opens the same menu as `atlas`.

Browsing loads the latest 100 sessions per harness; use `--limit 500` to load
more. Typing or `--search TEXT` searches that loaded window. Resuming an exact
native ID looks beyond the browse limit. Pi's all-project view reads its global
session store; add `--cwd DIR` to find a project's custom private session store.

Migrating from Atlas 0.2: managed `run`/`send`, background-session controls,
logs, and import/archive commands are retired. Use `atlas new` and `atlas resume`;
existing Atlas records are preserved.

## Start with an agent setup

Find a reusable setup, then initialize it in your project directory:

```bash
mkdir my-research && cd my-research
atlas agent list
atlas init researcher --harness claude
```

Atlas writes the research instructions and native resources, then opens Claude
Code. Choose `opencode`, `pi`, or `omp` for the other harnesses. In a terminal, omit
`--harness` to choose interactively when the project has no harness selected.
The built-in `researcher` template needs no download.

`atlas agent list` includes built-in and local templates. Add
`--catalog company` to include project templates from a configured GitHub
catalog. Initialize a local directory or a published template the same way:

```bash
atlas init ./my-template --harness opencode
atlas init @research/company-project@1.0.0 --catalog company --harness claude
```

Use `--no-open` for setup only. `--json`, `--dry-run`, and nonterminal initialization
also suppress launching the harness. The existing `--template NAME` spelling
remains supported.

## Let your agent manage configuration

Ask your agent to add a skill, connect an MCP server, or install your team's
setup. Atlas keeps these operations available as ordinary CLI commands:

```bash
atlas skill add /path/to/skills/evidence
atlas mcp add company_docs --url https://docs.example.com/mcp \
  --header 'Authorization=Bearer ${COMPANY_DOCS_TOKEN}'
atlas package status
```

Configuration changes preserve unrelated settings and detect edits to resources
Atlas owns. Package installs support previews, exact versions, lockfiles, and
recovery. Native trust, authentication, and tool approvals stay with the harness.

For shared setups, register your team's GitHub catalog:

```bash
atlas catalog add company platform/agent-packages --default
atlas agent list --catalog company
atlas package search research
atlas package add @research/researcher@1.4.0 --dry-run
atlas package add @research/researcher@1.4.0
atlas package sync --locked
```

Use `--harness ID` for configuration commands outside an initialized project.
GitHub Enterprise is supported with `catalog add --host HOST`.
See [sharing packages](docs/packages.md) for catalog access, publishing,
versions, and updates, and [configuration](docs/configuration.md) for custom
harnesses and templates.

Native arguments can be passed directly through `exec`:

```bash
atlas exec --harness opencode -- auth login
atlas exec --harness claude -- --resume
```

Running `claude`, `opencode`, `pi`, or `omp` directly in the project loads the same
installed resources.

## Build and test

Go 1.25 or newer is required. Native harness executables are installed separately.

```bash
go build -trimpath -o bin/atlas ./cmd/atlas
go test ./...
go vet ./...
install -m 755 bin/atlas ~/.local/bin/atlas
```

Native integration tests are opt-in. See [verification](docs/verification.md)
for the commands, tested harness versions, and which checks use authenticated
model inference. Native Windows execution is not supported; use WSL.

## Documentation

- [Agent workflow](docs/agent-workflow.md)
- [Configuration and templates](docs/configuration.md)
- [Sharing and publishing packages through GitHub](docs/packages.md)
- [Native Claude, Pi, and OpenCode packages](docs/native-packages.md)
- [Oh-my-pi configuration and native sessions](docs/omp.md)
- [Claude Code enterprise configuration and checks](docs/enterprise.md)
- [Architecture and extension boundaries](docs/architecture.md)
- [Verification and supported versions](docs/verification.md)
- [Claude adapter](docs/claude.md), [OpenCode adapter](docs/opencode.md), [Pi adapter](docs/pi.md)
- [Example company template](examples/templates/company-researcher/template.json)
- [Example distributable packages](examples/packages/README.md)
