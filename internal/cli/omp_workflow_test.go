package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/harness"
)

func TestOMPSetupPreservesNativeYAMLAndProjectsRemainSeparate(t *testing.T) {
	c := newPackageCLI(t)
	dir := t.TempDir()
	original := "# Existing Oh-my-pi preferences\nautoResume: true # user choice\n"
	cliWrite(t, dir, ".omp/config.yml", original)
	cliWrite(t, dir, ".pi/settings.json", `{"quietStartup":true}`)
	c.must("init", "researcher", "--harness", "omp", "--cwd", dir, "--no-open")
	if cliRead(t, dir, ".omp/config.yml") != original || cliRead(t, dir, ".pi/settings.json") != `{"quietStartup":true}` {
		t.Fatal("OMP setup changed unrelated native settings")
	}
	if _, err := os.Stat(filepath.Join(dir, ".omp/skills/research/SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".omp/mcp.json")); err != nil {
		t.Fatal(err)
	}
	cfg, p, _, err := c.app.selection("", dir)
	if err != nil || cfg.Adapter != "omp" || p.Harness != "omp" {
		t.Fatal(cfg, p, err)
	}
	args, err := launchArgs(cfg, dir, nil, p)
	if err != nil {
		t.Fatal(err)
	}
	// Native OMP suppresses its optional autoResume when an explicit directory
	// is given. This must be a fresh start without rewriting user configuration.
	if !strings.Contains(strings.Join(args, " "), "--session-dir") {
		t.Fatalf("new OMP session can auto-resume existing history: %v", args)
	}
	if cliRead(t, dir, ".omp/config.yml") != original {
		t.Fatal("launch planning changed YAML")
	}
}

func TestOMPConfigPackagePreservesYAMLAndDetectsOwnedEdits(t *testing.T) {
	c := newPackageCLI(t)
	dir, source := t.TempDir(), t.TempDir()
	cliWrite(t, dir, ".omp/config.yml", "# Enterprise preference\nquietStartup: true # keep this\n")
	cliManifest(t, source, artifact.Manifest{SchemaVersion: 1, ID: "@test/omp-config", Version: "1.0.0", Kind: "harness-config", Harnesses: []string{"omp"}, Config: map[string]string{"omp": "config.json"}})
	cliWrite(t, source, "config.json", `{"autoResume":false}`)
	c.must("package", "add", source, "--harness", "omp", "--cwd", dir, "--json")
	installed := cliRead(t, dir, ".omp/config.yml")
	for _, want := range []string{"# Enterprise preference", "quietStartup: true", "# keep this", "autoResume: false"} {
		if !strings.Contains(installed, want) {
			t.Fatalf("lost %q:\n%s", want, installed)
		}
	}
	c.must("package", "sync", "--cwd", dir, "--locked")
	if cliRead(t, dir, ".omp/config.yml") != installed {
		t.Fatal("locked sync rewrote unchanged YAML")
	}
	cliWrite(t, dir, ".omp/config.yml", strings.Replace(installed, "autoResume: false", "autoResume: true", 1))
	if _, err := c.run("package", "remove", "@test/omp-config", "--cwd", dir); err == nil {
		t.Fatal("removed edited configuration")
	}
	cliWrite(t, dir, ".omp/config.yml", installed)
	c.must("package", "remove", "@test/omp-config", "--cwd", dir)
	removed := cliRead(t, dir, ".omp/config.yml")
	if strings.Contains(removed, "autoResume") || !strings.Contains(removed, "quietStartup: true") || !strings.Contains(removed, "# keep this") {
		t.Fatal(removed)
	}
}

func TestOMPProfileUserPackagesFailBeforeDefaultDirectoryWrites(t *testing.T) {
	for _, key := range []string{"OMP_PROFILE", "PI_PROFILE", "PI_CONFIG_DIR", "PI_CODING_AGENT_DIR"} {
		err := checkUserEnvironment(harness.Config{Adapter: "omp", Env: map[string]string{key: "enterprise"}})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: %v", key, err)
		}
	}
	for _, command := range [][]string{{"omp", "--profile", "enterprise"}, {"omp", "--profile=enterprise"}} {
		if err := checkUserEnvironment(harness.Config{Adapter: "omp", Command: command}); err == nil {
			t.Fatal(command)
		}
	}
	if err := checkUserEnvironment(harness.Config{Adapter: "omp", Options: map[string]json.RawMessage{"profile": json.RawMessage(`"enterprise"`)}}); err == nil {
		t.Fatal("accepted custom profile for default user assets")
	}
	if err := checkUserEnvironment(harness.Config{Adapter: "omp"}); err != nil {
		t.Fatal(err)
	}
}
