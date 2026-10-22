package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cottrellashley/atlas/internal/harness"
)

const fixtureSessionID = "12345678-1234-1234-1234-123456789abc"
const fixtureHarness = "claude-fixture"
const fixtureArgument = "literal argument ; $(not-a-shell)"

// TestNativeCLI exercises the compiled public CLI with isolated native history
// and a fixture executable. It never starts an installed harness or uses model
// credentials. Build once; each subtest receives separate homes and projects.
func TestNativeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native terminal handoff requires Linux/WSL or another Unix host")
	}
	_, source, _, _ := runtime.Caller(0)
	bin := filepath.Join(t.TempDir(), "atlas")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "./cmd/atlas")
	build.Dir = filepath.Dir(filepath.Dir(source))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build atlas: %v\n%s", err, output)
	}

	t.Run("init preserves configuration without opening a harness", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		settings := []byte("{\"permissions\":{\"allow\":[\"Read\"]},\"env\":{\"KEEP\":\"value\"}}\n")
		f.write(t, filepath.Join(f.project, ".claude", "settings.json"), settings)
		f.write(t, filepath.Join(f.project, "notes.txt"), []byte("user content\n"))
		result := f.must(t, "init", "researcher", "--harness", fixtureHarness, "--no-open", "--json")
		var plan struct {
			Project struct {
				Harness string `json:"harness"`
			} `json:"project"`
		}
		if err := json.Unmarshal(result.stdout, &plan); err != nil || plan.Project.Harness != fixtureHarness {
			t.Fatalf("initialization plan: %v: %s", err, result.stdout)
		}
		for _, path := range []string{"AGENTS.md", "CLAUDE.md", ".claude/skills/research/SKILL.md", ".mcp.json", ".atlas/project.json"} {
			if _, err := os.Stat(filepath.Join(f.project, path)); err != nil {
				t.Fatalf("missing initialized resource %s: %v", path, err)
			}
		}
		f.assertFile(t, filepath.Join(f.project, ".claude", "settings.json"), settings)
		f.assertFile(t, filepath.Join(f.project, "notes.txt"), []byte("user content\n"))
		f.assertNotLaunched(t)
		f.assertNoManagedState(t)
	})

	t.Run("nonterminal init and dry run do not launch", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		f.must(t, "init", "researcher", "--harness", fixtureHarness, "--dry-run")
		if _, err := os.Stat(filepath.Join(f.project, ".atlas")); !os.IsNotExist(err) {
			t.Fatalf("dry run wrote project metadata: %v", err)
		}
		f.must(t, "init", "researcher", "--harness", fixtureHarness)
		if _, err := os.Stat(filepath.Join(f.project, ".atlas", "project.json")); err != nil {
			t.Fatal(err)
		}
		f.assertNotLaunched(t)
		f.assertNoManagedState(t)
	})

	t.Run("lists existing native sessions without importing", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		transcript, original := f.transcript(t, "Existing research \x1b[31mred\x1b[0m")
		result := f.must(t, "session", "list", "--harness", fixtureHarness, "--cwd", f.project, "--json")
		var groups []struct {
			Harness  string                  `json:"harness"`
			Adapter  string                  `json:"adapter"`
			Sessions []harness.NativeSession `json:"sessions"`
		}
		if err := json.Unmarshal(result.stdout, &groups); err != nil {
			t.Fatalf("session groups: %v: %s", err, result.stdout)
		}
		if len(groups) != 1 || groups[0].Harness != fixtureHarness || groups[0].Adapter != "claude" || len(groups[0].Sessions) != 1 {
			t.Fatalf("unexpected groups: %#v", groups)
		}
		session := groups[0].Sessions[0]
		if session.ID != fixtureSessionID || session.CWD != f.project || !strings.Contains(session.Title, "Existing research") {
			t.Fatalf("native identity was not preserved: %#v", session)
		}
		alias := f.must(t, "sessions", "list", "--harness", fixtureHarness, "--json")
		if !bytes.Equal(result.stdout, alias.stdout) {
			t.Fatalf("plural alias differs:\n%s\n%s", result.stdout, alias.stdout)
		}
		plain := f.must(t, "session", "list", "--harness", fixtureHarness, "--plain")
		if bytes.ContainsRune(plain.stdout, '\x1b') || !bytes.Contains(plain.stdout, []byte(fixtureSessionID)) {
			t.Fatalf("unsafe or missing native listing: %q", plain.stdout)
		}
		f.assertFile(t, transcript, original)
		f.assertNotLaunched(t)
		f.assertNoManagedState(t)
	})

	t.Run("resume replaces atlas with native executable in saved directory", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		transcript, original := f.transcript(t, "Resume native work")
		f.invocation = f.root // Resume must use the saved directory, not this one.
		result := f.must(t, "resume", fixtureSessionID, "--harness", fixtureHarness)
		f.assertHandoff(t, result, f.project, []string{fixtureArgument, "--resume", fixtureSessionID})
		f.assertFile(t, transcript, original)
		f.assertNoManagedState(t)
	})

	t.Run("new opens a blank directory and exec preserves native arguments", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		blank := filepath.Join(f.root, "blank project")
		if err := os.Mkdir(blank, 0700); err != nil {
			t.Fatal(err)
		}
		result := f.must(t, "new", "--harness", fixtureHarness, "--cwd", blank)
		f.assertHandoff(t, result, blank, []string{fixtureArgument, "--model", "opaque/model:default"})
		entries, err := os.ReadDir(blank)
		if err != nil || len(entries) != 0 {
			t.Fatalf("new unexpectedly initialized the blank directory: %v, %v", entries, err)
		}
		result = f.must(t, "exec", "--harness", fixtureHarness, "--cwd", blank, "--", "--native-option", "value with spaces")
		f.assertHandoff(t, result, blank, []string{fixtureArgument, "--native-option", "value with spaces"})
		f.assertNoManagedState(t)
	})

	t.Run("help exposes native workflow and managed commands are retired", func(t *testing.T) {
		f := newNativeCLIFixture(t, bin)
		help := f.must(t)
		for _, command := range []string{"atlas new", "atlas init researcher", "atlas session list", "atlas resume", "atlas help agent"} {
			if !bytes.Contains(help.stdout, []byte(command)) {
				t.Errorf("root help missing %q", command)
			}
		}
		for _, command := range []string{"run", "send", "logs", "wait", "stop", "permissions", "configure", "daemon"} {
			result, err := f.call(t, command)
			if err == nil || !bytes.Contains(result.stderr, []byte("retired")) || !bytes.Contains(result.stderr, []byte("atlas new or atlas resume")) {
				t.Errorf("%s did not provide migration guidance: error=%v stderr=%s", command, err, result.stderr)
			}
		}
		for _, operation := range []string{"import", "discover", "show", "archive", "unarchive"} {
			if result, err := f.call(t, "sessions", operation); err == nil {
				t.Errorf("old managed operation %s was accepted: %s", operation, result.stdout)
			}
		}
		f.assertNotLaunched(t)
		f.assertNoManagedState(t)
	})
}

type nativeCLIResult struct {
	stdout, stderr []byte
	pid            int
}

type nativeCLIFixture struct {
	bin, root, home, project, invocation, nativeHome, marker string
	env                                                      []string
}

func newNativeCLIFixture(t *testing.T, bin string) *nativeCLIFixture {
	t.Helper()
	root := t.TempDir()
	f := &nativeCLIFixture{bin: bin, root: root, home: filepath.Join(root, "atlas home"), project: filepath.Join(root, "saved project"), nativeHome: filepath.Join(root, "claude home"), marker: filepath.Join(root, "native-launched")}
	f.invocation = f.project
	for _, dir := range []string{f.home, f.project, f.nativeHome, filepath.Join(root, "empty-path"), filepath.Join(root, "user-home")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// No inherited credentials or native configuration directories enter the CLI.
	f.env = []string{
		"HOME=" + filepath.Join(root, "user-home"), "ATLAS_HOME=" + f.home,
		"CLAUDE_CONFIG_DIR=" + f.nativeHome, "PATH=" + filepath.Join(root, "empty-path"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"), "XDG_DATA_HOME=" + filepath.Join(root, "xdg-data"),
		"ATLAS_NATIVE_INHERITED=inherited value", "ATLAS_NATIVE_CONFIGURED=before override",
		"CLAUDECODE=stale-parent-session", "CLAUDE_CODE_SESSION_ID=stale-parent-id",
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"schemaVersion": 1, "harnesses": map[string]any{fixtureHarness: map[string]any{
		"adapter": "claude", "command": []string{executable, "-test.run=^TestNativeCLIExecutable$", "--", fixtureArgument},
		"model": "opaque/model:default",
		"env":   map[string]string{"ATLAS_NATIVE_CLI_HELPER": "1", "ATLAS_NATIVE_CONFIGURED": "configured value", "ATLAS_NATIVE_MARKER": f.marker, "CLAUDE_CONFIG_DIR": f.nativeHome},
	}}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join(f.home, "config.json"), data)
	return f
}

func (f *nativeCLIFixture) write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f *nativeCLIFixture) transcript(t *testing.T, title string) (string, []byte) {
	t.Helper()
	row := map[string]any{"type": "user", "sessionId": fixtureSessionID, "cwd": f.project, "message": map[string]string{"role": "user", "content": title}}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(f.nativeHome, "projects", "fixture-project", fixtureSessionID+".jsonl")
	f.write(t, path, data)
	return path, data
}

func (f *nativeCLIFixture) call(t *testing.T, args ...string) (nativeCLIResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.bin, args...)
	cmd.Dir, cmd.Env = f.invocation, f.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nativeCLIResult{}, err
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("atlas %v timed out: %s", args, stderr.String())
	}
	return nativeCLIResult{stdout.Bytes(), stderr.Bytes(), cmd.Process.Pid}, err
}

func (f *nativeCLIFixture) must(t *testing.T, args ...string) nativeCLIResult {
	t.Helper()
	result, err := f.call(t, args...)
	if err != nil {
		t.Fatalf("atlas %v: %v\nstdout: %s\nstderr: %s", args, err, result.stdout, result.stderr)
	}
	return result
}

func (f *nativeCLIFixture) assertFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file %s changed: err=%v got=%q want=%q", path, err, got, want)
	}
}

func (f *nativeCLIFixture) assertNotLaunched(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(f.marker); !os.IsNotExist(err) {
		t.Fatalf("native executable unexpectedly launched: %v", err)
	}
}

func (f *nativeCLIFixture) assertNoManagedState(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(f.home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("native workflow wrote Atlas runtime state: %v", entries)
	}
}

type nativeCLIObservation struct {
	Args        []string          `json:"args"`
	CWD         string            `json:"cwd"`
	PID         int               `json:"pid"`
	Environment map[string]string `json:"environment"`
}

func (f *nativeCLIFixture) assertHandoff(t *testing.T, result nativeCLIResult, cwd string, args []string) {
	t.Helper()
	var observed nativeCLIObservation
	if err := json.Unmarshal(result.stdout, &observed); err != nil {
		t.Fatalf("native fixture output: %v: %s", err, result.stdout)
	}
	if observed.PID != result.pid || observed.CWD != cwd || !reflect.DeepEqual(observed.Args, args) {
		t.Fatalf("incorrect native exec handoff: %#v; want pid=%d cwd=%s args=%q", observed, result.pid, cwd, args)
	}
	wantEnv := map[string]string{"ATLAS_NATIVE_INHERITED": "inherited value", "ATLAS_NATIVE_CONFIGURED": "configured value", "CLAUDE_CONFIG_DIR": f.nativeHome, "CLAUDECODE": "", "CLAUDE_CODE_SESSION_ID": ""}
	if !reflect.DeepEqual(observed.Environment, wantEnv) {
		t.Fatalf("native launch environment: got=%v want=%v", observed.Environment, wantEnv)
	}
	f.assertFile(t, f.marker, []byte("launched\n"))
}

// TestNativeCLIExecutable is the fake native command reached via syscall.Exec.
// The test runner is only a portable fixture executable; it makes no inference.
func TestNativeCLIExecutable(t *testing.T) {
	if os.Getenv("ATLAS_NATIVE_CLI_HELPER") != "1" {
		t.Skip("native executable subprocess fixture")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	env := map[string]string{}
	for _, key := range []string{"ATLAS_NATIVE_INHERITED", "ATLAS_NATIVE_CONFIGURED", "CLAUDE_CONFIG_DIR", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID"} {
		env[key] = os.Getenv(key)
	}
	if err := os.WriteFile(os.Getenv("ATLAS_NATIVE_MARKER"), []byte("launched\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(nativeCLIObservation{Args: args, CWD: cwd, PID: os.Getpid(), Environment: env}); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}
