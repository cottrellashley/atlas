package assets

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/install"
	"github.com/cottrellashley/atlas/internal/process"
)

type mcpAssetRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

func assetMCPResponse(request mcpAssetRequest) map[string]any {
	var result any
	switch request.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": request.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "atlas-package-fixture", "version": "1.0.0"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "package_probe", "description": "Inspect the harmless Atlas package fixture", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
	case "tools/call":
		result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "ATLAS_PACKAGE_MCP_RESULT"}}}
	case "resources/list":
		result = map[string]any{"resources": []any{}}
	case "prompts/list":
		result = map[string]any{"prompts": []any{}}
	default:
		result = map[string]any{}
	}
	return map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}
}

// This helper is a real stdio MCP server child launched by native harnesses.
func TestAssetMCPProcess(t *testing.T) {
	if os.Getenv("ATLAS_ASSET_MCP_HELPER") != "1" {
		t.Skip("MCP subprocess helper")
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request mcpAssetRequest
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		_ = json.NewEncoder(os.Stdout).Encode(assetMCPResponse(request))
	}
	os.Exit(0)
}

func installedAssetBinary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("native resource gate requires installed %s: %v", name, err)
	}
	if strings.Contains(filepath.ToSlash(path), "/shims/") {
		if data, err := exec.Command("mise", "which", name).Output(); err == nil {
			path = strings.TrimSpace(string(data))
		}
	}
	return path
}

func nativeAssetCommand(t *testing.T, binary, cwd string, env map[string]string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := process.Command(ctx, harness.Config{Command: []string{binary}, Env: env}, cwd, args...)
	cmd.Dir, cmd.Env = cwd, process.Environment(env)
	data, err := cmd.CombinedOutput()
	defer process.KillGroup(cmd)
	if err != nil {
		t.Fatalf("native resource command %s %v: %v\n%s", filepath.Base(binary), args, err, data)
	}
	return string(data)
}

func TestNativePackageMCPConnections(t *testing.T) {
	if os.Getenv("ATLAS_ASSETS_NATIVE") != "1" {
		t.Skip("set ATLAS_ASSETS_NATIVE=1 to test actual native MCP discovery")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			binary := installedAssetBinary(t, provider)
			var initializations, tools atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				if r.Header.Get("Authorization") != "Bearer package-fixture-token" {
					http.Error(w, "missing configured header", 401)
					return
				}
				var request mcpAssetRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					w.WriteHeader(400)
					return
				}
				if request.Method == "initialize" {
					initializations.Add(1)
				}
				if request.Method == "tools/list" {
					tools.Add(1)
				}
				if len(request.ID) == 0 {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(assetMCPResponse(request))
			}))
			defer server.Close()
			pkg := resourceFixture(t)
			servers := map[string]MCPServer{
				"atlas_http":  {Transport: "http", URL: "${ATLAS_ASSET_ENDPOINT}", Headers: map[string]string{"Authorization": "Bearer ${ATLAS_ASSET_TOKEN}"}},
				"atlas_stdio": {Command: helper, Args: []string{"-test.run=^TestAssetMCPProcess$"}, Env: map[string]string{"ATLAS_ASSET_MCP_HELPER": "1"}},
			}
			if provider == "pi" {
				s := servers["atlas_http"]
				s.URL = server.URL // Pi expands headers/env, not URL fields.
				servers["atlas_http"] = s
			}
			data, _ := json.Marshal(servers)
			writeAsset(t, pkg.Root, "mcp.json", string(data), 0600)
			root := t.TempDir()
			cwd, home := filepath.Join(root, "project"), filepath.Join(root, "home")
			for _, dir := range []string{cwd, home} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			c, err := RenderAt(cwd, pkg, provider, "project")
			if err != nil {
				t.Fatal(err)
			}
			plan, err := install.Plan(cwd, []install.Contribution{c})
			if err != nil {
				t.Fatal(err)
			}
			if err := install.Apply(cwd, plan); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{
				"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local/share"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local/state"),
				"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "PI_CODING_AGENT_DIR": filepath.Join(home, ".pi/agent"),
				"ATLAS_ASSET_TOKEN": "package-fixture-token", "ATLAS_ASSET_ENDPOINT": server.URL, "NO_COLOR": "1", "OPENCODE_DISABLE_AUTOUPDATE": "true", "OPENCODE_DISABLE_MODELS_FETCH": "true",
			}
			args := []string{"mcp", "list"}
			if provider == "claude" {
				// Explicit test-only native approval. Installation itself never
				// writes trust/approval state for a user.
				writeAsset(t, home, ".claude/settings.json", `{"enabledMcpjsonServers":["atlas_http","atlas_stdio"]}`, 0600)
			}
			if provider == "pi" {
				trust, _ := json.Marshal(map[string]bool{cwd: true})
				writeAsset(t, home, ".pi/agent/trust.json", string(trust), 0600)
				args = []string{"mcp", "list", "--json"}
			}
			var output string
			if provider == "opencode" {
				output = checkOpenCodeNativeResources(t, binary, cwd, env, "research", true)
			} else {
				output = nativeAssetCommand(t, binary, cwd, env, args...)
			}
			if !strings.Contains(output, "atlas_http") || !strings.Contains(output, "atlas_stdio") || initializations.Load() == 0 {
				t.Fatalf("native package MCP connections missing (HTTP initializations=%d tools=%d): %s", initializations.Load(), tools.Load(), output)
			}
			if strings.Contains(strings.ToLower(output), "failed") || strings.Contains(output, "Pending approval") || strings.Contains(output, `"status":"error"`) {
				t.Fatalf("native MCP reported unsuccessful connection: %s", output)
			}
			t.Logf("%s native MCP stdio + authenticated HTTP discovery: %s", provider, strings.TrimSpace(output))
			if provider == "pi" {
				checkPiNativeSkill(t, binary, cwd, env, "research", true)
			}
			if provider == "claude" {
				checkClaudeNativeSkill(t, binary, cwd, env, "research", true)
			}
		})
	}
}

// OpenCode's terminal commands normally contact its machine-wide service. An
// isolated serve process uses the same native loader with its own temporary
// directories and an ephemeral port, keeping tests out of that shared service.
func nativeOpenCodeGet(t *testing.T, binary, cwd string, env map[string]string) (func(string) string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	env["OPENCODE_PASSWORD"] = "asset-fixture-password"
	env["OPENCODE_SERVER_PASSWORD"] = "asset-fixture-password"
	env["OPENCODE_CONFIG_DIR"] = filepath.Join(env["HOME"], ".config/opencode")
	env["OPENCODE_CONFIG"], env["OPENCODE_CONFIG_CONTENT"], env["OPENCODE_CLI_CONFIG_CONTENT"] = "", "", ""
	cmd := process.Command(ctx, harness.Config{Command: []string{binary}, Env: env}, cwd, "serve", "--hostname", "127.0.0.1", "--port", "0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	cleanup := func() { once.Do(func() { cancel(); _ = cmd.Wait(); _ = process.KillGroup(cmd) }) }
	t.Cleanup(cleanup)
	scanner := bufio.NewScanner(stdout)
	base := ""
	for scanner.Scan() {
		if index := strings.Index(scanner.Text(), "http://127.0.0.1:"); index >= 0 {
			base = strings.TrimSpace(scanner.Text()[index:])
			break
		}
	}
	if base == "" {
		t.Fatalf("OpenCode failed to start: %s", stderr.String())
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	get := func(path string) string {
		req, err := http.NewRequestWithContext(ctx, "GET", base+path+"?"+url.Values{"location[directory]": {cwd}}.Encode(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.SetBasicAuth("opencode", env["OPENCODE_PASSWORD"])
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("OpenCode resource inventory %s: %d %v %s", path, response.StatusCode, err, body)
		}
		return string(body)
	}
	return get, cleanup
}

func checkOpenCodeNativeResources(t *testing.T, binary, cwd string, env map[string]string, skill string, installed bool) string {
	t.Helper()
	get, cleanup := nativeOpenCodeGet(t, binary, cwd, env)
	defer cleanup()
	deadline := time.Now().Add(10 * time.Second)
	for {
		skills := get("/api/skill")
		body := get("/api/mcp")
		if installed && strings.Count(body, "connected") >= 2 && strings.Contains(skills, skill) || !installed && !strings.Contains(body, "atlas_") && !strings.Contains(skills, skill) {
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("OpenCode native resources did not load: MCP=%s skills=%s", body, skills)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func checkPiNativeSkill(t *testing.T, binary, cwd string, env map[string]string, skill string, installed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := process.Command(ctx, harness.Config{Command: []string{binary}, Env: env}, cwd, "--mode", "rpc", "--approve", "--offline")
	cmd.Dir, cmd.Env = cwd, process.Environment(env)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); cancel(); _ = cmd.Wait(); _ = process.KillGroup(cmd) }()
	_, _ = fmt.Fprintln(input, `{"id":"asset-skills","type":"get_commands"}`)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var response struct {
			ID      string          `json:"id"`
			Success bool            `json:"success"`
			Data    json.RawMessage `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != "asset-skills" {
			continue
		}
		if !response.Success || bytes.Contains(response.Data, []byte(skill)) != installed {
			t.Fatalf("Pi native skill command missing: %s", scanner.Bytes())
		}
		t.Logf("Pi native command inventory: %q present=%v", skill, installed)
		return
	}
	t.Fatalf("Pi command discovery failed: %v %s", scanner.Err(), stderr.String())
}

func checkClaudeNativeSkill(t *testing.T, binary, cwd string, env map[string]string, skill string, installed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := process.Command(ctx, harness.Config{Command: []string{binary}, Env: env}, cwd, "--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); cancel(); _ = cmd.Wait(); _ = process.KillGroup(cmd) }()
	_, _ = fmt.Fprintln(input, `{"type":"control_request","request_id":"asset-skills","request":{"subtype":"initialize","hooks":{}}}`)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var response struct {
			Type     string `json:"type"`
			Response struct {
				ID      string          `json:"request_id"`
				Subtype string          `json:"subtype"`
				Data    json.RawMessage `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.Type != "control_response" || response.Response.ID != "asset-skills" {
			continue
		}
		if response.Response.Subtype != "success" || bytes.Contains(response.Response.Data, []byte(`"name":"`+skill+`"`)) != installed {
			t.Fatalf("Claude native skill command missing: %s", scanner.Bytes())
		}
		t.Logf("Claude native skill inventory: %q present=%v", skill, installed)
		return
	}
	t.Fatalf("Claude command discovery failed: %v %s", scanner.Err(), stderr.String())
}
