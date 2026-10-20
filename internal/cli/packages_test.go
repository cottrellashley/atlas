package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/resolve"
)

type packageCLI struct {
	t           *testing.T
	app         *App
	out, stderr bytes.Buffer
}

func newPackageCLI(t *testing.T) *packageCLI {
	t.Helper()
	c := &packageCLI{t: t}
	c.app = &App{Home: t.TempDir(), Out: &c.out, Err: &c.stderr, In: strings.NewReader("")}
	return c
}
func (c *packageCLI) run(args ...string) (string, error) {
	c.t.Helper()
	c.out.Reset()
	c.stderr.Reset()
	err := c.app.Run(context.Background(), args)
	return c.out.String(), err
}
func (c *packageCLI) must(args ...string) string {
	c.t.Helper()
	out, err := c.run(args...)
	if err != nil {
		c.t.Fatalf("atlas %v: %v\nstdout: %s\nstderr: %s", args, err, out, c.stderr.String())
	}
	return out
}
func cliWrite(t *testing.T, root, path, text string) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
func cliRead(t *testing.T, root, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func cliManifest(t *testing.T, root string, m artifact.Manifest) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	cliWrite(t, root, artifact.ManifestName, string(b))
}
func cliSkillPackage(t *testing.T, id, name, body string) string {
	t.Helper()
	root := t.TempDir()
	cliManifest(t, root, artifact.Manifest{SchemaVersion: 1, ID: id, Version: "1.0.0", Kind: "skill", Skills: []string{name}})
	cliWrite(t, root, name+"/SKILL.md", "---\nname: "+name+"\ndescription: Test shared skill\n---\n"+body+"\n")
	return root
}
func cliJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(s), &result); err != nil {
		t.Fatalf("command did not return one JSON object: %v\n%s", err, s)
	}
	return result
}

func TestCLIPackageSkillRoundTripAndLockedSync(t *testing.T) {
	for _, provider := range []string{"claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			c := newPackageCLI(t)
			target := t.TempDir()
			source := cliSkillPackage(t, "@test/research", "research", "original instructions")
			prefix := map[string]string{"claude": ".claude", "pi": ".pi", "opencode": ".opencode"}[provider]
			path := prefix + "/skills/research/SKILL.md"
			preview := cliJSON(t, c.must("package", "add", source, "--harness", provider, "--cwd", target, "--dry-run", "--json"))
			if preview["dryRun"] != true {
				t.Fatal(preview)
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("dry run changed target: %v %v", entries, err)
			}
			cliJSON(t, c.must("package", "add", source, "--harness", provider, "--cwd", target, "--json"))
			if !strings.Contains(cliRead(t, target, path), "original instructions") {
				t.Fatal("missing skill")
			}
			cliWrite(t, source, "research/SKILL.md", "---\nname: research\ndescription: Test shared skill\n---\nupdated instructions\n")
			cliManifest(t, source, artifact.Manifest{SchemaVersion: 1, ID: "@test/research", Version: "1.1.0", Kind: "skill", Skills: []string{"research"}})
			c.must("package", "sync", "--cwd", target)
			if !strings.Contains(cliRead(t, target, path), "original instructions") {
				t.Fatal("locked sync moved to changed source")
			}
			cliJSON(t, c.must("package", "update", "@test/research", "--cwd", target, "--json"))
			if !strings.Contains(cliRead(t, target, path), "updated instructions") {
				t.Fatal("explicit update did not apply changed source")
			}
			state := cliJSON(t, c.must("package", "status", "--cwd", target, "--json"))
			if state["locked"] != true {
				t.Fatal(state)
			}
			c.must("package", "remove", "@test/research", "--cwd", target)
			if _, err := os.Stat(filepath.Join(target, path)); !os.IsNotExist(err) {
				t.Fatalf("remove left skill: %v", err)
			}
		})
	}
}

func TestCLIPackageRefusesOverwriteOfUserEdits(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	source := cliSkillPackage(t, "@test/research", "research", "original")
	c.must("package", "add", source, "--harness", "claude", "--cwd", target)
	path := ".claude/skills/research/SKILL.md"
	cliWrite(t, target, path, "user modified instructions\n")
	for _, op := range []string{"update", "remove"} {
		if _, err := c.run("package", op, "@test/research", "--cwd", target); err == nil {
			t.Fatalf("%s overwrote user edits", op)
		}
		if cliRead(t, target, path) != "user modified instructions\n" {
			t.Fatal("user edit lost")
		}
	}
	out, err := c.run("package", "status", "--cwd", target, "--json")
	if err == nil {
		t.Fatal("status did not report drift")
	}
	status := cliJSON(t, out)
	if len(status["drift"].([]any)) == 0 {
		t.Fatal("drift diagnostics empty")
	}
}

func TestCLIBundlesRetainSharedDependencyUntilLastRemoval(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	shared := cliSkillPackage(t, "@test/shared", "shared", "shared instructions")
	var roots []string
	for _, name := range []string{"first", "second"} {
		root := t.TempDir()
		cliManifest(t, root, artifact.Manifest{SchemaVersion: 1, ID: "@test/" + name, Version: "1.0.0", Kind: "bundle", Dependencies: []artifact.Dependency{{Source: "file:" + shared}}})
		roots = append(roots, root)
		c.must("package", "add", root, "--harness", "pi", "--cwd", target)
	}
	_, lock, err := resolve.Read(target)
	if err != nil || len(lock.Packages) != 3 {
		t.Fatalf("shared dependency resolution: %#v %v", lock, err)
	}
	if _, err := c.run("package", "remove", "@test/shared", "--cwd", target); err == nil {
		t.Fatal("removed a transitive dependency directly")
	}
	c.must("package", "remove", "@test/first", "--cwd", target)
	if !strings.Contains(cliRead(t, target, ".pi/skills/shared/SKILL.md"), "shared instructions") {
		t.Fatal("shared dependency removed too early")
	}
	c.must("package", "remove", "@test/second", "--cwd", target)
	if _, err := os.Stat(filepath.Join(target, ".pi/skills/shared/SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("last removal left dependency: %v", err)
	}
}

func TestCLIRawSkillAndMCPShortcuts(t *testing.T) {
	for _, provider := range []string{"claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			c := newPackageCLI(t)
			if provider == "pi" {
				tools := t.TempDir()
				binary := filepath.Join(tools, "pi-version")
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '1.3.0\\n'\n"), 0755); err != nil {
					t.Fatal(err)
				}
				config, _ := json.Marshal(map[string]any{"schemaVersion": 1, "harnesses": map[string]any{"pi": map[string]any{"adapter": "pi", "command": []string{binary}}}})
				cliWrite(t, c.app.Home, "config.json", string(config))
			}
			target := t.TempDir()
			skill := t.TempDir()
			cliWrite(t, skill, "SKILL.md", "---\nname: raw-proof\ndescription: Raw skill import\n---\nRead the source.\n")
			cliWrite(t, skill, "scripts/proof.sh", "#!/bin/sh\nexit 0\n")
			if err := os.Chmod(filepath.Join(skill, "scripts/proof.sh"), 0755); err != nil {
				t.Fatal(err)
			}
			cliJSON(t, c.must("skill", "add", skill, "--harness", provider, "--cwd", target, "--json"))
			prefix := map[string]string{"claude": ".claude", "pi": ".pi", "opencode": ".opencode"}[provider]
			info, err := os.Stat(filepath.Join(target, prefix, "skills/raw-proof/scripts/proof.sh"))
			if err != nil || info.Mode().Perm()&0111 == 0 {
				t.Fatalf("helper executable mode lost: %v %v", info, err)
			}
			cliJSON(t, c.must("mcp", "add", "company_docs", "--command", "node", "--arg=--read-only", "--arg", "server.js", "--env", "MODE=research", "--harness", provider, "--cwd", target, "--json"))
			configPath := map[string]string{"claude": ".mcp.json", "pi": ".pi/mcp.json", "opencode": "opencode.json"}[provider]
			var document map[string]any
			if err := json.Unmarshal([]byte(cliRead(t, target, configPath)), &document); err != nil {
				t.Fatal(err)
			}
			key := "mcpServers"
			if provider == "opencode" {
				key = "mcp"
			}
			servers := document[key].(map[string]any)
			server := servers["company_docs"].(map[string]any)
			if provider == "opencode" {
				args := server["command"].([]any)
				if len(args) != 3 || args[0] != "node" || args[1] != "--read-only" {
					t.Fatal(server)
				}
			} else {
				if server["command"] != "node" || len(server["args"].([]any)) != 2 {
					t.Fatal(server)
				}
			}
			c.must("package", "remove", "mcp-company_docs", "--cwd", target)
			c.must("skill", "remove", "raw-proof", "--cwd", target)
		})
	}
}

func TestCLIShortcutRejectsIgnoredArguments(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	if _, err := c.run("mcp", "add", "docs", "--url", "https://docs.example/mcp", "--arg", "ignored", "--harness", "claude", "--cwd", target); err == nil {
		t.Fatal("HTTP MCP shortcut silently ignored a stdio argument")
	}
}

func TestCLIInitAndValidateJSON(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	source := cliSkillPackage(t, "@test/research", "extra-research", "instructions")
	cliJSON(t, c.must("package", "validate", source, "--json"))
	cliJSON(t, c.must("init", "--harness", "pi", "--cwd", target, "--package", source, "--json"))
	if !strings.Contains(cliRead(t, target, ".pi/skills/extra-research/SKILL.md"), "instructions") {
		t.Fatal("init did not install declared package")
	}
}

func TestCLIInitDryRunIncludesPackagesWithoutWriting(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	source := cliSkillPackage(t, "@test/research", "extra-research", "instructions")
	out := c.must("init", "--harness", "pi", "--cwd", target, "--package", source, "--dry-run")
	if !strings.Contains(out, "extra-research") {
		t.Fatalf("init dry run omitted package plan: %s", out)
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("init dry run wrote files: %v %v", entries, err)
	}
}

func TestCLIInitPackageTemplate(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	source := t.TempDir()
	cliManifest(t, source, artifact.Manifest{SchemaVersion: 1, ID: "@test/project", Version: "1.0.0", Kind: "project-template", Template: "template"})
	cliWrite(t, source, "template/template.json", `{"schemaVersion":1,"id":"company-project","version":"1.0.0","instructions":"instructions.md","agents":{"researcher":{}}}`)
	cliWrite(t, source, "template/instructions.md", "Company project instructions.\n")
	c.must("init", "--harness", "pi", "--cwd", target, "--template", "file:"+source)
	if !strings.Contains(cliRead(t, target, "AGENTS.md"), "Company project instructions.") {
		t.Fatal("packaged template missing")
	}
	declaration, lock, err := resolve.Read(target)
	if err != nil || len(declaration.Packages) != 1 || len(lock.Packages) != 1 || lock.Packages[0].ID != "@test/project" {
		t.Fatalf("template release was not locked: %#v %#v %v", declaration, lock, err)
	}
}

func TestCLIInitConflictingPackagesLeaveProjectUntouched(t *testing.T) {
	for _, mode := range []string{"template", "two-packages"} {
		t.Run(mode, func(t *testing.T) {
			c := newPackageCLI(t)
			target := t.TempDir()
			name := "research"
			if mode == "two-packages" {
				name = "extra-research"
			}
			first := cliSkillPackage(t, "@test/first", name, "first package instructions")
			args := []string{"init", "--harness", "pi", "--cwd", target, "--package", first}
			if mode == "two-packages" {
				second := cliSkillPackage(t, "@test/second", name, "different package instructions")
				args = append(args, "--package", second)
			}
			if _, err := c.run(args...); err == nil {
				t.Fatal("conflicting initialization was accepted")
			}
			entries, err := os.ReadDir(target)
			if err != nil || len(entries) != 0 {
				t.Fatalf("conflicting initialization partially changed project: %v %v", entries, err)
			}
		})
	}
}

func TestCLIPackageSyncPreservesConfiguredHarnessAlias(t *testing.T) {
	c := newPackageCLI(t)
	target, source, tools := t.TempDir(), t.TempDir(), t.TempDir()
	cliWrite(t, tools, "pi-version", "#!/bin/sh\nprintf 'checked\\n' >> \"$ATLAS_TEST_VERSION_LOG\"\nprintf '1.3.0\\n'\n")
	binary := filepath.Join(tools, "pi-version")
	if err := os.Chmod(binary, 0755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(tools, "checks")
	cfg, _ := json.Marshal(map[string]any{"schemaVersion": 1, "harnesses": map[string]any{"company-pi": map[string]any{"adapter": "pi", "command": []string{binary}, "env": map[string]string{"ATLAS_TEST_VERSION_LOG": log}}}})
	cliWrite(t, c.app.Home, "config.json", string(cfg))
	cliManifest(t, source, artifact.Manifest{SchemaVersion: 1, ID: "@test/native", Version: "1.0.0", Kind: "pi-package", Native: &artifact.Native{Path: "payload"}})
	cliWrite(t, source, "payload/extensions/proof.js", "export default function(pi) {}\n")
	c.must("package", "add", source, "--harness", "company-pi", "--cwd", target)
	c.must("package", "sync", "--cwd", target)
	if calls := cliRead(t, tools, "checks"); strings.Count(calls, "checked") != 2 {
		t.Fatalf("sync did not preserve configured executable: %q", calls)
	}
}

func TestCLIStatusChecksInstalledReleaseAndProtectsReceipt(t *testing.T) {
	c := newPackageCLI(t)
	target := t.TempDir()
	source := cliSkillPackage(t, "@test/status", "status", "body")
	cliWrite(t, target, ".atlas/.gitignore", "custom-private-file\n")
	c.must("package", "add", source, "--harness", "claude", "--cwd", target)
	c.must("package", "status", "--cwd", target)
	ignore := cliRead(t, target, ".atlas/.gitignore")
	if !strings.Contains(ignore, "custom-private-file") || !strings.Contains(ignore, "install-receipt.json") {
		t.Fatal(ignore)
	}
	_, lock, e := resolve.Read(target)
	if e != nil {
		t.Fatal(e)
	}
	lock.Packages[0].Digest = strings.Repeat("0", 64)
	cliWrite(t, target, ".atlas/packages.lock.json", string(resolve.Marshal(lock)))
	out, e := c.run("package", "status", "--cwd", target, "--json")
	if e == nil || !strings.Contains(out, "installed release differs from lock") {
		t.Fatalf("status accepted wrong installed digest: %v %s", e, out)
	}
}

func TestCLIPublishRejectsDependencyRangesBeforeNetwork(t *testing.T) {
	c := newPackageCLI(t)
	source := t.TempDir()
	cliManifest(t, source, artifact.Manifest{SchemaVersion: 1, ID: "@test/bundle", Version: "1.0.0", Kind: "bundle", Dependencies: []artifact.Dependency{{Source: "@test/skill@^1.0.0"}}})
	if _, e := c.run("package", "publish", source, "--dry-run"); e == nil || !strings.Contains(e.Error(), "exact versions") {
		t.Fatalf("range accepted: %v", e)
	}
}

func TestCLIUserScopeCannotBeReinterpretedAsProject(t *testing.T) {
	c := newPackageCLI(t)
	userHome := t.TempDir()
	source := cliSkillPackage(t, "@test/personal", "personal", "body")
	t.Setenv("HOME", userHome)
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "PI_CODING_AGENT_DIR", "XDG_CONFIG_HOME", "OPENCODE_CONFIG_DIR"} {
		t.Setenv(name, "")
	}
	c.must("package", "add", source, "--harness", "pi", "--scope", "user")
	c.must("package", "status", "--scope", "user")
	if _, e := c.run("package", "sync", "--cwd", userHome); e == nil || !strings.Contains(e.Error(), "user scope") {
		t.Fatalf("user state reinterpreted: %v", e)
	}
	if _, e := os.Stat(filepath.Join(userHome, ".pi/agent/skills/personal/SKILL.md")); e != nil {
		t.Fatal(e)
	}
	c.must("package", "remove", "@test/personal", "--scope", "user")
}
