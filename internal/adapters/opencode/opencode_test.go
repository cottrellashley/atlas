package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func TestSSEFramingAndTextDeduplication(t *testing.T) {
	var out []harness.Event
	tr := newTracker("ses_test", func(e harness.Event) error { out = append(out, e); return nil })
	input := ": heartbeat\r\n\r\nevent: message\r\ndata: {\"type\":\"session.text.started\",\"data\":{\"sessionID\":\"ses_test\",\"assistantMessageID\":\"m1\",\"ordinal\":0}}\r\n\r\n" +
		"data: {\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_test\",\"assistantMessageID\":\"m1\",\"ordinal\":0,\"delta\":\"hello\u2028world\"}}\n\n"
	if err := readSSE(strings.NewReader(input), tr.observe); err != nil {
		t.Fatal(err)
	}
	if err := tr.appendText("m1:text:0", "text", "hello\u2028world!", false); err != nil {
		t.Fatal(err)
	}
	if got := tr.text.String(); got != "hello\u2028world!" {
		t.Fatalf("text %q", got)
	}
	if len(out) != 2 || out[1].Text != "!" {
		t.Fatalf("duplicate snapshot emission: %+v", out)
	}
	if err := tr.appendText("m1:text:0", "text", "different and longer text", false); err == nil {
		t.Fatal("expected conflicting history error")
	}
}
func TestRunFailureDoesNotBecomeSuccessOnAcknowledgment(t *testing.T) {
	tr := newTracker("ses_test", func(harness.Event) error { return nil })
	tr.sequence = 8
	old := nativeEvent{Type: "session.execution.succeeded", Data: harness.JSON(map[string]string{"sessionID": "ses_test"})}
	old.Durable.Seq = 7
	if err := tr.observe(old); err != nil {
		t.Fatal(err)
	}
	if tr.terminal != nil {
		t.Fatal("old turn completed resumed turn")
	}
	failed := nativeEvent{Type: "session.execution.failed", Data: harness.JSON(map[string]any{"sessionID": "ses_test", "error": map[string]string{"message": "provider denied"}})}
	failed.Durable.Seq = 10
	if err := tr.observe(failed); err != nil {
		t.Fatal(err)
	}
	if tr.terminal == nil || tr.terminal.Type != "session.execution.failed" {
		t.Fatal("failure missing")
	}
}
func TestModelAndHandleValidation(t *testing.T) {
	m, err := parseModel("gateway/vendor/model", "high")
	if err != nil || m.ProviderID != "gateway" || m.ID != "vendor/model" {
		t.Fatalf("parse: %+v %v", m, err)
	}
	if _, err = parseModel("unqualified", ""); err == nil {
		t.Fatal("accepted unqualified model")
	}
	if _, err = decodeHandle(harness.Handle{Version: 2}); err == nil {
		t.Fatal("accepted unknown handle")
	}
	if _, err = New(harness.Config{Options: map[string]json.RawMessage{"typo": harness.JSON(true)}}); err == nil {
		t.Fatal("ignored unsupported option")
	}
}

// This is a real installed OpenCode subprocess and real HTTP/event/session
// transport, with a deterministic local model backend. It does not certify
// account access or claim successful external model inference.
func TestRealOpenCodeLocalModelLifecycle(t *testing.T) {
	if os.Getenv("ATLAS_OPENCODE_REAL") != "1" {
		t.Skip("set ATLAS_OPENCODE_REAL=1 to exercise the installed OpenCode v2 binary with a local model fixture")
	}
	var mu sync.Mutex
	requests := 0
	sawHistory := false
	sawInstructions, sawMCPResult := false, false
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Stream bool `json:"stream"`
			Tools  []struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("model request %s: %v", r.URL.Path, err)
			http.Error(w, "bad request", 400)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected model endpoint %s", r.URL.Path)
			http.Error(w, "unsupported", 404)
			return
		}
		mu.Lock()
		requests++
		for _, m := range request.Messages {
			if m.Role == "assistant" && strings.Contains(string(m.Content), "ATLAS_FIXTURE_OK") {
				sawHistory = true
			}
			if m.Role == "system" && strings.Contains(string(m.Content), "ATLAS_PROJECT_INSTRUCTIONS_SENTINEL") {
				sawInstructions = true
			}
			if m.Role == "tool" && strings.Contains(string(m.Content), "ATLAS_MCP_RESULT_SENTINEL") {
				sawMCPResult = true
			}
		}
		mu.Unlock()
		toolScenario, cancelScenario, mcpScenario, hasToolResult := false, false, false, false
		mcpCatalogFound, mcpComplete := false, false
		for _, m := range request.Messages {
			if m.Role == "user" {
				toolScenario = toolScenario || strings.Contains(string(m.Content), "ATLAS_TOOL_SCENARIO")
				cancelScenario = cancelScenario || strings.Contains(string(m.Content), "ATLAS_CANCEL_SCENARIO")
				mcpScenario = mcpScenario || strings.Contains(string(m.Content), "ATLAS_MCP_SCENARIO")
			}
			if m.Role == "tool" {
				hasToolResult = true
				mcpCatalogFound = mcpCatalogFound || (strings.Contains(string(m.Content), "atlas_fixture.atlas_probe") && !strings.Contains(string(m.Content), "Unknown tool"))
				mcpComplete = mcpComplete || strings.Contains(string(m.Content), "ATLAS_MCP_RESULT_SENTINEL")
			}
		}
		if (toolScenario && !hasToolResult) || (mcpScenario && !mcpComplete) {
			tool := ""
			args := `{"command":"printf ATLAS_TOOL_OK > atlas-tool.txt","description":"Write the isolated Atlas test marker"}`
			for _, candidate := range request.Tools {
				if mcpScenario && candidate.Function.Name == "execute" {
					tool = "execute"
					args = `{"code":"return search({query:'atlas_probe'});"}`
					if mcpCatalogFound {
						args = `{"code":"return await tools.atlas_fixture.atlas_probe();"}`
					}
					break
				}
				if mcpScenario && strings.Contains(candidate.Function.Name, "atlas_probe") {
					tool = candidate.Function.Name
					args = `{}`
					break
				}
				if toolScenario && (candidate.Function.Name == "shell" || candidate.Function.Name == "bash") {
					tool = candidate.Function.Name
					break
				}
			}
			if tool == "" {
				t.Errorf("native requested tool missing from fixture request (MCP=%v)", mcpScenario)
				http.Error(w, "no shell", 500)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			payload := map[string]any{"id": "fixture-tool", "object": "chat.completion.chunk", "created": 1, "model": "echo", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_atlas_fixture", "type": "function", "function": map[string]string{"name": tool, "arguments": args}}}}, "finish_reason": nil}}}
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "data: %s\n\n", b)
			fmt.Fprint(w, "data: {\"id\":\"fixture-tool\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"echo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		if cancelScenario {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"fixture-cancel\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"echo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ATLAS_STREAM_STARTED\"},\"finish_reason\":null}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, chunk := range []string{"ATLAS_", "FIXTURE_OK"} {
				b, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "echo", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": chunk}, "finish_reason": nil}}})
				fmt.Fprintf(w, "data: %s\n\n", b)
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"echo\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":3,\"total_tokens\":6}}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"fixture","object":"chat.completion","created":1,"model":"echo","choices":[{"index":0,"message":{"role":"assistant","content":"ATLAS_FIXTURE_OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":3,"total_tokens":6}}`)
		}
	}))
	defer fixture.Close()
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"model": "atlasfixture/echo", "provider": map[string]any{"atlasfixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Atlas local fixture", "options": map[string]string{"baseURL": fixture.URL + "/v1", "apiKey": "local-fixture-only"}, "models": map[string]any{"echo": map[string]any{"name": "Atlas fixture", "limit": map[string]int{"context": 32768, "output": 1024}}}}}}
	env := map[string]string{"OPENCODE_CONFIG_CONTENT": string(harness.JSON(config)), "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state")}
	d, err := New(harness.Config{Adapter: "opencode", Env: env})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	catalog, err := d.Catalog(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range catalog.Models {
		if m.ID == "atlasfixture/echo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture model absent: %+v", catalog.Models)
	}
	events := []harness.Event{}
	emit := func(e harness.Event) error { events = append(events, e); return nil }
	req := harness.RunRequest{SessionID: "test", CWD: cwd, Prompt: "Reply with ATLAS_FIXTURE_OK and use no tools.", Model: "atlasfixture/echo", PermissionMode: "deny", StateDir: root}
	first, err := d.Run(ctx, req, emit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "ATLAS_FIXTURE_OK" || first.Handle == nil {
		t.Fatalf("first result %+v", first)
	}
	if len(events) == 0 || events[0].Type != "persistence" {
		t.Fatalf("identity was not first: %+v", events)
	}
	discovered, err := d.Discover(ctx, cwd, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 1 {
		t.Fatalf("native discovery: %+v", discovered)
	}
	var firstHandle nativeHandle
	_ = json.Unmarshal(first.Handle.Data, &firstHandle)
	if discovered[0].ID != firstHandle.SessionID {
		t.Fatal("native discovery identity mismatch")
	}
	req.Resume = first.Handle
	req.Prompt = "Repeat your previous response."
	events = nil
	second, err := d.Run(ctx, req, emit, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "ATLAS_FIXTURE_OK" {
		t.Fatalf("resumed output duplicates old history: %q", second.Text)
	}
	mu.Lock()
	if !sawHistory || requests < 2 {
		t.Fatalf("resume did not reach model with native history; requests=%d history=%v", requests, sawHistory)
	}
	mu.Unlock()
	permissionControls := make(chan harness.Control, 4)
	asked := false
	toolEvents := 0
	req.Resume = nil
	req.SessionID = "permission-test"
	req.Prompt = "ATLAS_TOOL_SCENARIO: create the isolated marker."
	req.PermissionMode = "ask"
	_, err = d.Run(ctx, req, func(e harness.Event) error {
		if e.Type == "permission" && e.Permission != nil {
			asked = true
			permissionControls <- harness.Control{Type: "permission", ID: e.Permission.ID, Allow: true}
		}
		if e.Type == "tool" {
			toolEvents++
		}
		return nil
	}, permissionControls)
	if err != nil {
		t.Fatal(err)
	}
	if !asked || toolEvents == 0 {
		t.Fatalf("permission/tool bridge missing: permission=%v toolEvents=%d", asked, toolEvents)
	}
	marker, err := os.ReadFile(filepath.Join(cwd, "atlas-tool.txt"))
	if err != nil || string(marker) != "ATLAS_TOOL_OK" {
		t.Fatalf("approved tool did not execute in project: %q %v", marker, err)
	}
	cancelCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	req.SessionID = "cancel-test"
	req.Prompt = "ATLAS_CANCEL_SCENARIO"
	req.PermissionMode = "deny"
	started := false
	_, err = d.Run(cancelCtx, req, func(e harness.Event) error {
		if e.Type == "text" {
			started = true
			cancelRun()
		}
		return nil
	}, nil)
	if !started || !errors.Is(err, context.Canceled) {
		t.Fatalf("active stream cancellation started=%v err=%v", started, err)
	}
	mu.Lock()
	t.Logf("real OpenCode lifecycle passed; model requests=%d, persisted session=%s", requests, firstHandle.SessionID)
	mu.Unlock()
	t.Run("NativeConfiguration", func(t *testing.T) {
		testOpenCodeNativeConfiguration(t, env, root)
		mu.Lock()
		defer mu.Unlock()
		if !sawInstructions || !sawMCPResult {
			t.Fatalf("native config loading instructions=%v MCP tool result=%v", sawInstructions, sawMCPResult)
		}
	})
	if os.Getenv("ATLAS_OPENCODE_CLI") == "1" {
		t.Run("FullCLI", func(t *testing.T) { testOpenCodeCLI(t, env, root, firstHandle.SessionID, cwd) })
	}
}

func TestRealOpenCodeExternal(t *testing.T) {
	model := os.Getenv("ATLAS_OPENCODE_EXTERNAL_MODEL")
	if model == "" {
		t.Skip("set ATLAS_OPENCODE_EXTERNAL_MODEL to an authorized model to run external inference")
	}
	d, err := New(harness.Config{Adapter: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cwd := t.TempDir()
	var persisted bool
	result, err := d.Run(ctx, harness.RunRequest{SessionID: "external-e2e", CWD: cwd, Prompt: "Reply exactly ATLAS_EXTERNAL_OK. Do not use tools.", Model: model, PermissionMode: "deny", StateDir: cwd}, func(e harness.Event) error {
		if e.Type == "persistence" {
			persisted = true
		}
		return nil
	}, nil)
	if err != nil {
		if !persisted || result.Handle == nil {
			t.Fatalf("failed run lost native handle: %v", err)
		}
		t.Fatalf("external model run failed after persisting native identity: %v", err)
	}
	if !strings.Contains(result.Text, "ATLAS_EXTERNAL_OK") {
		t.Fatalf("unexpected response %q", result.Text)
	}
	req := harness.RunRequest{SessionID: "external-e2e", CWD: cwd, Prompt: "Repeat the exact marker from your previous response.", Resume: result.Handle, PermissionMode: "deny", StateDir: cwd}
	resumed, err := d.Run(ctx, req, func(harness.Event) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resumed.Text, "ATLAS_EXTERNAL_OK") {
		t.Fatalf("resume response %q", resumed.Text)
	}
}

func TestCancelledBeforeStart(t *testing.T) {
	d, _ := New(harness.Config{Adapter: "opencode"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Catalog(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
