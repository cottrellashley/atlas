# Native packages and direct provider launches

Atlas installs resources in provider-native locations. After installing a package,
start the provider from the project root with `claude`, `pi`, or `opencode`, or
use Atlas's native TUI handoff. There is no Atlas runtime loader and no hidden
environment variable required to see the installed resources.

Keep the native workspace trust decisions: Claude asks whether to trust a
project, and Pi asks whether to load project resources. Atlas does not silently
accept either decision. Restart a running provider after installing or updating,
or use its documented resource reload command.

## Supported versions

Native package preflight requires the versions exercised by the integration
gate: Claude Code 2.1.302+, Pi 1.3.0+, and OpenCode 2.1.0+, within their current
major versions. These floors describe Atlas's tested implementation; they do
not claim the features first appeared in those releases. OpenCode v1 plugin
formats are incompatible with this implementation.

## Claude plugins

An Atlas release contains the original native plugin directory:

```json
{
  "schemaVersion": 1,
  "id": "@research/claude-researcher",
  "version": "1.0.0",
  "kind": "claude-plugin",
  "harnesses": ["claude"],
  "native": {"path": "plugin"}
}
```

```text
plugin/
  .claude-plugin/plugin.json
  skills/research/SKILL.md
  hooks/hooks.json
  .mcp.json
```

Atlas copies this directory to `.claude/skills/<native-plugin-name>/` and owns
the corresponding `enabledPlugins` setting. Claude's native plugin identity is
`<native-plugin-name>@skills-dir`; the original skill namespace is preserved.
The same layout under the user's home provides user scope. If the native
manifest declares a version, it must match the Atlas release version.

This is Claude's documented persistent plugin mechanism, including project
sharing through a repository. Atlas does not edit `installed_plugins.json` or
`known_marketplaces.json`, or maintain a competing native plugin cache.
Before installation, Atlas invokes `claude plugin validate` on the acquired
plugin. [Native plugin creation](https://code.claude.com/docs/en/plugins/create),
[Native loading rules](https://code.claude.com/docs/en/plugins/loading)

Project plugins load only in the exact trusted primary working directory.
Launch from the project root. Claude restricts project-scope background monitors
and bundled `.mcpb`/`.dxt` servers; Atlas rejects those manifest declarations at
project scope rather than report a fully working install. Use user scope for
those components. Project MCP servers retain Claude's per-server approval.

Use Atlas dependencies to compose releases. Claude marketplace dependency
declarations are rejected in this installation mode because their marketplace
resolution and lifecycle are different. Existing private marketplaces remain
usable through `claude plugin marketplace add` and `claude plugin install`;
Atlas does not take ownership of resources installed outside its lifecycle.
To distribute an existing plugin through Atlas, package its native directory in
an Atlas Git release. `native.source` and `native.marketplace` are deliberately
rejected for Claude until a verified marketplace import lifecycle exists.

For plugins with `userConfig`, run the provider's `claude plugin configure`
workflow. Secrets belong in the native credential/configuration mechanism,
not the shared Atlas manifest.

## Pi packages

Use the same envelope with `kind: "pi-package"`, `harnesses: ["pi"]`, and
`native.path` selecting a native Pi package directory. It can contain
`extensions`, `skills`, `prompts`, and `themes`, either conventionally or via
the `pi` property in its `package.json`.

Atlas writes the payload under `.pi/atlas-packages/<stable-package-identity>/`
and adds a relative entry to `.pi/settings.json`'s `packages` array. User scope
uses `.pi/agent/`. Relative package paths resolve from the containing settings
file, so cloning a project does not retain another user's absolute path.

Alternatively, let Pi install a pinned package source:

```json
"native": {
  "source": "npm:@company/pi-researcher@1.4.0",
  "options": {"skills": [], "extensions": ["extensions/*.js"]}
}
```

Exact npm versions and full Git commits are accepted. For Git, use
`git:github.company.example/team/pi-researcher@<full-commit-sha>`.
Branches, mutable tags, and npm version ranges are rejected.

Pi's native runtime installs configured remote packages after project trust.
Local packages are loaded in place without dependency installation. Pi's
resource filters (`extensions`, `skills`, `prompts`, `themes`, and `autoload`)
are preserved and unknown options are rejected. [Pi package documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/packages.md)

## OpenCode 2 plugins

For `kind: "opencode-plugin"`, a local `native.path` directory must contain a
native `server.js`, `index.js`, or `tui.js` entrypoint. Corresponding TypeScript
sources can also be used where supported by the provider loader. Atlas copies
the directory under `.opencode/plugins/<stable-package-identity>/`. Both native
runtimes discover this directory, so a project can share server and TUI plugins
together. User scope uses `.config/opencode/plugins/`.

`native.target` can declare `server`, `tui`, or `both`. For local payloads this
describes the entrypoints the directory contains; it does not disable one of
the native runtime's discovered entrypoints.

A pinned remote server package looks like:

```json
"native": {
  "source": "@company/opencode-researcher@1.4.0",
  "target": "server",
  "options": {"reviewMode": "research"}
}
```

Atlas owns one entry in the native `plugins` array in `opencode.json`. Exact npm
versions and Git package sources with full commit hashes are accepted. Remote
TUI sources use `cli.json`, which OpenCode 2 reads at user scope; project TUI
sources must therefore use a local payload. `target: "both"` at user scope
declares the source in both server and CLI configurations. OpenCode fetches
remote packages through its native package manager at startup.

Local server-only plugin options are added through a native configuration
entry pointing at the same discovered directory. Local TUI plugin options are
rejected because there is no equivalent project configuration lifecycle.
[Native directory discovery](https://github.com/anomalyco/opencode/blob/v2.1.0/packages/core/src/plugin/source-directory.ts),
[TUI discovery](https://github.com/anomalyco/opencode/blob/v2.1.0/packages/tui/src/plugin/discovery.ts),
[OpenCode plugin API](https://github.com/anomalyco/opencode/blob/v2.1.0/packages/plugin/src/README.md)

## Dependencies and reproducibility

Prefer self-contained payloads for company releases. Build extensions before
publishing so runtime imports are bundled, or include the required runtime
dependency tree. Atlas rejects a local payload that declares a runtime
dependency but does not include that dependency. It does not run npm lifecycle
scripts or silently create an untracked `node_modules` tree after installation.
Pi host SDK packages belong in `peerDependencies`, as required by Pi.

For `native.source`, Atlas pins the top-level native source. Native package
managers still own download, transitive resolution, authentication, and caches.
An exact top-level version does **not** claim a content hash for the whole
native dependency tree. Use a self-contained payload for reproducibility of
the complete installed content.

Git and npm authentication remains native. Credentials must not be included in
shared manifests. An Atlas GitHub catalog login does not automatically log in
the Pi/OpenCode package managers to private npm or Git hosts.

## Verification

```bash
ATLAS_PACKAGE_NATIVE=1 go test ./internal/assets \
  -run '^TestNativePackageRuntimes$' -v -count=1
```

The gate uses installed native binaries and isolated homes. It verifies:

- Claude refuses to load an untrusted project plugin, then discovers the plugin
  and skill inventory with an isolated already-trusted workspace fixture.
- Pi loads the installed extension, executes its setup code, and exposes its
  registered command through its native RPC command catalog.
- OpenCode loads and executes the installed plugin and reports it active through
  its native server inventory.
- Removing each package through Atlas removes its resources from subsequent
  native discovery.

These checks make no inference requests and do not change real user settings.
They validate native resource loading, not Windows terminal rendering, company
GitHub access, or every third-party plugin's behavior.
