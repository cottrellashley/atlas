package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func writeTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, harness.JSON(v), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryPrecedenceAndNativeHandle(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	agentDir := filepath.Join(root, "agent")
	writeTestJSON(t, filepath.Join(agentDir, "settings.json"), map[string]any{"sessionDir": "user-sessions"})
	writeTestJSON(t, filepath.Join(cwd, ".pi", "settings.json"), map[string]any{"sessionDir": "project-sessions"})
	driver, err := New(harness.Config{Env: map[string]string{"PI_CODING_AGENT_DIR": agentDir, "PI_CODING_AGENT_SESSION_DIR": ""}})
	if err != nil {
		t.Fatal(err)
	}
	d := driver.(*Driver)
	if got, err := d.sessionsDir(cwd); err != nil || got != filepath.Join(cwd, "project-sessions") {
		t.Fatalf("project precedence: %s %v", got, err)
	}
	d.cfg.Env["PI_CODING_AGENT_SESSION_DIR"] = "environment-sessions"
	if got, err := d.sessionsDir(cwd); err != nil || got != filepath.Join(cwd, "environment-sessions") {
		t.Fatalf("environment precedence: %s %v", got, err)
	}
	d.opts.SessionDir = "explicit-sessions"
	sessionDir, _ := d.sessionsDir(cwd)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "native-id.jsonl")
	data := strings.Join([]string{string(harness.JSON(map[string]any{"type": "session", "version": 3, "id": "native-id", "cwd": cwd, "timestamp": "2026-01-01T00:00:00Z"})), `{"type":"message","timestamp":"2026-01-01T00:01:00Z","message":{"role":"user","content":"first request"}}`, `{"type":"session_info","timestamp":"2026-01-01T00:02:00Z","name":"Research"}`, `{"type":"model_change","timestamp":"2026-01-01T00:03:00Z","provider":"vendor","modelId":"model/a"}`}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	sessions, err := d.Discover(context.Background(), cwd, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Title != "Research" || sessions[0].Model != "vendor/model/a" {
		t.Fatalf("unexpected discovery: %#v", sessions)
	}
	native, err := decodeHandle(sessions[0].Handle)
	if err != nil || native.SessionFile != path || native.SessionID != "native-id" {
		t.Fatalf("native identity: %#v %v", native, err)
	}
	other, err := d.Discover(context.Background(), filepath.Join(root, "other"), 20)
	if err != nil || len(other) != 0 {
		t.Fatalf("cwd leaked: %#v %v", other, err)
	}
}

func TestRunRejectsUnsupportedFlagsBeforeStarting(t *testing.T) {
	d, _ := New(harness.Config{Command: []string{"missing-pi-binary"}})
	_, err := d.Run(context.Background(), harness.RunRequest{NativeAgent: "researcher"}, nil, nil)
	if !errors.Is(err, harness.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}

type fixtureRequest struct {
	Tools []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}
type modelFixture struct {
	t              *testing.T
	calls          atomic.Int32
	mu             sync.Mutex
	requests       []fixtureRequest
	hang           bool
	observedTool   bool
	observedMemory bool
	toolName       string
	toolArguments  string
	toolMarker     string
}

func (f *modelFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.Error(w, "unexpected fixture route", 404)
		return
	}
	var request fixtureRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	call := f.calls.Add(1)
	w.Header().Set("Content-Type", "text/event-stream")
	write := func(delta any, finish any) {
		data := map[string]any{"id": fmt.Sprintf("fixture-%d", call), "object": "chat.completion.chunk", "created": 1, "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		fmt.Fprintf(w, "data: %s\n\n", harness.JSON(data))
		w.(http.Flusher).Flush()
	}
	if f.hang {
		write(map[string]any{"role": "assistant", "content": "started"}, nil)
		<-r.Context().Done()
		return
	}
	if call == 1 {
		name, arguments := f.toolName, f.toolArguments
		if name == "" {
			name, arguments = "read", "{\"path\":\"facts.txt\"}"
		}
		write(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "fixture-read", "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}}}, nil)
		write(map[string]any{}, "tool_calls")
	} else {
		marker := f.toolMarker
		if marker == "" {
			marker = "atlas-pi-file-marker"
		}
		for _, message := range request.Messages {
			if message.Role == "tool" && strings.Contains(string(message.Content), marker) {
				f.mu.Lock()
				f.observedTool = true
				f.mu.Unlock()
			}
			if message.Role == "assistant" && strings.Contains(string(message.Content), "fixture-first-answer") {
				f.mu.Lock()
				f.observedMemory = true
				f.mu.Unlock()
			}
		}
		text := "fixture-first-answer"
		if call >= 3 {
			text = "fixture-resumed-answer"
		}
		write(map[string]any{"role": "assistant", "content": text}, nil)
		write(map[string]any{}, "stop")
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	w.(http.Flusher).Flush()
}

func nativeFixtureDriver(t *testing.T, endpoint string) (harness.Driver, string, string) {
	t.Helper()
	if os.Getenv("ATLAS_PI_E2E") != "1" {
		t.Skip("set ATLAS_PI_E2E=1 to exercise installed Pi with a local deterministic model API fixture")
	}
	binary, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal("Pi executable required:", err)
	}
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	cwd := filepath.Join(root, "project")
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "facts.txt"), []byte("atlas-pi-file-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(agentDir, "models.json"), map[string]any{"providers": map[string]any{"atlas-fixture": map[string]any{"baseUrl": endpoint + "/v1", "api": "openai-completions", "apiKey": "local-fixture-no-secret", "models": []any{map[string]any{"id": "test-model", "name": "Atlas deterministic fixture", "reasoning": false, "input": []string{"text"}, "contextWindow": 32768, "maxTokens": 256}}}}})
	writeTestJSON(t, filepath.Join(agentDir, "settings.json"), map[string]any{"defaultProvider": "atlas-fixture", "defaultModel": "test-model", "defaultThinkingLevel": "off", "retry": map[string]any{"enabled": false}, "compaction": map[string]any{"enabled": false}})
	driver, err := New(harness.Config{Adapter: "pi", Command: []string{binary}, Env: map[string]string{"PI_CODING_AGENT_DIR": agentDir, "PI_CODING_AGENT_SESSION_DIR": sessions, "PI_OFFLINE": "1", "PI_TELEMETRY": "0"}, Options: map[string]json.RawMessage{"offline": harness.JSON(true)}})
	if err != nil {
		t.Fatal(err)
	}
	return driver, cwd, root
}

func TestInstalledPiFixtureToolResumeAndCatalog(t *testing.T) {
	fixture := &modelFixture{t: t}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, _ := nativeFixtureDriver(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	catalog, err := d.Catalog(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range catalog.Models {
		if model.ID == "atlas-fixture/test-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("native catalog omitted fixture: %#v", catalog)
	}
	var events []harness.Event
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, SessionID: "atlas-fixture-session", Prompt: "Read facts.txt, then answer briefly.", Model: "atlas-fixture/test-model"}, func(e harness.Event) error { events = append(events, e); return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "fixture-first-answer" || result.Handle == nil {
		t.Fatalf("result: %#v", result)
	}
	if len(events) == 0 || events[0].Type != "persistence" {
		t.Fatalf("native identity was not persisted before work: %#v", events)
	}
	tool := false
	for _, e := range events {
		if e.Type == "tool" {
			tool = true
		}
	}
	if !tool {
		t.Fatal("no real Pi tool events")
	}
	native, err := decodeHandle(*result.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(native.SessionFile); err != nil {
		t.Fatal("native transcript missing:", err)
	}
	sessions, err := d.Discover(ctx, cwd, 20)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("native discovery: %#v %v", sessions, err)
	}
	resumed, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "What did you answer earlier?", Resume: result.Handle}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Text != "fixture-resumed-answer" {
		t.Fatalf("resume result: %#v", resumed)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !fixture.observedTool || !fixture.observedMemory {
		t.Fatalf("real native execution missing tool=%v memory=%v", fixture.observedTool, fixture.observedMemory)
	}
}

func TestInstalledPiFixtureCancellation(t *testing.T) {
	fixture := &modelFixture{t: t, hang: true}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, _ := nativeFixtureDriver(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var gotText bool
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "Begin", Model: "atlas-fixture/test-model"}, func(e harness.Event) error {
		if e.Type == "text" {
			gotText = true
			cancel()
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	if !gotText || result.Handle == nil {
		t.Fatalf("did not cancel live native stream: text=%v handle=%v", gotText, result.Handle)
	}
}

func TestInstalledPiFixtureExtensionPermission(t *testing.T) {
	fixture := &modelFixture{t: t}
	fixture.calls.Store(1)
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	extension := filepath.Join(root, "approval.ts")
	source := `export default function(pi) { pi.on("before_agent_start", async (_event,ctx) => { const accepted = await ctx.ui.confirm("Atlas fixture approval", "Continue with local fixture?"); if (!accepted) throw new Error("fixture approval denied"); }); }`
	if err := os.WriteFile(extension, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	d.(*Driver).cfg.Command = append(d.(*Driver).cfg.Command, "--extension", extension)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	controls := make(chan harness.Control, 1)
	var permissionSeen, resolved bool
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "Reply", Model: "atlas-fixture/test-model"}, func(e harness.Event) error {
		if e.Type == "permission" {
			permissionSeen = true
			controls <- harness.Control{Type: "permission", ID: e.Permission.ID, Allow: true}
		}
		if e.Type == "permission_resolved" {
			resolved = true
		}
		return nil
	}, controls)
	if err != nil {
		t.Fatal(err)
	}
	if !permissionSeen || !resolved || result.Text != "fixture-first-answer" {
		t.Fatalf("permission flow: seen=%v resolved=%v result=%#v", permissionSeen, resolved, result)
	}
}

func TestInstalledPiFixtureRetryWaitsForSettlement(t *testing.T) {
	var attempts atomic.Int32
	fixture := &modelFixture{t: t}
	fixture.calls.Store(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":{"message":"temporary fixture overload","type":"server_error"}}`)
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	writeTestJSON(t, filepath.Join(root, "agent", "settings.json"), map[string]any{
		"defaultProvider": "atlas-fixture", "defaultModel": "test-model",
		"retry":      map[string]any{"enabled": true, "maxRetries": 1, "baseDelayMs": 10, "provider": map[string]any{"maxRetries": 0}},
		"compaction": map[string]any{"enabled": false},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "Reply", Model: "atlas-fixture/test-model"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() < 2 || result.Text != "fixture-first-answer" {
		t.Fatalf("returned before retry settled: attempts=%d result=%#v", attempts.Load(), result)
	}
}

func TestInstalledPiFixtureProviderFailureReturnsErrorAndHandle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"intentional atlas fixture failure","type":"invalid_request_error"}}`)
	}))
	defer server.Close()
	d, cwd, _ := nativeFixtureDriver(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "Reply", Model: "atlas-fixture/test-model"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "intentional atlas fixture failure") || result.Handle == nil {
		t.Fatalf("provider failure lost: err=%v result=%#v", err, result)
	}
}

func TestInstalledPiFixtureHandledCommand(t *testing.T) {
	fixture := &modelFixture{t: t}
	server := httptest.NewServer(fixture)
	defer server.Close()
	d, cwd, root := nativeFixtureDriver(t, server.URL)
	extension := filepath.Join(root, "command.ts")
	if err := os.WriteFile(extension, []byte(`export default function(pi) { pi.registerCommand("atlas-fixture", {description:"Local fixture",handler:async(_args,ctx)=>ctx.ui.notify("fixture command handled","info")}); }`), 0600); err != nil {
		t.Fatal(err)
	}
	d.(*Driver).cfg.Command = append(d.(*Driver).cfg.Command, "--extension", extension)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var notice bool
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "/atlas-fixture", Model: "atlas-fixture/test-model"}, func(e harness.Event) error {
		if e.Text == "fixture command handled" {
			notice = true
		}
		return nil
	}, nil)
	if err != nil || !notice || fixture.calls.Load() != 0 || result.Handle == nil {
		t.Fatalf("handled command did not finish without model run: notice=%v calls=%d result=%#v err=%v", notice, fixture.calls.Load(), result, err)
	}
}

// This separate gate requires real user-configured provider authentication and
// makes billable model calls. It never substitutes the deterministic API fixture.
func TestInstalledPiLiveModelResume(t *testing.T) {
	if os.Getenv("ATLAS_PI_LIVE") != "1" {
		t.Skip("set ATLAS_PI_LIVE=1 for real authenticated model inference")
	}
	binary, err := exec.LookPath("pi")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	d, err := New(harness.Config{Adapter: "pi", Command: []string{binary}, Options: map[string]json.RawMessage{"sessionDir": harness.JSON(filepath.Join(cwd, "sessions")), "offline": harness.JSON(true)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	model := os.Getenv("ATLAS_PI_MODEL")
	if model == "" {
		catalog, err := d.Catalog(ctx, cwd)
		if err != nil {
			t.Fatal(err)
		}
		if len(catalog.Models) == 0 {
			t.Fatal("no authenticated Pi models; authenticate Pi with a provider before running ATLAS_PI_LIVE")
		}
		model = catalog.Models[0].ID
		for _, candidate := range catalog.Models {
			if candidate.Default {
				model = candidate.ID
				break
			}
		}
	}
	t.Logf("real Pi model: %s", model)
	marker := "atlas-pi-memory-" + fmt.Sprint(time.Now().UnixNano())
	first, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Model: model, Prompt: "Remember this exact verification marker: " + marker + ". Reply with that marker only. Do not use tools."}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Handle == nil || !strings.Contains(first.Text, marker) {
		t.Fatalf("live first response did not return the requested marker: %q", first.Text)
	}
	second, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Resume: first.Handle, Prompt: "What exact verification marker did I give you earlier? Reply with the marker only. Do not use tools."}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Text, marker) {
		t.Fatalf("live native resume lost conversation context: %q", second.Text)
	}
}
