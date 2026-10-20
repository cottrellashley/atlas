package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func TestStrictOptionsAndOpaqueHandles(t *testing.T) {
	if _, err := New(harness.Config{Options: map[string]json.RawMessage{"misspelled": json.RawMessage(`true`)}}); err == nil {
		t.Fatal("unknown option accepted")
	}
	for _, h := range []*harness.Handle{nil, {Version: 2, Data: json.RawMessage(`{}`)}, {Version: 1, Data: json.RawMessage(`{"sessionId":"--bad-flag"}`)}} {
		if _, err := decodeHandle(h); err == nil {
			t.Fatal("invalid handle accepted")
		}
	}
	id, err := newUUID()
	if err != nil || !validUUID(id) {
		t.Fatalf("UUID: %q %v", id, err)
	}
	decoded, err := decodeHandle(handle(id, "/tmp/project"))
	if err != nil || decoded.SessionID != id {
		t.Fatalf("handle round trip: %#v %v", decoded, err)
	}
}

func TestStreamPreservesIdentityAndDoesNotDuplicateText(t *testing.T) {
	var events []harness.Event
	result := harness.RunResult{}
	s := streamState{cwd: "/project", result: &result, pending: map[string]permissionRequest{}, emit: func(e harness.Event) error { events = append(events, e); return nil }}
	frames := []string{
		`{"type":"system","subtype":"init","session_id":"9de45347-46f9-4522-87a8-95bf0d8cf21e","model":"native-model"}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"assistant","message":{"model":"native-model","content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool-1","content":"read"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hello","usage":{"input_tokens":4}}`,
	}
	for i, f := range frames {
		done, err := s.consume(nil, json.RawMessage(f))
		if err != nil {
			t.Fatal(err)
		}
		if done != (i == len(frames)-1) {
			t.Fatalf("unexpected terminal marker at %d", i)
		}
	}
	if result.Handle == nil || result.Model != "native-model" || result.Text != "hello" {
		t.Fatalf("result %#v", result)
	}
	if len(events) == 0 || events[0].Type != "persistence" {
		t.Fatal("persistence must precede content")
	}
	textCount, toolCount := 0, 0
	for _, e := range events {
		if e.Type == "text" {
			textCount++
		}
		if e.Type == "tool" {
			toolCount++
		}
	}
	if textCount != 1 || toolCount != 2 {
		t.Fatalf("text=%d tools=%d", textCount, toolCount)
	}
}

func TestResultFailureIsFailureDespiteExitStatus(t *testing.T) {
	result := harness.RunResult{}
	s := streamState{result: &result, emit: func(harness.Event) error { return nil }}
	done, err := s.consume(nil, json.RawMessage(`{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["model is unavailable"]}`))
	if done || err == nil || !strings.Contains(err.Error(), "model is unavailable") {
		t.Fatalf("terminal failure %v %v", done, err)
	}
}

func TestResultOnlyOutputIsVisible(t *testing.T) {
	result := harness.RunResult{}
	var text string
	s := streamState{result: &result, emit: func(e harness.Event) error {
		if e.Type == "text" {
			text += e.Text
		}
		return nil
	}}
	done, err := s.consume(nil, json.RawMessage(`{"type":"result","subtype":"success","is_error":false,"result":"command output"}`))
	if !done || err != nil || text != "command output" {
		t.Fatalf("done=%v error=%v text=%q", done, err, text)
	}
}

func TestQuestionAnswersPreserveNativeInput(t *testing.T) {
	p := permissionRequest{Tool: "AskUserQuestion", Input: json.RawMessage(`{"questions":[{"question":"Which scope?","header":"Scope","options":[{"label":"All"}]}],"metadata":{"source":"test"}}`)}
	updated, err := permissionInput(p, "All")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Answers  map[string]string `json:"answers"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(updated, &result); err != nil {
		t.Fatal(err)
	}
	if result.Answers["Which scope?"] != "All" || result.Metadata["source"] != "test" {
		t.Fatalf("updated input %s", updated)
	}
	if _, err := permissionInput(p, ""); err == nil {
		t.Fatal("blank approval silently accepted a question")
	}
	p.Input = json.RawMessage(`{"questions":[{"question":"First?"},{"question":"Second?"}]}`)
	if _, err := permissionInput(p, `{"First?":"a","Second?":"b"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := permissionInput(p, "a"); err == nil {
		t.Fatal("ambiguous multi-question answer accepted")
	}
}

func TestDiscoverNativeSessionsAndFilterWorkspace(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "projects", "any-native-encoded-name")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	id := "9de45347-46f9-4522-87a8-95bf0d8cf21e"
	transcript := `{"type":"user","cwd":"/workspace/research","sessionId":"` + id + `","message":{"content":"First research prompt"}}` + "\n" + `{"type":"assistant","message":{"model":"resolved-model","content":[{"type":"text","text":"hello"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(transcript), 0600); err != nil {
		t.Fatal(err)
	}
	d, _ := New(harness.Config{Env: map[string]string{"CLAUDE_CONFIG_DIR": root}})
	got, err := d.Discover(context.Background(), "/workspace/research", 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("discover %v %v", got, err)
	}
	if got[0].ID != id || got[0].Model != "resolved-model" || got[0].Title != "First research prompt" {
		t.Fatalf("summary %#v", got[0])
	}
	got, err = d.Discover(context.Background(), "/different", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("cwd filtering %v %v", got, err)
	}
}

// Opt-in actual model calls. The isolated project inherits the developer's
// existing native authentication; this test neither copies nor prints secrets.
func TestRealClaudeLifecycle(t *testing.T) {
	if os.Getenv("ATLAS_TEST_CLAUDE") != "1" {
		t.Skip("set ATLAS_TEST_CLAUDE=1 for real Claude model calls")
	}
	cwd := t.TempDir()
	token := "ATLAS_NATIVE_428"
	nonce := fmt.Sprintf("atlas-memory-%d", time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("The project check token is "+token+". For check prompts, reply briefly and do not call tools.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := New(harness.Config{Adapter: "claude", Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	catalog, err := d.Catalog(ctx, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) == 0 {
		t.Fatal("no native models")
	}
	t.Logf("native catalog: %s, models=%d", catalog.Source, len(catalog.Models))
	var events []harness.Event
	first, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "Remember this nonce: " + nonce + ". Reply only with the project check token from your instructions and this nonce.", StateDir: t.TempDir()}, func(e harness.Event) error { events = append(events, e); return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Handle == nil || !strings.Contains(first.Text, token) || !strings.Contains(first.Text, nonce) {
		t.Fatalf("first result %#v", first)
	}
	var sawText, sawPersistence bool
	for _, e := range events {
		if e.Type == "text" {
			sawText = true
		}
		if e.Type == "persistence" {
			sawPersistence = true
		}
	}
	if !sawText || !sawPersistence {
		t.Fatalf("stream text=%v persistence=%v", sawText, sawPersistence)
	}
	native, _ := decodeHandle(first.Handle)
	t.Logf("created native session %s with %s", native.SessionID, first.Model)
	found, err := d.Discover(ctx, cwd, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ID != native.SessionID {
		t.Fatalf("native discovery %v", found)
	}
	if err := os.Remove(filepath.Join(cwd, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	second, err := d.Run(ctx, harness.RunRequest{CWD: cwd, Prompt: "What was the nonce I asked you to remember in the prior message? Reply only with that nonce and do not call tools.", Resume: first.Handle, StateDir: t.TempDir()}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Text, nonce) || !sameSession(second.Handle, native.SessionID) {
		t.Fatalf("resume failed %#v", second)
	}
	t.Log("native project instructions, catalog, stream, discovery, and two-turn resume passed")
}

func TestRealClaudePermissionAndCancellation(t *testing.T) {
	if os.Getenv("ATLAS_TEST_CLAUDE") != "1" {
		t.Skip("set ATLAS_TEST_CLAUDE=1 for real Claude model calls")
	}
	d, err := New(harness.Config{Adapter: "claude", Model: "haiku", Options: map[string]json.RawMessage{"settings": json.RawMessage(`{"permissions":{"ask":["Bash"]}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	controls := make(chan harness.Control, 8)
	approved := false
	resolved := false
	result, err := d.Run(ctx, harness.RunRequest{CWD: cwd, PermissionMode: "manual", Prompt: "Use the Bash tool to execute exactly: printf ATLAS_PERMISSION_CHECK. Then report the command output. Do not use any other tool.", StateDir: t.TempDir()}, func(e harness.Event) error {
		if e.Type == "permission" {
			approved = true
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
	if !approved || !resolved || !strings.Contains(result.Text, "ATLAS_PERMISSION_CHECK") {
		t.Fatalf("permission flow approved=%v resolved=%v result=%#v", approved, resolved, result)
	}
	t.Log("native host approval succeeded")
	stopCtx, stop := context.WithCancel(context.Background())
	defer stop()
	began := time.Now()
	_, err = d.Run(stopCtx, harness.RunRequest{CWD: cwd, Prompt: "Reply only with OK", StateDir: t.TempDir()}, func(e harness.Event) error {
		if e.Type == "config" {
			stop()
		}
		return nil
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
	if time.Since(began) > 15*time.Second {
		t.Fatal("cancellation cleanup exceeded bound")
	}
	t.Log("cancellation returned only after subprocess cleanup")
}
