package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, path, content string) {
	t.Helper()
	p := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestBuiltinsAndIdempotence(t *testing.T) {
	for _, adapter := range []string{"opencode", "pi", "claude"} {
		t.Run(adapter, func(t *testing.T) {
			root := t.TempDir()
			plan, e := Build(root, t.TempDir(), adapter, adapter, "researcher")
			if e != nil {
				t.Fatal(e)
			}
			if e = Apply(root, plan); e != nil {
				t.Fatal(e)
			}
			p, found, e := Load(root)
			if e != nil || p.DefaultAgent != "researcher" || found != root {
				t.Fatalf("load: %+v %s %v", p, found, e)
			}
			again, e := Build(root, t.TempDir(), adapter, adapter, "researcher")
			if e != nil {
				t.Fatal(e)
			}
			for _, f := range again.Files {
				if f.Action != "unchanged" {
					t.Fatalf("not idempotent: %s %s", f.Path, f.Action)
				}
			}
			if e = Apply(root, again); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestConflictDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	put(t, root, "AGENTS.md", "user instructions\n")
	_, e := Build(root, t.TempDir(), "pi", "pi", "researcher")
	if e == nil || !strings.Contains(e.Error(), "conflicting") {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(b) != "user instructions\n" {
		t.Fatal("overwrote user file")
	}
	if _, e = os.Stat(filepath.Join(root, ".pi")); !os.IsNotExist(e) {
		t.Fatal("partial writes")
	}
}
func TestAdditiveNativeConfig(t *testing.T) {
	root := t.TempDir()
	put(t, root, "opencode.json", `{"model":"company/model","mcp":{"existing":{"type":"remote","url":"https://example.org/mcp"}}}`)
	p, e := Build(root, t.TempDir(), "opencode", "opencode", "researcher")
	if e != nil {
		t.Fatal(e)
	}
	if e = Apply(root, p); e != nil {
		t.Fatal(e)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(filepath.Join(root, "opencode.json"))
	_ = json.Unmarshal(b, &cfg)
	if cfg["model"] != "company/model" || cfg["mcp"].(map[string]any)["existing"] == nil {
		t.Fatal("lost existing keys")
	}
}
func TestSymlinkAndTraversalRejected(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	if e := os.Symlink(elsewhere, filepath.Join(root, ".pi")); e != nil {
		t.Fatal(e)
	}
	if _, e := Build(root, t.TempDir(), "pi", "pi", "researcher"); e == nil {
		t.Fatal("accepted symlink output")
	}
	source := t.TempDir()
	put(t, source, "template.json", `{"schemaVersion":1,"id":"bad","version":"1","instructions":"../secret"}`)
	if _, e := Build(t.TempDir(), t.TempDir(), "pi", "pi", source); e == nil {
		t.Fatal("accepted traversal")
	}
}
func TestMCPTranslations(t *testing.T) {
	source := t.TempDir()
	put(t, source, "template.json", `{"schemaVersion":1,"id":"researcher","version":"2","instructions":"AGENTS.md","mcp":"servers.json"}`)
	put(t, source, "AGENTS.md", "Template\n")
	put(t, source, "servers.json", `{"local":{"transport":"stdio","command":"node","args":["server.js"],"env":{"TOKEN":"${TOKEN}"}},"remote":{"transport":"http","url":"https://example.org/mcp"}}`)
	for _, adapter := range []string{"opencode", "claude", "pi"} {
		plan, e := Build(t.TempDir(), t.TempDir(), adapter, adapter, source)
		if e != nil {
			t.Fatal(e)
		}
		path := map[string]string{"opencode": "opencode.json", "claude": ".mcp.json", "pi": ".pi/mcp.json"}[adapter]
		found := false
		for _, f := range plan.Files {
			if f.Path == path {
				var cfg map[string]any
				if e = json.Unmarshal(f.Data, &cfg); e != nil {
					t.Fatal(e)
				}
				key := "mcpServers"
				if adapter == "opencode" {
					key = "mcp"
				}
				servers := cfg[key].(map[string]any)
				if len(servers) != 2 {
					t.Fatal(string(f.Data))
				}
				found = true
			}
		}
		if !found {
			t.Fatal("missing MCP output")
		}
	}
}
func TestPlanDetectsConcurrentChanges(t *testing.T) {
	root := t.TempDir()
	plan, e := Build(root, t.TempDir(), "claude", "claude", "researcher")
	if e != nil {
		t.Fatal(e)
	}
	put(t, root, "CLAUDE.md", "concurrent\n")
	if e = Apply(root, plan); e == nil {
		t.Fatal("failed to detect concurrent file")
	}
	if _, e = os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(e) {
		t.Fatal("partial changes before validation")
	}
}
func TestJSONCNotShadowed(t *testing.T) {
	root := t.TempDir()
	put(t, root, "opencode.jsonc", "{ // comments\n}\n")
	if _, e := Build(root, t.TempDir(), "opencode", "opencode", "researcher"); e == nil {
		t.Fatal("would shadow JSONC")
	}
}

func TestExternalManifestDoesNotInheritBuiltinDefaults(t *testing.T) {
	source := t.TempDir()
	put(t, source, "template.json", `{}`)
	if _, e := Build(t.TempDir(), t.TempDir(), "pi", "pi", source); e == nil {
		t.Fatal("empty custom manifest accepted")
	}
	put(t, source, "template.json", `{"schemaVersion":1,"id":"custom","version":"1"}`)
	plan, e := Build(t.TempDir(), t.TempDir(), "pi", "pi", source)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Project.DefaultAgent != "custom" || len(plan.Project.Agents) != 1 {
		t.Fatal(plan.Project)
	}
}
func TestNullNativeOverlayReturnsError(t *testing.T) {
	source := t.TempDir()
	put(t, source, "template.json", `{"schemaVersion":1,"id":"x","version":"1","harnesses":{"pi":{"overlay":"native"}}}`)
	put(t, source, "native/.pi/settings.json", "null")
	if _, e := Build(t.TempDir(), t.TempDir(), "pi", "pi", source); e == nil {
		t.Fatal("null overlay accepted")
	}
}
func TestPreservesPrivateConfigAndExecutableSkill(t *testing.T) {
	root := t.TempDir()
	put(t, root, "opencode.json", `{"model":"company/model"}`)
	source := t.TempDir()
	put(t, source, "template.json", `{"schemaVersion":1,"id":"x","version":"1","skills":["skills/custom"]}`)
	put(t, source, "skills/custom/SKILL.md", "skill")
	put(t, source, "skills/custom/helper.sh", "#!/bin/sh\nexit 0\n")
	_ = os.Chmod(filepath.Join(source, "skills/custom/helper.sh"), 0755)
	plan, e := Build(root, t.TempDir(), "opencode", "opencode", source)
	if e != nil {
		t.Fatal(e)
	}
	if e = Apply(root, plan); e != nil {
		t.Fatal(e)
	}
	for path, want := range map[string]os.FileMode{"opencode.json": 0600, ".opencode/skills/custom/helper.sh": 0755} {
		info, e := os.Stat(filepath.Join(root, path))
		if e != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode want%o: %v %v", path, want, info, e)
		}
	}
}
func TestMCPEnvironmentReferencesAndOptionalFields(t *testing.T) {
	source := t.TempDir()
	put(t, source, "template.json", `{"schemaVersion":1,"id":"x","version":"1","mcp":"mcp.json"}`)
	put(t, source, "mcp.json", `{"test":{"command":"probe","env":{"TOKEN":"${TOKEN}"}},"empty":{"command":"probe"}}`)
	plan, e := Build(t.TempDir(), t.TempDir(), "opencode", "opencode", source)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range plan.Files {
		if f.Path == "opencode.json" {
			if !strings.Contains(string(f.Data), "{env:TOKEN}") || strings.Contains(string(f.Data), "null") {
				t.Fatal(string(f.Data))
			}
		}
	}
}
