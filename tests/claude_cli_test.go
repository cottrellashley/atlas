package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/store"
)

// TestClaudeCLI exercises the built CLI and resident daemon through subprocesses.
// It preserves the user's native authentication and uses an isolated ATLAS_HOME.
func TestClaudeCLI(t *testing.T) {
	if os.Getenv("ATLAS_CLAUDE_E2E") != "1" {
		t.Skip("set ATLAS_CLAUDE_E2E=1 for real native Claude CLI tests")
	}
	_, source, _, _ := runtime.Caller(0)
	repo := filepath.Dir(filepath.Dir(source))
	root, err := os.MkdirTemp("", "atlas-claude-cli-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "project")
	for _, p := range []string{home, cwd} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "atlas")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	build := exec.Command(goBinary, "build", "-o", bin, "./cmd/atlas")
	build.Dir = repo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "ATLAS_HOME=") {
			env = append(env, v)
		}
	}
	env = append(env, "ATLAS_HOME="+home)
	call := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		cmd.Dir = cwd
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			return stdout.Bytes(), fmt.Errorf("atlas %v: %w; %s", args, err, stderr.String())
		}
		return stdout.Bytes(), nil
	}
	defer func() {
		if _, err := call("daemon", "stop"); err != nil {
			t.Logf("daemon cleanup: %v", err)
		}
	}()
	must := func(args ...string) []byte {
		t.Helper()
		out, err := call(args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	readRecord := func(args ...string) store.Record {
		t.Helper()
		out := must(args...)
		var r store.Record
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatalf("record decode: %v: %s", err, out)
		}
		return r
	}
	must("init", "--harness", "claude", "--template", "researcher", "--cwd", cwd)
	for _, file := range []string{"AGENTS.md", "CLAUDE.md", ".claude/skills/research/SKILL.md", ".mcp.json", ".atlas/project.json"} {
		if _, err := os.Stat(filepath.Join(cwd, file)); err != nil {
			t.Fatal(err)
		}
	}
	instructions, err := os.OpenFile(filepath.Join(cwd, "AGENTS.md"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = instructions.WriteString("\nFor the Atlas native integration check, the project token is ATLAS_PROJECT_382.\n")
	instructions.Close()
	if err != nil {
		t.Fatal(err)
	}
	nonce := fmt.Sprintf("atlas-retain-%d", time.Now().UnixNano())
	skill := "---\nname: research\ndescription: Run the small Atlas native integration check.\n---\n\nCall the MCP tool mcp__atlas_probe__atlas_probe exactly once. Then reply with its token, the project token from the project instructions, the literal ATLAS_SKILL_917, and these arguments: $ARGUMENTS. Do not run any other tools. Remember the arguments for the next turn.\n"
	if err := os.WriteFile(filepath.Join(cwd, ".claude/skills/research/SKILL.md"), []byte(skill), 0600); err != nil {
		t.Fatal(err)
	}
	invocations := filepath.Join(root, "mcp-invoked")
	mcp := map[string]any{"mcpServers": map[string]any{"atlas_probe": map[string]any{"command": os.Args[0], "args": []string{"-test.run=^TestClaudeCLIMCPServer$"}, "env": map[string]string{"ATLAS_CLAUDE_MCP_HELPER": "1", "ATLAS_MCP_INVOCATIONS": invocations}}}}
	data, _ := json.Marshal(mcp)
	if err := os.WriteFile(filepath.Join(cwd, ".mcp.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	settings := `{"enableAllProjectMcpServers":true,"permissions":{"allow":["Skill","mcp__atlas_probe__atlas_probe"],"ask":["Bash"]}}`
	if err := os.WriteFile(filepath.Join(cwd, ".claude/settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}
	var catalog harness.Catalog
	if err := json.Unmarshal(must("models", "--cwd", cwd, "--json"), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) == 0 {
		t.Fatal("empty Claude catalog")
	}
	t.Logf("catalog %s (%d models)", catalog.Source, len(catalog.Models))
	r := readRecord("run", "--cwd", cwd, "--agent", "researcher", "--model", "haiku", "--permission-mode", "manual", "--background", "--json", "/research "+nonce)
	if r.ID == "" {
		t.Fatal("background run returned no ID")
	}
	// Native managed/user policy may still ask despite a project allow rule.
	// Exercise the actual host approval rather than assume project policy wins.
	firstDeadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(firstDeadline) {
		current := readRecord("sessions", "show", r.ID)
		for _, p := range current.Permissions {
			if p.Tool != "mcp__atlas_probe__atlas_probe" && p.Tool != "Skill" {
				t.Fatalf("unexpected integration-check tool approval: %s", p.Tool)
			}
			must("permissions", "allow", r.ID, p.ID)
		}
		if current.State != "running" && current.State != "waiting_permission" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	r = readRecord("wait", r.ID, "--timeout", "90s", "--json")
	if r.State != "idle" || r.Resume == nil {
		t.Fatalf("first turn: %#v", r)
	}
	logs := string(must("logs", r.ID))
	for _, token := range []string{"ATLAS_MCP_624", "ATLAS_PROJECT_382", "ATLAS_SKILL_917", nonce} {
		if !strings.Contains(logs, token) {
			t.Fatalf("missing %s in logs: %s", token, logs)
		}
	}
	if _, err := os.Stat(invocations); err != nil {
		t.Fatalf("native MCP tool was not called: %v", err)
	}
	var handle struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(r.Resume.Data, &handle); err != nil {
		t.Fatal(err)
	}
	if handle.SessionID == "" {
		t.Fatal("native identity missing")
	}
	t.Logf("managed %s -> native %s; skill/MCP/project instructions passed", r.ID, handle.SessionID)
	before := r.Seq
	must("daemon", "stop")
	must("daemon", "start")
	restored := readRecord("sessions", "show", r.ID)
	if restored.ID != r.ID || restored.Resume == nil || string(restored.Resume.Data) != string(r.Resume.Data) {
		t.Fatalf("restart lost identity: %#v", restored)
	}
	must("resume", r.ID, "What exact argument beginning atlas-retain- did I pass to /research in the previous turn? Reply only with that argument. Do not call any tools.", "--background")
	r = readRecord("wait", r.ID, "--timeout", "90s", "--json")
	logs = string(must("logs", r.ID, "--after", fmt.Sprint(before)))
	if !strings.Contains(logs, nonce) {
		t.Fatalf("resume lost prior conversation: %s", logs)
	}
	var native []harness.NativeSession
	if err := json.Unmarshal(must("sessions", "discover", "--harness", "claude", "--cwd", cwd, "--json"), &native); err != nil {
		t.Fatal(err)
	}
	if len(native) != 1 || native[0].ID != handle.SessionID {
		t.Fatalf("native discovery: %#v", native)
	}
	for i := 0; i < 2; i++ {
		imported := readRecord("sessions", "import", "--harness", "claude", "--cwd", cwd, "--handle", handle.SessionID, "--json")
		if imported.ID != r.ID {
			t.Fatalf("duplicate import created %s instead of %s", imported.ID, r.ID)
		}
	}
	t.Log("daemon restart, conversation resume, native discovery and import dedup passed")

	// Tool permission is exercised through independent CLI invocations while
	// the original background owner remains alive.
	must("send", r.ID, "Use the Bash tool to execute exactly: printf ATLAS_CLI_PERMISSION. Report only the output. Do not use any other tool.", "--background")
	permission := awaitClaudeCLIPermission(t, func() []byte { return must("permissions", "list", r.ID) })
	must("permissions", "allow", r.ID, permission.ID)
	r = readRecord("wait", r.ID, "--timeout", "90s", "--json")
	if !strings.Contains(string(must("logs", r.ID)), "ATLAS_CLI_PERMISSION") {
		t.Fatal("permission-approved result absent")
	}
	must("send", r.ID, "Use the Bash tool to run exactly: sleep 20. Do not perform any other action before it.", "--background")
	_ = awaitClaudeCLIPermission(t, func() []byte { return must("permissions", "list", r.ID) })
	started := time.Now()
	must("stop", r.ID)
	stopped := readRecord("sessions", "show", r.ID)
	if stopped.State != "stopped" || len(stopped.Permissions) != 0 {
		t.Fatalf("stop did not settle %#v", stopped)
	}
	if time.Since(started) > 15*time.Second {
		t.Fatal("stop cleanup exceeded bound")
	}
	t.Log("CLI approval and stop while permission-blocked passed")
}

func awaitClaudeCLIPermission(t *testing.T, read func() []byte) harness.Permission {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		var permissions []harness.Permission
		if err := json.Unmarshal(read(), &permissions); err != nil {
			t.Fatal(err)
		}
		if len(permissions) > 0 {
			return permissions[0]
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("native permission did not arrive")
	return harness.Permission{}
}

// This subprocess is a deterministic stdio MCP server used by the actual
// Claude process. It has no network access or credentials of its own.
func TestClaudeCLIMCPServer(t *testing.T) {
	if os.Getenv("ATLAS_CLAUDE_MCP_HELPER") != "1" {
		t.Skip("MCP fixture subprocess")
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			var params struct {
				Version string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			result = map[string]any{"protocolVersion": params.Version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "atlas-test", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "atlas_probe", "description": "Return the deterministic Atlas integration check token.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			_ = os.WriteFile(os.Getenv("ATLAS_MCP_INVOCATIONS"), []byte("called\n"), 0600)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "ATLAS_MCP_624"}}}
		case "ping":
			result = map[string]any{}
		default:
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			continue
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}
