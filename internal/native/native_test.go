package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

func fixture(t *testing.T, path string, rows ...any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, row := range rows {
		data = append(data, harness.JSON(row)...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestListNativeHistoryAcrossProjectsAndAliases(t *testing.T) {
	root := t.TempDir()
	claudeDir, piDir := filepath.Join(root, "claude"), filepath.Join(root, "pi")
	const claudeID = "9de45347-46f9-4522-87a8-95bf0d8cf21e"
	projectA, projectB := filepath.Join(root, "a"), filepath.Join(root, "b")
	fixture(t, filepath.Join(claudeDir, "projects", "project-a", claudeID+".jsonl"), map[string]any{
		"type": "user", "cwd": projectA, "message": map[string]any{"content": "Research prompt"},
	})
	// Duplicate transcripts must not produce ambiguous native session selectors.
	fixture(t, filepath.Join(claudeDir, "projects", "copy", claudeID+".jsonl"), map[string]any{
		"type": "user", "cwd": projectA, "message": map[string]any{"content": "Research prompt"},
	})
	for i, project := range []string{projectA, projectB} {
		fixture(t, filepath.Join(piDir, filepath.Base(project), "session.jsonl"), map[string]any{
			"type": "session", "id": "pi-" + filepath.Base(project), "cwd": project,
		}, map[string]any{
			"type": "session_info", "name": "Pi " + filepath.Base(project), "timestamp": time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC),
		})
	}
	configs := []harness.Config{
		{ID: "work-claude", Adapter: "claude", Command: []string{"not-installed-claude"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": claudeDir}},
		{ID: "research-pi", Adapter: "pi", Command: []string{"not-installed-pi"}, Env: map[string]string{"PI_CODING_AGENT_SESSION_DIR": piDir}},
		{ID: "personal-claude", Adapter: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": filepath.Join(root, "other-claude")}},
		{ID: "missing-opencode", Adapter: "opencode", Command: []string{filepath.Join(root, "missing-executable")}},
	}
	groups := List(context.Background(), configs, "", 100)
	if len(groups) != 4 || groups[0].Harness != "work-claude" || len(groups[0].Sessions) != 1 {
		t.Fatalf("Claude alias/native-only discovery: %#v", groups)
	}
	if groups[0].Error != "" || groups[1].Error != "" || groups[2].Error != "" || groups[3].Error == "" {
		t.Fatalf("per-harness failures: %#v", groups)
	}
	if len(groups[1].Sessions) != 2 || groups[1].Sessions[0].ID != "pi-b" || len(groups[2].Sessions) != 0 {
		t.Fatalf("global projects, sorting and separate alias stores: %#v", groups)
	}
	if got := groups[0].Sessions[0]; got.ID != claudeID || got.CWD != projectA || got.Title != "Research prompt" || got.Handle.Version != 1 {
		t.Fatalf("native descriptor changed: %#v", got)
	}
	filtered := List(context.Background(), configs[:2], projectA, 100)
	if len(filtered[0].Sessions) != 1 || len(filtered[1].Sessions) != 1 || filtered[1].Sessions[0].CWD != projectA {
		t.Fatalf("explicit project filter: %#v", filtered)
	}
	limited := List(context.Background(), configs[1:2], "", 1)
	if len(limited[0].Sessions) != 1 || limited[0].Sessions[0].ID != "pi-b" {
		t.Fatalf("newest bounded history: %#v", limited)
	}
	// Exact lookup retains aliases and does not rely on the recent list window.
	aliases := []harness.Config{configs[0], configs[0]}
	aliases[1].ID = "same-store-alias"
	found := Find(context.Background(), aliases, "", claudeID)
	if len(found) != 2 || found[0].Error != "" || found[1].Error != "" || len(found[0].Sessions) != 1 || len(found[1].Sessions) != 1 || found[1].Harness != "same-store-alias" {
		t.Fatalf("exact native lookup must preserve alias ambiguity: %#v", found)
	}
}

func TestListCancelledBeforeProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	groups := List(ctx, []harness.Config{{ID: "claude", Adapter: "claude"}}, "", 100)
	if len(groups) != 1 || groups[0].Error != context.Canceled.Error() || len(groups[0].Sessions) != 0 {
		t.Fatalf("cancelled discovery: %#v", groups)
	}
}

func TestOpenCodeDiscoveryReadsNativeAPIWithoutInference(t *testing.T) {
	cwd := t.TempDir()
	cfg := harness.Config{ID: "work-opencode", Adapter: "opencode", Command: []string{os.Args[0], "-test.run=^TestOpenCodeNativeServerFixture$", "--"}, Env: map[string]string{
		"ATLAS_NATIVE_SERVER_FIXTURE": "1", "ATLAS_NATIVE_FIXTURE_CWD": cwd,
	}}
	groups := List(context.Background(), []harness.Config{cfg}, "", 100)
	if len(groups) != 1 || groups[0].Error != "" || len(groups[0].Sessions) != 1 {
		t.Fatalf("global OpenCode discovery: %#v", groups)
	}
	session := groups[0].Sessions[0]
	if session.ID != "ses-fixture" || session.CWD != cwd || session.Title != "Native research" {
		t.Fatalf("OpenCode native descriptor: %#v", session)
	}
	args, err := Args(cfg, &session)
	if err != nil || !reflect.DeepEqual(args, []string{"--session", "ses-fixture"}) {
		t.Fatalf("OpenCode discovered handle cannot resume: %v %v", args, err)
	}
	older := Find(context.Background(), []harness.Config{cfg}, "", "ses-older")
	if len(older) != 1 || older[0].Error != "" || len(older[0].Sessions) != 1 || older[0].Sessions[0].ID != "ses-older" {
		t.Fatalf("exact native lookup omitted an older session outside the list: %#v", older)
	}
	missing := Find(context.Background(), []harness.Config{cfg}, "", "ses-missing")
	if len(missing) != 1 || missing[0].Error != "" || len(missing[0].Sessions) != 0 {
		t.Fatalf("missing native lookup: %#v", missing)
	}
}

func TestOpenCodeRelativeExecutableUsesProjectAndInvocationDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		command    string
		invocation bool
	}{
		{"relative-command-project", "./bin/native-opencode", false},
		{"relative-path-project", "native-opencode", false},
		{"relative-command-invocation", "./bin/native-opencode", true},
		{"relative-path-invocation", "native-opencode", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			if err := os.Mkdir(filepath.Join(project, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(executable, filepath.Join(project, "bin", "native-opencode")); err != nil {
				t.Fatal(err)
			}
			cwd := project
			if test.invocation {
				t.Chdir(project)
				cwd = ""
			}
			startupFile := filepath.Join(project, "server-startup-cwd")
			cfg := harness.Config{ID: "relative-opencode", Adapter: "opencode", Command: []string{test.command, "-test.run=^TestOpenCodeNativeServerFixture$", "--"}, Env: map[string]string{
				"PATH": "bin", "ATLAS_NATIVE_SERVER_FIXTURE": "1", "ATLAS_NATIVE_FIXTURE_CWD": project,
				"ATLAS_NATIVE_FIXTURE_FILTER": cwd, "ATLAS_NATIVE_FIXTURE_STARTUP_FILE": startupFile,
			}}
			assertIsolatedStartup := func() {
				t.Helper()
				data, err := os.ReadFile(startupFile)
				if err != nil {
					t.Fatal(err)
				}
				startedIn := string(data)
				if startedIn == project || !filepath.IsAbs(startedIn) || !strings.HasPrefix(filepath.Base(startedIn), "atlas-opencode-host-") {
					t.Fatalf("server must retain isolated startup after executable resolution: %q", startedIn)
				}
				if _, err := os.Stat(startedIn); !os.IsNotExist(err) {
					t.Fatalf("temporary server directory was not cleaned up: %v", err)
				}
			}
			groups := List(context.Background(), []harness.Config{cfg}, cwd, 100)
			if len(groups) != 1 || groups[0].Error != "" || len(groups[0].Sessions) != 1 || groups[0].Sessions[0].CWD != project {
				t.Fatalf("relative executable discovery: %#v", groups)
			}
			assertIsolatedStartup()
			found := Find(context.Background(), []harness.Config{cfg}, cwd, "ses-older")
			if len(found) != 1 || found[0].Error != "" || len(found[0].Sessions) != 1 || found[0].Sessions[0].ID != "ses-older" {
				t.Fatalf("relative executable exact resume lookup: %#v", found)
			}
			assertIsolatedStartup()
			if cfg.Command[0] != test.command || cfg.Env["PATH"] != "bin" || cfg.Env["OPENCODE_PASSWORD"] != "" {
				t.Fatalf("native discovery mutated configured command or environment: %#v", cfg)
			}
		})
	}
}

func TestOpenCodeStandaloneConfigurationKeepsInteractiveCommand(t *testing.T) {
	cwd := t.TempDir()
	// The Go fixture rejects unknown flags before its -- delimiter. Successful
	// discovery therefore proves the redundant native root flag was removed
	// for serve, while the literal argument after -- must still reach the child.
	command := []string{os.Args[0], "--standalone", "-test.run=^TestOpenCodeNativeServerFixture$", "--", "--standalone"}
	cfg := harness.Config{ID: "standalone-opencode", Adapter: "opencode", Command: append([]string{}, command...), Env: map[string]string{
		"ATLAS_NATIVE_SERVER_FIXTURE": "1", "ATLAS_NATIVE_FIXTURE_CWD": cwd, "ATLAS_NATIVE_FIXTURE_LITERAL_STANDALONE": "1",
	}}
	groups := List(context.Background(), []harness.Config{cfg}, "", 100)
	if len(groups) != 1 || groups[0].Error != "" || len(groups[0].Sessions) != 1 {
		t.Fatalf("standalone native discovery: %#v", groups)
	}
	if !reflect.DeepEqual(cfg.Command, command) {
		t.Fatalf("server probe changed the interactive configured command: %v", cfg.Command)
	}
	if args, err := Args(cfg, nil); err != nil || len(args) != 0 {
		t.Fatalf("interactive standalone command must stay native: %v %v", args, err)
	}
}

// This local subprocess fixture exposes only read operations. Any attempt to
// create a native session, send a prompt, or initialize a project fails the test.
func TestOpenCodeNativeServerFixture(t *testing.T) {
	if os.Getenv("ATLAS_NATIVE_SERVER_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	if os.Getenv("ATLAS_NATIVE_FIXTURE_LITERAL_STANDALONE") == "1" {
		literal, found := false, false
		for _, arg := range os.Args[1:] {
			if arg == "--" {
				literal = true
			} else if literal && arg == "--standalone" {
				found = true
			}
		}
		if !found {
			t.Fatal("server preparation removed an argument after --")
		}
	}
	cwd := os.Getenv("ATLAS_NATIVE_FIXTURE_CWD")
	if path := os.Getenv("ATLAS_NATIVE_FIXTURE_STARTUP_FILE"); path != "" {
		startedIn, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(startedIn), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			http.Error(w, "fixture accepts read-only API requests", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/info":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "2.1.0"})
		case "/api/session":
			if r.URL.Query().Get("directory") != os.Getenv("ATLAS_NATIVE_FIXTURE_FILTER") || r.URL.Query().Get("parentID") != "null" || r.URL.Query().Get("archived") != "false" {
				http.Error(w, "expected matching top-level active native history", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{
				"id": "ses-fixture", "title": "Native research", "location": map[string]any{"directory": cwd}, "time": map[string]any{"updated": int64(1792540800000)},
			}}, "cursor": map[string]any{}})
		case "/api/session/ses-older":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
				"id": "ses-older", "title": "Older native session", "location": map[string]any{"directory": cwd}, "time": map[string]any{"updated": int64(1692540800000)},
			}})
		case "/api/session/ses-missing":
			http.Error(w, "session not found", http.StatusNotFound)
		default:
			http.Error(w, "only discovery API requests are permitted", http.StatusForbidden)
		}
	}))
	fmt.Printf("server listening on %s\n", s.URL)
	select {}
}

func TestNativeResumeUsesHandlesWithoutModelOverride(t *testing.T) {
	cwd := t.TempDir()
	const claudeID = "9de45347-46f9-4522-87a8-95bf0d8cf21e"
	piFile := filepath.Join(cwd, "native sessions", "pi-session.jsonl")
	fixture(t, piFile, map[string]any{"type": "session", "id": "pi-id", "cwd": cwd})
	ompFile := filepath.Join(cwd, "native sessions", "omp-session.jsonl")
	fixture(t, ompFile, map[string]any{"type": "title", "title": "OMP work"}, map[string]any{"type": "session", "id": "omp-id", "cwd": cwd})
	for _, test := range []struct {
		adapter string
		session harness.NativeSession
		want    []string
	}{
		{"claude", harness.NativeSession{ID: claudeID, CWD: cwd, Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]any{"sessionId": claudeID, "cwd": cwd})}}, []string{"--resume", claudeID}},
		{"pi", harness.NativeSession{ID: "pi-id", CWD: cwd, Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]any{"sessionId": "pi-id", "sessionFile": piFile})}}, []string{"--session", piFile}},
		{"omp", harness.NativeSession{ID: "omp-id", CWD: cwd, Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]any{"harness": "omp", "sessionId": "omp-id", "sessionFile": ompFile})}}, []string{"--resume", ompFile}},
		{"opencode", harness.NativeSession{ID: "ses-native", CWD: cwd, Handle: harness.Handle{Version: 1, Data: harness.JSON(map[string]any{"sessionId": "ses-native", "cwd": cwd, "apiMajor": 2})}}, []string{"--session", "ses-native"}},
	} {
		t.Run(test.adapter, func(t *testing.T) {
			cfg := harness.Config{Adapter: test.adapter, Model: "different/default-model"}
			got, err := Args(cfg, &test.session)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("native resume: %v %v, want %v", got, err, test.want)
			}
			bad := test.session
			bad.ID = "different-id"
			if _, err := Args(cfg, &bad); err == nil {
				t.Fatal("mismatched native identity accepted")
			}
			bad = test.session
			bad.CWD = filepath.Join(cwd, "different-project")
			if _, err := Args(cfg, &bad); err == nil {
				t.Fatal("mismatched native working directory accepted")
			}
			bad = test.session
			bad.Handle.Version = 99
			if _, err := Args(cfg, &bad); err == nil {
				t.Fatal("unsupported handle version accepted")
			}
			if _, err := ArgsWithOptions(cfg, &test.session, StartOptions{Model: "override"}); !errors.Is(err, harness.ErrUnsupported) {
				t.Fatalf("resume model override accepted: %v", err)
			}
		})
	}
}

func TestStartOptionsAndConfiguredNativeFlags(t *testing.T) {
	claude := harness.Config{Adapter: "claude", Model: "default-model", Options: map[string]json.RawMessage{
		"allowedTools": harness.JSON([]string{"Read", "Grep"}), "settings": harness.JSON(map[string]any{"language": "English"}),
	}}
	args, err := ArgsWithOptions(claude, nil, StartOptions{Model: "role-model", Thinking: "high", NativeAgent: "researcher", PermissionMode: "plan"})
	want := []string{"--allowedTools", "Read,Grep", "--settings", `{"language":"English"}`, "--model", "role-model", "--effort", "high", "--agent", "researcher", "--permission-mode", "plan"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("Claude native options: %v %v, want %v", args, err, want)
	}
	pi := harness.Config{Adapter: "pi", Model: "provider/default", Options: map[string]json.RawMessage{
		"sessionDir": harness.JSON("/custom native store"), "offline": harness.JSON(true), "approve": harness.JSON(false),
	}}
	args, err = ArgsWithOptions(pi, nil, StartOptions{Thinking: "high"})
	want = []string{"--no-approve", "--offline", "--session-dir", "/custom native store", "--model", "provider/default", "--thinking", "high"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("Pi native options: %v %v, want %v", args, err, want)
	}
	args, err = Args(harness.Config{Adapter: "opencode"}, nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("OpenCode default interactive start: %v %v", args, err)
	}
	for _, cfg := range []harness.Config{
		{Adapter: "claude", Options: map[string]json.RawMessage{"maxTurns": harness.JSON(2)}},
		{Adapter: "opencode", Model: "provider/default"},
		{Adapter: "opencode", Options: map[string]json.RawMessage{"permissions": harness.JSON([]map[string]string{{"action": "*", "resource": "*", "effect": "deny"}})}},
	} {
		if _, err := Args(cfg, nil); !errors.Is(err, harness.ErrUnsupported) {
			t.Fatalf("unsupported interactive override accepted for %s: %v", cfg.Adapter, err)
		}
	}
	for _, test := range []struct {
		adapter string
		opts    StartOptions
	}{
		{"pi", StartOptions{NativeAgent: "researcher"}},
		{"pi", StartOptions{PermissionMode: "allow"}},
		{"opencode", StartOptions{Thinking: "high"}},
		{"opencode", StartOptions{NativeAgent: "researcher"}},
	} {
		if _, err := ArgsWithOptions(harness.Config{Adapter: test.adapter}, nil, test.opts); !errors.Is(err, harness.ErrUnsupported) {
			t.Fatalf("unsupported project defaults accepted for %s: %v", test.adapter, err)
		}
	}
	if _, err := ArgsWithOptions(claude, nil, StartOptions{PermissionMode: "typo"}); err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("invalid permission mode accepted: %v", err)
	}
}
