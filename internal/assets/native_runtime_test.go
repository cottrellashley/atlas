package assets

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/artifact"
	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/install"
	"github.com/cottrellashley/atlas/internal/process"
)

// This gate exercises installed provider binaries, native plugin discovery and
// execution. It makes no model requests and never changes the user's home.
func TestNativePackageRuntimes(t *testing.T) {
	if os.Getenv("ATLAS_PACKAGE_NATIVE") != "1" {
		t.Skip("set ATLAS_PACKAGE_NATIVE=1 to exercise installed native plugin runtimes")
	}
	t.Run("claude", testClaudeNativePackage)
	t.Run("pi", testPiNativePackage)
	t.Run("opencode", testOpenCodeNativePackage)
}

func nativeTestConfig(t *testing.T, provider string) (harness.Config, string, string) {
	t.Helper()
	binary, err := exec.LookPath(provider)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve mise before changing HOME, otherwise its shim loses the user's
	// installation selection and an isolated-home test fails before execution.
	if strings.Contains(binary, "/mise/shims/") {
		out, err := exec.Command("mise", "which", provider).Output()
		if err != nil {
			t.Fatal(err)
		}
		binary = strings.TrimSpace(string(out))
	}
	base := t.TempDir()
	home, cwd := filepath.Join(base, "home"), filepath.Join(base, "project")
	for _, path := range []string{home, cwd} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_DATA_HOME": filepath.Join(home, ".local/share"), "XDG_STATE_HOME": filepath.Join(home, ".local/state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "CLAUDE_CODE_PLUGIN_DIRS": "", "CLAUDE_CODE_PLUGIN_CACHE_DIR": filepath.Join(home, ".claude/plugins"), "CLAUDE_CODE_SETTINGS_PATH": "",
		"PI_CODING_AGENT_DIR": filepath.Join(home, ".pi/agent"), "PI_CODING_AGENT_SESSION_DIR": "", "PI_OFFLINE": "1", "PI_TELEMETRY": "0",
		"OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "", "OPENCODE_CLI_CONFIG_CONTENT": "", "OPENCODE_CONFIG_DIR": filepath.Join(home, ".config/opencode"), "OPENCODE_PASSWORD": "atlas-native-package-test", "OPENCODE_SERVER_PASSWORD": "atlas-native-package-test",
		"ATLAS_NATIVE_MARKER": filepath.Join(base, "marker"),
	}
	return harness.Config{Adapter: provider, Command: []string{binary}, Env: env}, cwd, home
}

func applyNativePackage(t *testing.T, pkg artifact.Package, provider, scope, root string) {
	t.Helper()
	c, err := RenderNative(pkg, provider, scope)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := install.Plan(root, []install.Contribution{c})
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
}
func removeNativePackage(t *testing.T, root string) {
	t.Helper()
	plan, err := install.Plan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := install.Apply(root, plan); err != nil {
		t.Fatal(err)
	}
}

func testClaudeNativePackage(t *testing.T) {
	cfg, cwd, home := nativeTestConfig(t, "claude")
	pkg := nativeFixture(t, "claude-plugin", map[string]string{
		".claude-plugin/plugin.json": `{"name":"atlas-native-proof","version":"1.0.0","description":"Atlas native loading verification"}`,
		"skills/proof/SKILL.md":      "---\nname: proof\ndescription: Verify Atlas native plugin discovery\n---\nReturn ATLAS_NATIVE_PROOF.\n",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := CheckNativeVersion(ctx, cfg, cwd, pkg); err != nil {
		t.Fatal(err)
	}
	applyNativePackage(t, pkg, "claude", "project", cwd)
	list := func() []map[string]any {
		out, err := process.Command(ctx, cfg, cwd, "plugin", "list", "--json").CombinedOutput()
		if err != nil {
			t.Fatalf("Claude list: %v\n%s", err, out)
		}
		var plugins []map[string]any
		if err := json.Unmarshal(out, &plugins); err != nil {
			t.Fatalf("Claude list JSON: %v\n%s", err, out)
		}
		return plugins
	}
	found := func(plugins []map[string]any) bool {
		for _, p := range plugins {
			if p["id"] == "atlas-native-proof@skills-dir" && p["enabled"] == true {
				return true
			}
		}
		return false
	}
	if found(list()) {
		t.Fatal("Atlas installation bypassed Claude project trust")
	}
	// Isolated fixture represents a user who already accepted this exact
	// workspace's native trust dialog. Atlas never writes this preference.
	trust, _ := json.Marshal(map[string]any{"projects": map[string]any{cwd: map[string]any{"hasTrustDialogAccepted": true}}})
	if err := os.WriteFile(filepath.Join(home, ".claude", ".claude.json"), trust, 0600); err != nil {
		t.Fatal(err)
	}
	if !found(list()) {
		t.Fatal("trusted native Claude did not load installed plugin")
	}
	out, err := process.Command(ctx, cfg, cwd, "plugin", "details", "atlas-native-proof").CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("proof")) {
		t.Fatalf("Claude component inventory missing skill: %v\n%s", err, out)
	}
	removeNativePackage(t, cwd)
	if found(list()) {
		t.Fatal("removed native plugin still loaded")
	}
}

func testPiNativePackage(t *testing.T) {
	cfg, cwd, _ := nativeTestConfig(t, "pi")
	pkg := nativeFixture(t, "pi-package", map[string]string{
		"package.json":        `{"name":"atlas-native-proof","type":"module","pi":{"extensions":["./extensions/proof.js"]}}`,
		"extensions/proof.js": `import {writeFileSync} from "node:fs"; export default function(pi) { writeFileSync(process.env.ATLAS_NATIVE_MARKER, "PI_NATIVE_LOADED"); pi.registerCommand("atlas-native-proof", {description:"Atlas package proof", handler: async () => {}}); }`,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := CheckNativeVersion(ctx, cfg, cwd, pkg); err != nil {
		t.Fatal(err)
	}
	applyNativePackage(t, pkg, "pi", "project", cwd)
	commands := func() string {
		life, stop := context.WithCancel(ctx)
		defer stop()
		cmd := process.Command(life, cfg, cwd, "--mode", "rpc", "--approve", "--no-session")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { stop(); _ = cmd.Wait() }()
		if _, err := io.WriteString(stdin, "{\"id\":\"proof\",\"type\":\"get_commands\"}\n"); err != nil {
			t.Fatal(err)
		}
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 4096), 1024*1024)
		for scan.Scan() {
			var response map[string]any
			if json.Unmarshal(scan.Bytes(), &response) == nil && response["id"] == "proof" {
				if response["success"] != true {
					t.Fatalf("Pi get_commands failed: %s", scan.Text())
				}
				return scan.Text()
			}
		}
		t.Fatalf("Pi exited before commands: %s", stderr.String())
		return ""
	}
	if got := commands(); !strings.Contains(got, "atlas-native-proof") {
		t.Fatalf("native Pi did not register extension command: %s", got)
	}
	marker, err := os.ReadFile(cfg.Env["ATLAS_NATIVE_MARKER"])
	if err != nil || string(marker) != "PI_NATIVE_LOADED" {
		t.Fatalf("Pi extension did not execute: %q %v", marker, err)
	}
	removeNativePackage(t, cwd)
	if got := commands(); strings.Contains(got, "atlas-native-proof") {
		t.Fatalf("removed Pi extension still active: %s", got)
	}
}

func testOpenCodeNativePackage(t *testing.T) {
	cfg, cwd, _ := nativeTestConfig(t, "opencode")
	pkg := nativeFixture(t, "opencode-plugin", map[string]string{
		"package.json": `{"name":"atlas-native-proof","type":"module"}`,
		"server.ts":    `import {writeFileSync} from "node:fs"; export default {id: "atlas.native.proof", setup: async (ctx: any) => { writeFileSync(process.env.ATLAS_NATIVE_MARKER, "OPENCODE_NATIVE_LOADED:" + ctx.options.proof); }};`,
	})
	pkg.Manifest.Native.Options = map[string]any{"proof": "OPTIONS_PRESERVED"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := CheckNativeVersion(ctx, cfg, cwd, pkg); err != nil {
		t.Fatal(err)
	}
	applyNativePackage(t, pkg, "opencode", "project", cwd)
	list := func() []map[string]any {
		life, stop := context.WithCancel(ctx)
		defer stop()
		cmd := process.Command(life, cfg, cwd, "serve", "--hostname", "127.0.0.1", "--port", "0")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { stop(); _ = cmd.Wait() }()
		scan := bufio.NewScanner(stdout)
		base := ""
		for scan.Scan() {
			line := scan.Text()
			if idx := strings.Index(line, "http://127.0.0.1:"); idx >= 0 {
				base = strings.TrimSpace(line[idx:])
				break
			}
		}
		if base == "" {
			t.Fatalf("OpenCode did not announce server: %s", stderr.String())
		}
		client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
		defer client.CloseIdleConnections()
		u := base + "/api/plugin?" + url.Values{"location[directory]": {cwd}}.Encode()
		deadline := time.Now().Add(5 * time.Second)
		for {
			req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.SetBasicAuth("opencode", cfg.Env["OPENCODE_PASSWORD"])
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != 200 {
				t.Fatalf("OpenCode plugin inventory HTTP %d: %s", res.StatusCode, body)
			}
			var data struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(body, &data); err != nil {
				t.Fatalf("OpenCode inventory: %v\n%s", err, body)
			}
			for _, p := range data.Data {
				if p["id"] == "atlas.native.proof" {
					return data.Data
				}
			}
			if time.Now().After(deadline) {
				return data.Data
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	find := func(entries []map[string]any) bool {
		for _, p := range entries {
			if p["id"] == "atlas.native.proof" {
				state, _ := p["state"].(map[string]any)
				if state["status"] != "active" {
					t.Fatalf("OpenCode plugin failed: %v", p)
				}
				return true
			}
		}
		return false
	}
	if plugins := list(); !find(plugins) {
		t.Fatalf("OpenCode native plugin absent: %s", fmt.Sprint(plugins))
	}
	marker, err := os.ReadFile(cfg.Env["ATLAS_NATIVE_MARKER"])
	if err != nil || string(marker) != "OPENCODE_NATIVE_LOADED:OPTIONS_PRESERVED" {
		t.Fatalf("OpenCode extension did not execute: %q %v", marker, err)
	}
	removeNativePackage(t, cwd)
	if find(list()) {
		t.Fatal("removed OpenCode native plugin still active")
	}
}
