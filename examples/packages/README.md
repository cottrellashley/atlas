# Small provider-native packages

Each directory is a complete local Atlas package and can also be published to
an Atlas GitHub catalog. They have no external runtime dependencies and perform
no network requests or file mutations.

From the repository root, install into a separate test project:

```bash
atlas package add ./examples/packages/claude-plugin --harness claude --cwd /path/to/project
atlas package add ./examples/packages/pi-package --harness pi --cwd /path/to/project
atlas package add ./examples/packages/opencode-plugin --harness opencode --cwd /path/to/project
```

Use separate projects when testing one harness at a time. Each native command
below runs from the target project's root after accepting its native project
trust prompt where required:

| Package | Native observation |
| --- | --- |
| `@demo/claude-tools` | `claude plugin list --json` contains `atlas-demo@skills-dir`; `claude plugin details atlas-demo` lists `proof`; `/atlas-demo:proof` is available in a session |
| `@demo/pi-tools` | `pi list` includes the installed local package; Pi's TUI has `/atlas-demo`; the RPC `get_commands` response includes `atlas-demo` |
| `@demo/opencode-tools` | OpenCode's native `/api/plugin` inventory for the project reports `atlas.demo.tools` with `state.status: active`; it is a server plugin and also loads for a native TUI session using that project |

The OpenCode demo intentionally adds no model tools or UI elements. It is a
minimal valid v2 plugin whose presence is visible in the native inventory.

Read [Native packages](../../docs/native-packages.md) for scope, dependency,
version, and trust behavior. These examples use self-contained payloads so
Atlas can own every installed file and remove them cleanly.
