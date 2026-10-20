package assets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/install"
)

func writeAsset(t *testing.T, root, name, data string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

func resourceFixture(t *testing.T) artifact.Package {
	t.Helper()
	root := t.TempDir()
	writeAsset(t, root, "skills/research/SKILL.md", "---\nname: research\ndescription: >-\n  Investigate sources.\n  Produce a cited report.\n---\nRead references/style.md.\n", 0644)
	writeAsset(t, root, "skills/research/scripts/search.sh", "#!/bin/sh\nprintf '%s\\n' research\n", 0755)
	writeAsset(t, root, "skills/research/references/style.md", "Cite primary sources.\n", 0640)
	writeAsset(t, root, "mcp.json", `{
  "docs": {"transport":"http","url":"https://docs.example.test/mcp","headers":{"Authorization":"Bearer ${DOCS_TOKEN}"},"native":{"pi":{"exposure":"direct"}}},
  "local_tools": {"command":"node","args":["tools.mjs"],"env":{"TOKEN":"${LOCAL_TOKEN}"}}
}`, 0644)
	return artifact.Package{Root: root, Manifest: artifact.Manifest{SchemaVersion: 1, ID: "@research/resources", Version: "1.0.0", Kind: "bundle", Skills: []string{"skills/research"}, MCP: "mcp.json"}}
}

func valueAt(t *testing.T, c install.Contribution, path, pointer string) any {
	t.Helper()
	for _, v := range c.Values {
		if v.Path == path && v.Pointer == pointer {
			return v.Value
		}
	}
	t.Fatalf("missing %s%s in %+v", path, pointer, c.Values)
	return nil
}

func TestPortableResourcesRenderNativePathsAndValues(t *testing.T) {
	pkg := resourceFixture(t)
	for _, harness := range []string{"claude", "pi", "opencode"} {
		for _, scope := range []string{"project", "user"} {
			t.Run(harness+"/"+scope, func(t *testing.T) {
				c, err := Render(pkg, harness, scope)
				if err != nil {
					t.Fatal(err)
				}
				layout, _ := LayoutFor(harness, scope)
				if len(c.Files) != 3 {
					t.Fatalf("skill tree not preserved: %+v", c.Files)
				}
				for _, f := range c.Files {
					if !strings.HasPrefix(f.Path, layout.Prefix+"/skills/research/") {
						t.Fatalf("unexpected skill path %s", f.Path)
					}
					if strings.HasSuffix(f.Path, ".sh") && f.Mode != 0755 {
						t.Fatalf("script executable mode lost: %v", f.Mode)
					}
				}
				prefix := layout.MCPPointer
				wantAuth := "Bearer ${DOCS_TOKEN}"
				if harness == "opencode" {
					wantAuth = "Bearer {env:DOCS_TOKEN}"
					if got := valueAt(t, c, layout.MCP, prefix+"/docs/type"); got != "remote" {
						t.Fatalf("remote MCP type = %v", got)
					}
					if got := valueAt(t, c, layout.MCP, prefix+"/local_tools/environment/TOKEN"); got != "{env:LOCAL_TOKEN}" {
						t.Fatalf("MCP env = %v", got)
					}
				} else if got := valueAt(t, c, layout.MCP, prefix+"/local_tools/env/TOKEN"); got != "${LOCAL_TOKEN}" {
					t.Fatalf("MCP env = %v", got)
				}
				if got := valueAt(t, c, layout.MCP, prefix+"/docs/headers/Authorization"); got != wantAuth {
					t.Fatalf("MCP auth placeholder = %v", got)
				}
				if harness == "pi" && valueAt(t, c, layout.MCP, prefix+"/docs/exposure") != "direct" {
					t.Fatal("Pi native exposure missing")
				}
			})
		}
	}
}

func TestSkillsUseNativeYAMLAndRejectUnsafeSources(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, text string
		ok         bool
	}{
		{"valid", "name: valid\ndescription: 'Investigate users'' questions.'", true},
		{"folded", "name: folded\ndescription: >\n  Investigate a question\n  with sources.", true},
		{"bad-name", "name: WRONG\ndescription: investigate", false},
		{"mismatch", "name: other\ndescription: investigate", false},
		{"missing", "name: missing", false},
		{"null", "name: null\ndescription: investigate", false},
		{"duplicate", "name: duplicate\nname: duplicate\ndescription: investigate", false},
		{"bad-yaml", "name: bad-yaml\ndescription: [", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeAsset(t, root, tc.name+"/SKILL.md", "---\n"+tc.text+"\n---\nInstructions.", 0644)
			_, err := ReadSkill(root, tc.name)
			if (err == nil) != tc.ok {
				t.Fatalf("ReadSkill error = %v; want success=%v", err, tc.ok)
			}
		})
	}
	if _, err := ReadSkill(root, "../outside"); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Symlink(filepath.Join(root, "valid"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSkill(root, "linked"); err == nil {
		t.Fatal("accepted symlink skill source")
	}
}

func TestPortableMCPRejectsUnsupportedAndConflictingConfiguration(t *testing.T) {
	for _, data := range []string{
		`null`,
		`{"docs":{"url":"https://example.test","unexpected":true}}`,
		`{"docs":{"transport":"sse","url":"https://example.test"}}`,
		`{"docs":{"command":"node","url":"https://example.test"}}`,
		`{"docs":{"url":"https://user:secret@example.test"}}`,
		`{"docs":{"url":"file:///etc/passwd"}}`,
		`{"docs":{"command":"node","native":{"other":{}}}}`,
		`{"docs":{"command":"node","native":{"pi":{"command":"different"}}}}`,
		`{"same-name":{"command":"node"},"same_name":{"command":"node"}}`,
	} {
		t.Run(data, func(t *testing.T) {
			root := t.TempDir()
			writeAsset(t, root, "mcp.json", data, 0600)
			pkg := artifact.Package{Root: root, Manifest: artifact.Manifest{ID: "test", MCP: "mcp.json"}}
			if _, err := Render(pkg, "pi", "project"); err == nil {
				t.Fatalf("accepted invalid MCP %s", data)
			}
		})
	}
}

func TestMCPEnvironmentEndpointSupportMatchesNativeProviders(t *testing.T) {
	for _, provider := range []string{"claude", "opencode", "pi"} {
		entry, err := renderServer("docs", MCPServer{Transport: "http", URL: "${DOCS_ENDPOINT}"}, provider)
		if provider == "pi" {
			if err == nil || !strings.Contains(err.Error(), "does not expand") {
				t.Fatalf("Pi URL placeholder requires actionable error: %v", err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			want := "${DOCS_ENDPOINT}"
			if provider == "opencode" {
				want = "{env:DOCS_ENDPOINT}"
			}
			if entry["url"] != want {
				t.Fatalf("%s URL placeholder = %v", provider, entry["url"])
			}
		}
	}
}

func TestCrossPackageMCPNameNormalizationConflicts(t *testing.T) {
	contributions := []install.Contribution{
		{PackageID: "one", Values: []install.Value{{Path: ".pi/mcp.json", Pointer: "/mcpServers/docs-search/url", Value: "https://one.test"}}},
		{PackageID: "two", Values: []install.Value{{Path: ".pi/mcp.json", Pointer: "/mcpServers/docs_search/url", Value: "https://two.test"}}},
	}
	if err := ValidateContributions(contributions); err == nil {
		t.Fatal("accepted cross-package names which collide in native Pi")
	}
	contributions[1].Values[0].Path = ".mcp.json"
	if err := ValidateContributions(contributions); err != nil {
		t.Fatalf("independent provider files should not conflict: %v", err)
	}
}

func TestClaudeStandaloneSkillCannotShareNativePluginDirectory(t *testing.T) {
	skillPackage := resourceFixture(t)
	skillPackage.Manifest.MCP = ""
	skill, err := Render(skillPackage, "claude", "project")
	if err != nil {
		t.Fatal(err)
	}
	pluginPackage := nativeFixture(t, "claude-plugin", map[string]string{
		".claude-plugin/plugin.json": `{"name":"research","version":"1.0.0"}`,
		"skills/research/SKILL.md":   "---\nname: research\ndescription: A namespaced plugin skill.\n---\nRead sources.\n",
	})
	plugin, err := Render(pluginPackage, "claude", "project")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		set  []install.Contribution
	}{
		{"skill then plugin", []install.Contribution{skill, plugin}},
		{"plugin then skill", []install.Contribution{plugin, skill}},
		{"one package owns both", []install.Contribution{{PackageID: "combined", Files: append(append([]install.File{}, skill.Files...), plugin.Files...), Values: plugin.Values}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateContributions(tc.set); err == nil || !strings.Contains(err.Error(), "both a standalone skill") || !strings.Contains(err.Error(), ".claude/skills/research") {
				t.Fatalf("native plugin would shadow standalone skill without a file conflict: %v", err)
			}
		})
	}
	// A differently named plugin may legitimately contain its own namespaced
	// research skill; that nested SKILL.md is not a competing standalone root.
	writeAsset(t, pluginPackage.Root, "payload/.claude-plugin/plugin.json", `{"name":"research-tools","version":"1.0.0"}`, 0644)
	plugin, err = Render(pluginPackage, "claude", "project")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateContributions([]install.Contribution{skill, plugin}); err != nil {
		t.Fatalf("distinct plugin and standalone roots should coexist: %v", err)
	}
}

func TestConfigKeysAndNativeFileTargets(t *testing.T) {
	root := t.TempDir()
	writeAsset(t, root, "settings.json", `{"permissions":{"allow":["Read"],"deny":["Bash"]},"custom/key":{"~option":true}}`, 0644)
	pkg := artifact.Package{Root: root, Manifest: artifact.Manifest{ID: "test", Config: map[string]string{"claude": "settings.json"}}}
	c, err := Render(pkg, "claude", "project")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Values) != 3 || valueAt(t, c, ".claude/settings.json", "/custom~1key/~0option") != true {
		t.Fatalf("configuration not owned per leaf key: %+v", c.Values)
	}
	for _, target := range []string{"../escape", ".atlas/project.json", ".bashrc", "/tmp/escape", ".claude/../escape"} {
		pkg.Manifest.Files = map[string]map[string]string{"claude": {target: "settings.json"}}
		if _, err := Render(pkg, "claude", "project"); err == nil {
			t.Fatalf("accepted unsafe/non-native target %s", target)
		}
	}
}

func TestRenderAtUsesExistingJSONCAndPreservesUserConfiguration(t *testing.T) {
	pkg := resourceFixture(t)
	root := t.TempDir()
	writeAsset(t, root, "opencode.jsonc", "{\n  // Company endpoint is configured by the user.\n  \"model\": \"company/research\",\n}\n", 0600)
	c, err := RenderAt(root, pkg, "opencode", "project")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range c.Values {
		if value.Path != "opencode.jsonc" {
			t.Fatalf("would shadow JSONC: %+v", value)
		}
	}
	plan, err := install.Plan(root, []install.Contribution{c})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "opencode.jsonc"))
	if err != nil || !strings.Contains(string(data), "// Company endpoint") || !strings.Contains(string(data), "company/research") || !strings.Contains(string(data), "docs") {
		t.Fatalf("native JSONC changed unexpectedly: %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "opencode.json")); !os.IsNotExist(err) {
		t.Fatal("created overriding JSON config")
	}
	// Removal leaves the preexisting model/comment and deletes managed skills.
	plan, err = install.Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(root, "opencode.jsonc"))
	if !strings.Contains(string(data), "company/research") || strings.Contains(string(data), "docs") {
		t.Fatalf("uninstall did not preserve unrelated config: %s", data)
	}
}

func TestRenderRejectsUnsupportedHarnessAndDualConfigs(t *testing.T) {
	pkg := resourceFixture(t)
	pkg.Manifest.Harnesses = []string{"claude"}
	if _, err := Render(pkg, "pi", "project"); err == nil {
		t.Fatal("ignored explicit harness compatibility")
	}
	pkg.Manifest.Harnesses = nil
	root := t.TempDir()
	writeAsset(t, root, "opencode.json", "{}", 0600)
	writeAsset(t, root, "opencode.jsonc", "{}", 0600)
	if _, err := RenderAt(root, pkg, "opencode", "project"); err == nil {
		t.Fatal("accepted ambiguous native configuration")
	}
	if _, err := Render(pkg, "pi", "invalid"); err == nil {
		t.Fatal("accepted invalid scope")
	}
	encoded, err := json.Marshal(pkg.Manifest)
	if err != nil || len(encoded) == 0 {
		t.Fatal(err)
	}
}

func TestOpenCode2CLIConfigUsesExactFilenameAndOwnedKeys(t *testing.T) {
	source := t.TempDir()
	writeAsset(t, source, "cli-settings.json", `{"tabs":{"mode":"on"}}`, 0644)
	pkg := artifact.Package{Root: source, Manifest: artifact.Manifest{ID: "@test/cli-settings", Files: map[string]map[string]string{"opencode": {".config/opencode/cli.json": "cli-settings.json"}}}}
	root := t.TempDir()
	original := "{\n  // User plugins remain installed.\n  \"plugins\": [],\n}\n"
	writeAsset(t, root, ".config/opencode/cli.json", original, 0600)
	// OpenCode 2 does not read this alternate filename. Atlas must not select
	// it merely because cli.json itself accepts JSONC syntax.
	writeAsset(t, root, ".config/opencode/cli.jsonc", "{\"ignored\": true}\n", 0600)
	c, err := RenderAt(root, pkg, "opencode", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Files) != 0 || len(c.Values) != 1 || c.Values[0].Path != ".config/opencode/cli.json" || c.Values[0].Pointer != "/tabs/mode" {
		t.Fatalf("CLI configuration must own keys in cli.json: %+v", c)
	}
	plan, err := install.Plan(root, []install.Contribution{c})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(filepath.Join(root, ".config/opencode/cli.json"))
	if !strings.Contains(string(updated), "// User plugins") || !strings.Contains(string(updated), `"plugins": []`) || !strings.Contains(strings.ReplaceAll(string(updated), " ", ""), `"mode":"on"`) {
		t.Fatalf("CLI settings update lost comments/user settings: %s", updated)
	}
	ignored, _ := os.ReadFile(filepath.Join(root, ".config/opencode/cli.jsonc"))
	if string(ignored) != "{\"ignored\": true}\n" {
		t.Fatalf("ignored alternate filename changed: %s", ignored)
	}
	plan, err = install.Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
	removed, _ := os.ReadFile(filepath.Join(root, ".config/opencode/cli.json"))
	if !strings.Contains(string(removed), "// User plugins") || !strings.Contains(string(removed), `"plugins": []`) || strings.Contains(string(removed), "tabs") {
		t.Fatalf("CLI settings removal did not preserve user data: %s", removed)
	}
}

func TestOpenCode2RejectsIgnoredCLIConfigurationTargets(t *testing.T) {
	source := t.TempDir()
	writeAsset(t, source, "settings.json", `{"tabs":{"mode":"on"}}`, 0644)
	for _, tc := range []struct{ scope, target string }{
		{"project", "tui.json"},
		{"project", "tui.jsonc"},
		{"project", "cli.json"},
		{"project", ".opencode/cli.json"},
		{"project", ".opencode/cli.jsonc"},
		{"project", ".opencode/tui.json"},
		{"project", ".opencode/tui.jsonc"},
		{"user", ".config/opencode/tui.json"},
		{"user", ".config/opencode/tui.jsonc"},
		{"user", ".config/opencode/cli.jsonc"},
	} {
		t.Run(tc.scope+"/"+tc.target, func(t *testing.T) {
			pkg := artifact.Package{Root: source, Manifest: artifact.Manifest{ID: "@test/ignored", Files: map[string]map[string]string{"opencode": {tc.target: "settings.json"}}}}
			if _, err := Render(pkg, "opencode", tc.scope); err == nil {
				t.Fatalf("accepted config filename not loaded by OpenCode 2: %s", tc.target)
			}
		})
	}
}
