package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

// A real local MCP server with a deterministic, harmless tool. Only the model
// backend is simulated: native config discovery and MCP execution are real.
func testOpenCodeNativeConfiguration(t *testing.T, env map[string]string, root string) {
	t.Helper()
	var calls atomic.Int32
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "atlas-fixture", "version": "1.0.0"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "atlas_probe", "description": "Read the local Atlas test marker", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			calls.Add(1)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "ATLAS_MCP_RESULT_SENTINEL"}}}
		case "ping":
			result = map[string]any{}
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "Method not found"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer mcp.Close()
	cwd := filepath.Join(root, "native-config-project")
	skillPath := filepath.Join(cwd, ".opencode", "skills", "atlas-probe", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		filepath.Join(cwd, "AGENTS.md"):     []byte("# Project instructions\nATLAS_PROJECT_INSTRUCTIONS_SENTINEL\n"),
		skillPath:                           []byte("---\nname: atlas-probe\ndescription: Inspect the isolated Atlas project fixture.\n---\n\nATLAS_SKILL_BODY_SENTINEL\n"),
		filepath.Join(cwd, "opencode.json"): harness.JSON(map[string]any{"$schema": "https://opencode.ai/config.json", "mcp": map[string]any{"atlas_fixture": map[string]any{"type": "remote", "url": mcp.URL, "enabled": true}}}),
	}
	for path, content := range files {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	drv, err := New(harness.Config{Adapter: "opencode", Env: env})
	if err != nil {
		t.Fatal(err)
	}
	d := drv.(*driver)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	s, err := d.start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := s.readyLocation(ctx, cwd); err != nil {
		t.Fatal(err)
	}
	var skills json.RawMessage
	if err := s.json(ctx, "GET", "/api/skill"+locationQuery(cwd), nil, &skills); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skills), "atlas-probe") || !strings.Contains(string(skills), "ATLAS_SKILL_BODY_SENTINEL") {
		t.Fatalf("native skill discovery omitted project skill: %s", skills)
	}
	var servers json.RawMessage
	if err := s.json(ctx, "GET", "/api/mcp"+locationQuery(cwd), nil, &servers); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(servers), "atlas_fixture") || !strings.Contains(string(servers), "connected") {
		t.Fatalf("native MCP discovery/connection: %s", servers)
	}
	s.close()
	var toolEvents []json.RawMessage
	result, err := d.Run(ctx, harness.RunRequest{SessionID: "native-config-test", CWD: cwd, Prompt: "ATLAS_MCP_SCENARIO: use the local probe once.", Model: "atlasfixture/echo", PermissionMode: "allow", StateDir: root}, func(e harness.Event) error {
		if e.Type == "tool" {
			toolEvents = append(toolEvents, e.Data)
		}
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "ATLAS_FIXTURE_OK" || calls.Load() != 1 {
		t.Fatalf("native MCP execution result=%+v calls=%d tools=%s", result, calls.Load(), harness.JSON(toolEvents))
	}
	t.Log("native AGENTS.md, .opencode/skills discovery, opencode.json MCP connection and actual MCP tool execution passed")
}
