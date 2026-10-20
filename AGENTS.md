# Atlas contributor notes

Atlas is a Go CLI running inside Linux/WSL. Native harnesses own inference,
tools, authentication and conversation transcripts. Keep changes scoped to the
package that owns the behavior; see docs/architecture.md.

- Preserve separate configured harness, adapter, model, managed session, turn,
  and native persistence identities. Model IDs are opaque in shared code.
- A request acknowledgment is not a completed turn. Native cancellation must
  settle before allowing the next turn. Emit persistence as soon as it exists.
- Keep requested configuration separate from observed effective state. Reject
  unsupported operations and unknown native options rather than ignoring them.
- Template initialization must preserve unrelated configuration, existing file
  permissions, executable skill assets, and user edits. Validate before writing.
- Use argv arrays and internal/process for owned native launches. Keep protocol
  stdout separate from stderr; never print credential stores to debug an issue.
- Unit tests use local fixtures. Native integration gates are explicit because
  they launch installed harnesses and some use real authenticated inference.
  Report deterministic model fixtures separately from genuine model responses.
- Run gofmt, focused tests, and go vet for changed packages. Use the full race
  gate for shared lifecycle/protocol changes. scripts/test-native.sh exercises
  all three installed harnesses; read docs/verification.md before running it.
- Do not change an existing user's harness login or global native configuration
  to make tests pass. Test projects, home directories and local servers are isolated.
- Do not claim WSL/Windows validation from a test run on a regular Linux host.
- Package distribution is separate from inference drivers. Preserve immutable
  catalog source/digest identities, configured harness aliases, and native scope.
- Package changes use internal/install ownership and transactions; never replace
  an entire user configuration to change one field. Preserve JSONC comments,
  unrelated settings, shared dependencies, edits and executable file modes.
- Close acquired artifact.Package snapshots after rendering. Keep archive caches;
  never cache credentials or publish files outside declared package resources.
- Native resource gates: scripts/test-packages.sh. Live GitHub tests are explicit,
  and publishing tests create real releases; use an authorized test catalog.
