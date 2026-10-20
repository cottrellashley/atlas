package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
)

type cliFixture struct {
	t      *testing.T
	binary string
	env    []string
	cwd    string
}

func newCLIFixture(t *testing.T, driver harness.Driver, cwd, root string) *cliFixture {
	t.Helper()
	_, source, _, _ := runtime.Caller(0)
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	binary := filepath.Join(root, "atlas")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.Command(goBinary, "build", "-o", binary, "./cmd/atlas")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build current Atlas: %v\n%s", err, output)
	}
	home := filepath.Join(root, "atlas-home")
	writeTestJSON(t, filepath.Join(home, "config.json"), map[string]any{"schemaVersion": 1, "harnesses": map[string]any{"pi": driver.(*Driver).cfg}})
	c := &cliFixture{t: t, binary: binary, cwd: cwd, env: process.Environment(map[string]string{"ATLAS_HOME": home})}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "daemon", "stop")
		cmd.Env = c.env
		cmd.Dir = c.cwd
		_ = cmd.Run()
	})
	return c
}

func (c *cliFixture) command(args ...string) (string, error) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Env = c.env
	cmd.Dir = c.cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("atlas %v: %w; stderr: %s", args, err, stderr.String())
	}
	return string(out), nil
}
func (c *cliFixture) must(args ...string) string {
	c.t.Helper()
	out, err := c.command(args...)
	if err != nil {
		c.t.Fatal(err)
	}
	return out
}

func TestInstalledPiFullCLIBackgroundResumeDiscoveryImport(t *testing.T) {
	fixture := &modelFixture{t: t}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	c := newCLIFixture(t, d, cwd, root)
	c.must("init", "--harness", "pi", "--template", "researcher")
	for _, rel := range []string{"AGENTS.md", ".pi/skills/research/SKILL.md", ".pi/settings.json", ".pi/mcp.json", ".atlas/project.json"} {
		if _, err := os.Stat(filepath.Join(cwd, rel)); err != nil {
			t.Fatalf("init did not create %s: %v", rel, err)
		}
	}
	if out := c.must("models", "--harness", "pi", "--json"); !strings.Contains(out, "atlas-fixture/test-model") {
		t.Fatalf("CLI catalog: %s", out)
	}
	id := strings.TrimSpace(c.must("run", "--harness", "pi", "--model", "atlas-fixture/test-model", "--background", "--trust-project", "/skill:research Read facts.txt and answer."))
	if len(id) != 32 {
		t.Fatalf("background did not return managed ID: %q", id)
	}
	var finished struct {
		State  string          `json:"state"`
		Resume *harness.Handle `json:"resume"`
	}
	if err := json.Unmarshal([]byte(c.must("wait", id, "--timeout", "20s", "--json")), &finished); err != nil {
		t.Fatal(err)
	}
	if finished.State != "idle" || finished.Resume == nil {
		t.Fatalf("background did not persist completion: %#v", finished)
	}
	logs := c.must("logs", id, "--json")
	if !strings.Contains(logs, "fixture-first-answer") || !strings.Contains(logs, `"type":"tool"`) {
		t.Fatalf("CLI logs missing native output/tool: %s", logs)
	}
	c.must("send", id, "Recall earlier answer.", "--background")
	c.must("wait", id, "--timeout", "20s")
	if !strings.Contains(c.must("logs", id), "fixture-resumed-answer") {
		t.Fatal("CLI send did not resume native conversation")
	}
	var discovered []harness.NativeSession
	if err := json.Unmarshal([]byte(c.must("sessions", "discover", "--harness", "pi", "--json")), &discovered); err != nil || len(discovered) != 1 {
		t.Fatalf("CLI discover: %#v %v", discovered, err)
	}
	// Create a second native conversation outside Atlas's manager, then import it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	native, err := d.Run(ctx, harness.RunRequest{CWD: cwd, SessionID: "external-native-fixture", Prompt: "This conversation originated outside Atlas.", Model: "atlas-fixture/test-model"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if native.Handle == nil {
		t.Fatal("missing external native handle")
	}
	imported := strings.TrimSpace(c.must("sessions", "import", "--harness", "pi", "--handle", "external-native-fixture"))
	if imported == id || len(imported) != 32 {
		t.Fatalf("import did not create separate managed identity: %q", imported)
	}
	c.must("send", imported, "Continue imported session.", "--background")
	c.must("wait", imported, "--timeout", "20s")
	if !strings.Contains(c.must("sessions", "list", "--json"), imported) {
		t.Fatal("import missing in managed listing")
	}
	if again := strings.TrimSpace(c.must("sessions", "import", "--harness", "pi", "--handle", "external-native-fixture")); again != imported {
		t.Fatalf("import created duplicate native ownership: %q != %q", again, imported)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.observedTool || !fixture.observedMemory {
		t.Fatalf("CLI flow bypassed native tool/context: tool=%v memory=%v", fixture.observedTool, fixture.observedMemory)
	}
	loadedSkill := false
	for _, request := range fixture.requests {
		for _, message := range request.Messages {
			if strings.Contains(string(message.Content), "State the question, scope and success criteria") {
				loadedSkill = true
			}
		}
	}
	if !loadedSkill {
		t.Fatal("generated researcher skill body was not expanded into native model request")
	}
}

func TestInstalledPiFullCLIPermissionsAndStop(t *testing.T) {
	fixture := &modelFixture{t: t}
	fixture.calls.Store(1)
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	extension := filepath.Join(root, "cli-approval.ts")
	source := `export default function(pi) { pi.on("before_agent_start",async(_event,ctx)=>{const accepted=await ctx.ui.confirm("Atlas CLI fixture","Allow fixture?");ctx.ui.notify(accepted?"fixture-allowed":"fixture-denied","info");}); }`
	if err := os.WriteFile(extension, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	d.(*Driver).cfg.Command = append(d.(*Driver).cfg.Command, "--extension", extension)
	c := newCLIFixture(t, d, cwd, root)
	for _, action := range []string{"allow", "deny"} {
		id := strings.TrimSpace(c.must("run", "--harness", "pi", "--model", "atlas-fixture/test-model", "--background", "Reply."))
		permissionID := waitCLIPermission(t, c, id)
		c.must("permissions", action, id, permissionID)
		c.must("wait", id, "--timeout", "20s")
		want := "fixture-allowed"
		if action == "deny" {
			want = "fixture-denied"
		}
		if logs := c.must("logs", id, "--json"); !strings.Contains(logs, want) {
			t.Fatalf("native extension did not receive %s: %s", action, logs)
		}
	}
	// Cancel while the real native extension is blocked waiting for a dialog.
	id := strings.TrimSpace(c.must("run", "--harness", "pi", "--model", "atlas-fixture/test-model", "--background", "Reply."))
	_ = waitCLIPermission(t, c, id)
	c.must("stop", id)
	var record struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(c.must("sessions", "show", id)), &record); err != nil {
		t.Fatal(err)
	}
	if record.State != "stopped" {
		t.Fatalf("stop returned before cancellation: %#v", record)
	}
}

func waitCLIPermission(t *testing.T, c *cliFixture, id string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var permissions []harness.Permission
		if err := json.Unmarshal([]byte(c.must("permissions", "list", id)), &permissions); err != nil {
			t.Fatal(err)
		}
		if len(permissions) > 0 {
			return permissions[0].ID
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("native extension permission did not arrive")
	return ""
}

func TestInstalledPiFullCLITemplateMCP(t *testing.T) {
	fixture := &modelFixture{t: t, toolName: "mcp__atlas_fixture__echo", toolArguments: "{}", toolMarker: "atlas-pi-mcp-executed-marker"}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	c := newCLIFixture(t, d, cwd, root)
	template := filepath.Join(root, "template")
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(template, "template.json"), map[string]any{
		"schemaVersion": 1, "id": "pi-mcp-fixture", "version": "1.0.0", "instructions": "instructions.md",
		"skills": []string{"skills/probe"}, "mcp": "mcp.json", "requires": []string{"skills", "mcp.stdio"},
		"agents":    map[string]any{"researcher": map[string]any{}},
		"harnesses": map[string]any{"pi": map[string]string{"overlay": "pi-overlay"}},
	})
	writeTestJSON(t, filepath.Join(template, "mcp.json"), map[string]any{"atlas_fixture": map[string]any{
		"transport": "stdio", "command": helper, "args": []string{"-test.run=^TestPiMCPFixtureProcess$"},
		"env": map[string]string{"ATLAS_PI_MCP_HELPER": "1"},
	}})
	// Exposure is a Pi-specific extension, preserved through a native overlay.
	writeTestJSON(t, filepath.Join(template, "pi-overlay/.pi/mcp.json"), map[string]any{"mcpServers": map[string]any{"atlas_fixture": map[string]string{"exposure": "direct"}}})
	if err = os.WriteFile(filepath.Join(template, "instructions.md"), []byte("# Fixture\nUse the project skill and MCP tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(template, "skills/probe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(template, "skills/probe/SKILL.md"), []byte("---\nname: probe\ndescription: Verify the Atlas MCP fixture.\n---\n\natlas-pi-loaded-custom-skill-marker\nCall the echo MCP tool.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.must("init", "--harness", "pi", "--template", template)
	id := strings.TrimSpace(c.must("run", "--harness", "pi", "--model", "atlas-fixture/test-model", "--background", "--trust-project", "/skill:probe Verify the configured tool."))
	c.must("wait", id, "--timeout", "20s")
	logs := c.must("logs", id, "--json")
	if !strings.Contains(logs, "atlas-pi-mcp-executed-marker") {
		t.Fatalf("real MCP result missing in native event log: %s", logs)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	declared, skill := false, false
	for _, request := range fixture.requests {
		for _, tool := range request.Tools {
			if tool.Function.Name == fixture.toolName {
				declared = true
			}
		}
		for _, message := range request.Messages {
			if strings.Contains(string(message.Content), "atlas-pi-loaded-custom-skill-marker") {
				skill = true
			}
		}
	}
	if !declared || !skill || !fixture.observedTool {
		t.Fatalf("native MCP template did not load or execute: declared=%v skill=%v executed=%v", declared, skill, fixture.observedTool)
	}
}

// This helper is a real stdio MCP server process used only by the native Pi
// fixture. It emits protocol JSON only, and exits before Go's test footer.
func TestPiMCPFixtureProcess(t *testing.T) {
	if os.Getenv("ATLAS_PI_MCP_HELPER") != "1" {
		t.Skip("stdio helper")
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &params)
			result = map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "atlas-pi-fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Return an Atlas verification marker.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "atlas-pi-mcp-executed-marker"}}}
		case "ping":
			result = map[string]any{}
		default:
			result = map[string]any{}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}
