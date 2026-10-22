package omp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/process"
	"github.com/cottrellashley/atlas/internal/project"
)

type mcpRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		ProtocolVersion string `json:"protocolVersion"`
	} `json:"params"`
}

func mcpResponse(req mcpRequest) map[string]any {
	result := any(map[string]any{})
	switch req.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": req.Params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "atlas-omp-fixture", "version": "1.0.0"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "omp_probe", "description": "Harmless native OMP resource probe", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
	}
	return map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
}

func TestOMPMCPProcess(t *testing.T) {
	if os.Getenv("ATLAS_OMP_MCP_HELPER") != "1" {
		t.Skip("native MCP subprocess helper")
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req mcpRequest
		if json.Unmarshal(scanner.Bytes(), &req) == nil && len(req.ID) > 0 {
			if req.Method == "tools/list" {
				if marker := os.Getenv("ATLAS_OMP_MCP_MARKER"); marker != "" {
					_ = os.WriteFile(marker, []byte("native tools/list received"), 0600)
				}
			}
			_ = json.NewEncoder(os.Stdout).Encode(mcpResponse(req))
		}
	}
	os.Exit(0)
}

type nativeRPC struct {
	in     io.WriteCloser
	frames chan json.RawMessage
	done   chan error
	cmd    *exec.Cmd
	stderr bytes.Buffer
}

func startNativeRPC(t *testing.T, binary, cwd, home string, env map[string]string, args []string) *nativeRPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	rpc := &nativeRPC{frames: make(chan json.RawMessage, 32), done: make(chan error, 1)}
	rpc.cmd = process.Command(ctx, harness.Config{Command: []string{binary}}, cwd, append(args, "--mode", "rpc", "--no-ui", "--no-title", "--no-lsp", "--no-pty")...)
	// Only synthetic credentials and isolated paths enter this test process.
	rpc.cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=dumb", "NO_COLOR=1", "OMP_PROFILE=", "PI_PROFILE=", "PI_CODING_AGENT_DIR=" + filepath.Join(home, ".omp/agent"), "OMP_MCP_STARTUP_TIMEOUT_MS=0", "PI_NO_TITLE=1", "PI_NOTIFICATIONS=off", "ATLAS_OMP_MCP_HELPER=1"}
	for key, value := range env {
		rpc.cmd.Env = append(rpc.cmd.Env, key+"="+value)
	}
	var err error
	rpc.in, err = rpc.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := rpc.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	rpc.cmd.Stderr = &rpc.stderr
	if err := rpc.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rpc.in.Close()
		process.KillGroup(rpc.cmd)
	})
	go func() {
		defer close(rpc.frames)
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 64<<10), 2<<20)
		for scan.Scan() {
			select {
			case rpc.frames <- append(json.RawMessage(nil), scan.Bytes()...):
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { rpc.done <- rpc.cmd.Wait() }()
	for {
		select {
		case frame, ok := <-rpc.frames:
			if !ok {
				<-rpc.done
				t.Fatalf("OMP exited before ready: %s", rpc.stderr.String())
			}
			var ready struct{ Type string }
			if json.Unmarshal(frame, &ready) == nil && ready.Type == "ready" {
				return rpc
			}
		case <-ctx.Done():
			t.Fatal("OMP did not become ready within 40 seconds")
		}
	}
}

func (r *nativeRPC) request(t *testing.T, kind string) json.RawMessage {
	t.Helper()
	if err := json.NewEncoder(r.in).Encode(map[string]string{"id": kind, "type": kind}); err != nil {
		t.Fatal(err)
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case frame, ok := <-r.frames:
			if !ok {
				t.Fatal("OMP closed its RPC output")
			}
			var reply struct {
				Type    string          `json:"type"`
				ID      string          `json:"id"`
				Success bool            `json:"success"`
				Error   string          `json:"error"`
				Data    json.RawMessage `json:"data"`
			}
			if json.Unmarshal(frame, &reply) == nil && reply.Type == "response" && reply.ID == kind {
				if !reply.Success {
					t.Fatalf("OMP %s failed: %s", kind, reply.Error)
				}
				return reply.Data
			}
		case <-timeout.C:
			t.Fatalf("OMP %s did not respond", kind)
		}
	}
}

func (r *nativeRPC) close(t *testing.T) {
	t.Helper()
	r.in.Close()
	select {
	case err := <-r.done:
		if err != nil {
			t.Fatalf("OMP shutdown: %v\n%s", err, r.stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OMP did not settle after input closed")
	}
}

func TestInstalledOMPResourcesAndNativeSessions(t *testing.T) {
	if os.Getenv("ATLAS_TEST_OMP") != "1" {
		t.Skip("set ATLAS_TEST_OMP=1 and ATLAS_OMP_BINARY to the installed native executable")
	}
	binary := os.Getenv("ATLAS_OMP_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("ATLAS_OMP_BINARY must name the absolute installed OMP binary, not an auto-update wrapper")
	}
	f, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	var magic [2]byte
	_, err = f.Read(magic[:])
	f.Close()
	if err != nil || string(magic[:]) == "#!" {
		t.Fatal("native gate refuses shell wrappers that might mutate global tool-manager configuration")
	}
	d, home, cwd := testDriver(t)
	d.cfg.Command, d.cfg.Model = []string{binary}, "atlas-fixture/test-model"
	writeFixture(t, filepath.Join(home, ".omp/agent/models.yml"), "providers:\n  atlas-fixture:\n    baseUrl: http://127.0.0.1:1/v1\n    apiKey: fixture-only\n    api: openai-completions\n    models:\n      - id: test-model\n        name: Local fixture\n        reasoning: false\n        input: [text]\n        contextWindow: 8192\n        maxTokens: 1024\n        cost: {input: 0, output: 0, cacheRead: 0, cacheWrite: 0}\n")
	writeFixture(t, filepath.Join(home, ".omp/agent/config.yml"), "autoResume: true\nmodelRoles:\n  default: atlas-fixture/test-model\n")
	var initializations, toolLists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer omp-fixture-token" {
			http.Error(w, "missing fixture header", http.StatusUnauthorized)
			return
		}
		var req mcpRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Method == "initialize" {
			initializations.Add(1)
		}
		if req.Method == "tools/list" {
			toolLists.Add(1)
		}
		if len(req.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mcpResponse(req))
	}))
	defer server.Close()
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(home, "atlas-template")
	writeFixture(t, filepath.Join(template, "template.json"), `{"schemaVersion":1,"id":"omp-fixture","version":"1.0.0","instructions":"AGENTS.md","skills":["skills/atlas-omp-probe"],"mcp":"servers.json"}`)
	writeFixture(t, filepath.Join(template, "AGENTS.md"), "ATLAS_OMP_CONTEXT_PROBE: this is an isolated native resource test.\n")
	writeFixture(t, filepath.Join(template, "skills/atlas-omp-probe/SKILL.md"), "---\nname: atlas-omp-probe\ndescription: Inspect the harmless Atlas OMP fixture.\n---\nReport fixture information without making changes.\n")
	servers := map[string]project.MCPServer{
		"atlas_http":  {Transport: "http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer ${ATLAS_OMP_TEST_TOKEN}"}},
		"atlas_stdio": {Transport: "stdio", Command: helper, Args: []string{"-test.run=^TestOMPMCPProcess$"}, Env: map[string]string{"ATLAS_OMP_MCP_HELPER": "1", "ATLAS_OMP_MCP_MARKER": filepath.Join(home, "stdio-ready")}},
	}
	writeFixture(t, filepath.Join(template, "servers.json"), string(harness.JSON(servers)))
	plan, err := project.Build(cwd, home, "omp", "omp", template)
	if err != nil {
		t.Fatal(err)
	}
	if err := project.Apply(cwd, plan); err != nil {
		t.Fatal(err)
	}
	dir, err := d.projectSessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	oldID := "019c625b-b900-7000-8000-000000000001"
	writeFixture(t, filepath.Join(dir, "2026-10-20_old.jsonl"), transcript(oldID, cwd, "Existing native session", true))
	args, err := d.InteractiveArgsForCWD(cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	extra, err := d.InteractiveOptions("off", "", "always-ask")
	if err != nil {
		t.Fatal(err)
	}
	rpc := startNativeRPC(t, binary, cwd, home, map[string]string{"ATLAS_OMP_TEST_TOKEN": "omp-fixture-token"}, append(args, extra...))
	state := rpc.request(t, "get_state")
	var fresh struct {
		SessionID   string                  `json:"sessionId"`
		SessionFile string                  `json:"sessionFile"`
		System      []string                `json:"systemPrompt"`
		Tools       []struct{ Name string } `json:"dumpTools"`
	}
	if err := json.Unmarshal(state, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.SessionID == "" || fresh.SessionID == oldID {
		t.Fatalf("atlas new implicitly resumed an old OMP conversation: %q", fresh.SessionID)
	}
	prompt := strings.Join(fresh.System, "\n")
	if !strings.Contains(prompt, "ATLAS_OMP_CONTEXT_PROBE") || !strings.Contains(prompt, "atlas-omp-probe") {
		t.Fatal("OMP did not discover Atlas-rendered instructions and skill")
	}
	// OMP can mount MCP tools under xd:// instead of top-level function names.
	// Observe each server's real native handshake rather than assuming a tool UI.
	if _, err := os.Stat(filepath.Join(home, "stdio-ready")); err != nil || initializations.Load() == 0 || toolLists.Load() == 0 {
		t.Fatalf("native stdio/HTTP MCP handshake missing: stdio=%v HTTP init=%d tools=%d", err, initializations.Load(), toolLists.Load())
	}
	// OMP's explicit new_session command persists its otherwise lazy empty header.
	rpc.request(t, "new_session")
	if err := json.Unmarshal(rpc.request(t, "get_state"), &fresh); err != nil {
		t.Fatal(err)
	}
	rpc.close(t)
	found, err := d.Find(context.Background(), cwd, fresh.SessionID)
	if err != nil || len(found) != 1 || found[0].ID != fresh.SessionID {
		t.Fatalf("native-created OMP history not discoverable: %#v, %v", found, err)
	}
	args, err = d.InteractiveArgsForCWD(cwd, &found[0])
	if err != nil {
		t.Fatal(err)
	}
	rpc = startNativeRPC(t, binary, cwd, home, map[string]string{"ATLAS_OMP_TEST_TOKEN": "omp-fixture-token"}, args)
	var resumed struct{ SessionID string }
	if err := json.Unmarshal(rpc.request(t, "get_state"), &resumed); err != nil || resumed.SessionID != fresh.SessionID {
		t.Fatalf("native resume selected a different session: %q, %v", resumed.SessionID, err)
	}
	rpc.close(t)
	t.Log("Installed OMP discovered Atlas instructions, skills, stdio and authenticated HTTP MCP; fresh session and exact resume verified without inference")
}
