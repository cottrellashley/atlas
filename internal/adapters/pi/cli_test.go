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

func TestInstalledPiNativeCLIDiscoveryAcrossProjects(t *testing.T) {
	fixture := &modelFixture{t: t}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	c := newCLIFixture(t, d, cwd, root)
	c.must("init", "researcher", "--harness", "pi", "--no-open")
	for _, rel := range []string{"AGENTS.md", ".pi/skills/research/SKILL.md", ".pi/settings.json", ".pi/mcp.json", ".atlas/project.json"} {
		if _, err := os.Stat(filepath.Join(cwd, rel)); err != nil {
			t.Fatalf("init did not create %s: %v", rel, err)
		}
	}
	if out := c.must("agent", "list", "--json"); !strings.Contains(out, "researcher") {
		t.Fatalf("agent templates missing researcher: %s", out)
	}
	if out := c.must("models", "--harness", "pi", "--json"); !strings.Contains(out, "atlas-fixture/test-model") {
		t.Fatalf("CLI catalog: %s", out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	approved := true
	d.(*Driver).opts.Approve = &approved
	first, err := d.Run(ctx, harness.RunRequest{CWD: cwd, SessionID: "external-native-fixture", Prompt: "/skill:research Read facts.txt and answer.", Model: "atlas-fixture/test-model"}, nil, nil)
	if err != nil || first.Handle == nil {
		t.Fatalf("native conversation: %#v %v", first, err)
	}
	otherCWD := filepath.Join(root, "another-project")
	if err := os.MkdirAll(otherCWD, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := d.Run(ctx, harness.RunRequest{CWD: otherCWD, SessionID: "other-native-fixture", Prompt: "A separate project conversation.", Model: "atlas-fixture/test-model"}, nil, nil)
	if err != nil || other.Handle == nil {
		t.Fatalf("second native conversation: %#v %v", other, err)
	}
	type group struct {
		Harness  string                  `json:"harness"`
		Sessions []harness.NativeSession `json:"sessions"`
		Error    string                  `json:"error"`
	}
	list := func(args ...string) []harness.NativeSession {
		t.Helper()
		var groups []group
		argv := append([]string{"session", "list", "--harness", "pi", "--json"}, args...)
		out := c.must(argv...)
		if err := json.Unmarshal([]byte(out), &groups); err != nil || len(groups) != 1 || groups[0].Harness != "pi" || groups[0].Error != "" {
			t.Fatalf("native groups: %s %v", out, err)
		}
		return groups[0].Sessions
	}
	all := list()
	if len(all) != 2 {
		t.Fatalf("global native history omitted another project: %#v", all)
	}
	filtered := list("--cwd", cwd)
	if len(filtered) != 1 || filtered[0].ID != "external-native-fixture" || filtered[0].CWD != cwd {
		t.Fatalf("filtered native identity: %#v", filtered)
	}
	actualHandle, handleErr := decodeHandle(filtered[0].Handle)
	expectedHandle, originalErr := decodeHandle(*first.Handle)
	if handleErr != nil || originalErr != nil || actualHandle != expectedHandle {
		t.Fatalf("discovered native handle changed: %#v != %#v (%v, %v)", actualHandle, expectedHandle, handleErr, originalErr)
	}
	searched := list("--search", "other-native-fixture")
	if len(searched) != 1 || searched[0].ID != "other-native-fixture" {
		t.Fatalf("native search: %#v", searched)
	}
	if _, err := os.Stat(filepath.Join(root, "atlas-home", "sessions")); !os.IsNotExist(err) {
		t.Fatalf("native browsing created Atlas session records: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.observedTool {
		t.Fatal("native session fixture did not execute its tool")
	}
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
	c.must("init", "--harness", "pi", "--template", template, "--no-open")
	approved := true
	d.(*Driver).opts.Approve = &approved
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var logs strings.Builder
	_, err = d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "/skill:probe Verify the configured tool.", Model: "atlas-fixture/test-model"}, func(event harness.Event) error {
		logs.Write(harness.JSON(event))
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "atlas-pi-mcp-executed-marker") {
		t.Fatalf("real MCP result missing in native event log: %s", logs.String())
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
