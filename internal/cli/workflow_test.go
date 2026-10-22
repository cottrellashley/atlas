package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cottrellashley/atlas/internal/harness"
	"github.com/cottrellashley/atlas/internal/native"
	"github.com/cottrellashley/atlas/internal/picker"
)

func workflowConfig(t *testing.T, c *packageCLI, profiles map[string]harness.Config) {
	t.Helper()
	cliWrite(t, c.app.Home, "config.json", string(harness.JSON(map[string]any{"schemaVersion": 1, "harnesses": profiles})))
}

func claudeHistory(t *testing.T, root, id, cwd, title string) {
	t.Helper()
	row := map[string]any{"type": "user", "cwd": cwd, "message": map[string]any{"content": title}}
	cliWrite(t, root, "projects/project/"+id+".jsonl", string(harness.JSON(row))+"\n")
}

func TestNativeSessionListUsesHarnessHistoryAcrossProjects(t *testing.T) {
	c := newPackageCLI(t)
	root, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	id1, id2 := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	claudeHistory(t, root, id1, first, "Research birds")
	claudeHistory(t, root, id2, second, "Research oceans\x1b[31m")
	workflowConfig(t, c, map[string]harness.Config{"claude": {Adapter: "claude", Env: map[string]string{"CLAUDE_CONFIG_DIR": root}}})
	// A stale Atlas record must never appear in native history or be imported.
	cliWrite(t, c.app.Home, "sessions/atlas-only/record.json", `{"id":"atlas-only","title":"old managed session"}`)
	out := c.must("session", "list", "--harness", "claude", "--json")
	var groups []native.Group
	if err := json.Unmarshal([]byte(out), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Sessions) != 2 || strings.Contains(out, "atlas-only") {
		t.Fatal(out)
	}
	filtered := c.must("sessions", "list", "--harness", "claude", "--cwd", second, "--search", "oceans", "--plain")
	if !strings.Contains(filtered, id2) || strings.Contains(filtered, id1) || strings.Contains(filtered, "\x1b") {
		t.Fatal(filtered)
	}
	if _, err := os.Stat(filepath.Join(c.app.Home, "daemon.sock")); !os.IsNotExist(err) {
		t.Fatalf("native listing created a daemon: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(c.app.Home, "sessions"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "atlas-only" {
		t.Fatalf("native listing created Atlas sessions: %v %v", entries, err)
	}
}

func TestNativeResumePreservesAliasDirectoryAndModel(t *testing.T) {
	c := newPackageCLI(t)
	root, cwd := t.TempDir(), t.TempDir()
	id := "33333333-3333-4333-8333-333333333333"
	claudeHistory(t, root, id, cwd, "Continue this")
	cfg := harness.Config{Adapter: "claude", Command: []string{"/bin/true", "--configured-flag"}, Model: "must-not-override", Env: map[string]string{"CLAUDE_CONFIG_DIR": root, "PROFILE_MARKER": "work"}}
	workflowConfig(t, c, map[string]harness.Config{"work": cfg})
	called := false
	c.app.launch = func(got harness.Config, dir string, args []string) error {
		called = true
		if got.ID != "work" || got.Env["PROFILE_MARKER"] != "work" || dir != cwd || !reflect.DeepEqual(args, []string{"--resume", id}) {
			t.Fatalf("unexpected launch: %#v %s %v", got, dir, args)
		}
		return nil
	}
	c.must("resume", id, "--harness", "work")
	if !called {
		t.Fatal("did not launch native harness")
	}
	if _, err := os.Stat(filepath.Join(c.app.Home, "sessions")); !os.IsNotExist(err) {
		t.Fatal("resume created Atlas session records")
	}
	// Two aliases pointing at one store require explicit disambiguation.
	workflowConfig(t, c, map[string]harness.Config{
		"claude": cfg, "work": cfg,
		"pi":       {Adapter: "pi", Env: map[string]string{"HOME": t.TempDir(), "PI_CODING_AGENT_DIR": t.TempDir()}},
		"opencode": {Adapter: "opencode", Command: []string{"/nonexistent/opencode"}},
	})
	called = false
	_, err := c.run("resume", id)
	if err == nil || !strings.Contains(err.Error(), "multiple harnesses") || called {
		t.Fatalf("ambiguous resume launched: %v", err)
	}
}

func TestInteractiveInitWritesThenLaunchesAndSetupFlagsNeverLaunch(t *testing.T) {
	for _, option := range []string{"", "--no-open", "--json", "--dry-run"} {
		t.Run(option, func(t *testing.T) {
			c := newPackageCLI(t)
			cwd := t.TempDir()
			workflowConfig(t, c, map[string]harness.Config{"claude": {Adapter: "claude", Command: []string{"/bin/true"}}})
			c.app.choose = func(context.Context, string, []picker.Item) (int, error) {
				t.Fatal("explicit harness should not prompt")
				return 0, nil
			}
			launched := false
			c.app.launch = func(cfg harness.Config, dir string, args []string) error {
				launched = true
				if dir != cwd || cfg.ID != "claude" || !strings.Contains(cliRead(t, cwd, "AGENTS.md"), "atlas help agent") {
					t.Fatal("setup was not ready at native launch")
				}
				return nil
			}
			args := []string{"init", "researcher", "--harness", "claude", "--cwd", cwd}
			if option != "" {
				args = append(args, option)
			}
			c.must(args...)
			if launched != (option == "") {
				t.Fatalf("launch=%v with %s", launched, option)
			}
			if option == "--dry-run" {
				files, _ := os.ReadDir(cwd)
				if len(files) != 0 {
					t.Fatal("dry run wrote project files")
				}
			}
		})
	}
}

func TestInitUnsupportedRoleAndConflictDoNotWriteOrLaunch(t *testing.T) {
	for _, mode := range []string{"unsupported-role", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			c := newPackageCLI(t)
			cwd, template := t.TempDir(), t.TempDir()
			workflowConfig(t, c, map[string]harness.Config{"pi": {Adapter: "pi", Command: []string{"/bin/true"}}})
			c.app.choose = func(context.Context, string, []picker.Item) (int, error) { return 0, nil }
			c.app.launch = func(harness.Config, string, []string) error { t.Fatal("invalid setup launched"); return nil }
			name := "researcher"
			if mode == "unsupported-role" {
				cliWrite(t, template, "template.json", `{"schemaVersion":1,"id":"custom","version":"1.0.0","agents":{"custom":{"nativeAgent":"unsupported"}}}`)
				name = template
			} else {
				cliWrite(t, cwd, "AGENTS.md", "user instructions")
			}
			_, err := c.run("init", name, "--harness", "pi", "--cwd", cwd)
			if err == nil {
				t.Fatal("invalid setup succeeded")
			}
			files, _ := os.ReadDir(cwd)
			want := 0
			if mode == "conflict" {
				want = 1
				if cliRead(t, cwd, "AGENTS.md") != "user instructions" {
					t.Fatal("overwrote instructions")
				}
			}
			if len(files) != want {
				t.Fatalf("invalid setup wrote files: %v", files)
			}
		})
	}
}

func TestSessionPickerSelectionAndCancellation(t *testing.T) {
	c := newPackageCLI(t)
	root, cwd := t.TempDir(), t.TempDir()
	id := "44444444-4444-4444-8444-444444444444"
	claudeHistory(t, root, id, cwd, "A conversation")
	workflowConfig(t, c, map[string]harness.Config{"claude": {Adapter: "claude", Command: []string{"/bin/true"}, Env: map[string]string{"CLAUDE_CONFIG_DIR": root}}})
	launched := false
	c.app.launch = func(harness.Config, string, []string) error { launched = true; return nil }
	c.app.choose = func(ctx context.Context, title string, items []picker.Item) (int, error) {
		if len(items) != 1 || items[0].Group != "Claude Code" || !strings.Contains(items[0].Detail, cwd) {
			t.Fatal(items)
		}
		return 0, nil
	}
	c.must("session", "list", "--harness", "claude")
	if !launched {
		t.Fatal("selection did not launch")
	}
	launched = false
	c.app.choose = func(context.Context, string, []picker.Item) (int, error) { return -1, picker.ErrCanceled }
	c.must("session", "list", "--harness", "claude")
	if launched {
		t.Fatal("cancellation launched a harness")
	}
}

func TestAgentListingKeepsExactLocalSourcesAndSkipsBrokenTemplates(t *testing.T) {
	c := newPackageCLI(t)
	cliWrite(t, c.app.Home, "templates/custom/template.json", `{"schemaVersion":1,"id":"custom","version":"1.0.0"}`)
	cliWrite(t, c.app.Home, "templates/broken/template.json", `{`)
	out := c.must("agent", "list", "--json")
	var agents []agentTemplate
	if err := json.Unmarshal([]byte(out), &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "researcher" || agents[1].Source != filepath.Join(c.app.Home, "templates", "custom") {
		t.Fatal(out)
	}
	if !strings.Contains(c.stderr.String(), "Skipping agent broken") {
		t.Fatal("missing diagnostic")
	}
}

func TestRetiredManagedCommandsCannotCreateSessionState(t *testing.T) {
	c := newPackageCLI(t)
	for _, cmd := range []string{"run", "send", "logs", "wait", "stop", "permissions", "configure", "daemon"} {
		_, err := c.run(cmd)
		if err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatalf("%s: %v", cmd, err)
		}
	}
	for _, op := range []string{"import", "archive", "show"} {
		if _, err := c.run("sessions", op); err == nil {
			t.Fatalf("managed operation %s still public", op)
		}
	}
	if _, err := os.Stat(filepath.Join(c.app.Home, "sessions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestJSONInitNeverOpensHarnessPicker(t *testing.T) {
	c := newPackageCLI(t)
	workflowConfig(t, c, map[string]harness.Config{
		"claude": {Adapter: "claude", Command: []string{"/bin/true"}},
		"pi":     {Adapter: "pi", Command: []string{"/bin/true"}},
	})
	c.app.choose = func(context.Context, string, []picker.Item) (int, error) {
		t.Fatal("JSON setup opened an interactive picker")
		return 0, nil
	}
	out, err := c.run("init", "researcher", "--cwd", t.TempDir(), "--json")
	if err == nil || !strings.Contains(err.Error(), "choose a harness") || out != "" {
		t.Fatalf("expected explicit harness selection without output: %s %v", out, err)
	}
}
