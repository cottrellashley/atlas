# Claude Code enterprise compatibility

Atlas's native workflow preserves the installed harness's authentication,
network configuration, and policy enforcement. The checks below passed on
**2026-10-22**, Linux x86_64, with **Claude Code 2.1.302** and **Go 1.27.3**.
They establish specific local behavior; they do not establish acceptance by a
company tenant, identity provider, proxy, or managed workstation fleet.

## Configuration ownership

`atlas new` and `atlas resume` execute the configured native command with the
inherited environment plus explicit harness `env` overrides. Atlas removes
Claude's parent-session markers before handoff. It does not automatically add
permission bypass, restricted setting sources, a replacement login, or a new
credential store. `CLAUDE_CONFIG_DIR` and the native home remain the user's
selected context; configured aliases can select separate native profiles.

Claude applies organization-managed settings above user, project, local, and
`--settings` values, with documented exceptions for stricter lower-level values.
Atlas forwards explicit settings to Claude and leaves that precedence intact.
The Linux managed-policy location is `/etc/claude-code`; `--setting-sources`
selects user/project/local sources, not an alternative organization-policy
implementation. [Managed settings](https://code.claude.com/docs/en/managed-settings),
[settings precedence](https://code.claude.com/docs/en/settings).

Use existing native enterprise setup with an Atlas harness alias. Keep secrets
in its normal credential mechanism or environment, and use native settings for
company restrictions. `atlas config show` redacts configured environment values.
Starting an initialized project preserves native settings and unrelated files.

## Authentication and network paths

The handoff regression tests use synthetic values to check these categories:

| Native setup | Values preserved across Atlas's process replacement |
| --- | --- |
| Anthropic login/API routing | Native config directory, API key, bearer/OAuth token variables, base URL, custom headers |
| Corporate network | HTTP/HTTPS proxy, proxy exclusions, custom CA files, mTLS certificate/key/passphrase variables |
| Amazon Bedrock | Provider selector, AWS profile/region, config and credential file paths, temporary credential and bearer-token variables |
| Google Cloud / Vertex | Provider selector, Application Default Credentials file, project ID and region |
| Microsoft Foundry | Provider selector, resource name, API/bearer token variables, Azure application identity variables |

Claude reads proxy, custom CA, and mTLS configuration at startup. The installed
native MCP test below additionally verifies rejection of an untrusted local TLS
endpoint and successful connection after supplying its CA through
`NODE_EXTRA_CA_CERTS`. Corporate proxy negotiation and mTLS were tested for
handoff preservation only. [Network configuration](https://code.claude.com/docs/en/network-config).

Bedrock uses Claude's native AWS credential resolution, including profiles and
SSO; Atlas does not acquire AWS credentials.
[Bedrock setup](https://code.claude.com/docs/en/amazon-bedrock).
Google Cloud uses its native Application Default Credentials chain and configured
project/region. [Google Cloud / Vertex setup](https://code.claude.com/docs/en/google-vertex-ai).
Foundry supports API keys, Entra ID's default credential chain, and supplied
bearer tokens. [Foundry setup](https://code.claude.com/docs/en/microsoft-foundry).

## Verified local behavior

`internal/adapters/claude/enterprise_test.go` verifies that discovery and exact
resume lookup use the selected `CLAUDE_CONFIG_DIR`, including inherited values,
alias overrides, and an explicit empty override falling back to the configured
home. Default interactive arguments leave native settings/authentication loading
intact; explicit native restrictions are forwarded unchanged.

`tests/enterprise_cli_test.go` builds Atlas once and verifies:

- Actual process replacement forwards the synthetic provider/network environment
  and configured overrides, preserves the working directory, and leaves fixture
  native login/settings files unchanged. It creates no Atlas session or daemon
  state.
- Installed Claude's `auth status --json` selects the isolated native config
  directory and environment API-key source without exposing the key. This is
  **auth-source selection**, not validation of a real credential.
- In a private Linux mount and network namespace, a managed MCP denylist remains
  enforced when `--settings` attempts to allow the blocked server. Malformed
  managed JSON causes native startup to fail closed. The fixture mounts a
  disposable `/etc`; it never writes the host's managed policy.
- An unapproved project MCP configuration makes no connection. After approval
  inside the disposable native profile, an HTTPS loopback MCP server receives
  its dedicated service token but not interpolated Claude, AWS, or Foundry
  credential variables. The `.mcp.json` remains unchanged, including its
  environment references.

The MCP behavior follows Claude's native project approval and credential-variable
filtering rules. Use a service-specific variable for a service token; native
provider credentials are not generic project MCP credentials.
[MCP approvals and environment expansion](https://code.claude.com/docs/en/mcp).

## Repeating the checks

The deterministic adapter and process-handoff regressions need no installed
Claude or credentials:

```bash
go test ./internal/adapters/claude ./tests -run Enterprise -count=1
```

The installed-Claude checks are opt-in and make **no model inference requests**:

```bash
ATLAS_CLAUDE_ENTERPRISE=1 \
  go test -race ./internal/adapters/claude ./tests -run Enterprise -count=1 -v
go vet ./internal/adapters/claude ./tests
```

The managed-policy subtest requires Linux, `bwrap`, and working private mount and
network namespaces. It reports a skip when those are unavailable. All native
homes, settings, approvals, certificates, and tokens are disposable fixtures.

Not verified here: actual enterprise SSO or tenant authorization; live Bedrock,
Vertex, or Foundry inference; enterprise proxy/mTLS negotiation; server-managed
policy delivery; organization-specific MCP OAuth; Windows/WSL policy inheritance
or MDM rollout. Those require the organization's own access and deployment
context. A personal Claude login or the local checks above cannot establish
those results.
