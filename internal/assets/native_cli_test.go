package assets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
)

// This end-to-end gate builds the current Atlas CLI, creates three isolated
// projects, installs through the public commands, then interrogates the actual
// native runtimes. No model API or existing user credentials are needed.
func TestPackageCLINativeRoundTrip(t *testing.T) {
	if os.Getenv("ATLAS_PACKAGE_CLI_NATIVE") != "1" {
		t.Skip("set ATLAS_PACKAGE_CLI_NATIVE=1 for Atlas CLI/native provider round trips")
	}
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	atlas := filepath.Join(t.TempDir(), "atlas")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin/go"), "build", "-o", atlas, "./cmd/atlas")
	build.Dir = repo
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Atlas: %v\n%s", err, output)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			cfg, cwd, home := nativeTestConfig(t, provider)
			cfg.Env["ATLAS_HOME"] = filepath.Join(home, ".atlas")
			cfg.Env["ATLAS_ASSET_TOKEN"] = "cli-fixture-token"
			cfg.Env["OPENCODE_DISABLE_MODELS_FETCH"] = "true"
			config, _ := json.Marshal(map[string]any{"schemaVersion": 1, "harnesses": map[string]harness.Config{provider: cfg}})
			writeAsset(t, cfg.Env["ATLAS_HOME"], "config.json", string(config), 0600)
			command := func(args ...string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				cmd := process.Command(ctx, harness.Config{Command: []string{atlas}, Env: cfg.Env}, cwd, args...)
				output, err := cmd.CombinedOutput()
				defer process.KillGroup(cmd)
				if err != nil {
					t.Fatalf("atlas %v: %v\n%s", args, err, output)
				}
				return string(output)
			}
			command("init", "--harness", provider, "--template", "researcher")
			// A raw standard skill requires no Atlas manifest at its source.
			skill := filepath.Join(t.TempDir(), "company-research")
			writeAsset(t, skill, "SKILL.md", "---\nname: company-research\ndescription: Research company documents using primary sources.\n---\nAlways cite the source document.\n", 0644)
			writeAsset(t, skill, "scripts/probe.sh", "#!/bin/sh\nprintf '%s\\n' company-research\n", 0755)
			command("skill", "add", skill, "--harness", provider)
			// A normal package can independently contribute another native skill.
			pkgDir := t.TempDir()
			writeAsset(t, pkgDir, "skills/team-guide/SKILL.md", "---\nname: team-guide\ndescription: Follow the team documentation conventions.\n---\nPrefer primary sources.\n", 0644)
			manifest, _ := json.Marshal(artifact.Manifest{SchemaVersion: 1, ID: "@team/guide", Version: "1.0.0", Kind: "skill", Skills: []string{"skills/team-guide"}})
			writeAsset(t, pkgDir, artifact.ManifestName, string(manifest), 0644)
			command("package", "add", pkgDir, "--harness", provider)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.Header.Get("Authorization") != "Bearer cli-fixture-token" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				var request mcpAssetRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					w.WriteHeader(400)
					return
				}
				if len(request.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(assetMCPResponse(request))
			}))
			defer server.Close()
			command("mcp", "add", "atlas_http", "--url", server.URL, "--header", "Authorization=Bearer ${ATLAS_ASSET_TOKEN}", "--harness", provider)
			command("mcp", "add", "atlas_stdio", "--command", helper, "--arg=-test.run=^TestAssetMCPProcess$", "--env", "ATLAS_ASSET_MCP_HELPER=1", "--harness", provider)
			command("package", "sync", "--locked")
			var status struct {
				Packages []json.RawMessage `json:"packages"`
				Drift    []json.RawMessage `json:"drift"`
				Locked   bool              `json:"locked"`
			}
			if err := json.Unmarshal([]byte(command("package", "status", "--json")), &status); err != nil || !status.Locked || len(status.Drift) != 0 || len(status.Packages) != 4 {
				t.Fatalf("CLI package status: %+v %v", status, err)
			}
			layout, _ := LayoutFor(provider, "project")
			mode, err := os.Stat(filepath.Join(cwd, layout.Prefix, "skills/company-research/scripts/probe.sh"))
			if err != nil || mode.Mode().Perm() != 0755 {
				t.Fatalf("raw skill executable asset missing/mode lost: %v %v", mode, err)
			}
			if direct, viaAtlas := nativeAssetCommand(t, cfg.Command[0], cwd, cfg.Env, "--version"), command("tui", "--harness", provider, "--", "--version"); strings.TrimSpace(direct) != strings.TrimSpace(viaAtlas) {
				t.Fatalf("native TUI handoff differs: direct=%q viaAtlas=%q", direct, viaAtlas)
			}
			// Grant test-only native trust/approvals outside the installed project.
			if provider == "claude" {
				writeAsset(t, home, ".claude/settings.json", `{"enabledMcpjsonServers":["atlas_http","atlas_stdio"]}`, 0600)
			}
			if provider == "pi" {
				trust, _ := json.Marshal(map[string]bool{cwd: true})
				writeAsset(t, home, ".pi/agent/trust.json", string(trust), 0600)
			}
			checkNative := func(installed bool) {
				t.Helper()
				if provider == "opencode" {
					checkOpenCodeNativeResources(t, cfg.Command[0], cwd, cfg.Env, "company-research", installed)
					return
				}
				args := []string{"mcp", "list"}
				if provider == "pi" {
					args = append(args, "--json")
					checkPiNativeSkill(t, cfg.Command[0], cwd, cfg.Env, "company-research", installed)
				} else {
					checkClaudeNativeSkill(t, cfg.Command[0], cwd, cfg.Env, "company-research", installed)
				}
				output := nativeAssetCommand(t, cfg.Command[0], cwd, cfg.Env, args...)
				for _, name := range []string{"atlas_http", "atlas_stdio"} {
					if strings.Contains(output, name) != installed {
						t.Fatalf("native MCP presence %s installed=%v: %s", name, installed, output)
					}
				}
				if strings.Contains(output, "Failed") || strings.Contains(output, "Pending approval") {
					t.Fatalf("native MCP did not connect: %s", output)
				}
			}
			checkNative(true)
			command("skill", "remove", "company-research", "--harness", provider)
			command("package", "remove", "@team/guide", "--harness", provider)
			command("mcp", "remove", "mcp-atlas_http", "--harness", provider)
			command("mcp", "remove", "mcp-atlas_stdio", "--harness", provider)
			checkNative(false)
			if _, err := os.Stat(filepath.Join(cwd, layout.Prefix, "skills/research/SKILL.md")); err != nil {
				t.Fatalf("package removal deleted original template skill: %v", err)
			}
			t.Log("CLI init + skill add + package add + MCP stdio/HTTP + locked sync + TUI handoff + direct native discovery + removal passed")
		})
	}
}
