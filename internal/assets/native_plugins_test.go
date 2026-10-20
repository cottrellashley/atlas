package assets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/harness"
)

func TestStandalonePiMCPVersionPreflight(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "pi-version-fixture")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 17\nprintf '%s\\n' \"$ATLAS_TEST_PI_VERSION\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	pkg := artifact.Package{Manifest: artifact.Manifest{Kind: "mcp", MCP: "servers.json"}}
	for _, tc := range []struct {
		version string
		wantErr string
	}{
		{"1.2.0", "native MCP servers require version 1.3.0"},
		{"1.3.0", ""},
		{"1.4.1", ""},
		{"2.0.0", "support major version 1"},
		{"unrecognized", "cannot read pi version"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			cfg := harness.Config{Adapter: "pi", Command: []string{binary}, Env: map[string]string{"ATLAS_TEST_PI_VERSION": tc.version}}
			err := CheckNativeVersion(context.Background(), cfg, root, pkg)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("version %s preflight = %v; want %q", tc.version, err, tc.wantErr)
			}
		})
	}
	// Portable resource rendering/validation has no executable dependency.
	// Only installing a Pi MCP package invokes this targeted runtime preflight.
	missing := harness.Config{Adapter: "pi", Command: []string{filepath.Join(root, "missing-pi")}}
	if err := CheckNativeVersion(context.Background(), missing, root, artifact.Package{Manifest: artifact.Manifest{Kind: "skill"}}); err != nil {
		t.Fatalf("pure skill package unexpectedly needs an installed Pi: %v", err)
	}
	if err := CheckNativeVersion(context.Background(), missing, root, pkg); err == nil {
		t.Fatal("Pi MCP installation accepted a missing native executable")
	}
}

func nativeFixture(t *testing.T, kind string, files map[string]string) artifact.Package {
	t.Helper()
	root := t.TempDir()
	for path, text := range files {
		p := filepath.Join(root, "payload", path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return artifact.Package{Root: root, Manifest: artifact.Manifest{SchemaVersion: 1, ID: "@test/native", Version: "1.0.0", Kind: kind, Native: &artifact.Native{Path: "payload"}}}
}

func TestNativePluginRender(t *testing.T) {
	t.Run("Claude plugin keeps native identity and executable helpers", func(t *testing.T) {
		pkg := nativeFixture(t, "claude-plugin", map[string]string{".claude-plugin/plugin.json": `{"name":"test-native","version":"1.0.0"}`, "skills/check/SKILL.md": "---\nname: check\ndescription: Test plugin\n---\nCheck", "scripts/helper.sh": "#!/bin/sh\necho OK\n"})
		if err := os.Chmod(filepath.Join(pkg.Root, "payload/scripts/helper.sh"), 0755); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"project", "user"} {
			c, err := RenderNative(pkg, "claude", scope)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Files) != 3 || len(c.Values) != 1 || c.Values[0].Pointer != "/enabledPlugins/test-native@skills-dir" {
				t.Fatalf("unexpected contribution: %#v", c)
			}
			for _, f := range c.Files {
				if !strings.HasPrefix(f.Path, ".claude/skills/test-native/") {
					t.Fatal(f.Path)
				}
				if strings.HasSuffix(f.Path, ".sh") && f.Mode != 0755 {
					t.Fatalf("lost executable mode: %v", f.Mode)
				}
			}
		}
	})
	t.Run("Pi package uses relative native settings source", func(t *testing.T) {
		pkg := nativeFixture(t, "pi-package", map[string]string{"extensions/test.js": `export default function(pi) {}`})
		pkg.Manifest.Native.Options = map[string]any{"skills": []any{}, "extensions": []any{"extensions/*.js"}}
		for _, scope := range []string{"project", "user"} {
			c, err := RenderNative(pkg, "pi", scope)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Values) != 1 || !c.Values[0].Array || c.Values[0].Pointer != "/packages" {
				t.Fatalf("unexpected contribution: %#v", c)
			}
			v := c.Values[0].Value.(map[string]any)
			source := v["source"].(string)
			if filepath.Clean(filepath.Join(filepath.Dir(c.Values[0].Path), source, "extensions/test.js")) != c.Files[0].Path {
				t.Fatalf("package path does not resolve to installed resource: %#v", c)
			}
		}
	})
	t.Run("OpenCode both runtimes use one local plugin", func(t *testing.T) {
		pkg := nativeFixture(t, "opencode-plugin", map[string]string{"server.js": `export default {id: "test", setup: async () => {}}`, "tui.js": `export default {id: "test", setup: async () => {}}`})
		pkg.Manifest.Native.Target = "both"
		c, err := RenderNative(pkg, "opencode", "project")
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Files) != 2 || len(c.Values) != 0 {
			t.Fatalf("unexpected contribution: %#v", c)
		}
		if !strings.HasPrefix(c.Files[0].Path, ".opencode/plugins/") {
			t.Fatal(c.Files[0].Path)
		}
	})
}

func TestNativeSourcePinsAndTargets(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		provider, source string
		valid            bool
	}{
		{"pi", "npm:@company/tools@1.2.3", true}, {"pi", "git:git.company.example/team/tools@" + sha, true}, {"pi", "npm:tools@latest", false}, {"pi", "git:github.com/team/tools@main", false}, {"pi", "npm:tools@^1.2.3", false},
		{"opencode", "@company/tools@1.2.3", true}, {"opencode", "git+https://git.company.example/tools.git#" + sha, true}, {"opencode", "@company/tools", false}, {"opencode", "./local", false},
		{"opencode", "git+https://user:secret@git.company.example/tools.git#" + sha, false}, {"pi", "git:https://token@git.company.example/tools.git@" + sha, false},
	} {
		if err := validateNativeSource(tc.provider, tc.source); (err == nil) != tc.valid {
			t.Errorf("%s %q: %v", tc.provider, tc.source, err)
		}
	}
	pkg := artifact.Package{Manifest: artifact.Manifest{ID: "@test/tui", Kind: "opencode-plugin", Native: &artifact.Native{Source: "@company/tui@1.2.3", Target: "tui", Options: map[string]any{"theme": "night"}}}}
	if _, err := RenderNative(pkg, "opencode", "project"); err == nil {
		t.Fatal("project TUI package source silently accepted")
	}
	c, err := RenderNative(pkg, "opencode", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Values) != 1 || c.Values[0].Path != ".config/opencode/cli.json" {
		t.Fatalf("wrong TUI config: %#v", c)
	}
	b, _ := json.Marshal(c.Values[0].Value)
	if string(b) != `{"options":{"theme":"night"},"package":"@company/tui@1.2.3"}` {
		t.Fatal(string(b))
	}
}

func TestNativePluginRejectsUnusableOrEscapingPayloads(t *testing.T) {
	pkg := nativeFixture(t, "claude-plugin", map[string]string{".claude-plugin/plugin.json": `{"name":"test","version":"2.0.0"}`})
	if _, err := RenderNative(pkg, "claude", "project"); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("native mismatch accepted: %v", err)
	}
	pkg.Manifest.Native.Path = "../other"
	if _, err := RenderNative(pkg, "claude", "project"); err == nil {
		t.Fatal("escaping native path accepted")
	}
	pkg = nativeFixture(t, "pi-package", map[string]string{"package.json": `{"dependencies":{"missing":"1.0.0"}}`, "extensions/a.js": "export default function(pi) {}"})
	if _, err := RenderNative(pkg, "pi", "project"); err == nil || !strings.Contains(err.Error(), "not included") {
		t.Fatalf("unresolved local dependency accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkg.Root, "payload/package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(pkg.Root, "payload/escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := RenderNative(pkg, "pi", "project"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink accepted: %v", err)
	}
	pkg = nativeFixture(t, "pi-package", map[string]string{"extensions/a.js": "export default function(pi) {}"})
	pkg.Manifest.Native.Options = map[string]any{"typo": true}
	if _, err := RenderNative(pkg, "pi", "project"); err == nil {
		t.Fatal("unknown Pi option accepted")
	}
}
