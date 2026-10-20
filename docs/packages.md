# Sharing skills, MCP servers, plugins, and project templates

Atlas uses GitHub repositories and Releases as its package catalog. It talks to
GitHub.com or GitHub Enterprise directly through `google/go-github/v82`; no
Atlas gateway or package server is required. GitHub repository permissions
control who can read or publish releases.

Packages install into the harness's native project or user configuration. A
user can start the same setup with `atlas tui`, `claude`, `pi`, or `opencode`.
Native trust, authentication, and tool permissions remain the provider's own.

## 1. Use resources locally

Initialize a project, then start its native terminal interface:

```bash
mkdir my-research
cd my-research
atlas init --harness pi --template researcher
atlas tui
# Or run the provider directly, from this directory:
pi
```

Choose `claude`, `pi`, or `opencode`, or an Atlas configured harness alias.
`atlas tui` and `atlas exec` are equivalent terminal handoffs. They do not create
an Atlas-managed session. Native arguments follow `--`:

```bash
atlas tui --harness claude -- --resume
atlas exec --harness opencode -- auth login
```

For Atlas-managed execution, use `atlas run`, `atlas send`, and the other
[session commands](../README.md#managed-work). Both execution paths see the
same installed resources.

### Install a skill directory

The directory needs an Agent Skills `SKILL.md` with `name` and `description` in
its YAML frontmatter. Supporting files and executable scripts are preserved.

```bash
atlas skill add /path/to/skills/research --harness pi
atlas package status
```

The shortcut creates a local package snapshot under the Atlas home. Its ID is
the skill's frontmatter `name`. Re-run `skill add` to import a revised directory;
the original source directory is not watched. Use a published package for
cross-user distribution and versioned updates.

### Configure an MCP server

```bash
atlas mcp add company_docs \
  --command node --arg /path/to/docs-mcp/server.js --arg=--read-only \
  --env 'DOCS_TOKEN=${COMPANY_DOCS_TOKEN}'

atlas mcp add company_search \
  --url https://search.example.com/mcp \
  --header 'Authorization=Bearer ${COMPANY_SEARCH_TOKEN}'
```

Supply `--harness` if the directory has not been initialized. Single quotes keep
environment references intact for native expansion instead of expanding them
into the shared configuration. Pi 1.3 supports environment references in MCP
environment values and headers but not in the URL itself.

The shortcut package IDs are `mcp-company_docs` and `mcp-company_search`:

```bash
atlas package remove mcp-company_docs
atlas skill remove research
```

Installing the declaration does not install the MCP server executable, log in
to its API, or approve it on the user's behalf. Start the harness and use its
native MCP connection/authentication interface where required. Share a complete
MCP package when its configuration should be common across users.

## 2. Connect an internal GitHub catalog

One repository can hold many versioned package releases, even when their source
code lives in separate team repositories. Start with one organization catalog
and use namespaced package IDs such as `@research/evidence`.

Register an existing repository:

```bash
atlas catalog add company platform/agent-packages \
  --host github.company.example --default
atlas catalog list
```

Or create a repository and register it in one command:

```bash
atlas catalog create company platform/agent-packages \
  --host github.company.example --default
```

`catalog create` creates a **private** repository unless `--public` is supplied.
For GitHub.com, omit `--host`. `catalog remove NAME` removes the local catalog
configuration; it does not delete the GitHub repository.

Enterprise installations with nonstandard API endpoints can set them explicitly:

```bash
atlas catalog add company platform/agent-packages \
  --host github.company.example \
  --api-url https://github.company.example/api/v3/ \
  --upload-url https://github.company.example/api/uploads/ \
  --token-env COMPANY_GITHUB_TOKEN --default
```

These are GitHub SDK API/upload base URLs, not raw repository or artifact URLs.
They must use HTTPS and must not contain credentials. Settings are saved in
`$ATLAS_HOME/catalogs.json` (default `~/.atlas/catalogs.json`).

### Authentication

Atlas checks credentials in this order:

1. The catalog's explicit `--token-env NAME`, if configured.
2. `GH_TOKEN`, then `GITHUB_TOKEN`, for GitHub.com; `GH_ENTERPRISE_TOKEN`, then
   `GITHUB_ENTERPRISE_TOKEN`, for Enterprise hosts.
3. A token stored for that hostname by `atlas github login`.
4. An existing `gh auth token --hostname HOST` login, if `gh` is installed.
5. Anonymous access, which can read public catalogs.

Environment credentials need no `gh` executable. All repository, release,
upload, search, and download operations use the Go SDK. `gh` is only an optional
bridge to an existing login.

To persist a credential, pass it through standard input rather than an argument:

```bash
atlas github login --host github.company.example --token-stdin < /path/to/token-file
atlas github status --host github.company.example
```

Stored tokens are plain JSON in `github-credentials.json` under the Atlas home,
with mode `0600`; they are not stored in the project or lockfile. Use environment
tokens or your existing credential workflow when you do not want local token
storage. Readers need repository access; publishers need permission to create
and upload Releases. Company SSO/token authorization still applies.

GitHub catalog credentials do not automatically authenticate native npm or Git
package sources used by a provider. Those use their native credential setup.

## 3. Install and maintain shared packages

```bash
atlas package search research
atlas package add @research/researcher@1.4.0 --harness opencode --dry-run
atlas package add @research/researcher@1.4.0 --harness opencode
atlas package status
atlas tui --harness opencode
```

Use `--catalog company`, the `company::@research/researcher@1.4.0` source form,
or the explicit form
`github:github.company.example/platform/agent-packages#@research/researcher@1.4.0`.
Explicit GitHub sources still use matching local catalog configuration for
custom API endpoints and credential environment names.

Local package directories work without a catalog:

```bash
atlas package add /path/to/package --harness claude
```

All package mutation commands default to project scope. In an initialized
project, Atlas finds the project root from a subdirectory. Without a project
manifest, `--cwd` selects the target directory. `--scope user` targets native
user configuration and records package state under the user's home. User scope
rejects custom native config-directory overrides rather than install somewhere
the provider will not read. Multiple aliases for the same provider cannot own
competing configurations in one scope. Scope is recorded so user installations cannot be reinterpreted as project installations; the configured alias is retained for
subsequent preflight checks.

### Exact versions and updates

```bash
# Keep the versions and digests already recorded in the lockfile.
atlas package sync --locked

# Move an explicitly pinned dependency to another explicit release.
atlas package update @research/researcher --version 1.5.0 --dry-run
atlas package update @research/researcher --version 1.5.0

# Remove a direct dependency and any now-unused transitive dependencies.
atlas package remove @research/researcher
```

An unversioned `package add @research/researcher` selects the newest stable
release and locks that selection. It does not update on every launch or sync.
An explicit `package update` re-resolves unversioned roots to the newest stable
release; a pinned `@1.4.0` root stays pinned unless you supply `--version`.
Prereleases must be selected explicitly. Semver ranges and release channels
are not part of this implementation.

For local directory packages, update reads the current local manifest and
content. `--version` does not rewrite a local manifest. Locked sync can use the
verified cached snapshot even after the original local directory changes.

After editing `.atlas/packages.json` manually, use
`atlas package sync --locked=false` to resolve the changed declaration. Review
the resulting lockfile before committing it.

### A new teammate or clean machine

Commit `.atlas/packages.json` and `.atlas/packages.lock.json`. The teammate
configures the same catalog access and runs:

```bash
atlas package sync --locked
atlas package status
atlas tui --harness opencode
```

The lock stores full GitHub catalog identity, exact releases, and archive
SHA-256 digests. A fresh user downloads the same release and verifies its digest
before extraction. Local `file:` package sources are development conveniences;
publish them to GitHub before expecting another machine to resolve them.

Atlas does not start a provider during ordinary sync, except for read-only
native package version/Claude manifest preflight. Providers install or load
native package sources and resolve MCP connections when they start.

## 4. Package format

The root file is `atlas-package.json`. There are eight resource kinds:

| `kind` | Required resource | Purpose |
| --- | --- | --- |
| `skill` | `skills` | One or more Agent Skills directories |
| `mcp` | `mcp` | Portable MCP server declarations |
| `harness-config` | `config` and/or `files` | Native settings and supporting resources |
| `claude-plugin` | `native` | A Claude plugin directory |
| `pi-package` | `native` | A Pi package directory or pinned native source |
| `opencode-plugin` | `native` | An OpenCode 2 plugin directory or pinned native source |
| `project-template` | `template` | Initial project scaffolding |
| `bundle` | Usually `dependencies` | A collection selected for a harness |

All manifests use `schemaVersion: 1`, a lowercase ID, and a semantic version
such as `1.4.0`. Namespaced IDs are recommended. Paths are relative to the
package root. `harnesses` is optional; use it to limit a release to compatible
providers. The native kinds additionally require the matching provider.

### Skill package

```json
{
  "schemaVersion": 1,
  "id": "@research/evidence",
  "version": "1.0.0",
  "kind": "skill",
  "description": "Gather evidence and preserve source attribution",
  "skills": ["skills/evidence"]
}
```

```text
atlas-package.json
skills/evidence/SKILL.md
skills/evidence/references/research-policy.md
skills/evidence/scripts/check-sources.sh
```

The frontmatter name must match the skill directory. Atlas preserves the whole
skill; a skill that depends on a provider-specific tool still needs compatible
instructions for that provider.

### MCP package

Set `"kind": "mcp"` and `"mcp": "servers.json"`. The file is a server-name map:

```json
{
  "company-docs": {
    "transport": "http",
    "url": "https://docs.example.com/mcp",
    "headers": {"Authorization": "Bearer ${COMPANY_DOCS_TOKEN}"}
  },
  "local-index": {
    "transport": "stdio",
    "command": "node",
    "args": ["/opt/company/index-mcp/server.js"],
    "env": {"INDEX_MODE": "readonly"}
  }
}
```

Atlas renders Claude `.mcp.json`, Pi `.pi/mcp.json`, or OpenCode's native `mcp`
settings. It translates environment references for OpenCode. Native-only
additional server fields can go under a server's `native` map keyed by provider;
they cannot replace a portable field silently.

### Harness configuration

```json
{
  "schemaVersion": 1,
  "id": "@research/profile",
  "version": "1.0.0",
  "kind": "harness-config",
  "config": {
    "claude": "settings/claude.json",
    "pi": "settings/pi.json",
    "opencode": "settings/opencode.json"
  },
  "files": {
    "claude": {".claude/agents/reviewer.md": "agents/claude-reviewer.md"},
    "opencode": {".opencode/agents/reviewer.md": "agents/opencode-reviewer.md"}
  }
}
```

`config` selects JSON objects to merge into the provider's settings; each leaf
key is owned independently. `files` maps native destination paths to packaged
source files or trees. Destinations are restricted to the selected harness's
resource paths and supported project instruction files. Explicit destinations
must match the selected scope; use `config` for settings that should follow
the automatic project/user layout. A `common` entry applies before the
provider-specific entry; conflicting ownership is rejected.

### Bundle with provider-specific dependencies

```json
{
  "schemaVersion": 1,
  "id": "@research/researcher",
  "version": "1.4.0",
  "kind": "bundle",
  "dependencies": [
    {"source": "@research/evidence@1.0.0"},
    {"source": "@research/docs-mcp@1.2.0"},
    {"source": "@research/claude-tools@1.4.0", "harnesses": ["claude"]},
    {"source": "@research/pi-tools@1.4.0", "harnesses": ["pi"]},
    {"source": "@research/opencode-tools@1.4.0", "harnesses": ["opencode"]}
  ]
}
```

Published dependencies require exact versions. Unqualified dependencies inherit
their parent's GitHub catalog, preventing accidental resolution against a
different default catalog. Local development bundles may use local paths;
those must be replaced before publication. Cycles, incompatible harnesses, and
conflicting versions are errors. Shared dependencies remain installed until
the last direct dependency needing them is removed.

### Project templates

A `project-template` package points `template` at a directory containing the
existing Atlas `template.json` format. Its dependencies can install skills,
plugins, or configuration alongside initial scaffolding:

```bash
atlas init --harness claude \
  --template @research/company-project@1.0.0 \
  --package @research/researcher@1.4.0
```

Template files, all requested package resources, dependency metadata, and the
installation receipt are planned together and applied as one recoverable
transaction. A template/package conflict or conflicting packages fail before
project files change. `--dry-run` previews the combined changes; `--json`
returns one JSON object. Template scaffolding is initial project content:
package update does not rerun the template over the user's project.

See [configuration and templates](configuration.md) for the inner template
format and [native packages](native-packages.md) for Claude/Pi/OpenCode plugin
formats, native version requirements, runtime dependencies, and marketplace
limitations. Claude marketplace dependencies are not silently translated into
Atlas dependencies.

## 5. Publish through GitHub

From the Atlas repository root, the examples can be validated and published:

```bash
atlas package validate ./examples/packages/research-skill --json
atlas package publish ./examples/packages/research-skill --dry-run
atlas package publish ./examples/packages/research-skill --catalog company
```

Publish a bundle's dependencies before publishing and installing the bundle.
The example packages include native plugin payloads for all three harnesses,
an MCP configuration, a reusable skill, a template, and a bundle.

Publication:

1. Validates the manifest and resource rendering.
2. Snapshots only declared resources and conventional README/license files.
3. Creates a draft GitHub Release with manifest and SHA-256 metadata.
4. Uploads `package.tar.gz` through the SDK.
5. Publishes the release only after the upload succeeds.

Atlas refuses to overwrite an existing package/version. A failed upload leaves
an identifiable draft for manual recovery. The archive is deterministic for
the same file content/modes; symlinks and path traversal are rejected. The
release digest and manifest identity are checked again on download and cache
reuse. Locked clients reject changed release bytes rather than silently move
to a new artifact.

`--source-commit SHA` records the reviewed source commit in release metadata;
it is provenance supplied by the publisher, not a signature or proof that the
working tree matches that commit. Company CI should build from a reviewed
checkout, run native validation/tests, and publish using a restricted release
credential. GitHub reviews and repository permissions supply the approval
workflow; Atlas itself does not enforce a separate approval service.

The repository includes a [GitHub Actions publishing workflow](../.github/workflows/publish-package.yml).
It runs from `main`, uses the `package-publish` environment, tests/builds Atlas,
then validates and publishes the selected package directory. Configure required
reviewers for that environment. `ATLAS_CATALOG_REPOSITORY` selects the target
catalog repository; `ATLAS_PUBLISH_TOKEN` can supply a credential with access to
a different repository. Otherwise it uses the workflow's repository token.
For GitHub Enterprise, configure the catalog hostname and any nonstandard
API/upload endpoints to match the installation, as in the catalog examples above.

## 6. Ownership, drift, and recovery

| Record | What it contains | Share in Git? |
| --- | --- | --- |
| `.atlas/packages.json` | Requested sources, provider and configured harness identity | Yes |
| `.atlas/packages.lock.json` | Exact graph, release identities, archive digests | Yes |
| `.atlas/install-receipt.json` | Installed content, file/key/array ownership, original values | No |
| `.atlas/install-journal.json` | Recovery state for an interrupted transaction | No |
| `.atlas/install.lock` | Local installation lock | No |

Atlas writes local-state patterns to a new `.atlas/.gitignore`. If that file
already exists, retain its contents and add the patterns yourself when needed.

Files retain executable modes. Existing JSONC comments and unrelated keys are
preserved. Owned configuration keys and resource-list entries can have several
package owners. Native resource identity collisions, such as duplicate MCP
server names or plugins selected twice under different versions, are rejected.

`package status` reports drift and exits nonzero when managed content was edited,
the declaration no longer matches its lock, or the packages have not been
installed on this machine. Atlas refuses to overwrite changed managed files or
keys during update/removal. Preserve the edits, put them in the package source
or a separate unmanaged setting, then restore the expected managed state and
retry. Unrelated user content remains in place.

Plans record the files they read; apply checks those snapshots again while
holding an installation lock. If an operation is interrupted:

```bash
atlas package recover
atlas package status
```

Recovery uses the durable journal and refuses to overwrite edits made after an
interruption. It does not delete native conversation history or provider-owned
package caches. Changes made directly through provider commands can cause
drift in Atlas-owned configuration; use Atlas for updates to those resources.

## 7. Verification and implementation boundaries

The local tests cover package resolution, exact pins, conflicts, shared
dependencies, JSONC preservation, ownership, rollback/recovery, CLI shortcuts,
single-object JSON output, and combined initialization. Opt-in installed-native
tests verify discovery and execution through both Atlas handoff and direct
provider commands. See the [verification report](verification.md) for the exact
commands, tested versions, and real GitHub checks.

This release uses GitHub Releases as a catalog. It does not provide semver-range
resolution, signed artifacts, a company-specific SSO device flow, a standalone
registry server, or a generated Claude marketplace. Those can be added behind
the existing catalog/artifact/installer boundaries without changing native
resource layouts. Native package manager transitive dependencies are not claimed
to be pinned by Atlas's top-level source reference; use self-contained payloads
when the entire installed resource tree must be reproducible.
