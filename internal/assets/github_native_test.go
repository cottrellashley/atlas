package assets

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
	"github.com/cottrellashley/atlas/internal/resolve"
)

// This read-only live gate needs an existing release graph matching the example
// @demo/researcher bundle. It uses the caller's GitHub login without copying it
// into test homes, and never publishes or changes a remote repository.
func TestGitHubNativeBundleTwoConsumers(t *testing.T) {
	if os.Getenv("ATLAS_PACKAGE_GITHUB_NATIVE") != "1" {
		t.Skip("set ATLAS_PACKAGE_GITHUB_NATIVE=1 and ATLAS_PACKAGE_GITHUB_REPO=OWNER/REPO for read-only live distribution verification")
	}
	repository := os.Getenv("ATLAS_PACKAGE_GITHUB_REPO")
	if repository == "" {
		t.Fatal("ATLAS_PACKAGE_GITHUB_REPO is required")
	}
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	atlas := filepath.Join(t.TempDir(), "atlas")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-o", atlas, "./cmd/atlas")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Atlas: %v\n%s", err, output)
	}
	for _, provider := range []string{"claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			var priorProject string
			var priorLock []byte
			for consumer := 0; consumer < 2; consumer++ {
				cfg, cwd, nativeHome := nativeTestConfig(t, provider)
				atlasHome := filepath.Join(t.TempDir(), "atlas-home")
				cfg.Env["OPENCODE_DISABLE_MODELS_FETCH"] = "true"
				config, _ := json.Marshal(map[string]any{"schemaVersion": 1, "harnesses": map[string]harness.Config{provider: cfg}})
				writeAsset(t, atlasHome, "config.json", string(config), 0600)
				command := func(args ...string) string {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					defer cancel()
					// Keep real HOME solely for the existing GitHub auth bridge.
					// Native commands use cfg.Env and its isolated fixture HOME.
					cmd := process.Command(ctx, harness.Config{Command: []string{atlas}, Env: map[string]string{"ATLAS_HOME": atlasHome}}, cwd, args...)
					output, err := cmd.CombinedOutput()
					defer process.KillGroup(cmd)
					if err != nil {
						t.Fatalf("GitHub consumer %d atlas %v: %v\n%s", consumer+1, args, err, output)
					}
					return string(output)
				}
				command("catalog", "add", "native-fixture", repository)
				if consumer == 0 {
					command("init", "--harness", provider, "--template", "researcher")
					command("package", "add", "@demo/researcher@1.0.0", "--harness", provider)
				} else {
					// Another developer checks out only project/dependency records.
					// No native files, cache or receipt are copied.
					for _, name := range []string{"project.json", "packages.json", "packages.lock.json"} {
						data, err := os.ReadFile(filepath.Join(priorProject, ".atlas", name))
						if err != nil {
							t.Fatal(err)
						}
						writeAsset(t, cwd, ".atlas/"+name, string(data), 0644)
					}
					command("package", "sync", "--locked")
				}
				lock, err := os.ReadFile(filepath.Join(cwd, ".atlas/packages.lock.json"))
				if err != nil {
					t.Fatal(err)
				}
				if consumer == 1 && !bytes.Equal(lock, priorLock) {
					t.Fatalf("second consumer changed the lockfile:\n%s\n%s", priorLock, lock)
				}
				var parsed resolve.Lock
				if err := json.Unmarshal(lock, &parsed); err != nil || len(parsed.Packages) != 3 {
					t.Fatalf("expected bundle, shared skill, native plugin: %v %s", err, lock)
				}
				for _, pkg := range parsed.Packages {
					if !strings.Contains(pkg.Source, repository) || len(pkg.Digest) != 64 || pkg.Harness != provider {
						t.Fatalf("release lacks locked remote identity: %+v", pkg)
					}
				}
				command("package", "status", "--json")
				checkPublishedNativeBundle(t, cfg, cwd, nativeHome)
				t.Logf("%s consumer %d loaded shared skill and native plugin from %s with exact release hashes", provider, consumer+1, repository)
				if consumer == 0 {
					priorProject, priorLock = cwd, lock
				}
			}
		})
	}
}

func checkPublishedNativeBundle(t *testing.T, cfg harness.Config, cwd, home string) {
	t.Helper()
	switch cfg.Adapter {
	case "claude":
		trust, _ := json.Marshal(map[string]any{"projects": map[string]any{cwd: map[string]any{"hasTrustDialogAccepted": true}}})
		writeAsset(t, home, ".claude/.claude.json", string(trust), 0600)
		checkClaudeNativeSkill(t, cfg.Command[0], cwd, cfg.Env, "evidence", true)
		output := nativeAssetCommand(t, cfg.Command[0], cwd, cfg.Env, "plugin", "list", "--json")
		var plugins []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.Unmarshal([]byte(output), &plugins); err != nil {
			t.Fatal(err)
		}
		for _, plugin := range plugins {
			if plugin.ID == "atlas-demo@skills-dir" && plugin.Enabled {
				return
			}
		}
		t.Fatalf("published Claude plugin is not enabled: %s", output)
	case "pi":
		checkPiNativeSkill(t, cfg.Command[0], cwd, cfg.Env, "evidence", true)
		checkPiNativeSkill(t, cfg.Command[0], cwd, cfg.Env, "atlas-demo", true)
	case "opencode":
		get, cleanup := nativeOpenCodeGet(t, cfg.Command[0], cwd, cfg.Env)
		defer cleanup()
		deadline := time.Now().Add(10 * time.Second)
		for {
			skills, rawPlugins := get("/api/skill"), get("/api/plugin")
			var plugins struct {
				Data []struct {
					ID    string `json:"id"`
					State struct {
						Status string `json:"status"`
					} `json:"state"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(rawPlugins), &plugins); err != nil {
				t.Fatal(err)
			}
			for _, plugin := range plugins.Data {
				if plugin.ID == "atlas.demo.tools" && plugin.State.Status == "active" && strings.Contains(skills, "evidence") {
					return
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("published OpenCode resources not active: skills=%s plugins=%s", skills, rawPlugins)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
